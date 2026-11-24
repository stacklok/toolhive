// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/ignore"
)

func TestGetPermissionConfigFromProfile(t *testing.T) {
	t.Parallel()

	t.Run("rejects unsupported transport", func(t *testing.T) {
		t.Parallel()
		_, err := getPermissionConfigFromProfile(nil, "grpc", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported transport")
	})

	t.Run("rejects host network mode", func(t *testing.T) {
		t.Parallel()
		profile := &permissions.Profile{
			Network: &permissions.NetworkPermissions{Mode: "Host"},
		}
		_, err := getPermissionConfigFromProfile(profile, "stdio", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not supported by the Apple container runtime")
	})

	t.Run("rejects privileged mode", func(t *testing.T) {
		t.Parallel()
		profile := &permissions.Profile{Privileged: true}
		_, err := getPermissionConfigFromProfile(profile, "stdio", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "privileged mode is not supported")
	})

	t.Run("rejects bind mount paths containing colon", func(t *testing.T) {
		t.Parallel()
		baseDir := t.TempDir()
		resolvedDir, err := filepath.EvalSymlinks(baseDir)
		require.NoError(t, err)

		colonFile := filepath.Join(resolvedDir, "secret:key")
		require.NoError(t, os.WriteFile(colonFile, []byte("x"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(resolvedDir, ".thvignore"), []byte("secret:key\n"), 0o600))

		profile := &permissions.Profile{
			Read: []permissions.MountDeclaration{
				permissions.MountDeclaration(resolvedDir + ":/workspace"),
			},
		}
		_, err = getPermissionConfigFromProfile(profile, "stdio", &ignore.Config{LoadGlobal: false})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "contains ':'")
	})

	t.Run("upgrades read-only mount to read-write and skips URI mounts", func(t *testing.T) {
		t.Parallel()
		baseDir := t.TempDir()
		resolvedDir, err := filepath.EvalSymlinks(baseDir)
		require.NoError(t, err)

		profile := &permissions.Profile{
			Read: []permissions.MountDeclaration{
				permissions.MountDeclaration(resolvedDir + ":/workspace"),
				permissions.MountDeclaration("s3://bucket/key:/remote"),
			},
			Write: []permissions.MountDeclaration{
				permissions.MountDeclaration(resolvedDir + ":/workspace"),
			},
		}

		cfg, err := getPermissionConfigFromProfile(profile, "streamable-http", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"ALL"}, cfg.CapDrop)
		require.Len(t, cfg.Mounts, 1)
		assert.Equal(t, resolvedDir, cfg.Mounts[0].Source)
		assert.Equal(t, "/workspace", cfg.Mounts[0].Target)
		assert.False(t, cfg.Mounts[0].ReadOnly)
	})

	t.Run("applies local .thvignore overlay mounts", func(t *testing.T) {
		t.Parallel()
		baseDir := t.TempDir()
		resolvedDir, err := filepath.EvalSymlinks(baseDir)
		require.NoError(t, err)

		secretDir := filepath.Join(resolvedDir, "secrets")
		require.NoError(t, os.Mkdir(secretDir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(resolvedDir, ".thvignore"), []byte("secrets/\n"), 0o600))

		profile := &permissions.Profile{
			Read: []permissions.MountDeclaration{
				permissions.MountDeclaration(resolvedDir + ":/workspace"),
			},
		}

		cfg, err := getPermissionConfigFromProfile(profile, "stdio", &ignore.Config{LoadGlobal: false})
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(cfg.Mounts), 2)
		assert.Equal(t, "/workspace", cfg.Mounts[0].Target)
		assert.Equal(t, "/workspace/secrets", cfg.Mounts[1].Target)
	})
}

func TestAppendMountArgs(t *testing.T) {
	t.Parallel()

	permCfg := &runtime.PermissionConfig{
		ReadOnlyRootfs: true,
		Mounts: []runtime.Mount{
			{Source: "/host/data", Target: "/data", ReadOnly: true, Type: runtime.MountTypeBind},
			{Source: "/host/tmp", Target: "/tmp", ReadOnly: false, Type: runtime.MountTypeBind},
			{Target: "/cache", Type: runtime.MountTypeTmpfs},
		},
	}

	args, err := appendMountArgs(nil, permCfg)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"--volume", "/host/data:/data:ro",
		"--volume", "/host/tmp:/tmp",
		"--tmpfs", "/cache",
		"--read-only",
		"--tmpfs", "/var/tmp:rw,nosuid,nodev,noexec,size=64m",
		"--tmpfs", "/run:rw,nosuid,nodev,noexec,size=64m",
	}, args)

	_, err = appendMountArgs(nil, &runtime.PermissionConfig{
		Mounts: []runtime.Mount{
			{Source: "/host/a:b", Target: "/data", Type: runtime.MountTypeBind},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains ':'")
}

func TestAppendSecurityArgs(t *testing.T) {
	t.Parallel()

	args, err := appendSecurityArgs(nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"--cap-drop", "ALL"}, args)

	_, err = appendSecurityArgs(nil, &runtime.PermissionConfig{
		Privileged: true,
		CapDrop:    []string{"ALL"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "privileged mode is not supported")

	args, err = appendSecurityArgs(nil, &runtime.PermissionConfig{
		CapDrop: []string{"ALL"},
		CapAdd:  []string{"NET_BIND_SERVICE"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"--cap-drop", "ALL", "--cap-add", "NET_BIND_SERVICE"}, args)
}

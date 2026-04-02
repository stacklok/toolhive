// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/go-microvm/image"
	"github.com/stretchr/testify/require"
)

func TestInjectEntrypoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		imgConfig *image.OCIConfig
		wantCmd   []string
		wantEnv   []string
		wantDir   string
		wantErr   string
	}{
		{
			name: "full OCI config",
			imgConfig: &image.OCIConfig{
				Entrypoint: []string{"/usr/bin/mcp-server"},
				Cmd:        []string{"--port", "8080"},
				Env:        []string{"LOG_LEVEL=info"},
				WorkingDir: "/app",
			},
			wantCmd: []string{"/usr/bin/mcp-server", "--port", "8080"},
			wantEnv: []string{"LOG_LEVEL=info"},
			wantDir: "/app",
		},
		{
			name: "only entrypoint",
			imgConfig: &image.OCIConfig{
				Entrypoint: []string{"python3", "server.py"},
			},
			wantCmd: []string{"python3", "server.py"},
		},
		{
			name: "only cmd",
			imgConfig: &image.OCIConfig{
				Cmd: []string{"/bin/server"},
			},
			wantCmd: []string{"/bin/server"},
		},
		{
			name:      "nil config",
			imgConfig: nil,
			wantErr:   "OCI image has no entrypoint or cmd",
		},
		{
			name:      "empty entrypoint and cmd",
			imgConfig: &image.OCIConfig{},
			wantErr:   "OCI image has no entrypoint or cmd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rootfs := t.TempDir()

			hook := InjectEntrypoint()
			err := hook(rootfs, tt.imgConfig)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

			data, err := os.ReadFile(filepath.Join(rootfs, "etc", "thv-entrypoint.json"))
			require.NoError(t, err)

			var cfg entrypointConfig
			require.NoError(t, json.Unmarshal(data, &cfg))
			require.Equal(t, tt.wantCmd, cfg.Cmd)
			require.Equal(t, tt.wantEnv, cfg.Env)
			require.Equal(t, tt.wantDir, cfg.WorkingDir)
		})
	}
}

func TestInjectInitBinary(t *testing.T) {
	t.Parallel()
	rootfs := t.TempDir()

	hook := InjectInitBinary()
	err := hook(rootfs, nil)
	require.NoError(t, err)

	initPath := filepath.Join(rootfs, "thv-vm-init")
	info, err := os.Stat(initPath)
	require.NoError(t, err)
	require.True(t, info.Mode().Perm()&0o111 != 0, "init binary should be executable")

	content, err := os.ReadFile(initPath)
	require.NoError(t, err)
	require.NotEmpty(t, content, "init binary should not be empty")
}

func TestInjectEntrypointOverride(t *testing.T) {
	t.Parallel()

	t.Run("prepends OCI entrypoint to override", func(t *testing.T) {
		t.Parallel()
		rootfs := t.TempDir()

		override := []string{"stdio", "--toolsets", "all"}
		imgConfig := &image.OCIConfig{
			Entrypoint: []string{"/usr/bin/github-mcp-server"},
			Env:        []string{"FROM_IMAGE=true"},
			WorkingDir: "/app",
		}

		hook := InjectEntrypointOverride(override)
		err := hook(rootfs, imgConfig)
		require.NoError(t, err)

		data, err := os.ReadFile(filepath.Join(rootfs, "etc", "thv-entrypoint.json"))
		require.NoError(t, err)

		var cfg entrypointConfig
		require.NoError(t, json.Unmarshal(data, &cfg))
		require.Equal(t, []string{"/usr/bin/github-mcp-server", "stdio", "--toolsets", "all"}, cfg.Cmd,
			"should prepend OCI entrypoint to override args")
		require.Equal(t, []string{"FROM_IMAGE=true"}, cfg.Env, "should preserve OCI env")
		require.Equal(t, "/app", cfg.WorkingDir, "should preserve OCI workdir")
	})

	t.Run("no OCI entrypoint uses override as-is", func(t *testing.T) {
		t.Parallel()
		rootfs := t.TempDir()

		override := []string{"/custom/server", "--flag"}
		imgConfig := &image.OCIConfig{
			Env: []string{"FROM_IMAGE=true"},
		}

		hook := InjectEntrypointOverride(override)
		err := hook(rootfs, imgConfig)
		require.NoError(t, err)

		data, err := os.ReadFile(filepath.Join(rootfs, "etc", "thv-entrypoint.json"))
		require.NoError(t, err)

		var cfg entrypointConfig
		require.NoError(t, json.Unmarshal(data, &cfg))
		require.Equal(t, override, cfg.Cmd, "should use override as-is when no entrypoint")
	})

	t.Run("nil imgConfig uses override as-is", func(t *testing.T) {
		t.Parallel()
		rootfs := t.TempDir()

		override := []string{"/custom/server"}
		hook := InjectEntrypointOverride(override)
		err := hook(rootfs, nil)
		require.NoError(t, err)

		data, err := os.ReadFile(filepath.Join(rootfs, "etc", "thv-entrypoint.json"))
		require.NoError(t, err)

		var cfg entrypointConfig
		require.NoError(t, json.Unmarshal(data, &cfg))
		require.Equal(t, override, cfg.Cmd)
	})

	t.Run("empty cmd rejected", func(t *testing.T) {
		t.Parallel()
		rootfs := t.TempDir()
		hook := InjectEntrypointOverride(nil)
		err := hook(rootfs, nil)
		require.ErrorContains(t, err, "entrypoint override has empty cmd")
	})
}

func TestInjectSSHKeys(t *testing.T) {
	t.Parallel()
	rootfs := t.TempDir()

	pubKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest test@toolhive"
	hook := InjectSSHKeys(pubKey)
	err := hook(rootfs, nil)
	require.NoError(t, err)

	authKeysPath := filepath.Join(rootfs, "root", ".ssh", "authorized_keys")
	data, err := os.ReadFile(authKeysPath)
	require.NoError(t, err)
	require.Contains(t, string(data), pubKey)
}

func TestBuildCmd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		imgConfig *image.OCIConfig
		want      []string
	}{
		{
			name:      "nil config",
			imgConfig: nil,
			want:      nil,
		},
		{
			name:      "empty config",
			imgConfig: &image.OCIConfig{},
			want:      []string{},
		},
		{
			name: "entrypoint only",
			imgConfig: &image.OCIConfig{
				Entrypoint: []string{"/bin/server"},
			},
			want: []string{"/bin/server"},
		},
		{
			name: "cmd only",
			imgConfig: &image.OCIConfig{
				Cmd: []string{"--help"},
			},
			want: []string{"--help"},
		},
		{
			name: "entrypoint plus cmd",
			imgConfig: &image.OCIConfig{
				Entrypoint: []string{"python3"},
				Cmd:        []string{"server.py", "--port", "8080"},
			},
			want: []string{"python3", "server.py", "--port", "8080"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildCmd(tt.imgConfig)
			require.Equal(t, tt.want, got)
		})
	}
}

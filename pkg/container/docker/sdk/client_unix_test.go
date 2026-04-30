// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package sdk

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// clearSocketEnv removes any inherited TOOLHIVE_*_SOCKET overrides so the
// helpers fall through to filesystem discovery during the test.
func clearSocketEnv(t *testing.T) {
	t.Helper()
	t.Setenv(PodmanSocketEnv, "")
	t.Setenv(DockerSocketEnv, "")
	t.Setenv(ColimaSocketEnv, "")
}

// redirectSystemDockerSocket points the system-socket probe at a path that
// definitely does not exist, so user-level fallbacks are exercised regardless
// of whether the host has a real /var/run/docker.sock.
func redirectSystemDockerSocket(t *testing.T) {
	t.Helper()
	orig := systemDockerSocketPath
	systemDockerSocketPath = filepath.Join(t.TempDir(), "no-such-docker.sock")
	t.Cleanup(func() { systemDockerSocketPath = orig })
}

func TestFindDockerSocket_DockerDesktopOnLinux(t *testing.T) {
	clearSocketEnv(t)
	redirectSystemDockerSocket(t)

	home := t.TempDir()
	t.Setenv("HOME", home)

	socketDir := filepath.Join(home, filepath.Dir(DockerDesktopLinuxSocketPath))
	require.NoError(t, os.MkdirAll(socketDir, 0o755))
	socketPath := filepath.Join(home, DockerDesktopLinuxSocketPath)
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))

	got, err := findDockerSocket()
	require.NoError(t, err)
	assert.Equal(t, socketPath, got)
}

func TestFindPlatformContainerSocket_DockerEnvOverrideWins(t *testing.T) {
	clearSocketEnv(t)

	tmp := t.TempDir()
	envSocket := filepath.Join(tmp, "docker-from-env.sock")
	require.NoError(t, os.WriteFile(envSocket, nil, 0o600))
	t.Setenv(DockerSocketEnv, envSocket)

	// Even with a Docker Desktop on Linux socket present at $HOME, the env
	// var must take precedence.
	home := t.TempDir()
	t.Setenv("HOME", home)
	socketDir := filepath.Join(home, filepath.Dir(DockerDesktopLinuxSocketPath))
	require.NoError(t, os.MkdirAll(socketDir, 0o755))
	homeSocket := filepath.Join(home, DockerDesktopLinuxSocketPath)
	require.NoError(t, os.WriteFile(homeSocket, nil, 0o600))

	path, rt, err := findPlatformContainerSocket(runtime.TypeDocker)
	require.NoError(t, err)
	assert.Equal(t, envSocket, path)
	assert.Equal(t, runtime.TypeDocker, rt)
}

func TestFindPlatformContainerSocket_NotFound(t *testing.T) {
	clearSocketEnv(t)
	redirectSystemDockerSocket(t)

	// Empty HOME with no sockets created — every discovery path should miss.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", "")

	_, _, err := findPlatformContainerSocket(runtime.TypeDocker)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRuntimeNotFound), "expected ErrRuntimeNotFound, got %v", err)
}

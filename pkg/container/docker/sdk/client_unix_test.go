// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package sdk

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// TestNewClientWithSocketPathPodman guards against applying WithHost to the
// Podman compatibility round-tripper, which the moby client rejects because it
// is not an *http.Transport (#6844).
func TestNewClientWithSocketPathPodman(t *testing.T) {
	t.Parallel()

	// t.TempDir paths can exceed the Unix socket path limit on macOS.
	dir, err := os.MkdirTemp("", "thv-sdk")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "api.sock")

	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.44")
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/networks") {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`[{"Id":"bridge","Driver":"bridge","IPAM":{"Config":[{"Gateway":"<nil>"}]}}]`))
			assert.NoError(t, err)
			return
		}
		http.NotFound(w, r)
	}))
	require.NoError(t, server.Listener.Close())
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	client, err := newClientWithSocketPath(t.Context(), socket, runtime.TypePodman)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, client.Close()) })

	// A "<nil>" gateway only decodes when the compatibility transport is installed.
	networks, err := client.NetworkList(t.Context(), mobyclient.NetworkListOptions{})
	require.NoError(t, err)
	require.Len(t, networks.Items, 1)
	assert.False(t, networks.Items[0].IPAM.Config[0].Gateway.IsValid())
}

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

	got := findDockerSocket()
	assert.Equal(t, []string{socketPath}, got)
}

// TestFindDockerSocket_ReturnsAllCandidates exercises the multi-candidate path
// that motivates the slice return type: a system Docker socket AND a
// Docker Desktop on Linux user socket both exist at once (common when Docker
// Desktop installs a host-side wrapper at /var/run/docker.sock alongside its
// per-user socket). Both must be returned, system-first, so NewDockerClient
// can fall back from one to the other on connect failure.
func TestFindDockerSocket_ReturnsAllCandidates(t *testing.T) {
	clearSocketEnv(t)

	sysDir := t.TempDir()
	sysSocket := filepath.Join(sysDir, "docker.sock")
	require.NoError(t, os.WriteFile(sysSocket, nil, 0o600))
	orig := systemDockerSocketPath
	systemDockerSocketPath = sysSocket
	t.Cleanup(func() { systemDockerSocketPath = orig })

	home := t.TempDir()
	t.Setenv("HOME", home)
	socketDir := filepath.Join(home, filepath.Dir(DockerDesktopLinuxSocketPath))
	require.NoError(t, os.MkdirAll(socketDir, 0o755))
	userSocket := filepath.Join(home, DockerDesktopLinuxSocketPath)
	require.NoError(t, os.WriteFile(userSocket, nil, 0o600))

	got := findDockerSocket()
	require.Len(t, got, 2, "expected system socket + user socket to both be returned")
	assert.Equal(t, sysSocket, got[0], "system socket must be probed first")
	assert.Equal(t, userSocket, got[1], "Docker Desktop on Linux socket must be the next candidate")
}

func TestFindPlatformContainerSocket_DockerEnvOverrideWins(t *testing.T) {
	clearSocketEnv(t)

	tmp := t.TempDir()
	envSocket := filepath.Join(tmp, "docker-from-env.sock")
	require.NoError(t, os.WriteFile(envSocket, nil, 0o600))
	t.Setenv(DockerSocketEnv, envSocket)

	// Even with a Docker Desktop on Linux socket present at $HOME, the env
	// var must take precedence and short-circuit candidate collection.
	home := t.TempDir()
	t.Setenv("HOME", home)
	socketDir := filepath.Join(home, filepath.Dir(DockerDesktopLinuxSocketPath))
	require.NoError(t, os.MkdirAll(socketDir, 0o755))
	homeSocket := filepath.Join(home, DockerDesktopLinuxSocketPath)
	require.NoError(t, os.WriteFile(homeSocket, nil, 0o600))

	paths, rt, err := findPlatformContainerSocket(runtime.TypeDocker)
	require.NoError(t, err)
	assert.Equal(t, []string{envSocket}, paths)
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

func TestDockerPermissionHint(t *testing.T) {
	t.Parallel()

	t.Run("nil error returns empty hint", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, dockerPermissionHint(nil))
	})

	t.Run("non-permission error returns empty hint", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, dockerPermissionHint(errors.New("connection refused")))
	})

	t.Run("fs.ErrPermission triggers hint", func(t *testing.T) {
		t.Parallel()
		wrapped := fmt.Errorf("failed to ping Docker server: %w", fs.ErrPermission)
		hint := dockerPermissionHint(wrapped)
		assert.Contains(t, hint, "docker' group")
		assert.Contains(t, hint, "usermod -aG docker")
	})

	t.Run("rendered 'permission denied' substring triggers hint", func(t *testing.T) {
		t.Parallel()
		// The Docker SDK wraps the underlying *os.SyscallError in its own
		// formatted message, which we can't reach via errors.Is. Make sure
		// the substring fallback still surfaces the hint in that shape.
		err := errors.New("Got permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock")
		hint := dockerPermissionHint(err)
		assert.NotEmpty(t, hint)
	})
}

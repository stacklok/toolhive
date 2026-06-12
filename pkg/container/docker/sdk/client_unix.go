// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"

	mobyclient "github.com/moby/moby/client"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// ErrRuntimeNotFound is returned when a container runtime is not found
var ErrRuntimeNotFound = fmt.Errorf("container runtime not found")

// newPlatformClient creates a Docker client using Unix sockets
func newPlatformClient(socketPath string) (*http.Client, []mobyclient.Opt) {
	// Create a custom HTTP client that uses the Unix socket
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	// Create Docker client options. API-version negotiation is enabled by
	// default in the new client, so it no longer needs to be requested
	// explicitly.
	opts := []mobyclient.Opt{
		mobyclient.WithHTTPClient(httpClient),
		mobyclient.WithHost("unix://" + socketPath),
	}

	return httpClient, opts
}

// findPlatformContainerSocket finds container socket paths on Unix systems.
// Returns every candidate path that exists on disk, in priority order, so the
// caller can fall back to the next candidate when an earlier one is found but
// cannot be connected to (e.g. /var/run/docker.sock present but EACCES, while
// Docker Desktop's per-user socket would have worked).
func findPlatformContainerSocket(rt runtime.Type) ([]string, runtime.Type, error) {
	// First check for custom socket paths via environment variables. The
	// override is explicit, so we don't add other fallbacks alongside it.
	if customSocketPath := os.Getenv(PodmanSocketEnv); customSocketPath != "" {
		//nolint:gosec // G706: socket path from trusted environment variable
		slog.Debug("using Podman socket from env", "path", customSocketPath)
		if _, err := os.Stat(customSocketPath); err != nil { //nolint:gosec // G703: socket path from trusted environment variable
			return nil, runtime.TypePodman, fmt.Errorf("invalid Podman socket path: %w", err)
		}
		return []string{customSocketPath}, runtime.TypePodman, nil
	}

	if customSocketPath := os.Getenv(DockerSocketEnv); customSocketPath != "" {
		//nolint:gosec // G706: socket path from trusted environment variable
		slog.Debug("using Docker socket from env", "path", customSocketPath)
		if _, err := os.Stat(customSocketPath); err != nil { //nolint:gosec // G703: socket path from trusted environment variable
			return nil, runtime.TypeDocker, fmt.Errorf("invalid Docker socket path: %w", err)
		}
		return []string{customSocketPath}, runtime.TypeDocker, nil
	}

	if customSocketPath := os.Getenv(ColimaSocketEnv); customSocketPath != "" {
		//nolint:gosec // G706: socket path from trusted environment variable
		slog.Debug("using Colima socket from env", "path", customSocketPath)
		if _, err := os.Stat(customSocketPath); err != nil { //nolint:gosec // G703: socket path from trusted environment variable
			return nil, runtime.TypeDocker, fmt.Errorf("invalid Colima socket path: %w", err)
		}
		return []string{customSocketPath}, runtime.TypeDocker, nil
	}

	if rt == runtime.TypePodman {
		if paths := findPodmanSocket(); len(paths) > 0 {
			return paths, runtime.TypePodman, nil
		}
	}

	if rt == runtime.TypeDocker {
		if paths := findDockerSocket(); len(paths) > 0 {
			return paths, runtime.TypeDocker, nil
		}
	}

	if rt == runtime.TypeColima {
		if paths := findColimaSocket(); len(paths) > 0 {
			return paths, runtime.TypeColima, nil
		}
	}

	return nil, "", ErrRuntimeNotFound
}

// findPodmanSocket attempts to locate Podman sockets. Returns every candidate
// path that exists on disk in priority order; an empty slice means no Podman
// socket was found.
func findPodmanSocket() []string {
	var paths []string

	if _, err := os.Stat(PodmanSocketPath); err == nil {
		slog.Debug("found Podman socket", "path", PodmanSocketPath)
		paths = append(paths, PodmanSocketPath)
	} else {
		slog.Debug("failed to check Podman socket", "path", PodmanSocketPath, "error", err)
	}

	if xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR"); xdgRuntimeDir != "" {
		xdgSocketPath := filepath.Join(xdgRuntimeDir, PodmanXDGRuntimeSocketPath)
		if _, err := os.Stat(xdgSocketPath); err == nil { //nolint:gosec // G703: path from trusted env + constant
			//nolint:gosec // G706: socket path derived from XDG_RUNTIME_DIR env var
			slog.Debug("found Podman socket", "path", xdgSocketPath)
			paths = append(paths, xdgSocketPath)
		} else {
			//nolint:gosec // G706: socket path derived from XDG_RUNTIME_DIR env var
			slog.Debug("failed to check Podman socket", "path", xdgSocketPath, "error", err)
		}
	}

	if home := os.Getenv("HOME"); home != "" {
		userSocketPath := filepath.Join(home, ".local/share/containers/podman/machine/podman.sock")
		if _, err := os.Stat(userSocketPath); err == nil { //nolint:gosec // G703: path from trusted env + constant
			//nolint:gosec // G706: socket path derived from HOME env var
			slog.Debug("found Podman socket", "path", userSocketPath)
			paths = append(paths, userSocketPath)
		} else {
			//nolint:gosec // G706: socket path derived from HOME env var
			slog.Debug("failed to check Podman socket", "path", userSocketPath, "error", err)
		}
	}

	// Check TMPDIR for Podman Machine API sockets (macOS).
	// The socket path follows the pattern: $TMPDIR/podman/<machine-name>-api.sock
	if tmpDir := os.Getenv("TMPDIR"); tmpDir != "" {
		podmanTmpDir := filepath.Join(tmpDir, "podman")
		if _, err := os.Stat(podmanTmpDir); err == nil { //nolint:gosec // G703: path from trusted env
			matches, err := filepath.Glob(filepath.Join(podmanTmpDir, "*-api.sock"))
			if err == nil && len(matches) > 0 {
				for _, socketPath := range matches {
					//nolint:gosec // G706: socket path discovered from TMPDIR
					slog.Debug("found Podman machine API socket", "path", socketPath)
					paths = append(paths, socketPath)
				}
			} else {
				//nolint:gosec // G706: directory path from TMPDIR env var
				slog.Debug("no Podman machine API sockets found", "dir", podmanTmpDir)
			}
		} else {
			//nolint:gosec // G706: directory path from TMPDIR env var
			slog.Debug("podman temp directory not found", "dir", podmanTmpDir, "error", err)
		}
	}

	return paths
}

// systemDockerSocketPath is the system-wide Docker socket path probed by
// findDockerSocket. It defaults to DockerSocketPath and is package-private so
// tests can redirect the system check to a sandbox path.
var systemDockerSocketPath = DockerSocketPath

// findDockerSocket attempts to locate Docker sockets. Returns every candidate
// path that exists on disk in priority order; an empty slice means no Docker
// socket was found.
//
// Returning every candidate lets callers fall back to a per-user socket like
// ~/.docker/desktop/docker.sock when the system socket /var/run/docker.sock
// exists but is not connectable (e.g. the running user is not in the docker
// group). Stat-OK on a path does not imply the daemon will accept a connect.
func findDockerSocket() []string {
	var paths []string

	if _, err := os.Stat(systemDockerSocketPath); err == nil {
		slog.Debug("found Docker socket", "path", systemDockerSocketPath)
		paths = append(paths, systemDockerSocketPath)
	} else {
		slog.Debug("failed to check Docker socket", "path", systemDockerSocketPath, "error", err)
	}

	home := os.Getenv("HOME")
	if home == "" {
		return paths
	}

	userCandidates := []struct {
		label    string
		relative string
	}{
		{"Docker Desktop socket", DockerDesktopMacSocketPath},
		{"Docker Desktop socket", DockerDesktopLinuxSocketPath},
		{"Rancher Desktop socket", RancherDesktopMacSocketPath},
		{"OrbStack socket", OrbStackMacSocketPath},
	}
	for _, c := range userCandidates {
		path := filepath.Join(home, c.relative)
		if _, err := os.Stat(path); err == nil { // #nosec G703 -- path is built from HOME + constant socket path
			//nolint:gosec // G706: socket path derived from HOME env var
			slog.Debug("found "+c.label, "path", path)
			paths = append(paths, path)
		} else {
			//nolint:gosec // G706: socket path derived from HOME env var
			slog.Debug("failed to check "+c.label, "path", path, "error", err)
		}
	}

	return paths
}

// findColimaSocket attempts to locate Colima sockets. Returns every candidate
// path that exists on disk in priority order; an empty slice means no Colima
// socket was found.
func findColimaSocket() []string {
	var paths []string

	if _, err := os.Stat(ColimaDesktopMacSocketPath); err == nil {
		slog.Debug("found Colima socket", "path", ColimaDesktopMacSocketPath)
		paths = append(paths, ColimaDesktopMacSocketPath)
	} else {
		slog.Debug("failed to check Colima socket", "path", ColimaDesktopMacSocketPath, "error", err)
	}

	if home := os.Getenv("HOME"); home != "" {
		userSocketPath := filepath.Join(home, ColimaDesktopMacSocketPath)
		if _, err := os.Stat(userSocketPath); err == nil { // #nosec G703 -- path is built from HOME + constant socket path
			//nolint:gosec // G706: socket path derived from HOME env var
			slog.Debug("found Colima socket", "path", userSocketPath)
			paths = append(paths, userSocketPath)
		} else {
			//nolint:gosec // G706: socket path derived from HOME env var
			slog.Debug("failed to check Colima socket", "path", userSocketPath, "error", err)
		}
	}

	return paths
}

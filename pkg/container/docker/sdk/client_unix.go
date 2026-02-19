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

	"github.com/docker/docker/client"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// ErrRuntimeNotFound is returned when a container runtime is not found
var ErrRuntimeNotFound = fmt.Errorf("container runtime not found")

// newPlatformClient creates a Docker client using Unix sockets
func newPlatformClient(socketPath string) (*http.Client, []client.Opt) {
	// Create a custom HTTP client that uses the Unix socket
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	// Create Docker client options
	opts := []client.Opt{
		client.WithAPIVersionNegotiation(),
		client.WithHTTPClient(httpClient),
		client.WithHost("unix://" + socketPath),
	}

	return httpClient, opts
}

// findPlatformContainerSocket finds a container socket path on Unix systems
func findPlatformContainerSocket(rt runtime.Type) (string, runtime.Type, error) {
	// First check for custom socket paths via environment variables
	if customSocketPath := os.Getenv(PodmanSocketEnv); customSocketPath != "" {
		//nolint:gosec // G706: socket path from trusted environment variable
		slog.Debug("using Podman socket from env", "path", customSocketPath)
		// validate the socket path
		if _, err := os.Stat(customSocketPath); err != nil { //nolint:gosec // G703: socket path from trusted environment variable
			return "", runtime.TypePodman, fmt.Errorf("invalid Podman socket path: %w", err)
		}
		return customSocketPath, runtime.TypePodman, nil
	}

	if customSocketPath := os.Getenv(DockerSocketEnv); customSocketPath != "" {
		//nolint:gosec // G706: socket path from trusted environment variable
		slog.Debug("using Docker socket from env", "path", customSocketPath)
		// validate the socket path
		if _, err := os.Stat(customSocketPath); err != nil { //nolint:gosec // G703: socket path from trusted environment variable
			return "", runtime.TypeDocker, fmt.Errorf("invalid Docker socket path: %w", err)
		}
		return customSocketPath, runtime.TypeDocker, nil
	}

	if customSocketPath := os.Getenv(ColimaSocketEnv); customSocketPath != "" {
		//nolint:gosec // G706: socket path from trusted environment variable
		slog.Debug("using Colima socket from env", "path", customSocketPath)
		// validate the socket path
		if _, err := os.Stat(customSocketPath); err != nil { //nolint:gosec // G703: socket path from trusted environment variable
			return "", runtime.TypeDocker, fmt.Errorf("invalid Colima socket path: %w", err)
		}
		return customSocketPath, runtime.TypeDocker, nil
	}

	if rt == runtime.TypePodman {
		socketPath, err := findPodmanSocket()
		if err == nil {
			return socketPath, runtime.TypePodman, nil
		}
	}

	if rt == runtime.TypeDocker {
		socketPath, err := findDockerSocket()
		if err == nil {
			return socketPath, runtime.TypeDocker, nil
		}
	}

	if rt == runtime.TypeColima {
		socketPath, err := findColimaSocket()
		if err == nil {
			return socketPath, runtime.TypeColima, nil
		}
	}

	return "", "", ErrRuntimeNotFound
}

// findPodmanSocket attempts to locate a Podman socket
func findPodmanSocket() (string, error) {
	// Check standard Podman location
	_, err := os.Stat(PodmanSocketPath)
	if err == nil {
		slog.Debug("found Podman socket", "path", PodmanSocketPath)
		return PodmanSocketPath, nil
	}

	slog.Debug("failed to check Podman socket", "path", PodmanSocketPath, "error", err)

	// Check XDG_RUNTIME_DIR location for Podman
	if xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR"); xdgRuntimeDir != "" {
		xdgSocketPath := filepath.Join(xdgRuntimeDir, PodmanXDGRuntimeSocketPath)
		_, err := os.Stat(xdgSocketPath) //nolint:gosec // G703: path from trusted env + constant

		if err == nil {
			//nolint:gosec // G706: socket path derived from XDG_RUNTIME_DIR env var
			slog.Debug("found Podman socket", "path", xdgSocketPath)
			return xdgSocketPath, nil
		}

		//nolint:gosec // G706: socket path derived from XDG_RUNTIME_DIR env var
		slog.Debug("failed to check Podman socket", "path", xdgSocketPath, "error", err)
	}

	// Check user-specific location for Podman
	if home := os.Getenv("HOME"); home != "" {
		userSocketPath := filepath.Join(home, ".local/share/containers/podman/machine/podman.sock")
		_, err := os.Stat(userSocketPath) //nolint:gosec // G703: path from trusted env + constant

		if err == nil {
			//nolint:gosec // G706: socket path derived from HOME env var
			slog.Debug("found Podman socket", "path", userSocketPath)
			return userSocketPath, nil
		}

		//nolint:gosec // G706: socket path derived from HOME env var
		slog.Debug("failed to check Podman socket", "path", userSocketPath, "error", err)
	}

	// Check TMPDIR for Podman Machine API sockets (macOS)
	// The socket path follows the pattern: $TMPDIR/podman/<machine-name>-api.sock
	if tmpDir := os.Getenv("TMPDIR"); tmpDir != "" {
		podmanTmpDir := filepath.Join(tmpDir, "podman")
		if _, err := os.Stat(podmanTmpDir); err == nil { //nolint:gosec // G703: path from trusted env
			// Look for any -api.sock files (there may be multiple machines)
			matches, err := filepath.Glob(filepath.Join(podmanTmpDir, "*-api.sock"))
			if err == nil && len(matches) > 0 {
				// Use the first available API socket
				socketPath := matches[0]
				//nolint:gosec // G706: socket path discovered from TMPDIR
				slog.Debug("found Podman machine API socket", "path", socketPath)
				return socketPath, nil
			}
			//nolint:gosec // G706: directory path from TMPDIR env var
			slog.Debug("no Podman machine API sockets found", "dir", podmanTmpDir)
		} else {
			//nolint:gosec // G706: directory path from TMPDIR env var
			slog.Debug("podman temp directory not found", "dir", podmanTmpDir, "error", err)
		}
	}

	return "", fmt.Errorf("podman socket not found in standard locations")
}

// findDockerSocket attempts to locate a Docker socket
func findDockerSocket() (string, error) {
	// Try Docker socket as fallback
	_, err := os.Stat(DockerSocketPath)

	if err == nil {
		slog.Debug("found Docker socket", "path", DockerSocketPath)
		return DockerSocketPath, nil
	}

	slog.Debug("failed to check Docker socket", "path", DockerSocketPath, "error", err)

	// Try Docker Desktop socket path on macOS
	if home := os.Getenv("HOME"); home != "" {
		dockerDesktopPath := filepath.Join(home, DockerDesktopMacSocketPath)
		_, err := os.Stat(dockerDesktopPath) // #nosec G703 -- path is built from HOME + constant socket path

		if err == nil {
			//nolint:gosec // G706: socket path derived from HOME env var
			slog.Debug("found Docker Desktop socket", "path", dockerDesktopPath)
			return dockerDesktopPath, nil
		}

		//nolint:gosec // G706: socket path derived from HOME env var
		slog.Debug("failed to check Docker Desktop socket", "path", dockerDesktopPath, "error", err)
	}

	// Try Rancher Desktop socket path on macOS
	if home := os.Getenv("HOME"); home != "" {
		rancherDesktopPath := filepath.Join(home, RancherDesktopMacSocketPath)
		_, err := os.Stat(rancherDesktopPath) // #nosec G703 -- path is built from HOME + constant socket path

		if err == nil {
			//nolint:gosec // G706: socket path derived from HOME env var
			slog.Debug("found Rancher Desktop socket", "path", rancherDesktopPath)
			return rancherDesktopPath, nil
		}

		//nolint:gosec // G706: socket path derived from HOME env var
		slog.Debug("failed to check Rancher Desktop socket", "path", rancherDesktopPath, "error", err)
	}

	// Try OrbStack socket path on macOS
	if home := os.Getenv("HOME"); home != "" {
		orbStackPath := filepath.Join(home, OrbStackMacSocketPath)
		_, err := os.Stat(orbStackPath) // #nosec G703 -- path is built from HOME + constant socket path

		if err == nil {
			//nolint:gosec // G706: socket path derived from HOME env var
			slog.Debug("found OrbStack socket", "path", orbStackPath)
			return orbStackPath, nil
		}

		//nolint:gosec // G706: socket path derived from HOME env var
		slog.Debug("failed to check OrbStack socket", "path", orbStackPath, "error", err)
	}

	return "", fmt.Errorf("docker socket not found in standard locations")
}

// findColimaSocket attempts to locate a Colima socket
func findColimaSocket() (string, error) {
	// Check standard Colima location
	_, err := os.Stat(ColimaDesktopMacSocketPath)
	if err == nil {
		slog.Debug("found Colima socket", "path", ColimaDesktopMacSocketPath)
		return ColimaDesktopMacSocketPath, nil
	}

	slog.Debug("failed to check Colima socket", "path", ColimaDesktopMacSocketPath, "error", err)

	// Check user-specific location for Colima
	if home := os.Getenv("HOME"); home != "" {
		userSocketPath := filepath.Join(home, ColimaDesktopMacSocketPath)
		_, err := os.Stat(userSocketPath) // #nosec G703 -- path is built from HOME + constant socket path

		if err == nil {
			//nolint:gosec // G706: socket path derived from HOME env var
			slog.Debug("found Colima socket", "path", userSocketPath)
			return userSocketPath, nil
		}

		//nolint:gosec // G706: socket path derived from HOME env var
		slog.Debug("failed to check Colima socket", "path", userSocketPath, "error", err)
	}

	return "", fmt.Errorf("colima socket not found in standard locations")
}

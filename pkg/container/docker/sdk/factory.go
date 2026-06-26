// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sdk provides a factory method for creating a Docker client.
package sdk

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	mobyclient "github.com/moby/moby/client"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

/*
 * This file contains logic for instantiating the Docker SDK client.
 * In future, this may be centralized behind a single client wrapper.
 */

// Environment variable names
const (
	// DockerSocketEnv is the environment variable for custom Docker socket path
	DockerSocketEnv = "TOOLHIVE_DOCKER_SOCKET"
	// PodmanSocketEnv is the environment variable for custom Podman socket path
	PodmanSocketEnv = "TOOLHIVE_PODMAN_SOCKET"
	// ColimaSocketEnv is the environment variable for custom Colima socket path
	ColimaSocketEnv = "TOOLHIVE_COLIMA_SOCKET"
)

// Common socket paths
const (
	// PodmanSocketPath is the default Podman socket path
	PodmanSocketPath = "/var/run/podman/podman.sock"
	// PodmanXDGRuntimeSocketPath is the XDG runtime Podman socket path
	PodmanXDGRuntimeSocketPath = "podman/podman.sock"
	// DockerSocketPath is the default Docker socket path
	DockerSocketPath = "/var/run/docker.sock"
	// DockerDesktopMacSocketPath is the Docker Desktop socket path on macOS
	DockerDesktopMacSocketPath = ".docker/run/docker.sock"
	// DockerDesktopLinuxSocketPath is the Docker Desktop socket path on Linux
	// (relative to $HOME). Docker Desktop on Linux registers a "desktop-linux"
	// Docker context that points to this socket.
	DockerDesktopLinuxSocketPath = ".docker/desktop/docker.sock"
	// RancherDesktopMacSocketPath is the Docker socket path for Rancher Desktop on macOS
	RancherDesktopMacSocketPath = ".rd/docker.sock"
	// OrbStackMacSocketPath is the Docker socket path for OrbStack on macOS
	OrbStackMacSocketPath = ".orbstack/run/docker.sock"
	// ColimaDesktopMacSocketPath is the Docker socket path for Colima on macOS
	ColimaDesktopMacSocketPath = ".colima/default/docker.sock"
)

var supportedSocketPaths = []runtime.Type{runtime.TypePodman, runtime.TypeDocker, runtime.TypeColima}

// NewDockerClient creates a new container client.
//
// For each runtime (Podman, Docker, Colima) it asks the platform helper for
// every candidate socket that exists on disk and then tries to connect to each
// in turn. This matters in mixed setups — e.g. /var/run/docker.sock is present
// but the running user is not in the docker group, while Docker Desktop's
// per-user socket at ~/.docker/desktop/docker.sock would work. Without
// per-runtime fallback the first stat-OK socket short-circuits discovery and
// we'd surface "no container runtime available" even though a usable Docker
// daemon is reachable through a different socket.
func NewDockerClient(ctx context.Context) (*mobyclient.Client, string, runtime.Type, error) {
	var lastErr error

	for _, sp := range supportedSocketPaths {
		socketPaths, runtimeType, err := findContainerSocket(sp)
		if err != nil {
			//nolint:gosec // G706: runtime type from internal config
			slog.Debug("failed to find socket", "runtime", sp, "error", err)
			lastErr = err
			continue
		}

		for _, socketPath := range socketPaths {
			c, err := newClientWithSocketPath(ctx, socketPath)
			if err != nil {
				lastErr = err
				//nolint:gosec // G706: runtime type from internal config
				slog.Debug("failed to create client", "runtime", sp, "socket", socketPath, "error", err)
				continue
			}

			//nolint:gosec // G706: runtime type from internal detection
			slog.Debug("successfully connected to runtime", "runtime", runtimeType, "socket", socketPath)
			return c, socketPath, runtimeType, nil
		}
	}

	if lastErr != nil {
		return nil, "", "", fmt.Errorf("no supported container runtime available%s: %w", dockerPermissionHint(lastErr), lastErr)
	}
	return nil, "", "", fmt.Errorf("no supported container runtime found/running")
}

// dockerPermissionHint returns an actionable hint when the underlying socket
// connect was rejected with a permission error. The most common cause on
// Linux is the calling user not being a member of the docker group, which the
// raw "permission denied" error does not call out.
func dockerPermissionHint(err error) string {
	if err == nil {
		return ""
	}
	// errors.Is handles syscall.EACCES/EPERM (which map to fs.ErrPermission via
	// the syscall.Errno.Is shim); the Docker client wraps the underlying
	// *os.SyscallError in its own message, so also fall back to a substring
	// match on the rendered text for robustness across error wrappings.
	if !errors.Is(err, fs.ErrPermission) && !strings.Contains(err.Error(), "permission denied") {
		return ""
	}
	return " (hint: the Docker socket rejected the connection with 'permission denied' — " +
		"is your user in the 'docker' group? On Linux: 'sudo usermod -aG docker $USER', " +
		"then log out and back in. See https://docs.docker.com/engine/install/linux-postinstall/.)"
}

// NewClientWithSocketPath creates a new container client with a specific socket path
func newClientWithSocketPath(ctx context.Context, socketPath string) (*mobyclient.Client, error) {
	// Create platform-specific client
	_, opts := newPlatformClient(socketPath)

	// Create Docker client with the custom HTTP client
	dockerClient, err := mobyclient.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	// Make sure we can ping the server.
	_, err = dockerClient.Ping(ctx, mobyclient.PingOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to ping Docker server at %s: %w", socketPath, err)
	}

	return dockerClient, nil
}

// findContainerSocket finds candidate container socket paths for the given
// runtime, preferring Podman over Docker. The returned slice is ordered from
// highest to lowest priority and may contain multiple entries on Unix when
// several known socket locations exist on disk simultaneously.
func findContainerSocket(rt runtime.Type) ([]string, runtime.Type, error) {
	// Use platform-specific implementation
	return findPlatformContainerSocket(rt)
}

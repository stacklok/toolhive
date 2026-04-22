// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sdk provides a factory method for creating a Docker client.
package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
	"github.com/docker/docker/client"
	"gopkg.in/yaml.v3"

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
	// RancherDesktopMacSocketPath is the Docker socket path for Rancher Desktop on macOS
	RancherDesktopMacSocketPath = ".rd/docker.sock"
	// OrbStackMacSocketPath is the Docker socket path for OrbStack on macOS
	OrbStackMacSocketPath = ".orbstack/run/docker.sock"
	// ColimaDesktopMacSocketPath is the Docker socket path for Colima on macOS
	ColimaDesktopMacSocketPath = ".colima/default/docker.sock"
)

var supportedSocketPaths = []runtime.Type{runtime.TypePodman, runtime.TypeDocker, runtime.TypeColima}

// loadSocketOverrides reads socket path overrides from the ToolHive config file.
// Best-effort: returns empty overrides on any error so auto-detection takes over.
func loadSocketOverrides() runtime.SocketConfig {
	configPath, err := xdg.ConfigFile("toolhive/config.yaml")
	if err != nil {
		slog.Debug("failed to resolve config path for socket overrides", "error", err)
		return runtime.SocketConfig{}
	}

	// #nosec G304: path is derived from XDG config dir, not user input.
	data, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		slog.Debug("failed to read config file for socket overrides", "error", err)
		return runtime.SocketConfig{}
	}

	var cfg struct {
		ContainerRuntime runtime.SocketConfig `yaml:"container_runtime,omitempty"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		slog.Debug("failed to parse config file for socket overrides", "error", err)
		return runtime.SocketConfig{}
	}

	return cfg.ContainerRuntime
}

// NewDockerClient creates a new container client
func NewDockerClient(ctx context.Context) (*client.Client, string, runtime.Type, error) {
	var lastErr error

	overrides := loadSocketOverrides()

	// We try to find a container socket for the given runtime
	// We try Podman first, then Docker as fallback
	for _, sp := range supportedSocketPaths {
		// Try to find a container socket for the given runtime
		socketPath, runtimeType, err := findContainerSocket(sp, overrides)
		if err != nil {
			//nolint:gosec // G706: runtime type from internal config
			slog.Debug("failed to find socket", "runtime", sp, "error", err)
			lastErr = err
			continue
		}

		c, err := newClientWithSocketPath(ctx, socketPath)
		if err != nil {
			lastErr = err
			//nolint:gosec // G706: runtime type from internal config
			slog.Debug("failed to create client", "runtime", sp, "error", err)
			continue
		}

		//nolint:gosec // G706: runtime type from internal detection
		slog.Debug("successfully connected to runtime", "runtime", runtimeType)
		return c, socketPath, runtimeType, nil
	}

	if lastErr != nil {
		return nil, "", "", fmt.Errorf("no supported container runtime available: %w", lastErr)
	}
	return nil, "", "", fmt.Errorf("no supported container runtime found/running")
}

// NewClientWithSocketPath creates a new container client with a specific socket path
func newClientWithSocketPath(ctx context.Context, socketPath string) (*client.Client, error) {
	// Create platform-specific client
	_, opts := newPlatformClient(socketPath)

	// Create Docker client with the custom HTTP client
	dockerClient, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	// Make sure we can ping the server.
	_, err = dockerClient.Ping(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to ping Docker server at %s: %w", socketPath, err)
	}

	return dockerClient, nil
}

// findContainerSocket finds a container socket path, preferring Podman over Docker
func findContainerSocket(rt runtime.Type, overrides runtime.SocketConfig) (string, runtime.Type, error) {
	// Use platform-specific implementation
	return findPlatformContainerSocket(rt, overrides)
}

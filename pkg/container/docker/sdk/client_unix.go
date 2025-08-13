//go:build !windows
// +build !windows

package sdk

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/docker/docker/client"
	"go.uber.org/zap"

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
func findPlatformContainerSocket(rt runtime.Type, logger *zap.SugaredLogger) (string, runtime.Type, error) {
	// First check for custom socket paths via environment variables
	if customSocketPath := os.Getenv(PodmanSocketEnv); customSocketPath != "" {
		logger.Debugf("Using Podman socket from env: %s", customSocketPath)
		// validate the socket path
		if _, err := os.Stat(customSocketPath); err != nil {
			return "", runtime.TypePodman, fmt.Errorf("invalid Podman socket path: %w", err)
		}
		return customSocketPath, runtime.TypePodman, nil
	}

	if customSocketPath := os.Getenv(DockerSocketEnv); customSocketPath != "" {
		logger.Debugf("Using Docker socket from env: %s", customSocketPath)
		// validate the socket path
		if _, err := os.Stat(customSocketPath); err != nil {
			return "", runtime.TypeDocker, fmt.Errorf("invalid Docker socket path: %w", err)
		}
		return customSocketPath, runtime.TypeDocker, nil
	}

	if rt == runtime.TypePodman {
		socketPath, err := findPodmanSocket(logger)
		if err == nil {
			return socketPath, runtime.TypePodman, nil
		}
	}

	if rt == runtime.TypeDocker {
		socketPath, err := findDockerSocket(logger)
		if err == nil {
			return socketPath, runtime.TypeDocker, nil
		}
	}

	return "", "", ErrRuntimeNotFound
}

// findPodmanSocket attempts to locate a Podman socket
func findPodmanSocket(logger *zap.SugaredLogger) (string, error) {
	// Check standard Podman location
	_, err := os.Stat(PodmanSocketPath)
	if err == nil {
		logger.Debugf("Found Podman socket at %s", PodmanSocketPath)
		return PodmanSocketPath, nil
	}

	logger.Debugf("Failed to check Podman socket at %s: %v", PodmanSocketPath, err)

	// Check XDG_RUNTIME_DIR location for Podman
	if xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR"); xdgRuntimeDir != "" {
		xdgSocketPath := filepath.Join(xdgRuntimeDir, PodmanXDGRuntimeSocketPath)
		_, err := os.Stat(xdgSocketPath)

		if err == nil {
			logger.Debugf("Found Podman socket at %s", xdgSocketPath)
			return xdgSocketPath, nil
		}

		logger.Debugf("Failed to check Podman socket at %s: %v", xdgSocketPath, err)
	}

	// Check user-specific location for Podman
	if home := os.Getenv("HOME"); home != "" {
		userSocketPath := filepath.Join(home, ".local/share/containers/podman/machine/podman.sock")
		_, err := os.Stat(userSocketPath)

		if err == nil {
			logger.Debugf("Found Podman socket at %s", userSocketPath)
			return userSocketPath, nil
		}

		logger.Debugf("Failed to check Podman socket at %s: %v", userSocketPath, err)
	}

	return "", fmt.Errorf("podman socket not found in standard locations")
}

// findDockerSocket attempts to locate a Docker socket
func findDockerSocket(logger *zap.SugaredLogger) (string, error) {
	// Try Docker socket as fallback
	_, err := os.Stat(DockerSocketPath)

	if err == nil {
		logger.Debugf("Found Docker socket at %s", DockerSocketPath)
		return DockerSocketPath, nil
	}

	logger.Debugf("Failed to check Docker socket at %s: %v", DockerSocketPath, err)

	// Try Docker Desktop socket path on macOS
	if home := os.Getenv("HOME"); home != "" {
		dockerDesktopPath := filepath.Join(home, DockerDesktopMacSocketPath)
		_, err := os.Stat(dockerDesktopPath)

		if err == nil {
			logger.Debugf("Found Docker Desktop socket at %s", dockerDesktopPath)
			return dockerDesktopPath, nil
		}

		logger.Debugf("Failed to check Docker Desktop socket at %s: %v", dockerDesktopPath, err)
	}

	// Try Rancher Desktop socket path on macOS
	if home := os.Getenv("HOME"); home != "" {
		rancherDesktopPath := filepath.Join(home, RancherDesktopMacSocketPath)
		_, err := os.Stat(rancherDesktopPath)

		if err == nil {
			logger.Debugf("Found Rancher Desktop socket at %s", rancherDesktopPath)
			return rancherDesktopPath, nil
		}

		logger.Debugf("Failed to check Rancher Desktop socket at %s: %v", rancherDesktopPath, err)
	}

	return "", fmt.Errorf("docker socket not found in standard locations")
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
	mobyclient "github.com/moby/moby/client"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

var ErrRuntimeNotFound = fmt.Errorf("container runtime not found")

// Windows named pipe paths
const (
	// DockerDesktopWindowsPipePath is the Docker Desktop named pipe path on Windows
	DockerDesktopWindowsPipePath = `\\.\pipe\docker_engine`

	// PodmanDesktopWindowsPipePath is the Podman Desktop named pipe path on Windows
	PodmanDesktopWindowsPipePath = `\\.\pipe\podman-api`
)

// Windows named pipe connection timeout
const pipeConnectionTimeout = 2 * time.Second

// newPlatformClient creates a Docker client using Windows named pipes
func newPlatformClient(pipePath string) (*http.Client, []mobyclient.Opt) {
	// Create a custom HTTP client that uses Windows named pipes
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				// Create a context with timeout for the pipe connection
				dialCtx, cancel := context.WithTimeout(ctx, pipeConnectionTimeout)
				defer cancel()
				return winio.DialPipeContext(dialCtx, pipePath)
			},
		},
	}

	// Create Docker client options. API-version negotiation is enabled by
	// default in the new client, so it no longer needs to be requested
	// explicitly.
	opts := []mobyclient.Opt{
		mobyclient.WithHTTPClient(httpClient),
		mobyclient.WithHost("npipe://" + pipePath),
	}

	return httpClient, opts
}

// findPlatformContainerSocket finds container pipe paths on Windows. Returns
// a slice for parity with the Unix implementation, but Windows only ever has
// one candidate per runtime type (the well-known named pipe).
func findPlatformContainerSocket(rt runtime.Type) ([]string, runtime.Type, error) {
	// First check for custom socket paths via environment variables
	if customPipePath := os.Getenv(PodmanSocketEnv); customPipePath != "" {
		//nolint:gosec // G706: pipe path from trusted environment variable
		slog.Debug("using Podman pipe from env", "path", customPipePath)
		// Validate the pipe path exists with timeout
		ctx, cancel := context.WithTimeout(context.Background(), pipeConnectionTimeout)
		defer cancel()
		conn, err := winio.DialPipeContext(ctx, customPipePath)
		if err != nil {
			return nil, runtime.TypePodman, fmt.Errorf("invalid Podman pipe path: %w", err)
		}
		conn.Close()
		return []string{customPipePath}, runtime.TypePodman, nil
	}

	if customPipePath := os.Getenv(DockerSocketEnv); customPipePath != "" {
		//nolint:gosec // G706: pipe path from trusted environment variable
		slog.Debug("using Docker pipe from env", "path", customPipePath)
		// Validate the pipe path exists with timeout
		ctx, cancel := context.WithTimeout(context.Background(), pipeConnectionTimeout)
		defer cancel()
		conn, err := winio.DialPipeContext(ctx, customPipePath)
		if err != nil {
			return nil, runtime.TypeDocker, fmt.Errorf("invalid Docker pipe path: %w", err)
		}
		conn.Close()
		return []string{customPipePath}, runtime.TypeDocker, nil
	}

	if rt == runtime.TypePodman {
		// Try Podman named pipe with timeout
		ctx, cancel := context.WithTimeout(context.Background(), pipeConnectionTimeout)
		defer cancel()
		conn, err := winio.DialPipeContext(ctx, PodmanDesktopWindowsPipePath)
		if err == nil {
			slog.Debug("found Podman pipe", "path", PodmanDesktopWindowsPipePath)
			conn.Close()
			return []string{PodmanDesktopWindowsPipePath}, runtime.TypePodman, nil
		}
		slog.Debug("failed to connect to Podman pipe", "path", PodmanDesktopWindowsPipePath, "error", err)
	}

	if rt == runtime.TypeDocker {
		// Try Docker named pipe with timeout
		ctx, cancel := context.WithTimeout(context.Background(), pipeConnectionTimeout)
		defer cancel()
		conn, err := winio.DialPipeContext(ctx, DockerDesktopWindowsPipePath)
		if err == nil {
			slog.Debug("found Docker pipe", "path", DockerDesktopWindowsPipePath)
			conn.Close()
			return []string{DockerDesktopWindowsPipePath}, runtime.TypeDocker, nil
		}
		slog.Debug("failed to connect to Docker pipe", "path", DockerDesktopWindowsPipePath, "error", err)
	}

	return nil, "", ErrRuntimeNotFound
}

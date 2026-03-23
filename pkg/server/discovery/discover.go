// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"os"

	"github.com/stacklok/toolhive/pkg/process"
)

// ServerState represents the state of a discovered server.
type ServerState int

const (
	// StateNotFound means no discovery file exists.
	StateNotFound ServerState = iota
	// StateRunning means the server is healthy and responding.
	StateRunning
	// StateStale means the discovery file exists but the process is dead.
	StateStale
	// StateUnhealthy means the process is alive but the server is not responding.
	StateUnhealthy
)

// String returns a human-readable representation of the server state.
func (s ServerState) String() string {
	switch s {
	case StateNotFound:
		return "not_found"
	case StateRunning:
		return "running"
	case StateStale:
		return "stale"
	case StateUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// DiscoverResult holds the result of a server discovery attempt.
type DiscoverResult struct {
	// State is the discovered server state.
	State ServerState
	// Info is the server information from the discovery file.
	// It is nil when State is StateNotFound.
	Info *ServerInfo
}

// Discover attempts to find a running ToolHive server by reading the discovery
// file and verifying the server is healthy.
func Discover(ctx context.Context) (*DiscoverResult, error) {
	return discover(ctx, defaultDiscoveryDir())
}

// discover is the internal implementation that accepts a directory for testability.
func discover(ctx context.Context, dir string) (*DiscoverResult, error) {
	info, err := readServerInfoFrom(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &DiscoverResult{State: StateNotFound}, nil
		}
		return nil, err
	}

	// Try health check with nonce verification
	if err := CheckHealth(ctx, info.URL, info.Nonce); err == nil {
		return &DiscoverResult{State: StateRunning, Info: info}, nil
	}

	// Health check failed — check if the process is still alive
	alive, err := process.FindProcess(info.PID)
	if err != nil {
		// Can't determine process state; treat as stale
		return &DiscoverResult{State: StateStale, Info: info}, nil
	}

	if !alive {
		return &DiscoverResult{State: StateStale, Info: info}, nil
	}

	return &DiscoverResult{State: StateUnhealthy, Info: info}, nil
}

// CleanupStale removes a stale discovery file. Clients should call this
// when Discover returns StateStale.
func CleanupStale() error {
	return RemoveServerInfo()
}

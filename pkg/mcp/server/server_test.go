// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/config"
	registrymocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

// newTestServer creates a Server for testing. On macOS, where a container
// runtime may not be available, it uses mock dependencies. On other platforms
// it uses the real New() constructor with an actual container runtime.
func newTestServer(t *testing.T, cfg *Config) *Server {
	t.Helper()
	if runtime.GOOS == "darwin" {
		ctrl := gomock.NewController(t)
		t.Cleanup(func() { ctrl.Finish() })

		handler := &Handler{
			ctx:              context.Background(),
			workloadManager:  workloadsmocks.NewMockManager(ctrl),
			registryProvider: registrymocks.NewMockProvider(ctrl),
			configProvider:   config.NewDefaultProvider(),
		}
		return newServerWithHandler(context.Background(), cfg, handler)
	}
	s, err := New(context.Background(), cfg)
	require.NoError(t, err)
	return s
}

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config *Config
	}{
		{
			name: "valid config",
			config: &Config{
				Host: "localhost",
				Port: "8080",
			},
		},
		{
			name: "empty host defaults to empty",
			config: &Config{
				Host: "",
				Port: "8080",
			},
		},
		{
			name: "custom port",
			config: &Config{
				Host: "127.0.0.1",
				Port: "9090",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newTestServer(t, tt.config)
			assert.NotNil(t, s)
			assert.Equal(t, tt.config, s.config)
			assert.NotNil(t, s.mcpServer)
			assert.NotNil(t, s.httpServer)
			assert.NotNil(t, s.handler)
		})
	}
}

func TestServer_GetAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		config   *Config
		expected string
	}{
		{
			name: "localhost with default port",
			config: &Config{
				Host: "localhost",
				Port: DefaultMCPPort,
			},
			expected: "http://localhost:4483/mcp",
		},
		{
			name: "custom host and port",
			config: &Config{
				Host: "192.168.1.1",
				Port: "9090",
			},
			expected: "http://192.168.1.1:9090/mcp",
		},
		{
			name: "empty host",
			config: &Config{
				Host: "",
				Port: "8080",
			},
			expected: "http://:8080/mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newTestServer(t, tt.config)
			assert.Equal(t, tt.expected, s.GetAddress())
		})
	}
}

func TestServer_StartAndShutdown(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Host: "127.0.0.1",
		Port: "0", // Use port 0 to let the system assign a free port
	}

	server := newTestServer(t, cfg)
	require.NotNil(t, server)

	// Start server in a goroutine
	serverErr := make(chan error, 1)
	go func() {
		err := server.Start()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Check if server started without error
	select {
	case err := <-serverErr:
		t.Fatalf("Server failed to start: %v", err)
	default:
		// Server is running
	}

	// Shutdown the server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	shutdownErr := server.Shutdown(shutdownCtx)
	assert.NoError(t, shutdownErr)

	// Wait for server goroutine to finish
	select {
	case <-serverErr:
		// Server stopped
	case <-time.After(1 * time.Second):
		t.Fatal("Server did not stop in time")
	}
}

func TestDefaultMCPPort(t *testing.T) {
	t.Parallel()
	// Test that the default port is set correctly
	assert.Equal(t, "4483", DefaultMCPPort)
}

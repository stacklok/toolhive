package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	// Initialize the logger for tests
	logger.Initialize()
}

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: &Config{
				Host: "localhost",
				Port: "8080",
			},
			wantErr: false,
		},
		{
			name: "empty host defaults to empty",
			config: &Config{
				Host: "",
				Port: "8080",
			},
			wantErr: false,
		},
		{
			name: "custom port",
			config: &Config{
				Host: "127.0.0.1",
				Port: "9090",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			server, err := New(ctx, tt.config)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, server)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, server)
				assert.Equal(t, tt.config, server.config)
				assert.NotNil(t, server.mcpServer)
				assert.NotNil(t, server.httpServer)
				assert.NotNil(t, server.handler)
			}
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
			ctx := context.Background()
			server, err := New(ctx, tt.config)
			require.NoError(t, err)

			address := server.GetAddress()
			assert.Equal(t, tt.expected, address)
		})
	}
}

func TestServer_StartAndShutdown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	config := &Config{
		Host: "127.0.0.1",
		Port: "0", // Use port 0 to let the system assign a free port
	}

	server, err := New(ctx, config)
	require.NoError(t, err)

	// Start server in a goroutine
	serverErr := make(chan error, 1)
	go func() {
		err := server.Start()
		if err != nil && err != http.ErrServerClosed {
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

	err = server.Shutdown(shutdownCtx)
	assert.NoError(t, err)

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

// Package e2e provides end-to-end testing utilities for ToolHive HTTP API.
package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:staticcheck // Standard practice for Ginkgo
	. "github.com/onsi/gomega"    //nolint:staticcheck // Standard practice for Gomega

	"github.com/stacklok/toolhive/pkg/api"
	"github.com/stacklok/toolhive/pkg/container"
)

// ServerConfig holds configuration for the API server in tests
type ServerConfig struct {
	Address        string
	StartTimeout   time.Duration
	RequestTimeout time.Duration
	DebugMode      bool
}

// NewServerConfig creates a new API server configuration with defaults
func NewServerConfig() *ServerConfig {
	return &ServerConfig{
		Address:        "127.0.0.1:0", // Use random port
		StartTimeout:   30 * time.Second,
		RequestTimeout: 10 * time.Second,
		DebugMode:      false,
	}
}

// Server represents a running API server instance for testing
type Server struct {
	config     *ServerConfig
	baseURL    string
	ctx        context.Context
	cancel     context.CancelFunc
	serverErr  chan error
	done       chan struct{}
	httpClient *http.Client
}

// NewServer creates and starts a new API server instance
func NewServer(config *ServerConfig) (*Server, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Create a temporary listener to get a free port
	listener, err := net.Listen("tcp", config.Address)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}
	actualAddr := listener.Addr().String()
	// Close the listener immediately as the server will create its own
	_ = listener.Close()

	server := &Server{
		config:    config,
		baseURL:   fmt.Sprintf("http://%s", actualAddr),
		ctx:       ctx,
		cancel:    cancel,
		serverErr: make(chan error, 1),
		done:      make(chan struct{}),
		httpClient: &http.Client{
			Timeout: config.RequestTimeout,
		},
	}

	// Start the server in a goroutine
	go func() {
		defer close(server.done)
		// Create container runtime for the API server
		containerRuntime, err := container.NewFactory().Create(ctx)
		if err != nil {
			server.serverErr <- fmt.Errorf("failed to create container runtime: %w", err)
			return
		}

		builder := api.NewServerBuilder().
			WithAddress(actualAddr).
			WithUnixSocket(false).
			WithDebugMode(config.DebugMode).
			WithDocs(false).
			WithOIDCConfig(nil).
			WithContainerRuntime(containerRuntime)

		apiServer, err := api.NewServer(ctx, builder)
		if err != nil {
			server.serverErr <- fmt.Errorf("failed to create API server: %w", err)
			return
		}

		if err := apiServer.Start(ctx); err != nil {
			server.serverErr <- err
			return
		}
	}()

	// Wait for server to be ready
	if err := server.WaitForReady(); err != nil {
		server.Stop()
		return nil, err
	}

	return server, nil
}

// WaitForReady waits for the API server to be ready to accept requests
func (s *Server) WaitForReady() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.config.StartTimeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for API server to be ready")
		case err := <-s.serverErr:
			return fmt.Errorf("API server failed to start: %w", err)
		case <-ticker.C:
			// Try to connect to the health endpoint
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/health", nil)
			if err != nil {
				continue
			}

			resp, err := s.httpClient.Do(req)
			if err != nil {
				continue
			}
			_ = resp.Body.Close()

			// Server is ready if we get the expected response.
			if resp.StatusCode == http.StatusNoContent {
				return nil
			}
		}
	}
}

// Stop stops the API server
func (s *Server) Stop() {
	s.cancel()
	// Wait for server to shut down gracefully
	<-s.done
}

// Get performs a GET request to the specified path
func (s *Server) Get(path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return s.httpClient.Do(req)
}

// GetWithHeaders performs a GET request with custom headers
func (s *Server) GetWithHeaders(path string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	return s.httpClient.Do(req)
}

// BaseURL returns the base URL of the API server
func (s *Server) BaseURL() string {
	return s.baseURL
}

// StartServer is a helper function that creates and starts an API server
// and registers cleanup in the Ginkgo AfterEach
func StartServer(config *ServerConfig) *Server {
	server, err := NewServer(config)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Failed to start API server")

	// Register cleanup
	DeferCleanup(func() {
		server.Stop()
	})

	return server
}

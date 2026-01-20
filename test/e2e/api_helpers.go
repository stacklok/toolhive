// Package e2e provides end-to-end testing utilities for ToolHive HTTP API.
package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:staticcheck // Standard practice for Ginkgo
	. "github.com/onsi/gomega"    //nolint:staticcheck // Standard practice for Gomega

	"github.com/stacklok/toolhive/pkg/networking"
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

// Server represents a running API server instance for testing.
// It runs `thv serve` as a subprocess.
type Server struct {
	config     *ServerConfig
	baseURL    string
	cmd        *exec.Cmd
	ctx        context.Context
	cancel     context.CancelFunc
	httpClient *http.Client
	port       int
	stderr     *strings.Builder
	stdout     *strings.Builder
}

// NewServer creates and starts a new API server instance by running `thv serve` as a subprocess.
func NewServer(config *ServerConfig) (*Server, error) {
	testConfig := NewTestConfig()

	// Find a free port
	port, err := networking.FindOrUsePort(0)
	if err != nil {
		return nil, fmt.Errorf("failed to find free port: %w", err)
	}

	// Create temporary config directory (similar to CLI tests)
	tempXdgConfigHome := GinkgoT().TempDir()
	tempHome := GinkgoT().TempDir()

	ctx, cancel := context.WithCancel(context.Background())

	// Create string builders to capture output
	var stdout, stderr strings.Builder

	// Create the command: thv serve --host 127.0.0.1 --port <port>
	//nolint:gosec // Intentional for e2e testing
	cmd := exec.CommandContext(
		ctx,
		testConfig.THVBinary,
		"serve",
		"--host",
		"127.0.0.1",
		"--port",
		strconv.Itoa(port),
	)
	// Set environment variables including temporary config paths
	cmd.Env = append([]string{
		"TOOLHIVE_DEV=true",
		fmt.Sprintf("XDG_CONFIG_HOME=%s", tempXdgConfigHome),
		fmt.Sprintf("HOME=%s", tempHome),
	}, cmd.Env...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Start the server process
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start thv serve: %w", err)
	}

	server := &Server{
		config:  config,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		cmd:     cmd,
		ctx:     ctx,
		cancel:  cancel,
		httpClient: &http.Client{
			Timeout: config.RequestTimeout,
		},
		port:   port,
		stdout: &stdout,
		stderr: &stderr,
	}

	// Wait for server to be ready
	if err := server.WaitForReady(); err != nil {
		_ = server.Stop()
		return nil, err
	}

	return server, nil
}

// WaitForReady waits for the API server to be ready to accept requests.
func (s *Server) WaitForReady() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.config.StartTimeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Include server logs in the error message for debugging
			return fmt.Errorf("timeout waiting for API server to be ready on port %d.\nStdout: %s\nStderr: %s",
				s.port, s.stdout.String(), s.stderr.String())
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

			// Server is ready if we get the expected response
			if resp.StatusCode == http.StatusNoContent {
				return nil
			}
		}
	}
}

// Stop stops the API server subprocess.
func (s *Server) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}

	if s.cmd != nil && s.cmd.Process != nil {
		// Wait for the process to exit
		_ = s.cmd.Wait()
	}

	return nil
}

// Get performs a GET request to the specified path.
func (s *Server) Get(path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return s.httpClient.Do(req)
}

// GetWithHeaders performs a GET request with custom headers.
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

// BaseURL returns the base URL of the API server.
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
		_ = server.Stop()
	})

	return server
}

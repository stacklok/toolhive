// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Health Check Zombie Process Prevention", Label("stability", "healthcheck", "zombie", "e2e"), Serial, func() {
	var (
		config     *e2e.TestConfig
		serverName string
		mockServer *controllableMockServer
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = generateHealthCheckTestServerName("hc-zombie")

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		// Stop the mock server if it's running
		if mockServer != nil {
			mockServer.Stop()
			mockServer = nil
		}

		if config.CleanupAfter {
			// Clean up the server if it exists
			err := e2e.StopAndRemoveMCPServer(config, serverName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
		}
	})

	Describe("Transport detection of health check failure", func() {
		Context("when a remote server's health checks fail", func() {
			It("should detect the failure and attempt restart instead of becoming a zombie", func() {
				By("Starting a controllable mock HTTP server")
				var err error
				mockServer, err = newControllableMockServer()
				Expect(err).ToNot(HaveOccurred(), "Should be able to start mock server")

				mockServerURL := mockServer.URL()
				GinkgoWriter.Printf("Mock server started at: %s\n", mockServerURL)

				By("Starting thv as a remote server with health checks enabled and fast interval")
				// Use 1s health check interval for faster test execution
				thvCmd := exec.Command(config.THVBinary, "run",
					"--name", serverName,
					mockServerURL+"/mcp")
				thvCmd.Env = append(os.Environ(),
					"TOOLHIVE_REMOTE_HEALTHCHECKS=true",
					"TOOLHIVE_HEALTH_CHECK_INTERVAL=1s",
				)
				thvCmd.Stdout = GinkgoWriter
				thvCmd.Stderr = GinkgoWriter

				err = thvCmd.Start()
				Expect(err).ToNot(HaveOccurred(), "Should be able to start thv")

				thvPID := thvCmd.Process.Pid
				GinkgoWriter.Printf("thv process started with PID: %d\n", thvPID)

				// Ensure cleanup on test failure
				defer func() {
					if proc, err := os.FindProcess(thvPID); err == nil {
						_ = proc.Kill()
					}
				}()

				By("Waiting for thv to register as running")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Getting the proxy port from thv list")
				proxyPort := getServerPort(config, serverName)
				Expect(proxyPort).ToNot(BeZero(), "Should be able to get proxy port")
				GinkgoWriter.Printf("Proxy listening on port: %d\n", proxyPort)

				By("Sending a request through the proxy to initialize it")
				// This triggers serverInitialized() so health checks will run
				proxyURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", proxyPort)
				resp, err := http.Get(proxyURL)
				Expect(err).ToNot(HaveOccurred(), "Should be able to connect to proxy")
				resp.Body.Close()
				GinkgoWriter.Printf("Proxy initialized (got response status: %d)\n", resp.StatusCode)

				By("Verifying the server is running initially")
				status := getServerStatus(config, serverName)
				Expect(status).To(Equal("running"), "Server should be in running state")

				By("Making the mock server return 500 errors to fail health checks")
				mockServer.SetHealthy(false)
				GinkgoWriter.Printf("Mock server now returning 500 errors\n")

				By("Waiting for health checks to fail and status to change")
				// With 1s health check interval and 3 failures required + 5s retry delays:
				// Worst case: 3 intervals + 2 retries * 5s = 3s + 10s = 13s
				// We poll for up to 30s to be safe
				var finalStatus string
				deadline := time.Now().Add(30 * time.Second)

				for time.Now().Before(deadline) {
					finalStatus = getServerStatus(config, serverName)
					GinkgoWriter.Printf("Current status of %s: %s\n", serverName, finalStatus)

					if finalStatus != "running" {
						GinkgoWriter.Printf("Status changed from 'running' to '%s'\n", finalStatus)
						break
					}

					time.Sleep(1 * time.Second)
				}

				Expect(finalStatus).ToNot(Equal("running"),
					"Server status should change from 'running' after health check failures")

				By("Verifying the server is still tracked (not a zombie)")
				// The server should still be listed, indicating the runner detected
				// the failure and is handling it (not hanging as a zombie)
				stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName),
					"Server should still be tracked in the system")
			})
		})
	})
})

// controllableMockServer is a simple HTTP server that can switch between healthy and unhealthy states
type controllableMockServer struct {
	server   *http.Server
	listener net.Listener
	port     int
	healthy  atomic.Bool
}

// newControllableMockServer creates and starts a new controllable mock server
func newControllableMockServer() (*controllableMockServer, error) {
	// Find an available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	mock := &controllableMockServer{
		listener: listener,
		port:     port,
	}
	mock.healthy.Store(true) // Start healthy

	// Use a custom handler that:
	// 1. Returns 404 for OAuth well-known URIs (prevents OAuth discovery)
	// 2. Returns 500 for ALL other paths when unhealthy (triggers health check failure)
	// 3. Returns appropriate responses when healthy
	mock.server = &http.Server{
		Handler: http.HandlerFunc(mock.handleRequest),
	}

	// Start serving in background
	go func() {
		if err := mock.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			GinkgoWriter.Printf("Mock server error: %v\n", err)
		}
	}()

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	return mock, nil
}

// handleRequest handles all HTTP requests to the mock server
func (m *controllableMockServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	// Always return 404 for OAuth well-known URIs to prevent OAuth discovery
	// from triggering authentication flows. These paths are checked before
	// the healthy/unhealthy logic.
	if strings.HasPrefix(r.URL.Path, "/.well-known/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if !m.healthy.Load() {
		// Return 500 to fail health checks (on any path including root "/")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Return a response with Mcp-Session-Id header to trigger server initialization
	// This is needed for health checks to start running
	w.Header().Set("Mcp-Session-Id", "test-session-123")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{}}`))
}

// SetHealthy sets whether the mock server should return healthy or unhealthy responses
func (m *controllableMockServer) SetHealthy(healthy bool) {
	m.healthy.Store(healthy)
}

// URL returns the base URL of the mock server
func (m *controllableMockServer) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", m.port)
}

// Stop stops the mock server
func (m *controllableMockServer) Stop() {
	if m.server != nil {
		_ = m.server.Close()
	}
}

// generateHealthCheckTestServerName creates a unique server name for health check tests
func generateHealthCheckTestServerName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, GinkgoRandomSeed())
}

// getServerStatus returns the status of a specific server from thv list output
func getServerStatus(config *e2e.TestConfig, serverName string) string {
	stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()

	// Parse the output line by line to find the specific server
	lines := strings.Split(stdout, "\n")
	for _, line := range lines {
		// Skip empty lines and header
		if line == "" || strings.HasPrefix(line, "NAME") || strings.HasPrefix(line, "A new") || strings.HasPrefix(line, "Currently") {
			continue
		}

		// Check if this line is for our server (server name should be at the start)
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == serverName {
			// Status is typically the 3rd field (after NAME and PACKAGE/remote)
			// But for remote servers, PACKAGE might be "remote" which shifts things
			// Look for known status values in the fields
			for _, field := range fields {
				switch field {
				case "running", "starting", "unhealthy", "stopped", "error":
					return field
				}
			}
		}
	}

	return ""
}

// getServerPort returns the port of a specific server from thv list output
func getServerPort(config *e2e.TestConfig, serverName string) int {
	stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()

	lines := strings.Split(stdout, "\n")
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "NAME") || strings.HasPrefix(line, "A new") || strings.HasPrefix(line, "Currently") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) >= 1 && fields[0] == serverName {
			// Look for a field that looks like a port number (all digits, reasonable range)
			for _, field := range fields {
				var port int
				if _, err := fmt.Sscanf(field, "%d", &port); err == nil {
					if port > 1024 && port < 65536 {
						return port
					}
				}
			}
		}
	}

	return 0
}

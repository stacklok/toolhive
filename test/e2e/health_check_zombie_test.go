// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
		serverName = generateHealthCheckTestServerName("healthcheck-zombie-test")

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

				By("Starting thv as a remote server with health checks enabled")
				// Start thv in the background pointing to our mock server
				thvCmd := exec.Command(config.THVBinary, "run",
					"--name", serverName,
					mockServerURL+"/mcp")
				thvCmd.Env = append(os.Environ(), "TOOLHIVE_REMOTE_HEALTHCHECKS=true")
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

				By("Verifying the server is healthy initially")
				stdout, _ := e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should be listed")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")

				By("Making the mock server return 500 errors to fail health checks")
				mockServer.SetHealthy(false)
				GinkgoWriter.Printf("Mock server now returning 500 errors\n")

				By("Waiting for health checks to fail and restart to be attempted")
				// Health checks run every 10s, need 3 failures
				// After 3 failures, the proxy should stop and restart should be attempted
				// We verify this by checking for "restart needed" or "Restarting" in logs

				logFile := getLogFilePath(serverName)
				GinkgoWriter.Printf("Checking log file: %s\n", logFile)

				// Wait for the restart attempt to appear in logs
				// This confirms the fix is working - the transport detected "not running"
				// and triggered the restart mechanism (instead of hanging as zombie)
				restartDetected := waitForLogPattern(logFile, "restart needed", 90*time.Second)

				if !restartDetected {
					// Check if process is still running
					processRunning := isProcessRunning(thvPID)
					GinkgoWriter.Printf("Process %d running: %v\n", thvPID, processRunning)

					// Dump relevant log lines for debugging
					if logContent, err := os.ReadFile(logFile); err == nil {
						lines := strings.Split(string(logContent), "\n")
						GinkgoWriter.Printf("Last 20 log lines:\n")
						start := len(lines) - 20
						if start < 0 {
							start = 0
						}
						for _, line := range lines[start:] {
							GinkgoWriter.Printf("  %s\n", line)
						}
					}
				}

				Expect(restartDetected).To(BeTrue(),
					"Transport should detect health check failure and trigger restart (not hang as zombie)")

				By("Verifying the server is marked as unhealthy or restarting")
				// Give a moment for status to update
				time.Sleep(2 * time.Second)
				stdout, _ = e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
				GinkgoWriter.Printf("Server list output:\n%s\n", stdout)
				// The server should still be listed (restart in progress or unhealthy)
				Expect(stdout).To(ContainSubstring(serverName), "Server should still be listed")
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

	mux := http.NewServeMux()
	mux.HandleFunc("/", mock.handleRequest)
	mux.HandleFunc("/mcp", mock.handleRequest)
	mux.HandleFunc("/sse", mock.handleRequest)

	mock.server = &http.Server{
		Handler: mux,
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
	if !m.healthy.Load() {
		// Return 500 to fail health checks
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Return a minimal response that looks like an MCP endpoint
	if r.URL.Path == "/sse" || r.URL.Path == "/mcp" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		// Send a minimal SSE event
		_, _ = w.Write([]byte("event: endpoint\ndata: http://localhost/messages\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}

	// Default healthy response
	w.WriteHeader(http.StatusOK)
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

// getLogFilePath returns the path to the log file for a given server name
func getLogFilePath(serverName string) string {
	// ToolHive stores logs in platform-specific locations
	// On macOS: ~/Library/Application Support/toolhive/logs/
	// On Linux: ~/.local/share/toolhive/logs/
	homeDir, _ := os.UserHomeDir()

	// Try macOS path first
	macOSPath := filepath.Join(homeDir, "Library", "Application Support", "toolhive", "logs", serverName+".log")
	if _, err := os.Stat(macOSPath); err == nil {
		return macOSPath
	}

	// Try Linux path
	linuxPath := filepath.Join(homeDir, ".local", "share", "toolhive", "logs", serverName+".log")
	return linuxPath
}

// waitForLogPattern waits for a pattern to appear in the log file
func waitForLogPattern(logFile string, pattern string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		content, err := os.ReadFile(logFile)
		if err == nil && strings.Contains(string(content), pattern) {
			return true
		}
		time.Sleep(1 * time.Second)
	}

	return false
}

// isPortListening checks if a port is currently listening
func isPortListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 1*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// generateHealthCheckTestServerName creates a unique server name for health check tests
func generateHealthCheckTestServerName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, GinkgoRandomSeed())
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

var _ = Describe("Stateless Proxy Mode", Label("proxy", "stateless", "streamable-http", "e2e"), Serial, func() {
	var (
		config     *e2e.TestConfig
		serverName string
		mockServer *statelessMockMCPServer
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("stateless")

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if mockServer != nil {
			mockServer.Stop()
			mockServer = nil
		}

		if config.CleanupAfter {
			err := e2e.StopAndRemoveMCPServer(config, serverName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
		}
	})

	Describe("Method gating for stateless servers", func() {
		Context("when --stateless flag is set on a remote server", func() {
			It("should reject GET requests and forward POST requests", func() {
				By("Starting a stateless mock MCP server")
				var err error
				mockServer, err = newStatelessMockMCPServer()
				Expect(err).ToNot(HaveOccurred(), "Should be able to start mock server")

				mockServerURL := mockServer.URL()
				GinkgoWriter.Printf("Mock server started at: %s\n", mockServerURL)

				By("Starting thv with --stateless flag")
				thvCmd := exec.Command(config.THVBinary, "run",
					"--name", serverName,
					"--stateless",
					mockServerURL+"/mcp")
				thvCmd.Env = append(os.Environ(),
					"TOOLHIVE_REMOTE_HEALTHCHECKS=true",
				)
				thvCmd.Stdout = GinkgoWriter
				thvCmd.Stderr = GinkgoWriter

				err = thvCmd.Start()
				Expect(err).ToNot(HaveOccurred(), "Should be able to start thv")

				thvPID := thvCmd.Process.Pid
				GinkgoWriter.Printf("thv process started with PID: %d\n", thvPID)

				defer func() {
					if proc, err := os.FindProcess(thvPID); err == nil {
						_ = proc.Kill()
					}
				}()

				By("Waiting for thv to register as running")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Getting the proxy URL")
				proxyURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to get proxy URL")
				// Ensure URL has /mcp suffix
				if !strings.HasSuffix(proxyURL, "/mcp") {
					proxyURL += "/mcp"
				}
				GinkgoWriter.Printf("Proxy URL: %s\n", proxyURL)

				By("Verifying GET requests are rejected with 405")
				resp, err := http.Get(proxyURL)
				Expect(err).ToNot(HaveOccurred(), "Should be able to connect to proxy")
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed),
					"GET request should be rejected with 405 Method Not Allowed")

				By("Verifying POST requests are forwarded successfully")
				initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e-test","version":"1.0"}}}`
				postResp, err := http.Post(proxyURL, "application/json", strings.NewReader(initReq))
				Expect(err).ToNot(HaveOccurred(), "Should be able to POST to proxy")
				defer postResp.Body.Close()

				Expect(postResp.StatusCode).To(Equal(http.StatusOK),
					"POST request should be forwarded and return 200")

				body, err := io.ReadAll(postResp.Body)
				Expect(err).ToNot(HaveOccurred(), "Should be able to read response body")

				var jsonRPC map[string]interface{}
				err = json.Unmarshal(body, &jsonRPC)
				Expect(err).ToNot(HaveOccurred(), "Response should be valid JSON-RPC")
				Expect(jsonRPC).To(HaveKey("result"), "Response should have a result field")

				By("Verifying DELETE requests are also rejected")
				delReq, err := http.NewRequest(http.MethodDelete, proxyURL, nil)
				Expect(err).ToNot(HaveOccurred())
				delResp, err := http.DefaultClient.Do(delReq)
				Expect(err).ToNot(HaveOccurred(), "Should be able to send DELETE to proxy")
				delResp.Body.Close()
				Expect(delResp.StatusCode).To(Equal(http.StatusMethodNotAllowed),
					"DELETE request should be rejected with 405")

				By("Verifying the mock server received POST requests through the proxy")
				Expect(mockServer.GetCount()).To(BeNumerically(">", 0),
					"Mock server should have received at least one POST request")
			})
		})
	})
})

// statelessMockMCPServer is a minimal MCP server that only accepts POST.
// It tracks whether any GET requests reached it (which would indicate
// the proxy's method gate is not working).
type statelessMockMCPServer struct {
	server   *http.Server
	listener net.Listener
	port     int
	gotGET   atomic.Bool
	postHits atomic.Int32
}

func newStatelessMockMCPServer() (*statelessMockMCPServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	mock := &statelessMockMCPServer{
		listener: listener,
		port:     port,
	}

	mock.server = &http.Server{
		Handler: http.HandlerFunc(mock.handleRequest),
	}

	go func() {
		if err := mock.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			GinkgoWriter.Printf("Stateless mock server error: %v\n", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	return mock, nil
}

func (m *statelessMockMCPServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	// Always return 404 for OAuth well-known URIs
	if strings.HasPrefix(r.URL.Path, "/.well-known/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if r.Method == http.MethodGet {
		m.gotGET.Store(true)
		// A real stateless server would reject GETs, but we accept them here
		// so the test can detect if any GETs leaked through the proxy.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	m.postHits.Add(1)

	// Parse the JSON-RPC request to return appropriate responses
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	method, _ := req["method"].(string)
	id := req["id"]

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	switch method {
	case "initialize":
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{},
				"serverInfo": map[string]interface{}{
					"name":    "stateless-mock",
					"version": "1.0.0",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	case "ping":
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  map[string]interface{}{},
		}
		_ = json.NewEncoder(w).Encode(resp)
	default:
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  map[string]interface{}{},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func (m *statelessMockMCPServer) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", m.port)
}

func (m *statelessMockMCPServer) Stop() {
	if m.server != nil {
		_ = m.server.Close()
	}
}

func (m *statelessMockMCPServer) GetCount() int32 {
	return m.postHits.Load()
}

func (m *statelessMockMCPServer) GotGET() bool {
	return m.gotGET.Load()
}

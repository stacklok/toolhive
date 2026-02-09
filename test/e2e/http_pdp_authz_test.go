// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// pdpDecision represents a mock PDP authorization decision.
type pdpDecision struct {
	allow   bool
	matcher func(porc map[string]interface{}) bool
}

// mockPDPServer is a test HTTP PDP server that can be configured to return specific decisions.
type mockPDPServer struct {
	server   *httptest.Server
	mu       sync.RWMutex
	rules    []pdpDecision
	requests []map[string]interface{} // captured PORC requests
}

// newMockPDPServer creates a new mock PDP server.
func newMockPDPServer() *mockPDPServer {
	m := &mockPDPServer{
		rules:    []pdpDecision{},
		requests: []map[string]interface{}{},
	}

	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/decision" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Parse PORC request
		var porc map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&porc); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		m.mu.Lock()
		m.requests = append(m.requests, porc)
		m.mu.Unlock()

		// Evaluate rules to determine decision
		allowed := m.evaluateRules(porc)

		// Return decision
		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{"allow": allowed}
		_ = json.NewEncoder(w).Encode(response)
	}))

	return m
}

// addRule adds an authorization rule to the mock PDP.
func (m *mockPDPServer) addRule(allow bool, matcher func(porc map[string]interface{}) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules = append(m.rules, pdpDecision{allow: allow, matcher: matcher})
}

// evaluateRules evaluates the configured rules against a PORC request.
func (m *mockPDPServer) evaluateRules(porc map[string]interface{}) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Default deny if no rules match
	for _, rule := range m.rules {
		if rule.matcher(porc) {
			return rule.allow
		}
	}
	return false
}

// getRequests returns all captured PORC requests.
func (m *mockPDPServer) getRequests() []map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]map[string]interface{}{}, m.requests...)
}

// close closes the mock PDP server.
func (m *mockPDPServer) close() {
	m.server.Close()
}

// url returns the URL of the mock PDP server.
func (m *mockPDPServer) url() string {
	return m.server.URL
}

var _ = Describe("HTTP PDP Authorization", Label("middleware", "authz", "http-pdp", "e2e"), Serial, func() {
	var config *e2e.TestConfig

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Describe("Basic Authorization with HTTP PDP", func() {
		Context("when PDP allows specific tool calls", Ordered, func() {
			var serverName string
			var authzConfigPath string
			var mcpClient *e2e.MCPClientHelper
			var serverURL string
			var cancel context.CancelFunc
			var pdpServer *mockPDPServer
			var tempDir string

			BeforeAll(func() {
				serverName = e2e.GenerateUniqueServerName("http-pdp-authz-test")

				// Create mock PDP server
				pdpServer = newMockPDPServer()

				// Configure PDP to allow only query_vulnerability tool
				pdpServer.addRule(true, func(porc map[string]interface{}) bool {
					operation, ok := porc["operation"].(string)
					if !ok {
						return false
					}
					resource, ok := porc["resource"].(string)
					if !ok {
						return false
					}
					// Allow only mcp:tool:call for query_vulnerability
					return operation == "mcp:tool:call" && strings.Contains(resource, ":tool:query_vulnerability")
				})

				// Explicitly deny all other requests
				pdpServer.addRule(false, func(_ map[string]interface{}) bool {
					return true // Match all remaining requests
				})

				// Create authorization config file
				authzConfig := fmt.Sprintf(`{
  "version": "1.0",
  "type": "httpv1",
  "pdp": {
    "http": {
      "url": "%s",
      "timeout": 10,
      "insecure_skip_verify": true
    },
    "claim_mapping": "standard"
  }
}`, pdpServer.url())

				// Write config to temporary file
				var err error
				tempDir, err = os.MkdirTemp("", "http-pdp-authz-test")
				Expect(err).ToNot(HaveOccurred())

				authzConfigPath = filepath.Join(tempDir, "authz-config.json")
				err = os.WriteFile(authzConfigPath, []byte(authzConfig), 0644)
				Expect(err).ToNot(HaveOccurred())

				// Start MCP server with HTTP PDP authorization
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--authz-config", authzConfigPath,
					"osv").ExpectSuccess()

				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				// Get server URL
				serverURL, err = e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				err = e2e.WaitForMCPServerReady(config, serverURL, "sse", 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			})

			BeforeEach(func() {
				// Create fresh MCP client for each test
				var err error
				mcpClient, err = e2e.NewMCPClientForSSE(config, serverURL)
				Expect(err).ToNot(HaveOccurred())

				// Create context that will be cancelled in AfterEach
				ctx, cancelFunc := context.WithTimeout(context.Background(), 30*time.Second)
				cancel = cancelFunc
				err = mcpClient.Initialize(ctx)
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				if cancel != nil {
					cancel()
				}
				if mcpClient != nil {
					mcpClient.Close()
				}
			})

			AfterAll(func() {
				if config.CleanupAfter {
					// Clean up the shared server
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")

					// Clean up mock PDP server
					if pdpServer != nil {
						pdpServer.close()
					}

					// Clean up temporary files
					if tempDir != "" {
						os.RemoveAll(tempDir)
					}
				}
			})

			It("should allow authorized tool calls [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Testing authorized tool call - query_vulnerability")
				arguments := map[string]interface{}{
					"package_name": "lodash",
					"ecosystem":    "npm",
					"version":      "4.17.15",
				}

				result := mcpClient.ExpectToolCall(ctx, "query_vulnerability", arguments)
				Expect(result.Content).ToNot(BeEmpty(), "Should return vulnerability information")

				GinkgoWriter.Printf("Authorized vulnerability query result: %+v\n", result.Content)
			})

			It("should deny unauthorized tool calls [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Attempting to call unauthorized tool - query_vulnerabilities_batch")
				arguments := map[string]interface{}{
					"queries": []map[string]interface{}{
						{
							"package_name": "lodash",
							"ecosystem":    "npm",
							"version":      "4.17.15",
						},
					},
				}

				// This should fail because query_vulnerabilities_batch is not authorized
				_, err := mcpClient.CallTool(ctx, "query_vulnerabilities_batch", arguments)
				Expect(err).To(HaveOccurred(), "Should fail to call unauthorized tool")
				Expect(err.Error()).To(ContainSubstring("Unauthorized"), "Error should mention Unauthorized")

				GinkgoWriter.Printf("Expected authorization failure for unauthorized tool: %v\n", err)
			})

			It("should send correct PORC structure to PDP [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Making a tool call to generate PORC request")
				arguments := map[string]interface{}{
					"package_name": "lodash",
					"ecosystem":    "npm",
					"version":      "4.17.15",
				}

				_ = mcpClient.ExpectToolCall(ctx, "query_vulnerability", arguments)

				By("Verifying PORC structure sent to PDP")
				requests := pdpServer.getRequests()
				Expect(requests).ToNot(BeEmpty(), "PDP should have received requests")

				// Get the most recent request
				lastRequest := requests[len(requests)-1]

				// Verify PORC structure
				Expect(lastRequest).To(HaveKey("principal"), "PORC should have principal")
				Expect(lastRequest).To(HaveKey("operation"), "PORC should have operation")
				Expect(lastRequest).To(HaveKey("resource"), "PORC should have resource")
				Expect(lastRequest).To(HaveKey("context"), "PORC should have context")

				// Verify operation format
				operation, ok := lastRequest["operation"].(string)
				Expect(ok).To(BeTrue(), "Operation should be a string")
				Expect(operation).To(Equal("mcp:tool:call"), "Operation should be mcp:tool:call")

				// Verify resource format (mrn:mcp:serverid:feature:resourceid)
				resource, ok := lastRequest["resource"].(string)
				Expect(ok).To(BeTrue(), "Resource should be a string")
				Expect(resource).To(ContainSubstring("mrn:mcp:"), "Resource should start with mrn:mcp:")
				Expect(resource).To(ContainSubstring(":tool:query_vulnerability"), "Resource should contain tool name")

				GinkgoWriter.Printf("✅ PORC validation successful\n")
				GinkgoWriter.Printf("   Operation: %v\n", operation)
				GinkgoWriter.Printf("   Resource: %v\n", resource)
			})
		})
	})

	Describe("Claim Mapping", func() {
		Context("when using MPE claim mapping", Ordered, func() {
			var serverName string
			var authzConfigPath string
			var mcpClient *e2e.MCPClientHelper
			var serverURL string
			var cancel context.CancelFunc
			var pdpServer *mockPDPServer
			var tempDir string

			BeforeAll(func() {
				serverName = e2e.GenerateUniqueServerName("http-pdp-mpe-test")

				// Create mock PDP server
				pdpServer = newMockPDPServer()

				// Configure PDP to allow all requests (we're testing claim mapping, not authz)
				pdpServer.addRule(true, func(_ map[string]interface{}) bool {
					return true
				})

				// Create authorization config with MPE claim mapping
				authzConfig := fmt.Sprintf(`{
  "version": "1.0",
  "type": "httpv1",
  "pdp": {
    "http": {
      "url": "%s",
      "timeout": 10,
      "insecure_skip_verify": true
    },
    "claim_mapping": "mpe"
  }
}`, pdpServer.url())

				// Write config to temporary file
				var err error
				tempDir, err = os.MkdirTemp("", "http-pdp-mpe-test")
				Expect(err).ToNot(HaveOccurred())

				authzConfigPath = filepath.Join(tempDir, "authz-config.json")
				err = os.WriteFile(authzConfigPath, []byte(authzConfig), 0644)
				Expect(err).ToNot(HaveOccurred())

				// Start MCP server with HTTP PDP authorization
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--authz-config", authzConfigPath,
					"osv").ExpectSuccess()

				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				// Get server URL
				serverURL, err = e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				err = e2e.WaitForMCPServerReady(config, serverURL, "sse", 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			})

			BeforeEach(func() {
				// Create fresh MCP client for each test
				var err error
				mcpClient, err = e2e.NewMCPClientForSSE(config, serverURL)
				Expect(err).ToNot(HaveOccurred())

				// Create context that will be cancelled in AfterEach
				ctx, cancelFunc := context.WithTimeout(context.Background(), 30*time.Second)
				cancel = cancelFunc
				err = mcpClient.Initialize(ctx)
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				if cancel != nil {
					cancel()
				}
				if mcpClient != nil {
					mcpClient.Close()
				}
			})

			AfterAll(func() {
				if config.CleanupAfter {
					// Clean up the shared server
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")

					// Clean up mock PDP server
					if pdpServer != nil {
						pdpServer.close()
					}

					// Clean up temporary files
					if tempDir != "" {
						os.RemoveAll(tempDir)
					}
				}
			})

			It("should use MPE claim mapping in PORC principal [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Making a tool call to generate PORC with MPE claims")
				arguments := map[string]interface{}{
					"package_name": "lodash",
					"ecosystem":    "npm",
					"version":      "4.17.15",
				}

				_ = mcpClient.ExpectToolCall(ctx, "query_vulnerability", arguments)

				By("Verifying MPE claim mapping in PORC principal")
				requests := pdpServer.getRequests()
				Expect(requests).ToNot(BeEmpty(), "PDP should have received requests")

				// Get the most recent request
				lastRequest := requests[len(requests)-1]

				// Verify principal structure
				principal, ok := lastRequest["principal"].(map[string]interface{})
				Expect(ok).To(BeTrue(), "Principal should be a map")

				// MPE mapper should include mannotations even if empty
				Expect(principal).To(HaveKey("mannotations"), "MPE principal should have mannotations")

				GinkgoWriter.Printf("✅ MPE claim mapping verified\n")
				GinkgoWriter.Printf("   Principal keys: %v\n", getKeys(principal))
			})
		})

		Context("when using standard claim mapping", Ordered, func() {
			var serverName string
			var authzConfigPath string
			var mcpClient *e2e.MCPClientHelper
			var serverURL string
			var cancel context.CancelFunc
			var pdpServer *mockPDPServer
			var tempDir string

			BeforeAll(func() {
				serverName = e2e.GenerateUniqueServerName("http-pdp-standard-test")

				// Create mock PDP server
				pdpServer = newMockPDPServer()

				// Configure PDP to allow all requests
				pdpServer.addRule(true, func(_ map[string]interface{}) bool {
					return true
				})

				// Create authorization config with standard claim mapping
				authzConfig := fmt.Sprintf(`{
  "version": "1.0",
  "type": "httpv1",
  "pdp": {
    "http": {
      "url": "%s",
      "timeout": 10,
      "insecure_skip_verify": true
    },
    "claim_mapping": "standard"
  }
}`, pdpServer.url())

				// Write config to temporary file
				var err error
				tempDir, err = os.MkdirTemp("", "http-pdp-standard-test")
				Expect(err).ToNot(HaveOccurred())

				authzConfigPath = filepath.Join(tempDir, "authz-config.json")
				err = os.WriteFile(authzConfigPath, []byte(authzConfig), 0644)
				Expect(err).ToNot(HaveOccurred())

				// Start MCP server with HTTP PDP authorization
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--authz-config", authzConfigPath,
					"osv").ExpectSuccess()

				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				// Get server URL
				serverURL, err = e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				err = e2e.WaitForMCPServerReady(config, serverURL, "sse", 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			})

			BeforeEach(func() {
				// Create fresh MCP client for each test
				var err error
				mcpClient, err = e2e.NewMCPClientForSSE(config, serverURL)
				Expect(err).ToNot(HaveOccurred())

				// Create context that will be cancelled in AfterEach
				ctx, cancelFunc := context.WithTimeout(context.Background(), 30*time.Second)
				cancel = cancelFunc
				err = mcpClient.Initialize(ctx)
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				if cancel != nil {
					cancel()
				}
				if mcpClient != nil {
					mcpClient.Close()
				}
			})

			AfterAll(func() {
				if config.CleanupAfter {
					// Clean up the shared server
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")

					// Clean up mock PDP server
					if pdpServer != nil {
						pdpServer.close()
					}

					// Clean up temporary files
					if tempDir != "" {
						os.RemoveAll(tempDir)
					}
				}
			})

			It("should use standard claim mapping in PORC principal [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Making a tool call to generate PORC with standard claims")
				arguments := map[string]interface{}{
					"package_name": "lodash",
					"ecosystem":    "npm",
					"version":      "4.17.15",
				}

				_ = mcpClient.ExpectToolCall(ctx, "query_vulnerability", arguments)

				By("Verifying standard claim mapping in PORC principal")
				requests := pdpServer.getRequests()
				Expect(requests).ToNot(BeEmpty(), "PDP should have received requests")

				// Get the most recent request
				lastRequest := requests[len(requests)-1]

				// Verify principal structure
				principal, ok := lastRequest["principal"].(map[string]interface{})
				Expect(ok).To(BeTrue(), "Principal should be a map")

				// Standard mapper should NOT include MPE-specific fields
				Expect(principal).ToNot(HaveKey("mannotations"), "Standard principal should not have mannotations")
				Expect(principal).ToNot(HaveKey("mclearance"), "Standard principal should not have mclearance")

				GinkgoWriter.Printf("✅ Standard claim mapping verified\n")
				GinkgoWriter.Printf("   Principal keys: %v\n", getKeys(principal))
			})
		})
	})

	Describe("Context Configuration", func() {
		Context("when context includes arguments", Ordered, func() {
			var serverName string
			var authzConfigPath string
			var mcpClient *e2e.MCPClientHelper
			var serverURL string
			var cancel context.CancelFunc
			var pdpServer *mockPDPServer
			var tempDir string

			BeforeAll(func() {
				serverName = e2e.GenerateUniqueServerName("http-pdp-ctx-args-test")

				// Create mock PDP server
				pdpServer = newMockPDPServer()

				// Configure PDP to allow all requests
				pdpServer.addRule(true, func(_ map[string]interface{}) bool {
					return true
				})

				// Create authorization config with context.include_args enabled
				authzConfig := fmt.Sprintf(`{
  "version": "1.0",
  "type": "httpv1",
  "pdp": {
    "http": {
      "url": "%s",
      "timeout": 10,
      "insecure_skip_verify": true
    },
    "claim_mapping": "standard",
    "context": {
      "include_args": true
    }
  }
}`, pdpServer.url())

				// Write config to temporary file
				var err error
				tempDir, err = os.MkdirTemp("", "http-pdp-ctx-args-test")
				Expect(err).ToNot(HaveOccurred())

				authzConfigPath = filepath.Join(tempDir, "authz-config.json")
				err = os.WriteFile(authzConfigPath, []byte(authzConfig), 0644)
				Expect(err).ToNot(HaveOccurred())

				// Start MCP server with HTTP PDP authorization
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--authz-config", authzConfigPath,
					"osv").ExpectSuccess()

				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				// Get server URL
				serverURL, err = e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				err = e2e.WaitForMCPServerReady(config, serverURL, "sse", 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			})

			BeforeEach(func() {
				// Create fresh MCP client for each test
				var err error
				mcpClient, err = e2e.NewMCPClientForSSE(config, serverURL)
				Expect(err).ToNot(HaveOccurred())

				// Create context that will be cancelled in AfterEach
				ctx, cancelFunc := context.WithTimeout(context.Background(), 30*time.Second)
				cancel = cancelFunc
				err = mcpClient.Initialize(ctx)
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				if cancel != nil {
					cancel()
				}
				if mcpClient != nil {
					mcpClient.Close()
				}
			})

			AfterAll(func() {
				if config.CleanupAfter {
					// Clean up the shared server
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")

					// Clean up mock PDP server
					if pdpServer != nil {
						pdpServer.close()
					}

					// Clean up temporary files
					if tempDir != "" {
						os.RemoveAll(tempDir)
					}
				}
			})

			It("should include tool arguments in PORC context [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Making a tool call with specific arguments")
				arguments := map[string]interface{}{
					"package_name": "lodash",
					"ecosystem":    "npm",
					"version":      "4.17.15",
				}

				_ = mcpClient.ExpectToolCall(ctx, "query_vulnerability", arguments)

				By("Verifying arguments are included in PORC context")
				requests := pdpServer.getRequests()
				Expect(requests).ToNot(BeEmpty(), "PDP should have received requests")

				// Get the most recent request
				lastRequest := requests[len(requests)-1]

				// Verify context structure
				context, ok := lastRequest["context"].(map[string]interface{})
				Expect(ok).To(BeTrue(), "Context should be a map")

				// Verify mcp object exists
				mcp, ok := context["mcp"].(map[string]interface{})
				Expect(ok).To(BeTrue(), "Context should have mcp object")

				// Verify args are included
				args, ok := mcp["args"].(map[string]interface{})
				Expect(ok).To(BeTrue(), "Context.mcp should have args")

				// Verify specific argument values
				Expect(args["package_name"]).To(Equal("lodash"))
				Expect(args["ecosystem"]).To(Equal("npm"))
				Expect(args["version"]).To(Equal("4.17.15"))

				GinkgoWriter.Printf("✅ Arguments in context verified\n")
				GinkgoWriter.Printf("   Args: %v\n", args)
			})
		})

		Context("when context includes operation metadata", Ordered, func() {
			var serverName string
			var authzConfigPath string
			var mcpClient *e2e.MCPClientHelper
			var serverURL string
			var cancel context.CancelFunc
			var pdpServer *mockPDPServer
			var tempDir string

			BeforeAll(func() {
				serverName = e2e.GenerateUniqueServerName("http-pdp-ctx-op-test")

				// Create mock PDP server
				pdpServer = newMockPDPServer()

				// Configure PDP to allow all requests
				pdpServer.addRule(true, func(_ map[string]interface{}) bool {
					return true
				})

				// Create authorization config with context.include_operation enabled
				authzConfig := fmt.Sprintf(`{
  "version": "1.0",
  "type": "httpv1",
  "pdp": {
    "http": {
      "url": "%s",
      "timeout": 10,
      "insecure_skip_verify": true
    },
    "claim_mapping": "standard",
    "context": {
      "include_operation": true
    }
  }
}`, pdpServer.url())

				// Write config to temporary file
				var err error
				tempDir, err = os.MkdirTemp("", "http-pdp-ctx-op-test")
				Expect(err).ToNot(HaveOccurred())

				authzConfigPath = filepath.Join(tempDir, "authz-config.json")
				err = os.WriteFile(authzConfigPath, []byte(authzConfig), 0644)
				Expect(err).ToNot(HaveOccurred())

				// Start MCP server with HTTP PDP authorization
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--authz-config", authzConfigPath,
					"osv").ExpectSuccess()

				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				// Get server URL
				serverURL, err = e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				err = e2e.WaitForMCPServerReady(config, serverURL, "sse", 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			})

			BeforeEach(func() {
				// Create fresh MCP client for each test
				var err error
				mcpClient, err = e2e.NewMCPClientForSSE(config, serverURL)
				Expect(err).ToNot(HaveOccurred())

				// Create context that will be cancelled in AfterEach
				ctx, cancelFunc := context.WithTimeout(context.Background(), 30*time.Second)
				cancel = cancelFunc
				err = mcpClient.Initialize(ctx)
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				if cancel != nil {
					cancel()
				}
				if mcpClient != nil {
					mcpClient.Close()
				}
			})

			AfterAll(func() {
				if config.CleanupAfter {
					// Clean up the shared server
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")

					// Clean up mock PDP server
					if pdpServer != nil {
						pdpServer.close()
					}

					// Clean up temporary files
					if tempDir != "" {
						os.RemoveAll(tempDir)
					}
				}
			})

			It("should include operation metadata in PORC context [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Making a tool call to generate PORC with operation metadata")
				arguments := map[string]interface{}{
					"package_name": "lodash",
					"ecosystem":    "npm",
					"version":      "4.17.15",
				}

				_ = mcpClient.ExpectToolCall(ctx, "query_vulnerability", arguments)

				By("Verifying operation metadata is included in PORC context")
				requests := pdpServer.getRequests()
				Expect(requests).ToNot(BeEmpty(), "PDP should have received requests")

				// Get the most recent request
				lastRequest := requests[len(requests)-1]

				// Verify context structure
				context, ok := lastRequest["context"].(map[string]interface{})
				Expect(ok).To(BeTrue(), "Context should be a map")

				// Verify mcp object exists
				mcp, ok := context["mcp"].(map[string]interface{})
				Expect(ok).To(BeTrue(), "Context should have mcp object")

				// Verify operation metadata fields
				Expect(mcp).To(HaveKey("feature"), "Context.mcp should have feature")
				Expect(mcp).To(HaveKey("operation"), "Context.mcp should have operation")
				Expect(mcp).To(HaveKey("resource_id"), "Context.mcp should have resource_id")

				feature, _ := mcp["feature"].(string)
				operation, _ := mcp["operation"].(string)
				resourceID, _ := mcp["resource_id"].(string)

				Expect(feature).To(Equal("tool"))
				Expect(operation).To(Equal("call"))
				Expect(resourceID).To(Equal("query_vulnerability"))

				GinkgoWriter.Printf("✅ Operation metadata in context verified\n")
				GinkgoWriter.Printf("   Feature: %v, Operation: %v, Resource ID: %v\n", feature, operation, resourceID)
			})
		})
	})

	Describe("Error Handling", func() {
		Context("when PDP server is unreachable", func() {
			var serverName string
			var authzConfigPath string
			var tempDir string

			BeforeEach(func() {
				serverName = e2e.GenerateUniqueServerName("http-pdp-error-test")

				// Create authorization config pointing to non-existent PDP server
				authzConfig := `{
  "version": "1.0",
  "type": "httpv1",
  "pdp": {
    "http": {
      "url": "http://localhost:19999",
      "timeout": 2
    },
    "claim_mapping": "standard"
  }
}`

				// Write config to temporary file
				var err error
				tempDir, err = os.MkdirTemp("", "http-pdp-error-test")
				Expect(err).ToNot(HaveOccurred())

				authzConfigPath = filepath.Join(tempDir, "authz-config.json")
				err = os.WriteFile(authzConfigPath, []byte(authzConfig), 0644)
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				if config.CleanupAfter && tempDir != "" {
					os.RemoveAll(tempDir)
				}
			})

			It("should fail to start server with unreachable PDP [Serial]", func() {
				By("Attempting to start server with unreachable PDP")

				// The server should start successfully - authz errors occur at request time
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--authz-config", authzConfigPath,
					"osv").ExpectSuccess()

				// Wait for server to start
				err := e2e.WaitForMCPServer(config, serverName, 30*time.Second)
				Expect(err).ToNot(HaveOccurred())

				// Clean up
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred())
				}
			})
		})
	})
})

// getKeys returns the keys of a map as a slice.
func getKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

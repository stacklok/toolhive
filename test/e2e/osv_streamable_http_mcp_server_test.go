package e2e_test

import (
	"context"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("OsvStreamableHttpMcpServer", Label("mcp", "streamable-http", "e2e"), Serial, func() {
	var config *e2e.TestConfig

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Describe("Running OSV MCP server with Streamable HTTP transport", func() {
		Context("when starting the server from registry", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueServerName("osv-registry-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					// Clean up the server after each test in this context
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should successfully start and be accessible via Streamable HTTP [Serial]", func() {
				By("Starting the OSV MCP server with Streamable HTTP transport and audit enabled")
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "streamable-http",
					"--enable-audit",
					"osv").ExpectSuccess()

				// The command should indicate success
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the OSV server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Verifying the server appears in the list with Streamable HTTP transport")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should appear in the list")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
				Expect(stdout).To(ContainSubstring("mcp"), "Server should show mcp endpoint")
			})

			It("should be accessible via HTTP Streamable HTTP endpoint [Serial]", func() {
				By("Starting the OSV MCP server with audit enabled")
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "streamable-http",
					"--enable-audit",
					"osv").ExpectSuccess()

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Getting the server URL")
				serverURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to get server URL")
				Expect(serverURL).To(ContainSubstring("http"), "URL should be HTTP-based")
				Expect(serverURL).To(ContainSubstring("/mcp"), "URL should contain MCP endpoint")

				By("Waiting before starting the HTTP request")
				time.Sleep(10 * time.Second)

				By("Making an HTTP request to the MCP endpoint")

				client := &http.Client{Timeout: 10 * time.Second}
				var resp *http.Response
				var httpErr error

				maxRetries := 5
				for i := 0; i < maxRetries; i++ {
					resp, httpErr = client.Get(serverURL)
					if httpErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 500 {
						break
					}
					if resp != nil {
						resp.Body.Close()
					}
					time.Sleep(10 * time.Second)
				}

				Expect(httpErr).ToNot(HaveOccurred(), "Should be able to connect to streamable HTTP endpoint")
				Expect(resp).ToNot(BeNil(), "Response should not be nil")
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(BeNumerically(">=", 200), "Should get a valid HTTP response")
				Expect(resp.StatusCode).To(BeNumerically("<", 500), "Should not get a server error")
			})

			It("should respond to proper MCP protocol operations [Serial]", func() {
				By("Starting the OSV MCP server")
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "streamable-http",
					"osv").ExpectSuccess()

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Getting the server URL")
				serverURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				By("Waiting for MCP server to be ready")
				err = e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "MCP server should be ready for protocol operations")

				By("Creating MCP client and initializing connection")
				mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, serverURL)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create MCP client")
				defer mcpClient.Close()

				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				err = mcpClient.Initialize(ctx)
				Expect(err).ToNot(HaveOccurred(), "Should be able to initialize MCP connection")

				By("Testing basic MCP operations")
				err = mcpClient.Ping(ctx)
				Expect(err).ToNot(HaveOccurred(), "Should be able to ping the server")

				By("Listing available tools")
				tools, err := mcpClient.ListTools(ctx)
				Expect(err).ToNot(HaveOccurred(), "Should be able to list tools")
				Expect(tools.Tools).ToNot(BeEmpty(), "OSV server should provide tools")

				GinkgoWriter.Printf("Available tools: %d\n", len(tools.Tools))
				for _, tool := range tools.Tools {
					GinkgoWriter.Printf("  - %s: %s\n", tool.Name, tool.Description)
				}
			})
		})

		Context("when testing OSV-specific functionality", Ordered, func() {
			var mcpClient *e2e.MCPClientHelper
			var serverURL string
			var cancel context.CancelFunc
			var serverName string

			BeforeAll(func() {
				// Generate unique server name for this context
				serverName = generateUniqueServerName("osv-functionality-test")

				// Start ONE server for ALL OSV-specific tests
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "streamable-http",
					"osv").ExpectSuccess()
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				// Get server URL
				serverURL, err = e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				err = e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			})

			BeforeEach(func() {
				// Create fresh MCP client for each test
				var err error
				mcpClient, err = e2e.NewMCPClientForStreamableHTTP(config, serverURL)
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
					// Clean up the shared server after all tests
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should be listed in registry with OSV-specific information [Serial]", func() {
				By("Getting OSV server info from registry")
				stdout, _ := e2e.NewTHVCommand(config, "registry", "info", "osv").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("osv"), "Info should be about OSV server")
				Expect(stdout).To(ContainSubstring("vulnerability"), "Info should mention vulnerability scanning")
			})

			It("should provide vulnerability query tools [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Listing available tools")
				mcpClient.ExpectToolExists(ctx, "query_vulnerability")

				By("Testing vulnerability query with a known package")
				// Test with a well-known package that should have vulnerabilities
				arguments := map[string]interface{}{
					"package_name": "lodash",
					"ecosystem":    "npm",
					"version":      "4.17.15", // Known vulnerable version from OSV docs
				}

				result := mcpClient.ExpectToolCall(ctx, "query_vulnerability", arguments)
				Expect(result.Content).ToNot(BeEmpty(), "Should return vulnerability information")

				GinkgoWriter.Printf("Vulnerability query result: %+v\n", result.Content)
			})

			It("should handle batch vulnerability queries [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Testing batch vulnerability query")
				mcpClient.ExpectToolExists(ctx, "query_vulnerabilities_batch")

				arguments := map[string]interface{}{
					"queries": []map[string]interface{}{
						{
							"package_name": "lodash",
							"ecosystem":    "npm",
							"version":      "4.17.15",
						},
						{
							"package_name": "jinja2",
							"ecosystem":    "PyPI",
							"version":      "2.4.1",
						},
					},
				}

				result := mcpClient.ExpectToolCall(ctx, "query_vulnerabilities_batch", arguments)
				Expect(result.Content).ToNot(BeEmpty(), "Should return batch vulnerability information")

				GinkgoWriter.Printf("Batch vulnerability query result: %+v\n", result.Content)
			})

			It("should get vulnerability details by ID [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Testing get vulnerability by ID")
				mcpClient.ExpectToolExists(ctx, "get_vulnerability")

				arguments := map[string]interface{}{
					"id": "GHSA-vqj2-4v8m-8vrq", // Example from OSV docs
				}

				result := mcpClient.ExpectToolCall(ctx, "get_vulnerability", arguments)
				Expect(result.Content).ToNot(BeEmpty(), "Should return vulnerability details")

				GinkgoWriter.Printf("Vulnerability details result: %+v\n", result.Content)
			})

			It("should handle invalid vulnerability queries gracefully [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Testing with invalid package information")
				arguments := map[string]interface{}{
					"package_name": "non-existent-package-12345",
					"ecosystem":    "npm",
					"version":      "1.0.0",
				}

				// This should not fail, but should return empty results
				result, err := mcpClient.CallTool(ctx, "query_vulnerability", arguments)
				Expect(err).ToNot(HaveOccurred(), "Should handle invalid queries gracefully")
				Expect(result).ToNot(BeNil(), "Should return a result even for non-existent packages")

				GinkgoWriter.Printf("Invalid query result: %+v\n", result.Content)
			})
		})

		Context("when managing server lifecycle", func() {
			var serverName string

			BeforeEach(func() {
				// Generate unique server name for each lifecycle test
				serverName = generateUniqueServerName("osv-lifecycle-test")

				// Start a server for lifecycle tests
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "streamable-http",
					"osv").ExpectSuccess()
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				if config.CleanupAfter {
					// Clean up the server after each lifecycle test
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should stop the Streamable HTTP server successfully [Serial]", func() {
				By("Stopping the server")
				stdout, _ := e2e.NewTHVCommand(config, "stop", serverName).ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Output should mention the server name")

				By("Verifying the server is stopped")
				Eventually(func() string {
					stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
					return stdout
				}, 10*time.Second, 1*time.Second).Should(Or(
					// Server should either be in exited state or completely removed
					And(ContainSubstring(serverName), ContainSubstring("stopped")),
					Not(ContainSubstring(serverName)),
				), "Server should be stopped (exited) or removed from list")
			})

			It("should restart the Streamable HTTP server successfully [Serial]", func() {
				By("Restarting the server")
				stdout, _ := e2e.NewTHVCommand(config, "restart", serverName).ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName))

				By("Waiting for the server to be running again")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Verifying Streamable HTTP endpoint is accessible again")
				serverURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				client := &http.Client{Timeout: 5 * time.Second}
				resp, err := client.Get(serverURL)
				if err == nil {
					resp.Body.Close()
				}
				// Connection attempt should not fail completely
			})
		})
	})

	Describe("Error handling for Streamable HTTP transport", func() {
		Context("when providing invalid configuration", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueServerName("osv-error-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					// Clean up any server that might have been created
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should fail when trying to use stdio transport with OSV if not supported [Serial]", func() {
				By("Trying to run OSV with stdio transport")
				// Note: This test assumes OSV doesn't support stdio.
				// If it does, this test should be adjusted or removed.
				stdout, stderr, err := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "stdio",
					"osv").Run()

				// Check if the command succeeded or failed
				if err != nil {
					// If it failed, that's expected for Streamable HTTP-only servers
					Expect(stderr).To(ContainSubstring("transport"), "Error should mention transport issue")
				} else {
					// If it succeeded, OSV supports both transports
					GinkgoWriter.Printf("Note: OSV server supports stdio transport: %s\n", stdout)
					// Clean up the successfully started server
					_ = e2e.StopAndRemoveMCPServer(config, serverName)
				}
			})
		})
	})
})

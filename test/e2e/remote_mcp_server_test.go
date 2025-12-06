package e2e_test

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// WorkloadInfo represents a workload from thv list --format json
type WorkloadInfo struct {
	Name          string            `json:"name"`
	Package       string            `json:"package"`
	URL           string            `json:"url"`
	Port          int               `json:"port"`
	TransportType string            `json:"transport_type"`
	ProxyMode     string            `json:"proxy_mode"`
	Status        string            `json:"status"`
	CreatedAt     string            `json:"created_at"`
	Labels        map[string]string `json:"labels"`
	Group         string            `json:"group"`
	Remote        bool              `json:"remote"`
}

var _ = Describe("Remote MCP Server", Label("remote", "mcp", "e2e"), Serial, func() {
	var config *e2e.TestConfig

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Describe("Running remote MCP server from registry", func() {
		Context("when starting mcp-spec remote server", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueServerName("mcp-spec-remote-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					// Clean up the server after each test
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should successfully start remote server from registry [Serial]", func() {
				By("Starting the mcp-spec remote MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"mcp-spec").ExpectSuccess()

				// The command should indicate success
				Expect(stdout+stderr).To(ContainSubstring("mcp-spec"), "Output should mention the mcp-spec server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Verifying the server appears in the list with correct attributes")
				stdout, _ = e2e.NewTHVCommand(config, "list", "--format", "json").ExpectSuccess()

				var workloads []WorkloadInfo
				err = json.Unmarshal([]byte(stdout), &workloads)
				Expect(err).ToNot(HaveOccurred(), "Should be able to parse JSON output")

				// Find the server in the list
				var serverInfo *WorkloadInfo
				for i := range workloads {
					if workloads[i].Name == serverName {
						serverInfo = &workloads[i]
						break
					}
				}

				Expect(serverInfo).ToNot(BeNil(), "Server should appear in the list")
				Expect(serverInfo.Status).To(Equal("running"), "Server should be in running state")
				Expect(serverInfo.Remote).To(BeTrue(), "Server should be marked as remote")
				Expect(serverInfo.Package).To(Equal("remote"), "Package should be 'remote'")
				Expect(serverInfo.TransportType).To(Equal("streamable-http"), "Transport should be streamable-http")
			})

			It("should verify server has remote flag set [Serial]", func() {
				By("Starting the mcp-spec remote MCP server")
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"mcp-spec").ExpectSuccess()

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Verifying server has remote=true in JSON output")
				stdout, _ := e2e.NewTHVCommand(config, "list", "--format", "json").ExpectSuccess()

				var workloads []WorkloadInfo
				err = json.Unmarshal([]byte(stdout), &workloads)
				Expect(err).ToNot(HaveOccurred())

				var found bool
				for i := range workloads {
					if workloads[i].Name == serverName {
						Expect(workloads[i].Remote).To(BeTrue(), "Remote field should be true")
						found = true
						break
					}
				}
				Expect(found).To(BeTrue(), "Server should be found in list")
			})

			It("should be accessible via the proxy endpoint [Serial]", func() {
				By("Starting the mcp-spec remote MCP server")
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"mcp-spec").ExpectSuccess()

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Getting the server URL")
				serverURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to get server URL")
				Expect(serverURL).To(ContainSubstring("http"), "URL should be HTTP-based")

				By("Waiting for MCP server to be ready")
				err = e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "MCP server should be ready for protocol operations")
			})

			It("should respond to MCP protocol operations [Serial]", func() {
				By("Starting the mcp-spec remote MCP server")
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"mcp-spec").ExpectSuccess()

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
				Expect(tools.Tools).ToNot(BeEmpty(), "mcp-spec server should provide tools")

				By("Verifying SearchModelContextProtocol tool is available")
				var foundSearchTool bool
				for _, tool := range tools.Tools {
					GinkgoWriter.Printf("  - %s: %s\n", tool.Name, tool.Description)
					if tool.Name == "SearchModelContextProtocol" {
						foundSearchTool = true
					}
				}
				Expect(foundSearchTool).To(BeTrue(), "Should find SearchModelContextProtocol tool")
			})

			It("should successfully call SearchModelContextProtocol tool [Serial]", func() {
				By("Starting the mcp-spec remote MCP server")
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"mcp-spec").ExpectSuccess()

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Getting the server URL")
				serverURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				By("Waiting for MCP server to be ready")
				err = e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Creating MCP client and initializing connection")
				mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, serverURL)
				Expect(err).ToNot(HaveOccurred())
				defer mcpClient.Close()

				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				err = mcpClient.Initialize(ctx)
				Expect(err).ToNot(HaveOccurred())

				By("Calling SearchModelContextProtocol tool with a query")
				arguments := map[string]interface{}{
					"query": "transport",
				}

				result := mcpClient.ExpectToolCall(ctx, "SearchModelContextProtocol", arguments)
				Expect(result.Content).ToNot(BeEmpty(), "Should return search results")

				GinkgoWriter.Printf("Search results: %+v\n", result.Content)
			})
		})

		Context("when managing server lifecycle", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueServerName("mcp-spec-lifecycle-test")

				// Start a server for lifecycle tests
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"mcp-spec").ExpectSuccess()
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should stop the remote server successfully [Serial]", func() {
				By("Stopping the server")
				stdout, _ := e2e.NewTHVCommand(config, "stop", serverName).ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Output should mention the server name")

				By("Verifying the server is stopped")
				Eventually(func() string {
					stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
					return stdout
				}, 10*time.Second, 1*time.Second).Should(Or(
					And(ContainSubstring(serverName), ContainSubstring("stopped")),
					Not(ContainSubstring(serverName)),
				), "Server should be stopped or removed from list")
			})

			It("should restart the remote server successfully [Serial]", func() {
				By("Restarting the server")
				stdout, _ := e2e.NewTHVCommand(config, "restart", serverName).ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName))

				By("Waiting for the server to be running again")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Verifying endpoint is accessible again")
				serverURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				err = e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 30*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be ready after restart")
			})

			It("should view logs for remote server [Serial]", func() {
				By("Getting logs for the remote server")
				stdout, _ := e2e.NewTHVCommand(config, "logs", serverName).ExpectSuccess()

				// Logs should exist (even if empty) and not error out
				// Remote servers have proxy logs
				Expect(stdout).ToNot(BeNil())
				GinkgoWriter.Printf("Remote server logs:\n%s\n", stdout)
			})
		})
	})

	Describe("Running remote MCP server with custom URL", func() {
		Context("when providing explicit remote URL", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueServerName("custom-remote-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should start remote server with explicit URL [Serial]", func() {
				By("Starting remote MCP server with explicit URL")
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"https://modelcontextprotocol.io/mcp").ExpectSuccess()

				Expect(stdout+stderr).To(Or(
					ContainSubstring("modelcontextprotocol.io"),
					ContainSubstring(serverName),
				), "Output should mention the URL or server name")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Verifying the server is marked as remote")
				stdout, _ = e2e.NewTHVCommand(config, "list", "--format", "json").ExpectSuccess()

				var workloads []WorkloadInfo
				err = json.Unmarshal([]byte(stdout), &workloads)
				Expect(err).ToNot(HaveOccurred())

				var found bool
				for i := range workloads {
					if workloads[i].Name == serverName {
						Expect(workloads[i].Remote).To(BeTrue(), "Should be marked as remote")
						found = true
						break
					}
				}
				Expect(found).To(BeTrue())

				By("Verifying the server is accessible")
				serverURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				err = e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Testing MCP protocol operations")
				mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, serverURL)
				Expect(err).ToNot(HaveOccurred())
				defer mcpClient.Close()

				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				err = mcpClient.Initialize(ctx)
				Expect(err).ToNot(HaveOccurred())

				tools, err := mcpClient.ListTools(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(tools.Tools).ToNot(BeEmpty(), "Should have tools available")
			})
		})
	})
})

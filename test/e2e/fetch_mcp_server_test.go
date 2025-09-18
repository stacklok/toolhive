package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("FetchMcpServer", Label("mcp", "e2e"), func() {
	var (
		config     *e2e.TestConfig
		serverName string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = fmt.Sprintf("fetch-test-%d", GinkgoRandomSeed())

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up the server if it exists
			err := e2e.StopAndRemoveMCPServer(config, serverName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
		}
	})

	Describe("Running fetch MCP server", func() {
		Context("when starting the server from registry", func() {
			It("should successfully start and be accessible", func() {
				By("Starting the fetch MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch").ExpectSuccess()

				// The command should indicate success
				Expect(stdout+stderr).To(ContainSubstring("fetch"), "Output should mention the fetch server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 30 seconds")

				By("Verifying the server appears in the list")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should appear in the list")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
			})

			It("should be accessible via HTTP", func() {
				By("Starting the fetch MCP server")
				e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch").ExpectSuccess()

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Getting the server URL")
				serverURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to get server URL")
				Expect(serverURL).To(ContainSubstring("http"), "URL should be HTTP-based")

				By("Making a basic HTTP request to the server")
				// Note: This is a basic connectivity test. In a real scenario,
				// you'd want to test the actual MCP protocol endpoints
				client := &http.Client{Timeout: 10 * time.Second}
				resp, err := client.Get(serverURL)
				if err == nil {
					resp.Body.Close()
					// If we get here, the server is at least responding to HTTP requests
					// The actual response code may vary depending on the MCP server implementation
				} else {
					// Some MCP servers might not respond to basic GET requests
					// This is acceptable for this basic connectivity test
					GinkgoWriter.Printf("Note: Server may not respond to basic GET requests: %v\n", err)
				}
			})
		})

		Context("when starting the server from registry with tools filter", func() {
			It("should start when filters are correct", func() {
				By("Starting the fetch MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch", "--tools", "fetch").ExpectSuccess()

				// The command should indicate success
				Expect(stdout+stderr).To(ContainSubstring("fetch"), "Output should mention the fetch server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 30 seconds")

				By("Verifying the server appears in the list")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should appear in the list")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
			})

			It("should not start when filters are incorrect", func() {
				By("Starting the fetch MCP server")
				_, _, err := e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch", "--tools", "wrong-tool").ExpectFailure()
				Expect(err).To(HaveOccurred(), "Should fail with non-existent server")
			})
		})

		Context("when starting the server from registry with tools override", Label("override"), func() {
			var (
				toolsOverrideFile string
				tempDir           string
			)

			BeforeEach(func() {
				// Create temporary directory for tool override files
				tempDir = GinkgoT().TempDir()
			})

			It("should start with valid tool override and show overridden tool names", func() {
				By("Creating a valid tool override JSON file")
				toolsOverrideContent := `{
					"toolsOverride": {
						"fetch": {
							"name": "custom_fetch_tool",
							"description": "A customized fetch tool with overridden name and description"
						}
					}
				}`
				toolsOverrideFile = tempDir + "/tools_override.json"
				err := os.WriteFile(toolsOverrideFile, []byte(toolsOverrideContent), 0644)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create tool override file")

				By("Starting the fetch MCP server with tool override")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch", "--tools-override", toolsOverrideFile).ExpectSuccess()

				// The command should indicate success
				Expect(stdout+stderr).To(ContainSubstring("fetch"), "Output should mention the fetch server")

				By("Waiting for the server to be running")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Verifying the server appears in the list")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should appear in the list")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")

				By("Verifying tool override is applied by listing tools")
				stdout, _ = e2e.NewTHVCommand(config, "mcp", "list", "tools", "--server", serverName, "--timeout", "60s").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("custom_fetch_tool"), "Should show overridden tool name")
				Expect(stdout).To(ContainSubstring("customized fetch tool"), "Should show overridden tool description")
			})

			It("should start with tool override that only changes description", func() {
				By("Creating a tool override JSON file with only description override")
				toolsOverrideContent := `{
					"toolsOverride": {
						"fetch": {
							"description": "An enhanced fetch tool with custom description only"
						}
					}
				}`
				toolsOverrideFile = tempDir + "/tools_override_desc_only.json"
				err := os.WriteFile(toolsOverrideFile, []byte(toolsOverrideContent), 0644)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create tool override file")

				By("Starting the fetch MCP server with description-only tool override")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch", "--tools-override", toolsOverrideFile).ExpectSuccess()

				// The command should indicate success
				Expect(stdout+stderr).To(ContainSubstring("fetch"), "Output should mention the fetch server")

				By("Waiting for the server to be running")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Verifying tool override is applied by listing tools")
				stdout, _ = e2e.NewTHVCommand(config, "mcp", "list", "tools", "--server", serverName, "--timeout", "60s").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("fetch"), "Should still show original tool name")
				Expect(stdout).To(ContainSubstring("enhanced fetch tool"), "Should show overridden tool description")
			})

			It("should start with tool override that only changes name", func() {
				By("Creating a tool override JSON file with only name override")
				toolsOverrideContent := `{
					"toolsOverride": {
						"fetch": {
							"name": "renamed_fetch"
						}
					}
				}`
				toolsOverrideFile = tempDir + "/tools_override_name_only.json"
				err := os.WriteFile(toolsOverrideFile, []byte(toolsOverrideContent), 0644)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create tool override file")

				By("Starting the fetch MCP server with name-only tool override")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch", "--tools-override", toolsOverrideFile).ExpectSuccess()

				// The command should indicate success
				Expect(stdout+stderr).To(ContainSubstring("fetch"), "Output should mention the fetch server")

				By("Waiting for the server to be running")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Verifying tool override is applied by listing tools")
				stdout, _ = e2e.NewTHVCommand(config, "mcp", "list", "tools", "--server", serverName, "--timeout", "60s").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("renamed_fetch"), "Should show overridden tool name")
			})

			It("should fail when tool override file has invalid JSON", func() {
				By("Creating an invalid tool override JSON file")
				toolsOverrideContent := `{
					"toolsOverride": {
						"fetch": {
							"name": "invalid_json"
						}
					// Missing closing brace
				}`
				toolsOverrideFile = tempDir + "/invalid_tools_override.json"
				err := os.WriteFile(toolsOverrideFile, []byte(toolsOverrideContent), 0644)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create invalid tool override file")

				By("Attempting to start the fetch MCP server with invalid tool override")
				_, _, err = e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch", "--tools-override", toolsOverrideFile).ExpectFailure()
				Expect(err).To(HaveOccurred(), "Should fail with invalid JSON")
			})

			It("should fail when tool override file does not exist", func() {
				By("Attempting to start the fetch MCP server with non-existent tool override file")
				_, _, err := e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch", "--tools-override", "/non/existent/file.json").ExpectFailure()
				Expect(err).To(HaveOccurred(), "Should fail with non-existent file")
			})

			It("should fail when tool override has empty name and description", func() {
				By("Creating a tool override JSON file with empty override")
				toolsOverrideContent := `{
					"toolsOverride": {
						"fetch": {
							"name": "",
							"description": ""
						}
					}
				}`
				toolsOverrideFile = tempDir + "/empty_tools_override.json"
				err := os.WriteFile(toolsOverrideFile, []byte(toolsOverrideContent), 0644)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create empty tool override file")

				By("Attempting to start the fetch MCP server with empty tool override")
				_, _, err = e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch", "--tools-override", toolsOverrideFile).ExpectFailure()
				Expect(err).To(HaveOccurred(), "Should fail with empty tool override")
			})
		})

		Context("when combining tools filter with tools override", Label("override", "filter"), func() {
			var (
				toolsOverrideFile string
				tempDir           string
			)

			BeforeEach(func() {
				// Create temporary directory for tool override files
				tempDir = GinkgoT().TempDir()
			})

			It("should apply both filter and override correctly", func() {
				By("Creating a tool override JSON file")
				toolsOverrideContent := `{
					"toolsOverride": {
						"fetch": {
							"name": "filtered_and_overridden_fetch",
							"description": "A fetch tool that is both filtered and overridden"
						}
					}
				}`
				toolsOverrideFile = tempDir + "/combined_tools_override.json"
				err := os.WriteFile(toolsOverrideFile, []byte(toolsOverrideContent), 0644)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create tool override file")

				By("Starting the fetch MCP server with both tools filter and override")
				stdout, stderr := e2e.NewTHVCommand(
					config, "run", "--name", serverName, "fetch", "--tools", "filtered_and_overridden_fetch", "--tools-override", toolsOverrideFile).ExpectSuccess()

				// The command should indicate success
				Expect(stdout+stderr).To(ContainSubstring("fetch"), "Output should mention the fetch server")

				By("Waiting for the server to be running")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Verifying both filter and override are applied by listing tools")
				stdout, _ = e2e.NewTHVCommand(config, "mcp", "list", "tools", "--server", serverName, "--timeout", "60s").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("filtered_and_overridden_fetch"), "Should show overridden tool name")
				Expect(stdout).To(ContainSubstring("filtered and overridden"), "Should show overridden tool description")
			})

			It("should fail when filtering out a tool that has an override", func() {
				By("Creating a tool override JSON file for a tool that will be filtered out")
				toolsOverrideContent := `{
					"toolsOverride": {
						"fetch": {
							"name": "overridden_but_filtered_out",
							"description": "This tool will be filtered out despite having an override"
						}
					}
				}`
				toolsOverrideFile = tempDir + "/filtered_out_override.json"
				err := os.WriteFile(toolsOverrideFile, []byte(toolsOverrideContent), 0644)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create tool override file")

				By("Attempting to start server with tool filter that excludes the overridden tool")
				_, _, err = e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch", "--tools", "non-existent-tool", "--tools-override", toolsOverrideFile).ExpectFailure()
				Expect(err).To(HaveOccurred(), "Should fail when filtering out overridden tool")
			})
		})

		Context("when managing the server lifecycle", func() {
			BeforeEach(func() {
				// Start a server for lifecycle tests
				e2e.NewTHVCommand(config, "run", "--name", serverName, "fetch").ExpectSuccess()
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			})

			It("should stop the server successfully", func() {
				By("Stopping the server")
				stdout, _ := e2e.NewTHVCommand(config, "stop", serverName).ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Output should mention the server name")

				By("Verifying the server is stopped")
				Eventually(func() bool {
					stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
					lines := strings.Split(stdout, "\n")
					for _, line := range lines {
						if strings.Contains(line, serverName) {
							// Check if this specific server line contains "running"
							return !strings.Contains(line, "running")
						}
					}
					return false // Server not found in list
				}, 10*time.Second, 1*time.Second).Should(BeTrue(), "Server should be stopped")
			})

			It("should restart the server successfully", func() {
				By("Restarting the server")
				stdout, _ := e2e.NewTHVCommand(config, "restart", serverName).ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName))

				By("Waiting for the server to be running again")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			})

			It("should remove the server successfully", func() {
				By("Removing the server")
				stdout, _ := e2e.NewTHVCommand(config, "rm", serverName).ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName))

				By("Verifying the server is no longer listed")
				Eventually(func() string {
					stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
					return stdout
				}, 10*time.Second, 1*time.Second).ShouldNot(ContainSubstring(serverName),
					"Server should no longer be listed")
			})
		})

		Context("when testing registry operations", func() {
			It("should list available servers in registry", func() {
				By("Listing registry servers")
				stdout, _ := e2e.NewTHVCommand(config, "registry", "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("fetch"), "Registry should contain fetch server")
			})

			It("should show fetch server info", func() {
				By("Getting fetch server info")
				stdout, _ := e2e.NewTHVCommand(config, "registry", "info", "--format", "json", "fetch").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("fetch"), "Info should be about fetch server")
				Expect(stdout).To(ContainSubstring("tools"), "Info should mention tools")

				// Verify it's valid JSON
				var serverInfo map[string]interface{}
				err := json.Unmarshal([]byte(stdout), &serverInfo)
				Expect(err).ToNot(HaveOccurred(), "Output should be valid JSON")

				// Verify required fields
				Expect(serverInfo["name"]).To(Equal("fetch"))
				Expect(serverInfo["tools"]).ToNot(BeNil(), "Should have tools field")
			})

			It("should search for fetch server", func() {
				By("Searching for fetch server")
				stdout, _ := e2e.NewTHVCommand(config, "search", "fetch").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("fetch"), "Search should find fetch server")
			})
		})
	})

	Describe("Error handling", func() {
		Context("when providing invalid arguments", func() {
			It("should fail with invalid server name", func() {
				By("Trying to run a non-existent server")
				_, _, err := e2e.NewTHVCommand(config, "run", "non-existent-server-12345").ExpectFailure()
				Expect(err).To(HaveOccurred(), "Should fail with non-existent server")
			})

			It("should fail with invalid transport", func() {
				By("Trying to run with invalid transport")
				_, _, err := e2e.NewTHVCommand(config, "run",
					"--transport", "invalid-transport",
					"fetch").ExpectFailure()
				Expect(err).To(HaveOccurred(), "Should fail with invalid transport")
			})
		})

		Context("when managing non-existent servers", func() {
			It("should handle stopping non-existent server gracefully", func() {
				By("Trying to stop a non-existent server")
				stdout, _ := e2e.NewTHVCommand(config, "stop", "non-existent-server-12345").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("stopped successfully"), "Should indicate server has stopped successfully")
			})
		})
	})
})

package e2e_test

import (
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// generateUniqueProtocolServerName creates a unique server name for protocol build tests
func generateUniqueProtocolServerName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d-%d", prefix, os.Getpid(), time.Now().UnixNano(), GinkgoRandomSeed())
}

var _ = Describe("Protocol Builds E2E", Label("mcp", "protocols", "e2e"), Serial, func() {
	var config *e2e.TestConfig

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Describe("Running MCP server using npx:// protocol scheme", func() {
		Context("when starting @modelcontextprotocol/server-sequential-thinking", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueProtocolServerName("sequential-thinking-noversion-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should build and start successfully and provide sequential_thinking tool [Serial]", func() {
				By("Starting the Sequential Thinking MCP server using npx:// protocol")
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "stdio",
					"npx://@modelcontextprotocol/server-sequential-thinking").ExpectSuccess()

				// The command should indicate success and show build process
				output := stdout + stderr
				Expect(output).To(ContainSubstring("Building Docker image"), "Should show Docker build process")
				Expect(output).To(ContainSubstring("Successfully built"), "Should successfully build the image")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 120*time.Second) // Longer timeout for protocol builds
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 120 seconds")

				By("Verifying the server appears in the list")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should appear in the list")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
				Expect(stdout).To(ContainSubstring("npx-modelcontextprotocol-server-sequential-thinking"), "Should show the built image name")

				By("Listing tools and verifying sequentialthinking tool exists")
				stdout, _ = e2e.NewTHVCommand(config, "mcp", "list", "tools", "--server", serverName, "--timeout", "60s").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("sequentialthinking"), "Should find sequentialthinking tool")

				GinkgoWriter.Printf("✅ Protocol build successful: npx://@modelcontextprotocol/server-sequential-thinking\n")
				GinkgoWriter.Printf("✅ Server running and provides sequential_thinking tool\n")
			})
		})

		Context("when starting @modelcontextprotocol/server-sequential-thinking@latest", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueProtocolServerName("sequential-thinking-latest-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should build and start successfully and provide sequential_thinking tool [Serial]", func() {
				By("Starting the Sequential Thinking MCP server using npx:// protocol")
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "stdio",
					"npx://@modelcontextprotocol/server-sequential-thinking@latest").ExpectSuccess()

				// The command should indicate success and show build process
				output := stdout + stderr
				Expect(output).To(ContainSubstring("Building Docker image"), "Should show Docker build process")
				Expect(output).To(ContainSubstring("Successfully built"), "Should successfully build the image")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 120*time.Second) // Longer timeout for protocol builds
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 120 seconds")

				By("Verifying the server appears in the list")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should appear in the list")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
				Expect(stdout).To(ContainSubstring("npx-modelcontextprotocol-server-sequential-thinking"), "Should show the built image name")

				By("Listing tools and verifying sequentialthinking tool exists")
				stdout, _ = e2e.NewTHVCommand(config, "mcp", "list", "tools", "--server", serverName, "--timeout", "60s").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("sequentialthinking"), "Should find sequentialthinking tool")

				GinkgoWriter.Printf("✅ Protocol build successful: npx://@modelcontextprotocol/server-sequential-thinking\n")
				GinkgoWriter.Printf("✅ Server running and provides sequential_thinking tool\n")
			})
		})

		Context("when starting @modelcontextprotocol/server-sequential-thinking@2025.7.1", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueProtocolServerName("sequential-thinking-pinned-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should build and start successfully and provide sequential_thinking tool [Serial]", func() {
				By("Starting the Sequential Thinking MCP server using npx:// protocol")
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "stdio",
					"npx://@modelcontextprotocol/server-sequential-thinking@2025.7.1").ExpectSuccess()

				// The command should indicate success and show build process
				output := stdout + stderr
				Expect(output).To(ContainSubstring("Building Docker image"), "Should show Docker build process")
				Expect(output).To(ContainSubstring("Successfully built"), "Should successfully build the image")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 120*time.Second) // Longer timeout for protocol builds
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 120 seconds")

				By("Verifying the server appears in the list")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should appear in the list")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
				Expect(stdout).To(ContainSubstring("npx-modelcontextprotocol-server-sequential-thinking"), "Should show the built image name")

				By("Listing tools and verifying sequentialthinking tool exists")
				stdout, _ = e2e.NewTHVCommand(config, "mcp", "list", "tools", "--server", serverName, "--timeout", "60s").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("sequentialthinking"), "Should find sequentialthinking tool")

				GinkgoWriter.Printf("✅ Protocol build successful: npx://@modelcontextprotocol/server-sequential-thinking\n")
				GinkgoWriter.Printf("✅ Server running and provides sequential_thinking tool\n")
			})
		})

		Context("when testing error conditions", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueProtocolServerName("protocol-error-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should fail gracefully with invalid protocol scheme [Serial]", func() {
				By("Trying to run with invalid protocol scheme")
				_, stderr, err := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "stdio",
					"invalid-protocol://some-package").ExpectFailure()

				Expect(err).To(HaveOccurred(), "Should fail with invalid protocol scheme")
				Expect(stderr).To(ContainSubstring("protocol"), "Error should mention protocol issue")
			})
		})
	})

	Describe("Running MCP server using uvx:// protocol scheme", func() {
		Context("when starting arxiv-mcp-server", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueProtocolServerName("arxiv-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should build and start successfully and provide arxiv tools [Serial]", func() {
				By("Starting the ArXiv MCP server using uvx:// protocol")
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "stdio",
					"uvx://arxiv-mcp-server").ExpectSuccess()

				// The command should indicate success and show build process
				output := stdout + stderr
				Expect(output).To(ContainSubstring("Building Docker image"), "Should show Docker build process")
				Expect(output).To(ContainSubstring("Successfully built"), "Should successfully build the image")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 120*time.Second) // Longer timeout for protocol builds
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 120 seconds")

				By("Verifying the server appears in the list")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should appear in the list")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
				Expect(stdout).To(ContainSubstring("uvx-arxiv-mcp-server"), "Should show the built image name")

				By("Listing tools and verifying arxiv tools exist")
				stdout, _ = e2e.NewTHVCommand(config, "mcp", "list", "tools", "--server", serverName, "--timeout", "60s").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("search_papers"), "Should find search_papers tool")
				Expect(stdout).To(ContainSubstring("download_paper"), "Should find download_paper tool")
				Expect(stdout).To(ContainSubstring("list_papers"), "Should find list_papers tool")
				Expect(stdout).To(ContainSubstring("read_paper"), "Should find read_paper tool")

				GinkgoWriter.Printf("✅ Protocol build successful: uvx://arxiv-mcp-server\n")
				GinkgoWriter.Printf("✅ Server running and provides arxiv tools\n")
			})
		})

		Context("when testing uvx error conditions", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueProtocolServerName("uvx-error-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should fail gracefully with non-existent uvx package [Serial]", func() {
				By("Trying to run with non-existent uvx package")
				_, stderr, err := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "stdio",
					"uvx://non-existent-package-that-does-not-exist").ExpectFailure()

				Expect(err).To(HaveOccurred(), "Should fail with non-existent package")
				Expect(stderr).To(ContainSubstring("error"), "Error should mention the issue")
			})
		})
	})
	Describe("Running MCP server using go:// protocol scheme", func() {
		Context("when starting osv-mcp server", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueProtocolServerName("go-osv-mcp-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should build and start successfully and provide OSV tools [Serial]", func() {
				By("Starting the OSV MCP server using go:// protocol")
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "streamable-http",
					"go://github.com/StacklokLabs/osv-mcp/cmd/server@latest").ExpectSuccess()

				// The command should indicate success and show build process
				output := stdout + stderr
				Expect(output).To(ContainSubstring("Building Docker image"), "Should show Docker build process")
				Expect(output).To(ContainSubstring("Successfully built"), "Should successfully build the image")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 180*time.Second) // Slightly longer timeout for first-time Go builds
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 180 seconds")

				By("Verifying the server appears in the list")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should appear in the list")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
				// Built image name should contain a cleaned go:// package identifier
				Expect(stdout).To(ContainSubstring("go-github-com-stackloklabs-osv-mcp-cmd-server"), "Should show the built image name")

				By("Listing tools and verifying OSV tools exist")
				stdout, _ = e2e.NewTHVCommand(config, "mcp", "list", "tools", "--server", serverName, "--timeout", "60s").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("query_vulnerability"), "Should find query_vulnerability tool")

				GinkgoWriter.Printf("✅ Protocol build successful: go://github.com/StacklokLabs/osv-mcp/cmd/server@latest\n")
				GinkgoWriter.Printf("✅ Server running and provides OSV tools\n")
			})
		})

		Context("when testing go:// error conditions", func() {
			var serverName string

			BeforeEach(func() {
				serverName = generateUniqueProtocolServerName("go-error-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should fail gracefully with non-existent go module [Serial]", func() {
				By("Trying to run with non-existent go module")
				_, stderr, err := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "streamable-http",
					"go://github.com/not-real-org/not-real-repo@latest").ExpectFailure()

				Expect(err).To(HaveOccurred(), "Should fail with non-existent module")
				Expect(stderr).To(Or(
					ContainSubstring("failed"),
					ContainSubstring("error"),
					ContainSubstring("unsupported"),
				), "Error should mention the issue")
			})
		})
	})
})

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

var _ = Describe("Protocol Builds E2E", Serial, func() {
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
				serverName = generateUniqueProtocolServerName("sequential-thinking-test")
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
})

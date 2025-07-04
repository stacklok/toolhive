package e2e_test

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("TimeStreamableHttpMcpServer", Serial, func() {
	var config *e2e.TestConfig

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Context("when starting the time server with streamable-http proxy", func() {
		var serverName string

		BeforeEach(func() {
			serverName = generateUniqueServerName("time-streamable-test")
		})

		AfterEach(func() {
			if config.CleanupAfter {
				err := e2e.StopAndRemoveMCPServer(config, serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
			}
		})

		It("should respond to a single get_time request", func() {
			By("Starting the time MCP server with streamable-http proxy")
			e2e.NewTHVCommand(config, "run",
				"--name", serverName,
				"--proxy-mode", "streamable-http",
				"time").ExpectSuccess()

			By("Waiting for the server to be running")
			err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("Getting the server URL")
			serverURL, err := e2e.GetMCPServerURL(config, serverName)
			Expect(err).ToNot(HaveOccurred())

			// Patch: Use /messages for streamable-http
			serverURL = strings.Replace(serverURL, "/sse", "/messages", 1)
			if idx := strings.Index(serverURL, "#"); idx != -1 {
				serverURL = serverURL[:idx]
			}

			By("Waiting for MCP server to be ready")
			err = e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("Creating MCP client and initializing connection")
			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = mcpClient.Initialize(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("Calling get_time tool")
			mcpClient.ExpectToolExists(ctx, "get_time")
			result := mcpClient.ExpectToolCall(ctx, "get_time", nil)
			Expect(result.Content).ToNot(BeEmpty(), "Should return the current time")
		})
	})
})

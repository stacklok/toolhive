package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("OSV MCP Server with Authorization", Serial, func() {
	var config *e2e.TestConfig

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Describe("Running OSV MCP server with Cedar authorization", func() {
		Context("when authorization allows only one tool call for anybody", Ordered, func() {
			var serverName string
			var authzConfigPath string
			var mcpClient *e2e.MCPClientHelper
			var serverURL string
			var cancel context.CancelFunc

			BeforeAll(func() {
				serverName = generateUniqueServerName("osv-authz-test")

				// Create a temporary authorization config file
				// This policy allows anybody to call only the query_vulnerability tool
				authzConfig := `{
  "version": "1.0",
  "type": "cedarv1",
  "cedar": {
    "policies": [
      "permit(principal, action == Action::\"call_tool\", resource == Tool::\"query_vulnerability\");"
    ],
    "entities_json": "[]"
  }
}`

				// Write the config to a temporary file
				tempDir, err := os.MkdirTemp("", "osv-authz-test")
				Expect(err).ToNot(HaveOccurred())

				authzConfigPath = filepath.Join(tempDir, "authz-config.json")
				err = os.WriteFile(authzConfigPath, []byte(authzConfig), 0644)
				Expect(err).ToNot(HaveOccurred())

				// Start ONE server for ALL tests in this context
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--authz-config", authzConfigPath,
					"osv").ExpectSuccess()

				err = e2e.WaitForMCPServer(config, serverName, 30*time.Second)
				Expect(err).ToNot(HaveOccurred())

				// Get server URL
				serverURL, err = e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				err = e2e.WaitForMCPServerReady(config, serverURL, 60*time.Second)
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
					// Clean up the shared server after all tests
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")

					// Clean up the temporary config file
					if authzConfigPath != "" {
						os.RemoveAll(filepath.Dir(authzConfigPath))
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
					"version":      "4.17.15", // Known vulnerable version
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

				GinkgoWriter.Printf("Expected authorization failure for unauthorized tool: %v\n", err)

				By("Attempting to call another unauthorized tool - get_vulnerability")
				arguments = map[string]interface{}{
					"id": "GHSA-vqj2-4v8m-8vrq",
				}

				// This should also fail because get_vulnerability is not authorized
				_, err = mcpClient.CallTool(ctx, "get_vulnerability", arguments)
				Expect(err).To(HaveOccurred(), "Should fail to call unauthorized tool")

				GinkgoWriter.Printf("Expected authorization failure for get_vulnerability: %v\n", err)
			})
		})
	})
})

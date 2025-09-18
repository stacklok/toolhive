package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("OSV MCP Server with Authorization", Label("middleware", "authz", "sse", "e2e"), Serial, func() {
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

				// Start ONE server for ALL tests in this context with metrics enabled
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--authz-config", authzConfigPath,
					"--otel-enable-prometheus-metrics-path",
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

			It("should show authorization metrics in Prometheus endpoint [Serial]", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				By("Making both authorized and unauthorized requests to generate metrics")

				// Make an authorized request
				authorizedArgs := map[string]interface{}{
					"package_name": "lodash",
					"ecosystem":    "npm",
					"version":      "4.17.15",
				}
				result := mcpClient.ExpectToolCall(ctx, "query_vulnerability", authorizedArgs)
				Expect(result.Content).ToNot(BeEmpty(), "Should return vulnerability information")
				GinkgoWriter.Printf("Authorized request completed successfully\n")

				// Make unauthorized requests
				unauthorizedArgs := map[string]interface{}{
					"queries": []map[string]interface{}{
						{
							"package_name": "lodash",
							"ecosystem":    "npm",
							"version":      "4.17.15",
						},
					},
				}
				_, err := mcpClient.CallTool(ctx, "query_vulnerabilities_batch", unauthorizedArgs)
				Expect(err).To(HaveOccurred(), "Should fail to call unauthorized tool")
				GinkgoWriter.Printf("Unauthorized request correctly denied\n")

				// Make another unauthorized request
				unauthorizedArgs2 := map[string]interface{}{
					"id": "GHSA-vqj2-4v8m-8vrq",
				}
				_, err = mcpClient.CallTool(ctx, "get_vulnerability", unauthorizedArgs2)
				Expect(err).To(HaveOccurred(), "Should fail to call unauthorized tool")
				GinkgoWriter.Printf("Second unauthorized request correctly denied\n")

				By("Fetching Prometheus metrics to verify authorization statistics")

				// Extract the port from the server URL and construct metrics URL
				metricsURL, err := extractMetricsURL(serverURL)
				Expect(err).ToNot(HaveOccurred(), "Should be able to construct metrics URL")

				GinkgoWriter.Printf("Fetching metrics from: %s\n", metricsURL)

				// Fetch metrics with retry logic
				var metricsBody string
				Eventually(func() error {
					resp, err := http.Get(metricsURL)
					if err != nil {
						return fmt.Errorf("failed to fetch metrics: %w", err)
					}
					defer resp.Body.Close()

					if resp.StatusCode != http.StatusOK {
						return fmt.Errorf("metrics endpoint returned status %d", resp.StatusCode)
					}

					bodyBytes, err := io.ReadAll(resp.Body)
					if err != nil {
						return fmt.Errorf("failed to read metrics response: %w", err)
					}

					metricsBody = string(bodyBytes)
					return nil
				}, 10*time.Second, 1*time.Second).Should(Succeed(), "Should be able to fetch metrics")

				By("Analyzing metrics for authorization patterns")

				GinkgoWriter.Printf("Metrics response length: %d bytes\n", len(metricsBody))

				// Look for ToolHive-specific metrics
				Expect(metricsBody).To(ContainSubstring("toolhive_mcp_requests_total"),
					"Should contain ToolHive MCP request counter")

				// Parse and verify metrics contain both success and error status codes
				successCount := extractMetricValue(metricsBody, "toolhive_mcp_requests_total", "status=\"success\"")
				errorCount := extractMetricValue(metricsBody, "toolhive_mcp_requests_total", "status=\"error\"")

				GinkgoWriter.Printf("Success requests: %d\n", successCount)
				GinkgoWriter.Printf("Error requests: %d\n", errorCount)

				// We should have at least 1 successful request (authorized) and 2 error requests (unauthorized)
				Expect(successCount).To(BeNumerically(">=", 1),
					"Should have at least 1 successful request")
				Expect(errorCount).To(BeNumerically(">=", 2),
					"Should have at least 2 error requests (authorization denials)")

				// Look for specific status codes
				status200Count := extractMetricValue(metricsBody, "toolhive_mcp_requests_total", "status_code=\"200\"")
				status403Count := extractMetricValue(metricsBody, "toolhive_mcp_requests_total", "status_code=\"403\"")

				GinkgoWriter.Printf("HTTP 200 responses: %d\n", status200Count)
				GinkgoWriter.Printf("HTTP 403 responses: %d\n", status403Count)

				// We should see 403 responses for authorization denials
				Expect(status403Count).To(BeNumerically(">=", 2),
					"Should have at least 2 HTTP 403 responses for authorization denials")

				// Look for tool-specific metrics
				if strings.Contains(metricsBody, "toolhive_mcp_tool_calls_total") {
					toolCallsCount := extractMetricValue(metricsBody, "toolhive_mcp_tool_calls_total", "tool=\"query_vulnerability\"")
					GinkgoWriter.Printf("Tool calls for query_vulnerability: %d\n", toolCallsCount)

					Expect(toolCallsCount).To(BeNumerically(">=", 1),
						"Should have at least 1 successful tool call for query_vulnerability")
				}

				By("Verifying server name is included in metrics")
				Expect(metricsBody).To(ContainSubstring(fmt.Sprintf("server=\"%s\"", serverName)),
					"Metrics should include the server name")

				GinkgoWriter.Printf("✅ Authorization metrics verification completed successfully\n")
				GinkgoWriter.Printf("📊 Metrics show proper tracking of authorized vs unauthorized requests\n")
			})
		})
	})
})

// Helper functions for metrics analysis

// extractMetricsURL constructs the metrics URL from the server URL
func extractMetricsURL(serverURL string) (string, error) {
	// Parse the server URL to extract host and port
	// serverURL format: http://localhost:PORT/sse#servername
	parts := strings.Split(serverURL, ":")
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid server URL format: %s", serverURL)
	}

	// The metrics are exposed on the same host and port at /metrics path
	host := parts[1][2:] // Remove "//" prefix
	portAndPath := parts[2]

	// Extract just the port (remove /sse#servername part)
	portParts := strings.Split(portAndPath, "/")
	if len(portParts) < 1 {
		return "", fmt.Errorf("invalid server URL format: %s", serverURL)
	}
	port := portParts[0]

	metricsURL := fmt.Sprintf("http://%s:%s/metrics", host, port)

	return metricsURL, nil
}

// extractMetricValue parses Prometheus metrics text and extracts the value for a specific metric with labels
func extractMetricValue(metricsBody, metricName, labelFilter string) int {
	lines := strings.Split(metricsBody, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip comments and empty lines
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		// Check if this line contains our metric
		if !strings.HasPrefix(line, metricName) {
			continue
		}

		// Check if the line contains our label filter
		if labelFilter != "" && !strings.Contains(line, labelFilter) {
			continue
		}

		// Extract the value (last part after space)
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			valueStr := parts[len(parts)-1]
			if value, err := strconv.Atoi(valueStr); err == nil {
				return value
			}
			// Try parsing as float and convert to int
			if valueFloat, err := strconv.ParseFloat(valueStr, 64); err == nil {
				return int(valueFloat)
			}
		}
	}

	return 0
}

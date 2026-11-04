// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/stacklok/toolhive/pkg/diagnostics"
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
				serverName = e2e.GenerateUniqueServerName("osv-authz-test")

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

				By("Snapshotting metrics baseline before making requests")

				metricsURL, err := extractMetricsURL(serverURL)
				Expect(err).ToNot(HaveOccurred(), "Should be able to construct metrics URL")
				GinkgoWriter.Printf("Fetching metrics from: %s\n", metricsURL)

				beforeFamilies, _ := scrapeMetrics(metricsURL)

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
				_, err = mcpClient.CallTool(ctx, "query_vulnerabilities_batch", unauthorizedArgs)
				Expect(err).To(HaveOccurred(), "Should fail to call unauthorized tool")
				GinkgoWriter.Printf("Unauthorized request correctly denied\n")

				// Make another unauthorized request
				unauthorizedArgs2 := map[string]interface{}{
					"id": "GHSA-vqj2-4v8m-8vrq",
				}
				// The go-sdk client records the first 403 as a write error and fails every
				// later call on the same connection with "client is closing", without
				// sending it. Use a fresh client so the second denial reaches the server.
				freshClient, err := e2e.NewMCPClientForSSE(config, serverURL)
				Expect(err).ToNot(HaveOccurred())
				defer freshClient.Close()
				Expect(freshClient.Initialize(ctx)).To(Succeed())

				_, err = freshClient.CallTool(ctx, "get_vulnerability", unauthorizedArgs2)
				Expect(err).To(HaveOccurred(), "Should fail to call unauthorized tool")
				GinkgoWriter.Printf("Second unauthorized request correctly denied\n")

				By("Fetching Prometheus metrics to verify authorization statistics")

				afterFamilies, afterBody := scrapeMetrics(metricsURL)
				GinkgoWriter.Printf("Metrics response length: %d bytes\n", len(afterBody))

				By("Analyzing per-tool metric deltas for authorization patterns")

				Expect(afterBody).To(ContainSubstring("toolhive_mcp_requests_total"),
					"Should contain ToolHive MCP request counter")

				// Each tool is its own series, so sum all matches and compare against the
				// baseline: the server is shared with earlier specs in this container.
				requestDelta := func(tool, status, statusCode string) (float64, string) {
					labels := map[string]string{
						"mcp_method":      "tools/call",
						"mcp_resource_id": tool,
						"status":          status,
					}
					if statusCode != "" {
						labels["status_code"] = statusCode
					}
					delta := sumCounter(afterFamilies, requestsMetric, labels) -
						sumCounter(beforeFamilies, requestsMetric, labels)
					series := fmt.Sprintf("tool=%s status=%s status_code=%q delta=%v\n  before: %v\n  after:  %v",
						tool, status, statusCode, delta,
						matchingSeries(beforeFamilies, requestsMetric, labels),
						matchingSeries(afterFamilies, requestsMetric, labels))
					GinkgoWriter.Printf("%s\n", series)
					return delta, series
				}

				authorizedDelta, authorizedSeries := requestDelta("query_vulnerability", "success", "")
				Expect(authorizedDelta).To(BeNumerically(">=", 1),
					"Should have at least 1 successful request for the authorized tool:\n%s", authorizedSeries)

				for _, tool := range []string{"query_vulnerabilities_batch", "get_vulnerability"} {
					deniedDelta, deniedSeries := requestDelta(tool, "error", "403")
					Expect(deniedDelta).To(BeNumerically(">=", 1),
						"Should have at least 1 HTTP 403 error request for the denied tool:\n%s", deniedSeries)
				}

				// Look for tool-specific metrics
				if metricFamily(afterFamilies, toolCallsMetric) != nil {
					toolLabels := map[string]string{"tool": "query_vulnerability"}
					toolCallsDelta := sumCounter(afterFamilies, toolCallsMetric, toolLabels) -
						sumCounter(beforeFamilies, toolCallsMetric, toolLabels)
					GinkgoWriter.Printf("Tool calls delta for query_vulnerability: %v (series after: %v)\n",
						toolCallsDelta, matchingSeries(afterFamilies, toolCallsMetric, toolLabels))

					Expect(toolCallsDelta).To(BeNumerically(">=", 1),
						"Should have at least 1 successful tool call for query_vulnerability")
				}

				By("Verifying server name is included in metrics")
				Expect(afterBody).To(ContainSubstring(fmt.Sprintf("server=\"%s\"", serverName)),
					"Metrics should include the server name")

				GinkgoWriter.Printf("✅ Authorization metrics verification completed successfully\n")
				GinkgoWriter.Printf("📊 Metrics show proper tracking of authorized vs unauthorized requests\n")
			})
		})
	})
})

// Helper functions for metrics analysis

// extractMetricsURL constructs the metrics URL from the server URL.
//
// Metrics are served on the diagnostics listener, not on the transport port the
// server URL points at, so only the host is reused here. See pkg/diagnostics.
//
// This assumes the listener bound diagnostics.DefaultPort. That holds on a CI
// runner, where nothing else is on 9464, but the listener does fall back to
// another port when it is taken — so on a busy machine this points at the wrong
// endpoint and the assertions read empty metrics. There is no way to discover
// the resolved port today; exposing it in workload status is the follow-up that
// makes this deterministic.
func extractMetricsURL(serverURL string) (string, error) {
	// Parse the server URL to extract the host
	// serverURL format: http://localhost:PORT/sse#servername
	parts := strings.Split(serverURL, ":")
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid server URL format: %s", serverURL)
	}

	host := parts[1][2:] // Remove "//" prefix

	metricsURL := fmt.Sprintf("http://%s:%d%s", host, diagnostics.DefaultPort, diagnostics.MetricsPath)

	return metricsURL, nil
}

const (
	requestsMetric  = "toolhive_mcp_requests_total"
	toolCallsMetric = "toolhive_mcp_tool_calls_total"
)

// scrapeMetrics fetches the metrics endpoint, retrying until it responds, and
// returns the parsed families together with the raw body.
func scrapeMetrics(metricsURL string) (map[string]*dto.MetricFamily, string) {
	GinkgoHelper()

	var (
		families map[string]*dto.MetricFamily
		body     string
	)
	Eventually(func() error {
		resp, err := http.Get(metricsURL)
		if err != nil {
			return fmt.Errorf("failed to fetch metrics: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, resp.Body)
			return fmt.Errorf("metrics endpoint returned status %d", resp.StatusCode)
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read metrics response: %w", err)
		}

		parser := expfmt.NewTextParser(model.UTF8Validation)
		parsed, err := parser.TextToMetricFamilies(strings.NewReader(string(bodyBytes)))
		if err != nil {
			return fmt.Errorf("failed to parse metrics response: %w", err)
		}

		families = parsed
		body = string(bodyBytes)
		return nil
	}, 10*time.Second, 1*time.Second).Should(Succeed(), "Should be able to fetch metrics")

	return families, body
}

// metricFamily looks up a family by name, also accepting the name without a
// "_total" suffix in case the parser or exporter strips it.
func metricFamily(families map[string]*dto.MetricFamily, name string) *dto.MetricFamily {
	if family, ok := families[name]; ok {
		return family
	}
	return families[strings.TrimSuffix(name, "_total")]
}

// sumCounter adds up every counter series in the named family whose labels
// include all of wantLabels.
func sumCounter(families map[string]*dto.MetricFamily, name string, wantLabels map[string]string) float64 {
	family := metricFamily(families, name)
	if family == nil {
		return 0
	}
	var sum float64
	for _, metric := range family.Metric {
		if metric.Counter != nil && prometheusLabelsMatch(metric, wantLabels) {
			sum += metric.Counter.GetValue()
		}
	}
	return sum
}

// matchingSeries renders the series counted by sumCounter as "labels=value"
// strings for failure diagnostics.
func matchingSeries(families map[string]*dto.MetricFamily, name string, wantLabels map[string]string) []string {
	family := metricFamily(families, name)
	if family == nil {
		return nil
	}
	var series []string
	for _, metric := range family.Metric {
		if metric.Counter == nil || !prometheusLabelsMatch(metric, wantLabels) {
			continue
		}
		pairs := make([]string, 0, len(metric.Label))
		for _, pair := range metric.Label {
			pairs = append(pairs, fmt.Sprintf("%s=%q", pair.GetName(), pair.GetValue()))
		}
		series = append(series, fmt.Sprintf("{%s}=%v", strings.Join(pairs, ","), metric.Counter.GetValue()))
	}
	return series
}

func prometheusLabelsMatch(metric *dto.Metric, want map[string]string) bool {
	labels := make(map[string]string, len(metric.Label))
	for _, pair := range metric.Label {
		labels[pair.GetName()] = pair.GetValue()
	}
	for name, value := range want {
		if labels[name] != value {
			return false
		}
	}
	return true
}

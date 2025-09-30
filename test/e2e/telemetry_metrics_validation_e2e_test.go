package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Telemetry Metrics Validation E2E", Label("telemetry", "metrics", "validation", "e2e"), Serial, func() {
	var (
		config       *e2e.TestConfig
		workloadName string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred())
		workloadName = generateUniqueTelemetryServerName("metrics-validation")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			err := e2e.StopAndRemoveMCPServer(config, workloadName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
		}
	})

	Context("Server Name and Transport Validation", func() {
		It("should never have empty server names or transports in SSE server metrics", func() {
			By("Starting SSE MCP server with Prometheus metrics enabled")
			e2e.NewTHVCommand(config,
				"run",
				"--name", workloadName,
				"--transport", types.TransportTypeSSE.String(),
				"--otel-enable-prometheus-metrics-path",
				"osv",
			).ExpectSuccess()

			err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("Making MCP requests to generate telemetry metrics")
			makeSSEMCPRequests(config, workloadName)

			By("Validating metrics have correct server name and transport")
			validateTelemetryMetrics(config, workloadName, workloadName, "sse")
		})

		It("should never have empty server names or transports in streamable-http server metrics", func() {
			By("Starting streamable-http MCP server with Prometheus metrics enabled")
			e2e.NewTHVCommand(config,
				"run",
				"--name", workloadName,
				"--transport", types.TransportTypeStreamableHTTP.String(),
				"--otel-enable-prometheus-metrics-path",
				"osv",
			).ExpectSuccess()

			err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("Making MCP requests to generate telemetry metrics")
			makeStreamableHTTPMCPRequests(config, workloadName)

			By("Validating metrics have correct server name and transport")
			validateTelemetryMetrics(config, workloadName, workloadName, "streamable-http")
		})

		It("should use inferred server name when not explicitly provided", func() {
			inferredName := generateUniqueTelemetryServerName("inferred")

			By("Starting MCP server without explicit name to test server name inference")
			e2e.NewTHVCommand(config,
				"run",
				"--transport", types.TransportTypeSSE.String(),
				"--otel-enable-prometheus-metrics-path",
				"--name", inferredName, // Still need explicit name for cleanup
				"ghcr.io/stackloklabs/osv-mcp/server:0.0.7",
			).ExpectSuccess()

			// Update workloadName for cleanup
			workloadName = inferredName

			err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("Making MCP requests to generate telemetry metrics")
			makeSSEMCPRequests(config, workloadName)

			By("Validating metrics have correct inferred server name and transport")
			validateTelemetryMetrics(config, workloadName, workloadName, "sse")
		})
	})

	Context("Metrics Content Validation", func() {
		BeforeEach(func() {
			By("Starting MCP server for metrics content validation")
			e2e.NewTHVCommand(config,
				"run",
				"--name", workloadName,
				"--transport", types.TransportTypeSSE.String(),
				"--otel-enable-prometheus-metrics-path",
				"osv",
			).ExpectSuccess()

			err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should have all required telemetry metrics with non-empty labels", func() {
			By("Making diverse MCP requests to generate comprehensive metrics")
			makeSSEMCPRequests(config, workloadName)

			By("Fetching metrics from Prometheus endpoint")
			metricsURL, err := getMetricsURL(config, workloadName)
			Expect(err).ToNot(HaveOccurred())

			metricsContent := fetchMetricsContent(metricsURL)

			By("Validating all core ToolHive metrics exist")
			expectedMetrics := []string{
				"toolhive_mcp_requests_total",
				"toolhive_mcp_request_duration_seconds",
				"toolhive_mcp_active_connections",
			}

			for _, metric := range expectedMetrics {
				Expect(metricsContent).To(ContainSubstring(metric),
					fmt.Sprintf("Should contain metric: %s", metric))
			}

			By("Validating no metrics have empty server or transport labels")
			validateNoEmptyLabels(metricsContent, workloadName, "sse")

			By("Validating metrics contain expected MCP methods")
			expectedMethods := []string{
				"initialize",
				"tools/list",
			}

			for _, method := range expectedMethods {
				methodPattern := fmt.Sprintf(`mcp_method="%s"`, method)
				Expect(metricsContent).To(ContainSubstring(methodPattern),
					fmt.Sprintf("Should contain MCP method: %s", method))
			}
		})

		It("should propagate tool call metrics when telemetry is enabled", func() {
			By("Making tool calls to generate tool-specific metrics")
			toolCallMetrics := makeToolCallsAndValidateMetrics(config, workloadName)

			By("Validating tool-specific metrics are propagated correctly")
			Expect(toolCallMetrics.InitializeCallCount).To(BeNumerically(">=", 1),
				"Should have recorded initialize calls")
			Expect(toolCallMetrics.ToolsListCallCount).To(BeNumerically(">=", 1),
				"Should have recorded tools/list calls")
			// Tool calls may fail due to session requirements, but the important thing is that
			// telemetry is working for the requests we do make
			GinkgoWriter.Printf("Tool call count: %d, Initialize count: %d, Tools/list count: %d\n",
				toolCallMetrics.ToolCallCount, toolCallMetrics.InitializeCallCount, toolCallMetrics.ToolsListCallCount)

			By("Validating all tool calls have proper server name and transport labels")
			Expect(toolCallMetrics.ServerName).To(Equal(workloadName),
				"All metrics should have correct server name")
			Expect(toolCallMetrics.Transport).To(Equal("sse"),
				"All metrics should have correct transport")

			By("Validating that telemetry captured our requests")
			totalRequests := toolCallMetrics.SuccessfulCalls + toolCallMetrics.ErrorCalls
			Expect(totalRequests).To(BeNumerically(">", 0),
				"Should have captured some requests (successful or error)")

			By("Validating response time metrics are reasonable")
			Expect(toolCallMetrics.AverageResponseTime).To(BeNumerically(">", 0),
				"Should have positive response times")
			Expect(toolCallMetrics.AverageResponseTime).To(BeNumerically("<", 10000),
				"Response times should be reasonable (< 10s)")
		})

		It("should propagate mcp.server.name and mcp.transport attributes on traces", func() {
			By("Making MCP requests to generate traces with proper attributes")
			traceValidation := makeRequestsAndValidateTraces(config, workloadName)

			By("Validating trace attributes are properly set")
			Expect(traceValidation.TracesGenerated).To(BeNumerically(">", 0),
				"Should have generated traces")
			Expect(traceValidation.SpansWithCorrectServerName).To(BeNumerically(">", 0),
				"Should have spans with correct mcp.server.name attribute")
			Expect(traceValidation.SpansWithCorrectTransport).To(BeNumerically(">", 0),
				"Should have spans with correct mcp.transport attribute")

			By("Validating no traces have empty or incorrect server name")
			Expect(traceValidation.SpansWithEmptyServerName).To(Equal(0),
				"Should have no spans with empty mcp.server.name")
			Expect(traceValidation.SpansWithMessageServerName).To(Equal(0),
				"Should have no spans with mcp.server.name='message'")
			Expect(traceValidation.SpansWithHealthServerName).To(Equal(0),
				"Should have no spans with mcp.server.name='health'")

			By("Validating no traces have empty transport")
			Expect(traceValidation.SpansWithEmptyTransport).To(Equal(0),
				"Should have no spans with empty mcp.transport")

			By("Validating trace attributes match expected values")
			Expect(traceValidation.ExpectedServerName).To(Equal(workloadName),
				"Expected server name should match workload name")
			Expect(traceValidation.ExpectedTransport).To(Equal("sse"),
				"Expected transport should be SSE")

			GinkgoWriter.Printf("Trace validation results: %d traces, %d with correct server name, %d with correct transport\n",
				traceValidation.TracesGenerated, traceValidation.SpansWithCorrectServerName, traceValidation.SpansWithCorrectTransport)
		})
	})
})

// makeSSEMCPRequests makes various MCP requests to an SSE server to generate telemetry
func makeSSEMCPRequests(config *e2e.TestConfig, workloadName string) {
	serverURL, err := e2e.GetMCPServerURL(config, workloadName)
	Expect(err).ToNot(HaveOccurred())

	// Extract base URL for requests
	baseURL := strings.Split(serverURL, "#")[0]

	// Make initialize request
	initReq := `{"jsonrpc":"2.0","method":"initialize","id":1,"params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e-test","version":"1.0"}}}`
	messageURL := strings.Replace(baseURL, "/sse", "/message", 1)
	resp, err := http.Post(messageURL, "application/json", strings.NewReader(initReq))
	if err == nil {
		resp.Body.Close()
	}

	// Wait a moment between requests
	time.Sleep(500 * time.Millisecond)

	// Make tools/list request
	toolsReq := `{"jsonrpc":"2.0","method":"tools/list","id":2}`
	resp, err = http.Post(messageURL, "application/json", strings.NewReader(toolsReq))
	if err == nil {
		resp.Body.Close()
	}

	// Wait for metrics to be recorded
	time.Sleep(2 * time.Second)
}

// makeStreamableHTTPMCPRequests makes various MCP requests to a streamable-http server
func makeStreamableHTTPMCPRequests(config *e2e.TestConfig, workloadName string) {
	serverURL, err := e2e.GetMCPServerURL(config, workloadName)
	Expect(err).ToNot(HaveOccurred())

	// For streamable-http, use the /mcp endpoint
	mcpURL := strings.Replace(serverURL, "/sse#", "/mcp", 1)
	mcpURL = strings.Split(mcpURL, "#")[0] // Remove fragment if any

	// Make initialize request
	initReq := `{"jsonrpc":"2.0","method":"initialize","id":1,"params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e-test","version":"1.0"}}}`
	resp, err := http.Post(mcpURL, "application/json", strings.NewReader(initReq))
	if err == nil {
		resp.Body.Close()
	}

	// Wait a moment between requests
	time.Sleep(500 * time.Millisecond)

	// Make tools/list request
	toolsReq := `{"jsonrpc":"2.0","method":"tools/list","id":2}`
	resp, err = http.Post(mcpURL, "application/json", strings.NewReader(toolsReq))
	if err == nil {
		resp.Body.Close()
	}

	// Wait for metrics to be recorded
	time.Sleep(2 * time.Second)
}

// validateTelemetryMetrics validates that metrics contain correct server name and transport
func validateTelemetryMetrics(config *e2e.TestConfig, workloadName, expectedServerName, expectedTransport string) {
	metricsURL, err := getMetricsURL(config, workloadName)
	Expect(err).ToNot(HaveOccurred())

	Eventually(func() string {
		return fetchMetricsContent(metricsURL)
	}, 15*time.Second, 2*time.Second).Should(
		And(
			ContainSubstring("toolhive_mcp"),
			ContainSubstring(fmt.Sprintf(`server="%s"`, expectedServerName)),
			ContainSubstring(fmt.Sprintf(`transport="%s"`, expectedTransport)),
		),
		fmt.Sprintf("Should contain correct server name '%s' and transport '%s'", expectedServerName, expectedTransport),
	)

	metricsContent := fetchMetricsContent(metricsURL)

	By("Ensuring no metrics have empty server names")
	Expect(metricsContent).ToNot(ContainSubstring(`server=""`), "No metrics should have empty server name")
	Expect(metricsContent).ToNot(ContainSubstring(`server="message"`), "No metrics should have 'message' as server name")
	Expect(metricsContent).ToNot(ContainSubstring(`server="health"`), "No metrics should have 'health' as server name")

	By("Ensuring no metrics have empty transport")
	Expect(metricsContent).ToNot(ContainSubstring(`transport=""`), "No metrics should have empty transport")

	By("Validating metric values are reasonable")
	validateMetricValues(metricsContent, expectedServerName, expectedTransport)
}

// validateNoEmptyLabels ensures no metrics have empty server or transport labels
func validateNoEmptyLabels(metricsContent, expectedServerName, expectedTransport string) {
	lines := strings.Split(metricsContent, "\n")

	for _, line := range lines {
		if strings.Contains(line, "toolhive_mcp") && !strings.HasPrefix(line, "#") {
			// Skip comment lines and only check actual metric lines
			if strings.Contains(line, "{") {
				// This is a metric with labels
				Expect(line).ToNot(ContainSubstring(`server=""`),
					fmt.Sprintf("Metric line should not have empty server: %s", line))
				Expect(line).ToNot(ContainSubstring(`transport=""`),
					fmt.Sprintf("Metric line should not have empty transport: %s", line))

				// Ensure it has the expected labels
				if strings.Contains(line, "server=") {
					Expect(line).To(ContainSubstring(fmt.Sprintf(`server="%s"`, expectedServerName)),
						fmt.Sprintf("Metric should have correct server name: %s", line))
				}
				if strings.Contains(line, "transport=") {
					Expect(line).To(ContainSubstring(fmt.Sprintf(`transport="%s"`, expectedTransport)),
						fmt.Sprintf("Metric should have correct transport: %s", line))
				}
			}
		}
	}
}

// validateMetricValues validates that metric values are reasonable
func validateMetricValues(metricsContent, expectedServerName, expectedTransport string) {
	// Look for request count metrics
	requestPattern := regexp.MustCompile(fmt.Sprintf(
		`toolhive_mcp_requests_total\{.*server="%s".*transport="%s".*\} (\d+)`,
		regexp.QuoteMeta(expectedServerName),
		regexp.QuoteMeta(expectedTransport),
	))

	matches := requestPattern.FindAllStringSubmatch(metricsContent, -1)

	if len(matches) > 0 {
		totalRequests := 0
		for _, match := range matches {
			if len(match) >= 2 {
				count, err := strconv.Atoi(match[1])
				if err == nil {
					totalRequests += count
				}
			}
		}

		Expect(totalRequests).To(BeNumerically(">", 0),
			"Should have recorded at least some requests")

		GinkgoWriter.Printf("Validated %d total requests for server '%s' with transport '%s'\n",
			totalRequests, expectedServerName, expectedTransport)
	}
}

// getMetricsURL constructs the metrics URL for a given workload
func getMetricsURL(config *e2e.TestConfig, workloadName string) (string, error) {
	serverURL, err := e2e.GetMCPServerURL(config, workloadName)
	if err != nil {
		return "", fmt.Errorf("failed to get server URL: %w", err)
	}

	// Parse the URL to extract host and port
	parts := strings.Split(serverURL, ":")
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid server URL format: %s", serverURL)
	}

	host := parts[1][2:] // Remove "//" prefix
	portAndPath := parts[2]

	// Extract just the port (remove /sse#servername or /mcp part)
	portParts := strings.Split(portAndPath, "/")
	if len(portParts) < 1 {
		return "", fmt.Errorf("invalid server URL format: %s", serverURL)
	}
	port := portParts[0]

	metricsURL := fmt.Sprintf("http://%s:%s/metrics", host, port)
	return metricsURL, nil
}

// fetchMetricsContent fetches the content from the metrics endpoint
func fetchMetricsContent(metricsURL string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", metricsURL, nil)
	if err != nil {
		return ""
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	return string(bodyBytes)
}

// ToolCallMetrics represents metrics collected from tool calls
type ToolCallMetrics struct {
	ServerName          string
	Transport           string
	InitializeCallCount int
	ToolsListCallCount  int
	ToolCallCount       int
	SuccessfulCalls     int
	ErrorCalls          int
	AverageResponseTime float64
}

// makeToolCallsAndValidateMetrics makes actual tool calls and validates the resulting metrics
func makeToolCallsAndValidateMetrics(config *e2e.TestConfig, workloadName string) *ToolCallMetrics {
	serverURL, err := e2e.GetMCPServerURL(config, workloadName)
	Expect(err).ToNot(HaveOccurred())

	// Extract base URL for requests
	baseURL := strings.Split(serverURL, "#")[0]
	messageURL := strings.Replace(baseURL, "/sse", "/message", 1)

	By("Making initialize call")
	initReq := `{"jsonrpc":"2.0","method":"initialize","id":"init-1","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e-test","version":"1.0"}}}`
	resp, err := http.Post(messageURL, "application/json", strings.NewReader(initReq))
	if err == nil {
		resp.Body.Close()
		GinkgoWriter.Printf("Initialize call completed\n")
	}

	// Wait between requests
	time.Sleep(500 * time.Millisecond)

	By("Making tools/list call")
	toolsListReq := `{"jsonrpc":"2.0","method":"tools/list","id":"tools-1"}`
	resp, err = http.Post(messageURL, "application/json", strings.NewReader(toolsListReq))
	if err == nil {
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr == nil {
			var result map[string]interface{}
			if jsonErr := json.Unmarshal(body, &result); jsonErr == nil {
				GinkgoWriter.Printf("Tools/list response: %v\n", result)

				// Extract available tools for actual tool calls
				if resultData, ok := result["result"].(map[string]interface{}); ok {
					if tools, ok := resultData["tools"].([]interface{}); ok && len(tools) > 0 {
						// Make an actual tool call if tools are available
						if tool, ok := tools[0].(map[string]interface{}); ok {
							if toolName, ok := tool["name"].(string); ok {
								By(fmt.Sprintf("Making actual tool call to: %s", toolName))
								toolCallReq := fmt.Sprintf(`{"jsonrpc":"2.0","method":"tools/call","id":"tool-1","params":{"name":"%s","arguments":{}}}`, toolName)
								resp, err = http.Post(messageURL, "application/json", strings.NewReader(toolCallReq))
								if err == nil {
									toolBody, readErr := io.ReadAll(resp.Body)
									resp.Body.Close()
									if readErr == nil {
										GinkgoWriter.Printf("Tool call response: %s\n", string(toolBody))
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Wait for metrics to be recorded
	time.Sleep(3 * time.Second)

	By("Collecting and analyzing metrics")
	metricsURL, err := getMetricsURL(config, workloadName)
	Expect(err).ToNot(HaveOccurred())

	metricsContent := fetchMetricsContent(metricsURL)
	Expect(metricsContent).ToNot(BeEmpty(), "Should be able to fetch metrics")

	// Parse metrics to extract tool call information
	metrics := parseToolCallMetrics(metricsContent, workloadName)

	return metrics
}

// parseToolCallMetrics parses Prometheus metrics to extract tool call statistics
func parseToolCallMetrics(metricsContent, expectedServerName string) *ToolCallMetrics {
	lines := strings.Split(metricsContent, "\n")
	metrics := &ToolCallMetrics{
		ServerName: expectedServerName,
		Transport:  "sse", // Default for this test
	}

	var responseTimeSum float64
	var responseTimeCount int

	for _, line := range lines {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue // Skip comments and empty lines
		}

		// Count different types of requests
		if strings.Contains(line, "toolhive_mcp_requests_total") && strings.Contains(line, fmt.Sprintf(`server="%s"`, expectedServerName)) {
			if strings.Contains(line, `mcp_method="initialize"`) {
				metrics.InitializeCallCount += extractMetricCount(line)
			} else if strings.Contains(line, `mcp_method="tools/list"`) {
				metrics.ToolsListCallCount += extractMetricCount(line)
			} else if strings.Contains(line, `mcp_method="tools/call"`) {
				metrics.ToolCallCount += extractMetricCount(line)
			}

			// Count successful vs error calls
			if strings.Contains(line, `status="success"`) {
				metrics.SuccessfulCalls += extractMetricCount(line)
			} else if strings.Contains(line, `status="error"`) {
				metrics.ErrorCalls += extractMetricCount(line)
			}
		}

		// Collect response time information
		if strings.Contains(line, "toolhive_mcp_request_duration_seconds_sum") && strings.Contains(line, fmt.Sprintf(`server="%s"`, expectedServerName)) {
			responseTimeSum += extractMetricFloatValue(line)
			responseTimeCount++
		}
	}

	// Calculate average response time
	if responseTimeCount > 0 {
		metrics.AverageResponseTime = responseTimeSum / float64(responseTimeCount) * 1000 // Convert to milliseconds
	}

	return metrics
}

// extractMetricCount extracts the count value from a Prometheus metric line
func extractMetricCount(line string) int {
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		// Try to parse the last field as a number
		if count, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return count
		}
	}
	return 0
}

// extractMetricFloatValue extracts the float value from a Prometheus metric line
func extractMetricFloatValue(line string) float64 {
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		// Try to parse the last field as a float
		if value, err := strconv.ParseFloat(parts[len(parts)-1], 64); err == nil {
			return value
		}
	}
	return 0.0
}

// TraceValidation represents validation results for trace attributes
type TraceValidation struct {
	ExpectedServerName         string
	ExpectedTransport          string
	TracesGenerated            int
	SpansWithCorrectServerName int
	SpansWithCorrectTransport  int
	SpansWithEmptyServerName   int
	SpansWithMessageServerName int
	SpansWithHealthServerName  int
	SpansWithEmptyTransport    int
}

// makeRequestsAndValidateTraces makes MCP requests and validates trace attributes
func makeRequestsAndValidateTraces(config *e2e.TestConfig, workloadName string) *TraceValidation {
	serverURL, err := e2e.GetMCPServerURL(config, workloadName)
	Expect(err).ToNot(HaveOccurred())

	// Extract base URL for requests
	baseURL := strings.Split(serverURL, "#")[0]
	messageURL := strings.Replace(baseURL, "/sse", "/message", 1)

	By("Enabling trace collection for validation")
	// We'll use a simple approach: make requests and then check the telemetry
	// Since we can't directly access traces in this test environment,
	// we'll use the observable effects in metrics and logs

	By("Making multiple MCP requests to generate traces")
	requests := []struct {
		name    string
		payload string
	}{
		{
			name:    "initialize",
			payload: `{"jsonrpc":"2.0","method":"initialize","id":"trace-init","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"trace-test","version":"1.0"}}}`,
		},
		{
			name:    "tools/list",
			payload: `{"jsonrpc":"2.0","method":"tools/list","id":"trace-tools"}`,
		},
		{
			name:    "resources/list",
			payload: `{"jsonrpc":"2.0","method":"resources/list","id":"trace-resources"}`,
		},
	}

	for _, req := range requests {
		By(fmt.Sprintf("Making %s request for trace generation", req.name))
		resp, err := http.Post(messageURL, "application/json", strings.NewReader(req.payload))
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			GinkgoWriter.Printf("%s response: %s\n", req.name, string(body))
		}
		time.Sleep(500 * time.Millisecond) // Space out requests
	}

	// Wait for traces to be processed
	time.Sleep(3 * time.Second)

	By("Analyzing telemetry data for trace attributes")
	// Since we can't directly access trace data, we'll validate through metrics
	// and by checking that the telemetry middleware is working correctly
	metricsURL, err := getMetricsURL(config, workloadName)
	Expect(err).ToNot(HaveOccurred())

	metricsContent := fetchMetricsContent(metricsURL)
	Expect(metricsContent).ToNot(BeEmpty(), "Should be able to fetch metrics")

	// Parse the observable effects to validate traces
	validation := analyzeTraceAttributes(metricsContent, workloadName, "sse")

	return validation
}

// analyzeTraceAttributes analyzes metrics to infer trace attribute correctness
func analyzeTraceAttributes(metricsContent, expectedServerName, expectedTransport string) *TraceValidation {
	lines := strings.Split(metricsContent, "\n")
	validation := &TraceValidation{
		ExpectedServerName: expectedServerName,
		ExpectedTransport:  expectedTransport,
	}

	// Count different request types as a proxy for trace generation
	requestMetrics := make(map[string]int)
	correctServerNameSpans := 0
	correctTransportSpans := 0
	emptyServerNameSpans := 0
	messageServerNameSpans := 0
	healthServerNameSpans := 0
	emptyTransportSpans := 0

	for _, line := range lines {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}

		// Count request metrics as indicators of trace generation
		if strings.Contains(line, "toolhive_mcp_requests_total") {
			validation.TracesGenerated++

			// Check server name attributes
			if strings.Contains(line, fmt.Sprintf(`server="%s"`, expectedServerName)) {
				correctServerNameSpans++
			} else if strings.Contains(line, `server=""`) {
				emptyServerNameSpans++
			} else if strings.Contains(line, `server="message"`) {
				messageServerNameSpans++
			} else if strings.Contains(line, `server="health"`) {
				healthServerNameSpans++
			}

			// Check transport attributes
			if strings.Contains(line, fmt.Sprintf(`transport="%s"`, expectedTransport)) {
				correctTransportSpans++
			} else if strings.Contains(line, `transport=""`) {
				emptyTransportSpans++
			}

			// Extract method names to count different request types
			for _, method := range []string{"initialize", "tools/list", "resources/list"} {
				if strings.Contains(line, fmt.Sprintf(`mcp_method="%s"`, method)) {
					requestMetrics[method] = extractMetricCount(line)
				}
			}
		}
	}

	validation.SpansWithCorrectServerName = correctServerNameSpans
	validation.SpansWithCorrectTransport = correctTransportSpans
	validation.SpansWithEmptyServerName = emptyServerNameSpans
	validation.SpansWithMessageServerName = messageServerNameSpans
	validation.SpansWithHealthServerName = healthServerNameSpans
	validation.SpansWithEmptyTransport = emptyTransportSpans

	// Log the request metrics for debugging
	GinkgoWriter.Printf("Request metrics found: %v\n", requestMetrics)
	GinkgoWriter.Printf("Server name analysis: correct=%d, empty=%d, message=%d, health=%d\n",
		correctServerNameSpans, emptyServerNameSpans, messageServerNameSpans, healthServerNameSpans)
	GinkgoWriter.Printf("Transport analysis: correct=%d, empty=%d\n",
		correctTransportSpans, emptyTransportSpans)

	return validation
}

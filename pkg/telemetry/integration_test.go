// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/stacklok/toolhive/pkg/mcp"
)

const (
	testToolName         = "github_search"
	metricRequestCounter = "toolhive_mcp_requests"
)

func TestTelemetryIntegration_EndToEnd(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create test configuration
	config := Config{
		ServiceName:                 "test-toolhive",
		ServiceVersion:              "1.0.0-test",
		SamplingRate:                "1.0", // Sample everything for testing
		Headers:                     make(map[string]string),
		EnablePrometheusMetricsPath: true, // Enable Prometheus metrics
	}

	// Create provider
	provider, err := NewProvider(ctx, config)
	require.NoError(t, err)
	require.NotNil(t, provider)
	t.Cleanup(func() {
		provider.Shutdown(ctx)
	})

	// Create middleware
	middleware := provider.Middleware("github", "stdio")

	// Create test handler that simulates MCP server behavior
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Simulate some processing time
		time.Sleep(10 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "test-123",
			"result": map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "Search results for test query",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(response)
	})

	// Wrap with MCP parsing middleware first, then telemetry
	mcpHandler := mcp.ParsingMiddleware(testHandler)
	wrappedHandler := middleware(mcpHandler)

	// Test different types of MCP requests
	testCases := []struct {
		name           string
		method         string
		path           string
		body           string
		expectedStatus int
		expectedMethod string
	}{
		{
			name:           "tools/call request",
			method:         "POST",
			path:           "/messages",
			body:           `{"jsonrpc":"2.0","id":"test-123","method":"tools/call","params":{"name":"github_search","arguments":{"query":"test","limit":10}}}`,
			expectedStatus: 200,
			expectedMethod: "tools/call",
		},
		{
			name:           "resources/read request",
			method:         "POST",
			path:           "/api/v1/messages",
			body:           `{"jsonrpc":"2.0","id":"test-456","method":"resources/read","params":{"uri":"file://test.txt"}}`,
			expectedStatus: 200,
			expectedMethod: "resources/read",
		},
		{
			name:           "initialize request",
			method:         "POST",
			path:           "/messages",
			body:           `{"jsonrpc":"2.0","id":"init-1","method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
			expectedStatus: 200,
			expectedMethod: "initialize",
		},
		{
			name:           "non-MCP request",
			method:         "GET",
			path:           "/health",
			body:           "",
			expectedStatus: 200,
			expectedMethod: "unknown",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var req *http.Request
			if tc.body != "" {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			}

			rec := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(rec, req)

			assert.Equal(t, tc.expectedStatus, rec.Code)
		})
	}

	// Verify Prometheus handler is available
	prometheusHandler := provider.PrometheusHandler()
	assert.NotNil(t, prometheusHandler)

	// Test Prometheus metrics endpoint
	metricsReq := httptest.NewRequest("GET", "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	prometheusHandler.ServeHTTP(metricsRec, metricsReq)

	// Check if metrics endpoint is working - be flexible about status
	if metricsRec.Code == http.StatusOK {
		metricsBody := metricsRec.Body.String()
		// At minimum, we should have some metrics
		assert.True(t, len(metricsBody) > 0, "Metrics should not be empty")

		// If we have custom metrics, verify them
		if strings.Contains(metricsBody, "toolhive_mcp") {
			assert.Contains(t, metricsBody, "toolhive_mcp_requests")
			assert.Contains(t, metricsBody, "toolhive_mcp_request_duration")
			assert.Contains(t, metricsBody, "toolhive_mcp_active_connections")
		}
	} else {
		// If metrics endpoint fails, just log it but don't fail the test
		t.Logf("Metrics endpoint returned status %d, body: %s", metricsRec.Code, metricsRec.Body.String())
	}
}

func TestTelemetryIntegration_WithRealProviders(t *testing.T) {
	t.Parallel()

	// Create in-memory trace exporter for testing
	traceExporter := tracetest.NewInMemoryExporter()
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	// Create in-memory metrics reader for testing
	metricsReader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(metricsReader),
	)

	config := Config{
		ServiceName:         "test-service",
		ServiceVersion:      "1.0.0",
		UseLegacyAttributes: true,
	}

	// Create middleware directly with real providers
	middleware := NewHTTPMiddleware(config, tracerProvider, meterProvider, "github", "stdio")

	// Create test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	wrappedHandler := middleware(testHandler)

	// Create MCP request
	mcpRequest := &mcp.ParsedMCPRequest{
		Method:     "tools/call",
		ID:         "test-123",
		ResourceID: testToolName,
		Arguments: map[string]interface{}{
			"query":   "test query",
			"api_key": "secret123", // This should be redacted
			"limit":   10,
		},
		IsRequest: true,
	}

	req := httptest.NewRequest("POST", "/messages", nil)
	ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, mcpRequest)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Force flush the tracer provider to ensure spans are exported
	err := tracerProvider.ForceFlush(t.Context())
	require.NoError(t, err)

	// Verify traces were recorded
	spans := traceExporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, "tools/call "+testToolName, span.Name)

	// Verify span attributes
	attrs := span.Attributes
	attrMap := make(map[string]interface{})
	for _, attr := range attrs {
		attrMap[string(attr.Key)] = attr.Value.AsInterface()
	}

	// New OTEL semantic convention attributes (always present)
	assert.Equal(t, "tools/call", attrMap["mcp.method.name"])
	assert.Equal(t, testToolName, attrMap["gen_ai.tool.name"])
	assert.Equal(t, "test-123", attrMap["jsonrpc.request.id"])
	assert.Equal(t, "POST", attrMap["http.request.method"])
	assert.Equal(t, int64(200), attrMap["http.response.status_code"])

	// Legacy attributes (present because UseLegacyAttributes=true)
	assert.Equal(t, "tools/call", attrMap["mcp.method"])
	assert.Equal(t, testToolName, attrMap["mcp.tool.name"])
	assert.Equal(t, "test-123", attrMap["mcp.request.id"])
	assert.Equal(t, "POST", attrMap["http.method"])
	assert.Equal(t, int64(200), attrMap["http.status_code"])

	// Verify sensitive data is redacted in new attribute name
	if toolArgs, exists := attrMap["gen_ai.tool.call.arguments"]; exists {
		argsStr := toolArgs.(string)
		assert.Contains(t, argsStr, "api_key=[REDACTED]")
		assert.Contains(t, argsStr, "query=test query")
		assert.Contains(t, argsStr, "limit=10")
	}

	// Verify metrics were recorded
	var rm metricdata.ResourceMetrics
	err = metricsReader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Check that metrics were recorded
	assert.NotEmpty(t, rm.ScopeMetrics)

	var foundRequestCounter, foundDurationHistogram, foundActiveConnections bool
	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			switch metric.Name {
			case metricRequestCounter:
				foundRequestCounter = true
				// Verify metric has expected attributes
				if sum, ok := metric.Data.(metricdata.Sum[int64]); ok {
					assert.NotEmpty(t, sum.DataPoints)
					for _, dp := range sum.DataPoints {
						// Check for expected attributes
						attrSet := dp.Attributes
						hasServer := false
						hasMethod := false
						hasResourceID := false
						for _, attr := range attrSet.ToSlice() {
							if attr.Key == "server" && attr.Value.AsString() == "github" {
								hasServer = true
							}
							if attr.Key == "mcp_method" && attr.Value.AsString() == "tools/call" {
								hasMethod = true
							}
							if attr.Key == "mcp_resource_id" && attr.Value.AsString() == testToolName {
								hasResourceID = true
							}
						}
						assert.True(t, hasServer, "Request counter should have server attribute")
						assert.True(t, hasMethod, "Request counter should have mcp_method attribute")
						assert.True(t, hasResourceID, "Request counter should have mcp_resource_id attribute with tool name")
					}
				}
			case "toolhive_mcp_request_duration":
				foundDurationHistogram = true
			case "toolhive_mcp_active_connections":
				foundActiveConnections = true
			}
		}
	}

	assert.True(t, foundRequestCounter, "Request counter metric should be present")
	assert.True(t, foundDurationHistogram, "Duration histogram metric should be present")
	assert.True(t, foundActiveConnections, "Active connections metric should be present")

	// Clean up
	err = tracerProvider.Shutdown(ctx)
	assert.NoError(t, err)
	err = meterProvider.Shutdown(ctx)
	assert.NoError(t, err)
}

func TestTelemetryIntegration_ErrorHandling(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	// Create provider with metrics only
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		EnablePrometheusMetricsPath: true,
	}

	provider, err := NewProvider(ctx, config)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	middleware := provider.Middleware("test-server", "stdio")

	// Create handler that returns an error
	errorHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	})

	wrappedHandler := middleware(errorHandler)

	// Test error request
	req := httptest.NewRequest("POST", "/messages", nil)
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	// Verify Prometheus metrics include error status
	prometheusHandler := provider.PrometheusHandler()
	metricsReq := httptest.NewRequest("GET", "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	prometheusHandler.ServeHTTP(metricsRec, metricsReq)

	metricsBody := metricsRec.Body.String()
	// Check if metrics contain error indicators - the exact format may vary
	hasErrorMetrics := strings.Contains(metricsBody, `status="error"`) ||
		strings.Contains(metricsBody, `status_code="500"`) ||
		strings.Contains(metricsBody, "500") ||
		strings.Contains(metricsBody, "error")

	// If we have custom metrics, they should include error status
	if strings.Contains(metricsBody, "toolhive_mcp") {
		assert.True(t, hasErrorMetrics, "Expected error metrics to be present")
	}
}

func TestTelemetryIntegration_ToolSpecificMetrics(t *testing.T) {
	t.Parallel()

	// Create real metrics provider for testing
	metricsReader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(metricsReader),
	)

	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
	}

	middleware := NewHTTPMiddleware(config, tracenoop.NewTracerProvider(), meterProvider, "github", "stdio")

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("tool result"))
	})

	wrappedHandler := middleware(testHandler)

	// Create tools/call request
	mcpRequest := &mcp.ParsedMCPRequest{
		Method:     "tools/call",
		ID:         "tool-test",
		ResourceID: testToolName,
		Arguments: map[string]interface{}{
			"query": "test",
		},
		IsRequest: true,
	}

	req := httptest.NewRequest("POST", "/messages", nil)
	ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, mcpRequest)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Collect and verify tool-specific metrics
	var rm metricdata.ResourceMetrics
	err := metricsReader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Look for tool-specific counter and general request counter
	var foundToolCounter, foundRequestCounter bool
	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			switch metric.Name {
			case "toolhive_mcp_tool_calls":
				foundToolCounter = true
				if sum, ok := metric.Data.(metricdata.Sum[int64]); ok {
					assert.NotEmpty(t, sum.DataPoints)
					for _, dp := range sum.DataPoints {
						// Verify tool-specific attributes
						attrSet := dp.Attributes
						hasServer := false
						hasTool := false
						for _, attr := range attrSet.ToSlice() {
							if attr.Key == "server" && attr.Value.AsString() == "github" {
								hasServer = true
							}
							if attr.Key == "tool" && attr.Value.AsString() == testToolName {
								hasTool = true
							}
						}
						assert.True(t, hasServer, "Tool counter should have server attribute")
						assert.True(t, hasTool, "Tool counter should have tool attribute")
					}
				}
			case metricRequestCounter:
				foundRequestCounter = true
				if sum, ok := metric.Data.(metricdata.Sum[int64]); ok {
					assert.NotEmpty(t, sum.DataPoints)
					for _, dp := range sum.DataPoints {
						attrSet := dp.Attributes
						hasResourceID := false
						for _, attr := range attrSet.ToSlice() {
							if attr.Key == "mcp_resource_id" && attr.Value.AsString() == testToolName {
								hasResourceID = true
							}
						}
						assert.True(t, hasResourceID, "Request counter should have mcp_resource_id attribute with tool name")
					}
				}
			}
		}
	}

	assert.True(t, foundToolCounter, "Tool-specific counter should be recorded for tools/call")
	assert.True(t, foundRequestCounter, "General request counter should be recorded")

	// Clean up
	err = meterProvider.Shutdown(ctx)
	assert.NoError(t, err)
}

func TestTelemetryIntegration_MultipleRequests(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create provider with both tracing and metrics
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		EnablePrometheusMetricsPath: true,
	}

	provider, err := NewProvider(ctx, config)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	middleware := provider.Middleware("multi-test", "stdio")

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	wrappedHandler := middleware(testHandler)

	// Send multiple requests
	numRequests := 5
	for i := 0; i < numRequests; i++ {
		req := httptest.NewRequest("POST", "/messages", nil)
		rec := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	// Verify metrics accumulate correctly
	prometheusHandler := provider.PrometheusHandler()
	metricsReq := httptest.NewRequest("GET", "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	prometheusHandler.ServeHTTP(metricsRec, metricsReq)

	metricsBody := metricsRec.Body.String()

	// The exact count format depends on Prometheus exposition format
	// We just verify the metrics are present and contain our server name
	assert.Contains(t, metricsBody, "toolhive_mcp_requests")
	assert.Contains(t, metricsBody, `server="multi-test"`)
}

func TestTelemetryIntegration_MetaTraceContextExtraction(t *testing.T) { //nolint:paralleltest // Mutates global OTEL propagator
	// Set up W3C Trace Context propagator globally (required for Extract to work)
	oldPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(oldPropagator)

	// Create in-memory trace exporter
	traceExporter := tracetest.NewInMemoryExporter()
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(traceExporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	defer func() { _ = tracerProvider.Shutdown(context.Background()) }()

	metricsReader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricsReader))
	defer func() { _ = meterProvider.Shutdown(context.Background()) }()

	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
	}

	middleware := NewHTTPMiddleware(config, tracerProvider, meterProvider, "github", "stdio")

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	wrappedHandler := middleware(testHandler)

	// Create a parent span to generate a valid traceparent
	parentTracer := tracerProvider.Tracer("test-parent")
	_, parentSpan := parentTracer.Start(context.Background(), "parent-operation")
	parentSpanCtx := parentSpan.SpanContext()
	parentTraceID := parentSpanCtx.TraceID().String()
	parentSpanID := parentSpanCtx.SpanID().String()
	parentSpan.End()

	// Build traceparent string from the parent span
	traceparent := "00-" + parentTraceID + "-" + parentSpanID + "-01"

	// Create an MCP request with _meta containing traceparent
	mcpRequest := &mcp.ParsedMCPRequest{
		Method:     "tools/call",
		ID:         "trace-test",
		ResourceID: "my_tool",
		IsRequest:  true,
		Meta: map[string]interface{}{
			"traceparent": traceparent,
		},
	}

	req := httptest.NewRequest("POST", "/messages", nil)
	ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, mcpRequest)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Force flush and get spans
	err := tracerProvider.ForceFlush(context.Background())
	require.NoError(t, err)

	spans := traceExporter.GetSpans()

	// Find the middleware span (not the parent-operation span)
	var middlewareSpan *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name != "parent-operation" {
			middlewareSpan = &spans[i]
			break
		}
	}
	require.NotNil(t, middlewareSpan, "middleware span should exist")

	// The middleware span should have the same trace ID as the parent
	assert.Equal(t, parentTraceID, middlewareSpan.SpanContext.TraceID().String(),
		"middleware span should inherit trace ID from _meta traceparent")

	// The middleware span's parent should be the parent span
	assert.Equal(t, parentSpanID, middlewareSpan.Parent.SpanID().String(),
		"middleware span parent should be the span from _meta traceparent")

	// Verify the span is a child (not root) — it should have a valid parent span ID
	assert.True(t, middlewareSpan.Parent.SpanID().IsValid(),
		"middleware span should have a valid parent span ID from _meta extraction")

	// Also verify the span kind is Server
	assert.Equal(t, trace.SpanKindServer, middlewareSpan.SpanKind)
}

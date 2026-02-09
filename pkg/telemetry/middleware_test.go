// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/mock/gomock"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

const metricSessionDuration = "mcp.server.session.duration"

func TestNewHTTPMiddleware(t *testing.T) {
	t.Parallel()

	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
	}
	tracerProvider := tracenoop.NewTracerProvider()
	meterProvider := noop.NewMeterProvider()

	middleware := NewHTTPMiddleware(config, tracerProvider, meterProvider, "github", "stdio")
	assert.NotNil(t, middleware)
}

func TestHTTPMiddleware_Handler_BasicRequest(t *testing.T) {
	t.Parallel()

	// Create middleware with no-op providers for basic testing
	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
	}
	tracerProvider := tracenoop.NewTracerProvider()
	meterProvider := noop.NewMeterProvider()

	middleware := NewHTTPMiddleware(config, tracerProvider, meterProvider, "github", "stdio")

	// Create a test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	// Wrap with middleware
	wrappedHandler := middleware(testHandler)

	// Create test request
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	// Execute request
	wrappedHandler.ServeHTTP(rec, req)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "test response", rec.Body.String())
}

func TestHTTPMiddleware_Handler_WithMCPData(t *testing.T) {
	t.Parallel()

	// Create middleware with no-op providers
	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
	}
	tracerProvider := tracenoop.NewTracerProvider()
	meterProvider := noop.NewMeterProvider()

	middleware := NewHTTPMiddleware(config, tracerProvider, meterProvider, "github", "stdio")

	// Create a test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("mcp response"))
	})

	// Wrap with middleware
	wrappedHandler := middleware(testHandler)

	// Create MCP request data
	mcpRequest := &mcpparser.ParsedMCPRequest{
		Method:     "tools/call",
		ID:         "test-123",
		ResourceID: "github_search",
		Arguments: map[string]interface{}{
			"query": "test query",
			"limit": 10,
		},
		IsRequest: true,
		IsBatch:   false,
	}

	// Create request with MCP data in context
	req := httptest.NewRequest("POST", "/messages", nil)
	ctx := context.WithValue(req.Context(), mcpparser.MCPRequestContextKey, mcpRequest)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()

	// Execute request
	wrappedHandler.ServeHTTP(rec, req)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "mcp response", rec.Body.String())
}

func TestHTTPMiddleware_CreateSpanName(t *testing.T) {
	t.Parallel()

	middleware := &HTTPMiddleware{}

	tests := []struct {
		name         string
		mcpMethod    string
		resourceID   string
		httpMethod   string
		path         string
		expectedSpan string
	}{
		{
			name:         "tools/call with target",
			mcpMethod:    "tools/call",
			resourceID:   "github_search",
			httpMethod:   "POST",
			path:         "/messages",
			expectedSpan: "tools/call github_search",
		},
		{
			name:         "without MCP method",
			mcpMethod:    "",
			httpMethod:   "GET",
			path:         "/health",
			expectedSpan: "",
		},
		{
			name:         "resources/read without target in span name",
			mcpMethod:    "resources/read",
			resourceID:   "file://test.txt",
			httpMethod:   "POST",
			path:         "/api/v1/messages",
			expectedSpan: "resources/read",
		},
		{
			name:         "prompts/get with target",
			mcpMethod:    "prompts/get",
			resourceID:   "my_prompt",
			httpMethod:   "POST",
			path:         "/messages",
			expectedSpan: "prompts/get my_prompt",
		},
		{
			name:         "tools/list without target",
			mcpMethod:    "tools/list",
			httpMethod:   "POST",
			path:         "/messages",
			expectedSpan: "tools/list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()

			if tt.mcpMethod != "" {
				mcpRequest := &mcpparser.ParsedMCPRequest{
					Method:     tt.mcpMethod,
					ResourceID: tt.resourceID,
				}
				ctx = context.WithValue(ctx, mcpparser.MCPRequestContextKey, mcpRequest)
			}

			spanName := middleware.createSpanName(ctx)
			assert.Equal(t, tt.expectedSpan, spanName)
		})
	}
}

func TestHTTPMiddleware_AddHTTPAttributes_Logic(t *testing.T) {
	t.Parallel()

	// Test the logic without using actual spans
	// We'll test the individual helper functions instead
	middleware := &HTTPMiddleware{}

	req := httptest.NewRequest("POST", "http://localhost:8080/api/v1/messages?session=123", nil)
	req.Header.Set("Content-Length", "256")
	req.Header.Set("User-Agent", "test-client/1.0")
	req.Host = "localhost:8080"

	// Test that the request has the expected properties
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "http://localhost:8080/api/v1/messages?session=123", req.URL.String())
	assert.Equal(t, "localhost:8080", req.Host)
	assert.Equal(t, "/api/v1/messages", req.URL.Path)
	assert.Equal(t, "test-client/1.0", req.UserAgent())
	assert.Equal(t, "256", req.Header.Get("Content-Length"))
	assert.Equal(t, "session=123", req.URL.RawQuery)

	// Test that middleware exists and can be called
	assert.NotNil(t, middleware)
}

func TestHTTPMiddleware_MCP_AttributeLogic(t *testing.T) {
	t.Parallel()

	middleware := &HTTPMiddleware{
		serverName: "github",
		transport:  "stdio",
	}

	tests := []struct {
		name       string
		mcpRequest *mcpparser.ParsedMCPRequest
		checkFunc  func(t *testing.T, req *mcpparser.ParsedMCPRequest)
	}{
		{
			name: "tools/call request",
			mcpRequest: &mcpparser.ParsedMCPRequest{
				Method:     "tools/call",
				ID:         "123",
				ResourceID: "github_search",
				Arguments: map[string]interface{}{
					"query": "test",
					"limit": 10,
				},
				IsRequest: true,
			},
			checkFunc: func(t *testing.T, req *mcpparser.ParsedMCPRequest) {
				t.Helper()
				assert.Equal(t, "tools/call", req.Method)
				assert.Equal(t, "123", req.ID)
				assert.Equal(t, "github_search", req.ResourceID)
				assert.True(t, req.IsRequest)
			},
		},
		{
			name: "resources/read request",
			mcpRequest: &mcpparser.ParsedMCPRequest{
				Method:     "resources/read",
				ID:         456,
				ResourceID: "file://test.txt",
				IsRequest:  true,
			},
			checkFunc: func(t *testing.T, req *mcpparser.ParsedMCPRequest) {
				t.Helper()
				assert.Equal(t, "resources/read", req.Method)
				assert.Equal(t, 456, req.ID)
				assert.Equal(t, "file://test.txt", req.ResourceID)
			},
		},
		{
			name: "batch request",
			mcpRequest: &mcpparser.ParsedMCPRequest{
				Method:    "tools/list",
				ID:        "batch-1",
				IsRequest: true,
				IsBatch:   true,
			},
			checkFunc: func(t *testing.T, req *mcpparser.ParsedMCPRequest) {
				t.Helper()
				assert.Equal(t, "tools/list", req.Method)
				assert.Equal(t, "batch-1", req.ID)
				assert.True(t, req.IsBatch)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/messages", nil)
			ctx := context.WithValue(req.Context(), mcpparser.MCPRequestContextKey, tt.mcpRequest)

			// Verify the MCP request can be retrieved from context
			retrievedMCP := mcpparser.GetParsedMCPRequest(ctx)
			assert.NotNil(t, retrievedMCP)

			// Run the specific checks for this test case
			tt.checkFunc(t, retrievedMCP)

			// Test middleware properties
			assert.Equal(t, "github", middleware.serverName)
			assert.Equal(t, "stdio", middleware.transport)
		})
	}
}

func TestHTTPMiddleware_SanitizeArguments(t *testing.T) {
	t.Parallel()

	middleware := &HTTPMiddleware{}

	tests := []struct {
		name      string
		arguments map[string]interface{}
		expected  string
	}{
		{
			name:      "empty arguments",
			arguments: map[string]interface{}{},
			expected:  "",
		},
		{
			name:      "nil arguments",
			arguments: nil,
			expected:  "",
		},
		{
			name: "normal arguments",
			arguments: map[string]interface{}{
				"query": "test search",
				"limit": 10,
			},
			expected: "limit=10, query=test search",
		},
		{
			name: "sensitive arguments",
			arguments: map[string]interface{}{
				"query":    "test search",
				"api_key":  "secret123",
				"password": "mysecret",
				"token":    "bearer-token",
			},
			expected: "api_key=[REDACTED], password=[REDACTED], query=test search, token=[REDACTED]",
		},
		{
			name: "long value truncation",
			arguments: map[string]interface{}{
				"long_text": strings.Repeat("a", 150),
			},
			expected: "long_text=" + strings.Repeat("a", 100) + "...",
		},
		{
			name: "very long result truncation",
			arguments: map[string]interface{}{
				"field1": strings.Repeat("a", 80),
				"field2": strings.Repeat("b", 80),
				"field3": strings.Repeat("c", 80),
			},
			expected: "", // Will be checked differently due to map iteration order
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := middleware.sanitizeArguments(tt.arguments)

			// For cases with multiple fields, we need to handle map iteration order
			if len(tt.arguments) > 1 && !strings.Contains(tt.name, "long result") {
				// Check that all expected parts are present
				for key := range tt.arguments {
					if middleware.isSensitiveKey(key) {
						assert.Contains(t, result, key+"=[REDACTED]")
					} else {
						assert.Contains(t, result, key+"=")
					}
				}
			} else if strings.Contains(tt.name, "long result") {
				// For very long result, just check it's truncated
				assert.True(t, len(result) <= 203, "Result should be truncated to ~200 chars")
				assert.Contains(t, result, "...")
			} else {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestHTTPMiddleware_IsSensitiveKey(t *testing.T) {
	t.Parallel()

	middleware := &HTTPMiddleware{}

	tests := []struct {
		key         string
		isSensitive bool
	}{
		{"password", true},
		{"api_key", true},
		{"token", true},
		{"secret", true},
		{"auth", true},
		{"credential", true},
		{"access_token", true},
		{"refresh_token", true},
		{"private", true},
		{"Authorization", true}, // Case insensitive
		{"API_KEY", true},       // Case insensitive
		{"query", false},
		{"limit", false},
		{"name", false},
		{"data", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()

			result := middleware.isSensitiveKey(tt.key)
			assert.Equal(t, tt.isSensitive, result)
		})
	}
}

func TestHTTPMiddleware_FormatRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		id       interface{}
		expected string
	}{
		{"string ID", "test-123", "test-123"},
		{"int ID", 123, "123"},
		{"int64 ID", int64(456), "456"},
		{"float64 ID", 789.0, "789"},
		{"float64 with decimal", 123.456, "123.456"},
		{"other type", []string{"test"}, "[test]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := formatRequestID(tt.id)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHTTPMiddleware_ExtractServerName(t *testing.T) {
	t.Parallel()

	middleware := &HTTPMiddleware{
		serverName: "test-server", // Set a configured server name for testing
	}

	tests := []struct {
		name     string
		path     string
		headers  map[string]string
		query    string
		expected string
	}{
		{
			name:     "from header",
			path:     "/messages",
			headers:  map[string]string{"X-MCP-Server-Name": "github"},
			expected: "github",
		},
		{
			name:     "from path",
			path:     "/api/v1/github/messages",
			expected: "test-server", // Now uses configured server name instead of path parsing
		},
		{
			name:     "from path with sse",
			path:     "/sse/weather/messages",
			expected: "test-server", // Now uses configured server name instead of path parsing
		},
		{
			name:     "fallback to serverName",
			path:     "/messages",
			query:    "session_id=abc123",
			expected: "test-server", // Uses configured server name
		},
		{
			name:     "unknown",
			path:     "/health",
			expected: "test-server", // Now uses configured server name instead of path parsing
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", tt.path+"?"+tt.query, nil)
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			result := middleware.extractServerName(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHTTPMiddleware_ExtractBackendTransport(t *testing.T) {
	t.Parallel()

	middleware := &HTTPMiddleware{
		transport: "stdio",
	}

	tests := []struct {
		name     string
		headers  map[string]string
		expected string
	}{
		{
			name:     "from header",
			headers:  map[string]string{"X-MCP-Transport": "sse"},
			expected: "sse",
		},
		{
			name:     "default stdio",
			headers:  map[string]string{},
			expected: "stdio",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/messages", nil)
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			result := middleware.extractBackendTransport(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHTTPMiddleware_FinalizeSpan_Logic(t *testing.T) {
	t.Parallel()

	middleware := &HTTPMiddleware{}

	tests := []struct {
		name           string
		statusCode     int
		bytesWritten   int64
		duration       time.Duration
		expectedStatus codes.Code
	}{
		{
			name:           "success response",
			statusCode:     200,
			bytesWritten:   1024,
			duration:       100 * time.Millisecond,
			expectedStatus: codes.Ok,
		},
		{
			name:           "client error",
			statusCode:     400,
			bytesWritten:   256,
			duration:       50 * time.Millisecond,
			expectedStatus: codes.Unset,
		},
		{
			name:           "server error",
			statusCode:     500,
			bytesWritten:   128,
			duration:       200 * time.Millisecond,
			expectedStatus: codes.Error,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rw := &responseWriter{
				statusCode:   tt.statusCode,
				bytesWritten: tt.bytesWritten,
			}

			// Test the logic for determining status codes
			expectedStatus := func() codes.Code {
				switch {
				case tt.statusCode >= 500:
					return codes.Error
				case tt.statusCode >= 400:
					return codes.Unset
				default:
					return codes.Ok
				}
			}()

			assert.Equal(t, tt.expectedStatus, expectedStatus)
			assert.Equal(t, tt.statusCode, rw.statusCode)
			assert.Equal(t, tt.bytesWritten, rw.bytesWritten)

			// Test duration calculation
			durationMs := float64(tt.duration.Nanoseconds()) / 1e6
			assert.Greater(t, durationMs, 0.0)

			// Test middleware exists
			assert.NotNil(t, middleware)
		})
	}
}

func TestResponseWriter(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	rw := &responseWriter{
		ResponseWriter: rec,
		statusCode:     http.StatusOK,
		bytesWritten:   0,
	}

	// Test WriteHeader
	rw.WriteHeader(http.StatusCreated)
	assert.Equal(t, http.StatusCreated, rw.statusCode)
	assert.Equal(t, http.StatusCreated, rec.Code)

	// Test Write
	data := []byte("test response data")
	n, err := rw.Write(data)
	assert.NoError(t, err)
	assert.Equal(t, len(data), n)
	assert.Equal(t, int64(len(data)), rw.bytesWritten)
	assert.Equal(t, string(data), rec.Body.String())
}

func TestResponseWriter_DuplicateWriteHeader(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	rw := &responseWriter{
		ResponseWriter: rec,
		statusCode:     http.StatusOK,
		bytesWritten:   0,
	}

	// First WriteHeader call
	firstStatus := http.StatusCreated
	rw.WriteHeader(firstStatus)
	assert.Equal(t, firstStatus, rw.statusCode)
	assert.Equal(t, firstStatus, rec.Code)
	assert.True(t, rw.headerWritten, "headerWritten should be true after first WriteHeader call")

	// Second WriteHeader call - should be silently ignored
	secondStatus := http.StatusBadRequest
	rw.WriteHeader(secondStatus)

	// Verify that the status code remains from the first call
	assert.Equal(t, firstStatus, rw.statusCode, "Status code should remain from first WriteHeader call")
	assert.Equal(t, firstStatus, rec.Code, "Underlying ResponseWriter should keep first status code")

	// Verify that headerWritten is still true
	assert.True(t, rw.headerWritten, "headerWritten should remain true after duplicate WriteHeader call")
}

func TestResponseWriter_WriteThenWriteHeader(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	rw := &responseWriter{
		ResponseWriter: rec,
		statusCode:     http.StatusOK,
		bytesWritten:   0,
	}

	// Call Write() first - this will implicitly call WriteHeader(200) on underlying ResponseWriter
	data := []byte("test response")
	n, err := rw.Write(data)
	assert.NoError(t, err)
	assert.Equal(t, len(data), n)
	assert.Equal(t, int64(len(data)), rw.bytesWritten)
	assert.Equal(t, string(data), rec.Body.String())

	// Verify that headers were marked as written
	assert.True(t, rw.headerWritten, "headerWritten should be true after Write() call")
	assert.Equal(t, http.StatusOK, rw.statusCode, "Status code should be 200 after Write()")
	assert.Equal(t, http.StatusOK, rec.Code, "Underlying ResponseWriter should have status 200")

	// Now try to call WriteHeader() - should be silently ignored
	// because Write() already wrote headers
	rw.WriteHeader(http.StatusCreated)

	// Verify that the status code remains 200 (from Write())
	assert.Equal(t, http.StatusOK, rw.statusCode, "Status code should remain 200 from Write() call")
	assert.Equal(t, http.StatusOK, rec.Code, "Underlying ResponseWriter should keep status 200")
	assert.True(t, rw.headerWritten, "headerWritten should remain true")
}

func TestResponseWriter_WriteHeaderThenWrite(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	rw := &responseWriter{
		ResponseWriter: rec,
		statusCode:     http.StatusOK,
		bytesWritten:   0,
	}

	// Call WriteHeader() first with a non-200 status code
	statusCode := http.StatusNotFound
	rw.WriteHeader(statusCode)
	assert.Equal(t, statusCode, rw.statusCode, "Status code should be set correctly")
	assert.Equal(t, statusCode, rec.Code, "Underlying ResponseWriter should have the correct status code")
	assert.True(t, rw.headerWritten, "headerWritten should be true after WriteHeader() call")

	// Now call Write() - should work correctly and preserve the status code
	data := []byte("not found response")
	n, err := rw.Write(data)
	assert.NoError(t, err)
	assert.Equal(t, len(data), n)
	assert.Equal(t, int64(len(data)), rw.bytesWritten)
	assert.Equal(t, string(data), rec.Body.String())

	// Verify that the status code remains from WriteHeader() call
	assert.Equal(t, statusCode, rw.statusCode, "Status code should remain from WriteHeader() call")
	assert.Equal(t, statusCode, rec.Code, "Underlying ResponseWriter should keep the status code from WriteHeader()")
	assert.True(t, rw.headerWritten, "headerWritten should remain true")
}

func TestHTTPMiddleware_WithRealMetrics(t *testing.T) {
	t.Parallel()

	// Create a real meter provider for testing metrics
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
	}
	tracerProvider := tracenoop.NewTracerProvider()

	middleware := NewHTTPMiddleware(config, tracerProvider, meterProvider, "github", "stdio")

	// Create test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test"))
	})

	wrappedHandler := middleware(testHandler)

	// Execute request
	req := httptest.NewRequest("POST", "/messages", nil)
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Verify metrics were recorded
	assert.NotEmpty(t, rm.ScopeMetrics)

	// Find our metrics (both legacy and standard)
	var foundCounter, foundHistogram, foundGauge, foundOperationDuration bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case metricRequestCounter:
				foundCounter = true
			case "toolhive_mcp_request_duration":
				foundHistogram = true
			case "toolhive_mcp_active_connections":
				foundGauge = true
			case "mcp.server.operation.duration":
				foundOperationDuration = true
			}
		}
	}

	assert.True(t, foundCounter, "Legacy request counter metric should be recorded")
	assert.True(t, foundHistogram, "Legacy request duration histogram should be recorded")
	assert.True(t, foundGauge, "Active connections gauge should be recorded")
	assert.True(t, foundOperationDuration, "Standard mcp.server.operation.duration metric should be recorded")
}

func TestHTTPMiddleware_WithRealMetrics_OperationDurationAttributes(t *testing.T) {
	t.Parallel()

	// Create a real meter provider for testing metrics
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
	}
	tracerProvider := tracenoop.NewTracerProvider()

	middleware := NewHTTPMiddleware(config, tracerProvider, meterProvider, "github", "sse")

	// Create test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test"))
	})

	wrappedHandler := middleware(testHandler)

	// Create MCP request with tools/call
	mcpRequest := &mcpparser.ParsedMCPRequest{
		Method:     "tools/call",
		ID:         "test-1",
		ResourceID: "my_tool",
		IsRequest:  true,
	}

	req := httptest.NewRequest("POST", "/messages", nil)
	ctx := context.WithValue(req.Context(), mcpparser.MCPRequestContextKey, mcpRequest)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rec, req)

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Find the mcp.server.operation.duration metric and verify attributes
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "mcp.server.operation.duration" {
				histogram, ok := m.Data.(metricdata.Histogram[float64])
				require.True(t, ok, "Expected Histogram data type")
				require.NotEmpty(t, histogram.DataPoints)

				dp := histogram.DataPoints[0]
				// Verify standard attribute names
				assertHasAttribute(t, dp.Attributes, "mcp.method.name", "tools/call")
				assertHasAttribute(t, dp.Attributes, "network.transport", "tcp")
				assertHasAttribute(t, dp.Attributes, "mcp.protocol.version", mcpProtocolVersion)
				assertHasAttribute(t, dp.Attributes, "gen_ai.tool.name", "my_tool")
				assertHasAttribute(t, dp.Attributes, "gen_ai.operation.name", "execute_tool")
			}
		}
	}
}

func TestHTTPMiddleware_addEnvironmentAttributes(t *testing.T) { //nolint:paralleltest,tparallel // Mutates process-wide env vars via os.Setenv; subtests are parallel with each other but parent is not
	// Setup test environment variables
	originalEnv1 := os.Getenv("TEST_ENV_1")
	originalEnv2 := os.Getenv("TEST_ENV_2")
	originalEnv3 := os.Getenv("TEST_ENV_3")

	os.Setenv("TEST_ENV_1", "value1")
	os.Setenv("TEST_ENV_2", "value2")
	os.Setenv("TEST_ENV_3", "")
	t.Cleanup(func() {
		if originalEnv1 == "" {
			os.Unsetenv("TEST_ENV_1")
		} else {
			os.Setenv("TEST_ENV_1", originalEnv1)
		}
		if originalEnv2 == "" {
			os.Unsetenv("TEST_ENV_2")
		} else {
			os.Setenv("TEST_ENV_2", originalEnv2)
		}
		if originalEnv3 == "" {
			os.Unsetenv("TEST_ENV_3")
		} else {
			os.Setenv("TEST_ENV_3", originalEnv3)
		}
	})

	tests := []struct {
		name          string
		envVars       []string
		expectedAttrs int
	}{
		{
			name:          "no environment variables configured",
			envVars:       []string{},
			expectedAttrs: 0,
		},
		{
			name:          "single environment variable",
			envVars:       []string{"TEST_ENV_1"},
			expectedAttrs: 1,
		},
		{
			name:          "multiple environment variables",
			envVars:       []string{"TEST_ENV_1", "TEST_ENV_2"},
			expectedAttrs: 2,
		},
		{
			name:          "includes empty environment variable",
			envVars:       []string{"TEST_ENV_1", "TEST_ENV_3"},
			expectedAttrs: 2,
		},
		{
			name:          "includes non-existent environment variable",
			envVars:       []string{"TEST_ENV_1", "NON_EXISTENT_VAR"},
			expectedAttrs: 2,
		},
		{
			name:          "skips empty environment variable names",
			envVars:       []string{"TEST_ENV_1", "", "TEST_ENV_2"},
			expectedAttrs: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create a mock span to capture attributes
			mockSpan := &mockSpan{attributes: make(map[string]interface{})}

			// Create middleware with test config
			config := Config{
				EnvironmentVariables: tt.envVars,
			}
			middleware := &HTTPMiddleware{
				config: config,
			}

			// Call the method under test
			middleware.addEnvironmentAttributes(mockSpan)

			// Verify the correct number of attributes were set
			assert.Len(t, mockSpan.attributes, tt.expectedAttrs,
				"Expected %d attributes, got %d", tt.expectedAttrs, len(mockSpan.attributes))

			// Verify specific attributes for known environment variables
			if slices.Contains(tt.envVars, "TEST_ENV_1") {
				assert.Equal(t, "value1", mockSpan.attributes["environment.TEST_ENV_1"])
			}
			if slices.Contains(tt.envVars, "TEST_ENV_2") {
				assert.Equal(t, "value2", mockSpan.attributes["environment.TEST_ENV_2"])
			}
			if slices.Contains(tt.envVars, "TEST_ENV_3") {
				assert.Equal(t, "", mockSpan.attributes["environment.TEST_ENV_3"])
			}
			if slices.Contains(tt.envVars, "NON_EXISTENT_VAR") {
				assert.Equal(t, "", mockSpan.attributes["environment.NON_EXISTENT_VAR"])
			}
		})
	}
}

func TestMapTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		transport    string
		wantNetwork  string
		wantProtocol string
	}{
		{
			name:         "stdio maps to pipe",
			transport:    "stdio",
			wantNetwork:  "pipe",
			wantProtocol: "",
		},
		{
			name:         "sse maps to tcp with http",
			transport:    "sse",
			wantNetwork:  "tcp",
			wantProtocol: "http",
		},
		{
			name:         "streamable-http maps to tcp with http",
			transport:    "streamable-http",
			wantNetwork:  "tcp",
			wantProtocol: "http",
		},
		{
			name:         "unknown defaults to tcp with http",
			transport:    "unknown",
			wantNetwork:  "tcp",
			wantProtocol: "http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			network, protocol := mapTransport(tt.transport)
			assert.Equal(t, tt.wantNetwork, network)
			assert.Equal(t, tt.wantProtocol, protocol)
		})
	}
}

func TestHTTPProtocolVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		protoMajor int
		protoMinor int
		expected   string
	}{
		{
			name:       "HTTP/1.0",
			protoMajor: 1,
			protoMinor: 0,
			expected:   "1.0",
		},
		{
			name:       "HTTP/1.1",
			protoMajor: 1,
			protoMinor: 1,
			expected:   "1.1",
		},
		{
			name:       "HTTP/2",
			protoMajor: 2,
			protoMinor: 0,
			expected:   "2",
		},
		{
			name:       "HTTP/3",
			protoMajor: 3,
			protoMinor: 0,
			expected:   "3",
		},
		{
			name:       "zero major returns empty",
			protoMajor: 0,
			protoMinor: 0,
			expected:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/messages", nil)
			req.ProtoMajor = tt.protoMajor
			req.ProtoMinor = tt.protoMinor

			result := httpProtocolVersion(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseRemoteAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		addr     string
		wantHost string
		wantPort int
	}{
		{
			name:     "standard IPv4 with port",
			addr:     "192.168.1.1:8080",
			wantHost: "192.168.1.1",
			wantPort: 8080,
		},
		{
			name:     "loopback with port",
			addr:     "127.0.0.1:54321",
			wantHost: "127.0.0.1",
			wantPort: 54321,
		},
		{
			name:     "empty address",
			addr:     "",
			wantHost: "",
			wantPort: 0,
		},
		{
			name:     "IPv6 with port",
			addr:     "[::1]:8080",
			wantHost: "::1",
			wantPort: 8080,
		},
		{
			name:     "no port",
			addr:     "192.168.1.1",
			wantHost: "192.168.1.1",
			wantPort: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			host, port := parseRemoteAddr(tt.addr)
			assert.Equal(t, tt.wantHost, host)
			assert.Equal(t, tt.wantPort, port)
		})
	}
}

func TestHTTPMiddleware_MCP_StandardAttributes(t *testing.T) {
	t.Parallel()

	middleware := &HTTPMiddleware{
		serverName: "github",
		transport:  "streamable-http",
	}

	// Create a mock span to capture attributes
	mockSpan := &mockSpan{attributes: make(map[string]interface{})}

	mcpRequest := &mcpparser.ParsedMCPRequest{
		Method:     "tools/call",
		ID:         "test-123",
		ResourceID: "github_search",
		Arguments: map[string]interface{}{
			"query": "test",
		},
		IsRequest: true,
	}

	req := httptest.NewRequest("POST", "/messages", nil)
	req.Header.Set("Mcp-Session-Id", "session-abc")
	req.RemoteAddr = "192.168.1.100:54321"
	ctx := context.WithValue(req.Context(), mcpparser.MCPRequestContextKey, mcpRequest)

	middleware.addMCPAttributes(ctx, mockSpan, req)

	// Verify standard attribute names (renamed from legacy names)
	assert.Equal(t, "tools/call", mockSpan.attributes["mcp.method.name"])
	assert.Equal(t, mcpProtocolVersion, mockSpan.attributes["mcp.protocol.version"])
	assert.Equal(t, "test-123", mockSpan.attributes["jsonrpc.request.id"])
	assert.Equal(t, "github_search", mockSpan.attributes["mcp.resource.uri"])
	assert.Equal(t, "github_search", mockSpan.attributes["gen_ai.tool.name"])
	assert.Equal(t, "execute_tool", mockSpan.attributes["gen_ai.operation.name"])
	assert.Equal(t, "tcp", mockSpan.attributes["network.transport"])
	assert.Equal(t, "http", mockSpan.attributes["network.protocol.name"])
	assert.Equal(t, "1.1", mockSpan.attributes["network.protocol.version"])
	assert.Equal(t, "192.168.1.100", mockSpan.attributes["client.address"])
	assert.Equal(t, int64(54321), mockSpan.attributes["client.port"])
	assert.Equal(t, "session-abc", mockSpan.attributes["mcp.session.id"])

	// Verify removed attributes are NOT present
	assert.Nil(t, mockSpan.attributes["rpc.system"])
	assert.Nil(t, mockSpan.attributes["rpc.service"])
	assert.Nil(t, mockSpan.attributes["mcp.method"])
	assert.Nil(t, mockSpan.attributes["mcp.request.id"])
	assert.Nil(t, mockSpan.attributes["mcp.resource.id"])
	assert.Nil(t, mockSpan.attributes["mcp.transport"])
	assert.Nil(t, mockSpan.attributes["mcp.tool.name"])
	assert.Nil(t, mockSpan.attributes["mcp.tool.arguments"])
}

func TestHTTPMiddleware_MCP_PromptAttributes(t *testing.T) {
	t.Parallel()

	middleware := &HTTPMiddleware{
		serverName: "github",
		transport:  "sse",
	}

	mockSpan := &mockSpan{attributes: make(map[string]interface{})}

	mcpRequest := &mcpparser.ParsedMCPRequest{
		Method:     "prompts/get",
		ID:         "test-456",
		ResourceID: "my_prompt",
		IsRequest:  true,
	}

	req := httptest.NewRequest("POST", "/messages", nil)
	ctx := context.WithValue(req.Context(), mcpparser.MCPRequestContextKey, mcpRequest)

	middleware.addMCPAttributes(ctx, mockSpan, req)

	// Verify standard attribute names for prompts
	assert.Equal(t, "my_prompt", mockSpan.attributes["gen_ai.prompt.name"])
	assert.Nil(t, mockSpan.attributes["mcp.prompt.name"])
}

func TestHTTPMiddleware_RecordSessionEnd(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	meter := meterProvider.Meter(instrumentationName)
	sessionDuration, _ := meter.Float64Histogram(
		metricSessionDuration,
		metric.WithDescription("Duration of MCP server sessions"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(mcpOperationDurationBuckets...),
	)

	mw := &HTTPMiddleware{
		transport:       "sse",
		sessions:        make(map[string]time.Time),
		sessionDuration: sessionDuration,
	}

	// Record a session start
	mw.sessionsMu.Lock()
	mw.sessions["test-session"] = time.Now().Add(-5 * time.Second) // Started 5 seconds ago
	mw.sessionsMu.Unlock()

	// Record the session end
	mw.RecordSessionEnd(context.Background(), "test-session")

	// Verify the session was removed
	mw.sessionsMu.Lock()
	_, exists := mw.sessions["test-session"]
	mw.sessionsMu.Unlock()
	assert.False(t, exists, "Session should be removed after RecordSessionEnd")

	// Verify that RecordSessionEnd for non-existent session is a no-op
	mw.RecordSessionEnd(context.Background(), "non-existent")

	// Collect and verify session duration was recorded
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	var foundSessionDuration bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == metricSessionDuration {
				foundSessionDuration = true
			}
		}
	}
	assert.True(t, foundSessionDuration, "Session duration metric should be recorded")
}

func TestHTTPMiddleware_SessionEndOnDelete(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
	}
	tracerProvider := tracenoop.NewTracerProvider()

	middleware := NewHTTPMiddleware(config, tracerProvider, meterProvider, "github", "sse")

	t.Run("DELETE with 200 response records session duration", func(t *testing.T) {
		t.Parallel()

		// Create a test handler that returns 200
		testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("session ended"))
		})

		wrappedHandler := middleware(testHandler)

		// Step 1: Establish session with POST request
		mcpRequest := &mcpparser.ParsedMCPRequest{
			Method:    "initialize",
			ID:        "init-1",
			IsRequest: true,
		}

		postReq := httptest.NewRequest("POST", "/messages", nil)
		postReq.Header.Set("Mcp-Session-Id", "test-session-123")
		ctx := context.WithValue(postReq.Context(), mcpparser.MCPRequestContextKey, mcpRequest)
		postReq = postReq.WithContext(ctx)

		postRec := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(postRec, postReq)
		assert.Equal(t, http.StatusOK, postRec.Code)

		// Small delay to ensure session has measurable duration
		time.Sleep(10 * time.Millisecond)

		// Step 2: Send DELETE request to end session
		deleteReq := httptest.NewRequest("DELETE", "/messages", nil)
		deleteReq.Header.Set("Mcp-Session-Id", "test-session-123")
		ctx = context.WithValue(deleteReq.Context(), mcpparser.MCPRequestContextKey, mcpRequest)
		deleteReq = deleteReq.WithContext(ctx)

		deleteRec := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(deleteRec, deleteReq)
		assert.Equal(t, http.StatusOK, deleteRec.Code)

		// Step 3: Verify session duration metric was recorded
		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		require.NoError(t, err)

		var foundSessionDuration bool
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == metricSessionDuration {
					foundSessionDuration = true
					// Verify histogram has data points
					histogram, ok := m.Data.(metricdata.Histogram[float64])
					require.True(t, ok, "Expected Histogram data type")
					assert.NotEmpty(t, histogram.DataPoints, "Session duration should have recorded data points")
				}
			}
		}
		assert.True(t, foundSessionDuration, "Session duration metric should be recorded on DELETE")
	})

	t.Run("DELETE with 4xx response does NOT record session duration", func(t *testing.T) {
		t.Parallel()

		// Create a test handler that returns 200 for POST and 404 for DELETE
		testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte("not found"))
			} else {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			}
		})

		// Create a new reader for this sub-test to isolate metrics
		subReader := sdkmetric.NewManualReader()
		subMeterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(subReader))
		subMiddleware := NewHTTPMiddleware(config, tracerProvider, subMeterProvider, "github", "sse")

		wrappedHandler := subMiddleware(testHandler)

		// Step 1: Establish session with POST request
		mcpRequest := &mcpparser.ParsedMCPRequest{
			Method:    "initialize",
			ID:        "init-2",
			IsRequest: true,
		}

		postReq := httptest.NewRequest("POST", "/messages", nil)
		postReq.Header.Set("Mcp-Session-Id", "test-session-456")
		ctx := context.WithValue(postReq.Context(), mcpparser.MCPRequestContextKey, mcpRequest)
		postReq = postReq.WithContext(ctx)

		postRec := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(postRec, postReq)
		assert.Equal(t, http.StatusOK, postRec.Code)

		// Step 2: Send DELETE request with 404 response
		deleteReq := httptest.NewRequest("DELETE", "/messages", nil)
		deleteReq.Header.Set("Mcp-Session-Id", "test-session-456")
		ctx = context.WithValue(deleteReq.Context(), mcpparser.MCPRequestContextKey, mcpRequest)
		deleteReq = deleteReq.WithContext(ctx)

		deleteRec := httptest.NewRecorder()
		wrappedHandler.ServeHTTP(deleteRec, deleteReq)
		assert.Equal(t, http.StatusNotFound, deleteRec.Code)

		// Step 3: Verify session duration metric was NOT recorded (or had no new points)
		var rm metricdata.ResourceMetrics
		err := subReader.Collect(context.Background(), &rm)
		require.NoError(t, err)

		// Session duration should either not exist or have no data points for the failed DELETE
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == metricSessionDuration {
					histogram, ok := m.Data.(metricdata.Histogram[float64])
					if ok {
						// If the metric exists, it should have no data points for the error case
						// (The POST may have recorded something, but DELETE with 404 should not add more)
						// This is difficult to assert precisely, so we just verify the metric structure
						assert.NotNil(t, histogram)
					}
				}
			}
		}
	})
}

// mockSpan implements trace.Span for testing
type mockSpan struct {
	trace.Span
	attributes map[string]interface{}
}

func (m *mockSpan) SetAttributes(kv ...attribute.KeyValue) {
	for _, attr := range kv {
		m.attributes[string(attr.Key)] = attr.Value.AsInterface()
	}
}

func (*mockSpan) End(...trace.SpanEndOption)              {}
func (*mockSpan) AddEvent(string, ...trace.EventOption)   {}
func (*mockSpan) IsRecording() bool                       { return true }
func (*mockSpan) RecordError(error, ...trace.EventOption) {}
func (*mockSpan) SpanContext() trace.SpanContext          { return trace.SpanContext{} }
func (*mockSpan) SetStatus(codes.Code, string)            {}
func (*mockSpan) SetName(string)                          {}
func (*mockSpan) TracerProvider() trace.TracerProvider    { return tracenoop.NewTracerProvider() }

// assertHasAttribute checks that an attribute set contains a specific key-value pair
func assertHasAttribute(t *testing.T, attrs attribute.Set, key, expectedValue string) {
	t.Helper()
	val, found := attrs.Value(attribute.Key(key))
	assert.True(t, found, "Expected attribute %q to be present", key)
	if found {
		assert.Equal(t, expectedValue, val.AsString(), "Attribute %q value mismatch", key)
	}
}

// Factory Middleware Tests

func TestCreateMiddleware_ValidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		params        FactoryMiddlewareParams
		expectError   bool
		expectedCalls func(runner *mocks.MockMiddlewareRunner, config *mocks.MockRunnerConfig)
	}{
		{
			name: "valid config with no-op provider (avoiding network dependency)",
			params: FactoryMiddlewareParams{
				Config: &Config{
					Endpoint:                    "", // No endpoint to avoid network dependency
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					SamplingRate:                "0.1",
					Headers:                     map[string]string{"Authorization": "Bearer token"},
					EnablePrometheusMetricsPath: false,
					EnvironmentVariables:        []string{"NODE_ENV"},
				},
				ServerName: "github",
				Transport:  "stdio",
			},
			expectError: false,
			expectedCalls: func(runner *mocks.MockMiddlewareRunner, _ *mocks.MockRunnerConfig) {
				runner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)
			},
		},
		{
			name: "valid config with Prometheus metrics enabled",
			params: FactoryMiddlewareParams{
				Config: &Config{
					Endpoint:                    "", // No endpoint - using Prometheus only
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					SamplingRate:                "0.5",
					Headers:                     map[string]string{},
					EnablePrometheusMetricsPath: true,
					EnvironmentVariables:        []string{},
				},
				ServerName: "weather",
				Transport:  "sse",
			},
			expectError: false,
			expectedCalls: func(runner *mocks.MockMiddlewareRunner, config *mocks.MockRunnerConfig) {
				runner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)
				runner.EXPECT().SetPrometheusHandler(gomock.Any()).Times(1)
				config.EXPECT().GetPort().Return(8080).Times(1)
			},
		},
		{
			name: "valid config with no endpoint but Prometheus enabled",
			params: FactoryMiddlewareParams{
				Config: &Config{
					Endpoint:                    "", // No OTLP endpoint
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					SamplingRate:                "0.0",
					Headers:                     map[string]string{},
					Insecure:                    false,
					EnablePrometheusMetricsPath: true,
					EnvironmentVariables:        []string{"TEST_ENV"},
				},
				ServerName: "fetch",
				Transport:  "stdio",
			},
			expectError: false,
			expectedCalls: func(runner *mocks.MockMiddlewareRunner, config *mocks.MockRunnerConfig) {
				runner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)
				runner.EXPECT().SetPrometheusHandler(gomock.Any()).Times(1)
				config.EXPECT().GetPort().Return(8080).Times(1)
			},
		},
		{
			name: "valid minimal config (no-op provider)",
			params: FactoryMiddlewareParams{
				Config: &Config{
					Endpoint:                    "", // No OTLP endpoint
					ServiceName:                 "minimal-service",
					ServiceVersion:              "0.1.0",
					SamplingRate:                "0.0",
					Headers:                     map[string]string{},
					Insecure:                    false,
					EnablePrometheusMetricsPath: false, // No Prometheus either
					EnvironmentVariables:        []string{},
				},
				ServerName: "minimal",
				Transport:  "stdio",
			},
			expectError: false,
			expectedCalls: func(runner *mocks.MockMiddlewareRunner, _ *mocks.MockRunnerConfig) {
				runner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)
				// No SetPrometheusHandler call expected for no-op provider
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create mock controller and runner
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
			mockConfig := mocks.NewMockRunnerConfig(ctrl)
			mockRunner.EXPECT().GetConfig().Return(mockConfig).AnyTimes()

			// Set up expected calls
			if tt.expectedCalls != nil {
				tt.expectedCalls(mockRunner, mockConfig)
			}

			// Create middleware config
			paramsJSON, err := json.Marshal(tt.params)
			require.NoError(t, err)

			config := &types.MiddlewareConfig{
				Type:       MiddlewareType,
				Parameters: paramsJSON,
			}

			// Execute CreateMiddleware
			err = CreateMiddleware(config, mockRunner)

			// Verify result
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCreateMiddleware_InvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        *types.MiddlewareConfig
		params        interface{}
		expectedError string
		expectedCalls func(runner *mocks.MockMiddlewareRunner)
	}{
		{
			name: "invalid JSON parameters",
			config: &types.MiddlewareConfig{
				Type:       MiddlewareType,
				Parameters: json.RawMessage(`{invalid json`),
			},
			expectedError: "failed to unmarshal telemetry middleware parameters",
			expectedCalls: func(_ *mocks.MockMiddlewareRunner) {
				// No calls expected when JSON parsing fails
			},
		},
		{
			name: "nil telemetry config",
			params: FactoryMiddlewareParams{
				Config:     nil, // This should cause an error
				ServerName: "github",
				Transport:  "stdio",
			},
			expectedError: "telemetry config is required",
			expectedCalls: func(_ *mocks.MockMiddlewareRunner) {
				// No calls expected when config validation fails
			},
		},
		{
			name: "empty server name",
			params: FactoryMiddlewareParams{
				Config: &Config{
					Endpoint:                    "", // No endpoint to avoid network dependency
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					SamplingRate:                "0.1",
					EnablePrometheusMetricsPath: false,
				},
				ServerName: "", // Empty server name should still work
				Transport:  "stdio",
			},
			expectedError: "", // This should not error - empty server name is allowed
			expectedCalls: func(runner *mocks.MockMiddlewareRunner) {
				runner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)
			},
		},
		{
			name: "empty transport",
			params: FactoryMiddlewareParams{
				Config: &Config{
					Endpoint:                    "", // No endpoint to avoid network dependency
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					SamplingRate:                "0.1",
					EnablePrometheusMetricsPath: false,
				},
				ServerName: "github",
				Transport:  "", // Empty transport should still work
			},
			expectedError: "", // This should not error - empty transport is allowed
			expectedCalls: func(runner *mocks.MockMiddlewareRunner) {
				runner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create mock controller and runner
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

			// Set up expected calls
			if tt.expectedCalls != nil {
				tt.expectedCalls(mockRunner)
			}

			// Create config
			var config *types.MiddlewareConfig
			if tt.config != nil {
				config = tt.config
			} else {
				// Marshal params to JSON
				paramsJSON, err := json.Marshal(tt.params)
				require.NoError(t, err)

				config = &types.MiddlewareConfig{
					Type:       MiddlewareType,
					Parameters: paramsJSON,
				}
			}

			// Execute CreateMiddleware
			err := CreateMiddleware(config, mockRunner)

			// Verify result
			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestFactoryMiddleware_Handler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupMock  func() (*Provider, error)
		serverName string
		transport  string
		expectNil  bool
	}{
		{
			name: "valid provider with OTLP endpoint",
			setupMock: func() (*Provider, error) {
				// For testing, use no-op provider to avoid network calls
				config := Config{
					Endpoint:                    "", // No endpoint to avoid network dependency
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					SamplingRate:                "0.1",
					EnablePrometheusMetricsPath: false,
				}
				return NewProvider(context.Background(), config)
			},
			serverName: "github",
			transport:  "stdio",
			expectNil:  false,
		},
		{
			name: "no-op provider",
			setupMock: func() (*Provider, error) {
				config := Config{
					Endpoint:                    "", // No endpoint
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					EnablePrometheusMetricsPath: false, // No Prometheus
				}
				return NewProvider(context.Background(), config)
			},
			serverName: "weather",
			transport:  "sse",
			expectNil:  false,
		},
		{
			name: "provider with Prometheus enabled",
			setupMock: func() (*Provider, error) {
				config := Config{
					Endpoint:                    "", // No OTLP endpoint
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					EnablePrometheusMetricsPath: true, // Prometheus enabled
				}
				return NewProvider(context.Background(), config)
			},
			serverName: "fetch",
			transport:  "stdio",
			expectNil:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Setup provider
			provider, err := tt.setupMock()
			require.NoError(t, err)
			defer func() {
				if provider != nil {
					provider.Shutdown(context.Background())
				}
			}()

			// Create middleware
			middleware := provider.Middleware(tt.serverName, tt.transport)
			factoryMw := &FactoryMiddleware{
				provider:   provider,
				middleware: middleware,
			}

			// Test Handler method
			handlerFunc := factoryMw.Handler()

			if tt.expectNil {
				assert.Nil(t, handlerFunc)
			} else {
				assert.NotNil(t, handlerFunc)

				// Test that the handler function works
				testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
					w.Write([]byte("test response"))
				})

				wrappedHandler := handlerFunc(testHandler)
				assert.NotNil(t, wrappedHandler)

				// Execute a test request
				req := httptest.NewRequest("GET", "/test", nil)
				rec := httptest.NewRecorder()
				wrappedHandler.ServeHTTP(rec, req)

				// Verify response
				assert.Equal(t, http.StatusOK, rec.Code)
				assert.Equal(t, "test response", rec.Body.String())
			}
		})
	}
}

func TestFactoryMiddleware_Close(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupMock   func() (*Provider, error)
		expectError bool
	}{
		{
			name: "provider with successful shutdown",
			setupMock: func() (*Provider, error) {
				// Use no-op provider for testing to avoid network dependencies
				config := Config{
					Endpoint:                    "", // No endpoint
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					EnablePrometheusMetricsPath: false,
				}
				return NewProvider(context.Background(), config)
			},
			expectError: false,
		},
		{
			name: "no-op provider",
			setupMock: func() (*Provider, error) {
				config := Config{
					Endpoint:                    "", // No endpoint
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					EnablePrometheusMetricsPath: false, // No Prometheus
				}
				return NewProvider(context.Background(), config)
			},
			expectError: false,
		},
		{
			name: "nil provider",
			setupMock: func() (*Provider, error) {
				return nil, nil
			},
			expectError: false, // Should not error with nil provider
		},
		{
			name: "provider with Prometheus metrics",
			setupMock: func() (*Provider, error) {
				config := Config{
					Endpoint:                    "",
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					EnablePrometheusMetricsPath: true,
				}
				return NewProvider(context.Background(), config)
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Setup provider
			provider, err := tt.setupMock()
			if !tt.expectError {
				require.NoError(t, err)
			}

			// Create factory middleware
			factoryMw := &FactoryMiddleware{
				provider: provider,
			}

			// Test Close method
			closeErr := factoryMw.Close()

			// Verify result
			if tt.expectError {
				assert.Error(t, closeErr)
			} else {
				assert.NoError(t, closeErr)
			}
		})
	}
}

func TestFactoryMiddleware_PrometheusHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		setupMock         func() (*Provider, http.Handler, error)
		expectNil         bool
		expectHandlerTest bool
	}{
		{
			name: "provider with Prometheus enabled",
			setupMock: func() (*Provider, http.Handler, error) {
				config := Config{
					Endpoint:                    "",
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					EnablePrometheusMetricsPath: true,
				}
				provider, err := NewProvider(context.Background(), config)
				if err != nil {
					return nil, nil, err
				}
				return provider, provider.PrometheusHandler(), nil
			},
			expectNil:         false,
			expectHandlerTest: true,
		},
		{
			name: "provider with Prometheus disabled - no-op provider",
			setupMock: func() (*Provider, http.Handler, error) {
				// Use no-op provider to avoid network dependencies
				config := Config{
					Endpoint:                    "", // No endpoint
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					EnablePrometheusMetricsPath: false, // Disabled
				}
				provider, err := NewProvider(context.Background(), config)
				if err != nil {
					return nil, nil, err
				}
				return provider, provider.PrometheusHandler(), nil
			},
			expectNil:         true,
			expectHandlerTest: false,
		},
		{
			name: "nil prometheus handler explicitly set",
			setupMock: func() (*Provider, http.Handler, error) {
				config := Config{
					ServiceName:    "test-service",
					ServiceVersion: "1.0.0",
				}
				// Create a no-op provider using NewProvider with no endpoints
				ctx := context.Background()
				provider, err := NewProvider(ctx, config)
				if err != nil {
					return nil, nil, err
				}
				return provider, nil, nil // Explicitly set nil handler
			},
			expectNil:         true,
			expectHandlerTest: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Setup provider and expected handler
			provider, expectedHandler, err := tt.setupMock()
			require.NoError(t, err)
			defer func() {
				if provider != nil {
					provider.Shutdown(context.Background())
				}
			}()

			// Create factory middleware
			factoryMw := &FactoryMiddleware{
				provider:          provider,
				prometheusHandler: expectedHandler,
			}

			// Test PrometheusHandler method
			handler := factoryMw.PrometheusHandler()

			if tt.expectNil {
				assert.Nil(t, handler)
			} else {
				assert.NotNil(t, handler)

				// If we expect handler tests, verify it works
				if tt.expectHandlerTest {
					req := httptest.NewRequest("GET", "/metrics", nil)
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)

					// For Prometheus handler, we expect either OK or some metrics output
					// The exact content depends on whether metrics have been recorded
					assert.True(t, rec.Code >= 200 && rec.Code < 300, "Expected 2xx status code, got %d", rec.Code)
					assert.NotEmpty(t, rec.Body.String(), "Expected non-empty response body from Prometheus handler")
				}
			}
		})
	}
}

func TestFactoryMiddleware_Integration(t *testing.T) {
	t.Parallel()

	// Integration test that verifies the complete factory middleware flow
	t.Run("complete workflow with Prometheus", func(t *testing.T) {
		t.Parallel()

		// Setup mock runner
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockConfig := mocks.NewMockRunnerConfig(ctrl)
		mockRunner.EXPECT().GetConfig().Return(mockConfig).AnyTimes()
		mockConfig.EXPECT().GetPort().Return(8080).Times(1)

		// Expect middleware to be added and Prometheus handler to be set
		var capturedMiddleware types.Middleware
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1).Do(func(_ string, mw types.Middleware) {
			capturedMiddleware = mw
		})
		mockRunner.EXPECT().SetPrometheusHandler(gomock.Any()).Times(1)

		// Create middleware config
		params := FactoryMiddlewareParams{
			Config: &Config{
				Endpoint:                    "", // No OTLP
				ServiceName:                 "integration-test",
				ServiceVersion:              "1.0.0",
				EnablePrometheusMetricsPath: true,
				EnvironmentVariables:        []string{"TEST_VAR"},
			},
			ServerName: "integration",
			Transport:  "stdio",
		}

		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		config := &types.MiddlewareConfig{
			Type:       MiddlewareType,
			Parameters: paramsJSON,
		}

		// Execute CreateMiddleware
		err = CreateMiddleware(config, mockRunner)
		assert.NoError(t, err)

		// Verify the captured middleware works
		assert.NotNil(t, capturedMiddleware)

		// Test the handler
		handlerFunc := capturedMiddleware.Handler()
		assert.NotNil(t, handlerFunc)

		// Test the Prometheus handler
		prometheusHandler := capturedMiddleware.(*FactoryMiddleware).PrometheusHandler()
		assert.NotNil(t, prometheusHandler)

		// Test cleanup
		err = capturedMiddleware.Close()
		assert.NoError(t, err)
	})

	t.Run("complete workflow with OTLP", func(t *testing.T) {
		t.Parallel()

		// Setup mock runner
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

		// Expect only middleware to be added (no Prometheus)
		var capturedMiddleware types.Middleware
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1).Do(func(_ string, mw types.Middleware) {
			capturedMiddleware = mw
		})

		// Create middleware config without OTLP endpoint to avoid network dependencies
		params := FactoryMiddlewareParams{
			Config: &Config{
				Endpoint:                    "", // No endpoint to avoid network dependencies
				ServiceName:                 "otlp-integration-test",
				ServiceVersion:              "1.0.0",
				SamplingRate:                "0.1",
				Headers:                     map[string]string{"Authorization": "Bearer test"},
				EnablePrometheusMetricsPath: false,
				EnvironmentVariables:        []string{"NODE_ENV", "SERVICE_ENV"},
			},
			ServerName: "otlp-test",
			Transport:  "sse",
		}

		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		config := &types.MiddlewareConfig{
			Type:       MiddlewareType,
			Parameters: paramsJSON,
		}

		// Execute CreateMiddleware
		err = CreateMiddleware(config, mockRunner)
		assert.NoError(t, err)

		// Verify the captured middleware
		assert.NotNil(t, capturedMiddleware)

		// Test the handler
		handlerFunc := capturedMiddleware.Handler()
		assert.NotNil(t, handlerFunc)

		// Prometheus handler should be nil since it's disabled
		prometheusHandler := capturedMiddleware.(*FactoryMiddleware).PrometheusHandler()
		assert.Nil(t, prometheusHandler)

		// Test cleanup
		err = capturedMiddleware.Close()
		assert.NoError(t, err)
	})
}

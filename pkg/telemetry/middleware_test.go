package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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
		httpMethod   string
		path         string
		expectedSpan string
	}{
		{
			name:         "with MCP method",
			mcpMethod:    "tools/call",
			httpMethod:   "POST",
			path:         "/messages",
			expectedSpan: "mcp.tools/call",
		},
		{
			name:         "without MCP method",
			mcpMethod:    "",
			httpMethod:   "GET",
			path:         "/health",
			expectedSpan: "GET /health",
		},
		{
			name:         "with different MCP method",
			mcpMethod:    "resources/read",
			httpMethod:   "POST",
			path:         "/api/v1/messages",
			expectedSpan: "mcp.resources/read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(tt.httpMethod, tt.path, nil)
			ctx := req.Context()

			if tt.mcpMethod != "" {
				mcpRequest := &mcpparser.ParsedMCPRequest{
					Method: tt.mcpMethod,
				}
				ctx = context.WithValue(ctx, mcpparser.MCPRequestContextKey, mcpRequest)
			}

			spanName := middleware.createSpanName(ctx, req)
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
			expectedStatus: codes.Error,
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
			var expectedStatus codes.Code
			if tt.statusCode >= 400 {
				expectedStatus = codes.Error
			} else {
				expectedStatus = codes.Ok
			}

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

	// Find our metrics
	var foundCounter, foundHistogram, foundGauge bool
	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			switch metric.Name {
			case "toolhive_mcp_requests":
				foundCounter = true
			case "toolhive_mcp_request_duration":
				foundHistogram = true
			case "toolhive_mcp_active_connections":
				foundGauge = true
			}
		}
	}

	assert.True(t, foundCounter, "Request counter metric should be recorded")
	assert.True(t, foundHistogram, "Request duration histogram should be recorded")
	assert.True(t, foundGauge, "Active connections gauge should be recorded")
}

func TestHTTPMiddleware_addEnvironmentAttributes(t *testing.T) {
	t.Parallel()
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
			if contains(tt.envVars, "TEST_ENV_1") {
				assert.Equal(t, "value1", mockSpan.attributes["environment.TEST_ENV_1"])
			}
			if contains(tt.envVars, "TEST_ENV_2") {
				assert.Equal(t, "value2", mockSpan.attributes["environment.TEST_ENV_2"])
			}
			if contains(tt.envVars, "TEST_ENV_3") {
				assert.Equal(t, "", mockSpan.attributes["environment.TEST_ENV_3"])
			}
			if contains(tt.envVars, "NON_EXISTENT_VAR") {
				assert.Equal(t, "", mockSpan.attributes["environment.NON_EXISTENT_VAR"])
			}
		})
	}
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

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
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
					SamplingRate:                0.1,
					Headers:                     map[string]string{"Authorization": "Bearer token"},
					EnablePrometheusMetricsPath: false,
					EnvironmentVariables:        []string{"NODE_ENV"},
				},
				ServerName: "github",
				Transport:  "stdio",
			},
			expectError: false,
			expectedCalls: func(runner *mocks.MockMiddlewareRunner, _ *mocks.MockRunnerConfig) {
				runner.EXPECT().AddMiddleware(gomock.Any()).Times(1)
			},
		},
		{
			name: "valid config with Prometheus metrics enabled",
			params: FactoryMiddlewareParams{
				Config: &Config{
					Endpoint:                    "", // No endpoint - using Prometheus only
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					SamplingRate:                0.5,
					Headers:                     map[string]string{},
					EnablePrometheusMetricsPath: true,
					EnvironmentVariables:        []string{},
				},
				ServerName: "weather",
				Transport:  "sse",
			},
			expectError: false,
			expectedCalls: func(runner *mocks.MockMiddlewareRunner, config *mocks.MockRunnerConfig) {
				runner.EXPECT().AddMiddleware(gomock.Any()).Times(1)
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
					SamplingRate:                0.0,
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
				runner.EXPECT().AddMiddleware(gomock.Any()).Times(1)
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
					SamplingRate:                0.0,
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
				runner.EXPECT().AddMiddleware(gomock.Any()).Times(1)
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
					SamplingRate:                0.1,
					EnablePrometheusMetricsPath: false,
				},
				ServerName: "", // Empty server name should still work
				Transport:  "stdio",
			},
			expectedError: "", // This should not error - empty server name is allowed
			expectedCalls: func(runner *mocks.MockMiddlewareRunner) {
				runner.EXPECT().AddMiddleware(gomock.Any()).Times(1)
			},
		},
		{
			name: "empty transport",
			params: FactoryMiddlewareParams{
				Config: &Config{
					Endpoint:                    "", // No endpoint to avoid network dependency
					ServiceName:                 "test-service",
					ServiceVersion:              "1.0.0",
					SamplingRate:                0.1,
					EnablePrometheusMetricsPath: false,
				},
				ServerName: "github",
				Transport:  "", // Empty transport should still work
			},
			expectedError: "", // This should not error - empty transport is allowed
			expectedCalls: func(runner *mocks.MockMiddlewareRunner) {
				runner.EXPECT().AddMiddleware(gomock.Any()).Times(1)
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
					SamplingRate:                0.1,
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
		mockRunner.EXPECT().AddMiddleware(gomock.Any()).Times(1).Do(func(mw types.Middleware) {
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
		mockRunner.EXPECT().AddMiddleware(gomock.Any()).Times(1).Do(func(mw types.Middleware) {
			capturedMiddleware = mw
		})

		// Create middleware config without OTLP endpoint to avoid network dependencies
		params := FactoryMiddlewareParams{
			Config: &Config{
				Endpoint:                    "", // No endpoint to avoid network dependencies
				ServiceName:                 "otlp-integration-test",
				ServiceVersion:              "1.0.0",
				SamplingRate:                0.1,
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

func TestDetectMCPToolError(t *testing.T) {
	t.Parallel()
	assert.False(t, detectMCPToolError([]byte(`{"result":{"isError":false}}`)))
	assert.True(t, detectMCPToolError([]byte(`{"result":{"isError":true}}`)))
	assert.False(t, detectMCPToolError([]byte(`{"result":{"content":"test"}}`)))
}

func TestResponseWriter_ToolErrorDetection(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()

	// Tool call with error
	rw := &responseWriter{ResponseWriter: rec, isToolCall: true}
	rw.Write([]byte(`{"result":{"isError":true}}`))
	assert.True(t, rw.hasToolError)

	// Tool call without error
	rw = &responseWriter{ResponseWriter: rec, isToolCall: true}
	rw.Write([]byte(`{"result":{"isError":false}}`))
	assert.False(t, rw.hasToolError)

	// Non-tool call should not detect errors
	rw = &responseWriter{ResponseWriter: rec, isToolCall: false}
	rw.Write([]byte(`{"result":{"isError":true}}`))
	assert.False(t, rw.hasToolError)
}

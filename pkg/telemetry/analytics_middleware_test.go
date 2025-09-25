package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

func TestHTTPMiddleware_AnonymousAnalytics(t *testing.T) {
	t.Parallel()
	// Create a mock analytics meter provider
	analyticsMeterProvider := noop.NewMeterProvider()

	// Create middleware with analytics enabled
	config := Config{
		UsageAnalyticsEnabled: true,
	}

	middleware := &HTTPMiddleware{
		config:                 config,
		tracerProvider:         tracenoop.NewTracerProvider(),
		tracer:                 tracenoop.NewTracerProvider().Tracer("test"),
		meterProvider:          noop.NewMeterProvider(),
		meter:                  noop.NewMeterProvider().Meter("test"),
		analyticsMeterProvider: analyticsMeterProvider,
		analyticsMeter:         analyticsMeterProvider.Meter("test"),
		serverName:             "test-server",
		transport:              "stdio",
	}

	// Initialize analytics metrics
	analyticsToolCallCounter, _ := middleware.analyticsMeter.Int64Counter(
		"toolhive_usage_tool_calls",
		metric.WithDescription("Anonymous count of MCP tool calls for usage analytics"),
	)
	middleware.analyticsToolCallCounter = analyticsToolCallCounter

	// Test analytics recording
	t.Run("records analytics for tool calls", func(st *testing.T) {
		st.Parallel()
		ctx := context.Background()

		// Set up parsed MCP context to simulate a tool call
		parsedMCP := &mcpparser.ParsedMCPRequest{
			Method:     string(mcp.MethodToolsCall),
			ResourceID: "test-tool",
		}
		ctx = context.WithValue(ctx, mcpparser.MCPRequestContextKey, parsedMCP)

		// Call recordAnonymousAnalytics directly
		middleware.recordAnonymousAnalytics(ctx, "success")

		// Test passes if no panic occurs and method completes
		// In a real test environment, we would verify the metrics were recorded
		// using a test meter provider that captures metric calls
	})

	t.Run("does not record analytics when disabled", func(st *testing.T) {
		st.Parallel()
		// Create middleware with analytics disabled
		disabledMiddleware := &HTTPMiddleware{
			config:                 Config{UsageAnalyticsEnabled: false},
			tracerProvider:         tracenoop.NewTracerProvider(),
			tracer:                 tracenoop.NewTracerProvider().Tracer("test"),
			meterProvider:          noop.NewMeterProvider(),
			meter:                  noop.NewMeterProvider().Meter("test"),
			analyticsMeterProvider: analyticsMeterProvider,
			analyticsMeter:         analyticsMeterProvider.Meter("test"),
			serverName:             "test-server",
			transport:              "stdio",
		}

		ctx := context.Background()

		// This should do nothing when analytics is disabled
		disabledMiddleware.recordAnonymousAnalytics(ctx, "success")

		// Test passes if no panic occurs
	})
}

func TestNewHTTPMiddleware_WithAnalytics(t *testing.T) {
	t.Parallel()
	config := Config{
		ServiceName:           "test-service",
		ServiceVersion:        "1.0.0",
		UsageAnalyticsEnabled: true,
		AnalyticsEndpoint:     "https://test-analytics.example.com",
	}

	tracerProvider := tracenoop.NewTracerProvider()
	meterProvider := noop.NewMeterProvider()
	analyticsMeterProvider := noop.NewMeterProvider()

	middlewareFunc := NewHTTPMiddleware(
		config,
		tracerProvider,
		meterProvider,
		analyticsMeterProvider,
		"test-server",
		"stdio",
	)

	assert.NotNil(t, middlewareFunc, "Middleware function should not be nil")

	// Test that the middleware can handle a basic HTTP request
	handler := middlewareFunc(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	req := httptest.NewRequest("POST", "/test", nil)
	w := httptest.NewRecorder()

	// This should complete without panicking
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "OK", w.Body.String())
}

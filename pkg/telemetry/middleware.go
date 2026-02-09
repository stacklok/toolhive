// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/logger"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// instrumentationName is the name of this instrumentation package
	instrumentationName = "github.com/stacklok/toolhive/pkg/telemetry"

	// mcpProtocolVersion is the MCP protocol version used by ToolHive
	mcpProtocolVersion = "2025-06-18"

	// methodPromptsGet is the MCP method for prompts/get requests
	methodPromptsGet = "prompts/get"

	// networkTransportTCP is the standard network transport value for TCP-based transports
	networkTransportTCP = "tcp"

	// networkProtocolHTTP is the standard protocol name for HTTP-based transports
	networkProtocolHTTP = "http"

	// maxSessionAge is the maximum age of a tracked session before it's cleaned up.
	// This prevents memory leaks from sessions that never explicitly end.
	maxSessionAge = 24 * time.Hour
)

// MCPOperationDurationBuckets defines the standard histogram bucket boundaries
// for MCP operation/session duration metrics per the MCP OTEL specification.
var MCPOperationDurationBuckets = []float64{
	0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300,
}

// HTTPMiddleware provides OpenTelemetry instrumentation for HTTP requests.
type HTTPMiddleware struct {
	config         Config
	tracerProvider trace.TracerProvider
	tracer         trace.Tracer
	meterProvider  metric.MeterProvider
	meter          metric.Meter
	serverName     string
	transport      string

	// Legacy metrics (backward-compatible toolhive_mcp_* metrics)
	requestCounter    metric.Int64Counter
	requestDuration   metric.Float64Histogram
	activeConnections metric.Int64UpDownCounter

	// Standard MCP OTEL metrics
	operationDuration metric.Float64Histogram
	sessionDuration   metric.Float64Histogram

	// Session tracking for session duration metrics
	sessions   map[string]time.Time
	sessionsMu sync.Mutex
}

// NewHTTPMiddleware creates a new HTTP middleware for OpenTelemetry instrumentation.
// serverName is the name of the MCP server (e.g., "github", "fetch")
// transport is the backend transport type ("stdio", "sse", or "streamable-http").
func NewHTTPMiddleware(
	config Config,
	tracerProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
	serverName, transport string,
) types.MiddlewareFunction {
	meter := meterProvider.Meter(instrumentationName)

	// Initialize legacy metrics (backward-compatible)
	requestCounter, _ := meter.Int64Counter(
		"toolhive_mcp_requests", // The exporter adds the _total suffix automatically
		metric.WithDescription("Total number of MCP requests"),
	)

	requestDuration, _ := meter.Float64Histogram(
		"toolhive_mcp_request_duration", // The exporter adds the _seconds suffix automatically
		metric.WithDescription("Duration of MCP requests in seconds"),
		metric.WithUnit("s"),
	)

	activeConnections, _ := meter.Int64UpDownCounter(
		"toolhive_mcp_active_connections",
		metric.WithDescription("Number of active MCP connections"),
	)

	// Initialize standard MCP OTEL metrics
	operationDuration, _ := meter.Float64Histogram(
		"mcp.server.operation.duration",
		metric.WithDescription("Duration of MCP server operations"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(MCPOperationDurationBuckets...),
	)

	sessionDuration, _ := meter.Float64Histogram(
		"mcp.server.session.duration",
		metric.WithDescription("Duration of MCP server sessions"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(MCPOperationDurationBuckets...),
	)

	middleware := &HTTPMiddleware{
		config:            config,
		tracerProvider:    tracerProvider,
		tracer:            tracerProvider.Tracer(instrumentationName),
		meterProvider:     meterProvider,
		meter:             meter,
		serverName:        serverName,
		transport:         transport,
		requestCounter:    requestCounter,
		requestDuration:   requestDuration,
		activeConnections: activeConnections,
		operationDuration: operationDuration,
		sessionDuration:   sessionDuration,
		sessions:          make(map[string]time.Time),
	}

	return middleware.Handler
}

// Handler implements the middleware function that wraps HTTP handlers.
// This middleware should be placed after the MCP parsing middleware in the chain
// to leverage the parsed MCP data.
// Note: Panic recovery is handled by the dedicated recovery middleware.
func (m *HTTPMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Handle SSE endpoints specially - they are long-lived connections
		// that don't follow the normal request/response pattern
		if strings.HasSuffix(r.URL.Path, "/sse") {
			// Record SSE connection establishment immediately
			m.recordSSEConnection(ctx, r)

			// Pass through to SSE handler without waiting for completion
			next.ServeHTTP(w, r)
			return
		}

		// Normal HTTP request handling
		// Extract trace context from incoming request headers
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))

		// Increment active connections
		m.activeConnections.Add(ctx, 1, metric.WithAttributes(
			attribute.String("server", m.serverName),
			attribute.String("transport", m.transport),
		))
		defer m.activeConnections.Add(ctx, -1, metric.WithAttributes(
			attribute.String("server", m.serverName),
			attribute.String("transport", m.transport),
		))

		// Create span name based on MCP method if available, otherwise use HTTP method + path
		spanName := m.createSpanName(ctx)
		if spanName == "" {
			spanName = fmt.Sprintf("%s %s", r.Method, r.URL.Path)
		}
		ctx, span := m.tracer.Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		// Create a response writer wrapper to capture response details
		rw := &responseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
			bytesWritten:   0,
		}

		// Add HTTP attributes
		m.addHTTPAttributes(span, r)

		// Add MCP attributes if parsed data is available
		m.addMCPAttributes(ctx, span, r)

		// Add environment variables as attributes
		m.addEnvironmentAttributes(span)

		// Track session initialization
		m.trackSessionInit(ctx)

		// Record request start time
		startTime := time.Now()

		// Call the next handler with the instrumented context
		next.ServeHTTP(rw, r.WithContext(ctx))

		// Record completion metrics and finalize span
		duration := time.Since(startTime)
		m.finalizeSpan(ctx, span, rw, duration)
		m.recordMetrics(ctx, r, rw, duration)
	})
}

// RecordSessionEnd records the duration of a completed MCP session.
// Call this when a session ends (e.g., connection close, explicit shutdown).
func (m *HTTPMiddleware) RecordSessionEnd(ctx context.Context, sessionID string) {
	m.sessionsMu.Lock()
	startTime, exists := m.sessions[sessionID]
	if exists {
		delete(m.sessions, sessionID)
	}
	m.sessionsMu.Unlock()

	if !exists {
		return
	}

	duration := time.Since(startTime)
	networkTransport, protocolName, _ := mapTransport(m.transport)

	attrs := []attribute.KeyValue{
		attribute.String("mcp.protocol.version", mcpProtocolVersion),
		attribute.String("network.transport", networkTransport),
	}
	if protocolName != "" {
		attrs = append(attrs, attribute.String("network.protocol.name", protocolName))
	}

	m.sessionDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
}

// createSpanName creates an appropriate span name based on available context.
// Standard format: "{method}" or "{method} {target}" for tool/prompt calls.
// For non-MCP requests: "{HTTP_METHOD} {path}".
func (*HTTPMiddleware) createSpanName(ctx context.Context) string {
	parsedMCP := mcpparser.GetParsedMCPRequest(ctx)
	if parsedMCP == nil || parsedMCP.Method == "" {
		return ""
	}

	// For methods with a meaningful target (tool name, prompt name), include it
	if parsedMCP.ResourceID != "" {
		switch parsedMCP.Method {
		case string(mcp.MethodToolsCall), methodPromptsGet:
			return fmt.Sprintf("%s %s", parsedMCP.Method, parsedMCP.ResourceID)
		}
	}

	return parsedMCP.Method
}

// addHTTPAttributes adds standard HTTP attributes to the span.
func (*HTTPMiddleware) addHTTPAttributes(span trace.Span, r *http.Request) {
	span.SetAttributes(
		attribute.String("http.method", r.Method),
		attribute.String("http.url", r.URL.String()),
		attribute.String("http.scheme", r.URL.Scheme),
		attribute.String("http.host", r.Host),
		attribute.String("http.target", r.URL.Path),
		attribute.String("http.user_agent", r.UserAgent()),
	)

	// Add content length if available
	if contentLength := r.Header.Get("Content-Length"); contentLength != "" {
		span.SetAttributes(attribute.String("http.request_content_length", contentLength))
	}

	// Add query parameters if present
	if r.URL.RawQuery != "" {
		span.SetAttributes(attribute.String("http.query", r.URL.RawQuery))
	}
}

// addMCPAttributes adds MCP-specific attributes using the parsed MCP data from context.
func (m *HTTPMiddleware) addMCPAttributes(ctx context.Context, span trace.Span, r *http.Request) {
	// Get parsed MCP request from context (set by MCP parsing middleware)
	parsedMCP := mcpparser.GetParsedMCPRequest(ctx)
	if parsedMCP == nil {
		// No MCP data available, this might be a non-MCP request (e.g., health check)
		return
	}

	// Add standard MCP attributes (renamed from legacy names)
	span.SetAttributes(
		attribute.String("mcp.method.name", parsedMCP.Method),
		attribute.String("mcp.protocol.version", mcpProtocolVersion),
	)

	// Add request ID if available (renamed: mcp.request.id -> jsonrpc.request.id)
	if parsedMCP.ID != nil {
		span.SetAttributes(attribute.String("jsonrpc.request.id", formatRequestID(parsedMCP.ID)))
	}

	// Add resource ID if available (renamed: mcp.resource.id -> mcp.resource.uri)
	if parsedMCP.ResourceID != "" {
		span.SetAttributes(attribute.String("mcp.resource.uri", parsedMCP.ResourceID))
	}

	// Add method-specific attributes
	m.addMethodSpecificAttributes(span, parsedMCP)

	// Extract server name from the request, defaulting to the middleware's configured server name
	serverName := m.extractServerName(r)
	span.SetAttributes(attribute.String("mcp.server.name", serverName))

	// Determine backend transport type and map to standard values
	backendTransport := m.extractBackendTransport(r)
	networkTransport, protocolName, protocolVersion := mapTransport(backendTransport)
	span.SetAttributes(attribute.String("network.transport", networkTransport))
	if protocolName != "" {
		span.SetAttributes(attribute.String("network.protocol.name", protocolName))
	}
	if protocolVersion != "" {
		span.SetAttributes(attribute.String("network.protocol.version", protocolVersion))
	}

	// Add client address and port from the request
	clientAddr, clientPort := parseRemoteAddr(r.RemoteAddr)
	if clientAddr != "" {
		span.SetAttributes(attribute.String("client.address", clientAddr))
	}
	if clientPort > 0 {
		span.SetAttributes(attribute.Int("client.port", clientPort))
	}

	// Add session ID from MCP _meta if available
	if parsedMCP.Meta != nil {
		if sessionID, ok := parsedMCP.Meta["sessionId"].(string); ok && sessionID != "" {
			span.SetAttributes(attribute.String("mcp.session.id", sessionID))
		}
	}

	// Add batch indicator
	if parsedMCP.IsBatch {
		span.SetAttributes(attribute.Bool("mcp.is_batch", true))
	}
}

// addMethodSpecificAttributes adds attributes specific to certain MCP methods.
func (m *HTTPMiddleware) addMethodSpecificAttributes(span trace.Span, parsedMCP *mcpparser.ParsedMCPRequest) {
	switch parsedMCP.Method {
	case string(mcp.MethodToolsCall):
		// For tool calls, the ResourceID is the tool name
		if parsedMCP.ResourceID != "" {
			span.SetAttributes(attribute.String("gen_ai.tool.name", parsedMCP.ResourceID))
		}
		// Add sanitized arguments (renamed: mcp.tool.arguments -> gen_ai.tool.call.arguments)
		if args := m.sanitizeArguments(parsedMCP.Arguments); args != "" {
			span.SetAttributes(attribute.String("gen_ai.tool.call.arguments", args))
		}
		// Add gen_ai.operation.name for tool calls
		span.SetAttributes(attribute.String("gen_ai.operation.name", "execute_tool"))

	case "resources/read":
		// Resource URI is already set as mcp.resource.uri in addMCPAttributes

	case methodPromptsGet:
		// For prompt gets, the ResourceID is the prompt name
		if parsedMCP.ResourceID != "" {
			span.SetAttributes(attribute.String("gen_ai.prompt.name", parsedMCP.ResourceID))
		}

	case "initialize":
		// For initialize, the ResourceID is the client name
		if parsedMCP.ResourceID != "" {
			span.SetAttributes(attribute.String("mcp.client.name", parsedMCP.ResourceID))
		}
	}
}

func (m *HTTPMiddleware) addEnvironmentAttributes(span trace.Span) {
	// Include environment variables from host machine as configured
	// Only environment variables specified in the config will be read and included
	for _, envVar := range m.config.EnvironmentVariables {
		if envVar == "" {
			continue // Skip empty environment variable names
		}

		value := os.Getenv(envVar)
		// Always set the attribute, even if the environment variable is empty
		// This helps distinguish between unset variables and empty string values
		span.SetAttributes(
			attribute.String(fmt.Sprintf("environment.%s", envVar), value),
		)
	}
}

// extractServerName extracts the MCP server name from the HTTP request.
// It checks for an explicit X-MCP-Server-Name header first, then falls back to the
// configured server name. This approach is more reliable than parsing URL paths since
// the server name is already known during middleware construction.
func (m *HTTPMiddleware) extractServerName(r *http.Request) string {
	// Check for explicit server name header (for advanced routing scenarios)
	if serverName := r.Header.Get("X-MCP-Server-Name"); serverName != "" {
		return serverName
	}

	// Always use the configured server name - this is the correct server name
	// that was passed during middleware construction and doesn't depend on URL structure
	//
	// NOTE: Previously this function attempted to parse server names from URL paths by
	// splitting r.URL.Path and filtering out known endpoint segments like "sse", "messages",
	// "api", "v1", etc. This approach was fundamentally flawed because:
	// 1. It incorrectly treated endpoint names like "message" as server names
	// 2. It made assumptions about URL structure that don't always hold
	// 3. The actual server name is already available via m.serverName
	// Adding more exclusions (like "message") would just be treating symptoms, not the root cause.
	return m.serverName
}

// extractBackendTransport determines the backend transport type.
// ToolHive supports multiple transport types: stdio, sse, streamable-http.
func (m *HTTPMiddleware) extractBackendTransport(r *http.Request) string {
	// Try to get transport info from custom headers (if set by proxy)
	if transport := r.Header.Get("X-MCP-Transport"); transport != "" {
		return transport
	}

	return m.transport
}

// finalizeSpan adds response attributes and sets the span status.
func (*HTTPMiddleware) finalizeSpan(_ context.Context, span trace.Span, rw *responseWriter, duration time.Duration) {
	// Add response attributes
	span.SetAttributes(
		attribute.Int("http.status_code", rw.statusCode),
		attribute.Int64("http.response_content_length", rw.bytesWritten),
		attribute.Float64("http.duration_ms", float64(duration.Nanoseconds())/1e6),
	)

	// Set span status and error.type based on HTTP status code
	if rw.statusCode >= 400 {
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", rw.statusCode))
		// Add error.type attribute as required by the MCP OTEL spec
		span.SetAttributes(attribute.String("error.type", strconv.Itoa(rw.statusCode)))
	} else {
		span.SetStatus(codes.Ok, "")
	}

}

// recordMetrics records request metrics.
func (m *HTTPMiddleware) recordMetrics(ctx context.Context, r *http.Request, rw *responseWriter, duration time.Duration) {
	// Get MCP method from context if available
	mcpMethod := mcpparser.GetMCPMethod(ctx)
	if mcpMethod == "" {
		mcpMethod = "unknown"
	}

	// Determine status (success/error)
	status := "success"
	if rw.statusCode >= 400 {
		status = "error"
	}

	// Get the resource ID from the parsed MCP request if available.
	// For tools/call this is the tool name, for resources/read the URI,
	// and for prompts/get the prompt name.
	mcpResourceID := ""
	if parsedMCP := mcpparser.GetParsedMCPRequest(ctx); parsedMCP != nil {
		mcpResourceID = parsedMCP.ResourceID
	}

	// Common attributes for legacy metrics
	attrs := metric.WithAttributes(
		attribute.String("method", r.Method),
		attribute.String("status_code", strconv.Itoa(rw.statusCode)),
		attribute.String("status", status),
		attribute.String("mcp_method", mcpMethod),
		attribute.String("mcp_resource_id", mcpResourceID),
		attribute.String("server", m.serverName),
		attribute.String("transport", m.transport),
	)

	// Record legacy request count
	m.requestCounter.Add(ctx, 1, attrs)

	// Record legacy request duration
	m.requestDuration.Record(ctx, duration.Seconds(), attrs)

	// Record standard mcp.server.operation.duration metric
	m.recordStandardOperationDuration(ctx, rw, duration)

	// For tools/call, record tool-specific metrics
	if mcpMethod == string(mcp.MethodToolsCall) {
		if parsedMCP := mcpparser.GetParsedMCPRequest(ctx); parsedMCP != nil && parsedMCP.ResourceID != "" {
			toolAttrs := metric.WithAttributes(
				attribute.String("server", m.serverName),
				attribute.String("tool", parsedMCP.ResourceID),
				attribute.String("status", status),
			)

			// Record tool-specific counter
			if toolCounter, err := m.meter.Int64Counter(
				"toolhive_mcp_tool_calls", // The exporter adds the _total suffix automatically
				metric.WithDescription("Total number of MCP tool calls"),
			); err == nil {
				toolCounter.Add(ctx, 1, toolAttrs)
			}
		}
	}
}

// recordSSEConnection records telemetry for SSE connection establishment.
// SSE connections are long-lived and don't follow the normal request/response pattern,
// so we record the connection establishment event immediately.
func (m *HTTPMiddleware) recordSSEConnection(ctx context.Context, r *http.Request) {
	// Create a short-lived span for SSE connection establishment
	spanName := "sse.connection_established"
	_, span := m.tracer.Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindServer))

	// Add HTTP attributes for the connection
	m.addHTTPAttributes(span, r)

	// Map transport to standard values for SSE-specific attributes
	networkTransport, _, _ := mapTransport(m.transport)

	// Add SSE-specific attributes using standard names
	span.SetAttributes(
		attribute.String("sse.event_type", "connection_established"),
		attribute.String("mcp.server.name", m.serverName),
		attribute.String("network.transport", networkTransport),
	)

	// End the span immediately since this is just the connection establishment
	span.SetStatus(codes.Ok, "SSE connection established")
	span.End()

	// Record SSE connection metrics
	attrs := metric.WithAttributes(
		attribute.String("method", r.Method),
		attribute.String("status_code", "200"), // SSE connections start with 200
		attribute.String("status", "success"),
		attribute.String("mcp_method", "sse_connection"),
		attribute.String("server", m.serverName),
		attribute.String("transport", m.transport),
	)

	// Record the connection establishment
	m.requestCounter.Add(ctx, 1, attrs)

	// Increment active SSE connections (these will be decremented when connection closes)
	sseAttrs := metric.WithAttributes(
		attribute.String("server", m.serverName),
		attribute.String("transport", m.transport),
		attribute.String("connection_type", "sse"),
	)
	m.activeConnections.Add(ctx, 1, sseAttrs)
}

// sanitizeArguments converts arguments to a safe string representation.
func (m *HTTPMiddleware) sanitizeArguments(arguments map[string]interface{}) string {
	if len(arguments) == 0 {
		return ""
	}

	// Create a sanitized representation
	var parts []string
	for key, value := range arguments {
		// Check for sensitive keys
		if m.isSensitiveKey(key) {
			parts = append(parts, fmt.Sprintf("%s=[REDACTED]", key))
			continue
		}

		// Limit value length and convert to string
		valueStr := fmt.Sprintf("%v", value)
		if len(valueStr) > 100 {
			valueStr = valueStr[:100] + "..."
		}

		parts = append(parts, fmt.Sprintf("%s=%s", key, valueStr))
	}

	result := strings.Join(parts, ", ")
	if len(result) > 200 {
		result = result[:200] + "..."
	}

	return result
}

// isSensitiveKey checks if a key might contain sensitive information.
func (*HTTPMiddleware) isSensitiveKey(key string) bool {
	sensitivePatterns := []string{
		"password", "token", "secret", "key", "auth", "credential",
		"api_key", "access_token", "refresh_token", "private",
	}

	keyLower := strings.ToLower(key)
	for _, pattern := range sensitivePatterns {
		if strings.Contains(keyLower, pattern) {
			return true
		}
	}
	return false
}

// formatRequestID converts the request ID to a string representation.
func formatRequestID(id interface{}) string {
	switch v := id.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// mapTransport maps ToolHive transport types to standard network attribute values.
// Returns (network.transport, network.protocol.name, network.protocol.version).
func mapTransport(transport string) (networkTransport, protocolName, protocolVersion string) {
	switch transport {
	case "stdio":
		return "pipe", "", ""
	case "sse":
		return networkTransportTCP, networkProtocolHTTP, "1.1"
	case "streamable-http":
		return networkTransportTCP, networkProtocolHTTP, "2"
	default:
		return networkTransportTCP, networkProtocolHTTP, ""
	}
}

// parseRemoteAddr splits an HTTP RemoteAddr into address and port components.
func parseRemoteAddr(remoteAddr string) (string, int) {
	if remoteAddr == "" {
		return "", 0
	}

	host, portStr, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// RemoteAddr might not have a port (unlikely for HTTP but handle gracefully)
		return remoteAddr, 0
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return host, 0
	}

	return host, port
}

// trackSessionInit tracks session initialization for session duration metrics.
// When an "initialize" method is detected with a session ID in _meta, the session
// start time is recorded. Stale sessions are cleaned up periodically.
func (m *HTTPMiddleware) trackSessionInit(ctx context.Context) {
	parsedMCP := mcpparser.GetParsedMCPRequest(ctx)
	if parsedMCP == nil || parsedMCP.Method != "initialize" {
		return
	}

	if parsedMCP.Meta == nil {
		return
	}

	sessionID, ok := parsedMCP.Meta["sessionId"].(string)
	if !ok || sessionID == "" {
		return
	}

	m.sessionsMu.Lock()
	m.sessions[sessionID] = time.Now()
	// Clean up stale sessions to prevent memory leaks
	m.cleanupStaleSessions()
	m.sessionsMu.Unlock()
}

// cleanupStaleSessions removes sessions older than maxSessionAge.
// Must be called with sessionsMu held.
func (m *HTTPMiddleware) cleanupStaleSessions() {
	now := time.Now()
	for id, startTime := range m.sessions {
		if now.Sub(startTime) > maxSessionAge {
			delete(m.sessions, id)
		}
	}
}

// recordStandardOperationDuration records the mcp.server.operation.duration metric
// with standard attribute names as defined by the MCP OTEL spec.
func (m *HTTPMiddleware) recordStandardOperationDuration(ctx context.Context, rw *responseWriter, duration time.Duration) {
	parsedMCP := mcpparser.GetParsedMCPRequest(ctx)

	mcpMethod := "unknown"
	if parsedMCP != nil && parsedMCP.Method != "" {
		mcpMethod = parsedMCP.Method
	}

	networkTransport, _, _ := mapTransport(m.transport)

	// Build attributes for the standard operation duration metric
	attrs := []attribute.KeyValue{
		attribute.String("mcp.method.name", mcpMethod),
		attribute.String("network.transport", networkTransport),
		attribute.String("mcp.protocol.version", mcpProtocolVersion),
	}

	// Add error.type on failures
	if rw.statusCode >= 400 {
		attrs = append(attrs, attribute.String("error.type", strconv.Itoa(rw.statusCode)))
	}

	// Add conditionally required attributes based on method type
	if parsedMCP != nil {
		switch parsedMCP.Method {
		case string(mcp.MethodToolsCall):
			if parsedMCP.ResourceID != "" {
				attrs = append(attrs, attribute.String("gen_ai.tool.name", parsedMCP.ResourceID))
			}
			attrs = append(attrs, attribute.String("gen_ai.operation.name", "execute_tool"))
		case methodPromptsGet:
			if parsedMCP.ResourceID != "" {
				attrs = append(attrs, attribute.String("gen_ai.prompt.name", parsedMCP.ResourceID))
			}
		}
	}

	m.operationDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
}

// responseWriter wraps http.ResponseWriter to capture response details.
type responseWriter struct {
	http.ResponseWriter
	statusCode    int
	bytesWritten  int64
	headerWritten bool // Guard against double WriteHeader calls
}

// WriteHeader captures the status code. Guards against duplicate calls which
// can cause panics in Go's reverse proxy (http: superfluous response.WriteHeader call).
func (rw *responseWriter) WriteHeader(statusCode int) {
	if rw.headerWritten {
		return // Silently ignore duplicate WriteHeader calls
	}
	rw.headerWritten = true
	rw.statusCode = statusCode
	rw.ResponseWriter.WriteHeader(statusCode)
}

// Write captures the number of bytes written.
// Note: Write() implicitly calls WriteHeader(200) on the underlying ResponseWriter
// if headers haven't been written yet. This is standard HTTP behavior - once headers
// are written, the status code cannot be changed. We track this to accurately record
// what actually happened and to prevent subsequent WriteHeader() calls from panicking.
//
// Important: If a non-200 status code is needed, WriteHeader() MUST be called BEFORE Write().
// Once Write() is called first, the status code is fixed at 200 and cannot be changed.
func (rw *responseWriter) Write(data []byte) (int, error) {
	// If headers haven't been written yet, Write() will implicitly write them with status 200.
	// This is what the underlying ResponseWriter actually does - we're tracking what happened,
	// not forcing a status code. Mark headers as written to prevent subsequent WriteHeader()
	// calls from panicking.
	if !rw.headerWritten {
		rw.headerWritten = true
		rw.statusCode = http.StatusOK // Write() implicitly uses 200 - this is what actually happened
	}

	n, err := rw.ResponseWriter.Write(data)
	rw.bytesWritten += int64(n)
	return n, err
}

// Flush implements http.Flusher if the underlying ResponseWriter supports it.
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Factory middleware type constant
const (
	MiddlewareType = "telemetry"
)

// FactoryMiddlewareParams represents the parameters for telemetry middleware
type FactoryMiddlewareParams struct {
	Config     *Config `json:"config"`
	ServerName string  `json:"server_name"`
	Transport  string  `json:"transport"`
}

// FactoryMiddleware wraps telemetry middleware functionality for factory pattern
type FactoryMiddleware struct {
	provider          *Provider
	middleware        types.MiddlewareFunction
	prometheusHandler http.Handler
}

// Handler returns the middleware function used by the proxy.
func (m *FactoryMiddleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (m *FactoryMiddleware) Close() error {
	if m.provider != nil {
		return m.provider.Shutdown(context.Background())
	}
	return nil
}

// PrometheusHandler returns the Prometheus metrics handler.
func (m *FactoryMiddleware) PrometheusHandler() http.Handler {
	return m.prometheusHandler
}

// CreateMiddleware factory function for telemetry middleware
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params FactoryMiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal telemetry middleware parameters: %w", err)
	}

	if params.Config == nil {
		return fmt.Errorf("telemetry config is required")
	}

	provider, err := NewProvider(context.Background(), *params.Config)
	if err != nil {
		return fmt.Errorf("failed to create telemetry provider: %w", err)
	}

	middleware := provider.Middleware(params.ServerName, params.Transport)

	var prometheusHandler http.Handler
	if params.Config.EnablePrometheusMetricsPath {
		prometheusHandler = provider.PrometheusHandler()
	}

	telemetryMw := &FactoryMiddleware{
		provider:          provider,
		middleware:        middleware,
		prometheusHandler: prometheusHandler,
	}

	// Add middleware to runner
	runner.AddMiddleware(config.Type, telemetryMw)

	// Set Prometheus handler if enabled
	if prometheusHandler != nil {
		runner.SetPrometheusHandler(prometheusHandler)
		logger.Infof("Prometheus metrics will be exposed on port %d at /metrics", runner.GetConfig().GetPort())
	}

	return nil
}

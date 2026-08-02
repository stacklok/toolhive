// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	coremetrics "github.com/stacklok/toolhive-core/telemetry/metrics"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/telemetry/providers"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/versions"
)

const (
	// instrumentationName is the name of this instrumentation package
	instrumentationName = "github.com/stacklok/toolhive/pkg/telemetry"
	// methodPromptsGet is the MCP method name for prompts/get
	methodPromptsGet = "prompts/get"
	// attrValueOther is the OTEL semconv sentinel for a value outside the
	// known set. Used to bound the cardinality of metric attributes whose
	// raw value comes from the client.
	attrValueOther = "_OTHER"
	// networkTransportTCP is the OTEL value for TCP transport
	networkTransportTCP = "tcp"
	// networkProtocolHTTP is the OTEL value for HTTP protocol
	networkProtocolHTTP = "http"
)

// HTTPMiddleware provides OpenTelemetry instrumentation for HTTP requests.
type HTTPMiddleware struct {
	config         Config
	tracerProvider trace.TracerProvider
	tracer         trace.Tracer
	meterProvider  metric.MeterProvider
	meter          metric.Meter
	serverName     string
	transport      string

	// Metrics
	operationDuration     metric.Float64Histogram
	httpServerReqDuration metric.Float64Histogram
	sseConnectionDuration metric.Float64Histogram
	activeConnections     metric.Int64UpDownCounter

	// Legacy metric aliases, emitted alongside the names above when
	// Config.UseLegacyMetrics is set so an operator has an overlap window to
	// migrate dashboards. Each is a no-op instrument when the flag is off, so
	// record sites need no branch. Retired in a future minor release.
	legacyRequests          metric.Int64Counter
	legacyRequestDuration   metric.Float64Histogram
	legacyToolCalls         metric.Int64Counter
	legacyActiveConnections metric.Int64UpDownCounter
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

	activeConnections, err := meter.Int64UpDownCounter(
		"stacklok.toolhive.proxy.active_connections",
		metric.WithDescription("Number of active MCP connections"),
	)
	if err != nil {
		slog.Debug("failed to create active connections metric", "error", err)
		activeConnections = noop.Int64UpDownCounter{}
	}

	// Semconv-named metric, so it carries the semconv bucket set exactly rather
	// than the coarser Stacklok proxy preset.
	operationDuration, err := meter.Float64Histogram(
		"mcp.server.operation.duration",
		metric.WithDescription("Duration of MCP server operations"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPSemconv()...),
	)
	if err != nil {
		slog.Debug("failed to create operation duration metric", "error", err)
		operationDuration = noop.Float64Histogram{}
	}

	// OTEL HTTP semantic-convention metric. Name kept verbatim (no stacklok_
	// prefix) so OTel-aware tooling recognizes it. Recorded for every
	// request/response-cycle request the middleware handles, including
	// session-delete DELETEs that carry no MCP method — restoring
	// transport-level coverage that the MCP-scoped operation-duration metric
	// misses. SSE-open GETs are excluded: they block for the connection's
	// full lifetime (minutes to hours), which would pin every observation to
	// this histogram's 10s top bucket. Those go to sseConnectionDuration
	// instead, below.
	httpServerReqDuration, err := meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of HTTP server requests"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(coremetrics.BucketsFastHTTP()...),
	)
	if err != nil {
		slog.Debug("failed to create http server request duration metric", "error", err)
		httpServerReqDuration = noop.Float64Histogram{}
	}

	// SSE connections live for minutes to hours, not milliseconds, so they get
	// their own histogram on BucketsMCPProxy() (tops out at 300s) instead of
	// sharing http.server.request.duration's 10s-capped buckets.
	sseConnectionDuration, err := meter.Float64Histogram(
		"stacklok.toolhive.proxy.sse_connection.duration",
		metric.WithDescription("Duration of SSE connections, recorded once the connection closes"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPProxy()...),
	)
	if err != nil {
		slog.Debug("failed to create sse connection duration metric", "error", err)
		sseConnectionDuration = noop.Float64Histogram{}
	}

	registerBuildInfo(meterProvider, meter)

	// Legacy aliases. toolhive_mcp_requests / _request_duration / _tool_calls were
	// deleted in favour of their semconv equivalents, and _active_connections was
	// renamed; all four are re-emitted here for one release so dashboards can be
	// migrated against live data rather than after the fact.
	legacy := config.UseLegacyMetrics
	legacyRequests := LegacyInt64Counter(meter, legacy, "toolhive_mcp_requests",
		metric.WithDescription("DEPRECATED: use http.server.request.duration's _count series"))
	legacyRequestDuration := LegacyFloat64Histogram(meter, legacy, "toolhive_mcp_request_duration",
		metric.WithDescription("DEPRECATED: use mcp.server.operation.duration"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPSemconv()...))
	legacyToolCalls := LegacyInt64Counter(meter, legacy, "toolhive_mcp_tool_calls",
		metric.WithDescription(`DEPRECATED: use mcp.server.operation.duration filtered to mcp.method.name="tools/call"`))
	legacyActiveConnections := LegacyInt64UpDownCounter(meter, legacy, "toolhive_mcp_active_connections",
		metric.WithDescription("DEPRECATED: renamed to stacklok.toolhive.proxy.active_connections"))

	middleware := &HTTPMiddleware{
		config:                  config,
		tracerProvider:          tracerProvider,
		tracer:                  tracerProvider.Tracer(instrumentationName),
		meterProvider:           meterProvider,
		meter:                   meter,
		serverName:              serverName,
		transport:               transport,
		operationDuration:       operationDuration,
		httpServerReqDuration:   httpServerReqDuration,
		sseConnectionDuration:   sseConnectionDuration,
		activeConnections:       activeConnections,
		legacyRequests:          legacyRequests,
		legacyRequestDuration:   legacyRequestDuration,
		legacyToolCalls:         legacyToolCalls,
		legacyActiveConnections: legacyActiveConnections,
	}

	return middleware.Handler
}

// buildInfoProviders tracks the MeterProviders build_info has been registered on.
//
// The dedup key is the provider, not the process. RegisterBuildInfo attaches an
// observable callback while returning no handle to release it, so registering
// twice on one provider permanently duplicates a callback observing the same
// attribute set. But each provider owns its own reader and /metrics endpoint, so a
// process-wide guard would leave every provider after the first exporting no
// build_info at all — and absence is the harder failure to notice, since a fleet
// dashboard joining on build_info just returns empty for that scrape target.
// NewHTTPMiddleware is reachable from the public Provider.Middleware() and from
// CreateMiddleware, which builds a fresh provider per call, so both cases are real.
//
// The durable fix is to register during provider assembly in
// pkg/telemetry/providers, where ComponentName and the resource already live;
// this keeps the guard correct until then. See registerBuildInfoNow for the test
// escape hatch.
var buildInfoProviders = struct {
	mu   sync.Mutex
	seen map[metric.MeterProvider]struct{}
}{seen: make(map[metric.MeterProvider]struct{})}

// registerBuildInfo registers the stacklok.build_info gauge via the shared
// toolhive-core helper, stamping this component's identity (toolhive) plus the
// build version/commit. Registers at most once per MeterProvider.
func registerBuildInfo(meterProvider metric.MeterProvider, meter metric.Meter) {
	buildInfoProviders.mu.Lock()
	defer buildInfoProviders.mu.Unlock()
	if _, done := buildInfoProviders.seen[meterProvider]; done {
		return
	}
	buildInfoProviders.seen[meterProvider] = struct{}{}
	registerBuildInfoNow(meter)
}

// registerBuildInfoNow performs the registration unconditionally, bypassing the
// per-provider guard. Exists so a test asserting on build_info can register
// against its own meter provider regardless of what earlier tests in the same
// process already registered.
func registerBuildInfoNow(meter metric.Meter) {
	info := versions.GetVersionInfo()
	if err := coremetrics.RegisterBuildInfo(meter, providers.ComponentName, info.Version, info.Commit); err != nil {
		slog.Debug("failed to register build info", "error", err)
	}
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

			// Track active SSE connections with defer to ensure decrement on close
			sseAttrs := metric.WithAttributes(
				attribute.String(coremetrics.LabelMCPServer, m.serverName),
				attribute.String(coremetrics.LabelTransport, m.transport),
				attribute.String("connection_type", "sse"),
			)
			m.activeConnections.Add(ctx, 1, sseAttrs)
			defer m.activeConnections.Add(ctx, -1, sseAttrs)

			// Legacy alias under the pre-rename label keys (server/transport).
			legacySSEAttrs := metric.WithAttributes(
				attribute.String("server", m.serverName),
				attribute.String("transport", m.transport),
				attribute.String("connection_type", "sse"),
			)
			m.legacyActiveConnections.Add(ctx, 1, legacySSEAttrs)
			defer m.legacyActiveConnections.Add(ctx, -1, legacySSEAttrs)

			// Pass through to SSE handler - blocks for the lifetime of the SSE
			// connection. Record its duration on close, into sseConnectionDuration
			// rather than http.server.request.duration — see the comment on that
			// histogram's construction above for why the two are kept separate.
			sseStart := time.Now()
			rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			defer func() {
				m.sseConnectionDuration.Record(ctx, time.Since(sseStart).Seconds(), metric.WithAttributes(
					attribute.String(coremetrics.LabelMCPServer, m.serverName),
				))
			}()
			next.ServeHTTP(rw, r)
			return
		}

		// Normal HTTP request handling
		// Extract trace context from incoming request headers
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))

		// Extract trace context from MCP _meta field if present.
		// Per the MCP OTEL spec, servers should use traceparent/tracestate from
		// params._meta as the parent span context. This takes priority over HTTP
		// headers since _meta is the MCP-specified propagation mechanism.
		if parsedMCP := mcpparser.GetParsedMCPRequest(ctx); parsedMCP != nil && parsedMCP.Meta != nil {
			carrier := NewMetaCarrier(parsedMCP.Meta)
			ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
		}

		// Increment active connections
		m.activeConnections.Add(ctx, 1, metric.WithAttributes(
			attribute.String(coremetrics.LabelMCPServer, m.serverName),
			attribute.String(coremetrics.LabelTransport, m.transport),
		))
		defer m.activeConnections.Add(ctx, -1, metric.WithAttributes(
			attribute.String(coremetrics.LabelMCPServer, m.serverName),
			attribute.String(coremetrics.LabelTransport, m.transport),
		))

		// Legacy alias under the pre-rename label keys (server/transport).
		legacyConnAttrs := metric.WithAttributes(
			attribute.String("server", m.serverName),
			attribute.String("transport", m.transport),
		)
		m.legacyActiveConnections.Add(ctx, 1, legacyConnAttrs)
		defer m.legacyActiveConnections.Add(ctx, -1, legacyConnAttrs)

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

		// Record request start time
		startTime := time.Now()

		// Call the next handler with the instrumented context
		next.ServeHTTP(rw, r.WithContext(ctx))

		// Record completion metrics and finalize span
		duration := time.Since(startTime)
		m.finalizeSpan(span, rw, duration)
		m.recordMetrics(ctx, r, rw, duration)
	})
}

func (*HTTPMiddleware) createSpanName(ctx context.Context) string {
	parsedMCP := mcpparser.GetParsedMCPRequest(ctx)
	if parsedMCP == nil || parsedMCP.Method == "" {
		return ""
	}
	// OTEL MCP semconv: span name should be "{mcp.method.name} {target}"
	// where target is the tool/prompt/resource name when available.
	if parsedMCP.ResourceID != "" {
		return parsedMCP.Method + " " + parsedMCP.ResourceID
	}
	return parsedMCP.Method
}

// addHTTPAttributes adds standard HTTP attributes to the span.
func (m *HTTPMiddleware) addHTTPAttributes(span trace.Span, r *http.Request) {
	// New OTEL HTTP semantic convention attributes (always emitted)
	span.SetAttributes(
		attribute.String("http.request.method", r.Method),
		attribute.String("url.full", r.URL.String()),
		attribute.String("url.scheme", r.URL.Scheme),
		attribute.String("server.address", r.Host),
		attribute.String("url.path", r.URL.Path),
		attribute.String("user_agent.original", r.UserAgent()),
	)

	if r.ContentLength > 0 {
		span.SetAttributes(attribute.Int64("http.request.body.size", r.ContentLength))
	}

	if r.URL.RawQuery != "" {
		span.SetAttributes(attribute.String("url.query", r.URL.RawQuery))
	}

	// Legacy attribute names (emitted only when UseLegacyAttributes is true)
	if m.config.UseLegacyAttributes {
		span.SetAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.url", r.URL.String()),
			attribute.String("http.scheme", r.URL.Scheme),
			attribute.String("http.host", r.Host),
			attribute.String("http.target", r.URL.Path),
			attribute.String("http.user_agent", r.UserAgent()),
		)

		if contentLength := r.Header.Get("Content-Length"); contentLength != "" {
			span.SetAttributes(attribute.String("http.request_content_length", contentLength))
		}

		if r.URL.RawQuery != "" {
			span.SetAttributes(attribute.String("http.query", r.URL.RawQuery))
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

// addMCPAttributes adds MCP-specific attributes using the parsed MCP data from context.
func (m *HTTPMiddleware) addMCPAttributes(ctx context.Context, span trace.Span, r *http.Request) {
	// Get parsed MCP request from context (set by MCP parsing middleware)
	parsedMCP := mcpparser.GetParsedMCPRequest(ctx)
	if parsedMCP == nil {
		// No MCP data available, this might be a non-MCP request (e.g., health check)
		return
	}

	// New OTEL MCP semantic convention attributes (always emitted)
	span.SetAttributes(
		attribute.String("mcp.method.name", parsedMCP.Method),
		attribute.String("rpc.system.name", "jsonrpc"),
		attribute.String("jsonrpc.protocol.version", "2.0"),
	)

	if parsedMCP.ID != nil {
		span.SetAttributes(attribute.String("jsonrpc.request.id", formatRequestID(parsedMCP.ID)))
	}

	// Resource URI: only set for resource-related methods
	if parsedMCP.ResourceID != "" {
		switch parsedMCP.Method {
		case "resources/read", "resources/subscribe", "resources/unsubscribe", "notifications/resources/updated":
			span.SetAttributes(attribute.String("mcp.resource.uri", parsedMCP.ResourceID))
		}
	}

	// Legacy attribute names (emitted only when UseLegacyAttributes is true)
	if m.config.UseLegacyAttributes {
		span.SetAttributes(
			attribute.String("mcp.method", parsedMCP.Method),
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.service", "mcp"),
		)

		if parsedMCP.ID != nil {
			span.SetAttributes(attribute.String("mcp.request.id", formatRequestID(parsedMCP.ID)))
		}

		if parsedMCP.ResourceID != "" {
			span.SetAttributes(attribute.String("mcp.resource.id", parsedMCP.ResourceID))
		}
	}

	// Add method-specific attributes
	m.addMethodSpecificAttributes(span, parsedMCP)

	// Extract server name from the request
	serverName := m.extractServerName(r)
	span.SetAttributes(attribute.String("mcp.server.name", serverName))

	// Add network, client, and session attributes
	backendTransport := m.extractBackendTransport(r)
	m.addNetworkAttributes(span, r, backendTransport)

	if m.config.UseLegacyAttributes {
		span.SetAttributes(attribute.String("mcp.transport", backendTransport))
	}

	// Add batch indicator
	if parsedMCP.IsBatch {
		span.SetAttributes(attribute.Bool("mcp.is_batch", true))
	}
}

// addNetworkAttributes adds network, client, and session attributes to the span.
func (*HTTPMiddleware) addNetworkAttributes(span trace.Span, r *http.Request, backendTransport string) {
	networkTransport, protocolName, backendProtocolVersion := mapTransport(backendTransport)
	span.SetAttributes(attribute.String("network.transport", networkTransport))
	if protocolName != "" {
		span.SetAttributes(attribute.String("network.protocol.name", protocolName))
	}
	if backendProtocolVersion != "" {
		span.SetAttributes(attribute.String("mcp.backend.protocol.version", backendProtocolVersion))
	}

	// HTTP protocol version from the incoming request
	if protocolVer := httpProtocolVersion(r); protocolVer != "" {
		span.SetAttributes(attribute.String("network.protocol.version", protocolVer))
	}

	// Client address and port
	if clientAddr, clientPort := parseRemoteAddr(r.RemoteAddr); clientAddr != "" {
		span.SetAttributes(attribute.String("client.address", clientAddr))
		if clientPort > 0 {
			span.SetAttributes(attribute.Int("client.port", clientPort))
		}
	}

	// Session ID if available
	if sessionID := r.Header.Get("Mcp-Session-Id"); sessionID != "" {
		span.SetAttributes(attribute.String("mcp.session.id", sessionID))
	}

	// MCP protocol version from the streamable HTTP transport header
	if mcpVersion := r.Header.Get("MCP-Protocol-Version"); mcpVersion != "" {
		span.SetAttributes(attribute.String("mcp.protocol.version", mcpVersion))
	}
}

// addMethodSpecificAttributes adds attributes specific to certain MCP methods.
// Despite the name, the mcp.client.name block below runs for every method: under
// the Modern (stateless) MCP revision there is no initialize request, so per-request
// _meta.clientInfo is the only source of client attribution. It is a no-op for
// Legacy requests, which carry no _meta.clientInfo and still get mcp.client.name
// from the "initialize" case below.
func (m *HTTPMiddleware) addMethodSpecificAttributes(span trace.Span, parsedMCP *mcpparser.ParsedMCPRequest) {
	if name, ok := parsedMCP.ClientInfo["name"].(string); ok && name != "" {
		span.SetAttributes(attribute.String("mcp.client.name", name))
	}

	switch parsedMCP.Method {
	case string(mcp.MethodToolsCall):
		// New gen_ai namespace attributes (always emitted)
		if parsedMCP.ResourceID != "" {
			span.SetAttributes(attribute.String("gen_ai.tool.name", parsedMCP.ResourceID))
		}
		span.SetAttributes(attribute.String("gen_ai.operation.name", "execute_tool"))

		sanitizedArgs := m.sanitizeArguments(parsedMCP.Arguments)
		if sanitizedArgs != "" {
			span.SetAttributes(attribute.String("gen_ai.tool.call.arguments", sanitizedArgs))
		}

		// Legacy names
		if m.config.UseLegacyAttributes {
			if parsedMCP.ResourceID != "" {
				span.SetAttributes(attribute.String("mcp.tool.name", parsedMCP.ResourceID))
			}
			if sanitizedArgs != "" {
				span.SetAttributes(attribute.String("mcp.tool.arguments", sanitizedArgs))
			}
		}

	case methodPromptsGet:
		// New gen_ai namespace attribute (always emitted)
		if parsedMCP.ResourceID != "" {
			span.SetAttributes(attribute.String("gen_ai.prompt.name", parsedMCP.ResourceID))
		}

		// Legacy name
		if m.config.UseLegacyAttributes {
			if parsedMCP.ResourceID != "" {
				span.SetAttributes(attribute.String("mcp.prompt.name", parsedMCP.ResourceID))
			}
		}

	case "initialize":
		if parsedMCP.ResourceID != "" {
			span.SetAttributes(attribute.String("mcp.client.name", parsedMCP.ResourceID))
		}
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

func mapTransport(mcpTransport string) (networkTransport, protocolName, protocolVersion string) {
	switch mcpTransport {
	case "stdio":
		return "pipe", "", ""
	case "sse":
		return networkTransportTCP, networkProtocolHTTP, "1.1"
	case "streamable-http":
		return networkTransportTCP, networkProtocolHTTP, ""
	default:
		return networkTransportTCP, networkProtocolHTTP, ""
	}
}

// httpProtocolVersion extracts the HTTP protocol version from the request.
func httpProtocolVersion(r *http.Request) string {
	if r.ProtoMajor == 0 {
		return ""
	}
	if r.ProtoMajor >= 2 && r.ProtoMinor == 0 {
		return strconv.Itoa(r.ProtoMajor)
	}
	return fmt.Sprintf("%d.%d", r.ProtoMajor, r.ProtoMinor)
}

// parseRemoteAddr parses the remote address into host and port.
func parseRemoteAddr(remoteAddr string) (string, int) {
	if remoteAddr == "" {
		return "", 0
	}
	host, portStr, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr, 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return host, 0
	}
	return host, port
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

// finalizeSpan adds response attributes and sets the span status.
func (m *HTTPMiddleware) finalizeSpan(span trace.Span, rw *responseWriter, duration time.Duration) {
	// New OTEL HTTP semantic convention response attributes (always emitted)
	span.SetAttributes(
		attribute.Int("http.response.status_code", rw.statusCode),
		attribute.Int64("http.response.body.size", rw.bytesWritten),
	)

	// Legacy response attributes
	if m.config.UseLegacyAttributes {
		span.SetAttributes(
			attribute.Int("http.status_code", rw.statusCode),
			attribute.Int64("http.response_content_length", rw.bytesWritten),
			attribute.Float64("http.duration_ms", float64(duration.Nanoseconds())/1e6),
		)
	}

	// Set span status based on HTTP status code per OTEL semconv
	if rw.statusCode >= 500 {
		// 5xx: Server errors set span status to Error with error.type
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", rw.statusCode))
		span.SetAttributes(attribute.String("error.type", strconv.Itoa(rw.statusCode)))
	} else if rw.statusCode >= 400 {
		// 4xx: Client errors leave span status Unset (not server errors per OTEL semconv)
	} else {
		// 2xx/3xx: Success
		span.SetStatus(codes.Ok, "")
	}
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

// recordMetrics records request metrics.
func (m *HTTPMiddleware) recordMetrics(ctx context.Context, r *http.Request, rw *responseWriter, duration time.Duration) {
	// Get MCP method from context if available
	mcpMethod := mcpparser.GetMCPMethod(ctx)
	if mcpMethod == "" {
		mcpMethod = "unknown"
	}

	// Get the resource ID from the parsed MCP request if available.
	// For tools/call this is the tool name, for resources/read the URI,
	// and for prompts/get the prompt name. It feeds gen_ai.tool.name on the
	// semconv operation-duration metric and is deliberately kept off the current
	// custom request metrics to bound cardinality. The legacy aliases carry it
	// regardless: the pre-rename metrics had it, and reproducing them faithfully
	// matters more than narrowing them for the overlap window's lifetime.
	mcpResourceID := ""
	if parsedMCP := mcpparser.GetParsedMCPRequest(ctx); parsedMCP != nil {
		mcpResourceID = parsedMCP.ResourceID
	}

	// Record OTEL HTTP semconv duration for every request/response-cycle
	// request, regardless of MCP method. This is the transport-level
	// counterpart that stays valid for DELETE (session terminate) requests
	// carrying no MCP method. SSE-open GETs never reach recordMetrics — they
	// return early in Handler and record into sseConnectionDuration instead.
	m.recordHTTPServerDuration(ctx, r, rw.statusCode, duration)

	m.recordLegacyRequestMetrics(ctx, r, rw, mcpMethod, mcpResourceID, duration)

	// Record OTEL MCP spec mcp.server.operation.duration for actual MCP requests.
	// Only POST requests carry a JSON-RPC body; GET (SSE stream open) and DELETE
	// (session termination) are valid Streamable HTTP lifecycle requests with no
	// MCP method to record. An unknown method on a POST indicates either a
	// misconfigured middleware chain (see #3687) or a parse failure.
	if mcpMethod != "unknown" {
		m.recordOperationDuration(ctx, r, mcpMethod, mcpResourceID, rw.statusCode, duration)
	} else if r.Method == http.MethodPost {
		//nolint:gosec // G706: HTTP method and URL path from request
		slog.Warn("mcp method could not be determined, middleware may be misconfigured",
			"http_method", r.Method, "path", r.URL.Path)
	}
}

// recordLegacyRequestMetrics re-emits the deleted toolhive_mcp_requests,
// toolhive_mcp_request_duration and toolhive_mcp_tool_calls twins under their
// original names and label vocabulary, so a dashboard written against them keeps
// working for one release while it is migrated.
//
// The instruments are already no-ops when Config.UseLegacyMetrics is false, so
// the early return below is an optimization rather than a correctness guard: it
// skips building three attribute sets per request on the proxy's hot path.
//
// The attribute sets and the "any status >= 400 is an error" classification
// deliberately reproduce the pre-rename behaviour rather than the narrower
// semconv one — an alias that reports different numbers than the metric it
// replaces is worse than no alias.
func (m *HTTPMiddleware) recordLegacyRequestMetrics(
	ctx context.Context, r *http.Request, rw *responseWriter,
	mcpMethod, mcpResourceID string, duration time.Duration,
) {
	if !m.config.UseLegacyMetrics {
		return
	}

	status := "success"
	if rw.statusCode >= 400 {
		status = "error"
	}

	attrs := metric.WithAttributes(
		attribute.String("method", r.Method),
		attribute.String("status_code", strconv.Itoa(rw.statusCode)),
		attribute.String("status", status),
		attribute.String("mcp_method", mcpMethod),
		attribute.String("mcp_resource_id", mcpResourceID),
		attribute.String("server", m.serverName),
		attribute.String("transport", m.transport),
	)
	m.legacyRequests.Add(ctx, 1, attrs)
	m.legacyRequestDuration.Record(ctx, duration.Seconds(), attrs)

	if mcpMethod == string(mcp.MethodToolsCall) && mcpResourceID != "" {
		m.legacyToolCalls.Add(ctx, 1, metric.WithAttributes(
			attribute.String("server", m.serverName),
			attribute.String("tool", mcpResourceID),
			attribute.String("status", status),
		))
	}
}

// recordHTTPServerDuration records the http.server.request.duration OTEL HTTP
// semantic-convention metric. It carries the semconv attribute keys
// (http.request.method, url.scheme, http.response.status_code, plus
// network.protocol.version when known and error.type only on failure) and
// mcp_server, so a single Prometheus instance scraping several proxies can still
// split this metric per backend without a join — the toolhive_mcp_requests /
// _request_duration twins this metric replaces both carried an equivalent server
// label.
func (m *HTTPMiddleware) recordHTTPServerDuration(
	ctx context.Context, r *http.Request, statusCode int, duration time.Duration,
) {
	specAttrs := []attribute.KeyValue{
		attribute.String("http.request.method", normalizeHTTPMethod(r.Method)),
		attribute.String("url.scheme", requestScheme(r)),
		attribute.Int("http.response.status_code", statusCode),
		attribute.String(coremetrics.LabelMCPServer, m.serverName),
	}
	if pv := httpProtocolVersion(r); pv != "" {
		specAttrs = append(specAttrs, attribute.String("network.protocol.version", pv))
	}
	if statusCode >= 500 {
		specAttrs = append(specAttrs, attribute.String("error.type", strconv.Itoa(statusCode)))
	}
	m.httpServerReqDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(specAttrs...))
}

// knownHTTPMethods is the set semconv treats as recording-safe for
// http.request.method. Anything else must be recorded as attrValueOther.
var knownHTTPMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {},
	http.MethodPut: {}, http.MethodPatch: {}, http.MethodDelete: {},
	http.MethodConnect: {}, http.MethodOptions: {}, http.MethodTrace: {},
}

// normalizeHTTPMethod bounds http.request.method's cardinality. Go's net/http
// accepts any RFC 7230 token as a method, so an unauthenticated caller can
// otherwise mint a permanently-retained series per distinct method string.
// Semconv mandates recording unrecognized methods as _OTHER.
//
// The raw value is not lost: the span keeps it under http.request.method (spans
// are not a cardinality surface). Semconv would have the span carry it as
// http.request.method_original alongside an _OTHER http.request.method; that
// rename is deferred with the rest of the span-attribute work (RFC D9).
func normalizeHTTPMethod(method string) string {
	if _, ok := knownHTTPMethods[method]; ok {
		return method
	}
	return attrValueOther
}

// requestScheme reports the URL scheme for url.scheme, which semconv marks
// Required on http.server.request.duration. Server-side request URLs are
// typically scheme-less, so fall back to the TLS state.
func requestScheme(r *http.Request) string {
	if r.URL != nil && r.URL.Scheme != "" {
		return r.URL.Scheme
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// recordOperationDuration records the mcp.server.operation.duration metric
// per the OTEL MCP semantic conventions.
func (m *HTTPMiddleware) recordOperationDuration(
	ctx context.Context, r *http.Request, mcpMethod, resourceID string, statusCode int, duration time.Duration,
) {
	networkTransport, protocolName, _ := mapTransport(m.transport)

	// mcp.method.name arrives verbatim from the JSON-RPC body, so it is bounded
	// against the parser's own method set; the raw value stays on the span.
	if !mcpparser.IsKnownMethod(mcpMethod) {
		mcpMethod = attrValueOther
	}

	specAttrs := []attribute.KeyValue{
		attribute.String("mcp.method.name", mcpMethod),
		attribute.String("jsonrpc.protocol.version", "2.0"),
		attribute.String("network.transport", networkTransport),
		attribute.String(coremetrics.LabelMCPServer, m.serverName),
	}
	if protocolName != "" {
		specAttrs = append(specAttrs, attribute.String("network.protocol.name", protocolName))
	}
	if pv := httpProtocolVersion(r); pv != "" {
		specAttrs = append(specAttrs, attribute.String("network.protocol.version", pv))
	}

	// error.type: Conditionally required on error.
	// NOTE: This only captures HTTP-level errors (5xx). JSON-RPC errors returned
	// with HTTP 200 are not yet captured here — that requires response body parsing
	// which is tracked as future work (rpc.response.status_code, error.type for
	// JSON-RPC error codes like -32601).
	if statusCode >= 500 {
		specAttrs = append(specAttrs, attribute.String("error.type", strconv.Itoa(statusCode)))
	}

	// Method-specific attributes.
	//
	// NOTE: gen_ai.tool.name and gen_ai.prompt.name derive from params.name in
	// the client's JSON-RPC body and are NOT bounded against a resolved
	// capability set, so their cardinality is client-controlled. Bounding them
	// is tracked separately; the proxy has no tool list in scope here.
	switch mcpMethod {
	case string(mcp.MethodToolsCall):
		specAttrs = append(specAttrs, attribute.String("gen_ai.operation.name", "execute_tool"))
		if resourceID != "" {
			specAttrs = append(specAttrs, attribute.String("gen_ai.tool.name", resourceID))
		}
	case methodPromptsGet:
		if resourceID != "" {
			specAttrs = append(specAttrs, attribute.String("gen_ai.prompt.name", resourceID))
		}
	}

	m.operationDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(specAttrs...))
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

	// Add SSE-specific attributes
	networkTransport, protocolName, backendProtocolVersion := mapTransport(m.transport)
	span.SetAttributes(
		attribute.String("sse.event_type", "connection_established"),
		attribute.String("mcp.server.name", m.serverName),
		attribute.String("network.transport", networkTransport),
	)
	if protocolName != "" {
		span.SetAttributes(attribute.String("network.protocol.name", protocolName))
	}
	if backendProtocolVersion != "" {
		span.SetAttributes(attribute.String("mcp.backend.protocol.version", backendProtocolVersion))
	}
	if protocolVer := httpProtocolVersion(r); protocolVer != "" {
		span.SetAttributes(attribute.String("network.protocol.version", protocolVer))
	}
	if m.config.UseLegacyAttributes {
		span.SetAttributes(attribute.String("mcp.transport", m.transport))
	}

	// End the span immediately since this is just the connection establishment
	span.SetStatus(codes.Ok, "SSE connection established")
	span.End()

	// Legacy alias: toolhive_mcp_requests was incremented from here as well as
	// from recordMetrics, under a distinct mcp_method="sse_connection" series
	// carrying no mcp_resource_id. SSE-open requests return early in Handler and
	// never reach recordLegacyRequestMetrics, so without this a dashboard
	// counting connection establishments reads zero.
	m.legacyRequests.Add(ctx, 1, metric.WithAttributes(
		attribute.String("method", r.Method),
		attribute.String("status_code", "200"), // SSE connections start with 200
		attribute.String("status", "success"),
		attribute.String("mcp_method", "sse_connection"),
		attribute.String("server", m.serverName),
		attribute.String("transport", m.transport),
	))
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
		//nolint:gosec // G706: port number from config
		slog.Info("prometheus metrics will be exposed at /metrics",
			"port", runner.GetConfig().GetPort())
	}

	return nil
}

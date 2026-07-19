// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package audit provides audit logging functionality for ToolHive.
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// LevelAudit is a custom audit log level - between Info and Warn
const LevelAudit = slog.Level(2)

// contextKey is an unexported type for context keys to avoid collisions
type contextKey struct{}

// backendInfoKey is the context key for storing backend routing information
var backendInfoKey = contextKey{}

// BackendInfo stores backend routing information that can be mutated by handlers.
// This allows handlers deep in the call stack to provide backend info to the audit middleware.
type BackendInfo struct {
	BackendName string
}

// WithBackendInfo returns a new context with BackendInfo attached.
func WithBackendInfo(ctx context.Context, info *BackendInfo) context.Context {
	return context.WithValue(ctx, backendInfoKey, info)
}

// BackendInfoFromContext retrieves BackendInfo from the context.
// Returns (nil, false) if BackendInfo is not found in the context.
func BackendInfoFromContext(ctx context.Context) (*BackendInfo, bool) {
	info, ok := ctx.Value(backendInfoKey).(*BackendInfo)
	return info, ok
}

// NewAuditLogger creates a new structured audit logger that writes to the specified writer.
func NewAuditLogger(w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}

	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: LevelAudit,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// Replace the custom audit level with "AUDIT" string for better
			// compatibility with log aggregation systems (Loki, Elasticsearch, etc.)
			// that expect standard level names. This prevents audit events from
			// appearing as "INFO+2" which breaks level-based filtering.
			if a.Key == slog.LevelKey {
				if level, ok := a.Value.Any().(slog.Level); ok && level == LevelAudit {
					a.Value = slog.StringValue("AUDIT")
				}
			}
			return a
		},
	})

	return slog.New(handler)
}

// Auditor handles audit logging for HTTP requests.
type Auditor struct {
	config        *Config
	auditLogger   *slog.Logger
	transportType string // e.g., "sse", "streamable-http"
	logWriter     io.Writer
}

// NewAuditorWithTransport creates a new Auditor with the given configuration and transport information.
func NewAuditorWithTransport(config *Config, transportType string) (*Auditor, error) {
	var logWriter io.Writer = os.Stdout // default to stdout

	if config != nil {
		w, err := config.GetLogWriter()
		if err != nil {
			// Log error and fall back to stdout
			slog.Error("failed to open audit log file, falling back to stdout",
				"error", err)
			return nil, err
		}
		logWriter = w
	}

	return &Auditor{
		config:        config,
		auditLogger:   NewAuditLogger(logWriter),
		transportType: transportType,
		logWriter:     logWriter,
	}, nil
}

// Close closes the underlying log writer if it implements io.Closer.
// This should be called when the auditor is no longer needed to properly release resources.
func (a *Auditor) Close() error {
	if closer, ok := a.logWriter.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// isSSETransport checks if the current transport is SSE
func (a *Auditor) isSSETransport() bool {
	return a.transportType == types.TransportTypeSSE.String()
}

// errorDetectionBufferSize is the maximum number of bytes buffered from the
// response body for JSON-RPC error detection. JSON-RPC error responses have
// the "error" field near the top of the object, so a small prefix is
// sufficient. This buffer is allocated independently of IncludeResponseData.
const errorDetectionBufferSize = 512

// maxAuditErrorMessageLength caps the JSON-RPC error message length stored
// in audit event metadata to keep log entries compact.
const maxAuditErrorMessageLength = 256

// responseWriter wraps http.ResponseWriter to capture response data and status.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	body       *bytes.Buffer
	// errorDetectionBody is a small prefix buffer used exclusively for
	// JSON-RPC error detection. It is allocated when DetectApplicationErrors
	// is true, independent of IncludeResponseData.
	errorDetectionBody *bytes.Buffer
	auditor            *Auditor
}

func (rw *responseWriter) WriteHeader(statusCode int) {
	rw.statusCode = statusCode
	rw.ResponseWriter.WriteHeader(statusCode)
}

func (rw *responseWriter) Write(data []byte) (int, error) {
	// Capture response data if configured
	if rw.auditor.config.IncludeResponseData && rw.body != nil {
		// Limit the size of captured data
		if rw.body.Len()+len(data) <= rw.auditor.config.MaxDataSize {
			rw.body.Write(data)
		}
	}
	// Capture a small prefix for JSON-RPC error detection
	if rw.errorDetectionBody != nil && rw.errorDetectionBody.Len() < errorDetectionBufferSize {
		remaining := errorDetectionBufferSize - rw.errorDetectionBody.Len()
		if len(data) <= remaining {
			rw.errorDetectionBody.Write(data)
		} else {
			rw.errorDetectionBody.Write(data[:remaining])
		}
	}
	return rw.ResponseWriter.Write(data)
}

// Flush implements http.Flusher if the underlying ResponseWriter supports it.
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap exposes the underlying ResponseWriter so http.ResponseController
// can reach interfaces this wrapper does not re-implement (e.g. SetWriteDeadline).
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// isMCPStreamOpenRequest returns true only for MCP "stream" opens:
// - SSE transport's SSE endpoint (GET + Accept: text/event-stream)
// - Streamable HTTP's GET stream (same header pattern)
// Everything else (including POST message sends) is non-sticky.
func (*Auditor) isMCPStreamOpenRequest(r *http.Request) bool {
	// Optional hardening: limit to your MCP base path(s)
	// if !strings.HasPrefix(r.URL.Path, a.config.MCPBasePath) { return false }

	if r.Method != http.MethodGet {
		return false
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(strings.ToLower(accept), "text/event-stream")
}

// ensureAuditContext injects the mutable carriers the auditor reads after the
// inner chain returns: BackendInfo (backend routing), an auth.IdentityHolder
// (identity attached by an auth middleware running INSIDE audit), and an
// mcp.ParsedRequestHolder (parsed MCP data from a parser running INSIDE
// audit). Each is only injected when absent so nested auditors share carriers.
func ensureAuditContext(r *http.Request) *http.Request {
	ctx := r.Context()
	changed := false
	if _, ok := BackendInfoFromContext(ctx); !ok {
		ctx = WithBackendInfo(ctx, &BackendInfo{})
		changed = true
	}
	if _, ok := auth.IdentityHolderFromContext(ctx); !ok {
		ctx = auth.WithIdentityHolder(ctx, &auth.IdentityHolder{})
		changed = true
	}
	if _, ok := mcp.ParsedRequestHolderFromContext(ctx); !ok {
		ctx = mcp.WithParsedRequestHolder(ctx, &mcp.ParsedRequestHolder{})
		changed = true
	}
	if !changed {
		return r
	}
	return r.WithContext(ctx)
}

// Middleware creates an HTTP middleware that logs audit events.
func (a *Auditor) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = ensureAuditContext(r)

		// Handle MCP stream opens (SSE endpoint, streamable GET) specially:
		// these connections are long-lived, so instead of waiting for the
		// response to complete, the connection event is logged on the FIRST
		// write. By then any inner auth middleware has run, so the event
		// carries the authenticated identity (or records the 401/403 denial).
		if a.isMCPStreamOpenRequest(r) {
			sw := &streamOpenWriter{ResponseWriter: w, auditor: a, req: r}
			next.ServeHTTP(sw, r)
			// Streams that end without a single write still get an event
			// (net/http sends an implicit 200 in that case).
			sw.logOnce(http.StatusOK)
			return
		}

		startTime := time.Now()

		// Capture request data if configured
		var requestData []byte
		if a.config.IncludeRequestData && r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err == nil {
				// Always restore the body for the next handler
				r.Body = io.NopCloser(bytes.NewReader(body))
				// Only capture for auditing if within size limit
				if len(body) <= a.config.MaxDataSize {
					requestData = body
				}
			}
		}

		// Wrap the response writer to capture response data and status
		rw := &responseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK, // Default status
			auditor:        a,
		}

		if a.config.IncludeResponseData {
			rw.body = &bytes.Buffer{}
		}

		// Allocate a small prefix buffer for JSON-RPC error detection,
		// independent of IncludeResponseData. When IncludeResponseData
		// is already true, we reuse rw.body instead of double-buffering.
		if a.config.ShouldDetectApplicationErrors() && !a.config.IncludeResponseData {
			rw.errorDetectionBody = &bytes.Buffer{}
		}

		// Process the request
		next.ServeHTTP(rw, r)

		// Calculate duration
		duration := time.Since(startTime)

		// Create and log the audit event
		a.logAuditEvent(r, rw, requestData, duration)
	})
}

// logAuditEvent creates and logs an audit event for the HTTP request.
func (a *Auditor) logAuditEvent(r *http.Request, rw *responseWriter, requestData []byte, duration time.Duration) {
	// Determine event type based on the request
	eventType := a.determineEventType(r)

	// Determine outcome based on status code
	outcome := a.determineOutcome(rw.statusCode)

	// When HTTP status indicates success, check for JSON-RPC errors
	// hidden inside HTTP 200 responses.
	var mcpResponse *mcp.ParsedMCPResponse
	if outcome == OutcomeSuccess && a.config.ShouldDetectApplicationErrors() {
		mcpResponse = a.detectApplicationError(rw)
		if mcpResponse != nil && mcpResponse.HasError {
			outcome = OutcomeApplicationError
		}
	}

	// Check if we should audit this event
	if !a.config.ShouldAuditEvent(eventType) {
		return
	}

	// Extract source information
	source := a.extractSource(r)

	// Extract subject information
	subjects := a.extractSubjects(r)

	// Determine component name
	component := a.determineComponent(r)

	// Create the audit event
	event := NewAuditEvent(eventType, source, outcome, subjects, component)

	// Add target information
	target := a.extractTarget(r, eventType)
	if len(target) > 0 {
		event.WithTarget(target)
	}

	// Add metadata
	a.addMetadata(event, r, duration, rw)

	// Attach JSON-RPC error details so operators can see the error code
	// and message without enabling full response data capture.
	if outcome == OutcomeApplicationError {
		if event.Metadata.Extra == nil {
			event.Metadata.Extra = make(map[string]any)
		}
		event.Metadata.Extra["jsonrpc_error_code"] = mcpResponse.ErrorCode
		msg := mcpResponse.ErrorMessage
		if len(msg) > maxAuditErrorMessageLength {
			msg = msg[:maxAuditErrorMessageLength]
		}
		event.Metadata.Extra["jsonrpc_error_message"] = msg
	}

	// Add request/response data if configured
	a.addEventData(event, r, rw, requestData)

	// Log the audit event
	event.LogTo(r.Context(), a.auditLogger, LevelAudit)
}

// mcpMethodFor returns the parsed MCP method for the request, whether the
// parser ran outside audit (context value) or inside it (holder filled by
// the parser and read back after the inner chain returns).
func mcpMethodFor(r *http.Request) string {
	if m := mcp.GetMCPMethod(r.Context()); m != "" {
		return m
	}
	if holder, ok := mcp.ParsedRequestHolderFromContext(r.Context()); ok && holder.Parsed != nil {
		return holder.Parsed.Method
	}
	return ""
}

// mcpResourceIDFor returns the parsed MCP resource ID for the request, with
// the same context-then-holder fallback as mcpMethodFor.
func mcpResourceIDFor(r *http.Request) string {
	if id := mcp.GetMCPResourceID(r.Context()); id != "" {
		return id
	}
	if holder, ok := mcp.ParsedRequestHolderFromContext(r.Context()); ok && holder.Parsed != nil {
		return holder.Parsed.ResourceID
	}
	return ""
}

// determineEventType determines the event type based on the HTTP request.
func (a *Auditor) determineEventType(r *http.Request) string {
	// First, try to get the parsed MCP method
	if mcpMethod := mcpMethodFor(r); mcpMethod != "" {
		return a.mapMCPMethodToEventType(mcpMethod)
	}

	// Handle SSE connection establishment
	if a.isSSETransport() && r.Method == http.MethodGet {
		return EventTypeSSEConnection
	}
	// Handle MCP message endpoints that weren't parsed (malformed requests)
	if a.isSSETransport() && r.Method == http.MethodPost {
		return EventTypeMCPRequest
	}

	// Default for non-MCP requests
	return EventTypeHTTPRequest
}

// mapMCPMethodToEventType maps MCP method names to event types.
func (*Auditor) mapMCPMethodToEventType(mcpMethod string) string {
	switch mcpMethod {
	case "initialize":
		return EventTypeMCPInitialize
	case "tools/call":
		return EventTypeMCPToolCall
	case "tools/list":
		return EventTypeMCPToolsList
	case "resources/read":
		return EventTypeMCPResourceRead
	case "resources/list":
		return EventTypeMCPResourcesList
	case "prompts/get":
		return EventTypeMCPPromptGet
	case "prompts/list":
		return EventTypeMCPPromptsList
	case "notifications/message":
		return EventTypeMCPNotification
	case "ping":
		return EventTypeMCPPing
	case "logging/setLevel":
		return EventTypeMCPLogging
	case "completion/complete":
		return EventTypeMCPCompletion
	case "notifications/roots/list_changed":
		return EventTypeMCPRootsListChanged
	default:
		return EventTypeMCPRequest
	}
}

// determineOutcome determines the outcome based on the HTTP status code.
func (*Auditor) determineOutcome(statusCode int) string {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return OutcomeSuccess
	case statusCode == 401 || statusCode == 403:
		return OutcomeDenied
	case statusCode >= 400 && statusCode < 500:
		return OutcomeFailure
	case statusCode >= 500:
		return OutcomeError
	default:
		return OutcomeSuccess
	}
}

// detectApplicationError inspects the captured response body prefix for a
// JSON-RPC error field. It reuses rw.body when IncludeResponseData is
// enabled to avoid double-buffering.
func (*Auditor) detectApplicationError(rw *responseWriter) *mcp.ParsedMCPResponse {
	var prefix []byte
	if rw.body != nil && rw.body.Len() > 0 {
		prefix = rw.body.Bytes()
		if len(prefix) > errorDetectionBufferSize {
			prefix = prefix[:errorDetectionBufferSize]
		}
	} else if rw.errorDetectionBody != nil && rw.errorDetectionBody.Len() > 0 {
		prefix = rw.errorDetectionBody.Bytes()
	}
	if len(prefix) > 0 && prefix[0] == '{' {
		return mcp.ParseMCPResponse(prefix)
	}
	return nil
}

// extractSource extracts source information from the HTTP request.
func (a *Auditor) extractSource(r *http.Request) EventSource {
	// Get the client IP address
	clientIP := a.getClientIP(r)

	source := EventSource{
		Type:  SourceTypeNetwork,
		Value: clientIP,
		Extra: make(map[string]any),
	}

	// Add user agent if available
	if userAgent := r.Header.Get("User-Agent"); userAgent != "" {
		source.Extra[SourceExtraKeyUserAgent] = userAgent
	}

	// Add request ID if available
	if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
		source.Extra[SourceExtraKeyRequestID] = requestID
	}

	return source
}

// getClientIP extracts the client IP address from the request.
func (*Auditor) getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header first
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP in the list
		if ips := strings.Split(xff, ","); len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}

	return r.RemoteAddr
}

// extractSubjectsFromIdentity extracts subject information from an Identity.
// This helper ensures consistent fallback order and validation across all auditors.
// Fallback order for user: Name → PreferredUsername → Email
func extractSubjectsFromIdentity(identity *auth.Identity) map[string]string {
	subjects := make(map[string]string)

	// Extract user ID (subject)
	if identity.Subject != "" {
		subjects[SubjectKeyUserID] = identity.Subject
	}

	// Extract user name with fallback order: Name → PreferredUsername → Email
	if identity.Name != "" {
		subjects[SubjectKeyUser] = identity.Name
	} else if preferredUsername, ok := identity.Claims["preferred_username"].(string); ok && preferredUsername != "" {
		subjects[SubjectKeyUser] = preferredUsername
	} else if identity.Email != "" {
		subjects[SubjectKeyUser] = identity.Email
	}

	// Add client information if available
	if clientName, ok := identity.Claims["client_name"].(string); ok && clientName != "" {
		subjects[SubjectKeyClientName] = clientName
	}

	if clientVersion, ok := identity.Claims["client_version"].(string); ok && clientVersion != "" {
		subjects[SubjectKeyClientVersion] = clientVersion
	}

	return subjects
}

// extractSubjects extracts subject information from the HTTP request.
func (*Auditor) extractSubjects(r *http.Request) map[string]string {
	subjects := make(map[string]string)

	// Extract user information from Identity. The context value is present
	// when an auth middleware runs OUTSIDE audit; the holder covers the
	// audit-wraps-auth arrangement, where the identity attached for inner
	// handlers is published back up via auth.WithIdentity.
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		if holder, hok := auth.IdentityHolderFromContext(r.Context()); hok && holder.Identity != nil {
			identity, ok = holder.Identity, true
		}
	}
	if ok {
		subjects = extractSubjectsFromIdentity(identity)
	}

	// If no user found in claims, set anonymous
	if subjects[SubjectKeyUser] == "" {
		subjects[SubjectKeyUser] = "anonymous"
	}

	return subjects
}

// determineComponent determines the component name based on the request.
func (a *Auditor) determineComponent(_ *http.Request) string {
	// Use the component from configuration if set
	if a.config.Component != "" {
		return a.config.Component
	}
	// For MCP requests, we could extract the server name from the path or headers
	// For now, we'll use a default component name
	return ComponentToolHive
}

// extractTarget extracts target information from the HTTP request.
func (*Auditor) extractTarget(r *http.Request, eventType string) map[string]string {
	target := make(map[string]string)

	target[TargetKeyEndpoint] = r.URL.Path
	target[TargetKeyMethod] = r.Method

	// Add MCP method if available from parsed data
	if mcpMethod := mcpMethodFor(r); mcpMethod != "" {
		target[TargetKeyMethod] = mcpMethod
	}

	// Add resource ID if available from parsed data
	if resourceID := mcpResourceIDFor(r); resourceID != "" {
		target[TargetKeyName] = resourceID
	}

	// Add event-specific target information
	switch eventType {
	case EventTypeMCPToolCall:
		target[TargetKeyType] = TargetTypeTool
	case EventTypeMCPResourceRead:
		target[TargetKeyType] = TargetTypeResource
	case EventTypeMCPPromptGet:
		target[TargetKeyType] = TargetTypePrompt
	default:
		target[TargetKeyType] = "endpoint"
	}

	return target
}

// addMetadata adds metadata to the audit event.
func (a *Auditor) addMetadata(event *AuditEvent, r *http.Request, duration time.Duration, rw *responseWriter) {
	if event.Metadata.Extra == nil {
		event.Metadata.Extra = make(map[string]any)
	}

	// Add duration
	event.Metadata.Extra[MetadataExtraKeyDuration] = duration.Milliseconds()

	// Add transport information
	if a.isSSETransport() {
		event.Metadata.Extra[MetadataExtraKeyTransport] = "sse"
	} else {
		event.Metadata.Extra[MetadataExtraKeyTransport] = "http"
	}

	// Add response size if available
	if rw.body != nil {
		event.Metadata.Extra[MetadataExtraKeyResponseSize] = rw.body.Len()
	}

	// Add backend routing information from context if available.
	// BackendInfo is populated upstream: by the backend-enrichment middleware on the
	// vMCP legacy path, or by the vMCP Serve-path session handlers (which label the
	// request directly). Either way the value is written before this runs.
	if backendInfo, ok := BackendInfoFromContext(r.Context()); ok && backendInfo != nil && backendInfo.BackendName != "" {
		event.Metadata.Extra["backend_name"] = backendInfo.BackendName
	}
}

// addEventData adds request/response data to the audit event if configured.
func (a *Auditor) addEventData(event *AuditEvent, _ *http.Request, rw *responseWriter, requestData []byte) {
	if !a.config.IncludeRequestData && !a.config.IncludeResponseData {
		return
	}

	data := make(map[string]any)

	if a.config.IncludeRequestData && len(requestData) > 0 {
		// Try to parse as JSON, otherwise store as string
		var requestJSON any
		if err := json.Unmarshal(requestData, &requestJSON); err == nil {
			data["request"] = requestJSON
		} else {
			data["request"] = string(requestData)
		}
	}

	if a.config.IncludeResponseData && rw.body != nil && rw.body.Len() > 0 {
		responseData := rw.body.Bytes()
		// Try to parse as JSON, otherwise store as string
		var responseJSON any
		if err := json.Unmarshal(responseData, &responseJSON); err == nil {
			data["response"] = responseJSON
		} else {
			data["response"] = string(responseData)
		}
	}

	if len(data) > 0 {
		if dataBytes, err := json.Marshal(data); err == nil {
			rawMsg := json.RawMessage(dataBytes)
			event.WithData(&rawMsg)
		}
	}
}

// streamOpenWriter wraps the ResponseWriter for MCP stream-open requests
// (SSE endpoint, streamable GET). It logs the connection audit event exactly
// once, on the first WriteHeader/Write, so the event reflects the actual
// outcome (200 stream established, 401/403 denied by inner middleware) and
// carries the identity the inner auth middleware attached by that point.
type streamOpenWriter struct {
	http.ResponseWriter
	auditor *Auditor
	req     *http.Request
	logged  bool
}

func (sw *streamOpenWriter) WriteHeader(statusCode int) {
	// Informational (1xx) responses are not the final status — don't consume
	// the one-shot connection event on them.
	if statusCode >= http.StatusOK {
		sw.logOnce(statusCode)
	}
	sw.ResponseWriter.WriteHeader(statusCode)
}

func (sw *streamOpenWriter) Write(data []byte) (int, error) {
	// An implicit WriteHeader(200) happens on first Write.
	sw.logOnce(http.StatusOK)
	return sw.ResponseWriter.Write(data)
}

// Flush implements http.Flusher if the underlying ResponseWriter supports it.
func (sw *streamOpenWriter) Flush() {
	if flusher, ok := sw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap exposes the underlying ResponseWriter so http.ResponseController
// can reach interfaces this wrapper does not re-implement (e.g. SetWriteDeadline).
func (sw *streamOpenWriter) Unwrap() http.ResponseWriter {
	return sw.ResponseWriter
}

// logOnce logs the stream connection event with the given status on the first
// call; subsequent calls are no-ops.
func (sw *streamOpenWriter) logOnce(statusCode int) {
	if sw.logged {
		return
	}
	sw.logged = true
	sw.auditor.logSSEConnectionEvent(sw.req, statusCode)
}

// logSSEConnectionEvent logs an audit event for SSE connection initiation.
func (a *Auditor) logSSEConnectionEvent(r *http.Request, statusCode int) {
	// Honor the configured event-type filter, like logAuditEvent does.
	if !a.config.ShouldAuditEvent(EventTypeSSEConnection) {
		return
	}

	// Extract source information
	source := a.extractSource(r)

	// Extract subject information
	subjects := a.extractSubjects(r)

	// Determine component name
	component := a.determineComponent(r)

	// Create the audit event for SSE connection
	event := NewAuditEvent(EventTypeSSEConnection, source, a.determineOutcome(statusCode), subjects, component)

	// Add target information
	target := map[string]string{
		"endpoint": r.URL.Path,
		"method":   r.Method,
		"type":     "sse_endpoint",
	}
	event.WithTarget(target)

	// Add metadata
	event.Metadata.Extra = map[string]any{
		"transport":  a.transportType,
		"user_agent": r.Header.Get("User-Agent"),
	}

	// Log the event
	event.LogTo(r.Context(), a.auditLogger, LevelAudit)
}

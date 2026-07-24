// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package streamable provides a streamable HTTP proxy for MCP servers.
package streamable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/exp/jsonrpc2"

	sdkmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/bodylimit"
	"github.com/stacklok/toolhive/pkg/healthcheck"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// StreamableHTTPEndpoint is the endpoint for streamable HTTP.
	StreamableHTTPEndpoint = "/mcp"

	// defaultRequestTimeout is the maximum time to wait for an MCP request to
	// complete. Override with TOOLHIVE_PROXY_REQUEST_TIMEOUT (e.g. "30s", "2m").
	defaultRequestTimeout = 60 * time.Second

	// proxyRequestTimeoutEnv is the environment variable that overrides the
	// default proxy request timeout.
	proxyRequestTimeoutEnv = "TOOLHIVE_PROXY_REQUEST_TIMEOUT"

	// defaultReadTimeout bounds reading the entire request (headers + body) on
	// the proxy http.Server, mitigating slow-upload connection exhaustion. It
	// does not affect responses, so SSE response streams are unaffected. Matches
	// the vMCP server default.
	defaultReadTimeout = 30 * time.Second

	// proxyChannelBufferSize is the buffer size for the messageCh and
	// responseCh channels connecting the HTTP proxy to the container runner.
	proxyChannelBufferSize = 100
)

// HTTPProxy implements a proxy for streamable HTTP transport.
type HTTPProxy struct {
	host              string
	port              int
	requestTimeout    time.Duration
	readTimeout       time.Duration
	shutdownCh        chan struct{}
	prometheusHandler http.Handler
	middlewares       []types.NamedMiddleware

	// Message channel for sending JSON-RPC to the container (from HTTP -> runner)
	messageCh chan jsonrpc2.Message
	// Response channel for receiving JSON-RPC from the container (runner -> HTTP)
	responseCh chan jsonrpc2.Message

	// Session manager for streamable HTTP sessions
	sessionManager *session.Manager

	// sessionTTL is the resolved inactivity timeout for the session manager.
	// Defaults to session.DefaultSessionTTL; overridable via WithSessionTTL.
	sessionTTL time.Duration

	// sessionStorage is the optional custom storage backend for the session manager.
	// When nil, in-memory LocalStorage is used. Set via WithSessionStorage.
	sessionStorage session.Storage

	// authInfoHandler is the optional RFC 9728 OAuth protected resource discovery handler.
	// When nil, /.well-known/ returns a clean JSON 404. Set via WithAuthInfoHandler.
	authInfoHandler http.Handler

	// prefixHandlers contains additional HTTP handlers mounted outside the middleware chain.
	// Keys are URL path prefixes. Set via WithPrefixHandlers.
	prefixHandlers map[string]http.Handler

	// Waiters keyed by compositeKey(sessID, idKey) -> one-shot channel for response delivery.
	// The composite key MUST be unique per concurrent request; sharing it across requests
	// (e.g. with sessID="" for sessionless requests) silently overwrites entries and crosses
	// response payloads between unrelated clients. See resolveSessionForRequest.
	waiters sync.Map // map[string]chan jsonrpc2.Message
	// Keyed by the same compositeKey(sessID, idKey); stores the original client JSON-RPC ID
	// to restore before replying. Same uniqueness requirement as `waiters`.
	idRestore sync.Map // map[string]jsonrpc2.ID

	// serverStreams delivers server->client notifications to connected
	// sessions' SSE streams (the standalone GET stream for global
	// notifications, or a specific session's stream for subscription-scoped
	// ones). See dispatcher_streams.go.
	serverStreams *serverStreamRegistry

	// routing owns the per-session routing state (progress tokens,
	// subscriptions, log levels) that dispatcher.go's routeNotification uses
	// to deliver server->client messages to the correct session(s) without
	// cross-session leakage. See dispatcher_routing.go.
	routing *sessionRouter

	// uriLocks serializes each uri's resources/subscribe|unsubscribe ref-count
	// decision with the upstream forward it may trigger, so concurrent
	// subscribe/unsubscribe calls for the SAME uri from different sessions can
	// never reach the backend out of order. See keyed_mutex.go and
	// handlePost/interceptSessionScopedRequest.
	uriLocks *keyedMutex

	// standaloneSSE controls whether GET /mcp opens a standalone SSE stream for
	// server->client notifications (the default) or returns 405. Set via
	// WithStandaloneSSE(false) to opt out.
	standaloneSSE bool

	// strictProtocolValidation controls whether handlePost rejects a request
	// whose MCP-Protocol-Version header names an unknown/unsupported MCP
	// revision (see isSupportedMCPVersion) with HTTP 400. Default false: any
	// version string is accepted, since this proxy is transport-level and does
	// not depend on a specific MCP revision. An absent header is always
	// accepted, in either mode, per the streamable HTTP spec's rule to assume
	// 2025-03-26 when the header is missing. Set via WithStrictProtocolValidation.
	strictProtocolValidation bool

	// Health checker
	healthChecker *healthcheck.HealthChecker

	server   *http.Server
	stopOnce sync.Once
}

// Option configures an HTTPProxy.
type Option func(*HTTPProxy)

// WithSessionStorage injects a custom storage backend into the session manager.
// When not provided, the proxy uses in-memory LocalStorage (single-replica default).
func WithSessionStorage(storage session.Storage) Option {
	return func(p *HTTPProxy) {
		if storage == nil {
			return
		}
		p.sessionStorage = storage
	}
}

// WithSessionTTL overrides the session inactivity timeout used by this proxy.
// Zero or negative values are ignored so the constructor's default is preserved.
func WithSessionTTL(ttl time.Duration) Option {
	return func(p *HTTPProxy) {
		if ttl <= 0 {
			return
		}
		p.sessionTTL = ttl
	}
}

// WithReadTimeout overrides http.Server.ReadTimeout for this proxy, which bounds
// reading the entire request (headers + body). Zero or negative values are
// ignored so the constructor's default (defaultReadTimeout) is preserved.
func WithReadTimeout(d time.Duration) Option {
	return func(p *HTTPProxy) {
		if d <= 0 {
			return
		}
		p.readTimeout = d
	}
}

// WithAuthInfoHandler sets the handler for RFC 9728 OAuth protected resource discovery.
// When set, the handler is mounted at /.well-known/ outside the middleware chain.
// When nil, /.well-known/ returns a clean JSON 404 so OAuth clients parse it cleanly.
func WithAuthInfoHandler(h http.Handler) Option {
	return func(p *HTTPProxy) {
		p.authInfoHandler = h
	}
}

// WithPrefixHandlers registers additional HTTP handlers mounted before the MCP endpoint.
// These are mounted outside the middleware chain (RFC 9728, embedded auth server routes).
func WithPrefixHandlers(handlers map[string]http.Handler) Option {
	return func(p *HTTPProxy) {
		p.prefixHandlers = maps.Clone(handlers)
	}
}

// WithStandaloneSSE controls whether GET /mcp opens a standalone SSE stream
// for server->client notifications. It defaults to enabled; pass false to opt
// out and restore the prior behavior of returning 405 for GET requests.
func WithStandaloneSSE(enabled bool) Option {
	return func(p *HTTPProxy) {
		p.standaloneSSE = enabled
	}
}

// WithStrictProtocolValidation enables strict MCP-Protocol-Version checking.
// When enabled, a request whose MCP-Protocol-Version header names an
// unsupported/unknown MCP revision is rejected with HTTP 400. An absent
// header is still accepted (the streamable HTTP spec says to assume
// 2025-03-26). Default (false) preserves the version-agnostic behavior:
// any version string is accepted.
func WithStrictProtocolValidation(enabled bool) Option {
	return func(p *HTTPProxy) { p.strictProtocolValidation = enabled }
}

// NewHTTPProxy creates a new HTTPProxy for streamable HTTP transport.
func NewHTTPProxy(
	host string,
	port int,
	prometheusHandler http.Handler,
	middlewares []types.NamedMiddleware,
	opts ...Option,
) *HTTPProxy {
	// Use typed Streamable sessions
	sFactory := func(id string) session.Session { return session.NewStreamableSession(id) }

	proxy := &HTTPProxy{
		host:              host,
		port:              port,
		requestTimeout:    resolveRequestTimeout(),
		readTimeout:       defaultReadTimeout,
		shutdownCh:        make(chan struct{}),
		prometheusHandler: prometheusHandler,
		middlewares:       middlewares,
		messageCh:         make(chan jsonrpc2.Message, proxyChannelBufferSize),
		responseCh:        make(chan jsonrpc2.Message, proxyChannelBufferSize),
		sessionTTL:        session.DefaultSessionTTL,
		serverStreams:     newServerStreamRegistry(),
		routing:           newSessionRouter(),
		uriLocks:          newKeyedMutex(),
		standaloneSSE:     true,
	}

	for _, opt := range opts {
		opt(proxy)
	}

	// Construct the session manager once, after options have resolved sessionTTL and sessionStorage.
	if proxy.sessionStorage != nil {
		proxy.sessionManager = session.NewManagerWithStorage(proxy.sessionTTL, sFactory, proxy.sessionStorage)
	} else {
		proxy.sessionManager = session.NewManager(proxy.sessionTTL, sFactory)
	}

	// Create health checker without MCP pinger
	// Streamable transport doesn't support MCP ping, so health check only verifies proxy is running
	proxy.healthChecker = healthcheck.NewHealthChecker(string(types.TransportTypeStreamableHTTP), nil)

	return proxy
}

// Start starts the HTTPProxy server.
func (p *HTTPProxy) Start(_ context.Context) error {
	mux := http.NewServeMux()
	mux.Handle(StreamableHTTPEndpoint, p.applyMiddlewares(http.HandlerFunc(p.handleStreamableRequest)))

	// Add health check endpoint (no middlewares)
	if p.healthChecker != nil {
		mux.Handle("/health", p.healthChecker)
	}

	if p.prometheusHandler != nil {
		mux.Handle("/metrics", p.prometheusHandler)
	}

	// Mount prefix handlers (e.g. embedded auth server routes) outside the middleware chain.
	// RFC 9728 requires discovery endpoints to be reachable without authentication.
	for prefix, h := range p.prefixHandlers {
		mux.Handle(prefix, h)
		slog.Debug("mounted prefix handler", "prefix", prefix)
	}

	// Mount RFC 9728 OAuth protected resource discovery endpoint (no middlewares).
	// Always register so OAuth discovery gets a clean JSON 404 when auth is off.
	wellKnownHandler := auth.NewWellKnownHandler(p.authInfoHandler)
	mux.Handle("/.well-known/", wellKnownHandler)
	if p.authInfoHandler != nil {
		slog.Debug("rfc 9728 OAuth discovery endpoint enabled at /.well-known/oauth-protected-resource")
	}

	p.server = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", p.host, p.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       p.readTimeout,
	}

	// Route container responses to matching waiter channels
	go p.dispatchResponses()

	// Periodically bound routing's subscription/log-level maps, and
	// serverStreams' standalone GET streams, to active sessions (see
	// reapRoutingState's doc comment for the isActive tradeoff).
	go p.reapRoutingState()

	go func() {
		slog.Debug("streamable HTTP proxy started", "port", p.port)
		//nolint:gosec // G706: logging configured host and port
		slog.Debug("streamable HTTP endpoint",
			"url", fmt.Sprintf("http://%s:%d%s", p.host, p.port, StreamableHTTPEndpoint))
		if err := p.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("streamable HTTP server error", "error", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the HTTPProxy server.
func (p *HTTPProxy) Stop(ctx context.Context) error {
	var err error

	p.stopOnce.Do(func() {
		close(p.shutdownCh)

		// Signal every live GET SSE stream's stop channel so in-flight handlers
		// observe a closed per-stream stop signal and unblock/return promptly,
		// rather than relying solely on shutdownCh (which they also select on).
		p.serverStreams.closeAll()

		// Stop session manager cleanup; active sessions expire via TTL
		if p.sessionManager != nil {
			if err := p.sessionManager.Stop(); err != nil {
				slog.Error("failed to stop session manager", "error", err)
			}
		}

		if p.server != nil {
			if e := p.server.Shutdown(ctx); e != nil {
				err = e
			}
		}
	})

	return err
}

// IsRunning checks if the proxy is running.
func (p *HTTPProxy) IsRunning() (bool, error) {
	select {
	case <-p.shutdownCh:
		return false, nil
	default:
		return true, nil
	}
}

// GetMessageChannel returns the message channel for sending JSON-RPC to the container.
func (p *HTTPProxy) GetMessageChannel() chan jsonrpc2.Message {
	return p.messageCh
}

// GetResponseChannel returns the response channel for receiving JSON-RPC from the container.
func (p *HTTPProxy) GetResponseChannel() <-chan jsonrpc2.Message {
	return p.responseCh
}

// SendMessageToDestination sends a message to the container.
func (p *HTTPProxy) SendMessageToDestination(msg jsonrpc2.Message) error {
	select {
	case p.messageCh <- msg:
		return nil
	default:
		return fmt.Errorf("failed to send message to destination")
	}
}

// ForwardResponseToClients forwards a response from the container to the client.
func (p *HTTPProxy) ForwardResponseToClients(_ context.Context, msg jsonrpc2.Message) error {
	select {
	case p.responseCh <- msg:
		return nil
	default:
		return fmt.Errorf("failed to forward response to client")
	}
}

// SendResponseMessage is for compatibility with the Proxy interface.
func (p *HTTPProxy) SendResponseMessage(msg jsonrpc2.Message) error {
	return p.ForwardResponseToClients(context.Background(), msg)
}

// ------------------------- HTTP handlers -------------------------

// handleStreamableRequest handles HTTP POST requests to /mcp.
func (p *HTTPProxy) handleStreamableRequest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p.handleGet(w, r)
	case http.MethodDelete:
		p.handleDelete(w, r)
	case http.MethodPost:
		p.handlePost(w, r)
	default:
		writeHTTPError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleGet serves a standalone GET SSE stream for unsolicited server->client
// notifications (progress, resources/updated, */list_changed, logging/message).
// It is enabled by default; WithStandaloneSSE(false) restores the prior 405
// behavior. The caller MUST provide a known Mcp-Session-Id header: an empty
// header is rejected with 400, and an unknown one with 404 (mirroring
// handleDelete). A session's stream is one-per-session: a second GET for the
// same session evicts the first (see serverStreamRegistry.register). Per the
// locked product decision, a notification dispatched while no GET stream is
// connected for a session is dropped -- there is no replay or pending queue
// for this stream.
func (p *HTTPProxy) handleGet(w http.ResponseWriter, r *http.Request) {
	if !p.standaloneSSE {
		// SSE not offered here; explicit 405 is spec-compliant
		writeHTTPError(w, http.StatusMethodNotAllowed, "SSE not supported on this endpoint")
		return
	}

	flusher, ok := assertFlushable(w)
	if !ok {
		return
	}

	sessID := r.Header.Get("Mcp-Session-Id")
	if sessID == "" {
		writeHTTPError(w, http.StatusBadRequest, "Mcp-Session-Id header required for standalone SSE")
		return
	}
	if _, ok := p.sessionManager.Get(sessID); !ok {
		session.WriteNotFound(w, nil)
		return
	}

	setSSEHeaders(w)

	s := p.serverStreams.register(sessID)
	defer p.serverStreams.deregister(sessID, s)

	// Send headers immediately so the client knows the stream is open.
	flusher.Flush()

	keepAliveTicker := time.NewTicker(sseKeepAliveInterval)
	defer keepAliveTicker.Stop()

	for {
		select {
		case msg := <-s.data:
			// No ok-check: data is never closed (see serverStream's doc comment).
			data, err := jsonrpc2.EncodeMessage(msg)
			if err != nil {
				slog.Error("failed to encode server->client notification", "error", err)
				continue
			}
			if err := writeSSEData(w, flusher, data); err != nil {
				slog.Debug("failed to write notification to GET SSE stream", "error", err)
				return
			}
		case <-keepAliveTicker.C:
			if err := writeSSEKeepAlive(w, flusher); err != nil {
				slog.Debug("failed to write keep-alive", "error", err)
				return
			}
		case <-s.stop:
			// Evicted by a second GET for this session, or proxy Stop's closeAll.
			return
		case <-r.Context().Done():
			return
		case <-p.shutdownCh:
			return
		}
	}
}

func (p *HTTPProxy) handleDelete(w http.ResponseWriter, r *http.Request) {
	sessID := r.Header.Get("Mcp-Session-Id")
	if sessID == "" {
		writeHTTPError(w, http.StatusBadRequest, "Mcp-Session-Id header required for DELETE")
		return
	}
	if _, ok := p.sessionManager.Get(sessID); !ok {
		session.WriteNotFound(w, nil)
		return
	}
	if err := p.sessionManager.Delete(sessID); err != nil {
		//nolint:gosec // G706: session ID is from validated request header
		slog.Debug("failed to delete session", "session_id", sessID, "error", err)
	}

	// Purge routing state (subscriptions, log level) for the deleted session so
	// it cannot outlive the session itself. maxChanged is intentionally not
	// reconciled upstream here: if the deleted session held the max-verbosity
	// log level, the shared backend simply stays MORE verbose than strictly
	// necessary for the remaining sessions until one of them changes the level
	// again -- extra log volume, not a correctness or security issue, so it is
	// not worth an extra upstream round-trip on every DELETE.
	p.routing.purgeSession(sessID)

	// Tear down the session's standalone GET SSE stream (if any), so its
	// handler goroutine (and the list_changed/subscription broadcasts it would
	// otherwise keep receiving) stops immediately instead of surviving until
	// the client eventually closes the underlying socket on its own (see FIX 2
	// in #5744's review). Ordering matches the durable-before-in-memory style
	// rule: the durable session delete and routing purge above have already
	// happened, so nothing durable or in routing state can reference this
	// session by the time its stream is closed.
	p.serverStreams.closeStream(sessID)

	w.WriteHeader(http.StatusNoContent)
}

func (p *HTTPProxy) handlePost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// MCP-Protocol-Version validation is opt-in via strictProtocolValidation
	// (WithStrictProtocolValidation). Default (false) is version-agnostic:
	// any header value is accepted, since this proxy is transport-level and
	// does not depend on a specific MCP revision. When strict mode is
	// enabled, a present-but-unrecognized version is rejected with 400; an
	// absent header is always accepted in either mode, per the streamable
	// HTTP spec's rule to assume 2025-03-26 when the header is missing.
	protoVer := r.Header.Get("MCP-Protocol-Version")
	if p.strictProtocolValidation && protoVer != "" && !isSupportedMCPVersion(protoVer) {
		writeHTTPError(w, http.StatusBadRequest, "Unsupported MCP-Protocol-Version")
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		// A body that exceeds the configured limit without a Content-Length
		// (e.g. chunked) trips http.MaxBytesReader here rather than at the
		// early Content-Length check. Surface it as 413, not 500.
		if bodylimit.IsRequestTooLarge(err) {
			writeHTTPError(w, http.StatusRequestEntityTooLarge, "Request Entity Too Large")
			return
		}
		writeHTTPError(w, http.StatusInternalServerError, fmt.Sprintf("Error reading request body: %v", err))
		return
	}

	// Reject JSON-RPC batches outright. Batching was removed in MCP revision
	// 2025-06-18 and ToolHive serves only 2025-11-25 and 2026-07-28. Rejecting
	// at the executor (not only in ParsingMiddleware) makes this independent of
	// middleware presence, ordering, and Content-Type, so a batch can never
	// reach the backend uninspected by authz/audit/tool-filtering (see #5745).
	if mcp.IsBatchRequest(body) {
		mcp.WriteBatchUnsupportedError(w)
		return
	}

	msg, ok := decodeJSONRPCMessage(w, body)
	if !ok {
		return
	}

	// Notifications or client responses are accepted and forwarded (202)
	if p.handleNotificationOrClientResponse(w, r.Header.Get("Mcp-Session-Id"), msg) {
		return
	}

	req, ok := msg.(*jsonrpc2.Request)
	if !ok || !req.ID.IsValid() {
		writeHTTPError(w, http.StatusBadRequest, "Invalid JSON-RPC request (missing id)")
		return
	}

	// Resolve session per spec (initialize vs ordinary)
	sessID, setSessionHeader, err := p.resolveSessionForRequest(w, r, req)
	if err != nil {
		return
	}

	// Intercept resources/subscribe, resources/unsubscribe, and
	// logging/setLevel for session-bearing (Legacy) requests, AFTER
	// applyMiddlewares/authz has admitted req (Start wraps handleStreamableRequest
	// in the middleware chain, and handlePost is only ever reached through it),
	// so an authz-denied request never gets recorded in routing.
	//
	// For resources/subscribe|unsubscribe, uriLocks serializes the ref-count
	// decision made inside interceptSessionScopedRequest WITH the upstream
	// forward it may perform itself (see interceptSubscribe/
	// interceptUnsubscribe), per uri, so a concurrent last-unsubscribe and
	// first-subscribe for the SAME uri from different sessions can never reach
	// the backend out of order (see uriLocks's doc comment and FIX 1 in
	// #5744's review). The lock is scoped to ONLY this decision+forward+record
	// -- acquired immediately before interceptSessionScopedRequest and released
	// immediately after it returns, BEFORE writeInterceptedResponse or any
	// client-facing streaming -- so a slow or SSE-streamed response never
	// head-of-line-blocks a different session's request for the SAME uri (see
	// FIX 3 in #5744's review). A different uri, or any non-subscription
	// request, is never blocked by this at all: see resourceSubscriptionURI.
	interceptedMsg, handled := func() (jsonrpc2.Message, bool) {
		uri, ok := resourceSubscriptionURI(req)
		if !ok {
			return p.interceptSessionScopedRequest(ctx, sessID, req)
		}
		unlock := p.uriLocks.lock(uri)
		defer unlock()
		return p.interceptSessionScopedRequest(ctx, sessID, req)
	}()
	if handled {
		writeInterceptedResponse(w, r, interceptedMsg, sessID, setSessionHeader)
		return
	}

	// If client accepts SSE, stream the response on an SSE stream for this request
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		p.handleSingleRequestSSE(ctx, w, sessID, req, setSessionHeader)
		return
	}

	// Request/response path with correlation (JSON response)
	p.handleSingleRequest(ctx, w, sessID, req, setSessionHeader)
}

// handleSingleRequest handles a single JSON-RPC request message end-to-end.
//
// If req carries a params._meta.progressToken, it is forwarded upstream
// UNREWRITTEN: a plain JSON POST response has no stream to carry interim
// progress on, so there is nothing to correlate a progress route to, and no
// progress routing is set up (see handleSingleRequestSSE for the SSE path,
// which does). Any notifications/progress the backend later sends for this
// request's original token will simply find no registered route in
// dispatcher.go's routeProgress and be dropped, which is correct: this
// non-streaming client could never have received them anyway.
func (p *HTTPProxy) handleSingleRequest(
	ctx context.Context,
	w http.ResponseWriter,
	sessID string,
	req *jsonrpc2.Request,
	setSessionHeader bool,
) {
	ctx, cancel := context.WithTimeout(ctx, p.requestTimeout)
	defer cancel()

	msg, err := p.doRequest(ctx, sessID, req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			//nolint:gosec // G706: method is from parsed JSON-RPC request
			slog.Warn("timeout waiting for response", "method", req.Method)
			writeHTTPError(w, http.StatusGatewayTimeout, "Timeout waiting for response from container")
		} else {
			//nolint:gosec // G706: method is from parsed JSON-RPC request
			slog.Error("failed to process request", "method", req.Method, "error", err)
			writeHTTPError(w, http.StatusInternalServerError, "Failed to process request")
		}
		return
	}

	if setSessionHeader {
		w.Header().Set("Mcp-Session-Id", sessID)
	}
	if err := writeJSONRPC(w, msg); err != nil {
		slog.Error("failed to write JSON-RPC response", "error", err)
	}
}

// handleSingleRequestSSE handles a single JSON-RPC request whose client asked
// for an SSE response (Accept: text/event-stream). Unlike handleSingleRequest,
// this stream stays open for the lifetime of the request, so it is the
// correct destination for any interim notifications/progress the backend
// sends while it works -- per MCP, request-related messages belong on the
// originating request's stream, not the standalone GET stream (see
// dispatcher.go's routeProgress and docs/arch/03-transport-architecture.md).
//
// If req carries a params._meta.progressToken, setupProgressRouting mints a
// proxy-only token, rewrites the outgoing request to carry it, and registers a
// delivery route for it; the select loop below then interleaves any progress
// notifications delivered on that route with the final response, writing each
// as its own SSE data: frame, and only returns once the final response (or a
// context/shutdown signal) arrives.
func (p *HTTPProxy) handleSingleRequestSSE(
	ctx context.Context,
	w http.ResponseWriter,
	sessID string,
	req *jsonrpc2.Request,
	setSessionHeader bool,
) {
	ctx, cancel := context.WithTimeout(ctx, p.requestTimeout)
	defer cancel()

	// Prepare SSE response headers
	var setMcpSessionID func()
	if setSessionHeader {
		setMcpSessionID = func() { w.Header().Set("Mcp-Session-Id", sessID) }
	}
	flusher, ok := startSSEStream(w, setMcpSessionID)
	if !ok {
		return
	}

	outgoingReq, deliverCh, dropProgressRoute := p.setupProgressRouting(req)
	defer dropProgressRoute()

	waitCh, ck, cleanup, err := p.sendCorrelatedRequest(sessID, outgoingReq)
	if err != nil {
		//nolint:gosec // G706: method is from parsed JSON-RPC request
		slog.Error("failed to send request upstream", "method", req.Method, "error", err)
		writeSSEErrorEvent(w, flusher, req.ID, err)
		return
	}
	defer cleanup()

	for {
		select {
		case progressMsg := <-deliverCh:
			// deliverCh is nil when req carried no progressToken (see
			// setupProgressRouting); a nil channel's case is never selected,
			// so this arm only fires when progress routing is active.
			data, err := jsonrpc2.EncodeMessage(progressMsg)
			if err != nil {
				slog.Error("failed to encode progress notification", "error", err)
				continue
			}
			if err := writeSSEData(w, flusher, data); err != nil {
				slog.Debug("failed to write progress notification to SSE stream", "error", err)
				return
			}
			// Progress does not end the request; keep waiting for more
			// progress or the final response.
		case msg := <-waitCh:
			p.writeSingleRequestSSEFinalResponse(w, flusher, msg, ck)
			return
		case <-ctx.Done():
			writeSSEErrorEvent(w, flusher, req.ID, ctx.Err())
			return
		case <-p.shutdownCh:
			// Matches the prior (pre-progress) behavior: proxy shutdown while a
			// request is in flight is reported to the client as a best-effort
			// Timeout error event, the same as context.Canceled.
			writeSSEErrorEvent(w, flusher, req.ID, context.Canceled)
			return
		}
	}
}

// writeSingleRequestSSEFinalResponse restores msg's original client ID (if it
// is a correlated *jsonrpc2.Response) and writes it as the final SSE data:
// frame for handleSingleRequestSSE's request. Errors encoding/restoring are
// logged; the client simply does not get a final frame in that case, matching
// the prior (pre-progress) behavior's error handling.
func (p *HTTPProxy) writeSingleRequestSSEFinalResponse(
	w http.ResponseWriter, flusher http.Flusher, msg jsonrpc2.Message, ck string,
) {
	finalMsg := msg
	if r, ok := msg.(*jsonrpc2.Response); ok && r.ID.IsValid() {
		restored, err := p.restoreResponseID(r, ck)
		if err != nil {
			slog.Error("failed to restore response id", "error", err)
			return
		}
		finalMsg = restored
	}

	data, err := jsonrpc2.EncodeMessage(finalMsg)
	if err != nil {
		slog.Error("failed to encode JSON-RPC response", "error", err)
		return
	}
	if err := writeSSEData(w, flusher, data); err != nil {
		slog.Debug("failed to write response", "error", err)
	}
}

// writeSSEErrorEvent writes a best-effort JSON-RPC error as a single SSE
// data: frame, for a request whose response headers (200 + text/event-stream)
// have already been sent -- an HTTP error status can no longer be set at this
// point, so the error must be communicated in-band as the response payload.
func writeSSEErrorEvent(w http.ResponseWriter, flusher http.Flusher, id jsonrpc2.ID, err error) {
	errMsg := "Internal error"
	code := -32603
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		errMsg = "Timeout"
		code = -32000
	}
	errObj := map[string]any{
		"jsonrpc": "2.0",
		"id":      id.Raw(),
		"error": map[string]any{
			"code":    code,
			"message": errMsg,
		},
	}
	data, mErr := json.Marshal(errObj)
	if mErr != nil {
		slog.Error("failed to encode SSE error event", "error", mErr)
		return
	}
	if err := writeSSEData(w, flusher, data); err != nil {
		slog.Debug("failed to write error message", "error", err)
	}
}

func encodeRequestWithID(req *jsonrpc2.Request, newID string) (jsonrpc2.Message, error) {
	data, err := jsonrpc2.EncodeMessage(req)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m["id"] = newID
	data2, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return jsonrpc2.DecodeMessage(data2)
}

// progressDeliverBufferSize bounds how many not-yet-written progress
// notifications may queue for a single in-flight POST-SSE request before
// further ones are dropped (see routeProgress's non-blocking send).
const progressDeliverBufferSize = 16

// rewriteMetaProgressToken returns a request identical to req except that, if
// params._meta.progressToken is present, it is replaced with ptGlobal. This
// lets dispatcher.go's routeProgress correlate the backend's later
// notifications/progress replies (which echo the token verbatim) back to the
// specific POST-SSE request that asked for them, without ever exposing
// ptGlobal -- or any other session's progress -- to more than the one client
// that requested it.
//
// It returns hadToken=false (with rewritten == req, unchanged) if req carried
// no progressToken, telling the caller not to set up progress routing at all.
// originalToken is the client's own progressToken value (string or number),
// to be restored before delivering progress back to the client.
//
// Per the "copy before mutating caller input" style rule, req's params map is
// decoded into fresh copies before any mutation, so req itself (and any
// caller-held reference to it) is never modified.
func rewriteMetaProgressToken(
	req *jsonrpc2.Request, ptGlobal string,
) (originalToken any, hadToken bool, rewritten *jsonrpc2.Request, err error) {
	if len(req.Params) == 0 {
		return nil, false, req, nil
	}
	var paramsMap map[string]any
	if err := json.Unmarshal(req.Params, &paramsMap); err != nil {
		return nil, false, nil, err
	}
	meta, ok := paramsMap["_meta"].(map[string]any)
	if !ok {
		return nil, false, req, nil
	}
	original, ok := meta["progressToken"]
	if !ok {
		return nil, false, req, nil
	}

	metaCopy := maps.Clone(meta)
	metaCopy["progressToken"] = ptGlobal
	paramsCopy := maps.Clone(paramsMap)
	paramsCopy["_meta"] = metaCopy

	data, err := json.Marshal(paramsCopy)
	if err != nil {
		return nil, false, nil, err
	}
	return original, true, &jsonrpc2.Request{ID: req.ID, Method: req.Method, Params: data}, nil
}

// setupProgressRouting mints a fresh proxy-only progress-routing token and
// rewrites req's params._meta.progressToken to it (see
// rewriteMetaProgressToken), registering a progressRoute so the backend's
// later notifications/progress messages that echo the token are delivered on
// the returned channel rather than leaking to the standalone GET stream or
// another session.
//
// If req carries no progressToken (or minting/rewriting fails, logged as a
// warning), the returned request is req itself, unmodified, and the returned
// channel is nil: a select on a nil channel never fires, so callers can select
// on it unconditionally without special-casing "no progress requested".
//
// The returned cleanup func drops the progress token and MUST be deferred by
// the caller; it is always safe to call, including when no route was
// registered.
func (p *HTTPProxy) setupProgressRouting(req *jsonrpc2.Request) (*jsonrpc2.Request, <-chan jsonrpc2.Message, func()) {
	noop := func() {}

	ptGlobal, err := uuid.NewRandom()
	if err != nil {
		slog.Warn("failed to mint progress routing token; interim progress will not be delivered for this request",
			"error", err)
		return req, nil, noop
	}

	originalToken, hadToken, rewritten, err := rewriteMetaProgressToken(req, ptGlobal.String())
	if err != nil {
		slog.Warn("failed to rewrite progress token; interim progress will not be delivered for this request",
			"error", err)
		return req, nil, noop
	}
	if !hadToken {
		return req, nil, noop
	}

	key := ptGlobal.String()
	deliver := make(chan jsonrpc2.Message, progressDeliverBufferSize)
	p.routing.recordProgressToken(key, progressRoute{deliver: deliver, originalToken: originalToken})
	return rewritten, deliver, func() { p.routing.dropProgressToken(key) }
}

func (p *HTTPProxy) restoreResponseID(resp *jsonrpc2.Response, ck string) (jsonrpc2.Message, error) {
	orig, ok := p.idRestore.Load(ck)
	if !ok {
		// No restore information; return as-is
		return resp, nil
	}
	origID, _ := orig.(jsonrpc2.ID)

	data, err := jsonrpc2.EncodeMessage(resp)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m["id"] = origID.Raw()
	data2, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return jsonrpc2.DecodeMessage(data2)
}

// sendCorrelatedRequest mints the composite-key waiter for req, rewrites its
// wire ID to that composite key, and sends it upstream. It returns the waiter
// channel and ck (needed to restore the original ID on the eventual response,
// see restoreResponseID), plus a cleanup func the caller MUST call (typically
// via defer) to release the waiter/idRestore entries once done waiting.
//
// This is split out from doRequest so callers that need to interleave OTHER
// events (e.g. handleSingleRequestSSE's progress notifications) with the wait
// for the final response can select over both, instead of being limited to
// doRequest's single blocking wait.
func (p *HTTPProxy) sendCorrelatedRequest(
	sessID string, req *jsonrpc2.Request,
) (waitCh chan jsonrpc2.Message, ck string, cleanup func(), err error) {
	key := idKeyFromID(req.ID)
	ck = compositeKey(sessID, key)

	waitCh, cleanup = p.createWaiter(sessID, req.ID)

	proxiedMsg, err := encodeRequestWithID(req, ck)
	if err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("encode request: %w", err)
	}
	if err := p.SendMessageToDestination(proxiedMsg); err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("send message: %w", err)
	}
	return waitCh, ck, cleanup, nil
}

func (p *HTTPProxy) doRequest(ctx context.Context, sessID string, req *jsonrpc2.Request) (jsonrpc2.Message, error) {
	waitCh, ck, cleanup, err := p.sendCorrelatedRequest(sessID, req)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	select {
	case msg := <-waitCh:
		if r, ok := msg.(*jsonrpc2.Response); ok && r.ID.IsValid() {
			restored, err := p.restoreResponseID(r, ck)
			if err != nil {
				return nil, fmt.Errorf("restore id: %w", err)
			}
			return restored, nil
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.shutdownCh:
		return nil, context.Canceled
	}
}

// ------------------------- Helpers: middleware, parsing, correlation -------------------------

func (p *HTTPProxy) applyMiddlewares(handler http.Handler) http.Handler {
	for i := len(p.middlewares) - 1; i >= 0; i-- {
		handler = p.middlewares[i].Function(handler)
	}
	return handler
}

func (p *HTTPProxy) ensureSession(id string) error {
	if _, ok := p.sessionManager.Get(id); ok {
		return nil
	}
	return p.sessionManager.AddWithID(id)
}

// resolveSessionForRequest resolves session rules for a single JSON-RPC request.
//
// It first classifies the request as Modern or Legacy MCP (mcp.ClassifyRevision).
// A classification error is rejected outright with an HTTP 400 JSON-RPC error
// response. A Modern request is always sessionless: it gets a fresh per-request
// routing token and never touches sessionManager, regardless of any
// Mcp-Session-Id header it carries (see the confidentiality note below). A
// Legacy request falls through to the existing session rules: on initialize,
// assigns a new session ID if none is provided and returns setSessionHeader=true;
// a provided but unknown session ID returns 404.
//
// Sessionless requests (both the Modern branch and Legacy's sessionless
// non-initialize branch) receive a per-request UUID used solely as an
// in-process routing token (not registered with sessionManager). Sessionless
// routing tokens MUST be unique per request: sharing one (e.g. the empty string,
// or a stale/foreign Mcp-Session-Id) across concurrent sessionless requests with
// the same JSON-RPC id collapses them onto the same compositeKey(sessID, idKey)
// and overwrites entries in the waiters / idRestore sync.Maps, leaking one
// client's response payload to another. This is a confidentiality bug, not a
// performance issue -- do not collapse the token, and do not let a Modern
// request fall through to the session-lookup path below.
//
// Writes HTTP errors on failure and returns error to stop handling.
func (p *HTTPProxy) resolveSessionForRequest(
	w http.ResponseWriter,
	r *http.Request,
	req *jsonrpc2.Request,
) (string, bool, error) {
	// Classification/routing here applies only to the single id-bearing request
	// path; batches are rejected earlier in handlePost, and the
	// notification/client-response path is Legacy-only by construction.
	meta := mcp.ExtractMeta(req.Params)
	protoHeader := r.Header.Get("MCP-Protocol-Version")
	rev, err := mcp.ClassifyRevision(req.Method, meta, protoHeader)
	if err != nil {
		mcp.WriteClassificationError(w, req.ID.Raw(), err)
		return "", false, err
	}

	if rev == mcp.RevisionModern {
		// Modern is stateless: mint a fresh routing token unconditionally and
		// ignore any client-supplied Mcp-Session-Id. Never fall through to the
		// session-lookup path below with it -- see the confidentiality note
		// in this function's doc comment.
		//
		// This is safe only because, in this proxy, Mcp-Session-Id has no
		// downstream authority: it is purely an in-process response-correlation
		// token (the backend is stateless stdio, unlike the transparent proxy,
		// which maps it to a backend-assigned SID via session metadata). If this
		// proxy ever gains a shared session store or a stateful backend mapping,
		// "ignore the client SID on Modern" must stay true -- silently reusing or
		// looking up a known client SID here would reopen a session-validation
		// bypass. See TestModernNeverReusesClientSessionIDAsRoutingToken.
		token, err := uuid.NewRandom()
		if err != nil {
			writeHTTPError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to generate routing token: %v", err))
			return "", false, fmt.Errorf("generate routing token: %w", err)
		}
		return token.String(), false, nil
	}

	var setSessionHeader bool
	sessID := r.Header.Get("Mcp-Session-Id")

	if req.Method == "initialize" {
		if sessID == "" {
			newID, err := uuid.NewRandom()
			if err != nil {
				writeHTTPError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to generate session ID: %v", err))
				return "", false, fmt.Errorf("generate session ID: %w", err)
			}
			sessID = newID.String()
			setSessionHeader = true
		}
		if err := p.ensureSession(sessID); err != nil {
			writeHTTPError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to create session: %v", err))
			return "", false, err
		}
		return sessID, setSessionHeader, nil
	}

	// Sessionless non-initialize: generate a per-request routing token.
	// setSessionHeader stays false so the client never sees this UUID and the
	// next request remains sessionless.
	if sessID == "" {
		token, err := uuid.NewRandom()
		if err != nil {
			writeHTTPError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to generate routing token: %v", err))
			return "", false, fmt.Errorf("generate routing token: %w", err)
		}
		return token.String(), false, nil
	}

	// Session ID provided but not found: reject with 404.
	if _, ok := p.sessionManager.Get(sessID); !ok {
		session.WriteNotFound(w, req.ID.Raw())
		return "", false, fmt.Errorf("session not found")
	}
	return sessID, false, nil
}

// resourceSubscriptionURI returns req's uri param and true if req is a
// resources/subscribe or resources/unsubscribe request carrying a non-empty
// uri -- the only requests whose ref-count decision and upstream forward (see
// interceptSubscribe/interceptUnsubscribe, called from
// interceptSessionScopedRequest) handlePost must serialize per-uri via
// uriLocks. Any other request -- including logging/setLevel (whose
// reconciliation is best-effort-ordered, see reconcileUpstreamLogLevel) and a
// subscribe/unsubscribe with no/empty uri (which interceptSessionScopedRequest
// itself no-ops on) -- returns ("", false), so handlePost skips locking
// entirely for it.
func resourceSubscriptionURI(req *jsonrpc2.Request) (string, bool) {
	switch req.Method {
	case methodResourcesSubscribe, methodResourcesUnsubscribe:
		uri, ok := extractStringParam(req.Params, "uri")
		if !ok || uri == "" {
			return "", false
		}
		return uri, true
	default:
		return "", false
	}
}

// interceptSessionScopedRequest applies ref-counted resources/subscribe and
// resources/unsubscribe forwarding, and logging/setLevel max-verbosity
// reconciliation, for a request that belongs to a real, persisted Legacy
// session. It MUST be called only after applyMiddlewares/authz has admitted
// req (handlePost is only ever reached through Start's applyMiddlewares-
// wrapped mux entry), so an authz-denied subscribe is never recorded in
// routing -- see the "authz non-bypass" security test.
//
// For resources/subscribe|unsubscribe, the caller (handlePost) holds uriLocks
// for req's uri around this call, scoped to ONLY this call (see FIX 3 in
// #5744's review): interceptSubscribe/interceptUnsubscribe perform their own
// upstream round-trip (via p.doRequest, bounded by p.requestTimeout so the
// lock is never held indefinitely) synchronously, INSIDE this call, rather
// than reporting "forward it yourself" back to handlePost -- this is what
// lets the ref-count decision, the forward, and recording the subscription be
// atomic with respect to any other session's concurrent (un)subscribe for the
// SAME uri (see resourceSubscriptionURI and FIX 1 in #5744's review).
//
// sessID identifies "a real session" (as opposed to a per-request routing
// token minted for a Modern or Legacy-sessionless request, see
// resolveSessionForRequest) by whether sessionManager currently has it: only
// real sessions are ever stored there, so this single check both selects the
// right requests AND naturally excludes ones with nothing durable to track.
//
// It returns (msg, true) when the caller should write msg to the client and
// nothing further needs to happen: msg may be a synthesized success (a
// non-first subscribe, a non-last unsubscribe, or logging/setLevel), or the
// real upstream response (success OR error) for a first subscribe/last
// unsubscribe that this call forwarded itself. It returns (nil, false) only
// when the method isn't one of the three above, or the request isn't
// session-bearing -- in which case the caller must proceed with the normal
// upstream request/response flow.
func (p *HTTPProxy) interceptSessionScopedRequest(
	ctx context.Context, sessID string, req *jsonrpc2.Request,
) (jsonrpc2.Message, bool) {
	if _, isSession := p.sessionManager.Get(sessID); !isSession {
		return nil, false
	}

	switch req.Method {
	case methodResourcesSubscribe:
		uri, ok := extractStringParam(req.Params, "uri")
		if !ok || uri == "" {
			return nil, false
		}
		return p.interceptSubscribe(ctx, sessID, uri, req), true

	case methodResourcesUnsubscribe:
		uri, ok := extractStringParam(req.Params, "uri")
		if !ok || uri == "" {
			return nil, false
		}
		return p.interceptUnsubscribe(ctx, sessID, uri, req), true

	case string(sdkmcp.MethodSetLogLevel):
		// Unlike resources/subscribe|unsubscribe, this decision is NOT ordered
		// with its upstream forward via uriLocks (there is no per-uri key to
		// order on, and no uriLocks acquisition happens for this method -- see
		// resourceSubscriptionURI). Concurrent maxChanged transitions from
		// different sessions may therefore reach the backend out of order; see
		// reconcileUpstreamLogLevel's doc comment for why that is an accepted,
		// harmless tradeoff for now. The client's own synthesized success below
		// is likewise a fire-and-forget, optimistic ack -- like subscribe's
		// dedup path, it is served without waiting for reconcileUpstreamLogLevel
		// to complete, which is intentional and low-stakes: notifications/message
		// is unconditionally dropped (see routeNotification's SECURE-DROP), so
		// there is no client-visible effect of the level change to get wrong.
		level, _ := extractStringParam(req.Params, "level")
		if maxChanged, newMax := p.routing.setLogLevel(sessID, logLevelRank(level)); maxChanged {
			p.reconcileUpstreamLogLevel(logLevelName(newMax))
		}
		return synthesizeSuccessResponse(req.ID), true

	default:
		return nil, false
	}
}

// interceptSubscribe handles a session-bearing resources/subscribe request
// for uri, called from interceptSessionScopedRequest while the caller
// (handlePost) holds uriLocks for uri. If uri already has at least one
// recorded subscriber, the backend has already admitted the FIRST subscribe
// and is sending updates for uri, so sess is simply recorded as an
// additional subscriber and a synthesized success is returned WITHOUT another
// upstream call -- dedup is safe here because the thing being deduped against
// is a call that is known to have succeeded.
//
// Otherwise sess is (candidate) first subscriber: the request is forwarded
// upstream via forwardUpstream, and sess is recorded as a subscriber ONLY if
// that forward succeeds with a non-error response. If the backend errors,
// times out, or the transport call itself fails, sess is NOT recorded and the
// real failure is returned to the client -- recording on failure would leave
// a phantom subscription entry that permanently starves every later
// session's subscribe for the SAME uri (deduping it against an upstream
// subscribe the backend never actually granted), and would violate the
// "write durable(upstream) storage before updating in-memory state" style
// rule. See FIX 1 in #5744's review.
func (p *HTTPProxy) interceptSubscribe(ctx context.Context, sessID, uri string, req *jsonrpc2.Request) jsonrpc2.Message {
	if len(p.routing.subscribersOf(uri)) > 0 {
		p.routing.addSubscription(uri, sessID)
		return synthesizeSuccessResponse(req.ID)
	}

	resp, err := p.forwardUpstream(ctx, sessID, req)
	if err != nil {
		return upstreamErrorResponse(req.ID, err)
	}
	if isErrorResponse(resp) {
		// The backend rejected the first subscribe (e.g. resource not found):
		// do not record. A later session's subscribe for the SAME uri must be
		// free to try again upstream, not be dedup-served a fake success.
		return resp
	}
	p.routing.addSubscription(uri, sessID)
	return resp
}

// interceptUnsubscribe handles a session-bearing resources/unsubscribe
// request for uri, called from interceptSessionScopedRequest while the
// caller (handlePost) holds uriLocks for uri. sess is removed from uri's
// recorded subscribers immediately, regardless of what happens next; the
// request is forwarded upstream only if sess was the LAST recorded
// subscriber (the backend should keep sending updates for uri as long as
// anyone still wants them).
//
// Unlike subscribe, a failed/erroring upstream unsubscribe is not rolled
// back: removeSubscription has already run, and there is no OTHER session to
// re-attribute the subscription to even if we wanted to undo it. The worst
// case is the backend keeps a subscription nobody is listening for anymore --
// wasted upstream notifications that are silently dropped by
// routeResourceUpdated finding no subscribers, not a correctness or security
// issue -- so it is not worth the complexity of reversing removeSubscription
// here.
func (p *HTTPProxy) interceptUnsubscribe(ctx context.Context, sessID, uri string, req *jsonrpc2.Request) jsonrpc2.Message {
	last := p.routing.removeSubscription(uri, sessID)
	if !last {
		return synthesizeSuccessResponse(req.ID)
	}

	resp, err := p.forwardUpstream(ctx, sessID, req)
	if err != nil {
		return upstreamErrorResponse(req.ID, err)
	}
	return resp
}

// forwardUpstream wraps ctx with p.requestTimeout and forwards req upstream
// via doRequest, for interceptSubscribe/interceptUnsubscribe's synchronous
// forward performed while still holding handlePost's per-uri uriLocks lock
// for req's uri: the timeout bounds how long that lock can ever be held, so a
// slow or unresponsive backend cannot indefinitely block other sessions'
// resources/subscribe|unsubscribe requests for the SAME uri (see FIX 1 in
// #5744's review).
func (p *HTTPProxy) forwardUpstream(ctx context.Context, sessID string, req *jsonrpc2.Request) (jsonrpc2.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, p.requestTimeout)
	defer cancel()
	return p.doRequest(ctx, sessID, req)
}

// isErrorResponse reports whether msg is a *jsonrpc2.Response carrying a
// JSON-RPC error, used by interceptSubscribe to distinguish a successful
// upstream resources/subscribe from one the backend rejected.
func isErrorResponse(msg jsonrpc2.Message) bool {
	resp, ok := msg.(*jsonrpc2.Response)
	return ok && resp.Error != nil
}

// upstreamErrorResponse builds a JSON-RPC error response for id when the
// upstream round-trip inside interceptSubscribe/interceptUnsubscribe itself
// fails (timeout, proxy shutdown, or a transport-level send error, as opposed
// to a JSON-RPC error the backend returned normally), mirroring the
// timeout-vs-internal error code mapping writeSSEErrorEvent uses for the
// normal request path.
func upstreamErrorResponse(id jsonrpc2.ID, err error) jsonrpc2.Message {
	var code int64 = -32603
	msg := "Internal error"
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		code = -32000
		msg = "Timeout"
	}
	return &jsonrpc2.Response{ID: id, Error: jsonrpc2.NewError(code, msg)}
}

// synthesizeSuccessResponse builds a locally-generated JSON-RPC success
// response for id with an empty result object, used by
// interceptSessionScopedRequest when a request is served without an upstream
// round-trip.
func synthesizeSuccessResponse(id jsonrpc2.ID) jsonrpc2.Message {
	resp, err := jsonrpc2.NewResponse(id, map[string]any{}, nil)
	if err != nil {
		// marshalToRaw(map[string]any{}) cannot fail; NewResponse's only
		// error path is result marshaling.
		slog.Error("failed to synthesize success response", "error", err)
		return &jsonrpc2.Response{ID: id, Error: jsonrpc2.NewError(-32603, "internal error")}
	}
	return resp
}

// writeInterceptedResponse writes msg (from interceptSessionScopedRequest) to
// the client, honoring the same Accept-header-driven JSON-vs-SSE choice and
// Mcp-Session-Id header behavior as the normal handleSingleRequest /
// handleSingleRequestSSE paths, but without any upstream correlation (msg is
// already the complete, final response).
func writeInterceptedResponse(
	w http.ResponseWriter, r *http.Request, msg jsonrpc2.Message, sessID string, setSessionHeader bool,
) {
	if setSessionHeader {
		w.Header().Set("Mcp-Session-Id", sessID)
	}

	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		if err := writeJSONRPC(w, msg); err != nil {
			slog.Error("failed to write JSON-RPC response", "error", err)
		}
		return
	}

	flusher, ok := startSSEStream(w, nil)
	if !ok {
		return
	}

	data, err := jsonrpc2.EncodeMessage(msg)
	if err != nil {
		slog.Error("failed to encode JSON-RPC response", "error", err)
		return
	}
	if err := writeSSEData(w, flusher, data); err != nil {
		slog.Debug("failed to write response", "error", err)
	}
}

// logLevelRanks assigns each MCP logging level a verbosity rank following the
// RFC 5424 syslog severity numbers MCP's logging capability is aligned with:
// higher number = more verbose = lower severity. "debug" (7) is the most
// verbose; "emergency" (0) is the least. This makes "the maximum rank across
// sessions" exactly the level that satisfies every session's request without
// under-serving the most verbose one (see sessionRouter.setLogLevel).
var logLevelRanks = map[string]int{
	string(sdkmcp.LoggingLevelEmergency): 0,
	string(sdkmcp.LoggingLevelAlert):     1,
	string(sdkmcp.LoggingLevelCritical):  2,
	string(sdkmcp.LoggingLevelError):     3,
	string(sdkmcp.LoggingLevelWarning):   4,
	string(sdkmcp.LoggingLevelNotice):    5,
	string(sdkmcp.LoggingLevelInfo):      6,
	string(sdkmcp.LoggingLevelDebug):     7,
}

// logLevelRank returns level's verbosity rank (see logLevelRanks). An unknown
// or empty level is treated as the MOST verbose (debug's rank): if we can't
// recognize what the client asked for, we would rather over-serve (extra log
// volume) than silently under-serve a session that asked for a level we
// failed to parse.
func logLevelRank(level string) int {
	if rank, ok := logLevelRanks[level]; ok {
		return rank
	}
	slog.Debug("unrecognized logging level; treating as most verbose", "level", level)
	return logLevelRanks[string(sdkmcp.LoggingLevelDebug)]
}

// logLevelName returns the level name for rank (the inverse of logLevelRank),
// used to build the reconciled upstream logging/setLevel request.
func logLevelName(rank int) string {
	for name, r := range logLevelRanks {
		if r == rank {
			return name
		}
	}
	return string(sdkmcp.LoggingLevelDebug)
}

// reconcileUpstreamLogLevel sends a logging/setLevel request upstream with
// level, reconciling the shared backend's single process-wide log level to
// the maximum verbosity requested by any session (see
// interceptSessionScopedRequest). It is fire-and-forget from the calling
// client request's perspective: the reconciliation happens in the background
// via the normal composite-key request/response correlation (so the eventual
// response, if any, is routed and cleaned up correctly instead of leaking a
// waiter), but its result is not surfaced to any client -- the client that
// triggered reconciliation already received its own synthesized success (see
// interceptSessionScopedRequest).
//
// Ordering tradeoff: this reconciliation is best-effort-ordered, NOT
// serialized the way resources/subscribe|unsubscribe is via uriLocks (see FIX
// 1 in #5744's review). Each call runs in its own goroutine, so two
// concurrent maxChanged transitions from different sessions' logging/setLevel
// requests can reach the backend in either order, and the backend could
// briefly end up at a lower verbosity than the CURRENT true max until the
// next transition reconciles it again. This is harmless while
// notifications/message is secure-dropped (see routeNotification): the
// backend's log level is not observable by any client either way, only its
// log volume is affected. Proper ordering (e.g. via a keyed lock analogous to
// uriLocks, or a monotonic sequence check) is deferred to the per-session
// backend / log-delivery follow-up (see the vMCP architecture, #5744).
func (p *HTTPProxy) reconcileUpstreamLogLevel(level string) {
	id, err := uuid.NewRandom()
	if err != nil {
		slog.Warn("failed to mint id for upstream log level reconciliation", "error", err)
		return
	}
	req, err := jsonrpc2.NewCall(jsonrpc2.StringID(id.String()), string(sdkmcp.MethodSetLogLevel), map[string]any{"level": level})
	if err != nil {
		slog.Warn("failed to build upstream log level reconciliation request", "error", err)
		return
	}

	go func() {
		// internalReconciliationSessionID keys this request's waiter under a
		// session ID no real request ever uses: resolveSessionForRequest
		// always assigns a non-empty UUID (real session or per-request
		// routing token), so "" can never collide with one.
		const internalReconciliationSessionID = ""
		ctx, cancel := context.WithTimeout(context.Background(), p.requestTimeout)
		defer cancel()
		if _, err := p.doRequest(ctx, internalReconciliationSessionID, req); err != nil {
			slog.Debug("upstream log level reconciliation did not complete", "error", err)
		}
	}()
}

// reapRoutingState periodically purges sessionRouter subscription and
// log-level entries, AND serverStreams' standalone GET streams, for sessions
// that are no longer active, bounding the otherwise-unbounded growth/leakage
// of that state for a session whose owning client disappears without ever
// sending DELETE (see handleDelete's purgeSession/closeStream calls for the
// explicit-teardown path). It ticks at the same cadence as the session
// manager's own cleanup routine (sessionTTL/2, see manager.go's
// cleanupRoutine), and exits when shutdownCh is closed.
//
// isActive is p.sessionManager.Get: the only session liveness check the
// session.Manager/Storage interfaces currently expose. Every Storage
// implementation intentionally refreshes its backend's TTL on every read
// (LocalStorage's Load, RedisStorage's GETEX) to keep genuinely active
// sessions alive -- there is no "peek without refreshing" method today (see
// manager.go and storage.go). Using Get here as isActive means: a session
// that still owns routing state (e.g. an open subscription) has its TTL
// refreshed by this reaper's own liveness check, which can delay that
// session's expiry by up to one reap tick (sessionTTL/2) beyond actual client
// inactivity. This is an accepted, documented tradeoff, not a security gap:
// Get on an ALREADY-deleted session correctly returns false without
// refreshing anything, so reap can never retain routing state past a
// session's true deletion -- it can only delay reaping state for a session
// that reap's own check just observed to still be nominally alive.
func (p *HTTPProxy) reapRoutingState() {
	ticker := time.NewTicker(p.sessionTTL / 2)
	defer ticker.Stop()

	isActive := func(sess string) bool {
		_, ok := p.sessionManager.Get(sess)
		return ok
	}

	for {
		select {
		case <-ticker.C:
			p.routing.reap(isActive)
			p.reapServerStreams(isActive)
		case <-p.shutdownCh:
			return
		}
	}
}

// reapServerStreams closes the standalone GET SSE stream (see
// serverStreamRegistry.closeStream) for any session serverStreams currently
// has registered that isActive no longer reports as live -- a session whose
// owning client disappeared (crashed, or otherwise never sent DELETE) without
// the proxy ever observing r.Context().Done() on its GET request. This mirrors
// handleDelete's explicit p.serverStreams.closeStream(sessID) call for the
// case where the client never disconnects and never calls DELETE either (see
// FIX 2 in #5744's review).
//
// Liveness is resolved for every candidate stream key in a first pass with no
// registry lock held, exactly like sessionRouter.reap's two-phase discipline:
// isActive may perform I/O (e.g. a Redis-backed session.Manager), and running
// it while holding serverStreams.mu would block every concurrent
// broadcast/dispatchTo/deliverToMany/register/deregister call for the
// (possibly slow) duration of every liveness check. closeStream itself only
// ever takes the lock for the in-memory work of removing one already-decided
// stale entry, never across isActive.
func (p *HTTPProxy) reapServerStreams(isActive func(sess string) bool) {
	for _, key := range p.serverStreams.streamKeys() {
		if isActive(key) {
			continue
		}
		if p.serverStreams.closeStream(key) {
			//nolint:gosec // G706: session ID is server-minted, not user-controlled free text
			slog.Debug("reaped standalone GET stream for inactive session", "session_id", key)
		}
	}
}

// decodeJSONRPCMessage decodes a JSON-RPC message from the request body.
func decodeJSONRPCMessage(w http.ResponseWriter, body []byte) (jsonrpc2.Message, bool) {
	msg, err := jsonrpc2.DecodeMessage(body)
	if err != nil {
		//nolint:gosec // G706: logging raw JSON-RPC data from HTTP request body
		slog.Warn("skipping message that failed to decode", "body", string(body))
		writeHTTPError(w, http.StatusBadRequest, "Invalid JSON-RPC 2.0 message")
		return nil, false
	}
	return msg, true
}

// handleNotificationOrClientResponse handles notifications and client responses
// as Legacy today; Modern-aware handling of this path is deferred.
func (p *HTTPProxy) handleNotificationOrClientResponse(w http.ResponseWriter, sessID string, msg jsonrpc2.Message) bool {
	if isNotification(msg) || isClientResponse(msg) {
		// Refresh TTL so a client sending only notifications doesn't get evicted.
		if sessID != "" {
			p.sessionManager.Get(sessID)
		}
		if err := p.SendMessageToDestination(msg); err != nil {
			slog.Error("failed to send message to destination", "error", err)
		}
		w.WriteHeader(http.StatusAccepted)
		return true
	}
	return false
}

// resolveRequestTimeout returns the proxy request timeout, reading from the
// TOOLHIVE_PROXY_REQUEST_TIMEOUT environment variable if set, otherwise
// returning defaultRequestTimeout.
func resolveRequestTimeout() time.Duration {
	v := os.Getenv(proxyRequestTimeoutEnv)
	if v == "" {
		return defaultRequestTimeout
	}
	d, _ := time.ParseDuration(v)
	if d > 0 {
		slog.Debug("using custom proxy request timeout", "timeout", d)
		return d
	}
	slog.Warn("invalid proxy request timeout, using default",
		"env_var", proxyRequestTimeoutEnv, "value", v, "default", defaultRequestTimeout)
	return defaultRequestTimeout
}

// createWaiter registers a waiter channel for the given request ID and returns cleanup fn.
func (p *HTTPProxy) createWaiter(sessID string, id jsonrpc2.ID) (chan jsonrpc2.Message, func()) {
	key := idKeyFromID(id)
	ck := compositeKey(sessID, key)
	// store original client id to restore before replying
	p.idRestore.Store(ck, id)

	ch := make(chan jsonrpc2.Message, 1)
	p.waiters.Store(ck, ch)

	cleanup := func() {
		p.waiters.Delete(ck)
		p.idRestore.Delete(ck)
	}
	return ch, cleanup
}

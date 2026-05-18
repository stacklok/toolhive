// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package httpsse provides an HTTP proxy implementation for Server-Sent Events (SSE)
// used in communication between the client and MCP server.
package httpsse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/healthcheck"
	"github.com/stacklok/toolhive/pkg/transport/proxy/socket"
	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Proxy defines the interface for proxying messages between clients and destinations.
type Proxy interface {
	// Start starts the proxy.
	Start(ctx context.Context) error

	// Stop stops the proxy.
	Stop(ctx context.Context) error

	// GetMessageChannel returns the channel for messages to/from the destination.
	GetMessageChannel() chan jsonrpc2.Message

	// GetResponseChannel returns the channel for receiving messages from the destination.
	GetResponseChannel() <-chan jsonrpc2.Message

	// SendMessageToDestination sends a message to the destination.
	SendMessageToDestination(msg jsonrpc2.Message) error

	// ForwardResponseToClients forwards a response from the destination to clients.
	ForwardResponseToClients(ctx context.Context, msg jsonrpc2.Message) error

	// SendResponseMessage sends a message to the response channel.
	SendResponseMessage(msg jsonrpc2.Message) error
}

// HTTPSSEProxy encapsulates the HTTP proxy functionality for SSE transports.
// It provides SSE endpoints and JSON-RPC message handling.
//
//nolint:revive // Intentionally named HTTPSSEProxy despite package name
type HTTPSSEProxy struct {
	// Basic configuration
	host              string
	port              int
	middlewares       []types.NamedMiddleware
	trustProxyHeaders bool

	// HTTP server
	server     *http.Server
	shutdownCh chan struct{}

	// Optional Prometheus metrics handler
	prometheusHandler http.Handler

	// Session manager for SSE clients
	sessionManager *session.Manager

	// sessionTTL is the resolved inactivity timeout for the session manager.
	// Defaults to session.DefaultSessionTTL; overridable via WithSessionTTL.
	sessionTTL time.Duration

	// sessionStorage is the optional custom storage backend for the session manager.
	// When nil, in-memory LocalStorage is used. Set via WithSessionStorage.
	sessionStorage session.Storage

	// liveSSESessions tracks active SSE connections local to this instance.
	// Keys are clientID strings; values are *session.SSESession.
	// This is separate from sessionManager so that distributed storage backends
	// (e.g. Redis) can be used for session metadata without breaking SSE fan-out,
	// which must iterate live in-memory connections regardless of storage backend.
	liveSSESessions sync.Map

	// Pending messages for SSE clients
	pendingMessages []*ssecommon.PendingSSEMessage
	pendingMutex    sync.Mutex

	// Message channel
	messageCh chan jsonrpc2.Message

	// Health checker
	healthChecker *healthcheck.HealthChecker

	// stopOnce ensures Stop is idempotent even when called concurrently.
	stopOnce sync.Once
}

// Option configures an HTTPSSEProxy.
type Option func(*HTTPSSEProxy)

// WithSessionStorage injects a custom storage backend into the session manager.
// When not provided, the proxy uses in-memory LocalStorage (single-replica default).
// Provide a Redis-backed storage for multi-replica deployments so all replicas
// share the same session store.
//
// Architectural note: HTTPSSEProxy is used by StdioTransport for stdio-backed MCP
// servers. SSE fan-out (ForwardResponseToClients) and POST handling are both local
// to the instance holding the live SSE connection, so Redis storage enables
// cross-replica session metadata sharing but does NOT solve cross-replica message
// delivery — a POST accepted on replica B won't reach a client whose SSE connection
// is on replica A. Callers must ensure an external load balancer provides session
// affinity (sticky sessions) when using distributed storage with this proxy.
//
// Prefer Streamable HTTP (ProxyModeStreamableHTTP), also supported on StdioTransport,
// which does not have this affinity constraint.
//
// Note: SSE fan-out and graceful disconnect use a separate in-memory liveSSESessions
// registry, not the session manager, so any Storage implementation is safe to inject here.
func WithSessionStorage(storage session.Storage) Option {
	return func(p *HTTPSSEProxy) {
		if storage == nil {
			return
		}
		p.sessionStorage = storage
	}
}

// WithSessionTTL overrides the session inactivity timeout used by this proxy.
// Zero or negative values are ignored so the constructor's default is preserved.
func WithSessionTTL(ttl time.Duration) Option {
	return func(p *HTTPSSEProxy) {
		if ttl <= 0 {
			return
		}
		p.sessionTTL = ttl
	}
}

// NewHTTPSSEProxy creates a new HTTP SSE proxy for transports.
func NewHTTPSSEProxy(
	host string,
	port int,
	trustProxyHeaders bool,
	prometheusHandler http.Handler,
	middlewares []types.NamedMiddleware,
	opts ...Option,
) *HTTPSSEProxy {
	proxy := &HTTPSSEProxy{
		middlewares:       middlewares,
		host:              host,
		port:              port,
		trustProxyHeaders: trustProxyHeaders,
		shutdownCh:        make(chan struct{}),
		messageCh:         make(chan jsonrpc2.Message, 100),
		sessionTTL:        session.DefaultSessionTTL,
		pendingMessages:   []*ssecommon.PendingSSEMessage{},
		prometheusHandler: prometheusHandler,
	}

	for _, opt := range opts {
		opt(proxy)
	}

	// Construct the session manager once, after options have resolved sessionTTL and sessionStorage.
	sseFactory := func(id string) session.Session { return session.NewSSESession(id) }
	if proxy.sessionStorage != nil {
		proxy.sessionManager = session.NewManagerWithStorage(proxy.sessionTTL, sseFactory, proxy.sessionStorage)
	} else {
		proxy.sessionManager = session.NewManager(proxy.sessionTTL, sseFactory)
	}

	// Create MCP pinger and health checker
	mcpPinger := NewMCPPinger(proxy)
	proxy.healthChecker = healthcheck.NewHealthChecker("stdio", mcpPinger)

	return proxy
}

// applyMiddlewares applies a chain of middlewares to a handler
func applyMiddlewares(handler http.Handler, middlewares ...types.NamedMiddleware) http.Handler {
	// Apply middleware chain in reverse order (last middleware is applied first)
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i].Function(handler)
	}
	return handler
}

// Start starts the HTTP SSE proxy.
func (p *HTTPSSEProxy) Start(_ context.Context) error {
	// Create a new HTTP server
	mux := http.NewServeMux()

	// Add handlers for SSE and JSON-RPC with middlewares
	// At some point we should add support for Streamable HTTP transport here
	// https://modelcontextprotocol.io/specification/2025-03-26/basic/transports#streamable-http
	mux.Handle(ssecommon.HTTPSSEEndpoint, applyMiddlewares(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			p.handleSSEConnection(w, r)
		}),
		p.middlewares...,
	))

	mux.Handle(ssecommon.HTTPMessagesEndpoint, applyMiddlewares(http.HandlerFunc(p.handlePostRequest), p.middlewares...))

	// Add health check endpoint with MCP status (no middlewares)
	mux.Handle("/health", p.healthChecker)

	// Add Prometheus metrics endpoint if handler is provided (no middlewares)
	if p.prometheusHandler != nil {
		mux.Handle("/metrics", p.prometheusHandler)
		slog.Debug("prometheus metrics endpoint enabled at /metrics")
	}

	// Create a listener to get the actual port when using port 0
	// Use ListenConfig with SO_REUSEADDR to allow port reuse after unclean shutdown
	addr := fmt.Sprintf("%s:%d", p.host, p.port)
	lc := socket.ListenConfig()
	listener, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	// Update the server address with the actual address
	actualAddr := listener.Addr().String()

	// Create the server
	p.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	// Store the actual address
	p.server.Addr = actualAddr

	// Start the server in a goroutine
	go func() {
		// Parse the actual port for logging
		_, portStr, _ := net.SplitHostPort(actualAddr)
		actualPort, _ := strconv.Atoi(portStr)

		slog.Debug("http proxy started", "port", actualPort)
		//nolint:gosec // G706: logging configured SSE and JSON-RPC endpoint addresses
		slog.Debug("sse endpoint",
			"url", fmt.Sprintf("http://%s%s", actualAddr, ssecommon.HTTPSSEEndpoint))
		//nolint:gosec // G706: logging configured JSON-RPC endpoint address
		slog.Debug("json-RPC endpoint",
			"url", fmt.Sprintf("http://%s%s", actualAddr, ssecommon.HTTPMessagesEndpoint))

		if err := p.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server error", "error", err)
		}
	}()

	// Give the server a moment to start
	time.Sleep(10 * time.Millisecond)

	return nil
}

// Stop stops the HTTP SSE proxy. It is safe to call Stop more than once or
// concurrently; only the first call performs the shutdown sequence.
func (p *HTTPSSEProxy) Stop(ctx context.Context) error {
	var stopErr error
	p.stopOnce.Do(func() {
		// Signal shutdown to SSE handlers waiting on shutdownCh.
		close(p.shutdownCh)

		// Disconnect all active SSE connections.
		p.liveSSESessions.Range(func(_, value interface{}) bool {
			if sess, ok := value.(*session.SSESession); ok {
				sess.Disconnect()
			}
			return true
		})

		// Stop the session manager last: terminates the cleanup goroutine and
		// closes any underlying storage connections (e.g. Redis client).
		// Deferred so it always runs even if server.Shutdown returns an error.
		defer func() {
			if p.sessionManager != nil {
				if err := p.sessionManager.Stop(); err != nil {
					slog.Error("failed to stop session manager", "error", err)
				}
			}
		}()

		// Drain active HTTP connections before tearing down storage. This ensures
		// that removeClient calls (triggered by SSE handler cancellation) can still
		// reach sessionManager.Delete without hitting a closed storage backend.
		if p.server != nil {
			if err := p.server.Shutdown(ctx); err != nil {
				stopErr = err
				return
			}
		}
	})
	return stopErr
}

// IsRunning checks if the proxy is running.
func (p *HTTPSSEProxy) IsRunning() (bool, error) {
	select {
	case <-p.shutdownCh:
		return false, nil
	default:
		return true, nil
	}
}

// GetMessageChannel returns the channel for messages to/from the destination.
func (p *HTTPSSEProxy) GetMessageChannel() chan jsonrpc2.Message {
	return p.messageCh
}

// SendMessageToDestination sends a message to the destination via the message channel.
func (p *HTTPSSEProxy) SendMessageToDestination(msg jsonrpc2.Message) error {
	select {
	case p.messageCh <- msg:
		// Message sent successfully
		return nil
	default:
		// Channel is full or closed
		return fmt.Errorf("failed to send message to destination")
	}
}

// ForwardResponseToClients forwards a response from the destination to all connected SSE clients.
func (p *HTTPSSEProxy) ForwardResponseToClients(_ context.Context, msg jsonrpc2.Message) error {
	// Serialize the message to JSON
	data, err := jsonrpc2.EncodeMessage(msg)
	if err != nil {
		return fmt.Errorf("failed to encode JSON-RPC message: %w", err)
	}

	// Create an SSE message
	sseMsg := ssecommon.NewSSEMessage("message", string(data))

	// Check if there are any connected clients
	hasClients := false
	p.liveSSESessions.Range(func(_, _ interface{}) bool {
		hasClients = true
		return false // Stop iteration after finding first session
	})

	if hasClients {
		// Send the message to all connected clients
		return p.sendSSEEvent(sseMsg)
	}

	// Queue the message for later delivery
	p.pendingMutex.Lock()
	p.pendingMessages = append(p.pendingMessages, ssecommon.NewPendingSSEMessage(sseMsg))
	p.pendingMutex.Unlock()

	return nil
}

// handleSSEConnection handles an SSE connection.
func (p *HTTPSSEProxy) handleSSEConnection(w http.ResponseWriter, r *http.Request) {
	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Create a unique client ID
	clientID := uuid.New().String()

	// Create a channel for sending messages to this client
	messageCh := make(chan string, 100)

	// Create SSE client info
	clientInfo := &ssecommon.SSEClient{
		MessageCh: messageCh,
		CreatedAt: time.Now(),
	}

	// Create and register the SSE session
	sseSession := session.NewSSESessionWithClient(clientID, clientInfo)
	if err := p.sessionManager.AddSession(sseSession); err != nil {
		slog.Error("failed to add SSE session", "error", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}
	p.liveSSESessions.Store(clientID, sseSession)

	// Process any pending messages for this client
	p.processPendingMessages(clientID, messageCh)

	// Create a flusher for SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Build and send the endpoint event
	endpointURL := p.buildEndpointURL(r, clientID)
	endpointMsg := ssecommon.NewSSEMessage("endpoint", endpointURL)

	// Send the initial event
	if _, err := fmt.Fprint(w, endpointMsg.ToSSEString()); err != nil { //nolint:gosec // G705: SSE data from internal MCP protocol
		slog.Debug("failed to write endpoint message", "error", err)
		return
	}
	flusher.Flush()

	// Create a context that is canceled when the client disconnects
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Start keep-alive ticker
	keepAliveTicker := time.NewTicker(30 * time.Second)
	defer keepAliveTicker.Stop()

	// Create a goroutine to monitor for client disconnection
	go func() {
		<-ctx.Done()
		p.removeClient(clientID)
		slog.Debug("client disconnected", "client_id", clientID)
	}()

	// Send messages to the client
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-messageCh:
			if !ok {
				return
			}
			if _, err := fmt.Fprint(w, msg); err != nil {
				slog.Debug("failed to write message", "error", err)
				return
			}
			flusher.Flush()
		case <-keepAliveTicker.C:
			// Refresh session TTL while the SSE socket is open so the cleanup
			// goroutine does not evict clients that haven't sent a POST recently.
			p.sessionManager.Get(clientID)
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				slog.Debug("failed to write keep-alive", "error", err)
				return
			}
			flusher.Flush()
		}
	}
}

// handlePostRequest handles a POST request with a JSON-RPC message.
func (p *HTTPSSEProxy) handlePostRequest(w http.ResponseWriter, r *http.Request) {
	// Only accept POST requests
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract session ID from query parameters
	query := r.URL.Query()
	sessionID := query.Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}

	// Check if the session exists in the distributed store.
	_, exists := p.sessionManager.Get(sessionID)
	if !exists {
		session.WriteNotFound(w, nil)
		return
	}

	// Verify the live SSE connection for this session is held by this instance.
	// With a distributed storage backend (e.g. Redis), sessionManager.Get succeeds
	// on any replica, but fan-out only reaches clients connected locally. Rejecting
	// here with 503 surfaces the affinity failure explicitly instead of silently
	// dropping the response after forwarding to the backend.
	if _, local := p.liveSSESessions.Load(sessionID); !local {
		http.Error(w, "SSE connection not held by this instance", http.StatusServiceUnavailable)
		return
	}

	// Read the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error reading request body: %v", err), http.StatusInternalServerError)
		return
	}

	// Parse the JSON-RPC message
	msg, err := jsonrpc2.DecodeMessage(body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error parsing JSON-RPC message: %v", err), http.StatusBadRequest)
		return
	}

	slog.Debug("received JSON-RPC message", "type", fmt.Sprintf("%T", msg))

	// Send the message to the destination
	if err := p.SendMessageToDestination(msg); err != nil {
		http.Error(w, "Failed to send message to destination", http.StatusInternalServerError)
		return
	}

	// Return a success response
	w.WriteHeader(http.StatusAccepted)
	if _, err := w.Write([]byte("Accepted")); err != nil {
		slog.Warn("failed to write response", "error", err)
	}
}

// sendSSEEvent sends an SSE event to all connected clients.
func (p *HTTPSSEProxy) sendSSEEvent(msg *ssecommon.SSEMessage) error {
	// Convert the message to an SSE-formatted string
	sseString := msg.ToSSEString()

	// Iterate through all live SSE connections and deliver the event
	p.liveSSESessions.Range(func(key, value interface{}) bool {
		clientID, ok := key.(string)
		if !ok {
			return true // Continue iteration
		}

		sseSession, ok := value.(*session.SSESession)
		if !ok {
			return true // Continue iteration
		}

		// Try to send the message
		if err := sseSession.SendMessage(sseString); err != nil {
			// Log the error but continue sending to other clients
			switch {
			case errors.Is(err, session.ErrSessionDisconnected):
				slog.Debug("client is disconnected, skipping message", "client_id", clientID)
			case errors.Is(err, session.ErrMessageChannelFull):
				slog.Debug("client channel full, skipping message", "client_id", clientID)
			}
		}

		return true // Continue iteration
	})

	return nil
}

// removeClient removes a client and closes its channel.
// liveSSESessions.LoadAndDelete is atomic, so concurrent calls for the same
// clientID are safe: only one will find the entry and call Disconnect.
func (p *HTTPSSEProxy) removeClient(clientID string) {
	// Disconnect the live session directly from liveSSESessions. With a
	// distributed storage backend (e.g. Redis), sessionManager.Get returns a
	// freshly-deserialized SSESession with a different MessageCh than the
	// actual live connection, so calling Disconnect() on it would close the
	// wrong channel and leave the real connection undrained.
	if val, ok := p.liveSSESessions.LoadAndDelete(clientID); ok {
		if sseSession, ok := val.(*session.SSESession); ok {
			sseSession.Disconnect()
		}
	}

	// Remove the session from the manager
	if err := p.sessionManager.Delete(clientID); err != nil {
		slog.Debug("failed to delete session", "client_id", clientID, "error", err)
	}
}

// processPendingMessages processes any pending messages for a new client.
func (p *HTTPSSEProxy) processPendingMessages(clientID string, messageCh chan<- string) {
	p.pendingMutex.Lock()
	defer p.pendingMutex.Unlock()

	if len(p.pendingMessages) == 0 {
		return
	}

	// Find messages for this client (all messages for now)
	for i, pendingMsg := range p.pendingMessages {
		// Convert to SSE string
		sseString := pendingMsg.Message.ToSSEString()

		// Send to the client
		select {
		case messageCh <- sseString:
			// Message sent successfully
		default:
			// Channel is full, stop sending
			slog.Error("client channel full after sending pending messages",
				"client_id", clientID, "sent", i, "total", len(p.pendingMessages))
			// Remove successfully sent messages and keep the rest
			p.pendingMessages = p.pendingMessages[i:]
			return
		}
	}

	// Clear the pending messages
	p.pendingMessages = nil
}

// buildEndpointURL constructs the endpoint URL from request headers and proxy configuration.
func (p *HTTPSSEProxy) buildEndpointURL(r *http.Request, clientID string) string {
	host := r.Host
	prefix := ""

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := r.Header.Get("X-Forwarded-Proto"); forwardedProto != "" {
		scheme = forwardedProto
	}

	// Handle X-Forwarded headers from reverse proxies only if trusted
	if p.trustProxyHeaders {
		if forwardedHost := r.Header.Get("X-Forwarded-Host"); forwardedHost != "" {
			host = forwardedHost
			if forwardedPort := r.Header.Get("X-Forwarded-Port"); forwardedPort != "" {
				// Strip any existing port from host before adding the forwarded port
				if hostOnly, _, err := net.SplitHostPort(host); err == nil {
					host = hostOnly
				}
				host = net.JoinHostPort(host, forwardedPort)
			}
		}

		prefix = r.Header.Get("X-Forwarded-Prefix")
	}

	// Strip the SSE endpoint suffix from prefix if present, since we'll add the full messages path
	prefix = stripSSEEndpointSuffix(prefix)

	u := &url.URL{
		Scheme: scheme,
		Host:   host,
		Path:   path.Join(prefix, ssecommon.HTTPMessagesEndpoint),
	}
	q := u.Query()
	q.Set("session_id", clientID)
	u.RawQuery = q.Encode()

	return u.String()
}

// stripSSEEndpointSuffix removes the SSE endpoint suffix from a path prefix if present.
func stripSSEEndpointSuffix(prefix string) string {
	sseEndpointLen := len(ssecommon.HTTPSSEEndpoint)
	if len(prefix) < sseEndpointLen {
		return prefix
	}

	// Check if the prefix ends with the SSE endpoint
	suffixStart := len(prefix) - sseEndpointLen
	if prefix[suffixStart:] == ssecommon.HTTPSSEEndpoint {
		return prefix[:suffixStart]
	}

	return prefix
}

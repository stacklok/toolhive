// Package httpsse provides an HTTP proxy implementation for Server-Sent Events (SSE)
// used in communication between the client and MCP server.
package httpsse

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/healthcheck"
	"github.com/stacklok/toolhive/pkg/logger"
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
	host          string
	port          int
	containerName string
	middlewares   []types.MiddlewareFunction

	// HTTP server
	server     *http.Server
	shutdownCh chan struct{}

	// Optional Prometheus metrics handler
	prometheusHandler http.Handler

	// Session manager for SSE clients
	sessionManager *session.Manager

	// Pending messages for SSE clients
	pendingMessages []*ssecommon.PendingSSEMessage
	pendingMutex    sync.Mutex

	// Message channel
	messageCh chan jsonrpc2.Message

	// Health checker
	healthChecker *healthcheck.HealthChecker

	// Track closed clients to prevent double-close
	closedClients      map[string]bool
	closedClientsMutex sync.Mutex
}

// NewHTTPSSEProxy creates a new HTTP SSE proxy for transports.
func NewHTTPSSEProxy(
	host string, port int, containerName string, prometheusHandler http.Handler, middlewares ...types.MiddlewareFunction,
) *HTTPSSEProxy {
	// Create a factory for SSE sessions
	sseFactory := func(id string) session.Session {
		return session.NewSSESession(id)
	}

	proxy := &HTTPSSEProxy{
		middlewares:       middlewares,
		host:              host,
		port:              port,
		containerName:     containerName,
		shutdownCh:        make(chan struct{}),
		messageCh:         make(chan jsonrpc2.Message, 100),
		sessionManager:    session.NewManager(session.DefaultSessionTTL, sseFactory),
		pendingMessages:   []*ssecommon.PendingSSEMessage{},
		prometheusHandler: prometheusHandler,
		closedClients:     make(map[string]bool),
	}

	// Create MCP pinger and health checker
	mcpPinger := NewMCPPinger(proxy)
	proxy.healthChecker = healthcheck.NewHealthChecker("stdio", mcpPinger)

	return proxy
}

// applyMiddlewares applies a chain of middlewares to a handler
func applyMiddlewares(handler http.Handler, middlewares ...types.MiddlewareFunction) http.Handler {
	// Apply middleware chain in reverse order (last middleware is applied first)
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
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
		logger.Info("Prometheus metrics endpoint enabled at /metrics")
	}

	// Create a listener to get the actual port when using port 0
	addr := fmt.Sprintf("%s:%d", p.host, p.port)
	listener, err := net.Listen("tcp", addr)
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

		logger.Infof("HTTP proxy started for container %s on port %d", p.containerName, actualPort)
		logger.Infof("SSE endpoint: http://%s%s", actualAddr, ssecommon.HTTPSSEEndpoint)
		logger.Infof("JSON-RPC endpoint: http://%s%s", actualAddr, ssecommon.HTTPMessagesEndpoint)

		if err := p.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.Errorf("HTTP server error: %v", err)
		}
	}()

	// Give the server a moment to start
	time.Sleep(10 * time.Millisecond)

	return nil
}

// Stop stops the HTTP SSE proxy.
func (p *HTTPSSEProxy) Stop(ctx context.Context) error {
	// Signal shutdown
	close(p.shutdownCh)

	// Stop the session manager cleanup routine
	if p.sessionManager != nil {
		p.sessionManager.Stop()
	}

	// Disconnect all active sessions
	p.sessionManager.Range(func(_, value interface{}) bool {
		if sess, ok := value.(*session.SSESession); ok {
			sess.Disconnect()
		}
		return true
	})

	// Stop the HTTP server
	if p.server != nil {
		return p.server.Shutdown(ctx)
	}

	return nil
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

	// Check if there are any connected clients by checking session count
	hasClients := false
	p.sessionManager.Range(func(_, _ interface{}) bool {
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
		logger.Errorf("Failed to add SSE session: %v", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	// Process any pending messages for this client
	p.processPendingMessages(clientID, messageCh)

	// Create a flusher for SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Get the base URL for the POST endpoint
	host := r.Host
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := r.Header.Get("X-Forwarded-Proto"); forwardedProto != "" {
		scheme = forwardedProto
	}

	baseURL := fmt.Sprintf("%s://%s", scheme, host)

	// Create and send the endpoint event
	endpointURL := fmt.Sprintf("%s%s?session_id=%s", baseURL, ssecommon.HTTPMessagesEndpoint, clientID)
	endpointMsg := ssecommon.NewSSEMessage("endpoint", endpointURL)

	// Send the initial event
	fmt.Fprint(w, endpointMsg.ToSSEString())
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
		logger.Infof("Client %s disconnected", clientID)
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
			fmt.Fprint(w, msg)
			flusher.Flush()
		case <-keepAliveTicker.C:
			// Send SSE comment as keep-alive
			fmt.Fprint(w, ": keep-alive\n\n")
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

	// Check if the session exists
	_, exists := p.sessionManager.Get(sessionID)
	if !exists {
		http.Error(w, "Could not find session", http.StatusNotFound)
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

	// Log the message
	logger.Infof("Received JSON-RPC message: %T", msg)

	// Send the message to the destination
	if err := p.SendMessageToDestination(msg); err != nil {
		http.Error(w, "Failed to send message to destination", http.StatusInternalServerError)
		return
	}

	// Return a success response
	w.WriteHeader(http.StatusAccepted)
	if _, err := w.Write([]byte("Accepted")); err != nil {
		logger.Warnf("Warning: Failed to write response: %v", err)
	}
}

// sendSSEEvent sends an SSE event to all connected clients.
func (p *HTTPSSEProxy) sendSSEEvent(msg *ssecommon.SSEMessage) error {
	// Convert the message to an SSE-formatted string
	sseString := msg.ToSSEString()

	// Iterate through all sessions and send to SSE sessions
	p.sessionManager.Range(func(key, value interface{}) bool {
		clientID, ok := key.(string)
		if !ok {
			return true // Continue iteration
		}

		sess, ok := value.(session.Session)
		if !ok {
			return true // Continue iteration
		}

		// Check if this is an SSE session
		if sseSession, ok := sess.(*session.SSESession); ok {
			// Try to send the message
			if err := sseSession.SendMessage(sseString); err != nil {
				// Log the error but continue sending to other clients
				switch err {
				case session.ErrSessionDisconnected:
					logger.Debugf("Client %s is disconnected, skipping message", clientID)
				case session.ErrMessageChannelFull:
					logger.Debugf("Client %s channel full, skipping message", clientID)
				}
			}
		}

		return true // Continue iteration
	})

	return nil
}

// removeClient safely removes a client and closes its channel
func (p *HTTPSSEProxy) removeClient(clientID string) {
	// Check if already closed
	p.closedClientsMutex.Lock()
	if p.closedClients[clientID] {
		p.closedClientsMutex.Unlock()
		return
	}
	p.closedClients[clientID] = true
	p.closedClientsMutex.Unlock()

	// Get the session from the manager
	sess, exists := p.sessionManager.Get(clientID)
	if !exists {
		return
	}

	// If it's an SSE session, disconnect it
	if sseSession, ok := sess.(*session.SSESession); ok {
		sseSession.Disconnect()
	}

	// Remove the session from the manager
	p.sessionManager.Delete(clientID)

	// Clean up closed clients map periodically (prevent memory leak)
	p.closedClientsMutex.Lock()
	if len(p.closedClients) > 1000 {
		// Reset the map when it gets too large
		p.closedClients = make(map[string]bool)
	}
	p.closedClientsMutex.Unlock()
}

// processPendingMessages processes any pending messages for a new client.
func (p *HTTPSSEProxy) processPendingMessages(clientID string, messageCh chan<- string) {
	p.pendingMutex.Lock()
	defer p.pendingMutex.Unlock()

	if len(p.pendingMessages) == 0 {
		return
	}

	// Find messages for this client (all messages for now)
	for _, pendingMsg := range p.pendingMessages {
		// Convert to SSE string
		sseString := pendingMsg.Message.ToSSEString()

		// Send to the client
		select {
		case messageCh <- sseString:
			// Message sent successfully
		default:
			// Channel is full, stop sending
			logger.Errorf("Failed to send pending message to client %s (channel full)", clientID)
			return
		}
	}

	// Clear the pending messages
	p.pendingMessages = nil
}

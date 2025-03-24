package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Proxy defines the interface for proxying messages between clients and destinations.
type Proxy interface {
	// Start starts the proxy.
	Start(ctx context.Context) error

	// Stop stops the proxy.
	Stop(ctx context.Context) error

	// GetMessageChannel returns the channel for messages to/from the destination.
	GetMessageChannel() chan *JSONRPCMessage

	// GetResponseChannel returns the channel for receiving messages from the destination.
	GetResponseChannel() <-chan *JSONRPCMessage

	// SendMessageToDestination sends a message to the destination.
	SendMessageToDestination(msg *JSONRPCMessage) error

	// ForwardResponseToClients forwards a response from the destination to clients.
	ForwardResponseToClients(ctx context.Context, msg *JSONRPCMessage) error

	// SendResponseMessage sends a message to the response channel.
	SendResponseMessage(msg *JSONRPCMessage) error
}

// HTTPSSEProxy encapsulates the HTTP proxy functionality for SSE transports.
// It provides SSE endpoints and JSON-RPC message handling.
type HTTPSSEProxy struct {
	// Basic configuration
	port          int
	containerName string

	// HTTP server
	server     *http.Server
	shutdownCh chan struct{}

	// SSE clients
	sseClients      map[string]*SSEClient
	sseClientsMutex sync.Mutex

	// Pending messages for SSE clients
	pendingMessages []*PendingSSEMessage
	pendingMutex    sync.Mutex

	// Message channels
	messageCh  chan *JSONRPCMessage
	responseCh chan *JSONRPCMessage
}

// NewHTTPSSEProxy creates a new HTTP SSE proxy for transports.
func NewHTTPSSEProxy(port int, containerName string) *HTTPSSEProxy {
	return &HTTPSSEProxy{
		port:            port,
		containerName:   containerName,
		shutdownCh:      make(chan struct{}),
		messageCh:       make(chan *JSONRPCMessage, 100),
		responseCh:      make(chan *JSONRPCMessage, 100),
		sseClients:      make(map[string]*SSEClient),
		pendingMessages: []*PendingSSEMessage{},
	}
}

// Start starts the HTTP SSE proxy.
func (p *HTTPSSEProxy) Start(_ context.Context) error {
	// Create a new HTTP server
	mux := http.NewServeMux()

	// Add handlers for SSE and JSON-RPC
	mux.HandleFunc(HTTPSSEEndpoint, p.handleSSEConnection)
	mux.HandleFunc(HTTPMessagesEndpoint, p.handlePostRequest)

	// Add a simple health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			fmt.Printf("Warning: Failed to write health check response: %v\n", err)
		}
	})

	// Create the server
	p.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", p.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	// Start the server in a goroutine
	go func() {
		fmt.Printf("HTTP proxy started for container %s on port %d\n", p.containerName, p.port)
		fmt.Printf("SSE endpoint: http://localhost:%d%s\n", p.port, HTTPSSEEndpoint)
		fmt.Printf("JSON-RPC endpoint: http://localhost:%d%s\n", p.port, HTTPMessagesEndpoint)

		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("HTTP server error: %v\n", err)
		}
	}()

	return nil
}

// Stop stops the HTTP SSE proxy.
func (p *HTTPSSEProxy) Stop(ctx context.Context) error {
	// Signal shutdown
	close(p.shutdownCh)

	// Stop the HTTP server
	if p.server != nil {
		return p.server.Shutdown(ctx)
	}

	return nil
}

// GetMessageChannel returns the channel for messages to/from the destination.
func (p *HTTPSSEProxy) GetMessageChannel() chan *JSONRPCMessage {
	return p.messageCh
}

// GetResponseChannel returns the channel for receiving messages from the destination.
func (p *HTTPSSEProxy) GetResponseChannel() <-chan *JSONRPCMessage {
	return p.responseCh
}

// SendMessageToDestination sends a message to the destination via the message channel.
func (p *HTTPSSEProxy) SendMessageToDestination(msg *JSONRPCMessage) error {
	select {
	case p.messageCh <- msg:
		// Message sent successfully
		return nil
	default:
		// Channel is full or closed
		return fmt.Errorf("failed to send message to destination")
	}
}

// SendResponseMessage sends a message to the response channel.
func (p *HTTPSSEProxy) SendResponseMessage(msg *JSONRPCMessage) error {
	select {
	case p.responseCh <- msg:
		// Message sent successfully
		return nil
	default:
		// Channel is full or closed
		return fmt.Errorf("failed to send message to response channel")
	}
}

// ForwardResponseToClients forwards a response from the destination to all connected SSE clients.
func (p *HTTPSSEProxy) ForwardResponseToClients(_ context.Context, msg *JSONRPCMessage) error {
	// Serialize the message to JSON
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON-RPC message: %w", err)
	}

	// Create an SSE message
	sseMsg := NewSSEMessage("message", string(data))

	// Check if there are any connected clients
	p.sseClientsMutex.Lock()
	hasClients := len(p.sseClients) > 0
	p.sseClientsMutex.Unlock()

	if hasClients {
		// Send the message to all connected clients
		return p.sendSSEEvent(sseMsg)
	}

	// Queue the message for later delivery
	p.pendingMutex.Lock()
	p.pendingMessages = append(p.pendingMessages, NewPendingSSEMessage(sseMsg))
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

	// Register the client
	p.sseClientsMutex.Lock()
	p.sseClients[clientID] = &SSEClient{
		MessageCh: messageCh,
		CreatedAt: time.Now(),
	}
	p.sseClientsMutex.Unlock()

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
	endpointURL := fmt.Sprintf("%s%s?session_id=%s", baseURL, HTTPMessagesEndpoint, clientID)
	endpointMsg := NewSSEMessage("endpoint", endpointURL)

	// Send the initial event
	fmt.Fprint(w, endpointMsg.ToSSEString())
	flusher.Flush()

	// Create a context that is canceled when the client disconnects
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Create a goroutine to monitor for client disconnection
	go func() {
		<-ctx.Done()
		p.sseClientsMutex.Lock()
		delete(p.sseClients, clientID)
		p.sseClientsMutex.Unlock()
		close(messageCh)
		fmt.Printf("Client %s disconnected\n", clientID)
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
	p.sseClientsMutex.Lock()
	_, exists := p.sseClients[sessionID]
	p.sseClientsMutex.Unlock()

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
	var msg JSONRPCMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, fmt.Sprintf("Error parsing JSON-RPC message: %v", err), http.StatusBadRequest)
		return
	}

	// Validate the message
	if err := msg.Validate(); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON-RPC message: %v", err), http.StatusBadRequest)
		return
	}

	// Log the message
	LogJSONRPCMessage(&msg)

	// Send the message to the destination
	if err := p.SendMessageToDestination(&msg); err != nil {
		http.Error(w, "Failed to send message to destination", http.StatusInternalServerError)
		return
	}

	// Return a success response
	w.WriteHeader(http.StatusAccepted)
	if _, err := w.Write([]byte("Accepted")); err != nil {
		fmt.Printf("Warning: Failed to write response: %v\n", err)
	}
}

// sendSSEEvent sends an SSE event to all connected clients.
func (p *HTTPSSEProxy) sendSSEEvent(msg *SSEMessage) error {
	// Convert the message to an SSE-formatted string
	sseString := msg.ToSSEString()

	// Send to all clients
	p.sseClientsMutex.Lock()
	defer p.sseClientsMutex.Unlock()

	for clientID, client := range p.sseClients {
		select {
		case client.MessageCh <- sseString:
			// Message sent successfully
		default:
			// Channel is full or closed, remove the client
			delete(p.sseClients, clientID)
			close(client.MessageCh)
			fmt.Printf("Client %s removed (channel full or closed)\n", clientID)
		}
	}

	return nil
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
			fmt.Printf("Failed to send pending message to client %s (channel full)\n", clientID)
			return
		}
	}

	// Clear the pending messages
	p.pendingMessages = nil
}

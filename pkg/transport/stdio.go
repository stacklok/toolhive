package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/stacklok/vibetool/pkg/container"
)

// SSEClient represents a connected SSE client
type SSEClient struct {
	// MessageCh is the channel for sending messages to the client
	MessageCh chan string
	// CreatedAt is the time the client connected
	CreatedAt time.Time
}

// StdioTransport implements the Transport interface using standard input/output.
// It acts as a proxy between the MCP client and the container's stdin/stdout.
type StdioTransport struct {
	port            int
	containerID     string
	containerName   string
	runtime         container.Runtime
	
	// Mutex for protecting shared state
	mutex           sync.Mutex
	
	// Channels for communication
	shutdownCh      chan struct{}
	messageCh       chan *JSONRPCMessage
	responseCh      chan *JSONRPCMessage
	
	// HTTP server for SSE
	httpServer      *http.Server
	httpShutdownCh  chan struct{}
	
	// SSE clients
	sseClients      map[string]*SSEClient
	sseClientsMutex sync.Mutex
	
	// Pending messages for SSE clients
	pendingMessages []*PendingSSEMessage
	pendingMutex    sync.Mutex
}

// NewStdioTransport creates a new stdio transport.
func NewStdioTransport(port int) *StdioTransport {
	return &StdioTransport{
		port:           port,
		shutdownCh:     make(chan struct{}),
		messageCh:      make(chan *JSONRPCMessage, 100),
		responseCh:     make(chan *JSONRPCMessage, 100),
		httpShutdownCh: make(chan struct{}),
		sseClients:     make(map[string]*SSEClient),
		pendingMessages: []*PendingSSEMessage{},
	}
}

// WithRuntime sets the container runtime for the transport.
func (t *StdioTransport) WithRuntime(runtime container.Runtime) *StdioTransport {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	
	t.runtime = runtime
	return t
}

// Mode returns the transport mode.
func (t *StdioTransport) Mode() TransportType {
	return TransportTypeStdio
}

// Port returns the port used by the transport.
func (t *StdioTransport) Port() int {
	return t.port
}

// Setup prepares the transport for use with a specific container.
func (t *StdioTransport) Setup(ctx context.Context, containerID, containerName string, envVars map[string]string, containerIP string) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	
	t.containerID = containerID
	t.containerName = containerName
	
	// Add transport-specific environment variables
	envVars["MCP_TRANSPORT"] = "stdio"
	
	return nil
}

// Start initializes the transport and begins processing messages.
// The stdin and stdout parameters are provided by the caller (run command)
// and are already attached to the container.
func (t *StdioTransport) Start(ctx context.Context, stdin io.WriteCloser, stdout io.ReadCloser) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	
	if t.containerID == "" {
		return ErrContainerIDNotSet
	}
	
	if t.containerName == "" {
		return ErrContainerNameNotSet
	}
	
	// Start processing messages in a goroutine
	go t.processMessages(ctx, stdin, stdout)
	
	// Start the HTTP server for SSE
	if err := t.startHTTPServer(ctx); err != nil {
		return err
	}
	
	return nil
}

// Stop gracefully shuts down the transport.
func (t *StdioTransport) Stop(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	
	// Signal shutdown
	close(t.shutdownCh)
	
	// Stop the HTTP server
	if t.httpServer != nil {
		t.httpServer.Shutdown(ctx)
	}
	
	return nil
}

// IsRunning checks if the transport is currently running.
func (t *StdioTransport) IsRunning(ctx context.Context) (bool, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	
	// Check if the shutdown channel is closed
	select {
	case <-t.shutdownCh:
		return false, nil
	default:
		return true, nil
	}
}

// GetReader returns a reader for receiving messages from the transport.
func (t *StdioTransport) GetReader() io.Reader {
	// This is not used in the StdioTransport implementation
	return nil
}

// GetWriter returns a writer for sending messages to the transport.
func (t *StdioTransport) GetWriter() io.Writer {
	// This is not used in the StdioTransport implementation
	return nil
}

// processMessages handles the message exchange between the client and container.
func (t *StdioTransport) processMessages(ctx context.Context, stdin io.WriteCloser, stdout io.ReadCloser) {
	// Create a context that will be canceled when shutdown is signaled
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	
	// Monitor for shutdown signal
	go func() {
		select {
		case <-t.shutdownCh:
			cancel()
		case <-ctx.Done():
			// Context was canceled elsewhere
		}
	}()
	
	// Start a goroutine to read from stdout
	go t.processStdout(ctx, stdout)
	
	// Process incoming messages and send them to the container
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-t.messageCh:
			if err := t.sendMessageToContainer(ctx, stdin, msg); err != nil {
				fmt.Printf("Error sending message to container: %v\n", err)
			}
		}
	}
}

// processStdout reads from the container's stdout and processes JSON-RPC messages.
func (t *StdioTransport) processStdout(ctx context.Context, stdout io.ReadCloser) {
	// Create a buffer for accumulating data
	var buffer bytes.Buffer
	
	// Create a buffer for reading
	readBuffer := make([]byte, 4096)
	
	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Read data from stdout
			n, err := stdout.Read(readBuffer)
			if err != nil {
				if err == io.EOF {
					fmt.Println("Container stdout closed")
				} else {
					fmt.Printf("Error reading from container stdout: %v\n", err)
				}
				return
			}
			
			if n > 0 {
				// Write the data to the buffer
				buffer.Write(readBuffer[:n])
				
				// Process the buffer
				t.processBuffer(ctx, &buffer)
			}
		}
	}
}

// processBuffer processes the accumulated data in the buffer.
func (t *StdioTransport) processBuffer(ctx context.Context, buffer *bytes.Buffer) {
	// Process complete lines
	for {
		line, err := buffer.ReadString('\n')
		if err == io.EOF {
			// No complete line found, put the data back in the buffer
			buffer.WriteString(line)
			break
		}
		
		// Remove the trailing newline
		line = line[:len(line)-1]
		
		// Try to parse as JSON-RPC
		if line != "" {
			t.parseAndForwardJSONRPC(ctx, line)
		}
	}
}

// sanitizeJSONString removes control characters and finds the first valid JSON object
func sanitizeJSONString(input string) string {
	// Find the first opening brace
	startIdx := strings.Index(input, "{")
	if startIdx == -1 {
		return input // No JSON object found
	}
	
	// Find the last closing brace
	endIdx := strings.LastIndex(input, "}")
	if endIdx == -1 || endIdx < startIdx {
		return input // No valid JSON object found
	}
	
	// Extract the JSON object
	return input[startIdx : endIdx+1]
}

// parseAndForwardJSONRPC parses a JSON-RPC message and forwards it.
func (t *StdioTransport) parseAndForwardJSONRPC(ctx context.Context, line string) {
	// Log the raw line for debugging
	fmt.Printf("JSON-RPC raw: %s\n", line)
	
	// Check if the line contains binary data
	hasBinaryData := false
	for _, c := range line {
		if c < 32 && c != '\t' && c != '\r' && c != '\n' {
			hasBinaryData = true
			fmt.Printf("Found binary data or control character: %x\n", c)
		}
	}
	
	// If the line contains binary data, try to sanitize it
	var jsonData string
	if hasBinaryData {
		jsonData = sanitizeJSONString(line)
		fmt.Printf("Sanitized JSON: %s\n", jsonData)
	} else {
		jsonData = line
	}
	
	// Try to parse the JSON
	var msg JSONRPCMessage
	if err := json.Unmarshal([]byte(jsonData), &msg); err != nil {
		fmt.Printf("Error parsing JSON-RPC message: %v\n", err)
		return
	}
	
	// Validate the message
	if err := msg.Validate(); err != nil {
		fmt.Printf("Invalid JSON-RPC message: %v\n", err)
		return
	}
	
	// Log the message
	t.logJSONRPCMessage(&msg)
	
	// Forward to SSE clients
	if err := t.forwardToSSEClients(ctx, &msg); err != nil {
		fmt.Printf("Error forwarding to SSE clients: %v\n", err)
	}
	
	// Send to response channel
	select {
	case t.responseCh <- &msg:
		// Message sent successfully
	default:
		// Channel is full or closed
		fmt.Println("Failed to send message to response channel")
	}
}

// sendMessageToContainer sends a JSON-RPC message to the container.
func (t *StdioTransport) sendMessageToContainer(ctx context.Context, stdin io.Writer, msg *JSONRPCMessage) error {
	// Serialize the message
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON-RPC message: %w", err)
	}
	
	// Add newline
	data = append(data, '\n')
	
	// Write to stdin
	if _, err := stdin.Write(data); err != nil {
		return fmt.Errorf("failed to write to container stdin: %w", err)
	}
	
	return nil
}

// logJSONRPCMessage logs a JSON-RPC message for debugging.
func (t *StdioTransport) logJSONRPCMessage(msg *JSONRPCMessage) {
	// Determine message type
	var messageType string
	if msg.IsRequest() {
		messageType = "request"
	} else if msg.IsResponse() {
		if msg.Error != nil {
			messageType = "error response"
		} else {
			messageType = "success response"
		}
	} else if msg.IsNotification() {
		messageType = "notification"
	} else {
		messageType = "unknown"
	}
	
	// Log basic info
	fmt.Printf("JSON-RPC %s: ", messageType)
	if msg.Method != "" {
		fmt.Printf("method=%s ", msg.Method)
	}
	if msg.ID != nil {
		fmt.Printf("id=%v ", msg.ID)
	}
	if msg.Error != nil {
		fmt.Printf("error=%s ", msg.Error.Message)
	}
	fmt.Println()
}

// startHTTPServer starts the HTTP server for SSE.
func (t *StdioTransport) startHTTPServer(ctx context.Context) error {
	// Create a new HTTP server
	mux := http.NewServeMux()
	
	// Add handlers for SSE and JSON-RPC
	mux.HandleFunc(HTTPSSEEndpoint, t.handleSSEConnection)
	mux.HandleFunc(HTTPMessagesEndpoint, t.handlePostRequest)
	
	// Create the server
	t.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", t.port),
		Handler: mux,
	}
	
	// Start the server in a goroutine
	go func() {
		fmt.Printf("HTTP with SSE transport started for STDIO container %s on port %d\n", t.containerName, t.port)
		fmt.Printf("SSE endpoint: http://localhost:%d%s\n", t.port, HTTPSSEEndpoint)
		fmt.Printf("JSON-RPC endpoint: http://localhost:%d%s\n", t.port, HTTPMessagesEndpoint)
		
		if err := t.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("HTTP server error: %v\n", err)
		}
	}()
	
	return nil
}

// handleSSEConnection handles an SSE connection.
func (t *StdioTransport) handleSSEConnection(w http.ResponseWriter, r *http.Request) {
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
	t.sseClientsMutex.Lock()
	t.sseClients[clientID] = &SSEClient{
		MessageCh: messageCh,
		CreatedAt: time.Now(),
	}
	t.sseClientsMutex.Unlock()
	
	// Process any pending messages for this client
	t.processPendingMessages(clientID, messageCh)
	
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
		t.sseClientsMutex.Lock()
		delete(t.sseClients, clientID)
		t.sseClientsMutex.Unlock()
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
func (t *StdioTransport) handlePostRequest(w http.ResponseWriter, r *http.Request) {
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
	t.sseClientsMutex.Lock()
	_, exists := t.sseClients[sessionID]
	t.sseClientsMutex.Unlock()
	
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
	t.logJSONRPCMessage(&msg)
	
	// Send the message to the container
	select {
	case t.messageCh <- &msg:
		// Message sent successfully
	default:
		// Channel is full or closed
		http.Error(w, "Failed to send message to container", http.StatusInternalServerError)
		return
	}
	
	// Return a success response
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte("Accepted"))
}

// forwardToSSEClients forwards a JSON-RPC message to all connected SSE clients.
func (t *StdioTransport) forwardToSSEClients(ctx context.Context, msg *JSONRPCMessage) error {
	// Serialize the message to JSON
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON-RPC message: %w", err)
	}
	
	// Create an SSE message
	sseMsg := NewSSEMessage("message", string(data))
	
	// Check if there are any connected clients
	t.sseClientsMutex.Lock()
	hasClients := len(t.sseClients) > 0
	t.sseClientsMutex.Unlock()
	
	if hasClients {
		// Send the message to all connected clients
		return t.sendSSEEvent(ctx, sseMsg)
	} else {
		// Queue the message for later delivery
		t.pendingMutex.Lock()
		t.pendingMessages = append(t.pendingMessages, NewPendingSSEMessage(sseMsg))
		t.pendingMutex.Unlock()
	}
	
	return nil
}

// sendSSEEvent sends an SSE event to all connected clients.
func (t *StdioTransport) sendSSEEvent(ctx context.Context, msg *SSEMessage) error {
	// Convert the message to an SSE-formatted string
	sseString := msg.ToSSEString()
	
	// Send to all clients
	t.sseClientsMutex.Lock()
	defer t.sseClientsMutex.Unlock()
	
	for clientID, client := range t.sseClients {
		select {
		case client.MessageCh <- sseString:
			// Message sent successfully
		default:
			// Channel is full or closed, remove the client
			delete(t.sseClients, clientID)
			close(client.MessageCh)
			fmt.Printf("Client %s removed (channel full or closed)\n", clientID)
		}
	}
	
	return nil
}

// processPendingMessages processes any pending messages for a new client.
func (t *StdioTransport) processPendingMessages(clientID string, messageCh chan<- string) {
	t.pendingMutex.Lock()
	defer t.pendingMutex.Unlock()
	
	if len(t.pendingMessages) == 0 {
		return
	}
	
	// Find messages for this client (all messages for now)
	for _, pendingMsg := range t.pendingMessages {
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
	t.pendingMessages = nil
}
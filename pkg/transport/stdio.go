package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/stacklok/vibetool/pkg/container"
)

// StdioTransport implements the Transport interface using standard input/output.
// It acts as a proxy between the MCP client and the container's stdin/stdout.
type StdioTransport struct {
	port          int
	containerID   string
	containerName string
	runtime       container.Runtime

	// Mutex for protecting shared state
	mutex sync.Mutex

	// Channels for communication
	shutdownCh chan struct{}

	// HTTP SSE proxy
	httpProxy Proxy
}

// NewStdioTransport creates a new stdio transport.
func NewStdioTransport(port int) *StdioTransport {
	return &StdioTransport{
		port:       port,
		shutdownCh: make(chan struct{}),
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
func (t *StdioTransport) Setup(ctx context.Context, containerID, containerName string, envVars map[string]string) error {
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

	// Create and start the HTTP SSE proxy
	t.httpProxy = NewHTTPSSEProxy(t.port, t.containerName)
	if err := t.httpProxy.Start(ctx); err != nil {
		return err
	}

	// Start processing messages in a goroutine
	go t.processMessages(ctx, stdin, stdout)

	return nil
}

// Stop gracefully shuts down the transport.
func (t *StdioTransport) Stop(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Signal shutdown
	close(t.shutdownCh)

	// Stop the HTTP proxy
	if t.httpProxy != nil {
		return t.httpProxy.Stop(ctx)
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
	messageCh := t.httpProxy.GetMessageChannel()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-messageCh:
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
	LogJSONRPCMessage(&msg)

	// Forward to SSE clients via the HTTP proxy
	if err := t.httpProxy.ForwardResponseToClients(ctx, &msg); err != nil {
		fmt.Printf("Error forwarding to SSE clients: %v\n", err)
	}

	// Send to the response channel
	if err := t.httpProxy.SendResponseMessage(&msg); err != nil {
		fmt.Printf("Error sending to response channel: %v\n", err)
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

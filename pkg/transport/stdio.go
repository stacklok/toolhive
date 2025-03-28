package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/stacklok/vibetool/pkg/container"
	rt "github.com/stacklok/vibetool/pkg/container/runtime"
	"github.com/stacklok/vibetool/pkg/permissions"
)

// StdioTransport implements the Transport interface using standard input/output.
// It acts as a proxy between the MCP client and the container's stdin/stdout.
type StdioTransport struct {
	port          int
	containerID   string
	containerName string
	runtime       rt.Runtime
	debug         bool
	middlewares   []Middleware

	// Mutex for protecting shared state
	mutex sync.Mutex

	// Channels for communication
	shutdownCh chan struct{}
	errorCh    <-chan error

	// HTTP SSE proxy
	httpProxy Proxy

	// Container I/O
	stdin  io.WriteCloser
	stdout io.ReadCloser

	// Container monitor
	monitor rt.Monitor
}

// NewStdioTransport creates a new stdio transport.
func NewStdioTransport(
	port int,
	runtime rt.Runtime,
	debug bool,
	middlewares ...Middleware,
) *StdioTransport {
	return &StdioTransport{
		port:        port,
		runtime:     runtime,
		debug:       debug,
		middlewares: middlewares,
		shutdownCh:  make(chan struct{}),
	}
}

// Mode returns the transport mode.
func (*StdioTransport) Mode() TransportType {
	return TransportTypeStdio
}

// Port returns the port used by the transport.
func (t *StdioTransport) Port() int {
	return t.port
}

// Setup prepares the transport for use.
func (t *StdioTransport) Setup(ctx context.Context, runtime rt.Runtime, containerName string, containerInfo rt.ContainerInfo,
	cmdArgs []string, permissionProfile *permissions.Profile) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.runtime = runtime
	t.containerName = containerName

	// Create environment variables map
	envVars := make(map[string]string)
	for _, env := range containerInfo.Env {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			envVars[parts[0]] = parts[1]
		}
	}

	// Add transport-specific environment variables
	envVars["MCP_TRANSPORT"] = "stdio"

	// Create container options
	containerOptions := rt.NewCreateContainerOptions()
	containerOptions.AttachStdio = true

	// Create the container
	fmt.Printf("Creating container %s from image %s...\n", containerName, containerInfo.Image)
	containerID, err := t.runtime.CreateContainer(
		ctx,
		containerInfo.Image,
		containerName,
		cmdArgs,
		envVars,
		containerInfo.Labels,
		permissionProfile,
		"stdio",
		containerOptions,
	)
	if err != nil {
		return fmt.Errorf("failed to create container: %v", err)
	}
	t.containerID = containerID
	fmt.Printf("Container created with ID: %s\n", containerID)

	return nil
}

// Start initializes the transport and begins processing messages.
// The transport is responsible for starting the container and attaching to it.
func (t *StdioTransport) Start(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.containerID == "" {
		return ErrContainerIDNotSet
	}

	if t.containerName == "" {
		return ErrContainerNameNotSet
	}

	if t.runtime == nil {
		return fmt.Errorf("container runtime not set")
	}

	// Start the container
	fmt.Printf("Starting container %s...\n", t.containerName)
	if err := t.runtime.StartContainer(ctx, t.containerID); err != nil {
		return fmt.Errorf("failed to start container: %v", err)
	}

	// Attach to the container
	var err error
	t.stdin, t.stdout, err = t.runtime.AttachContainer(ctx, t.containerID)
	if err != nil {
		return fmt.Errorf("failed to attach to container: %w", err)
	}

	// Create and start the HTTP SSE proxy with middlewares
	t.httpProxy = NewHTTPSSEProxy(t.port, t.containerName, t.middlewares...)
	if err := t.httpProxy.Start(ctx); err != nil {
		return err
	}

	// Start processing messages in a goroutine
	go t.processMessages(ctx, t.stdin, t.stdout)

	// Create a container monitor
	monitorRuntime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container monitor: %v", err)
	}
	t.monitor = container.NewMonitor(monitorRuntime, t.containerID, t.containerName)

	// Start monitoring the container
	t.errorCh, err = t.monitor.StartMonitoring(ctx)
	if err != nil {
		return fmt.Errorf("failed to start container monitoring: %v", err)
	}

	// Start a goroutine to handle container exit
	go t.handleContainerExit(ctx)

	return nil
}

// Stop gracefully shuts down the transport and the container.
func (t *StdioTransport) Stop(ctx context.Context) error {
	// First check if the transport is already stopped without locking
	// to avoid deadlocks if Stop is called from multiple goroutines
	select {
	case <-t.shutdownCh:
		// Channel is already closed, transport is already stopping or stopped
		// Just return without doing anything else
		return nil
	default:
		// Channel is still open, proceed with stopping
	}

	// Now lock the mutex for the actual stopping process
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Check again after locking to handle race conditions
	select {
	case <-t.shutdownCh:
		// Channel was closed between our first check and acquiring the lock
		return nil
	default:
		// Channel is still open, close it to signal shutdown
		close(t.shutdownCh)
	}

	// Stop the monitor if it's running and we haven't already stopped it
	if t.monitor != nil {
		t.monitor.StopMonitoring()
		t.monitor = nil
	}

	// Stop the HTTP proxy
	if t.httpProxy != nil {
		if err := t.httpProxy.Stop(ctx); err != nil {
			fmt.Printf("Warning: Failed to stop HTTP proxy: %v\n", err)
		}
	}

	// Close stdin and stdout if they're open
	if t.stdin != nil {
		if err := t.stdin.Close(); err != nil {
			fmt.Printf("Warning: Failed to close stdin: %v\n", err)
		}
		t.stdin = nil
	}

	// Stop the container if runtime is available and we haven't already stopped it
	if t.runtime != nil && t.containerID != "" {
		// Check if the container is still running before trying to stop it
		running, err := t.runtime.IsContainerRunning(ctx, t.containerID)
		if err != nil {
			// If there's an error checking the container status, it might be gone already
			fmt.Printf("Warning: Failed to check container status: %v\n", err)
		} else if running {
			// Only try to stop the container if it's still running
			if err := t.runtime.StopContainer(ctx, t.containerID); err != nil {
				fmt.Printf("Warning: Failed to stop container: %v\n", err)
			}
		}

		// Remove the container if debug mode is not enabled
		if !t.debug {
			fmt.Printf("Removing container %s...\n", t.containerName)
			if err := t.runtime.RemoveContainer(ctx, t.containerID); err != nil {
				fmt.Printf("Warning: Failed to remove container: %v\n", err)
			} else {
				fmt.Printf("Container %s removed\n", t.containerName)
			}
		} else {
			fmt.Printf("Debug mode enabled, container %s not removed\n", t.containerName)
		}
	}

	return nil
}

// IsRunning checks if the transport is currently running.
func (t *StdioTransport) IsRunning(_ context.Context) (bool, error) {
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

		// Verify if new line character is present as last character
		// If so, remove it
		if len(line) > 0 && line[len(line)-1] == '\n' {
			// Remove the trailing newline
			line = line[:len(line)-1]
		}

		// Try to parse as JSON-RPC
		if line != "" {
			t.parseAndForwardJSONRPC(ctx, line)
		}
	}
}

// sanitizeJSONString extracts the first valid JSON object from a string
func sanitizeJSONString(input string) string {
	return sanitizeBinaryString(input)
}

// sanitizeBinaryString removes all non-JSON characters and whitespace from a string
func sanitizeBinaryString(input string) string {
	// Find the first opening brace
	startIdx := strings.Index(input, "{")
	if startIdx == -1 {
		return "" // No JSON object found
	}

	// Find the last closing brace
	endIdx := strings.LastIndex(input, "}")
	if endIdx == -1 || endIdx < startIdx {
		return "" // No valid JSON object found
	}

	// Extract just the JSON object, discarding everything else
	jsonObj := input[startIdx : endIdx+1]

	// Remove all whitespace and control characters
	var buffer bytes.Buffer

	for _, r := range jsonObj {
		if unicode.IsPrint(r) || isSpace(r) {
			buffer.WriteRune(r)
		}
	}

	return buffer.String()
}

// isSpace reports whether r is a space character as defined by JSON.
// These are the valid space characters in this implementation:
//   - ' ' (U+0020, SPACE)
//   - '\n' (U+000A, LINE FEED)
func isSpace(r rune) bool {
	return r == ' ' || r == '\n'
}

// parseAndForwardJSONRPC parses a JSON-RPC message and forwards it.
func (t *StdioTransport) parseAndForwardJSONRPC(ctx context.Context, line string) {
	// Log the raw line for debugging
	fmt.Printf("JSON-RPC raw: %s\n", line)

	// Check if the line contains binary data
	hasBinaryData := false
	for _, c := range line {
		if !unicode.IsPrint(c) && !isSpace(c) {
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
func (*StdioTransport) sendMessageToContainer(_ context.Context, stdin io.Writer, msg *JSONRPCMessage) error {
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

// handleContainerExit handles container exit events.
func (t *StdioTransport) handleContainerExit(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case err, ok := <-t.errorCh:
		// Check if the channel is closed
		if !ok {
			fmt.Printf("Container monitor channel closed for %s\n", t.containerName)
			return
		}

		fmt.Printf("Container %s exited: %v\n", t.containerName, err)

		// Check if the transport is already stopped before trying to stop it
		select {
		case <-t.shutdownCh:
			// Transport is already stopping or stopped
			fmt.Printf("Transport for %s is already stopping or stopped\n", t.containerName)
			return
		default:
			// Transport is still running, stop it
			// Create a context with timeout for stopping the transport
			stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if stopErr := t.Stop(stopCtx); stopErr != nil {
				fmt.Printf("Error stopping transport after container exit: %v\n", stopErr)
			}
		}
	}
}

package transport

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// SSETransport implements the Transport interface using Server-Sent Events.
type SSETransport struct {
	host          string
	port          int
	containerID   string
	containerName string
	containerIP   string
	
	// Mutex for protecting shared state
	mutex         sync.Mutex
	
	// Transparent proxy
	proxy         Proxy
	
	// Shutdown channel
	shutdownCh    chan struct{}
}

// NewSSETransport creates a new SSE transport.
func NewSSETransport(host string, port int) *SSETransport {
	if host == "" {
		host = "localhost"
	}
	
	return &SSETransport{
		host:        host,
		port:        port,
		containerIP: "",
		shutdownCh:  make(chan struct{}),
	}
}

// Mode returns the transport mode.
func (t *SSETransport) Mode() TransportType {
	return TransportTypeSSE
}

// Port returns the port used by the transport.
func (t *SSETransport) Port() int {
	return t.port
}

// Setup prepares the transport for use with a specific container.
func (t *SSETransport) Setup(ctx context.Context, containerID, containerName string, envVars map[string]string, containerIP string) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	
	t.containerID = containerID
	t.containerName = containerName
	
	// Store the container IP for later use in Start
	t.containerIP = containerIP
	
	// Add transport-specific environment variables
	envVars["MCP_TRANSPORT"] = "sse"
	envVars["MCP_PORT"] = fmt.Sprintf("%d", t.port)
	envVars["FASTMCP_PORT"] = fmt.Sprintf("%d", t.port)
	
	// If container IP is provided, use it for the host
	if containerIP != "" {
		envVars["MCP_HOST"] = containerIP
		fmt.Printf("Using container IP: %s\n", containerIP)
	} else {
		envVars["MCP_HOST"] = t.host
		fmt.Printf("Container IP not provided, using host: %s\n", t.host)
	}
	
	return nil
}

// Start initializes the transport and begins processing messages.
// For SSE transport, stdin and stdout are not used.
func (t *SSETransport) Start(ctx context.Context, stdin io.WriteCloser, stdout io.ReadCloser) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	
	if t.containerID == "" {
		return ErrContainerIDNotSet
	}
	
	if t.containerName == "" {
		return ErrContainerNameNotSet
	}
	
	// Create and start the transparent proxy
	// The SSE transport forwards requests to the container's HTTP server
	// We need to use the container's IP address and the same port as the host
	
	// Use the container IP if provided, otherwise use localhost
	targetHost := t.containerIP
	if targetHost == "" {
		targetHost = "localhost"
	}
	
	// Use the same port as the host
	// The container is exposing the same port as the host
	targetPort := t.port
	
	fmt.Printf("Setting up transparent proxy to forward from host port %d to container port %d on %s\n",
		t.port, targetPort, targetHost)
	
	// Create the transparent proxy
	t.proxy = NewTransparentProxy(t.port, t.containerName, targetHost, targetPort)
	if err := t.proxy.Start(ctx); err != nil {
		return err
	}
	
	fmt.Printf("SSE transport started for container %s on port %d\n", t.containerName, t.port)
	
	return nil
}

// Stop gracefully shuts down the transport.
func (t *SSETransport) Stop(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	
	// Signal shutdown
	close(t.shutdownCh)
	
	// Stop the transparent proxy
	if t.proxy != nil {
		return t.proxy.Stop(ctx)
	}
	
	return nil
}

// IsRunning checks if the transport is currently running.
func (t *SSETransport) IsRunning(ctx context.Context) (bool, error) {
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
func (t *SSETransport) GetReader() io.Reader {
	// This is not used in the SSETransport implementation
	return nil
}

// GetWriter returns a writer for sending messages to the transport.
func (t *SSETransport) GetWriter() io.Writer {
	// This is not used in the SSETransport implementation
	return nil
}
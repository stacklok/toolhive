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
	targetPort    int
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
func NewSSETransport(host string, port int, targetPort int) *SSETransport {
	if host == "" {
		host = "localhost"
	}
	
	return &SSETransport{
		host:        host,
		port:        port,
		targetPort:  targetPort,
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
	
	// Use the target port for the container's environment variables
	envVars["MCP_PORT"] = fmt.Sprintf("%d", t.targetPort)
	envVars["FASTMCP_PORT"] = fmt.Sprintf("%d", t.targetPort)
	
	// Always use localhost for the host
	// In a Docker bridge network, the container IP is not directly accessible from the host
	envVars["MCP_HOST"] = "localhost"
	
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
	// The SSE transport forwards requests from the host port to the container's target port
	
	// In a Docker bridge network, we need to use localhost since the container port is mapped to the host
	// We ignore containerIP even if it's set, as it's not directly accessible from the host
	targetHost := "localhost"
	
	// Check if target port is set
	if t.targetPort <= 0 {
		return fmt.Errorf("target port not set for SSE transport")
	}
	
	// Use the target port for the container
	containerPort := t.targetPort
	
	fmt.Printf("Setting up transparent proxy to forward from host port %d to localhost:%d\n",
		t.port, containerPort)
	
	// Create the transparent proxy
	t.proxy = NewTransparentProxy(t.port, t.containerName, targetHost, containerPort)
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
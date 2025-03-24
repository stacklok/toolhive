package transport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// TransparentProxy implements the Proxy interface as a transparent HTTP proxy
// that forwards requests to a destination.
// It's used by the SSE transport to forward requests to the container's HTTP server.
type TransparentProxy struct {
	// Basic configuration
	port          int
	containerName string
	targetHost    string
	targetPort    int

	// HTTP server
	server *http.Server

	// Mutex for protecting shared state
	mutex sync.Mutex

	// Shutdown channel
	shutdownCh chan struct{}
}

// NewTransparentProxy creates a new transparent proxy.
func NewTransparentProxy(port int, containerName, targetHost string, targetPort int) *TransparentProxy {
	return &TransparentProxy{
		port:          port,
		containerName: containerName,
		targetHost:    targetHost,
		targetPort:    targetPort,
		shutdownCh:    make(chan struct{}),
	}
}

// Start starts the transparent proxy.
func (p *TransparentProxy) Start(_ context.Context) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Create the target URL
	targetURL, err := url.Parse(fmt.Sprintf("http://%s:%d", p.targetHost, p.targetPort))
	if err != nil {
		return fmt.Errorf("failed to parse target URL: %w", err)
	}

	// Create a reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Create a handler that logs requests
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("Transparent proxy: %s %s -> %s\n", r.Method, r.URL.Path, targetURL)
		proxy.ServeHTTP(w, r)
	})

	// Create the server
	p.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", p.port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	// Start the server in a goroutine
	go func() {
		fmt.Printf("Transparent proxy started for container %s on port %d -> %s:%d\n",
			p.containerName, p.port, p.targetHost, p.targetPort)

		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Transparent proxy error: %v\n", err)
		}
	}()

	return nil
}

// Stop stops the transparent proxy.
func (p *TransparentProxy) Stop(ctx context.Context) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Signal shutdown
	close(p.shutdownCh)

	// Stop the HTTP server
	if p.server != nil {
		return p.server.Shutdown(ctx)
	}

	return nil
}

// IsRunning checks if the proxy is running.
func (p *TransparentProxy) IsRunning(_ context.Context) (bool, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Check if the shutdown channel is closed
	select {
	case <-p.shutdownCh:
		return false, nil
	default:
		return true, nil
	}
}

// GetMessageChannel returns the channel for messages to/from the destination.
// This is not used in the TransparentProxy implementation as it forwards HTTP requests directly.
func (_ *TransparentProxy) GetMessageChannel() chan *JSONRPCMessage {
	return nil
}

// GetResponseChannel returns the channel for receiving messages from the destination.
// This is not used in the TransparentProxy implementation as it forwards HTTP requests directly.
func (_ *TransparentProxy) GetResponseChannel() <-chan *JSONRPCMessage {
	return nil
}

// SendMessageToDestination sends a message to the destination.
// This is not used in the TransparentProxy implementation as it forwards HTTP requests directly.
func (_ *TransparentProxy) SendMessageToDestination(_ *JSONRPCMessage) error {
	return fmt.Errorf("SendMessageToDestination not implemented for TransparentProxy")
}

// ForwardResponseToClients forwards a response from the destination to clients.
// This is not used in the TransparentProxy implementation as it forwards HTTP requests directly.
func (_ *TransparentProxy) ForwardResponseToClients(_ context.Context, _ *JSONRPCMessage) error {
	return fmt.Errorf("ForwardResponseToClients not implemented for TransparentProxy")
}

// SendResponseMessage sends a message to the response channel.
// This is not used in the TransparentProxy implementation as it forwards HTTP requests directly.
func (_ *TransparentProxy) SendResponseMessage(_ *JSONRPCMessage) error {
	return fmt.Errorf("SendResponseMessage not implemented for TransparentProxy")
}

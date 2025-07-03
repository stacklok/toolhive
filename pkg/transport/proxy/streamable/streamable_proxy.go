package streamable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// Endpoint for streamable HTTP
	StreamableHTTPEndpoint = "/mcp"
)

// StreamableHTTPProxy implements a proxy for streamable HTTP transport.
type StreamableHTTPProxy struct {
	host              string
	port              int
	containerName     string
	shutdownCh        chan struct{}
	prometheusHandler http.Handler
	middlewares       []types.Middleware

	// Message channel for sending JSON-RPC to the container
	messageCh chan jsonrpc2.Message
	// Response channel for receiving JSON-RPC from the container
	responseCh chan jsonrpc2.Message

	server *http.Server
	mutex  sync.Mutex
}

func NewStreamableHTTPProxy(host string, port int, containerName string, prometheusHandler http.Handler, middlewares ...types.Middleware) *StreamableHTTPProxy {
	return &StreamableHTTPProxy{
		host:              host,
		port:              port,
		containerName:     containerName,
		shutdownCh:        make(chan struct{}),
		prometheusHandler: prometheusHandler,
		middlewares:       middlewares,
		messageCh:         make(chan jsonrpc2.Message, 100),
		responseCh:        make(chan jsonrpc2.Message, 100),
	}
}

func (p *StreamableHTTPProxy) Start(_ context.Context) error {
	mux := http.NewServeMux()

	mux.Handle(StreamableHTTPEndpoint, p.applyMiddlewares(http.HandlerFunc(p.handleStreamableRequest)))

	if p.prometheusHandler != nil {
		mux.Handle("/metrics", p.prometheusHandler)
	}

	p.server = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", p.host, p.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Infof("Streamable HTTP proxy started for container %s on port %d", p.containerName, p.port)
		logger.Infof("Streamable HTTP endpoint: http://%s:%d%s", p.host, p.port, StreamableHTTPEndpoint)
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Streamable HTTP server error: %v", err)
		}
	}()

	return nil
}

func (p *StreamableHTTPProxy) Stop(ctx context.Context) error {
	close(p.shutdownCh)
	if p.server != nil {
		return p.server.Shutdown(ctx)
	}
	return nil
}

func (p *StreamableHTTPProxy) GetMessageChannel() chan jsonrpc2.Message {
	return p.messageCh
}

func (p *StreamableHTTPProxy) GetResponseChannel() <-chan jsonrpc2.Message {
	return p.responseCh
}

func (p *StreamableHTTPProxy) SendMessageToDestination(msg jsonrpc2.Message) error {
	select {
	case p.messageCh <- msg:
		return nil
	default:
		return fmt.Errorf("failed to send message to destination")
	}
}

func (p *StreamableHTTPProxy) ForwardResponseToClients(_ context.Context, msg jsonrpc2.Message) error {
	select {
	case p.responseCh <- msg:
		return nil
	default:
		return fmt.Errorf("failed to forward response to client")
	}
}

// For compatibility with the Proxy interface
func (p *StreamableHTTPProxy) SendResponseMessage(msg jsonrpc2.Message) error {
	return p.ForwardResponseToClients(context.Background(), msg)
}

func (p *StreamableHTTPProxy) applyMiddlewares(handler http.Handler) http.Handler {
	for i := len(p.middlewares) - 1; i >= 0; i-- {
		handler = p.middlewares[i](handler)
	}
	return handler
}

// isValidJSONRPC2Raw checks if the raw JSON contains 'jsonrpc': '2.0'.
func isValidJSONRPC2Raw(data []byte) bool {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	ver, ok := raw["jsonrpc"].(string)
	return ok && ver == "2.0"
}

func isNotification(msg jsonrpc2.Message) bool {
	if req, ok := msg.(*jsonrpc2.Request); ok {
		return req.ID.Raw() == nil
	}
	return false
}

// handleStreamableRequest handles HTTP POST requests to /mcp
func (p *StreamableHTTPProxy) handleStreamableRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error reading request body: %v", err), http.StatusInternalServerError)
		return
	}

	// Check for batch (JSON array)
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		logger.Warnf("Batch JSON-RPC requests are not supported: %s", string(body))
		http.Error(w, "Batch JSON-RPC requests are not supported", http.StatusBadRequest)
		return
	}

	// Single message
	if !isValidJSONRPC2Raw(body) {
		logger.Warnf("Skipping invalid JSON-RPC 2.0 message: %s", string(body))
		http.Error(w, "Invalid JSON-RPC 2.0 message", http.StatusBadRequest)
		return
	}
	msg, err := jsonrpc2.DecodeMessage(body)
	if err != nil {
		logger.Warnf("Skipping message that failed to decode: %s", string(body))
		http.Error(w, "Invalid JSON-RPC 2.0 message", http.StatusBadRequest)
		return
	}

	// Notification or response: 202 Accepted, no body
	if isNotification(msg) || (func() bool { _, ok := msg.(*jsonrpc2.Response); return ok })() {
		if err := p.SendMessageToDestination(msg); err != nil {
			logger.Errorf("Failed to send message to destination: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Request: forward and wait for response
	if err := p.SendMessageToDestination(msg); err != nil {
		http.Error(w, "Failed to send message to destination", http.StatusInternalServerError)
		return
	}
	select {
	case resp := <-p.responseCh:
		if r, ok := resp.(*jsonrpc2.Response); ok && r.ID.IsValid() {
			w.Header().Set("Content-Type", "application/json")
			data, err := jsonrpc2.EncodeMessage(r)
			if err != nil {
				logger.Errorf("Failed to encode JSON-RPC response: %v", err)
				http.Error(w, "Failed to encode response", http.StatusInternalServerError)
				return
			}
			w.Write(data)
		} else {
			// Return a JSON-RPC error response
			w.Header().Set("Content-Type", "application/json")
			errResp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      nil,
				"error": map[string]interface{}{
					"code":    -32603,
					"message": "Invalid JSON-RPC 2.0 response from server",
				},
			}
			json.NewEncoder(w).Encode(errResp)
		}
	case <-time.After(10 * time.Second):
		http.Error(w, "Timeout waiting for response from container", http.StatusGatewayTimeout)
	}
}

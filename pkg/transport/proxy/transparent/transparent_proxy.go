// Package transparent provides a transparent HTTP proxy implementation
// that forwards requests to a destination without modifying them.
package transparent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/healthcheck"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TransparentProxy implements the Proxy interface as a transparent HTTP proxy
// that forwards requests to a destination.
// It's used by the SSE transport to forward requests to the container's HTTP server.
//
//nolint:revive // Intentionally named TransparentProxy despite package name
type TransparentProxy struct {
	// Basic configuration
	host          string
	port          int
	containerName string
	targetURI     string

	// HTTP server
	server *http.Server

	// Middleware chain
	middlewares []types.Middleware

	// Mutex for protecting shared state
	mutex sync.Mutex

	// Shutdown channel
	shutdownCh chan struct{}

	// Health checker
	healthChecker *healthcheck.HealthChecker

	// Optional Prometheus metrics handler
	prometheusHandler http.Handler

	// Sessions for tracking state
	sessionManager *session.Manager

	// If mcp server has been initialized
	IsServerInitialized bool
}

// NewTransparentProxy creates a new transparent proxy with optional middlewares.
func NewTransparentProxy(
	host string,
	port int,
	containerName string,
	targetURI string,
	prometheusHandler http.Handler,
	middlewares ...types.Middleware,
) *TransparentProxy {
	proxy := &TransparentProxy{
		host:              host,
		port:              port,
		containerName:     containerName,
		targetURI:         targetURI,
		middlewares:       middlewares,
		shutdownCh:        make(chan struct{}),
		prometheusHandler: prometheusHandler,
		sessionManager:    session.NewManager(30 * time.Minute),
	}

	// Create MCP pinger and health checker
	mcpPinger := NewMCPPinger(targetURI)
	proxy.healthChecker = healthcheck.NewHealthChecker("sse", mcpPinger)

	return proxy
}

type tracingTransport struct {
	base http.RoundTripper
	p    *TransparentProxy
}

func (t *tracingTransport) setServerInitialized() {
	if !t.p.IsServerInitialized {
		t.p.mutex.Lock()
		t.p.IsServerInitialized = true
		t.p.mutex.Unlock()
		logger.Infof("Server was initialized successfully for %s", t.p.containerName)
	}
}

func (t *tracingTransport) forward(req *http.Request) (*http.Response, error) {
	tr := t.base
	if tr == nil {
		tr = http.DefaultTransport
	}
	return tr.RoundTrip(req)
}

func (t *tracingTransport) watchEventStream(r io.Reader, w *io.PipeWriter) {
	defer w.Close()

	scanner := bufio.NewScanner(r)
	sessionRe := regexp.MustCompile(`sessionId=([0-9a-fA-F-]+)|\"sessionId\"\s*:\s*\"([^\"]+)\"`)

	for scanner.Scan() {
		line := scanner.Text()

		if m := sessionRe.FindStringSubmatch(line); m != nil {
			sid := m[1]
			if sid == "" {
				sid = m[2]
			}
			t.setServerInitialized()
			if _, ok := t.p.sessionManager.Get(sid); !ok {
				err := t.p.sessionManager.AddWithID(sid)
				if err != nil {
					logger.Errorf("Failed to create session from event stream: %v", err)
				}
			}
		}
	}

	_, err := io.Copy(io.Discard, r)
	if err != nil {
		logger.Errorf("Failed to copy event stream: %v", err)
	}
}

func (t *tracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	reqBody := readRequestBody(req)

	path := req.URL.Path
	isMCP := strings.HasPrefix(path, "/mcp")
	isJSON := strings.Contains(req.Header.Get("Content-Type"), "application/json")
	sawInitialize := false

	if isMCP && isJSON && len(reqBody) > 0 {
		sawInitialize = t.detectInitialize(reqBody)
	}

	resp, err := t.forward(req)
	if err != nil {
		logger.Errorf("Failed to forward request: %v", err)
		return nil, err
	}

	if resp.StatusCode == http.StatusOK {
		// check if we saw a valid mcp header
		ct := resp.Header.Get("Mcp-Session-Id")
		if ct != "" {
			logger.Infof("Detected Mcp-Session-Id header: %s", ct)
			if _, ok := t.p.sessionManager.Get(ct); !ok {
				if err := t.p.sessionManager.AddWithID(ct); err != nil {
					logger.Errorf("Failed to create session from header %s: %v", ct, err)
				}
			}
			t.setServerInitialized()
			return resp, nil
		}
		// status was ok and we saw an initialize call
		if sawInitialize && !t.p.IsServerInitialized {
			t.setServerInitialized()
			return resp, nil
		}
		ct = resp.Header.Get("Content-Type")
		mediaType, _, _ := mime.ParseMediaType(ct)
		if mediaType == "text/event-stream" {
			originalBody := resp.Body
			pr, pw := io.Pipe()
			tee := io.TeeReader(originalBody, pw)
			resp.Body = pr

			go t.watchEventStream(tee, pw)
		}
	}

	return resp, nil
}

func readRequestBody(req *http.Request) []byte {
	reqBody := []byte{}
	if req.Body != nil {
		buf, err := io.ReadAll(req.Body)
		if err != nil {
			logger.Errorf("Failed to read request body: %v", err)
		} else {
			reqBody = buf
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	return reqBody
}

func (t *tracingTransport) detectInitialize(body []byte) bool {
	var rpc struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		logger.Errorf("Failed to parse JSON-RPC body: %v", err)
		return false
	}
	if rpc.Method == "initialize" {
		logger.Infof("Detected initialize method call for %s", t.p.containerName)
		return true
	}
	return false
}

// Start starts the transparent proxy.
func (p *TransparentProxy) Start(ctx context.Context) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Parse the target URI
	targetURL, err := url.Parse(p.targetURI)
	if err != nil {
		return fmt.Errorf("failed to parse target URI: %w", err)
	}

	// Create a reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = &tracingTransport{base: http.DefaultTransport, p: p}

	// Create a handler that logs requests
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Infof("Transparent proxy: %s %s -> %s", r.Method, r.URL.Path, targetURL)
		proxy.ServeHTTP(w, r)
	})

	// Create a mux to handle both proxy and health endpoints
	mux := http.NewServeMux()

	// Apply middleware chain in reverse order (last middleware is applied first)
	var finalHandler http.Handler = handler
	for i := len(p.middlewares) - 1; i >= 0; i-- {
		finalHandler = p.middlewares[i](finalHandler)
		logger.Infof("Applied middleware %d\n", i+1)
	}

	// Add the proxy handler for all paths except /health
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			// Health endpoint should not go through proxy
			http.NotFound(w, r)
			return
		}
		finalHandler.ServeHTTP(w, r)
	})

	// Add health check endpoint (no middlewares)
	mux.Handle("/health", p.healthChecker)

	// Add Prometheus metrics endpoint if handler is provided (no middlewares)
	if p.prometheusHandler != nil {
		mux.Handle("/metrics", p.prometheusHandler)
		logger.Info("Prometheus metrics endpoint enabled at /metrics")
	}

	// Create the server
	p.server = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", p.host, p.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	// Start the server in a goroutine
	go func() {
		logger.Infof("Transparent proxy started for container %s on %s:%d -> %s",
			p.containerName, p.host, p.port, p.targetURI)

		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Transparent proxy error: %v", err)
		}
	}()

	// Start health-check monitoring
	go p.monitorHealth(ctx)

	return nil
}

func (p *TransparentProxy) monitorHealth(parentCtx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-parentCtx.Done():
			logger.Infof("Context cancelled, stopping health monitor for %s", p.containerName)
			return
		case <-p.shutdownCh:
			logger.Infof("Shutdown initiated, stopping health monitor for %s", p.containerName)
			return
		case <-ticker.C:
			// Perform health check only if mcp server has been initialized
			if p.IsServerInitialized {
				alive := p.healthChecker.CheckHealth(parentCtx)
				if alive.Status != healthcheck.StatusHealthy {
					logger.Infof("Health check failed for %s; initiating proxy shutdown", p.containerName)
					if err := p.Stop(parentCtx); err != nil {
						logger.Errorf("Failed to stop proxy for %s: %v", p.containerName, err)
					}
					return
				}
			} else {
				logger.Infof("MCP server not initialized yet, skipping health check for %s", p.containerName)
			}
		}
	}
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
func (*TransparentProxy) GetMessageChannel() chan jsonrpc2.Message {
	return nil
}

// SendMessageToDestination sends a message to the destination.
// This is not used in the TransparentProxy implementation as it forwards HTTP requests directly.
func (*TransparentProxy) SendMessageToDestination(_ jsonrpc2.Message) error {
	return fmt.Errorf("SendMessageToDestination not implemented for TransparentProxy")
}

// ForwardResponseToClients forwards a response from the destination to clients.
// This is not used in the TransparentProxy implementation as it forwards HTTP requests directly.
func (*TransparentProxy) ForwardResponseToClients(_ context.Context, _ jsonrpc2.Message) error {
	return fmt.Errorf("ForwardResponseToClients not implemented for TransparentProxy")
}

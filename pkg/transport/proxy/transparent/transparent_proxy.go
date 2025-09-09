// Package transparent provides a transparent HTTP proxy implementation
// that forwards requests to a destination without modifying them.
package transparent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
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
	middlewares []types.MiddlewareFunction

	// Mutex for protecting shared state
	mutex sync.Mutex

	// Shutdown channel
	shutdownCh chan struct{}

	// Health checker
	healthChecker *healthcheck.HealthChecker

	// Optional Prometheus metrics handler
	prometheusHandler http.Handler

	// Optional auth info handler
	authInfoHandler http.Handler

	// Sessions for tracking state
	sessionManager *session.Manager

	// If mcp server has been initialized
	IsServerInitialized bool

	// Listener for the HTTP server
	listener net.Listener

	// Whether the target URI is remote
	isRemote bool

	// Transport type (sse, streamable-http)
	transportType string
}

// NewTransparentProxy creates a new transparent proxy with optional middlewares.
func NewTransparentProxy(
	host string,
	port int,
	containerName string,
	targetURI string,
	prometheusHandler http.Handler,
	authInfoHandler http.Handler,
	enableHealthCheck bool,
	isRemote bool,
	transportType string,
	middlewares ...types.MiddlewareFunction,
) *TransparentProxy {
	proxy := &TransparentProxy{
		host:              host,
		port:              port,
		containerName:     containerName,
		targetURI:         targetURI,
		middlewares:       middlewares,
		shutdownCh:        make(chan struct{}),
		prometheusHandler: prometheusHandler,
		authInfoHandler:   authInfoHandler,
		sessionManager:    session.NewManager(session.DefaultSessionTTL, session.NewProxySession),
		isRemote:          isRemote,
		transportType:     transportType,
	}

	// Create MCP pinger and health checker only if enabled
	if enableHealthCheck {
		mcpPinger := NewMCPPinger(targetURI)
		proxy.healthChecker = healthcheck.NewHealthChecker("sse", mcpPinger)
	}

	return proxy
}

type tracingTransport struct {
	base http.RoundTripper
	p    *TransparentProxy
}

func (p *TransparentProxy) setServerInitialized() {
	if !p.IsServerInitialized {
		p.mutex.Lock()
		p.IsServerInitialized = true
		p.mutex.Unlock()
		logger.Infof("Server was initialized successfully for %s", p.containerName)
	}
}

func (t *tracingTransport) forward(req *http.Request) (*http.Response, error) {
	tr := t.base
	if tr == nil {
		tr = http.DefaultTransport
	}
	return tr.RoundTrip(req)
}

// nolint:gocyclo // This function handles multiple request types and is complex by design
func (t *tracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.p.isRemote {
		// In case of remote servers, req.Host is set to the proxy host (localhost) which may cause 403 error,
		// so we need to set it to the target URI host
		if req.URL.Host != req.Host {
			req.Host = req.URL.Host
		}
	}

	reqBody := readRequestBody(req)

	// thv proxy does not provide the transport type, so we need to detect it from the request
	path := req.URL.Path
	isMCP := strings.HasPrefix(path, "/mcp")
	isJSON := strings.Contains(req.Header.Get("Content-Type"), "application/json")
	sawInitialize := false

	if len(reqBody) > 0 &&
		((isMCP && isJSON) ||
			t.p.transportType == types.TransportTypeStreamableHTTP.String()) {
		sawInitialize = t.detectInitialize(reqBody)
	}

	resp, err := t.forward(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// Expected during shutdown or client disconnect—silently ignore
			return nil, err
		}
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
			t.p.setServerInitialized()
			return resp, nil
		}
		// status was ok and we saw an initialize call
		if sawInitialize && !t.p.IsServerInitialized {
			t.p.setServerInitialized()
			return resp, nil
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

var sessionRe = regexp.MustCompile(`sessionId=([0-9A-Fa-f-]+)|"sessionId"\s*:\s*"([^"]+)"`)

func (p *TransparentProxy) modifyForSessionID(resp *http.Response) error {
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType != "text/event-stream" {
		return nil
	}

	pr, pw := io.Pipe()
	originalBody := resp.Body
	resp.Body = pr

	// NOTE: it would be better to have a proper function instead of a goroutine, as this
	// makes it harder to debug and test.
	go func() {
		defer pw.Close()
		scanner := bufio.NewScanner(originalBody)
		// NOTE: The following line mitigates the issue of the response body being too large.
		// By default, the maximum token size of the scanner is 64KB, which is too small in
		// the case of e.g. images. This raises the limit to 1MB. This is a workaround, and
		// not a proper fix.
		scanner.Buffer(make([]byte, 0, 1024), 1024*1024*1)
		found := false

		for scanner.Scan() {
			line := scanner.Bytes()
			if !found {
				if m := sessionRe.FindSubmatch(line); m != nil {
					sid := string(m[1])
					if sid == "" {
						sid = string(m[2])
					}
					p.setServerInitialized()
					err := p.sessionManager.AddWithID(sid)
					if err != nil {
						logger.Errorf("Failed to create session from SSE line: %v", err)
					}
					found = true
				}
			}
			if _, err := pw.Write(append(line, '\n')); err != nil {
				return
			}
		}

		// NOTE: this line is always necessary since scanner.Scan() will return false
		// in case of an error.
		if err := scanner.Err(); err != nil {
			logger.Errorf("Failed to scan response body: %v", err)
		}

		_, err := io.Copy(pw, originalBody)
		if err != nil && err != io.EOF {
			logger.Errorf("Failed to copy response body: %v", err)
		}
	}()

	return nil
}

// Start starts the transparent proxy.
// nolint:gocyclo // This function handles multiple startup scenarios and is complex by design
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
	proxy.FlushInterval = -1
	proxy.Transport = &tracingTransport{base: http.DefaultTransport, p: p}
	proxy.ModifyResponse = func(resp *http.Response) error {
		return p.modifyForSessionID(resp)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	// Create a mux to handle both proxy and health endpoints
	mux := http.NewServeMux()

	// Apply middleware chain in reverse order (last middleware is applied first)
	var finalHandler http.Handler = handler
	for i := len(p.middlewares) - 1; i >= 0; i-- {
		finalHandler = p.middlewares[i](finalHandler)
		// TODO: we should really log the middleware name here
		logger.Infof("Applied middleware %d", i+1)
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

	// Add health check endpoint (no middlewares) only if health checker is enabled
	if p.healthChecker != nil {
		mux.Handle("/health", p.healthChecker)
	}

	// Add Prometheus metrics endpoint if handler is provided (no middlewares)
	if p.prometheusHandler != nil {
		mux.Handle("/metrics", p.prometheusHandler)
		logger.Info("Prometheus metrics endpoint enabled at /metrics")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", p.host, p.port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	p.listener = ln

	// Add .well-known path space handler if auth info handler is provided (no middlewares)
	if p.authInfoHandler != nil {
		// Create a handler that routes .well-known requests
		wellKnownHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/.well-known/oauth-protected-resource":
				p.authInfoHandler.ServeHTTP(w, r)
			default:
				http.NotFound(w, r)
			}
		})
		mux.Handle("/.well-known/", wellKnownHandler)
		logger.Info("Well-known discovery endpoints enabled at /.well-known/ (no middlewares)")
	}

	// Create the server
	p.server = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", p.host, p.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	go func() {
		err := p.server.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Op == "accept" {
				// Expected when listener is closed—silently return
				return
			}
			logger.Errorf("Transparent proxy error: %v", err)
		}
	}()
	// Start health-check monitoring only if health checker is enabled
	if p.healthChecker != nil {
		go p.monitorHealth(ctx)
	}

	return nil
}

// CloseListener closes the listener for the transparent proxy.
func (p *TransparentProxy) CloseListener() error {
	if p.listener != nil {
		return p.listener.Close()
	}
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
		err := p.server.Shutdown(ctx)
		if err != nil && err != http.ErrServerClosed && err != context.DeadlineExceeded {
			logger.Warnf("Error during proxy shutdown: %v", err)
			return err
		}
		logger.Infof("Server for %s stopped successfully", p.containerName)
		p.server = nil
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

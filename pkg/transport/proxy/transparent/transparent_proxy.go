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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
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
	host      string
	port      int
	targetURI string

	// HTTP server
	server *http.Server

	// Middleware chain
	middlewares []types.NamedMiddleware

	// Mutex for protecting shared state
	mutex sync.Mutex

	// Track if Stop() has been called
	stopped bool

	// Shutdown channel
	shutdownCh chan struct{}

	// Health checker
	healthChecker *healthcheck.HealthChecker

	// Optional Prometheus metrics handler
	prometheusHandler http.Handler

	// Optional auth info handler
	authInfoHandler http.Handler

	// Optional embedded OAuth authorization server handler
	authServerMux http.Handler

	// Optional embedded OAuth authorization server well-known endpoints handler
	authServerWellKnownMux http.Handler

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

	// Callback when health check fails (for remote servers)
	onHealthCheckFailed types.HealthCheckFailedCallback
}

// NewTransparentProxy creates a new transparent proxy with optional middlewares.
func NewTransparentProxy(
	host string,
	port int,
	targetURI string,
	prometheusHandler http.Handler,
	authInfoHandler http.Handler,
	authServerMux http.Handler,
	authServerWellKnownMux http.Handler,
	enableHealthCheck bool,
	isRemote bool,
	transportType string,
	onHealthCheckFailed types.HealthCheckFailedCallback,
	middlewares ...types.NamedMiddleware,
) *TransparentProxy {
	proxy := &TransparentProxy{
		host:                   host,
		port:                   port,
		targetURI:              targetURI,
		middlewares:            middlewares,
		shutdownCh:             make(chan struct{}),
		prometheusHandler:      prometheusHandler,
		authInfoHandler:        authInfoHandler,
		authServerMux:          authServerMux,
		authServerWellKnownMux: authServerWellKnownMux,
		sessionManager:         session.NewManager(session.DefaultSessionTTL, session.NewProxySession),
		isRemote:               isRemote,
		transportType:          transportType,
		onHealthCheckFailed:    onHealthCheckFailed,
	}

	// Create health checker always for Kubernetes probes
	var mcpPinger healthcheck.MCPPinger
	if enableHealthCheck {
		mcpPinger = NewMCPPinger(targetURI)
	}
	proxy.healthChecker = healthcheck.NewHealthChecker(transportType, mcpPinger)

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
		logger.Infof("Server was initialized successfully for %s", p.targetURI)
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
		logger.Infof("Detected initialize method call for %s", t.p.targetURI)
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

	// Store the original director
	originalDirector := proxy.Director

	// Override director to inject trace propagation headers
	proxy.Director = func(req *http.Request) {
		// Apply original director logic first
		originalDirector(req)

		// Inject OpenTelemetry trace propagation headers for downstream tracing
		if req.Context() != nil {
			otel.GetTextMapPropagator().Inject(req.Context(), propagation.HeaderCarrier(req.Header))
		}
	}

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
		finalHandler = p.middlewares[i].Function(finalHandler)
		logger.Infof("Applied middleware: %s", p.middlewares[i].Name)
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

	// Add .well-known discovery endpoints (no middlewares)
	// Both auth server and protected resource metadata can be served simultaneously.
	if wellKnownHandler := p.createWellKnownHandler(); wellKnownHandler != nil {
		mux.Handle("/.well-known/", wellKnownHandler)
	}

	// Add embedded OAuth authorization server endpoints if provided (no middlewares)
	if p.authServerMux != nil {
		mux.Handle("/oauth/", p.authServerMux)
		mux.Handle("/oauth2/", p.authServerMux) // DCR endpoint at /oauth2/register
		logger.Info("Embedded OAuth authorization server enabled at /oauth/ and /oauth2/")
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", p.host, p.port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	p.listener = ln

	// Debug middleware to log all incoming requests (temporary for debugging ngrok rewrite)
	debugHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Infof("DEBUG REQUEST: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		mux.ServeHTTP(w, r)
	})

	// Create the server
	p.server = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", p.host, p.port),
		Handler:           debugHandler,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	// Capture server in local variable to avoid race with Stop()
	server := p.server
	go func() {
		err := server.Serve(ln)
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
			logger.Infof("Context cancelled, stopping health monitor for %s", p.targetURI)
			return
		case <-p.shutdownCh:
			logger.Infof("Shutdown initiated, stopping health monitor for %s", p.targetURI)
			return
		case <-ticker.C:
			// Perform health check only if mcp server has been initialized
			if p.IsServerInitialized {
				alive := p.healthChecker.CheckHealth(parentCtx)
				if alive.Status != healthcheck.StatusHealthy {
					logger.Infof("Health check failed for %s; initiating proxy shutdown", p.targetURI)
					// Notify the runner about health check failure so it can update workload status
					if p.onHealthCheckFailed != nil {
						p.onHealthCheckFailed()
					}
					if err := p.Stop(parentCtx); err != nil {
						logger.Errorf("Failed to stop proxy for %s: %v", p.targetURI, err)
					}
					return
				}
			} else {
				logger.Infof("MCP server not initialized yet, skipping health check for %s", p.targetURI)
			}
		}
	}
}

// createWellKnownHandler creates a composite handler for all /.well-known/* endpoints.
// It routes requests to the appropriate handler based on the path:
//   - /.well-known/openid-configuration, /.well-known/jwks.json -> authServerWellKnownMux
//   - /.well-known/oauth-protected-resource/* -> auth.NewWellKnownHandler(authInfoHandler)
//
// Returns nil if neither handler is available.
func (p *TransparentProxy) createWellKnownHandler() http.Handler {
	protectedResourceHandler := auth.NewWellKnownHandler(p.authInfoHandler)

	hasAuthServer := p.authServerWellKnownMux != nil
	hasProtectedResource := protectedResourceHandler != nil

	// Case 1: Neither handler is available
	if !hasAuthServer && !hasProtectedResource {
		return nil
	}

	// Case 2: Only auth server well-known endpoints
	if hasAuthServer && !hasProtectedResource {
		logger.Info("Embedded OAuth authorization server well-known endpoints enabled at /.well-known/")
		return p.authServerWellKnownMux
	}

	// Case 3: Only protected resource metadata
	if !hasAuthServer && hasProtectedResource {
		logger.Info("RFC 9728 OAuth discovery endpoints enabled at /.well-known/")
		return protectedResourceHandler
	}

	// Case 4: Both handlers are available - create composite handler
	logger.Info("Both embedded OAuth authorization server and RFC 9728 OAuth discovery endpoints enabled at /.well-known/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Route based on path prefix
		// Auth server handles: /.well-known/openid-configuration, /.well-known/jwks.json
		// Protected resource handler handles: /.well-known/oauth-protected-resource/*
		if strings.HasPrefix(r.URL.Path, auth.WellKnownOAuthResourcePath) {
			protectedResourceHandler.ServeHTTP(w, r)
			return
		}
		// All other .well-known paths go to auth server
		p.authServerWellKnownMux.ServeHTTP(w, r)
	})
}

// Stop stops the transparent proxy.
func (p *TransparentProxy) Stop(ctx context.Context) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Check if already stopped
	if p.stopped {
		logger.Debugf("Proxy for %s is already stopped, skipping", p.targetURI)
		return nil
	}

	// Mark as stopped before closing channel
	p.stopped = true

	// Signal shutdown
	close(p.shutdownCh)

	// Stop the HTTP server
	if p.server != nil {
		err := p.server.Shutdown(ctx)
		if err != nil && err != http.ErrServerClosed && err != context.DeadlineExceeded {
			logger.Warnf("Error during proxy shutdown: %v", err)
			return err
		}
		logger.Infof("Server for %s stopped successfully", p.targetURI)
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

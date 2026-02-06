// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package transparent provides a transparent HTTP proxy implementation
// that forwards requests to a destination without modifying them.
package transparent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/healthcheck"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/proxy/socket"
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

	// prefixHandlers is a map of path prefixes to HTTP handlers
	// mounted before the catch-all proxy handler
	prefixHandlers map[string]http.Handler

	// Sessions for tracking state
	sessionManager *session.Manager

	// If mcp server has been initialized (atomic access)
	isServerInitialized atomic.Bool

	// Listener for the HTTP server
	listener net.Listener

	// Whether the target URI is remote
	isRemote bool

	// Transport type (sse, streamable-http)
	transportType string

	// Callback when health check fails (for remote servers)
	onHealthCheckFailed types.HealthCheckFailedCallback

	// Callback when 401 Unauthorized response is received (for bearer token authentication)
	onUnauthorizedResponse types.UnauthorizedResponseCallback

	// Response processor for transport-specific logic
	responseProcessor ResponseProcessor

	// Deprecated: SSE endpoint URL rewriting configuration (moved to SSEResponseProcessor)
	// endpointPrefix is an explicit prefix to prepend to SSE endpoint URLs
	endpointPrefix string

	// Deprecated: trustProxyHeaders indicates whether to trust X-Forwarded-* headers (moved to SSEResponseProcessor)
	trustProxyHeaders bool

	// Health check interval (default: 10 seconds)
	healthCheckInterval time.Duration

	// Health check retry delay (default: 5 seconds)
	healthCheckRetryDelay time.Duration

	// Health check ping timeout (default: 5 seconds)
	healthCheckPingTimeout time.Duration
}

const (
	// DefaultHealthCheckInterval is the default interval for health checks
	DefaultHealthCheckInterval = 10 * time.Second

	// DefaultHealthCheckRetryDelay is the default delay between retry attempts
	DefaultHealthCheckRetryDelay = 5 * time.Second

	// HealthCheckIntervalEnvVar is the environment variable name for configuring health check interval.
	// This is primarily useful for testing with shorter intervals.
	HealthCheckIntervalEnvVar = "TOOLHIVE_HEALTH_CHECK_INTERVAL"
)

// Option is a functional option for configuring TransparentProxy
type Option func(*TransparentProxy)

// withHealthCheckInterval sets the health check interval.
// This is primarily useful for testing with shorter intervals.
// Ignores non-positive intervals; default will be used.
func withHealthCheckInterval(interval time.Duration) Option {
	return func(p *TransparentProxy) {
		if interval > 0 {
			p.healthCheckInterval = interval
		}
	}
}

// withHealthCheckRetryDelay sets the health check retry delay.
// This is primarily useful for testing with shorter delays.
// Ignores non-positive delays; default will be used.
func withHealthCheckRetryDelay(delay time.Duration) Option {
	return func(p *TransparentProxy) {
		if delay > 0 {
			p.healthCheckRetryDelay = delay
		}
	}
}

// withHealthCheckPingTimeout sets the health check ping timeout.
// This is primarily useful for testing with shorter timeouts.
// Ignores non-positive timeouts; default will be used.
func withHealthCheckPingTimeout(timeout time.Duration) Option {
	return func(p *TransparentProxy) {
		if timeout > 0 {
			p.healthCheckPingTimeout = timeout
		}
	}
}

// NewTransparentProxy creates a new transparent proxy with optional middlewares and configuration options.

// NewTransparentProxy creates a new transparent proxy with optional middlewares.
// The endpointPrefix parameter specifies an explicit prefix to prepend to SSE endpoint URLs.
// The trustProxyHeaders parameter indicates whether to trust X-Forwarded-* headers from reverse proxies.
// The prefixHandlers parameter is a map of path prefixes to HTTP handlers mounted before the catch-all proxy handler.
func NewTransparentProxy(
	host string,
	port int,
	targetURI string,
	prometheusHandler http.Handler,
	authInfoHandler http.Handler,
	prefixHandlers map[string]http.Handler,
	enableHealthCheck bool,
	isRemote bool,
	transportType string,
	onHealthCheckFailed types.HealthCheckFailedCallback,
	onUnauthorizedResponse types.UnauthorizedResponseCallback,
	endpointPrefix string,
	trustProxyHeaders bool,
	middlewares ...types.NamedMiddleware,
) *TransparentProxy {
	return newTransparentProxyWithOptions(
		host,
		port,
		targetURI,
		prometheusHandler,
		authInfoHandler,
		prefixHandlers,
		enableHealthCheck,
		isRemote,
		transportType,
		onHealthCheckFailed,
		onUnauthorizedResponse,
		endpointPrefix,
		trustProxyHeaders,
		middlewares,
	)
}

// getHealthCheckInterval returns the health check interval to use.
// Uses TOOLHIVE_HEALTH_CHECK_INTERVAL environment variable if set and valid,
// otherwise returns the default interval.
func getHealthCheckInterval() time.Duration {
	if val := os.Getenv(HealthCheckIntervalEnvVar); val != "" {
		if d, err := time.ParseDuration(val); err == nil && d > 0 {
			return d
		}
	}
	return DefaultHealthCheckInterval
}

// newTransparentProxyWithOptions creates a new transparent proxy with optional configuration.
func newTransparentProxyWithOptions(
	host string,
	port int,
	targetURI string,
	prometheusHandler http.Handler,
	authInfoHandler http.Handler,
	prefixHandlers map[string]http.Handler,
	enableHealthCheck bool,
	isRemote bool,
	transportType string,
	onHealthCheckFailed types.HealthCheckFailedCallback,
	onUnauthorizedResponse types.UnauthorizedResponseCallback,
	endpointPrefix string,
	trustProxyHeaders bool,
	middlewares []types.NamedMiddleware,
	options ...Option,
) *TransparentProxy {
	proxy := &TransparentProxy{
		host:                   host,
		port:                   port,
		targetURI:              targetURI,
		middlewares:            middlewares,
		shutdownCh:             make(chan struct{}),
		prometheusHandler:      prometheusHandler,
		authInfoHandler:        authInfoHandler,
		prefixHandlers:         prefixHandlers,
		sessionManager:         session.NewManager(session.DefaultSessionTTL, session.NewProxySession),
		isRemote:               isRemote,
		transportType:          transportType,
		onHealthCheckFailed:    onHealthCheckFailed,
		onUnauthorizedResponse: onUnauthorizedResponse,
		endpointPrefix:         endpointPrefix,
		trustProxyHeaders:      trustProxyHeaders,
		healthCheckInterval:    getHealthCheckInterval(),
		healthCheckRetryDelay:  DefaultHealthCheckRetryDelay,
		healthCheckPingTimeout: DefaultPingerTimeout,
	}

	// Apply options
	for _, opt := range options {
		opt(proxy)
	}

	// Create appropriate response processor based on transport type
	proxy.responseProcessor = createResponseProcessor(
		transportType,
		proxy,
		endpointPrefix,
		trustProxyHeaders,
	)

	// Create health checker always for Kubernetes probes
	var mcpPinger healthcheck.MCPPinger
	if enableHealthCheck {
		pingTimeout := proxy.healthCheckPingTimeout
		if pingTimeout == 0 {
			pingTimeout = DefaultPingerTimeout
		}
		mcpPinger = NewMCPPingerWithTimeout(targetURI, pingTimeout)
	}
	proxy.healthChecker = healthcheck.NewHealthChecker(transportType, mcpPinger)

	return proxy
}

type tracingTransport struct {
	base http.RoundTripper
	p    *TransparentProxy
}

func (p *TransparentProxy) setServerInitialized() {
	if p.isServerInitialized.CompareAndSwap(false, true) {
		logger.Debugf("Server was initialized successfully for %s", p.targetURI)
	}
}

// serverInitialized returns whether the server has been initialized (thread-safe)
func (p *TransparentProxy) serverInitialized() bool {
	return p.isServerInitialized.Load()
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
	// Always rewrite Host header to match the target URL to avoid "Invalid Host" errors
	// from backends with strict host validation (e.g., Django ALLOWED_HOSTS, FastAPI TrustedHostMiddleware).
	// Without this, the backend receives the proxy's Host header (e.g., Kubernetes service DNS name)
	// instead of its own hostname, causing validation failures.
	// See: https://github.com/stacklok/stacklok-epics/issues/231
	if req.URL.Host != req.Host {
		req.Host = req.URL.Host
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

	// Check for 401 Unauthorized response (bearer token authentication failure)
	if resp.StatusCode == http.StatusUnauthorized {
		logger.Debugf("Received 401 Unauthorized response from %s, bearer token may be invalid", t.p.targetURI)
		if t.p.onUnauthorizedResponse != nil {
			t.p.onUnauthorizedResponse()
		}
	}

	if resp.StatusCode == http.StatusOK {
		// check if we saw a valid mcp header
		ct := resp.Header.Get("Mcp-Session-Id")
		if ct != "" {
			logger.Debugf("Detected Mcp-Session-Id header: %s", ct)
			if _, ok := t.p.sessionManager.Get(ct); !ok {
				if err := t.p.sessionManager.AddWithID(ct); err != nil {
					logger.Errorf("Failed to create session from header %s: %v", ct, err)
				}
			}
			t.p.setServerInitialized()
			return resp, nil
		}
		// status was ok and we saw an initialize call
		if sawInitialize && !t.p.serverInitialized() {
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
			logger.Warnf("Failed to read request body: %v", err)
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
		logger.Debugf("Failed to parse JSON-RPC body: %v", err)
		return false
	}
	if rpc.Method == "initialize" {
		logger.Debugf("Detected initialize method call for %s", t.p.targetURI)
		return true
	}
	return false
}

// modifyResponse modifies HTTP responses based on transport-specific requirements.
// Delegates to the appropriate ResponseProcessor based on transport type.
func (p *TransparentProxy) modifyResponse(resp *http.Response) error {
	return p.responseProcessor.ProcessResponse(resp)
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
		return p.modifyResponse(resp)
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
		logger.Debugf("Applied middleware: %s", p.middlewares[i].Name)
	}

	// 1. Mount prefix handlers first (user-specified, most specific paths)
	// These are registered first but Go's ServeMux longest-match routing ensures
	// more specific paths take precedence regardless of registration order.
	for prefix, prefixHandler := range p.prefixHandlers {
		mux.Handle(prefix, prefixHandler)
		logger.Debugf("Mounted prefix handler at %s", prefix)
	}

	// 2. Mount health check endpoint if enabled, otherwise return 404
	// (prevents /health from being proxied to the backend)
	if p.healthChecker != nil {
		mux.Handle("/health", p.healthChecker)
	} else {
		mux.HandleFunc("/health", http.NotFound)
	}

	// 3. Mount Prometheus metrics endpoint if handler is provided (no middlewares)
	if p.prometheusHandler != nil {
		mux.Handle("/metrics", p.prometheusHandler)
		logger.Debug("Prometheus metrics endpoint enabled at /metrics")
	}

	// 4. Mount RFC 9728 OAuth Protected Resource discovery endpoint (no middlewares)
	// Note: This is DIFFERENT from the auth server's /.well-known/oauth-authorization-server
	// We mount at specific paths to allow prefix handlers to register other well-known paths.
	if wellKnownHandler := auth.NewWellKnownHandler(p.authInfoHandler); wellKnownHandler != nil {
		mux.Handle("/.well-known/oauth-protected-resource", wellKnownHandler)
		mux.Handle("/.well-known/oauth-protected-resource/", wellKnownHandler)
		logger.Debug("RFC 9728 OAuth discovery endpoint enabled at /.well-known/oauth-protected-resource")
	}

	// 5. Catch-all proxy handler (least specific - ServeMux routing handles precedence)
	// Note: No manual path checking needed - ServeMux longest-match routing ensures
	// more specific paths registered above take precedence over this catch-all
	mux.Handle("/", finalHandler)

	// Use ListenConfig with SO_REUSEADDR to allow port reuse after unclean shutdown
	// (e.g., after laptop sleep where zombie processes may hold ports)
	lc := socket.ListenConfig()
	ln, err := lc.Listen(context.Background(), "tcp", fmt.Sprintf("%s:%d", p.host, p.port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	p.listener = ln

	// Create the server
	p.server = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", p.host, p.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	// Capture server in local variable to avoid race with Stop()
	server := p.server
	go func() {
		err := server.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// healthCheckRetryConfig holds retry configuration for health checks.
// These values are designed to handle transient network issues like
// VPN/firewall idle connection timeouts (commonly 5-10 minutes).
const (
	// healthCheckRetryCount is the number of consecutive failures before marking unhealthy.
	// This prevents immediate shutdown on transient network issues.
	healthCheckRetryCount = 3
)

// performHealthCheckRetry performs a retry health check after a delay
// Returns true if the retry was successful (health check recovered), false otherwise
func (p *TransparentProxy) performHealthCheckRetry(ctx context.Context) bool {
	retryDelay := p.healthCheckRetryDelay
	if retryDelay == 0 {
		retryDelay = DefaultHealthCheckRetryDelay
	}

	retryTimer := time.NewTimer(retryDelay)
	defer retryTimer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-p.shutdownCh:
		return false
	case <-retryTimer.C:
		retryAlive := p.healthChecker.CheckHealth(ctx)
		if retryAlive.Status == healthcheck.StatusHealthy {
			logger.Debugf("Health check recovered for %s after retry", p.targetURI)
			return true
		}
		return false
	}
}

// handleHealthCheckFailure handles a failed health check, including retry logic and shutdown.
// Returns (updatedFailureCount, shouldContinue) - true if monitoring should continue, false if it should stop.
func (p *TransparentProxy) handleHealthCheckFailure(
	ctx context.Context,
	consecutiveFailures int,
	status healthcheck.HealthStatus,
) (int, bool) {
	consecutiveFailures++
	logger.Warnf("Health check failed for %s (attempt %d/%d): status=%s",
		p.targetURI, consecutiveFailures, healthCheckRetryCount, status)

	if consecutiveFailures < healthCheckRetryCount {
		if p.performHealthCheckRetry(ctx) {
			consecutiveFailures = 0
		}
		return consecutiveFailures, true
	}

	// All retries exhausted, initiate shutdown
	logger.Errorf("Health check failed for %s after %d consecutive attempts; initiating proxy shutdown",
		p.targetURI, healthCheckRetryCount)
	if p.onHealthCheckFailed != nil {
		p.onHealthCheckFailed()
	}
	if err := p.Stop(ctx); err != nil {
		logger.Errorf("Failed to stop proxy for %s: %v", p.targetURI, err)
	}
	return consecutiveFailures, false
}

func (p *TransparentProxy) monitorHealth(parentCtx context.Context) {
	interval := p.healthCheckInterval
	if interval == 0 {
		interval = DefaultHealthCheckInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	consecutiveFailures := 0

	for {
		select {
		case <-parentCtx.Done():
			logger.Debugf("Context cancelled, stopping health monitor for %s", p.targetURI)
			return
		case <-p.shutdownCh:
			logger.Debugf("Shutdown initiated, stopping health monitor for %s", p.targetURI)
			return
		case <-ticker.C:
			if !p.serverInitialized() {
				logger.Debugf("MCP server not initialized yet, skipping health check for %s", p.targetURI)
				continue
			}

			alive := p.healthChecker.CheckHealth(parentCtx)
			if alive.Status != healthcheck.StatusHealthy {
				var shouldContinue bool
				consecutiveFailures, shouldContinue = p.handleHealthCheckFailure(parentCtx, consecutiveFailures, alive.Status)
				if !shouldContinue {
					return
				}
				continue
			}

			// Reset failure count on successful health check
			if consecutiveFailures > 0 {
				logger.Debugf("Health check recovered for %s after %d failures", p.targetURI, consecutiveFailures)
			}
			consecutiveFailures = 0
		}
	}
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
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, context.DeadlineExceeded) {
			logger.Warnf("Error during proxy shutdown: %v", err)
			return err
		}
		logger.Debugf("Server for %s stopped successfully", p.targetURI)
		p.server = nil
	}

	return nil
}

// Address returns the address the proxy is listening on.
// Returns an empty string if the proxy is not running.
func (p *TransparentProxy) Address() string {
	if p.listener == nil {
		return ""
	}
	return p.listener.Addr().String()
}

// IsRunning checks if the proxy is running.
func (p *TransparentProxy) IsRunning() (bool, error) {
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

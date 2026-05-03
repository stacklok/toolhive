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
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/healthcheck"
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

	// stateless indicates the server is POST-only (no SSE/GET support)
	stateless bool

	// Callback when health check fails (for remote servers)
	onHealthCheckFailed types.HealthCheckFailedCallback

	// Callback when health check recovers after a failure (for remote servers)
	onHealthCheckRecovered types.HealthCheckRecoveredCallback

	// Callback when 401 Unauthorized response is received (for bearer token authentication)
	onUnauthorizedResponse types.UnauthorizedResponseCallback

	// Response processor for transport-specific logic
	responseProcessor ResponseProcessor

	// Deprecated: SSE endpoint URL rewriting configuration (moved to SSEResponseProcessor)
	// endpointPrefix is an explicit prefix to prepend to SSE endpoint URLs
	endpointPrefix string

	// remoteBasePath is the path prefix from the remote URL that must be prepended
	// to incoming request paths before forwarding. For example, if the remote URL is
	// https://mcp.asana.com/v2/mcp and a client sends to /mcp, the proxy must
	// forward to /v2/mcp. Without this, the path prefix is lost because the target
	// URI only contains the scheme and host.
	remoteBasePath string

	// remoteRawQuery holds the raw query string from the remote URL (e.g.,
	// "toolsets=core,alerting" from "https://mcp.example.com/mcp?toolsets=core,alerting").
	// When set, it is merged into every outbound request so query parameters
	// from the original registration URL are never silently dropped.
	remoteRawQuery string

	// Deprecated: trustProxyHeaders indicates whether to trust X-Forwarded-* headers (moved to SSEResponseProcessor)
	trustProxyHeaders bool

	// Health check interval (default: 10 seconds)
	healthCheckInterval time.Duration

	// Health check retry delay (default: 5 seconds)
	healthCheckRetryDelay time.Duration

	// Health check ping timeout (default: 5 seconds)
	healthCheckPingTimeout time.Duration

	// Health check failure threshold: consecutive failures before shutdown (default: 5)
	healthCheckFailureThreshold int

	// Shutdown timeout for graceful HTTP server shutdown (default: 30 seconds)
	shutdownTimeout time.Duration
}

const (
	// DefaultHealthCheckInterval is the default interval for health checks
	DefaultHealthCheckInterval = 10 * time.Second

	// DefaultHealthCheckRetryDelay is the default delay between retry attempts
	DefaultHealthCheckRetryDelay = 5 * time.Second

	// defaultShutdownTimeout is the maximum time to wait for graceful HTTP server
	// shutdown before force-closing connections.
	defaultShutdownTimeout = 30 * time.Second

	// defaultIdleTimeout is the maximum time to wait for the next request on a
	// keep-alive connection. Matches the value used by the vMCP server.
	defaultIdleTimeout = 120 * time.Second

	// HealthCheckIntervalEnvVar is the environment variable name for configuring health check interval.
	HealthCheckIntervalEnvVar = "TOOLHIVE_HEALTH_CHECK_INTERVAL"

	// sessionMetadataBackendURL is the session metadata key that stores the backend pod URL.
	// It is written on initialize and read in the Rewrite closure to route follow-up requests
	// to the same backend pod that handled the session's initialize request.
	sessionMetadataBackendURL = "backend_url"

	// sessionMetadataInitBody stores the raw JSON-RPC initialize request body.
	// It is used to transparently re-initialize a backend session when the pod that
	// originally handled initialize has been replaced (new IP or lost in-memory state).
	sessionMetadataInitBody = "init_body"

	// sessionMetadataBackendSID stores the backend's assigned Mcp-Session-Id when it
	// diverges from the client-facing session ID after a transparent re-initialization.
	// tracingTransport.RoundTrip rewrites the outbound Mcp-Session-Id header to this
	// value so the backend sees its own session ID while the client keeps its original one.
	sessionMetadataBackendSID = "backend_sid"

	// HealthCheckPingTimeoutEnvVar is the environment variable name for configuring health check ping timeout.
	HealthCheckPingTimeoutEnvVar = "TOOLHIVE_HEALTH_CHECK_PING_TIMEOUT"

	// HealthCheckRetryDelayEnvVar is the environment variable name for configuring health check retry delay.
	HealthCheckRetryDelayEnvVar = "TOOLHIVE_HEALTH_CHECK_RETRY_DELAY"

	// HealthCheckFailureThresholdEnvVar is the environment variable name for configuring
	// the number of consecutive health check failures before shutdown.
	HealthCheckFailureThresholdEnvVar = "TOOLHIVE_HEALTH_CHECK_FAILURE_THRESHOLD"

	// DefaultHealthCheckFailureThreshold is the default number of consecutive health check
	// failures before the proxy initiates shutdown.
	DefaultHealthCheckFailureThreshold = 5

	// maxRedirects is the maximum number of HTTP redirects to follow when
	// forwarding requests to a remote MCP server. Uses the same limit as
	// http.Client (10), but unlike http.Client the HTTP method is always
	// preserved (POST never becomes GET) because MCP uses JSON-RPC over POST.
	maxRedirects = 10
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

// WithRemoteBasePath sets the base path prefix from the remote URL.
// When set, incoming request paths are rewritten to include this prefix
// before forwarding to the remote server.
func WithRemoteBasePath(basePath string) Option {
	return func(p *TransparentProxy) {
		p.remoteBasePath = basePath
	}
}

// WithRemoteRawQuery sets the raw query string from the remote URL.
// When set, these query parameters are merged into every outbound request,
// ensuring query parameters from the original registration URL are always forwarded.
// Ignores empty strings; default (no query forwarding) will be used.
func WithRemoteRawQuery(rawQuery string) Option {
	return func(p *TransparentProxy) {
		if rawQuery != "" {
			p.remoteRawQuery = rawQuery
		}
	}
}

// WithStateless configures the proxy for stateless streamable-HTTP servers.
// In stateless mode, incoming GET and DELETE requests receive 405 Method Not Allowed
// instead of being forwarded, and health checks use POST ping instead of GET.
func WithStateless() Option {
	return func(p *TransparentProxy) {
		p.stateless = true
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

// withHealthCheckFailureThreshold sets the consecutive failure count before shutdown.
// This is primarily useful for testing with lower thresholds.
// Ignores non-positive values; default will be used.
func withHealthCheckFailureThreshold(threshold int) Option {
	return func(p *TransparentProxy) {
		if threshold > 0 {
			p.healthCheckFailureThreshold = threshold
		}
	}
}

// withShutdownTimeout sets the graceful shutdown timeout for the HTTP server.
// This is primarily useful for testing with shorter timeouts.
// Ignores non-positive timeouts; default will be used.
func withShutdownTimeout(timeout time.Duration) Option {
	return func(p *TransparentProxy) {
		if timeout > 0 {
			p.shutdownTimeout = timeout
		}
	}
}

// WithSessionStorage injects a custom storage backend into the session manager.
// When not provided, the proxy uses in-memory LocalStorage (single-replica default).
// Provide a Redis-backed storage for multi-replica deployments so all replicas
// share the same session store.
func WithSessionStorage(storage session.Storage) Option {
	return func(p *TransparentProxy) {
		if storage == nil {
			return
		}
		if p.sessionManager != nil {
			_ = p.sessionManager.Stop()
		}
		p.sessionManager = session.NewManagerWithStorage(
			session.DefaultSessionTTL,
			func(id string) session.Session { return session.NewProxySession(id) },
			storage,
		)
	}
}

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
	return NewTransparentProxyWithOptions(
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
		nil, // onHealthCheckRecovered
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
			slog.Debug("using custom health check interval", "interval", d)
			return d
		}
		slog.Warn("invalid health check interval, using default",
			"env_var", HealthCheckIntervalEnvVar, "value", val, "default", DefaultHealthCheckInterval)
	}
	return DefaultHealthCheckInterval
}

// getHealthCheckPingTimeout returns the health check ping timeout to use.
// Uses TOOLHIVE_HEALTH_CHECK_PING_TIMEOUT environment variable if set and valid,
// otherwise returns the default timeout.
func getHealthCheckPingTimeout() time.Duration {
	if val := os.Getenv(HealthCheckPingTimeoutEnvVar); val != "" {
		if d, err := time.ParseDuration(val); err == nil && d > 0 {
			slog.Debug("using custom health check ping timeout", "timeout", d)
			return d
		}
		slog.Warn("invalid health check ping timeout, using default",
			"env_var", HealthCheckPingTimeoutEnvVar, "value", val, "default", DefaultPingerTimeout)
	}
	return DefaultPingerTimeout
}

// getHealthCheckRetryDelay returns the health check retry delay to use.
// Uses TOOLHIVE_HEALTH_CHECK_RETRY_DELAY environment variable if set and valid,
// otherwise returns the default delay.
func getHealthCheckRetryDelay() time.Duration {
	if val := os.Getenv(HealthCheckRetryDelayEnvVar); val != "" {
		if d, err := time.ParseDuration(val); err == nil && d > 0 {
			slog.Debug("using custom health check retry delay", "delay", d)
			return d
		}
		slog.Warn("invalid health check retry delay, using default",
			"env_var", HealthCheckRetryDelayEnvVar, "value", val, "default", DefaultHealthCheckRetryDelay)
	}
	return DefaultHealthCheckRetryDelay
}

// getHealthCheckFailureThreshold returns the consecutive failure threshold.
// Uses TOOLHIVE_HEALTH_CHECK_FAILURE_THRESHOLD environment variable if set and valid,
// otherwise returns the default threshold.
func getHealthCheckFailureThreshold() int {
	if val := os.Getenv(HealthCheckFailureThresholdEnvVar); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			slog.Debug("using custom health check failure threshold", "threshold", n)
			return n
		}
		slog.Warn("invalid health check failure threshold, using default",
			"env_var", HealthCheckFailureThresholdEnvVar, "value", val, "default", DefaultHealthCheckFailureThreshold)
	}
	return DefaultHealthCheckFailureThreshold
}

// NewTransparentProxyWithOptions creates a new transparent proxy with optional configuration.
func NewTransparentProxyWithOptions(
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
	onHealthCheckRecovered types.HealthCheckRecoveredCallback,
	onUnauthorizedResponse types.UnauthorizedResponseCallback,
	endpointPrefix string,
	trustProxyHeaders bool,
	middlewares []types.NamedMiddleware,
	options ...Option,
) *TransparentProxy {
	proxy := &TransparentProxy{
		host:                        host,
		port:                        port,
		targetURI:                   targetURI,
		middlewares:                 middlewares,
		shutdownCh:                  make(chan struct{}),
		prometheusHandler:           prometheusHandler,
		authInfoHandler:             authInfoHandler,
		prefixHandlers:              prefixHandlers,
		sessionManager:              session.NewManager(session.DefaultSessionTTL, session.NewProxySession),
		isRemote:                    isRemote,
		transportType:               transportType,
		onHealthCheckFailed:         onHealthCheckFailed,
		onHealthCheckRecovered:      onHealthCheckRecovered,
		onUnauthorizedResponse:      onUnauthorizedResponse,
		endpointPrefix:              endpointPrefix,
		trustProxyHeaders:           trustProxyHeaders,
		healthCheckInterval:         getHealthCheckInterval(),
		healthCheckRetryDelay:       getHealthCheckRetryDelay(),
		healthCheckPingTimeout:      getHealthCheckPingTimeout(),
		healthCheckFailureThreshold: getHealthCheckFailureThreshold(),
		shutdownTimeout:             defaultShutdownTimeout,
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
		if proxy.stateless {
			mcpPinger = NewStatelessMCPPingerWithTimeout(targetURI, proxy.healthCheckPingTimeout)
		} else {
			mcpPinger = NewMCPPingerWithTimeout(targetURI, proxy.healthCheckPingTimeout)
		}
	}
	proxy.healthChecker = healthcheck.NewHealthChecker(transportType, mcpPinger)

	return proxy
}

// recoverySessionStore is the subset of session.Manager that backendRecovery needs.
type recoverySessionStore interface {
	Get(id string) (session.Session, bool)
	UpsertSession(sess session.Session) error
}

// backendRecovery handles transparent re-initialization of backend sessions when the
// target pod is unreachable (dial error) or has lost its in-memory session state (404).
// It depends only on a narrow session interface and a forward function, so it can be
// tested without standing up a full proxy.
type backendRecovery struct {
	targetURI string
	forward   func(*http.Request) (*http.Response, error)
	sessions  recoverySessionStore
}

type tracingTransport struct {
	p        *TransparentProxy
	recovery *backendRecovery
}

func newTracingTransport(base http.RoundTripper, p *TransparentProxy) *tracingTransport {
	return &tracingTransport{
		p: p,
		recovery: &backendRecovery{
			targetURI: p.targetURI,
			forward:   base.RoundTrip,
			sessions:  p.sessionManager,
		},
	}
}

func (p *TransparentProxy) setServerInitialized() {
	if p.isServerInitialized.CompareAndSwap(false, true) {
		//nolint:gosec // G706: logging target URI from config
		slog.Debug("server was initialized successfully", "target", p.targetURI)
	}
}

// serverInitialized returns whether the server has been initialized (thread-safe)
func (p *TransparentProxy) serverInitialized() bool {
	return p.isServerInitialized.Load()
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

	// Guard: reject non-initialize requests with unknown session IDs.
	// When multiple proxyrunner replicas share a Redis session store,
	// a valid session will always be found. If it isn't, the session
	// has expired or the request carries a stale/forged session ID.
	if sid := req.Header.Get("Mcp-Session-Id"); sid != "" && !sawInitialize {
		if _, err := t.p.sessionManager.GetWithError(normalizeSessionID(sid)); err != nil {
			if !errors.Is(err, session.ErrSessionNotFound) {
				// Storage error (e.g. Redis timeout) — client should retry.
				slog.Error("session store lookup failed", "error", err)
				hdr := make(http.Header)
				hdr.Set("Content-Type", "text/plain; charset=utf-8")
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Status:     fmt.Sprintf("%d %s", http.StatusServiceUnavailable, http.StatusText(http.StatusServiceUnavailable)),
					Proto:      "HTTP/1.1",
					ProtoMajor: 1,
					ProtoMinor: 1,
					Header:     hdr,
					Body:       io.NopCloser(strings.NewReader("session store unavailable\n")),
					Request:    req,
				}, nil
			}
			return session.NotFoundResponse(req), nil
		}
	}

	// Capture the client-facing session ID before the backend SID rewrite below.
	// Recovery and session cleanup paths must look up sessions by the client SID
	// (the store key), not the backend SID that is written into the header.
	clientSID := req.Header.Get("Mcp-Session-Id")

	// Rewrite the outbound Mcp-Session-Id to the backend's assigned session ID when
	// the proxy transparently re-initialized the backend session. This is done here
	// (after the guard check above) so the guard always sees the original client
	// session ID and can look it up correctly in the session store.
	if clientSID != "" {
		if sess, ok := t.p.sessionManager.Get(normalizeSessionID(clientSID)); ok {
			if backendSID, exists := sess.GetMetadataValue(sessionMetadataBackendSID); exists && backendSID != "" {
				req.Header.Set("Mcp-Session-Id", backendSID)
			}
		}
	}

	// Attach an httptrace to capture the actual backend pod IP after kube-proxy
	// DNAT resolves the ClusterIP to a specific pod. The captured address is stored
	// as backend_url so follow-up requests always reach the same pod, even after a
	// proxy runner restart that would otherwise lose the in-memory routing state.
	var capturedPodAddr string
	if sawInitialize {
		trace := &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) {
				capturedPodAddr = info.Conn.RemoteAddr().String()
			},
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	}

	resp, err := followRedirects(t.recovery.forward, req, reqBody)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// Expected during shutdown or client disconnect—silently ignore
			return nil, err
		}
		// Dial error against a stored pod IP means the pod has been replaced.
		// Attempt transparent re-initialization so the client sees no error.
		if isDialError(err) {
			req.Header.Set("Mcp-Session-Id", clientSID)
			if reInitResp, reInitErr := t.recovery.reinitializeAndReplay(req, reqBody); reInitResp != nil || reInitErr != nil {
				return reInitResp, reInitErr
			}
		}
		slog.Error("failed to forward request", "error", err)
		return nil, err
	}

	// Check for 401 Unauthorized response (bearer token authentication failure)
	if resp.StatusCode == http.StatusUnauthorized {
		//nolint:gosec // G706: logging target URI from config
		slog.Debug("received 401 Unauthorized response, bearer token may be invalid",
			"target", t.p.targetURI)
		if t.p.onUnauthorizedResponse != nil {
			t.p.onUnauthorizedResponse()
		}
	}

	// Clean up session on DELETE so the transparent proxy's session manager
	// doesn't hold references until TTL expiry (#4062).
	// Remove on 2xx (successful termination) and 404 (upstream already
	// considers the session gone), since in both cases keeping a local
	// reference would only waste memory.
	if req.Method == http.MethodDelete &&
		(resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode == http.StatusNotFound) {
		if clientSID != "" {
			if err := t.p.sessionManager.Delete(normalizeSessionID(clientSID)); err != nil {
				slog.Debug("failed to delete session from transparent proxy",
					"session_id", clientSID, "error", err)
			}
		}
	}

	// Backend returned 404 for a non-initialize, non-DELETE request whose session IS
	// known to the proxy. This means the backend pod lost its in-memory session state
	// (e.g. it was restarted but got the same IP). Attempt transparent re-initialization
	// so the client sees no error. DELETE is excluded because the session has already
	// been cleaned up above and the 404 is the expected terminal response.
	if resp.StatusCode == http.StatusNotFound && !sawInitialize && req.Method != http.MethodDelete {
		if clientSID != "" {
			req.Header.Set("Mcp-Session-Id", clientSID)
			if reInitResp, reInitErr := t.recovery.reinitializeAndReplay(req, reqBody); reInitResp != nil || reInitErr != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				return reInitResp, reInitErr
			}
		}
	}

	if resp.StatusCode == http.StatusOK {
		// check if we saw a valid mcp header
		ct := resp.Header.Get("Mcp-Session-Id")
		if ct != "" {
			//nolint:gosec // G706: logging session ID from HTTP response header
			slog.Debug("detected Mcp-Session-Id header", "session_id", ct)
			internalID := normalizeSessionID(ct)
			if _, ok := t.p.sessionManager.Get(internalID); !ok {
				sess := session.NewProxySession(internalID)
				// Store the actual pod IP (captured via GotConn) as backend_url so that
				// after a proxy runner restart the session is routed to the same backend
				// pod that handled initialize, not a random pod via ClusterIP.
				sess.SetMetadata(sessionMetadataBackendURL, t.recovery.podBackendURL(capturedPodAddr))
				// Store the initialize body so we can transparently re-initialize the
				// backend session if the pod is later replaced or loses session state.
				if len(reqBody) > 0 {
					sess.SetMetadata(sessionMetadataInitBody, string(reqBody))
				}
				if err := t.p.sessionManager.AddSession(sess); err != nil {
					//nolint:gosec // G706: session ID from HTTP response header
					slog.Error("failed to create session from header",
						"session_id", ct, "error", err)
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
			slog.Warn("failed to read request body", "error", err)
		} else {
			reqBody = buf
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	return reqBody
}

func (t *tracingTransport) detectInitialize(body []byte) bool {
	type rpcMethod struct {
		Method string `json:"method"`
	}

	// Single JSON-RPC object.
	var single rpcMethod
	if err := json.Unmarshal(body, &single); err == nil {
		if single.Method == "initialize" {
			//nolint:gosec // G706: logging target URI from config
			slog.Debug("detected initialize method call", "target", t.p.targetURI)
			return true
		}
		return false
	}

	// JSON-RPC batch: array of objects. Return true if any member is initialize.
	var batch []rpcMethod
	if err := json.Unmarshal(body, &batch); err != nil {
		slog.Debug("failed to parse JSON-RPC body", "error", err)
		return false
	}
	for _, rpc := range batch {
		if rpc.Method == "initialize" {
			//nolint:gosec // G706: logging target URI from config
			slog.Debug("detected initialize method call in batch", "target", t.p.targetURI)
			return true
		}
	}
	return false
}

// podBackendURL constructs a backend URL that targets the specific pod IP captured
// via httptrace.GotConn, using the scheme from targetURI. Falls back to targetURI
// when no address was captured (e.g. single-replica, connection reuse without a new conn),
// or when targetURI uses HTTPS — IP-literal HTTPS URLs fail TLS verification because
// server certificates are issued for hostnames, not pod IPs.
func (r *backendRecovery) podBackendURL(capturedAddr string) string {
	if capturedAddr == "" {
		return r.targetURI
	}
	parsed, err := url.Parse(r.targetURI)
	if err != nil {
		return r.targetURI
	}
	if parsed.Scheme == "https" { //nolint:goconst // protocol name, not a magic string
		return r.targetURI
	}
	parsed.Host = capturedAddr
	return parsed.String()
}

// isDialError reports whether err is a TCP dial failure, indicating that the
// target host is unreachable (pod has been terminated or rescheduled).
func isDialError(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// followRedirects wraps a forward call with same-host HTTP redirect following.
// MCP clients expect JSON-RPC responses and cannot handle 3xx redirects, so the
// proxy must resolve them before returning the response. Only same-host redirects
// are followed to prevent SSRF. The HTTP method and request body are always
// preserved (POST never becomes GET), which is correct for JSON-RPC semantics.
func followRedirects(
	forward func(*http.Request) (*http.Response, error),
	req *http.Request,
	body []byte,
) (*http.Response, error) {
	resp, err := forward(req)
	if err != nil {
		return nil, err
	}

	originalHost := req.URL.Host
	for redirectsFollowed := 0; redirectsFollowed < maxRedirects &&
		isRedirectStatus(resp.StatusCode); redirectsFollowed++ {
		location := resp.Header.Get("Location")
		if location == "" {
			break
		}

		redirectURL, parseErr := req.URL.Parse(location)
		if parseErr != nil {
			slog.Warn("failed to parse redirect Location header",
				"location", location, "error", parseErr)
			break
		}

		// Block cross-host redirects to prevent SSRF and credential leakage.
		if redirectURL.Host != originalHost {
			slog.Warn("refusing cross-host redirect from remote MCP server; update the configured target URL",
				"from_host", originalHost, "to_host", redirectURL.Host)
			break
		}

		// Block HTTPS-to-HTTP downgrades to prevent silent loss of transport security.
		//nolint:goconst // "https" is a protocol name, not a magic string worth extracting
		if req.URL.Scheme == "https" && redirectURL.Scheme == "http" {
			slog.Warn("refusing redirect that downgrades from HTTPS to HTTP",
				"from", req.URL.String(), "to", redirectURL.String())
			break
		}

		slog.Info("following HTTP redirect from remote MCP server; consider updating the server URL",
			"status", resp.StatusCode,
			"from", req.URL.String(),
			"to", redirectURL.String(),
			"redirect_number", redirectsFollowed+1)

		// Drain and close the redirect response body to release the
		// underlying connection back to the transport's connection pool.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		// Clone preserves Method and all headers. We intentionally do not
		// change the method to GET for 301/302 (as browsers do) because
		// MCP JSON-RPC requires POST with a body on every request.
		req = req.Clone(req.Context())
		req.URL = redirectURL
		req.Host = redirectURL.Host
		if len(body) > 0 {
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
		}

		resp, err = forward(req)
		if err != nil {
			return nil, err
		}
	}

	return resp, nil
}

// isRedirectStatus reports whether the HTTP status code is a redirect
// that should be followed. This excludes 300 (Multiple Choices), 303
// (See Other), and 304 (Not Modified) which are not standard redirects
// or would require changing the request method.
func isRedirectStatus(code int) bool {
	switch code {
	case http.StatusMovedPermanently, // 301
		http.StatusFound,             // 302
		http.StatusTemporaryRedirect, // 307
		http.StatusPermanentRedirect: // 308
		return true
	default:
		return false
	}
}

// reinitializeAndReplay is called when the proxy detects that the backend pod
// that owned a session is no longer reachable (dial error) or has lost its
// in-memory session state (backend returned 404). It transparently:
//  1. Re-sends the stored initialize body to the ClusterIP service so kube-proxy
//     selects a healthy pod and the backend creates a new session.
//  2. Captures the new pod IP via httptrace.GotConn and stores it as backend_url.
//  3. Maps the client's original session ID to the new backend session ID.
//  4. Replays the original client request so the client sees no error.
//
// Returns (nil, nil) when re-initialization is not applicable (session unknown
// to the proxy, or no stored init body for the session).
func (r *backendRecovery) reinitializeAndReplay(req *http.Request, origBody []byte) (*http.Response, error) {
	sid := req.Header.Get("Mcp-Session-Id")
	if sid == "" {
		return nil, nil
	}
	internalSID := normalizeSessionID(sid)
	sess, ok := r.sessions.Get(internalSID)
	if !ok {
		return nil, nil
	}

	initBody, hasInit := sess.GetMetadataValue(sessionMetadataInitBody)
	if !hasInit || initBody == "" {
		// No stored init body — cannot re-initialize transparently.
		// Reset backend_url to ClusterIP so the next request goes through
		// kube-proxy and lets the client receive a clean 404 to re-initialize.
		sess.SetMetadata(sessionMetadataBackendURL, r.targetURI)
		_ = r.sessions.UpsertSession(sess)
		return nil, nil
	}

	slog.Debug("backend session lost; transparently re-initializing",
		"session_id", sid, "target", r.targetURI)

	// Capture the new pod IP via GotConn on the re-initialize connection.
	var capturedPodAddr string
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			capturedPodAddr = info.Conn.RemoteAddr().String()
		},
	}
	initCtx := httptrace.WithClientTrace(req.Context(), trace)

	// Build a fresh initialize request to the ClusterIP (no Mcp-Session-Id —
	// the backend assigns a new session ID in the response).
	parsedTarget, err := url.Parse(r.targetURI)
	if err != nil {
		return nil, nil
	}
	initURL := *req.URL
	initURL.Scheme = parsedTarget.Scheme
	initURL.Host = parsedTarget.Host

	initReq, err := http.NewRequestWithContext(initCtx, http.MethodPost, initURL.String(), bytes.NewReader([]byte(initBody)))
	if err != nil {
		return nil, nil
	}
	// Propagate headers from the original request (Authorization, tenant headers, etc.)
	// so the backend accepts the re-initialize. Mcp-Session-Id must not be forwarded —
	// the backend assigns a new session ID in the response. Content-Length and
	// Transfer-Encoding are deleted because http.NewRequestWithContext already set
	// ContentLength from the body; leaving stale header values would be misleading
	// (Go's transport ignores them in favour of the struct field, but clarity matters).
	initReq.Header = req.Header.Clone()
	initReq.Header.Del("Mcp-Session-Id")
	initReq.Header.Del("Content-Length")
	initReq.Header.Del("Transfer-Encoding")
	initReq.Header.Set("Content-Type", "application/json")

	initResp, err := r.forward(initReq)
	if err != nil {
		slog.Error("transparent re-initialize failed", "error", err)
		return nil, err
	}
	_, _ = io.Copy(io.Discard, initResp.Body)
	_ = initResp.Body.Close()

	newBackendSID := initResp.Header.Get("Mcp-Session-Id")
	if newBackendSID == "" {
		slog.Debug("re-initialize response contained no Mcp-Session-Id; falling back to ClusterIP")
		sess.SetMetadata(sessionMetadataBackendURL, r.targetURI)
		_ = r.sessions.UpsertSession(sess)
		return nil, nil
	}

	// Update session: point backend_url at the newly-discovered pod and record
	// the backend session ID so tracingTransport.RoundTrip rewrites Mcp-Session-Id on outbound requests.
	newPodURL := r.podBackendURL(capturedPodAddr)
	sess.SetMetadata(sessionMetadataBackendURL, newPodURL)
	// Store the raw backend session ID (not normalized) because the Rewrite closure
	// uses this value verbatim as the outbound Mcp-Session-Id header. Normalizing
	// would change non-UUID IDs to a UUID v5 hash the backend never issued.
	sess.SetMetadata(sessionMetadataBackendSID, newBackendSID)
	if upsertErr := r.sessions.UpsertSession(sess); upsertErr != nil {
		slog.Debug("failed to update session after re-initialize", "error", upsertErr)
	}

	// Replay the original client request to the new pod with the new backend SID.
	// Use the captured pod address directly so we bypass the Rewrite closure
	// (which still holds the old backend_url until the next session load).
	// For HTTPS targets, keep the original hostname: IP-literal HTTPS requests
	// fail TLS verification because server certs are issued for hostnames, not pod IPs.
	replayHost := capturedPodAddr
	if replayHost == "" || parsedTarget.Scheme == "https" {
		replayHost = parsedTarget.Host
	}
	replayReq := req.Clone(req.Context())
	replayReq.URL.Scheme = parsedTarget.Scheme
	replayReq.URL.Host = replayHost
	replayReq.Host = replayHost // keep Host header consistent with URL to avoid backend validation errors
	replayReq.Header.Set("Mcp-Session-Id", newBackendSID)
	replayReq.Body = io.NopCloser(bytes.NewReader(origBody))
	replayReq.ContentLength = int64(len(origBody))
	// origBody is fully buffered, so chunked encoding is unnecessary and would
	// suppress the Content-Length header. Clear any TransferEncoding copied from
	// the original request so net/http sends Content-Length instead.
	replayReq.TransferEncoding = nil

	slog.Debug("replaying original request after transparent re-initialization",
		"new_pod_url", newPodURL, "new_backend_sid", newBackendSID)
	return r.forward(replayReq)
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

	// Guard against calling Start() after Stop()
	if p.stopped {
		return fmt.Errorf("proxy has been stopped")
	}

	// Parse the target URI
	targetURL, err := url.Parse(p.targetURI)
	if err != nil {
		return fmt.Errorf("failed to parse target URI: %w", err)
	}

	// Create a reverse proxy
	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			pr.SetXForwarded()

			// Route to the originating backend pod when session metadata contains backend_url.
			// Falls back to static targetURL when the session doesn't exist or has no backend_url.
			if sid := pr.In.Header.Get("Mcp-Session-Id"); sid != "" {
				if sess, ok := p.sessionManager.Get(normalizeSessionID(sid)); ok {
					if backendURLStr, exists := sess.GetMetadataValue(sessionMetadataBackendURL); exists && backendURLStr != "" {
						parsed, parseErr := url.Parse(backendURLStr)
						switch {
						case parseErr != nil:
							slog.Debug("failed to parse backend_url from session metadata; using static target",
								sessionMetadataBackendURL, backendURLStr, "error", parseErr)
						case parsed.Scheme == "" || parsed.Host == "":
							slog.Debug("backend_url from session metadata is not an absolute URL; using static target",
								sessionMetadataBackendURL, backendURLStr)
						default:
							pr.Out.URL.Scheme = parsed.Scheme
							pr.Out.URL.Host = parsed.Host
						}
					}
				}
			}

			// Stash the original inbound request in the outbound request's
			// context so that ModifyResponse (SSE response processor) can
			// read the client's real headers instead of the auto-injected
			// X-Forwarded-* values that SetXForwarded() wrote to pr.Out.
			ctx := InboundRequestToContext(pr.Out.Context(), pr.In)
			pr.Out = pr.Out.WithContext(ctx)

			// Rewrite path to the remote server's path when configured.
			// When the remote URL has a path (e.g., /v2/mcp), the target URI only
			// contains the scheme+host. The client sends to /mcp (default MCP
			// endpoint) but the remote server expects /v2/mcp. We replace the
			// request path with the remote server's configured path.
			if p.remoteBasePath != "" {
				pr.Out.URL.Path = p.remoteBasePath
				pr.Out.URL.RawPath = ""
			}

			// Merge query parameters from the remote URL into the outbound request.
			// Remote params are prepended so they appear first; most HTTP servers
			// adopt first-value-wins semantics for duplicate keys, ensuring operator
			// configured values (e.g., toolsets=core,alerting) take precedence over
			// any client-supplied params with the same key.
			// Raw string concatenation is intentional: url.Values.Encode() would
			// percent-encode characters like commas that some APIs expect as literals.
			if p.remoteRawQuery != "" {
				merged := p.remoteRawQuery
				if pr.Out.URL.RawQuery != "" {
					merged += "&" + pr.Out.URL.RawQuery
				}
				pr.Out.URL.RawQuery = merged
			}

			// Inject OpenTelemetry trace propagation headers for downstream tracing
			if pr.Out.Context() != nil {
				otel.GetTextMapPropagator().Inject(pr.Out.Context(), propagation.HeaderCarrier(pr.Out.Header))
			}
		},
	}

	proxy.Transport = newTracingTransport(http.DefaultTransport, p)
	proxy.ModifyResponse = func(resp *http.Response) error {
		return p.modifyResponse(resp)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r) // #nosec G704 -- target URL is the configured backend MCP server
	})

	// Create a mux to handle both proxy and health endpoints
	mux := http.NewServeMux()

	// Apply middleware chain in reverse order (last middleware is applied first)
	var finalHandler http.Handler = handler
	for i := len(p.middlewares) - 1; i >= 0; i-- {
		finalHandler = p.middlewares[i].Function(finalHandler)
		slog.Debug("applied middleware", "name", p.middlewares[i].Name)
	}

	// 1. Mount prefix handlers first (user-specified, most specific paths)
	// These are registered first but Go's ServeMux longest-match routing ensures
	// more specific paths take precedence regardless of registration order.
	for prefix, prefixHandler := range p.prefixHandlers {
		mux.Handle(prefix, prefixHandler)
		slog.Debug("mounted prefix handler", "prefix", prefix)
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
		slog.Debug("prometheus metrics endpoint enabled at /metrics")
	}

	// 4. Mount RFC 9728 OAuth Protected Resource discovery endpoint (no middlewares)
	// Note: This is DIFFERENT from the auth server's /.well-known/oauth-authorization-server
	// Always register so OAuth discovery gets a clean 404 JSON when auth is off,
	// instead of falling through to the proxy catch-all.
	wellKnownHandler := auth.NewWellKnownHandler(p.authInfoHandler)
	mux.Handle("/.well-known/", wellKnownHandler)
	if p.authInfoHandler != nil {
		slog.Debug("rfc 9728 OAuth discovery endpoint enabled at /.well-known/oauth-protected-resource")
	}

	// 5. Catch-all proxy handler (least specific - ServeMux routing handles precedence)
	// Note: No manual path checking needed - ServeMux longest-match routing ensures
	// more specific paths registered above take precedence over this catch-all.
	// In stateless mode, wrap with a method gate that rejects GET/DELETE with 405.
	if p.stateless {
		finalHandler = statelessMethodGate(finalHandler)
	}
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
		ReadHeaderTimeout: 10 * time.Second,   // Prevent Slowloris attacks
		IdleTimeout:       defaultIdleTimeout, // Prevent idle keep-alive connections from blocking Shutdown()
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
			slog.Error("transparent proxy error", "error", err)
		}
	}()
	// Start health-check monitoring only if health checker is enabled
	if p.healthChecker != nil {
		go p.monitorHealth(ctx)
	}

	return nil
}

// ListenerAddr returns the network address the proxy is listening on.
// Returns an empty string if the proxy has not been started.
func (p *TransparentProxy) ListenerAddr() string {
	if p.listener == nil {
		return ""
	}
	return p.listener.Addr().String()
}

// CloseListener closes the listener for the transparent proxy.
func (p *TransparentProxy) CloseListener() error {
	if p.listener != nil {
		return p.listener.Close()
	}
	return nil
}

// performHealthCheckRetry performs a retry health check after a delay
// Returns true if the retry was successful (health check recovered), false otherwise
func (p *TransparentProxy) performHealthCheckRetry(ctx context.Context) bool {
	retryTimer := time.NewTimer(p.healthCheckRetryDelay)
	defer retryTimer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-p.shutdownCh:
		return false
	case <-retryTimer.C:
		retryAlive := p.healthChecker.CheckHealth(ctx)
		if retryAlive.Status == healthcheck.StatusHealthy {
			//nolint:gosec // G706: logging target URI from config
			slog.Debug("health check recovered after retry", "target", p.targetURI)
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
	//nolint:gosec // G706: logging target URI from config and health status
	slog.Warn("health check failed",
		"target", p.targetURI,
		"attempt", consecutiveFailures,
		"max_attempts", p.healthCheckFailureThreshold,
		"status", status)

	if consecutiveFailures < p.healthCheckFailureThreshold {
		if p.performHealthCheckRetry(ctx) {
			consecutiveFailures = 0
		}
		return consecutiveFailures, true
	}

	// Threshold reached — notify the status manager.
	//nolint:gosec // G706: logging target URI from config
	slog.Error("health check failed after consecutive attempts",
		"target", p.targetURI, "attempts", p.healthCheckFailureThreshold)
	if p.onHealthCheckFailed != nil {
		p.onHealthCheckFailed()
	}

	if p.isRemote {
		// For remote workloads ToolHive does not own the server lifecycle.
		// Stay in degraded mode and keep monitoring so auto-recovery is possible
		// (e.g. scale-to-zero backends that come back after a cold start).
		// Keep the counter at threshold so that monitorHealth can detect recovery
		// (it fires onHealthCheckRecovered when consecutiveFailures >= threshold on
		// the next successful check), then resets to 0 for the next outage window.
		return p.healthCheckFailureThreshold, true
	}

	// Local container: ToolHive controls the lifecycle — stop the proxy.
	if err := p.Stop(ctx); err != nil {
		slog.Error("failed to stop proxy",
			"target", p.targetURI, "error", err)
	}
	return consecutiveFailures, false
}

func (p *TransparentProxy) monitorHealth(parentCtx context.Context) {
	ticker := time.NewTicker(p.healthCheckInterval)
	defer ticker.Stop()

	consecutiveFailures := 0

	for {
		select {
		case <-parentCtx.Done():
			//nolint:gosec // G706: logging target URI from config
			slog.Debug("context cancelled, stopping health monitor", "target", p.targetURI)
			return
		case <-p.shutdownCh:
			//nolint:gosec // G706: logging target URI from config
			slog.Debug("shutdown initiated, stopping health monitor", "target", p.targetURI)
			return
		case <-ticker.C:
			// For local container workloads, skip health checks until the MCP server
			// has completed initialization (i.e. a successful initialize response was
			// observed). This avoids false-positive unhealthy transitions during slow
			// container startup. Remote workloads never call setServerInitialized() on
			// 500 responses, so we always check them regardless of init state.
			if !p.isRemote && !p.serverInitialized() {
				//nolint:gosec // G706: logging target URI from config
				slog.Debug("mcp server not initialized yet, skipping health check", "target", p.targetURI)
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
			if consecutiveFailures >= p.healthCheckFailureThreshold {
				// Recovered from a degraded state — notify the status manager.
				//nolint:gosec // G706: logging target URI from config
				slog.Info("health check recovered after failures",
					"target", p.targetURI, "previous_failures", consecutiveFailures)
				if p.onHealthCheckRecovered != nil {
					p.onHealthCheckRecovered()
				}
			} else if consecutiveFailures > 0 {
				//nolint:gosec // G706: logging target URI from config
				slog.Debug("health check recovered",
					"target", p.targetURI, "previous_failures", consecutiveFailures)
			}
			consecutiveFailures = 0
		}
	}
}

// Stop stops the transparent proxy.
func (p *TransparentProxy) Stop(ctx context.Context) error {
	p.mutex.Lock()

	// Check if already stopped
	if p.stopped {
		p.mutex.Unlock()
		//nolint:gosec // G706: logging target URI from config
		slog.Debug("proxy is already stopped, skipping", "target", p.targetURI)
		return nil
	}

	// Mark as stopped and signal shutdown under the lock
	p.stopped = true
	close(p.shutdownCh)

	// Capture server reference and nil it out under the lock so no other
	// goroutine can race on p.server after we release the mutex.
	server := p.server
	p.server = nil

	// Release the lock before server.Shutdown() so IsRunning() is not blocked
	// while long-lived connections drain.
	p.mutex.Unlock()

	if server != nil {
		// Use the caller's context if still valid; fall back to a fresh one
		// when the caller's context is already cancelled (e.g. the health
		// monitor calls Stop() after its parent context is done).
		base := ctx
		if base.Err() != nil {
			base = context.Background()
		}
		shutdownCtx, cancel := context.WithTimeout(base, p.shutdownTimeout)
		defer cancel()

		err := server.Shutdown(shutdownCtx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				// Graceful shutdown timed out — force-close remaining connections
				slog.Warn("graceful shutdown timed out, force-closing connections",
					"target", p.targetURI, "timeout", p.shutdownTimeout)
				if closeErr := server.Close(); closeErr != nil {
					slog.Warn("error during forced server close", "error", closeErr)
				}
			} else if !errors.Is(err, http.ErrServerClosed) {
				slog.Warn("error during proxy shutdown", "error", err)
				return err
			}
		}
		//nolint:gosec // G706: logging target URI from config
		slog.Debug("server stopped successfully", "target", p.targetURI)
	}

	// Stop the session manager to terminate its cleanup goroutine and close any
	// underlying storage connections (e.g. Redis client) opened via WithSessionStorage.
	if p.sessionManager != nil {
		if err := p.sessionManager.Stop(); err != nil {
			slog.Warn("error stopping session manager", "error", err)
		}
	}

	return nil
}

// IsRunning checks if the proxy is running.
func (p *TransparentProxy) IsRunning() (bool, error) {
	// No mutex needed: shutdownCh is closed under the lock in Stop(),
	// and a select on a closed channel is goroutine-safe by design.
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

// statelessMethodGate wraps a handler to reject GET, HEAD, and DELETE requests with 405.
// Used in stateless mode where the server only supports POST.
// HEAD is blocked alongside GET because HEAD is semantically a GET without a response body;
// a server that cannot handle GET will not handle HEAD either.
func statelessMethodGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodDelete {
			w.Header().Set("Allow", "POST, OPTIONS")
			http.Error(w, "method not allowed: server is stateless (POST only)", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package client provides MCP protocol client implementation for communicating with backend servers.
//
// This package implements the BackendClient interface defined in the vmcp package,
// using the stacklok/toolhive-core/mcpcompat SDK for protocol communication.
package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/stacklok/toolhive-core/mcpcompat/client"
	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
	"github.com/stacklok/toolhive/pkg/vmcp/internal/pagination"
)

const (
	// maxResponseSize is the maximum size in bytes for HTTP responses from backend MCP servers.
	// This protects against DoS attacks via memory exhaustion from malicious or compromised backends.
	//
	// The MCP specification does not define size limits, so we enforce a reasonable limit
	// to prevent unbounded memory allocation during JSON deserialization.
	//
	// Value: 100 MB
	// Rationale:
	//   - Allows large tool outputs, resources, and capability lists
	//   - Prevents memory exhaustion (a single large response could OOM the process)
	//   - Applied at HTTP transport layer before JSON deserialization
	//   - Backends needing larger responses should use pagination or streaming
	//
	// Note: This limit is enforced per HTTP response, not per MCP request.
	// A tools/list response with 1000 tools would be limited to 100MB total.
	maxResponseSize = 100 * 1024 * 1024 // 100 MB
)

// Option configures an httpBackendClient.
type Option func(*httpBackendClient)

// WithDialControl installs a per-connection Control hook on the dialer used to
// reach every backend. The hook fires after DNS resolution and before the TCP
// handshake, receiving the resolved peer IP in address — which is why it
// defeats DNS-rebinding attacks that a host-name–based allow/deny check cannot:
// a hostname can legitimately resolve to a blocked IP after the name-based check
// passes.
//
// The hook composes with per-backend CA-bundle handling: it augments the
// internally built *http.Transport rather than replacing it. Supplying it
// implies the standard dial timeouts (30 s Timeout, 30 s KeepAlive) used
// throughout this package.
//
// The signature matches net.Dialer.Control exactly.
//
// Security limitations embedders must understand:
//
//   - Per-TCP-dial, not per-request: the hook fires once per TCP connection.
//     A pooled connection is reused without re-invoking the hook until it is
//     recycled. Because each backend gets its own isolated transport and
//     connection pool, a reused connection is always one this hook already
//     approved on its first dial — reuse cannot reach an unclassified peer.
//     This client does not offer per-request re-classification.
//
//   - Proxy transparency: when http.ProxyFromEnvironment selects a proxy
//     (HTTP_PROXY/HTTPS_PROXY set), the dial target is the proxy server, so
//     the hook receives the proxy's IP, not the backend's. Embedders relying
//     on this hook for SSRF or IP allow-listing must either unset the proxy
//     env vars or additionally validate the request URL's host before dialing.
//
//   - Both IP families: the address argument may be an IPv4 or IPv6 literal
//     (host:port form); embedders must handle both families — including
//     IPv4-mapped IPv6 such as ::ffff:127.0.0.1 — in their check. See the
//     OWASP SSRF Prevention Cheat Sheet for the full set of ranges to deny
//     (loopback, RFC 1918, link-local 169.254/16, CGNAT 100.64/10, IPv6 ULA).
func WithDialControl(control func(network, address string, c syscall.RawConn) error) Option {
	return func(h *httpBackendClient) {
		h.dialControl = control
	}
}

// httpBackendClient implements vmcp.BackendClient using stacklok/toolhive-core/mcpcompat HTTP client.
// It supports streamable-HTTP and SSE transports for backend MCP servers.
type httpBackendClient struct {
	// clientFactory creates MCP clients for backends.
	// Abstracted as a function to enable testing with mock clients.
	clientFactory func(ctx context.Context, target *vmcp.BackendTarget) (*client.Client, error)

	// registry manages authentication strategies for outgoing requests to backend MCP servers.
	// Must not be nil - use UnauthenticatedStrategy for no authentication.
	registry vmcpauth.OutgoingAuthRegistry

	// secretsProvider resolves TOOLHIVE_SECRET_<ident> env vars at client-creation
	// time for per-backend header-forward injection. Nil when no backends declare
	// headerForward.AddHeadersFromSecret — plaintext-only backends do not require it.
	secretsProvider secrets.Provider

	// dialControl is an optional per-connection hook injected via WithDialControl.
	// When non-nil it is installed on the net.Dialer used by every backend transport,
	// receiving the resolved peer IP before the TCP handshake. Nil reproduces the
	// default dialer behavior with no hook.
	dialControl func(network, address string, c syscall.RawConn) error

	// forwarders holds the server->client forwarding requesters (elicitation,
	// sampling, notifications) bound by server.New via BindForwarders. When set,
	// the client factory installs handlers on each backend client so a backend's
	// mid-call server->client traffic is relayed to the downstream client, and
	// enables continuous listening so standalone-SSE server->client messages are
	// delivered. Nil (unbound) reproduces the pre-forwarding behavior exactly, so
	// direct embedders and unit tests without a bound server are unaffected.
	forwarders atomic.Pointer[boundForwarders]
}

// NewHTTPBackendClient creates a new HTTP-based backend client.
// This client supports streamable-HTTP and SSE transports.
//
// The registry parameter manages authentication strategies for outgoing requests to backend MCP servers.
// It must not be nil. To disable authentication, use a registry configured with the
// "unauthenticated" strategy.
//
// Options are additive: nil or absent options reproduce the default behavior exactly.
// See [WithDialControl] to install a per-connection dial hook for SSRF /
// DNS-rebinding defense.
//
// Returns an error if registry is nil.
func NewHTTPBackendClient(registry vmcpauth.OutgoingAuthRegistry, opts ...Option) (vmcp.BackendClient, error) {
	if registry == nil {
		return nil, fmt.Errorf("registry cannot be nil; use UnauthenticatedStrategy for no authentication")
	}

	c := &httpBackendClient{
		registry:        registry,
		secretsProvider: secrets.NewEnvironmentProvider(),
	}
	for _, o := range opts {
		o(c)
	}
	c.clientFactory = c.defaultClientFactory
	return c, nil
}

// backendDialer returns a net.Dialer with the standard backend timeouts and an
// optional Control hook. Centralising the timeout constants here ensures the
// fallback-construction branch and the dial-control replacement branch always
// stay in sync — no "kept in sync with the branch below" promise required.
func backendDialer(control func(network, address string, c syscall.RawConn) error) *net.Dialer {
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   control,
	}
}

// newBackendTransport creates a *http.Transport with the same defaults as http.DefaultTransport.
// If http.DefaultTransport is a *http.Transport, it is cloned directly (preserving any
// environment-specific settings like TLS config or proxy overrides). Otherwise a transport
// with the standard Go defaults is constructed, preserving proxy, dial timeout, HTTP/2, and
// idle-connection settings that a zero-value &http.Transport{} would drop.
//
// If caBundlePath is non-empty, a custom TLS configuration is applied that trusts both
// the system root CAs and the certificate(s) in the specified file. This is used for
// entry-type backends with self-signed or internal CA certificates (static mode).
//
// If caBundleData is non-empty, the raw PEM bytes are used directly instead of reading
// from a file. This is used in dynamic mode where CA bundles are fetched from K8s
// ConfigMaps at discovery time. caBundleData takes precedence over caBundlePath.
//
// If dialControl is non-nil, a fresh net.Dialer carrying the hook is installed on the
// transport. The hook fires per-connection on the resolved peer IP, which is what
// defeats DNS-rebinding attacks — a name-based check cannot, because the name can
// resolve to a blocked IP after the check passes. A cloned *http.Transport exposes
// DialContext only as an opaque func, so we cannot read back the original dialer's
// settings; we reconstruct the dialer via backendDialer instead.
func newBackendTransport(
	caBundlePath string,
	caBundleData []byte,
	dialControl func(network, address string, c syscall.RawConn) error,
) (*http.Transport, error) {
	var t *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		t = dt.Clone()
	} else {
		// http.DefaultTransport has been replaced (e.g. in tests or by a third-party library).
		// Construct a transport with the same defaults as the Go standard library uses for
		// http.DefaultTransport so we don't silently drop proxy, timeout, or HTTP/2 settings.
		t = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           backendDialer(nil).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}

	if dialControl != nil {
		t.DialContext = backendDialer(dialControl).DialContext
	}

	// Resolve CA certificate PEM data: caBundleData takes precedence over caBundlePath
	var caPEM []byte
	switch {
	case len(caBundleData) > 0:
		caPEM = caBundleData
	case caBundlePath != "":
		var err error
		caPEM, err = os.ReadFile(caBundlePath) //nolint:gosec // CA bundle path is validated by config validator (no path traversal)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA bundle from %s: %w", caBundlePath, err)
		}
	}

	if len(caPEM) > 0 {
		caCertPool, err := x509.SystemCertPool()
		if err != nil {
			// Fall back to empty pool if system certs can't be loaded
			caCertPool = x509.NewCertPool()
		}

		if !caCertPool.AppendCertsFromPEM(caPEM) {
			source := "inline data"
			if len(caBundleData) == 0 && caBundlePath != "" {
				source = caBundlePath
			}
			return nil, fmt.Errorf("failed to parse CA certificate from %s", source)
		}

		if t.TLSClientConfig == nil {
			t.TLSClientConfig = &tls.Config{}
		} else {
			t.TLSClientConfig = t.TLSClientConfig.Clone()
		}
		t.TLSClientConfig.RootCAs = caCertPool
		t.TLSClientConfig.MinVersion = tls.VersionTLS12
	}

	return t, nil
}

// roundTripperFunc is a function adapter for http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper interface.
func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// identityPropagatingRoundTripper propagates the health-check marker and a
// fallback identity to backend HTTP requests.
//
// Identity invariant: an identity already present on req.Context() is NEVER
// overridden. The per-request identity placed on the request context by
// auth.TokenValidator.Middleware carries the freshest upstream tokens (the
// middleware refreshes them transparently on every incoming request via
// upstreamtoken.InProcessService.GetAllUpstreamCredentials). Overriding it with a
// snapshot captured at session-init time would silently re-inject stale
// upstream access tokens on every backend call, forcing users to re-auth
// once the captured access token expired (see issue #5323).
//
// The fallback identity is used only when req.Context() carries no identity
// at all. This covers mcp-go's streamable-HTTP Close() path, which constructs
// its DELETE request from context.Background() and therefore loses any
// identity attached to the original tool-call context. Without a fallback,
// auth strategies (UpstreamInjectStrategy, TokenExchangeStrategy, AWSSTSStrategy)
// would reject the teardown DELETE with "no identity found in context" — the
// teardown would still complete locally, but the upstream backend would see
// an unauthenticated DELETE. The fallback may itself be stale, which is
// acceptable for a session-close: the request is best-effort and any 401
// from the upstream does not affect functional behavior.
//
// The health-check marker is similarly captured at transport creation time and
// re-injected into every outgoing request — including the Close() DELETE — so
// that auth strategies correctly skip authentication for health probes even
// when the request context has been replaced with context.Background().
//
// This is the canonical location for the #5323 fallback-only identity
// invariant. A near-clone exists at identityRoundTripper in
// pkg/vmcp/session/internal/backend/mcp_session.go (without isHealthCheck
// support, since health probes do not flow through the session-backed
// connector). Keep the invariant in sync across both implementations until
// #5333 lands a shared transport.
type identityPropagatingRoundTripper struct {
	base http.RoundTripper
	// fallbackIdentity is injected only when req.Context() carries no identity.
	// It must never override a non-nil identity present on the request context.
	//
	// Shared across all concurrent RoundTrip invocations on this transport; the
	// pointed-to *auth.Identity (including UpstreamTokens) MUST be treated as
	// immutable — see pkg/auth/identity.go.
	fallbackIdentity *auth.Identity
	isHealthCheck    bool
}

// RoundTrip implements http.RoundTripper.
//
// It preserves any identity already present on req.Context() (the fresh,
// per-request identity placed there by TokenValidator.Middleware). When the
// request context carries no identity — for example, mcp-go's Close() DELETE
// is constructed from context.Background() — the captured fallback identity
// is injected so session-teardown requests can still authenticate. The
// health-check marker is always re-injected when configured.
func (i *identityPropagatingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	mutated := false

	// Inject the fallback identity ONLY when the request context carries no
	// identity. We must never overwrite a non-nil identity already on ctx —
	// doing so would clobber the freshly refreshed upstream tokens that the
	// auth middleware places on every incoming request (see #5323).
	if _, hasIdentity := auth.IdentityFromContext(ctx); !hasIdentity && i.fallbackIdentity != nil {
		ctx = auth.WithIdentity(ctx, i.fallbackIdentity)
		mutated = true
	}

	if i.isHealthCheck {
		ctx = healthcontext.WithHealthCheckMarker(ctx)
		mutated = true
	}

	if mutated {
		req = req.Clone(ctx)
	}
	return i.base.RoundTrip(req)
}

// tracePropagatingRoundTripper injects W3C Trace Context (traceparent/tracestate) and
// Baggage headers into outgoing HTTP requests. This links vMCP client spans with backend
// server spans in distributed traces without creating duplicate spans (unlike
// otelhttp.NewTransport).
type tracePropagatingRoundTripper struct {
	base       http.RoundTripper
	propagator propagation.TextMapPropagator
}

// RoundTrip implements http.RoundTripper by injecting trace context headers.
func (t *tracePropagatingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clonedReq := req.Clone(req.Context())
	t.propagator.Inject(clonedReq.Context(), propagation.HeaderCarrier(clonedReq.Header))
	return t.base.RoundTrip(clonedReq)
}

// authRoundTripper is an http.RoundTripper that adds authentication to backend requests.
// The authentication strategy is pre-resolved and validated at client creation time,
// eliminating per-request lookups and validation overhead.
type authRoundTripper struct {
	base         http.RoundTripper
	authStrategy vmcpauth.Strategy
	authConfig   *authtypes.BackendAuthStrategy
	target       *vmcp.BackendTarget
}

// RoundTrip implements http.RoundTripper by adding authentication headers to requests.
// The authentication strategy was pre-resolved and validated at client creation time,
// so this method simply applies the authentication without any lookups or validation.
func (a *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone request to avoid modifying the original
	reqClone := req.Clone(req.Context())

	// Apply pre-resolved authentication strategy
	if err := a.authStrategy.Authenticate(reqClone.Context(), reqClone, a.authConfig); err != nil {
		return nil, fmt.Errorf("authentication failed for backend %s: %w", a.target.WorkloadID, err)
	}

	return a.base.RoundTrip(reqClone)
}

// resolveAuthStrategy resolves the authentication strategy for a backend target.
// It handles defaulting to "unauthenticated" when no auth config is specified.
// This method should be called once at client creation time to enable fail-fast
// behavior for invalid authentication configurations.
func (h *httpBackendClient) resolveAuthStrategy(target *vmcp.BackendTarget) (vmcpauth.Strategy, error) {
	// Default to unauthenticated if not specified
	strategyName := authtypes.StrategyTypeUnauthenticated
	if target.AuthConfig != nil {
		strategyName = target.AuthConfig.Type
	}

	// Resolve strategy from registry
	strategy, err := h.registry.GetStrategy(strategyName)
	if err != nil {
		return nil, fmt.Errorf("authentication strategy %q not found: %w", strategyName, err)
	}

	return strategy, nil
}

// defaultClientFactory creates mcpcompat MCP clients for different transport types.
func (h *httpBackendClient) defaultClientFactory(ctx context.Context, target *vmcp.BackendTarget) (*client.Client, error) {
	// Build transport chain (outermost to innermost, request execution order):
	// size limit (response body) → trace propagation → identity propagation → authentication → HTTP
	//
	// Build an isolated per-call transport so each client gets its own connection pool,
	// preventing stale keep-alive connections from one backend affecting others.
	httpTransport, err := newBackendTransport(target.CABundlePath, target.CABundleData, h.dialControl)
	if err != nil {
		return nil, fmt.Errorf("failed to create transport for backend %s: %w", target.WorkloadID, err)
	}
	var baseTransport http.RoundTripper = httpTransport

	// Resolve authentication strategy ONCE at client creation time
	authStrategy, err := h.resolveAuthStrategy(target)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve authentication for backend %s: %w",
			target.WorkloadID, err)
	}

	// Validate auth config ONCE at client creation time
	if err := authStrategy.Validate(target.AuthConfig); err != nil {
		return nil, fmt.Errorf("invalid authentication configuration for backend %s: %w",
			target.WorkloadID, err)
	}

	slog.Debug("applied authentication strategy to backend", "strategy", authStrategy.Name(), "backend", target.WorkloadID)

	// Add authentication layer with pre-resolved strategy
	baseTransport = &authRoundTripper{
		base:         baseTransport,
		authStrategy: authStrategy,
		authConfig:   target.AuthConfig,
		target:       target,
	}

	// Merge the live per-request forwarded headers (captured by headerforward.CaptureMiddleware
	// into the request context) with the static per-backend header-forward config. This factory
	// runs on every backend call, so forwarding is per-request: each call reflects the current
	// incoming header value. Restricted names and static-config collisions are rejected by
	// MergeForwardedHeaders.
	mergedHeaderForward, mergeErr := headerforward.MergeForwardedHeaders(
		target.HeaderForward, headerforward.ForwardedHeadersFromContext(ctx),
	)
	if mergeErr != nil {
		return nil, fmt.Errorf("failed to merge forwarded headers for backend %s: %w", target.WorkloadID, mergeErr)
	}
	baseTransport, err = headerforward.BuildHeaderForwardTripper(
		ctx, baseTransport, mergedHeaderForward, h.secretsProvider, target.WorkloadID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build header-forward transport: %w", err)
	}

	// Capture a fallback identity and the health-check marker from the construction
	// context. These are used ONLY when the per-request context lacks them.
	//
	// Scope note: httpBackendClient calls this factory on every CallTool /
	// ReadResource / GetPrompt / ListCapabilities with the per-call context, so
	// the captured "fallback" is per-call, not per-session, in this path. The
	// always-stale capture-at-session-init behavior that caused #5323 lives in
	// the persistent-session path (pkg/vmcp/session/internal/backend); this
	// transport uses the same RoundTripper out of consistency, not because it
	// suffered the same bug.
	//
	// Fallback injection scope: the round-tripper injects the captured fallback
	// identity on ANY outgoing request whose context lacks an identity — not
	// only the teardown DELETE. In the current code paths the only known caller
	// that drops the per-request identity is mcp-go's streamable-HTTP Close()
	// (which builds its DELETE from context.Background()), so in practice the
	// fallback only fires for teardown. If a future code path wraps the client
	// with a context that doesn't carry identity (audit, telemetry, background
	// reconciliation), it will be silently authenticated using the captured
	// snapshot. Add an explicit method/URL gate if that becomes undesirable.
	//
	//   - Normal backend requests inherit the fresh, per-request identity placed on
	//     the request context by auth.TokenValidator.Middleware. The transport
	//     preserves it untouched so upstream-token refresh is not bypassed (#5323).
	//
	//   - mcp-go's streamable-HTTP Close() builds its DELETE from context.Background(),
	//     which loses both the identity and the health-check marker. Re-injecting them
	//     here keeps the teardown DELETE authenticated and tagged as a health-check
	//     probe when the original session was a probe (#4613).
	fallbackIdentity, _ := auth.IdentityFromContext(ctx)
	baseTransport = &identityPropagatingRoundTripper{
		base:             baseTransport,
		fallbackIdentity: fallbackIdentity,
		isHealthCheck:    healthcontext.IsHealthCheck(ctx),
	}

	// Inject W3C Trace Context headers (traceparent/tracestate) into outgoing requests.
	// This links vMCP spans with backend spans in the same distributed trace.
	baseTransport = &tracePropagatingRoundTripper{
		base:       baseTransport,
		propagator: otel.GetTextMapPropagator(),
	}

	// Snapshot the bound server->client forwarders (nil when unbound). When set,
	// the client is built with elicitation/sampling handlers and continuous
	// listening so a backend's mid-call server->client traffic reaches the
	// downstream client; when nil, construction is byte-for-byte the pre-forwarding
	// path.
	fwd := h.forwarders.Load()

	var c *client.Client

	switch target.TransportType {
	case "streamable-http", "streamable":
		// "streamable" is a legacy alias for "streamable-http".
		//
		// For streamable-HTTP each MCP call is a single bounded HTTP
		// request/response pair, so a per-response body size limit is safe.
		sizeLimitedTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			resp, err := baseTransport.RoundTrip(req)
			if err != nil {
				return nil, err
			}
			resp.Body = struct {
				io.Reader
				io.Closer
			}{
				Reader: io.LimitReader(resp.Body, maxResponseSize),
				Closer: resp.Body,
			}
			return resp, nil
		})
		httpClient := &http.Client{
			Transport: sizeLimitedTransport,
			Timeout:   30 * time.Second,
		}
		transportOpts := []transport.StreamableHTTPCOption{
			transport.WithHTTPTimeout(30 * time.Second),
			transport.WithHTTPBasicClient(httpClient),
		}
		if fwd != nil {
			// A backend's mid-call elicitation/sampling request is routed by the
			// go-sdk onto the standalone SSE stream (the shim server replies with
			// application/json), so the backend client must hold that stream open
			// for the request to arrive and be answered.
			transportOpts = append(transportOpts, transport.WithContinuousListening())
			c, err = client.NewStreamableHttpClientWithOpts(
				target.BaseURL, transportOpts, forwardingClientOptions(ctx, fwd),
			)
		} else {
			c, err = client.NewStreamableHttpClient(target.BaseURL, transportOpts...)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to create streamable-http client: %w", err)
		}

	case "sse":
		// For SSE the entire session is one long-lived HTTP response body.
		// Applying io.LimitReader would silently terminate the stream after
		// maxResponseSize cumulative bytes — not per-event — which is wrong.
		// http.Client.Timeout is also omitted: it would kill the stream.
		httpClient := &http.Client{Transport: baseTransport}
		c, err = client.NewSSEMCPClient(
			target.BaseURL,
			transport.WithHTTPClient(httpClient),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create SSE client: %w", err)
		}

	default:
		return nil, fmt.Errorf("%w: %s (supported: streamable-http, sse)", vmcp.ErrUnsupportedTransport, target.TransportType)
	}

	// Register the notification forwarder before Initialize (the caller runs it
	// after this factory returns) so a backend's mid-call progress/logging
	// notifications are relayed to the downstream client. OnNotification is a
	// post-construction method, so it applies to both transports.
	if fwd != nil && fwd.notifier != nil {
		c.OnNotification(newNotificationForwarder(ctx, fwd.notifier))
	}

	// Start the client connection
	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start client connection: %w", err)
	}

	// Note: Initialization is deferred to the caller (e.g., ListCapabilities)
	// so that ServerCapabilities can be captured and used for conditional querying
	return c, nil
}

// isAuthorizationRequired reports whether err is one of the mcp-go authorization-required
// sentinels. Extracted to keep wrapBackendError within the cyclomatic complexity limit.
func isAuthorizationRequired(err error) bool {
	return errors.Is(err, transport.ErrAuthorizationRequired) ||
		errors.Is(err, transport.ErrOAuthAuthorizationRequired)
}

// wrapBackendError wraps an error with the appropriate sentinel error based on error type.
// This enables type-safe error checking with errors.Is() instead of string matching.
//
// Error detection strategy (in order of preference):
// 1. Check for standard Go error types (context errors, net.Error, url.Error)
// 2. Fall back to string pattern matching for library-specific errors (MCP SDK, HTTP libs)
//
// Error chain preservation:
// The returned error wraps the sentinel error (ErrTimeout, ErrBackendUnavailable, etc.) with %w
// and formats the original error with %v. This means:
// - errors.Is() works for checking the sentinel error (e.g., errors.Is(err, vmcp.ErrTimeout))
// - errors.As() cannot access the underlying original error type
// This is a deliberate trade-off due to Go's limitation of one %w per fmt.Errorf call.
// If access to the underlying error type is needed in the future, consider implementing
// a custom error type with multiple Unwrap() methods (Go 1.20+).
func wrapBackendError(err error, backendID string, operation string) error {
	if err == nil {
		return nil
	}

	// 1. Type-based detection: Check for context deadline/cancellation
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: failed to %s for backend %s (timeout): %v",
			vmcp.ErrTimeout, operation, backendID, err)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: failed to %s for backend %s (cancelled): %v",
			vmcp.ErrCancelled, operation, backendID, err)
	}

	// 2. Type-based detection: Check for io.EOF errors
	// These indicate the connection was closed unexpectedly
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: failed to %s for backend %s (connection closed): %v",
			vmcp.ErrBackendUnavailable, operation, backendID, err)
	}

	// 3. Type-based detection: Check for net.Error with Timeout() method
	// This handles network timeouts from the standard library
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: failed to %s for backend %s (timeout): %v",
			vmcp.ErrTimeout, operation, backendID, err)
	}

	// 4. mcp-go transport sentinel errors: check before string-based fallbacks
	// to ensure accurate classification of protocol-level errors.
	if errors.Is(err, transport.ErrUnauthorized) {
		return fmt.Errorf("%w: failed to %s for backend %s: %v",
			vmcp.ErrAuthenticationFailed, operation, backendID, err)
	}
	// transport.ErrAuthorizationRequired is returned (wrapped in *transport.Error
	// and *transport.AuthorizationRequiredError) for 401 responses with a
	// WWW-Authenticate header. transport.ErrOAuthAuthorizationRequired is the
	// companion sentinel from the OAuth-handler path. Both must map to
	// ErrAuthenticationFailed so health monitoring engages the auth-aware
	// branch (#4935) instead of treating the probe as unhealthy (#5223).
	if isAuthorizationRequired(err) {
		return fmt.Errorf("%w: failed to %s for backend %s: %v",
			vmcp.ErrAuthenticationFailed, operation, backendID, err)
	}
	// ErrLegacySSEServer is returned for any 4xx (except 401) on initialize POST.
	// This includes 403 (auth rejection) and 404/405 (endpoint not found/method not allowed).
	// We cannot distinguish auth failures from routing errors without the raw status code,
	// so we surface a clear message and classify as backend unavailable to allow recovery.
	if errors.Is(err, transport.ErrLegacySSEServer) {
		const legacyMsg = "server rejected MCP initialize — possible auth rejection or legacy SSE-only server"
		return fmt.Errorf("%w: failed to %s for backend %s (%s): %v",
			vmcp.ErrBackendUnavailable, operation, backendID, legacyMsg, err)
	}

	// 5. ErrUpstreamTokenNotFound: upstream provider token missing from identity.
	// Explicit sentinel check takes priority over the string-based "authentication failed"
	// fallback below, which would also match (via the authRoundTripper error message)
	// but is fragile. This maps to ErrAuthenticationFailed so the pre-check middleware
	// and health monitors classify the error correctly.
	if errors.Is(err, authtypes.ErrUpstreamTokenNotFound) {
		return fmt.Errorf("%w: failed to %s for backend %s (upstream token missing): %v",
			vmcp.ErrAuthenticationFailed, operation, backendID, err)
	}

	// 6. String-based detection: Fall back to pattern matching for cases where
	// we don't have structured error types (MCP SDK, HTTP libraries with embedded status codes)
	// Authentication errors (401, 403, auth failures)
	if vmcp.IsAuthenticationError(err) {
		return fmt.Errorf("%w: failed to %s for backend %s: %v",
			vmcp.ErrAuthenticationFailed, operation, backendID, err)
	}

	// Timeout errors (deadline exceeded, timeout messages)
	if vmcp.IsTimeoutError(err) {
		return fmt.Errorf("%w: failed to %s for backend %s (timeout): %v",
			vmcp.ErrTimeout, operation, backendID, err)
	}

	// Connection errors (refused, reset, unreachable)
	if vmcp.IsConnectionError(err) {
		return fmt.Errorf("%w: failed to %s for backend %s (connection error): %v",
			vmcp.ErrBackendUnavailable, operation, backendID, err)
	}

	// Default to backend unavailable for unknown errors
	return fmt.Errorf("%w: failed to %s for backend %s: %v",
		vmcp.ErrBackendUnavailable, operation, backendID, err)
}

// initializeClient performs MCP protocol initialization handshake and returns server capabilities.
// This allows the caller to determine which optional features the server supports.
func initializeClient(ctx context.Context, c *client.Client) (*mcp.ServerCapabilities, error) {
	result, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "toolhive-vmcp",
				Version: versions.Version,
			},
			Capabilities: mcp.ClientCapabilities{
				// Virtual MCP acts as a client to backends
				Roots: &struct {
					ListChanged bool `json:"listChanged,omitempty"`
				}{
					ListChanged: false,
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return &result.Capabilities, nil
}

// queryTools queries tools from a backend if the server advertises tool support.
// It follows MCP pagination cursors so backends that paginate (mcpcompat
// paginates at DefaultPageSize=1000) contribute their complete tool set rather
// than only the first page.
func queryTools(ctx context.Context, c *client.Client, supported bool, backendID string) (*mcp.ListToolsResult, error) {
	if supported {
		tools, err := pagination.ListAll(ctx, func(ctx context.Context, cursor mcp.Cursor) ([]mcp.Tool, mcp.Cursor, error) {
			req := mcp.ListToolsRequest{}
			req.Params.Cursor = cursor
			result, err := c.ListTools(ctx, req)
			if err != nil {
				return nil, "", err
			}
			return result.Tools, result.NextCursor, nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list tools from backend %s: %w", backendID, err)
		}
		return &mcp.ListToolsResult{Tools: tools}, nil
	}
	slog.Debug("backend does not advertise tools capability, skipping tools query", "backend", backendID)
	return &mcp.ListToolsResult{Tools: []mcp.Tool{}}, nil
}

// queryResources queries resources from a backend if the server advertises
// resource support. It follows MCP pagination cursors (see queryTools).
func queryResources(ctx context.Context, c *client.Client, supported bool, backendID string) (*mcp.ListResourcesResult, error) {
	if supported {
		resources, err := pagination.ListAll(ctx, func(ctx context.Context, cursor mcp.Cursor) ([]mcp.Resource, mcp.Cursor, error) {
			req := mcp.ListResourcesRequest{}
			req.Params.Cursor = cursor
			result, err := c.ListResources(ctx, req)
			if err != nil {
				return nil, "", err
			}
			return result.Resources, result.NextCursor, nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list resources from backend %s: %w", backendID, err)
		}
		return &mcp.ListResourcesResult{Resources: resources}, nil
	}
	slog.Debug("backend does not advertise resources capability, skipping resources query", "backend", backendID)
	return &mcp.ListResourcesResult{Resources: []mcp.Resource{}}, nil
}

// queryResourceTemplates queries resource templates from a backend if the server
// advertises resource support. Resource templates are gated on the same
// serverCaps.Resources advertisement as plain resources (there is no separate
// capability flag for templates). It follows MCP pagination cursors (see queryTools).
func queryResourceTemplates(
	ctx context.Context, c *client.Client, supported bool, backendID string,
) (*mcp.ListResourceTemplatesResult, error) {
	if supported {
		templates, err := pagination.ListAll(
			ctx, func(ctx context.Context, cursor mcp.Cursor) ([]mcp.ResourceTemplate, mcp.Cursor, error) {
				req := mcp.ListResourceTemplatesRequest{}
				req.Params.Cursor = cursor
				result, err := c.ListResourceTemplates(ctx, req)
				if err != nil {
					return nil, "", err
				}
				return result.ResourceTemplates, result.NextCursor, nil
			})
		if err != nil {
			return nil, fmt.Errorf("failed to list resource templates from backend %s: %w", backendID, err)
		}
		return &mcp.ListResourceTemplatesResult{ResourceTemplates: templates}, nil
	}
	slog.Debug("backend does not advertise resources capability, skipping resource templates query", "backend", backendID)
	return &mcp.ListResourceTemplatesResult{ResourceTemplates: []mcp.ResourceTemplate{}}, nil
}

// queryPrompts queries prompts from a backend if the server advertises prompt
// support. It follows MCP pagination cursors (see queryTools).
func queryPrompts(ctx context.Context, c *client.Client, supported bool, backendID string) (*mcp.ListPromptsResult, error) {
	if supported {
		prompts, err := pagination.ListAll(ctx, func(ctx context.Context, cursor mcp.Cursor) ([]mcp.Prompt, mcp.Cursor, error) {
			req := mcp.ListPromptsRequest{}
			req.Params.Cursor = cursor
			result, err := c.ListPrompts(ctx, req)
			if err != nil {
				return nil, "", err
			}
			return result.Prompts, result.NextCursor, nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list prompts from backend %s: %w", backendID, err)
		}
		return &mcp.ListPromptsResult{Prompts: prompts}, nil
	}
	slog.Debug("backend does not advertise prompts capability, skipping prompts query", "backend", backendID)
	return &mcp.ListPromptsResult{Prompts: []mcp.Prompt{}}, nil
}

// ListCapabilities queries a backend for its MCP capabilities.
// Returns tools, resources, and prompts exposed by the backend.
// Only queries capabilities that the server advertises during initialization.
func (h *httpBackendClient) ListCapabilities(ctx context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
	slog.Debug("querying capabilities from backend", "backend", target.WorkloadName, "url", target.BaseURL)

	// Create a client for this backend (not yet initialized)
	c, err := h.clientFactory(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			slog.Debug("failed to close client", "error", err)
		}
	}()

	// Initialize the client and get server capabilities
	serverCaps, err := initializeClient(ctx, c)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
	}

	slog.Debug("backend capabilities",
		"backend", target.WorkloadID,
		"tools", serverCaps.Tools != nil,
		"resources", serverCaps.Resources != nil,
		"prompts", serverCaps.Prompts != nil)

	// Query each capability type based on server advertisement
	// Check for nil BEFORE passing to functions to avoid interface{} nil pointer issues
	toolsResp, err := queryTools(ctx, c, serverCaps.Tools != nil, target.WorkloadID)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "list tools")
	}

	resourcesResp, err := queryResources(ctx, c, serverCaps.Resources != nil, target.WorkloadID)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "list resources")
	}

	// Resource templates share the same server capability advertisement as resources.
	resourceTemplatesResp, err := queryResourceTemplates(ctx, c, serverCaps.Resources != nil, target.WorkloadID)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "list resource templates")
	}

	promptsResp, err := queryPrompts(ctx, c, serverCaps.Prompts != nil, target.WorkloadID)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "list prompts")
	}

	// Convert MCP types to vmcp types
	capabilities := &vmcp.CapabilityList{
		Tools:             make([]vmcp.Tool, len(toolsResp.Tools)),
		Resources:         make([]vmcp.Resource, len(resourcesResp.Resources)),
		ResourceTemplates: make([]vmcp.ResourceTemplate, len(resourceTemplatesResp.ResourceTemplates)),
		Prompts:           make([]vmcp.Prompt, len(promptsResp.Prompts)),
	}

	// Convert tools
	for i, tool := range toolsResp.Tools {
		capabilities.Tools[i] = vmcp.Tool{
			Name:         tool.Name,
			Description:  tool.Description,
			InputSchema:  conversion.ConvertToolInputSchema(tool.InputSchema),
			OutputSchema: conversion.ConvertToolOutputSchema(tool.OutputSchema),
			Annotations:  conversion.ConvertToolAnnotations(tool.Annotations),
			BackendID:    target.WorkloadID,
		}
	}

	// Convert resources
	for i, resource := range resourcesResp.Resources {
		capabilities.Resources[i] = vmcp.Resource{
			URI:         resource.URI,
			Name:        resource.Name,
			Description: resource.Description,
			MimeType:    resource.MIMEType,
			BackendID:   target.WorkloadID,
		}
	}

	// Convert resource templates (pass-through: no URI-template rewriting, like resources)
	for i, template := range resourceTemplatesResp.ResourceTemplates {
		capabilities.ResourceTemplates[i] = vmcp.ResourceTemplate{
			URITemplate: template.URITemplate,
			Name:        template.Name,
			Description: template.Description,
			MimeType:    template.MIMEType,
			BackendID:   target.WorkloadID,
		}
	}

	// Convert prompts
	for i, prompt := range promptsResp.Prompts {
		args := make([]vmcp.PromptArgument, len(prompt.Arguments))
		for j, arg := range prompt.Arguments {
			args[j] = vmcp.PromptArgument{
				Name:        arg.Name,
				Description: arg.Description,
				Required:    arg.Required,
			}
		}

		capabilities.Prompts[i] = vmcp.Prompt{
			Name:        prompt.Name,
			Description: prompt.Description,
			Arguments:   args,
			BackendID:   target.WorkloadID,
		}
	}

	// TODO: Query server capabilities to detect logging/sampling support
	// This requires additional MCP protocol support for capabilities introspection

	slog.Debug("backend capabilities queried",
		"backend", target.WorkloadName,
		"tools", len(capabilities.Tools),
		"resources", len(capabilities.Resources),
		"resource_templates", len(capabilities.ResourceTemplates),
		"prompts", len(capabilities.Prompts))

	return capabilities, nil
}

// CallTool invokes a tool on the backend MCP server.
// Returns the complete tool result including _meta field.
//
//nolint:gocyclo // this function is complex because it handles tool calls with various content types and error handling.
func (h *httpBackendClient) CallTool(
	ctx context.Context,
	target *vmcp.BackendTarget,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	slog.Debug("calling tool on backend", "tool", toolName, "backend", target.WorkloadName)

	// Create a client for this backend
	c, err := h.clientFactory(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			slog.Debug("failed to close client", "error", err)
		}
	}()

	// Initialize the client and capture the backend's advertised capabilities.
	serverCaps, err := initializeClient(ctx, c)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
	}

	// When forwarders are bound and the backend advertises logging, request debug
	// level so the backend emits notifications/message during the call; the
	// notification forwarder relays them to the downstream client. Best-effort:
	// a failure here must not fail the tool call.
	h.enableBackendLogging(ctx, c, serverCaps, target.WorkloadID)

	// Call the tool using the original capability name from the backend's perspective.
	// When conflict resolution renames tools (e.g., "fetch" → "fetch_fetch"),
	// we must use the original backend name when forwarding requests.
	backendToolName := target.GetBackendCapabilityName(toolName)
	if backendToolName != toolName {
		slog.Debug("translating tool name", "client_name", toolName, "backend_name", backendToolName)
	}

	result, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      backendToolName,
			Arguments: arguments,
			Meta:      conversion.ToMCPMeta(meta),
		},
	})
	if err != nil {
		// Network/connection errors are operational errors
		return nil, fmt.Errorf("%w: tool call failed on backend %s: %w", vmcp.ErrBackendUnavailable, target.WorkloadID, err)
	}

	// Extract _meta field from backend response
	responseMeta := conversion.FromMCPMeta(result.Meta)

	// Log if tool returned IsError=true (MCP protocol-level error, not a transport error)
	// We still return the full result to preserve metadata and error details for the client
	if result.IsError {
		var errorMsg string
		if len(result.Content) > 0 {
			if textContent, ok := mcp.AsTextContent(result.Content[0]); ok {
				errorMsg = textContent.Text
			}
		}
		if errorMsg == "" {
			errorMsg = "tool execution error"
		}

		// Log with metadata for distributed tracing
		if responseMeta != nil {
			slog.Warn("tool returned IsError=true",
				"tool", toolName, "backend", target.WorkloadID, "error", errorMsg, "meta", responseMeta)
		} else {
			slog.Warn("tool returned IsError=true",
				"tool", toolName, "backend", target.WorkloadID, "error", errorMsg)
		}
		// Continue processing - we return the result with IsError flag and metadata preserved
	}

	// Convert MCP content to vmcp.Content array.
	contentArray := conversion.ConvertMCPContents(result.Content)

	// Check for structured content first (preferred for composite tool step chaining).
	// StructuredContent allows templates to access nested fields directly via {{.steps.stepID.output.field}}.
	// Note: StructuredContent must be an object (map). Arrays or primitives are not supported.
	var structuredContent map[string]any
	if result.StructuredContent != nil {
		if structuredMap, ok := result.StructuredContent.(map[string]any); ok {
			slog.Debug("using structured content from tool", "tool", toolName, "backend", target.WorkloadID)
			structuredContent = structuredMap
		} else {
			// StructuredContent is not an object - fall through to Content processing
			slog.Debug("structuredContent is not an object, falling back to Content",
				"tool", toolName, "backend", target.WorkloadID)
		}
	}

	// If no structured content, convert result contents to a map for backward compatibility.
	// MCP tools return an array of Content interface (TextContent, ImageContent, etc.).
	// Text content is stored under "text" key, accessible via {{.steps.stepID.output.text}}.
	if structuredContent == nil {
		structuredContent = conversion.ContentArrayToMap(contentArray)
	}

	return &vmcp.ToolCallResult{
		Content:           contentArray,
		StructuredContent: structuredContent,
		IsError:           result.IsError,
		Meta:              responseMeta,
	}, nil
}

// ReadResource retrieves a resource from the backend MCP server.
// Returns the complete resource result including _meta field.
func (h *httpBackendClient) ReadResource(
	ctx context.Context, target *vmcp.BackendTarget, uri string,
) (*vmcp.ResourceReadResult, error) {
	slog.Debug("reading resource from backend", "resource", uri, "backend", target.WorkloadName)

	// Create a client for this backend
	c, err := h.clientFactory(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			slog.Debug("failed to close client", "error", err)
		}
	}()

	// Initialize the client
	if _, err := initializeClient(ctx, c); err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
	}

	// Read the resource using the original URI from the backend's perspective.
	// When conflict resolution renames resources, we must use the original backend URI.
	backendURI := target.GetBackendCapabilityName(uri)
	if backendURI != uri {
		slog.Debug("translating resource URI", "client_uri", uri, "backend_uri", backendURI)
	}

	result, err := c.ReadResource(ctx, mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{
			URI: backendURI,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("resource read failed on backend %s: %w", target.WorkloadID, err)
	}

	// Extract _meta field from backend response
	meta := conversion.FromMCPMeta(result.Meta)

	// Note: Due to MCP SDK limitations, the SDK's ReadResourceResult may not include Meta.
	// This preserves it for future SDK improvements.
	return &vmcp.ResourceReadResult{
		Contents: conversion.ConvertMCPResourceContents(result.Contents),
		Meta:     meta,
	}, nil
}

// GetPrompt retrieves a prompt from the backend MCP server.
// Returns the complete prompt result including _meta field.
func (h *httpBackendClient) GetPrompt(
	ctx context.Context,
	target *vmcp.BackendTarget,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	slog.Debug("getting prompt from backend", "prompt", name, "backend", target.WorkloadName)

	// Create a client for this backend
	c, err := h.clientFactory(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			slog.Debug("failed to close client", "error", err)
		}
	}()

	// Initialize the client
	if _, err := initializeClient(ctx, c); err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
	}

	// Get the prompt using the original prompt name from the backend's perspective.
	// When conflict resolution renames prompts, we must use the original backend name.
	backendPromptName := target.GetBackendCapabilityName(name)
	if backendPromptName != name {
		slog.Debug("translating prompt name", "client_name", name, "backend_name", backendPromptName)
	}

	stringArgs := conversion.ConvertPromptArguments(arguments)

	result, err := c.GetPrompt(ctx, mcp.GetPromptRequest{
		Params: mcp.GetPromptParams{
			Name:      backendPromptName,
			Arguments: stringArgs,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("prompt get failed on backend %s: %w", target.WorkloadID, err)
	}

	return &vmcp.PromptGetResult{
		Messages:    conversion.ConvertMCPPromptMessages(result.Messages),
		Description: result.Description,
		Meta:        conversion.FromMCPMeta(result.Meta),
	}, nil
}

// Complete requests argument-completion candidates from the backend MCP server.
// Returns an empty (non-nil) result when the backend does not advertise the
// completions capability, matching the MCP spec's lenient completion semantics.
func (h *httpBackendClient) Complete(
	ctx context.Context,
	target *vmcp.BackendTarget,
	ref vmcp.CompletionRef,
	argName, argValue string,
	contextArgs map[string]string,
) (*vmcp.CompletionResult, error) {
	slog.Debug("requesting completion from backend",
		"ref_type", ref.Type, "backend", target.WorkloadName)

	// Create a client for this backend
	c, err := h.clientFactory(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			slog.Debug("failed to close client", "error", err)
		}
	}()

	// Initialize the client and capture the backend's advertised capabilities.
	serverCaps, err := initializeClient(ctx, c)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
	}

	// Backends that do not advertise completions cannot serve completion/complete;
	// return an empty result rather than erroring (lenient completion semantics).
	if serverCaps.Completions == nil {
		slog.Debug("backend does not advertise completions capability, returning empty completion",
			"backend", target.WorkloadID)
		return &vmcp.CompletionResult{Values: []string{}}, nil
	}

	mcpRef, err := buildCompletionRef(target, ref)
	if err != nil {
		return nil, err
	}

	params := mcp.CompleteParams{
		Ref: mcpRef,
		Argument: mcp.CompleteArgument{
			Name:  argName,
			Value: argValue,
		},
	}
	if len(contextArgs) > 0 {
		params.Context = &mcp.CompleteContext{Arguments: contextArgs}
	}

	result, err := c.Complete(ctx, mcp.CompleteRequest{Params: params})
	if err != nil {
		return nil, fmt.Errorf("completion failed on backend %s: %w", target.WorkloadID, err)
	}

	values := result.Completion.Values
	if values == nil {
		values = []string{}
	}
	return &vmcp.CompletionResult{
		Values:  values,
		Total:   result.Completion.Total,
		HasMore: result.Completion.HasMore,
	}, nil
}

// buildCompletionRef converts a domain CompletionRef into the mcp-go-shaped
// PromptReference/ResourceReference the shim client expects. For a prompt ref the
// name is translated to the backend's own capability name (mirroring GetPrompt);
// for a resource ref the URI is passed through unchanged (mirroring ReadResource,
// whose translation is the identity for non-renamed resources).
func buildCompletionRef(target *vmcp.BackendTarget, ref vmcp.CompletionRef) (any, error) {
	switch ref.Type {
	case vmcp.CompletionRefTypePrompt:
		backendName := target.GetBackendCapabilityName(ref.Name)
		if backendName != ref.Name {
			slog.Debug("translating prompt name for completion",
				"client_name", ref.Name, "backend_name", backendName)
		}
		return mcp.PromptReference{Type: ref.Type, Name: backendName}, nil
	case vmcp.CompletionRefTypeResource:
		return mcp.ResourceReference{Type: ref.Type, URI: ref.URI}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported completion ref type %q", vmcp.ErrInvalidInput, ref.Type)
	}
}

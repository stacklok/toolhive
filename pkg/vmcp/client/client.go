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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/stacklok/toolhive-core/mcpcompat/client"
	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
	"github.com/stacklok/toolhive/pkg/vmcp/internal/backendtelemetry"
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
	// clientFactory creates MCP clients for backends. The forwarding flag is set
	// only for the tools/call path — the sole operation during which a backend
	// may issue server->client requests/notifications (elicitation, sampling,
	// progress, logging). It gates the continuous-listening SSE stream and the
	// forwarding handlers so that aggregation/list/read/prompt/complete calls do
	// NOT open a standalone GET stream (which hangs backends that don't support
	// one — see the multi-backend optimizer regression).
	// Abstracted as a function to enable testing with mock clients.
	clientFactory func(ctx context.Context, target *vmcp.BackendTarget, forwarding bool) (*client.Client, error)

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

	// revisions caches each backend's resolved MCP revision, keyed by
	// target.WorkloadID. An ABSENT key means unprobed — distinct from
	// RevisionLegacy (0), a resolved result. Populated by probeRevision on the
	// first ListCapabilities for a backend and read on subsequent calls to skip
	// the Modern-first discover probe.
	//
	// ponytail: never evicted — a transient failure on the FIRST probe pins a
	// backend to RevisionLegacy for the process lifetime. Recovery depends on
	// re-classification-on-error (re-probe and flip the cached revision when a
	// call reveals the other era); a TTL/periodic re-probe is deferred until
	// flapping backends surface.
	revisions sync.Map // map[string]mcpparser.Revision
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
// newStreamableHTTPClient builds a streamable-HTTP backend client. It bounds the
// response body size and — only when forwarding is requested (the tools/call path)
// and forwarders are bound — enables continuous listening plus the elicitation/
// sampling handlers so a backend's mid-call server->client traffic reaches the
// downstream client. Non-forwarding calls get the plain client (no standalone GET
// stream), which is byte-for-byte the pre-forwarding construction.
func (*httpBackendClient) newStreamableHTTPClient(
	ctx context.Context, target *vmcp.BackendTarget,
	baseTransport http.RoundTripper, forwarding bool, fwd *boundForwarders,
) (*client.Client, error) {
	// For streamable-HTTP each MCP call is a single bounded HTTP request/response
	// pair, so a per-response body size limit is safe.
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
	if fwd != nil && forwarding {
		// A backend's mid-call elicitation/sampling request is routed by the go-sdk
		// onto the standalone SSE stream (the shim server replies with
		// application/json), so the backend client must hold that stream open for the
		// request to arrive and be answered. Only enabled for the tools/call path:
		// opening this GET stream against a backend that does not support one hangs
		// the call, so aggregation/list/read/prompt/complete must not enable it.
		transportOpts = append(transportOpts, transport.WithContinuousListening())
		return client.NewStreamableHttpClientWithOpts(
			target.BaseURL, transportOpts, forwardingClientOptions(ctx, fwd),
		)
	}
	return client.NewStreamableHttpClient(target.BaseURL, transportOpts...)
}

// newSSEClient builds an SSE backend client. For SSE the entire session is one
// long-lived HTTP response body, so — unlike the streamable-HTTP client — no
// response-body size limit and no http.Client.Timeout apply: the former would
// silently terminate the stream after maxResponseSize CUMULATIVE bytes, the
// latter would kill the stream. When forwarding is requested (the tools/call
// path) and forwarders are bound, the elicitation/sampling handlers are
// installed so a backend's mid-call server->client traffic reaches the
// downstream client; SSE carries those requests on the already-open event
// stream, so no continuous-listening option is needed. Non-forwarding calls get
// the plain client, byte-for-byte the pre-forwarding construction.
func (*httpBackendClient) newSSEClient(
	ctx context.Context, target *vmcp.BackendTarget,
	baseTransport http.RoundTripper, forwarding bool, fwd *boundForwarders,
) (*client.Client, error) {
	c, err := client.NewSSEMCPClient(
		target.BaseURL,
		transport.WithHTTPClient(&http.Client{Transport: baseTransport}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSE client: %w", err)
	}
	if fwd != nil && forwarding {
		// NewSSEMCPClient accepts only transport options, so the client options
		// are applied post-construction; both run before the caller's Initialize,
		// which is what the handlers' capability declaration requires.
		for _, opt := range forwardingClientOptions(ctx, fwd) {
			opt(c)
		}
	}
	return c, nil
}

// buildBackendRoundTripper assembles the per-call backend RoundTripper chain
// shared by every backend transport (streamable-HTTP, SSE, and the raw Modern
// shim). Outermost to innermost, in request execution order:
//
//	trace propagation → identity propagation → header-forward → authentication → TLS/HTTP
//
// The nesting order is load-bearing: identity MUST wrap auth so the fresh
// per-request identity is on the context before an auth strategy reads it (#5323).
// The transport is isolated per call so each client gets its own connection pool,
// preventing stale keep-alive connections from one backend affecting others.
//
// ctx is the LIVE per-call context: the header-forward and identity layers read
// forwarded headers and the fallback identity/health-check marker off it, so
// callers MUST pass the real request context, never context.Background().
//
// This returns the CHAIN, not a wrapped *http.Client: streamable-HTTP wraps it in
// a size-limited/30s client, SSE wraps it bare (long-lived), and the Modern shim
// picks its own bound — see the callers.
func (h *httpBackendClient) buildBackendRoundTripper(
	ctx context.Context, target *vmcp.BackendTarget,
) (http.RoundTripper, error) {
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

	return baseTransport, nil
}

func (h *httpBackendClient) defaultClientFactory(
	ctx context.Context, target *vmcp.BackendTarget, forwarding bool,
) (*client.Client, error) {
	baseTransport, err := h.buildBackendRoundTripper(ctx, target)
	if err != nil {
		return nil, err
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
		c, err = h.newStreamableHTTPClient(ctx, target, baseTransport, forwarding, fwd)
		if err != nil {
			return nil, fmt.Errorf("failed to create streamable-http client: %w", err)
		}

	case "sse":
		c, err = h.newSSEClient(ctx, target, baseTransport, forwarding, fwd)
		if err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("%w: %s (supported: streamable-http, sse)", vmcp.ErrUnsupportedTransport, target.TransportType)
	}

	// Register the notification forwarder before Initialize (the caller runs it
	// after this factory returns) so a backend's mid-call progress/logging
	// notifications are relayed to the downstream client. OnNotification is a
	// post-construction method, so it applies to both transports.
	if fwd != nil && forwarding && fwd.notifier != nil {
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
		// Second %w preserves ErrLegacySSEServer in the chain so a revision
		// mismatch (Legacy initialize against a Modern backend) is detectable via
		// errors.Is; renders identically to %v.
		return fmt.Errorf("%w: failed to %s for backend %s (%s): %w",
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

	// Default to backend unavailable for unknown errors. Second %w preserves the
	// origin error (e.g. mcp.ErrMethodNotFound from a Legacy initialize against a
	// Modern backend, or errWrongEra from a Modern probe against a Legacy backend)
	// so a revision mismatch is detectable via errors.Is; renders identically to %v.
	return fmt.Errorf("%w: failed to %s for backend %s: %w",
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
		// A backend that advertises the resources capability but does not
		// implement resources/templates/list (JSON-RPC -32601) degrades to an
		// empty template list — its other capabilities still aggregate. Other
		// errors still propagate and drop the backend's capability set.
		if errors.Is(err, mcp.ErrMethodNotFound) {
			slog.Debug("backend does not implement resources/templates/list, treating templates as empty",
				"backend", backendID)
			return &mcp.ListResourceTemplatesResult{ResourceTemplates: []mcp.ResourceTemplate{}}, nil
		}
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

// cachedRevision returns the cached MCP revision for a backend. The second
// return is false when the backend has never been probed (distinct from a
// resolved RevisionLegacy).
func (h *httpBackendClient) cachedRevision(workloadID string) (mcpparser.Revision, bool) {
	v, ok := h.revisions.Load(workloadID)
	if !ok {
		return 0, false
	}
	return v.(mcpparser.Revision), true
}

// setRevision records a backend's resolved MCP revision.
func (h *httpBackendClient) setRevision(workloadID string, rev mcpparser.Revision) {
	h.revisions.Store(workloadID, rev)
}

// buildModernHTTPClient wraps the shared backend RoundTripper chain (auth,
// identity, header-forward, trace, TLS/SSRF — see buildBackendRoundTripper) in an
// *http.Client for the raw Modern shim. This is a LIVE production path: the
// discover probe must carry the same security controls as every other backend
// call, so it must NOT use a bare http.Client. A 30s timeout matches the
// streamable-HTTP client; the response body is bounded inside modernCall
// (io.LimitReader), so no size-limit transport wrapper is needed here.
func (h *httpBackendClient) buildModernHTTPClient(ctx context.Context, target *vmcp.BackendTarget) (*http.Client, error) {
	rt, err := h.buildBackendRoundTripper(ctx, target)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: rt, Timeout: 30 * time.Second}, nil
}

// modernDiscover issues a Modern server/discover and returns the backend's
// capability flags. Used both by probeRevision and, on a Modern cache hit, by
// ListCapabilities to re-fetch the flags without re-running the fallback ladder.
func (h *httpBackendClient) modernDiscover(
	ctx context.Context, target *vmcp.BackendTarget,
) (*mcp.ServerCapabilities, error) {
	hc, err := h.buildModernHTTPClient(ctx, target)
	if err != nil {
		return nil, err
	}
	var discover struct {
		Capabilities mcp.ServerCapabilities `json:"capabilities"`
	}
	if err := modernCall(ctx, hc, target.BaseURL, "server/discover", nil, "", &discover); err != nil {
		return nil, err
	}
	return &discover.Capabilities, nil
}

// probeRevision resolves and caches a backend's MCP revision, Modern-first.
//
// It attempts a Modern server/discover and classifies MODERN only on (a) a clean
// discover result, or (b) a Modern-specific protocol error (-3202x), which proves
// the peer validated our Modern headers/_meta. EVERY other outcome —
// errWrongEra, a -32601 (discover is mandatory for Modern, so its absence means
// not Modern), a generic JSON-RPC error (-32600/-32603), a bare 404/400/405, an
// empty/non-JSON body, a 200-with-Legacy-result, an input_required envelope, or a
// timeout — falls back to LEGACY. This never strands a Legacy backend on a probe
// hiccup.
//
// A hard error is returned only when the backend transport cannot be built at all
// (e.g. invalid auth/CA config); that is a genuine misconfiguration, not a
// revision signal.
func (h *httpBackendClient) probeRevision(
	ctx context.Context, target *vmcp.BackendTarget,
) (mcpparser.Revision, *mcp.ServerCapabilities, error) {
	hc, err := h.buildModernHTTPClient(ctx, target)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to build transport for backend %s: %w", target.WorkloadID, err)
	}

	var discover struct {
		Capabilities mcp.ServerCapabilities `json:"capabilities"`
	}
	err = modernCall(ctx, hc, target.BaseURL, "server/discover", nil, "", &discover)
	switch {
	case err == nil:
		h.setRevision(target.WorkloadID, mcpparser.RevisionModern)
		return mcpparser.RevisionModern, &discover.Capabilities, nil
	case errors.Is(err, errModernProtocolError):
		// The peer validated our Modern protocol metadata and rejected it: it IS
		// Modern, discover just failed application-side. No usable caps.
		// nil caps => modernEnumerate returns an empty list. The cache-hit path
		// tolerates the same -3202x error to nil caps, so both yield an empty
		// list consistently (a Modern backend that rejects our discover exposes
		// no enumerable capabilities).
		h.setRevision(target.WorkloadID, mcpparser.RevisionModern)
		return mcpparser.RevisionModern, nil, nil
	default:
		slog.Debug("backend is not Modern; falling back to Legacy",
			"backend", target.WorkloadID, "probe_error", err)
		h.setRevision(target.WorkloadID, mcpparser.RevisionLegacy)
		return mcpparser.RevisionLegacy, nil, nil
	}
}

// modernEnumerate builds a backend's CapabilityList by enumerating each
// capability the discover flags advertise, via the Modern (2026-07-28) shim.
// Each list is gated on the matching discover flag (mirroring the Legacy
// initialize-path gating) and follows nextCursor across pages (#5851).
//
// caps may be nil: a Modern backend that rejected our discover with a -3202x
// protocol error (errModernProtocolError) is still classified Modern but yields
// no capability flags, so nothing is enumerated and an empty list is returned —
// the same outcome on the first probe and on later cache hits.
func (h *httpBackendClient) modernEnumerate(
	ctx context.Context, target *vmcp.BackendTarget, caps *mcp.ServerCapabilities,
) (*vmcp.CapabilityList, error) {
	hc, err := h.buildModernHTTPClient(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	endpoint := target.BaseURL

	var tools []mcp.Tool
	if caps != nil && caps.Tools != nil {
		tools, err = pagination.ListAll(ctx, func(ctx context.Context, cursor mcp.Cursor) ([]mcp.Tool, mcp.Cursor, error) {
			var page struct {
				Tools      []mcp.Tool `json:"tools"`
				NextCursor mcp.Cursor `json:"nextCursor"`
			}
			if err := modernCall(ctx, hc, endpoint, "tools/list", cursorParams(cursor), "", &page); err != nil {
				return nil, "", err
			}
			return page.Tools, page.NextCursor, nil
		})
		if err != nil {
			return nil, wrapBackendError(err, target.WorkloadID, "list tools")
		}
	}

	var resources []mcp.Resource
	var templates []mcp.ResourceTemplate
	if caps != nil && caps.Resources != nil {
		resources, err = pagination.ListAll(ctx, func(ctx context.Context, cursor mcp.Cursor) ([]mcp.Resource, mcp.Cursor, error) {
			var page struct {
				Resources  []mcp.Resource `json:"resources"`
				NextCursor mcp.Cursor     `json:"nextCursor"`
			}
			if err := modernCall(ctx, hc, endpoint, "resources/list", cursorParams(cursor), "", &page); err != nil {
				return nil, "", err
			}
			return page.Resources, page.NextCursor, nil
		})
		if err != nil {
			return nil, wrapBackendError(err, target.WorkloadID, "list resources")
		}

		// Resource templates share the resources capability flag. A backend that
		// does not implement resources/templates/list (-32601) degrades to an
		// empty template list, mirroring the Legacy queryResourceTemplates path.
		templates, err = pagination.ListAll(
			ctx, func(ctx context.Context, cursor mcp.Cursor) ([]mcp.ResourceTemplate, mcp.Cursor, error) {
				var page struct {
					ResourceTemplates []mcp.ResourceTemplate `json:"resourceTemplates"`
					NextCursor        mcp.Cursor             `json:"nextCursor"`
				}
				if err := modernCall(ctx, hc, endpoint, "resources/templates/list", cursorParams(cursor), "", &page); err != nil {
					return nil, "", err
				}
				return page.ResourceTemplates, page.NextCursor, nil
			})
		switch {
		case errors.Is(err, mcp.ErrMethodNotFound):
			templates = nil
			err = nil // clear so a later reader can't mistake it for a live error
		case err != nil:
			return nil, wrapBackendError(err, target.WorkloadID, "list resource templates")
		}
	}

	var prompts []mcp.Prompt
	if caps != nil && caps.Prompts != nil {
		prompts, err = pagination.ListAll(ctx, func(ctx context.Context, cursor mcp.Cursor) ([]mcp.Prompt, mcp.Cursor, error) {
			var page struct {
				Prompts    []mcp.Prompt `json:"prompts"`
				NextCursor mcp.Cursor   `json:"nextCursor"`
			}
			if err := modernCall(ctx, hc, endpoint, "prompts/list", cursorParams(cursor), "", &page); err != nil {
				return nil, "", err
			}
			return page.Prompts, page.NextCursor, nil
		})
		if err != nil {
			return nil, wrapBackendError(err, target.WorkloadID, "list prompts")
		}
	}

	slog.Debug("backend capabilities queried (modern)",
		"backend", target.WorkloadName,
		"tools", len(tools), "resources", len(resources),
		"resource_templates", len(templates), "prompts", len(prompts))
	return newCapabilityListFromMCP(target.WorkloadID, tools, resources, templates, prompts), nil
}

// cursorParams builds the Modern list request params carrying a pagination
// cursor, or nil for the first page.
func cursorParams(cursor mcp.Cursor) map[string]any {
	if cursor == "" {
		return nil
	}
	return map[string]any{"cursor": string(cursor)}
}

// newCapabilityListFromMCP converts backend mcp types into the vmcp domain
// CapabilityList, tagging every item with backendID. Shared by the Legacy
// (initialize+enumerate) and Modern (discover+enumerate) paths so both produce
// identical domain shapes.
func newCapabilityListFromMCP(
	backendID string,
	tools []mcp.Tool, resources []mcp.Resource, templates []mcp.ResourceTemplate, prompts []mcp.Prompt,
) *vmcp.CapabilityList {
	capabilities := &vmcp.CapabilityList{
		Tools:             make([]vmcp.Tool, len(tools)),
		Resources:         make([]vmcp.Resource, len(resources)),
		ResourceTemplates: make([]vmcp.ResourceTemplate, len(templates)),
		Prompts:           make([]vmcp.Prompt, len(prompts)),
	}

	for i, tool := range tools {
		capabilities.Tools[i] = vmcp.Tool{
			Name:         tool.Name,
			Description:  tool.Description,
			InputSchema:  conversion.ConvertToolInputSchema(tool.InputSchema),
			OutputSchema: conversion.ConvertToolOutputSchema(tool.OutputSchema),
			Annotations:  conversion.ConvertToolAnnotations(tool.Annotations),
			BackendID:    backendID,
		}
	}

	for i, resource := range resources {
		capabilities.Resources[i] = vmcp.Resource{
			URI:         resource.URI,
			Name:        resource.Name,
			Description: resource.Description,
			MimeType:    resource.MIMEType,
			BackendID:   backendID,
		}
	}

	// Resource templates are a pass-through: no URI-template rewriting, like resources.
	for i, template := range templates {
		capabilities.ResourceTemplates[i] = vmcp.ResourceTemplate{
			URITemplate: template.URITemplate,
			Name:        template.Name,
			Description: template.Description,
			MimeType:    template.MIMEType,
			BackendID:   backendID,
		}
	}

	for i, prompt := range prompts {
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
			BackendID:   backendID,
		}
	}

	return capabilities
}

// ListCapabilities queries a backend for its MCP capabilities, selecting the
// Legacy or Modern path by revision. Returns tools, resources, and prompts
// exposed by the backend.
//
// Like the call verbs it routes through dispatch, so a mis-cached backend (e.g. a
// first-probe blip that pinned Legacy) self-corrects: the failing path triggers a
// re-probe and one retry under the corrected revision.
func (h *httpBackendClient) ListCapabilities(ctx context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
	slog.Debug("querying capabilities from backend", "backend", target.WorkloadName, "url", target.BaseURL)
	var out *vmcp.CapabilityList
	err := h.dispatch(ctx, target, func(ctx context.Context, rev mcpparser.Revision) error {
		var err error
		if rev == mcpparser.RevisionModern {
			out, err = h.modernListCapabilities(ctx, target)
		} else {
			out, err = h.legacyListCapabilities(ctx, target)
		}
		return err
	})
	return out, err
}

// modernListCapabilities resolves capabilities via Modern server/discover +
// enumeration. A -3202x protocol error is tolerated as nil caps (the backend is
// Modern but rejected our discover), yielding an empty list — consistent with
// probeRevision's classification.
func (h *httpBackendClient) modernListCapabilities(
	ctx context.Context, target *vmcp.BackendTarget,
) (*vmcp.CapabilityList, error) {
	caps, err := h.modernDiscover(ctx, target)
	if err != nil && !errors.Is(err, errModernProtocolError) {
		return nil, wrapBackendError(err, target.WorkloadID, "modern discover")
	}
	return h.modernEnumerate(ctx, target, caps)
}

// legacyListCapabilities is the initialize + enumerate path (unchanged behavior).
func (h *httpBackendClient) legacyListCapabilities(
	ctx context.Context, target *vmcp.BackendTarget,
) (*vmcp.CapabilityList, error) {
	// Create a client for this backend (not yet initialized)
	c, err := h.clientFactory(ctx, target, false)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			slog.Debug("failed to close client", "error", err)
		}
	}()

	// Initialize the client and get server capabilities
	serverCaps, err := h.legacyInit(ctx, c, target.WorkloadID)
	if err != nil {
		return nil, err
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

	// Convert MCP types to vmcp types (shared with the Modern enumeration path).
	capabilities := newCapabilityListFromMCP(
		target.WorkloadID,
		toolsResp.Tools, resourcesResp.Resources, resourceTemplatesResp.ResourceTemplates, promptsResp.Prompts,
	)

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

// errLegacyInitFailed marks a Legacy initialize-step failure (see legacyInit).
// It scopes the Legacy revision-mismatch signal to the initialize step so a
// data-plane -32601 from a genuine tool/resource/prompt/completion call on a real
// Legacy backend (a legitimately unimplemented method) never triggers a re-probe.
var errLegacyInitFailed = errors.New("legacy initialize step failed")

// legacyInit runs the Legacy initialize handshake, tagging any failure with
// errLegacyInitFailed (in addition to wrapBackendError's classification) so the
// revision-mismatch check can tell an initialize rejection apart from a
// data-plane error later in the same call.
func (*httpBackendClient) legacyInit(
	ctx context.Context, c *client.Client, backendID string,
) (*mcp.ServerCapabilities, error) {
	caps, err := initializeClient(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errLegacyInitFailed, wrapBackendError(err, backendID, "initialize client"))
	}
	return caps, nil
}

// dispatch resolves the backend's MCP revision (cache hit, else probeRevision)
// and runs fn with it. Every call verb AND ListCapabilities route through this
// seam so revision selection — and self-correction — lives in one place.
//
// On a revision mismatch (isRevisionMismatch), dispatch always re-probes
// authoritatively and flips the cache so future calls use the corrected
// revision. It RETRIES fn only when the failure proves the backend did NOT
// execute the request — a protocol rejection (errWrongEra) or a Legacy
// initialize-step failure. A Legacy-shaped success body (errLegacyResponseBody)
// means a lenient backend MAY have executed a side-effecting request, so the
// cache is flipped but fn is NOT re-run. The retry is unconditionally single
// (no re-check), so it can never loop.
//
// When the revision was just probed in THIS call (uncached), a re-probe would
// return the same answer, so a mismatch is surfaced directly without re-probing.
func (h *httpBackendClient) dispatch(
	ctx context.Context, target *vmcp.BackendTarget,
	fn func(ctx context.Context, rev mcpparser.Revision) error,
) error {
	rev, cached := h.cachedRevision(target.WorkloadID)
	if !cached {
		probed, _, err := h.probeRevision(ctx, target)
		if err != nil {
			return wrapBackendError(err, target.WorkloadID, "probe revision")
		}
		rev = probed
	}

	err := fn(ctx, rev)
	if err == nil || !isRevisionMismatch(rev, err) {
		return err
	}
	if !cached {
		// The revision was just probed authoritatively; a re-probe would agree, so
		// this is a genuine error (or a lenient backend), not a stale cache.
		return err
	}

	corrected := h.reclassify(ctx, target, rev)
	if corrected == rev {
		// Re-probe agreed with the cache: the mismatch was not a revision problem.
		return err
	}
	if errors.Is(err, errLegacyResponseBody) {
		// Cache is now corrected for future calls, but the backend may have
		// already executed this request — do NOT re-run it (no double-execution).
		return err
	}
	return fn(ctx, corrected)
}

// reclassify re-probes the backend authoritatively (overwriting the cached
// revision via probeRevision) and returns the corrected revision. On an actual
// era change it emits a WARN and increments the reclassification counter; a
// same-era re-probe (or a re-probe that can't run) is silent and returns prev.
func (h *httpBackendClient) reclassify(
	ctx context.Context, target *vmcp.BackendTarget, prev mcpparser.Revision,
) mcpparser.Revision {
	corrected, _, err := h.probeRevision(ctx, target)
	if err != nil {
		// Transport couldn't even be built to re-probe; keep the prior revision.
		return prev
	}
	if corrected != prev {
		slog.WarnContext(ctx, "backend MCP revision reclassified after mismatch",
			"backend", target.WorkloadID, "old", prev.String(), "new", corrected.String())
		backendtelemetry.RecordRevisionReclassification(ctx)
	}
	return corrected
}

// isRevisionMismatch reports whether err from an attempt made under rev signals
// that the backend actually speaks the OTHER MCP revision (triggering a cache
// reclassification). It is deliberately narrow, and keyed on rev because the same
// sentinel means different things per era:
//
//   - Modern attempt: errWrongEra (protocol rejection) or errLegacyResponseBody
//     (a Legacy-shaped success body). A data-plane -32601 comes back as
//     mcp.ErrMethodNotFound and is a real not-found on a genuine Modern backend,
//     NOT a mismatch.
//   - Legacy attempt: an initialize-STEP rejection only — errLegacyInitFailed
//     together with mcp.ErrMethodNotFound or transport.ErrLegacySSEServer. A
//     data-plane -32601 (a legitimately unimplemented method on a real Legacy
//     backend) lacks the errLegacyInitFailed marker and is NOT a mismatch.
//
// Auth failures are never a mismatch: their sentinels (ErrUnauthorized,
// ErrAuthorizationRequired, ErrUpstreamTokenNotFound, ErrAuthenticationFailed) do
// not wrap the era sentinels above, so they are excluded by construction.
//
// NOTE: mismatch != safe-to-retry. dispatch reclassifies on any mismatch but only
// re-runs fn when no execution could have occurred (see dispatch).
func isRevisionMismatch(rev mcpparser.Revision, err error) bool {
	if err == nil {
		return false
	}
	if rev == mcpparser.RevisionModern {
		return errors.Is(err, errWrongEra) || errors.Is(err, errLegacyResponseBody)
	}
	return errors.Is(err, errLegacyInitFailed) &&
		(errors.Is(err, mcp.ErrMethodNotFound) || errors.Is(err, transport.ErrLegacySSEServer))
}

// CallTool invokes a tool on the backend MCP server, selecting the Legacy or
// Modern (2026-07-28) path by the backend's resolved revision. Returns the
// complete tool result including _meta.
func (h *httpBackendClient) CallTool(
	ctx context.Context,
	target *vmcp.BackendTarget,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	slog.Debug("calling tool on backend", "tool", toolName, "backend", target.WorkloadName)
	var out *vmcp.ToolCallResult
	err := h.dispatch(ctx, target, func(ctx context.Context, rev mcpparser.Revision) error {
		var err error
		if rev == mcpparser.RevisionModern {
			out, err = h.modernCallTool(ctx, target, toolName, arguments, meta)
		} else {
			out, err = h.legacyCallTool(ctx, target, toolName, arguments, meta)
		}
		return err
	})
	return out, err
}

// modernCallTool invokes tools/call over the Modern (2026-07-28) shim. The
// advertised tool name is translated to the backend's capability name for BOTH
// the body identifier and the Mcp-Name header — the server rejects a mismatch
// (-32020). The caller's _meta is forwarded (modernCall strips reserved keys and
// overlays vMCP's) and the result _meta is forwarded back to core.
func (h *httpBackendClient) modernCallTool(
	ctx context.Context, target *vmcp.BackendTarget, toolName string, arguments, meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	backendToolName := target.GetBackendCapabilityName(toolName)
	if backendToolName != toolName {
		slog.Debug("translating tool name", "client_name", toolName, "backend_name", backendToolName)
	}
	hc, err := h.buildModernHTTPClient(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	params := map[string]any{"name": backendToolName, "arguments": arguments}
	if len(meta) > 0 {
		params["_meta"] = meta
	}
	var result mcp.CallToolResult
	if err := modernCall(ctx, hc, target.BaseURL, "tools/call", params, backendToolName, &result); err != nil {
		return nil, fmt.Errorf("%w: tool call failed on backend %s: %w", vmcp.ErrBackendUnavailable, target.WorkloadID, err)
	}
	return toolResultFromMCP(&result, toolName, target.WorkloadID), nil
}

// legacyCallTool is the initialize + SDK CallTool path (unchanged behavior).
//
//nolint:gocyclo // this function is complex because it handles tool calls with various content types and error handling.
func (h *httpBackendClient) legacyCallTool(
	ctx context.Context, target *vmcp.BackendTarget, toolName string, arguments, meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	// Create a client for this backend
	c, err := h.clientFactory(ctx, target, true)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			slog.Debug("failed to close client", "error", err)
		}
	}()

	// Initialize the client and capture the backend's advertised capabilities.
	serverCaps, err := h.legacyInit(ctx, c, target.WorkloadID)
	if err != nil {
		return nil, err
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
			Meta:      conversion.ToMCPMeta(telemetry.MetaWithTraceContext(ctx, meta)),
		},
	})
	if err != nil {
		// Network/connection errors are operational errors
		return nil, fmt.Errorf("%w: tool call failed on backend %s: %w", vmcp.ErrBackendUnavailable, target.WorkloadID, err)
	}

	// Flush the backend's server->client stream before the deferred Close tears
	// down this per-call client, so a fire-and-forget notification the backend
	// emitted mid-call (progress/logging) is relayed downstream instead of being
	// dropped. See drainServerToClientNotifications for the lost-notification race.
	h.drainServerToClientNotifications(ctx, c)

	return toolResultFromMCP(result, toolName, target.WorkloadID), nil
}

// toolResultFromMCP converts an mcp.CallToolResult into the vmcp domain result,
// shared by the Legacy and Modern tools/call paths so both handle IsError
// logging, structuredContent, and _meta forwarding identically.
func toolResultFromMCP(result *mcp.CallToolResult, toolName, backendID string) *vmcp.ToolCallResult {
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
				"tool", toolName, "backend", backendID, "error", errorMsg, "meta", responseMeta)
		} else {
			slog.Warn("tool returned IsError=true",
				"tool", toolName, "backend", backendID, "error", errorMsg)
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
			slog.Debug("using structured content from tool", "tool", toolName, "backend", backendID)
			structuredContent = structuredMap
		} else {
			// StructuredContent is not an object - fall through to Content processing
			slog.Debug("structuredContent is not an object, falling back to Content",
				"tool", toolName, "backend", backendID)
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
	}
}

// ReadResource retrieves a resource from the backend MCP server, selecting the
// Legacy or Modern path by revision. Returns the complete resource result
// including _meta.
func (h *httpBackendClient) ReadResource(
	ctx context.Context, target *vmcp.BackendTarget, uri string,
) (*vmcp.ResourceReadResult, error) {
	slog.Debug("reading resource from backend", "resource", uri, "backend", target.WorkloadName)
	var out *vmcp.ResourceReadResult
	err := h.dispatch(ctx, target, func(ctx context.Context, rev mcpparser.Revision) error {
		var err error
		if rev == mcpparser.RevisionModern {
			out, err = h.modernReadResource(ctx, target, uri)
		} else {
			out, err = h.legacyReadResource(ctx, target, uri)
		}
		return err
	})
	return out, err
}

// modernReadResource invokes resources/read over the Modern shim. The URI is
// translated to the backend's capability name for both the body and the Mcp-Name
// header (server rejects a mismatch), and the result _meta is forwarded to core.
func (h *httpBackendClient) modernReadResource(
	ctx context.Context, target *vmcp.BackendTarget, uri string,
) (*vmcp.ResourceReadResult, error) {
	backendURI := target.GetBackendCapabilityName(uri)
	if backendURI != uri {
		slog.Debug("translating resource URI", "client_uri", uri, "backend_uri", backendURI)
	}
	hc, err := h.buildModernHTTPClient(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	// mcp.ResourceContents is an interface with no JSON unmarshaler, so it cannot
	// decode directly. Decode the wire shape, rebuild the discriminated mcp types
	// (blob takes precedence, symmetric with conversion.ToMCPResourceContents),
	// then hand off to the SAME converter legacyReadResource uses so the
	// content->vmcp mapping is shared, not duplicated.
	var res struct {
		Contents []struct {
			URI      string `json:"uri"`
			MIMEType string `json:"mimeType"`
			Text     string `json:"text"`
			Blob     string `json:"blob"`
		} `json:"contents"`
		Meta map[string]any `json:"_meta"`
	}
	if err := modernCall(ctx, hc, target.BaseURL, "resources/read", map[string]any{"uri": backendURI}, backendURI, &res); err != nil {
		return nil, fmt.Errorf("resource read failed on backend %s: %w", target.WorkloadID, err)
	}
	mcpContents := make([]mcp.ResourceContents, len(res.Contents))
	for i, c := range res.Contents {
		if c.Blob != "" {
			mcpContents[i] = mcp.BlobResourceContents{URI: c.URI, MIMEType: c.MIMEType, Blob: c.Blob}
		} else {
			mcpContents[i] = mcp.TextResourceContents{URI: c.URI, MIMEType: c.MIMEType, Text: c.Text}
		}
	}
	return &vmcp.ResourceReadResult{
		Contents: conversion.ConvertMCPResourceContents(mcpContents),
		Meta:     res.Meta,
	}, nil
}

// legacyReadResource is the initialize + SDK ReadResource path (unchanged behavior).
func (h *httpBackendClient) legacyReadResource(
	ctx context.Context, target *vmcp.BackendTarget, uri string,
) (*vmcp.ResourceReadResult, error) {
	// Create a client for this backend
	c, err := h.clientFactory(ctx, target, false)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			slog.Debug("failed to close client", "error", err)
		}
	}()

	// Initialize the client
	if _, err := h.legacyInit(ctx, c, target.WorkloadID); err != nil {
		return nil, err
	}

	// Read the resource using the original URI from the backend's perspective.
	// When conflict resolution renames resources, we must use the original backend URI.
	backendURI := target.GetBackendCapabilityName(uri)
	if backendURI != uri {
		slog.Debug("translating resource URI", "client_uri", uri, "backend_uri", backendURI)
	}

	// Forward-compat: mcpcompat drops Params.Meta on this path today (no-op on
	// the wire). See TestOutboundMetaTraceContext for details/tripwire.
	result, err := c.ReadResource(ctx, mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{
			URI:  backendURI,
			Meta: conversion.ToMCPMeta(telemetry.MetaWithTraceContext(ctx, nil)),
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

// GetPrompt retrieves a prompt from the backend MCP server, selecting the Legacy
// or Modern path by revision. Returns the complete prompt result including _meta.
func (h *httpBackendClient) GetPrompt(
	ctx context.Context,
	target *vmcp.BackendTarget,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	slog.Debug("getting prompt from backend", "prompt", name, "backend", target.WorkloadName)
	var out *vmcp.PromptGetResult
	err := h.dispatch(ctx, target, func(ctx context.Context, rev mcpparser.Revision) error {
		var err error
		if rev == mcpparser.RevisionModern {
			out, err = h.modernGetPrompt(ctx, target, name, arguments)
		} else {
			out, err = h.legacyGetPrompt(ctx, target, name, arguments)
		}
		return err
	})
	return out, err
}

// modernGetPrompt invokes prompts/get over the Modern shim. The prompt name is
// translated to the backend's capability name for both the body and the Mcp-Name
// header (server rejects a mismatch); the result _meta is forwarded to core.
// Message content is decoded via mcp.UnmarshalContent so it goes through the same
// content conversion as the Legacy path.
func (h *httpBackendClient) modernGetPrompt(
	ctx context.Context, target *vmcp.BackendTarget, name string, arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	backendPromptName := target.GetBackendCapabilityName(name)
	if backendPromptName != name {
		slog.Debug("translating prompt name", "client_name", name, "backend_name", backendPromptName)
	}
	hc, err := h.buildModernHTTPClient(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	params := map[string]any{
		"name":      backendPromptName,
		"arguments": conversion.ConvertPromptArguments(arguments),
	}
	var res struct {
		Description string `json:"description"`
		Messages    []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Meta map[string]any `json:"_meta"`
	}
	if err := modernCall(ctx, hc, target.BaseURL, "prompts/get", params, backendPromptName, &res); err != nil {
		return nil, fmt.Errorf("prompt get failed on backend %s: %w", target.WorkloadID, err)
	}
	messages := make([]vmcp.PromptMessage, 0, len(res.Messages))
	for _, m := range res.Messages {
		content, err := mcp.UnmarshalContent(m.Content)
		if err != nil {
			return nil, fmt.Errorf("prompt get: decoding message content from backend %s: %w", target.WorkloadID, err)
		}
		messages = append(messages, vmcp.PromptMessage{
			Role:    m.Role,
			Content: conversion.ConvertMCPContent(content),
		})
	}
	return &vmcp.PromptGetResult{Messages: messages, Description: res.Description, Meta: res.Meta}, nil
}

// legacyGetPrompt is the initialize + SDK GetPrompt path (unchanged behavior).
func (h *httpBackendClient) legacyGetPrompt(
	ctx context.Context, target *vmcp.BackendTarget, name string, arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	// Create a client for this backend
	c, err := h.clientFactory(ctx, target, false)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			slog.Debug("failed to close client", "error", err)
		}
	}()

	// Initialize the client
	if _, err := h.legacyInit(ctx, c, target.WorkloadID); err != nil {
		return nil, err
	}

	// Get the prompt using the original prompt name from the backend's perspective.
	// When conflict resolution renames prompts, we must use the original backend name.
	backendPromptName := target.GetBackendCapabilityName(name)
	if backendPromptName != name {
		slog.Debug("translating prompt name", "client_name", name, "backend_name", backendPromptName)
	}

	stringArgs := conversion.ConvertPromptArguments(arguments)

	// Forward-compat: mcpcompat drops Params.Meta on this path today (no-op on
	// the wire). See TestOutboundMetaTraceContext for details/tripwire.
	result, err := c.GetPrompt(ctx, mcp.GetPromptRequest{
		Params: mcp.GetPromptParams{
			Name:      backendPromptName,
			Arguments: stringArgs,
			Meta:      conversion.ToMCPMeta(telemetry.MetaWithTraceContext(ctx, nil)),
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

// Complete requests argument-completion candidates from the backend MCP server,
// selecting the Legacy or Modern path by revision. Returns an empty (non-nil)
// result when the backend does not advertise completions, matching the MCP
// spec's lenient completion semantics.
func (h *httpBackendClient) Complete(
	ctx context.Context,
	target *vmcp.BackendTarget,
	ref vmcp.CompletionRef,
	argName, argValue string,
	contextArgs map[string]string,
) (*vmcp.CompletionResult, error) {
	slog.Debug("requesting completion from backend", "ref_type", ref.Type, "backend", target.WorkloadName)
	var out *vmcp.CompletionResult
	err := h.dispatch(ctx, target, func(ctx context.Context, rev mcpparser.Revision) error {
		var err error
		if rev == mcpparser.RevisionModern {
			out, err = h.modernComplete(ctx, target, ref, argName, argValue, contextArgs)
		} else {
			out, err = h.legacyComplete(ctx, target, ref, argName, argValue, contextArgs)
		}
		return err
	})
	return out, err
}

// modernComplete invokes completion/complete over the Modern shim (not a
// name-required method, so no Mcp-Name). A prompt ref's name is translated to the
// backend capability name. A backend without completions answers -32601, which is
// treated as an empty result (lenient completion semantics).
func (h *httpBackendClient) modernComplete(
	ctx context.Context, target *vmcp.BackendTarget,
	ref vmcp.CompletionRef, argName, argValue string, contextArgs map[string]string,
) (*vmcp.CompletionResult, error) {
	hc, err := h.buildModernHTTPClient(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	refMap, err := modernCompletionRef(target, ref)
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"ref":      refMap,
		"argument": map[string]any{"name": argName, "value": argValue},
	}
	if len(contextArgs) > 0 {
		params["context"] = map[string]any{"arguments": contextArgs}
	}
	var res struct {
		Completion struct {
			Values  []string `json:"values"`
			Total   int      `json:"total"`
			HasMore bool     `json:"hasMore"`
		} `json:"completion"`
	}
	err = modernCall(ctx, hc, target.BaseURL, "completion/complete", params, "", &res)
	if errors.Is(err, mcp.ErrMethodNotFound) {
		return &vmcp.CompletionResult{Values: []string{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("completion failed on backend %s: %w", target.WorkloadID, err)
	}
	values := res.Completion.Values
	if values == nil {
		values = []string{}
	}
	return &vmcp.CompletionResult{Values: values, Total: res.Completion.Total, HasMore: res.Completion.HasMore}, nil
}

// modernCompletionRef builds the Modern completion/complete ref params, mirroring
// buildCompletionRef's translation: a prompt ref's name is translated to the
// backend capability name; a resource ref's URI is passed through.
func modernCompletionRef(target *vmcp.BackendTarget, ref vmcp.CompletionRef) (map[string]any, error) {
	switch ref.Type {
	case vmcp.CompletionRefTypePrompt:
		backendName := target.GetBackendCapabilityName(ref.Name)
		if backendName != ref.Name {
			slog.Debug("translating prompt name for completion", "client_name", ref.Name, "backend_name", backendName)
		}
		return map[string]any{"type": ref.Type, "name": backendName}, nil
	case vmcp.CompletionRefTypeResource:
		return map[string]any{"type": ref.Type, "uri": ref.URI}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported completion ref type %q", vmcp.ErrInvalidInput, ref.Type)
	}
}

// legacyComplete is the initialize + SDK Complete path (unchanged behavior).
func (h *httpBackendClient) legacyComplete(
	ctx context.Context,
	target *vmcp.BackendTarget,
	ref vmcp.CompletionRef,
	argName, argValue string,
	contextArgs map[string]string,
) (*vmcp.CompletionResult, error) {
	// Create a client for this backend
	c, err := h.clientFactory(ctx, target, false)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			slog.Debug("failed to close client", "error", err)
		}
	}()

	// Initialize the client and capture the backend's advertised capabilities.
	serverCaps, err := h.legacyInit(ctx, c, target.WorkloadID)
	if err != nil {
		return nil, err
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

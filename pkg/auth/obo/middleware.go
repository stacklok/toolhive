// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package obo implements the proxy-runtime middleware for the on-behalf-of
// (OBO) external auth type.
//
// Per request the middleware takes the OBO subject (assertion) token from the
// authenticated Identity that the authentication middleware placed in the
// request context, exchanges it through a cached Microsoft Entra on-behalf-of
// Exchanger for a backend-scoped token, sets Authorization: Bearer <backend
// token> on the upstream request, and calls the downstream handler. The
// subject comes from one of two Identity sources, selected by
// MiddlewareParameters.SubjectTokenProviderName: the inbound token the client
// presented (Identity.Token, the default) or a named upstream provider's token
// (Identity.UpstreamTokens[name], when an embedded auth server fronts the
// proxy). On a conditional-access or claims challenge it fails fast with a
// generic 403 and an audit record, and the downstream handler is not called.
//
// The package, together with pkg/auth/obo/entra, pkg/auth/obo/cache, and
// pkg/auth/obo/exchange, is kept free of controller-runtime, client-go, and the
// operator API types, because the proxyrunner links it. A test in this package
// enforces that.
package obo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/obo/cache"
	"github.com/stacklok/toolhive/pkg/auth/obo/entra"
	"github.com/stacklok/toolhive/pkg/auth/obo/exchange"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// MiddlewareType is the type identifier used in MiddlewareConfig.Type for
// OBO middleware. Matches the ExternalAuthType constant value "obo".
const MiddlewareType = "obo"

const (
	// authorizationHeader is the upstream header the exchanged backend token is
	// written to. bearerPrefix is its RFC 6750 scheme.
	authorizationHeader = "Authorization"
	bearerPrefix        = "Bearer "
)

// The handler's response bodies are intentionally generic: they are visible to
// any caller, so they must not disclose the provider error, the claims
// challenge, or any configuration detail.
const (
	// unauthorizedMessage is returned (401) when there is no authenticated
	// identity, no subject token on it, or the IdP rejected the subject token
	// (invalid_grant).
	unauthorizedMessage = "missing or invalid user token"

	// forbiddenMessage is returned (403) on a fail-fast conditional-access or
	// claims challenge.
	forbiddenMessage = "OBO token exchange denied"

	// badGatewayMessage is returned (502) when the proxy could not obtain a
	// backend token for a reason that is not the caller's fault: a
	// confidential-client misconfiguration (invalid_client), a transient IdP
	// failure, or an unclassified error.
	badGatewayMessage = "OBO token exchange unavailable"
)

const (
	// AuditEventType is the AuditEvent.Type for an OBO exchange decision. The
	// proxy middleware and the vMCP strategy both emit it.
	AuditEventType = "obo_token_exchange"

	// auditComponent identifies the OBO proxy middleware as the source
	// component of the audit record.
	auditComponent = "toolhive-obo"
)

// factoryMu guards reads and writes of currentFactory.
var factoryMu sync.RWMutex

// currentFactory holds the factory CreateMiddleware dispatches to. It is the
// built-in DefaultFactory unless out-of-tree code replaced it through the
// deprecated RegisterFactory.
var currentFactory types.MiddlewareFactory = DefaultFactory

// CreateMiddleware is the package-level OBO middleware factory registered in
// the runner's middleware factory map. It dispatches to DefaultFactory unless
// a replacement was registered through RegisterFactory.
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	factoryMu.RLock()
	f := currentFactory
	factoryMu.RUnlock()
	return f(config, runner)
}

// RegisterFactory replaces the factory CreateMiddleware dispatches to.
// Last-write-wins. Panics if f is nil.
//
// Deprecated: OBO is built in and DefaultFactory is the implementation.
// RegisterFactory is kept only so out-of-tree code that still calls it keeps
// compiling; it will be removed in a future release.
func RegisterFactory(f types.MiddlewareFactory) {
	if f == nil {
		panic("obo.RegisterFactory: factory is nil")
	}
	factoryMu.Lock()
	defer factoryMu.Unlock()
	currentFactory = f
}

// DefaultFactory is the built-in OBO middleware factory. It parses the
// MiddlewareParameters contract, reads the client secret from the named
// environment variable, builds the cached Entra Exchanger, and installs the
// middleware under the config's type name. It fails loudly at startup
// (returns an error, installs nothing) on malformed parameters or a missing
// secret rather than deferring the failure to the first request.
func DefaultFactory(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	params, err := parseParameters(config.Parameters)
	if err != nil {
		return err
	}

	// The operator injects the secret value into the pod under this name; only
	// the name travels in the contract. The error names the env var (not its
	// value) so a missing injection is diagnosable.
	secret := os.Getenv(params.ClientSecretEnvVar)
	if secret == "" {
		return fmt.Errorf("obo middleware: client secret env var %q is empty or unset", params.ClientSecretEnvVar)
	}

	exchanger, err := entra.NewFromTokenURL(params.TokenURL, params.ClientID, secret, nil)
	if err != nil {
		return fmt.Errorf("obo middleware: build entra exchanger: %w", err)
	}

	var opts []cache.Option
	if params.CacheSkewSeconds > 0 {
		opts = append(opts, cache.WithSkew(time.Duration(params.CacheSkewSeconds)*time.Second))
	}

	mw := newMiddleware(cache.New(exchanger, opts...), params.SubjectTokenProviderName, params.ExchangeTarget(), nil)
	runner.AddMiddleware(config.Type, mw)
	return nil
}

// middleware exchanges the authenticated user's token for a backend-scoped
// token on every request and presents it upstream as Authorization: Bearer.
type middleware struct {
	exchanger      exchange.Exchanger
	providerName   string
	exchangeTarget string
	auditLogger    *slog.Logger
}

// newMiddleware constructs the middleware from an already-built Exchanger. An
// empty providerName selects the inbound-token source; a non-empty one selects
// that upstream provider's token. A nil auditLogger gets the default JSON
// audit logger.
func newMiddleware(exchanger exchange.Exchanger, providerName, exchangeTarget string, auditLogger *slog.Logger) *middleware {
	if auditLogger == nil {
		auditLogger = audit.NewAuditLogger(nil)
	}
	return &middleware{
		exchanger:      exchanger,
		providerName:   providerName,
		exchangeTarget: exchangeTarget,
		auditLogger:    auditLogger,
	}
}

// Handler returns a MiddlewareFunction that performs the per-request OBO
// exchange. On success it sets Authorization: Bearer <backend token> and calls
// downstream; on failure it short-circuits with a generic 401, 403, or 502 and
// never calls downstream.
func (m *middleware) Handler() types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// subjectToken writes the failure response itself when absent.
			userToken, ok := m.subjectToken(w, r)
			if !ok {
				return
			}

			backendToken, err := m.exchanger.Exchange(r.Context(), userToken, m.exchangeTarget)
			if err != nil {
				m.writeExchangeError(w, r, err)
				return
			}

			// Overwrite Authorization with the backend-scoped token so the raw
			// subject token never reaches the backend.
			r.Header.Set(authorizationHeader, bearerPrefix+backendToken)
			next.ServeHTTP(w, r)
		})
	}
}

// Close is a no-op: the cache holds no resource that needs releasing (it runs
// no background goroutine beyond the per-exchange flight).
func (*middleware) Close() error { return nil }

// subjectToken resolves the OBO subject (assertion) from the request's
// authenticated Identity. Inbound mode uses Identity.Token; upstream-provider
// mode uses Identity.UpstreamTokens[name]. On failure it writes a 401 (with a
// re-auth challenge for a missing provider token, mirroring upstreamswap) and
// returns ok=false.
func (m *middleware) subjectToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		http.Error(w, unauthorizedMessage, http.StatusUnauthorized)
		return "", false
	}

	if m.providerName != "" {
		token, ok := identity.UpstreamTokens[m.providerName]
		if !ok || token == "" {
			writeUpstreamAuthRequired(w)
			return "", false
		}
		return token, true
	}

	if identity.Token == "" {
		http.Error(w, unauthorizedMessage, http.StatusUnauthorized)
		return "", false
	}
	return identity.Token, true
}

// writeUpstreamAuthRequired writes a 401 with an RFC 6750 WWW-Authenticate
// Bearer challenge, signalling that the caller must re-authenticate with the
// upstream IdP so a provider token becomes available.
func writeUpstreamAuthRequired(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate",
		`Bearer error="invalid_token", error_description="upstream token is unavailable; re-authentication required"`)
	http.Error(w, unauthorizedMessage, http.StatusUnauthorized)
}

// writeExchangeError maps an exchange failure onto a generic HTTP response,
// without disclosing the provider error, the claims challenge, or any token or
// secret material. The conditional-access case also emits an audit record.
func (m *middleware) writeExchangeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, entra.ErrInteractionRequired):
		// Fail fast on a conditional-access or claims challenge: record the
		// decision and return a generic 403. No interactive step-up and no
		// claims passthrough.
		m.auditInteractionRequired(r, err)
		http.Error(w, forbiddenMessage, http.StatusForbidden)

	case errors.Is(err, entra.ErrInvalidGrant):
		// The end-user token was rejected by the IdP (expired, malformed, or
		// not consented for this backend).
		slog.DebugContext(r.Context(), "obo: subject token rejected by identity provider")
		http.Error(w, unauthorizedMessage, http.StatusUnauthorized)

	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The client (or its deadline) went away mid-exchange. Write no
		// response: a 502 would be a misleading 5xx for a request nobody is
		// waiting on.
		slog.DebugContext(r.Context(), "obo: token exchange cancelled before completion")

	default:
		// invalid_client, a transient IdP failure, or an unclassified error.
		// Only *entra.ExchangeError detail is logged: its Error() composes
		// solely non-sensitive fields. Any other error is logged without its
		// message so no implementation can leak credential material here.
		var exErr *entra.ExchangeError
		if errors.As(err, &exErr) {
			slog.WarnContext(r.Context(), "obo: token exchange failed", "error", exErr.Error())
		} else {
			slog.WarnContext(r.Context(), "obo: token exchange failed with an unclassified error")
		}
		http.Error(w, badGatewayMessage, http.StatusBadGateway)
	}
}

// auditInteractionRequired emits the audit record for a fail-fast
// conditional-access or claims challenge.
func (m *middleware) auditInteractionRequired(r *http.Request, err error) {
	identity, _ := auth.IdentityFromContext(r.Context())
	event := NewInteractionRequiredAuditEvent(
		audit.EventSource{Type: audit.SourceTypeNetwork, Value: clientAddr(r)},
		auditComponent, identity, m.exchangeTarget, err)
	event.LogTo(r.Context(), m.auditLogger, audit.LevelAudit)
}

// NewInteractionRequiredAuditEvent builds the audit event for an OBO exchange
// that Entra refused with a conditional-access or claims challenge. It records
// the denied subject (an identifier, not a credential) and only the
// non-sensitive fields the entra error carries: OAuthError, Description,
// HTTPStatus, and whether a claims challenge was present. It never records the
// user token, the client secret, an issued token, or the raw claims challenge.
//
// Shared by the proxy middleware and the vMCP strategy so both surfaces emit
// the same record shape. identity may be nil.
func NewInteractionRequiredAuditEvent(
	source audit.EventSource, component string, identity *auth.Identity, target string, err error,
) *audit.AuditEvent {
	var subjects map[string]string
	if identity != nil && identity.Subject != "" {
		subjects = map[string]string{"sub": identity.Subject}
	}

	event := audit.NewAuditEvent(AuditEventType, source, audit.OutcomeDenied, subjects, component).
		WithTarget(map[string]string{"audience": target})

	extra := map[string]any{"reason": "interaction_required"}
	var exErr *entra.ExchangeError
	if errors.As(err, &exErr) {
		extra["oauth_error"] = exErr.OAuthError
		extra["description"] = exErr.Description
		extra["http_status"] = exErr.HTTPStatus
		extra["claims_challenge_present"] = exErr.Claims != ""
	}
	event.Metadata.Extra = extra
	return event
}

// clientAddr returns the client's IP for the audit source, stripping the port
// from RemoteAddr when present.
func clientAddr(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// parseParameters unmarshals and validates the MiddlewareParameters contract.
// Unknown fields are tolerated so the contract can grow additively.
func parseParameters(raw json.RawMessage) (*MiddlewareParameters, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("obo middleware: parameters are required")
	}

	var p MiddlewareParameters
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("obo middleware: invalid parameters: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

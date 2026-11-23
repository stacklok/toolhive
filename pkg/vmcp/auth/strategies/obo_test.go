// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	"github.com/stacklok/toolhive/pkg/auth/obo/entra"
	"github.com/stacklok/toolhive/pkg/auth/obo/exchange"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

const (
	oboTestTokenURL = "https://login.microsoftonline.com/contoso.onmicrosoft.com/oauth2/v2.0/token"
	oboTestClientID = "client-123"
	oboTestSubject  = "user-123"
	oboTestScope    = "api://backend/.default"
)

// oboStubExchanger is a configurable exchange.Exchanger test double recording the last
// call's arguments.
type oboStubExchanger struct {
	token         string
	err           error
	calls         int
	lastUserToken string
	lastAudience  string
}

var _ exchange.Exchanger = (*oboStubExchanger)(nil)

func (s *oboStubExchanger) Exchange(_ context.Context, userToken, audience string) (string, error) {
	s.calls++
	s.lastUserToken = userToken
	s.lastAudience = audience
	return s.token, s.err
}

// oboMapEnv is a minimal env.Reader backed by a map.
type oboMapEnv map[string]string

var _ env.Reader = oboMapEnv(nil)

func (m oboMapEnv) Getenv(key string) string { return m[key] }

func (m oboMapEnv) LookupEnv(key string) (string, bool) {
	v, ok := m[key]
	return v, ok
}

// oboStrategyWithStub returns a Strategy whose Exchanger is always stub and whose
// audit records go to auditLogger (nil for the default).
func oboStrategyWithStub(stub *oboStubExchanger, auditLogger *slog.Logger) *OBOStrategy {
	return newOBOStrategy(oboMapEnv{}, auditLogger, func(_ *authtypes.OBOConfig, _ string) (exchange.Exchanger, error) {
		return stub, nil
	})
}

// validOBOStrategy returns a well-formed obo BackendAuthStrategy with the client
// secret already resolved (the discovered-auth state at request time).
func validOBOStrategy() *authtypes.BackendAuthStrategy {
	return &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeOBO,
		OBO: &authtypes.OBOConfig{
			TokenURL:     oboTestTokenURL,
			ClientID:     oboTestClientID,
			ClientSecret: "resolved-secret",
			Scopes:       []string{oboTestScope},
		},
	}
}

// oboInboundIdentity returns an Identity carrying token as the inbound subject.
func oboInboundIdentity(token string) *auth.Identity {
	return &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{Subject: oboTestSubject},
		Token:         token,
	}
}

// oboServeAuthenticate runs Authenticate against a request carrying ctx and returns
// the request (for header inspection) and the error.
func oboServeAuthenticate(
	ctx context.Context, t *testing.T, s *OBOStrategy, backendStrategy *authtypes.BackendAuthStrategy,
) (*http.Request, error) {
	t.Helper()
	// Authenticate runs on the outgoing backend-bound request (the vMCP client's
	// round-tripper), which — like the real call path — carries no inbound client
	// RemoteAddr; the audit source is therefore local to the component, not a
	// network IP.
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://backend.local/", nil)
	return req, s.Authenticate(ctx, req, backendStrategy)
}

func TestOBOStrategy_Name(t *testing.T) {
	t.Parallel()
	assert.Equal(t, authtypes.StrategyTypeOBO, (&OBOStrategy{}).Name())
}

// TestOBOAuthenticate_InboundSuccess exchanges the inbound token and injects the
// backend token, sending Entra the collapsed scope.
func TestOBOAuthenticate_InboundSuccess(t *testing.T) {
	t.Parallel()

	stub := &oboStubExchanger{token: "backend-token"}
	s := oboStrategyWithStub(stub, nil)
	ctx := auth.WithIdentity(t.Context(), oboInboundIdentity("user-token"))

	req, err := oboServeAuthenticate(ctx, t, s, validOBOStrategy())
	require.NoError(t, err)

	assert.Equal(t, "Bearer backend-token", req.Header.Get("Authorization"))
	assert.Equal(t, "user-token", stub.lastUserToken)
	assert.Equal(t, oboTestScope, stub.lastAudience, "the exchange target is the collapsed scope")
}

// TestOBOAuthenticate_UpstreamProviderSuccess uses a named upstream provider's token
// as the subject when SubjectTokenProviderName is set.
func TestOBOAuthenticate_UpstreamProviderSuccess(t *testing.T) {
	t.Parallel()

	stub := &oboStubExchanger{token: "backend-token"}
	s := oboStrategyWithStub(stub, nil)

	backendStrategy := validOBOStrategy()
	backendStrategy.OBO.SubjectTokenProviderName = "entra"
	identity := &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: oboTestSubject},
		Token:          "session-token", // the proxy's own token, not the subject
		UpstreamTokens: map[string]string{"entra": "upstream-token"},
	}
	ctx := auth.WithIdentity(t.Context(), identity)

	req, err := oboServeAuthenticate(ctx, t, s, backendStrategy)
	require.NoError(t, err)
	assert.Equal(t, "Bearer backend-token", req.Header.Get("Authorization"))
	assert.Equal(t, "upstream-token", stub.lastUserToken, "the named provider token is the subject")
}

// TestOBOAuthenticate_SubjectErrors covers the missing-identity / missing-token
// failure modes: each returns an error and injects nothing.
func TestOBOAuthenticate_SubjectErrors(t *testing.T) {
	t.Parallel()

	withProvider := func() *authtypes.BackendAuthStrategy {
		s := validOBOStrategy()
		s.OBO.SubjectTokenProviderName = "entra"
		return s
	}

	tests := []struct {
		name            string
		identity        *auth.Identity // nil means no identity in the context
		backendStrategy func() *authtypes.BackendAuthStrategy
		wantErrIs       error
		wantErrContains string
	}{
		{
			name:            "no identity in context",
			backendStrategy: validOBOStrategy,
			wantErrContains: "no identity found in context",
		},
		{
			name:            "identity has no inbound token",
			identity:        oboInboundIdentity(""),
			backendStrategy: validOBOStrategy,
			wantErrContains: "identity has no token",
		},
		{
			// The inbound token is present but must not be used as a fallback.
			name:            "named provider token absent",
			identity:        oboInboundIdentity("user-token"),
			backendStrategy: withProvider,
			wantErrIs:       authtypes.ErrUpstreamTokenNotFound,
			wantErrContains: `provider "entra"`,
		},
		{
			name: "named provider token empty",
			identity: &auth.Identity{
				PrincipalInfo:  auth.PrincipalInfo{Subject: oboTestSubject},
				Token:          "user-token",
				UpstreamTokens: map[string]string{"entra": ""},
			},
			backendStrategy: withProvider,
			wantErrIs:       authtypes.ErrUpstreamTokenNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stub := &oboStubExchanger{token: "unused"}
			s := oboStrategyWithStub(stub, nil)

			ctx := t.Context()
			if tt.identity != nil {
				ctx = auth.WithIdentity(ctx, tt.identity)
			}
			req, err := oboServeAuthenticate(ctx, t, s, tt.backendStrategy())
			require.Error(t, err)
			if tt.wantErrIs != nil {
				assert.ErrorIs(t, err, tt.wantErrIs)
			}
			if tt.wantErrContains != "" {
				assert.ErrorContains(t, err, tt.wantErrContains)
			}
			assert.Empty(t, req.Header.Get("Authorization"), "no token may be injected on failure")
			assert.Zero(t, stub.calls, "no exchange should be attempted")
		})
	}
}

// TestOBOAuthenticate_ConfigAndBuildErrors covers the failures that occur before
// any exchange: an invalid configuration (also under a health-check context,
// because the config is validated before the health-check skip), a
// ClientSecretEnv naming an unset variable, and an Exchanger that fails to build.
func TestOBOAuthenticate_ConfigAndBuildErrors(t *testing.T) {
	t.Parallel()

	buildErr := errors.New("exchanger build failed")
	withSecretEnv := func() *authtypes.BackendAuthStrategy {
		s := validOBOStrategy()
		s.OBO.ClientSecret = ""
		s.OBO.ClientSecretEnv = "OBO_TEST_SECRET_NOT_SET"
		return s
	}
	nilOBO := func() *authtypes.BackendAuthStrategy {
		return &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeOBO}
	}

	tests := []struct {
		name            string
		healthCheck     bool
		backendStrategy func() *authtypes.BackendAuthStrategy
		buildErr        error
		wantErrIs       error
		wantErrContains []string
		wantBuilds      int32
	}{
		{
			name:            "invalid configuration",
			backendStrategy: nilOBO,
			wantErrContains: []string{"obo: invalid strategy configuration", "OBO configuration is required"},
		},
		{
			name:            "invalid configuration fails a health check too",
			healthCheck:     true,
			backendStrategy: nilOBO,
			wantErrContains: []string{"obo: invalid strategy configuration", "OBO configuration is required"},
		},
		{
			name:            "client secret env var absent from the environment",
			backendStrategy: withSecretEnv,
			wantErrContains: []string{"obo: invalid strategy configuration", "OBO_TEST_SECRET_NOT_SET"},
		},
		{
			name:            "exchanger build fails",
			backendStrategy: validOBOStrategy,
			buildErr:        buildErr,
			wantErrIs:       buildErr,
			wantErrContains: []string{"obo: build exchanger"},
			wantBuilds:      1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stub := &oboStubExchanger{token: "unused"}
			var builds atomic.Int32
			s := newOBOStrategy(oboMapEnv{}, nil, func(_ *authtypes.OBOConfig, _ string) (exchange.Exchanger, error) {
				builds.Add(1)
				if tt.buildErr != nil {
					return nil, tt.buildErr
				}
				return stub, nil
			})

			ctx := auth.WithIdentity(t.Context(), oboInboundIdentity("user-token"))
			if tt.healthCheck {
				ctx = healthcontext.WithHealthCheckMarker(ctx)
			}
			req, err := oboServeAuthenticate(ctx, t, s, tt.backendStrategy())
			require.Error(t, err)
			if tt.wantErrIs != nil {
				assert.ErrorIs(t, err, tt.wantErrIs)
			}
			for _, want := range tt.wantErrContains {
				assert.ErrorContains(t, err, want)
			}
			assert.Empty(t, req.Header.Get("Authorization"))
			assert.Equal(t, tt.wantBuilds, builds.Load())
			assert.Zero(t, stub.calls, "no exchange may be attempted")
		})
	}
}

// TestOBOAuthenticate_ContextErrorsPassThrough verifies a cancelled or expired
// caller context surfaces as itself, not as a token exchange failure.
func TestOBOAuthenticate_ContextErrorsPassThrough(t *testing.T) {
	t.Parallel()

	for _, ctxErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(ctxErr.Error(), func(t *testing.T) {
			t.Parallel()

			stub := &oboStubExchanger{err: fmt.Errorf("entra obo: %w", ctxErr)}
			s := oboStrategyWithStub(stub, nil)
			ctx := auth.WithIdentity(t.Context(), oboInboundIdentity("user-token"))

			req, err := oboServeAuthenticate(ctx, t, s, validOBOStrategy())
			require.ErrorIs(t, err, ctxErr)
			assert.NotContains(t, err.Error(), "token exchange failed")
			assert.Empty(t, req.Header.Get("Authorization"))
		})
	}
}

// TestOBOAuthenticate_HealthCheckSkips proves a health probe (no user identity)
// skips authentication rather than failing.
func TestOBOAuthenticate_HealthCheckSkips(t *testing.T) {
	t.Parallel()

	stub := &oboStubExchanger{token: "unused"}
	s := oboStrategyWithStub(stub, nil)
	ctx := healthcontext.WithHealthCheckMarker(t.Context())

	req, err := oboServeAuthenticate(ctx, t, s, validOBOStrategy())
	require.NoError(t, err)
	assert.Empty(t, req.Header.Get("Authorization"))
	assert.Zero(t, stub.calls)
}

// TestOBOAuthenticate_ConditionalAccess_FailsFastWithAudit proves a conditional-
// access / claims challenge returns an error, injects nothing, and emits an
// audit record that names the denied subject but discloses neither the user
// token nor the raw claims challenge.
func TestOBOAuthenticate_ConditionalAccess_FailsFastWithAudit(t *testing.T) {
	t.Parallel()

	const (
		userToken = "user-token-secret"
		claims    = `{"access_token":{"acrs":{"essential":true}}}`
	)
	exErr := &entra.ExchangeError{
		Kind:        entra.KindInteractionRequired,
		OAuthError:  "interaction_required",
		Description: "AADSTS50076: MFA required",
		Claims:      claims,
		HTTPStatus:  http.StatusUnauthorized,
	}
	require.ErrorIs(t, exErr, entra.ErrInteractionRequired)

	var auditBuf bytes.Buffer
	auditLogger := slog.New(slog.NewJSONHandler(&auditBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	stub := &oboStubExchanger{err: exErr}
	s := oboStrategyWithStub(stub, auditLogger)
	ctx := auth.WithIdentity(t.Context(), oboInboundIdentity(userToken))

	req, err := oboServeAuthenticate(ctx, t, s, validOBOStrategy())
	require.Error(t, err)
	assert.ErrorIs(t, err, entra.ErrInteractionRequired)
	assert.Empty(t, req.Header.Get("Authorization"), "fail-fast must not inject a token")

	out := auditBuf.String()
	assert.NotEmpty(t, out, "a conditional-access denial must emit an audit record")
	assert.Contains(t, out, obo.AuditEventType)
	assert.Contains(t, out, "denied")
	assert.Contains(t, out, "interaction_required")
	assert.Contains(t, out, oboAuditComponent, "the audit source is local to this outgoing-auth component")
	assert.Contains(t, out, oboTestSubject, "the audit record identifies the denied subject")
	assert.NotContains(t, out, userToken, "the audit record must not contain the user token")
	assert.NotContains(t, out, claims, "the audit record must not contain the raw claims challenge")
}

// TestOBOAuthenticate_ExchangeErrorMapping proves every exchange failure
// returns an error that still matches its entra sentinel but carries none of
// Entra's provider detail (the error reaches MCP clients via the tool error),
// injects nothing, and that only a conditional-access denial emits the audit
// record — which keeps the detail server-side. Every other failure keeps it in
// the diagnostic log instead, at DEBUG for invalid_grant and WARN otherwise; a
// non-entra error is logged without its message.
func TestOBOAuthenticate_ExchangeErrorMapping(t *testing.T) {
	t.Parallel()

	const (
		description = "AADSTS7000215: PROVIDER-DIAGNOSTIC-DETAIL"
		oauthError  = "provider_error_code_detail"
	)
	exErr := func(kind entra.ErrorKind) error {
		return &entra.ExchangeError{Kind: kind, OAuthError: oauthError, Description: description, HTTPStatus: 400}
	}

	tests := []struct {
		name         string
		err          error
		wantSentinel error // nil: matches none of the entra sentinels
		wantAudit    bool
		// wantLogLevel is the diagnostic log level; "" means no diagnostic log.
		wantLogLevel string
		// wantLogDetail is whether the diagnostic log carries Entra's detail.
		wantLogDetail bool
		// wantMessage, when set, is the exact client-facing error text.
		wantMessage string
	}{
		{name: "interaction required", err: exErr(entra.KindInteractionRequired),
			wantSentinel: entra.ErrInteractionRequired, wantAudit: true},
		{name: "invalid grant", err: exErr(entra.KindInvalidGrant), wantSentinel: entra.ErrInvalidGrant,
			wantLogLevel: "DEBUG", wantLogDetail: true},
		{name: "invalid client", err: exErr(entra.KindInvalidClient), wantSentinel: entra.ErrInvalidClient,
			wantLogLevel: "WARN", wantLogDetail: true, wantMessage: "obo: token exchange failed"},
		{name: "transient", err: exErr(entra.KindTransient), wantSentinel: entra.ErrTransient,
			wantLogLevel: "WARN", wantLogDetail: true},
		{name: "unknown kind", err: exErr(entra.KindUnknown),
			wantLogLevel: "WARN", wantLogDetail: true, wantMessage: "obo: token exchange failed"},
		{name: "wrapped exchange error", err: fmt.Errorf("cache: %w", exErr(entra.KindInvalidClient)),
			wantSentinel: entra.ErrInvalidClient, wantLogLevel: "WARN", wantLogDetail: true,
			wantMessage: "obo: token exchange failed"},
		{name: "non-entra error", err: errors.New("unexpected failure " + description),
			wantLogLevel: "WARN", wantMessage: "obo: token exchange failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var auditBuf bytes.Buffer
			auditLogger := slog.New(slog.NewJSONHandler(&auditBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			var logBuf bytes.Buffer
			stub := &oboStubExchanger{err: tt.err}
			s := oboStrategyWithStub(stub, auditLogger)
			s.logger = slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			ctx := auth.WithIdentity(t.Context(), oboInboundIdentity("user-token"))

			req, err := oboServeAuthenticate(ctx, t, s, validOBOStrategy())
			require.Error(t, err)
			assert.Empty(t, req.Header.Get("Authorization"))
			if tt.wantMessage != "" {
				assert.Equal(t, tt.wantMessage, err.Error())
			}

			logOut := logBuf.String()
			if tt.wantLogLevel == "" {
				assert.Empty(t, logOut, "this failure is recorded in the audit log, not the diagnostic log")
			} else {
				assert.Contains(t, logOut, `"level":"`+tt.wantLogLevel+`"`)
				if tt.wantLogDetail {
					assert.Contains(t, logOut, description, "the diagnostic log keeps Entra's description")
					assert.Contains(t, logOut, oauthError, "the diagnostic log keeps Entra's error code")
				} else {
					assert.Contains(t, logOut, "unclassified error")
					assert.NotContains(t, logOut, description, "a non-entra error's text must not be logged")
				}
			}

			assert.NotContains(t, err.Error(), description, "Entra's error_description must not reach the client")
			assert.NotContains(t, err.Error(), oauthError, "Entra's error code must not reach the client")
			var leaked *entra.ExchangeError
			assert.NotErrorAs(t, err, &leaked, "the returned error must not wrap the ExchangeError")

			sentinels := []error{entra.ErrInteractionRequired, entra.ErrInvalidGrant, entra.ErrInvalidClient, entra.ErrTransient}
			for _, sentinel := range sentinels {
				if sentinel == tt.wantSentinel {
					assert.ErrorIs(t, err, sentinel)
				} else {
					assert.NotErrorIs(t, err, sentinel)
				}
			}

			if tt.wantAudit {
				assert.Contains(t, auditBuf.String(), description, "the audit record keeps the provider detail server-side")
			} else {
				assert.Empty(t, auditBuf.String(), "only conditional-access denials emit the audit record")
			}
		})
	}
}

// TestOBOAuthenticate_CachesExchangerPerConfig proves the per-backend Exchanger is
// built once and reused across requests, so the per-user token cache is shared.
func TestOBOAuthenticate_CachesExchangerPerConfig(t *testing.T) {
	t.Parallel()

	builds := 0
	stub := &oboStubExchanger{token: "backend-token"}
	s := newOBOStrategy(oboMapEnv{}, nil, func(_ *authtypes.OBOConfig, _ string) (exchange.Exchanger, error) {
		builds++
		return stub, nil
	})
	ctx := auth.WithIdentity(t.Context(), oboInboundIdentity("user-token"))

	for range 3 {
		_, err := oboServeAuthenticate(ctx, t, s, validOBOStrategy())
		require.NoError(t, err)
	}
	assert.Equal(t, 1, builds, "the exchanger must be built once and cached per backend config")
}

// TestOBOAuthenticate_RealEntraExchange exercises the production path end-to-end:
// NewOBOStrategy (default buildExchanger) wires entra.NewFromTokenURL + cache.New /
// WithSkew, and Authenticate performs real HTTP OBO exchanges against an httptest
// server standing in for the Entra token endpoint (loopback http, which
// NewFromTokenURL permits). The server mints a token per assertion and counts
// hits, so each row pins how many exchanges a sequence of requests costs: the
// per-user cache is shared across requests and partitioned by user, and the
// configured CacheSkewSeconds reaches the cache (zero and unset keep the 60s
// default).
func TestOBOAuthenticate_RealEntraExchange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		skew      *int32
		expiresIn int
		users     []string
		wantHits  int32
	}{
		{name: "unset skew, long-lived token is cached", expiresIn: 3599,
			users: []string{"alice", "alice"}, wantHits: 1},
		{name: "zero skew, long-lived token is cached", skew: ptrInt32(0), expiresIn: 3599,
			users: []string{"alice", "alice"}, wantHits: 1},
		{name: "zero skew keeps the 60s default", skew: ptrInt32(0), expiresIn: 30,
			users: []string{"alice", "alice"}, wantHits: 2},
		{name: "token lifetime shorter than the skew is re-exchanged", skew: ptrInt32(120), expiresIn: 90,
			users: []string{"alice", "alice"}, wantHits: 2},
		{name: "skew shorter than the token lifetime caches", skew: ptrInt32(10), expiresIn: 30,
			users: []string{"alice", "alice"}, wantHits: 1},
		{name: "a second user gets its own exchange", expiresIn: 3599,
			users: []string{"alice", "alice", "bob", "bob"}, wantHits: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				// assert, not require: this runs on the server's goroutine.
				if !assert.NoError(t, r.ParseForm()) {
					http.Error(w, "bad form", http.StatusBadRequest)
					return
				}
				assert.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", r.Form.Get("grant_type"),
					"the exchange must use the Entra on-behalf-of (jwt-bearer) grant")
				assert.Empty(t, r.Header.Get("Authorization"),
					"the subject token must travel in the form body, not an Authorization header")
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"token_type":"Bearer","expires_in":%d,"access_token":"backend-for-%s"}`,
					tt.expiresIn, r.Form.Get("assertion"))
			}))
			t.Cleanup(srv.Close)

			s := NewOBOStrategy(oboMapEnv{})
			backendStrategy := &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeOBO,
				OBO: &authtypes.OBOConfig{
					TokenURL:         srv.URL, // loopback http — accepted by NewFromTokenURL
					ClientID:         oboTestClientID,
					ClientSecret:     "resolved-secret",
					Scopes:           []string{oboTestScope},
					CacheSkewSeconds: tt.skew,
				},
			}

			for _, user := range tt.users {
				ctx := auth.WithIdentity(t.Context(), oboInboundIdentity(user))
				req, err := oboServeAuthenticate(ctx, t, s, backendStrategy)
				require.NoError(t, err)
				assert.Equal(t, "Bearer backend-for-"+user, req.Header.Get("Authorization"),
					"each user must receive the token exchanged for their own assertion")
			}
			assert.Equal(t, tt.wantHits, hits.Load())
		})
	}
}

func ptrInt32(v int32) *int32 { return &v }

// TestOBOExchangerCacheKey verifies every field that determines the Exchanger
// partitions the per-backend cache, and that the raw client secret never
// appears in the key.
func TestOBOExchangerCacheKey(t *testing.T) {
	t.Parallel()

	const secret = "client-secret-RAW-VALUE"
	base := func() *authtypes.OBOConfig {
		return &authtypes.OBOConfig{TokenURL: oboTestTokenURL, ClientID: oboTestClientID, CacheSkewSeconds: ptrInt32(30)}
	}
	baseKey := oboExchangerCacheKey(base(), secret)
	assert.NotContains(t, baseKey, secret, "the raw client secret must not appear in the key")
	assert.Equal(t, baseKey, oboExchangerCacheKey(base(), secret), "the key must be deterministic")

	tests := []struct {
		name   string
		mutate func(*authtypes.OBOConfig)
		secret string
	}{
		{name: "token URL", mutate: func(c *authtypes.OBOConfig) { c.TokenURL = "https://login.microsoftonline.us/t/oauth2/v2.0/token" }},
		{name: "client ID", mutate: func(c *authtypes.OBOConfig) { c.ClientID = "other-client" }},
		{name: "cache skew", mutate: func(c *authtypes.OBOConfig) { c.CacheSkewSeconds = ptrInt32(31) }},
		{name: "client secret", secret: "rotated-secret-RAW-VALUE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base()
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			sec := secret
			if tt.secret != "" {
				sec = tt.secret
			}
			key := oboExchangerCacheKey(cfg, sec)
			assert.NotEqual(t, baseKey, key, "changing the %s must select a different Exchanger", tt.name)
			assert.NotContains(t, key, sec, "the raw client secret must not appear in the key")
		})
	}
}

// barrierExchangerStore wraps an oboExchangerStore and holds every caller
// after a lookup miss until wantMisses callers have missed (or the timeout
// passes), so they all reach lookupOrStore together.
type barrierExchangerStore struct {
	oboExchangerStore
	wantMisses int32
	missed     atomic.Int32
	allMissed  chan struct{}
}

func (b *barrierExchangerStore) lookup(key string) (exchange.Exchanger, bool) {
	ex, ok := b.oboExchangerStore.lookup(key)
	if ok {
		return ex, ok
	}
	if b.missed.Add(1) == b.wantMisses {
		close(b.allMissed)
	}
	select {
	case <-b.allMissed:
	case <-time.After(5 * time.Second):
		// Fall through: the build count assertion reports the failure.
	}
	return nil, false
}

// TestOBOGetOrCreateExchanger_BuildsOnceUnderContention exercises the
// double-checked build deterministically: a wrapping store holds every caller
// after its lookup miss until all of them have missed, so they all then
// contend in lookupOrStore. Every caller after the first must find the
// Exchanger on the second check rather than building another one.
func TestOBOGetOrCreateExchanger_BuildsOnceUnderContention(t *testing.T) {
	t.Parallel()

	const callers = 8
	cfg := validOBOStrategy().OBO

	var builds atomic.Int32
	s := newOBOStrategy(oboMapEnv{}, nil, func(_ *authtypes.OBOConfig, _ string) (exchange.Exchanger, error) {
		builds.Add(1)
		return &oboStubExchanger{}, nil
	})

	store := &barrierExchangerStore{
		oboExchangerStore: s.exchangers,
		wantMisses:        callers,
		allMissed:         make(chan struct{}),
	}
	s.exchangers = store

	results := make(chan error, callers)
	for range callers {
		go func() {
			ex, err := s.getOrCreateExchanger(cfg, "resolved-secret")
			if err == nil && ex == nil {
				err = errors.New("nil exchanger")
			}
			results <- err
		}()
	}
	for range callers {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for getOrCreateExchanger callers")
		}
	}

	require.Equal(t, int32(callers), store.missed.Load(), "every caller must miss on the read-locked check")
	assert.Equal(t, int32(1), builds.Load(),
		"callers queued on the write lock must reuse the Exchanger built by the first")
}

func TestOBOValidate_Valid(t *testing.T) {
	t.Parallel()
	s := NewOBOStrategy(&env.OSReader{})
	require.NoError(t, s.Validate(validOBOStrategy()))
}

// TestOBOValidate_Invalid rejects malformed strategy configurations.
func TestOBOValidate_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*authtypes.BackendAuthStrategy)
		wantMsg string
	}{
		{name: "nil obo", mutate: func(s *authtypes.BackendAuthStrategy) { s.OBO = nil }, wantMsg: "OBO configuration is required"},
		{name: "missing token url", mutate: func(s *authtypes.BackendAuthStrategy) { s.OBO.TokenURL = "" }, wantMsg: "tokenUrl is required"},
		{name: "whitespace token url", mutate: func(s *authtypes.BackendAuthStrategy) { s.OBO.TokenURL = " \t " }, wantMsg: "tokenUrl is required"},
		{name: "missing client id", mutate: func(s *authtypes.BackendAuthStrategy) { s.OBO.ClientID = "" }, wantMsg: "clientId is required"},
		{name: "whitespace client id", mutate: func(s *authtypes.BackendAuthStrategy) { s.OBO.ClientID = "  " }, wantMsg: "clientId is required"},
		{name: "no audience or scopes", mutate: func(s *authtypes.BackendAuthStrategy) { s.OBO.Scopes = nil; s.OBO.Audience = "" }, wantMsg: "one of audience or scopes is required"},
		{name: "both secret sources", mutate: func(s *authtypes.BackendAuthStrategy) { s.OBO.ClientSecretEnv = "ENV" }, wantMsg: "mutually exclusive"},
		{name: "no secret source", mutate: func(s *authtypes.BackendAuthStrategy) { s.OBO.ClientSecret = "" }, wantMsg: "a client secret is required"},
		{name: "negative cache skew", mutate: func(s *authtypes.BackendAuthStrategy) {
			skew := int32(-1)
			s.OBO.CacheSkewSeconds = &skew
		}, wantMsg: "cacheSkewSeconds must not be negative"},
	}

	s := NewOBOStrategy(oboMapEnv{}) // empty env so ClientSecretEnv cannot resolve
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			backendStrategy := validOBOStrategy()
			tt.mutate(backendStrategy)
			err := s.Validate(backendStrategy)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantMsg, "the rejection must name the actual reason")
		})
	}
}

// TestOBOExchangeTarget_ParityWithSharedContract is the cross-path parity check: for
// a shared (audience, scopes) table, the value the vMCP strategy sends to Entra
// must equal obo.MiddlewareParameters.ExchangeTarget() — the single collapse
// implementation the proxy-runtime middleware also uses. Asserted both directly
// and behaviorally (the stub Exchanger records what Authenticate sent).
func TestOBOExchangeTarget_ParityWithSharedContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		audience string
		scopes   []string
	}{
		{name: "scopes win over audience", audience: "api://backend", scopes: []string{"api://backend/.default"}},
		{name: "multiple scopes space-joined", audience: "", scopes: []string{"openid", "api://backend/.default"}},
		{name: "audience only", audience: "api://backend", scopes: nil},
		{name: "whitespace-only scope falls back to audience", audience: "api://backend", scopes: []string{"   "}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &authtypes.OBOConfig{Audience: tt.audience, Scopes: tt.scopes}
			want := (&obo.MiddlewareParameters{Audience: tt.audience, Scopes: tt.scopes}).ExchangeTarget()

			// Direct parity: the strategy's collapse equals the shared contract's.
			assert.Equal(t, want, oboExchangeTarget(cfg))

			// Behavioral parity: Authenticate sends exactly that target to Entra.
			stub := &oboStubExchanger{token: "backend-token"}
			s := oboStrategyWithStub(stub, nil)
			backendStrategy := validOBOStrategy()
			backendStrategy.OBO.Audience = tt.audience
			backendStrategy.OBO.Scopes = tt.scopes
			ctx := auth.WithIdentity(t.Context(), oboInboundIdentity("user-token"))

			_, err := oboServeAuthenticate(ctx, t, s, backendStrategy)
			require.NoError(t, err)
			assert.Equal(t, want, stub.lastAudience)
		})
	}
}

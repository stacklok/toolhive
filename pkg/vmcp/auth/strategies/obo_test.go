// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

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
	ctx context.Context, t *testing.T, s *OBOStrategy, strat *authtypes.BackendAuthStrategy,
) (*http.Request, error) {
	t.Helper()
	// Authenticate runs on the outgoing backend-bound request (the vMCP client's
	// round-tripper), which — like the real call path — carries no inbound client
	// RemoteAddr; the audit source is therefore local to the component, not a
	// network IP.
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://backend.local/", nil)
	return req, s.Authenticate(ctx, req, strat)
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

	strat := validOBOStrategy()
	strat.OBO.SubjectTokenProviderName = "entra"
	identity := &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: oboTestSubject},
		Token:          "session-token", // the proxy's own token, not the subject
		UpstreamTokens: map[string]string{"entra": "upstream-token"},
	}
	ctx := auth.WithIdentity(t.Context(), identity)

	req, err := oboServeAuthenticate(ctx, t, s, strat)
	require.NoError(t, err)
	assert.Equal(t, "Bearer backend-token", req.Header.Get("Authorization"))
	assert.Equal(t, "upstream-token", stub.lastUserToken, "the named provider token is the subject")
}

// TestOBOAuthenticate_SubjectErrors covers the missing-identity / missing-token
// failure modes: each returns an error and injects nothing.
func TestOBOAuthenticate_SubjectErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		ctx   func(t *testing.T) context.Context
		strat func() *authtypes.BackendAuthStrategy
	}{
		{
			name:  "no identity in context",
			ctx:   func(t *testing.T) context.Context { t.Helper(); return t.Context() },
			strat: validOBOStrategy,
		},
		{
			name: "identity has no inbound token",
			ctx: func(t *testing.T) context.Context {
				t.Helper()
				return auth.WithIdentity(t.Context(), oboInboundIdentity(""))
			},
			strat: validOBOStrategy,
		},
		{
			name: "named provider token absent",
			ctx: func(t *testing.T) context.Context {
				t.Helper()
				return auth.WithIdentity(t.Context(), oboInboundIdentity("user-token"))
			},
			strat: func() *authtypes.BackendAuthStrategy {
				s := validOBOStrategy()
				s.OBO.SubjectTokenProviderName = "entra"
				return s
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stub := &oboStubExchanger{token: "unused"}
			s := oboStrategyWithStub(stub, nil)

			req, err := oboServeAuthenticate(tt.ctx(t), t, s, tt.strat())
			require.Error(t, err)
			assert.Empty(t, req.Header.Get("Authorization"), "no token may be injected on failure")
			assert.Zero(t, stub.calls, "no exchange should be attempted")
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

// TestOBOAuthenticate_OtherExchangeError_NoAudit proves a non-CA exchange failure
// returns an error and injects nothing, without emitting a D1 audit record.
func TestOBOAuthenticate_OtherExchangeError_NoAudit(t *testing.T) {
	t.Parallel()

	var auditBuf bytes.Buffer
	auditLogger := slog.New(slog.NewJSONHandler(&auditBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	stub := &oboStubExchanger{err: &entra.ExchangeError{Kind: entra.KindInvalidGrant, OAuthError: "invalid_grant"}}
	s := oboStrategyWithStub(stub, auditLogger)
	ctx := auth.WithIdentity(t.Context(), oboInboundIdentity("user-token"))

	req, err := oboServeAuthenticate(ctx, t, s, validOBOStrategy())
	require.Error(t, err)
	assert.ErrorIs(t, err, entra.ErrInvalidGrant)
	assert.Empty(t, req.Header.Get("Authorization"))
	assert.Empty(t, auditBuf.String(), "only conditional-access denials emit the D1 audit record")
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
// WithSkew, and Authenticate performs a real HTTP OBO exchange against an
// httptest server standing in for the Entra token endpoint (loopback http, which
// NewFromTokenURL permits). This is the only test that drives defaultBuildOBOExchanger
// rather than a stub,
// including the positive CacheSkewSeconds gate.
func TestOBOAuthenticate_RealEntraExchange(t *testing.T) {
	t.Parallel()

	var gotAuthHeader, gotGrantType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		_ = r.ParseForm()
		gotGrantType = r.Form.Get("grant_type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token_type":"Bearer","expires_in":3599,"access_token":"backend-abc"}`))
	}))
	defer srv.Close()

	skew := int32(120) // exercise the CacheSkewSeconds > 0 gate in defaultBuildOBOExchanger
	s := NewOBOStrategy(oboMapEnv{})
	strat := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeOBO,
		OBO: &authtypes.OBOConfig{
			TokenURL:         srv.URL, // loopback http — accepted by NewFromTokenURL
			ClientID:         oboTestClientID,
			ClientSecret:     "resolved-secret",
			Scopes:           []string{oboTestScope},
			CacheSkewSeconds: &skew,
		},
	}
	ctx := auth.WithIdentity(t.Context(), oboInboundIdentity("user-token"))

	req, err := oboServeAuthenticate(ctx, t, s, strat)
	require.NoError(t, err)
	assert.Equal(t, "Bearer backend-abc", req.Header.Get("Authorization"),
		"the backend-scoped token from the real exchange must be injected")
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", gotGrantType,
		"the exchange must use the Entra on-behalf-of (jwt-bearer) grant")
	assert.NotContains(t, gotAuthHeader, "user-token",
		"the subject token must travel in the form body, not the request's Authorization header")
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
		{name: "missing client id", mutate: func(s *authtypes.BackendAuthStrategy) { s.OBO.ClientID = "" }, wantMsg: "clientId is required"},
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
			strat := validOBOStrategy()
			tt.mutate(strat)
			err := s.Validate(strat)
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
			strat := validOBOStrategy()
			strat.OBO.Audience = tt.audience
			strat.OBO.Scopes = tt.scopes
			ctx := auth.WithIdentity(t.Context(), oboInboundIdentity("user-token"))

			_, err := oboServeAuthenticate(ctx, t, s, strat)
			require.NoError(t, err)
			assert.Equal(t, want, stub.lastAudience)
		})
	}
}

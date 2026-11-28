// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/obo/cache"
	"github.com/stacklok/toolhive/pkg/auth/obo/entra"
	"github.com/stacklok/toolhive/pkg/auth/obo/exchange"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

const (
	testTarget   = "api://backend/.default"
	testProvider = "entra"
	testSubject  = "user-123"
)

const (
	// invalidTokenChallenge is the WWW-Authenticate value on an inbound-mode 401.
	invalidTokenChallenge = `Bearer error="invalid_token"`
	// upstreamReauthChallenge is the WWW-Authenticate value on an
	// upstream-provider-mode 401, asking the client to re-authenticate.
	upstreamReauthChallenge = `Bearer error="invalid_token", ` +
		`error_description="upstream token is unavailable; re-authentication required"`
)

// stubExchanger is a configurable exchange.Exchanger test double. It records the
// arguments of the last call and the call count.
type stubExchanger struct {
	token         string
	err           error
	calls         int
	lastUserToken string
	lastAudience  string
}

var _ exchange.Exchanger = (*stubExchanger)(nil)

func (s *stubExchanger) Exchange(_ context.Context, userToken, audience string) (string, error) {
	s.calls++
	s.lastUserToken = userToken
	s.lastAudience = audience
	return s.token, s.err
}

// countingTokenExchanger is an exchange.TokenExchanger double that counts exchanges,
// used (behind a real cache) to prove the middleware reuses cached tokens. Each
// exchange mints a token expiring one hour after now(), so a test clock can drive
// the expiry→re-exchange transition deterministically.
type countingTokenExchanger struct {
	mu    sync.Mutex
	calls int
	now   func() time.Time
}

var _ exchange.TokenExchanger = (*countingTokenExchanger)(nil)

func (c *countingTokenExchanger) ExchangeToken(_ context.Context, _, _ string) (exchange.Token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return exchange.Token{AccessToken: "backend-token", Expiry: c.now().Add(time.Hour)}, nil
}

func (c *countingTokenExchanger) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// downstreamSpy records whether the downstream handler ran and the request
// headers it observed, so tests can assert short-circuiting, injection, and that
// the raw subject token is not forwarded.
type downstreamSpy struct {
	called     bool
	authHeader string
	headers    http.Header
}

func (d *downstreamSpy) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.called = true
		d.headers = r.Header.Clone()
		d.authHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})
}

// serve runs req through mw wrapping a downstreamSpy and returns the recorder
// and the spy.
func serve(t *testing.T, mw *middleware, req *http.Request) (*httptest.ResponseRecorder, *downstreamSpy) {
	t.Helper()
	spy := &downstreamSpy{}
	h := mw.Handler()(spy.handler())
	require.NotNil(t, h)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, spy
}

// inboundIdentity returns an Identity carrying token as the inbound (pass-through)
// token — the inbound-mode subject source.
func inboundIdentity(token string) *auth.Identity {
	return &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{Subject: testSubject},
		Token:         token,
	}
}

// testInboundToken is the inbound token every upstream-provider-mode identity
// also carries. Provider mode must never fall back to it, so tests assert it is
// never exchanged.
const testInboundToken = "INBOUND-TOKEN-NOT-THE-SUBJECT"

// upstreamIdentity returns an Identity carrying token under the given upstream
// provider name — the upstream-provider-mode subject source. It also carries a
// non-empty inbound token, as a real authenticated identity would, so a
// fallback to Identity.Token is observable.
func upstreamIdentity(provider, token string) *auth.Identity {
	return &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: testSubject},
		Token:          testInboundToken,
		UpstreamTokens: map[string]string{provider: token},
	}
}

// fakeClock is a manually-advanced, concurrency-safe time source for the cache.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// request builds a GET request rooted at the test's context (cancelled when the
// test ends), attaching identity when non-nil.
func request(t *testing.T, identity *auth.Identity) *http.Request {
	t.Helper()
	ctx := t.Context()
	if identity != nil {
		ctx = auth.WithIdentity(ctx, identity)
	}
	return httptest.NewRequest(http.MethodGet, "/anything", nil).WithContext(ctx)
}

// TestCreateMiddleware_InstallsOBOMiddleware verifies that dispatching through
// CreateMiddleware with a valid Parameters contract installs the OBO middleware,
// and that a request through it is handled (no authenticated identity yields
// 401, never the old 503 stub).
func TestCreateMiddleware_InstallsOBOMiddleware(t *testing.T) {
	t.Setenv("TEST_OBO_SECRET_INSTALL", "the-client-secret")

	ctrl := gomock.NewController(t)
	params := map[string]any{
		"tokenUrl":           "https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		"clientId":           "client-id",
		"scopes":             []string{testTarget},
		"clientSecretEnvVar": "TEST_OBO_SECRET_INSTALL",
	}
	cfg, err := types.NewMiddlewareConfig(MiddlewareType, params)
	require.NoError(t, err)

	var captured types.Middleware
	mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
	mockRunner.EXPECT().
		AddMiddleware(MiddlewareType, gomock.Any()).
		Do(func(_ string, mw types.Middleware) { captured = mw }).
		Times(1)

	require.NoError(t, CreateMiddleware(cfg, mockRunner),
		"CreateMiddleware must succeed")
	require.NotNil(t, captured)

	installed, ok := captured.(*middleware)
	require.True(t, ok, "CreateMiddleware must install the OBO middleware")
	assert.Empty(t, installed.providerName, "no subjectTokenProviderName means inbound mode")

	// A request with no authenticated identity must reach the OBO handler (401).
	rec, spy := serve(t, installed, request(t, nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotEqual(t, http.StatusServiceUnavailable, rec.Code)
	assert.False(t, spy.called)
}

// TestCreateMiddleware_InstallsUpstreamProviderMode verifies the factory wires the
// configured subjectTokenProviderName onto the installed middleware.
func TestCreateMiddleware_InstallsUpstreamProviderMode(t *testing.T) {
	t.Setenv("TEST_OBO_SECRET_PROVIDER", "the-client-secret")

	ctrl := gomock.NewController(t)
	params := map[string]any{
		"tokenUrl":                 "https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		"clientId":                 "client-id",
		"scopes":                   []string{testTarget},
		"clientSecretEnvVar":       "TEST_OBO_SECRET_PROVIDER",
		"subjectTokenProviderName": testProvider,
	}
	cfg, err := types.NewMiddlewareConfig(MiddlewareType, params)
	require.NoError(t, err)

	var captured types.Middleware
	mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
	mockRunner.EXPECT().
		AddMiddleware(MiddlewareType, gomock.Any()).
		Do(func(_ string, mw types.Middleware) { captured = mw }).
		Times(1)

	require.NoError(t, CreateMiddleware(cfg, mockRunner))
	installed, ok := captured.(*middleware)
	require.True(t, ok)
	assert.Equal(t, testProvider, installed.providerName)
}

// TestCreateMiddleware_InvalidParameters verifies the factory fails
// loudly (returns an error, installs nothing) for malformed parameters or a
// missing secret. A mock runner with no expectations fails the test if
// AddMiddleware is called.
func TestCreateMiddleware_InvalidParameters(t *testing.T) {
	t.Parallel()

	valid := map[string]any{
		"tokenUrl":           "https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		"clientId":           "client-id",
		"scopes":             []string{testTarget},
		"clientSecretEnvVar": "OBO_SECRET_NEVER_SET_XYZ",
	}
	without := func(key string) map[string]any {
		m := map[string]any{}
		for k, v := range valid {
			if k != key {
				m[k] = v
			}
		}
		return m
	}

	tests := []struct {
		name            string
		rawParams       json.RawMessage // takes precedence when non-nil
		params          map[string]any
		nilParams       bool
		wantErrContains string
	}{
		{name: "nil parameters", nilParams: true, wantErrContains: "parameters are required"},
		{name: "malformed json", rawParams: json.RawMessage("{not-json"), wantErrContains: "invalid parameters"},
		{name: "missing tokenUrl", params: without("tokenUrl"), wantErrContains: "tokenUrl is required"},
		{name: "missing clientId", params: without("clientId"), wantErrContains: "clientId is required"},
		{name: "missing clientSecretEnvVar", params: without("clientSecretEnvVar"), wantErrContains: "clientSecretEnvVar is required"},
		{
			name: "missing both audience and scopes",
			params: map[string]any{
				"tokenUrl":           valid["tokenUrl"],
				"clientId":           valid["clientId"],
				"clientSecretEnvVar": valid["clientSecretEnvVar"],
			},
			wantErrContains: "one of scopes or audience is required",
		},
		{
			name: "negative cacheSkewSeconds",
			params: map[string]any{
				"tokenUrl":           valid["tokenUrl"],
				"clientId":           valid["clientId"],
				"scopes":             []string{testTarget},
				"clientSecretEnvVar": valid["clientSecretEnvVar"],
				"cacheSkewSeconds":   -1,
			},
			wantErrContains: "cacheSkewSeconds must not be negative",
		},
		// OBO_SECRET_NEVER_SET_XYZ is never set, so the secret read fails.
		{name: "secret env var unset", params: valid, wantErrContains: "client secret env var"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			// No EXPECT: any AddMiddleware call fails the test.
			mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

			cfg := &types.MiddlewareConfig{Type: MiddlewareType}
			switch {
			case tt.nilParams:
				cfg.Parameters = nil
			case tt.rawParams != nil:
				cfg.Parameters = tt.rawParams
			default:
				raw, err := json.Marshal(tt.params)
				require.NoError(t, err)
				cfg.Parameters = raw
			}

			err := CreateMiddleware(cfg, mockRunner)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantErrContains)
		})
	}
}

// TestCreateMiddleware_BadTokenURL verifies that an entra-build failure
// (bad token URL, secret present) is surfaced as a factory error and installs
// nothing.
func TestCreateMiddleware_BadTokenURL(t *testing.T) {
	t.Setenv("TEST_OBO_SECRET_BADURL", "the-client-secret")

	ctrl := gomock.NewController(t)
	mockRunner := mocks.NewMockMiddlewareRunner(ctrl) // no EXPECT: AddMiddleware must not be called

	params := map[string]any{
		"tokenUrl":           "ftp://login.microsoftonline.com/tenant/token",
		"clientId":           "client-id",
		"scopes":             []string{testTarget},
		"clientSecretEnvVar": "TEST_OBO_SECRET_BADURL",
	}
	cfg, err := types.NewMiddlewareConfig(MiddlewareType, params)
	require.NoError(t, err)

	err = CreateMiddleware(cfg, mockRunner)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "the-client-secret")
}

// TestCreateMiddleware_WiresCacheSkew drives CreateMiddleware against a stub Entra
// token endpoint issuing 30s tokens and counts exchanges for two requests from
// one session. The default 60s skew exceeds the token lifetime, so every
// request re-exchanges; only a configured skew shorter than the lifetime lets
// the second request hit the cache, which proves the factory passes
// cacheSkewSeconds to the cache and that zero keeps the default.
func TestCreateMiddleware_WiresCacheSkew(t *testing.T) {
	tests := []struct {
		name      string
		skew      *int
		wantCalls int32
	}{
		{name: "unset skew uses the 60s default", skew: nil, wantCalls: 2},
		{name: "zero skew uses the 60s default", skew: ptrTo(0), wantCalls: 2},
		{name: "skew shorter than the token lifetime caches", skew: ptrTo(10), wantCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_OBO_SECRET_SKEW", "the-client-secret")

			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"token_type":"Bearer","expires_in":30,"access_token":"backend-token"}`))
			}))
			t.Cleanup(srv.Close)

			params := map[string]any{
				"tokenUrl":           srv.URL + "/tenant/oauth2/v2.0/token",
				"clientId":           "client-id",
				"scopes":             []string{testTarget},
				"clientSecretEnvVar": "TEST_OBO_SECRET_SKEW",
			}
			if tt.skew != nil {
				params["cacheSkewSeconds"] = *tt.skew
			}
			cfg, err := types.NewMiddlewareConfig(MiddlewareType, params)
			require.NoError(t, err)

			var captured types.Middleware
			mockRunner := mocks.NewMockMiddlewareRunner(gomock.NewController(t))
			mockRunner.EXPECT().
				AddMiddleware(MiddlewareType, gomock.Any()).
				Do(func(_ string, mw types.Middleware) { captured = mw }).
				Times(1)
			require.NoError(t, CreateMiddleware(cfg, mockRunner))
			installed, ok := captured.(*middleware)
			require.True(t, ok)

			for range 2 {
				rec, spy := serve(t, installed, request(t, inboundIdentity("same-user-token")))
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, "Bearer backend-token", spy.authHeader)
			}
			assert.Equal(t, tt.wantCalls, hits.Load())
		})
	}
}

func ptrTo[T any](v T) *T { return &v }

// TestClientAddr verifies the audit source strips the port from RemoteAddr and
// passes through a RemoteAddr that has none.
func TestClientAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		remoteAddr string
		want       string
	}{
		{remoteAddr: "203.0.113.7:54321", want: "203.0.113.7"},
		{remoteAddr: "[2001:db8::1]:443", want: "2001:db8::1"},
		{remoteAddr: "203.0.113.7", want: "203.0.113.7"},
		{remoteAddr: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.remoteAddr, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remoteAddr
			assert.Equal(t, tt.want, clientAddr(r))
		})
	}
}

// TestMiddleware_Success_Inbound verifies the inbound path: the Identity.Token is
// exchanged and the upstream request carries Authorization: Bearer <backend
// token>, and the downstream handler runs.
func TestMiddleware_Success_Inbound(t *testing.T) {
	t.Parallel()

	stub := &stubExchanger{token: "backend-token"}
	mw := newMiddleware(stub, "", testTarget, nil)

	// A pre-existing inbound Authorization header must be overwritten, not
	// forwarded — the backend sees only the exchanged token.
	req := request(t, inboundIdentity("user-token-XYZ"))
	req.Header.Set("Authorization", "Bearer ORIGINAL-INBOUND-TOKEN")
	rec, spy := serve(t, mw, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, spy.called, "downstream must be invoked on success")
	assert.Equal(t, "Bearer backend-token", spy.authHeader)
	assert.NotContains(t, spy.authHeader, "ORIGINAL-INBOUND-TOKEN", "the inbound Authorization must be overwritten")
	assert.Equal(t, "user-token-XYZ", stub.lastUserToken, "the inbound Identity token is the subject")
	assert.Equal(t, testTarget, stub.lastAudience, "scopes/audience collapse to the exchange target")
}

// TestMiddleware_Success_UpstreamProvider verifies the upstream-provider path: the
// named provider's token from Identity.UpstreamTokens is exchanged.
func TestMiddleware_Success_UpstreamProvider(t *testing.T) {
	t.Parallel()

	stub := &stubExchanger{token: "backend-token"}
	mw := newMiddleware(stub, testProvider, testTarget, nil)

	rec, spy := serve(t, mw, request(t, upstreamIdentity(testProvider, "upstream-entra-token")))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, spy.called)
	assert.Equal(t, "Bearer backend-token", spy.authHeader)
	assert.Equal(t, 1, stub.calls)
	assert.Equal(t, "upstream-entra-token", stub.lastUserToken,
		"the named upstream provider token is the subject, not the inbound token")
	assert.NotEqual(t, testInboundToken, stub.lastUserToken)
}

// TestMiddleware_NoIdentity_401 verifies both modes reject a request without an
// authenticated identity, without attempting an exchange.
func TestMiddleware_NoIdentity_401(t *testing.T) {
	t.Parallel()

	for _, providerName := range []string{"", testProvider} {
		t.Run("provider="+providerName, func(t *testing.T) {
			t.Parallel()
			stub := &stubExchanger{token: "unused"}
			mw := newMiddleware(stub, providerName, testTarget, nil)

			rec, spy := serve(t, mw, request(t, nil))

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Equal(t, invalidTokenChallenge, rec.Header().Get("WWW-Authenticate"),
				"every 401 must carry a Bearer challenge")
			assert.False(t, spy.called)
			assert.Equal(t, 0, stub.calls, "no exchange without an identity")
		})
	}
}

// TestMiddleware_InboundIdentityWithoutToken_401 verifies the inbound path rejects
// an identity that carries no pass-through token.
func TestMiddleware_InboundIdentityWithoutToken_401(t *testing.T) {
	t.Parallel()

	stub := &stubExchanger{token: "unused"}
	mw := newMiddleware(stub, "", testTarget, nil)

	rec, spy := serve(t, mw, request(t, inboundIdentity("")))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, invalidTokenChallenge, rec.Header().Get("WWW-Authenticate"),
		"every 401 must carry a Bearer challenge")
	assert.False(t, spy.called)
	assert.Equal(t, 0, stub.calls)
}

// TestMiddleware_UpstreamProviderTokenMissing_401Challenge verifies the
// upstream-provider path returns 401 with a re-auth challenge when the configured
// provider has no usable token, mirroring upstreamswap. Every identity carries a
// non-empty inbound token, so a fallback to it would show up as an exchange.
func TestMiddleware_UpstreamProviderTokenMissing_401Challenge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity *auth.Identity
	}{
		{name: "provider absent", identity: &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{Subject: testSubject},
			Token:         testInboundToken,
		}},
		{name: "provider present but empty", identity: upstreamIdentity(testProvider, "")},
		{name: "different provider", identity: upstreamIdentity("other", "tok")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stub := &stubExchanger{token: "unused"}
			mw := newMiddleware(stub, testProvider, testTarget, nil)

			rec, spy := serve(t, mw, request(t, tt.identity))

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.NotEmpty(t, rec.Header().Get("WWW-Authenticate"), "a re-auth challenge must be sent")
			assert.False(t, spy.called)
			assert.Equal(t, 0, stub.calls)
		})
	}
}

// TestMiddleware_InteractionRequired_403AndAudit verifies the fail-fast:
// a conditional-access / claims challenge yields a generic 403, an audit record
// (carrying the denied subject but not the token or claims), and no downstream
// call.
func TestMiddleware_InteractionRequired_403AndAudit(t *testing.T) {
	t.Parallel()

	const (
		userToken = "USER-TOKEN-SECRET"
		claims    = "CLAIMS-CHALLENGE-BLOB"
	)
	exErr := &entra.ExchangeError{
		Kind:        entra.KindInteractionRequired,
		OAuthError:  "interaction_required",
		Description: "AADSTS50079: due to a configuration change made by your administrator",
		Claims:      claims,
		HTTPStatus:  http.StatusUnauthorized,
	}
	require.ErrorIs(t, exErr, entra.ErrInteractionRequired)

	var auditBuf bytes.Buffer
	stub := &stubExchanger{err: exErr}
	mw := newMiddleware(stub, "", testTarget, slog.New(slog.NewJSONHandler(&auditBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	req := request(t, inboundIdentity(userToken))
	req.RemoteAddr = "203.0.113.7:54321"
	rec, spy := serve(t, mw, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, spy.called, "downstream must not be called on a claims challenge")
	assert.Contains(t, rec.Body.String(), forbiddenMessage)

	// The audit source is the client IP with the port stripped.
	var record struct {
		Source struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"source"`
		Target map[string]string `json:"target"`
	}
	require.NoError(t, json.Unmarshal(auditBuf.Bytes(), &record))
	assert.Equal(t, "203.0.113.7", record.Source.Value)
	assert.Equal(t, testTarget, record.Target["audience"])

	// Non-disclosure: neither the response body nor the audit record may leak
	// the user token or the raw claims challenge.
	assert.NotContains(t, rec.Body.String(), userToken)
	assert.NotContains(t, rec.Body.String(), claims)

	auditOut := auditBuf.String()
	assert.NotEmpty(t, auditOut, "a fail-fast claims challenge must emit an audit record")
	assert.Contains(t, auditOut, AuditEventType)
	assert.Contains(t, auditOut, "denied")
	assert.Contains(t, auditOut, "interaction_required")
	assert.Contains(t, auditOut, testSubject, "the audit record identifies the denied subject")
	assert.NotContains(t, auditOut, userToken, "the audit record must not contain the user token")
	assert.NotContains(t, auditOut, claims, "the audit record must not contain the raw claims challenge")
}

// TestMiddleware_ExchangeErrors_StatusMapping verifies the error→status mapping
// for exchange failures other than the claims challenge.
func TestMiddleware_ExchangeErrors_StatusMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		err           error
		wantStatus    int
		wantBody      string
		wantChallenge string
	}{
		{
			name:          "invalid grant is 401 with an invalid_token challenge",
			err:           &entra.ExchangeError{Kind: entra.KindInvalidGrant, OAuthError: "invalid_grant"},
			wantStatus:    http.StatusUnauthorized,
			wantBody:      unauthorizedMessage,
			wantChallenge: invalidTokenChallenge,
		},
		{
			name:       "invalid client is 502",
			err:        &entra.ExchangeError{Kind: entra.KindInvalidClient, OAuthError: "invalid_client"},
			wantStatus: http.StatusBadGateway,
			wantBody:   badGatewayMessage,
		},
		{
			name:       "transient is 502",
			err:        &entra.ExchangeError{Kind: entra.KindTransient},
			wantStatus: http.StatusBadGateway,
			wantBody:   badGatewayMessage,
		},
		{
			name:       "unclassified error is 502",
			err:        errors.New("something unexpected"),
			wantStatus: http.StatusBadGateway,
			wantBody:   badGatewayMessage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stub := &stubExchanger{err: tt.err}
			mw := newMiddleware(stub, "", testTarget, nil)

			rec, spy := serve(t, mw, request(t, inboundIdentity("user-token")))

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantBody)
			assert.Equal(t, tt.wantChallenge, rec.Header().Get("WWW-Authenticate"))
			assert.False(t, spy.called, "downstream must not be called on an exchange failure")
		})
	}
}

// TestMiddleware_InvalidGrant_UpstreamProvider_ReauthChallenge verifies that in
// upstream-provider mode a rejected provider token asks the client to
// re-authenticate with the embedded auth server, the recovery that yields a
// fresh provider token.
func TestMiddleware_InvalidGrant_UpstreamProvider_ReauthChallenge(t *testing.T) {
	t.Parallel()

	stub := &stubExchanger{err: &entra.ExchangeError{Kind: entra.KindInvalidGrant, OAuthError: "invalid_grant"}}
	mw := newMiddleware(stub, testProvider, testTarget, nil)

	rec, spy := serve(t, mw, request(t, upstreamIdentity(testProvider, "expired-upstream-token")))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, upstreamReauthChallenge, rec.Header().Get("WWW-Authenticate"))
	assert.Equal(t, unauthorizedMessage+"\n", rec.Body.String())
	assert.False(t, spy.called)
}

// TestMiddleware_UnclassifiedError_NotLoggedToDefault covers the "no token or
// secret in logs" guarantee on the failure path that uses the package-global
// logger: an unclassified (non-entra) error must not have its content written to
// the diagnostic log, so a future non-conforming Exchanger can't leak material
// through the 502 branch. It captures slog.Default() (restored via t.Cleanup) and
// asserts a sentinel error string never appears.
//
//nolint:paralleltest // Swaps slog.Default() for the duration; must not overlap parallel tests.
func TestMiddleware_UnclassifiedError_NotLoggedToDefault(t *testing.T) {
	const sentinel = "SENTINEL-MUST-NOT-REACH-LOGS"

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	stub := &stubExchanger{err: errors.New(sentinel)}
	mw := newMiddleware(stub, "", testTarget, nil)

	rec, spy := serve(t, mw, request(t, inboundIdentity("user-token")))

	require.Equal(t, http.StatusBadGateway, rec.Code)
	assert.False(t, spy.called)
	assert.NotContains(t, buf.String(), sentinel,
		"an unclassified error's content must not reach the diagnostic log")
}

// TestMiddleware_ContextErrors verifies the handling of context-shaped
// exchange errors. When the client's own request context is done (it
// disconnected or its deadline passed), no response is written: a 502 would be
// a misleading 5xx for a request nobody is waiting on. When the request is
// still live, a context-shaped error (e.g. the HTTP client timing out against
// the token endpoint) is a backend failure and must be a 502, not an empty 200.
func TestMiddleware_ContextErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		err           error
		cancelRequest bool
		wantStatus    int
	}{
		{name: "canceled request", err: context.Canceled, cancelRequest: true, wantStatus: http.StatusOK},
		{name: "canceled request, entra-wrapped", err: fmt.Errorf("entra obo: %w", context.Canceled),
			cancelRequest: true, wantStatus: http.StatusOK},
		{name: "deadline on a canceled request", err: context.DeadlineExceeded, cancelRequest: true, wantStatus: http.StatusOK},
		{name: "deadline on a live request is a 502", err: context.DeadlineExceeded, wantStatus: http.StatusBadGateway},
		{name: "canceled error on a live request is a 502", err: context.Canceled, wantStatus: http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stub := &stubExchanger{err: tt.err}
			mw := newMiddleware(stub, "", testTarget, nil)

			req := request(t, inboundIdentity("user-token"))
			if tt.cancelRequest {
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}
			rec, spy := serve(t, mw, req)

			// httptest.ResponseRecorder reports 200 when nothing was written.
			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.cancelRequest {
				assert.Empty(t, rec.Body.String(), "no error body is written for a cancelled request")
			}
			assert.False(t, spy.called)
			assert.Equal(t, 1, stub.calls)
		})
	}
}

// TestMiddleware_CachesAcrossRequests verifies the acceptance criterion that
// exchanged tokens are cached across requests for the same session: with the real
// cache (driven by a fake clock) wrapping a counting exchanger, repeated requests
// for the same subject reuse one exchange within the TTL, a different subject
// triggers its own exchange, and the token is re-exchanged once it expires.
func TestMiddleware_CachesAcrossRequests(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	counting := &countingTokenExchanger{now: clk.now}
	mw := newMiddleware(
		cache.New(counting, cache.WithClock(clk.now), cache.WithSkew(60*time.Second)),
		"", testTarget, nil,
	)

	// Two requests for the same session, within the TTL → one exchange.
	for i := 0; i < 2; i++ {
		rec, spy := serve(t, mw, request(t, inboundIdentity("same-user-token")))
		require.Equal(t, http.StatusOK, rec.Code)
		require.True(t, spy.called)
		require.Equal(t, "Bearer backend-token", spy.authHeader)
	}
	assert.Equal(t, 1, counting.callCount(), "a cached token must be reused within its TTL")

	// A different session forces a fresh exchange.
	serve(t, mw, request(t, inboundIdentity("other-user-token")))
	assert.Equal(t, 2, counting.callCount(), "a different session must not collide with a cached entry")

	// Advancing past the first session's expiry (issued at t0, +1h, minus 60s
	// skew) makes its cached token stale, so the next request re-exchanges.
	clk.advance(time.Hour)
	rec, spy := serve(t, mw, request(t, inboundIdentity("same-user-token")))
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, spy.called)
	assert.Equal(t, 3, counting.callCount(), "an expired token must be re-exchanged")
}

// TestMiddleware_RealEntra_InteractionRequired_NonDisclosure exercises the full
// real path for the non-disclosure guarantee: the actual Entra classifier (entra.NewFromTokenURL against
// an httptest endpoint returning a genuine interaction_required claims challenge)
// → cache → middleware. It proves the seam between what Entra produces and what
// the middleware audits never discloses the claims challenge, the user token, or
// the client secret — and that the audit record carries exactly the expected safe
// keys, so a future widening would fail here.
func TestMiddleware_RealEntra_InteractionRequired_NonDisclosure(t *testing.T) {
	t.Parallel()

	const (
		userToken    = "USER-ASSERTION-SECRET"
		claimsBlob   = "eyJhY2Nlc3NfdG9rZW4iOnsiTUZBIjp7ImVzc2VudGlhbCI6dHJ1ZX19fQ"
		clientSecret = "the-client-secret-VALUE"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"interaction_required","error_codes":[50079],` +
			`"error_description":"AADSTS50079: due to a configuration change made by your administrator",` +
			`"claims":"` + claimsBlob + `"}`))
	}))
	t.Cleanup(srv.Close)

	ex, err := entra.NewFromTokenURL(srv.URL+"/tenant/oauth2/v2.0/token", "client-id", clientSecret, nil)
	require.NoError(t, err)

	var auditBuf bytes.Buffer
	mw := newMiddleware(cache.New(ex), "", testTarget,
		slog.New(slog.NewJSONHandler(&auditBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	rec, spy := serve(t, mw, request(t, inboundIdentity(userToken)))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, spy.called)
	assert.Contains(t, rec.Body.String(), forbiddenMessage)
	assert.NotContains(t, rec.Body.String(), claimsBlob)
	assert.NotContains(t, rec.Body.String(), userToken)
	assert.NotContains(t, rec.Body.String(), clientSecret)

	auditOut := auditBuf.String()
	require.NotEmpty(t, auditOut, "a fail-fast claims challenge must emit an audit record")
	assert.NotContains(t, auditOut, claimsBlob, "the audit record must not contain the raw claims challenge")
	assert.NotContains(t, auditOut, userToken)
	assert.NotContains(t, auditOut, clientSecret)

	// The audit Extra carries exactly the expected safe keys — locking the record
	// shape so a future change that widened it (e.g. dumping the claims blob or
	// the assertion) would fail this assertion.
	var record struct {
		Msg      string `json:"msg"`
		Outcome  string `json:"outcome"`
		Metadata struct {
			Extra map[string]any `json:"extra"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal([]byte(auditOut), &record))
	assert.Equal(t, "audit_event", record.Msg)
	assert.Equal(t, "denied", record.Outcome)

	gotKeys := make([]string, 0, len(record.Metadata.Extra))
	for k := range record.Metadata.Extra {
		gotKeys = append(gotKeys, k)
	}
	assert.ElementsMatch(t,
		[]string{"reason", "oauth_error", "description", "http_status", "claims_challenge_present"},
		gotKeys)
	assert.Equal(t, "interaction_required", record.Metadata.Extra["reason"])
	assert.Equal(t, true, record.Metadata.Extra["claims_challenge_present"])
}

// TestMiddleware_Close verifies the middleware's Close is a no-op.
func TestMiddleware_Close(t *testing.T) {
	t.Parallel()

	assert.NoError(t, (&middleware{}).Close())
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestHandlersClaimDisjointRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		form        url.Values
		grantType   string
		wantRefresh bool
		wantAccess  bool
	}{
		{name: "plain access-token exchange", grantType: oauthproto.GrantTypeTokenExchange, wantAccess: true,
			form: url.Values{"subject_token_type": {oauthproto.TokenTypeAccessToken}}},
		{name: "no token types", grantType: oauthproto.GrantTypeTokenExchange, wantAccess: true},
		{name: "requested id-jag with an access-token subject", grantType: oauthproto.GrantTypeTokenExchange, wantAccess: true,
			form: url.Values{
				"subject_token_type": {oauthproto.TokenTypeAccessToken}, "requested_token_type": {oauthproto.TokenTypeIDJAG}}},
		{name: "requested refresh token", grantType: oauthproto.GrantTypeTokenExchange, wantRefresh: true,
			form: url.Values{
				"subject_token_type": {oauthproto.TokenTypeAccessToken}, "requested_token_type": {oauthproto.TokenTypeRefreshToken}}},
		{name: "refresh-token subject asking for an id-jag", grantType: oauthproto.GrantTypeTokenExchange, wantRefresh: true,
			form: url.Values{
				"subject_token_type": {oauthproto.TokenTypeRefreshToken}, "requested_token_type": {oauthproto.TokenTypeIDJAG}}},
		{name: "refresh-token subject with no requested type", grantType: oauthproto.GrantTypeTokenExchange, wantRefresh: true,
			form: url.Values{"subject_token_type": {oauthproto.TokenTypeRefreshToken}}},
		{name: "another grant type", grantType: oauthproto.GrantTypeRefreshToken,
			form: url.Values{"requested_token_type": {oauthproto.TokenTypeRefreshToken}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := fosite.NewAccessRequest(&session.Session{})
			req.GrantTypes = fosite.Arguments{tt.grantType}
			req.Form = tt.form

			assert.Equal(t, tt.wantRefresh, (&RefreshTokenHandler{}).CanHandleTokenEndpointRequest(t.Context(), req))
			assert.Equal(t, tt.wantAccess, (&Handler{}).CanHandleTokenEndpointRequest(t.Context(), req))
		})
	}
}

func TestCheckLoginToken(t *testing.T) {
	t.Parallel()

	valid := func() *ValidatedClaims {
		return &ValidatedClaims{Subject: "user-1", ClientID: "app", JWTID: "jti-1", Extra: map[string]any{}}
	}
	tests := []struct {
		name   string
		mutate func(*ValidatedClaims)
		want   string
	}{
		{name: "login token", mutate: func(*ValidatedClaims) {}},
		{name: "act", mutate: func(c *ValidatedClaims) { c.Extra["act"] = map[string]any{} }, want: "subject_token_delegated"},
		{name: "external_issuer claim", mutate: func(c *ValidatedClaims) { c.Extra["external_issuer"] = "https://idp" },
			want: "subject_token_external"},
		{name: "validated external issuer", mutate: func(c *ValidatedClaims) { c.ExternalIssuer = "https://idp" },
			want: "subject_token_external"},
		{name: "hash in sub", mutate: func(c *ValidatedClaims) { c.Subject = "https://idp#user" }, want: "subject_token_external"},
		{name: "no upstream session", mutate: func(c *ValidatedClaims) { c.Extra[session.NoUpstreamSessionClaimKey] = true },
			want: "subject_token_no_upstream_session"},
		{name: "no client_id", mutate: func(c *ValidatedClaims) { c.ClientID = "" }, want: "subject_token_missing_client_id"},
		{name: "no jti", mutate: func(c *ValidatedClaims) { c.JWTID = "" }, want: "subject_token_missing_jti"},
		{name: "act and no jti", mutate: func(c *ValidatedClaims) {
			c.Extra["act"] = map[string]any{}
			c.JWTID = ""
		}, want: "subject_token_delegated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := valid()
			tt.mutate(claims)
			assert.Equal(t, tt.want, checkLoginToken(claims))
		})
	}

	// A login-token fault is reported before a policy fault: the request goes
	// through decide, which runs checkLoginToken first.
	for name, mutate := range map[string]func(*ValidatedClaims){
		"missing jti and wrong audience": func(c *ValidatedClaims) {
			c.JWTID = ""
			c.Audience = []string{"https://other"}
		},
		"missing jti and non-allowed source": func(c *ValidatedClaims) {
			c.JWTID = ""
			c.ClientID = "stranger"
		},
	} {
		t.Run(name+" is refused as subject_token_invalid", func(t *testing.T) {
			t.Parallel()
			claims := validLoginClaims()
			mutate(claims)
			h := newRefreshHandlerForTest(t, &stubSubjectValidator{claims: claims}, &fakeGrantStore{})
			err := h.HandleTokenEndpointRequest(t.Context(), refreshRequestForTest(brokerClientForTest()))
			var oauthErr *fosite.RFC6749Error
			require.ErrorAs(t, err, &oauthErr)
			assert.Equal(t, "invalid_request", oauthErr.ErrorField)
			assert.Equal(t, reasonSubjectInvalid, oauthErr.GetDescription(), "missing_jti stays the generic refusal, not the policy reason")
		})
	}
}

func TestCheckExchangeGrantSubject(t *testing.T) {
	t.Parallel()

	policy := ConfiguredClientPolicy{APIAudience: "https://api.example.com", AllowedSourceClients: []string{"app"}}
	valid := func() *ValidatedClaims {
		return &ValidatedClaims{ClientID: "app", Audience: []string{"https://api.example.com"}}
	}
	tests := []struct {
		name   string
		mutate func(*ValidatedClaims)
		want   string
	}{
		{name: "allowed source client", mutate: func(*ValidatedClaims) {}},
		{name: "audience", mutate: func(c *ValidatedClaims) { c.Audience = []string{"https://other"} }, want: "subject_token_audience"},
		{name: "exchanging client's own token", mutate: func(c *ValidatedClaims) { c.ClientID = "broker" },
			want: "subject_token_source_client"},
		{name: "other source client", mutate: func(c *ValidatedClaims) { c.ClientID = "stranger" }, want: "subject_token_source_client"},
		{name: "audience and non-allowed source", mutate: func(c *ValidatedClaims) {
			c.Audience = []string{"https://other"}
			c.ClientID = "stranger"
		}, want: "subject_token_audience"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := valid()
			tt.mutate(claims)
			assert.Equal(t, tt.want, checkExchangeGrantSubject(claims, policy))
		})
	}
	assert.Equal(t, "subject_token_audience", checkExchangeGrantSubject(valid(), ConfiguredClientPolicy{}),
		"a policy without an API audience accepts no subject token")
	assert.Equal(t, "subject_token_source_client",
		checkExchangeGrantSubject(valid(), ConfiguredClientPolicy{APIAudience: policy.APIAudience}),
		"a policy without allowed source clients accepts no subject token")
}

// fakeRefreshStrategy returns a fixed token and signature.
type fakeRefreshStrategy struct{ oauth2.RefreshTokenStrategy }

func (fakeRefreshStrategy) GenerateRefreshToken(context.Context, fosite.Requester) (string, string, error) {
	return "token.sig", "sig", nil
}

// fakeGrantStore records created grants and returns err from CreateExchangeGrant.
type fakeGrantStore struct {
	storage.ExchangeGrantStorage
	mu      sync.Mutex
	err     error
	created []storage.ExchangeGrant
}

func (f *fakeGrantStore) CreateExchangeGrant(_ context.Context, g *storage.ExchangeGrant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.created = append(f.created, *g)
	return nil
}

// stubSubjectValidator returns the claims, or err when set.
type stubSubjectValidator struct {
	mu     sync.Mutex
	claims *ValidatedClaims
	err    error
}

func (v *stubSubjectValidator) Validate(context.Context, string) (*ValidatedClaims, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.claims, v.err
}

const (
	testRefreshClient = "broker"
	// testRefreshSourceClient is the allowed source client login tokens are issued to.
	testRefreshSourceClient = "app"
	testRefreshAPIAud       = "https://api.example.com"
)

func newRefreshHandlerForTest(t *testing.T, v *stubSubjectValidator, store *fakeGrantStore) *RefreshTokenHandler {
	t.Helper()
	registry, err := newExchangeGrantRegistry()
	require.NoError(t, err)
	return &RefreshTokenHandler{
		strategy:      fakeRefreshStrategy{},
		store:         store,
		selfValidator: v,
		registry:      registry,
		clientPolicies: map[string]ConfiguredClientPolicy{testRefreshClient: {
			APIAudience:           testRefreshAPIAud,
			AllowedSourceClients:  []string{testRefreshSourceClient},
			ExchangeRefreshTokens: map[string]time.Duration{oauthproto.TokenTypeRefreshToken: 720 * time.Hour},
		}},
	}
}

func refreshRequestForTest(client fosite.Client) *fosite.AccessRequest {
	req := fosite.NewAccessRequest(&session.Session{})
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeTokenExchange}
	req.Client = client
	req.Form = url.Values{
		"grant_type":           {oauthproto.GrantTypeTokenExchange},
		"subject_token":        {"subject"},
		"subject_token_type":   {oauthproto.TokenTypeAccessToken},
		"requested_token_type": {oauthproto.TokenTypeRefreshToken},
		"authorization_details": {`[{"type":"https://toolhive.dev/exchange_grant",` +
			`"issuable_token_types":["urn:ietf:params:oauth:token-type:refresh_token"]}]`},
	}
	return req
}

func brokerClientForTest() *fosite.DefaultClient {
	return &fosite.DefaultClient{
		ID: testRefreshClient, GrantTypes: fosite.Arguments{oauthproto.GrantTypeTokenExchange}, Public: false,
	}
}

func validLoginClaims() *ValidatedClaims {
	return &ValidatedClaims{
		Subject: "user-1", Issuer: "https://issuer.example.com", ClientID: testRefreshSourceClient, JWTID: "jti-1",
		Audience: []string{testRefreshAPIAud}, Expiry: time.Now().Add(30 * time.Minute), Extra: map[string]any{},
	}
}

func requireOAuthError(t *testing.T, err error, code, description string) {
	t.Helper()
	var oauthErr *fosite.RFC6749Error
	require.ErrorAs(t, err, &oauthErr)
	assert.Equal(t, code, oauthErr.ErrorField)
	if description != "" {
		assert.Equal(t, description, oauthErr.GetDescription())
	}
}

func TestRefreshTokenHandlerClientChecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cli  *fosite.DefaultClient
		code string
	}{
		{name: "public client", code: "invalid_grant",
			cli: &fosite.DefaultClient{ID: testRefreshClient, Public: true, GrantTypes: fosite.Arguments{oauthproto.GrantTypeTokenExchange}}},
		{name: "client without the token-exchange grant", code: "unauthorized_client",
			cli: &fosite.DefaultClient{ID: testRefreshClient, GrantTypes: fosite.Arguments{oauthproto.GrantTypeRefreshToken}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeGrantStore{}
			h := newRefreshHandlerForTest(t, &stubSubjectValidator{claims: validLoginClaims()}, store)
			req := refreshRequestForTest(tt.cli)
			requireOAuthError(t, h.HandleTokenEndpointRequest(t.Context(), req), tt.code, "")
			requireOAuthError(t, h.PopulateTokenEndpointResponse(t.Context(), req, fosite.NewAccessResponse()), tt.code, "")
			assert.Empty(t, store.created)
		})
	}
}

func TestRefreshTokenHandlerIssuesGrant(t *testing.T) {
	t.Parallel()

	store := &fakeGrantStore{}
	h := newRefreshHandlerForTest(t, &stubSubjectValidator{claims: validLoginClaims()}, store)
	req := refreshRequestForTest(brokerClientForTest())
	resp := fosite.NewAccessResponse()
	require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), req))
	require.Empty(t, store.created, "handling writes nothing")
	require.NoError(t, h.PopulateTokenEndpointResponse(t.Context(), req, resp))

	require.Len(t, store.created, 1)
	grant := store.created[0]
	assert.Equal(t, "sig", grant.Signature)
	assert.Equal(t, "https://issuer.example.com", grant.SourceIssuer)
	assert.Equal(t, "jti-1", grant.SourceJTI)
	assert.Equal(t, []string{oauthproto.TokenTypeRefreshToken}, grant.IssuableTokenTypes)
	assert.Empty(t, grant.ParentSignature)
	assert.Equal(t, "token.sig", resp.GetAccessToken())
	assert.Equal(t, "N_A", resp.GetTokenType())
}

// TestRefreshTokenHandlerInternalFailureIsGeneric covers an internal failure
// becoming a generic server_error that does not leak the cause.
func TestRefreshTokenHandlerInternalFailureIsGeneric(t *testing.T) {
	t.Parallel()

	failingRegistry := func(t *testing.T) *authorizationdetails.Registry {
		t.Helper()
		registry, err := authorizationdetails.New(map[string]authorizationdetails.Type{
			exchangeGrantType: {
				GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
				Grant: func(context.Context, authorizationdetails.Request) (authorizationdetails.Result, error) {
					return authorizationdetails.Result{}, errors.New("hook failed with internal detail")
				},
			},
		})
		require.NoError(t, err)
		return registry
	}
	tests := []struct {
		name     string
		setup    func(t *testing.T, h *RefreshTokenHandler, store *fakeGrantStore)
		wantDesc string
		leaks    []string
	}{
		{
			name: "store error",
			setup: func(_ *testing.T, _ *RefreshTokenHandler, store *fakeGrantStore) {
				store.err = errors.New("redis exploded at key xgrant:secret")
			},
			leaks: []string{"redis", "secret"},
		},
		{
			name:     "strategy error",
			setup:    func(_ *testing.T, h *RefreshTokenHandler, _ *fakeGrantStore) { h.strategy = failingRefreshStrategy{} },
			wantDesc: fosite.ErrServerError.WithHint("The refresh token could not be issued.").GetDescription(),
			leaks:    []string{"kid-42"},
		},
		{
			name: "details hook error",
			setup: func(t *testing.T, h *RefreshTokenHandler, _ *fakeGrantStore) {
				t.Helper()
				h.registry = failingRegistry(t)
			},
			leaks: []string{"internal detail"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeGrantStore{}
			h := newRefreshHandlerForTest(t, &stubSubjectValidator{claims: validLoginClaims()}, store)
			tt.setup(t, h, store)
			req := refreshRequestForTest(brokerClientForTest())

			err := h.PopulateTokenEndpointResponse(t.Context(), req, fosite.NewAccessResponse())
			requireOAuthError(t, err, "server_error", tt.wantDesc)
			var oauthErr *fosite.RFC6749Error
			require.ErrorAs(t, err, &oauthErr)
			for _, leak := range tt.leaks {
				assert.NotContains(t, oauthErr.GetDescription(), leak)
			}
			assert.Empty(t, store.created, "no grant is written")
		})
	}
}

// TestRefreshTokenHandlerDecidesAgainBeforeIssuing covers a subject token that
// stops being acceptable, or is consumed, between handling and the response.
func TestRefreshTokenHandlerDecidesAgainBeforeIssuing(t *testing.T) {
	t.Parallel()

	t.Run("subject token no longer verifies", func(t *testing.T) {
		t.Parallel()
		v := &stubSubjectValidator{claims: validLoginClaims()}
		store := &fakeGrantStore{}
		h := newRefreshHandlerForTest(t, v, store)
		req := refreshRequestForTest(brokerClientForTest())
		require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), req))

		v.mu.Lock()
		v.err = errors.New("expired")
		v.mu.Unlock()
		err := h.PopulateTokenEndpointResponse(t.Context(), req, fosite.NewAccessResponse())
		requireOAuthError(t, err, "invalid_request", reasonSubjectInvalid)
		assert.Empty(t, store.created, "no grant is created")
	})

	t.Run("subject token consumed in between", func(t *testing.T) {
		t.Parallel()
		store := &fakeGrantStore{}
		h := newRefreshHandlerForTest(t, &stubSubjectValidator{claims: validLoginClaims()}, store)
		req := refreshRequestForTest(brokerClientForTest())
		require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), req))

		store.err = fosite.ErrJTIKnown
		err := h.PopulateTokenEndpointResponse(t.Context(), req, fosite.NewAccessResponse())
		requireOAuthError(t, err, "invalid_request", "token_used")
		assert.Empty(t, store.created, "no grant is created")
	})
}

func TestRefreshTokenFactoryRequiresStoreForEnabledClients(t *testing.T) {
	t.Parallel()

	policies := map[string]ConfiguredClientPolicy{testRefreshClient: {
		ExchangeRefreshTokens: map[string]time.Duration{oauthproto.TokenTypeIDJAG: time.Hour},
	}}
	_, err := RefreshTokenFactory(policies)
	require.ErrorContains(t, err, "no exchange grant store")

	_, err = RefreshTokenFactory(policies, WithExchangeGrantStorage(&fakeGrantStore{}))
	require.NoError(t, err)
	_, err = RefreshTokenFactory(map[string]ConfiguredClientPolicy{testRefreshClient: {}})
	require.NoError(t, err, "no client enables the feature")
}

// failingRefreshStrategy fails to generate a token. Its message must not reach the client.
type failingRefreshStrategy struct{ oauth2.RefreshTokenStrategy }

func (failingRefreshStrategy) GenerateRefreshToken(context.Context, fosite.Requester) (string, string, error) {
	return "", "", errors.New("signing key kid-42 is missing")
}

func TestRefreshTokenFactoryClosure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		strategy  any
		configure func(*server.AuthorizationServerConfig)
		wantErr   string
	}{
		{name: "strategy without refresh support", strategy: "not a strategy",
			wantErr: "does not implement oauth2.RefreshTokenStrategy"},
		{name: "no signing keys", strategy: fakeRefreshStrategy{},
			configure: func(c *server.AuthorizationServerConfig) { c.SigningJWKS = nil },
			wantErr:   "JWKS must not be nil"},
		{name: "no access token issuer", strategy: fakeRefreshStrategy{},
			configure: func(c *server.AuthorizationServerConfig) { c.AccessTokenIssuer = "" },
			wantErr:   "issuer must not be empty"},
		{name: "valid", strategy: fakeRefreshStrategy{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			factory, err := RefreshTokenFactory(map[string]ConfiguredClientPolicy{})
			require.NoError(t, err)
			cfg := &server.AuthorizationServerConfig{
				Config:      &fosite.Config{AccessTokenIssuer: "https://issuer.example.com"},
				SigningJWKS: newTestJWKS(t).jwks,
			}
			if tt.configure != nil {
				tt.configure(cfg)
			}

			got, err := factory(cfg, nil, tt.strategy)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.IsType(t, &RefreshTokenHandler{}, got)
		})
	}
}

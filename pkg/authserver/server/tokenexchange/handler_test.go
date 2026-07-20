// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const testAgentClientID = "devops-agent"

// newTestHandler creates a TokenExchangeHandler wired to the given test JWKS and
// delegation lifespan. It does not need a full fosite strategy stack because
// HandleTokenEndpointRequest (which is the focus of these tests) does not issue tokens.
func newTestHandler(t *testing.T, tj *testJWKS, delegationLifespan time.Duration) *Handler {
	t.Helper()

	validator, err := NewSubjectTokenValidator(tj.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)

	return &Handler{
		// HandleHelper is nil — we only test HandleTokenEndpointRequest, not
		// PopulateTokenEndpointResponse, so IssueAccessToken is never called.
		HandleHelper:       nil,
		validator:          validator,
		delegationLifespan: delegationLifespan,
		allowedAudiences:   []string{testIssuer},
		config: &mockConfig{
			scopeStrategy:    fosite.ExactScopeStrategy,
			audienceStrategy: fosite.DefaultAudienceMatchingStrategy,
		},
	}
}

// newAccessRequest builds a fosite.AccessRequest for token exchange tests.
func newAccessRequest(t *testing.T, client fosite.Client, formValues url.Values) *fosite.AccessRequest {
	t.Helper()

	req := fosite.NewAccessRequest(&session.Session{})
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeTokenExchange}
	req.Client = client
	req.Form = formValues
	return req
}

// defaultClient returns a confidential client configured for token exchange.
func defaultClient() *fosite.DefaultClient {
	return &fosite.DefaultClient{
		ID:         testAgentClientID,
		GrantTypes: fosite.Arguments{oauthproto.GrantTypeTokenExchange},
		Scopes:     fosite.Arguments{"openid", "profile"},
		Audience:   []string{testIssuer},
		Public:     false,
	}
}

// defaultFormValues returns the minimum form values for a valid token exchange request.
func defaultFormValues(t *testing.T, tj *testJWKS) url.Values {
	t.Helper()

	token := tj.signToken(t, validClaims(), validExtraClaims())
	return url.Values{
		"grant_type":         {oauthproto.GrantTypeTokenExchange},
		"subject_token":      {token},
		"subject_token_type": {oauthproto.TokenTypeAccessToken},
	}
}

// nestedActChain builds a chain of depth nested "act" maps, each holding a
// distinct "sub", for exercising actChainDepth boundary behavior. depth must
// be at least 1.
func nestedActChain(depth int) map[string]any {
	chain := map[string]any{"sub": fmt.Sprintf("agent-%d", depth-1)}
	for i := depth - 2; i >= 0; i-- {
		chain = map[string]any{"sub": fmt.Sprintf("agent-%d", i), "act": chain}
	}
	return chain
}

func TestTokenExchangeHandler_CanHandleTokenEndpointRequest(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	h := newTestHandler(t, tj, 15*time.Minute)

	t.Run("matches token exchange grant type", func(t *testing.T) {
		t.Parallel()
		req := fosite.NewAccessRequest(&session.Session{})
		req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeTokenExchange}
		assert.True(t, h.CanHandleTokenEndpointRequest(context.Background(), req))
	})

	t.Run("rejects other grant types", func(t *testing.T) {
		t.Parallel()
		req := fosite.NewAccessRequest(&session.Session{})
		req.GrantTypes = fosite.Arguments{"client_credentials"}
		assert.False(t, h.CanHandleTokenEndpointRequest(context.Background(), req))
	})
}

func TestTokenExchangeHandler_CanSkipClientAuth(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	h := newTestHandler(t, tj, 15*time.Minute)

	req := fosite.NewAccessRequest(&session.Session{})
	assert.False(t, h.CanSkipClientAuth(context.Background(), req))
}

func TestTokenExchangeHandler_HandleTokenEndpointRequest(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)

	tests := []struct {
		name         string
		ctx          func(t *testing.T) context.Context
		client       func() *fosite.DefaultClient
		form         func(t *testing.T) url.Values
		grantTypes   fosite.Arguments // Override grant type; nil means oauthproto.GrantTypeTokenExchange
		lifespan     time.Duration
		wantErr      bool
		wantFositeIs *fosite.RFC6749Error // Check errors.Is against this fosite sentinel
		hintContains string               // Check the fosite error's Reason/Hint field
		check        func(t *testing.T, req *fosite.AccessRequest)
	}{
		{
			name:     "valid exchange sets act claim and session",
			ctx:      func(_ *testing.T) context.Context { return context.Background() },
			client:   defaultClient,
			form:     func(t *testing.T) url.Values { t.Helper(); return defaultFormValues(t, tj) },
			lifespan: 15 * time.Minute,
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()

				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok, "session should be *session.Session")

				// Verify subject is the user from the subject token.
				assert.Equal(t, "user-123", sess.JWTClaims.Subject)

				// Verify the act claim contains the authenticated client's ID.
				actClaim, exists := sess.JWTClaims.Extra["act"]
				require.True(t, exists, "act claim must be present")
				actMap, ok := actClaim.(map[string]any)
				require.True(t, ok, "act claim must be a map")
				assert.Equal(t, testAgentClientID, actMap["sub"])

				// Verify user claims were propagated.
				assert.Equal(t, "Test User", sess.JWTClaims.Extra["name"])
				assert.Equal(t, "test@example.com", sess.JWTClaims.Extra["email"])

				// Verify expiry was set on the session.
				expiry := sess.GetExpiresAt(fosite.AccessToken)
				assert.False(t, expiry.IsZero(), "access token expiry should be set")
			},
		},
		{
			name:   "missing subject_token",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(_ *testing.T) url.Values {
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "subject_token",
		},
		{
			name:   "missing subject_token_type",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				token := tj.signToken(t, validClaims(), validExtraClaims())
				return url.Values{
					"grant_type":    {oauthproto.GrantTypeTokenExchange},
					"subject_token": {token},
				}
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "subject_token_type",
		},
		{
			name:   "invalid subject_token_type",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				token := tj.signToken(t, validClaims(), validExtraClaims())
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {"urn:ietf:params:oauth:token-type:refresh_token"},
				}
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "subject_token_type",
		},
		{
			name:         "public client rejected",
			ctx:          func(_ *testing.T) context.Context { return context.Background() },
			client:       func() *fosite.DefaultClient { c := defaultClient(); c.Public = true; return c },
			form:         func(t *testing.T) url.Values { t.Helper(); return defaultFormValues(t, tj) },
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidGrant,
			hintContains: "public",
		},
		{
			name: "subject token client_id mismatch",
			ctx:  func(_ *testing.T) context.Context { return context.Background() },
			client: func() *fosite.DefaultClient {
				c := defaultClient()
				c.ID = "different-client"
				return c
			},
			form:         func(t *testing.T) url.Values { t.Helper(); return defaultFormValues(t, tj) },
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidGrant,
			hintContains: "different client",
		},
		{
			name:   "invalid subject_token — bad JWT",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(_ *testing.T) url.Values {
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {"not-a-valid-jwt"},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidGrant,
			hintContains: "subject token is invalid",
		},
		{
			name:   "unsupported requested_token_type rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("requested_token_type", "urn:ietf:params:oauth:token-type:id_token")
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "requested_token_type",
		},
		{
			name:   "delegation lifetime capped by subject token expiry",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				// Subject token expires in 5 minutes.
				claims := validClaims()
				claims.Expiry = jwt.NewNumericDate(time.Now().Add(5 * time.Minute))
				token := tj.signToken(t, claims, validExtraClaims())
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
			},
			lifespan: 15 * time.Minute, // Handler's max is 15 min, but subject expires in 5.
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()

				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok)

				expiry := sess.GetExpiresAt(fosite.AccessToken)
				// The delegated token should expire within ~5 minutes, not 15.
				remaining := time.Until(expiry)
				assert.Less(t, remaining, 6*time.Minute,
					"delegated token lifetime should be capped by subject token expiry")
				assert.Greater(t, remaining, 4*time.Minute,
					"delegated token lifetime should be close to subject token remaining time")
			},
		},
		{
			name:         "wrong grant type returns ErrUnknownRequest",
			ctx:          func(_ *testing.T) context.Context { return context.Background() },
			client:       defaultClient,
			form:         func(t *testing.T) url.Values { t.Helper(); return defaultFormValues(t, tj) },
			grantTypes:   fosite.Arguments{"client_credentials"},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrUnknownRequest,
		},
		{
			name: "requested scope not allowed by client",
			ctx:  func(_ *testing.T) context.Context { return context.Background() },
			client: func() *fosite.DefaultClient {
				c := defaultClient()
				c.Scopes = fosite.Arguments{"openid"} // No "admin" scope.
				return c
			},
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("scope", "admin")
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidScope,
			hintContains: "scope",
		},
		{
			name: "scope escalation blocked by subject token",
			ctx:  func(_ *testing.T) context.Context { return context.Background() },
			client: func() *fosite.DefaultClient {
				c := defaultClient()
				c.Scopes = fosite.Arguments{"openid", "profile", "admin"}
				return c
			},
			form: func(t *testing.T) url.Values {
				t.Helper()
				// Subject token has scope="openid" only.
				claims := validClaims()
				extra := validExtraClaims()
				extra["scope"] = "openid"
				token := tj.signToken(t, claims, extra)
				f := url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
				f.Set("scope", "admin") // Client has admin, but subject token doesn't.
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidScope,
			hintContains: "subject token",
		},
		{
			name:   "may_act claim authorizes this client",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				extra := validExtraClaims()
				extra["may_act"] = map[string]any{"sub": testAgentClientID}
				token := tj.signToken(t, validClaims(), extra)
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
			},
			lifespan: 15 * time.Minute,
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()
				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok, "session should be *session.Session")
				assert.Equal(t, "user-123", sess.JWTClaims.Subject)
				actClaim, exists := sess.JWTClaims.Extra["act"]
				require.True(t, exists, "act claim must be present")
				actMap, ok := actClaim.(map[string]any)
				require.True(t, ok, "act claim must be a map")
				assert.Equal(t, testAgentClientID, actMap["sub"])
			},
		},
		{
			name:   "may_act claim does not authorize this client",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				extra := validExtraClaims()
				extra["may_act"] = map[string]any{"sub": "different-agent"}
				token := tj.signToken(t, validClaims(), extra)
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidGrant,
			hintContains: "does not authorize",
		},
		{
			name:   "may_act authorizes cross-client delegation",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				extra := validExtraClaims()
				extra["client_id"] = "original-client" // different from the authenticated client
				extra["may_act"] = map[string]any{"sub": testAgentClientID}
				token := tj.signToken(t, validClaims(), extra)
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
			},
			lifespan: 15 * time.Minute,
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()
				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok, "session should be *session.Session")
				assert.Equal(t, "user-123", sess.JWTClaims.Subject)
				actClaim, exists := sess.JWTClaims.Extra["act"]
				require.True(t, exists, "act claim must be present")
				actMap, ok := actClaim.(map[string]any)
				require.True(t, ok, "act claim must be a map")
				assert.Equal(t, testAgentClientID, actMap["sub"])
			},
		},
		{
			name:   "subject token with no may_act and no client_id rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				extra := map[string]any{
					"name":  "Test User",
					"email": "test@example.com",
				}
				token := tj.signToken(t, validClaims(), extra)
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidGrant,
			hintContains: "no verifiable client binding",
		},
		{
			name:   "subject token's own act claim is nested under the new act claim",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				extra := validExtraClaims()
				extra["act"] = map[string]any{"sub": "original-agent"}
				token := tj.signToken(t, validClaims(), extra)
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
			},
			lifespan: 15 * time.Minute,
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()
				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok, "session should be *session.Session")
				actClaim, exists := sess.JWTClaims.Extra["act"]
				require.True(t, exists, "act claim must be present")
				actMap, ok := actClaim.(map[string]any)
				require.True(t, ok, "act claim must be a map")
				assert.Equal(t, testAgentClientID, actMap["sub"])
				priorAct, ok := actMap["act"].(map[string]any)
				require.True(t, ok, "prior act claim should be nested under the new act claim")
				assert.Equal(t, "original-agent", priorAct["sub"])
			},
		},
		{
			name: "requested audience not allowed by client",
			ctx:  func(_ *testing.T) context.Context { return context.Background() },
			client: func() *fosite.DefaultClient {
				c := defaultClient()
				c.Audience = []string{"https://allowed.example.com"}
				return c
			},
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("audience", "https://not-allowed.example.com")
				return f
			},
			lifespan: 15 * time.Minute,
			wantErr:  true,
		},
		{
			// The subject token is valid only for testIssuer, but the client is
			// registered for a second audience. Requesting that second audience
			// must be rejected: delegation cannot broaden the resource boundary.
			name: "audience escalation blocked by subject token",
			ctx:  func(_ *testing.T) context.Context { return context.Background() },
			client: func() *fosite.DefaultClient {
				c := defaultClient()
				c.Audience = []string{testIssuer, "https://other.example.com"}
				return c
			},
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("audience", "https://other.example.com")
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: server.ErrInvalidTarget,
			hintContains: "not covered by the subject token",
		},
		{
			// The subject token carries both audiences, so requesting one of them
			// is within the delegated boundary and must succeed.
			name: "audience within subject token granted",
			ctx:  func(_ *testing.T) context.Context { return context.Background() },
			client: func() *fosite.DefaultClient {
				c := defaultClient()
				c.Audience = []string{testIssuer, "https://other.example.com"}
				return c
			},
			form: func(t *testing.T) url.Values {
				t.Helper()
				claims := validClaims()
				claims.Audience = jwt.Audience{testIssuer, "https://other.example.com"}
				token := tj.signToken(t, claims, validExtraClaims())
				f := url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
				f.Set("audience", "https://other.example.com")
				return f
			},
			lifespan: 15 * time.Minute,
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()
				assert.Contains(t, req.GetGrantedAudience(), "https://other.example.com",
					"audience covered by the subject token should be granted")
			},
		},
		{
			name:   "valid resource parameter granted as audience",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("resource", testIssuer)
				return f
			},
			lifespan: 15 * time.Minute,
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()
				audiences := req.GetGrantedAudience()
				assert.Contains(t, audiences, testIssuer,
					"resource parameter should be granted as audience")
			},
		},
		{
			name:   "resource not in allowed audiences rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("resource", "https://evil.example.com")
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			hintContains: "not a registered audience",
		},
		{
			name:   "multiple resource parameters rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Add("resource", testIssuer)
				f.Add("resource", "https://other.example.com")
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			hintContains: "Multiple resource parameters",
		},
		{
			name:   "resource with invalid URI rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("resource", "not-a-uri")
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			hintContains: "absolute URI",
		},
		{
			// Server-wide allowedAudiences (set by newTestHandler to testIssuer)
			// permits testIssuer, but the client is not registered for it — the
			// per-client audience strategy must still reject the resource param.
			name: "resource not permitted for this client rejected",
			ctx:  func(_ *testing.T) context.Context { return context.Background() },
			client: func() *fosite.DefaultClient {
				c := defaultClient()
				c.Audience = []string{"https://other.example.com"}
				return c
			},
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("resource", testIssuer)
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: server.ErrInvalidTarget,
			hintContains: "not permitted",
		},
		{
			name: "confidential client without token-exchange grant type rejected",
			ctx:  func(_ *testing.T) context.Context { return context.Background() },
			client: func() *fosite.DefaultClient {
				c := defaultClient()
				c.GrantTypes = fosite.Arguments{"client_credentials"}
				return c
			},
			form:         func(t *testing.T) url.Values { t.Helper(); return defaultFormValues(t, tj) },
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrUnauthorizedClient,
			hintContains: "not allowed to use authorization grant",
		},
		{
			name:   "delegation chain at max depth rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				extra := validExtraClaims()
				extra["act"] = nestedActChain(maxDelegationDepth)
				token := tj.signToken(t, validClaims(), extra)
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidGrant,
			hintContains: "too deep",
		},
		{
			name:   "delegation chain under max depth is nested",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				extra := validExtraClaims()
				extra["act"] = nestedActChain(maxDelegationDepth - 1)
				token := tj.signToken(t, validClaims(), extra)
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
			},
			lifespan: 15 * time.Minute,
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()
				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok, "session should be *session.Session")
				actClaim, exists := sess.JWTClaims.Extra["act"]
				require.True(t, exists, "act claim must be present")
				actMap, ok := actClaim.(map[string]any)
				require.True(t, ok, "act claim must be a map")
				assert.Equal(t, testAgentClientID, actMap["sub"])
				assert.Equal(t, nestedActChain(maxDelegationDepth-1), actMap["act"],
					"the prior act chain should be nested unchanged under the new act claim")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestHandler(t, tj, tt.lifespan)
			ctx := tt.ctx(t)
			client := tt.client()
			form := tt.form(t)

			req := newAccessRequest(t, client, form)

			// Override grant type if specified in the test case.
			if tt.grantTypes != nil {
				req.GrantTypes = tt.grantTypes
			}

			// Set requested scopes from the form if present.
			if scopeStr := form.Get("scope"); scopeStr != "" {
				req.SetRequestedScopes(fosite.Arguments{scopeStr})
			}

			// Set requested audience from the form if present.
			if audStr := form.Get("audience"); audStr != "" {
				req.SetRequestedAudience(fosite.Arguments{audStr})
			}

			err := h.HandleTokenEndpointRequest(ctx, req)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantFositeIs != nil {
					assert.True(t, errors.Is(err, tt.wantFositeIs),
						"expected error to match %v, got: %v", tt.wantFositeIs.ErrorField, err)
				}
				if tt.hintContains != "" {
					var rfcErr *fosite.RFC6749Error
					require.True(t, errors.As(err, &rfcErr), "expected fosite RFC6749Error")
					assert.Contains(t, rfcErr.Reason(), tt.hintContains,
						"fosite error hint should contain %q", tt.hintContains)
				}
				return
			}

			require.NoError(t, err)
			if tt.check != nil {
				tt.check(t, req)
			}
		})
	}
}

func TestTokenExchangeHandler_DefaultAudience(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)

	tests := []struct {
		name             string
		allowedAudiences []string
		client           func() *fosite.DefaultClient // defaults to defaultClient if nil
		wantErr          bool
		hintContains     string // defaults to "unambiguous default audience" if empty
		wantAudience     string
	}{
		{
			name:             "single allowed audience defaults when none requested",
			allowedAudiences: []string{testIssuer},
			wantAudience:     testIssuer,
		},
		{
			name:             "no allowed audiences configured rejects the request",
			allowedAudiences: nil,
			wantErr:          true,
		},
		{
			name:             "multiple allowed audiences rejects the ambiguous request",
			allowedAudiences: []string{testIssuer, "https://other.example.com"},
			wantErr:          true,
		},
		{
			// The sole configured audience is unambiguous server-wide, but the
			// client is not registered for it — the per-client check must still
			// reject rather than silently granting an unregistered audience.
			name:             "default audience not permitted for this client rejected",
			allowedAudiences: []string{testIssuer},
			client: func() *fosite.DefaultClient {
				c := defaultClient()
				c.Audience = []string{"https://other.example.com"}
				return c
			},
			wantErr:      true,
			hintContains: "not permitted to request a token for the default audience",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// newTestHandler wires the subject-token validator's allowed
			// audiences to testIssuer regardless of tt.allowedAudiences —
			// that governs the subject token's own "aud" claim, a separate
			// concern from the delegated token's audience under test here.
			h := newTestHandler(t, tj, 15*time.Minute)
			h.allowedAudiences = tt.allowedAudiences

			client := defaultClient
			if tt.client != nil {
				client = tt.client
			}
			req := newAccessRequest(t, client(), defaultFormValues(t, tj))
			err := h.HandleTokenEndpointRequest(context.Background(), req)

			if tt.wantErr {
				require.Error(t, err)
				var rfcErr *fosite.RFC6749Error
				require.True(t, errors.As(err, &rfcErr), "expected fosite RFC6749Error")
				hintContains := tt.hintContains
				if hintContains == "" {
					hintContains = "unambiguous default audience"
				}
				assert.Contains(t, rfcErr.Reason(), hintContains)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, req.GetGrantedAudience(), tt.wantAudience)
		})
	}
}

func TestActChainDepth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		act  any
		want int
	}{
		{name: "nil is depth 0", act: nil, want: 0},
		{name: "non-map is depth 0", act: "not-a-map", want: 0},
		{name: "single act entry is depth 1", act: nestedActChain(1), want: 1},
		{name: "nested chain reports its full depth", act: nestedActChain(5), want: 5},
		{name: "map without a nested act claim stops at depth 1", act: map[string]any{"sub": "agent"}, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, actChainDepth(tt.act))
		})
	}
}

// mockConfig implements the tokenExchangeConfig interface for testing.
type mockConfig struct {
	scopeStrategy    fosite.ScopeStrategy
	audienceStrategy fosite.AudienceMatchingStrategy
	accessLifespan   time.Duration
}

func (m *mockConfig) GetScopeStrategy(_ context.Context) fosite.ScopeStrategy {
	return m.scopeStrategy
}

func (m *mockConfig) GetAudienceStrategy(_ context.Context) fosite.AudienceMatchingStrategy {
	return m.audienceStrategy
}

func (m *mockConfig) GetAccessTokenLifespan(_ context.Context) time.Duration {
	if m.accessLifespan > 0 {
		return m.accessLifespan
	}
	return 15 * time.Minute
}

// mockAccessTokenStrategy is a minimal oauth2.AccessTokenStrategy that issues
// deterministic opaque tokens without any JWT signing machinery.
type mockAccessTokenStrategy struct {
	generateErr error
}

func (*mockAccessTokenStrategy) AccessTokenSignature(_ context.Context, token string) string {
	return "sig-" + token
}

func (m *mockAccessTokenStrategy) GenerateAccessToken(_ context.Context, _ fosite.Requester) (string, string, error) {
	if m.generateErr != nil {
		return "", "", m.generateErr
	}
	return "test-access-token", "test-signature", nil
}

func (*mockAccessTokenStrategy) ValidateAccessToken(_ context.Context, _ fosite.Requester, _ string) error {
	return nil
}

// mockAccessTokenStorage is a minimal oauth2.AccessTokenStorage that records
// the persisted session signature and never fails.
type mockAccessTokenStorage struct {
	createdSig  string
	createErr   error
	createdReqs []fosite.Requester
}

func (m *mockAccessTokenStorage) CreateAccessTokenSession(_ context.Context, signature string, req fosite.Requester) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.createdSig = signature
	m.createdReqs = append(m.createdReqs, req)
	return nil
}

func (*mockAccessTokenStorage) GetAccessTokenSession(_ context.Context, _ string, _ fosite.Session) (fosite.Requester, error) {
	return nil, fosite.ErrNotFound
}

func (*mockAccessTokenStorage) DeleteAccessTokenSession(_ context.Context, _ string) error {
	return nil
}

// mockHandleHelperConfig implements oauth2.HandleHelperConfigProvider, providing
// both access and refresh token lifespans required by HandleHelper.
type mockHandleHelperConfig struct {
	accessLifespan  time.Duration
	refreshLifespan time.Duration
}

func (m *mockHandleHelperConfig) GetAccessTokenLifespan(_ context.Context) time.Duration {
	if m.accessLifespan > 0 {
		return m.accessLifespan
	}
	return 15 * time.Minute
}

func (m *mockHandleHelperConfig) GetRefreshTokenLifespan(_ context.Context) time.Duration {
	if m.refreshLifespan > 0 {
		return m.refreshLifespan
	}
	return 30 * 24 * time.Hour
}

// newTestHandlerWithHelper builds a Handler wired with a real oauth2.HandleHelper
// backed by mock strategy/storage, enabling PopulateTokenEndpointResponse tests.
// The delegation lifespan is fixed at 15 minutes; only accessLifespan varies
// across callers.
func newTestHandlerWithHelper(t *testing.T, tj *testJWKS, accessLifespan time.Duration,
	strategy *mockAccessTokenStrategy, storage *mockAccessTokenStorage) *Handler {
	t.Helper()

	const delegationLifespan = 15 * time.Minute

	validator, err := NewSubjectTokenValidator(tj.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)

	cfg := &mockConfig{
		scopeStrategy:    fosite.ExactScopeStrategy,
		audienceStrategy: fosite.DefaultAudienceMatchingStrategy,
		accessLifespan:   accessLifespan,
	}

	return &Handler{
		HandleHelper: &oauth2.HandleHelper{
			AccessTokenStrategy: strategy,
			AccessTokenStorage:  storage,
			Config: &mockHandleHelperConfig{
				accessLifespan: accessLifespan,
			},
		},
		validator:          validator,
		delegationLifespan: delegationLifespan,
		allowedAudiences:   []string{testIssuer},
		config:             cfg,
	}
}

func TestTokenExchangeHandler_PopulateTokenEndpointResponse(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)

	t.Run("wrong grant type returns ErrUnknownRequest", func(t *testing.T) {
		t.Parallel()

		strategy := &mockAccessTokenStrategy{}
		storage := &mockAccessTokenStorage{}
		h := newTestHandlerWithHelper(t, tj, 15*time.Minute, strategy, storage)

		req := newAccessRequest(t, defaultClient(), defaultFormValues(t, tj))
		// Override grant type so CanHandleTokenEndpointRequest returns false.
		req.GrantTypes = fosite.Arguments{"client_credentials"}

		responder := fosite.NewAccessResponse()
		err := h.PopulateTokenEndpointResponse(context.Background(), req, responder)

		require.Error(t, err)
		assert.True(t, errors.Is(err, fosite.ErrUnknownRequest),
			"expected ErrUnknownRequest, got: %v", err)
		// No token should have been issued on this error path.
		assert.Empty(t, responder.GetAccessToken())
		assert.Empty(t, storage.createdSig)
	})

	t.Run("client without token-exchange grant returns ErrUnauthorizedClient", func(t *testing.T) {
		t.Parallel()

		strategy := &mockAccessTokenStrategy{}
		storage := &mockAccessTokenStorage{}
		h := newTestHandlerWithHelper(t, tj, 15*time.Minute, strategy, storage)

		// Client is allowed client_credentials only, not token-exchange.
		client := defaultClient()
		client.GrantTypes = fosite.Arguments{"client_credentials"}
		req := newAccessRequest(t, client, defaultFormValues(t, tj))
		req.SetSession(&session.Session{})

		responder := fosite.NewAccessResponse()
		err := h.PopulateTokenEndpointResponse(context.Background(), req, responder)

		require.Error(t, err)
		assert.True(t, errors.Is(err, fosite.ErrUnauthorizedClient),
			"expected ErrUnauthorizedClient, got: %v", err)
		// No token should have been issued.
		assert.Empty(t, responder.GetAccessToken())
		assert.Empty(t, storage.createdSig)
	})

	t.Run("successful populate sets issued_token_type and access token", func(t *testing.T) {
		t.Parallel()

		strategy := &mockAccessTokenStrategy{}
		storage := &mockAccessTokenStorage{}
		// Access token lifespan larger than the delegation lifespan so the
		// session expiry (capped at delegation lifespan) is the binding lifetime.
		h := newTestHandlerWithHelper(t, tj, time.Hour, strategy, storage)

		req := newAccessRequest(t, defaultClient(), defaultFormValues(t, tj))
		require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))

		responder := fosite.NewAccessResponse()
		require.NoError(t, h.PopulateTokenEndpointResponse(context.Background(), req, responder))

		// RFC 8693 Section 2.2.1: response MUST include issued_token_type.
		assert.Equal(t, oauthproto.TokenTypeAccessToken, responder.GetExtra("issued_token_type"))
		// The mock strategy issues a fixed opaque token; verify it propagated.
		assert.Equal(t, "test-access-token", responder.GetAccessToken())
		assert.Equal(t, "bearer", responder.GetTokenType())
		// The session should have been persisted via CreateAccessTokenSession.
		assert.Equal(t, "test-signature", storage.createdSig)
		require.Len(t, storage.createdReqs, 1)
	})

	t.Run("lifetime capped by subject token expiry", func(t *testing.T) {
		t.Parallel()

		strategy := &mockAccessTokenStrategy{}
		storage := &mockAccessTokenStorage{}
		// Subject token expires in 5 minutes; delegation max is 15; access
		// token lifespan default is 1h. The effective issued lifetime must be
		// capped at the subject token's remaining life (~5 minutes).
		h := newTestHandlerWithHelper(t, tj, time.Hour, strategy, storage)

		claims := validClaims()
		claims.Expiry = jwt.NewNumericDate(time.Now().Add(5 * time.Minute))
		token := tj.signToken(t, claims, validExtraClaims())
		form := url.Values{
			"grant_type":         {oauthproto.GrantTypeTokenExchange},
			"subject_token":      {token},
			"subject_token_type": {oauthproto.TokenTypeAccessToken},
		}

		req := newAccessRequest(t, defaultClient(), form)
		require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))

		responder := fosite.NewAccessResponse()
		require.NoError(t, h.PopulateTokenEndpointResponse(context.Background(), req, responder))

		// getExpiresIn in HandleHelper computes expires_in from the session's
		// ExpiresAt minus now. The capping logic in PopulateTokenEndpointResponse
		// sets that ExpiresAt from the delegation lifetime computation (min of
		// subject remaining and delegationLifespan). Subject expires in ~5 min,
		// delegationLifespan is 15 min, so ExpiresIn should be ~5 min (< 6 min).
		expiresIn := responder.ToMap()["expires_in"]
		require.NotNil(t, expiresIn)
		// fosite serializes expires_in as an int (seconds). Tolerate the type.
		var secs int64
		switch v := expiresIn.(type) {
		case int:
			secs = int64(v)
		case int64:
			secs = v
		case float64:
			secs = int64(v)
		default:
			t.Fatalf("unexpected expires_in type %T: %v", expiresIn, expiresIn)
		}
		assert.Greater(t, secs, int64((4 * time.Minute).Seconds()),
			"expires_in should be capped near subject token remaining (~5m), got %d", secs)
		assert.Less(t, secs, int64((6 * time.Minute).Seconds()),
			"expires_in should be capped near subject token remaining (~5m), got %d", secs)
	})

	t.Run("AccessTokenStrategy error propagates", func(t *testing.T) {
		t.Parallel()

		strategy := &mockAccessTokenStrategy{
			generateErr: errors.New("strategy failure"),
		}
		storage := &mockAccessTokenStorage{}
		h := newTestHandlerWithHelper(t, tj, time.Hour, strategy, storage)

		req := newAccessRequest(t, defaultClient(), defaultFormValues(t, tj))
		require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))

		responder := fosite.NewAccessResponse()
		err := h.PopulateTokenEndpointResponse(context.Background(), req, responder)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "strategy failure")
		// No session should have been persisted on failure.
		assert.Empty(t, storage.createdSig)
	})

	t.Run("stale session expiry fails closed instead of falling back to full lifespan", func(t *testing.T) {
		t.Parallel()

		strategy := &mockAccessTokenStrategy{}
		storage := &mockAccessTokenStorage{}
		h := newTestHandlerWithHelper(t, tj, time.Hour, strategy, storage)

		req := newAccessRequest(t, defaultClient(), defaultFormValues(t, tj))
		require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))

		// Simulate the narrow window where the session's bound expiry (set by
		// HandleTokenEndpointRequest) elapses before PopulateTokenEndpointResponse runs.
		sess, ok := req.GetSession().(*session.Session)
		require.True(t, ok)
		sess.SetExpiresAt(fosite.AccessToken, time.Now().Add(-time.Second))

		responder := fosite.NewAccessResponse()
		err := h.PopulateTokenEndpointResponse(context.Background(), req, responder)

		require.Error(t, err)
		assert.True(t, errors.Is(err, fosite.ErrInvalidGrant),
			"expected ErrInvalidGrant, got: %v", err)
		assert.Empty(t, responder.GetAccessToken())
		assert.Empty(t, storage.createdSig)
	})

	t.Run("AccessTokenStorage error propagates", func(t *testing.T) {
		t.Parallel()

		strategy := &mockAccessTokenStrategy{}
		storage := &mockAccessTokenStorage{
			createErr: errors.New("storage failure"),
		}
		h := newTestHandlerWithHelper(t, tj, time.Hour, strategy, storage)

		req := newAccessRequest(t, defaultClient(), defaultFormValues(t, tj))
		require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))

		responder := fosite.NewAccessResponse()
		err := h.PopulateTokenEndpointResponse(context.Background(), req, responder)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "storage failure")
		// issued_token_type must NOT be set when token issuance failed.
		assert.Nil(t, responder.GetExtra("issued_token_type"))
	})
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coreaudit "github.com/stacklok/toolhive-core/audit"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const testAgentClientID = "devops-agent"

// newTestHandler creates a TokenExchangeHandler wired to the given test JWKS and
// delegation lifespan. It does not need a full fosite strategy stack because
// HandleTokenEndpointRequest (which is the focus of these tests) does not issue tokens.
func newTestHandler(t *testing.T, tj *testJWKS, delegationLifespan time.Duration) *Handler {
	t.Helper()

	validator, err := NewSelfIssuedTokenValidator(tj.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)

	return &Handler{
		// HandleHelper is nil — we only test HandleTokenEndpointRequest, not
		// PopulateTokenEndpointResponse, so IssueAccessToken is never called.
		HandleHelper:       nil,
		validator:          validator,
		selfValidator:      validator,
		issuer:             testIssuer,
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

// actFormValues returns valid token exchange form values whose subject token
// carries the given "act" claim.
func actFormValues(t *testing.T, tj *testJWKS, act any) url.Values {
	t.Helper()

	extra := validExtraClaims()
	extra["act"] = act
	form := defaultFormValues(t, tj)
	form.Set("subject_token", tj.signToken(t, validClaims(), extra))
	return form
}

// nestedActChain builds a chain of depth nested "act" maps, each holding a
// distinct "sub", for exercising the delegation-depth cap. depth must be at
// least 1.
func nestedActChain(depth int) map[string]any {
	chain := map[string]any{"sub": fmt.Sprintf("agent-%d", depth-1)}
	for i := depth - 2; i >= 0; i-- {
		chain = map[string]any{"sub": fmt.Sprintf("agent-%d", i), "act": chain}
	}
	return chain
}

// signActorToken creates a self-issued JWT suitable for use as an actor_token.
// The sub claim is set to the given subject (the asserted actor identity),
// and the "client_id" claim is set to testAgentClientID (the authenticated
// client the token is bound to).
func signActorToken(t *testing.T, tj *testJWKS, subject string) string {
	t.Helper()
	return signActorTokenForClient(t, tj, subject, testAgentClientID)
}

// signActorTokenForClient is signActorToken with an explicit "client_id"
// claim, for exercising the binding check against a client other than
// testAgentClientID.
func signActorTokenForClient(t *testing.T, tj *testJWKS, subject, clientID string) string {
	t.Helper()

	now := time.Now()
	claims := jwt.Claims{
		Subject:  subject,
		Issuer:   testIssuer,
		Audience: jwt.Audience{testIssuer},
		Expiry:   jwt.NewNumericDate(now.Add(time.Hour)),
		IssuedAt: jwt.NewNumericDate(now),
	}
	return tj.signToken(t, claims, map[string]any{
		"client_id": clientID,
	})
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

	tests := []struct {
		name       string
		grantTypes fosite.Arguments
		want       bool
	}{
		{name: "token exchange requires authentication", grantTypes: fosite.Arguments{oauthproto.GrantTypeTokenExchange}},
		{name: "other grants require authentication", grantTypes: fosite.Arguments{oauthproto.GrantTypeJWTBearer}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := fosite.NewAccessRequest(&session.Session{})
			req.GrantTypes = tt.grantTypes
			assert.Equal(t, tt.want, h.CanSkipClientAuth(context.Background(), req))
		})
	}
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
			name:     "valid exchange with actor_token",
			ctx:      func(_ *testing.T) context.Context { return context.Background() },
			client:   defaultClient,
			lifespan: 15 * time.Minute,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("actor_token", signActorToken(t, tj, testAgentClientID))
				f.Set("actor_token_type", oauthproto.TokenTypeJWT)
				return f
			},
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()

				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok, "session should be *session.Session")

				// Verify subject is the user from the subject token.
				assert.Equal(t, "user-123", sess.JWTClaims.Subject)

				// Verify the act claim uses the actor token's sub.
				actClaim, exists := sess.JWTClaims.Extra["act"]
				require.True(t, exists, "act claim must be present")
				actMap, ok := actClaim.(map[string]interface{})
				require.True(t, ok, "act claim must be a map")
				assert.Equal(t, testAgentClientID, actMap["sub"])
			},
		},
		{
			name:     "actor_token asserts a distinct actor identity",
			ctx:      func(_ *testing.T) context.Context { return context.Background() },
			client:   defaultClient,
			lifespan: 15 * time.Minute,
			form: func(t *testing.T) url.Values {
				t.Helper()
				// The subject token's client_id claim matches the
				// authenticated client (testAgentClientID) — the normal
				// binding case, unaffected by actor_token. actor_token
				// separately asserts a distinct actor ("other-agent"); that
				// identity flows into act.sub only, never into client-binding
				// checks or the issued token's own client_id (see the
				// "cannot bypass" test below for the case that used to be
				// confused with this one).
				f := defaultFormValues(t, tj)
				f.Set("actor_token", signActorToken(t, tj, "other-agent"))
				f.Set("actor_token_type", oauthproto.TokenTypeJWT)
				return f
			},
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()

				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok, "session should be *session.Session")

				actClaim, exists := sess.JWTClaims.Extra["act"]
				require.True(t, exists, "act claim must be present")
				actMap, ok := actClaim.(map[string]interface{})
				require.True(t, ok, "act claim must be a map")
				assert.Equal(t, "other-agent", actMap["sub"])

				// The issued token's client_id (RFC 9068) must identify the
				// authenticated client, not the asserted actor.
				assert.Equal(t, testAgentClientID, sess.JWTClaims.Extra["client_id"])
			},
		},
		{
			// Regression guard for the exact bug the actor_token/client_id
			// binding split fixes: previously, checkDelegationConsent's
			// client_id fallback compared the subject token's client_id
			// against the *asserted actor* (actorSub), not the authenticated
			// client. That let a client authenticated as testAgentClientID
			// exchange a subject token issued to ANY other client, simply by
			// presenting an actor_token whose sub happened to equal that
			// subject token's client_id — a complete bypass of "the subject
			// token was issued to a different client". All four identities
			// here are distinct on purpose: authenticated client
			// (testAgentClientID), actor_token's client_id claim
			// (testAgentClientID, so the actor_token binding itself passes),
			// actor_token's sub ("other-agent"), and the subject token's own
			// client_id claim ("other-agent", chosen to equal the actor sub —
			// exactly the value that used to unlock the bypass).
			name:     "actor_token cannot bypass subject-token client_id binding",
			ctx:      func(_ *testing.T) context.Context { return context.Background() },
			client:   defaultClient,
			lifespan: 15 * time.Minute,
			form: func(t *testing.T) url.Values {
				t.Helper()
				extra := validExtraClaims()
				extra["client_id"] = "other-agent"
				subjectToken := tj.signToken(t, validClaims(), extra)
				f := url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {subjectToken},
					"subject_token_type": {oauthproto.TokenTypeAccessToken},
				}
				f.Set("actor_token", signActorToken(t, tj, "other-agent"))
				f.Set("actor_token_type", oauthproto.TokenTypeJWT)
				return f
			},
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidGrant,
			hintContains: "different client",
		},
		{
			name:     "actor_token client_id mismatch with authenticated client",
			ctx:      func(_ *testing.T) context.Context { return context.Background() },
			client:   defaultClient,
			lifespan: 15 * time.Minute,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				// The actor_token's client_id claim names a different client
				// than the one authenticating this request, so the binding
				// check must reject it regardless of the asserted actor sub.
				f.Set("actor_token", signActorTokenForClient(t, tj, "some-actor", "a-different-client"))
				f.Set("actor_token_type", oauthproto.TokenTypeJWT)
				return f
			},
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "does not match the authenticated client identity",
		},
		{
			name:     "invalid actor_token returns invalid_request",
			ctx:      func(_ *testing.T) context.Context { return context.Background() },
			client:   defaultClient,
			lifespan: 15 * time.Minute,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("actor_token", "not-a-valid-jwt")
				f.Set("actor_token_type", oauthproto.TokenTypeJWT)
				return f
			},
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "actor token is invalid",
		},
		{
			name:   "actor_token without actor_token_type",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("actor_token", signActorToken(t, tj, testAgentClientID))
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "actor_token_type",
		},
		{
			name:   "actor_token_type without actor_token",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("actor_token_type", oauthproto.TokenTypeJWT)
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "actor_token_type",
		},
		{
			name:   "id_token actor_token_type rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("actor_token", signActorToken(t, tj, testAgentClientID))
				f.Set("actor_token_type", oauthproto.TokenTypeIDToken)
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "access_token",
		},
		{
			name:   "id_token subject_token_type rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				token := tj.signToken(t, validClaims(), validExtraClaims())
				return url.Values{
					"grant_type":         {oauthproto.GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {oauthproto.TokenTypeIDToken},
				}
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "subject_token_type",
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
			wantFositeIs: fosite.ErrInvalidRequest,
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
			// Regression test: a subject token's prior act claim can carry
			// non-identity claims (here "role") that we never validated and
			// did not mint. Re-signing them verbatim under ToolHive's own
			// key would present them as vouched-for. buildActClaim must
			// rebuild the nested act from iss/sub only (chainToAct) and drop
			// everything else.
			name:   "valid prior iss and sub hop is normalized without extras",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				return actFormValues(t, tj, map[string]any{
					"iss":  "https://prior-issuer.example.com",
					"sub":  "original-agent",
					"role": "admin",
				})
			},
			lifespan: 15 * time.Minute,
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()
				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok, "session should be *session.Session")
				actMap, ok := sess.JWTClaims.Extra["act"].(map[string]any)
				require.True(t, ok, "act claim must be a map")
				priorAct, ok := actMap["act"].(map[string]any)
				require.True(t, ok, "prior act claim should be nested under the new act claim")
				assert.Equal(t, map[string]any{
					"iss": "https://prior-issuer.example.com",
					"sub": "original-agent",
				}, priorAct,
					"only iss/sub survive; the untrusted 'role' claim must not be re-signed")
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
			wantFositeIs: fosite.ErrInvalidRequest,
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
		{
			// A single shallow hop can still blow the encoded-size cap if its
			// own iss/sub value is large, independent of maxDelegationDepth
			// (depth here is 1, far under the depth gate) — this exercises the
			// size gate on its own, not as a side effect of depth. The hop's
			// non-identity claims no longer count toward this: buildActClaim
			// rebuilds the prior chain from iss/sub only (see chainToAct), so
			// an oversized unrelated claim ("filler") would be stripped
			// before the size check and never trigger it.
			name:   "act claim exceeding encoded size cap rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				extra := validExtraClaims()
				extra["act"] = map[string]any{
					"sub": strings.Repeat("a", maxActClaimSize),
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
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "too large",
		},
		{
			// RFC 8693 Section 4.1 requires act to be a JSON object. Rejecting keeps
			// us from re-minting the violation. A non-object array or scalar reaches
			// the same parser branch, so the string case covers both.
			name:   "non-object act rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				return actFormValues(t, tj, "some-agent")
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "act_not_object",
		},
		{
			name:   "non-object nested act rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				return actFormValues(t, tj, map[string]any{"sub": "agent-0", "act": "agent-1"})
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "nested_act_not_object",
		},
		{
			// RFC 7519 Section 4.1.2 fixes sub as a string, so a hop carrying a
			// non-string sub is non-conformant even though the chain is an object.
			name:   "non-string sub in act hop rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				return actFormValues(t, tj, map[string]any{"sub": 42})
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "sub_not_string",
		},
		{
			// RFC 7519 Section 4.1.1 fixes iss as a string too. sub is kept valid
			// here to isolate the reason: the parser reports the first violation it
			// finds and checks iss before sub, so a hop with both would report only
			// iss_not_string and leave this branch unpinned.
			name:   "non-string iss in act hop rejected",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				return actFormValues(t, tj, map[string]any{"iss": 42, "sub": "agent-0"})
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "iss_not_string",
		},
		{
			// A JSON-null nested act ends the chain; it asserts no further
			// delegation and must not read as malformed. The rebuilt hop
			// carries no "act" key at all (see chainToAct) — an explicit
			// null and an absent key both mean "no further delegation", and
			// normalization does not need to preserve the distinction.
			name:   "null nested act is accepted and normalized to a single hop",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				return actFormValues(t, tj, map[string]any{"sub": "agent-0", "act": nil})
			},
			lifespan: 15 * time.Minute,
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()
				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok, "session should be *session.Session")
				actMap, ok := sess.JWTClaims.Extra["act"].(map[string]any)
				require.True(t, ok, "act claim must be a map")
				assert.Equal(t, testAgentClientID, actMap["sub"])
				assert.Equal(t, map[string]any{"sub": "agent-0"}, actMap["act"],
					"the prior act claim is rebuilt from iss/sub only, dropping the null act member")
			},
		},
		{
			// Tolerated by design: RFC 8693 Section 4.1 only requires an object, and
			// sub is conventional rather than mandatory, so an empty hop is
			// conformant. Pinned so a future tightening of the shared parser breaks
			// a test rather than a caller.
			name:   "empty outer act hop is rejected during issuance",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				return actFormValues(t, tj, map[string]any{})
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "empty actor",
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()
				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok, "session should be *session.Session")
				actMap, ok := sess.JWTClaims.Extra["act"].(map[string]any)
				require.True(t, ok, "act claim must be a map")
				assert.Equal(t, testAgentClientID, actMap["sub"])
				assert.Equal(t, map[string]any{}, actMap["act"])
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

// TestTokenExchangeHandler_DelegateClientDepthCap exercises the self-issued
// delegate-client relaxation end to end: a token originally issued to
// delegate D1 is re-exchanged by a different configured delegate D2. The
// consent check (TestCheckDelegationConsent) accepts this on its own, but
// the resulting delegation chain remains subject to buildActClaim's
// maxDelegationDepth gate exactly as it would for any other re-exchange.
func TestTokenExchangeHandler_DelegateClientDepthCap(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	const d1ClientID = "delegate-d1"

	newHandler := func(t *testing.T) *Handler {
		t.Helper()
		h := newTestHandler(t, tj, 15*time.Minute)
		h.configuredDelegateClients = []string{testAgentClientID}
		return h
	}

	// tokenFromD1 builds a self-issued subject token whose client_id is D1
	// (not the acting client, testAgentClientID/"D2"), carrying a prior act
	// chain of the given depth.
	tokenFromD1 := func(t *testing.T, depth int) string {
		t.Helper()
		extra := validExtraClaims()
		extra["client_id"] = d1ClientID
		if depth > 0 {
			extra["act"] = nestedActChain(depth)
		}
		return tj.signToken(t, validClaims(), extra)
	}

	t.Run("re-exchange under max depth is accepted and nested", func(t *testing.T) {
		t.Parallel()
		h := newHandler(t)
		form := url.Values{
			"grant_type":         {oauthproto.GrantTypeTokenExchange},
			"subject_token":      {tokenFromD1(t, maxDelegationDepth-1)},
			"subject_token_type": {oauthproto.TokenTypeAccessToken},
		}
		req := newAccessRequest(t, defaultClient(), form)

		err := h.HandleTokenEndpointRequest(context.Background(), req)
		require.NoError(t, err)

		sess, ok := req.GetSession().(*session.Session)
		require.True(t, ok, "session should be *session.Session")
		actMap, ok := sess.JWTClaims.Extra["act"].(map[string]any)
		require.True(t, ok, "act claim must be a map")
		assert.Equal(t, testAgentClientID, actMap["sub"], "the acting delegate D2, not D1, is the outermost actor")
		assert.Equal(t, nestedActChain(maxDelegationDepth-1), actMap["act"],
			"D1's prior act chain must be nested unchanged under D2's new act claim")
	})

	t.Run("re-exchange at max depth is rejected", func(t *testing.T) {
		t.Parallel()
		h := newHandler(t)
		form := url.Values{
			"grant_type":         {oauthproto.GrantTypeTokenExchange},
			"subject_token":      {tokenFromD1(t, maxDelegationDepth)},
			"subject_token_type": {oauthproto.TokenTypeAccessToken},
		}
		req := newAccessRequest(t, defaultClient(), form)

		err := h.HandleTokenEndpointRequest(context.Background(), req)
		require.Error(t, err)
		assert.True(t, errors.Is(err, fosite.ErrInvalidRequest))
		var rfcErr *fosite.RFC6749Error
		require.True(t, errors.As(err, &rfcErr), "expected fosite RFC6749Error")
		assert.Contains(t, rfcErr.Reason(), "too deep")
	})
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

func TestCheckDelegationConsent(t *testing.T) {
	t.Parallel()

	const actorID = testAgentClientID

	tests := []struct {
		name   string
		claims *ValidatedClaims
		// clientID/actorSub default to actorID when unset, reproducing the
		// pre-split behavior for every case that doesn't care about the
		// distinction. Set both explicitly, to different values, for cases
		// that test the clientID/actorSub split itself.
		clientID           string
		actorSub           string
		configuredDelegate bool // defaults to false, reproducing pre-delegate-exception behavior
		wantErr            bool
		errContains        string
	}{
		{
			name:   "may_act matching actorID accepted",
			claims: &ValidatedClaims{MayAct: &MayActClaim{Sub: actorID}},
		},
		{
			name:        "may_act not matching actorID rejected",
			claims:      &ValidatedClaims{MayAct: &MayActClaim{Sub: "different-agent"}},
			wantErr:     true,
			errContains: "does not authorize",
		},
		{
			// #5989 fix: AllowedDelegateClients must be enforced on the
			// may_act path too, not only ExternalActor's — an issuer that
			// can emit may_act but has no AllowedActors-equivalent consent
			// path (Entra, Okta) would otherwise have no per-client
			// containment at all. ExternalIssuer must be set for this check
			// to apply at all — see checkDelegationConsent's doc comment for
			// why a self-issued may_act (ExternalIssuer == "") skips it.
			name: "may_act matching actorID, actorID in AllowedDelegateClients accepted",
			claims: &ValidatedClaims{
				MayAct:                 &MayActClaim{Sub: actorID},
				ExternalIssuer:         "https://idp.example.com",
				AllowedDelegateClients: []string{"some-other-client", actorID},
			},
		},
		{
			name: "may_act matching actorID, actorID not in AllowedDelegateClients rejected",
			claims: &ValidatedClaims{
				MayAct:                 &MayActClaim{Sub: actorID},
				ExternalIssuer:         "https://idp.example.com",
				AllowedDelegateClients: []string{"some-other-client"},
			},
			wantErr:     true,
			errContains: "not authorized to exchange subject tokens",
		},
		{
			// A self-issued may_act (ExternalIssuer unset) has no
			// AllowedDelegateClients equivalent, so an empty/absent value
			// here must not reject it — unlike the external-issuer case
			// below, where the same emptiness is a hard rejection.
			name: "may_act matching actorID, ExternalIssuer unset, AllowedDelegateClients not enforced",
			claims: &ValidatedClaims{
				MayAct: &MayActClaim{Sub: actorID},
			},
		},
		{
			name: "matcher-only authorization, actorID in AllowedDelegateClients accepted",
			claims: &ValidatedClaims{
				ExternalActorAuthorized: true,
				AllowedDelegateClients:  []string{actorID},
			},
		},
		{
			name: "ExternalActor set, actorID in wildcard AllowedDelegateClients accepted",
			claims: &ValidatedClaims{
				ExternalActor:           "ext-agent",
				ExternalActorAuthorized: true,
				AllowedDelegateClients:  []string{anyDelegateClient},
			},
		},
		{
			// Guards the switch ordering in checkDelegationConsent: the
			// ExternalActorAuthorized case must be checked before the
			// client_id cases, so a populated (and mismatched) ClientID must
			// not cause rejection when the external actor is already
			// authorized.
			name: "ExternalActor set with a differing ClientID still accepted",
			claims: &ValidatedClaims{
				ExternalActor:           "ext-agent",
				ExternalActorAuthorized: true,
				ClientID:                "some-other-client",
				AllowedDelegateClients:  []string{anyDelegateClient},
			},
		},
		{
			// may_act must keep winning even when ExternalActor is also set
			// (the external validator only sets ExternalActor when MayAct is
			// nil, but checkDelegationConsent's own ordering must not depend
			// on that invariant holding).
			name: "ExternalActor set but MayAct mismatched is rejected",
			claims: &ValidatedClaims{
				ExternalActor: "ext-agent",
				MayAct:        &MayActClaim{Sub: "different-agent"},
			},
			wantErr:     true,
			errContains: "does not authorize",
		},
		{
			// #5989 hardening: AllowedDelegateClients is now required at
			// construction (validateTrustedIssuer), so nil/empty must be
			// rejected at runtime too, not treated as "any client" — this
			// pins the fail-closed default this PR introduces.
			name: "ExternalActor set, nil AllowedDelegateClients rejected",
			claims: &ValidatedClaims{
				ExternalActor:           "ext-agent",
				ExternalActorAuthorized: true,
				// AllowedDelegateClients intentionally nil.
			},
			wantErr:     true,
			errContains: "not authorized to exchange subject tokens",
		},
		{
			name: "ExternalActor set, actorID in AllowedDelegateClients accepted",
			claims: &ValidatedClaims{
				ExternalActor:           "ext-agent",
				ExternalActorAuthorized: true,
				AllowedDelegateClients:  []string{"some-other-client", actorID},
			},
		},
		{
			name: "ExternalActor set, actorID not in AllowedDelegateClients rejected",
			claims: &ValidatedClaims{
				ExternalActor:           "ext-agent",
				ExternalActorAuthorized: true,
				AllowedDelegateClients:  []string{"some-other-client"},
			},
			wantErr:     true,
			errContains: "not authorized to exchange subject tokens",
		},
		{
			name:   "client_id matching actorID accepted",
			claims: &ValidatedClaims{ClientID: actorID},
		},
		{
			name:        "client_id not matching actorID rejected",
			claims:      &ValidatedClaims{ClientID: "different-client"},
			wantErr:     true,
			errContains: "different client",
		},
		{
			name:        "no may_act, no ExternalActor, no client_id rejected",
			claims:      &ValidatedClaims{},
			wantErr:     true,
			errContains: "no verifiable client binding",
		},
		{
			// The core new behavior: a configured delegate client presenting a
			// self-issued token issued to a different client_id is accepted,
			// where a non-delegate client (the case above) would be rejected.
			name:               "configured delegate, self-issued token issued to a different client accepted",
			claims:             &ValidatedClaims{ClientID: "different-client"},
			configuredDelegate: true,
		},
		{
			// Regression guard, not an e2e-reachable scenario: in production no
			// non-delegate client can actually reach this code path at all,
			// since DCR/CIMD-registered clients can never hold the
			// token-exchange grant (only statically-configured delegate
			// clients do). This pins that a hand-built client without
			// configuredDelegate set still gets the pre-existing rejection.
			name:        "non-delegate client, self-issued token issued to a different client still rejected",
			claims:      &ValidatedClaims{ClientID: "different-client"},
			wantErr:     true,
			errContains: "different client",
		},
		{
			// may_act is checked first and is unaffected by the delegate
			// exception: a self-issued may_act naming a different client than
			// the configured delegate is still rejected for that delegate.
			name:               "configured delegate, self-issued may_act naming a different client still rejected",
			claims:             &ValidatedClaims{MayAct: &MayActClaim{Sub: "different-agent"}},
			configuredDelegate: true,
			wantErr:            true,
			errContains:        "does not authorize",
		},
		{
			// The "no verifiable client binding" case is untouched by the
			// delegate exception: a configured delegate still cannot exchange
			// a self-issued token that carries no client_id at all.
			name:               "configured delegate, self-issued token with no client_id still rejected",
			claims:             &ValidatedClaims{},
			configuredDelegate: true,
			wantErr:            true,
			errContains:        "no verifiable client binding",
		},
		{
			// Proves the ExternalIssuer=="" guard actually gates the
			// relaxation, not just that it's logically supposed to: a
			// configured delegate presenting an EXTERNALLY-issued token with a
			// mismatched client_id is still rejected by case 3 as before.
			name: "configured delegate, externally-issued token with mismatched client_id still rejected",
			claims: &ValidatedClaims{
				ClientID:       "different-client",
				ExternalIssuer: "https://idp.example.com",
			},
			configuredDelegate: true,
			wantErr:            true,
			errContains:        "different client",
		},
		{
			// The clientID/actorSub split: may_act.sub binds to the asserted
			// actor (actorSub), not the authenticated client (clientID) —
			// this is may_act's whole purpose, letting a client authenticate
			// as itself while acting as a distinct actor.
			name:     "may_act matching actorSub, differing from clientID, accepted",
			claims:   &ValidatedClaims{MayAct: &MayActClaim{Sub: "distinct-actor"}},
			clientID: "the-authenticated-client",
			actorSub: "distinct-actor",
		},
		{
			// The clientID/actorSub split, external-issuer path:
			// AllowedDelegateClients binds to the authenticated client
			// (clientID), not the asserted actor — an actor identity that
			// happens to collide with an allowlisted client must not grant
			// access on its own.
			name: "ExternalActor set, actorSub in AllowedDelegateClients but clientID is not rejected",
			claims: &ValidatedClaims{
				ExternalActor:           "ext-agent",
				ExternalActorAuthorized: true,
				AllowedDelegateClients:  []string{"distinct-actor"},
			},
			clientID:    "the-authenticated-client",
			actorSub:    "distinct-actor",
			wantErr:     true,
			errContains: "not authorized to exchange subject tokens",
		},
		{
			// The clientID/actorSub split, client_id-binding fallback: the
			// subject token's client_id must match the authenticated client
			// (clientID), not the asserted actor (actorSub) — this is the
			// exact bypass TestTokenExchangeHandler_HandleTokenEndpointRequest's
			// "actor_token cannot bypass subject-token client_id binding"
			// covers end-to-end; this pins the same invariant at the
			// checkDelegationConsent unit level.
			name:        "client_id matching actorSub but not clientID rejected",
			claims:      &ValidatedClaims{ClientID: "distinct-actor"},
			clientID:    "the-authenticated-client",
			actorSub:    "distinct-actor",
			wantErr:     true,
			errContains: "different client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clientID, actorSub := tt.clientID, tt.actorSub
			if clientID == "" {
				clientID = actorID
			}
			if actorSub == "" {
				actorSub = actorID
			}
			err := checkDelegationConsent(tt.claims, clientID, actorSub, tt.configuredDelegate)
			if tt.wantErr {
				require.Error(t, err)
				var rfcErr *fosite.RFC6749Error
				require.True(t, errors.As(err, &rfcErr), "expected fosite RFC6749Error")
				assert.Contains(t, rfcErr.Reason(), tt.errContains)
				return
			}
			require.NoError(t, err)
		})
	}
}

// newTestHandlerWithValidator creates a Handler wired to a caller-provided
// validator, for tests that need external-issuer delegation coverage that
// newTestHandler's self-issued-only validator cannot produce.
func newTestHandlerWithValidator(validator SubjectTokenValidator) *Handler {
	return &Handler{
		validator:          validator,
		issuer:             testIssuer,
		delegationLifespan: 15 * time.Minute,
		allowedAudiences:   []string{testIssuer},
		config: &mockConfig{
			scopeStrategy:    fosite.ExactScopeStrategy,
			audienceStrategy: fosite.DefaultAudienceMatchingStrategy,
		},
	}
}

func TestTokenExchangeHandler_ActorMatcherDelegationConsent(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)
	validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		ActorMatcher:           `claims.azp == "trusted-app"`,
		AllowedDelegateClients: []string{testAgentClientID},
	}})
	handler := newTestHandlerWithValidator(validator)

	claims := externalClaims()
	claims.Audience = jwt.Audience{testExternalAudience, testIssuer}
	subjectToken := externalJWKS.signToken(t, claims, map[string]any{"azp": "trusted-app"})

	tests := []struct {
		name    string
		client  fosite.Client
		wantErr bool
	}{
		{
			name:   "matching client is authorized",
			client: defaultClient(),
		},
		{
			name: "client absent from AllowedDelegateClients is rejected",
			client: func() fosite.Client {
				client := defaultClient()
				client.ID = "different-toolhive-client"
				return client
			}(),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := newAccessRequest(t, tt.client, url.Values{
				"grant_type":         {oauthproto.GrantTypeTokenExchange},
				"subject_token":      {subjectToken},
				"subject_token_type": {oauthproto.TokenTypeAccessToken},
			})

			err := handler.HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantErr {
				require.Error(t, err)
				var rfcErr *fosite.RFC6749Error
				require.True(t, errors.As(err, &rfcErr), "expected fosite RFC6749Error")
				assert.Contains(t, rfcErr.Reason(), "not authorized to exchange subject tokens")
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestTokenExchangeHandler_ActChainProvenance covers the act-chain shape that
// HandleTokenEndpointRequest builds, for the case a token exchange terminates
// on (ValidatedClaims.ExternalActor set) that TestTokenExchangeHandler_
// HandleTokenEndpointRequest's self-issued-only table cannot exercise.
func TestTokenExchangeHandler_ActChainProvenance(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)

	trustedIssuers := []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		AllowedActors:          []string{"ext-agent"},
		AllowedDelegateClients: []string{testAgentClientID},
		AllowMayAct:            true,
	}}
	multiValidator := newMultiValidator(t, tj, trustedIssuers)

	// externalToken signs a subject token from the trusted external issuer
	// with an allowlisted actor (so ExternalActor is set) and an optional
	// prior act chain. The audience includes testIssuer alongside the
	// issuer's required ExpectedAudience so the delegated token's
	// default-audience grant (testIssuer) stays covered by the subject
	// token's own audience (ensureAudienceSubsetOfSubject).
	externalToken := func(t *testing.T, priorAct any) string {
		t.Helper()
		claims := externalClaims()
		claims.Audience = jwt.Audience{testExternalAudience, testIssuer}
		extra := map[string]any{"azp": "ext-agent"}
		if priorAct != nil {
			extra["act"] = priorAct
		}
		return externalJWKS.signToken(t, claims, extra)
	}

	requestWith := func(t *testing.T, h *Handler, token string) (*fosite.AccessRequest, error) {
		t.Helper()
		form := url.Values{
			"grant_type":         {oauthproto.GrantTypeTokenExchange},
			"subject_token":      {token},
			"subject_token_type": {oauthproto.TokenTypeAccessToken},
		}
		req := newAccessRequest(t, defaultClient(), form)
		return req, h.HandleTokenEndpointRequest(context.Background(), req)
	}

	t.Run("external delegation nests actor and issuer under the client's act entry", func(t *testing.T) {
		t.Parallel()
		h := newTestHandlerWithValidator(multiValidator)

		req, err := requestWith(t, h, externalToken(t, nil))
		require.NoError(t, err)

		sess, ok := req.GetSession().(*session.Session)
		require.True(t, ok, "session should be *session.Session")
		assert.Equal(t, testExternalIssuer+"#"+"ext-user-456", sess.JWTClaims.Subject,
			"external subject must be qualified with the issuer URL, not copied verbatim")
		act, ok := sess.JWTClaims.Extra["act"].(map[string]any)
		require.True(t, ok, "act claim must be a map")
		assert.Equal(t, testAgentClientID, act["sub"], "outermost act.sub is always the ToolHive client")
		assert.Equal(t, testIssuer, act["iss"], "outermost act.iss is always this server's own issuer")

		nested, ok := act["act"].(map[string]any)
		require.True(t, ok, "external actor/issuer must be nested under act.act")
		assert.Equal(t, "ext-agent", nested["sub"])
		assert.Equal(t, testExternalIssuer, nested["iss"])
		assert.Nil(t, nested["act"], "no prior chain to nest further")
		assert.Equal(t, testExternalIssuer, sess.JWTClaims.Extra["external_issuer"],
			"external_issuer is always recorded, even when a genuine actor was also nested under act")
	})

	t.Run("self-issued delegation stays a single level with no external nesting", func(t *testing.T) {
		t.Parallel()
		h := newTestHandler(t, tj, 15*time.Minute)

		req, err := requestWith(t, h, tj.signToken(t, validClaims(), validExtraClaims()))
		require.NoError(t, err)

		sess, ok := req.GetSession().(*session.Session)
		require.True(t, ok, "session should be *session.Session")
		assert.Equal(t, "user-123", sess.JWTClaims.Subject,
			"self-issued subject must pass through unchanged, never qualified")
		act, ok := sess.JWTClaims.Extra["act"].(map[string]any)
		require.True(t, ok, "act claim must be a map")
		assert.Equal(t, testAgentClientID, act["sub"])
		assert.Equal(t, testIssuer, act["iss"], "outermost act.iss is always this server's own issuer")
		assert.Nil(t, act["act"], "self-issued delegation must not nest an external actor")
		assert.NotContains(t, sess.JWTClaims.Extra, "external_issuer",
			"self-issued delegation involves no external issuer to record")
	})

	// #5989 fix: an external issuer choosing a "sub" equal to a native
	// ToolHive user's UUID must never produce a delegated token whose "sub"
	// collides with that UUID — Cedar's extractClientIDFromClaims
	// (pkg/authz/authorizers/cedar/core.go) keys on "sub" alone, and native
	// ToolHive subjects are UUIDs from UserResolver.ResolveUser, never an
	// upstream provider's raw subject value.
	t.Run("external subject equal to a plausible native UUID is qualified, not collided", func(t *testing.T) {
		t.Parallel()
		h := newTestHandlerWithValidator(multiValidator)

		const nativeLikeUUID = "8a34c9e0-6b1a-4e5f-9c2d-1f0a7b3d5e6c"
		claims := externalClaims()
		claims.Subject = nativeLikeUUID
		claims.Audience = jwt.Audience{testExternalAudience, testIssuer}
		token := externalJWKS.signToken(t, claims, map[string]any{"azp": "ext-agent"})

		req, err := requestWith(t, h, token)
		require.NoError(t, err)

		sess, ok := req.GetSession().(*session.Session)
		require.True(t, ok, "session should be *session.Session")
		assert.NotEqual(t, nativeLikeUUID, sess.JWTClaims.Subject,
			"the qualified subject must never equal the bare external sub, "+
				"or it could collide with a native user sharing that UUID")
		assert.Equal(t, testExternalIssuer+"#"+nativeLikeUUID, sess.JWTClaims.Subject)
	})

	// The documented precedence (see checkDelegationConsent and
	// resolveActorAuthorization's doc comments): a trusted external issuer
	// emitting a well-formed may_act naming this client bypasses external
	// actor authorization, but AllowedDelegateClients still applies. Only
	// covered at the checkDelegationConsent unit level until now — this
	// exercises it through the full handler.
	t.Run("external token carrying may_act records the issuer without nesting a phantom actor", func(t *testing.T) {
		t.Parallel()
		h := newTestHandlerWithValidator(multiValidator)

		claims := externalClaims()
		claims.Audience = jwt.Audience{testExternalAudience, testIssuer}
		token := externalJWKS.signToken(t, claims, map[string]any{
			"may_act": map[string]any{"sub": testAgentClientID, "iss": testIssuer},
		})

		req, err := requestWith(t, h, token)
		require.NoError(t, err)

		sess, ok := req.GetSession().(*session.Session)
		require.True(t, ok, "session should be *session.Session")
		act, ok := sess.JWTClaims.Extra["act"].(map[string]any)
		require.True(t, ok, "act claim must be a map")
		assert.Equal(t, testAgentClientID, act["sub"])

		// External actor authorization is bypassed, and may_act.sub already
		// named the delegate directly via the outermost act.sub above, so
		// there is no client-namespace actor to nest. The external issuer is
		// still recorded (ValidatedClaims.ExternalIssuer is set unconditionally
		// by validateExternalToken) -- but as its own top-level claim, not as
		// a phantom, issuer-only hop under act.act: an act entry identifies a
		// party that acted, and an issuer alone identifies no party.
		assert.Nil(t, act["act"], "no actor was resolved, so act must not nest an issuer-only phantom hop")
		assert.Equal(t, testExternalIssuer, sess.JWTClaims.Extra["external_issuer"],
			"the external issuer must still be recorded, as its own top-level claim")
	})

	// maxDelegationDepth (10) bounds the prior chain depth + newLevels. The
	// external-actor wrapper adds a level of its own (newLevels=2) versus the
	// self-issued path (newLevels=1), so the external path's prior chain must
	// be one level shallower to fit:
	//   self-issued: depth 9 accepted (9+1=10); depth 10 rejected (10+1=11>10)
	//     — see "delegation chain under max depth is nested" /
	//     "delegation chain at max depth rejected" in
	//     TestTokenExchangeHandler_HandleTokenEndpointRequest.
	//   external:    depth 8 accepted (8+2=10); depth 9 rejected (9+2=11>10)
	// Split into two subtests (rather than one asserting both sides) so a
	// failure on the accept half can't hide a regression on the reject half.
	t.Run("external delegation at depth 8 fits exactly and preserves the full nested chain", func(t *testing.T) {
		t.Parallel()
		h := newTestHandlerWithValidator(multiValidator)

		req, err := requestWith(t, h, externalToken(t, nestedActChain(8)))
		require.NoError(t, err, "depth 8 plus the external wrapper must fit exactly at maxDelegationDepth")

		sess, ok := req.GetSession().(*session.Session)
		require.True(t, ok, "session should be *session.Session")
		act, ok := sess.JWTClaims.Extra["act"].(map[string]any)
		require.True(t, ok, "act claim must be a map")
		external, ok := act["act"].(map[string]any)
		require.True(t, ok, "external actor/issuer must be nested under act.act")
		assert.Equal(t, "ext-agent", external["sub"])
		assert.Equal(t, testExternalIssuer, external["iss"])
		// The prior chain must be preserved under the external wrapper, not
		// dropped or overwritten by it — this is the provenance record
		// checkDelegationConsent's doc comment relies on being auditable.
		assert.Equal(t, nestedActChain(8), external["act"],
			"the prior act chain must be nested under the external wrapper, unchanged")
	})

	t.Run("external delegation at depth 9 is rejected as too deep", func(t *testing.T) {
		t.Parallel()
		h := newTestHandlerWithValidator(multiValidator)

		_, err := requestWith(t, h, externalToken(t, nestedActChain(9)))
		require.Error(t, err)
		assert.True(t, errors.Is(err, fosite.ErrInvalidRequest))
		var rfcErr *fosite.RFC6749Error
		require.True(t, errors.As(err, &rfcErr), "expected fosite RFC6749Error")
		assert.Contains(t, rfcErr.Reason(), "too deep")
	})

	// reExchange re-submits a first exchange's resulting session as a new
	// subject_token: the delegated token's own "iss" is always this server's
	// own issuer (see delegatedSubject's doc comment), so a second exchange
	// validates it via the self-issued path, not the external-issuer path.
	// It carries forward exactly the claims a real re-exchange would see on
	// the wire — client_id, the nested act chain, and external_issuer.
	reExchange := func(t *testing.T, h *Handler, sess *session.Session) (*fosite.AccessRequest, error) {
		t.Helper()
		extra := map[string]any{"client_id": testAgentClientID}
		if act, ok := sess.JWTClaims.Extra["act"]; ok {
			extra["act"] = act
		}
		if externalIssuer, ok := sess.JWTClaims.Extra["external_issuer"]; ok {
			extra["external_issuer"] = externalIssuer
		}
		claims := validClaims()
		claims.Subject = sess.JWTClaims.Subject
		return requestWith(t, h, tj.signToken(t, claims, extra))
	}

	// #6473 regression: re-exchanging a delegated token that itself recorded
	// an external_issuer must not silently drop that provenance just because
	// the second exchange validates via the self-issued path.
	t.Run("external_issuer survives a self-issued re-exchange (may_act path)", func(t *testing.T) {
		t.Parallel()
		h := newTestHandlerWithValidator(multiValidator)

		claims := externalClaims()
		claims.Audience = jwt.Audience{testExternalAudience, testIssuer}
		token := externalJWKS.signToken(t, claims, map[string]any{
			"may_act": map[string]any{"sub": testAgentClientID, "iss": testIssuer},
		})

		firstReq, err := requestWith(t, h, token)
		require.NoError(t, err)
		firstSess, ok := firstReq.GetSession().(*session.Session)
		require.True(t, ok, "session should be *session.Session")
		require.Equal(t, testExternalIssuer, firstSess.JWTClaims.Extra["external_issuer"],
			"first exchange must record the external issuer")

		secondReq, err := reExchange(t, h, firstSess)
		require.NoError(t, err)
		secondSess, ok := secondReq.GetSession().(*session.Session)
		require.True(t, ok, "session should be *session.Session")
		assert.Equal(t, testExternalIssuer, secondSess.JWTClaims.Extra["external_issuer"],
			"external_issuer must survive a self-issued re-exchange, not just the first hop")
	})

	t.Run("external_issuer survives a self-issued re-exchange (ActorMatcher-only path)", func(t *testing.T) {
		t.Parallel()
		actorMatcherValidator := newMultiValidator(t, tj, []TrustedIssuer{{
			IssuerURL:              testExternalIssuer,
			ExpectedAudience:       testExternalAudience,
			JWKSURL:                jwksServer.URL + "/jwks",
			ActorMatcher:           `claims.azp == "trusted-app"`,
			AllowedDelegateClients: []string{testAgentClientID},
		}})
		h := newTestHandlerWithValidator(actorMatcherValidator)

		claims := externalClaims()
		claims.Audience = jwt.Audience{testExternalAudience, testIssuer}
		token := externalJWKS.signToken(t, claims, map[string]any{"azp": "trusted-app"})

		firstReq, err := requestWith(t, h, token)
		require.NoError(t, err)
		firstSess, ok := firstReq.GetSession().(*session.Session)
		require.True(t, ok, "session should be *session.Session")
		require.Equal(t, testExternalIssuer, firstSess.JWTClaims.Extra["external_issuer"],
			"first exchange must record the external issuer even with no actor resolved")
		firstAct, ok := firstSess.JWTClaims.Extra["act"].(map[string]any)
		require.True(t, ok, "act claim must be a map")
		assert.Nil(t, firstAct["act"], "ActorMatcher alone resolves no actor to nest")

		secondReq, err := reExchange(t, h, firstSess)
		require.NoError(t, err)
		secondSess, ok := secondReq.GetSession().(*session.Session)
		require.True(t, ok, "session should be *session.Session")
		assert.Equal(t, testExternalIssuer, secondSess.JWTClaims.Extra["external_issuer"],
			"external_issuer must survive a self-issued re-exchange even when no client-namespace actor was ever nested")
	})
}

// TestBuildActClaim_ExternalIssuerIsNeverNestedAsAPhantomActor locks in the
// design fix for a category error: an act object identifies a party that
// acted (RFC 8693 §4.1), so a bare {"iss": externalIssuer} entry -- naming
// no party -- must never be nested under act.act. When an actual actor was
// resolved (the allowlist path's ExternalActor), it nests correctly with
// both iss and sub. When no actor was resolved (the may_act-bearing or
// ActorMatcher-only path), act stays a single hop and the external issuer
// is reported only through buildActClaim's separate return value, which the
// caller records as its own external_issuer claim -- never inside act.
func TestBuildActClaim_ExternalIssuerIsNeverNestedAsAPhantomActor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		claims          *ValidatedClaims
		wantNestedActor bool
	}{
		{
			name: "allowlist path nests the allowlisted external actor",
			claims: &ValidatedClaims{
				Subject:        "ext-user-456",
				ExternalIssuer: testExternalIssuer,
				ExternalActor:  "ext-agent",
			},
			wantNestedActor: true,
		},
		{
			name: "may_act path with no ExternalActor nests nothing under act",
			claims: &ValidatedClaims{
				Subject:        "ext-user-456",
				ExternalIssuer: testExternalIssuer,
				ExternalActor:  "",
			},
			wantNestedActor: false,
		},
		{
			// newLevels stays 1 (not 2) on the no-actor path, so a prior
			// chain of depth maxDelegationDepth-1 must fit exactly here,
			// unlike the external-actor case in
			// "external delegation at depth 8 fits exactly...", which only
			// admits depth-8 because its newLevels is 2. Pins the depth
			// arithmetic for the no-actor-plus-prior-chain combination,
			// which no other test exercises.
			name: "may_act path with a prior chain: no actor wrapper means one more level of prior chain fits",
			claims: &ValidatedClaims{
				Subject:        "ext-user-456",
				ExternalIssuer: testExternalIssuer,
				ExternalActor:  "",
				Extra:          map[string]any{"act": nestedActChain(maxDelegationDepth - 1)},
			},
			wantNestedActor: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			act, externalIssuer, err := buildActClaim(tt.claims, testIssuer, testAgentClientID)
			require.NoError(t, err)

			// The external issuer is always reported via the return value,
			// regardless of whether an actor was also resolved.
			assert.Equal(t, testExternalIssuer, externalIssuer)

			if !tt.wantNestedActor {
				if prior, ok := tt.claims.Extra["act"]; ok {
					assert.Equal(t, prior, act["act"],
						"with no actor to nest, the prior chain must hang directly under the outer hop, unchanged")
				} else {
					assert.Nil(t, act["act"], "no actor was resolved, so act must not nest a phantom, issuer-only hop")
				}
				return
			}
			nested, ok := act["act"].(map[string]any)
			require.True(t, ok, "a genuine actor must nest under act.act")
			assert.Equal(t, testExternalIssuer, nested["iss"])
			assert.Equal(t, "ext-agent", nested["sub"])
		})
	}
}

// TestBuildActClaim_ExternalIssuerCarriesForwardOnReExchange covers the fix
// for the provenance-loss bug: a self-issued subject token being re-exchanged
// validates via SelfIssuedTokenValidator, which never sets
// ValidatedClaims.ExternalIssuer, so without reading back the subject
// token's own "external_issuer" claim, a second exchange would silently drop
// the fact that an earlier hop involved an external issuer.
func TestBuildActClaim_ExternalIssuerCarriesForwardOnReExchange(t *testing.T) {
	t.Parallel()

	const freshIssuer = "https://fresh-external-issuer.example.com"
	const priorIssuer = "https://prior-external-issuer.example.com"

	tests := []struct {
		name   string
		claims *ValidatedClaims
		want   string
	}{
		{
			name: "a fresh external issuer this hop wins over a stale prior one",
			claims: &ValidatedClaims{
				ExternalIssuer: freshIssuer,
				Extra:          map[string]any{"external_issuer": priorIssuer},
			},
			want: freshIssuer,
		},
		{
			name: "no fresh external issuer this hop carries the prior one forward",
			claims: &ValidatedClaims{
				Extra: map[string]any{"external_issuer": priorIssuer},
			},
			want: priorIssuer,
		},
		{
			name:   "neither this hop nor the subject token has an external issuer",
			claims: &ValidatedClaims{},
			want:   "",
		},
		{
			name: "a non-string prior external_issuer is ignored, not carried forward",
			claims: &ValidatedClaims{
				Extra: map[string]any{"external_issuer": 12345},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, externalIssuer, err := buildActClaim(tt.claims, testIssuer, testAgentClientID)
			require.NoError(t, err)
			assert.Equal(t, tt.want, externalIssuer)
		})
	}
}

func TestChainToAct_EmptyIdentityHop(t *testing.T) {
	t.Parallel()

	act, err := chainToAct([]coreaudit.DelegatedActor{{}})

	require.Error(t, err)
	assert.Nil(t, act)
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

	validator, err := NewSelfIssuedTokenValidator(tj.publicJWKS(), testIssuer, []string{testIssuer})
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
		issuer:             testIssuer,
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

// TestGrantResourceAudience_SPIFFEClientEnforcesResourcesIndependentlyOfAudiences
// is a regression test for a bug where the RFC 8707 "resource" parameter was
// checked against a SPIFFE client's audience allowlist instead of its
// independently configured resource allowlist: a resource the client was
// registered for was rejected, and a resource the client was NOT registered
// for -- but which happened to be one of its audiences -- was granted.
func TestGrantResourceAudience_SPIFFEClientEnforcesResourcesIndependentlyOfAudiences(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	h := newTestHandler(t, tj, time.Hour)
	// Both values must be server-wide allowed so the test isolates the
	// per-client check (grantResourceAudience) from the server-wide one.
	h.allowedAudiences = []string{testIssuer, "https://other.example.com"}

	const registeredResource = testIssuer
	const registeredAudience = "https://other.example.com"
	client, err := registration.NewSPIFFEClient(
		"spiffe-client",
		[]string{"openid"},
		[]string{registeredAudience},
		[]string{registeredResource},
	)
	require.NoError(t, err)

	t.Run("resource in the client's resources is granted", func(t *testing.T) {
		t.Parallel()

		req := newAccessRequest(t, client, url.Values{"resource": {registeredResource}})
		require.NoError(t, h.grantResourceAudience(context.Background(), req, client))
		assert.Contains(t, req.GetGrantedAudience(), registeredResource)
	})

	t.Run("resource that is only an audience, not a resource, is rejected", func(t *testing.T) {
		t.Parallel()

		req := newAccessRequest(t, client, url.Values{"resource": {registeredAudience}})
		err := h.grantResourceAudience(context.Background(), req, client)
		require.Error(t, err)
		assert.True(t, errors.Is(err, server.ErrInvalidTarget))
		assert.Empty(t, req.GetGrantedAudience())
	})
}

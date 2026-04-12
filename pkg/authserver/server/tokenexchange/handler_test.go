// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
)

const testAgentClientID = "devops-agent"

// newTestHandler creates a TokenExchangeHandler wired to the given test JWKS and
// delegation lifespan. It does not need a full fosite strategy stack because
// HandleTokenEndpointRequest (which is the focus of these tests) does not issue tokens.
func newTestHandler(t *testing.T, tj *testJWKS, delegationLifespan time.Duration) *Handler {
	t.Helper()

	validator, err := NewSelfIssuedTokenValidator(tj.jwks, testIssuer)
	require.NoError(t, err)

	return &Handler{
		// HandleHelper is nil — we only test HandleTokenEndpointRequest, not
		// PopulateTokenEndpointResponse, so IssueAccessToken is never called.
		HandleHelper:       nil,
		validator:          validator,
		selfValidator:      validator,
		delegationLifespan: delegationLifespan,
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
	req.GrantTypes = fosite.Arguments{GrantTypeTokenExchange}
	req.Client = client
	req.Form = formValues
	return req
}

// defaultClient returns a confidential client configured for token exchange.
func defaultClient() *fosite.DefaultClient {
	return &fosite.DefaultClient{
		ID:         testAgentClientID,
		GrantTypes: fosite.Arguments{GrantTypeTokenExchange},
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
		"grant_type":         {GrantTypeTokenExchange},
		"subject_token":      {token},
		"subject_token_type": {TokenTypeAccessToken},
	}
}

// signActorToken creates a self-issued JWT suitable for use as an actor_token.
// The sub claim is set to the given subject (typically the client_id).
func signActorToken(t *testing.T, tj *testJWKS, subject string) string {
	t.Helper()

	now := time.Now()
	claims := jwt.Claims{
		Subject:  subject,
		Issuer:   testIssuer,
		Audience: jwt.Audience{testIssuer},
		Expiry:   jwt.NewNumericDate(now.Add(time.Hour)),
		IssuedAt: jwt.NewNumericDate(now),
	}
	return tj.signToken(t, claims, map[string]interface{}{
		"client_id": testAgentClientID,
	})
}

func TestTokenExchangeHandler_CanHandleTokenEndpointRequest(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	h := newTestHandler(t, tj, 15*time.Minute)

	t.Run("matches token exchange grant type", func(t *testing.T) {
		t.Parallel()
		req := fosite.NewAccessRequest(&session.Session{})
		req.GrantTypes = fosite.Arguments{GrantTypeTokenExchange}
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
		grantTypes   fosite.Arguments // Override grant type; nil means GrantTypeTokenExchange
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
				actMap, ok := actClaim.(map[string]interface{})
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
					"grant_type":         {GrantTypeTokenExchange},
					"subject_token_type": {TokenTypeAccessToken},
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
					"grant_type":    {GrantTypeTokenExchange},
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
					"grant_type":         {GrantTypeTokenExchange},
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
			name:     "valid exchange with actor_token",
			ctx:      func(_ *testing.T) context.Context { return context.Background() },
			client:   defaultClient,
			lifespan: 15 * time.Minute,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("actor_token", signActorToken(t, tj, testAgentClientID))
				f.Set("actor_token_type", TokenTypeJWT)
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
			name:     "actor_token sub mismatch with client ID",
			ctx:      func(_ *testing.T) context.Context { return context.Background() },
			client:   defaultClient,
			lifespan: 15 * time.Minute,
			form: func(t *testing.T) url.Values {
				t.Helper()
				f := defaultFormValues(t, tj)
				f.Set("actor_token", signActorToken(t, tj, "other-agent"))
				f.Set("actor_token_type", TokenTypeJWT)
				return f
			},
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidGrant,
			hintContains: "does not match the authenticated client identity",
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
				f.Set("actor_token_type", TokenTypeJWT)
				return f
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidRequest,
			hintContains: "actor_token_type",
		},
		{
			name:   "id_token subject_token_type accepted",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(t *testing.T) url.Values {
				t.Helper()
				token := tj.signToken(t, validClaims(), validExtraClaims())
				return url.Values{
					"grant_type":         {GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {TokenTypeIDToken},
				}
			},
			lifespan: 15 * time.Minute,
			check: func(t *testing.T, req *fosite.AccessRequest) {
				t.Helper()

				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok, "session should be *session.Session")
				assert.Equal(t, "user-123", sess.JWTClaims.Subject)
			},
		},
		{
			name:   "invalid subject_token — bad JWT",
			ctx:    func(_ *testing.T) context.Context { return context.Background() },
			client: defaultClient,
			form: func(_ *testing.T) url.Values {
				return url.Values{
					"grant_type":         {GrantTypeTokenExchange},
					"subject_token":      {"not-a-valid-jwt"},
					"subject_token_type": {TokenTypeAccessToken},
				}
			},
			lifespan:     15 * time.Minute,
			wantErr:      true,
			wantFositeIs: fosite.ErrInvalidGrant,
			hintContains: "subject token is invalid",
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
					"grant_type":         {GrantTypeTokenExchange},
					"subject_token":      {token},
					"subject_token_type": {TokenTypeAccessToken},
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

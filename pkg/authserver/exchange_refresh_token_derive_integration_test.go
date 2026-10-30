// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// grantToken exchanges a fresh login-shaped access token, issued to the allowed
// source client, for a grant of tokenType owned by auth and returns the response.
func (e *xrtEnv) grantToken(t *testing.T, auth clientAuth, tokenType string, expiresAt *int64) tokenResult {
	t.Helper()
	subject := e.mint(t, nil)
	res := e.requestRefreshToken(t, auth, subject, func(f url.Values) {
		f.Set("authorization_details", "["+xrtEntry(tokenType, expiresAt)+"]")
	})
	require.Equal(t, http.StatusOK, res.status, res.body)
	return res
}

// derive presents a grant token and asks for a child that allows childType.
func (e *xrtEnv) derive(
	t *testing.T, auth clientAuth, parent, childType string, mutate func(url.Values),
) tokenResult {
	t.Helper()
	form := url.Values{
		"grant_type":            {oauthproto.GrantTypeTokenExchange},
		"subject_token":         {parent},
		"subject_token_type":    {oauthproto.TokenTypeRefreshToken},
		"requested_token_type":  {oauthproto.TokenTypeRefreshToken},
		"authorization_details": {"[" + xrtEntry(childType, nil) + "]"},
	}
	if mutate != nil {
		mutate(form)
	}
	return postToken(t, e.srv.URL, auth, form)
}

// revoke revokes a grant through the store; the revocation endpoint does not
// exist yet.
func (e *xrtEnv) revoke(t *testing.T, token string) {
	t.Helper()
	grants, ok := storage.Unwrap(e.store).(storage.ExchangeGrantStorage)
	require.True(t, ok)
	require.NoError(t, grants.RevokeExchangeGrant(t.Context(), tokenSignature(token), time.Now()))
}

// loginRefreshToken returns a refresh token from the authorization-code flow.
func (e *xrtEnv) loginRefreshToken(t *testing.T) string {
	t.Helper()
	code, verifier := login(t, e.srv.URL, cfgBrokerID)
	tokens := postToken(t, e.srv.URL, e.broker, url.Values{
		"grant_type": {oauthproto.GrantTypeAuthorizationCode}, "code": {code},
		"redirect_uri": {cfgRedirectURI}, "code_verifier": {verifier},
	})
	require.Equal(t, http.StatusOK, tokens.status, tokens.body)
	return tokens.str(t, "refresh_token")
}

func runDeriveFlows(t *testing.T, env *xrtEnv) {
	t.Helper()
	t.Run("derive", func(t *testing.T) {
		t.Run("a refresh_token grant yields an id-jag child", func(t *testing.T) {
			parent := env.grantToken(t, env.broker, oauthproto.TokenTypeRefreshToken, nil)
			before := time.Now()
			res := env.derive(t, env.broker, parent.str(t, "access_token"), oauthproto.TokenTypeIDJAG, nil)
			after := time.Now()
			require.Equal(t, http.StatusOK, res.status, res.body)
			assertRefreshTokenResponse(t, res, oauthproto.TokenTypeIDJAG, before, after, xrtIDJAGCap)
			assert.NotEqual(t, parent.str(t, "access_token"), res.str(t, "access_token"))
		})

		t.Run("a child never outlives its parent", func(t *testing.T) {
			soon := time.Now().Add(time.Hour).Unix()
			parent := env.grantToken(t, env.broker, oauthproto.TokenTypeRefreshToken, &soon)
			res := env.derive(t, env.broker, parent.str(t, "access_token"), oauthproto.TokenTypeIDJAG, nil)
			require.Equal(t, http.StatusOK, res.status, res.body)
			entry := res.body["authorization_details"].([]any)[0].(map[string]any)
			assert.EqualValues(t, soon, entry["expires_at"], "capped to the parent, which is below the 12h id-jag cap")
		})

		t.Run("the unusable-token cases are one identical error", func(t *testing.T) {
			parent := env.grantToken(t, env.broker, oauthproto.TokenTypeRefreshToken, nil).str(t, "access_token")
			cut := strings.LastIndex(parent, ".")
			forged := parent[:strings.Index(parent, "rt_")+3] + strings.Repeat("A", cut-strings.Index(parent, "rt_")-3) + parent[cut:]
			require.NotEqual(t, parent, forged)
			for name, res := range map[string]tokenResult{
				"forged key part":      env.derive(t, env.broker, forged, oauthproto.TokenTypeIDJAG, nil),
				"fails the HMAC check": env.derive(t, env.broker, "garbage", oauthproto.TokenTypeIDJAG, nil),
				"another client":       env.derive(t, env.secretIn, parent, oauthproto.TokenTypeIDJAG, nil),
				"login refresh token":  env.derive(t, env.broker, env.loginRefreshToken(t), oauthproto.TokenTypeIDJAG, nil),
			} {
				assert.Equal(t, http.StatusBadRequest, res.status, name)
				assert.Equal(t, map[string]any{"error": "invalid_request", "error_description": "grant_invalid"}, res.body, name)
				assert.Equal(t, "no-store", res.header.Get("Cache-Control"), name)
				assert.Equal(t, "no-cache", res.header.Get("Pragma"), name)
			}
		})

		t.Run("grant that does not allow refresh_token is not_issuable", func(t *testing.T) {
			parent := env.grantToken(t, env.broker, oauthproto.TokenTypeIDJAG, nil)
			requireError(t, env.derive(t, env.broker, parent.str(t, "access_token"), oauthproto.TokenTypeIDJAG, nil),
				"invalid_request", "not_issuable")
		})

		t.Run("child cannot list refresh_token", func(t *testing.T) {
			parent := env.grantToken(t, env.broker, oauthproto.TokenTypeRefreshToken, nil)
			requireError(t, env.derive(t, env.broker, parent.str(t, "access_token"), oauthproto.TokenTypeRefreshToken, nil),
				"invalid_authorization_details", "token_type_not_permitted")
		})

		t.Run("depth is 1", func(t *testing.T) {
			parent := env.grantToken(t, env.broker, oauthproto.TokenTypeRefreshToken, nil)
			child := env.derive(t, env.broker, parent.str(t, "access_token"), oauthproto.TokenTypeIDJAG, nil)
			require.Equal(t, http.StatusOK, child.status, child.body)
			requireError(t, env.derive(t, env.broker, child.str(t, "access_token"), oauthproto.TokenTypeIDJAG, nil),
				"invalid_request", "not_issuable")
		})

		t.Run("expired grants are the grant_invalid error", func(t *testing.T) {
			// Both grants expire three seconds from now. One sleep covers both.
			// Both stores report an expired record as not found, revoked or not.
			soon := time.Now().Add(3 * time.Second).Unix()
			expired := env.grantToken(t, env.broker, oauthproto.TokenTypeRefreshToken, &soon).str(t, "access_token")
			revokedThenExpired := env.grantToken(t, env.broker, oauthproto.TokenTypeRefreshToken, &soon).str(t, "access_token")
			require.Equal(t, http.StatusOK, env.derive(t, env.broker, expired, oauthproto.TokenTypeIDJAG, nil).status)
			env.revoke(t, revokedThenExpired)
			requireError(t, env.derive(t, env.broker, revokedThenExpired, oauthproto.TokenTypeIDJAG, nil),
				"invalid_request", "revoked")
			time.Sleep(4 * time.Second)
			for _, tc := range []struct{ name, token string }{
				{"expired", expired},
				{"revoked then expired", revokedThenExpired},
			} {
				res := env.derive(t, env.broker, tc.token, oauthproto.TokenTypeIDJAG, nil)
				assert.Equal(t, http.StatusBadRequest, res.status, tc.name)
				assert.Equal(t, map[string]any{"error": "invalid_request", "error_description": "grant_invalid"}, res.body, tc.name)
			}
		})

		t.Run("revoked parent", func(t *testing.T) {
			parent := env.grantToken(t, env.broker, oauthproto.TokenTypeRefreshToken, nil).str(t, "access_token")
			child := env.derive(t, env.broker, parent, oauthproto.TokenTypeIDJAG, nil)
			require.Equal(t, http.StatusOK, child.status, child.body)
			env.revoke(t, parent)
			requireError(t, env.derive(t, env.broker, parent, oauthproto.TokenTypeIDJAG, nil),
				"invalid_request", "revoked")
			requireError(t, env.derive(t, env.broker, child.str(t, "access_token"), oauthproto.TokenTypeIDJAG, nil),
				"invalid_request", "parent_revoked")
		})

		t.Run("two derives give two independent grants", func(t *testing.T) {
			parent := env.grantToken(t, env.broker, oauthproto.TokenTypeRefreshToken, nil).str(t, "access_token")
			first := env.derive(t, env.broker, parent, oauthproto.TokenTypeIDJAG, nil)
			second := env.derive(t, env.broker, parent, oauthproto.TokenTypeIDJAG, nil)
			require.Equal(t, http.StatusOK, first.status, first.body)
			require.Equal(t, http.StatusOK, second.status, second.body)
			require.NotEqual(t, first.str(t, "access_token"), second.str(t, "access_token"))
			env.revoke(t, first.str(t, "access_token"))
			requireError(t, env.derive(t, env.broker, first.str(t, "access_token"), oauthproto.TokenTypeIDJAG, nil),
				"invalid_request", "revoked")
			requireError(t, env.derive(t, env.broker, second.str(t, "access_token"), oauthproto.TokenTypeIDJAG, nil),
				"invalid_request", "not_issuable")
		})

		t.Run("request refusals apply to a derive", func(t *testing.T) {
			parent := env.grantToken(t, env.broker, oauthproto.TokenTypeRefreshToken, nil).str(t, "access_token")
			for _, tc := range []struct {
				mutate func(url.Values)
				code   string
				reason string
			}{
				{func(f url.Values) { f.Del("authorization_details") },
					"invalid_request", "The authorization_details parameter is required."},
				{func(f url.Values) { f.Set("actor_token", "x") }, "invalid_request",
					hinted(fosite.ErrInvalidRequest, "The 'actor_token' parameter is not supported when requesting a refresh token.")},
				{func(f url.Values) { f.Set("scope", "openid") }, "invalid_scope",
					hinted(fosite.ErrInvalidScope, "The 'scope' parameter is not supported when requesting a refresh token.")},
				{func(f url.Values) { f.Set("resource", cfgAudienceA) }, "invalid_target",
					hinted(server.ErrInvalidTarget, "The 'resource' and 'audience' parameters are not supported when requesting a refresh token.")},
			} {
				requireError(t, env.derive(t, env.broker, parent, oauthproto.TokenTypeIDJAG, tc.mutate),
					tc.code, tc.reason)
			}
		})

		t.Run("client without exchange_refresh_tokens keeps the subject refusal", func(t *testing.T) {
			parent := env.grantToken(t, env.broker, oauthproto.TokenTypeRefreshToken, nil).str(t, "access_token")
			requireError(t, env.derive(t, env.exchange, parent, oauthproto.TokenTypeIDJAG, nil),
				"invalid_request", hinted(fosite.ErrInvalidRequest, fmt.Sprintf(
					"The 'subject_token_type' value %q is not supported. Use %q or %q.",
					oauthproto.TokenTypeRefreshToken, oauthproto.TokenTypeAccessToken, oauthproto.TokenTypeJWT)))
		})
	})
}

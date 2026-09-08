// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cedar

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// TestDelegatedTokenEndToEnd drives a real RFC 8693-shaped token through the
// production authentication middleware and into Cedar, rather than
// hand-constructing an Identity as the unit tests above do.
//
// The two cases together pin the trust boundary the session-less fallback rests
// on. A hand-built Identity cannot express the distinction: it sets
// SessionlessGrant directly, so it proves nothing about whether the validator
// would have derived it. Only the real middleware can, because the fact is
// derived there from the validated issuer.
func TestDelegatedTokenEndToEnd(t *testing.T) {
	t.Parallel()

	const (
		embeddedIssuer = "https://auth.toolhive.example.com"
		foreignIssuer  = "https://idp.other.example.com"
		audience       = "https://mcp.example.com/mcp"
		keyID          = "e2e-key-1"
	)

	// A pinned provider whose credentials never load: every request here has a
	// nil UpstreamTokens map, so the outcome turns purely on provenance.
	const providerName = "github"

	policy := `
		permit(
			principal,
			action == Action::"call_tool",
			resource == Tool::"deploy"
		)
		when {
			principal.thv_claim_source == "request:no-upstream-session" &&
			principal has claim_act &&
			principal.claim_act.sub == "delegate-agent"
		};
	`

	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies:                []string{policy},
		EntitiesJSON:            `[]`,
		PrimaryUpstreamProvider: providerName,
	}, "")
	require.NoError(t, err)

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	pubKey, err := jwk.Import(&privateKey.PublicKey)
	require.NoError(t, err)
	require.NoError(t, pubKey.Set(jwk.KeyIDKey, keyID))
	require.NoError(t, pubKey.Set(jwk.AlgorithmKey, "RS256"))
	require.NoError(t, pubKey.Set(jwk.KeyUsageKey, "sig"))

	keySet := jwk.NewSet()
	require.NoError(t, keySet.AddKey(pubKey))

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		buf, mErr := json.Marshal(keySet)
		if mErr != nil {
			http.Error(w, mErr.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buf)
	}))
	t.Cleanup(jwksServer.Close)

	signToken := func(t *testing.T, claims jwt.MapClaims) string {
		t.Helper()
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = keyID
		signed, sErr := tok.SignedString(privateKey)
		require.NoError(t, sErr)
		return signed
	}

	// validatorFor builds the incoming-auth validator a deployment would run:
	// oidcIssuer is what the vMCP OIDC config pins, embeddedIssuer names the
	// local embedded authorization server. They coincide only when the embedded
	// server is the deployment's own front door.
	validatorFor := func(t *testing.T, oidcIssuer string) *auth.TokenValidator {
		t.Helper()
		v, vErr := auth.NewTokenValidator(t.Context(), auth.TokenValidatorConfig{
			Issuer:            oidcIssuer,
			Audience:          audience,
			JWKSURL:           jwksServer.URL,
			ClientID:          "vmcp",
			AllowPrivateIP:    true,
			InsecureAllowHTTP: true,
		}, auth.WithEmbeddedAuthServerIssuer(embeddedIssuer))
		require.NoError(t, vErr)
		return v
	}

	// identityThroughMiddleware runs the token through real validation and
	// returns the Identity the rest of the stack would see.
	identityThroughMiddleware := func(t *testing.T, v *auth.TokenValidator, token string) *auth.Identity {
		t.Helper()
		var captured *auth.Identity
		handler := v.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			captured, _ = auth.IdentityFromContext(r.Context())
		}))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "token must authenticate; this test is about authorization")
		require.NotNil(t, captured)
		require.Nil(t, captured.UpstreamTokens, "no tsid claim, so no upstream credential should load")
		return captured
	}

	delegatedClaims := func(issuer string) jwt.MapClaims {
		return jwt.MapClaims{
			"iss":       issuer,
			"aud":       audience,
			"sub":       "alice",
			"exp":       time.Now().Add(time.Hour).Unix(),
			"client_id": "delegate-agent",
			// RFC 8693 §4.1: the current actor is the outermost act.
			"act": map[string]any{"iss": issuer, "sub": "delegate-agent"},
		}
	}

	t.Run("delegated token from the embedded auth server authorizes under a pinned provider", func(t *testing.T) {
		t.Parallel()

		v := validatorFor(t, embeddedIssuer)
		identity := identityThroughMiddleware(t, v, signToken(t, delegatedClaims(embeddedIssuer)))
		require.True(t, identity.SessionlessGrant,
			"the validator must derive provenance for its own embedded auth server")

		authorized, aErr := authorizer.AuthorizeWithJWTClaims(
			auth.WithIdentity(t.Context(), identity),
			authorizers.MCPFeatureTool, authorizers.MCPOperationCall, "deploy", nil,
		)
		require.NoError(t, aErr, "a proven session-less grant must reach policy evaluation")
		assert.True(t, authorized, "the policy permits this actor under request:no-upstream-session")
	})

	t.Run("foreign issuer asserting act and the marker is denied", func(t *testing.T) {
		t.Parallel()

		// The deployment's incoming auth trusts a different IdP than the embedded
		// authorization server. That IdP's tokens are perfectly valid, so they
		// authenticate — but they are not evidence about a grant this deployment's
		// own authorization server minted, and must not reach the fallback.
		claims := delegatedClaims(foreignIssuer)
		claims[upstreamtoken.NoUpstreamSessionClaimKey] = true

		v := validatorFor(t, foreignIssuer)
		identity := identityThroughMiddleware(t, v, signToken(t, claims))
		require.False(t, identity.SessionlessGrant,
			"a foreign issuer must not establish session-less grant provenance, "+
				"however loudly its tokens assert act and the marker")

		_, aErr := authorizer.AuthorizeWithJWTClaims(
			auth.WithIdentity(t.Context(), identity),
			authorizers.MCPFeatureTool, authorizers.MCPOperationCall, "deploy", nil,
		)
		require.Error(t, aErr, "the pinned provider's boundary must hold")
		assert.Contains(t, aErr.Error(), "no session-less grant provenance")
	})
}

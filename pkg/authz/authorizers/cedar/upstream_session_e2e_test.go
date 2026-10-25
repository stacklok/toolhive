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
	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// TestUpstreamSessionWithoutRowsEndToEnd drives a token that carries a token
// session id through the production authentication middleware, backed by the real
// upstream-token service over in-memory storage, and on into Cedar pinned to the
// session's provider. It pins that a still-valid session whose stored upstream
// rows are all gone is rejected by authentication before Cedar is consulted at
// all; the challenge itself is pinned in pkg/auth. A hand-built Identity cannot
// express that, so the real middleware runs, as TestDelegatedTokenEndToEnd does
// for provenance.
func TestUpstreamSessionWithoutRowsEndToEnd(t *testing.T) {
	t.Parallel()

	const (
		embeddedIssuer = "https://auth.toolhive.example.com"
		audience       = "https://mcp.example.com/mcp"
		keyID          = "e2e-session-key-1"
		providerName   = "google"
	)

	// The opaque upstream token makes Cedar fall back to the request token's
	// claims, so the control case authorizes on the email the login mirrored.
	authorizer, err := NewCedarAuthorizer(ConfigOptions{
		Policies: []string{`
			permit(principal, action == Action::"call_tool", resource == Tool::"deploy")
			when { principal has claim_email };
		`},
		EntitiesJSON:            `[]`,
		PrimaryUpstreamProvider: providerName,
	}, "")
	require.NoError(t, err)

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	pubKey, err := jwk.Import[jwk.Key](&privateKey.PublicKey)
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

	// sessionToken mints a validator-accepted token bound to one token session id.
	sessionToken := func(t *testing.T, sessionID string) string {
		t.Helper()
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss":                                embeddedIssuer,
			"aud":                                audience,
			"sub":                                "alice",
			"email":                              "alice@example.com",
			"exp":                                time.Now().Add(time.Hour).Unix(),
			upstreamtoken.TokenSessionIDClaimKey: sessionID,
		})
		tok.Header["kid"] = keyID
		signed, sErr := tok.SignedString(privateKey)
		require.NoError(t, sErr)
		return signed
	}

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })

	validator, err := auth.NewTokenValidator(t.Context(), auth.TokenValidatorConfig{
		Issuer:            embeddedIssuer,
		Audience:          audience,
		JWKSURL:           jwksServer.URL,
		ClientID:          "vmcp",
		AllowPrivateIP:    true,
		InsecureAllowHTTP: true,
	},
		auth.WithUpstreamTokenReader(upstreamtoken.NewInProcessService(stor, nil)),
		auth.WithEmbeddedAuthServerIssuer(embeddedIssuer),
	)
	require.NoError(t, err)

	// authenticate runs the token through the real middleware and returns the
	// recorded response plus the Identity the rest of the stack would see (nil
	// when authentication rejected the request).
	authenticate := func(t *testing.T, token string) (*httptest.ResponseRecorder, *auth.Identity) {
		t.Helper()
		var captured *auth.Identity
		handler := validator.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			captured, _ = auth.IdentityFromContext(r.Context())
		}))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr, captured
	}

	storeRow := func(t *testing.T, sessionID string) {
		t.Helper()
		require.NoError(t, stor.StoreUpstreamTokens(t.Context(), sessionID, providerName, &storage.UpstreamTokens{
			ProviderID:  providerName,
			AccessToken: "ya29.opaque-access-token",
			ExpiresAt:   time.Now().Add(time.Hour),
		}))
	}

	t.Run("stored row authenticates and authorizes", func(t *testing.T) {
		t.Parallel()
		const sessionID = "tsid-e2e-stored"
		storeRow(t, sessionID)

		rr, identity := authenticate(t, sessionToken(t, sessionID))
		require.Equal(t, http.StatusOK, rr.Code)
		require.NotNil(t, identity)
		require.Equal(t, map[string]string{providerName: "ya29.opaque-access-token"}, identity.UpstreamTokens)

		authorized, aErr := authorizer.AuthorizeWithJWTClaims(
			auth.WithIdentity(t.Context(), identity),
			authorizers.MCPFeatureTool, authorizers.MCPOperationCall, "deploy", nil,
		)
		require.NoError(t, aErr)
		assert.True(t, authorized)
	})

	t.Run("session whose rows are gone is rejected before Cedar is consulted", func(t *testing.T) {
		t.Parallel()
		const sessionID = "tsid-e2e-expired"
		storeRow(t, sessionID)
		// Deleting the rows yields the same read as a storage TTL that elapsed:
		// the service returns no credentials for the session.
		require.NoError(t, stor.DeleteUpstreamTokens(t.Context(), sessionID))

		rr, identity := authenticate(t, sessionToken(t, sessionID))
		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Nil(t, identity, "authentication must not admit a session without upstream credentials")
	})
}

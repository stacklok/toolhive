// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	pkgauth "github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	keysmocks "github.com/stacklok/toolhive/pkg/authserver/server/keys/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestNewOIDCAuthMiddleware_KeyProviderWiring verifies that an in-process
// PublicKeyProvider is used for JWKS key resolution, avoiding self-referential
// HTTP calls when the embedded auth server runs in the same process.
func TestNewOIDCAuthMiddleware_KeyProviderWiring(t *testing.T) {
	t.Parallel()

	// Generate an ECDSA P-256 key pair (matching the embedded auth server's
	// default GeneratingProvider algorithm).
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	const ecdsaKeyID = "test-ecdsa-key-1"

	// Stand up a minimal OIDC discovery server so issuer validation passes.
	// The JWKS endpoint returns an empty key set — all key resolution should
	// happen through the local provider, not HTTP.
	server, _ := newTestOIDCServer(t)
	t.Cleanup(server.Close)

	issuer := server.URL

	oidcCfg := &config.OIDCConfig{
		Issuer:             issuer,
		ClientID:           "test-client",
		Audience:           "test-audience",
		InsecureAllowHTTP:  true,
		JwksAllowPrivateIP: true,
	}

	t.Run("keys resolved from local provider instead of HTTP", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		mockProvider := keysmocks.NewMockPublicKeyProvider(ctrl)
		mockProvider.EXPECT().
			PublicKeys(gomock.Any()).
			Return([]*keys.PublicKeyData{{
				KeyID:     ecdsaKeyID,
				Algorithm: "ES256",
				PublicKey: &privateKey.PublicKey,
				CreatedAt: time.Now(),
			}}, nil).
			AnyTimes()

		authMw, _, err := newOIDCAuthMiddleware(t.Context(), oidcCfg, nil, mockProvider)
		require.NoError(t, err, "middleware creation should succeed with key provider")
		require.NotNil(t, authMw)

		var capturedIdentity *pkgauth.Identity
		handler := authMw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedIdentity, _ = pkgauth.IdentityFromContext(r.Context())
		}))

		// Sign a JWT with the ECDSA private key — only the local provider
		// holds the matching public key.
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
			"iss": issuer,
			"aud": "test-audience",
			"sub": "test-user",
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		tok.Header["kid"] = ecdsaKeyID
		tokenString, err := tok.SignedString(privateKey)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+tokenString)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "request should succeed via local key provider")
		require.NotNil(t, capturedIdentity, "identity should be present in context")
		assert.Equal(t, "test-user", capturedIdentity.Subject)
	})

	t.Run("falls back to HTTP JWKS when key provider is nil", func(t *testing.T) {
		t.Parallel()

		// Use the RSA key from the test OIDC server (served via HTTP JWKS).
		httpServer, rsaPrivateKey := newTestOIDCServer(t)
		t.Cleanup(httpServer.Close)

		httpIssuer := httpServer.URL
		httpOIDCCfg := &config.OIDCConfig{
			Issuer:             httpIssuer,
			ClientID:           "test-client",
			Audience:           "test-audience",
			InsecureAllowHTTP:  true,
			JwksAllowPrivateIP: true,
		}

		authMw, _, err := newOIDCAuthMiddleware(t.Context(), httpOIDCCfg, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, authMw)

		var capturedIdentity *pkgauth.Identity
		handler := authMw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedIdentity, _ = pkgauth.IdentityFromContext(r.Context())
		}))

		token := signJWT(t, rsaPrivateKey, jwt.MapClaims{
			"iss": httpIssuer,
			"aud": "test-audience",
			"sub": "test-user",
			"exp": time.Now().Add(time.Hour).Unix(),
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "request should succeed via HTTP JWKS fallback")
		require.NotNil(t, capturedIdentity, "identity should be present in context")
		assert.Equal(t, "test-user", capturedIdentity.Subject)
	})
}

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

// TestNewOIDCAuthMiddleware_KeyProvider_LocalResolution verifies that when a
// PublicKeyProvider is wired in, key resolution happens in-process via the
// local provider rather than through an HTTP JWKS fetch.
func TestNewOIDCAuthMiddleware_KeyProvider_LocalResolution(t *testing.T) {
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
}

// TestNewOIDCAuthMiddleware_KeyProvider_HTTPFallback verifies that when the
// key provider is nil, key resolution falls back to an HTTP JWKS fetch.
func TestNewOIDCAuthMiddleware_KeyProvider_HTTPFallback(t *testing.T) {
	t.Parallel()

	// Use the RSA key from the test OIDC server (served via HTTP JWKS).
	server, rsaPrivateKey := newTestOIDCServer(t)
	t.Cleanup(server.Close)

	issuer := server.URL
	oidcCfg := &config.OIDCConfig{
		Issuer:             issuer,
		ClientID:           "test-client",
		Audience:           "test-audience",
		InsecureAllowHTTP:  true,
		JwksAllowPrivateIP: true,
	}

	authMw, _, err := newOIDCAuthMiddleware(t.Context(), oidcCfg, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, authMw)

	var capturedIdentity *pkgauth.Identity
	handler := authMw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedIdentity, _ = pkgauth.IdentityFromContext(r.Context())
	}))

	token := signJWT(t, rsaPrivateKey, jwt.MapClaims{
		"iss": issuer,
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
}

// TestNewOIDCAuthMiddleware_KeyProvider_KidMissFallback verifies that when the
// local PublicKeyProvider does not hold a key matching the JWT's kid, the
// validator falls back to HTTP JWKS and the request still succeeds. This
// confirms the end-to-end wiring for the kid-miss path at the factory level.
func TestNewOIDCAuthMiddleware_KeyProvider_KidMissFallback(t *testing.T) {
	t.Parallel()

	// Stand up a real OIDC server that serves the RSA key via HTTP JWKS.
	server, rsaPrivateKey := newTestOIDCServer(t)
	t.Cleanup(server.Close)

	issuer := server.URL
	oidcCfg := &config.OIDCConfig{
		Issuer:             issuer,
		ClientID:           "test-client",
		Audience:           "test-audience",
		InsecureAllowHTTP:  true,
		JwksAllowPrivateIP: true,
	}

	// Wire a mock provider that returns a key with a *different* kid than the
	// one in the JWT. The validator should call the local provider first, get a
	// kid-miss (nil key returned), and then fall back to HTTP JWKS.
	ctrl := gomock.NewController(t)
	mockProvider := keysmocks.NewMockPublicKeyProvider(ctrl)

	// Generate a throwaway ECDSA key so the mock returns a non-nil key list
	// with a different kid.
	throwawayKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	mockProvider.EXPECT().
		PublicKeys(gomock.Any()).
		Return([]*keys.PublicKeyData{{
			KeyID:     "unrelated-key-id", // does NOT match testKeyID used by signJWT
			Algorithm: "ES256",
			PublicKey: &throwawayKey.PublicKey,
			CreatedAt: time.Now(),
		}}, nil).
		AnyTimes()

	authMw, _, err := newOIDCAuthMiddleware(t.Context(), oidcCfg, nil, mockProvider)
	require.NoError(t, err)
	require.NotNil(t, authMw)

	var capturedIdentity *pkgauth.Identity
	handler := authMw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedIdentity, _ = pkgauth.IdentityFromContext(r.Context())
	}))

	// Sign the JWT with the RSA key from the test server (kid = testKeyID).
	// The mock provider holds a key with a different kid, so the validator must
	// fall back to HTTP JWKS to find the matching key.
	token := signJWT(t, rsaPrivateKey, jwt.MapClaims{
		"iss": issuer,
		"aud": "test-audience",
		"sub": "test-user",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "request should succeed via HTTP JWKS fallback on kid-miss")
	require.NotNil(t, capturedIdentity, "identity should be present in context")
	assert.Equal(t, "test-user", capturedIdentity.Subject)
}

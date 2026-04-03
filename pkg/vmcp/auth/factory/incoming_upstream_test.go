// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

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
	"go.uber.org/mock/gomock"

	pkgauth "github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	upstreamtokenmocks "github.com/stacklok/toolhive/pkg/auth/upstreamtoken/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

const testKeyID = "test-key-1"

// newTestOIDCServer creates a test HTTP server that serves both an OIDC
// discovery document and a JWKS endpoint. It returns the server, the RSA
// private key for signing JWTs, and the issuer URL.
func newTestOIDCServer(t *testing.T) (*httptest.Server, *rsa.PrivateKey) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	key, err := jwk.Import(&privateKey.PublicKey)
	require.NoError(t, err)
	require.NoError(t, key.Set(jwk.KeyIDKey, testKeyID))
	require.NoError(t, key.Set(jwk.AlgorithmKey, "RS256"))
	require.NoError(t, key.Set(jwk.KeyUsageKey, "sig"))

	keySet := jwk.NewSet()
	require.NoError(t, keySet.AddKey(key))

	mux := http.NewServeMux()

	// Serve JWKS
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		buf, marshalErr := json.Marshal(keySet)
		require.NoError(t, marshalErr)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buf)
	})

	// We use a placeholder for the issuer and jwks_uri here; they get patched
	// after the server starts (we need the server URL).
	var issuerURL string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]any{
			"issuer":                                issuerURL,
			"jwks_uri":                              issuerURL + "/jwks",
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})

	server := httptest.NewServer(mux)
	issuerURL = server.URL

	return server, privateKey
}

// signJWT signs a JWT with the given claims using the test RSA private key.
func signJWT(t *testing.T, privateKey *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = testKeyID
	s, err := tok.SignedString(privateKey)
	require.NoError(t, err)
	return s
}

// TestNewOIDCAuthMiddleware_UpstreamTokenReaderWiring verifies the full wiring:
// newOIDCAuthMiddleware forwards the TokenReader through to the TokenValidator,
// and a request with a JWT containing a "tsid" claim triggers GetAllValidTokens
// on the reader, populating Identity.UpstreamTokens.
func TestNewOIDCAuthMiddleware_UpstreamTokenReaderWiring(t *testing.T) {
	t.Parallel()

	server, privateKey := newTestOIDCServer(t)
	t.Cleanup(server.Close)

	issuer := server.URL

	oidcCfg := &config.OIDCConfig{
		Issuer:             issuer,
		ClientID:           "test-client",
		Audience:           "test-audience",
		InsecureAllowHTTP:  true,
		JwksAllowPrivateIP: true,
	}

	t.Run("upstream tokens populated when reader is non-nil and tsid present", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		reader := upstreamtokenmocks.NewMockTokenReader(ctrl)
		reader.EXPECT().
			GetAllValidTokens(gomock.Any(), "session-abc").
			Return(map[string]string{"google": "gcp-access-token"}, nil)

		authMw, _, err := newOIDCAuthMiddleware(t.Context(), oidcCfg, reader, nil)
		require.NoError(t, err, "middleware creation should succeed with non-nil reader")
		require.NotNil(t, authMw)

		var capturedIdentity *pkgauth.Identity
		handler := authMw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedIdentity, _ = pkgauth.IdentityFromContext(r.Context())
		}))

		token := signJWT(t, privateKey, jwt.MapClaims{
			"iss":                                issuer,
			"aud":                                "test-audience",
			"sub":                                "test-user",
			"exp":                                time.Now().Add(time.Hour).Unix(),
			upstreamtoken.TokenSessionIDClaimKey: "session-abc",
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "request should succeed")
		require.NotNil(t, capturedIdentity, "identity should be present in context")
		assert.Equal(t, map[string]string{"google": "gcp-access-token"}, capturedIdentity.UpstreamTokens,
			"upstream tokens should be populated from the reader")
	})

	t.Run("upstream tokens nil when reader is nil", func(t *testing.T) {
		t.Parallel()

		authMw, _, err := newOIDCAuthMiddleware(t.Context(), oidcCfg, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, authMw)

		var capturedIdentity *pkgauth.Identity
		handler := authMw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedIdentity, _ = pkgauth.IdentityFromContext(r.Context())
		}))

		token := signJWT(t, privateKey, jwt.MapClaims{
			"iss":                                issuer,
			"aud":                                "test-audience",
			"sub":                                "test-user",
			"exp":                                time.Now().Add(time.Hour).Unix(),
			upstreamtoken.TokenSessionIDClaimKey: "session-abc",
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "request should succeed")
		require.NotNil(t, capturedIdentity, "identity should be present in context")
		assert.Nil(t, capturedIdentity.UpstreamTokens,
			"upstream tokens should be nil when no reader is configured")
	})

	t.Run("reader not called when tsid claim absent", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		reader := upstreamtokenmocks.NewMockTokenReader(ctrl)
		// No EXPECT -- reader should not be called when tsid is absent.

		authMw, _, err := newOIDCAuthMiddleware(t.Context(), oidcCfg, reader, nil)
		require.NoError(t, err)
		require.NotNil(t, authMw)

		var capturedIdentity *pkgauth.Identity
		handler := authMw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedIdentity, _ = pkgauth.IdentityFromContext(r.Context())
		}))

		token := signJWT(t, privateKey, jwt.MapClaims{
			"iss": issuer,
			"aud": "test-audience",
			"sub": "test-user",
			"exp": time.Now().Add(time.Hour).Unix(),
			// No tsid claim
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "request should succeed")
		require.NotNil(t, capturedIdentity, "identity should be present in context")
		assert.Nil(t, capturedIdentity.UpstreamTokens,
			"upstream tokens should be nil when JWT has no tsid claim")
	})
}

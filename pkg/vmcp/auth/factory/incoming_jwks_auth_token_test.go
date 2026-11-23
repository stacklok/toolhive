// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestOIDCTokenValidatorConfig_JWKSAuthTokenPath(t *testing.T) {
	t.Parallel()

	got, err := oidcTokenValidatorConfig(&config.OIDCConfig{
		JWKSAuthTokenPath: "/var/run/secrets/kubernetes.io/serviceaccount/token",
	}, stubEnvReader{})
	require.NoError(t, err)
	assert.Equal(t, "/var/run/secrets/kubernetes.io/serviceaccount/token", got.AuthTokenFile)

	got, err = oidcTokenValidatorConfig(&config.OIDCConfig{}, stubEnvReader{})
	require.NoError(t, err)
	assert.Empty(t, got.AuthTokenFile)
}

func TestCheckJWKSAuthTokenFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("token"), 0o600))

	tests := []struct {
		name   string
		path   string
		errMsg string
	}{
		{name: "unset", path: ""},
		{name: "regular file", path: tokenPath},
		{name: "missing file", path: filepath.Join(dir, "missing"), errMsg: "is not accessible"},
		{name: "directory", path: dir, errMsg: "is not a regular file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkJWKSAuthTokenFile(tt.path)
			if tt.errMsg == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "incomingAuth.oidc.jwksAuthTokenPath")
			assert.Contains(t, err.Error(), tt.path)
			assert.Contains(t, err.Error(), tt.errMsg)
			assert.Contains(t, err.Error(), "unset jwksAuthTokenPath")
		})
	}
}

func TestNewOIDCAuthMiddleware_MissingJWKSAuthTokenFile(t *testing.T) {
	t.Parallel()

	cfg := &config.OIDCConfig{
		Issuer:            "https://issuer.example.com",
		Audience:          "test-audience",
		JWKSAuthTokenPath: filepath.Join(t.TempDir(), "missing"),
	}

	_, _, err := newOIDCAuthMiddleware(t.Context(), cfg, nil, nil, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "incomingAuth.oidc.jwksAuthTokenPath")
}

// TestNewOIDCAuthMiddleware_JWKSAuthTokenPathReachesIssuer covers an issuer
// that, like the Kubernetes API server, rejects unauthenticated OIDC discovery
// and JWKS requests. The middleware must send the token read from
// JWKSAuthTokenPath on both requests for a valid JWT to be accepted.
func TestNewOIDCAuthMiddleware_JWKSAuthTokenPathReachesIssuer(t *testing.T) {
	t.Parallel()

	const issuerToken = "issuer-access-token"

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	key, err := jwk.Import[jwk.Key](&privateKey.PublicKey)
	require.NoError(t, err)
	require.NoError(t, key.Set(jwk.KeyIDKey, testKeyID))
	require.NoError(t, key.Set(jwk.AlgorithmKey, "RS256"))
	keySet := jwk.NewSet()
	require.NoError(t, keySet.AddKey(key))
	jwksBody, err := json.Marshal(keySet)
	require.NoError(t, err)

	var discoveryAuthorized, jwksAuthorized atomic.Bool
	var issuerURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+issuerToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		discoveryAuthorized.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuerURL,
			"jwks_uri":                              issuerURL + "/jwks",
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+issuerToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		jwksAuthorized.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksBody)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	issuerURL = server.URL

	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte(issuerToken), 0o600))

	cfg := &config.OIDCConfig{
		Issuer:             issuerURL,
		Audience:           "test-audience",
		InsecureAllowHTTP:  true,
		JwksAllowPrivateIP: true,
		JWKSAuthTokenPath:  tokenPath,
	}

	authMw, _, err := newOIDCAuthMiddleware(t.Context(), cfg, nil, nil, "")
	require.NoError(t, err)
	require.NotNil(t, authMw)

	handler := authMw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	signed := signJWT(t, privateKey, jwt.MapClaims{
		"iss": issuerURL,
		"aud": "test-audience",
		"sub": "system:serviceaccount:default:client",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, discoveryAuthorized.Load(), "discovery request should carry the issuer token")
	assert.True(t, jwksAuthorized.Load(), "JWKS request should carry the issuer token")
}

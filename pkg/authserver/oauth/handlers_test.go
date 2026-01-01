package oauth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// testSetup creates a Router with all dependencies for testing.
func testSetup(t *testing.T) *Router {
	t.Helper()

	// Generate RSA key for testing
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	require.NoError(t, err)

	cfg := &Config{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		Secret:               secret,
		PrivateKeys: []PrivateKey{
			{
				KeyID:     "test-key-1",
				Algorithm: "RS256",
				Key:       rsaKey,
			},
		},
	}

	oauth2Config, err := NewOAuth2Config(cfg)
	require.NoError(t, err)

	stor := storage.NewMemoryStorage()
	provider := fosite.NewOAuth2Provider(stor, oauth2Config.Config)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger, provider, oauth2Config, stor)

	return router
}

func TestJWKSHandler(t *testing.T) {
	t.Parallel()
	router := testSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	rec := httptest.NewRecorder()

	router.JWKSHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Header().Get("Cache-Control"), "max-age=")

	// Parse the response as JWKS
	var jwks jose.JSONWebKeySet
	err := json.NewDecoder(rec.Body).Decode(&jwks)
	require.NoError(t, err)

	// Verify we have at least one key
	assert.Len(t, jwks.Keys, 1)

	// Verify the key has expected properties
	key := jwks.Keys[0]
	assert.Equal(t, "test-key-1", key.KeyID)
	assert.Equal(t, "RS256", key.Algorithm)
	assert.Equal(t, "sig", key.Use)

	// Verify the key is public (not private)
	assert.False(t, key.IsPublic() == false, "JWKS should only contain public keys")
}

func TestJWKSHandler_NilJWKS(t *testing.T) {
	t.Parallel()
	// Create a router with nil JWKS to test error handling
	cfg := &OAuth2Config{
		Config:      &fosite.Config{},
		SigningKey:  nil,
		SigningJWKS: nil,
	}

	stor := storage.NewMemoryStorage()
	provider := fosite.NewOAuth2Provider(stor, cfg.Config)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger, provider, cfg, stor)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	rec := httptest.NewRecorder()

	router.JWKSHandler(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestOIDCDiscoveryHandler(t *testing.T) {
	t.Parallel()
	router := testSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
	rec := httptest.NewRecorder()

	router.OIDCDiscoveryHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Header().Get("Cache-Control"), "max-age=")

	// Parse the discovery document
	var discovery OIDCDiscoveryDocument
	err := json.NewDecoder(rec.Body).Decode(&discovery)
	require.NoError(t, err)

	// Verify required fields
	assert.Equal(t, "https://auth.example.com", discovery.Issuer)
	assert.Equal(t, "https://auth.example.com/oauth/token", discovery.TokenEndpoint)
	assert.Equal(t, "https://auth.example.com/oauth/authorize", discovery.AuthorizationEndpoint)
	assert.Equal(t, "https://auth.example.com/.well-known/jwks.json", discovery.JWKSURI)

	// Verify supported values
	assert.Contains(t, discovery.ResponseTypesSupported, "code")
	assert.Contains(t, discovery.GrantTypesSupported, "authorization_code")
	assert.Contains(t, discovery.GrantTypesSupported, "refresh_token")
	assert.Contains(t, discovery.CodeChallengeMethodsSupported, "S256")
	assert.Contains(t, discovery.TokenEndpointAuthMethodsSupported, "none")
}

func TestTokenHandler_InvalidRequest(t *testing.T) {
	t.Parallel()
	router := testSetup(t)

	// Send an empty/invalid token request
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	router.TokenHandler(rec, req)

	// Should return an error (400 Bad Request typically)
	assert.GreaterOrEqual(t, rec.Code, 400)
	assert.LessOrEqual(t, rec.Code, 500)

	// Response should be JSON
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	// Parse error response
	var errResp map[string]interface{}
	err := json.NewDecoder(rec.Body).Decode(&errResp)
	require.NoError(t, err)

	// Should have an error field
	assert.Contains(t, errResp, "error")
}

func TestTokenHandler_InvalidGrantType(t *testing.T) {
	t.Parallel()
	router := testSetup(t)

	// Send a request with an invalid grant type
	body := "grant_type=invalid_grant"
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	router.TokenHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var errResp map[string]interface{}
	err := json.NewDecoder(rec.Body).Decode(&errResp)
	require.NoError(t, err)

	// Should return an error (fosite returns invalid_request when no handler matches the grant type)
	assert.Contains(t, errResp, "error")
}

func TestTokenHandler_AuthorizationCodeWithoutCode(t *testing.T) {
	t.Parallel()
	router := testSetup(t)

	// Send authorization_code request without the required 'code' parameter
	body := "grant_type=authorization_code&client_id=test-client"
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	router.TokenHandler(rec, req)

	// Should return an error
	assert.GreaterOrEqual(t, rec.Code, 400)

	var errResp map[string]interface{}
	err := json.NewDecoder(rec.Body).Decode(&errResp)
	require.NoError(t, err)

	// Should report an error
	assert.Contains(t, errResp, "error")
}

func TestNewRouter_NilLogger(t *testing.T) {
	t.Parallel()
	cfg := &OAuth2Config{
		Config: &fosite.Config{},
	}
	stor := storage.NewMemoryStorage()
	provider := fosite.NewOAuth2Provider(stor, cfg.Config)

	// Should not panic with nil logger
	router := NewRouter(nil, provider, cfg, stor)
	assert.NotNil(t, router)
	assert.NotNil(t, router.logger)
}

func TestRoutes(t *testing.T) {
	t.Parallel()
	router := testSetup(t)

	mux := http.NewServeMux()
	router.Routes(mux)

	// Test that routes are registered by making requests
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/.well-known/jwks.json"},
		{http.MethodGet, "/.well-known/openid-configuration"},
		{http.MethodPost, "/oauth/token"},
	}

	for _, tc := range tests {
		tc := tc // capture range variable for parallel test
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.method == http.MethodPost {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			// Should not return 404 (route not found)
			assert.NotEqual(t, http.StatusNotFound, rec.Code,
				"route %s %s should be registered", tc.method, tc.path)
		})
	}
}

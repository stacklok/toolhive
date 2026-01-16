// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package handlers

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
)

// testSetup creates a Handler with all dependencies for testing.
func testSetup(t *testing.T) *Handler {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(func() {
		ctrl.Finish()
	})

	// Generate RSA key for testing
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	require.NoError(t, err)

	cfg := &server.AuthorizationServerParams{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecrets:          servercrypto.NewHMACSecrets(secret),
		SigningKeyID:         "test-key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           rsaKey,
	}

	oauth2Config, err := server.NewAuthorizationServerConfig(cfg)
	require.NoError(t, err)

	stor := mocks.NewMockStorage(ctrl)
	// Setup minimal mock expectations for GetClient (needed by fosite)
	stor.EXPECT().GetClient(gomock.Any(), gomock.Any()).Return(nil, fosite.ErrNotFound).AnyTimes()

	provider := fosite.NewOAuth2Provider(stor, oauth2Config.Config)

	// Use nil upstream for basic handler tests that don't need IDP functionality
	handler := NewHandler(provider, oauth2Config, stor, nil)

	return handler
}

func TestJWKSHandler(t *testing.T) {
	t.Parallel()
	handler := testSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	rec := httptest.NewRecorder()

	handler.JWKSHandler(rec, req)

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
	assert.True(t, key.IsPublic(), "JWKS should only contain public keys")
}

func TestJWKSHandler_NilJWKS(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(func() {
		ctrl.Finish()
	})

	// Create a handler with nil JWKS to test error handling
	cfg := &server.AuthorizationServerConfig{
		Config:      &fosite.Config{},
		SigningKey:  nil,
		SigningJWKS: nil,
	}

	stor := mocks.NewMockStorage(ctrl)
	provider := fosite.NewOAuth2Provider(stor, cfg.Config)
	handler := NewHandler(provider, cfg, stor, nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	rec := httptest.NewRecorder()

	handler.JWKSHandler(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestOIDCDiscoveryHandler(t *testing.T) {
	t.Parallel()
	handler := testSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
	rec := httptest.NewRecorder()

	handler.OIDCDiscoveryHandler(rec, req)

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

	// Verify REQUIRED fields per OIDC Discovery 1.0
	assert.Contains(t, discovery.ResponseTypesSupported, "code")
	assert.Contains(t, discovery.SubjectTypesSupported, "public")
	assert.NotEmpty(t, discovery.IDTokenSigningAlgValuesSupported, "id_token_signing_alg_values_supported is REQUIRED")
	assert.Contains(t, discovery.IDTokenSigningAlgValuesSupported, "RS256")

	// Verify OPTIONAL fields
	assert.Contains(t, discovery.GrantTypesSupported, "authorization_code")
	assert.Contains(t, discovery.GrantTypesSupported, "refresh_token")
	assert.Contains(t, discovery.CodeChallengeMethodsSupported, "S256")
	assert.Contains(t, discovery.TokenEndpointAuthMethodsSupported, "none")
}

// TODO: Add tests for TokenHandler once implemented:
// - TestTokenHandler_InvalidRequest
// - TestTokenHandler_InvalidGrantType
// - TestTokenHandler_AuthorizationCodeWithoutCode

func TestWellKnownRoutes(t *testing.T) {
	t.Parallel()
	handler := testSetup(t)

	router := handler.Routes()

	// Test that well-known routes are registered by making requests
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/.well-known/jwks.json"},
		{http.MethodGet, "/.well-known/openid-configuration"},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			// Should not return 404 (route not found)
			assert.NotEqual(t, http.StatusNotFound, rec.Code,
				"route %s %s should be registered", tc.method, tc.path)
		})
	}
}

// TODO: Add TestOAuthRoutes once OAuth handlers are implemented

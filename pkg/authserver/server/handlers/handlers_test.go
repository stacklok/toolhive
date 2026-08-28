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
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	sharedobauth "github.com/stacklok/toolhive/pkg/oauthproto"
)

// testSetupOptions allows customizing the test handler setup.
type testSetupOptions struct {
	AuthorizationEndpointBaseURL        string
	CIMDEnabled                         bool
	AllowConfidentialClientRegistration bool
	AllowPrivateKeyJWTRegistration      bool
	HasStaticDelegateClients            bool
	JWTBearerGrantEnabled               bool
}

// testSetup creates a Handler with all dependencies for testing.
func testSetup(t *testing.T) *Handler {
	t.Helper()
	return testSetupWithOptions(t, testSetupOptions{})
}

// testSetupWithOptions creates a Handler with customizable configuration.
func testSetupWithOptions(t *testing.T, opts testSetupOptions) *Handler {
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
		Issuer:                              "https://auth.example.com",
		AuthorizationEndpointBaseURL:        opts.AuthorizationEndpointBaseURL,
		CIMDEnabled:                         opts.CIMDEnabled,
		AllowConfidentialClientRegistration: opts.AllowConfidentialClientRegistration,
		AllowPrivateKeyJWTRegistration:      opts.AllowPrivateKeyJWTRegistration,
		HasStaticDelegateClients:            opts.HasStaticDelegateClients,
		JWTBearerGrantEnabled:               opts.JWTBearerGrantEnabled,
		AccessTokenLifespan:                 time.Hour,
		RefreshTokenLifespan:                time.Hour * 24,
		AuthCodeLifespan:                    time.Minute * 10,
		HMACSecrets:                         servercrypto.NewHMACSecrets(secret),
		SigningKeyID:                        "test-key-1",
		SigningKeyAlgorithm:                 "RS256",
		SigningKey:                          rsaKey,
	}

	oauth2Config, err := server.NewAuthorizationServerConfig(cfg)
	require.NoError(t, err)

	stor := mocks.NewMockStorage(ctrl)
	// Setup minimal mock expectations for GetClient (needed by fosite)
	stor.EXPECT().GetClient(gomock.Any(), gomock.Any()).Return(nil, fosite.ErrNotFound).AnyTimes()

	provider := fosite.NewOAuth2Provider(stor, oauth2Config.Config)

	// Use a dummy upstream for basic handler tests that don't need IDP functionality
	dummyUpstream := &mockIDPProvider{}
	handler, err := NewHandler(provider, oauth2Config, stor,
		[]NamedUpstream{{Name: "default", Provider: dummyUpstream}})
	require.NoError(t, err)

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
	assert.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"))

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
	dummyUpstream := &mockIDPProvider{}
	handler, err := NewHandler(provider, cfg, stor,
		[]NamedUpstream{{Name: "default", Provider: dummyUpstream}})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	rec := httptest.NewRecorder()

	handler.JWKSHandler(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestOAuthDiscoveryHandler(t *testing.T) {
	t.Parallel()
	handler := testSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	rec := httptest.NewRecorder()

	handler.OAuthDiscoveryHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"))

	// Parse the OAuth AS metadata document
	var metadata sharedobauth.AuthorizationServerMetadata
	err := json.NewDecoder(rec.Body).Decode(&metadata)
	require.NoError(t, err)

	// Verify REQUIRED field per RFC 8414
	assert.Equal(t, "https://auth.example.com", metadata.Issuer)

	// Verify RECOMMENDED fields per RFC 8414
	assert.Equal(t, "https://auth.example.com/oauth/token", metadata.TokenEndpoint)
	assert.Equal(t, "https://auth.example.com/oauth/authorize", metadata.AuthorizationEndpoint)
	assert.Equal(t, "https://auth.example.com/.well-known/jwks.json", metadata.JWKSURI)
	assert.Equal(t, "https://auth.example.com/oauth/register", metadata.RegistrationEndpoint)
	assert.Contains(t, metadata.ResponseTypesSupported, "code")

	// Verify OPTIONAL fields per RFC 8414
	assert.Contains(t, metadata.GrantTypesSupported, "authorization_code")
	assert.Contains(t, metadata.GrantTypesSupported, "refresh_token")
	assert.Contains(t, metadata.GrantTypesSupported, sharedobauth.GrantTypeTokenExchange)
	assert.Contains(t, metadata.CodeChallengeMethodsSupported, "S256")
	// Flag off: only "none" is advertised (exact-slice, not Contains — a
	// Contains assertion cannot fail to notice an unexpectedly-added method).
	assert.Equal(t, []string{sharedobauth.TokenEndpointAuthMethodNone}, metadata.TokenEndpointAuthMethodsSupported)
}

func TestOAuthDiscoveryHandler_DoesNotContainOIDCFields(t *testing.T) {
	t.Parallel()
	handler := testSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	rec := httptest.NewRecorder()

	handler.OAuthDiscoveryHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Parse as raw JSON to check for OIDC-specific fields
	var rawResponse map[string]interface{}
	err := json.NewDecoder(rec.Body).Decode(&rawResponse)
	require.NoError(t, err)

	// Verify OIDC-specific fields are NOT present in OAuth AS metadata
	_, hasSubjectTypes := rawResponse["subject_types_supported"]
	assert.False(t, hasSubjectTypes, "subject_types_supported should not be in OAuth AS metadata")

	_, hasIDTokenSigningAlgs := rawResponse["id_token_signing_alg_values_supported"]
	assert.False(t, hasIDTokenSigningAlgs, "id_token_signing_alg_values_supported should not be in OAuth AS metadata")
}

func TestOIDCDiscoveryHandler(t *testing.T) {
	t.Parallel()
	handler := testSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
	rec := httptest.NewRecorder()

	handler.OIDCDiscoveryHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"))

	// Parse the discovery document
	var discovery sharedobauth.OIDCDiscoveryDocument
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
	assert.Contains(t, discovery.GrantTypesSupported, sharedobauth.GrantTypeTokenExchange)
	assert.Contains(t, discovery.CodeChallengeMethodsSupported, "S256")
	// Flag off: only "none" is advertised (exact-slice, not Contains).
	assert.Equal(t, []string{sharedobauth.TokenEndpointAuthMethodNone}, discovery.TokenEndpointAuthMethodsSupported)
}

// TestDiscoveryHandlers_ConfidentialAuthMethods verifies both discovery endpoints
// advertise the client authentication methods enabled by configuration, including
// the independent private_key_jwt capability. "none" must stay at index 0
// (readability convention; RFC 8414 defines no ordering).
func TestDiscoveryHandlers_ConfidentialAuthMethods(t *testing.T) {
	t.Parallel()

	wantOff := []string{sharedobauth.TokenEndpointAuthMethodNone}
	wantSecrets := []string{
		sharedobauth.TokenEndpointAuthMethodNone,
		sharedobauth.TokenEndpointAuthMethodClientSecretBasic,
		sharedobauth.TokenEndpointAuthMethodClientSecretPost,
	}
	wantPrivateKeyJWT := []string{
		sharedobauth.TokenEndpointAuthMethodNone,
		sharedobauth.TokenEndpointAuthMethodPrivateKeyJWT,
	}
	wantSecretsAndPrivateKeyJWT := append(
		append([]string(nil), wantSecrets...), sharedobauth.TokenEndpointAuthMethodPrivateKeyJWT,
	)
	wantAlgorithms := registration.SupportedSigningAlgorithms()

	tests := []struct {
		name              string
		allowConfidential bool
		allowPrivate      bool
		hasStaticClient   bool
		wantMethods       []string
		wantAlgorithms    []string
	}{
		{"public only advertises only none", false, false, false, wantOff, nil},
		{"private_key_jwt only advertises private_key_jwt", false, true, false, wantPrivateKeyJWT, wantAlgorithms},
		{"confidential DCR advertises client_secret methods", true, false, false, wantSecrets, nil},
		{"confidential DCR and private_key_jwt advertise both", true, true, false, wantSecretsAndPrivateKeyJWT, wantAlgorithms},
		{"static delegate advertises client_secret methods", false, false, true, wantSecrets, nil},
		{"static delegate and private_key_jwt advertise both", false, true, true, wantSecretsAndPrivateKeyJWT, wantAlgorithms},
		{"confidential DCR and static delegate advertise client_secret methods", true, false, true, wantSecrets, nil},
		{"all authentication methods are advertised", true, true, true, wantSecretsAndPrivateKeyJWT, wantAlgorithms},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler := testSetupWithOptions(t, testSetupOptions{
				AllowConfidentialClientRegistration: tc.allowConfidential,
				AllowPrivateKeyJWTRegistration:      tc.allowPrivate,
				HasStaticDelegateClients:            tc.hasStaticClient,
			})

			// OAuth AS metadata endpoint.
			rec := httptest.NewRecorder()
			handler.OAuthDiscoveryHandler(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			var metadata sharedobauth.AuthorizationServerMetadata
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&metadata))
			assert.Equal(t, tc.wantMethods, metadata.TokenEndpointAuthMethodsSupported,
				"oauth-authorization-server must advertise configured client authentication methods")
			assert.Equal(t, tc.wantAlgorithms, metadata.TokenEndpointAuthSigningAlgValuesSupported,
				"oauth-authorization-server must advertise private_key_jwt signing algorithms only when enabled")
			assert.Equal(t, sharedobauth.TokenEndpointAuthMethodNone, metadata.TokenEndpointAuthMethodsSupported[0],
				"none must remain at index 0")

			// OIDC discovery endpoint (shares buildOAuthMetadata).
			rec2 := httptest.NewRecorder()
			handler.OIDCDiscoveryHandler(rec2, httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil))
			require.Equal(t, http.StatusOK, rec2.Code)
			var discovery sharedobauth.OIDCDiscoveryDocument
			require.NoError(t, json.NewDecoder(rec2.Body).Decode(&discovery))
			assert.Equal(t, tc.wantMethods, discovery.TokenEndpointAuthMethodsSupported,
				"openid-configuration must advertise configured client authentication methods")
			assert.Equal(t, tc.wantAlgorithms, discovery.TokenEndpointAuthSigningAlgValuesSupported,
				"openid-configuration must advertise private_key_jwt signing algorithms only when enabled")
		})
	}
}

func TestOAuthDiscoveryHandler_WithAuthorizationEndpointBaseURL(t *testing.T) {
	t.Parallel()
	handler := testSetupWithOptions(t, testSetupOptions{
		AuthorizationEndpointBaseURL: "https://login.example.com",
	})

	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	rec := httptest.NewRecorder()

	handler.OAuthDiscoveryHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var metadata sharedobauth.AuthorizationServerMetadata
	err := json.NewDecoder(rec.Body).Decode(&metadata)
	require.NoError(t, err)

	// Authorization endpoint should use the override base URL
	assert.Equal(t, "https://login.example.com/oauth/authorize", metadata.AuthorizationEndpoint)

	// All other endpoints should still use the issuer
	assert.Equal(t, "https://auth.example.com", metadata.Issuer)
	assert.Equal(t, "https://auth.example.com/oauth/token", metadata.TokenEndpoint)
	assert.Equal(t, "https://auth.example.com/.well-known/jwks.json", metadata.JWKSURI)
	assert.Equal(t, "https://auth.example.com/oauth/register", metadata.RegistrationEndpoint)
}

func TestOIDCDiscoveryHandler_WithAuthorizationEndpointBaseURL(t *testing.T) {
	t.Parallel()
	handler := testSetupWithOptions(t, testSetupOptions{
		AuthorizationEndpointBaseURL: "https://login.example.com",
	})

	req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
	rec := httptest.NewRecorder()

	handler.OIDCDiscoveryHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var discovery sharedobauth.OIDCDiscoveryDocument
	err := json.NewDecoder(rec.Body).Decode(&discovery)
	require.NoError(t, err)

	// Authorization endpoint should use the override base URL
	assert.Equal(t, "https://login.example.com/oauth/authorize", discovery.AuthorizationEndpoint)

	// All other endpoints should still use the issuer
	assert.Equal(t, "https://auth.example.com", discovery.Issuer)
	assert.Equal(t, "https://auth.example.com/oauth/token", discovery.TokenEndpoint)
	assert.Equal(t, "https://auth.example.com/.well-known/jwks.json", discovery.JWKSURI)
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
		{http.MethodGet, "/.well-known/oauth-authorization-server"},
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

func TestDiscoveryHandlers_JWTBearerGrant(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		enabled bool
	}{
		{"enabled", true},
		{"disabled", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler := testSetupWithOptions(t, testSetupOptions{JWTBearerGrantEnabled: tc.enabled})

			for _, endpoint := range []struct {
				name string
				fn   func(http.ResponseWriter, *http.Request)
			}{
				{"OAuth AS metadata", handler.OAuthDiscoveryHandler},
				{"OIDC discovery", handler.OIDCDiscoveryHandler},
			} {
				t.Run(endpoint.name, func(t *testing.T) {
					t.Parallel()
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					rec := httptest.NewRecorder()
					endpoint.fn(rec, req)
					require.Equal(t, http.StatusOK, rec.Code)

					var meta sharedobauth.AuthorizationServerMetadata
					require.NoError(t, json.NewDecoder(rec.Body).Decode(&meta))
					if tc.enabled {
						assert.Contains(t, meta.GrantTypesSupported, sharedobauth.GrantTypeJWTBearer)
					} else {
						assert.NotContains(t, meta.GrantTypesSupported, sharedobauth.GrantTypeJWTBearer)
					}
				})
			}
		})
	}
}

// TODO: Add TestOAuthRoutes once OAuth handlers are implemented

func TestDiscoveryHandlers_CIMDEnabled_AdvertisesSupport(t *testing.T) {
	t.Parallel()

	handler := testSetupWithOptions(t, testSetupOptions{CIMDEnabled: true})

	for _, tc := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
	}{
		{"OAuth AS metadata", handler.OAuthDiscoveryHandler},
		{"OIDC discovery", handler.OIDCDiscoveryHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			tc.fn(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)

			var meta sharedobauth.AuthorizationServerMetadata
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&meta))
			assert.True(t, meta.ClientIDMetadataDocumentSupported,
				"client_id_metadata_document_supported must be true when CIMD is enabled")
		})
	}
}

func TestDiscoveryHandlers_CIMDDisabled_OmitsFlag(t *testing.T) {
	t.Parallel()

	handler := testSetupWithOptions(t, testSetupOptions{CIMDEnabled: false})

	for _, tc := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
	}{
		{"OAuth AS metadata", handler.OAuthDiscoveryHandler},
		{"OIDC discovery", handler.OIDCDiscoveryHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			tc.fn(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)

			// Decode to raw map to verify the field is truly absent from the JSON
			// output (omitempty), not merely false — a struct decode cannot
			// distinguish between an omitted field and an explicit false value.
			var raw map[string]interface{}
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&raw))
			_, present := raw["client_id_metadata_document_supported"]
			assert.False(t, present,
				"client_id_metadata_document_supported must be omitted from JSON when CIMD is disabled")
		})
	}
}

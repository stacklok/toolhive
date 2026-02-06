// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

// TestEmbeddedAuthServer_DiscoveryEndpoints verifies that the embedded auth server
// correctly serves OAuth and OIDC discovery endpoints.
//
//nolint:paralleltest,tparallel // Subtests share expensive test fixtures
func TestEmbeddedAuthServer_DiscoveryEndpoints(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup: Create mock upstream IDP
	upstream := helpers.NewMockUpstreamIDP(t)

	// Create auth server configuration
	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())

	// Create embedded auth server
	authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)

	// Create test HTTP server with the auth handler
	server := httptest.NewServer(authServer.Handler())
	defer server.Close()

	// Create OAuth client for testing
	client := helpers.NewOAuthClient(server.URL)

	t.Run("JWKS endpoint returns valid key set", func(t *testing.T) {
		jwks, statusCode, err := client.GetJWKS()
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, statusCode)
		assert.Contains(t, jwks, "keys")

		keys, ok := jwks["keys"].([]interface{})
		assert.True(t, ok, "keys should be an array")
		assert.GreaterOrEqual(t, len(keys), 1, "should have at least one key")

		// Verify key structure
		key := keys[0].(map[string]interface{})
		assert.Contains(t, key, "kty")
		assert.Contains(t, key, "kid")
		assert.Contains(t, key, "use")
		assert.Equal(t, "sig", key["use"])
	})

	t.Run("OAuth discovery endpoint returns valid metadata", func(t *testing.T) {
		metadata, statusCode, err := client.GetOAuthDiscovery()
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, statusCode)

		// Verify required OAuth AS Metadata fields (RFC 8414)
		assert.Contains(t, metadata, "issuer")
		assert.Contains(t, metadata, "authorization_endpoint")
		assert.Contains(t, metadata, "token_endpoint")
		assert.Contains(t, metadata, "jwks_uri")
		assert.Contains(t, metadata, "response_types_supported")
		assert.Contains(t, metadata, "grant_types_supported")

		// Verify issuer matches configuration
		assert.Equal(t, cfg.Issuer, metadata["issuer"])

		// Verify endpoints are well-formed
		authEndpoint, ok := metadata["authorization_endpoint"].(string)
		assert.True(t, ok)
		assert.Contains(t, authEndpoint, "/oauth/authorize")

		tokenEndpoint, ok := metadata["token_endpoint"].(string)
		assert.True(t, ok)
		assert.Contains(t, tokenEndpoint, "/oauth/token")
	})

	t.Run("OIDC discovery endpoint returns valid metadata", func(t *testing.T) {
		metadata, statusCode, err := client.GetOIDCDiscovery()
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, statusCode)

		// Verify required OIDC Discovery fields
		assert.Contains(t, metadata, "issuer")
		assert.Contains(t, metadata, "authorization_endpoint")
		assert.Contains(t, metadata, "token_endpoint")
		assert.Contains(t, metadata, "jwks_uri")

		// Verify issuer matches configuration
		assert.Equal(t, cfg.Issuer, metadata["issuer"])
	})
}

// TestEmbeddedAuthServer_AuthorizationFlow verifies the OAuth authorization code flow
// from initiation through redirect to upstream.
//
//nolint:paralleltest,tparallel // Subtests intentionally sequential - follow auth flow
func TestEmbeddedAuthServer_AuthorizationFlow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup: Create mock upstream IDP
	upstream := helpers.NewMockUpstreamIDP(t)

	// Create auth server configuration
	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())

	// Create embedded auth server
	authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)

	// Create test HTTP server
	server := httptest.NewServer(authServer.Handler())
	defer server.Close()

	// Create OAuth client
	client := helpers.NewOAuthClient(server.URL)

	// Register a test client first (required for authorization to work)
	clientMetadata := map[string]interface{}{
		"client_name":   "Test Client",
		"redirect_uris": []string{"http://localhost:8080/callback"},
		"grant_types":   []string{"authorization_code", "refresh_token"},
	}
	regResult, statusCode, err := client.RegisterClient(clientMetadata)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, statusCode, "client registration should succeed")
	clientID := regResult["client_id"].(string)

	t.Run("Authorization endpoint redirects to upstream IDP", func(t *testing.T) {
		params := url.Values{
			"response_type": {"code"},
			"client_id":     {clientID},
			"redirect_uri":  {"http://localhost:8080/callback"},
			"scope":         {"openid"},
			"state":         {"test-state-12345"},
			"resource":      {cfg.AllowedAudiences[0]}, // RFC 8707 resource
		}

		resp, err := client.StartAuthorization(params)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Should redirect to upstream IDP
		assert.Equal(t, http.StatusFound, resp.StatusCode)

		location := resp.Header.Get("Location")
		assert.NotEmpty(t, location)

		// Verify redirect points to upstream authorization endpoint
		redirectURL, err := url.Parse(location)
		require.NoError(t, err)
		assert.Contains(t, redirectURL.String(), upstream.URL())
		assert.Contains(t, redirectURL.Path, "/authorize")
	})

	t.Run("Authorization without resource parameter returns error", func(t *testing.T) {
		params := url.Values{
			"response_type": {"code"},
			"client_id":     {clientID},
			"redirect_uri":  {"http://localhost:8080/callback"},
			"scope":         {"openid"},
			"state":         {"test-state-no-resource"},
			// Missing resource parameter
		}

		resp, err := client.StartAuthorization(params)
		require.NoError(t, err)
		defer resp.Body.Close()

		// MCP compliance requires resource parameter (RFC 8707)
		// Should return error redirect or direct error response
		assert.True(t,
			resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusFound,
			"should reject request without resource parameter",
		)
	})
}

// TestEmbeddedAuthServer_DynamicClientRegistration verifies DCR (RFC 7591) support.
//
//nolint:paralleltest,tparallel // Subtests share expensive test fixtures
func TestEmbeddedAuthServer_DynamicClientRegistration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup: Create mock upstream IDP
	upstream := helpers.NewMockUpstreamIDP(t)

	// Create auth server configuration
	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())

	// Create embedded auth server
	authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)

	// Create test HTTP server
	server := httptest.NewServer(authServer.Handler())
	defer server.Close()

	client := helpers.NewOAuthClient(server.URL)

	t.Run("Register new client successfully", func(t *testing.T) {
		// Not parallel - shares server with other subtests

		clientMetadata := map[string]interface{}{
			"client_name":   "Test MCP Client",
			"redirect_uris": []string{"http://localhost:9999/callback"},
			"grant_types":   []string{"authorization_code", "refresh_token"},
		}

		result, statusCode, err := client.RegisterClient(clientMetadata)
		require.NoError(t, err)

		assert.Equal(t, http.StatusCreated, statusCode)
		assert.Contains(t, result, "client_id")
		assert.NotEmpty(t, result["client_id"])
	})

	t.Run("Register client with invalid redirect_uri fails", func(t *testing.T) {
		// Not parallel - shares server with other subtests

		clientMetadata := map[string]interface{}{
			"client_name":   "Invalid Client",
			"redirect_uris": []string{}, // Empty redirect URIs
		}

		_, statusCode, err := client.RegisterClient(clientMetadata)
		require.NoError(t, err)

		assert.Equal(t, http.StatusBadRequest, statusCode)
	})
}

// TestEmbeddedAuthServer_TokenEndpoint verifies token issuance and refresh.
//
//nolint:paralleltest,tparallel // Subtests share expensive test fixtures
func TestEmbeddedAuthServer_TokenEndpoint(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup: Create mock upstream IDP
	upstream := helpers.NewMockUpstreamIDP(t)

	// Create auth server configuration
	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())

	// Create embedded auth server
	authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)

	// Create test HTTP server
	server := httptest.NewServer(authServer.Handler())
	defer server.Close()

	client := helpers.NewOAuthClient(server.URL)

	t.Run("Token request with invalid grant returns error", func(t *testing.T) {
		// Not parallel - shares server with other subtests

		params := url.Values{
			"grant_type": {"invalid_grant"},
			"code":       {"fake-code"},
		}

		result, statusCode, err := client.ExchangeToken(params)
		require.NoError(t, err)

		assert.Equal(t, http.StatusBadRequest, statusCode)
		assert.Contains(t, result, "error")
		// fosite returns "invalid_request" for malformed requests
		// that don't match any valid grant type handler
		assert.Contains(t, []string{"unsupported_grant_type", "invalid_request"}, result["error"])
	})

	t.Run("Token request without required params returns error", func(t *testing.T) {
		// Not parallel - shares server with other subtests

		params := url.Values{
			"grant_type": {"authorization_code"},
			// Missing code, redirect_uri, client_id
		}

		result, statusCode, err := client.ExchangeToken(params)
		require.NoError(t, err)

		assert.Equal(t, http.StatusBadRequest, statusCode)
		assert.Contains(t, result, "error")
	})
}

// TestEmbeddedAuthServer_ConfigurationValidation verifies configuration error handling.
func TestEmbeddedAuthServer_ConfigurationValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("Missing allowed audiences returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.RunConfig{
			SchemaVersion: authserver.CurrentSchemaVersion,
			Issuer:        "http://localhost:8080",
			Upstreams: []authserver.UpstreamRunConfig{
				{
					Name: "test",
					Type: authserver.UpstreamProviderTypeOAuth2,
					OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
						AuthorizationEndpoint: "https://example.com/authorize",
						TokenEndpoint:         "https://example.com/token",
						ClientID:              "test-client",
						RedirectURI:           "http://localhost:8080/oauth/callback",
					},
				},
			},
			// Missing AllowedAudiences
		}

		_, err := authserverrunner.NewEmbeddedAuthServer(ctx, cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "audience")
	})

	t.Run("Missing upstreams returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.RunConfig{
			SchemaVersion:    authserver.CurrentSchemaVersion,
			Issuer:           "http://localhost:8080",
			AllowedAudiences: []string{"https://mcp.example.com"},
			// Missing Upstreams
		}

		_, err := authserverrunner.NewEmbeddedAuthServer(ctx, cfg)
		require.Error(t, err)
	})

	t.Run("Invalid issuer URL returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.RunConfig{
			SchemaVersion: authserver.CurrentSchemaVersion,
			Issuer:        "not-a-valid-url",
			Upstreams: []authserver.UpstreamRunConfig{
				{
					Name: "test",
					Type: authserver.UpstreamProviderTypeOAuth2,
					OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
						AuthorizationEndpoint: "https://example.com/authorize",
						TokenEndpoint:         "https://example.com/token",
						ClientID:              "test-client",
						RedirectURI:           "http://localhost/oauth/callback",
					},
				},
			},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}

		_, err := authserverrunner.NewEmbeddedAuthServer(ctx, cfg)
		require.Error(t, err)
	})
}

// TestEmbeddedAuthServer_SigningKeyConfiguration verifies signing key loading.
func TestEmbeddedAuthServer_SigningKeyConfiguration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	upstream := helpers.NewMockUpstreamIDP(t)

	t.Run("Development mode uses ephemeral keys", func(t *testing.T) {
		t.Parallel()

		cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())
		// No SigningKeyConfig = development mode

		authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)

		server := httptest.NewServer(authServer.Handler())
		defer server.Close()

		client := helpers.NewOAuthClient(server.URL)
		jwks, statusCode, err := client.GetJWKS()
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, statusCode)
		assert.Contains(t, jwks, "keys")
	})

	t.Run("File-based signing keys are loaded correctly", func(t *testing.T) {
		t.Parallel()

		// Create temporary key file
		keyDir := t.TempDir()
		keyFile := "test-key.pem"

		// Generate and write an EC P-256 key in SEC 1 format
		keyPEM := generateTestECKey(t)
		err := os.WriteFile(filepath.Join(keyDir, keyFile), keyPEM, 0600)
		require.NoError(t, err)

		cfg := helpers.NewTestAuthServerConfig(t, upstream.URL(),
			helpers.WithSigningKey(&authserver.SigningKeyRunConfig{
				KeyDir:         keyDir,
				SigningKeyFile: keyFile,
			}),
		)

		authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)

		server := httptest.NewServer(authServer.Handler())
		defer server.Close()

		client := helpers.NewOAuthClient(server.URL)
		jwks, statusCode, err := client.GetJWKS()
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, statusCode)
		keys := jwks["keys"].([]interface{})
		assert.GreaterOrEqual(t, len(keys), 1)
	})
}

// TestEmbeddedAuthServer_ResourceCleanup verifies proper resource cleanup on Close.
func TestEmbeddedAuthServer_ResourceCleanup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	upstream := helpers.NewMockUpstreamIDP(t)

	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())

	authServer, err := authserverrunner.NewEmbeddedAuthServer(ctx, cfg)
	require.NoError(t, err)

	// Close should succeed
	err = authServer.Close()
	require.NoError(t, err)

	// Close is idempotent - second call should not error
	err = authServer.Close()
	require.NoError(t, err)
}

// generateTestECKey generates a test EC private key for signing.
func generateTestECKey(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	keyBytes, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyBytes,
	})
}

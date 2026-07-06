// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
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

// TestEmbeddedAuthServer_CallbackCompletesAuthorization drives the full
// single-upstream authorization: authorize (redirect to the upstream) → callback
// (upstream code exchanged, tokens stored) → the chain completes at the sole
// upstream and the authorization code is issued back to the client. This
// exercises the callback + chain-completion path that the authorize-only test
// above does not reach.
func TestEmbeddedAuthServer_CallbackCompletesAuthorization(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	upstream := helpers.NewMockUpstreamIDP(t)
	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())
	authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)

	server := httptest.NewServer(authServer.Handler())
	defer server.Close()

	client := helpers.NewOAuthClient(server.URL)
	clientID := registerAuthCodeClient(t, client)
	challenge := pkceS256Challenge()

	// Leg 1: authorize → redirect to the upstream; capture the internal state the
	// server threaded to the upstream so we can drive the callback.
	authParams := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {testClientRedirectURI},
		"scope":                 {"openid"},
		"state":                 {"client-state-single"},
		"resource":              {cfg.AllowedAudiences[0]},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	resp, err := client.StartAuthorization(authParams)
	require.NoError(t, err)
	location := resp.Header.Get("Location")
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusFound, resp.StatusCode, "authorize should redirect to the upstream")
	internalState := stateParam(t, location)
	require.NotEmpty(t, internalState, "authorize must thread an internal state to the upstream")

	// Callback: with only one upstream in the chain, the server exchanges the code
	// and issues the authorization code back to the client.
	cbResp, err := client.Callback("mock-auth-code", internalState)
	require.NoError(t, err)
	defer cbResp.Body.Close()

	require.Equal(t, http.StatusSeeOther, cbResp.StatusCode,
		"single-upstream chain should complete and issue the authorization code")
	final := cbResp.Header.Get("Location")
	assert.Contains(t, final, testClientRedirectURI, "should redirect back to the client")
	assert.Contains(t, final, "code=", "should include an authorization code")
	assert.Contains(t, final, "state=client-state-single", "should preserve the client state")
	assert.NotContains(t, final, "error=", "should not be an error redirect")
}

// TestEmbeddedAuthServer_MultiUpstreamChain drives an authorization across two
// configured upstreams: the first callback continues the chain to the second
// upstream, and the second callback completes it and issues the authorization
// code. This exercises the end-to-end chain traversal — the effective chain is
// computed once, carried across legs, and walked to completion.
func TestEmbeddedAuthServer_MultiUpstreamChain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	upstreamA := helpers.NewMockUpstreamIDP(t)
	upstreamB := helpers.NewMockUpstreamIDP(t)
	cfg := helpers.NewTestAuthServerConfig(t, upstreamA.URL(),
		helpers.WithUpstreams([]authserver.UpstreamRunConfig{
			helpers.NewOAuth2Upstream("provider-a", upstreamA.URL()),
			helpers.NewOAuth2Upstream("provider-b", upstreamB.URL()),
		}),
	)
	authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)

	server := httptest.NewServer(authServer.Handler())
	defer server.Close()

	client := helpers.NewOAuthClient(server.URL)
	clientID := registerAuthCodeClient(t, client)
	challenge := pkceS256Challenge()

	authParams := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {testClientRedirectURI},
		"scope":                 {"openid"},
		"state":                 {"client-state-chain"},
		"resource":              {cfg.AllowedAudiences[0]},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}

	// Leg 1: authorize → redirect to the first upstream.
	resp, err := client.StartAuthorization(authParams)
	require.NoError(t, err)
	locA := resp.Header.Get("Location")
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Contains(t, locA, upstreamA.URL(), "first leg targets provider-a")
	stateA := stateParam(t, locA)
	require.NotEmpty(t, stateA)

	// Leg 1 callback → chain continues, redirect onward to the second upstream.
	cbA, err := client.Callback("code-a", stateA)
	require.NoError(t, err)
	locB := cbA.Header.Get("Location")
	require.NoError(t, cbA.Body.Close())
	require.Equal(t, http.StatusFound, cbA.StatusCode, "chain should continue to the second upstream")
	assert.Contains(t, locB, upstreamB.URL(), "second leg targets provider-b")
	stateB := stateParam(t, locB)
	require.NotEmpty(t, stateB)
	require.NotEqual(t, stateA, stateB, "each leg uses a fresh internal state")

	// Leg 2 callback → chain complete, authorization code issued to the client.
	cbB, err := client.Callback("code-b", stateB)
	require.NoError(t, err)
	defer cbB.Body.Close()

	require.Equal(t, http.StatusSeeOther, cbB.StatusCode, "completed chain should issue the code")
	final := cbB.Header.Get("Location")
	assert.Contains(t, final, testClientRedirectURI, "should redirect back to the client")
	assert.Contains(t, final, "code=", "should include an authorization code")
	assert.Contains(t, final, "state=client-state-chain", "should preserve the client state")
	assert.NotContains(t, final, "error=", "should not be an error redirect")
}

// testClientRedirectURI is the client callback used by the authorization-flow tests.
const testClientRedirectURI = "http://localhost:8080/callback"

// registerAuthCodeClient performs DCR for an authorization-code client and returns
// its client_id.
func registerAuthCodeClient(t *testing.T, client *helpers.OAuthClient) string {
	t.Helper()
	regResult, statusCode, err := client.RegisterClient(map[string]interface{}{
		"client_name":   "Integration Test Client",
		"redirect_uris": []string{testClientRedirectURI},
		"grant_types":   []string{"authorization_code", "refresh_token"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, statusCode, "client registration should succeed")
	clientID, ok := regResult["client_id"].(string)
	require.True(t, ok, "registration response must include a client_id")
	return clientID
}

// stateParam extracts the `state` query parameter from a redirect Location.
func stateParam(t *testing.T, location string) string {
	t.Helper()
	require.NotEmpty(t, location, "redirect must include a Location header")
	u, err := url.Parse(location)
	require.NoError(t, err)
	return u.Query().Get("state")
}

// pkceS256Challenge returns a valid S256 code challenge for a fixed verifier. The
// verifier itself is unused because these tests assert the authorization-code
// redirect rather than completing the token exchange.
func pkceS256Challenge() string {
	const verifier = "integration-test-pkce-verifier-0123456789abcdef"
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
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

// TestEmbeddedAuthServer_BaselineClientScopes_RegressionForDCRScopeNarrowing
// is a regression test for the Claude Code DCR scope-narrowing bug
// (anthropics/claude-code#4540). Claude Code registers with a narrowed scope
// (e.g. "openid") but later requests the full set at /oauth/authorize.
// The fix unions BaselineClientScopes into every DCR registration so the
// client's registered set always includes the operator-configured baseline,
// preventing fosite from rejecting the subsequent authorize request with
// invalid_scope.
//
//nolint:paralleltest,tparallel // Subtests intentionally sequential - second reuses first's client_id
func TestEmbeddedAuthServer_BaselineClientScopes_RegressionForDCRScopeNarrowing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	upstream := helpers.NewMockUpstreamIDP(t)

	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL(),
		helpers.WithScopesSupported([]string{"openid", "offline_access"}),
		helpers.WithBaselineClientScopes([]string{"offline_access"}),
	)

	authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)

	server := httptest.NewServer(authServer.Handler())
	t.Cleanup(server.Close)

	client := helpers.NewOAuthClient(server.URL)

	var clientID string

	t.Run("DCR response echoes the baseline-augmented scope set", func(t *testing.T) {
		clientMetadata := map[string]interface{}{
			"client_name":   "Claude Code",
			"redirect_uris": []string{"http://localhost:8080/callback"},
			"grant_types":   []string{"authorization_code", "refresh_token"},
			// Narrow scope — the bug-trigger pattern from Claude Code
			"scope": "openid",
		}

		result, statusCode, err := client.RegisterClient(clientMetadata)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, statusCode, "DCR registration should succeed")

		clientID = result["client_id"].(string)
		require.NotEmpty(t, clientID)

		// The registered scope set must include "offline_access" from the baseline
		// even though the client only requested "openid".
		registeredScope, ok := result["scope"].(string)
		require.True(t, ok, "scope field should be a string in DCR response")
		// Order: requested scopes first, then non-overlapping baseline (unionScopes contract).
		assert.Equal(t, "openid offline_access", registeredScope,
			"DCR response scope must be the union of requested+baseline scopes")
	})

	t.Run("authorize accepts a request for the unioned scope set", func(t *testing.T) {
		// Pre-fix: fosite would reject this with invalid_scope because the
		// registered client only had "openid" in its scope set. Post-fix: the
		// client has "openid offline_access" so the authorize request succeeds.
		params := url.Values{
			"response_type": {"code"},
			"client_id":     {clientID},
			"redirect_uri":  {"http://localhost:8080/callback"},
			"scope":         {"openid offline_access"},
			"state":         {"test-state-baseline-regression"},
			"resource":      {cfg.AllowedAudiences[0]},
		}

		resp, err := client.StartAuthorization(params)
		require.NoError(t, err)
		defer func() {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()

		// Must redirect to upstream — NOT a 400 invalid_scope.
		assert.Equal(t, http.StatusFound, resp.StatusCode,
			"authorize must accept the full scope set that includes the baseline; pre-fix this returned 400 invalid_scope")

		location := resp.Header.Get("Location")
		assert.NotEmpty(t, location)

		redirectURL, err := url.Parse(location)
		require.NoError(t, err)
		assert.Contains(t, redirectURL.String(), upstream.URL())
	})

	t.Run("authorize rejects a scope not in scopes_supported", func(t *testing.T) {
		// Negative case: even with the baseline expansion, scopes that aren't
		// in ScopesSupported must still be rejected. This guards against silent
		// privilege escalation if BaselineClientScopes ever drifts.
		params := url.Values{
			"response_type": {"code"},
			"client_id":     {clientID},
			"redirect_uri":  {"http://localhost:8080/callback"},
			"scope":         {"openid offline_access admin:read"},
			"state":         {"test-state-baseline-negative"},
			"resource":      {cfg.AllowedAudiences[0]},
		}

		resp, err := client.StartAuthorization(params)
		require.NoError(t, err)
		// Read the body up front: the 400 branch below needs to parse it,
		// and reading once is simpler than teeing around a deferred drain.
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())

		// Fosite rejects with invalid_scope. Depending on whether the redirect URI
		// has been validated by that point, this surfaces either as a 400 with
		// a JSON body or as a redirect (3xx) to redirect_uri with
		// error=invalid_scope in the query. Both must carry `invalid_scope` —
		// asserting on the error code in both branches guards against a future
		// fosite upgrade swapping in a different code (e.g. server_error)
		// while the privilege-escalation regression silently slips through.
		if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
			// Redirect-with-error case — verify it points to the registered
			// redirect_uri (loopback), NOT the upstream.
			location := resp.Header.Get("Location")
			require.NotEmpty(t, location)
			assert.NotContains(t, location, upstream.URL(),
				"rejected request must NOT redirect to the upstream IDP")
			redirectURL, err := url.Parse(location)
			require.NoError(t, err)
			assert.Contains(t, redirectURL.RawQuery, "invalid_scope",
				"rejection error must be invalid_scope")
		} else {
			require.Equal(t, http.StatusBadRequest, resp.StatusCode,
				"unsupported scope must produce 400 invalid_scope (or redirect-with-error)")
			var errResp struct {
				Error string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(body, &errResp),
				"400 response body must be JSON with an error field")
			assert.Equal(t, "invalid_scope", errResp.Error,
				"400 rejection must carry the invalid_scope error code, not a different one")
		}
	})
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

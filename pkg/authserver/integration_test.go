// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/oauth2-proxy/mockoidc"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

const (
	testClientID    = "test-client"
	testRedirectURI = "http://localhost:8080/callback"
	testIssuer      = "http://localhost"
	testAudience    = "https://mcp.example.com"

	// testAccessTokenLifetime is the configured access token lifetime in setupTestServer.
	testAccessTokenLifetime = time.Hour
)

// testServer bundles all test server components together.
type testServer struct {
	Server     *httptest.Server
	PrivateKey *rsa.PrivateKey
}

// testServerOptions configures the test server setup.
type testServerOptions struct {
	upstream upstream.OAuth2Provider
	scopes   []string
}

// testServerOption is a functional option for test server setup.
type testServerOption func(*testServerOptions)

// withUpstream configures the test server to use an upstream OAuth2 provider.
func withUpstream(provider upstream.OAuth2Provider) testServerOption {
	return func(opts *testServerOptions) {
		opts.upstream = provider
	}
}

// withScopes configures the scopes available to the test client.
func withScopes(scopes []string) testServerOption {
	return func(opts *testServerOptions) {
		opts.scopes = scopes
	}
}

// testKeyProvider is a simple KeyProvider for tests that uses a pre-generated RSA key.
type testKeyProvider struct {
	key *rsa.PrivateKey
}

func (p *testKeyProvider) SigningKey(_ context.Context) (*keys.SigningKeyData, error) {
	return &keys.SigningKeyData{
		KeyID:     "test-key",
		Algorithm: "RS256",
		Key:       p.key,
		CreatedAt: time.Now(),
	}, nil
}

func (p *testKeyProvider) PublicKeys(_ context.Context) ([]*keys.PublicKeyData, error) {
	return []*keys.PublicKeyData{{
		KeyID:     "test-key",
		Algorithm: "RS256",
		PublicKey: p.key.Public(),
		CreatedAt: time.Now(),
	}}, nil
}

// setupTestServer creates a full test server using newServer with fosite provider configured
// for authorization code flow with PKCE. Options allow configuring upstream provider.
func setupTestServer(t *testing.T, opts ...testServerOption) *testServer {
	t.Helper()
	ctx := context.Background()

	// Apply options
	options := &testServerOptions{
		scopes: []string{"openid", "profile", "offline_access"},
	}
	for _, opt := range opts {
		opt(options)
	}

	// 1. Generate RSA key for signing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// 2. Generate HMAC secret
	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	require.NoError(t, err)

	// 3. Create storage
	stor := storage.NewMemoryStorage()

	// 4. Register test client (public client for PKCE)
	err = stor.RegisterClient(ctx, &fosite.DefaultClient{
		ID:            testClientID,
		Secret:        nil, // public client
		RedirectURIs:  []string{testRedirectURI},
		ResponseTypes: []string{"code"},
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		Scopes:        options.scopes,
		Public:        true,
	})
	require.NoError(t, err)

	// 5. Build upstream config for newServer
	// When no upstream is provided, use a dummy config that satisfies validation
	// Note: Uses HTTPS to pass config validation
	upstreamCfg := &upstream.OAuth2Config{
		CommonOAuthConfig: upstream.CommonOAuthConfig{
			ClientID:    "test-upstream-client",
			RedirectURI: "https://example.com/oauth/callback",
		},
		AuthorizationEndpoint: "https://idp.example.com/auth",
		TokenEndpoint:         "https://idp.example.com/token",
	}

	// 6. Create config using testKeyProvider
	cfg := Config{
		Issuer:               testIssuer,
		KeyProvider:          &testKeyProvider{key: privateKey},
		HMACSecrets:          servercrypto.NewHMACSecrets(secret),
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: 24 * time.Hour,
		AuthCodeLifespan:     10 * time.Minute,
		Upstreams:            []UpstreamConfig{{Name: "default", Config: upstreamCfg}},
		AllowedAudiences:     []string{"https://mcp.example.com"},
	}

	// 7. Create server using newServer with test options
	srv, err := newServer(ctx, cfg, stor,
		withUpstreamFactory(func(_ *upstream.OAuth2Config) (upstream.OAuth2Provider, error) {
			// Return the provided upstream or nil (which is valid for tests without upstream)
			return options.upstream, nil
		}),
	)
	require.NoError(t, err)

	// 8. Create HTTP test server
	httpServer := httptest.NewServer(srv.Handler())

	t.Cleanup(func() {
		httpServer.Close()
		require.NoError(t, srv.Close())
	})

	return &testServer{
		Server:     httpServer,
		PrivateKey: privateKey,
	}
}

// parseTokenResponse parses a token endpoint response.
func parseTokenResponse(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	require.NoError(t, err, "failed to parse response: %s", string(body))

	return result
}

// makeTokenRequest makes a POST request to the token endpoint.
func makeTokenRequest(t *testing.T, serverURL string, params url.Values) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, serverURL+"/oauth/token", strings.NewReader(params.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	require.NoError(t, err)

	return resp
}

// ============================================================================
// Token Endpoint Error Handling Tests
// ============================================================================

// TestIntegration_TokenEndpoint_Errors tests various error conditions at the token endpoint.
func TestIntegration_TokenEndpoint_Errors(t *testing.T) {
	t.Parallel()

	// Setup: Start mock IDP and auth server once for all subtests
	m := startMockOIDC(t)

	cases := []struct {
		name           string
		useRealCode    bool                     // whether to get a real auth code via full flow
		modifyParams   func(url.Values, string) // modify params; receives auth code if useRealCode=true
		expectedStatus int                      // expected HTTP status code per RFC 6749 Section 5.2
		expectedErrors []string                 // acceptable OAuth error codes (any match passes)
	}{
		{
			name:           "invalid_pkce_verifier",
			useRealCode:    true,
			expectedStatus: http.StatusBadRequest,
			expectedErrors: []string{"invalid_grant"},
			modifyParams: func(p url.Values, _ string) {
				p.Set("code_verifier", "wrong-verifier-that-wont-match-the-challenge")
			},
		},
		{
			name:           "invalid_code",
			useRealCode:    false,
			expectedStatus: http.StatusBadRequest,
			expectedErrors: []string{"invalid_grant"},
			modifyParams: func(p url.Values, _ string) {
				p.Set("code", "non-existent-auth-code")
			},
		},
		{
			name:           "missing_redirect_uri",
			useRealCode:    true,
			expectedStatus: http.StatusBadRequest,
			expectedErrors: []string{"invalid_grant"},
			modifyParams: func(p url.Values, _ string) {
				p.Del("redirect_uri")
			},
		},
		{
			name:           "wrong_client_id",
			useRealCode:    true,
			expectedStatus: http.StatusUnauthorized,
			expectedErrors: []string{"invalid_client"},
			modifyParams: func(p url.Values, _ string) {
				p.Set("client_id", "wrong-client-id")
			},
		},
		{
			name:           "missing_pkce_verifier",
			useRealCode:    true,
			expectedStatus: http.StatusBadRequest,
			// fosite may return either depending on validation order
			expectedErrors: []string{"invalid_request", "invalid_grant"},
			modifyParams: func(p url.Values, _ string) {
				p.Del("code_verifier")
			},
		},
		{
			name:           "mismatched_redirect_uri",
			useRealCode:    true,
			expectedStatus: http.StatusBadRequest,
			expectedErrors: []string{"invalid_grant"},
			modifyParams: func(p url.Values, _ string) {
				p.Set("redirect_uri", "http://evil.example.com/callback")
			},
		},
		{
			name:           "grant_type_confusion",
			useRealCode:    true,
			expectedStatus: http.StatusBadRequest,
			expectedErrors: []string{"invalid_grant", "invalid_request"},
			modifyParams: func(p url.Values, _ string) {
				// Try to use an auth code as a refresh token
				code := p.Get("code")
				p.Set("grant_type", "refresh_token")
				p.Set("refresh_token", code)
				p.Del("code")
				p.Del("code_verifier")
				p.Del("redirect_uri")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Queue a mock user for the upstream IDP. Note: since subtests run in parallel,
			// the FIFO pop order is nondeterministic across subtests. This is acceptable
			// because these tests only verify error responses — user identity is irrelevant.
			m.QueueUser(&mockoidc.MockUser{
				Subject: "mock-user-" + tc.name,
				Email:   tc.name + "@example.com",
			})

			ts := setupTestServerWithMockOIDC(t, m)
			verifier := servercrypto.GeneratePKCEVerifier()
			challenge := servercrypto.ComputePKCEChallenge(verifier)

			var authCode string
			if tc.useRealCode {
				authCode, _ = completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
					ClientID:     testClientID,
					RedirectURI:  testRedirectURI,
					State:        "test-state",
					Challenge:    challenge,
					Scope:        "openid profile",
					ResponseType: "code",
				})
			} else {
				authCode = "placeholder"
			}

			params := url.Values{
				"grant_type":    {"authorization_code"},
				"code":          {authCode},
				"client_id":     {testClientID},
				"redirect_uri":  {testRedirectURI},
				"code_verifier": {verifier},
			}
			tc.modifyParams(params, authCode)

			resp := makeTokenRequest(t, ts.Server.URL, params)
			defer resp.Body.Close()

			require.Equal(t, tc.expectedStatus, resp.StatusCode, "unexpected HTTP status code")

			errResp := parseTokenResponse(t, resp)
			errorField, ok := errResp["error"].(string)
			require.True(t, ok, "error should be a string")
			assert.Contains(t, tc.expectedErrors, errorField,
				"expected one of %v, got %q", tc.expectedErrors, errorField)
		})
	}
}

// TestIntegration_TokenEndpoint_ReplayAttack tests that auth codes cannot be reused.
func TestIntegration_TokenEndpoint_ReplayAttack(t *testing.T) {
	t.Parallel()

	m := startMockOIDC(t)
	ts := setupTestServerWithMockOIDC(t, m)

	verifier := servercrypto.GeneratePKCEVerifier()
	challenge := servercrypto.ComputePKCEChallenge(verifier)

	// Get a real auth code via the full flow
	authCode, _ := completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
		ClientID:     testClientID,
		RedirectURI:  testRedirectURI,
		State:        "replay-test-state",
		Challenge:    challenge,
		Scope:        "openid profile",
		ResponseType: "code",
	})

	// First request - should succeed
	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authCode},
		"client_id":     {testClientID},
		"redirect_uri":  {testRedirectURI},
		"code_verifier": {verifier},
	}

	resp1 := makeTokenRequest(t, ts.Server.URL, params)
	defer resp1.Body.Close()
	require.Equal(t, http.StatusOK, resp1.StatusCode, "first request should succeed")
	resp1Body := parseTokenResponse(t, resp1)
	assert.NotEmpty(t, resp1Body["access_token"], "first request should return access token")

	// Second request with same code - should fail (replay attack)
	resp2 := makeTokenRequest(t, ts.Server.URL, params)
	defer resp2.Body.Close()

	require.GreaterOrEqual(t, resp2.StatusCode, 400, "second request should fail (replay attack)")

	errResp := parseTokenResponse(t, resp2)
	errorField, ok := errResp["error"].(string)
	assert.True(t, ok, "error should be a string")
	assert.NotEmpty(t, errorField, "error should not be empty")
}

// TestIntegration_TokenEndpoint_RefreshToken tests that refresh tokens can be used to get new access tokens.
func TestIntegration_TokenEndpoint_RefreshToken(t *testing.T) {
	t.Parallel()

	m := startMockOIDC(t)
	ts := setupTestServerWithMockOIDC(t, m)

	verifier := servercrypto.GeneratePKCEVerifier()
	challenge := servercrypto.ComputePKCEChallenge(verifier)

	// Get auth code with offline_access scope to receive a refresh token
	authCode, _ := completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
		ClientID:     testClientID,
		RedirectURI:  testRedirectURI,
		State:        "refresh-test-state",
		Challenge:    challenge,
		Scope:        "openid profile offline_access",
		ResponseType: "code",
	})

	// Exchange code for tokens
	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authCode},
		"client_id":     {testClientID},
		"redirect_uri":  {testRedirectURI},
		"code_verifier": {verifier},
	}

	resp := makeTokenRequest(t, ts.Server.URL, params)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "initial token request should succeed")
	tokenResp := parseTokenResponse(t, resp)

	// Verify refresh token was returned
	refreshToken, hasRefresh := tokenResp["refresh_token"].(string)
	require.True(t, hasRefresh, "response should contain refresh_token field")
	require.NotEmpty(t, refreshToken, "refresh_token should not be empty")

	// Use the refresh token to get a new access token
	refreshParams := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {testClientID},
	}

	refreshResp := makeTokenRequest(t, ts.Server.URL, refreshParams)
	defer refreshResp.Body.Close()
	require.Equal(t, http.StatusOK, refreshResp.StatusCode, "refresh token request should succeed")
	refreshTokenResp := parseTokenResponse(t, refreshResp)

	// Verify we got a new access token
	newAccessToken, ok := refreshTokenResp["access_token"].(string)
	require.True(t, ok, "access_token should be a string")
	assert.NotEmpty(t, newAccessToken, "new access_token should not be empty")

	tokenType, ok := refreshTokenResp["token_type"].(string)
	require.True(t, ok, "token_type should be a string")
	assert.Equal(t, "bearer", strings.ToLower(tokenType))

	// Verify expires_in is present and reasonable (RFC 6749 Section 5.1)
	expiresIn, ok := refreshTokenResp["expires_in"].(float64)
	require.True(t, ok, "expires_in should be a number")
	assert.Greater(t, expiresIn, float64(0), "expires_in should be positive")

	// Verify new access token is different from original
	originalAccessToken := tokenResp["access_token"].(string)
	assert.NotEqual(t, originalAccessToken, newAccessToken, "refreshed access token should differ from original")

	// Verify refresh token rotation: a new refresh token should be issued
	newRefreshToken, ok := refreshTokenResp["refresh_token"].(string)
	require.True(t, ok, "refresh response should contain a new refresh_token")
	assert.NotEqual(t, refreshToken, newRefreshToken, "token rotation must issue new refresh token")

	// Verify old refresh token is rejected after rotation
	replayParams := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {testClientID},
	}
	replayResp := makeTokenRequest(t, ts.Server.URL, replayParams)
	defer replayResp.Body.Close()
	require.GreaterOrEqual(t, replayResp.StatusCode, 400, "old refresh token must be rejected after rotation")
}

// ============================================================================
// Full PKCE Flow Integration Tests with Mock Upstream IDP (using mockoidc)
// ============================================================================

// testServerWithUpstream bundles test server components with upstream IDP.
type testServerWithUpstream struct {
	*testServer
	mockOIDC         *mockoidc.MockOIDC
	upstreamProvider upstream.OAuth2Provider
}

// startMockOIDC starts a mockoidc server with default test user.
func startMockOIDC(t *testing.T) *mockoidc.MockOIDC {
	t.Helper()

	m, err := mockoidc.Run()
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, m.Shutdown())
	})

	// Queue default test user
	m.QueueUser(&mockoidc.MockUser{
		Subject: "mock-user-sub-123",
		Email:   "testuser@example.com",
	})

	return m
}

// setupTestServerWithMockOIDC creates a test server with mockoidc as upstream.
func setupTestServerWithMockOIDC(t *testing.T, m *mockoidc.MockOIDC) *testServerWithUpstream {
	t.Helper()

	cfg := m.Config()

	upstreamCfg := &upstream.OAuth2Config{
		CommonOAuthConfig: upstream.CommonOAuthConfig{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Scopes:       []string{"openid", "profile", "email"},
			RedirectURI:  testIssuer + "/oauth/callback",
		},
		AuthorizationEndpoint: m.AuthorizationEndpoint(),
		TokenEndpoint:         m.TokenEndpoint(),
		UserInfo: &upstream.UserInfoConfig{
			EndpointURL: m.UserinfoEndpoint(),
			// mockoidc's userinfo endpoint only returns {"email":"..."}, not "sub"
			// Configure field mapping to use email as the subject identifier
			FieldMapping: &upstream.UserInfoFieldMapping{
				SubjectFields: []string{"sub", "email"},
			},
		},
	}
	upstreamIDP, err := upstream.NewOAuth2Provider(upstreamCfg)
	require.NoError(t, err)

	ts := setupTestServer(t,
		withUpstream(upstreamIDP),
		withScopes([]string{"openid", "profile", "email", "offline_access"}),
	)

	return &testServerWithUpstream{
		testServer:       ts,
		mockOIDC:         m,
		upstreamProvider: upstreamIDP,
	}
}

// noRedirectClient returns an HTTP client that does not follow redirects.
func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// authorizationParams contains parameters for initiating an authorization request.
type authorizationParams struct {
	ClientID     string
	RedirectURI  string
	State        string
	Challenge    string
	Scope        string
	ResponseType string
}

// completeAuthorizationFlow performs the full OAuth authorization flow through mockoidc
// and returns the authorization code and state returned by our auth server.
//
// The flow is: Client → Our /authorize → mockoidc → Our /callback → Client redirect
//
// We manually step through redirects to handle the fact that mockoidc's redirect
// points to "localhost" (from the config) but our test server runs on a random port.
func completeAuthorizationFlow(
	t *testing.T,
	serverURL string,
	params authorizationParams,
) (code string, state string) {
	t.Helper()
	client := noRedirectClient()

	// Step 1: Start authorization flow on our server
	authorizeURL := serverURL + "/oauth/authorize?" + url.Values{
		"client_id":             {params.ClientID},
		"redirect_uri":          {params.RedirectURI},
		"state":                 {params.State},
		"code_challenge":        {params.Challenge},
		"code_challenge_method": {"S256"},
		"response_type":         {params.ResponseType},
		"scope":                 {params.Scope},
	}.Encode()

	resp, err := client.Get(authorizeURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode, "expected redirect to mockoidc")
	mockOIDCLocation, err := resp.Location()
	require.NoError(t, err)
	resp.Body.Close()

	// Step 2: Follow redirect to mockoidc authorization endpoint
	resp, err = client.Get(mockOIDCLocation.String())
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode, "expected redirect from mockoidc to callback")
	callbackLocation, err := resp.Location()
	require.NoError(t, err)
	resp.Body.Close()

	// Step 3: Rewrite callback URL to use actual test server
	// mockoidc redirects to http://localhost/oauth/callback, but our server is at serverURL
	parsedServerURL, err := url.Parse(serverURL)
	require.NoError(t, err)
	callbackLocation.Scheme = parsedServerURL.Scheme
	callbackLocation.Host = parsedServerURL.Host

	// Step 4: Call our callback endpoint with the rewritten URL
	resp, err = client.Get(callbackLocation.String())
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "expected redirect to client")
	clientLocation, err := resp.Location()
	require.NoError(t, err)
	resp.Body.Close()

	// Step 5: Extract the authorization code and state
	code = clientLocation.Query().Get("code")
	require.NotEmpty(t, code, "authorization code should be present")
	state = clientLocation.Query().Get("state")

	return code, state
}

// exchangeCodeForTokens exchanges an authorization code for tokens and validates the response.
// The resource parameter (RFC 8707) specifies the intended audience for the token.
func exchangeCodeForTokens(
	t *testing.T,
	serverURL string,
	code string,
	verifier string,
	resource string,
) map[string]interface{} {
	t.Helper()

	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {testRedirectURI},
		"client_id":     {testClientID},
		"code_verifier": {verifier},
	}
	if resource != "" {
		params.Set("resource", resource)
	}

	tokenResp := makeTokenRequest(t, serverURL, params)
	defer tokenResp.Body.Close()

	tokenData := parseTokenResponse(t, tokenResp)
	require.Equal(t, http.StatusOK, tokenResp.StatusCode, "token request should succeed")

	return tokenData
}

// TestIntegration_FullPKCEFlow tests the complete OAuth flow:
// Client -> Auth Server -> Upstream IDP -> Auth Server -> Client -> Token Exchange
func TestIntegration_FullPKCEFlow(t *testing.T) {
	t.Parallel()

	// Setup: Start mock IDP and auth server
	m := startMockOIDC(t)
	ts := setupTestServerWithMockOIDC(t, m)
	verifier := servercrypto.GeneratePKCEVerifier()
	challenge := servercrypto.ComputePKCEChallenge(verifier)
	clientState := "client-state-123"
	requestedScopes := []string{"openid", "profile", "offline_access"}

	// Complete authorization flow through mockoidc (follows redirects)
	// Request offline_access to get a refresh token
	authCode, returnedState := completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
		ClientID:     testClientID,
		RedirectURI:  testRedirectURI,
		State:        clientState,
		Challenge:    challenge,
		Scope:        strings.Join(requestedScopes, " "),
		ResponseType: "code",
	})

	// Verify client state was preserved through the flow
	assert.Equal(t, clientState, returnedState, "client state should be preserved through authorization flow")

	// Exchange code for tokens with resource parameter (RFC 8707) for audience binding
	tokenData := exchangeCodeForTokens(t, ts.Server.URL, authCode, verifier, testAudience)

	// Verify token response structure
	accessToken, ok := tokenData["access_token"].(string)
	require.True(t, ok, "access_token should be a string")
	require.NotEmpty(t, accessToken, "access_token should not be empty")

	tokenType, ok := tokenData["token_type"].(string)
	require.True(t, ok, "token_type should be a string")
	assert.Equal(t, "bearer", strings.ToLower(tokenType), "token type should be Bearer")

	// Verify refresh token is returned when offline_access scope is requested
	refreshToken, ok := tokenData["refresh_token"].(string)
	require.True(t, ok, "refresh_token should be a string when offline_access is requested")
	require.NotEmpty(t, refreshToken, "refresh_token should not be empty")

	// Verify expires_in matches configured token lifetime
	expiresIn, ok := tokenData["expires_in"].(float64)
	require.True(t, ok, "expires_in should be a number")
	assert.InDelta(t, testAccessTokenLifetime.Seconds(), expiresIn, 5, "expires_in should match configured lifetime")

	// Verify JWT signature and parse claims
	parsedToken, err := jwt.ParseSigned(accessToken, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err, "should be able to parse JWT")

	var claims map[string]interface{}
	err = parsedToken.Claims(ts.PrivateKey.Public(), &claims)
	require.NoError(t, err, "JWT signature should be valid")

	// Verify issuer and client
	assert.Equal(t, testIssuer, claims["iss"], "issuer should match")
	assert.Equal(t, testClientID, claims["client_id"], "client_id should match")

	// Verify audience from resource parameter (RFC 8707)
	aud, ok := claims["aud"].([]interface{})
	require.True(t, ok, "aud claim should be an array")
	require.Len(t, aud, 1, "aud should have exactly one audience")
	assert.Equal(t, testAudience, aud[0], "audience should match requested resource")

	// Verify subject is present (from upstream IDP)
	sub, ok := claims["sub"].(string)
	require.True(t, ok, "sub claim should be a string")
	assert.NotEmpty(t, sub, "sub claim should not be empty")

	// Verify timestamps are reasonable
	now := time.Now().Unix()

	iat, ok := claims["iat"].(float64)
	require.True(t, ok, "iat claim should be a number")
	assert.LessOrEqual(t, int64(iat), now+5, "iat should not be in the future (with 5s tolerance)")
	assert.GreaterOrEqual(t, int64(iat), now-60, "iat should not be more than 60s in the past")

	exp, ok := claims["exp"].(float64)
	require.True(t, ok, "exp claim should be a number")
	expectedExp := iat + testAccessTokenLifetime.Seconds()
	assert.InDelta(t, expectedExp, exp, 2, "exp should be iat + configured token lifetime (within 2s tolerance)")

	// Verify scope claim matches requested scopes
	scope, ok := claims["scp"].([]interface{})
	require.True(t, ok, "scp claim should be an array")
	scopeStrings := make([]string, len(scope))
	for i, s := range scope {
		scopeStr, ok := s.(string)
		require.True(t, ok, "each scope should be a string, got %T at index %d", s, i)
		scopeStrings[i] = scopeStr
	}
	assert.ElementsMatch(t, requestedScopes, scopeStrings, "granted scopes should match requested scopes")
}

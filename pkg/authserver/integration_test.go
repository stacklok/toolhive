package authserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
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
	"github.com/ory/fosite/compose"
	fositehandler "github.com/ory/fosite/handler/oauth2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authserver "github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	sharedobauth "github.com/stacklok/toolhive/pkg/oauth"
)

const (
	testClientID    = "test-client"
	testRedirectURI = "http://localhost:8080/callback"
	testIssuer      = "http://localhost"
	testSubject     = "test-user"
)

// testServer bundles all test server components together.
type testServer struct {
	Server       *httptest.Server
	Storage      *storage.MemoryStorage
	ServerConfig *authserver.AuthorizationServerConfig
	PrivateKey   *rsa.PrivateKey
	Strategy     fositehandler.CoreStrategy
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

// setupTestServer creates a full test server with fosite provider configured
// for authorization code flow with PKCE. Options allow configuring upstream provider.
func setupTestServer(t *testing.T, opts ...testServerOption) *testServer {
	t.Helper()

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

	// 3. Create AuthorizationServerConfig
	serverConfig, err := authserver.NewAuthorizationServerConfig(&authserver.AuthorizationServerParams{
		Issuer:               testIssuer,
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: 24 * time.Hour,
		AuthCodeLifespan:     10 * time.Minute,
		HMACSecrets:          servercrypto.NewHMACSecrets(secret),
		SigningKeyID:         "test-key",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           privateKey,
	})
	require.NoError(t, err)

	// 4. Create storage
	stor := storage.NewMemoryStorage()

	// 5. Register test client (public client for PKCE)
	err = stor.RegisterClient(context.Background(), &fosite.DefaultClient{
		ID:            testClientID,
		Secret:        nil, // public client
		RedirectURIs:  []string{testRedirectURI},
		ResponseTypes: []string{"code"},
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		Scopes:        options.scopes,
		Public:        true,
	})
	require.NoError(t, err)

	// 6. Create fosite provider using compose.Compose()
	jwtStrategy := compose.NewOAuth2JWTStrategy(
		func(_ context.Context) (interface{}, error) {
			return privateKey, nil
		},
		compose.NewOAuth2HMACStrategy(serverConfig.Config),
		serverConfig.Config,
	)

	provider := compose.Compose(
		serverConfig.Config,
		stor,
		&compose.CommonStrategy{CoreStrategy: jwtStrategy},
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2PKCEFactory,
	)

	// 7. Create handler and HTTP server
	handler := handlers.NewHandler(provider, serverConfig, stor, options.upstream)
	httpServer := httptest.NewServer(handler.Routes())

	t.Cleanup(func() {
		httpServer.Close()
		stor.Close()
	})

	return &testServer{
		Server:       httpServer,
		Storage:      stor,
		ServerConfig: serverConfig,
		PrivateKey:   privateKey,
		Strategy:     jwtStrategy,
	}
}

// integrationTestSetup creates a test server without upstream provider.
// Use setupTestServer with options for more control.
func integrationTestSetup(t *testing.T) *testServer {
	t.Helper()
	return setupTestServer(t)
}

// generatePKCE generates a PKCE code verifier and S256 challenge.
func generatePKCE(t *testing.T) (verifier, challenge string) {
	t.Helper()

	// Generate random verifier (43-128 URL-safe characters)
	verifierBytes := make([]byte, 32)
	_, err := rand.Read(verifierBytes)
	require.NoError(t, err)
	verifier = base64.RawURLEncoding.EncodeToString(verifierBytes)

	// Calculate S256 challenge: base64url(sha256(verifier))
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])

	return verifier, challenge
}

// createAuthCodeSession pre-populates storage with an authorization code session.
// It uses the fosite strategy to generate a proper authorization code and returns
// the code token that the client should submit.
func createAuthCodeSession(
	t *testing.T,
	ts *testServer,
	pkceChallenge string,
	scopes []string,
) string {
	t.Helper()
	ctx := context.Background()

	// Get the client from storage
	oauthClient, err := ts.Storage.GetClient(ctx, testClientID)
	require.NoError(t, err)

	// Create the session
	sess := session.New(testSubject, "", testClientID)
	sess.SetExpiresAt(fosite.AccessToken, time.Now().Add(time.Hour))
	sess.SetExpiresAt(fosite.RefreshToken, time.Now().Add(24*time.Hour))
	sess.SetExpiresAt(fosite.AuthorizeCode, time.Now().Add(10*time.Minute))

	// Create a unique request ID
	requestID := generateRandomID(t)

	// Create the fosite request
	request := &fosite.Request{
		ID:          requestID,
		Client:      oauthClient,
		Session:     sess,
		RequestedAt: time.Now(),
		Form: url.Values{
			"redirect_uri":          {testRedirectURI},
			"code_challenge":        {pkceChallenge},
			"code_challenge_method": {"S256"},
		},
	}
	request.SetRequestedScopes(scopes)
	for _, scope := range scopes {
		request.GrantScope(scope)
	}

	// Generate the authorization code using fosite's strategy
	// This ensures the code and signature match what fosite expects
	authCode, authCodeSignature, err := ts.Strategy.GenerateAuthorizeCode(ctx, request)
	require.NoError(t, err)

	// Store the authorization code session using the signature
	err = ts.Storage.CreateAuthorizeCodeSession(ctx, authCodeSignature, request)
	require.NoError(t, err)

	// Store the PKCE session using the same signature
	err = ts.Storage.CreatePKCERequestSession(ctx, authCodeSignature, request)
	require.NoError(t, err)

	return authCode
}

// generateRandomID generates a random ID for requests.
func generateRandomID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(b)
}

// parseTokenResponse parses a token endpoint response.
func parseTokenResponse(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	defer resp.Body.Close()

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

// TestIntegration_TokenEndpoint_Success tests a successful authorization code exchange with PKCE.
func TestIntegration_TokenEndpoint_Success(t *testing.T) {
	t.Parallel()

	ts := integrationTestSetup(t)

	// Generate PKCE verifier and challenge
	verifier, challenge := generatePKCE(t)

	// Pre-populate storage with auth code
	authCode := createAuthCodeSession(t, ts, challenge, []string{"openid", "profile"})

	// Make token request
	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authCode},
		"client_id":     {testClientID},
		"redirect_uri":  {testRedirectURI},
		"code_verifier": {verifier},
	}

	resp := makeTokenRequest(t, ts.Server.URL, params)
	defer resp.Body.Close()

	// Parse response first to see what error we might be getting
	tokenResp := parseTokenResponse(t, resp)

	// Verify response status
	require.Equal(t, http.StatusOK, resp.StatusCode, "expected successful token response, got error: %v", tokenResp)

	// Verify access token is present
	accessToken, ok := tokenResp["access_token"].(string)
	assert.True(t, ok, "access_token should be a string")
	assert.NotEmpty(t, accessToken, "access_token should not be empty")

	// Verify token type
	tokenType, ok := tokenResp["token_type"].(string)
	assert.True(t, ok, "token_type should be a string")
	assert.Equal(t, "bearer", strings.ToLower(tokenType))

	// Verify expires_in is present and positive
	expiresIn, ok := tokenResp["expires_in"].(float64)
	assert.True(t, ok, "expires_in should be a number")
	assert.Greater(t, expiresIn, float64(0), "expires_in should be positive")
}

// TestIntegration_TokenEndpoint_Errors tests various error conditions at the token endpoint.
func TestIntegration_TokenEndpoint_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		setupCode    bool                     // whether to create a valid auth code
		modifyParams func(url.Values, string) // modify params; receives auth code if setupCode=true
	}{
		{
			name:      "invalid_pkce_verifier",
			setupCode: true,
			modifyParams: func(p url.Values, _ string) {
				p.Set("code_verifier", "wrong-verifier-that-wont-match")
			},
		},
		{
			name:      "invalid_code",
			setupCode: false,
			modifyParams: func(p url.Values, _ string) {
				p.Set("code", "non-existent-auth-code")
			},
		},
		{
			name:      "missing_redirect_uri",
			setupCode: true,
			modifyParams: func(p url.Values, _ string) {
				p.Del("redirect_uri")
			},
		},
		{
			name:      "wrong_client_id",
			setupCode: true,
			modifyParams: func(p url.Values, _ string) {
				p.Set("client_id", "wrong-client-id")
			},
		},
		{
			name:      "missing_pkce_verifier",
			setupCode: true,
			modifyParams: func(p url.Values, _ string) {
				p.Del("code_verifier")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := integrationTestSetup(t)
			verifier, challenge := generatePKCE(t)

			var authCode string
			if tc.setupCode {
				authCode = createAuthCodeSession(t, ts, challenge, []string{"openid", "profile"})
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

			require.GreaterOrEqual(t, resp.StatusCode, 400, "expected error response")

			errResp := parseTokenResponse(t, resp)
			errorField, ok := errResp["error"].(string)
			require.True(t, ok, "error should be a string")
			assert.NotEmpty(t, errorField, "error should not be empty")
		})
	}
}

// TestIntegration_TokenEndpoint_ReplayAttack tests that auth codes cannot be reused.
func TestIntegration_TokenEndpoint_ReplayAttack(t *testing.T) {
	t.Parallel()

	ts := integrationTestSetup(t)

	// Generate PKCE verifier and challenge
	verifier, challenge := generatePKCE(t)

	// Pre-populate storage with auth code
	authCode := createAuthCodeSession(t, ts, challenge, []string{"openid", "profile"})

	// First request - should succeed
	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authCode},
		"client_id":     {testClientID},
		"redirect_uri":  {testRedirectURI},
		"code_verifier": {verifier},
	}

	resp1 := makeTokenRequest(t, ts.Server.URL, params)
	resp1Body := parseTokenResponse(t, resp1)
	resp1.Body.Close()

	require.Equal(t, http.StatusOK, resp1.StatusCode, "first request should succeed, got error: %v", resp1Body)
	assert.NotEmpty(t, resp1Body["access_token"], "first request should return access token")

	// Second request with same code - should fail (replay attack)
	resp2 := makeTokenRequest(t, ts.Server.URL, params)
	defer resp2.Body.Close()

	// Verify response is an error
	assert.GreaterOrEqual(t, resp2.StatusCode, 400, "second request should fail (replay attack)")

	// Parse error response
	errResp := parseTokenResponse(t, resp2)

	// Verify error field is present
	errorField, ok := errResp["error"].(string)
	assert.True(t, ok, "error should be a string")
	assert.NotEmpty(t, errorField, "error should not be empty")
}

// TestIntegration_TokenEndpoint_RefreshToken tests that refresh tokens can be used to get new access tokens.
func TestIntegration_TokenEndpoint_RefreshToken(t *testing.T) {
	t.Parallel()

	ts := integrationTestSetup(t)

	// Generate PKCE verifier and challenge
	verifier, challenge := generatePKCE(t)

	// Pre-populate storage with auth code requesting offline_access for refresh token
	authCode := createAuthCodeSession(t, ts, challenge, []string{"openid", "profile", "offline_access"})

	// First, get tokens via authorization code
	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authCode},
		"client_id":     {testClientID},
		"redirect_uri":  {testRedirectURI},
		"code_verifier": {verifier},
	}

	resp := makeTokenRequest(t, ts.Server.URL, params)
	tokenResp := parseTokenResponse(t, resp)
	resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "initial token request should succeed")

	// Verify refresh token was returned (offline_access scope)
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
	refreshTokenResp := parseTokenResponse(t, refreshResp)
	refreshResp.Body.Close()

	require.Equal(t, http.StatusOK, refreshResp.StatusCode, "refresh token request should succeed")

	// Verify we got a new access token
	newAccessToken, ok := refreshTokenResp["access_token"].(string)
	require.True(t, ok, "access_token should be a string")
	assert.NotEmpty(t, newAccessToken, "new access_token should not be empty")

	// Verify token type
	tokenType, ok := refreshTokenResp["token_type"].(string)
	require.True(t, ok, "token_type should be a string")
	assert.Equal(t, "bearer", strings.ToLower(tokenType))
}

// TestIntegration_JWKS_ValidatesJWT tests that JWTs from the token endpoint can be validated using JWKS.
func TestIntegration_JWKS_ValidatesJWT(t *testing.T) {
	t.Parallel()

	ts := integrationTestSetup(t)

	// Generate PKCE verifier and challenge
	verifier, challenge := generatePKCE(t)

	// Pre-populate storage with auth code
	authCode := createAuthCodeSession(t, ts, challenge, []string{"openid", "profile"})

	// Get JWT from token endpoint
	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authCode},
		"client_id":     {testClientID},
		"redirect_uri":  {testRedirectURI},
		"code_verifier": {verifier},
	}

	resp := makeTokenRequest(t, ts.Server.URL, params)
	tokenResp := parseTokenResponse(t, resp)
	resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "token request should succeed, got error: %v", tokenResp)

	accessToken, ok := tokenResp["access_token"].(string)
	require.True(t, ok, "access_token should be a string")
	require.NotEmpty(t, accessToken, "access_token should not be empty")

	// Fetch JWKS from endpoint
	jwksResp, err := http.Get(ts.Server.URL + "/.well-known/jwks.json")
	require.NoError(t, err)
	defer jwksResp.Body.Close()

	require.Equal(t, http.StatusOK, jwksResp.StatusCode, "JWKS request should succeed")

	var jwks jose.JSONWebKeySet
	err = json.NewDecoder(jwksResp.Body).Decode(&jwks)
	require.NoError(t, err)
	require.NotEmpty(t, jwks.Keys, "JWKS should have at least one key")

	// Parse the JWT
	parsedToken, err := jwt.ParseSigned(accessToken, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err, "should be able to parse JWT")

	// Get the key ID from the token header (may be empty if not set)
	require.NotEmpty(t, parsedToken.Headers, "JWT should have headers")
	keyID := parsedToken.Headers[0].KeyID

	// Find the matching key in JWKS
	// If keyID is empty, use the first key in JWKS (common for single-key setups)
	var key jose.JSONWebKey
	if keyID != "" {
		keys := jwks.Key(keyID)
		require.NotEmpty(t, keys, "JWKS should contain key with ID %s", keyID)
		key = keys[0]
	} else {
		// No kid in token, use first JWKS key
		require.NotEmpty(t, jwks.Keys, "JWKS should have at least one key")
		key = jwks.Keys[0]
	}

	// Validate the JWT signature using the public key from JWKS
	var claims map[string]interface{}
	err = parsedToken.Claims(key.Key, &claims)
	require.NoError(t, err, "JWT signature should be valid")

	// Verify the issuer claim matches
	iss, ok := claims["iss"].(string)
	assert.True(t, ok, "iss claim should be a string")
	assert.Equal(t, ts.ServerConfig.AccessTokenIssuer, iss, "issuer should match config")
}

// TestIntegration_Discovery_ValidDocument tests that the discovery document contains all required fields.
func TestIntegration_Discovery_ValidDocument(t *testing.T) {
	t.Parallel()

	ts := integrationTestSetup(t)

	// Fetch discovery document
	resp, err := http.Get(ts.Server.URL + "/.well-known/openid-configuration")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "discovery request should succeed")
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	// Parse discovery document
	var discovery sharedobauth.OIDCDiscoveryDocument
	err = json.NewDecoder(resp.Body).Decode(&discovery)
	require.NoError(t, err)

	// Verify required fields are present and correct
	assert.Equal(t, ts.ServerConfig.AccessTokenIssuer, discovery.Issuer, "issuer should match config")
	assert.NotEmpty(t, discovery.AuthorizationEndpoint, "authorization_endpoint should be present")
	assert.NotEmpty(t, discovery.TokenEndpoint, "token_endpoint should be present")
	assert.NotEmpty(t, discovery.JWKSURI, "jwks_uri should be present")

	// Verify endpoints are valid URLs with correct issuer prefix
	assert.True(t, strings.HasPrefix(discovery.AuthorizationEndpoint, ts.ServerConfig.AccessTokenIssuer),
		"authorization_endpoint should use issuer as base URL")
	assert.True(t, strings.HasPrefix(discovery.TokenEndpoint, ts.ServerConfig.AccessTokenIssuer),
		"token_endpoint should use issuer as base URL")
	assert.True(t, strings.HasPrefix(discovery.JWKSURI, ts.ServerConfig.AccessTokenIssuer),
		"jwks_uri should use issuer as base URL")

	// Verify supported values
	assert.Contains(t, discovery.ResponseTypesSupported, "code", "should support code response type")
	assert.Contains(t, discovery.GrantTypesSupported, "authorization_code", "should support authorization_code grant")
	assert.Contains(t, discovery.GrantTypesSupported, "refresh_token", "should support refresh_token grant")
	assert.Contains(t, discovery.CodeChallengeMethodsSupported, "S256", "should support S256 PKCE")
	assert.Contains(t, discovery.TokenEndpointAuthMethodsSupported, "none", "should support public clients")
}

// TestIntegration_JWKS_KeyProperties tests that JWKS keys have all required properties.
func TestIntegration_JWKS_KeyProperties(t *testing.T) {
	t.Parallel()

	ts := integrationTestSetup(t)

	// Fetch JWKS
	resp, err := http.Get(ts.Server.URL + "/.well-known/jwks.json")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	// Parse JWKS
	var jwks jose.JSONWebKeySet
	err = json.NewDecoder(resp.Body).Decode(&jwks)
	require.NoError(t, err)

	require.NotEmpty(t, jwks.Keys, "JWKS should have at least one key")

	// Verify each key has required properties
	for i, key := range jwks.Keys {
		key := key // capture range variable
		t.Run("key_"+string(rune(i)), func(t *testing.T) {
			t.Parallel()
			assert.NotEmpty(t, key.KeyID, "key should have kid")
			assert.NotEmpty(t, key.Algorithm, "key should have alg")
			assert.Equal(t, "sig", key.Use, "key use should be 'sig'")
			assert.True(t, key.IsPublic(), "JWKS should only contain public keys")
		})
	}
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
		withScopes([]string{"openid", "profile", "email"}),
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
// and returns the authorization code issued by our auth server.
//
// The flow is: Client → Our /authorize → mockoidc → Our /callback → Client redirect
//
// We manually step through redirects to handle the fact that mockoidc's redirect
// points to "localhost" (from the config) but our test server runs on a random port.
func completeAuthorizationFlow(
	t *testing.T,
	serverURL string,
	params authorizationParams,
) string {
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

	// Step 5: Extract and validate the authorization code
	assert.Equal(t, params.State, clientLocation.Query().Get("state"), "client state should be preserved")
	authCode := clientLocation.Query().Get("code")
	require.NotEmpty(t, authCode, "authorization code should be present")

	return authCode
}

// initiateAuthorization starts an authorization flow and returns the internal state
// from the redirect to the upstream IDP. Use this when testing individual steps.
func initiateAuthorization(
	t *testing.T,
	serverURL string,
	mockIDPURL string,
	params authorizationParams,
) string {
	t.Helper()

	authorizeURL := serverURL + "/oauth/authorize?" + url.Values{
		"client_id":             {params.ClientID},
		"redirect_uri":          {params.RedirectURI},
		"state":                 {params.State},
		"code_challenge":        {params.Challenge},
		"code_challenge_method": {"S256"},
		"response_type":         {params.ResponseType},
		"scope":                 {params.Scope},
	}.Encode()

	httpClient := noRedirectClient()
	resp, err := httpClient.Get(authorizeURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusFound, resp.StatusCode, "expected redirect to upstream IDP")

	idpRedirect, err := resp.Location()
	require.NoError(t, err)
	assert.Contains(t, idpRedirect.String(), mockIDPURL, "redirect should point to mock IDP")

	internalState := idpRedirect.Query().Get("state")
	require.NotEmpty(t, internalState, "internal state should be present in redirect")

	return internalState
}

// exchangeCodeForTokens exchanges an authorization code for tokens and validates the response.
func exchangeCodeForTokens(
	t *testing.T,
	serverURL string,
	code string,
	verifier string,
) map[string]interface{} {
	t.Helper()

	tokenResp := makeTokenRequest(t, serverURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {testRedirectURI},
		"client_id":     {testClientID},
		"code_verifier": {verifier},
	})
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
	verifier, challenge := generatePKCE(t)
	clientState := "client-state-123"

	// Complete authorization flow through mockoidc (follows redirects)
	authCode := completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
		ClientID:     testClientID,
		RedirectURI:  testRedirectURI,
		State:        clientState,
		Challenge:    challenge,
		Scope:        "openid profile",
		ResponseType: "code",
	})

	// Exchange code for tokens
	tokenData := exchangeCodeForTokens(t, ts.Server.URL, authCode, verifier)

	// Step 4: Verify token response
	accessToken, ok := tokenData["access_token"].(string)
	require.True(t, ok, "access_token should be a string")
	require.NotEmpty(t, accessToken, "access_token should not be empty")

	tokenType, ok := tokenData["token_type"].(string)
	require.True(t, ok, "token_type should be a string")
	assert.Equal(t, "bearer", strings.ToLower(tokenType), "token type should be Bearer")

	// Step 5: Verify JWT signature and claims
	parsedToken, err := jwt.ParseSigned(accessToken, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err, "should be able to parse JWT")

	var claims map[string]interface{}
	err = parsedToken.Claims(ts.PrivateKey.Public(), &claims)
	require.NoError(t, err, "JWT signature should be valid")
	assert.Equal(t, ts.ServerConfig.AccessTokenIssuer, claims["iss"], "issuer should match")
}

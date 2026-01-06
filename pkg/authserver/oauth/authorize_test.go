package oauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/idp"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

const (
	testAuthClientID    = "test-auth-client"
	testAuthRedirectURI = "http://localhost:8080/callback"
	testAuthIssuer      = "http://test-auth-issuer"
	testInternalState   = "internal-state-123"
)

// mockIDPProvider implements idp.Provider for testing.
type mockIDPProvider struct {
	name                  string
	authorizationURL      string
	authURLErr            error
	exchangeTokens        *idp.Tokens
	exchangeErr           error
	userInfo              *idp.UserInfo
	userInfoErr           error
	refreshTokens         *idp.Tokens
	refreshErr            error
	capturedState         string
	capturedCode          string
	capturedCodeChallenge string
	capturedCodeVerifier  string
	capturedNonce         string
}

func (m *mockIDPProvider) Name() string {
	return m.name
}

func (m *mockIDPProvider) AuthorizationURL(state, codeChallenge, nonce string) (string, error) {
	m.capturedState = state
	m.capturedCodeChallenge = codeChallenge
	m.capturedNonce = nonce
	if m.authURLErr != nil {
		return "", m.authURLErr
	}
	return m.authorizationURL + "?state=" + state, nil
}

func (m *mockIDPProvider) ExchangeCode(_ context.Context, code, codeVerifier string) (*idp.Tokens, error) {
	m.capturedCode = code
	m.capturedCodeVerifier = codeVerifier
	if m.exchangeErr != nil {
		return nil, m.exchangeErr
	}
	return m.exchangeTokens, nil
}

func (m *mockIDPProvider) RefreshTokens(_ context.Context, _ string) (*idp.Tokens, error) {
	if m.refreshErr != nil {
		return nil, m.refreshErr
	}
	return m.refreshTokens, nil
}

func (m *mockIDPProvider) UserInfo(_ context.Context, _ string) (*idp.UserInfo, error) {
	if m.userInfoErr != nil {
		return nil, m.userInfoErr
	}
	return m.userInfo, nil
}

// authorizeTestSetup creates a test setup with all dependencies including an upstream provider.
func authorizeTestSetup(t *testing.T) (*Router, *storage.MemoryStorage, *mockIDPProvider) {
	t.Helper()

	// Generate RSA key for testing
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	require.NoError(t, err)

	cfg := &AuthServerConfig{
		Issuer:               testAuthIssuer,
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecret:           secret,
		SigningKeyID:         "test-key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           rsaKey,
	}

	oauth2Config, err := NewOAuth2ConfigFromAuthServerConfig(cfg)
	require.NoError(t, err)

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() {
		stor.Close()
	})

	// Register a test client (public client for PKCE)
	err = stor.RegisterClient(context.Background(), &fosite.DefaultClient{
		ID:            testAuthClientID,
		Secret:        nil, // public client
		RedirectURIs:  []string{testAuthRedirectURI},
		ResponseTypes: []string{"code"},
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		Scopes:        []string{"openid", "profile", "email"},
		Public:        true,
	})
	require.NoError(t, err)

	// Create fosite provider with authorization code support
	jwtStrategy := compose.NewOAuth2JWTStrategy(
		func(_ context.Context) (interface{}, error) {
			return rsaKey, nil
		},
		compose.NewOAuth2HMACStrategy(oauth2Config.Config),
		oauth2Config.Config,
	)

	provider := compose.Compose(
		oauth2Config.Config,
		stor,
		&compose.CommonStrategy{CoreStrategy: jwtStrategy},
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2PKCEFactory,
	)

	mockUpstream := &mockIDPProvider{
		name:             "test-idp",
		authorizationURL: "https://idp.example.com/authorize",
		exchangeTokens: &idp.Tokens{
			AccessToken:  "upstream-access-token",
			RefreshToken: "upstream-refresh-token",
			IDToken:      "upstream-id-token",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
		userInfo: &idp.UserInfo{
			Subject: "user-123",
			Email:   "user@example.com",
			Name:    "Test User",
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger, provider, oauth2Config, stor, mockUpstream)

	return router, stor, mockUpstream
}

func TestAuthorizeHandler_MissingClientID(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize", nil)
	rec := httptest.NewRecorder()

	router.AuthorizeHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "client_id is required")
}

func TestAuthorizeHandler_MissingRedirectURI(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id="+testAuthClientID, nil)
	rec := httptest.NewRecorder()

	router.AuthorizeHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "redirect_uri is required")
}

func TestAuthorizeHandler_ClientNotFound(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id=unknown&redirect_uri=http://example.com", nil)
	rec := httptest.NewRecorder()

	router.AuthorizeHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "client not found")
}

func TestAuthorizeHandler_InvalidRedirectURI(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id="+testAuthClientID+"&redirect_uri=http://evil.com/callback", nil)
	rec := httptest.NewRecorder()

	router.AuthorizeHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "redirect_uri does not match")
}

func TestAuthorizeHandler_UnsupportedResponseType(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)

	params := url.Values{
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"response_type": {"token"}, // implicit flow not supported
		"state":         {"test-state"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	router.AuthorizeHandler(rec, req)

	// Should redirect with error
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=unsupported_response_type")
	assert.Contains(t, location, "state=test-state")
}

func TestAuthorizeHandler_RequiresPKCEForPublicClient(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)

	params := url.Values{
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"response_type": {"code"},
		"state":         {"test-state"},
		// Missing code_challenge
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	router.AuthorizeHandler(rec, req)

	// Should redirect with error
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=invalid_request")
	assert.Contains(t, location, "code_challenge")
}

func TestAuthorizeHandler_RequiresS256Method(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)

	params := url.Values{
		"client_id":             {testAuthClientID},
		"redirect_uri":          {testAuthRedirectURI},
		"response_type":         {"code"},
		"state":                 {"test-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"plain"}, // Should be S256
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	router.AuthorizeHandler(rec, req)

	// Should redirect with error
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=invalid_request")
	assert.Contains(t, location, "S256")
}

func TestAuthorizeHandler_NoIDPProvider(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)
	// Remove upstream provider
	router.upstream = nil

	params := url.Values{
		"client_id":             {testAuthClientID},
		"redirect_uri":          {testAuthRedirectURI},
		"response_type":         {"code"},
		"state":                 {"test-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	router.AuthorizeHandler(rec, req)

	// Should redirect with error
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error")
}

func TestAuthorizeHandler_RedirectsToUpstream(t *testing.T) {
	t.Parallel()
	router, stor, mockUpstream := authorizeTestSetup(t)

	params := url.Values{
		"client_id":             {testAuthClientID},
		"redirect_uri":          {testAuthRedirectURI},
		"response_type":         {"code"},
		"state":                 {"client-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
		"scope":                 {"openid profile"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	router.AuthorizeHandler(rec, req)

	// Should redirect to upstream IDP
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "https://idp.example.com/authorize")

	// Should have captured the internal state
	assert.NotEmpty(t, mockUpstream.capturedState)

	// Should have sent PKCE challenge to upstream IDP
	assert.NotEmpty(t, mockUpstream.capturedCodeChallenge, "upstream PKCE challenge should be set")

	// Should have sent nonce to upstream IDP
	assert.NotEmpty(t, mockUpstream.capturedNonce, "upstream nonce should be set")

	// Should have stored pending authorization
	pending, err := stor.LoadPendingAuthorization(context.Background(), mockUpstream.capturedState)
	require.NoError(t, err)
	assert.Equal(t, testAuthClientID, pending.ClientID)
	assert.Equal(t, testAuthRedirectURI, pending.RedirectURI)
	assert.Equal(t, "client-state", pending.State)
	assert.Equal(t, "challenge123", pending.PKCEChallenge)
	assert.Equal(t, "S256", pending.PKCEMethod)
	assert.Contains(t, pending.Scopes, "openid")
	assert.Contains(t, pending.Scopes, "profile")

	// Should have stored upstream PKCE verifier
	assert.NotEmpty(t, pending.UpstreamPKCEVerifier, "upstream PKCE verifier should be stored")

	// Should have stored upstream nonce
	assert.NotEmpty(t, pending.UpstreamNonce, "upstream nonce should be stored")
	assert.Equal(t, mockUpstream.capturedNonce, pending.UpstreamNonce, "stored nonce should match sent nonce")

	// Verify the challenge matches the stored verifier
	assert.Equal(t, ComputePKCEChallenge(pending.UpstreamPKCEVerifier), mockUpstream.capturedCodeChallenge)
}

func TestCallbackHandler_MissingState(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=test-code", nil)
	rec := httptest.NewRecorder()

	router.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "missing state")
}

func TestCallbackHandler_MissingCode(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?state=test-state", nil)
	rec := httptest.NewRecorder()

	router.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "missing code")
}

func TestCallbackHandler_PendingAuthorizationNotFound(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=test-code&state=unknown-state", nil)
	rec := httptest.NewRecorder()

	router.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "not found")
}

func TestCallbackHandler_UpstreamError(t *testing.T) {
	t.Parallel()
	router, stor, _ := authorizeTestSetup(t)

	// Store a pending authorization
	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:      testAuthClientID,
		RedirectURI:   testAuthRedirectURI,
		State:         "client-state",
		PKCEChallenge: "challenge123",
		PKCEMethod:    "S256",
		Scopes:        []string{"openid"},
		InternalState: internalState,
		CreatedAt:     time.Now(),
	}
	err := stor.StorePendingAuthorization(context.Background(), internalState, pending)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?error=access_denied&error_description=User+denied&state="+internalState, nil)
	rec := httptest.NewRecorder()

	router.CallbackHandler(rec, req)

	// Should redirect to client with error
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=access_denied")
	assert.Contains(t, location, "state=client-state")

	// Pending authorization should be deleted
	_, err = stor.LoadPendingAuthorization(context.Background(), internalState)
	assert.Error(t, err)
}

func TestCallbackHandler_ExchangeCodeFailure(t *testing.T) {
	t.Parallel()
	router, stor, mockUpstream := authorizeTestSetup(t)

	// Configure upstream to fail code exchange
	mockUpstream.exchangeErr = assert.AnError
	mockUpstream.exchangeTokens = nil

	// Store a pending authorization
	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:      testAuthClientID,
		RedirectURI:   testAuthRedirectURI,
		State:         "client-state",
		PKCEChallenge: "challenge123",
		PKCEMethod:    "S256",
		Scopes:        []string{"openid"},
		InternalState: internalState,
		CreatedAt:     time.Now(),
	}
	err := stor.StorePendingAuthorization(context.Background(), internalState, pending)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	router.CallbackHandler(rec, req)

	// Should redirect to client with error
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error")
	assert.Contains(t, location, "state=client-state")
}

func TestCallbackHandler_Success(t *testing.T) {
	t.Parallel()
	router, stor, mockUpstream := authorizeTestSetup(t)

	// Store a pending authorization with upstream PKCE verifier
	internalState := testInternalState
	upstreamVerifier := "test-upstream-pkce-verifier-12345678901234567890"
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        internalState,
		UpstreamPKCEVerifier: upstreamVerifier,
		CreatedAt:            time.Now(),
	}
	err := stor.StorePendingAuthorization(context.Background(), internalState, pending)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	router.CallbackHandler(rec, req)

	// Should redirect to client with our authorization code
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, testAuthRedirectURI)
	assert.Contains(t, location, "code=")
	assert.Contains(t, location, "state=client-state")
	assert.NotContains(t, location, "error=")

	// Verify upstream code was exchanged with PKCE verifier
	assert.Equal(t, "upstream-code", mockUpstream.capturedCode)
	assert.Equal(t, upstreamVerifier, mockUpstream.capturedCodeVerifier, "PKCE verifier should be passed to upstream")

	// Pending authorization should be deleted
	_, err = stor.LoadPendingAuthorization(context.Background(), internalState)
	assert.Error(t, err)

	// IDP tokens should be stored
	stats := stor.Stats()
	assert.GreaterOrEqual(t, stats.IDPTokens, 1)
}

func TestCallbackHandler_NoIDPProvider(t *testing.T) {
	t.Parallel()
	router, stor, _ := authorizeTestSetup(t)
	// Remove upstream provider
	router.upstream = nil

	// Store a pending authorization
	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:      testAuthClientID,
		RedirectURI:   testAuthRedirectURI,
		State:         "client-state",
		PKCEChallenge: "challenge123",
		PKCEMethod:    "S256",
		Scopes:        []string{"openid"},
		InternalState: internalState,
		CreatedAt:     time.Now(),
	}
	err := stor.StorePendingAuthorization(context.Background(), internalState, pending)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=test-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	router.CallbackHandler(rec, req)

	// Should redirect with error
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error")
}

func TestCallbackHandler_UserInfoFailure_StillSucceeds(t *testing.T) {
	t.Parallel()
	router, stor, mockUpstream := authorizeTestSetup(t)

	// Configure upstream to fail userinfo but succeed on token exchange
	mockUpstream.userInfoErr = assert.AnError
	mockUpstream.userInfo = nil

	// Store a pending authorization
	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:      testAuthClientID,
		RedirectURI:   testAuthRedirectURI,
		State:         "client-state",
		PKCEChallenge: "challenge123",
		PKCEMethod:    "S256",
		Scopes:        []string{"openid"},
		InternalState: internalState,
		CreatedAt:     time.Now(),
	}
	err := stor.StorePendingAuthorization(context.Background(), internalState, pending)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	router.CallbackHandler(rec, req)

	// Should still succeed - userinfo failure is not fatal
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "code=")
	assert.NotContains(t, location, "error=")
}

func TestPendingAuthorizationStorage_StoreAndLoad(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() {
		stor.Close()
	})
	ctx := context.Background()

	pending := &storage.PendingAuthorization{
		ClientID:      "test-client",
		RedirectURI:   "http://localhost/callback",
		State:         "client-state",
		PKCEChallenge: "challenge",
		PKCEMethod:    "S256",
		Scopes:        []string{"openid", "profile"},
		InternalState: "internal-state",
		CreatedAt:     time.Now(),
	}

	err := stor.StorePendingAuthorization(ctx, "state-123", pending)
	require.NoError(t, err)

	loaded, err := stor.LoadPendingAuthorization(ctx, "state-123")
	require.NoError(t, err)
	assert.Equal(t, pending.ClientID, loaded.ClientID)
	assert.Equal(t, pending.RedirectURI, loaded.RedirectURI)
	assert.Equal(t, pending.State, loaded.State)
	assert.Equal(t, pending.PKCEChallenge, loaded.PKCEChallenge)
	assert.Equal(t, pending.PKCEMethod, loaded.PKCEMethod)
	assert.Equal(t, pending.Scopes, loaded.Scopes)
}

func TestPendingAuthorizationStorage_Delete(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() {
		stor.Close()
	})
	ctx := context.Background()

	pending := &storage.PendingAuthorization{
		ClientID:    "test-client",
		RedirectURI: "http://localhost/callback",
		CreatedAt:   time.Now(),
	}

	err := stor.StorePendingAuthorization(ctx, "state-456", pending)
	require.NoError(t, err)

	err = stor.DeletePendingAuthorization(ctx, "state-456")
	require.NoError(t, err)

	_, err = stor.LoadPendingAuthorization(ctx, "state-456")
	assert.Error(t, err)
}

func TestPendingAuthorizationStorage_NotFound(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() {
		stor.Close()
	})
	ctx := context.Background()

	_, err := stor.LoadPendingAuthorization(ctx, "nonexistent")
	assert.Error(t, err)
}

func TestPendingAuthorizationStorage_EmptyState(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() {
		stor.Close()
	})
	ctx := context.Background()

	err := stor.StorePendingAuthorization(ctx, "", &storage.PendingAuthorization{})
	assert.Error(t, err)
}

func TestPendingAuthorizationStorage_NilPending(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() {
		stor.Close()
	})
	ctx := context.Background()

	err := stor.StorePendingAuthorization(ctx, "state", nil)
	assert.Error(t, err)
}

func TestGenerateRandomState(t *testing.T) {
	t.Parallel()

	state1, err := generateRandomState()
	require.NoError(t, err)
	assert.NotEmpty(t, state1)

	state2, err := generateRandomState()
	require.NoError(t, err)
	assert.NotEmpty(t, state2)

	// States should be unique
	assert.NotEqual(t, state1, state2)
}

func TestIsValidRedirectURI(t *testing.T) {
	t.Parallel()

	client := &fosite.DefaultClient{
		RedirectURIs: []string{"http://localhost:8080/callback", "https://app.example.com/oauth/callback"},
	}

	assert.True(t, isValidRedirectURI(client, "http://localhost:8080/callback"))
	assert.True(t, isValidRedirectURI(client, "https://app.example.com/oauth/callback"))
	assert.False(t, isValidRedirectURI(client, "http://evil.com/callback"))
	assert.False(t, isValidRedirectURI(client, "http://localhost:8080/callback?extra=param"))
}

func TestBuildCallbackURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		redirectURI string
		code        string
		state       string
		expected    string
	}{
		{
			name:        "with state",
			redirectURI: "http://localhost:8080/callback",
			code:        "auth-code-123",
			state:       "client-state",
			expected:    "http://localhost:8080/callback?code=auth-code-123&state=client-state",
		},
		{
			name:        "without state",
			redirectURI: "http://localhost:8080/callback",
			code:        "auth-code-123",
			state:       "",
			expected:    "http://localhost:8080/callback?code=auth-code-123",
		},
		{
			name:        "with existing query params",
			redirectURI: "http://localhost:8080/callback?existing=param",
			code:        "auth-code-123",
			state:       "state",
			expected:    "http://localhost:8080/callback?code=auth-code-123&existing=param&state=state",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := buildCallbackURL(tc.redirectURI, tc.code, tc.state)

			// Parse both URLs to compare
			expectedURL, _ := url.Parse(tc.expected)
			resultURL, _ := url.Parse(result)

			assert.Equal(t, expectedURL.Scheme, resultURL.Scheme)
			assert.Equal(t, expectedURL.Host, resultURL.Host)
			assert.Equal(t, expectedURL.Path, resultURL.Path)
			assert.Equal(t, expectedURL.Query().Get("code"), resultURL.Query().Get("code"))
			assert.Equal(t, expectedURL.Query().Get("state"), resultURL.Query().Get("state"))
		})
	}
}

func TestRoutesIncludeAuthorizeAndCallback(t *testing.T) {
	t.Parallel()
	router, _, _ := authorizeTestSetup(t)

	mux := http.NewServeMux()
	router.Routes(mux)

	// Test that routes are registered
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/oauth/authorize"},
		{http.MethodGet, "/oauth/callback"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			// Should not return 404 (route not found)
			assert.NotEqual(t, http.StatusNotFound, rec.Code,
				"route %s %s should be registered", tc.method, tc.path)
		})
	}
}

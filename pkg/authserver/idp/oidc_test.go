package idp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// mockOIDCServer creates a mock OIDC server for testing.
type mockOIDCServer struct {
	*httptest.Server
	issuer       string
	discoveryDoc *OIDCEndpoints
	tokenHandler func(w http.ResponseWriter, r *http.Request)
	userHandler  func(w http.ResponseWriter, r *http.Request)
}

func newMockOIDCServer() *mockOIDCServer {
	mock := &mockOIDCServer{}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", mock.handleDiscovery)
	mux.HandleFunc("/authorize", mock.handleAuthorize)
	mux.HandleFunc("/token", mock.handleToken)
	mux.HandleFunc("/userinfo", mock.handleUserInfo)

	mock.Server = httptest.NewServer(mux)
	mock.issuer = mock.URL

	// Set default discovery document
	mock.discoveryDoc = &OIDCEndpoints{
		Issuer:                        mock.issuer,
		AuthorizationEndpoint:         mock.issuer + "/authorize",
		TokenEndpoint:                 mock.issuer + "/token",
		UserInfoEndpoint:              mock.issuer + "/userinfo",
		JWKSEndpoint:                  mock.issuer + "/.well-known/jwks.json",
		CodeChallengeMethodsSupported: []string{pkceChallengeMethodS256},
	}

	return mock
}

func (m *mockOIDCServer) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(m.discoveryDoc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (*mockOIDCServer) handleAuthorize(w http.ResponseWriter, _ *http.Request) {
	// The authorize endpoint is typically user-facing and redirects back
	// We don't need to implement this for unit tests
	w.WriteHeader(http.StatusOK)
}

func (m *mockOIDCServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if m.tokenHandler != nil {
		m.tokenHandler(w, r)
		return
	}

	// Default token response
	w.Header().Set("Content-Type", "application/json")
	resp := tokenResponse{
		AccessToken:  "test-access-token",
		TokenType:    "Bearer",
		RefreshToken: "test-refresh-token",
		ExpiresIn:    3600,
		IDToken:      "test-id-token",
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (m *mockOIDCServer) handleUserInfo(w http.ResponseWriter, r *http.Request) {
	if m.userHandler != nil {
		m.userHandler(w, r)
		return
	}

	// Verify authorization header
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		http.Error(w, "missing authorization", http.StatusUnauthorized)
		return
	}

	// Default userinfo response
	w.Header().Set("Content-Type", "application/json")
	userInfo := map[string]any{
		"sub":   "user-123",
		"email": "user@example.com",
		"name":  "Test User",
	}
	if err := json.NewEncoder(w).Encode(userInfo); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func TestNewOIDCProvider(t *testing.T) {
	t.Parallel()

	mock := newMockOIDCServer()
	defer mock.Close()

	config := Config{
		Issuer:       mock.issuer,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURI:  "http://localhost:8080/callback",
		Scopes:       []string{"openid", "profile", "email"},
	}

	ctx := context.Background()
	provider, err := NewOIDCProvider(ctx, config)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	if provider.Name() != "oidc" {
		t.Errorf("expected name 'oidc', got %q", provider.Name())
	}

	endpoints := provider.Endpoints()
	if endpoints == nil {
		t.Fatal("expected endpoints to be set")
	}

	if endpoints.Issuer != mock.issuer {
		t.Errorf("expected issuer %q, got %q", mock.issuer, endpoints.Issuer)
	}

	if endpoints.AuthorizationEndpoint != mock.issuer+"/authorize" {
		t.Errorf("expected authorization endpoint %q, got %q",
			mock.issuer+"/authorize", endpoints.AuthorizationEndpoint)
	}

	if endpoints.TokenEndpoint != mock.issuer+"/token" {
		t.Errorf("expected token endpoint %q, got %q",
			mock.issuer+"/token", endpoints.TokenEndpoint)
	}
}

func TestNewOIDCProvider_InvalidConfig(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	tests := []struct {
		name   string
		config Config
	}{
		{
			name:   "empty config",
			config: Config{},
		},
		{
			name: "missing client id",
			config: Config{
				Issuer:       "https://example.com",
				ClientSecret: "secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
		},
		{
			name: "missing client secret",
			config: Config{
				Issuer:      "https://example.com",
				ClientID:    "client",
				RedirectURI: "http://localhost:8080/callback",
			},
		},
		{
			name: "missing redirect uri",
			config: Config{
				Issuer:       "https://example.com",
				ClientID:     "client",
				ClientSecret: "secret",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewOIDCProvider(ctx, tt.config)
			if err == nil {
				t.Error("expected error for invalid config")
			}
		})
	}
}

func TestNewOIDCProvider_DiscoveryFailure(t *testing.T) {
	t.Parallel()

	// Server that returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	config := Config{
		Issuer:       server.URL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURI:  "http://localhost:8080/callback",
	}

	ctx := context.Background()
	_, err := NewOIDCProvider(ctx, config)
	if err == nil {
		t.Error("expected error when discovery fails")
	}
}

func TestNewOIDCProvider_IssuerMismatch(t *testing.T) {
	t.Parallel()

	// Server that returns mismatched issuer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		doc := OIDCEndpoints{
			Issuer:                "https://wrong-issuer.example.com",
			AuthorizationEndpoint: "https://wrong-issuer.example.com/authorize",
			TokenEndpoint:         "https://wrong-issuer.example.com/token",
		}
		if err := json.NewEncoder(w).Encode(doc); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	config := Config{
		Issuer:       server.URL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURI:  "http://localhost:8080/callback",
	}

	ctx := context.Background()
	_, err := NewOIDCProvider(ctx, config)
	if err == nil {
		t.Error("expected error for issuer mismatch")
	}

	if !strings.Contains(err.Error(), "issuer mismatch") {
		t.Errorf("expected issuer mismatch error, got: %v", err)
	}
}

func TestOIDCIDPProvider_AuthorizationURL(t *testing.T) {
	t.Parallel()

	mock := newMockOIDCServer()
	t.Cleanup(mock.Close)

	config := Config{
		Issuer:       mock.issuer,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURI:  "http://localhost:8080/callback",
		Scopes:       []string{"openid", "profile"},
	}

	ctx := context.Background()
	provider, err := NewOIDCProvider(ctx, config)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	t.Run("basic authorization URL", func(t *testing.T) {
		t.Parallel()
		authURL, err := provider.AuthorizationURL("test-state", "", nil)
		if err != nil {
			t.Fatalf("failed to build authorization URL: %v", err)
		}

		parsed, err := url.Parse(authURL)
		if err != nil {
			t.Fatalf("failed to parse authorization URL: %v", err)
		}

		query := parsed.Query()
		if query.Get("response_type") != "code" {
			t.Errorf("expected response_type=code, got %q", query.Get("response_type"))
		}

		if query.Get("client_id") != "test-client" {
			t.Errorf("expected client_id=test-client, got %q", query.Get("client_id"))
		}

		if query.Get("redirect_uri") != "http://localhost:8080/callback" {
			t.Errorf("expected redirect_uri=http://localhost:8080/callback, got %q", query.Get("redirect_uri"))
		}

		if query.Get("state") != "test-state" {
			t.Errorf("expected state=test-state, got %q", query.Get("state"))
		}

		// Should use configured scopes
		if query.Get("scope") != "openid profile" {
			t.Errorf("expected scope='openid profile', got %q", query.Get("scope"))
		}
	})

	t.Run("authorization URL with PKCE", func(t *testing.T) {
		t.Parallel()
		authURL, err := provider.AuthorizationURL("test-state", "test-challenge", nil)
		if err != nil {
			t.Fatalf("failed to build authorization URL: %v", err)
		}

		parsed, err := url.Parse(authURL)
		if err != nil {
			t.Fatalf("failed to parse authorization URL: %v", err)
		}

		query := parsed.Query()
		if query.Get("code_challenge") != "test-challenge" {
			t.Errorf("expected code_challenge=test-challenge, got %q", query.Get("code_challenge"))
		}

		if query.Get("code_challenge_method") != pkceChallengeMethodS256 {
			t.Errorf("expected code_challenge_method=%s, got %q", pkceChallengeMethodS256, query.Get("code_challenge_method"))
		}
	})

	t.Run("authorization URL ignores client scopes and uses config scopes", func(t *testing.T) {
		t.Parallel()
		// Client-requested scopes should be ignored; config scopes should always be used
		// because config scopes represent what the upstream integration requires
		authURL, err := provider.AuthorizationURL("test-state", "", []string{"custom", "scopes"})
		if err != nil {
			t.Fatalf("failed to build authorization URL: %v", err)
		}

		parsed, err := url.Parse(authURL)
		if err != nil {
			t.Fatalf("failed to parse authorization URL: %v", err)
		}

		query := parsed.Query()
		// Should use config scopes ("openid profile") not the passed-in scopes
		if query.Get("scope") != "openid profile" {
			t.Errorf("expected scope='openid profile' (config scopes), got %q", query.Get("scope"))
		}
	})

	t.Run("authorization URL without state fails", func(t *testing.T) {
		t.Parallel()
		_, err := provider.AuthorizationURL("", "", nil)
		if err == nil {
			t.Error("expected error for empty state")
		}
	})
}

func TestOIDCIDPProvider_AuthorizationURL_DefaultScopes(t *testing.T) {
	t.Parallel()

	mock := newMockOIDCServer()
	t.Cleanup(mock.Close)

	// Config with NO scopes - should fall back to defaults
	config := Config{
		Issuer:       mock.issuer,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURI:  "http://localhost:8080/callback",
		// Scopes is intentionally empty
	}

	ctx := context.Background()
	provider, err := NewOIDCProvider(ctx, config)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	// Even though we pass client scopes, they should be ignored and defaults used
	authURL, err := provider.AuthorizationURL("test-state", "", []string{"client", "requested", "scopes"})
	if err != nil {
		t.Fatalf("failed to build authorization URL: %v", err)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("failed to parse authorization URL: %v", err)
	}

	query := parsed.Query()
	// Should use default scopes since config has none, client scopes are ignored
	if query.Get("scope") != "openid profile email" {
		t.Errorf("expected scope='openid profile email' (default scopes), got %q", query.Get("scope"))
	}
}

func TestOIDCIDPProvider_ExchangeCode(t *testing.T) {
	t.Parallel()

	mock := newMockOIDCServer()
	t.Cleanup(mock.Close)

	config := Config{
		Issuer:       mock.issuer,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURI:  "http://localhost:8080/callback",
	}

	ctx := context.Background()

	t.Run("successful code exchange", func(t *testing.T) {
		t.Parallel()

		localMock := newMockOIDCServer()
		defer localMock.Close()

		var receivedParams url.Values
		localMock.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			receivedParams = r.PostForm

			w.Header().Set("Content-Type", "application/json")
			resp := tokenResponse{
				AccessToken:  "exchanged-access-token",
				TokenType:    "Bearer",
				RefreshToken: "exchanged-refresh-token",
				ExpiresIn:    7200,
				IDToken:      "exchanged-id-token",
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		localConfig := Config{
			Issuer:       localMock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, localConfig)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		tokens, err := provider.ExchangeCode(ctx, "test-code", "test-verifier")
		if err != nil {
			t.Fatalf("failed to exchange code: %v", err)
		}

		// Verify request parameters
		if receivedParams.Get("grant_type") != "authorization_code" {
			t.Errorf("expected grant_type=authorization_code, got %q", receivedParams.Get("grant_type"))
		}

		if receivedParams.Get("code") != "test-code" {
			t.Errorf("expected code=test-code, got %q", receivedParams.Get("code"))
		}

		if receivedParams.Get("code_verifier") != "test-verifier" {
			t.Errorf("expected code_verifier=test-verifier, got %q", receivedParams.Get("code_verifier"))
		}

		if receivedParams.Get("client_id") != "test-client" {
			t.Errorf("expected client_id=test-client, got %q", receivedParams.Get("client_id"))
		}

		if receivedParams.Get("client_secret") != "test-secret" {
			t.Errorf("expected client_secret=test-secret, got %q", receivedParams.Get("client_secret"))
		}

		// Verify response
		if tokens.AccessToken != "exchanged-access-token" {
			t.Errorf("expected access token 'exchanged-access-token', got %q", tokens.AccessToken)
		}

		if tokens.RefreshToken != "exchanged-refresh-token" {
			t.Errorf("expected refresh token 'exchanged-refresh-token', got %q", tokens.RefreshToken)
		}

		if tokens.IDToken != "exchanged-id-token" {
			t.Errorf("expected ID token 'exchanged-id-token', got %q", tokens.IDToken)
		}

		// Verify expiration is set approximately correctly
		expectedExpiry := time.Now().Add(7200 * time.Second)
		if tokens.ExpiresAt.Before(expectedExpiry.Add(-10*time.Second)) ||
			tokens.ExpiresAt.After(expectedExpiry.Add(10*time.Second)) {
			t.Errorf("expected expiry around %v, got %v", expectedExpiry, tokens.ExpiresAt)
		}
	})

	t.Run("code exchange without verifier", func(t *testing.T) {
		t.Parallel()

		localMock := newMockOIDCServer()
		defer localMock.Close()

		var receivedParams url.Values
		localMock.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			receivedParams = r.PostForm

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(tokenResponse{AccessToken: "token"}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		localConfig := Config{
			Issuer:       localMock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, localConfig)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		_, err = provider.ExchangeCode(ctx, "test-code", "")
		if err != nil {
			t.Fatalf("failed to exchange code: %v", err)
		}

		if receivedParams.Get("code_verifier") != "" {
			t.Error("expected code_verifier to be empty")
		}
	})

	t.Run("code exchange without code fails", func(t *testing.T) {
		t.Parallel()
		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		_, err = provider.ExchangeCode(ctx, "", "")
		if err == nil {
			t.Error("expected error for empty code")
		}
	})

	t.Run("code exchange with error response", func(t *testing.T) {
		t.Parallel()

		localMock := newMockOIDCServer()
		defer localMock.Close()

		localMock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			resp := tokenErrorResponse{
				Error:            "invalid_grant",
				ErrorDescription: "The authorization code has expired",
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		localConfig := Config{
			Issuer:       localMock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, localConfig)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		_, err = provider.ExchangeCode(ctx, "expired-code", "")
		if err == nil {
			t.Error("expected error for expired code")
		}

		if !strings.Contains(err.Error(), "invalid_grant") {
			t.Errorf("expected error to contain 'invalid_grant', got: %v", err)
		}
	})
}

func TestOIDCIDPProvider_RefreshTokens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful token refresh", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		defer mock.Close()

		var receivedParams url.Values
		mock.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			receivedParams = r.PostForm

			w.Header().Set("Content-Type", "application/json")
			resp := tokenResponse{
				AccessToken:  "refreshed-access-token",
				TokenType:    "Bearer",
				RefreshToken: "new-refresh-token",
				ExpiresIn:    3600,
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := Config{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		tokens, err := provider.RefreshTokens(ctx, "old-refresh-token")
		if err != nil {
			t.Fatalf("failed to refresh tokens: %v", err)
		}

		// Verify request parameters
		if receivedParams.Get("grant_type") != "refresh_token" {
			t.Errorf("expected grant_type=refresh_token, got %q", receivedParams.Get("grant_type"))
		}

		if receivedParams.Get("refresh_token") != "old-refresh-token" {
			t.Errorf("expected refresh_token=old-refresh-token, got %q", receivedParams.Get("refresh_token"))
		}

		// Verify response
		if tokens.AccessToken != "refreshed-access-token" {
			t.Errorf("expected access token 'refreshed-access-token', got %q", tokens.AccessToken)
		}

		if tokens.RefreshToken != "new-refresh-token" {
			t.Errorf("expected refresh token 'new-refresh-token', got %q", tokens.RefreshToken)
		}
	})

	t.Run("refresh without token fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		defer mock.Close()

		config := Config{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		_, err = provider.RefreshTokens(ctx, "")
		if err == nil {
			t.Error("expected error for empty refresh token")
		}
	})

	t.Run("refresh with server error", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		defer mock.Close()

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}

		config := Config{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		_, err = provider.RefreshTokens(ctx, "refresh-token")
		if err == nil {
			t.Error("expected error for server error")
		}
	})
}

func TestOIDCIDPProvider_UserInfo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful userinfo request", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		defer mock.Close()

		var receivedAuth string
		mock.userHandler = func(w http.ResponseWriter, r *http.Request) {
			receivedAuth = r.Header.Get("Authorization")

			w.Header().Set("Content-Type", "application/json")
			userInfo := map[string]any{
				"sub":            "user-456",
				"email":          "test@example.com",
				"name":           "Test User",
				"custom_claim":   "custom_value",
				"numeric_claim":  123,
				"email_verified": true,
			}
			if err := json.NewEncoder(w).Encode(userInfo); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := Config{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		userInfo, err := provider.UserInfo(ctx, "test-access-token")
		if err != nil {
			t.Fatalf("failed to get userinfo: %v", err)
		}

		// Verify authorization header
		if receivedAuth != "Bearer test-access-token" {
			t.Errorf("expected authorization 'Bearer test-access-token', got %q", receivedAuth)
		}

		// Verify standard claims
		if userInfo.Subject != "user-456" {
			t.Errorf("expected subject 'user-456', got %q", userInfo.Subject)
		}

		if userInfo.Email != "test@example.com" {
			t.Errorf("expected email 'test@example.com', got %q", userInfo.Email)
		}

		if userInfo.Name != "Test User" {
			t.Errorf("expected name 'Test User', got %q", userInfo.Name)
		}

		// Verify all claims are available
		if userInfo.Claims["custom_claim"] != "custom_value" {
			t.Errorf("expected custom_claim 'custom_value', got %v", userInfo.Claims["custom_claim"])
		}

		if userInfo.Claims["numeric_claim"] != float64(123) {
			t.Errorf("expected numeric_claim 123, got %v", userInfo.Claims["numeric_claim"])
		}
	})

	t.Run("userinfo without access token fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		defer mock.Close()

		config := Config{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		_, err = provider.UserInfo(ctx, "")
		if err == nil {
			t.Error("expected error for empty access token")
		}
	})

	t.Run("userinfo with unauthorized response", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		defer mock.Close()

		mock.userHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("invalid token"))
		}

		config := Config{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		_, err = provider.UserInfo(ctx, "invalid-token")
		if err == nil {
			t.Error("expected error for unauthorized response")
		}

		if !strings.Contains(err.Error(), "401") {
			t.Errorf("expected error to contain '401', got: %v", err)
		}
	})

	t.Run("userinfo endpoint not available", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		mock.discoveryDoc.UserInfoEndpoint = "" // Remove userinfo endpoint
		defer mock.Close()

		config := Config{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		_, err = provider.UserInfo(ctx, "test-token")
		if err == nil {
			t.Error("expected error when userinfo endpoint not available")
		}

		if !strings.Contains(err.Error(), "userinfo endpoint not available") {
			t.Errorf("expected 'userinfo endpoint not available' error, got: %v", err)
		}
	})
}

func TestOIDCIDPProvider_WithOptions(t *testing.T) {
	t.Parallel()

	mock := newMockOIDCServer()
	defer mock.Close()

	config := Config{
		Issuer:       mock.issuer,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURI:  "http://localhost:8080/callback",
	}

	ctx := context.Background()

	// Test with custom logger
	customLogger := slog.Default().With("component", "test")
	_, err := NewOIDCProvider(ctx, config, WithLogger(customLogger))
	if err != nil {
		t.Fatalf("failed to create provider with custom logger: %v", err)
	}

	// Test with custom HTTP client
	customClient := &http.Client{Timeout: 5 * time.Second}
	_, err = NewOIDCProvider(ctx, config, WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("failed to create provider with custom HTTP client: %v", err)
	}
}

func Test_buildDiscoveryURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		issuer      string
		expectedURL string
		expectError bool
	}{
		{
			name:        "simple HTTPS issuer",
			issuer:      "https://example.com",
			expectedURL: "https://example.com/.well-known/openid-configuration",
		},
		{
			name:        "issuer with trailing slash",
			issuer:      "https://example.com/",
			expectedURL: "https://example.com/.well-known/openid-configuration",
		},
		{
			name:        "issuer with path",
			issuer:      "https://example.com/auth/realms/myrealm",
			expectedURL: "https://example.com/auth/realms/myrealm/.well-known/openid-configuration",
		},
		{
			name:        "localhost HTTP allowed",
			issuer:      "http://localhost:8080",
			expectedURL: "http://localhost:8080/.well-known/openid-configuration",
		},
		{
			name:        "127.0.0.1 HTTP allowed",
			issuer:      "http://127.0.0.1:8080",
			expectedURL: "http://127.0.0.1:8080/.well-known/openid-configuration",
		},
		{
			name:        "non-localhost HTTP not allowed",
			issuer:      "http://example.com",
			expectError: true,
		},
		{
			name:        "empty issuer",
			issuer:      "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := buildDiscoveryURL(tt.issuer)
			if tt.expectError {
				if err == nil {
					t.Error("expected error")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result != tt.expectedURL {
					t.Errorf("expected %q, got %q", tt.expectedURL, result)
				}
			}
		})
	}
}

func Test_validateDiscoveryDocument(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		doc         *OIDCEndpoints
		issuer      string
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid document",
			doc: &OIDCEndpoints{
				Issuer:                "https://example.com",
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
			},
			issuer: "https://example.com",
		},
		{
			name: "missing issuer",
			doc: &OIDCEndpoints{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
			},
			issuer:      "https://example.com",
			expectError: true,
			errorMsg:    "missing issuer",
		},
		{
			name: "issuer mismatch",
			doc: &OIDCEndpoints{
				Issuer:                "https://wrong.com",
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
			},
			issuer:      "https://example.com",
			expectError: true,
			errorMsg:    "issuer mismatch",
		},
		{
			name: "missing authorization endpoint",
			doc: &OIDCEndpoints{
				Issuer:        "https://example.com",
				TokenEndpoint: "https://example.com/token",
			},
			issuer:      "https://example.com",
			expectError: true,
			errorMsg:    "missing authorization_endpoint",
		},
		{
			name: "missing token endpoint",
			doc: &OIDCEndpoints{
				Issuer:                "https://example.com",
				AuthorizationEndpoint: "https://example.com/authorize",
			},
			issuer:      "https://example.com",
			expectError: true,
			errorMsg:    "missing token_endpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateDiscoveryDocument(tt.doc, tt.issuer)
			if tt.expectError {
				if err == nil {
					t.Error("expected error")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("expected error containing %q, got: %v", tt.errorMsg, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestTokensIsExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		expiresAt time.Time
		expected  bool
	}{
		{
			name:      "not expired",
			expiresAt: time.Now().Add(time.Hour),
			expected:  false,
		},
		{
			name:      "expired",
			expiresAt: time.Now().Add(-time.Hour),
			expected:  true,
		},
		{
			name:      "exactly now",
			expiresAt: time.Now(),
			expected:  true, // After means strictly after, so "now" is expired
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tokens := &Tokens{
				AccessToken: "test",
				ExpiresAt:   tt.expiresAt,
			}
			if tokens.IsExpired() != tt.expected {
				t.Errorf("expected IsExpired()=%v, got %v", tt.expected, tokens.IsExpired())
			}
		})
	}
}

func TestOIDCIDPProvider_DefaultExpiryWhenNotSpecified(t *testing.T) {
	t.Parallel()

	mock := newMockOIDCServer()
	defer mock.Close()

	// Token response without expires_in
	mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := tokenResponse{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			// No ExpiresIn
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}

	config := Config{
		Issuer:       mock.issuer,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURI:  "http://localhost:8080/callback",
	}

	ctx := context.Background()
	provider, err := NewOIDCProvider(ctx, config)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	tokens, err := provider.ExchangeCode(ctx, "test-code", "")
	if err != nil {
		t.Fatalf("failed to exchange code: %v", err)
	}

	// Should default to 1 hour
	expectedExpiry := time.Now().Add(time.Hour)
	if tokens.ExpiresAt.Before(expectedExpiry.Add(-10*time.Second)) ||
		tokens.ExpiresAt.After(expectedExpiry.Add(10*time.Second)) {
		t.Errorf("expected expiry around %v (1 hour default), got %v", expectedExpiry, tokens.ExpiresAt)
	}
}

func TestOIDCIDPProvider_NetworkError(t *testing.T) {
	t.Parallel()

	// Use an invalid URL to simulate network error
	config := Config{
		Issuer:       "http://localhost:1", // Invalid port, should fail to connect
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURI:  "http://localhost:8080/callback",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := NewOIDCProvider(ctx, config)
	if err == nil {
		t.Error("expected error for network failure")
	}
}

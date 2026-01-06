package idp

import (
	"context"
	"encoding/json"
	"errors"
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

	config := &UpstreamConfig{
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
		config *UpstreamConfig
	}{
		{
			name:   "nil config",
			config: nil,
		},
		{
			name:   "empty config",
			config: &UpstreamConfig{},
		},
		{
			name: "missing client id",
			config: &UpstreamConfig{
				Issuer:       "https://example.com",
				ClientSecret: "secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
		},
		{
			name: "missing client secret",
			config: &UpstreamConfig{
				Issuer:      "https://example.com",
				ClientID:    "client",
				RedirectURI: "http://localhost:8080/callback",
			},
		},
		{
			name: "missing redirect uri",
			config: &UpstreamConfig{
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

	config := &UpstreamConfig{
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

	config := &UpstreamConfig{
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

	config := &UpstreamConfig{
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
		authURL, err := provider.AuthorizationURL("test-state", "", "")
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

		// Should not have nonce when not provided
		if query.Get("nonce") != "" {
			t.Errorf("expected nonce to be empty, got %q", query.Get("nonce"))
		}
	})

	t.Run("authorization URL with PKCE", func(t *testing.T) {
		t.Parallel()
		authURL, err := provider.AuthorizationURL("test-state", "test-challenge", "")
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

	t.Run("authorization URL with nonce", func(t *testing.T) {
		t.Parallel()
		authURL, err := provider.AuthorizationURL("test-state", "", "test-nonce-12345")
		if err != nil {
			t.Fatalf("failed to build authorization URL: %v", err)
		}

		parsed, err := url.Parse(authURL)
		if err != nil {
			t.Fatalf("failed to parse authorization URL: %v", err)
		}

		query := parsed.Query()
		if query.Get("nonce") != "test-nonce-12345" {
			t.Errorf("expected nonce=test-nonce-12345, got %q", query.Get("nonce"))
		}
	})

	t.Run("authorization URL uses config scopes", func(t *testing.T) {
		t.Parallel()
		// Config scopes should always be used because they represent what the upstream integration requires
		authURL, err := provider.AuthorizationURL("test-state", "", "")
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
		_, err := provider.AuthorizationURL("", "", "")
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
	config := &UpstreamConfig{
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

	// Since config has no scopes, default scopes should be used
	authURL, err := provider.AuthorizationURL("test-state", "", "")
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

	config := &UpstreamConfig{
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

		localConfig := &UpstreamConfig{
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
			if err := json.NewEncoder(w).Encode(tokenResponse{AccessToken: "token", TokenType: "Bearer"}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		localConfig := &UpstreamConfig{
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

		localConfig := &UpstreamConfig{
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

		config := &UpstreamConfig{
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

		config := &UpstreamConfig{
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

		config := &UpstreamConfig{
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

		config := &UpstreamConfig{
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

		config := &UpstreamConfig{
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

		config := &UpstreamConfig{
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

		config := &UpstreamConfig{
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

func TestOIDCIDPProvider_UserInfoWithSubjectValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful validation with matching subject", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		defer mock.Close()

		mock.userHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			userInfo := map[string]any{
				"sub":   "user-123",
				"email": "test@example.com",
				"name":  "Test User",
			}
			if err := json.NewEncoder(w).Encode(userInfo); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := &UpstreamConfig{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		userInfo, err := provider.UserInfoWithSubjectValidation(ctx, "test-access-token", "user-123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if userInfo.Subject != "user-123" {
			t.Errorf("expected subject 'user-123', got %q", userInfo.Subject)
		}

		if userInfo.Email != "test@example.com" {
			t.Errorf("expected email 'test@example.com', got %q", userInfo.Email)
		}
	})

	t.Run("subject mismatch returns error", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		defer mock.Close()

		mock.userHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			userInfo := map[string]any{
				"sub":   "attacker-user-456",
				"email": "attacker@example.com",
				"name":  "Attacker User",
			}
			if err := json.NewEncoder(w).Encode(userInfo); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := &UpstreamConfig{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		_, err = provider.UserInfoWithSubjectValidation(ctx, "test-access-token", "expected-user-123")
		if err == nil {
			t.Fatal("expected error for subject mismatch")
		}

		if !errors.Is(err, ErrUserInfoSubjectMismatch) {
			t.Errorf("expected ErrUserInfoSubjectMismatch, got: %v", err)
		}

		if !strings.Contains(err.Error(), "expected-user-123") {
			t.Errorf("expected error to contain expected subject, got: %v", err)
		}

		if !strings.Contains(err.Error(), "attacker-user-456") {
			t.Errorf("expected error to contain actual subject, got: %v", err)
		}
	})

	t.Run("empty subject in userinfo returns mismatch error", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		defer mock.Close()

		mock.userHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Response without 'sub' claim
			userInfo := map[string]any{
				"email": "test@example.com",
				"name":  "Test User",
			}
			if err := json.NewEncoder(w).Encode(userInfo); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := &UpstreamConfig{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		_, err = provider.UserInfoWithSubjectValidation(ctx, "test-access-token", "expected-user-123")
		if err == nil {
			t.Fatal("expected error when userinfo response has no subject")
		}

		if !errors.Is(err, ErrUserInfoSubjectMismatch) {
			t.Errorf("expected ErrUserInfoSubjectMismatch, got: %v", err)
		}
	})

	t.Run("userinfo fetch error propagates", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		defer mock.Close()

		mock.userHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("invalid token"))
		}

		config := &UpstreamConfig{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		_, err = provider.UserInfoWithSubjectValidation(ctx, "invalid-token", "user-123")
		if err == nil {
			t.Fatal("expected error for unauthorized response")
		}

		// Should not be a subject mismatch error - should be the underlying fetch error
		if errors.Is(err, ErrUserInfoSubjectMismatch) {
			t.Error("expected underlying fetch error, not subject mismatch")
		}

		if !strings.Contains(err.Error(), "401") {
			t.Errorf("expected error to contain '401', got: %v", err)
		}
	})

	t.Run("empty expected subject with valid userinfo subject returns mismatch", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer()
		defer mock.Close()

		mock.userHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			userInfo := map[string]any{
				"sub":   "user-123",
				"email": "test@example.com",
			}
			if err := json.NewEncoder(w).Encode(userInfo); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := &UpstreamConfig{
			Issuer:       mock.issuer,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		}

		provider, err := NewOIDCProvider(ctx, config)
		if err != nil {
			t.Fatalf("failed to create provider: %v", err)
		}

		// Per OIDC spec, we should always validate - if expected is empty but response has subject,
		// that's a mismatch
		_, err = provider.UserInfoWithSubjectValidation(ctx, "test-access-token", "")
		if err == nil {
			t.Fatal("expected error when expected subject is empty but response has subject")
		}

		if !errors.Is(err, ErrUserInfoSubjectMismatch) {
			t.Errorf("expected ErrUserInfoSubjectMismatch, got: %v", err)
		}
	})
}

func TestOIDCIDPProvider_WithOptions(t *testing.T) {
	t.Parallel()

	mock := newMockOIDCServer()
	defer mock.Close()

	config := &UpstreamConfig{
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

	// Test with force consent screen enabled
	provider, err := NewOIDCProvider(ctx, config, WithForceConsentScreen(true))
	if err != nil {
		t.Fatalf("failed to create provider with force consent screen: %v", err)
	}

	authURL, err := provider.AuthorizationURL("test-state", "", "")
	if err != nil {
		t.Fatalf("failed to build authorization URL: %v", err)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("failed to parse authorization URL: %v", err)
	}

	query := parsed.Query()
	if query.Get("prompt") != "consent" {
		t.Errorf("expected prompt=consent when force consent screen is enabled, got %q", query.Get("prompt"))
	}

	// Test with force consent screen disabled
	provider, err = NewOIDCProvider(ctx, config, WithForceConsentScreen(false))
	if err != nil {
		t.Fatalf("failed to create provider with force consent screen disabled: %v", err)
	}

	authURL, err = provider.AuthorizationURL("test-state", "", "")
	if err != nil {
		t.Fatalf("failed to build authorization URL: %v", err)
	}

	parsed, err = url.Parse(authURL)
	if err != nil {
		t.Fatalf("failed to parse authorization URL: %v", err)
	}

	query = parsed.Query()
	if query.Get("prompt") != "" {
		t.Errorf("expected prompt to be absent when force consent screen is disabled, got %q", query.Get("prompt"))
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
		name, issuer, errorMsg string
		doc                    *OIDCEndpoints
	}{
		{"valid document", "https://example.com", "", &OIDCEndpoints{
			Issuer: "https://example.com", AuthorizationEndpoint: "https://example.com/authorize", TokenEndpoint: "https://example.com/token"}},
		{"valid with optional endpoints", "https://example.com", "", &OIDCEndpoints{
			Issuer: "https://example.com", AuthorizationEndpoint: "https://example.com/authorize", TokenEndpoint: "https://example.com/token",
			UserInfoEndpoint: "https://example.com/userinfo", JWKSEndpoint: "https://example.com/.well-known/jwks.json"}},
		{"missing issuer", "https://example.com", "missing issuer", &OIDCEndpoints{
			AuthorizationEndpoint: "https://example.com/authorize", TokenEndpoint: "https://example.com/token"}},
		{"issuer mismatch", "https://example.com", "issuer mismatch", &OIDCEndpoints{
			Issuer: "https://wrong.com", AuthorizationEndpoint: "https://example.com/authorize", TokenEndpoint: "https://example.com/token"}},
		{"missing authorization endpoint", "https://example.com", "missing authorization_endpoint", &OIDCEndpoints{
			Issuer: "https://example.com", TokenEndpoint: "https://example.com/token"}},
		{"missing token endpoint", "https://example.com", "missing token_endpoint", &OIDCEndpoints{
			Issuer: "https://example.com", AuthorizationEndpoint: "https://example.com/authorize"}},
		{"auth endpoint different host", "https://example.com", "authorization_endpoint origin mismatch", &OIDCEndpoints{
			Issuer: "https://example.com", AuthorizationEndpoint: "https://attacker.com/authorize", TokenEndpoint: "https://example.com/token"}},
		{"token endpoint different host", "https://example.com", "token_endpoint origin mismatch", &OIDCEndpoints{
			Issuer: "https://example.com", AuthorizationEndpoint: "https://example.com/authorize", TokenEndpoint: "https://attacker.com/token"}},
		{"userinfo endpoint different host", "https://example.com", "userinfo_endpoint origin mismatch", &OIDCEndpoints{
			Issuer: "https://example.com", AuthorizationEndpoint: "https://example.com/authorize", TokenEndpoint: "https://example.com/token",
			UserInfoEndpoint: "https://attacker.com/userinfo"}},
		{"jwks_uri different host", "https://example.com", "jwks_uri origin mismatch", &OIDCEndpoints{
			Issuer: "https://example.com", AuthorizationEndpoint: "https://example.com/authorize", TokenEndpoint: "https://example.com/token",
			JWKSEndpoint: "https://attacker.com/.well-known/jwks.json"}},
		{"auth endpoint different scheme", "https://example.com", "authorization_endpoint origin mismatch", &OIDCEndpoints{
			Issuer: "https://example.com", AuthorizationEndpoint: "http://example.com/authorize", TokenEndpoint: "https://example.com/token"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateDiscoveryDocument(tt.doc, tt.issuer)
			if tt.errorMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("expected error containing %q, got: %v", tt.errorMsg, err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func Test_validateEndpointOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, endpoint, issuer, errorMsg string
	}{
		{"same origin HTTPS", "https://example.com/authorize", "https://example.com", ""},
		{"same origin with port", "https://example.com:8443/authorize", "https://example.com:8443", ""},
		{"same origin localhost", "http://localhost:8080/authorize", "http://localhost:8080", ""},
		{"different host", "https://attacker.com/authorize", "https://example.com", "host mismatch"},
		{"different port", "https://example.com:9443/authorize", "https://example.com:8443", "host mismatch"},
		{"different scheme", "http://example.com/authorize", "https://example.com", "scheme mismatch"},
		{"localhost to non-localhost", "http://attacker.com/authorize", "http://localhost:8080", "host mismatch"},
		{"subdomain attack", "https://auth.example.com/authorize", "https://example.com", "host mismatch"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateEndpointOrigin(tt.endpoint, tt.issuer)
			if tt.errorMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("expected error containing %q, got: %v", tt.errorMsg, err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
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

	config := &UpstreamConfig{
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

func TestOIDCIDPProvider_TokenTypeValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tests := []struct {
		tokenType, errorMsg string
	}{
		{"Bearer", ""}, {"bearer", ""}, {"BEARER", ""}, // case-insensitive valid
		{"", "unexpected token_type"}, {"MAC", "unexpected token_type"}, // invalid
	}

	for _, tt := range tests {
		t.Run("token_type_"+tt.tokenType, func(t *testing.T) {
			t.Parallel()
			mock := newMockOIDCServer()
			defer mock.Close()
			mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "test", TokenType: tt.tokenType, ExpiresIn: 3600})
			}
			provider, _ := NewOIDCProvider(ctx, &UpstreamConfig{
				Issuer: mock.issuer, ClientID: "c", ClientSecret: "s", RedirectURI: "http://localhost/cb",
			})
			_, err := provider.ExchangeCode(ctx, "code", "")
			if tt.errorMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("expected error containing %q, got: %v", tt.errorMsg, err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestOIDCIDPProvider_NetworkError(t *testing.T) {
	t.Parallel()

	// Use an invalid URL to simulate network error
	config := &UpstreamConfig{
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

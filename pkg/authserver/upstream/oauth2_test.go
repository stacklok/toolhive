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

package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockOAuth2Server creates a mock OAuth 2.0 server for testing.
type mockOAuth2Server struct {
	*httptest.Server
	authEndpoint string
	tokenHandler func(w http.ResponseWriter, r *http.Request)
}

func newMockOAuth2Server() *mockOAuth2Server {
	mock := &mockOAuth2Server{}

	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", mock.handleAuthorize)
	mux.HandleFunc("/token", mock.handleToken)

	mock.Server = httptest.NewServer(mux)
	mock.authEndpoint = mock.URL + "/authorize"

	return mock
}

func (*mockOAuth2Server) handleAuthorize(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (m *mockOAuth2Server) handleToken(w http.ResponseWriter, r *http.Request) {
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
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func TestNewOAuth2Provider(t *testing.T) {
	t.Parallel()

	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	t.Run("valid config creates provider successfully", func(t *testing.T) {
		t.Parallel()

		localMock := newMockOAuth2Server()
		t.Cleanup(localMock.Close)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
				Scopes:       []string{"read", "write"},
			},
			AuthorizationEndpoint: localMock.URL + "/authorize",
			TokenEndpoint:         localMock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)
		require.NotNil(t, provider)
		assert.Equal(t, ProviderTypeOAuth2, provider.Type())
	})

	t.Run("missing authorization endpoint returns error", func(t *testing.T) {
		t.Parallel()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			TokenEndpoint: mock.URL + "/token",
		}

		_, err := NewOAuth2Provider(config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "authorization_endpoint is required")
	})

	t.Run("missing token endpoint returns error", func(t *testing.T) {
		t.Parallel()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
		}

		_, err := NewOAuth2Provider(config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "token_endpoint is required")
	})

	t.Run("missing client ID returns error", func(t *testing.T) {
		t.Parallel()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		_, err := NewOAuth2Provider(config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "client_id is required")
	})

	t.Run("nil config returns error", func(t *testing.T) {
		t.Parallel()

		_, err := NewOAuth2Provider(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "config is required")
	})

	t.Run("missing client secret returns error", func(t *testing.T) {
		t.Parallel()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:    "test-client",
				RedirectURI: "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		_, err := NewOAuth2Provider(config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "client_secret is required")
	})

	t.Run("missing redirect URI returns error", func(t *testing.T) {
		t.Parallel()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		_, err := NewOAuth2Provider(config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "redirect_uri is required")
	})
}

func TestBaseOAuth2Provider_Type(t *testing.T) {
	t.Parallel()

	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	config := &OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		},
		AuthorizationEndpoint: mock.URL + "/authorize",
		TokenEndpoint:         mock.URL + "/token",
	}

	provider, err := NewOAuth2Provider(config)
	require.NoError(t, err)
	assert.Equal(t, ProviderTypeOAuth2, provider.Type())
}

func TestBaseOAuth2Provider_AuthorizationURL(t *testing.T) {
	t.Parallel()

	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	config := &OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
			Scopes:       []string{"read", "write"},
		},
		AuthorizationEndpoint: mock.URL + "/authorize",
		TokenEndpoint:         mock.URL + "/token",
	}

	provider, err := NewOAuth2Provider(config)
	require.NoError(t, err)

	t.Run("builds correct URL with all parameters", func(t *testing.T) {
		t.Parallel()

		authURL, err := provider.AuthorizationURL("test-state", "")
		require.NoError(t, err)

		parsed, err := url.Parse(authURL)
		require.NoError(t, err)

		query := parsed.Query()
		assert.Equal(t, "code", query.Get("response_type"))
		assert.Equal(t, "test-client", query.Get("client_id"))
		assert.Equal(t, "http://localhost:8080/callback", query.Get("redirect_uri"))
		assert.Equal(t, "test-state", query.Get("state"))
		assert.Equal(t, "read write", query.Get("scope"))
	})

	t.Run("includes PKCE code_challenge when provided", func(t *testing.T) {
		t.Parallel()

		authURL, err := provider.AuthorizationURL("test-state", "test-challenge-abc123")
		require.NoError(t, err)

		parsed, err := url.Parse(authURL)
		require.NoError(t, err)

		query := parsed.Query()
		assert.Equal(t, "test-challenge-abc123", query.Get("code_challenge"))
		assert.Equal(t, pkceChallengeMethodS256, query.Get("code_challenge_method"))
	})

	t.Run("handles WithAdditionalParams option", func(t *testing.T) {
		t.Parallel()

		authURL, err := provider.AuthorizationURL("test-state", "",
			WithAdditionalParams(map[string]string{
				"audience":     "https://api.example.com",
				"login_hint":   "user@example.com",
				"custom_param": "custom_value",
			}))
		require.NoError(t, err)

		parsed, err := url.Parse(authURL)
		require.NoError(t, err)

		query := parsed.Query()
		assert.Equal(t, "https://api.example.com", query.Get("audience"))
		assert.Equal(t, "user@example.com", query.Get("login_hint"))
		assert.Equal(t, "custom_value", query.Get("custom_param"))
	})

	t.Run("returns error for empty state", func(t *testing.T) {
		t.Parallel()

		_, err := provider.AuthorizationURL("", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "state parameter is required")
	})

	t.Run("does not include code_challenge when not provided", func(t *testing.T) {
		t.Parallel()

		authURL, err := provider.AuthorizationURL("test-state", "")
		require.NoError(t, err)

		parsed, err := url.Parse(authURL)
		require.NoError(t, err)

		query := parsed.Query()
		assert.Empty(t, query.Get("code_challenge"))
		assert.Empty(t, query.Get("code_challenge_method"))
	})
}

func TestBaseOAuth2Provider_AuthorizationURL_NoScopes(t *testing.T) {
	t.Parallel()

	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	// Config without scopes
	config := &OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		},
		AuthorizationEndpoint: mock.URL + "/authorize",
		TokenEndpoint:         mock.URL + "/token",
	}

	provider, err := NewOAuth2Provider(config)
	require.NoError(t, err)

	authURL, err := provider.AuthorizationURL("test-state", "")
	require.NoError(t, err)

	parsed, err := url.Parse(authURL)
	require.NoError(t, err)

	query := parsed.Query()
	// Scope parameter should not be present if no scopes configured
	assert.Empty(t, query.Get("scope"))
}

func TestBaseOAuth2Provider_ExchangeCode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful token exchange", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		var receivedParams url.Values
		mock.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
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
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		tokens, err := provider.ExchangeCode(ctx, "test-auth-code", "test-verifier")
		require.NoError(t, err)

		// Verify request parameters
		assert.Equal(t, "authorization_code", receivedParams.Get("grant_type"))
		assert.Equal(t, "test-auth-code", receivedParams.Get("code"))
		assert.Equal(t, "test-verifier", receivedParams.Get("code_verifier"))
		assert.Equal(t, "test-client", receivedParams.Get("client_id"))
		assert.Equal(t, "test-secret", receivedParams.Get("client_secret"))
		assert.Equal(t, "http://localhost:8080/callback", receivedParams.Get("redirect_uri"))

		// Verify response
		assert.Equal(t, "exchanged-access-token", tokens.AccessToken)
		assert.Equal(t, "exchanged-refresh-token", tokens.RefreshToken)

		// Verify expiration is set approximately correctly
		expectedExpiry := time.Now().Add(7200 * time.Second)
		assert.WithinDuration(t, expectedExpiry, tokens.ExpiresAt, 10*time.Second)
	})

	t.Run("handles error response from token endpoint", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
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

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCode(ctx, "expired-code", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid_grant")
		assert.Contains(t, err.Error(), "authorization code has expired")
	})

	t.Run("network error handling", func(t *testing.T) {
		t.Parallel()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: "http://localhost:1/authorize",
			TokenEndpoint:         "http://localhost:1/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err = provider.ExchangeCode(ctx, "test-code", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "token request failed")
	})

	t.Run("empty code returns error", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCode(ctx, "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "authorization code is required")
	})

	t.Run("code exchange without verifier omits code_verifier param", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		var receivedParams url.Values
		mock.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			receivedParams = r.PostForm

			w.Header().Set("Content-Type", "application/json")
			resp := tokenResponse{
				AccessToken: "token",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCode(ctx, "test-code", "")
		require.NoError(t, err)

		assert.Empty(t, receivedParams.Get("code_verifier"))
	})

	t.Run("missing access_token in response returns error", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			resp := tokenResponse{
				TokenType: "Bearer",
				ExpiresIn: 3600,
				// AccessToken intentionally missing
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCode(ctx, "test-code", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing access_token")
	})

	t.Run("invalid token_type returns error", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			resp := tokenResponse{
				AccessToken: "token",
				TokenType:   "MAC", // Invalid type
				ExpiresIn:   3600,
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCode(ctx, "test-code", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected token_type")
	})

	t.Run("default expiry when not specified", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			resp := tokenResponse{
				AccessToken: "token",
				TokenType:   "Bearer",
				// ExpiresIn intentionally missing
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		tokens, err := provider.ExchangeCode(ctx, "test-code", "")
		require.NoError(t, err)

		// Should default to 1 hour
		expectedExpiry := time.Now().Add(time.Hour)
		assert.WithinDuration(t, expectedExpiry, tokens.ExpiresAt, 10*time.Second)
	})
}

func TestBaseOAuth2Provider_RefreshTokens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful token refresh", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

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

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		tokens, err := provider.RefreshTokens(ctx, "old-refresh-token")
		require.NoError(t, err)

		// Verify request parameters
		assert.Equal(t, "refresh_token", receivedParams.Get("grant_type"))
		assert.Equal(t, "old-refresh-token", receivedParams.Get("refresh_token"))
		assert.Equal(t, "test-client", receivedParams.Get("client_id"))
		assert.Equal(t, "test-secret", receivedParams.Get("client_secret"))

		// Verify response
		assert.Equal(t, "refreshed-access-token", tokens.AccessToken)
		assert.Equal(t, "new-refresh-token", tokens.RefreshToken)
	})

	t.Run("error response from token endpoint", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			resp := tokenErrorResponse{
				Error:            "invalid_grant",
				ErrorDescription: "The refresh token has expired",
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.RefreshTokens(ctx, "expired-refresh-token")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid_grant")
	})

	t.Run("empty refresh token returns error", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.RefreshTokens(ctx, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "refresh token is required")
	})

	t.Run("server error response", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.RefreshTokens(ctx, "refresh-token")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "token request failed")
	})
}

func TestBaseOAuth2Provider_WithOAuth2HTTPClient(t *testing.T) {
	t.Parallel()

	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	config := &OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		},
		AuthorizationEndpoint: mock.URL + "/authorize",
		TokenEndpoint:         mock.URL + "/token",
	}

	customClient := &http.Client{Timeout: 5 * time.Second}

	provider, err := NewOAuth2Provider(config, WithOAuth2HTTPClient(customClient))
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Verify the provider works with custom client
	ctx := context.Background()
	tokens, err := provider.ExchangeCode(ctx, "test-code", "")
	require.NoError(t, err)
	assert.NotEmpty(t, tokens.AccessToken)
}

func TestBaseOAuth2Provider_TokenTypeValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	tests := []struct {
		name      string
		tokenType string
		wantErr   bool
		errMsg    string
	}{
		{"valid Bearer", "Bearer", false, ""},
		{"valid bearer lowercase", "bearer", false, ""},
		{"valid BEARER uppercase", "BEARER", false, ""},
		{"invalid empty", "", true, "unexpected token_type"},
		{"invalid MAC", "MAC", true, "unexpected token_type"},
		{"invalid Basic", "Basic", true, "unexpected token_type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := newMockOAuth2Server()
			t.Cleanup(mock.Close)

			mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				resp := tokenResponse{
					AccessToken: "test-token",
					TokenType:   tt.tokenType,
					ExpiresIn:   3600,
				}
				if err := json.NewEncoder(w).Encode(resp); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
			}

			config := &OAuth2Config{
				CommonOAuthConfig: CommonOAuthConfig{
					ClientID:     "test-client",
					ClientSecret: "test-secret",
					RedirectURI:  "http://localhost:8080/callback",
				},
				AuthorizationEndpoint: mock.URL + "/authorize",
				TokenEndpoint:         mock.URL + "/token",
			}

			provider, err := NewOAuth2Provider(config)
			require.NoError(t, err)

			_, err = provider.ExchangeCode(ctx, "test-code", "")
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBaseOAuth2Provider_NonJSONErrorResponse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Not JSON error"))
	}

	config := &OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		},
		AuthorizationEndpoint: mock.URL + "/authorize",
		TokenEndpoint:         mock.URL + "/token",
	}

	provider, err := NewOAuth2Provider(config)
	require.NoError(t, err)

	_, err = provider.ExchangeCode(ctx, "test-code", "")
	require.Error(t, err)
	// Should contain status code in sanitized error
	assert.Contains(t, err.Error(), "400")
	// Should not contain the raw error body for security
	assert.NotContains(t, err.Error(), "Not JSON error")
}

func TestBaseOAuth2Provider_InvalidJSONResponse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not valid json"))
	}

	config := &OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		},
		AuthorizationEndpoint: mock.URL + "/authorize",
		TokenEndpoint:         mock.URL + "/token",
	}

	provider, err := NewOAuth2Provider(config)
	require.NoError(t, err)

	_, err = provider.ExchangeCode(ctx, "test-code", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse token response")
}

func TestBaseOAuth2Provider_ContentTypeHeaders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	var receivedContentType string
	var receivedAccept string
	mock.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		receivedAccept = r.Header.Get("Accept")

		w.Header().Set("Content-Type", "application/json")
		resp := tokenResponse{
			AccessToken: "token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}

	config := &OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		},
		AuthorizationEndpoint: mock.URL + "/authorize",
		TokenEndpoint:         mock.URL + "/token",
	}

	provider, err := NewOAuth2Provider(config)
	require.NoError(t, err)

	_, err = provider.ExchangeCode(ctx, "test-code", "")
	require.NoError(t, err)

	assert.Equal(t, "application/x-www-form-urlencoded", receivedContentType)
	assert.Equal(t, "application/json", receivedAccept)
}

func TestBaseOAuth2Provider_IDToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := tokenResponse{
			AccessToken:  "access-token",
			TokenType:    "Bearer",
			RefreshToken: "refresh-token",
			IDToken:      "test-id-token.payload.signature",
			ExpiresIn:    3600,
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}

	config := &OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURI:  "http://localhost:8080/callback",
		},
		AuthorizationEndpoint: mock.URL + "/authorize",
		TokenEndpoint:         mock.URL + "/token",
	}

	provider, err := NewOAuth2Provider(config)
	require.NoError(t, err)

	tokens, err := provider.ExchangeCode(ctx, "test-code", "")
	require.NoError(t, err)

	// OAuth2 providers can also return ID tokens if they support hybrid flows
	assert.Equal(t, "test-id-token.payload.signature", tokens.IDToken)
}

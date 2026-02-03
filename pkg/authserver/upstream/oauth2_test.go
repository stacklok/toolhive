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
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testTokenResponse is a test helper to produce token responses.
type testTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// testTokenErrorResponse is a test helper for OAuth error responses.
type testTokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
	ErrorURI         string `json:"error_uri,omitempty"`
}

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
	resp := testTokenResponse{
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

	t.Run("public client without client_secret is valid", func(t *testing.T) {
		t.Parallel()

		localMock := newMockOAuth2Server()
		t.Cleanup(localMock.Close)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:    "public-client",
				RedirectURI: "http://localhost:8080/callback",
				Scopes:      []string{"openid"},
			},
			AuthorizationEndpoint: localMock.URL + "/authorize",
			TokenEndpoint:         localMock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)
		require.NotNil(t, provider)
		assert.Equal(t, ProviderTypeOAuth2, provider.Type())
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
		assert.Equal(t, "S256", query.Get("code_challenge_method"))
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
			resp := testTokenResponse{
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
			resp := testTokenErrorResponse{
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
		assert.Contains(t, err.Error(), "request failed")
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
			resp := testTokenResponse{
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
			resp := testTokenResponse{
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
			resp := testTokenResponse{
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
			resp := testTokenResponse{
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
			resp := testTokenResponse{
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
			resp := testTokenErrorResponse{
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
				resp := testTokenResponse{
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

func TestBaseOAuth2Provider_IDToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := testTokenResponse{
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

func Test_validateRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		uri         string
		wantErr     bool
		errContains string
	}{
		// Valid HTTPS URIs
		{"HTTPS with path", "https://auth.example.com/oauth/callback", false, ""},
		{"HTTPS with port", "https://auth.example.com:8443/oauth/callback", false, ""},
		{"HTTPS without path", "https://example.com", false, ""},

		// Valid HTTP loopback URIs
		{"HTTP localhost", "http://localhost/callback", false, ""},
		{"HTTP localhost with port", "http://localhost:8080/callback", false, ""},
		{"HTTP 127.0.0.1", "http://127.0.0.1/callback", false, ""},
		{"HTTP 127.0.0.1 with port", "http://127.0.0.1:8080/callback", false, ""},
		{"HTTP IPv6 ::1", "http://[::1]/callback", false, ""},
		{"HTTP IPv6 ::1 with port", "http://[::1]:8080/callback", false, ""},

		// Invalid: HTTP to non-loopback
		{"HTTP non-loopback hostname", "http://example.com/callback", true, "redirect_uri must use http (for loopback) or https scheme"},
		{"HTTP non-loopback hostname with port", "http://example.com:8080/callback", true, "redirect_uri must use http (for loopback) or https scheme"},
		{"HTTP non-loopback IP", "http://192.168.1.1/callback", true, "redirect_uri must use http (for loopback) or https scheme"},

		// Invalid: fragment, scheme, relative, empty
		{"URI with fragment", "https://example.com/callback#section", true, "redirect_uri must be an absolute URI without a fragment"},
		{"FTP scheme", "ftp://example.com/callback", true, "redirect_uri must use http (for loopback) or https scheme"},
		{"relative URI", "/oauth/callback", true, "redirect_uri must be an absolute URI without a fragment"},
		{"empty URI", "", true, "redirect_uri must be an absolute URI without a fragment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateRedirectURI(tt.uri)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBaseOAuth2Provider_ResolveIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Helper to create a minimal token server (OAuth endpoints not used for userinfo tests)
	newTokenServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	}

	t.Run("nil tokens returns ErrIdentityResolutionFailed", func(t *testing.T) {
		t.Parallel()

		tokenServer := newTokenServer()
		defer tokenServer.Close()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-123"})
		}))
		defer userInfoServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		subject, err := provider.ResolveIdentity(ctx, nil, "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrIdentityResolutionFailed))
		assert.Empty(t, subject)
	})

	t.Run("successful resolution", func(t *testing.T) {
		t.Parallel()

		tokenServer := newTokenServer()
		defer tokenServer.Close()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-123"})
		}))
		defer userInfoServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		tokens := &Tokens{
			AccessToken: "valid-access-token",
		}
		subject, err := provider.ResolveIdentity(ctx, tokens, "")
		require.NoError(t, err)
		assert.Equal(t, "user-123", subject)
	})

	t.Run("server returns 401", func(t *testing.T) {
		t.Parallel()

		tokenServer := newTokenServer()
		defer tokenServer.Close()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer userInfoServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		tokens := &Tokens{
			AccessToken: "invalid-access-token",
		}
		subject, err := provider.ResolveIdentity(ctx, tokens, "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrIdentityResolutionFailed))
		assert.Contains(t, err.Error(), "401")
		assert.Empty(t, subject)
	})

	t.Run("missing subject in response", func(t *testing.T) {
		t.Parallel()

		tokenServer := newTokenServer()
		defer tokenServer.Close()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "Test"})
		}))
		defer userInfoServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		tokens := &Tokens{
			AccessToken: "valid-access-token",
		}
		subject, err := provider.ResolveIdentity(ctx, tokens, "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrIdentityResolutionFailed))
		assert.Empty(t, subject)
	})

	t.Run("empty subject in response", func(t *testing.T) {
		t.Parallel()

		tokenServer := newTokenServer()
		defer tokenServer.Close()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": ""})
		}))
		defer userInfoServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		tokens := &Tokens{
			AccessToken: "valid-access-token",
		}
		subject, err := provider.ResolveIdentity(ctx, tokens, "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrIdentityResolutionFailed))
		assert.Empty(t, subject)
	})
}

func TestBaseOAuth2Provider_FetchUserInfo(t *testing.T) {
	t.Parallel()

	// Helper to create a minimal token server (OAuth endpoints not used for userinfo tests)
	newTokenServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	}

	t.Run("error cases without server", func(t *testing.T) {
		t.Parallel()

		tokenServer := newTokenServer()
		defer tokenServer.Close()

		tests := []struct {
			name        string
			userInfo    *UserInfoConfig
			accessToken string
			wantErr     string
		}{
			{
				name:        "not configured",
				userInfo:    nil,
				accessToken: "test-token",
				wantErr:     "userinfo endpoint not configured",
			},
			{
				name:        "empty access token",
				userInfo:    &UserInfoConfig{EndpointURL: "http://localhost/userinfo"},
				accessToken: "",
				wantErr:     "access token is required",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				config := &OAuth2Config{
					CommonOAuthConfig: CommonOAuthConfig{
						ClientID:     "test-client",
						ClientSecret: "test-secret",
						RedirectURI:  "http://localhost:8080/callback",
					},
					AuthorizationEndpoint: tokenServer.URL + "/authorize",
					TokenEndpoint:         tokenServer.URL + "/token",
					UserInfo:              tt.userInfo,
				}

				provider, err := NewOAuth2Provider(config)
				require.NoError(t, err)

				_, err = provider.FetchUserInfo(context.Background(), tt.accessToken)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			})
		}
	})

	t.Run("server response cases", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name         string
			serverResp   map[string]any
			serverStatus int
			fieldMapping *UserInfoFieldMapping
			wantSubject  string
			wantErr      string
		}{
			{
				name:         "successful with default sub field",
				serverResp:   map[string]any{"sub": "user-123", "name": "Test User", "email": "test@example.com"},
				serverStatus: http.StatusOK,
				wantSubject:  "user-123",
			},
			{
				name:         "custom subject field (numeric ID)",
				serverResp:   map[string]any{"id": float64(12345), "login": "octocat"},
				serverStatus: http.StatusOK,
				fieldMapping: &UserInfoFieldMapping{SubjectFields: []string{"id"}},
				wantSubject:  "12345",
			},
			{
				name:         "server returns 401",
				serverStatus: http.StatusUnauthorized,
				wantErr:      "status 401",
			},
			{
				name:         "missing subject claim",
				serverResp:   map[string]any{"name": "No Subject", "email": "nosub@example.com"},
				serverStatus: http.StatusOK,
				wantErr:      "missing required subject claim",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tt.serverStatus)
					if tt.serverResp != nil {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(tt.serverResp)
					}
				}))
				defer userInfoServer.Close()

				tokenServer := newTokenServer()
				defer tokenServer.Close()

				config := &OAuth2Config{
					CommonOAuthConfig: CommonOAuthConfig{
						ClientID:     "test-client",
						ClientSecret: "test-secret",
						RedirectURI:  "http://localhost:8080/callback",
					},
					AuthorizationEndpoint: tokenServer.URL + "/authorize",
					TokenEndpoint:         tokenServer.URL + "/token",
					UserInfo: &UserInfoConfig{
						EndpointURL:  userInfoServer.URL,
						FieldMapping: tt.fieldMapping,
					},
				}

				provider, err := NewOAuth2Provider(config)
				require.NoError(t, err)

				userInfo, err := provider.FetchUserInfo(context.Background(), "test-access-token")
				if tt.wantErr != "" {
					require.Error(t, err)
					assert.Contains(t, err.Error(), tt.wantErr)
					return
				}

				require.NoError(t, err)
				assert.Equal(t, tt.wantSubject, userInfo.Subject)
			})
		}
	})

	t.Run("additional headers are sent", func(t *testing.T) {
		t.Parallel()

		var receivedHeaders http.Header
		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedHeaders = r.Header.Clone()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-123"})
		}))
		defer userInfoServer.Close()

		tokenServer := newTokenServer()
		defer tokenServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
				AdditionalHeaders: map[string]string{
					"X-GitHub-Api-Version": "2022-11-28",
					"Accept":               "application/vnd.github+json",
				},
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.FetchUserInfo(context.Background(), "test-access-token")
		require.NoError(t, err)

		assert.Equal(t, "2022-11-28", receivedHeaders.Get("X-GitHub-Api-Version"))
		assert.Equal(t, "application/vnd.github+json", receivedHeaders.Get("Accept"))
	})
}

func TestBaseOAuth2Provider_FetchUserInfo_FieldMapping(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful userinfo request with default fields", func(t *testing.T) {
		t.Parallel()

		var receivedAuth string
		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"sub":   "user-123",
				"name":  "Test User",
				"email": "test@example.com",
			}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userInfoServer.Close()

		// Create a simple mock for token endpoint (not used in this test)
		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer tokenServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		userInfo, err := provider.FetchUserInfo(ctx, "test-access-token")
		require.NoError(t, err)

		assert.Equal(t, "Bearer test-access-token", receivedAuth)
		assert.Equal(t, "user-123", userInfo.Subject)
		assert.Equal(t, "Test User", userInfo.Name)
		assert.Equal(t, "test@example.com", userInfo.Email)
	})

	t.Run("userinfo with custom field mapping", func(t *testing.T) {
		t.Parallel()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Simulate GitHub-like response
			resp := map[string]any{
				"id":    float64(12345),
				"login": "octocat",
				"name":  "The Octocat",
				"email": "octocat@github.com",
			}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userInfoServer.Close()

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer tokenServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
				FieldMapping: &UserInfoFieldMapping{
					SubjectFields: []string{"id", "login"},
					NameFields:    []string{"name", "login"},
					EmailFields:   []string{"email"},
				},
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		userInfo, err := provider.FetchUserInfo(ctx, "test-access-token")
		require.NoError(t, err)

		assert.Equal(t, "12345", userInfo.Subject) // Numeric ID converted to string
		assert.Equal(t, "The Octocat", userInfo.Name)
		assert.Equal(t, "octocat@github.com", userInfo.Email)
	})

	t.Run("userinfo with additional headers", func(t *testing.T) {
		t.Parallel()

		var receivedHeaders http.Header
		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedHeaders = r.Header.Clone()
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{"sub": "user-123"}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userInfoServer.Close()

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer tokenServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
				AdditionalHeaders: map[string]string{
					"X-GitHub-Api-Version": "2022-11-28",
					"Accept":               "application/vnd.github+json",
				},
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.FetchUserInfo(ctx, "test-access-token")
		require.NoError(t, err)

		assert.Equal(t, "2022-11-28", receivedHeaders.Get("X-GitHub-Api-Version"))
		assert.Equal(t, "application/vnd.github+json", receivedHeaders.Get("Accept"))
	})

	t.Run("userinfo with POST method", func(t *testing.T) {
		t.Parallel()

		var receivedMethod string
		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedMethod = r.Method
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{"sub": "user-123"}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userInfoServer.Close()

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer tokenServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
				HTTPMethod:  http.MethodPost,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		userInfo, err := provider.FetchUserInfo(ctx, "test-access-token")
		require.NoError(t, err)

		assert.Equal(t, http.MethodPost, receivedMethod)
		assert.Equal(t, "user-123", userInfo.Subject)
	})

	t.Run("userinfo not configured returns error", func(t *testing.T) {
		t.Parallel()

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer tokenServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			// No UserInfo configured
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.FetchUserInfo(ctx, "test-access-token")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "userinfo endpoint not configured")
	})

	t.Run("userinfo without access token fails", func(t *testing.T) {
		t.Parallel()

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer tokenServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: "http://localhost/userinfo",
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.FetchUserInfo(ctx, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "access token is required")
	})

	t.Run("userinfo server error returns error", func(t *testing.T) {
		t.Parallel()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer userInfoServer.Close()

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer tokenServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.FetchUserInfo(ctx, "test-access-token")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status 401")
	})

	t.Run("userinfo missing subject returns error", func(t *testing.T) {
		t.Parallel()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"name":  "No Subject User",
				"email": "nosub@example.com",
			}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userInfoServer.Close()

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer tokenServer.Close()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenServer.URL + "/authorize",
			TokenEndpoint:         tokenServer.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.FetchUserInfo(ctx, "test-access-token")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing required subject claim")
	})
}

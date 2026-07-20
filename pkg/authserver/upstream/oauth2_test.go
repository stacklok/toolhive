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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/oauthproto"
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

	t.Run("unsupported token endpoint auth method returns error", func(t *testing.T) {
		t.Parallel()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint:   mock.URL + "/authorize",
			TokenEndpoint:           mock.URL + "/token",
			TokenEndpointAuthMethod: "private_key_jwt",
		}

		_, err := NewOAuth2Provider(config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "private_key_jwt",
			"construction must fail loudly on a method this client cannot fulfil")
	})
}

// TestAuthStyleFromMethod pins the mapping from RFC 7591
// token_endpoint_auth_method values to oauth2.AuthStyle. Recognised methods
// (basic/post/none) and the unset case resolve to a definite style; an
// unrecognised, non-empty method must be rejected (fail loudly) rather than
// silently degrading to the POST-body default.
func TestAuthStyleFromMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		want    oauth2.AuthStyle
		wantErr bool
	}{
		{"client_secret_basic maps to header", oauthproto.TokenEndpointAuthMethodClientSecretBasic, oauth2.AuthStyleInHeader, false},
		{"client_secret_post maps to params", oauthproto.TokenEndpointAuthMethodClientSecretPost, oauth2.AuthStyleInParams, false},
		{"none maps to params", oauthproto.TokenEndpointAuthMethodNone, oauth2.AuthStyleInParams, false},
		{"unset maps to params", "", oauth2.AuthStyleInParams, false},
		{"private_key_jwt is rejected", "private_key_jwt", oauth2.AuthStyleAutoDetect, true},
		{"unknown method is rejected", "totally_bogus", oauth2.AuthStyleAutoDetect, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := authStyleFromMethod(tt.method)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.method,
					"error must name the offending method for operator diagnosis")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestNewOAuth2Provider_TokenEndpointAuthMethod verifies that the negotiated
// token_endpoint_auth_method controls how client credentials are presented at
// the token endpoint. This is the regression guard for issue #5865: a
// DCR-registered client whose upstream negotiated client_secret_basic must send
// its credentials in the HTTP Basic Authorization header, not the POST body, or
// strict authorization servers (e.g. Ory Hydra) reject the exchange with
// invalid_client.
func TestNewOAuth2Provider_TokenEndpointAuthMethod(t *testing.T) {
	t.Parallel()

	const (
		clientID     = "dcr-client"
		clientSecret = "dcr-secret"
	)

	tests := []struct {
		name          string
		authMethod    string
		wantAuthStyle oauth2.AuthStyle
		wantBasicAuth bool // credentials expected in the Authorization header
	}{
		{
			name:          "client_secret_basic sends credentials in Basic header",
			authMethod:    oauthproto.TokenEndpointAuthMethodClientSecretBasic,
			wantAuthStyle: oauth2.AuthStyleInHeader,
			wantBasicAuth: true,
		},
		{
			name:          "client_secret_post sends credentials in body",
			authMethod:    oauthproto.TokenEndpointAuthMethodClientSecretPost,
			wantAuthStyle: oauth2.AuthStyleInParams,
			wantBasicAuth: false,
		},
		{
			name:          "unset defaults to credentials in body",
			authMethod:    "",
			wantAuthStyle: oauth2.AuthStyleInParams,
			wantBasicAuth: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := newMockOAuth2Server()
			t.Cleanup(mock.Close)

			var (
				gotUser, gotPass string
				gotBasicAuth     bool
				gotBodySecret    string
			)
			mock.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
				gotUser, gotPass, gotBasicAuth = r.BasicAuth()
				if err := r.ParseForm(); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				gotBodySecret = r.PostForm.Get("client_secret")

				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(testTokenResponse{
					AccessToken: "access-token",
					TokenType:   "Bearer",
				}); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
			}

			config := &OAuth2Config{
				CommonOAuthConfig: CommonOAuthConfig{
					ClientID:     clientID,
					ClientSecret: clientSecret,
					RedirectURI:  "http://localhost:8080/callback",
				},
				AuthorizationEndpoint:   mock.URL + "/authorize",
				TokenEndpoint:           mock.URL + "/token",
				TokenEndpointAuthMethod: tt.authMethod,
			}

			provider, err := NewOAuth2Provider(config)
			require.NoError(t, err)
			assert.Equal(t, tt.wantAuthStyle, provider.oauth2Config.Endpoint.AuthStyle,
				"oauth2 endpoint auth style must reflect the negotiated method")

			_, err = provider.exchangeCodeForTokens(context.Background(), "auth-code", "")
			require.NoError(t, err)

			if tt.wantBasicAuth {
				require.True(t, gotBasicAuth, "credentials must be sent in the Basic Authorization header")
				assert.Equal(t, clientID, gotUser)
				assert.Equal(t, clientSecret, gotPass)
				assert.Empty(t, gotBodySecret, "client_secret must not also appear in the POST body")
			} else {
				assert.False(t, gotBasicAuth, "no Basic Authorization header expected")
				assert.Equal(t, clientSecret, gotBodySecret, "client_secret must be sent in the POST body")
			}
		})
	}
}

// TestBaseOAuth2Provider_RefreshTokens_TokenEndpointAuthMethod verifies that
// the negotiated auth method also governs the refresh path. RefreshTokens
// shares the same oauth2.Config.Endpoint.AuthStyle as code exchange, so a
// client_secret_basic client must present its credentials in the Basic
// Authorization header on refresh too — this pins the refresh half of the
// contract documented on authStyleFromMethod so a future change that rebuilds
// the config or bypasses AuthStyle in RefreshTokens does not ship unnoticed.
func TestBaseOAuth2Provider_RefreshTokens_TokenEndpointAuthMethod(t *testing.T) {
	t.Parallel()

	const (
		clientID     = "dcr-client"
		clientSecret = "dcr-secret"
	)

	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	var (
		gotUser, gotPass string
		gotBasicAuth     bool
		gotBodySecret    string
	)
	mock.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotBasicAuth = r.BasicAuth()
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotBodySecret = r.PostForm.Get("client_secret")

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(testTokenResponse{
			AccessToken: "refreshed-access-token",
			TokenType:   "Bearer",
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}

	config := &OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURI:  "http://localhost:8080/callback",
		},
		AuthorizationEndpoint:   mock.URL + "/authorize",
		TokenEndpoint:           mock.URL + "/token",
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretBasic,
	}

	provider, err := NewOAuth2Provider(config)
	require.NoError(t, err)

	_, err = provider.RefreshTokens(context.Background(), "some-refresh-token", "")
	require.NoError(t, err)

	require.True(t, gotBasicAuth, "refresh must send credentials in the Basic Authorization header")
	assert.Equal(t, clientID, gotUser)
	assert.Equal(t, clientSecret, gotPass)
	assert.Empty(t, gotBodySecret, "client_secret must not also appear in the refresh POST body")
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

func TestBaseOAuth2Provider_exchangeCodeForTokens(t *testing.T) {
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

		result, err := provider.exchangeCodeForTokens(ctx, "test-auth-code", "test-verifier")
		require.NoError(t, err)

		// Verify request parameters
		assert.Equal(t, "authorization_code", receivedParams.Get("grant_type"))
		assert.Equal(t, "test-auth-code", receivedParams.Get("code"))
		assert.Equal(t, "test-verifier", receivedParams.Get("code_verifier"))
		assert.Equal(t, "test-client", receivedParams.Get("client_id"))
		assert.Equal(t, "test-secret", receivedParams.Get("client_secret"))
		assert.Equal(t, "http://localhost:8080/callback", receivedParams.Get("redirect_uri"))

		// Verify response
		assert.Equal(t, "exchanged-access-token", result.tokens.AccessToken)
		assert.Equal(t, "exchanged-refresh-token", result.tokens.RefreshToken)

		// Verify expiration is set approximately correctly
		expectedExpiry := time.Now().Add(7200 * time.Second)
		assert.WithinDuration(t, expectedExpiry, result.tokens.ExpiresAt, 10*time.Second)
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

		_, err = provider.exchangeCodeForTokens(ctx, "expired-code", "")
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

		_, err = provider.exchangeCodeForTokens(ctx, "test-code", "")
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

		_, err = provider.exchangeCodeForTokens(ctx, "", "")
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

		_, err = provider.exchangeCodeForTokens(ctx, "test-code", "")
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

		_, err = provider.exchangeCodeForTokens(ctx, "test-code", "")
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

		_, err = provider.exchangeCodeForTokens(ctx, "test-code", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected token_type")
	})

	t.Run("zero expiry when expires_in absent", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			resp := testTokenResponse{
				AccessToken: "token",
				TokenType:   "Bearer",
				// ExpiresIn intentionally missing — provider issues a non-expiring token
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

		result, err := provider.exchangeCodeForTokens(ctx, "test-code", "")
		require.NoError(t, err)

		// No expires_in in the response means the token has no expiry.
		assert.True(t, result.tokens.ExpiresAt.IsZero(), "ExpiresAt should be zero for non-expiring tokens")
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

		tokens, err := provider.RefreshTokens(ctx, "old-refresh-token", "")
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

		_, err = provider.RefreshTokens(ctx, "expired-refresh-token", "")
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

		_, err = provider.RefreshTokens(ctx, "", "")
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

		_, err = provider.RefreshTokens(ctx, "refresh-token", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "token request failed")
	})

	t.Run("refresh request includes configured scopes", func(t *testing.T) {
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
				Scopes: []string{
					"openid",
					"profile",
					"api://example.com/custom:scope",
				},
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.RefreshTokens(ctx, "old-refresh-token", "")
		require.NoError(t, err)

		// Scope must be sent on refresh; some ASes drop custom scopes otherwise.
		sentScopes := strings.Fields(receivedParams.Get("scope"))
		assert.ElementsMatch(t,
			[]string{"openid", "profile", "api://example.com/custom:scope"},
			sentScopes,
			"refresh request must include the configured scope set verbatim",
		)
	})

	t.Run("refresh preserves existing refresh_token when AS does not issue a new one", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Response omits refresh_token (allowed by RFC 6749 §6).
			resp := testTokenResponse{
				AccessToken: "refreshed-access-token",
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

		tokens, err := provider.RefreshTokens(ctx, "still-valid-refresh-token", "")
		require.NoError(t, err)

		assert.Equal(t, "refreshed-access-token", tokens.AccessToken)
		assert.Equal(t, "still-valid-refresh-token", tokens.RefreshToken,
			"original refresh token must be preserved when response omits one")
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
	result, err := provider.exchangeCodeForTokens(ctx, "test-code", "")
	require.NoError(t, err)
	assert.NotEmpty(t, result.tokens.AccessToken)
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

			_, err = provider.exchangeCodeForTokens(ctx, "test-code", "")
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

	_, err = provider.exchangeCodeForTokens(ctx, "test-code", "")
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

	result, err := provider.exchangeCodeForTokens(ctx, "test-code", "")
	require.NoError(t, err)

	// OAuth2 providers can also return ID tokens if they support hybrid flows
	assert.Equal(t, "test-id-token.payload.signature", result.tokens.IDToken)
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

func TestBaseOAuth2Provider_ExchangeCodeForIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful exchange and identity resolution", func(t *testing.T) {
		t.Parallel()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-123"})
		}))
		defer userInfoServer.Close()

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
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		result, err := provider.ExchangeCodeForIdentity(ctx, "test-code", "", "ignored-nonce")
		require.NoError(t, err)
		assert.Equal(t, "user-123", result.Subject)
		assert.NotEmpty(t, result.Tokens.AccessToken)
	})

	t.Run("userinfo server returns 401", func(t *testing.T) {
		t.Parallel()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer userInfoServer.Close()

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
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrIdentityResolutionFailed))
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("missing subject in userinfo response", func(t *testing.T) {
		t.Parallel()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "Test"})
		}))
		defer userInfoServer.Close()

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
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoServer.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrIdentityResolutionFailed))
	})

	t.Run("token exchange failure", func(t *testing.T) {
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
			UserInfo: &UserInfoConfig{
				EndpointURL: "http://localhost/userinfo",
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCodeForIdentity(ctx, "expired-code", "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid_grant")
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
			UserInfo: &UserInfoConfig{
				EndpointURL: "http://localhost/userinfo",
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCodeForIdentity(ctx, "", "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "authorization code is required")
	})

	// When UserInfo is nil, ExchangeCodeForIdentity must synthesize Subject
	// from the access token. The prefix-tagged Subject + empty Name/Email
	// are the observable signals that the synthesis branch ran — the
	// userinfo path populates Name/Email and would never emit a "tk-…" sub.
	t.Run("synthesizes identity when UserInfo is nil", func(t *testing.T) {
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
			// UserInfo intentionally nil.
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		result, err := provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.NoError(t, err)

		require.NotNil(t, result)
		assert.NotEmpty(t, result.Tokens.AccessToken)
		// Subject is the prefix-tagged hash of the access token.
		assert.True(t, strings.HasPrefix(result.Subject, synthesizedSubjectPrefix),
			"synthesized subject must carry the %q prefix; got %q",
			synthesizedSubjectPrefix, result.Subject)
		assert.Equal(t,
			synthesizeSubjectFromAccessToken(result.Tokens.AccessToken),
			result.Subject,
			"subject must be deterministic given the same access token")
		// Synthesized identities expose no display surface.
		assert.Empty(t, result.Name)
		assert.Empty(t, result.Email)
		// Synthetic=true is what tells the callback handler to bypass UserResolver.
		assert.True(t, result.Synthetic, "synthesized identities must set Synthetic=true")
	})
}

func TestSynthesizeSubjectFromAccessToken(t *testing.T) {
	t.Parallel()

	t.Run("is deterministic for a given token", func(t *testing.T) {
		t.Parallel()
		token := "atlassian-mcp-style-opaque-token-93c"
		assert.Equal(t,
			synthesizeSubjectFromAccessToken(token),
			synthesizeSubjectFromAccessToken(token),
		)
	})

	t.Run("differs for different tokens", func(t *testing.T) {
		t.Parallel()
		assert.NotEqual(t,
			synthesizeSubjectFromAccessToken("token-a"),
			synthesizeSubjectFromAccessToken("token-b"),
		)
	})

	t.Run("output shape: prefix + 32 hex chars", func(t *testing.T) {
		t.Parallel()
		got := synthesizeSubjectFromAccessToken("any-input")
		assert.True(t, strings.HasPrefix(got, synthesizedSubjectPrefix))
		hexPart := strings.TrimPrefix(got, synthesizedSubjectPrefix)
		assert.Len(t, hexPart, 32, "first 16 bytes of SHA-256 in hex is 32 chars")
		// Must be valid hex.
		_, err := hex.DecodeString(hexPart)
		assert.NoError(t, err)
	})

}

// TestSynthesizeIdentity exercises the synthesis-mode helper directly,
// including the empty-access-token guard. The synthesizer itself is a pure
// hash and would happily emit the well-known sha256("") constant for "" —
// synthesizeIdentity is the layer that refuses to do so, preventing distinct
// sessions from collapsing onto a single (UserID, ProviderID) storage bucket.
func TestSynthesizeIdentity(t *testing.T) {
	t.Parallel()

	t.Run("rejects empty access token", func(t *testing.T) {
		t.Parallel()
		// Defense-in-depth: convertOAuth2Token already catches empty
		// AccessToken at exchange time, so this guard is unreachable
		// through the public API today. The test asserts the invariant
		// regardless, so a future code path that bypasses
		// convertOAuth2Token cannot silently synthesize the constant
		// sha256("") subject.
		got, err := synthesizeIdentity(&Tokens{AccessToken: ""})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrIdentityResolutionFailed)
		assert.Nil(t, got)
	})

	t.Run("synthesizes for non-empty access token", func(t *testing.T) {
		t.Parallel()
		tokens := &Tokens{AccessToken: "atlassian-mcp-style-opaque-token"}
		got, err := synthesizeIdentity(tokens)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.True(t, got.Synthetic, "synthesized identities must set Synthetic=true")
		assert.Equal(t,
			synthesizeSubjectFromAccessToken(tokens.AccessToken),
			got.Subject,
			"subject must be deterministic given the same access token")
		assert.Empty(t, got.Name)
		assert.Empty(t, got.Email)
		assert.Same(t, tokens, got.Tokens, "tokens reference is preserved")
	})
}

func TestIsSynthesizedSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		subject string
		want    bool
	}{
		// Round-trip: predicate must recognize anything the synthesizer emits.
		{
			name:    "round-trip on synthesized subject",
			subject: synthesizeSubjectFromAccessToken("any-opaque-token"),
			want:    true,
		},
		// Real upstream subjects (UUIDs, integer IDs) must not classify as synthesized.
		{
			name:    "uuid-shaped subject is not synthesized",
			subject: "11012b90-98d0-4594-916e-54db832ebe8f",
			want:    false,
		},
		{
			name:    "integer-shaped subject is not synthesized",
			subject: "1234567890",
			want:    false,
		},
		{
			name:    "atlassian-shaped account_id is not synthesized",
			subject: "5e1234567890abcdef123456",
			want:    false,
		},
		{
			name:    "empty string is not synthesized",
			subject: "",
			want:    false,
		},
		// HasPrefix, not substring search.
		{
			name:    "tk- in middle of subject is not synthesized",
			subject: "user-tk-abc",
			want:    false,
		},
		// Predicate is a fast prefix guard, not a digest validator.
		{
			name:    "prefix-only string is treated as synthesized",
			subject: "tk-",
			want:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, IsSynthesizedSubject(tc.subject))
		})
	}
}

func TestBaseOAuth2Provider_fetchUserInfo(t *testing.T) {
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

				_, err = provider.fetchUserInfo(context.Background(), tt.accessToken)
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

				userInfo, err := provider.fetchUserInfo(context.Background(), "test-access-token")
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

		_, err = provider.fetchUserInfo(context.Background(), "test-access-token")
		require.NoError(t, err)

		assert.Equal(t, "2022-11-28", receivedHeaders.Get("X-GitHub-Api-Version"))
		assert.Equal(t, "application/vnd.github+json", receivedHeaders.Get("Accept"))
	})
}

func TestBaseOAuth2Provider_fetchUserInfo_FieldMapping(t *testing.T) {
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

		userInfo, err := provider.fetchUserInfo(ctx, "test-access-token")
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

		userInfo, err := provider.fetchUserInfo(ctx, "test-access-token")
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

		_, err = provider.fetchUserInfo(ctx, "test-access-token")
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

		userInfo, err := provider.fetchUserInfo(ctx, "test-access-token")
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

		_, err = provider.fetchUserInfo(ctx, "test-access-token")
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

		_, err = provider.fetchUserInfo(ctx, "")
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

		_, err = provider.fetchUserInfo(ctx, "test-access-token")
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

		_, err = provider.fetchUserInfo(ctx, "test-access-token")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing required subject claim")
	})
}

func TestValidateAdditionalAuthorizationParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		params      map[string]string
		wantErr     bool
		errContains string
	}{
		{
			name:   "nil map",
			params: nil,
		},
		{
			name:   "empty map",
			params: map[string]string{},
		},
		{
			name:   "valid single param",
			params: map[string]string{"access_type": "offline"},
		},
		{
			name:   "valid multiple params",
			params: map[string]string{"access_type": "offline", "prompt": "consent"},
		},
		{
			name:        "reserved: state",
			params:      map[string]string{"state": "x"},
			wantErr:     true,
			errContains: "state",
		},
		{
			name:        "reserved: nonce",
			params:      map[string]string{"nonce": "x"},
			wantErr:     true,
			errContains: "nonce",
		},
		{
			name:        "reserved: response_type",
			params:      map[string]string{"response_type": "token"},
			wantErr:     true,
			errContains: "response_type",
		},
		{
			name:        "reserved: code_challenge",
			params:      map[string]string{"code_challenge": "x"},
			wantErr:     true,
			errContains: "code_challenge",
		},
		{
			name:        "reserved: code_challenge_method",
			params:      map[string]string{"code_challenge_method": "S256"},
			wantErr:     true,
			errContains: "code_challenge_method",
		},
		{
			name:        "reserved: client_id",
			params:      map[string]string{"client_id": "x"},
			wantErr:     true,
			errContains: "client_id",
		},
		{
			name:        "reserved: redirect_uri",
			params:      map[string]string{"redirect_uri": "http://evil.com"},
			wantErr:     true,
			errContains: "redirect_uri",
		},
		{
			name:        "reserved: scope",
			params:      map[string]string{"scope": "admin"},
			wantErr:     true,
			errContains: "scope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := &CommonOAuthConfig{
				ClientID:                      "test-client",
				RedirectURI:                   "http://localhost:8080/callback",
				AdditionalAuthorizationParams: tt.params,
			}

			err := config.ValidateWithInsecure(false)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestAuthorizationURL_AdditionalAuthorizationParams(t *testing.T) {
	t.Parallel()

	t.Run("config params appear on URL", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:    "test-client",
				RedirectURI: "http://localhost:8080/callback",
				AdditionalAuthorizationParams: map[string]string{
					"access_type": "offline",
					"prompt":      "consent",
				},
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		authURL, err := provider.AuthorizationURL("test-state", "test-challenge")
		require.NoError(t, err)

		parsed, err := url.Parse(authURL)
		require.NoError(t, err)

		query := parsed.Query()
		assert.Equal(t, "offline", query.Get("access_type"))
		assert.Equal(t, "consent", query.Get("prompt"))
	})

	t.Run("caller opts override config params", func(t *testing.T) {
		t.Parallel()

		mock := newMockOAuth2Server()
		t.Cleanup(mock.Close)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:    "test-client",
				RedirectURI: "http://localhost:8080/callback",
				AdditionalAuthorizationParams: map[string]string{
					"custom": "config-value",
				},
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		authURL, err := provider.AuthorizationURL("test-state", "",
			WithAdditionalParams(map[string]string{"custom": "caller-value"}))
		require.NoError(t, err)

		parsed, err := url.Parse(authURL)
		require.NoError(t, err)

		assert.Equal(t, "caller-value", parsed.Query().Get("custom"))
	})
}

// tokenBodyWithUsername is a minimal valid token response body that includes a
// "username" field used across several identityFromToken test cases.
const tokenBodyWithUsername = `{"access_token":"a","token_type":"Bearer","username":"u1"}`

// newTokenResponseServer is a test helper that starts an httptest.Server whose
// /token endpoint returns tokenBody as the JSON response body, and whose
// /authorize endpoint always returns 200 OK.
func newTokenResponseServer(t *testing.T, tokenBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tokenBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestBaseOAuth2Provider_ExchangeCodeForIdentity_IdentityFromToken covers the
// priority-1 path where IdentityFromToken is configured.
func TestBaseOAuth2Provider_ExchangeCodeForIdentity_IdentityFromToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("identityFromToken resolves subject name email", func(t *testing.T) {
		t.Parallel()

		tokenBody := `{"access_token":"a","token_type":"Bearer","username":"u1","display_name":"User One","email":"u1@example.com"}`
		tokenSrv := newTokenResponseServer(t, tokenBody)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenSrv.URL + "/authorize",
			TokenEndpoint:         tokenSrv.URL + "/token",
			IdentityFromToken: &IdentityFromTokenConfig{
				SubjectPath: "username",
				NamePath:    "display_name",
				EmailPath:   "email",
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		identity, err := provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.NoError(t, err)
		assert.Equal(t, "u1", identity.Subject)
		assert.Equal(t, "User One", identity.Name)
		assert.Equal(t, "u1@example.com", identity.Email)
		assert.NotEmpty(t, identity.Tokens.AccessToken)
		assert.False(t, identity.Synthetic, "IdentityFromToken path must not produce synthetic identities")
	})

	t.Run("@upstreamjwt modifier resolves identity from JWT-shaped access token", func(t *testing.T) {
		t.Parallel()

		// RegisterModifiers is called once by TestMain before all tests run.
		// Do not call it again here: gjson.AddModifier writes to a shared map and
		// races with concurrent reads in parallel subtests.

		accessToken := makeJWT(`{"sub":"u-jwt-1","name":"JWT User","email":"jwt@example.com"}`)
		tokenBody := fmt.Sprintf(`{"access_token":%q,"token_type":"Bearer"}`, accessToken)
		tokenSrv := newTokenResponseServer(t, tokenBody)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenSrv.URL + "/authorize",
			TokenEndpoint:         tokenSrv.URL + "/token",
			IdentityFromToken: &IdentityFromTokenConfig{
				SubjectPath: "access_token|@upstreamjwt|sub",
				NamePath:    "access_token|@upstreamjwt|name",
				EmailPath:   "access_token|@upstreamjwt|email",
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		identity, err := provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.NoError(t, err)
		assert.Equal(t, "u-jwt-1", identity.Subject)
		assert.Equal(t, "JWT User", identity.Name)
		assert.Equal(t, "jwt@example.com", identity.Email)
		assert.False(t, identity.Synthetic)
	})

	t.Run("@upstreamjwt modifier accepts hand-forged unsigned JWT", func(t *testing.T) {
		t.Parallel()

		// Construct a JWT with a garbage signature. The payload is valid base64url-encoded
		// JSON, but the signature is not the real HMAC/RSA output. This test pins the
		// intentional no-signature-check behavior. A future contributor who adds
		// verification must update the trust model docs and this test.
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"forged-subject"}`))
		forgedJWT := header + "." + payload + ".INVALIDSIG"

		tokenBody := fmt.Sprintf(`{"access_token":%q,"token_type":"Bearer"}`, forgedJWT)
		tokenSrv := newTokenResponseServer(t, tokenBody)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenSrv.URL + "/authorize",
			TokenEndpoint:         tokenSrv.URL + "/token",
			IdentityFromToken: &IdentityFromTokenConfig{
				SubjectPath: "access_token|@upstreamjwt|sub",
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		identity, err := provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.NoError(t, err)
		// No signature verification — extraction succeeds despite the forged signature.
		assert.Equal(t, "forged-subject", identity.Subject)
	})

	t.Run("identityFromToken bypasses userinfo endpoint entirely", func(t *testing.T) {
		t.Parallel()

		// httptest.Server dispatches each request on its own goroutine, so
		// the counter must be accessed atomically — t.Errorf inside the
		// handler is the load-bearing assertion; the counter just gives a
		// numeric value the final assertion can read race-free.
		var tripwireCallCount atomic.Int32
		tripwire := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			tripwireCallCount.Add(1)
			t.Errorf("userinfo endpoint must NOT be called when identityFromToken is configured")
		}))
		t.Cleanup(tripwire.Close)

		tokenSrv := newTokenResponseServer(t, tokenBodyWithUsername)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenSrv.URL + "/authorize",
			TokenEndpoint:         tokenSrv.URL + "/token",
			IdentityFromToken: &IdentityFromTokenConfig{
				SubjectPath: "username",
			},
			// UserInfo is also configured — identityFromToken must win and
			// the tripwire userinfo server must never be contacted.
			UserInfo: &UserInfoConfig{
				EndpointURL: tripwire.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		identity, err := provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.NoError(t, err)
		assert.Equal(t, "u1", identity.Subject)
		assert.Equal(t, int32(0), tripwireCallCount.Load(), "userinfo endpoint must not be called")
	})

	t.Run("identityFromToken extraction failure returns ErrIdentityResolutionFailed without calling userinfo", func(t *testing.T) {
		t.Parallel()

		var tripwireCallCount atomic.Int32
		tripwire := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			tripwireCallCount.Add(1)
			t.Errorf("userinfo endpoint must NOT be called when identityFromToken is configured")
		}))
		t.Cleanup(tripwire.Close)

		// Token body does NOT contain "missing_path"
		tokenSrv := newTokenResponseServer(t, tokenBodyWithUsername)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenSrv.URL + "/authorize",
			TokenEndpoint:         tokenSrv.URL + "/token",
			IdentityFromToken: &IdentityFromTokenConfig{
				SubjectPath: "missing_path",
			},
			UserInfo: &UserInfoConfig{
				EndpointURL: tripwire.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrIdentityResolutionFailed))
		assert.Equal(t, int32(0), tripwireCallCount.Load(), "userinfo endpoint must not be called on extraction failure")
	})

	t.Run("extraction failure error surfaces the misconfigured path name", func(t *testing.T) {
		t.Parallel()

		tokenSrv := newTokenResponseServer(t, tokenBodyWithUsername)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenSrv.URL + "/authorize",
			TokenEndpoint:         tokenSrv.URL + "/token",
			IdentityFromToken: &IdentityFromTokenConfig{
				// Path that does not exist in the token response so the extractor
				// produces a diagnostic that names the path and the failure reason.
				SubjectPath: "nonexistent_field",
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrIdentityResolutionFailed))
		// The error must carry the operator-supplied path name so the
		// misconfiguration is diagnosable without log access.
		assert.Contains(t, err.Error(), "nonexistent_field",
			"error must contain the misconfigured subject path name")
		assert.Contains(t, err.Error(), "not found",
			"error must describe why extraction failed")
	})

	t.Run("userInfo-only path is unchanged when identityFromToken is not set", func(t *testing.T) {
		t.Parallel()

		userInfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sub":   "u-456",
				"name":  "Bob",
				"email": "bob@example.com",
			})
		}))
		t.Cleanup(userInfoSrv.Close)

		tokenBody := `{"access_token":"a","token_type":"Bearer"}`
		tokenSrv := newTokenResponseServer(t, tokenBody)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenSrv.URL + "/authorize",
			TokenEndpoint:         tokenSrv.URL + "/token",
			UserInfo: &UserInfoConfig{
				EndpointURL: userInfoSrv.URL,
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		identity, err := provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.NoError(t, err)
		assert.Equal(t, "u-456", identity.Subject)
		assert.Equal(t, "Bob", identity.Name)
		assert.Equal(t, "bob@example.com", identity.Email)
	})

	t.Run("neither identityFromToken nor userInfo set falls through to synthesis", func(t *testing.T) {
		t.Parallel()

		tokenBody := `{"access_token":"a","token_type":"Bearer"}`
		tokenSrv := newTokenResponseServer(t, tokenBody)

		// Both identity surfaces absent — the priority chain falls through to
		// synthesizeIdentity (PR 5094): a non-PII Subject derived from the
		// access token, Synthetic=true, no Name/Email.
		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenSrv.URL + "/authorize",
			TokenEndpoint:         tokenSrv.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		identity, err := provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.NoError(t, err)
		assert.True(t, identity.Synthetic, "expected synthesized identity when neither IdentityFromToken nor UserInfo is set")
		assert.True(t, IsSynthesizedSubject(identity.Subject),
			"expected synthesized subject prefix; got %q", identity.Subject)
		assert.Empty(t, identity.Name, "synthesized identity has no name")
		assert.Empty(t, identity.Email, "synthesized identity has no email")
	})

	t.Run("IdentityFromToken configured does not cause RefreshTokens to fail when identity field is absent", func(t *testing.T) {
		t.Parallel()

		// Token body intentionally does NOT include "username". If extraction ran on
		// the refresh path, it would fail (path absent) and RefreshTokens would error.
		// A successful return proves extraction was NOT run on the refresh path.
		tokenBody := `{"access_token":"refreshed","token_type":"Bearer","refresh_token":"new-refresh"}`
		tokenSrv := newTokenResponseServer(t, tokenBody)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenSrv.URL + "/authorize",
			TokenEndpoint:         tokenSrv.URL + "/token",
			IdentityFromToken: &IdentityFromTokenConfig{
				SubjectPath: "username",
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		// RefreshTokens must succeed even though "username" is absent from the
		// response, proving that identity extraction is skipped on the refresh path.
		tokens, err := provider.RefreshTokens(ctx, "old-refresh-token", "")
		require.NoError(t, err)
		assert.Equal(t, "refreshed", tokens.AccessToken)
	})

	t.Run("error message does not contain token body content", func(t *testing.T) {
		t.Parallel()

		secretMarker := "SUPER_SECRET_TOKEN_BODY_MARKER_XYZ789"
		tokenBody := `{"access_token":"` + secretMarker + `","token_type":"Bearer","username":"u1"}`
		tokenSrv := newTokenResponseServer(t, tokenBody)

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: tokenSrv.URL + "/authorize",
			TokenEndpoint:         tokenSrv.URL + "/token",
			IdentityFromToken: &IdentityFromTokenConfig{
				// Deliberately wrong path to trigger extraction failure.
				SubjectPath: "missing_field",
			},
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)

		_, err = provider.ExchangeCodeForIdentity(ctx, "test-code", "", "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrIdentityResolutionFailed))
		// The error must not leak any part of the token response body.
		assert.NotContains(t, err.Error(), secretMarker,
			"error message must not contain token body content")
	})
}

func TestNewHTTPClientForHost(t *testing.T) {
	t.Parallel()

	// privateIP is an RFC-1918 address unlikely to have a listener; used to
	// trigger the private-IP dial-block without needing a real server.
	const privateIP = "10.255.255.1"

	tests := []struct {
		name            string
		host            string
		allowPrivateIPs bool
		// wantPrivateIPBlocked: when true, dialing privateIP must produce the
		// "private IP address" error; when false, it must not (any other error,
		// e.g. connection refused or TLS, is fine).
		wantPrivateIPBlocked bool
	}{
		{
			name:                 "external host with private IPs disallowed blocks private IP dial",
			host:                 "external.example.com",
			allowPrivateIPs:      false,
			wantPrivateIPBlocked: true,
		},
		{
			name:                 "external host with private IPs allowed passes dial guard",
			host:                 "external.example.com",
			allowPrivateIPs:      true,
			wantPrivateIPBlocked: false,
		},
		{
			name:            "localhost always allows private IPs regardless of flag",
			host:            "localhost",
			allowPrivateIPs: false,
			// localhost sets allowInsecure=true which already sets WithPrivateIPs(true)
			wantPrivateIPBlocked: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := newHTTPClientForHost(tt.host, tt.allowPrivateIPs, false)
			require.NoError(t, err)
			require.NotNil(t, client)

			// Use a short deadline so "allowed" cases don't wait for a real TCP
			// timeout when dialing the unreachable private IP.
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			t.Cleanup(cancel)

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+privateIP+":9999", nil)
			require.NoError(t, err)

			// The private-IP guard fires synchronously inside the dialer's control
			// function, before any TCP packets are sent, so the "private IP address"
			// error is immediate and deterministic when the guard is active.
			// When the guard is absent the dial proceeds over the network; the
			// 500 ms context deadline bounds the test. Any non-guard error
			// (context deadline exceeded, connection refused, TLS) does not contain
			// "private IP address", so the NotContains assertion is not vacuous:
			// an incorrectly-active guard would fail the check immediately.
			_, dialErr := client.Do(req)
			require.Error(t, dialErr)

			if tt.wantPrivateIPBlocked {
				assert.Contains(t, dialErr.Error(), "private IP address",
					"expected private IP to be blocked")
			} else {
				assert.NotContains(t, dialErr.Error(), "private IP address",
					"expected private IP dial to pass the IP guard")
			}
		})
	}
}

func TestOAuth2Config_AllowPrivateIPs(t *testing.T) {
	t.Parallel()

	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)

	t.Run("AllowPrivateIPs field is propagated to provider", func(t *testing.T) {
		t.Parallel()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:    "test-client",
				RedirectURI: "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
			AllowPrivateIPs:       true,
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)
		require.NotNil(t, provider)
		assert.True(t, provider.config.AllowPrivateIPs)
	})

	t.Run("AllowPrivateIPs defaults to false", func(t *testing.T) {
		t.Parallel()

		config := &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:    "test-client",
				RedirectURI: "http://localhost:8080/callback",
			},
			AuthorizationEndpoint: mock.URL + "/authorize",
			TokenEndpoint:         mock.URL + "/token",
		}

		provider, err := NewOAuth2Provider(config)
		require.NoError(t, err)
		require.NotNil(t, provider)
		assert.False(t, provider.config.AllowPrivateIPs)
	})
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractTokensFromResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		mapping     *TokenResponseMapping
		wantErr     bool
		errContains string
		check       func(t *testing.T, tokens *Tokens)
	}{
		{
			name: "govslack nested access token",
			body: `{
				"ok": true,
				"authed_user": {
					"id": "U1234",
					"access_token": "xoxp-secret-token",
					"token_type": "user",
					"scope": "channels:history channels:read"
				}
			}`,
			mapping: &TokenResponseMapping{
				AccessTokenPath: "authed_user.access_token",
				TokenTypePath:   "authed_user.token_type",
				ScopePath:       "authed_user.scope",
			},
			check: func(t *testing.T, tokens *Tokens) {
				t.Helper()
				assert.Equal(t, "xoxp-secret-token", tokens.AccessToken)
			},
		},
		{
			name: "standard top-level fields with custom mapping",
			body: `{
				"access_token": "standard-token",
				"token_type": "Bearer",
				"expires_in": 3600,
				"refresh_token": "refresh-123"
			}`,
			mapping: &TokenResponseMapping{
				AccessTokenPath: "access_token",
			},
			check: func(t *testing.T, tokens *Tokens) {
				t.Helper()
				assert.Equal(t, "standard-token", tokens.AccessToken)
				assert.Equal(t, "refresh-123", tokens.RefreshToken)
				assert.WithinDuration(t, time.Now().Add(time.Hour), tokens.ExpiresAt, 5*time.Second)
			},
		},
		{
			name: "all fields nested",
			body: `{
				"data": {
					"token": "nested-token",
					"type": "bearer",
					"refresh": "nested-refresh",
					"ttl": 7200
				}
			}`,
			mapping: &TokenResponseMapping{
				AccessTokenPath:  "data.token",
				TokenTypePath:    "data.type",
				RefreshTokenPath: "data.refresh",
				ExpiresInPath:    "data.ttl",
			},
			check: func(t *testing.T, tokens *Tokens) {
				t.Helper()
				assert.Equal(t, "nested-token", tokens.AccessToken)
				assert.Equal(t, "nested-refresh", tokens.RefreshToken)
				assert.WithinDuration(t, time.Now().Add(2*time.Hour), tokens.ExpiresAt, 5*time.Second)
			},
		},
		{
			name: "missing access token at path",
			body: `{"other_field": "value"}`,
			mapping: &TokenResponseMapping{
				AccessTokenPath: "authed_user.access_token",
			},
			wantErr:     true,
			errContains: "missing access_token at path: authed_user.access_token",
		},
		{
			name: "empty access token at path",
			body: `{"authed_user": {"access_token": ""}}`,
			mapping: &TokenResponseMapping{
				AccessTokenPath: "authed_user.access_token",
			},
			wantErr:     true,
			errContains: "missing access_token at path",
		},
		{
			name: "default token type when not specified",
			body: `{"access_token": "tok"}`,
			mapping: &TokenResponseMapping{
				AccessTokenPath: "access_token",
			},
			check: func(t *testing.T, tokens *Tokens) {
				t.Helper()
				assert.Equal(t, "tok", tokens.AccessToken)
				// token_type defaults to Bearer, no error
			},
		},
		{
			name: "default expiration when missing",
			body: `{"access_token": "tok", "token_type": "Bearer"}`,
			mapping: &TokenResponseMapping{
				AccessTokenPath: "access_token",
			},
			check: func(t *testing.T, tokens *Tokens) {
				t.Helper()
				// Default: 1 hour from now
				assert.WithinDuration(t, time.Now().Add(time.Hour), tokens.ExpiresAt, 5*time.Second)
			},
		},
		{
			name: "id token extracted from standard path",
			body: `{
				"authed_user": {"access_token": "tok"},
				"id_token": "eyJhbGciOiJSUzI1NiJ9.test"
			}`,
			mapping: &TokenResponseMapping{
				AccessTokenPath: "authed_user.access_token",
			},
			check: func(t *testing.T, tokens *Tokens) {
				t.Helper()
				assert.Equal(t, "tok", tokens.AccessToken)
				assert.Equal(t, "eyJhbGciOiJSUzI1NiJ9.test", tokens.IDToken)
			},
		},
		{
			name:        "nil mapping returns error",
			body:        `{"access_token": "tok"}`,
			mapping:     nil,
			wantErr:     true,
			errContains: "token response mapping is required",
		},
		{
			name: "user token type accepted (govslack)",
			body: `{"access_token": "tok", "token_type": "user"}`,
			mapping: &TokenResponseMapping{
				AccessTokenPath: "access_token",
			},
			check: func(t *testing.T, tokens *Tokens) {
				t.Helper()
				assert.Equal(t, "tok", tokens.AccessToken)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tokens, err := extractTokensFromResponse([]byte(tt.body), tt.mapping)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, tokens)
			if tt.check != nil {
				tt.check(t, tokens)
			}
		})
	}
}

func TestCustomExchangeCodeForTokens_Integration(t *testing.T) {
	t.Parallel()

	// Mock GovSlack token endpoint
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		err := r.ParseForm()
		require.NoError(t, err)
		require.Equal(t, "authorization_code", r.Form.Get("grant_type"))
		require.Equal(t, "test-code", r.Form.Get("code"))

		resp := map[string]any{
			"ok": true,
			"authed_user": map[string]any{
				"id":           "U1234",
				"access_token": "xoxp-user-token",
				"token_type":   "user",
				"scope":        "channels:read",
			},
			"refresh_token": "xoxe-refresh",
			"expires_in":    43200,
		}

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(resp)
		require.NoError(t, err)
	}))
	defer tokenServer.Close()

	provider := &BaseOAuth2Provider{
		config: &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				RedirectURI:  "http://localhost/callback",
			},
			TokenEndpoint: tokenServer.URL,
			TokenResponseMapping: &TokenResponseMapping{
				AccessTokenPath:  "authed_user.access_token",
				TokenTypePath:    "authed_user.token_type",
				RefreshTokenPath: "refresh_token",
				ExpiresInPath:    "expires_in",
			},
		},
		httpClient: http.DefaultClient,
	}

	tokens, err := provider.customExchangeCodeForTokens(t.Context(), "test-code", "")
	require.NoError(t, err)

	assert.Equal(t, "xoxp-user-token", tokens.AccessToken)
	assert.Equal(t, "xoxe-refresh", tokens.RefreshToken)
	assert.WithinDuration(t, time.Now().Add(12*time.Hour), tokens.ExpiresAt, 5*time.Second)
}

func TestCustomRefreshTokens_Integration(t *testing.T) {
	t.Parallel()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err)
		require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
		require.Equal(t, "old-refresh", r.Form.Get("refresh_token"))

		resp := map[string]any{
			"authed_user": map[string]any{
				"access_token": "new-access-token",
				"token_type":   "user",
			},
			"refresh_token": "new-refresh",
			"expires_in":    3600,
		}

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(resp)
		require.NoError(t, err)
	}))
	defer tokenServer.Close()

	provider := &BaseOAuth2Provider{
		config: &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
			},
			TokenEndpoint: tokenServer.URL,
			TokenResponseMapping: &TokenResponseMapping{
				AccessTokenPath:  "authed_user.access_token",
				TokenTypePath:    "authed_user.token_type",
				RefreshTokenPath: "refresh_token",
			},
		},
		httpClient: http.DefaultClient,
	}

	tokens, err := provider.customRefreshTokens(t.Context(), "old-refresh")
	require.NoError(t, err)

	assert.Equal(t, "new-access-token", tokens.AccessToken)
	assert.Equal(t, "new-refresh", tokens.RefreshToken)
}

func TestCustomTokenRequest_ServerError(t *testing.T) {
	t.Parallel()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "invalid_grant"}`))
	}))
	defer tokenServer.Close()

	provider := &BaseOAuth2Provider{
		config: &OAuth2Config{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID: "test-client",
			},
			TokenEndpoint: tokenServer.URL,
			TokenResponseMapping: &TokenResponseMapping{
				AccessTokenPath: "access_token",
			},
		},
		httpClient: http.DefaultClient,
	}

	_, err := provider.customExchangeCodeForTokens(t.Context(), "bad-code", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token request failed with status 400")
}

func TestPathOrDefault(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "custom.path", pathOrDefault("custom.path", "default"))
	assert.Equal(t, "default", pathOrDefault("", "default"))
}

func TestOAuth2Config_Validate_TokenResponseMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mapping *TokenResponseMapping
		wantErr bool
	}{
		{
			name:    "nil mapping is valid",
			mapping: nil,
			wantErr: false,
		},
		{
			name: "valid mapping with access token path",
			mapping: &TokenResponseMapping{
				AccessTokenPath: "authed_user.access_token",
			},
			wantErr: false,
		},
		{
			name: "missing access token path",
			mapping: &TokenResponseMapping{
				TokenTypePath: "authed_user.token_type",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &OAuth2Config{
				CommonOAuthConfig: CommonOAuthConfig{
					ClientID:    "test",
					RedirectURI: "http://localhost/callback",
				},
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				TokenResponseMapping:  tt.mapping,
			}

			err := cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "access_token_path")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

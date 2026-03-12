// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRewriteTokenResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		mapping *TokenResponseMapping
		check   func(t *testing.T, result map[string]any)
	}{
		{
			name: "govslack nested access token extracted to top level",
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
				ScopePath:       "authed_user.scope",
			},
			check: func(t *testing.T, result map[string]any) {
				t.Helper()
				assert.Equal(t, "xoxp-secret-token", result["access_token"])
				assert.Equal(t, "Bearer", result["token_type"])
				assert.Equal(t, "channels:history channels:read", result["scope"])
				// Original fields preserved
				assert.Equal(t, true, result["ok"])
			},
		},
		{
			name: "all fields nested under custom paths",
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
				RefreshTokenPath: "data.refresh",
				ExpiresInPath:    "data.ttl",
			},
			check: func(t *testing.T, result map[string]any) {
				t.Helper()
				assert.Equal(t, "nested-token", result["access_token"])
				assert.Equal(t, "Bearer", result["token_type"])
				assert.Equal(t, "nested-refresh", result["refresh_token"])
				assert.Equal(t, float64(7200), result["expires_in"])
			},
		},
		{
			name: "default token type added when missing",
			body: `{"custom_token": "tok"}`,
			mapping: &TokenResponseMapping{
				AccessTokenPath: "custom_token",
			},
			check: func(t *testing.T, result map[string]any) {
				t.Helper()
				assert.Equal(t, "tok", result["access_token"])
				assert.Equal(t, "Bearer", result["token_type"])
			},
		},
		{
			name: "existing top-level fields preserved when mapping paths are empty",
			body: `{"access_token": "original", "refresh_token": "orig-refresh", "expires_in": 3600}`,
			mapping: &TokenResponseMapping{
				AccessTokenPath: "access_token",
			},
			check: func(t *testing.T, result map[string]any) {
				t.Helper()
				assert.Equal(t, "original", result["access_token"])
				assert.Equal(t, "orig-refresh", result["refresh_token"])
				assert.Equal(t, float64(3600), result["expires_in"])
			},
		},
		{
			name: "invalid JSON returns body unchanged",
			body: `not json`,
			mapping: &TokenResponseMapping{
				AccessTokenPath: "access_token",
			},
			check: func(t *testing.T, _ map[string]any) {
				t.Helper()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := rewriteTokenResponse([]byte(tt.body), tt.mapping)

			var parsed map[string]any
			if err := json.Unmarshal(result, &parsed); err != nil {
				assert.Equal(t, tt.body, string(result))
				if tt.check != nil {
					tt.check(t, nil)
				}
				return
			}

			if tt.check != nil {
				tt.check(t, parsed)
			}
		})
	}
}

func TestTokenResponseRewriter_TokenEndpoint(t *testing.T) {
	t.Parallel()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"ok": true,
			"authed_user": map[string]any{
				"access_token": "xoxp-user-token",
				"token_type":   "user",
				"scope":        "channels:read",
			},
			"refresh_token": "xoxe-refresh",
			"expires_in":    43200,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer tokenServer.Close()

	mapping := &TokenResponseMapping{
		AccessTokenPath: "authed_user.access_token",
		ScopePath:       "authed_user.scope",
	}

	client := wrapHTTPClientWithMapping(http.DefaultClient, mapping, tokenServer.URL)

	req, err := http.NewRequest("POST", tokenServer.URL, strings.NewReader("grant_type=authorization_code"))
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	assert.Equal(t, "xoxp-user-token", parsed["access_token"])
	assert.Equal(t, "Bearer", parsed["token_type"])
	assert.Equal(t, "channels:read", parsed["scope"])
	assert.Equal(t, "xoxe-refresh", parsed["refresh_token"])
	assert.Equal(t, float64(43200), parsed["expires_in"])
}

func TestTokenResponseRewriter_NonTokenEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user_id": "U1234", "user": "testuser"}`))
	}))
	defer server.Close()

	mapping := &TokenResponseMapping{AccessTokenPath: "authed_user.access_token"}
	// Token URL points elsewhere, so this server's responses should pass through unchanged
	client := wrapHTTPClientWithMapping(http.DefaultClient, mapping, "https://other.example.com/token")

	req, err := http.NewRequest("GET", server.URL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	assert.Equal(t, "U1234", parsed["user_id"])
	_, hasAccessToken := parsed["access_token"]
	assert.False(t, hasAccessToken)
}

func TestWrapHTTPClientWithMapping_NilMapping(t *testing.T) {
	t.Parallel()

	original := &http.Client{}
	result := wrapHTTPClientWithMapping(original, nil, "https://example.com/token")
	assert.Same(t, original, result)
}

func TestTokenResponseRewriter_ErrorResponse(t *testing.T) {
	t.Parallel()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "invalid_grant"}`))
	}))
	defer tokenServer.Close()

	mapping := &TokenResponseMapping{AccessTokenPath: "authed_user.access_token"}
	client := wrapHTTPClientWithMapping(http.DefaultClient, mapping, tokenServer.URL)

	req, err := http.NewRequest("POST", tokenServer.URL, strings.NewReader("grant_type=authorization_code"))
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	assert.Equal(t, "invalid_grant", parsed["error"])
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
		{name: "nil mapping is valid", mapping: nil, wantErr: false},
		{name: "valid mapping", mapping: &TokenResponseMapping{AccessTokenPath: "authed_user.access_token"}, wantErr: false},
		{name: "missing access token path", mapping: &TokenResponseMapping{ScopePath: "scope"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &OAuth2Config{
				CommonOAuthConfig:     CommonOAuthConfig{ClientID: "test", RedirectURI: "http://localhost/callback"},
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

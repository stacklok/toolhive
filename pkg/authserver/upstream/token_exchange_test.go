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

// tokenEndpointHandler returns an HTTP handler that responds with the given JSON body.
func tokenEndpointHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

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

	client, _ := wrapHTTPClientForTokenExchange(http.DefaultClient, mapping, nil, tokenServer.URL)

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
	client, _ := wrapHTTPClientForTokenExchange(http.DefaultClient, mapping, nil, "https://other.example.com/token")

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
	result, rewriter := wrapHTTPClientForTokenExchange(original, nil, nil, "https://example.com/token")
	assert.Same(t, original, result)
	assert.Nil(t, rewriter)
}

func TestTokenResponseRewriter_ErrorResponse(t *testing.T) {
	t.Parallel()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "invalid_grant"}`))
	}))
	defer tokenServer.Close()

	mapping := &TokenResponseMapping{AccessTokenPath: "authed_user.access_token"}
	client, _ := wrapHTTPClientForTokenExchange(http.DefaultClient, mapping, nil, tokenServer.URL)

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

func TestOAuth2Config_Validate_IdentityFromToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		identityCfg *IdentityFromTokenConfig
		wantErr     bool
		errContains string
	}{
		{name: "nil identity config is valid", identityCfg: nil, wantErr: false},
		{
			name:        "valid identity config with subject path",
			identityCfg: &IdentityFromTokenConfig{SubjectPath: "username"},
			wantErr:     false,
		},
		{
			name:        "missing subject path",
			identityCfg: &IdentityFromTokenConfig{NamePath: "name"},
			wantErr:     true,
			errContains: "identity_from_token.subject_path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &OAuth2Config{
				CommonOAuthConfig:     CommonOAuthConfig{ClientID: "test", RedirectURI: "http://localhost/callback"},
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				IdentityFromToken:     tt.identityCfg,
			}
			err := cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestTokenResponseRewriter_IdentityCfgNil verifies that when identityCfg is nil,
// extractedIdentity remains nil after RoundTrip even when a mapping is configured.
func TestTokenResponseRewriter_IdentityCfgNil(t *testing.T) {
	t.Parallel()

	body := `{"access_token":"tok","token_type":"Bearer","username":"u1"}`
	server := httptest.NewServer(tokenEndpointHandler(body))
	t.Cleanup(server.Close)

	mapping := &TokenResponseMapping{AccessTokenPath: "access_token"}
	client, _ := wrapHTTPClientForTokenExchange(http.DefaultClient, mapping, nil, server.URL)

	transport, ok := client.Transport.(*tokenResponseRewriter)
	require.True(t, ok, "expected *tokenResponseRewriter transport")

	req, err := http.NewRequest("POST", server.URL, strings.NewReader("grant_type=authorization_code"))
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Nil(t, transport.extractedIdentity)
}

// TestTokenResponseRewriter_IdentityCfgSet verifies that when identityCfg is set
// and the body contains the subject path, extractedIdentity is populated.
func TestTokenResponseRewriter_IdentityCfgSet(t *testing.T) {
	t.Parallel()

	body := `{"access_token":"a","token_type":"Bearer","username":"u1"}`
	server := httptest.NewServer(tokenEndpointHandler(body))
	t.Cleanup(server.Close)

	identityCfg := &IdentityFromTokenConfig{SubjectPath: "username"}
	client, _ := wrapHTTPClientForTokenExchange(http.DefaultClient, nil, identityCfg, server.URL)

	transport, ok := client.Transport.(*tokenResponseRewriter)
	require.True(t, ok, "expected *tokenResponseRewriter transport")

	req, err := http.NewRequest("POST", server.URL, strings.NewReader("grant_type=authorization_code"))
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.NotNil(t, transport.extractedIdentity)
	assert.Equal(t, "u1", transport.extractedIdentity.Subject)
}

// TestTokenResponseRewriter_IdentityFromRawBody verifies the raw-body
// invariant: identity is resolved against the pre-rewrite body. The fixture
// places the subject at a nested path ("authed_user.id") and configures a
// mapping that lifts access_token to the top level; the assertion confirms
// extraction reads the original nested location, not a post-rewrite shape.
func TestTokenResponseRewriter_IdentityFromRawBody(t *testing.T) {
	t.Parallel()

	rawBody := `{"authed_user":{"access_token":"x","id":"U1234"},"username":""}`
	server := httptest.NewServer(tokenEndpointHandler(rawBody))
	t.Cleanup(server.Close)

	mapping := &TokenResponseMapping{AccessTokenPath: "authed_user.access_token"}
	identityCfg := &IdentityFromTokenConfig{SubjectPath: "authed_user.id"}
	client, _ := wrapHTTPClientForTokenExchange(http.DefaultClient, mapping, identityCfg, server.URL)

	transport, ok := client.Transport.(*tokenResponseRewriter)
	require.True(t, ok, "expected *tokenResponseRewriter transport")

	req, err := http.NewRequest("POST", server.URL, strings.NewReader("grant_type=authorization_code"))
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	// Identity should be extracted from the raw body where authed_user.id == "U1234"
	require.NotNil(t, transport.extractedIdentity)
	assert.Equal(t, "U1234", transport.extractedIdentity.Subject)

	// The rewritten body should have access_token lifted to top-level
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(respBody, &parsed))
	assert.Equal(t, "x", parsed["access_token"])
	assert.Equal(t, "Bearer", parsed["token_type"])
}

// TestWrapHTTPClientForTokenExchange_OnlyIdentityCfg verifies that wrapping occurs
// when only identityCfg is set (mapping is nil).
func TestWrapHTTPClientForTokenExchange_OnlyIdentityCfg(t *testing.T) {
	t.Parallel()

	original := &http.Client{}
	identityCfg := &IdentityFromTokenConfig{SubjectPath: "username"}
	result, rewriter := wrapHTTPClientForTokenExchange(original, nil, identityCfg, "https://example.com/token")

	assert.NotSame(t, original, result)
	assert.NotNil(t, rewriter)
	_, ok := result.Transport.(*tokenResponseRewriter)
	assert.True(t, ok, "expected *tokenResponseRewriter transport when only identityCfg is set")
}

// TestWrapHTTPClientForTokenExchange_OnlyMapping verifies that wrapping occurs
// when only mapping is set (identityCfg is nil), preserving existing behavior.
func TestWrapHTTPClientForTokenExchange_OnlyMapping(t *testing.T) {
	t.Parallel()

	original := &http.Client{}
	mapping := &TokenResponseMapping{AccessTokenPath: "access_token"}
	result, rewriter := wrapHTTPClientForTokenExchange(original, mapping, nil, "https://example.com/token")

	assert.NotSame(t, original, result)
	assert.NotNil(t, rewriter)
	_, ok := result.Transport.(*tokenResponseRewriter)
	assert.True(t, ok, "expected *tokenResponseRewriter transport when only mapping is set")
}

// TestWrapHTTPClientForTokenExchange_BothSet verifies that wrapping occurs
// when both mapping and identityCfg are set.
func TestWrapHTTPClientForTokenExchange_BothSet(t *testing.T) {
	t.Parallel()

	original := &http.Client{}
	mapping := &TokenResponseMapping{AccessTokenPath: "access_token"}
	identityCfg := &IdentityFromTokenConfig{SubjectPath: "username"}
	result, rewriter := wrapHTTPClientForTokenExchange(original, mapping, identityCfg, "https://example.com/token")

	assert.NotSame(t, original, result)
	assert.NotNil(t, rewriter)
	_, ok := result.Transport.(*tokenResponseRewriter)
	assert.True(t, ok, "expected *tokenResponseRewriter transport when both are set")
}

// TestWrapHTTPClientForTokenExchange_BothNil verifies that the original client
// is returned unchanged when both mapping and identityCfg are nil.
func TestWrapHTTPClientForTokenExchange_BothNil(t *testing.T) {
	t.Parallel()

	original := &http.Client{}
	result, rewriter := wrapHTTPClientForTokenExchange(original, nil, nil, "https://example.com/token")
	assert.Same(t, original, result)
	assert.Nil(t, rewriter)
}

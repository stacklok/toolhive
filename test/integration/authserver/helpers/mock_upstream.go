// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package helpers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// MockUpstreamIDP creates a mock OAuth2/OIDC upstream identity provider.
// It provides minimal endpoints needed for testing the auth server integration.
type MockUpstreamIDP struct {
	Server           *httptest.Server
	AuthorizeHandler func(w http.ResponseWriter, r *http.Request)
	TokenHandler     func(w http.ResponseWriter, r *http.Request)
	UserInfoHandler  func(w http.ResponseWriter, r *http.Request)
	tb               testing.TB
}

// MockUpstreamOption is a functional option for configuring the mock upstream.
type MockUpstreamOption func(*MockUpstreamIDP)

// WithAuthorizeHandler sets a custom authorization endpoint handler.
func WithAuthorizeHandler(h func(w http.ResponseWriter, r *http.Request)) MockUpstreamOption {
	return func(m *MockUpstreamIDP) {
		m.AuthorizeHandler = h
	}
}

// WithTokenHandler sets a custom token endpoint handler.
func WithTokenHandler(h func(w http.ResponseWriter, r *http.Request)) MockUpstreamOption {
	return func(m *MockUpstreamIDP) {
		m.TokenHandler = h
	}
}

// WithUserInfoHandler sets a custom userinfo endpoint handler.
func WithUserInfoHandler(h func(w http.ResponseWriter, r *http.Request)) MockUpstreamOption {
	return func(m *MockUpstreamIDP) {
		m.UserInfoHandler = h
	}
}

// NewMockUpstreamIDP creates a mock upstream IDP for testing.
// The server is automatically started and will be ready when this function returns.
func NewMockUpstreamIDP(tb testing.TB, opts ...MockUpstreamOption) *MockUpstreamIDP {
	tb.Helper()

	mock := &MockUpstreamIDP{tb: tb}

	// Apply options
	for _, opt := range opts {
		opt(mock)
	}

	// Set default handlers if not provided
	if mock.AuthorizeHandler == nil {
		mock.AuthorizeHandler = mock.defaultAuthorizeHandler
	}
	if mock.TokenHandler == nil {
		mock.TokenHandler = mock.defaultTokenHandler
	}
	if mock.UserInfoHandler == nil {
		mock.UserInfoHandler = mock.defaultUserInfoHandler
	}

	// Create HTTP server with routing
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", mock.AuthorizeHandler)
	mux.HandleFunc("/token", mock.TokenHandler)
	mux.HandleFunc("/userinfo", mock.UserInfoHandler)

	// Add OIDC discovery endpoint
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		mock.discoveryHandler(w, r)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		mock.discoveryHandler(w, r)
	})

	mock.Server = httptest.NewServer(mux)

	tb.Cleanup(func() {
		mock.Server.Close()
	})

	tb.Logf("Mock upstream IDP started at: %s", mock.Server.URL)

	return mock
}

// URL returns the base URL of the mock upstream.
func (m *MockUpstreamIDP) URL() string {
	return m.Server.URL
}

// defaultAuthorizeHandler returns an authorization code via redirect.
func (*MockUpstreamIDP) defaultAuthorizeHandler(w http.ResponseWriter, r *http.Request) {
	redirectURI := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")

	if redirectURI == "" {
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	}

	// Normalize and validate redirect URL to prevent open redirects.
	// Replace backslashes with forward slashes before parsing the URL,
	// since some browsers may treat them as path separators.
	redirectURI = strings.ReplaceAll(redirectURI, "\\", "/")

	redirectURL, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}

	// Only allow local redirects (no external host).
	if redirectURL.Hostname() != "" {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}

	// Build redirect URL with authorization code
	q := redirectURL.Query()
	q.Set("code", "mock-auth-code-12345")
	if state != "" {
		q.Set("state", state)
	}
	redirectURL.RawQuery = q.Encode()

	http.Redirect(w, r, redirectURL.String(), http.StatusFound)
}

// defaultTokenHandler exchanges an auth code for tokens.
func (*MockUpstreamIDP) defaultTokenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	grantType := r.FormValue("grant_type")
	if grantType != "authorization_code" && grantType != "refresh_token" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "unsupported_grant_type",
			"error_description": "grant type not supported",
		})
		return
	}

	// Return mock tokens
	response := map[string]interface{}{
		"access_token":  "mock-upstream-access-token",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": "mock-upstream-refresh-token",
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// defaultUserInfoHandler returns mock user information.
func (*MockUpstreamIDP) defaultUserInfoHandler(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	response := map[string]interface{}{
		"sub":   "mock-user-id-12345",
		"name":  "Test User",
		"email": "testuser@example.com",
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// discoveryHandler returns OIDC/OAuth discovery document.
func (m *MockUpstreamIDP) discoveryHandler(w http.ResponseWriter, _ *http.Request) {
	baseURL := m.Server.URL

	doc := map[string]interface{}{
		"issuer":                 baseURL,
		"authorization_endpoint": baseURL + "/authorize",
		"token_endpoint":         baseURL + "/token",
		"userinfo_endpoint":      baseURL + "/userinfo",
		"jwks_uri":               baseURL + "/.well-known/jwks.json",
		"response_types_supported": []string{
			"code",
			"token",
		},
		"grant_types_supported": []string{
			"authorization_code",
			"refresh_token",
		},
		"scopes_supported": []string{
			"openid",
			"profile",
			"email",
			"offline_access",
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

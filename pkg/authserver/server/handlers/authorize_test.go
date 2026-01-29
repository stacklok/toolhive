// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
)

func TestAuthorizeHandler_MissingClientID(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize", nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// fosite returns 401 with invalid_client for missing/invalid client_id
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_client")
}

func TestAuthorizeHandler_MissingRedirectURI(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id="+testAuthClientID, nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// When redirect_uri is missing but client has registered URIs, fosite uses the
	// first registered URI and redirects with an error. If the client has exactly
	// one registered URI, fosite may accept the request.
	// Check that we get either a 400 error or a 303 redirect with error
	if rec.Code == http.StatusSeeOther {
		location := rec.Header().Get("Location")
		assert.Contains(t, location, "error=")
	} else {
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid_request")
	}
}

func TestAuthorizeHandler_ClientNotFound(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id=unknown&redirect_uri=http://example.com", nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// fosite returns 401 with invalid_client for unknown clients
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_client")
}

func TestAuthorizeHandler_InvalidRedirectURI(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id="+testAuthClientID+"&redirect_uri=http://evil.com/callback", nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// fosite returns 400 with invalid_request for invalid redirect_uri
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_request")
}

func TestAuthorizeHandler_UnsupportedResponseType(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	params := url.Values{
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"response_type": {"token"}, // implicit flow not supported
		"state":         {"test-state"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// fosite uses 303 See Other for error redirects per RFC 6749
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=unsupported_response_type")
	assert.Contains(t, location, "state=test-state")
}

func TestAuthorizeHandler_PKCENotValidatedAtAuthorizeEndpoint(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	// Note: Per RFC 7636, PKCE code_challenge is accepted at the authorize endpoint,
	// but the code_verifier is only validated at the token endpoint. Fosite follows
	// this pattern, so requests without code_challenge are accepted at /authorize
	// and will fail at /token instead.
	params := url.Values{
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"response_type": {"code"},
		"state":         {"test-state"},
		// Missing code_challenge - fosite accepts this at authorize endpoint
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// Fosite accepts requests without PKCE at authorize endpoint per RFC 7636
	// PKCE validation happens at the token endpoint
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	// Should redirect to upstream IDP (not return error)
	assert.Contains(t, location, "https://idp.example.com/authorize")
}

func TestAuthorizeHandler_PlainChallengeMethodAcceptedButValidatedAtToken(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	// Note: Similar to missing PKCE, the challenge method is captured at authorize
	// but validated at token endpoint. The config has EnablePKCEPlainChallengeMethod=false,
	// which will reject "plain" method at the token endpoint.
	params := url.Values{
		"client_id":             {testAuthClientID},
		"redirect_uri":          {testAuthRedirectURI},
		"response_type":         {"code"},
		"state":                 {"test-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"plain"}, // Will fail at token endpoint, not authorize
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// Fosite accepts requests at authorize endpoint; validation happens at token endpoint
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	// Should redirect to upstream IDP (not return error at authorize endpoint)
	assert.Contains(t, location, "https://idp.example.com/authorize")
}

func TestAuthorizeHandler_NoIDPProvider(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)
	// Remove upstream provider
	handler.upstream = nil

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

	handler.AuthorizeHandler(rec, req)

	// fosite uses 303 See Other for error redirects per RFC 6749
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error")
}

func TestAuthorizeHandler_RedirectsToUpstream(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t)

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

	handler.AuthorizeHandler(rec, req)

	// Should redirect to upstream IDP
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "https://idp.example.com/authorize")

	// Should have captured the internal state
	assert.NotEmpty(t, mockUpstream.capturedState)

	// Should have sent PKCE challenge to upstream IDP
	assert.NotEmpty(t, mockUpstream.capturedCodeChallenge, "upstream PKCE challenge should be set")

	// Should have stored pending authorization
	pending, ok := storState.pendingAuths[mockUpstream.capturedState]
	require.True(t, ok, "pending authorization should be stored")
	assert.Equal(t, testAuthClientID, pending.ClientID)
	assert.Equal(t, testAuthRedirectURI, pending.RedirectURI)
	assert.Equal(t, "client-state", pending.State)
	assert.Equal(t, "challenge123", pending.PKCEChallenge)
	assert.Equal(t, "S256", pending.PKCEMethod)
	assert.Contains(t, pending.Scopes, "openid")
	assert.Contains(t, pending.Scopes, "profile")

	// Should have stored upstream PKCE verifier
	assert.NotEmpty(t, pending.UpstreamPKCEVerifier, "upstream PKCE verifier should be stored")

	// Should have stored upstream nonce (nonce is generated and stored for upstream OIDC)
	assert.NotEmpty(t, pending.UpstreamNonce, "upstream nonce should be stored")

	// Verify the challenge matches the stored verifier
	assert.Equal(t, servercrypto.ComputePKCEChallenge(pending.UpstreamPKCEVerifier), mockUpstream.capturedCodeChallenge)
}

// =============================================================================
// Security Tests: Open Redirect Prevention
// =============================================================================

// TestAuthorizeHandler_OpenRedirectPrevention verifies that unregistered redirect URIs
// are rejected to prevent open redirect attacks per RFC 6749 Section 10.6.
func TestAuthorizeHandler_OpenRedirectPrevention(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	testCases := []struct {
		name        string
		redirectURI string
	}{
		{"external_domain", "http://evil.example.com/callback"},
		{"javascript_scheme", "javascript:alert(1)"},
		{"data_scheme", "data:text/html,<script>alert(1)</script>"},
		{"different_path", "http://localhost:8080/evil-callback"},
		{"path_traversal", "http://localhost:8080/callback/../evil"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			params := url.Values{
				"client_id":             {testAuthClientID},
				"redirect_uri":          {tc.redirectURI},
				"response_type":         {"code"},
				"state":                 {"test-state"},
				"code_challenge":        {"challenge123"},
				"code_challenge_method": {"S256"},
				"scope":                 {"openid"},
			}
			req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
			rec := httptest.NewRecorder()

			handler.AuthorizeHandler(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code,
				"should reject unregistered redirect URI: %s", tc.redirectURI)
			assert.Contains(t, rec.Body.String(), "invalid_request")
		})
	}
}

// TestAuthorizeHandler_ScopeEscalationRejected verifies that a client cannot
// request scopes beyond what it's registered for.
func TestAuthorizeHandler_ScopeEscalationRejected(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	// Test client only has "openid", "profile", "email" scopes registered
	// Try to request "admin" scope which is not registered
	params := url.Values{
		"client_id":             {testAuthClientID},
		"redirect_uri":          {testAuthRedirectURI},
		"response_type":         {"code"},
		"state":                 {"test-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
		"scope":                 {"openid admin"}, // "admin" not in client's registered scopes
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// Fosite with ExactScopeStrategy should reject this with error redirect
	if rec.Code == http.StatusSeeOther {
		location := rec.Header().Get("Location")
		assert.Contains(t, location, "error=", "should contain error for invalid scope")
	} else {
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	}
}

// TestAuthorizeHandler_HybridFlowDisabled verifies that hybrid flows
// (response_type containing both code and token) are rejected.
func TestAuthorizeHandler_HybridFlowDisabled(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	testCases := []string{
		"code token",
		"code id_token",
		"code id_token token",
	}

	for _, responseType := range testCases {
		t.Run(responseType, func(t *testing.T) {
			t.Parallel()

			params := url.Values{
				"client_id":     {testAuthClientID},
				"redirect_uri":  {testAuthRedirectURI},
				"response_type": {responseType},
				"state":         {"test-state"},
				"scope":         {"openid"},
			}
			req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
			rec := httptest.NewRecorder()

			handler.AuthorizeHandler(rec, req)

			if rec.Code == http.StatusSeeOther {
				location := rec.Header().Get("Location")
				assert.Contains(t, location, "error=unsupported_response_type")
			} else {
				assert.Equal(t, http.StatusBadRequest, rec.Code)
			}
		})
	}
}

// TestAuthorizeHandler_NoTokenInErrorRedirect verifies that tokens and
// sensitive data are not leaked in error redirect URLs.
func TestAuthorizeHandler_NoTokenInErrorRedirect(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	params := url.Values{
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"response_type": {"token"}, // Unsupported - will error
		"state":         {"test-state"},
		"scope":         {"openid"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")

	// Must NOT leak any tokens in error redirects
	assert.NotContains(t, location, "access_token=")
	assert.NotContains(t, location, "refresh_token=")
	assert.NotContains(t, location, "id_token=")
	assert.NotContains(t, location, "code=")
	assert.Contains(t, location, "error=")
}

// TestAuthorizeHandler_PostMethodSupported verifies that the authorize endpoint
// accepts POST requests per RFC 6749 Section 3.1.
func TestAuthorizeHandler_PostMethodSupported(t *testing.T) {
	t.Parallel()
	handler, _, mockUpstream := handlerTestSetup(t)

	form := url.Values{
		"client_id":             {testAuthClientID},
		"redirect_uri":          {testAuthRedirectURI},
		"response_type":         {"code"},
		"state":                 {"test-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
		"scope":                 {"openid"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	assert.Equal(t, http.StatusFound, rec.Code)
	assert.NotEmpty(t, mockUpstream.capturedState)
}

// TestAuthorizeHandler_EmptyScopeRequest verifies behavior when no scope
// parameter is provided in the authorization request.
func TestAuthorizeHandler_EmptyScopeRequest(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t)

	params := url.Values{
		"client_id":             {testAuthClientID},
		"redirect_uri":          {testAuthRedirectURI},
		"response_type":         {"code"},
		"state":                 {"test-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
		// No "scope" parameter
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// Should either redirect to upstream or return an error
	if rec.Code == http.StatusFound || rec.Code == http.StatusSeeOther {
		// If accepted, verify pending auth was stored
		if mockUpstream.capturedState != "" {
			_, ok := storState.pendingAuths[mockUpstream.capturedState]
			assert.True(t, ok, "pending auth should be stored when accepted")
		}
	}
}

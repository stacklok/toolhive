// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestTokenHandler_MissingGrantType(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	// POST with empty body (no grant_type)
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.TokenHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_request")
}

func TestTokenHandler_UnsupportedGrantType(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	form := url.Values{
		"grant_type": {"client_credentials"}, // Not supported
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.TokenHandler(rec, req)

	// fosite returns invalid_request for unsupported grant types when the handler isn't registered
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_request")
}

func TestTokenHandler_MissingCode(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"code_verifier": {"test-verifier-12345678901234567890123456789012345"},
		// Missing "code"
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.TokenHandler(rec, req)

	// fosite returns invalid_grant when code is missing (treated as invalid/empty code)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_grant")
}

func TestTokenHandler_InvalidCode(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"code":          {"invalid-code"},
		"code_verifier": {"test-verifier-12345678901234567890123456789012345"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.TokenHandler(rec, req)

	// fosite returns invalid_grant for codes it cannot find
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_grant")
}

func TestTokenHandler_MissingCodeVerifier(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	form := url.Values{
		"grant_type":   {"authorization_code"},
		"client_id":    {testAuthClientID},
		"redirect_uri": {testAuthRedirectURI},
		"code":         {"some-code"},
		// Missing "code_verifier" - PKCE is enforced
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.TokenHandler(rec, req)

	// fosite returns invalid_grant when PKCE verifier is missing but was required
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	// The error could be invalid_request or invalid_grant depending on fosite's validation order
	body := rec.Body.String()
	assert.True(t, strings.Contains(body, "invalid_request") || strings.Contains(body, "invalid_grant"),
		"expected invalid_request or invalid_grant, got: %s", body)
}

func TestTokenHandler_InvalidClient(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {"unknown-client"},
		"redirect_uri":  {"http://example.com/callback"},
		"code":          {"some-code"},
		"code_verifier": {"test-verifier-12345678901234567890123456789012345"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.TokenHandler(rec, req)

	// fosite returns invalid_client for unknown clients
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_client")
}

func TestTokenHandler_Success(t *testing.T) {
	t.Parallel()
	handler, storState, _ := handlerTestSetup(t)

	// First, simulate the authorize flow to create a valid authorization code
	// This creates the stored session that the token endpoint will retrieve
	authorizeCode := simulateAuthorizeFlow(t, handler, storState)

	// Now exchange the code for tokens
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"code":          {authorizeCode},
		"code_verifier": {testPKCEVerifier},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.TokenHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "expected 200 OK, got %d: %s", rec.Code, rec.Body.String())

	// Verify response contains expected token fields
	body := rec.Body.String()
	assert.Contains(t, body, "access_token")
	assert.Contains(t, body, "token_type")
	assert.Contains(t, body, "expires_in")
}

func TestTokenHandler_ResourceParameter(t *testing.T) {
	t.Parallel()
	handler, storState, _ := handlerTestSetup(t)

	// Simulate authorize flow
	authorizeCode := simulateAuthorizeFlow(t, handler, storState)

	// Exchange code with RFC 8707 resource parameter
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"code":          {authorizeCode},
		"code_verifier": {testPKCEVerifier},
		"resource":      {"https://api.example.com"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.TokenHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "expected 200 OK, got %d: %s", rec.Code, rec.Body.String())

	// The resource parameter should be granted as audience in the JWT
	// We can't easily verify the JWT contents here without decoding,
	// but we verify the request succeeded
	body := rec.Body.String()
	assert.Contains(t, body, "access_token")
}

func TestTokenHandler_RouteRegistered(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	router := handler.Routes()

	req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// Should not return 404 (route not found) or 405 (method not allowed)
	require.NotEqual(t, http.StatusNotFound, rec.Code, "POST /oauth/token route should be registered")
	require.NotEqual(t, http.StatusMethodNotAllowed, rec.Code, "POST method should be allowed")
}

// testPKCEVerifier is a valid PKCE verifier (43-128 characters, URL-safe).
const testPKCEVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

// simulateAuthorizeFlow runs through the authorize and callback flow to produce
// a valid authorization code that can be exchanged at the token endpoint.
func simulateAuthorizeFlow(t *testing.T, handler *Handler, storState *testStorageState) string {
	t.Helper()

	// Step 1: Store a pending authorization (simulating what AuthorizeHandler does)
	internalState := "test-internal-state-" + t.Name()
	pkceChallenge := servercrypto.ComputePKCEChallenge(testPKCEVerifier)

	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        pkceChallenge,
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        internalState,
		UpstreamPKCEVerifier: "upstream-verifier-12345678901234567890",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	// Step 2: Call the callback handler to exchange upstream code and issue our code
	callbackReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	callbackRec := httptest.NewRecorder()

	handler.CallbackHandler(callbackRec, callbackReq)

	require.Equal(t, http.StatusSeeOther, callbackRec.Code,
		"callback should redirect, got %d: %s", callbackRec.Code, callbackRec.Body.String())

	// Extract the authorization code from the redirect URL
	location := callbackRec.Header().Get("Location")
	require.NotEmpty(t, location, "callback should set Location header")

	redirectURL, err := url.Parse(location)
	require.NoError(t, err, "callback Location should be a valid URL")

	code := redirectURL.Query().Get("code")
	require.NotEmpty(t, code, "callback redirect should include authorization code")

	return code
}

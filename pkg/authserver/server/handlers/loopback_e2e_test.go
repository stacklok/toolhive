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

const loopbackE2EClientID = "loopback-e2e-client"

// driveLoopbackAuthorizeAndCallback runs a loopback dynamic-port client through
// /oauth/authorize and the simulated upstream callback, returning the handler
// (so the caller can exchange the code at /oauth/token) and the minted
// authorization code. It proves the fix end to end: AuthorizeHandler rewrites
// the redirect_uri for fosite's validation but preserves the dynamic port in
// PendingAuthorization, and CallbackHandler's code issuance sources the
// client redirect from that same PendingAuthorization.
func driveLoopbackAuthorizeAndCallback(t *testing.T, dynamicRedirectURI string) (*Handler, string) {
	t.Helper()

	handler, storState, mockUpstream := handlerTestSetup(t)
	registerLoopbackClient(t, storState, loopbackE2EClientID, "http://localhost/callback")

	pkceChallenge := servercrypto.ComputePKCEChallenge(testPKCEVerifier)

	authParams := url.Values{
		"client_id":             {loopbackE2EClientID},
		"redirect_uri":          {dynamicRedirectURI},
		"response_type":         {"code"},
		"state":                 {"client-state"},
		"code_challenge":        {pkceChallenge},
		"code_challenge_method": {"S256"},
	}
	authReq := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+authParams.Encode(), nil)
	authRec := httptest.NewRecorder()
	handler.AuthorizeHandler(authRec, authReq)
	require.Equal(t, http.StatusFound, authRec.Code,
		"authorize should redirect to upstream, got %d: %s", authRec.Code, authRec.Body.String())

	internalState := mockUpstream.capturedState
	require.NotEmpty(t, internalState, "upstream authorization URL should have been built with the internal state")

	callbackReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	callbackRec := httptest.NewRecorder()
	handler.CallbackHandler(callbackRec, callbackReq)
	require.Equal(t, http.StatusSeeOther, callbackRec.Code,
		"callback should redirect with an authorization code, got %d: %s", callbackRec.Code, callbackRec.Body.String())

	location := callbackRec.Header().Get("Location")
	require.True(t, strings.HasPrefix(location, dynamicRedirectURI),
		"callback should redirect back to the dynamic-port redirect_uri actually used at /authorize, got: %s", location)

	redirectURL, err := url.Parse(location)
	require.NoError(t, err)
	code := redirectURL.Query().Get("code")
	require.NotEmpty(t, code, "callback redirect should include an authorization code")

	return handler, code
}

// loopbackTokenExchange posts the given redirect_uri and code to /oauth/token.
func loopbackTokenExchange(t *testing.T, handler *Handler, code, redirectURI string) *httptest.ResponseRecorder {
	t.Helper()

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {loopbackE2EClientID},
		"redirect_uri":  {redirectURI},
		"code":          {code},
		"code_verifier": {testPKCEVerifier},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.TokenHandler(rec, req)
	return rec
}

// TestLoopbackLocalhost_FullFlow_TokenExchangeSucceeds is the core
// verification: a client registered with a portless http://localhost/callback
// requests authorization with a dynamic port (RFC 8252 §7.3), completes the
// upstream callback, and presents that SAME dynamic-port redirect_uri at
// /oauth/token. The token exchange must succeed -- proving the dynamic port
// survives authorize -> callback -> token without a redirect_uri mismatch.
func TestLoopbackLocalhost_FullFlow_TokenExchangeSucceeds(t *testing.T) {
	t.Parallel()

	const dynamicRedirectURI = "http://localhost:54321/callback"
	handler, code := driveLoopbackAuthorizeAndCallback(t, dynamicRedirectURI)

	rec := loopbackTokenExchange(t, handler, code, dynamicRedirectURI)

	require.Equal(t, http.StatusOK, rec.Code,
		"token exchange with the same dynamic-port redirect_uri should succeed, got %d: %s", rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "access_token")
}

// TestLoopbackLocalhost_FullFlow_TokenExchangeRejectsDifferentPort proves the
// fix doesn't weaken RFC 6749 §10.6's authorization-code/redirect-URI binding:
// presenting a different port than what was used at /authorize must still be
// rejected at /oauth/token.
func TestLoopbackLocalhost_FullFlow_TokenExchangeRejectsDifferentPort(t *testing.T) {
	t.Parallel()

	const dynamicRedirectURI = "http://localhost:54321/callback"
	handler, code := driveLoopbackAuthorizeAndCallback(t, dynamicRedirectURI)

	rec := loopbackTokenExchange(t, handler, code, "http://localhost:9999/callback")

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"token exchange with a different port than used at /authorize must be rejected, got %d: %s", rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "invalid_grant")
}

// TestLoopbackLocalhost_FullFlow_TokenExchangeRejectsDifferentPath proves the
// same binding holds for a different path, not just a different port.
func TestLoopbackLocalhost_FullFlow_TokenExchangeRejectsDifferentPath(t *testing.T) {
	t.Parallel()

	const dynamicRedirectURI = "http://localhost:54321/callback"
	handler, code := driveLoopbackAuthorizeAndCallback(t, dynamicRedirectURI)

	rec := loopbackTokenExchange(t, handler, code, "http://localhost:54321/other-path")

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"token exchange with a different path than used at /authorize must be rejected, got %d: %s", rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "invalid_grant")
}

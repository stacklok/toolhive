// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

func TestCallbackHandler_MissingState(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=test-code", nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "missing state")
}

func TestCallbackHandler_MissingCode(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?state=test-state", nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "missing code")
}

func TestCallbackHandler_PendingAuthorizationNotFound(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=test-code&state=unknown-state", nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "not found")
}

func TestCallbackHandler_UpstreamError(t *testing.T) {
	t.Parallel()
	handler, storState, _ := handlerTestSetup(t)

	// Store a pending authorization
	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        internalState,
		SessionID:            "session-upstream-error",
		UpstreamProviderName: "test-upstream",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?error=access_denied&error_description=User+denied&state="+internalState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// fosite uses 303 See Other for error redirects per RFC 6749
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=access_denied")
	assert.Contains(t, location, "state=client-state")

	// Pending authorization should be deleted
	_, ok := storState.pendingAuths[internalState]
	assert.False(t, ok, "pending authorization should be deleted")
}

// TestCallbackHandler_UpstreamError_LoopbackLocalhostRedirectsToDynamicPort
// proves that an upstream-IDP error (e.g. the user denies consent) for a
// "localhost" loopback client redirects to the client's real dynamic-port
// listener from pending.RedirectURI, not the registered portless literal and
// not a bare JSON body. See loopbackAuthorizeRequester (authorize.go) and
// wrapLoopbackErrorRequester's use in buildAuthorizeRequesterFromPending for
// the mechanism.
func TestCallbackHandler_UpstreamError_LoopbackLocalhostRedirectsToDynamicPort(t *testing.T) {
	t.Parallel()
	handler, storState, _ := handlerTestSetup(t)

	const clientID = "loopback-callback-error-client"
	registerLoopbackClient(t, storState, clientID, "http://localhost/callback")

	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:             clientID,
		RedirectURI:          "http://localhost:54321/callback",
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        internalState,
		SessionID:            "session-loopback-upstream-error",
		UpstreamProviderName: "test-upstream",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?error=access_denied&error_description=User+denied&state="+internalState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code,
		"the error must redirect to the client's real listener, not fall back to a JSON body; body: %s", rec.Body.String())
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=access_denied")
	assert.Contains(t, location, "state=client-state")
	assert.True(t, strings.HasPrefix(location, "http://localhost:54321/callback"),
		"error redirect must target the client's real dynamic-port listener, got: %s", location)
}

func TestCallbackHandler_ExchangeCodeFailure(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t)

	// Configure upstream to fail code exchange
	mockUpstream.exchangeErr = assert.AnError
	mockUpstream.exchangeResult = nil

	// Store a pending authorization
	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        internalState,
		SessionID:            "session-exchange-fail",
		UpstreamProviderName: "test-upstream",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// fosite uses 303 See Other for error redirects per RFC 6749
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error")
	assert.Contains(t, location, "state=client-state")
}

func TestCallbackHandler_Success(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t)

	// Store a pending authorization with upstream PKCE verifier
	internalState := testInternalState
	upstreamVerifier := "test-upstream-pkce-verifier-12345678901234567890"
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        internalState,
		UpstreamPKCEVerifier: upstreamVerifier,
		SessionID:            "session-success",
		UpstreamProviderName: "test-upstream",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// Should redirect to client with our authorization code
	// fosite uses 303 See Other for redirects per RFC 6749
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, testAuthRedirectURI)
	assert.Contains(t, location, "code=")
	assert.Contains(t, location, "state=client-state")
	assert.NotContains(t, location, "error=")

	// Verify upstream code was exchanged with PKCE verifier
	assert.Equal(t, "upstream-code", mockUpstream.capturedCode)
	assert.Equal(t, upstreamVerifier, mockUpstream.capturedCodeVerifier, "PKCE verifier should be passed to upstream")

	// Pending authorization should be deleted
	_, ok := storState.pendingAuths[internalState]
	assert.False(t, ok, "pending authorization should be deleted")

	// IDP tokens should be stored
	assert.GreaterOrEqual(t, storState.idpTokenCount, 1)
}

// TestCallbackHandler_SyntheticIdentity_BypassesUserResolver verifies that an
// Identity with Synthetic=true never reaches UserResolver — no `users` row,
// no `provider_identities` row. Guards against unbounded growth of those
// tables under per-token-rotating synthesized subjects.
func TestCallbackHandler_SyntheticIdentity_BypassesUserResolver(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t)

	// Synthesized-shaped subject + Synthetic=true mirrors production.
	mockUpstream.exchangeResult.Subject = "tk-deadbeefdeadbeefdeadbeefdeadbeef"
	mockUpstream.exchangeResult.Synthetic = true

	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        internalState,
		UpstreamPKCEVerifier: "test-upstream-pkce-verifier-12345678901234567890",
		SessionID:            "session-synthetic",
		UpstreamProviderName: "test-upstream",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	usersBefore := len(storState.users)
	identitiesBefore := len(storState.providerIdentities)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// Auth flow succeeds end-to-end.
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, testAuthRedirectURI)
	assert.Contains(t, location, "code=")
	assert.NotContains(t, location, "error=")

	// IDP tokens still persist — synthesis bypasses user resolution only.
	assert.GreaterOrEqual(t, storState.idpTokenCount, 1,
		"synthetic identity must still persist upstream tokens")

	// The bypass: no user row, no provider_identity row.
	assert.Equal(t, usersBefore, len(storState.users))
	assert.Equal(t, identitiesBefore, len(storState.providerIdentities))

	// Stored UserID is the synthesized subject directly (no UUID indirection).
	require.NotEmpty(t, storState.upstreamTokens, "upstream tokens should have been stored")
	for _, tok := range storState.upstreamTokens {
		assert.Equal(t, "tk-deadbeefdeadbeefdeadbeefdeadbeef", tok.UserID,
			"UserID on stored upstream tokens must be the synthesized subject")
	}
}

func TestCallbackHandler_ScopeFiltering(t *testing.T) {
	t.Parallel()
	handler, storState, _ := handlerTestSetup(t)

	// The test client is registered with scopes ["openid", "profile", "email"].
	// Create a pending authorization that includes an unregistered scope.
	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "sneaky_admin"},
		InternalState:        internalState,
		UpstreamPKCEVerifier: "test-upstream-pkce-verifier-12345678901234567890",
		SessionID:            "session-scope-filter",
		UpstreamProviderName: "test-upstream",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// Should redirect successfully with an authorization code
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "code=")
	assert.NotContains(t, location, "error=")

	// Inspect the stored auth code session to verify granted scopes.
	// The mock CreateAuthorizeCodeSession stores the requester in storState.authCodeSessions.
	require.NotEmpty(t, storState.authCodeSessions, "expected an auth code session to be stored")
	for _, session := range storState.authCodeSessions {
		granted := session.GetGrantedScopes()
		assert.Contains(t, granted, "openid", "openid should be granted (registered on client)")
		assert.NotContains(t, granted, "sneaky_admin", "sneaky_admin must NOT be granted (not registered on client)")
	}
}

func TestCallbackHandler_UnknownUpstreamProvider(t *testing.T) {
	t.Parallel()
	handler, storState, _ := handlerTestSetup(t)

	// Store a pending authorization with a provider name that doesn't exist in the handler's map
	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        internalState,
		SessionID:            "session-unknown-provider",
		UpstreamProviderName: "nonexistent-provider",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=test-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// fosite uses 303 See Other for error redirects per RFC 6749
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error")
}

func TestCallbackHandler_ProviderMismatchRejected(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t)

	// The handler is configured with upstreamName = "test-upstream" (from handlerTestSetup).
	// Store a pending authorization that was originated by a different upstream ("github").
	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        internalState,
		UpstreamProviderName: "github",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// fosite uses 303 See Other for error redirects per RFC 6749
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error")
	assert.Contains(t, location, "state=client-state")

	// Verify no upstream code exchange was attempted
	assert.Empty(t, mockUpstream.capturedCode, "upstream code exchange must not be attempted on provider mismatch")
}

func TestCallbackHandler_IdentityResolutionFailure(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t)

	// Configure upstream to fail identity resolution (now part of ExchangeCodeForIdentity)
	mockUpstream.exchangeErr = assert.AnError
	mockUpstream.exchangeResult = nil

	// Store a pending authorization
	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        internalState,
		SessionID:            "session-identity-fail",
		UpstreamProviderName: "test-upstream",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// Should fail because exchange/identity resolution failed
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=")
	assert.Contains(t, location, "failed+to+exchange+authorization+code")
}

// --- Multi-upstream chain tests ---

func TestCallbackHandler_UserResolutionFailure_UserNotProvisioned_DeniesAccess(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t, withCreateUserError(storage.ErrUserNotProvisioned))

	// A subject with no existing provider identity takes UserResolver's create
	// path, where the configured storage refuses to auto-provision it.
	mockUpstream.exchangeResult = &upstream.Identity{
		Tokens: &upstream.Tokens{
			AccessToken: "upstream-access-token",
			ExpiresAt:   time.Now().Add(time.Hour),
		},
		Subject: "unprovisioned-user",
	}

	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        internalState,
		SessionID:            "session-not-provisioned",
		UpstreamProviderName: "test-upstream",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=access_denied",
		"a deliberate provisioning refusal must deny the login, not surface as a server error")
}

func TestCallbackHandler_UserResolutionFailure_OtherError_ServerError(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t, withCreateUserError(assert.AnError))

	mockUpstream.exchangeResult = &upstream.Identity{
		Tokens: &upstream.Tokens{
			AccessToken: "upstream-access-token",
			ExpiresAt:   time.Now().Add(time.Hour),
		},
		Subject: "some-user",
	}

	internalState := testInternalState
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge123",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        internalState,
		SessionID:            "session-other-error",
		UpstreamProviderName: "test-upstream",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[internalState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=upstream-code&state="+internalState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error",
		"an internal storage failure unrelated to provisioning refusal keeps mapping to server_error")
}

func TestCallbackHandler_TwoUpstreams_FirstLeg_RedirectsToSecond(t *testing.T) {
	t.Parallel()
	handler, storState, provider1, _ := multiUpstreamTestSetup(t)

	// Simulate the first leg callback: provider-1's authorization code arrives.
	sessionID := "chain-session-1"
	firstLegState := "first-leg-state-abc"
	firstLegVerifier := "first-leg-pkce-verifier-123456789012345678"

	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-original-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        firstLegState,
		UpstreamPKCEVerifier: firstLegVerifier,
		UpstreamNonce:        "first-leg-nonce",
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[firstLegState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider1-code&state="+firstLegState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// Should redirect to provider-2 (HTTP 302), not issue auth code (HTTP 303)
	assert.Equal(t, http.StatusFound, rec.Code, "first leg should redirect to second upstream, not complete")
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "https://idp2.example.com/authorize", "redirect should point to provider-2's authorization URL")

	// provider-1's code should have been exchanged
	assert.Equal(t, "provider1-code", provider1.capturedCode, "provider-1 should have exchanged the code")
	assert.Equal(t, firstLegVerifier, provider1.capturedCodeVerifier, "PKCE verifier should be passed to provider-1")

	// provider-1's tokens should now be stored
	key1 := sessionID + ":provider-1"
	require.Contains(t, storState.upstreamTokens, key1, "provider-1 tokens should be stored")
	assert.Equal(t, "provider-1", storState.upstreamTokens[key1].ProviderID)

	// A new PendingAuthorization for provider-2 should have been stored
	var nextPending *storage.PendingAuthorization
	for state, p := range storState.pendingAuths {
		if state != firstLegState && p.UpstreamProviderName == "provider-2" {
			nextPending = p
			break
		}
	}
	require.NotNil(t, nextPending, "a new pending authorization for provider-2 should exist")
	assert.Equal(t, "provider-2", nextPending.UpstreamProviderName, "next leg targets provider-2")
	assert.Equal(t, sessionID, nextPending.SessionID, "sessionID must be threaded through")

	// Identity resolved from first leg should be carried forward
	assert.NotEmpty(t, nextPending.ResolvedUserID, "ResolvedUserID should be set from first leg")
	assert.Equal(t, "First Leg User", nextPending.ResolvedUserName, "ResolvedUserName should come from first leg")
	assert.Equal(t, "firstleg@example.com", nextPending.ResolvedUserEmail, "ResolvedUserEmail should come from first leg")

	// Fresh secrets: InternalState must differ from the first leg
	assert.NotEqual(t, firstLegState, nextPending.InternalState, "second leg must have fresh InternalState")
}

func TestCallbackHandler_TwoUpstreams_SecondLeg_IssuesCode(t *testing.T) {
	t.Parallel()
	handler, storState, _, provider2 := multiUpstreamTestSetup(t)

	sessionID := "chain-session-2"

	// Pre-populate storage with provider-1's tokens for this session (first leg already completed)
	key1 := sessionID + ":provider-1"
	storState.upstreamTokens[key1] = &storage.UpstreamTokens{
		ProviderID:   "provider-1",
		AccessToken:  "provider1-access-token",
		RefreshToken: "provider1-refresh-token",
		IDToken:      "provider1-id-token",
		ExpiresAt:    time.Now().Add(time.Hour),
		ClientID:     testAuthClientID,
		UserID:       "resolved-user-id-from-leg1",
	}

	// Set up the second leg's pending authorization (as would be created by continueChainOrComplete)
	secondLegState := "second-leg-state-xyz"
	secondLegVerifier := "second-leg-pkce-verifier-98765432109876543210"
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-original-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        secondLegState,
		UpstreamPKCEVerifier: secondLegVerifier,
		UpstreamNonce:        "second-leg-nonce",
		UpstreamProviderName: "provider-2",
		SessionID:            sessionID,
		ChainUpstreams:       []string{"provider-1", "provider-2"},
		ResolvedUserID:       "resolved-user-id-from-leg1",
		ResolvedUserName:     "First Leg User",
		ResolvedUserEmail:    "firstleg@example.com",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[secondLegState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider2-code&state="+secondLegState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// All upstreams satisfied: should issue authorization code (HTTP 303)
	assert.Equal(t, http.StatusSeeOther, rec.Code, "second leg should issue auth code")
	location := rec.Header().Get("Location")
	assert.Contains(t, location, testAuthRedirectURI, "redirect should be to client's redirect_uri")
	assert.Contains(t, location, "code=", "redirect should include authorization code")
	assert.Contains(t, location, "state=client-original-state", "redirect should include client's state")
	assert.NotContains(t, location, "error=", "redirect should not contain an error")

	// provider-2's code should have been exchanged
	assert.Equal(t, "provider2-code", provider2.capturedCode, "provider-2 should have exchanged the code")
	assert.Equal(t, secondLegVerifier, provider2.capturedCodeVerifier)

	// Both providers' tokens should exist under the same session
	key2 := sessionID + ":provider-2"
	assert.Contains(t, storState.upstreamTokens, key1, "provider-1 tokens should still exist")
	assert.Contains(t, storState.upstreamTokens, key2, "provider-2 tokens should be stored")

	// Pending should be deleted (single-use)
	_, ok := storState.pendingAuths[secondLegState]
	assert.False(t, ok, "second leg pending should be consumed")
}

func TestCallbackHandler_TwoUpstreams_FilterDropsSecondLeg_IssuesCode(t *testing.T) {
	t.Parallel()
	// Filter keeps nothing, so the chain narrows to just the first upstream.
	filter := &stubUpstreamFilter{keep: []string{}}
	handler, storState, provider1, _ := multiUpstreamTestSetup(t, WithUpstreamFilter(filter))

	sessionID := "chain-session-filter-drop"
	firstLegState := "filter-drop-first-leg-state"
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state-drop",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        firstLegState,
		UpstreamPKCEVerifier: "filter-drop-verifier-1234567890123456789012",
		UpstreamNonce:        "filter-drop-nonce",
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[firstLegState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider1-code&state="+firstLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	// The filtered-out second leg means the chain completes at the first upstream:
	// issue the authorization code (303) rather than redirect onward (302).
	assert.Equal(t, http.StatusSeeOther, rec.Code, "filtered chain should complete at the first upstream")
	location := rec.Header().Get("Location")
	assert.Contains(t, location, testAuthRedirectURI, "redirect should be to the client's redirect_uri")
	assert.Contains(t, location, "code=", "redirect should include an authorization code")
	assert.NotContains(t, location, "error=", "redirect should not contain an error")

	// The filter was consulted exactly once, with the non-first upstream names.
	assert.Equal(t, 1, filter.calls, "filter should be consulted once on the first leg")
	assert.Equal(t, []string{"provider-2"}, filter.capturedArgs, "filter receives non-first upstreams")

	// provider-1's code was exchanged and no second-leg pending was created.
	assert.Equal(t, "provider1-code", provider1.capturedCode)
	for state, p := range storState.pendingAuths {
		assert.NotEqualf(t, "provider-2", p.UpstreamProviderName,
			"no second-leg pending should be created (state %q)", state)
	}
}

func TestCallbackHandler_TwoUpstreams_FilterKeepsSecondLeg_CarriesChain(t *testing.T) {
	t.Parallel()
	filter := &stubUpstreamFilter{keep: []string{"provider-2"}}
	handler, storState, _, _ := multiUpstreamTestSetup(t, WithUpstreamFilter(filter))

	sessionID := "chain-session-filter-keep"
	firstLegState := "filter-keep-first-leg-state"
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state-keep",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        firstLegState,
		UpstreamPKCEVerifier: "filter-keep-verifier-1234567890123456789012",
		UpstreamNonce:        "filter-keep-nonce",
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[firstLegState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider1-code&state="+firstLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	// Kept provider-2, so the chain continues onward.
	assert.Equal(t, http.StatusFound, rec.Code, "kept second leg should redirect onward")
	assert.Contains(t, rec.Header().Get("Location"), "https://idp2.example.com/authorize")
	assert.Equal(t, 1, filter.calls, "filter should be consulted once on the first leg")

	// The effective chain must be carried into the next leg's pending so the
	// filter is not re-run per leg.
	var nextPending *storage.PendingAuthorization
	for state, p := range storState.pendingAuths {
		if state != firstLegState && p.UpstreamProviderName == "provider-2" {
			nextPending = p
			break
		}
	}
	require.NotNil(t, nextPending, "a second-leg pending should exist")
	assert.Equal(t, []string{"provider-1", "provider-2"}, nextPending.ChainUpstreams,
		"the computed chain should be carried forward")
}

func TestCallbackHandler_FilterError_FailsAuthorization(t *testing.T) {
	t.Parallel()
	filter := &stubUpstreamFilter{err: errors.New("filter unavailable")}
	handler, storState, _, _ := multiUpstreamTestSetup(t, WithUpstreamFilter(filter))

	sessionID := "chain-session-filter-error"
	firstLegState := "filter-error-first-leg-state"
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state-error",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        firstLegState,
		UpstreamPKCEVerifier: "filter-error-verifier-123456789012345678901",
		UpstreamNonce:        "filter-error-nonce",
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[firstLegState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider1-code&state="+firstLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	// A filter error fails the authorization cleanly with a server error — no
	// silent fallback to walking every upstream.
	assert.Equal(t, http.StatusSeeOther, rec.Code, "filter error should produce a fosite error redirect")
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error", "should surface a server error")
	assert.Contains(t, location, "state=client-state-error", "should preserve client state")
	assert.Equal(t, 1, filter.calls)

	// A filter error does not invalidate provider-1's already-stored credential, so it is
	// preserved; only the authorization fails.
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-1",
		"a filter error must not delete the already-stored credential")
}

func TestCallbackHandler_SecondLeg_ReusesChain_DoesNotReRunFilter(t *testing.T) {
	t.Parallel()
	// A filter that errors if consulted — proving the second leg reuses the chain
	// from the pending authorization rather than re-running the filter.
	filter := &stubUpstreamFilter{err: errors.New("must not be called on a subsequent leg")}
	handler, storState, _, provider2 := multiUpstreamTestSetup(t, WithUpstreamFilter(filter))

	sessionID := "chain-session-reuse"
	key1 := sessionID + ":provider-1"
	storState.upstreamTokens[key1] = &storage.UpstreamTokens{
		ProviderID:  "provider-1",
		AccessToken: "provider1-access-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		ClientID:    testAuthClientID,
		UserID:      "resolved-user-id-from-leg1",
	}

	secondLegState := "reuse-second-leg-state"
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state-reuse",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        secondLegState,
		UpstreamPKCEVerifier: "reuse-verifier-98765432109876543210987654",
		UpstreamNonce:        "reuse-nonce",
		UpstreamProviderName: "provider-2",
		SessionID:            sessionID,
		ChainUpstreams:       []string{"provider-1", "provider-2"}, // computed on the first leg
		ResolvedUserID:       "resolved-user-id-from-leg1",
		ResolvedUserName:     "First Leg User",
		ResolvedUserEmail:    "firstleg@example.com",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[secondLegState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider2-code&state="+secondLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code, "second leg should issue the authorization code")
	assert.Contains(t, rec.Header().Get("Location"), "code=")
	assert.Equal(t, 0, filter.calls, "filter must not be re-run on a subsequent leg")
	assert.Equal(t, "provider2-code", provider2.capturedCode)
}

func TestCallbackHandler_Filter_ReceivesFirstUpstreamIdentity(t *testing.T) {
	t.Parallel()
	filter := &stubUpstreamFilter{keep: []string{"provider-2"}}
	handler, storState, provider1, _ := multiUpstreamTestSetup(t, WithUpstreamFilter(filter))

	// Model an OIDC first upstream that resolved ID-token claims an authz filter
	// would key on. (The real OIDC provider's capture of these claims is covered by
	// TestOIDCProviderImpl_ExchangeCodeForIdentity/"captures ID token claims…"; this
	// test covers the callback -> filter propagation.)
	provider1.providerType = upstream.ProviderTypeOIDC
	provider1.exchangeResult.Claims = map[string]any{
		"sub":    "user-from-provider1",
		"email":  "firstleg@example.com",
		"groups": []any{"engineering", "admins"},
	}

	sessionID := "chain-session-filter-identity"
	firstLegState := "filter-identity-first-leg-state"
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state-identity",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        firstLegState,
		UpstreamPKCEVerifier: "filter-identity-verifier-123456789012345678",
		UpstreamNonce:        "filter-identity-nonce",
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[firstLegState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider1-code&state="+firstLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	// Kept provider-2, so the chain continues onward (proving the filter ran).
	require.Equal(t, http.StatusFound, rec.Code, "kept second leg should redirect onward")
	require.Equal(t, 1, filter.calls, "filter should be consulted once on the first leg")

	// The filter received the first upstream's resolved identity.
	assert.Equal(t, "user-from-provider1", filter.capturedPrincipal.Subject,
		"principal.Subject must be the claim-mapped upstream subject")
	assert.Equal(t, "firstleg@example.com", filter.capturedPrincipal.Email,
		"principal.Email must come from the first upstream")
	assert.Equal(t, map[string]any{
		"sub":    "user-from-provider1",
		"email":  "firstleg@example.com",
		"groups": []any{"engineering", "admins"},
	}, filter.capturedPrincipal.Claims, "principal.Claims must carry the first upstream's claims")

	// principal.PlatformUserID is the canonical (resolved) ToolHive user: non-empty
	// and distinct from the raw upstream subject.
	assert.NotEmpty(t, filter.capturedPrincipal.PlatformUserID, "platform user ID must be populated")
	assert.NotEqual(t, "user-from-provider1", filter.capturedPrincipal.PlatformUserID,
		"platform user ID is the canonical ToolHive user, not the raw upstream subject")
}

func TestCallbackHandler_SubsequentLeg_MissingChain_FailsClosed(t *testing.T) {
	t.Parallel()
	handler, storState, _, _ := multiUpstreamTestSetup(t)

	sessionID := "stale-pending-session"
	const leg1User = "resolved-user-id-from-leg1"

	// First leg already completed.
	storState.upstreamTokens[sessionID+":provider-1"] = &storage.UpstreamTokens{
		ProviderID:  "provider-1",
		AccessToken: "p1-at",
		ExpiresAt:   time.Now().Add(time.Hour),
		ClientID:    testAuthClientID,
		UserID:      leg1User,
	}

	// A subsequent-leg pending that predates ChainUpstreams: ResolvedUserID is set
	// (not a first leg) but ChainUpstreams is empty, as an older build would have
	// written it mid-rollout.
	secondLegState := "stale-pending-state"
	storState.pendingAuths[secondLegState] = &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-original-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        secondLegState,
		UpstreamPKCEVerifier: "stale-verifier-1234567890123456789012345678",
		UpstreamNonce:        "stale-nonce",
		UpstreamProviderName: "provider-2",
		SessionID:            sessionID,
		ResolvedUserID:       leg1User,
		ResolvedUserName:     "First Leg User",
		ResolvedUserEmail:    "firstleg@example.com",
		// ChainUpstreams intentionally empty (stale pending).
		CreatedAt: time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider2-code&state="+secondLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	// The stale pending is rejected (fail closed) rather than recomputing the chain
	// against this later leg's context.
	assert.Equal(t, http.StatusSeeOther, rec.Code, "stale pending should produce a fosite error redirect")
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error")
	assert.Contains(t, location, "state=client-original-state")

	// A stale pending fails the authorization closed but does not invalidate any
	// credential, so both the earlier leg and the leg just processed are preserved.
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-1",
		"the earlier leg's token must survive a fail-closed stale pending")
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-2",
		"the just-processed leg's token must survive a fail-closed stale pending")
}

func TestCallbackHandler_SingleLeg_IssuesCodeWithoutChaining(t *testing.T) {
	t.Parallel()
	handler, storState, provider1, _ := multiUpstreamTestSetup(t)

	// A SingleLeg pending targets provider-1 only. provider-2 is configured but has
	// no tokens for this session, so the default chain logic would redirect into it.
	// SingleLeg must suppress that and issue the authorization code immediately.
	sessionID := "single-leg-session"
	legState := "single-leg-state-abc"
	legVerifier := "single-leg-pkce-verifier-12345678901234567890"

	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-original-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        legState,
		UpstreamPKCEVerifier: legVerifier,
		UpstreamNonce:        "single-leg-nonce",
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		SingleLeg:            true,
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[legState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider1-code&state="+legState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// Should issue the authorization code (HTTP 303), not redirect to provider-2 (HTTP 302).
	assert.Equal(t, http.StatusSeeOther, rec.Code, "single-leg flow should issue auth code, not chain to provider-2")
	location := rec.Header().Get("Location")
	assert.Contains(t, location, testAuthRedirectURI, "redirect should be to client's redirect_uri")
	assert.Contains(t, location, "code=", "redirect should include authorization code")
	assert.NotContains(t, location, "idp2.example.com", "must not redirect into the second upstream")

	// provider-1's code should have been exchanged and its tokens stored.
	assert.Equal(t, "provider1-code", provider1.capturedCode, "provider-1 should have exchanged the code")
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-1", "provider-1 tokens should be stored")

	// No second-leg pending authorization should have been created for provider-2.
	for state, p := range storState.pendingAuths {
		assert.NotEqualf(t, "provider-2", p.UpstreamProviderName,
			"no chain leg should be created for provider-2 (state=%s)", state)
	}
}

func TestCallbackHandler_TwoUpstreams_IdentityFromFirstLeg(t *testing.T) {
	t.Parallel()
	handler, storState, _, _ := multiUpstreamTestSetup(t)

	sessionID := "chain-session-identity"
	firstLegUserID := "first-leg-user-id-stable"

	// Pre-populate provider-1's tokens so that GetAllUpstreamTokens returns it
	key1 := sessionID + ":provider-1"
	storState.upstreamTokens[key1] = &storage.UpstreamTokens{
		ProviderID:   "provider-1",
		AccessToken:  "p1-at",
		RefreshToken: "p1-rt",
		ExpiresAt:    time.Now().Add(time.Hour),
		ClientID:     testAuthClientID,
		UserID:       firstLegUserID,
	}

	// Pre-populate the user and provider identity so UserResolver can find it
	// (it should NOT be called for second leg, but the user must exist for
	// writeAuthorizationResponse -> session creation)
	storState.users[firstLegUserID] = &storage.User{
		ID:        firstLegUserID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Second leg pending carries ResolvedUserID from first leg
	secondLegState := "identity-test-state"
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        secondLegState,
		UpstreamPKCEVerifier: "identity-test-verifier-1234567890123456789",
		UpstreamNonce:        "identity-test-nonce",
		UpstreamProviderName: "provider-2",
		SessionID:            sessionID,
		ChainUpstreams:       []string{"provider-1", "provider-2"},
		ResolvedUserID:       firstLegUserID,
		ResolvedUserName:     "First Leg Name",
		ResolvedUserEmail:    "firstleg@example.com",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[secondLegState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=p2-code&state="+secondLegState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// Should complete successfully (all upstreams satisfied)
	require.Equal(t, http.StatusSeeOther, rec.Code, "should issue auth code")

	// The stored upstream tokens for provider-2 should have UserID from the first leg,
	// NOT from provider-2's exchange result
	key2 := sessionID + ":provider-2"
	require.Contains(t, storState.upstreamTokens, key2)
	assert.Equal(t, firstLegUserID, storState.upstreamTokens[key2].UserID,
		"UserID on provider-2 tokens should be the first leg's resolved user ID")

	// Verify the auth code session was created with the first leg's identity
	require.NotEmpty(t, storState.authCodeSessions, "auth code session should be stored")
}

func TestCallbackHandler_TwoUpstreams_IdentityMismatch_RejectsChain(t *testing.T) {
	t.Parallel()
	handler, storState, _, _ := multiUpstreamTestSetup(t)

	sessionID := "chain-session-mismatch"

	// Pre-populate provider-1's tokens with a DIFFERENT UserID than what the
	// pending authorization carries as ResolvedUserID. This simulates a tampered
	// or corrupted chain state where the identity drifted between legs.
	key1 := sessionID + ":provider-1"
	storState.upstreamTokens[key1] = &storage.UpstreamTokens{
		ProviderID:   "provider-1",
		AccessToken:  "provider1-access-token",
		RefreshToken: "provider1-refresh-token",
		IDToken:      "provider1-id-token",
		ExpiresAt:    time.Now().Add(time.Hour),
		ClientID:     testAuthClientID,
		UserID:       "tampered-user-id", // does NOT match ResolvedUserID below
	}

	// Set up the second leg's pending authorization with a different ResolvedUserID
	secondLegState := "mismatch-second-leg-state"
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state-mismatch",
		PKCEChallenge:        "challenge-mismatch",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        secondLegState,
		UpstreamPKCEVerifier: "mismatch-verifier-12345678901234567890123",
		UpstreamNonce:        "mismatch-nonce",
		UpstreamProviderName: "provider-2",
		SessionID:            sessionID,
		ChainUpstreams:       []string{"provider-1", "provider-2"},
		ResolvedUserID:       "correct-user-id", // does NOT match provider-1's UserID above
		ResolvedUserName:     "Correct User",
		ResolvedUserEmail:    "correct@example.com",
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[secondLegState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider2-code&state="+secondLegState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// Should reject with a fosite error redirect (303), not issue an auth code
	assert.Equal(t, http.StatusSeeOther, rec.Code, "should return fosite error redirect")
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error", "should contain server_error")
	assert.Contains(t, location, "state=client-state-mismatch", "should preserve client state")

	// Upstream tokens should be cleaned up
	for key := range storState.upstreamTokens {
		assert.Failf(t, "upstream tokens should be cleaned up",
			"found leftover token with key %q", key)
	}
}

func TestCallbackHandler_TwoUpstreams_FreshSecretsPerLeg(t *testing.T) {
	t.Parallel()
	handler, storState, _, _ := multiUpstreamTestSetup(t)

	sessionID := "chain-session-secrets"
	firstLegState := "secrets-test-first-state"
	firstLegVerifier := "secrets-test-first-verifier-12345678901234"
	firstLegNonce := "secrets-test-first-nonce"

	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        firstLegState,
		UpstreamPKCEVerifier: firstLegVerifier,
		UpstreamNonce:        firstLegNonce,
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[firstLegState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=p1-code&state="+firstLegState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// Should redirect to provider-2, creating a new pending
	require.Equal(t, http.StatusFound, rec.Code, "first leg should redirect to second upstream")

	// Find the pending authorization created for the second leg
	var nextPending *storage.PendingAuthorization
	for state, p := range storState.pendingAuths {
		if state != firstLegState && p.UpstreamProviderName == "provider-2" {
			nextPending = p
			break
		}
	}
	require.NotNil(t, nextPending, "second leg pending must exist")

	// All per-leg secrets must be freshly generated and different from the first leg
	assert.NotEqual(t, firstLegState, nextPending.InternalState,
		"InternalState must differ between legs")
	assert.NotEqual(t, firstLegVerifier, nextPending.UpstreamPKCEVerifier,
		"UpstreamPKCEVerifier must differ between legs")
	assert.NotEqual(t, firstLegNonce, nextPending.UpstreamNonce,
		"UpstreamNonce must differ between legs")

	// The new secrets should be non-empty (generated, not zero-value)
	assert.NotEmpty(t, nextPending.InternalState, "InternalState must not be empty")
	assert.NotEmpty(t, nextPending.UpstreamPKCEVerifier, "UpstreamPKCEVerifier must not be empty")
	assert.NotEmpty(t, nextPending.UpstreamNonce, "UpstreamNonce must not be empty")

	// Client request fields should be preserved unchanged
	assert.Equal(t, testAuthClientID, nextPending.ClientID)
	assert.Equal(t, testAuthRedirectURI, nextPending.RedirectURI)
	assert.Equal(t, "client-state", nextPending.State)
	assert.Equal(t, "client-challenge", nextPending.PKCEChallenge)
	assert.Equal(t, "S256", nextPending.PKCEMethod)
}

func TestCallbackHandler_TwoUpstreams_AuthorizationURLError_PreservesTokens(t *testing.T) {
	t.Parallel()
	handler, storState, _, mockProvider2 := multiUpstreamTestSetup(t)

	// Configure provider-2 to fail when building the authorization URL
	mockProvider2.authURLErr = errors.New("authorization URL error")

	// Set up a first-leg pending authorization for provider-1.
	// No pre-existing tokens — the first leg callback stores provider-1 tokens,
	// then continueChainOrComplete finds provider-2 missing and tries to redirect.
	sessionID := "chain-session-authurl-err"
	firstLegState := "authurl-err-first-leg-state"
	pending := &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state-authurl",
		PKCEChallenge:        "challenge-authurl",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        firstLegState,
		UpstreamPKCEVerifier: "authurl-err-verifier-123456789012345678901",
		UpstreamNonce:        "authurl-err-nonce",
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		CreatedAt:            time.Now(),
	}
	storState.pendingAuths[firstLegState] = pending

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=p1-code&state="+firstLegState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// Should NOT be a redirect to the next upstream (302) — it should be a fosite
	// error redirect (303) back to the client with an error.
	assert.Equal(t, http.StatusSeeOther, rec.Code, "should return fosite error redirect, not upstream redirect")
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error", "should contain server_error")
	assert.Contains(t, location, "state=client-state-authurl", "should preserve client state")

	// The completed first leg's credential is preserved — a failure building the NEXT
	// leg's authorization URL does not invalidate it.
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-1",
		"the first leg's token must survive a next-leg authorization-URL failure")

	// The ephemeral pending authorizations are still cleaned up: the first-leg pending was
	// deleted on load, and the second-leg pending is deleted on this error path.
	for state, p := range storState.pendingAuths {
		assert.Failf(t, "no pending authorizations should remain",
			"found pending for provider %q with state %q", p.UpstreamProviderName, state)
	}
}

// TestCallbackHandler_MismatchCleanup_LeavesFilteredOutProvider is the regression guard
// for the one path that deletes upstream tokens — the confirmed identity-mismatch
// fail-closed. That cleanup must be scoped to the EFFECTIVE chain, not to every
// configured provider: a provider the upstream filter excluded from the chain is still
// the user's live credential and must survive.
//
// This is stronger than skip-unconfigured coverage: the survivor here (provider-3) IS a
// configured upstream, so the only thing keeping it alive is that cleanup deletes the
// effective chain rather than the full configured set. Three providers are configured;
// the effective chain carried into this (final) leg is [provider-1, provider-2], as a
// first-leg filter would have narrowed it, excluding provider-3.
func TestCallbackHandler_MismatchCleanup_LeavesFilteredOutProvider(t *testing.T) {
	t.Parallel()

	provider, oauth2Config, stor, storState := baseTestSetup(t)
	p2 := &mockIDPProvider{
		providerType: upstream.ProviderTypeOAuth2,
		exchangeResult: &upstream.Identity{
			Tokens:  &upstream.Tokens{AccessToken: "p2-at", ExpiresAt: time.Now().Add(time.Hour)},
			Subject: "user-from-provider2",
		},
	}
	upstreams := []NamedUpstream{
		{Name: "provider-1", Provider: &mockIDPProvider{providerType: upstream.ProviderTypeOAuth2}},
		{Name: "provider-2", Provider: p2},
		{Name: "provider-3", Provider: &mockIDPProvider{providerType: upstream.ProviderTypeOAuth2}},
	}
	handler, err := NewHandler(provider, oauth2Config, stor, upstreams)
	require.NoError(t, err)

	sessionID := "mismatch-scoping-session"

	// provider-1's stored token disagrees with the carried subject → verifyChainIdentity
	// will report a confirmed mismatch and tear down the effective chain.
	storState.upstreamTokens[sessionID+":provider-1"] = &storage.UpstreamTokens{
		ProviderID:  "provider-1",
		AccessToken: "p1-at",
		ExpiresAt:   time.Now().Add(time.Hour),
		ClientID:    testAuthClientID,
		UserID:      "tampered-user-id",
	}
	// provider-3 is a configured upstream the filter excluded from this chain; it is the
	// user's live credential and must survive the whole-chain cleanup.
	filteredOutKey := sessionID + ":provider-3"
	storState.upstreamTokens[filteredOutKey] = &storage.UpstreamTokens{
		ProviderID:  "provider-3",
		AccessToken: "keep-me",
		ExpiresAt:   time.Now().Add(time.Hour),
		ClientID:    testAuthClientID,
		UserID:      "correct-user-id",
	}

	// Final leg (provider-2) of an effective chain that excludes provider-3.
	secondLegState := "mismatch-scoping-state"
	storState.pendingAuths[secondLegState] = &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-original-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        secondLegState,
		UpstreamPKCEVerifier: "mismatch-scoping-verifier-123456789012345678",
		UpstreamNonce:        "mismatch-scoping-nonce",
		UpstreamProviderName: "provider-2",
		SessionID:            sessionID,
		ChainUpstreams:       []string{"provider-1", "provider-2"},
		ResolvedUserID:       "correct-user-id",
		CreatedAt:            time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider2-code&state="+secondLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code, "a confirmed mismatch should fail closed")

	// The effective chain [provider-1, provider-2] is torn down...
	assert.NotContains(t, storState.upstreamTokens, sessionID+":provider-1",
		"an in-chain provider should be cleaned up on a confirmed mismatch")
	assert.NotContains(t, storState.upstreamTokens, sessionID+":provider-2",
		"an in-chain provider should be cleaned up on a confirmed mismatch")
	// ...but the configured provider the filter excluded from the chain survives. If
	// cleanup widened from the effective chain to every configured provider, this fails.
	require.Contains(t, storState.upstreamTokens, filteredOutKey,
		"a configured provider outside the effective chain must survive the cleanup")
	assert.Equal(t, "keep-me", storState.upstreamTokens[filteredOutKey].AccessToken)
}

// TestCallbackHandler_VerifyChainIdentity_StorageFailure_PreservesTokens covers the
// distinction the callback draws between verifyChainIdentity's two failure modes. A
// transient GetAllUpstreamTokens failure on the identity check — the second bulk read,
// after nextMissingUpstream's first read succeeds — is NOT a confirmed mismatch, so the
// authorization fails closed but every upstream token is preserved. Only a confirmed
// mismatch tears the chain down.
func TestCallbackHandler_VerifyChainIdentity_StorageFailure_PreservesTokens(t *testing.T) {
	t.Parallel()
	// Fail the SECOND GetAllUpstreamTokens call: nextMissingUpstream's read (call 1)
	// succeeds, verifyChainIdentity's read (call 2) fails.
	handler, storState, _, _ := multiUpstreamTestSetupWithStorage(t,
		withGetAllUpstreamTokensErrorAfterCalls(1, errors.New("storage unavailable")))

	sessionID := "verify-storage-fail-session"
	const leg1User = "resolved-user-id-from-leg1"

	// First leg keyed to leg1User, so identities agree — the ONLY failure is the
	// transient read, not a mismatch.
	storState.upstreamTokens[sessionID+":provider-1"] = &storage.UpstreamTokens{
		ProviderID:  "provider-1",
		AccessToken: "p1-at",
		ExpiresAt:   time.Now().Add(time.Hour),
		ClientID:    testAuthClientID,
		UserID:      leg1User,
	}

	secondLegState := "verify-storage-fail-state"
	storState.pendingAuths[secondLegState] = &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-original-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        secondLegState,
		UpstreamPKCEVerifier: "verify-storage-fail-verifier-1234567890123456",
		UpstreamNonce:        "verify-storage-fail-nonce",
		UpstreamProviderName: "provider-2",
		SessionID:            sessionID,
		ChainUpstreams:       []string{"provider-1", "provider-2"},
		ResolvedUserID:       leg1User,
		CreatedAt:            time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider2-code&state="+secondLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code, "a transient identity-check read failure should fail authorization")

	// Neither leg is deleted: a storage failure is not a confirmed mismatch.
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-1",
		"a transient identity-check read failure must not delete tokens")
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-2",
		"a transient identity-check read failure must not delete tokens")
}

// TestCleanupUpstreamTokens_SkipsUnconfiguredAndEmpty exercises cleanupUpstreamTokens
// directly: it skips empty names and any provider that is not a configured upstream — the
// guard that stops a corrupted or tampered pending row reaching a pre-resolveChain
// cleanup site from naming an arbitrary delete target — while removing the configured
// ones.
func TestCleanupUpstreamTokens_SkipsUnconfiguredAndEmpty(t *testing.T) {
	t.Parallel()
	handler, storState, _, _ := multiUpstreamTestSetup(t)

	sessionID := "direct-cleanup-session"
	for _, p := range []string{"provider-1", "provider-2", "not-configured"} {
		storState.upstreamTokens[sessionID+":"+p] = &storage.UpstreamTokens{
			ProviderID:  p,
			AccessToken: "at",
			ExpiresAt:   time.Now().Add(time.Hour),
			ClientID:    testAuthClientID,
			UserID:      "user-1",
		}
	}

	handler.cleanupUpstreamTokens(t.Context(), sessionID,
		[]string{"provider-1", "", "not-configured", "provider-2"})

	// Configured providers are removed...
	assert.NotContains(t, storState.upstreamTokens, sessionID+":provider-1")
	assert.NotContains(t, storState.upstreamTokens, sessionID+":provider-2")
	// ...while the unconfigured name is skipped (its row survives) and the empty name is
	// a no-op rather than a panic.
	assert.Contains(t, storState.upstreamTokens, sessionID+":not-configured",
		"a name that is not a configured upstream must not be deleted")
}

// TestCallbackHandler_StoreUpstreamTokensError_LeavesExistingTokens covers the
// StoreUpstreamTokens-failure path: a failed store wrote nothing, so it must delete
// nothing. The user's earlier leg (provider-1) and any existing credential for the leg
// being processed (a re-connect of provider-2 whose fresh store failed) are both left
// intact rather than wiped by the failure.
func TestCallbackHandler_StoreUpstreamTokensError_LeavesExistingTokens(t *testing.T) {
	t.Parallel()
	handler, storState, _, _ := multiUpstreamTestSetupWithStorage(t,
		withStoreUpstreamTokensError(errors.New("storage unavailable")))

	sessionID := "store-err-session"
	const leg1User = "resolved-user-id-from-leg1"

	// First leg already completed: provider-1's token exists.
	storState.upstreamTokens[sessionID+":provider-1"] = &storage.UpstreamTokens{
		ProviderID:  "provider-1",
		AccessToken: "p1-at",
		ExpiresAt:   time.Now().Add(time.Hour),
		ClientID:    testAuthClientID,
		UserID:      leg1User,
	}

	// provider-2 is already connected too — this is a re-connect whose fresh store will
	// fail. Its existing credential must survive the failure.
	existingP2 := sessionID + ":provider-2"
	storState.upstreamTokens[existingP2] = &storage.UpstreamTokens{
		ProviderID:  "provider-2",
		AccessToken: "existing-p2-at",
		ExpiresAt:   time.Now().Add(time.Hour),
		ClientID:    testAuthClientID,
		UserID:      leg1User,
	}

	secondLegState := "store-err-second-leg-state"
	storState.pendingAuths[secondLegState] = &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-original-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        secondLegState,
		UpstreamPKCEVerifier: "store-err-verifier-123456789012345678901234",
		UpstreamNonce:        "store-err-nonce",
		UpstreamProviderName: "provider-2",
		SessionID:            sessionID,
		ChainUpstreams:       []string{"provider-1", "provider-2"},
		ResolvedUserID:       leg1User,
		ResolvedUserName:     "First Leg User",
		ResolvedUserEmail:    "firstleg@example.com",
		CreatedAt:            time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider2-code&state="+secondLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code, "a store failure should produce a fosite error redirect")
	assert.Contains(t, rec.Header().Get("Location"), "error=server_error")

	// A failed store deletes nothing: the earlier leg and the existing provider-2
	// credential both survive.
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-1",
		"an earlier leg's token must survive a later leg's store failure")
	require.Contains(t, storState.upstreamTokens, existingP2,
		"an existing credential must survive a re-connect whose fresh store failed")
	assert.Equal(t, "existing-p2-at", storState.upstreamTokens[existingP2].AccessToken)
}

// TestCallbackHandler_NextMissingUpstreamError_PreservesTokens covers the
// nextMissingUpstream-failure path: a transient chain-state read (GetAllUpstreamTokens)
// error after the first leg has stored its token fails the authorization but preserves
// the token — a storage blip does not invalidate a collected credential.
func TestCallbackHandler_NextMissingUpstreamError_PreservesTokens(t *testing.T) {
	t.Parallel()
	handler, storState, _, _ := multiUpstreamTestSetupWithStorage(t,
		withGetAllUpstreamTokensError(errors.New("storage unavailable")))

	sessionID := "next-missing-err-session"
	firstLegState := "next-missing-err-first-leg-state"
	storState.pendingAuths[firstLegState] = &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state-next-missing",
		PKCEChallenge:        "challenge-next-missing",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        firstLegState,
		UpstreamPKCEVerifier: "next-missing-verifier-12345678901234567890",
		UpstreamNonce:        "next-missing-nonce",
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		CreatedAt:            time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=p1-code&state="+firstLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code, "a chain-state read failure should produce a fosite error redirect")
	assert.Contains(t, rec.Header().Get("Location"), "error=server_error")

	// The first leg's token survives a transient chain-state read failure.
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-1",
		"a transient nextMissingUpstream failure must not delete the stored token")
}

// TestCallbackHandler_WriteAuthorizationResponseError_OnSatisfiedChain_PreservesTokens
// covers the path reached when writeAuthorizationResponse fails after a multi-upstream
// chain is fully satisfied and identity-verified. That downstream failure (client lookup
// / redirect / code generation) does not invalidate the collected credentials, so all
// upstream tokens are preserved — matching the SingleLeg branch. GetClient is wired to
// fail only on its second call: the first (which builds the error-redirect requester)
// succeeds so the failure still produces a redirect, the second (inside
// writeAuthorizationResponse) drives the failure.
func TestCallbackHandler_WriteAuthorizationResponseError_OnSatisfiedChain_PreservesTokens(t *testing.T) {
	t.Parallel()
	handler, storState, _, _ := multiUpstreamTestSetupWithStorage(t,
		withGetClientErrorAfterCalls(1, errors.New("get client unavailable")))

	sessionID := "satisfied-writeresp-err-session"
	const leg1User = "resolved-user-id-from-leg1"

	// First leg already completed, keyed to leg1User so verifyChainIdentity passes.
	storState.upstreamTokens[sessionID+":provider-1"] = &storage.UpstreamTokens{
		ProviderID:  "provider-1",
		AccessToken: "p1-at",
		ExpiresAt:   time.Now().Add(time.Hour),
		ClientID:    testAuthClientID,
		UserID:      leg1User,
	}

	secondLegState := "satisfied-writeresp-err-state"
	storState.pendingAuths[secondLegState] = &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-original-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        secondLegState,
		UpstreamPKCEVerifier: "satisfied-verifier-123456789012345678901234",
		UpstreamNonce:        "satisfied-nonce",
		UpstreamProviderName: "provider-2",
		SessionID:            sessionID,
		ChainUpstreams:       []string{"provider-1", "provider-2"},
		ResolvedUserID:       leg1User,
		ResolvedUserName:     "First Leg User",
		ResolvedUserEmail:    "firstleg@example.com",
		CreatedAt:            time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider2-code&state="+secondLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code, "a response-write failure should produce a fosite error redirect")

	// Both legs are preserved: the chain is complete and identity-verified, and only the
	// downstream response write failed, which does not invalidate the credentials.
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-1",
		"the first leg's token must survive a downstream response-write failure")
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-2",
		"the final leg's token must survive a downstream response-write failure")
}

// TestCallbackHandler_Cleanup_ContinuesWhenAProviderDeleteFails covers
// cleanupUpstreamTokens' warn-and-continue path: one provider's delete errors, and the
// cleanup must still remove the sibling providers rather than aborting on the first
// failure.
func TestCallbackHandler_Cleanup_ContinuesWhenAProviderDeleteFails(t *testing.T) {
	t.Parallel()
	handler, storState, _, _ := multiUpstreamTestSetupWithStorage(t,
		withDeleteUpstreamTokensForProviderError("provider-1", errors.New("delete failed")))

	sessionID := "cleanup-partial-fail-session"

	// provider-1 has a tampered UserID so verifyChainIdentity fails and the chain is
	// cleaned up; provider-1's delete is wired to fail.
	storState.upstreamTokens[sessionID+":provider-1"] = &storage.UpstreamTokens{
		ProviderID:  "provider-1",
		AccessToken: "p1-at",
		ExpiresAt:   time.Now().Add(time.Hour),
		ClientID:    testAuthClientID,
		UserID:      "tampered-user-id",
	}

	secondLegState := "cleanup-partial-fail-state"
	storState.pendingAuths[secondLegState] = &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-original-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        secondLegState,
		UpstreamPKCEVerifier: "partial-fail-verifier-12345678901234567890",
		UpstreamNonce:        "partial-fail-nonce",
		UpstreamProviderName: "provider-2",
		SessionID:            sessionID,
		ChainUpstreams:       []string{"provider-1", "provider-2"},
		ResolvedUserID:       "correct-user-id",
		ResolvedUserName:     "Correct User",
		ResolvedUserEmail:    "correct@example.com",
		CreatedAt:            time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider2-code&state="+secondLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code)

	// provider-1's delete failed, so its row is still present...
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-1",
		"the provider whose delete failed should still be present")
	// ...but the cleanup continued and removed provider-2 anyway.
	assert.NotContains(t, storState.upstreamTokens, sessionID+":provider-2",
		"cleanup must continue past a per-provider delete failure and remove sibling providers")
}

func TestCallbackHandler_TwoUpstreams_StorePendingError_PreservesTokens(t *testing.T) {
	t.Parallel()

	provider, oauth2Config, stor, storState := baseTestSetup(t, withStorePendingError(errors.New("storage unavailable")))

	// Pre-populate the first-leg pending directly in state (bypassing Store mock)
	sessionID := "chain-session-store-err"
	firstLegState := "store-err-first-leg-state"
	storState.pendingAuths[firstLegState] = &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state-store-err",
		PKCEChallenge:        "challenge-store-err",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        firstLegState,
		UpstreamPKCEVerifier: "store-err-verifier-123456789012345678901",
		UpstreamNonce:        "store-err-nonce",
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		CreatedAt:            time.Now(),
	}

	mockP1 := &mockIDPProvider{
		providerType:     upstream.ProviderTypeOAuth2,
		authorizationURL: "https://idp1.example.com/authorize",
		exchangeResult: &upstream.Identity{
			Tokens: &upstream.Tokens{
				AccessToken:  "p1-access-token",
				RefreshToken: "p1-refresh-token",
				IDToken:      "p1-id-token",
				ExpiresAt:    time.Now().Add(time.Hour),
			},
			Subject: "user-from-p1",
			Name:    "Test User",
			Email:   "test@example.com",
		},
	}
	mockP2 := &mockIDPProvider{
		providerType:     upstream.ProviderTypeOAuth2,
		authorizationURL: "https://idp2.example.com/authorize",
	}

	upstreams := []NamedUpstream{
		{Name: "provider-1", Provider: mockP1},
		{Name: "provider-2", Provider: mockP2},
	}
	handler, err := NewHandler(provider, oauth2Config, stor, upstreams)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=p1-code&state="+firstLegState, nil)
	rec := httptest.NewRecorder()

	handler.CallbackHandler(rec, req)

	// Should be a fosite error redirect (303) back to the client, not a chain redirect (302)
	assert.Equal(t, http.StatusSeeOther, rec.Code, "should return fosite error redirect")
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error", "should contain server_error")
	assert.Contains(t, location, "state=client-state-store-err", "should preserve client state")

	// The first leg's credential is preserved — failing to persist the next leg's pending
	// does not invalidate it.
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-1",
		"the first leg's token must survive a next-leg pending store failure")
}

// TestCallbackHandler_RefreshTokenCarryForward verifies the OAuth callback's
// behavior when the upstream IdP omits refresh_token on re-authorization. The
// handler looks up a prior (user, provider) row and carries the prior RT
// forward, with a defensive UpstreamSubject guard against account-linking
// edge cases. Storage errors during the lookup are non-fatal.
func TestCallbackHandler_RefreshTokenCarryForward(t *testing.T) {
	t.Parallel()

	type priorRow struct {
		sessionID       string
		upstreamSubject string
		refreshToken    string
	}

	cases := []struct {
		name             string
		priorRow         *priorRow // nil = no prior row
		idpRefreshToken  string    // RT returned by upstream exchange
		idpSubject       string    // subject claim returned by upstream
		synthetic        bool      // upstream identity is synthetic (rotating subject)
		lookupErr        error     // if non-nil, storage lookup returns this error
		expectedStoredRT string    // expected RefreshToken on the new row
	}{
		{
			name:             "preserves prior RT when IdP omits it",
			priorRow:         &priorRow{sessionID: "old-session", upstreamSubject: "user-123", refreshToken: "rt-prior"},
			idpRefreshToken:  "",
			idpSubject:       "user-123",
			expectedStoredRT: "rt-prior",
		},
		{
			// Synthetic providers mint a fresh rotating subject every flow, so the
			// UpstreamSubject equality guard can never hold even though the stable
			// user identity (carried from the first leg) does match. The RT must
			// still be carried forward — otherwise refresh silently breaks for every
			// userinfo-less OAuth2 backend.
			name:             "synthetic carries prior RT despite rotating subject",
			priorRow:         &priorRow{sessionID: "old-session", upstreamSubject: "tk-oldrotatingsubject", refreshToken: "rt-prior"},
			idpRefreshToken:  "",
			idpSubject:       "tk-newrotatingsubject",
			synthetic:        true,
			expectedStoredRT: "rt-prior",
		},
		{
			// Guards the error state where the synthetic branch must not carry forward
			// (or panic) when there is no prior row to read from.
			name:             "synthetic with no prior row accepts empty RT",
			priorRow:         nil,
			idpRefreshToken:  "",
			idpSubject:       "tk-newrotatingsubject",
			synthetic:        true,
			expectedStoredRT: "",
		},
		{
			name:             "no carry across different upstream subjects",
			priorRow:         &priorRow{sessionID: "alice-session", upstreamSubject: "alice@idp", refreshToken: "rt-prior"},
			idpRefreshToken:  "",
			idpSubject:       "bob@idp",
			expectedStoredRT: "",
		},
		{
			name:             "fresh IdP RT wins",
			priorRow:         &priorRow{sessionID: "old-session", upstreamSubject: "user-123", refreshToken: "rt-prior"},
			idpRefreshToken:  "rt-fresh",
			idpSubject:       "user-123",
			expectedStoredRT: "rt-fresh",
		},
		{
			name:             "no prior row accepts empty RT",
			priorRow:         nil,
			idpRefreshToken:  "",
			idpSubject:       "user-123",
			expectedStoredRT: "",
		},
		{
			name:             "storage error during lookup is non-fatal",
			priorRow:         nil,
			idpRefreshToken:  "",
			idpSubject:       "user-123",
			lookupErr:        errors.New("simulated storage failure"),
			expectedStoredRT: "",
		},
		{
			name:             "does not carry prior RT when prior RT is empty",
			priorRow:         &priorRow{sessionID: "old-session", upstreamSubject: "user-123", refreshToken: ""},
			idpRefreshToken:  "",
			idpSubject:       "user-123",
			expectedStoredRT: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const (
				providerName   = "test-upstream"
				existingUserID = "user-id"
				newSessionID   = "new-session"
			)

			var opts []baseTestSetupOption
			if tc.lookupErr != nil {
				opts = append(opts, withGetLatestUpstreamTokensError(tc.lookupErr))
			}
			handler, storState, mockUpstream := handlerTestSetup(t, opts...)

			mockUpstream.exchangeResult = &upstream.Identity{
				Tokens: &upstream.Tokens{
					AccessToken:  "new-access-token",
					RefreshToken: tc.idpRefreshToken,
					IDToken:      "new-id-token",
					ExpiresAt:    time.Now().Add(time.Hour),
				},
				Subject:   tc.idpSubject,
				Synthetic: tc.synthetic,
			}

			// Pre-populate user + identity so ResolveUser is deterministic.
			storState.users[existingUserID] = &storage.User{
				ID:        existingUserID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			storState.providerIdentities[providerName+":"+tc.idpSubject] = &storage.ProviderIdentity{
				UserID:          existingUserID,
				ProviderID:      providerName,
				ProviderSubject: tc.idpSubject,
				LinkedAt:        time.Now(),
				LastUsedAt:      time.Now(),
			}

			if tc.priorRow != nil {
				storState.upstreamTokens[tc.priorRow.sessionID+":"+providerName] = &storage.UpstreamTokens{
					ProviderID:      providerName,
					AccessToken:     "old-access",
					RefreshToken:    tc.priorRow.refreshToken,
					ExpiresAt:       time.Now().Add(30 * time.Minute),
					ClientID:        testAuthClientID,
					UserID:          existingUserID,
					UpstreamSubject: tc.priorRow.upstreamSubject,
				}
			}

			internalState := testInternalState
			pendingAuth := &storage.PendingAuthorization{
				ClientID:             testAuthClientID,
				RedirectURI:          testAuthRedirectURI,
				State:                "client-state",
				PKCEChallenge:        "challenge123",
				PKCEMethod:           "S256",
				Scopes:               []string{"openid"},
				InternalState:        internalState,
				UpstreamPKCEVerifier: "verifier-1234567890123456789012345678",
				SessionID:            newSessionID,
				UpstreamProviderName: providerName,
				CreatedAt:            time.Now(),
			}
			if tc.synthetic {
				// Synthetic carry-forward only applies on a subsequent leg, where the
				// stable user identity is carried from the first leg (so the prior row
				// is found by UserID) while the provider's own subject rotates per flow.
				// A subsequent leg carries the effective chain computed on the first leg.
				pendingAuth.ResolvedUserID = existingUserID
				pendingAuth.ResolvedUserName = "First Leg User"
				pendingAuth.ResolvedUserEmail = "firstleg@example.com"
				pendingAuth.ChainUpstreams = []string{providerName}
			}
			storState.pendingAuths[internalState] = pendingAuth

			req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=test-code&state="+internalState, nil)
			rec := httptest.NewRecorder()
			handler.CallbackHandler(rec, req)

			require.Equal(t, http.StatusSeeOther, rec.Code)
			assert.NotContains(t, rec.Header().Get("Location"), "error=")

			newRow, ok := storState.upstreamTokens[newSessionID+":"+providerName]
			require.True(t, ok, "token row for new session should be stored")
			assert.Equal(t, tc.expectedStoredRT, newRow.RefreshToken)
			// Sanity-check the rest of the row was written by the callback path so a
			// regression that early-returns before StoreUpstreamTokens cannot pass.
			assert.Equal(t, "new-access-token", newRow.AccessToken)
			assert.Equal(t, "new-id-token", newRow.IDToken)
			assert.Equal(t, tc.idpSubject, newRow.UpstreamSubject)
			assert.False(t, newRow.ExpiresAt.IsZero(), "ExpiresAt must be populated")
		})
	}
}

func TestRoutesIncludeAuthorizeAndCallback(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	// Get the router with all routes registered
	router := handler.Routes()

	// Test that routes are registered
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/oauth/authorize"},
		{http.MethodGet, "/oauth/callback"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			// Should not return 404 (route not found)
			require.NotEqual(t, http.StatusNotFound, rec.Code,
				"route %s %s should be registered", tc.method, tc.path)
		})
	}
}

// TestCallbackHandler_PlacesPlatformUserInContext_OnChainRead verifies that during a
// subsequent-leg OAuth callback, the chain-consistency read (GetAllUpstreamTokens)
// runs with the resolved canonical user already in the request context.
//
// Why GetAllUpstreamTokens specifically, and not StoreUpstreamTokens: the callback
// makes several storage calls. StoreUpstreamTokens carries the user in its tokens
// argument (tokens.UserID), so a user-keyed storage decorator reads the user from
// there and does not need the context. GetAllUpstreamTokens and DeleteUpstreamTokens
// take only (ctx, sessionID) — no tokens argument — so the decorator can resolve the
// user only from ctx. This test pins the contract that the callback places the
// canonical user (via WithPlatformUser, not a stub Identity) into the context before
// those context-dependent reads run.
//
// The resolved user is carried forward from the first leg (leg1User) and is
// deliberately different from what provider-2's exchange returns, so the assertion
// catches the callback using the wrong user rather than merely a self-consistent one.
func TestCallbackHandler_PlacesPlatformUserInContext_OnChainRead(t *testing.T) {
	t.Parallel()
	handler, storState, _, _ := multiUpstreamTestSetup(t)

	sessionID := "chain-session-ctx"
	const leg1User = "resolved-user-id-from-leg1"

	// First leg already completed: provider-1's tokens exist, keyed to leg1User.
	storState.upstreamTokens[sessionID+":provider-1"] = &storage.UpstreamTokens{
		ProviderID:   "provider-1",
		AccessToken:  "p1-at",
		RefreshToken: "p1-rt",
		ExpiresAt:    time.Now().Add(time.Hour),
		ClientID:     testAuthClientID,
		UserID:       leg1User,
	}

	// Second-leg pending carries the resolved identity forward from leg 1.
	secondLegState := "chain-ctx-second-leg-state"
	storState.pendingAuths[secondLegState] = &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-original-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid", "profile"},
		InternalState:        secondLegState,
		UpstreamPKCEVerifier: "second-leg-pkce-verifier-98765432109876543210",
		UpstreamNonce:        "second-leg-nonce",
		UpstreamProviderName: "provider-2",
		SessionID:            sessionID,
		ChainUpstreams:       []string{"provider-1", "provider-2"},
		ResolvedUserID:       leg1User,
		ResolvedUserName:     "First Leg User",
		ResolvedUserEmail:    "firstleg@example.com",
		CreatedAt:            time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=provider2-code&state="+secondLegState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)

	// The callback's chain-consistency check reads GetAllUpstreamTokens; the harness
	// captured the ctx it ran with. Assert the callback had already placed the
	// resolved canonical user into that ctx, and that it is the user carried from leg 1.
	uid, ok := auth.PlatformUserFromContext(storState.getAllUpstreamCtx)
	require.True(t, ok, "callback must place platform user in ctx before the chain-consistency GetAllUpstreamTokens")
	require.Equal(t, leg1User, uid)

	// The callback must NOT place a stub Identity: a value under the identity key
	// would read as an authenticated principal to downstream consumers.
	_, hasIdentity := auth.IdentityFromContext(storState.getAllUpstreamCtx)
	require.False(t, hasIdentity, "callback must not place an Identity (only a platform user) in ctx")
}

// TestCallbackHandler_UpstreamError_LeavesExistingProviderToken is the regression guard
// for the re-connect data-loss bug: when the upstream IdP returns an error (e.g. the user
// cancels a re-connect), handleUpstreamError must delete only the single-use pending and
// leave the provider's existing token — a credential from an earlier successful
// authorization — intact. The upstream errored before this attempt exchanged a code, so
// this attempt stored nothing; deleting the provider's row would disconnect an
// already-connected account.
func TestCallbackHandler_UpstreamError_LeavesExistingProviderToken(t *testing.T) {
	t.Parallel()
	handler, storState, _, _ := multiUpstreamTestSetup(t)

	sessionID := "reconnect-cancel-session"
	const userID = "user-1"

	// provider-1 is already connected from a prior successful authorization.
	existingKey := sessionID + ":provider-1"
	storState.upstreamTokens[existingKey] = &storage.UpstreamTokens{
		ProviderID:   "provider-1",
		AccessToken:  "existing-at",
		RefreshToken: "existing-rt",
		ExpiresAt:    time.Now().Add(time.Hour),
		ClientID:     testAuthClientID,
		UserID:       userID,
	}

	// A single-leg re-connect of that same provider is in flight.
	reconnectState := "reconnect-cancel-state"
	storState.pendingAuths[reconnectState] = &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-original-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		InternalState:        reconnectState,
		UpstreamPKCEVerifier: "reconnect-verifier-1234567890123456789012345",
		UpstreamNonce:        "reconnect-nonce",
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		SingleLeg:            true,
		ResolvedUserID:       userID,
		CreatedAt:            time.Now(),
	}

	// The user cancels at the upstream: error=access_denied comes back.
	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?error=access_denied&state="+reconnectState, nil)
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code, "a canceled re-connect should redirect the error to the client")
	assert.Contains(t, rec.Header().Get("Location"), "error=access_denied")

	// The existing token survives the canceled re-connect...
	require.Contains(t, storState.upstreamTokens, existingKey,
		"canceling a re-connect must not disconnect the already-connected account")
	assert.Equal(t, "existing-at", storState.upstreamTokens[existingKey].AccessToken)
	// ...and the single-use pending authorization is consumed.
	assert.NotContains(t, storState.pendingAuths, reconnectState,
		"the single-use pending authorization should be deleted")
}

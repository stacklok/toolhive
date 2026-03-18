// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestCallbackHandler_TwoUpstreams_AuthorizationURLError_CleansUp(t *testing.T) {
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

	// Upstream tokens from the completed first leg should be cleaned up
	for key := range storState.upstreamTokens {
		assert.Failf(t, "upstream tokens should be cleaned up",
			"found leftover token with key %q", key)
	}

	// The pending authorization created for the second leg should also be cleaned up.
	// Only the first-leg pending remains (but it was deleted by CallbackHandler on load).
	for state, p := range storState.pendingAuths {
		assert.Failf(t, "no pending authorizations should remain",
			"found pending for provider %q with state %q", p.UpstreamProviderName, state)
	}
}

func TestCallbackHandler_TwoUpstreams_StorePendingError_CleansUp(t *testing.T) {
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

	// Upstream tokens should be cleaned up
	for key := range storState.upstreamTokens {
		assert.Failf(t, "upstream tokens should be cleaned up",
			"found leftover token with key %q", key)
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

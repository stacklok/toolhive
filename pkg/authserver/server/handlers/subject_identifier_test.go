// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	testSubIDAudience  = "https://mcp.example.com"
	testSubIDOtherAud  = "https://other.example.com"
	testSubIDIssuer    = "https://idp.example.com"
	testSubIDSubject   = "upstream-user-1"
	testSubIDStaleName = "stale"
	testSubIDClaim     = "oid"
	testSubIDValue     = "upstream-oid"
)

var testVerifiedUpstream = session.UpstreamSubject{
	Issuer: testSubIDIssuer, Subject: testSubIDSubject, StableClaim: testSubIDClaim, StableValue: testSubIDValue,
}

// withoutMember returns testVerifiedUpstream with one member cleared.
func withoutMember(mutate func(*session.UpstreamSubject)) session.UpstreamSubject {
	u := testVerifiedUpstream
	mutate(&u)
	return u
}

func TestApplySubjectIdentifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		grantTypes  []string
		verified    session.UpstreamSubject
		stale       bool
		granted     []string
		requested   []string
		configured  []string
		wantSubject bool
	}{
		{
			name:        "authorization_code with enabled granted audience adds sub_id",
			grantTypes:  []string{oauthproto.GrantTypeAuthorizationCode},
			verified:    testVerifiedUpstream,
			granted:     []string{testSubIDAudience},
			configured:  []string{testSubIDAudience},
			wantSubject: true,
		},
		{
			name:        "empty granted audience falls back to enabled requested audience",
			grantTypes:  []string{oauthproto.GrantTypeAuthorizationCode},
			verified:    testVerifiedUpstream,
			requested:   []string{testSubIDAudience},
			configured:  []string{testSubIDAudience},
			wantSubject: true,
		},
		{
			name:       "device_code does not fall back to requested audience",
			grantTypes: []string{oauthproto.GrantTypeDeviceCode},
			verified:   testVerifiedUpstream,
			requested:  []string{testSubIDAudience},
			configured: []string{testSubIDAudience},
		},
		{
			name:       "refresh_token does not fall back to requested audience",
			grantTypes: []string{oauthproto.GrantTypeRefreshToken},
			verified:   testVerifiedUpstream,
			requested:  []string{testSubIDAudience},
			configured: []string{testSubIDAudience},
		},
		{
			name:        "refresh_token adds sub_id",
			grantTypes:  []string{oauthproto.GrantTypeRefreshToken},
			verified:    testVerifiedUpstream,
			granted:     []string{testSubIDAudience},
			configured:  []string{testSubIDAudience},
			wantSubject: true,
		},
		{
			name:        "device_code adds sub_id",
			grantTypes:  []string{oauthproto.GrantTypeDeviceCode},
			verified:    testVerifiedUpstream,
			granted:     []string{testSubIDAudience},
			configured:  []string{testSubIDAudience},
			wantSubject: true,
		},
		{
			name:       "one of two granted audiences not enabled omits sub_id",
			grantTypes: []string{oauthproto.GrantTypeAuthorizationCode},
			verified:   testVerifiedUpstream,
			granted:    []string{testSubIDAudience, testSubIDOtherAud},
			configured: []string{testSubIDAudience},
		},
		{
			name:       "no audiences omits sub_id",
			grantTypes: []string{oauthproto.GrantTypeAuthorizationCode},
			verified:   testVerifiedUpstream,
			configured: []string{testSubIDAudience},
		},
		{
			name:       "empty setting omits sub_id",
			grantTypes: []string{oauthproto.GrantTypeAuthorizationCode},
			verified:   testVerifiedUpstream,
			granted:    []string{testSubIDAudience},
		},
		{
			name:       "refresh_token with stale sub_id and audience no longer enabled removes it",
			grantTypes: []string{oauthproto.GrantTypeRefreshToken},
			verified:   testVerifiedUpstream,
			stale:      true,
			granted:    []string{testSubIDAudience},
			configured: []string{testSubIDOtherAud},
		},
		{
			name:       "token exchange with stale sub_id removes it without re-adding",
			grantTypes: []string{oauthproto.GrantTypeTokenExchange},
			verified:   testVerifiedUpstream,
			stale:      true,
			granted:    []string{testSubIDAudience},
			configured: []string{testSubIDAudience},
		},
		{
			name:       "missing verified subject omits both claims",
			grantTypes: []string{oauthproto.GrantTypeAuthorizationCode},
			verified:   withoutMember(func(u *session.UpstreamSubject) { u.Subject = "" }),
			granted:    []string{testSubIDAudience},
			configured: []string{testSubIDAudience},
		},
		{
			name:       "missing verified issuer omits both claims",
			grantTypes: []string{oauthproto.GrantTypeAuthorizationCode},
			verified:   withoutMember(func(u *session.UpstreamSubject) { u.Issuer = "" }),
			granted:    []string{testSubIDAudience},
			configured: []string{testSubIDAudience},
		},
		{
			name:       "missing stable claim omits both claims",
			grantTypes: []string{oauthproto.GrantTypeAuthorizationCode},
			verified:   withoutMember(func(u *session.UpstreamSubject) { u.StableClaim = "" }),
			granted:    []string{testSubIDAudience},
			configured: []string{testSubIDAudience},
		},
		{
			name:       "missing stable value omits both claims",
			grantTypes: []string{oauthproto.GrantTypeAuthorizationCode},
			verified:   withoutMember(func(u *session.UpstreamSubject) { u.StableValue = "" }),
			granted:    []string{testSubIDAudience},
			configured: []string{testSubIDAudience},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := &Handler{config: &server.AuthorizationServerConfig{SubjectIdentifierAudiences: tt.configured}}

			stored := session.New("user-1", "tsid-1", "client-1", session.UserClaims{})
			stored.VerifiedUpstream = tt.verified
			if tt.stale {
				stored.JWTClaims.Extra["sub_id"] = testSubIDStaleName
				stored.JWTClaims.Extra[session.UpstreamSubjectClaimKey] = testSubIDStaleName
			}

			req := fosite.NewAccessRequest(stored)
			req.GrantTypes = fosite.Arguments(tt.grantTypes)
			req.SetRequestedAudience(fosite.Arguments(tt.requested))
			for _, a := range tt.granted {
				req.GrantAudience(a)
			}

			h.applySubjectIdentifier(req)

			got, ok := req.GetSession().(*session.Session)
			require.True(t, ok)
			require.NotSame(t, stored, got, "request must hold a clone, not the stored session")

			if tt.wantSubject {
				assert.Equal(t, map[string]string{
					"format": "iss_sub",
					"iss":    tt.verified.Issuer,
					"sub":    tt.verified.Subject,
				}, got.JWTClaims.Extra["sub_id"])
				assert.Equal(t, map[string]string{
					"iss":   tt.verified.Issuer,
					"claim": tt.verified.StableClaim,
					"value": tt.verified.StableValue,
				}, got.JWTClaims.Extra[session.UpstreamSubjectClaimKey])
			} else {
				assert.NotContains(t, got.JWTClaims.Extra, "sub_id")
				assert.NotContains(t, got.JWTClaims.Extra, session.UpstreamSubjectClaimKey)
			}

			// The originally stored session must never be mutated.
			if tt.stale {
				assert.Equal(t, testSubIDStaleName, stored.JWTClaims.Extra["sub_id"])
				assert.Equal(t, testSubIDStaleName, stored.JWTClaims.Extra[session.UpstreamSubjectClaimKey])
			} else {
				assert.NotContains(t, stored.JWTClaims.Extra, "sub_id")
				assert.NotContains(t, stored.JWTClaims.Extra, session.UpstreamSubjectClaimKey)
			}
		})
	}
}

func TestApplySubjectIdentifier_NilSession(t *testing.T) {
	t.Parallel()

	h := &Handler{config: &server.AuthorizationServerConfig{SubjectIdentifierAudiences: []string{testSubIDAudience}}}
	req := fosite.NewAccessRequest(nil)
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeAuthorizationCode}

	require.NotPanics(t, func() { h.applySubjectIdentifier(req) })
	assert.Nil(t, req.GetSession())
}

func TestApplySubjectIdentifier_NonLoginSessionType(t *testing.T) {
	t.Parallel()

	h := &Handler{config: &server.AuthorizationServerConfig{SubjectIdentifierAudiences: []string{testSubIDAudience}}}
	sess := &fosite.DefaultSession{}
	req := fosite.NewAccessRequest(sess)
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeAuthorizationCode}
	req.GrantAudience(testSubIDAudience)

	require.NotPanics(t, func() { h.applySubjectIdentifier(req) })
	require.Same(t, sess, req.GetSession())
}

func TestCarryFirstUpstreamIdentity(t *testing.T) {
	t.Parallel()

	complete := upstream.Identity{
		VerifiedIssuer: testSubIDIssuer, VerifiedSubject: testSubIDSubject,
		VerifiedStableClaim: testSubIDClaim, VerifiedStableValue: testSubIDValue,
	}
	with := func(mutate func(*upstream.Identity)) *upstream.Identity {
		id := complete
		mutate(&id)
		return &id
	}

	tests := []struct {
		name     string
		resolved string
		single   bool
		identity *upstream.Identity
		wantCopy bool
	}{
		{
			name:     "first leg with OIDC identity carries the verified identity",
			identity: with(func(*upstream.Identity) {}),
			wantCopy: true,
		},
		{
			name:     "later leg returns pending unchanged",
			resolved: "user-1",
			identity: with(func(*upstream.Identity) {}),
		},
		{
			name:     "single-leg authorization is not carried",
			single:   true,
			identity: with(func(*upstream.Identity) {}),
		},
		{
			name:     "synthetic identity is not carried",
			identity: with(func(id *upstream.Identity) { id.Synthetic = true }),
		},
		{
			name:     "identity without verified issuer is not carried",
			identity: with(func(id *upstream.Identity) { id.VerifiedIssuer = "" }),
		},
		{
			name:     "identity without verified subject is not carried",
			identity: with(func(id *upstream.Identity) { id.VerifiedSubject = "" }),
		},
		{
			name:     "identity without stable claim is not carried",
			identity: with(func(id *upstream.Identity) { id.VerifiedStableClaim = "" }),
		},
		{
			name:     "identity without stable value is not carried",
			identity: with(func(id *upstream.Identity) { id.VerifiedStableValue = "" }),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pending := &storage.PendingAuthorization{ResolvedUserID: tt.resolved, SingleLeg: tt.single}

			got := carryFirstUpstreamIdentity(pending, verifiedUpstreamIdentity(tt.identity))

			if tt.wantCopy {
				require.NotSame(t, pending, got)
				assert.Equal(t, testVerifiedUpstream, got.VerifiedUpstream)
			} else {
				assert.Same(t, pending, got)
			}
			// The input pending is never mutated.
			assert.Equal(t, session.UpstreamSubject{}, pending.VerifiedUpstream)
		})
	}
}

func verifiedIdentity() *upstream.Identity {
	return &upstream.Identity{
		VerifiedIssuer: testSubIDIssuer, VerifiedSubject: testSubIDSubject,
		VerifiedStableClaim: testSubIDClaim, VerifiedStableValue: testSubIDValue,
	}
}

// TestCallbackHandler_RecordsVerifiedUpstreamOnlyWhenDisclosureEnabled checks that
// the upstream identity is stored on the authorize code session, and carried to the
// next leg of a chain, only when SubjectIdentifierAudiences is non-empty.
func TestCallbackHandler_RecordsVerifiedUpstreamOnlyWhenDisclosureEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		audiences []string
		want      session.UpstreamSubject
	}{
		{name: "disclosure disabled records nothing"},
		{name: "disclosure enabled records the identity", audiences: []string{testSubIDAudience}, want: testVerifiedUpstream},
	}

	for _, tt := range tests {
		t.Run(tt.name+" - authorization code", func(t *testing.T) {
			t.Parallel()
			handler, storState, mockUpstream := handlerTestSetup(t)
			handler.config.SubjectIdentifierAudiences = tt.audiences
			id := verifiedIdentity()
			id.Tokens = mockUpstream.exchangeResult.Tokens
			id.Subject = "user-123"
			mockUpstream.exchangeResult = id

			pending := &storage.PendingAuthorization{
				ClientID: testAuthClientID, RedirectURI: testAuthRedirectURI, State: "client-state",
				PKCEChallenge: "challenge123", PKCEMethod: "S256", Scopes: []string{"openid"},
				InternalState: testInternalState, UpstreamPKCEVerifier: "test-upstream-pkce-verifier-12345678901234567890",
				SessionID: "session-sub-id", UpstreamProviderName: "test-upstream", CreatedAt: time.Now(),
			}
			cookie := bindPending(t, storState, testInternalState, pending)

			rec := httptest.NewRecorder()
			handler.CallbackHandler(rec, newCallbackRequest("code=upstream-code&state="+testInternalState, cookie))
			require.Equal(t, http.StatusSeeOther, rec.Code)

			require.Len(t, storState.authCodeSessions, 1)
			for _, req := range storState.authCodeSessions {
				sess, ok := req.GetSession().(*session.Session)
				require.True(t, ok)
				assert.Equal(t, tt.want, sess.VerifiedUpstream)
			}
		})

		t.Run(tt.name+" - next leg of a chain", func(t *testing.T) {
			t.Parallel()
			handler, storState, provider1, _ := multiUpstreamTestSetup(t)
			handler.config.SubjectIdentifierAudiences = tt.audiences
			id := verifiedIdentity()
			id.Tokens = provider1.exchangeResult.Tokens
			id.Subject = provider1.exchangeResult.Subject
			provider1.exchangeResult = id

			firstLegState := "first-leg-state-sub-id"
			pending := &storage.PendingAuthorization{
				ClientID: testAuthClientID, RedirectURI: testAuthRedirectURI, State: "client-state",
				PKCEChallenge: "client-challenge", PKCEMethod: "S256", Scopes: []string{"openid"},
				InternalState: firstLegState, UpstreamPKCEVerifier: "first-leg-pkce-verifier-123456789012345678",
				UpstreamNonce: "first-leg-nonce", UpstreamProviderName: "provider-1",
				SessionID: "chain-session-sub-id", CreatedAt: time.Now(),
			}
			cookie := bindPending(t, storState, firstLegState, pending)

			rec := httptest.NewRecorder()
			handler.CallbackHandler(rec, newCallbackRequest("code=provider1-code&state="+firstLegState, cookie))
			require.Equal(t, http.StatusFound, rec.Code)

			var next *storage.PendingAuthorization
			for state, p := range storState.pendingAuths {
				if state != firstLegState && p.UpstreamProviderName == "provider-2" {
					next = p
				}
			}
			require.NotNil(t, next)
			assert.Equal(t, tt.want, next.VerifiedUpstream)
		})
	}
}

// TestDeviceVerificationFlow_RecordsVerifiedUpstreamOnlyWhenDisclosureEnabled checks
// the pending device confirmation and the authorized device request.
func TestDeviceVerificationFlow_RecordsVerifiedUpstreamOnlyWhenDisclosureEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		audiences []string
		want      session.UpstreamSubject
	}{
		{name: "disclosure disabled records nothing"},
		{name: "disclosure enabled records the identity", audiences: []string{testSubIDAudience}, want: testVerifiedUpstream},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, mockUpstream := setupDeviceVerificationHandler(t)
			h.config.SubjectIdentifierAudiences = tt.audiences
			id := verifiedIdentity()
			id.Tokens = mockUpstream.exchangeResult.Tokens
			id.Subject = mockUpstream.exchangeResult.Subject
			id.Name, id.Email = "Ada Lovelace", "ada@example.com"
			mockUpstream.exchangeResult = id

			deviceCode, userCode := issueDeviceCode(t, h)
			submitRec := postDeviceUserCode(t, h, userCode)
			require.Equal(t, http.StatusFound, submitRec.Code)
			state := mockUpstream.capturedState

			callbackRec := httptest.NewRecorder()
			h.CallbackHandler(callbackRec, newCallbackRequest("code=upstream-code&state="+state, bindingCookieFrom(t, submitRec, state)))
			require.Equal(t, http.StatusOK, callbackRec.Code)
			confirmToken := extractConfirmToken(t, callbackRec.Body.String())

			confirmation, err := h.storage.LoadPendingDeviceConfirmation(context.Background(), confirmToken)
			require.NoError(t, err)
			assert.Equal(t, tt.want, confirmation.VerifiedUpstream)

			confirmForm := url.Values{"confirm_token": {confirmToken}, "action": {"approve"}}
			confirmReq := httptest.NewRequest(http.MethodPost, "/oauth/device/confirm", strings.NewReader(confirmForm.Encode()))
			confirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			confirmRec := httptest.NewRecorder()
			h.DeviceVerificationConfirmHandler(confirmRec, confirmReq)
			require.Equal(t, http.StatusOK, confirmRec.Code)

			device, err := h.deviceStorage.LoadDeviceRequestByDeviceCode(context.Background(), deviceCode)
			require.NoError(t, err)
			assert.Equal(t, tt.want, device.VerifiedUpstream)
		})
	}
}

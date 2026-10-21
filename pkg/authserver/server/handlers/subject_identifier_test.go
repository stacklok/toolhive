// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"testing"

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

			pending := &storage.PendingAuthorization{ResolvedUserID: tt.resolved}

			got := carryFirstUpstreamIdentity(pending, tt.identity)

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

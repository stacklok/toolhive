// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// hmacStrategy accepts any token that does not start with "bad" and takes the
// part after the last dot as the signature.
type hmacStrategy struct{ oauth2.RefreshTokenStrategy }

func (hmacStrategy) ValidateRefreshToken(_ context.Context, _ fosite.Requester, token string) error {
	if strings.HasPrefix(token, "bad") {
		return errors.New("invalid HMAC")
	}
	return nil
}

func (hmacStrategy) GenerateRefreshToken(context.Context, fosite.Requester) (string, string, error) {
	return "token.child", "child-sig", nil
}

func (hmacStrategy) RefreshTokenSignature(_ context.Context, token string) string {
	return token[strings.LastIndex(token, ".")+1:]
}

// recordStore serves fixed grants and counts reads. A grant past its ExpiresAt
// is still returned. Real stores report an expired record as not found; this
// one checks that Live is the authoritative guard.
type recordStore struct {
	storage.ExchangeGrantStorage
	grants map[string]storage.ExchangeGrant
	reads  int
}

func (s *recordStore) GetExchangeGrant(_ context.Context, signature string) (*storage.ExchangeGrant, error) {
	s.reads++
	g, ok := s.grants[signature]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return &g, nil
}

func deriveHandler(t *testing.T, store storage.ExchangeGrantStorage) *RefreshTokenHandler {
	t.Helper()
	h := newRefreshHandlerForTest(t, &stubSubjectValidator{}, &fakeGrantStore{})
	h.strategy = hmacStrategy{}
	h.store = store
	h.clientPolicies[testRefreshClient] = ConfiguredClientPolicy{ExchangeRefreshTokens: map[string]time.Duration{
		oauthproto.TokenTypeRefreshToken: 720 * time.Hour, oauthproto.TokenTypeIDJAG: 12 * time.Hour,
	}}
	return h
}

func deriveRequest(token, childType string) *fosite.AccessRequest {
	req := refreshRequestForTest(brokerClientForTest())
	req.Form.Set("subject_token", token)
	req.Form.Set("subject_token_type", oauthproto.TokenTypeRefreshToken)
	req.Form.Set("authorization_details", `[{"type":"https://toolhive.dev/exchange_grant",`+
		`"issuable_token_types":["`+childType+`"]}]`)
	req.Session = &session.Session{}
	return req
}

func TestDeriveChecksHMACBeforeAnyLookup(t *testing.T) {
	t.Parallel()

	store := &recordStore{}
	h := deriveHandler(t, store)
	err := h.HandleTokenEndpointRequest(t.Context(), deriveRequest("bad.sig", oauthproto.TokenTypeIDJAG))
	requireOAuthError(t, err, "invalid_request", reasonGrantInvalid)
	assert.Same(t, errUnknownGrant, unwrapOAuth(t, err))
	assert.Zero(t, store.reads, "a token with a bad HMAC causes no store call")
}

func unwrapOAuth(t *testing.T, err error) *fosite.RFC6749Error {
	t.Helper()
	var oauthErr *fosite.RFC6749Error
	require.ErrorAs(t, err, &oauthErr)
	return oauthErr
}

func TestDeriveCheckOrder(t *testing.T) {
	t.Parallel()

	now := time.Now()
	live := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	grant := func(sig, client, parent string, expires time.Time, revoked bool, types ...string) storage.ExchangeGrant {
		g := storage.ExchangeGrant{
			Signature: sig, ClientID: client, ParentSignature: parent, Subject: "user-1",
			IssuableTokenTypes: types, ExpiresAt: expires, CreatedAt: now,
		}
		if revoked {
			g.RevokedAt = now
		}
		return g
	}
	refresh := oauthproto.TokenTypeRefreshToken
	idJAG := oauthproto.TokenTypeIDJAG
	grants := []storage.ExchangeGrant{
		grant("ok", testRefreshClient, "", live, false, refresh),
		grant("revoked", testRefreshClient, "", live, true, refresh),
		grant("expired", testRefreshClient, "", past, false, refresh),
		grant("revoked-and-expired", testRefreshClient, "", past, true, refresh),
		grant("other-revoked", "stranger", "", live, true, refresh),
		grant("id-jag-only", testRefreshClient, "", live, false, idJAG),
		grant("child", testRefreshClient, "ok", live, false, idJAG),
		grant("child-lists-refresh", testRefreshClient, "ok", live, false, refresh),
		grant("child-of-revoked", testRefreshClient, "revoked", live, false, idJAG),
		grant("revoked-child-of-revoked", testRefreshClient, "revoked", live, true, idJAG),
		grant("child-of-expired", testRefreshClient, "expired", live, false, idJAG),
		grant("orphan", testRefreshClient, "gone", live, false, idJAG),
	}
	records := map[string]storage.ExchangeGrant{}
	for _, g := range grants {
		records[g.Signature] = g
	}

	tests := []struct {
		name  string
		token string
		code  string
		want  string // whole error_description
	}{
		{name: "unknown token", token: "x.nothere", code: "invalid_request", want: reasonGrantInvalid},
		{name: "another client's revoked grant is grant_invalid", token: "x.other-revoked",
			code: "invalid_request", want: reasonGrantInvalid},
		{name: "revoked", token: "x.revoked", code: "invalid_request", want: "revoked"},
		{name: "expired is answered like an unknown grant", token: "x.expired", code: "invalid_request",
			want: reasonGrantInvalid},
		{name: "a revoked grant that has expired is answered like an unknown grant", token: "x.revoked-and-expired",
			code: "invalid_request", want: reasonGrantInvalid},
		{name: "own revocation is checked before the parent's", token: "x.revoked-child-of-revoked",
			code: "invalid_request", want: "revoked"},
		{name: "parent revoked", token: "x.child-of-revoked", code: "invalid_request", want: "parent_revoked"},
		{name: "parent expired", token: "x.child-of-expired", code: "invalid_request", want: "parent_revoked"},
		{name: "parent gone", token: "x.orphan", code: "invalid_request", want: "parent_revoked"},
		{name: "does not allow refresh_token", token: "x.id-jag-only", code: "invalid_request", want: "not_issuable"},
		{name: "a child is not a source", token: "x.child", code: "invalid_request", want: "not_issuable"},
		{name: "a child that lists refresh_token is still not a source", token: "x.child-lists-refresh",
			code: "invalid_request", want: "not_issuable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := deriveHandler(t, &recordStore{grants: records})
			req := deriveRequest(tt.token, idJAG)
			for _, err := range []error{
				h.HandleTokenEndpointRequest(t.Context(), req),
				h.PopulateTokenEndpointResponse(t.Context(), req, fosite.NewAccessResponse()),
			} {
				requireOAuthError(t, err, tt.code, "")
				assert.Equal(t, tt.want, unwrapOAuth(t, err).GetDescription())
			}
		})
	}

	t.Run("a live grant is derived from, and the child records it as its parent", func(t *testing.T) {
		t.Parallel()
		store := &fakeGrantStoreWithRecords{recordStore: recordStore{grants: records}}
		h := deriveHandler(t, store)
		req := deriveRequest("x.ok", idJAG)
		resp := fosite.NewAccessResponse()
		require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), req))
		require.NoError(t, h.PopulateTokenEndpointResponse(t.Context(), req, resp))
		require.Len(t, store.created, 1)
		child := store.created[0]
		assert.Equal(t, "ok", child.ParentSignature)
		assert.Equal(t, testRefreshClient, child.ClientID)
		assert.Equal(t, "user-1", child.Subject)
		assert.Empty(t, child.SourceJTI, "a child has no replay marker")
		assert.Equal(t, []string{idJAG}, child.IssuableTokenTypes)
		assert.Equal(t, live.Unix(), child.ExpiresAt.Unix(), "capped to the source, which is below the 12h id-jag cap")
	})

	t.Run("a grant revoked between handling and the response is caught", func(t *testing.T) {
		t.Parallel()
		store := &fakeGrantStoreWithRecords{recordStore: recordStore{grants: map[string]storage.ExchangeGrant{
			"ok": records["ok"],
		}}}
		h := deriveHandler(t, store)
		req := deriveRequest("x.ok", idJAG)
		require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), req))
		revoked := store.grants["ok"]
		revoked.RevokedAt = now
		store.grants["ok"] = revoked
		err := h.PopulateTokenEndpointResponse(t.Context(), req, fosite.NewAccessResponse())
		requireOAuthError(t, err, "invalid_request", "revoked")
		assert.Empty(t, store.created)
	})
}

// TestDeriveSourceWithNoWholeSecondLeftIsGrantInvalid pins that a presented
// grant (the source) with under one whole second left gets the same answer as
// one that expired earlier, whatever else is wrong with it: revoked, or with a
// revoked parent of its own. The clock is fixed: now is 100ms past a whole
// second and the source expires 400ms after now, so Live passes on expiry and
// the whole-second check is the one that refuses it. Wall-clock timing would
// run that check only part of the time. A source that lists only id-jag gets
// the same answer, so the whole-second check runs before the type check.
func TestDeriveSourceWithNoWholeSecondLeftIsGrantInvalid(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_900_000_000, 100_000_000)
	tests := []struct {
		name          string
		expires       time.Time
		types         []string
		revoked       bool
		parentRevoked bool
	}{
		{name: "live by under one second", expires: now.Add(400 * time.Millisecond)},
		{name: "id-jag only with under one second left is not not_issuable",
			expires: now.Add(400 * time.Millisecond), types: []string{oauthproto.TokenTypeIDJAG}},
		{name: "expired earlier", expires: now.Add(-time.Hour)},
		{name: "revoked with under one second left", expires: now.Add(400 * time.Millisecond), revoked: true},
		{name: "own parent revoked with under one second left", expires: now.Add(400 * time.Millisecond),
			parentRevoked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			types := tt.types
			if types == nil {
				types = []string{oauthproto.TokenTypeRefreshToken}
			}
			source := storage.ExchangeGrant{Signature: "ok", ClientID: testRefreshClient, Subject: "user-1",
				IssuableTokenTypes: types, ExpiresAt: tt.expires}
			if tt.revoked {
				source.RevokedAt = now
			}
			grants := map[string]storage.ExchangeGrant{}
			if tt.parentRevoked {
				source.ParentSignature = "parent"
				grants["parent"] = storage.ExchangeGrant{Signature: "parent", ClientID: testRefreshClient,
					Subject: "user-1", IssuableTokenTypes: []string{oauthproto.TokenTypeRefreshToken},
					ExpiresAt: now.Add(time.Hour), RevokedAt: now}
			}
			grants["ok"] = source
			store := &fakeGrantStoreWithRecords{recordStore: recordStore{grants: grants}}
			h := deriveHandler(t, store)
			_, err := h.decide(t.Context(), deriveRequest("x.ok", oauthproto.TokenTypeIDJAG), now)
			requireOAuthError(t, err, "invalid_request", reasonGrantInvalid)
			assert.Empty(t, store.created)
		})
	}
}

// fakeGrantStoreWithRecords adds creation to recordStore.
type fakeGrantStoreWithRecords struct {
	recordStore
	created []storage.ExchangeGrant
}

func (s *fakeGrantStoreWithRecords) CreateExchangeGrant(_ context.Context, g *storage.ExchangeGrant) error {
	s.created = append(s.created, *g)
	return nil
}

func TestDeriveStoreFailureIsGeneric(t *testing.T) {
	t.Parallel()

	h := deriveHandler(t, &failingGetStore{})
	err := h.HandleTokenEndpointRequest(t.Context(), deriveRequest("x.sig", oauthproto.TokenTypeIDJAG))
	requireOAuthError(t, err, "server_error", "")
	assert.NotContains(t, unwrapOAuth(t, err).GetDescription(), "boom")
}

type failingGetStore struct{ storage.ExchangeGrantStorage }

func (*failingGetStore) GetExchangeGrant(context.Context, string) (*storage.ExchangeGrant, error) {
	return nil, errors.New("boom")
}

func TestDeriveParentStoreFailureIsGeneric(t *testing.T) {
	t.Parallel()

	store := &failingParentStore{recordStore: recordStore{grants: map[string]storage.ExchangeGrant{
		"child": {Signature: "child", ClientID: testRefreshClient, ParentSignature: "ok", Subject: "user-1",
			IssuableTokenTypes: []string{oauthproto.TokenTypeIDJAG}, ExpiresAt: time.Now().Add(time.Hour)},
	}}, failing: "ok"}
	h := deriveHandler(t, store)
	err := h.HandleTokenEndpointRequest(t.Context(), deriveRequest("x.child", oauthproto.TokenTypeIDJAG))
	requireOAuthError(t, err, "server_error", "")
	assert.NotContains(t, unwrapOAuth(t, err).GetDescription(), "boom")
}

// failingParentStore fails the lookup of one signature with a non-NotFound error.
type failingParentStore struct {
	recordStore
	failing string
}

func (s *failingParentStore) GetExchangeGrant(ctx context.Context, signature string) (*storage.ExchangeGrant, error) {
	if signature == s.failing {
		return nil, errors.New("boom")
	}
	return s.recordStore.GetExchangeGrant(ctx, signature)
}

// TestDeriveChildTypeNotConfiguredFallback covers a refresh-only policy. Startup
// validation rejects such a policy, so a configured client never reaches this
// refusal. The test pins it for a handler built with that policy directly: both
// Handle and Populate refuse with token_type_not_permitted.
func TestDeriveChildTypeNotConfiguredFallback(t *testing.T) {
	t.Parallel()

	h := deriveHandler(t, &recordStore{grants: map[string]storage.ExchangeGrant{
		"ok": {Signature: "ok", ClientID: testRefreshClient, Subject: "user-1",
			IssuableTokenTypes: []string{oauthproto.TokenTypeRefreshToken}, ExpiresAt: time.Now().Add(time.Hour)},
	}})
	h.clientPolicies[testRefreshClient] = ConfiguredClientPolicy{ExchangeRefreshTokens: map[string]time.Duration{
		oauthproto.TokenTypeRefreshToken: 720 * time.Hour,
	}}
	req := deriveRequest("x.ok", oauthproto.TokenTypeIDJAG)
	requireOAuthError(t, h.HandleTokenEndpointRequest(t.Context(), req),
		"invalid_authorization_details", "token_type_not_permitted")
	requireOAuthError(t, h.PopulateTokenEndpointResponse(t.Context(), req, fosite.NewAccessResponse()),
		"invalid_authorization_details", "token_type_not_permitted")
}

// TestDeriveNeedsRefreshTokenStillAllowed pins that a client whose policy no
// longer allows refresh tokens is refused before the source grant is looked up.
// The store holds a live source grant; reads stays 0 because the gate refuses first.
func TestDeriveNeedsRefreshTokenStillAllowed(t *testing.T) {
	t.Parallel()

	store := &recordStore{grants: map[string]storage.ExchangeGrant{
		"ok": {Signature: "ok", ClientID: testRefreshClient, Subject: "user-1",
			IssuableTokenTypes: []string{oauthproto.TokenTypeRefreshToken}, ExpiresAt: time.Now().Add(time.Hour)},
	}}
	h := deriveHandler(t, store)
	h.clientPolicies[testRefreshClient] = ConfiguredClientPolicy{ExchangeRefreshTokens: map[string]time.Duration{
		oauthproto.TokenTypeIDJAG: 12 * time.Hour,
	}}
	req := deriveRequest("x.ok", oauthproto.TokenTypeIDJAG)
	var oauthErr *fosite.RFC6749Error
	err := h.HandleTokenEndpointRequest(t.Context(), req)
	require.ErrorAs(t, err, &oauthErr)
	assert.Equal(t, "invalid_request", oauthErr.ErrorField)
	assert.Contains(t, oauthErr.GetDescription(), "is not supported")
	assert.Zero(t, store.reads, "the gate must refuse before any lookup")
}

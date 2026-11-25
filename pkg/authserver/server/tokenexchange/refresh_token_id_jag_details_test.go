// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// newMintDetailsRegistry registers testCallType for token exchange with
// callHook and testOtherType for token exchange with otherHook.
func newMintDetailsRegistry(t *testing.T, callHook, otherHook grantHook) *authorizationdetails.Registry {
	t.Helper()
	registry, err := authorizationdetails.New(map[string]authorizationdetails.Type{
		testCallType:  {GrantTypes: []string{oauthproto.GrantTypeTokenExchange}, Grant: callHook},
		testOtherType: {GrantTypes: []string{oauthproto.GrantTypeTokenExchange}, Grant: otherHook},
	})
	require.NoError(t, err)
	return registry
}

// detailsIDJAGHandler is idJAGHandler with registry as the ID-JAG details
// registry and one live ID-JAG grant, "ok", in a store that counts reads.
func detailsIDJAGHandler(t *testing.T, registry *authorizationdetails.Registry) (*RefreshTokenHandler, *recordStore) {
	t.Helper()
	store := &recordStore{grants: idJAGRecords(
		idJAGRecord("ok", testRefreshClient, "", time.Now().Add(time.Hour), oauthproto.TokenTypeIDJAG))}
	h := idJAGHandler(t, store)
	h.detailsRegistry = registry
	return h, store
}

// mintBothPhases runs req through both handler phases, as fosite does, and
// returns the response and the first error.
func mintBothPhases(t *testing.T, h *RefreshTokenHandler, req *fosite.AccessRequest) (*fosite.AccessResponse, error) {
	t.Helper()
	resp := fosite.NewAccessResponse()
	if err := h.HandleTokenEndpointRequest(t.Context(), req); err != nil {
		return resp, err
	}
	return resp, h.PopulateTokenEndpointResponse(t.Context(), req, resp)
}

// rawPayloadMember returns the raw bytes of a member of a JWT's payload, nil
// when the member is absent.
func rawPayloadMember(t *testing.T, token, name string) json.RawMessage {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload, &members))
	return members[name]
}

// TestIDJAGSignsGrantedDetails proves AS-1 signs the granted entries into the
// ID-JAG with the bytes the client sent, echoes the same bytes in the
// response, and gives the hook the grant's context.
func TestIDJAGSignsGrantedDetails(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	var seen authorizationdetails.Request
	hook := func(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
		calls.Add(1)
		seen = req
		return authorizationdetails.Result{Entries: req.Entries}, nil
	}
	h, store := detailsIDJAGHandler(t, newMintDetailsRegistry(t, hook, keepAllHook))
	grant := store.grants["ok"]
	// bigEntry holds an integer above 2^53, and the other entry characters
	// that encoding/json escapes.
	entries := `[` + bigEntry + `,{"type":"` + testOtherType + `","note":"a<b>&c"}]`
	req := idJAGRequest("x.ok")
	req.Form.Set("authorization_details", entries)

	resp, err := mintBothPhases(t, h, req)
	require.NoError(t, err)

	// The token's JSON encoding escapes <, > and &; every digit is kept.
	signed := `[` + bigEntry + `,{"type":"` + testOtherType + `","note":"a\u003cb\u003e\u0026c"}]`
	assert.Equal(t, signed, string(rawPayloadMember(t, resp.GetAccessToken(), "authorization_details")),
		"the ID-JAG claim has the client's entries in encoding/json form")
	assert.Equal(t, json.RawMessage(signed), resp.GetExtra("authorization_details"),
		"the response member has the signed bytes")

	assert.Equal(t, int32(2), calls.Load(), "decide runs in both phases, so the hook runs twice")
	assert.Equal(t, authorizationdetails.Request{
		Entries:         []json.RawMessage{json.RawMessage(bigEntry)},
		GrantType:       oauthproto.GrantTypeTokenExchange,
		ClientID:        testRefreshClient,
		GrantedAudience: []string{grantMintTarget},
		Subject: authorizationdetails.Subject{
			Issuer: testIssuer, Subject: "user-1", ClientID: testRefreshClient,
			IssuedAt: grant.CreatedAt, Expiry: grant.ExpiresAt,
		},
		Exchange: &authorizationdetails.ExchangeContext{
			Actor: testRefreshClient, RequestedTokenType: oauthproto.TokenTypeIDJAG,
		},
	}, seen)
}

// TestIDJAGWithoutDetails proves a request without the parameter, or with one
// empty value, mints an ID-JAG with no claim and no response member, whether
// or not a registry is set.
func TestIDJAGWithoutDetails(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		registry bool
		values   []string
	}{
		{name: "absent, no registry"},
		{name: "absent, registry", registry: true},
		{name: "one empty value, no registry", values: []string{""}},
		{name: "one empty value, registry", registry: true, values: []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var registry *authorizationdetails.Registry
			if tc.registry {
				registry = newMintDetailsRegistry(t, keepAllHook, keepAllHook)
			}
			h, _ := detailsIDJAGHandler(t, registry)
			req := idJAGRequest("x.ok")
			if tc.values != nil {
				req.Form["authorization_details"] = tc.values
			}
			resp, err := mintBothPhases(t, h, req)
			require.NoError(t, err)
			assert.Nil(t, rawPayloadMember(t, resp.GetAccessToken(), "authorization_details"))
			assert.Nil(t, resp.GetExtra("authorization_details"))
		})
	}
}

// TestIDJAGDropsWholeEntries proves a type whose hook keeps none of its
// entries is left out of the ID-JAG while the other types' entries are signed.
func TestIDJAGDropsWholeEntries(t *testing.T) {
	t.Parallel()

	h, _ := detailsIDJAGHandler(t, newMintDetailsRegistry(t, keepAllHook, grantingHook()))
	req := idJAGRequest("x.ok")
	req.Form.Set("authorization_details", `[`+testOtherEntry+`,`+testCallEntry+`]`)

	resp, err := mintBothPhases(t, h, req)
	require.NoError(t, err)
	assert.Equal(t, `[`+testCallEntry+`]`, string(rawPayloadMember(t, resp.GetAccessToken(), "authorization_details")))
	assert.Equal(t, json.RawMessage(`[`+testCallEntry+`]`), resp.GetExtra("authorization_details"))
}

// TestIDJAGDetailsRefusals proves each refusal of the authorization_details
// parameter, in both handler phases, mints nothing, and that the shape checks
// run before the store is read.
func TestIDJAGDetailsRefusals(t *testing.T) {
	t.Parallel()

	jwtBearerOnly, err := authorizationdetails.New(map[string]authorizationdetails.Type{
		testCallType: {GrantTypes: []string{oauthproto.GrantTypeJWTBearer}, Grant: keepAllHook},
	})
	require.NoError(t, err)
	hookRefusal := authorizationdetails.Error{
		Code: authorizationdetails.CodeInvalidAuthorizationDetails, Reason: "note_required",
	}
	refusingHook := func(context.Context, authorizationdetails.Request) (authorizationdetails.Result, error) {
		return authorizationdetails.Result{}, hookRefusal
	}
	one := `[` + testCallEntry + `]`

	tests := []struct {
		name       string
		registry   *authorizationdetails.Registry // default: both types kept
		noRegistry bool
		values     []string
		code       string
		desc       string
		storeRead  bool // whether the refusal comes after the grant is read
	}{
		{name: "two empty values are repeated", values: []string{"", ""},
			code: "invalid_request", desc: hintDetailsParamRepeated},
		{name: "two values are repeated", values: []string{one, one},
			code: "invalid_request", desc: hintDetailsParamRepeated},
		{name: "no registry", noRegistry: true, values: []string{one},
			code: "invalid_authorization_details", desc: reasonDetailsUnknownType},
		{name: "a type registered only for the JWT-bearer grant", registry: jwtBearerOnly, values: []string{one},
			code: "invalid_authorization_details", desc: reasonDetailsUnknownType},
		{name: "an unregistered type", values: []string{`[{"type":"https://example.com/unknown"}]`},
			code: "invalid_authorization_details", desc: reasonDetailsUnknownType},
		{name: "not JSON", values: []string{"not json"},
			code: "invalid_authorization_details", desc: reasonDetailsMalformedEntry},
		{name: "an object, not an array", values: []string{testCallEntry},
			code: "invalid_authorization_details", desc: reasonDetailsMalformedEntry},
		{name: "an entry without a type", values: []string{`[{"note":"x"}]`},
			code: "invalid_authorization_details", desc: reasonDetailsMalformedEntry},
		{name: "the empty array", values: []string{"[]"}, storeRead: true,
			code: "invalid_authorization_details", desc: reasonDetailsEmptyGrant},
		{name: "every hook drops its entries", values: []string{one}, storeRead: true,
			registry: newMintDetailsRegistry(t, grantingHook(), keepAllHook),
			code:     "invalid_authorization_details", desc: reasonDetailsEmptyGrant},
		{name: "a hook refuses", values: []string{one}, storeRead: true,
			registry: newMintDetailsRegistry(t, refusingHook, keepAllHook),
			code:     "invalid_authorization_details", desc: "note_required"},
		{name: "a hook fails", values: []string{one}, storeRead: true,
			registry: newMintDetailsRegistry(t, failingHook, keepAllHook),
			code:     "server_error", desc: detailsProcessingFailed},
		{name: "a hook changes an entry", values: []string{one}, storeRead: true,
			registry: newMintDetailsRegistry(t, grantingHook(testOtherCallEntry), keepAllHook),
			code:     "server_error", desc: detailsProcessingFailed},
		{name: "a hook adds an entry", values: []string{one}, storeRead: true,
			registry: newMintDetailsRegistry(t, grantingHook(testCallEntry, testCallEntry), keepAllHook),
			code:     "server_error", desc: detailsProcessingFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			registry := tc.registry
			if registry == nil && !tc.noRegistry {
				registry = newMintDetailsRegistry(t, keepAllHook, keepAllHook)
			}
			h, store := detailsIDJAGHandler(t, registry)
			req := idJAGRequest("x.ok")
			req.Form["authorization_details"] = tc.values

			for _, err := range []error{
				h.HandleTokenEndpointRequest(t.Context(), req),
				h.PopulateTokenEndpointResponse(t.Context(), req, fosite.NewAccessResponse()),
			} {
				requireOAuthError(t, err, tc.code, tc.desc)
			}
			if tc.storeRead {
				assert.NotZero(t, store.reads, "the hooks run after the grant is read")
			} else {
				assert.Zero(t, store.reads, "the parameter's shape is checked before the store is read")
			}
		})
	}
}

// TestIDJAGDetailsCheckOrder proves the parameter's shape is checked before the
// grant, and the hooks after it.
func TestIDJAGDetailsCheckOrder(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	hook := func(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
		calls.Add(1)
		return authorizationdetails.Result{Entries: req.Entries}, nil
	}
	h, _ := detailsIDJAGHandler(t, newMintDetailsRegistry(t, hook, hook))

	req := idJAGRequest("x.nothere")
	req.Form.Set("authorization_details", `[{"type":"https://example.com/unknown"}]`)
	requireOAuthError(t, h.HandleTokenEndpointRequest(t.Context(), req),
		"invalid_authorization_details", reasonDetailsUnknownType)

	req = idJAGRequest("x.nothere")
	req.Form.Set("authorization_details", `[`+testCallEntry+`]`)
	requireOAuthError(t, h.HandleTokenEndpointRequest(t.Context(), req), "invalid_request", reasonGrantInvalid)
	assert.Zero(t, calls.Load(), "no hook runs for a grant that is refused")
}

// TestIDJAGDetailsRefusalLogs proves an unknown_type refusal names the entry
// types but never an entry's value.
//
//nolint:paralleltest // captureLogs swaps the process-wide default logger.
func TestIDJAGDetailsRefusalLogs(t *testing.T) {
	logs := captureLogs(t)
	h, _ := detailsIDJAGHandler(t, nil)
	req := idJAGRequest("x.ok")
	req.Form.Set("authorization_details", `[{"type":"https://example.com/unknown","secret":"entry-value"}]`)

	requireOAuthError(t, h.HandleTokenEndpointRequest(t.Context(), req),
		"invalid_authorization_details", reasonDetailsUnknownType)
	text := logs.String()
	assert.Contains(t, text, "reason=unknown_type")
	assert.Contains(t, text, "https://example.com/unknown")
	assert.NotContains(t, text, "entry-value")
}

// TestRefreshTokenFactoryIDJAGDetailsRegistry proves the option reaches the
// handler and leaves the private exchange-grant registry in place.
func TestRefreshTokenFactoryIDJAGDetailsRegistry(t *testing.T) {
	t.Parallel()

	registry := newMintDetailsRegistry(t, keepAllHook, keepAllHook)
	h := buildRefreshTokenHandler(t, map[string]ConfiguredClientPolicy{}, &fakeGrantStore{},
		WithIDJAGAuthorizationDetailsRegistry(registry))
	assert.Same(t, registry, h.detailsRegistry)
	assert.NotNil(t, h.registry)
	assert.NotSame(t, registry, h.registry)

	h = buildRefreshTokenHandler(t, map[string]ConfiguredClientPolicy{}, &fakeGrantStore{})
	assert.Nil(t, h.detailsRegistry)
}

// TestMintIDJAGRefusesMalformedDetails proves the minting core refuses
// authorization_details that is not a non-empty array of objects, whatever
// the caller passes.
func TestMintIDJAGRefusesMalformedDetails(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{`[]`, `{}`, `null`, `[1]`, `["x"]`, `[{}`} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			m := mintTestMint(t)
			m.AuthorizationDetails = json.RawMessage(raw)
			_, err := mintIDJAG(t.Context(), newMintTestSigner(newTestJWKS(t)), m, mustSelectClaims(t, m), mintTestNow)
			requireMintFailure(t, err)
		})
	}
}

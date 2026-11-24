// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"maps"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
)

// TestRedemptionRefusesAuthorizationDetailsClaim pins that both redemption
// forms refuse an assertion that carries authorization_details instead of
// issuing a token without it, before the assertion is consumed. Redemption
// does not refuse the delegated-access marker: an assertion with the marker
// alone is accepted, and one that carries both is refused for its details. A
// cnf claim is refused first.
func TestRedemptionRefusesAuthorizationDetailsClaim(t *testing.T) {
	t.Parallel()

	const detailsHint = "invalid_assertion: The assertion must not carry an 'authorization_details' claim."

	type redemptionForm struct {
		name     string
		cnfHint  string
		claims   func() *ValidatedClaims
		redeemer func(t *testing.T, claims *ValidatedClaims, consumer *recordingAssertionConsumer) (
			fosite.TokenEndpointHandler, fosite.AccessRequester)
	}
	forms := []redemptionForm{
		{
			name:    "plain RFC 7523",
			cnfHint: "invalid_assertion: The JWT bearer assertion must not carry a 'cnf' claim.",
			claims:  refusalClaims,
			redeemer: func(t *testing.T, claims *ValidatedClaims, consumer *recordingAssertionConsumer) (
				fosite.TokenEndpointHandler, fosite.AccessRequester) {
				t.Helper()
				deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: claims}, refusalPolicies(t))
				deps.consumer = consumer
				return newTestJWTBearerHandler(t, deps), newJWTBearerRequest(map[string][]string{
					"assertion": {signAssertionWithType(t, newTestJWKS(t), nil)},
					"resource":  {refusalResource},
				})
			},
		},
		{
			name:    "ID-JAG",
			cnfHint: "invalid_assertion: The ID-JAG assertion must not carry a 'cnf' claim.",
			claims:  validIDJAGClaims,
			redeemer: func(t *testing.T, claims *ValidatedClaims, consumer *recordingAssertionConsumer) (
				fosite.TokenEndpointHandler, fosite.AccessRequester) {
				t.Helper()
				return newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{claims: claims}, consumer),
					newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
			},
		},
	}
	cases := []struct {
		name    string
		extra   map[string]any
		claims  map[string]any // the full claim set, when set
		wantCnf bool
		wantOK  bool // redemption does not refuse the delegated-access marker
	}{
		{name: "marker only is redeemed", wantOK: true,
			extra: map[string]any{claimnames.DelegatedAccessKey: true}},
		{name: "authorization_details in Extra",
			extra: map[string]any{authorizationDetailsParam: terminalDetails()}},
		{name: "authorization_details in the full claim set only",
			claims: map[string]any{authorizationDetailsParam: terminalDetails()}},
		// Every shape is refused, valid or not: presence alone counts. An
		// empty array matters most, since a token without details is
		// unrestricted.
		{name: "authorization_details null",
			extra: map[string]any{authorizationDetailsParam: nil}},
		{name: "authorization_details empty array",
			extra: map[string]any{authorizationDetailsParam: []any{}}},
		{name: "authorization_details string",
			extra: map[string]any{authorizationDetailsParam: "x"}},
		{name: "authorization_details object",
			extra: map[string]any{authorizationDetailsParam: map[string]any{"type": "x"}}},
		{name: "authorization_details array holding a non-object",
			extra: map[string]any{authorizationDetailsParam: []any{"x"}}},
		{name: "marker and authorization_details",
			extra: map[string]any{claimnames.DelegatedAccessKey: true, authorizationDetailsParam: terminalDetails()}},
		{name: "cnf and authorization_details", wantCnf: true,
			extra: map[string]any{claimConfirmation: map[string]any{"jkt": "t"}, authorizationDetailsParam: terminalDetails()}},
	}
	for _, form := range forms {
		for _, tt := range cases {
			t.Run(form.name+", "+tt.name, func(t *testing.T) {
				t.Parallel()

				claims := form.claims()
				claims.Extra = maps.Clone(claims.Extra)
				if claims.Extra == nil {
					claims.Extra = map[string]any{}
				}
				maps.Copy(claims.Extra, tt.extra)
				claims.Claims = tt.claims
				consumer := &recordingAssertionConsumer{}
				handler, req := form.redeemer(t, claims, consumer)

				err := handler.HandleTokenEndpointRequest(context.Background(), req)

				if tt.wantOK {
					require.NoError(t, err)
					return
				}
				want := detailsHint
				if tt.wantCnf {
					want = form.cnfHint
				}
				requireRefusal(t, err, "invalid_grant", want)
				assert.Empty(t, consumer.key, "a refused assertion must not be consumed")
			})
		}
	}
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
)

// TestPlainRedemptionRefusesAuthorizationDetailsClaim pins that the plain RFC
// 7523 grant refuses an assertion that carries authorization_details instead of
// issuing a token without it, before the assertion is consumed. The ID-JAG
// grant processes the same claim instead (TestIDJAGRedemptionDetailsClaim).
// Redemption does not refuse the delegated-access marker: an assertion with the
// marker alone is accepted, and one that carries both is refused for its
// details. A cnf claim is refused first, and an issuer that is not enabled for
// the grant gets that refusal instead, so the details refusal names only
// issuers the grant accepts.
func TestPlainRedemptionRefusesAuthorizationDetailsClaim(t *testing.T) {
	t.Parallel()

	const (
		detailsHint    = "invalid_assertion: The assertion must not carry an 'authorization_details' claim."
		cnfHint        = "invalid_assertion: The JWT bearer assertion must not carry a 'cnf' claim."
		notEnabledHint = "invalid_assertion: The JWT bearer assertion issuer is not enabled for this grant."
	)

	cases := []struct {
		name    string
		extra   map[string]any
		claims  map[string]any // the full claim set, when set
		issuer  string         // overrides the issuer when set
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
		{name: "issuer trusted but not enabled for the grant", issuer: "https://not-enabled.example.com",
			extra: map[string]any{authorizationDetailsParam: terminalDetails()}},
		{name: "cnf and authorization_details", wantCnf: true,
			extra: map[string]any{claimConfirmation: map[string]any{"jkt": "t"}, authorizationDetailsParam: terminalDetails()}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			claims := refusalClaims()
			claims.Extra = maps.Clone(claims.Extra)
			if claims.Extra == nil {
				claims.Extra = map[string]any{}
			}
			maps.Copy(claims.Extra, tt.extra)
			claims.Claims = tt.claims
			if tt.issuer != "" {
				claims.Issuer = tt.issuer
			}
			consumer := &recordingAssertionConsumer{}
			deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: claims}, refusalPolicies(t))
			deps.consumer = consumer
			handler := newTestJWTBearerHandler(t, deps)
			req := newJWTBearerRequest(map[string][]string{
				"assertion": {signAssertionWithType(t, newTestJWKS(t), nil)},
				"resource":  {refusalResource},
			})

			err := handler.HandleTokenEndpointRequest(context.Background(), req)

			if tt.wantOK {
				require.NoError(t, err)
				return
			}
			want := detailsHint
			switch {
			case tt.wantCnf:
				want = cnfHint
			case tt.issuer != "":
				want = notEnabledHint
			}
			requireRefusal(t, err, "invalid_grant", want)
			assert.Empty(t, consumer.key, "a refused assertion must not be consumed")
		})
	}
}

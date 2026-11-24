// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// terminalDetails is an authorization_details claim value. Only its presence
// matters to the terminal-token checks.
func terminalDetails() []any {
	return []any{map[string]any{"type": "https://example.com/call"}}
}

// terminalClaims returns a copy of base with the delegated-access marker
// and/or an authorization_details claim added.
func terminalClaims(base map[string]any, marker, details bool) map[string]any {
	extra := maps.Clone(base)
	if marker {
		extra[claimnames.DelegatedAccessKey] = true
	}
	if details {
		extra[authorizationDetailsParam] = terminalDetails()
	}
	return extra
}

// terminalCases are the restriction combinations every terminal-token site is
// tested with. The marker is checked first at the token-exchange sites, so a
// token that carries both gets the marker's wire text.
var terminalCases = []struct {
	name            string
	marker, details bool
}{
	{name: "authorization_details only", details: true},
	{name: "delegated-access marker only", marker: true},
	{name: "marker and authorization_details", marker: true, details: true},
}

func TestTerminalTokenRefusedInTokenExchange(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)

	actorForm := func(extra map[string]any) map[string]string {
		return map[string]string{
			"actor_token":      tj.signToken(t, validClaims(), extra),
			"actor_token_type": oauthproto.TokenTypeJWT,
		}
	}
	otherClient := validExtraClaims()
	otherClient["client_id"] = "some-other-client"

	type testCase struct {
		name string
		// form holds the parameters that override the default valid request.
		form map[string]string
		hint string // empty when the exchange succeeds
	}
	tests := []testCase{
		// Tokens without either claim are exchanged as before.
		{name: "unrestricted subject token is exchanged", form: map[string]string{}},
		{name: "unrestricted actor token is exchanged", form: actorForm(validExtraClaims())},
	}
	for _, c := range terminalCases {
		subjectHint, actorHint := hintSubjectAuthorizationDetails, hintActorAuthorizationDetails
		if c.marker {
			subjectHint, actorHint = hintSubjectDelegatedAccess, hintActorDelegatedAccess
		}
		subject := tj.signToken(t, validClaims(), terminalClaims(validExtraClaims(), c.marker, c.details))
		tests = append(tests,
			testCase{name: "subject token, " + c.name, hint: subjectHint,
				form: map[string]string{"subject_token": subject}},
			// A terminal subject token is refused before the actor token, which
			// is bound to another client and would fail on its own, is resolved.
			testCase{name: "subject token with an actor token bound to another client, " + c.name, hint: subjectHint,
				form: func() map[string]string {
					f := actorForm(otherClient)
					f["subject_token"] = subject
					return f
				}()},
			testCase{name: "actor token, " + c.name, hint: actorHint,
				form: actorForm(terminalClaims(validExtraClaims(), c.marker, c.details))},
			// The restriction is refused before client_id binding reads the token.
			testCase{name: "actor token bound to another client, " + c.name, hint: actorHint,
				form: actorForm(terminalClaims(otherClient, c.marker, c.details))},
		)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestHandler(t, tj, 15*time.Minute)
			form := defaultFormValues(t, tj)
			for k, v := range tt.form {
				form.Set(k, v)
			}
			req := newAccessRequest(t, defaultClient(), form)

			err := h.HandleTokenEndpointRequest(context.Background(), req)

			if tt.hint == "" {
				require.NoError(t, err)
				return
			}
			requireTerminalHint(t, err, tt.hint)
		})
	}
}

// TestTerminalTokenRefusedFromExternalIssuer pins that the RFC 8693 subject
// token check holds for a trusted external issuer too: if this server trusts
// another one that narrows tokens with authorization_details, a narrowed token
// cannot be exchanged here for an unrestricted one.
func TestTerminalTokenRefusedFromExternalIssuer(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)
	t.Cleanup(jwksServer.Close)
	validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		ActorMatcher:           `claims.azp == "trusted-app"`,
		AllowedDelegateClients: []string{testAgentClientID},
	}})

	type testCase struct {
		name            string
		marker, details bool
		hint            string // empty when the exchange succeeds
	}
	tests := []testCase{{name: "unrestricted token is exchanged"}}
	for _, c := range terminalCases {
		hint := hintSubjectAuthorizationDetails
		if c.marker {
			hint = hintSubjectDelegatedAccess
		}
		tests = append(tests, testCase{name: c.name, marker: c.marker, details: c.details, hint: hint})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			claims := externalClaims()
			claims.Audience = jwt.Audience{testExternalAudience, testIssuer}
			extra := terminalClaims(map[string]any{"azp": "trusted-app"}, tt.marker, tt.details)
			form := defaultFormValues(t, selfJWKS)
			form.Set("subject_token", externalJWKS.signToken(t, claims, extra))
			req := newAccessRequest(t, defaultClient(), form)

			err := newTestHandlerWithValidator(validator).HandleTokenEndpointRequest(context.Background(), req)

			if tt.hint == "" {
				require.NoError(t, err)
				return
			}
			requireTerminalHint(t, err, tt.hint)
		})
	}
}

func requireTerminalHint(t *testing.T, err error, hint string) {
	t.Helper()
	require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	var rfcErr *fosite.RFC6749Error
	require.ErrorAs(t, err, &rfcErr)
	assert.Equal(t, "invalid_request", rfcErr.ErrorField)
	assert.Equal(t, hint, rfcErr.HintField)
}

func TestTerminalTokenRefusedAtGrantOpening(t *testing.T) {
	t.Parallel()

	for _, c := range terminalCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			// Every later check would refuse this token with another reason:
			// it is a delegated token (act), from a client that is not an
			// allowed source, for another audience. A redeemed marked token
			// carries act too, so the terminal reason must come first.
			claims := validLoginClaims()
			claims.Extra = terminalClaims(map[string]any{"act": map[string]any{"sub": "agent"}}, c.marker, c.details)
			claims.ClientID = "stranger"
			claims.Audience = []string{"https://other.example.com"}
			h := newRefreshHandlerForTest(t, &stubSubjectValidator{claims: claims}, &fakeGrantStore{})

			err := h.HandleTokenEndpointRequest(t.Context(), refreshRequestForTest(brokerClientForTest()))

			want := reasonSubjectAuthorizationDetails
			if c.marker {
				want = reasonSubjectDelegatedAccess
			}
			requireOAuthError(t, err, "invalid_request", want)
		})
	}
}

func TestRestrictionsOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		claims *ValidatedClaims
		want   restrictions
	}{
		{name: "nil claims"},
		{name: "nil maps", claims: &ValidatedClaims{}},
		{name: "unrelated claims",
			claims: &ValidatedClaims{
				Claims: map[string]any{"tsid": "x"}, Extra: map[string]any{"client_id": "c"}}},
		{name: "marker in the verified claims only, nil Extra",
			claims: &ValidatedClaims{Claims: map[string]any{claimnames.DelegatedAccessKey: true}},
			want:   restrictions{DelegatedAccess: true}},
		{name: "marker in the extra claims only, with an empty value",
			claims: &ValidatedClaims{Extra: map[string]any{claimnames.DelegatedAccessKey: ""}},
			want:   restrictions{DelegatedAccess: true}},
		{name: "authorization_details in the verified claims only, nil Extra",
			claims: &ValidatedClaims{Claims: map[string]any{authorizationDetailsParam: terminalDetails()}},
			want:   restrictions{AuthorizationDetails: true}},
		{name: "authorization_details null in the extra claims only",
			claims: &ValidatedClaims{Extra: map[string]any{authorizationDetailsParam: nil}},
			want:   restrictions{AuthorizationDetails: true}},
		{name: "cnf object in the extra claims only",
			claims: &ValidatedClaims{Extra: map[string]any{claimConfirmation: map[string]any{"jkt": "t"}}},
			want:   restrictions{Confirmation: true}},
		{name: "cnf null in the verified claims only",
			claims: &ValidatedClaims{Claims: map[string]any{claimConfirmation: nil}},
			want:   restrictions{Confirmation: true}},
		{name: "every restriction, split across both maps",
			claims: &ValidatedClaims{
				Claims: map[string]any{claimnames.DelegatedAccessKey: true, claimConfirmation: map[string]any{}},
				Extra:  map[string]any{authorizationDetailsParam: []any{}},
			},
			want: restrictions{DelegatedAccess: true, AuthorizationDetails: true, Confirmation: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, restrictionsOf(tt.claims))
		})
	}
}

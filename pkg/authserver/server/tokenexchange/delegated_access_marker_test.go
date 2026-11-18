// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// markedExtra returns the extra claims of a token that carries the
// delegated-access marker.
func markedExtra() map[string]any {
	extra := validExtraClaims()
	extra[claimnames.DelegatedAccessKey] = true
	return extra
}

// detailsExtra returns the extra claims of a token that carries an
// authorization_details claim.
func detailsExtra() map[string]any {
	extra := validExtraClaims()
	extra[authorizationDetailsParam] = []any{map[string]any{"type": "https://example.com/call"}}
	return extra
}

func TestDelegatedAccessMarkerRefusedInTokenExchange(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)

	actorForm := func(extra map[string]any) map[string]string {
		return map[string]string{
			"actor_token":      tj.signToken(t, validClaims(), extra),
			"actor_token_type": oauthproto.TokenTypeJWT,
		}
	}
	otherClientExtra := markedExtra()
	otherClientExtra["client_id"] = "some-other-client"
	otherClientUnmarked := validExtraClaims()
	otherClientUnmarked["client_id"] = "some-other-client"

	tests := []struct {
		name string
		// form holds the parameters that override the default valid request.
		form map[string]string
		hint string
	}{
		{name: "marked subject token", hint: hintSubjectDelegatedAccess,
			form: map[string]string{"subject_token": tj.signToken(t, validClaims(), markedExtra())}},
		// A marked subject token is refused before the actor token, which is
		// bound to another client and would fail on its own, is resolved.
		{name: "marked subject token with an actor token bound to another client", hint: hintSubjectDelegatedAccess,
			form: func() map[string]string {
				f := actorForm(otherClientUnmarked)
				f["subject_token"] = tj.signToken(t, validClaims(), markedExtra())
				return f
			}()},
		{name: "marked actor token", hint: hintActorDelegatedAccess, form: actorForm(markedExtra())},
		{name: "subject token with authorization_details", hint: hintSubjectAuthorizationDetails,
			form: map[string]string{"subject_token": tj.signToken(t, validClaims(), detailsExtra())}},
		{name: "actor token with authorization_details", hint: hintActorAuthorizationDetails,
			form: actorForm(detailsExtra())},
		// The marker is refused before client_id binding reads the token.
		{name: "marked actor token bound to another client", hint: hintActorDelegatedAccess,
			form: actorForm(otherClientExtra)},
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

			require.ErrorIs(t, err, fosite.ErrInvalidRequest)
			var rfcErr *fosite.RFC6749Error
			require.ErrorAs(t, err, &rfcErr)
			assert.Equal(t, "invalid_request", rfcErr.ErrorField)
			assert.Equal(t, tt.hint, rfcErr.HintField)
		})
	}
}

func TestAuthorizationDetailsTokenRefusedAtGrantOpening(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	self, err := NewSelfIssuedTokenValidator(tj.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)
	extra := detailsExtra()
	extra["client_id"] = testRefreshSourceClient
	claims := validClaims()
	claims.Audience = []string{testIssuer}
	store := &fakeGrantStore{}
	h := newRefreshHandlerForTest(t, &stubSubjectValidator{}, store)
	h.selfValidator = self
	req := refreshRequestForTest(brokerClientForTest())
	req.Form.Set("subject_token", tj.signToken(t, claims, extra))

	err = h.HandleTokenEndpointRequest(t.Context(), req)

	requireOAuthError(t, err, "invalid_request", "subject_token_authorization_details")
	assert.Empty(t, store.created)
}

func TestHasDelegatedAccessMarker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		claims *ValidatedClaims
		want   bool
	}{
		{name: "nil claims"},
		{name: "neither map has the marker",
			claims: &ValidatedClaims{
				Claims: map[string]any{"tsid": "x"}, Extra: map[string]any{"client_id": "c"}}},
		{name: "marker in the verified claims only",
			claims: &ValidatedClaims{Claims: map[string]any{claimnames.DelegatedAccessKey: true}}, want: true},
		{name: "marker in the extra claims only, with an empty value",
			claims: &ValidatedClaims{Extra: map[string]any{claimnames.DelegatedAccessKey: ""}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, hasDelegatedAccessMarker(tt.claims))
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	actorDetailsType  = "https://example.com/actor-call"
	actorDetailsEntry = `[{"type":"` + actorDetailsType + `","note":"x"}]`
)

// TestIDJAGActorProfileDetails proves that for a client with an actor profile
// the hooks run before the actor validator, see an empty actor with
// ActorTokenPresent set, and that no refused entry reaches the validator,
// whatever refuses it. An accepted entry is signed next to the act claim.
func TestIDJAGActorProfileDetails(t *testing.T) {
	t.Parallel()

	refused := authorizationdetails.Error{
		Code: authorizationdetails.CodeInvalidAuthorizationDetails, Reason: "note_refused",
	}
	tests := []struct {
		name    string
		details string // default actorDetailsEntry
		// result is what the hook returns: nil keeps the entries.
		result func(req authorizationdetails.Request) (authorizationdetails.Result, error)
		// noHook is set when the refusal comes before the hooks.
		noHook       bool
		status       int // 0: accepted
		code, reason string
	}{
		{name: "an accepted entry is signed with act"},
		{name: "a hook refusal never reaches the validator",
			result: func(authorizationdetails.Request) (authorizationdetails.Result, error) {
				return authorizationdetails.Result{}, refused
			},
			status: http.StatusBadRequest, code: "invalid_authorization_details", reason: "note_refused"},
		{name: "empty_grant never reaches the validator",
			result: func(authorizationdetails.Request) (authorizationdetails.Result, error) {
				return authorizationdetails.Result{}, nil
			},
			status: http.StatusBadRequest, code: "invalid_authorization_details", reason: "empty_grant"},
		{name: "a failed hook never reaches the validator",
			result: func(authorizationdetails.Request) (authorizationdetails.Result, error) {
				return authorizationdetails.Result{}, errors.New("hook failed")
			},
			status: http.StatusInternalServerError, code: "server_error",
			reason: "The authorization_details could not be processed."},
		{name: "unknown_type never reaches the validator", noHook: true,
			details: `[{"type":"https://example.com/unknown"}]`,
			status:  http.StatusBadRequest, code: "invalid_authorization_details", reason: "unknown_type"},
		{name: "entries over the signed size limit never reach the validator",
			details: `[{"type":"` + actorDetailsType + `","note":"` + strings.Repeat("<", 6000) + `"}]`,
			status:  http.StatusBadRequest, code: "invalid_authorization_details", reason: "malformed_entry"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			details := tc.details
			if details == "" {
				details = actorDetailsEntry
			}
			var seen []authorizationdetails.Request
			registry, err := authorizationdetails.New(map[string]authorizationdetails.Type{
				actorDetailsType: {
					GrantTypes: []string{authorizationdetails.IDJAGIssuance},
					Grant: func(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
						seen = append(seen, req)
						if tc.result != nil {
							return tc.result(req)
						}
						return authorizationdetails.Result{Entries: req.Entries}, nil
					},
				},
			})
			require.NoError(t, err)

			v := mocks.NewMockActorTokenValidator(gomock.NewController(t))
			if tc.status == 0 {
				v.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(
					func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
						require.Len(t, seen, 2, "both phases ran the hook before the validator")
						return tokenexchange.ActorResult{AgentID: "agent-1", ExpiresAt: time.Now().Add(time.Hour)}, nil
					}).Times(1)
			}
			h := tokenexchange.NewRefreshTokenHandlerForTest(t, issuePolicies(v), issueGrants(),
				tokenexchange.WithIDJAGAuthorizationDetailsRegistry(registry))
			req := issueRequest(issueProfileClient, issueProfileClient)
			req.Form.Set("authorization_details", details)

			resp, err := issueBothPhases(t.Context(), h, req)
			if tc.noHook {
				assert.Empty(t, seen)
			} else {
				require.NotEmpty(t, seen)
			}
			for _, got := range seen {
				assert.Equal(t, &authorizationdetails.ExchangeContext{
					ActorTokenPresent: true, RequestedTokenType: oauthproto.TokenTypeIDJAG,
				}, got.Exchange, "the actor is not validated yet when the hook runs")
			}
			if tc.status != 0 {
				requireIssueError(t, err, tc.status, tc.code, tc.reason)
				assert.Empty(t, resp.GetAccessToken())
				return
			}
			require.NoError(t, err)
			claims := mintedClaims(t, resp.GetAccessToken())
			assert.Equal(t, map[string]any{"iss": "https://agents.example.com", "sub": "agent-1"}, claims["act"])
			got, err := json.Marshal(claims["authorization_details"])
			require.NoError(t, err)
			assert.JSONEq(t, actorDetailsEntry, string(got))
			assert.Equal(t, json.RawMessage(actorDetailsEntry), resp.GetExtra("authorization_details"))
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange_test

import (
	"context"
	"encoding/json"
	"net/http"
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
// ActorTokenPresent set, and that a refused entry never reaches the
// validator. An accepted entry is signed next to the act claim.
func TestIDJAGActorProfileDetails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		refuse  bool
		wantAct bool
	}{
		{name: "an accepted entry is signed with act", wantAct: true},
		{name: "a refused entry never reaches the validator", refuse: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var seen []authorizationdetails.Request
			registry, err := authorizationdetails.New(map[string]authorizationdetails.Type{
				actorDetailsType: {
					GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
					Grant: func(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
						seen = append(seen, req)
						if tc.refuse {
							return authorizationdetails.Result{}, authorizationdetails.Error{
								Code: authorizationdetails.CodeInvalidAuthorizationDetails, Reason: "note_refused",
							}
						}
						return authorizationdetails.Result{Entries: req.Entries}, nil
					},
				},
			})
			require.NoError(t, err)

			v := mocks.NewMockActorTokenValidator(gomock.NewController(t))
			if !tc.refuse {
				v.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(
					func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
						require.Len(t, seen, 2, "both phases ran the hook before the validator")
						return tokenexchange.ActorResult{AgentID: "agent-1", ExpiresAt: time.Now().Add(time.Hour)}, nil
					}).Times(1)
			}
			h := tokenexchange.NewRefreshTokenHandlerForTest(t, issuePolicies(v), issueGrants(),
				tokenexchange.WithIDJAGAuthorizationDetailsRegistry(registry))
			req := issueRequest(issueProfileClient, issueProfileClient)
			req.Form.Set("authorization_details", actorDetailsEntry)

			resp, err := issueBothPhases(t.Context(), h, req)
			require.NotEmpty(t, seen)
			for _, got := range seen {
				assert.Equal(t, &authorizationdetails.ExchangeContext{
					ActorTokenPresent: true, RequestedTokenType: oauthproto.TokenTypeIDJAG,
				}, got.Exchange, "the actor is not validated yet when the hook runs")
			}
			if tc.refuse {
				requireIssueError(t, err, http.StatusBadRequest, "invalid_authorization_details", "note_refused")
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

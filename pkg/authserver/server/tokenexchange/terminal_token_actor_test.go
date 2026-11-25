// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange_test

import (
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
)

func TestTerminalTokenRefusedAsIDJAGActorToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       tokenexchange.ActorTokenOptions
		wantReason string // empty when the validator decides and the ID-JAG is minted
	}{
		{name: "marked token signed with the server's key",
			opts: tokenexchange.ActorTokenOptions{Marked: true}, wantReason: "actor_token_delegated_access"},
		{name: "token with authorization_details signed with the server's key",
			opts: tokenexchange.ActorTokenOptions{Details: true}, wantReason: "actor_token_authorization_details"},
		// The marker is checked first, so a token that carries both keeps the
		// marker's reason.
		{name: "marked token with authorization_details signed with the server's key",
			opts:       tokenexchange.ActorTokenOptions{Marked: true, Details: true},
			wantReason: "actor_token_delegated_access"},
		{name: "expired marked token signed with the server's key",
			opts: tokenexchange.ActorTokenOptions{Marked: true, Expired: true}, wantReason: "actor_token_delegated_access"},
		{name: "expired token with authorization_details signed with the server's key",
			opts:       tokenexchange.ActorTokenOptions{Details: true, Expired: true},
			wantReason: "actor_token_authorization_details"},
		{name: "marked token signed with the server's key, audience not allowed",
			opts:       tokenexchange.ActorTokenOptions{Marked: true, Audience: []string{"https://other.example.com"}},
			wantReason: "actor_token_delegated_access"},
		{name: "unrestricted token signed with the server's key goes to the validator"},
		{name: "marked token signed with another key goes to the validator",
			opts: tokenexchange.ActorTokenOptions{Marked: true, ForeignKey: true}},
		// An external actor's authorization_details describe the agent's own
		// limits, so the actor validator decides on them.
		{name: "token with authorization_details signed with another key goes to the validator",
			opts: tokenexchange.ActorTokenOptions{Details: true, ForeignKey: true}},
		{name: "marked token signed with the server's key but naming another issuer goes to the validator",
			opts: tokenexchange.ActorTokenOptions{Marked: true, Issuer: "https://other.example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			validator := mocks.NewMockActorTokenValidator(gomock.NewController(t))
			if tt.wantReason == "" {
				validator.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).Times(1).Return(
					tokenexchange.ActorResult{AgentID: "agent-1", ExpiresAt: time.Now().Add(time.Hour)}, nil)
			} else {
				validator.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).Times(0)
			}
			h, req := tokenexchange.ActorTokenIDJAGRequestForTest(t, validator, tt.opts)
			resp := fosite.NewAccessResponse()

			require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), req))
			err := h.PopulateTokenEndpointResponse(t.Context(), req, resp)

			if tt.wantReason == "" {
				require.NoError(t, err)
				assert.NotEmpty(t, resp.GetAccessToken())
				return
			}
			requireIssueError(t, err, 400, "invalid_request", tt.wantReason)
			assert.Empty(t, resp.GetAccessToken())
		})
	}
}

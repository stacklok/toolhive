// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActorDeniedErrorError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *ActorDeniedError
		want string
	}{
		{
			name: "reason set",
			err:  &ActorDeniedError{Reason: ActorDeniedReasonActorTokenExpired},
			want: "actor denied: actor_token_expired",
		},
		{
			name: "empty reason",
			err:  &ActorDeniedError{},
			want: "actor denied",
		},
		{
			name: "nil receiver",
			err:  nil,
			want: "actor denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

const (
	actorTestAgentID = "ag_abcdefghijklmnopqrstuvwxyz"
	actorSentinel    = "SENTINEL-do-not-leak"
)

func actorServerDesc() string {
	return fosite.ErrServerError.WithHint("The actor token could not be validated.").GetDescription()
}

func TestMapActorOutcome(t *testing.T) {
	t.Parallel()

	checkedAt := time.Unix(1_800_000_000, 0)
	valid := ActorResult{
		AgentID:           actorTestAgentID,
		ExpiresAt:         checkedAt.Add(time.Minute),
		CredentialSubject: "spiffe://example.org/agent",
	}
	denied := func(r ActorDeniedReason) error { return &ActorDeniedError{Reason: r} }
	var nilDenied *ActorDeniedError

	const (
		invalid = "invalid_request" // a refusal: 400, the reason is the whole description
		server  = "server_error"    // 500 with the generic hint
	)
	tests := []struct {
		name       string
		res        ActorResult
		err        error
		wantReason string
		wantCode   string // empty means success
	}{
		{name: "actor_token_expired", err: denied(ActorDeniedReasonActorTokenExpired), wantReason: "actor_token_expired", wantCode: invalid},
		{name: "approval_required", err: denied(ActorDeniedReasonApprovalRequired), wantReason: "approval_required", wantCode: invalid},
		{name: "denied", err: denied(ActorDeniedReasonDenied), wantReason: "denied", wantCode: invalid},
		{name: "denied wrapped", err: fmt.Errorf("wrap: %w", denied(ActorDeniedReasonDenied)),
			wantReason: "denied", wantCode: invalid},
		{name: "typed nil denied", err: nilDenied, wantReason: "actor_validator_error", wantCode: server},
		{name: "unknown reason", err: denied("nope"), wantReason: "actor_validator_error", wantCode: server},
		{name: "plain error", err: errors.New("backend down"), wantReason: "actor_validator_error", wantCode: server},
		{name: "error wins over valid result", res: valid, err: errors.New("backend down"),
			wantReason: "actor_validator_error", wantCode: server},
		{name: "invalid agent ID", res: ActorResult{AgentID: "a:b", ExpiresAt: valid.ExpiresAt},
			wantReason: "actor_result_invalid", wantCode: server},
		{name: "zero expiry", res: ActorResult{AgentID: actorTestAgentID},
			wantReason: "actor_result_invalid", wantCode: server},
		{name: "expired result", res: ActorResult{AgentID: actorTestAgentID, ExpiresAt: checkedAt.Add(-time.Second)},
			wantReason: "actor_token_expired", wantCode: invalid},
		{name: "valid result", res: valid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, reason, err := mapActorOutcome(tt.res, tt.err, checkedAt)
			assert.Equal(t, tt.wantReason, reason)

			if tt.wantCode == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.res, got)
				return
			}

			assert.Equal(t, ActorResult{}, got)
			var rfc *fosite.RFC6749Error
			require.ErrorAs(t, err, &rfc)
			assert.Equal(t, tt.wantCode, rfc.ErrorField)
			if tt.wantCode == server {
				assert.Equal(t, http.StatusInternalServerError, rfc.CodeField)
				assert.Equal(t, actorServerDesc(), rfc.GetDescription())
				require.ErrorIs(t, err, fosite.ErrServerError)
			} else {
				assert.Equal(t, http.StatusBadRequest, rfc.CodeField)
				assert.Equal(t, tt.wantReason, rfc.GetDescription())
			}
		})
	}
}

// TestMapActorOutcomeFlattensValidatorErrors covers the two call sites in
// classifyActorError that turn a validator error into a server error. The text
// must not reach the client, and nothing the validator wrapped may stay reachable,
// because fosite reacts to a wrapped fosite.ErrUnknownRequest.
func TestMapActorOutcomeFlattensValidatorErrors(t *testing.T) {
	t.Parallel()

	// Typed as error so vet accepts it as a %w operand.
	var unknownRequest error = fosite.ErrUnknownRequest
	tests := []struct {
		name string
		err  error
	}{
		{"unknown denial reason", fmt.Errorf("%s: %w", actorSentinel, &ActorDeniedError{Reason: "nope"})},
		{"plain error", fmt.Errorf("backend said %s: %w", actorSentinel, unknownRequest)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := mapActorOutcome(ActorResult{}, tt.err, time.Unix(1_800_000_000, 0))
			var rfc *fosite.RFC6749Error
			require.ErrorAs(t, err, &rfc)
			assert.NotContains(t, rfc.GetDescription(), actorSentinel)
			assert.NotContains(t, rfc.WithExposeDebug(true).GetDescription(), actorSentinel)
			wrapped := errors.Unwrap(rfc)
			require.NotNil(t, wrapped)
			assert.Contains(t, wrapped.Error(), actorSentinel)
			assert.NotErrorIs(t, err, tt.err)
			assert.NotErrorIs(t, err, fosite.ErrUnknownRequest)
		})
	}
}

func TestAgentIDProblem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		wantErr string // empty means valid
	}{
		{"255 bytes", strings.Repeat("a", 255), ""},
		{"256 bytes", strings.Repeat("a", 256), "agent ID is invalid: length 256"},
		{"empty", "", "agent ID is invalid: length 0"},
		{"colon", "ag:1", "agent ID is invalid: byte 0x3a at offset 2"},
		{"space 0x20", "ag 1", "agent ID is invalid: byte 0x20 at offset 2"},
		{"DEL 0x7F", "ag\x7f", "agent ID is invalid: byte 0x7f at offset 2"},
		{"non-ASCII", "agé", "agent ID is invalid: byte 0xc3 at offset 2"},
		{"bang 0x21", "!", ""},
		{"tilde 0x7E", "~", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := agentIDProblem(tt.id)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantErr, err.Error())
		})
	}
}

func TestMapActorOutcomeWholeSeconds(t *testing.T) {
	t.Parallel()

	whole := time.Unix(1_800_000_000, 0)
	fractional := time.Unix(1_800_000_000, 700_000_000)

	tests := []struct {
		name       string
		checkedAt  time.Time
		expiresAt  time.Time
		wantReason string
	}{
		{"whole second, +400ms", whole, whole.Add(400 * time.Millisecond), "actor_token_expired"},
		{"whole second, +1s", whole, whole.Add(time.Second), ""},
		{"whole second, equal", whole, whole, "actor_token_expired"},
		{"x.7s, +400ms crosses a second", fractional, fractional.Add(400 * time.Millisecond), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res := ActorResult{AgentID: actorTestAgentID, ExpiresAt: tt.expiresAt}
			got, reason, err := mapActorOutcome(res, nil, tt.checkedAt)
			assert.Equal(t, tt.wantReason, reason)
			if tt.wantReason == "" {
				require.NoError(t, err)
				assert.Equal(t, res, got)
				return
			}
			var rfc *fosite.RFC6749Error
			require.ErrorAs(t, err, &rfc)
			assert.Equal(t, "invalid_request", rfc.ErrorField)
			assert.Equal(t, http.StatusBadRequest, rfc.CodeField)
			assert.Equal(t, "actor_token_expired", rfc.GetDescription())
		})
	}
}

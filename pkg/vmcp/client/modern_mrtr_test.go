// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInterpretModernResultInputRequired pins the egress MRTR seam
// (docs/arch/16-vmcp-mrtr.md, slice 1): an input_required envelope must
// surface as an error that (a) still satisfies every existing
// errors.Is(err, errModernInputRequired) classification, (b) renders the
// exact message the previous fmt.Errorf wrapping produced (no client-visible
// drift), and (c) now carries the decoded SEP-2322 payload, extractable
// through arbitrary wrapping via InputRequiredFromError.
func TestInterpretModernResultInputRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		result      string
		wantPayload bool
		wantKeys    map[string]string // request key -> method, per Methods()
		wantState   string
	}{
		{
			name: "input_required with elicitation and state",
			result: `{
				"resultType": "input_required",
				"inputRequests": {
					"github_login": {"method": "elicitation/create", "params": {"message": "user?"}},
					"summary":      {"method": "sampling/createMessage", "params": {"maxTokens": 10}}
				},
				"requestState": "opaque-blob"
			}`,
			wantPayload: true,
			wantKeys: map[string]string{
				"github_login": "elicitation/create",
				"summary":      "sampling/createMessage",
			},
			wantState: "opaque-blob",
		},
		{
			// SEP-2322's load-shedding shape: requestState only, no inputRequests.
			// The client may retry immediately; the payload must still decode.
			name:        "load-shedding round (requestState only)",
			result:      `{"resultType": "input_required", "requestState": "resume-here"}`,
			wantPayload: true,
			wantKeys:    nil,
			wantState:   "resume-here",
		},
		{
			// Malformed payload: the round is undrivable, but the typed error and
			// sentinel classification must survive a backend that violates the
			// schema (tolerant decode).
			name:        "malformed inputRequests still yields the typed error",
			result:      `{"resultType": "input_required", "inputRequests": "not-a-map"}`,
			wantPayload: true,
			wantKeys:    nil,
			wantState:   "",
		},
		{
			// An unrecognized resultType is an invalid protocol response per the
			// spec; it keeps pure sentinel semantics with NO extractable round.
			name:        "unrecognized resultType carries no payload",
			result:      `{"resultType": "task"}`,
			wantPayload: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := interpretModernResult(json.RawMessage(tt.result), nil, "tools/call", nil)
			require.Error(t, err)

			// (a) classification is unchanged.
			require.ErrorIs(t, err, errModernInputRequired)

			// (b) message is byte-identical to the previous wrapping.
			var envelope struct {
				ResultType string `json:"resultType"`
			}
			require.NoError(t, json.Unmarshal([]byte(tt.result), &envelope))
			assert.Equal(t,
				fmt.Errorf("%w: resultType=%q", errModernInputRequired, envelope.ResultType).Error(),
				err.Error())

			// (c) payload extraction, through an extra wrapping layer to mirror
			// how modernCallTool wraps backend errors before they reach dispatch.
			wrapped := fmt.Errorf("tool call failed on backend %s: %w", "b1", err)
			round, ok := InputRequiredFromError(wrapped)
			require.Equal(t, tt.wantPayload, ok)
			if !tt.wantPayload {
				assert.Nil(t, round)
				return
			}
			require.NotNil(t, round)
			assert.Equal(t, tt.wantState, round.RequestState)
			assert.Equal(t, tt.wantKeys, round.Methods())
		})
	}
}

// TestInputRequiredFromErrorRejectsForeignErrors pins that the extractor is a
// safe single branch point: unrelated errors — including the OTHER Modern
// sentinels — report ok=false.
func TestInputRequiredFromErrorRejectsForeignErrors(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		nil,
		errWrongEra,
		errLegacyResponseBody,
		fmt.Errorf("wrapped: %w", errModernProtocolError),
	} {
		round, ok := InputRequiredFromError(err)
		assert.False(t, ok, "error %v must not extract as input_required", err)
		assert.Nil(t, round)
	}
}

// TestInputRequiredRequestsAreRelayedVerbatim pins the pass-through
// invariant: the raw bytes of each input request survive the decode
// untouched, so the relay (slice 2) can forward them without reinterpretation.
func TestInputRequiredRequestsAreRelayedVerbatim(t *testing.T) {
	t.Parallel()

	const rawReq = `{"method":"elicitation/create","params":{"mode":"form","message":"x","extra":[1,2,3]}}`
	result := `{"resultType":"input_required","inputRequests":{"k1":` + rawReq + `}}`

	err := interpretModernResult(json.RawMessage(result), nil, "tools/call", nil)
	round, ok := InputRequiredFromError(err)
	require.True(t, ok)
	require.Contains(t, round.InputRequests, "k1")
	assert.JSONEq(t, rawReq, string(round.InputRequests["k1"]))
}

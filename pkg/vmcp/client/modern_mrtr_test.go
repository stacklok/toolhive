// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestInterpretModernResultInputRequired pins the egress MRTR seam
// (docs/arch/16-vmcp-mrtr.md, slice 1): an input_required envelope must
// surface as an error that (a) still satisfies every existing
// errors.Is(err, errModernInputRequired) classification, (b) renders the
// exact message the previous fmt.Errorf wrapping produced (no client-visible
// drift), and (c) carries an extractable SEP-2322 round — but only for a
// valid, drivable round on a method that may carry one; protocol violations
// fail closed to sentinel-only semantics.
func TestInterpretModernResultInputRequired(t *testing.T) {
	t.Parallel()

	strPtr := func(s string) *string { return &s }

	tests := []struct {
		name        string
		method      string
		result      string
		wantMsg     string // "" means derive from the resultType template
		wantPayload bool
		wantKeys    map[string]string // request key -> method, per Methods()
		wantState   *string
	}{
		{
			name:   "input_required with elicitation and state",
			method: "tools/call",
			result: `{
				"resultType": "input_required",
				"inputRequests": {
					"github_login": {"method": "elicitation/create", "params": {"message": "user?"}},
					"summary":      {"method": "sampling/createMessage", "params": {"maxTokens": 10}}
				},
				"requestState": "opaque-blob"
			}`,
			wantMsg: `modern response requires additional input (multi-round retrieval unsupported): ` +
				`resultType="input_required"`,
			wantPayload: true,
			wantKeys: map[string]string{
				"github_login": "elicitation/create",
				"summary":      "sampling/createMessage",
			},
			wantState: strPtr("opaque-blob"),
		},
		{
			// SEP-2322's load-shedding shape: requestState only, no inputRequests.
			// The client may retry immediately; the payload must still decode.
			name:        "load-shedding round (requestState only)",
			method:      "resources/read",
			result:      `{"resultType": "input_required", "requestState": "resume-here"}`,
			wantPayload: true,
			wantKeys:    nil,
			wantState:   strPtr("resume-here"),
		},
		{
			// requestState?: string has no minimum length: present-and-empty is
			// legal, distinct from absent, and must survive as such — client
			// requirement 2 makes the retry echo the exact value.
			name:        "present-and-empty requestState is a drivable round",
			method:      "prompts/get",
			result:      `{"resultType": "input_required", "requestState": ""}`,
			wantPayload: true,
			wantKeys:    nil,
			wantState:   strPtr(""),
		},
		{
			// Wrong-typed inputRequests: a schema violation. The typed error and
			// sentinel classification survive, but no round is extractable — a
			// consumer must fail closed on a protocol violation, not relay an
			// empty envelope.
			name:        "malformed inputRequests fails closed to sentinel-only",
			method:      "tools/call",
			result:      `{"resultType": "input_required", "inputRequests": "not-a-map"}`,
			wantPayload: false,
		},
		{
			// Server requirement 6 violation: neither inputRequests nor
			// requestState. Indistinguishable-from-empty must not become a
			// retry-forever round.
			name:        "empty envelope (server requirement 6 violation) fails closed",
			method:      "tools/call",
			result:      `{"resultType": "input_required"}`,
			wantPayload: false,
		},
		{
			// A wrong-typed requestState (numeric) must not silently decode as
			// absent or empty.
			name:        "wrong-typed requestState fails closed",
			method:      "tools/call",
			result:      `{"resultType": "input_required", "requestState": 42}`,
			wantPayload: false,
		},
		{
			// Only tools/call, resources/read, and prompts/get may carry a round
			// ("Servers MUST NOT send InputRequiredResult responses on any other
			// client requests"). A valid-looking round on another method is a
			// backend protocol violation; relaying it would make vMCP the
			// non-conformant server.
			name:        "input_required on a disallowed method carries no round",
			method:      "completion/complete",
			result:      `{"resultType": "input_required", "requestState": "x"}`,
			wantPayload: false,
		},
		{
			// An unrecognized resultType "MUST be considered invalid" per the
			// spec; it keeps pure sentinel semantics with NO extractable round.
			name:        "unrecognized resultType carries no payload",
			method:      "tools/call",
			result:      `{"resultType": "partial"}`,
			wantPayload: false,
		},
		{
			// "task" is the Tasks extension's CreateTaskResult discriminator, not
			// an input round. vMCP advertises no extensions, so rejecting it here
			// is correct — it lands on the same sentinel as a genuinely
			// unrecognized value, even though the sentinel's "requires additional
			// input" text names the wrong reason (message frozen for compat).
			name:        "Tasks-extension resultType carries no payload",
			method:      "tools/call",
			result:      `{"resultType": "task"}`,
			wantPayload: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := interpretModernResult(json.RawMessage(tt.result), nil, tt.method, nil)
			require.Error(t, err)

			// (a) classification is unchanged.
			require.ErrorIs(t, err, errModernInputRequired)

			// (b) message is byte-identical to the previous wrapping. The
			// expected text is hardcoded for the first case so the production
			// template and this assertion cannot drift together; the rest derive
			// it from the envelope like the old wrapping did.
			if tt.wantMsg != "" {
				assert.Equal(t, tt.wantMsg, err.Error())
			} else {
				var envelope struct {
					ResultType string `json:"resultType"`
				}
				require.NoError(t, json.Unmarshal([]byte(tt.result), &envelope))
				assert.Equal(t,
					fmt.Errorf("%w: resultType=%q", errModernInputRequired, envelope.ResultType).Error(),
					err.Error())
			}

			// (c) payload extraction, through an extra wrapping layer to mirror
			// how modernCallTool wraps backend errors before they reach dispatch.
			wrapped := fmt.Errorf("tool call failed on backend %s: %w", "b1", err)
			round, ok := vmcp.InputRequiredFromError(wrapped)
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

// TestInputRequiredErrorTruncatesHostileResultType pins that a
// backend-controlled resultType does not ride unbounded into the error text
// (which reaches the downstream client via writeModernDispatchError): beyond
// 64 bytes the message carries a prefix and the length, per the #6079/#6066
// report-the-length rule.
func TestInputRequiredErrorTruncatesHostileResultType(t *testing.T) {
	t.Parallel()

	hostile := strings.Repeat("A", 500)
	result := fmt.Sprintf(`{"resultType": %q}`, hostile)

	err := interpretModernResult(json.RawMessage(result), nil, "tools/call", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, errModernInputRequired)
	assert.Equal(t,
		"modern response requires additional input (multi-round retrieval unsupported): "+
			fmt.Sprintf("resultType=%q... (500 bytes)", strings.Repeat("A", 64)),
		err.Error())
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
		round, ok := vmcp.InputRequiredFromError(err)
		assert.False(t, ok, "error %v must not extract as input_required", err)
		assert.Nil(t, round)
	}
}

// TestInputRequiredRequestsAreRelayedVerbatim pins the pass-through
// invariant: the raw bytes of each input request survive the decode
// untouched (json.RawMessage preserves them exactly), so the relay (slice 2)
// can forward them without reinterpretation.
func TestInputRequiredRequestsAreRelayedVerbatim(t *testing.T) {
	t.Parallel()

	const rawReq = `{"method":"elicitation/create","params":{"mode":"form","message":"x","extra":[1,2,3]}}`
	result := `{"resultType":"input_required","inputRequests":{"k1":` + rawReq + `}}`

	err := interpretModernResult(json.RawMessage(result), nil, "tools/call", nil)
	round, ok := vmcp.InputRequiredFromError(err)
	require.True(t, ok)
	require.Contains(t, round.InputRequests, "k1")
	assert.Equal(t, rawReq, string(round.InputRequests["k1"]))
}

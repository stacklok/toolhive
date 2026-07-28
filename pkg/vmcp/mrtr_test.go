// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInputRequiredError pins the carrier's contract for consumers that
// construct it directly (e.g. tests against mocks.MockBackendClient, which
// cannot reach the backend client's unexported sentinel): it unwraps to
// whatever Sentinel it was built with, renders the sentinel-prefixed message,
// and InputRequiredFromError extracts the round only when Result is non-nil.
func TestInputRequiredError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("some classification")
	state := "s1"
	withRound := &InputRequiredError{
		ResultType: "input_required",
		Result:     &InputRequiredResult{RequestState: &state},
		Sentinel:   sentinel,
	}
	require.ErrorIs(t, withRound, sentinel)
	assert.Equal(t, `some classification: resultType="input_required"`, withRound.Error())

	round, ok := InputRequiredFromError(fmt.Errorf("wrapped: %w", withRound))
	require.True(t, ok)
	assert.Equal(t, &state, round.RequestState)

	// Result nil (unrecognized resultType, disallowed method, invalid
	// payload): classification survives, extraction reports false.
	sentinelOnly := &InputRequiredError{ResultType: "task", Sentinel: sentinel}
	require.ErrorIs(t, sentinelOnly, sentinel)
	round, ok = InputRequiredFromError(sentinelOnly)
	assert.False(t, ok)
	assert.Nil(t, round)
}

// TestInputRequiredErrorBoundsResultType pins that Error() never interpolates
// more than 64 bytes of the backend-controlled resultType.
func TestInputRequiredErrorBoundsResultType(t *testing.T) {
	t.Parallel()

	e := &InputRequiredError{
		ResultType: strings.Repeat("x", 200),
		Sentinel:   errors.New("sentinel"),
	}
	assert.Equal(t,
		fmt.Sprintf("sentinel: resultType=%q... (200 bytes)", strings.Repeat("x", 64)),
		e.Error())
}

func TestInputRequiredResultMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		requests map[string]json.RawMessage
		want     map[string]string
	}{
		{
			name: "methods extracted per key",
			requests: map[string]json.RawMessage{
				"a": json.RawMessage(`{"method":"elicitation/create","params":{}}`),
				"b": json.RawMessage(`{"method":"sampling/createMessage"}`),
			},
			want: map[string]string{"a": "elicitation/create", "b": "sampling/createMessage"},
		},
		{
			// A malformed entry maps to "" — the fail-closed value for a
			// capability gate — rather than being silently dropped.
			name: "malformed entry fails closed as empty method",
			requests: map[string]json.RawMessage{
				"bad":  json.RawMessage(`[1,2]`),
				"good": json.RawMessage(`{"method":"roots/list"}`),
			},
			want: map[string]string{"bad": "", "good": "roots/list"},
		},
		{
			name:     "empty requests yield nil",
			requests: nil,
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &InputRequiredResult{InputRequests: tt.requests}
			assert.Equal(t, tt.want, r.Methods())
		})
	}
}

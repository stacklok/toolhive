// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

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

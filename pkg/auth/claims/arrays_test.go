// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package claims

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeStringArray(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   any
		want    []string
		wantErr bool
		errSubs string
	}{
		{
			name:  "typed []string with values",
			input: []string{"a", "b", "c"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "[]any with all strings (from encoding/json)",
			input: []any{"a", "b", "c"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "typed []string empty returns nil",
			input: []string{},
			want:  nil,
		},
		{
			name:  "[]any empty returns nil",
			input: []any{},
			want:  nil,
		},
		{
			name:  "typed []string with single value",
			input: []string{"only"},
			want:  []string{"only"},
		},
		{
			name:  "[]any with single string",
			input: []any{"only"},
			want:  []string{"only"},
		},
		{
			name:    "nil input is rejected",
			input:   nil,
			wantErr: true,
			errSubs: "nil",
		},
		{
			name:    "typed []string(nil) is rejected",
			input:   []string(nil),
			wantErr: true,
			errSubs: "nil",
		},
		{
			name:    "typed []any(nil) is rejected",
			input:   []any(nil),
			wantErr: true,
			errSubs: "nil",
		},
		{
			name:    "[]any with int element is rejected",
			input:   []any{"a", 42, "c"},
			wantErr: true,
			errSubs: "index 1 is int",
		},
		{
			name:    "[]any with nil element is rejected",
			input:   []any{"a", nil},
			wantErr: true,
			errSubs: "index 1 is <nil>",
		},
		{
			name:    "[]any with nested map element is rejected",
			input:   []any{"a", map[string]any{"x": "y"}},
			wantErr: true,
			errSubs: "want string",
		},
		{
			name:    "scalar string (not an array) is rejected",
			input:   "just-a-string",
			wantErr: true,
			errSubs: "want JSON string array",
		},
		{
			name:    "int is rejected",
			input:   42,
			wantErr: true,
			errSubs: "want JSON string array",
		},
		{
			name:    "map is rejected",
			input:   map[string]any{"groups": "x"},
			wantErr: true,
			errSubs: "want JSON string array",
		},
		{
			name:    "[]int is rejected (not a supported array shape)",
			input:   []int{1, 2, 3},
			wantErr: true,
			errSubs: "want JSON string array",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := DecodeStringArray(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errSubs != "" {
					assert.Contains(t, err.Error(), tc.errSubs)
				}
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestDecodeStringArray_InputImmutability verifies that mutating the caller's
// input slice after a successful decode does not affect the returned slice.
// This matters because JWT claims are commonly reused across authorization
// hops; the filter contract treats principal.Claims as read-only, and this
// helper must not force the caller to defensively copy before passing a claim.
func TestDecodeStringArray_InputImmutability(t *testing.T) {
	t.Parallel()

	t.Run("typed []string", func(t *testing.T) {
		t.Parallel()
		input := []string{"a", "b"}
		got, err := DecodeStringArray(input)
		require.NoError(t, err)

		// Mutate the caller's slice.
		input[0] = "mutated"

		assert.Equal(t, []string{"a", "b"}, got, "returned slice must not alias input")
	})

	t.Run("[]any", func(t *testing.T) {
		t.Parallel()
		input := []any{"a", "b"}
		got, err := DecodeStringArray(input)
		require.NoError(t, err)

		// Mutate the caller's slice (replace element).
		input[0] = "mutated"

		assert.Equal(t, []string{"a", "b"}, got, "returned slice must not alias input")
	})
}

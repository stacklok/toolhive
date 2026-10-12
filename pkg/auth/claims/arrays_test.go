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

// TestDecodeStringArrayLenient covers the permissive contract the Cedar
// authorizer relies on. Every case here is a shape where the lenient and the
// strict decoder deliberately disagree, or where the nil-ness of the result
// carries meaning for the caller.
func TestDecodeStringArrayLenient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		input          any
		want           []string
		wantRecognized bool
	}{
		{
			name:           "typed []string with values",
			input:          []string{"a", "b"},
			want:           []string{"a", "b"},
			wantRecognized: true,
		},
		{
			name:           "[]any with all strings",
			input:          []any{"a", "b"},
			want:           []string{"a", "b"},
			wantRecognized: true,
		},
		{
			// Strict rejects this; lenient keeps the usable names.
			name:           "[]any with mixed elements drops non-strings",
			input:          []any{"a", 42, true, "b"},
			want:           []string{"a", "b"},
			wantRecognized: true,
		},
		{
			name:           "[]any of only non-strings decodes to non-nil empty",
			input:          []any{42, nil},
			want:           []string{},
			wantRecognized: true,
		},
		{
			// Non-nil empty must stay non-nil: callers read it as "the claim
			// is present and asserts no values".
			name:           "[]any empty stays non-nil",
			input:          []any{},
			want:           []string{},
			wantRecognized: true,
		},
		{
			name:           "typed []any(nil) is a recognized empty array",
			input:          []any(nil),
			want:           []string{},
			wantRecognized: true,
		},
		{
			// The one shape that decodes to nil while still being recognized.
			name:           "typed []string(nil) stays nil",
			input:          []string(nil),
			want:           nil,
			wantRecognized: true,
		},
		{
			name:           "typed []string{} stays non-nil empty",
			input:          []string{},
			want:           []string{},
			wantRecognized: true,
		},
		{
			name:           "untyped nil is not recognized",
			input:          nil,
			want:           nil,
			wantRecognized: false,
		},
		{
			name:           "scalar string is not recognized",
			input:          "not-an-array",
			want:           nil,
			wantRecognized: false,
		},
		{
			name:           "map is not recognized",
			input:          map[string]any{"groups": "x"},
			want:           nil,
			wantRecognized: false,
		},
		{
			name:           "[]int is not recognized",
			input:          []int{1, 2},
			want:           nil,
			wantRecognized: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, recognized := DecodeStringArrayLenient(tc.input)
			assert.Equal(t, tc.wantRecognized, recognized)
			// assert.Equal distinguishes a nil slice from an empty one, which
			// is exactly the distinction callers depend on.
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestDecodeStringArrayLenient_InputImmutability verifies the lenient decoder
// honours the same no-aliasing contract as the strict one, so a caller may
// append to the result without writing into the token's claim value.
func TestDecodeStringArrayLenient_InputImmutability(t *testing.T) {
	t.Parallel()

	t.Run("typed []string", func(t *testing.T) {
		t.Parallel()
		input := make([]string, 2, 8)
		input[0], input[1] = "a", "b"

		got, recognized := DecodeStringArrayLenient(input)
		require.True(t, recognized)

		// Appending to the result is exactly the pattern this guards against.
		merged := append(got, "appended")
		require.Len(t, merged, 3)
		got[0] = "mutated"

		assert.Equal(t, []string{"a", "b"}, input, "input must not be modified")
		assert.Equal(t, "", input[:cap(input)][2], "append must not reach the input's backing array")
	})

	t.Run("[]any", func(t *testing.T) {
		t.Parallel()
		input := []any{"a", "b"}

		got, recognized := DecodeStringArrayLenient(input)
		require.True(t, recognized)

		input[0] = "mutated"

		assert.Equal(t, []string{"a", "b"}, got, "returned slice must not alias input")
	})
}

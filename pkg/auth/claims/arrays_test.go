// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package claims

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	oauthclaims "github.com/stacklok/toolhive/pkg/oauthproto/claims"
)

func TestDecodeStringArrayCompatibilityWrapper(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input any
	}{
		{name: "typed strings", input: []string{"a", "b"}},
		{name: "interface strings", input: []any{"a", "b"}},
		{name: "empty typed strings", input: []string{}},
		{name: "empty interface strings", input: []any{}},
		{name: "nil", input: nil},
		{name: "typed nil strings", input: []string(nil)},
		{name: "typed nil interfaces", input: []any(nil)},
		{name: "scalar", input: "scalar"},
		{name: "mixed array", input: []any{"a", 42}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want, wantErr := oauthclaims.DecodeStringArray(tc.input)
			got, err := DecodeStringArray(tc.input)
			assert.Equal(t, want, got)
			if wantErr != nil {
				require.EqualError(t, err, wantErr.Error())
			} else {
				require.NoError(t, err)
			}
		})
	}
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

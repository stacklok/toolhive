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
		wantErr string
	}{
		{name: "typed strings", input: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "interface strings", input: []any{"a", "b"}, want: []string{"a", "b"}},
		{name: "empty typed strings", input: []string{}, want: nil},
		{name: "empty interface strings", input: []any{}, want: nil},
		{name: "nil", input: nil, wantErr: "nil"},
		{name: "typed nil strings", input: []string(nil), wantErr: "nil"},
		{name: "typed nil interfaces", input: []any(nil), wantErr: "nil"},
		{name: "scalar string", input: "group", wantErr: "want JSON string array"},
		{name: "scalar number", input: 42, wantErr: "want JSON string array"},
		{name: "object", input: map[string]any{"group": "a"}, wantErr: "want JSON string array"},
		{name: "unsupported slice", input: []int{1}, wantErr: "want JSON string array"},
		{name: "mixed elements", input: []any{"a", 42}, wantErr: "index 1 is int"},
		{name: "nil element", input: []any{"a", nil}, wantErr: "index 1 is <nil>"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := DecodeStringArray(tc.input)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDecodeStringArrayDoesNotAliasInput(t *testing.T) {
	t.Parallel()

	t.Run("typed strings", func(t *testing.T) {
		t.Parallel()
		input := make([]string, 2, 8)
		input[0], input[1] = "a", "b"
		got, err := DecodeStringArray(input)
		require.NoError(t, err)

		input[0] = "input mutation"
		assert.Equal(t, []string{"a", "b"}, got)
		got[1] = "output mutation"
		assert.Equal(t, "b", input[1])
	})

	t.Run("interface strings", func(t *testing.T) {
		t.Parallel()
		input := []any{"a", "b"}
		got, err := DecodeStringArray(input)
		require.NoError(t, err)

		input[0] = "input mutation"
		assert.Equal(t, []string{"a", "b"}, got)
		got[1] = "output mutation"
		assert.Equal(t, "b", input[1])
	})
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package idjag

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResourceClaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		claim      any
		wantValues []string
		wantOK     bool
	}{
		{name: "nil", claim: nil, wantValues: nil, wantOK: false},
		{name: "empty string", claim: "", wantValues: []string{""}, wantOK: false},
		{name: "string", claim: "a", wantValues: []string{"a"}, wantOK: true},
		{name: "empty array", claim: []any{}, wantValues: nil, wantOK: false},
		{name: "one element", claim: []any{"a"}, wantValues: []string{"a"}, wantOK: true},
		{name: "two elements", claim: []any{"a", "b"}, wantValues: []string{"a", "b"}, wantOK: true},
		{name: "number element", claim: []any{42.0}, wantValues: nil, wantOK: false},
		{name: "string and number", claim: []any{"a", 42.0}, wantValues: []string{"a"}, wantOK: false},
		{name: "duplicates", claim: []any{"a", "a"}, wantValues: []string{"a", "a"}, wantOK: false},
		{name: "empty element", claim: []any{""}, wantValues: []string{""}, wantOK: false},
		{name: "number", claim: 42.0, wantValues: nil, wantOK: false},
		{name: "object", claim: map[string]any{"x": 1}, wantValues: nil, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			values, ok := ResourceClaim(tt.claim)
			assert.Equal(t, tt.wantValues, values)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

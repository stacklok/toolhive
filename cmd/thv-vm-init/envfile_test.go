// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergeEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		base      []string
		overrides []string
		expected  []string
	}{
		{
			name:      "override replaces base key",
			base:      []string{"FOO=base", "BAR=keep"},
			overrides: []string{"FOO=override"},
			expected:  []string{"FOO=override", "BAR=keep"},
		},
		{
			name:      "new key appended",
			base:      []string{"FOO=base"},
			overrides: []string{"BAR=new"},
			expected:  []string{"FOO=base", "BAR=new"},
		},
		{
			name:      "empty overrides returns base",
			base:      []string{"FOO=bar"},
			overrides: nil,
			expected:  []string{"FOO=bar"},
		},
		{
			name:      "empty base with overrides",
			base:      nil,
			overrides: []string{"FOO=bar"},
			expected:  []string{"FOO=bar"},
		},
		{
			name:      "mixed override and new",
			base:      []string{"A=1", "B=2", "C=3"},
			overrides: []string{"B=override", "D=4"},
			expected:  []string{"A=1", "B=override", "C=3", "D=4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mergeEnv(tt.base, tt.overrides)
			assert.Equal(t, tt.expected, got)
		})
	}
}

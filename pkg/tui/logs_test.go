// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: []string{},
		},
		{
			name:     "single line no newline",
			input:    "hello",
			expected: []string{"hello"},
		},
		{
			name:     "trailing newline skipped",
			input:    "hello\nworld\n",
			expected: []string{"hello", "world"},
		},
		{
			name:     "multiple empty lines filtered",
			input:    "a\n\n\nb",
			expected: []string{"a", "b"},
		},
		{
			name:     "carriage return not stripped by splitLines",
			input:    "hello\r\nworld\r\n",
			expected: []string{"hello\r", "world\r"},
		},
		{
			name:     "only newlines",
			input:    "\n\n\n",
			expected: []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, splitLines(tc.input))
		})
	}
}

func TestDiffLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		prev     []string
		next     []string
		expected []string
	}{
		{
			name:     "prev empty returns all next",
			prev:     nil,
			next:     []string{"a", "b"},
			expected: []string{"a", "b"},
		},
		{
			name:     "next empty returns nil",
			prev:     []string{"a"},
			next:     nil,
			expected: nil,
		},
		{
			name:     "both empty",
			prev:     nil,
			next:     nil,
			expected: nil,
		},
		{
			name:     "full overlap no new lines",
			prev:     []string{"a", "b", "c"},
			next:     []string{"a", "b", "c"},
			expected: []string{},
		},
		{
			name:     "partial overlap returns new tail",
			prev:     []string{"a", "b"},
			next:     []string{"a", "b", "c", "d"},
			expected: []string{"c", "d"},
		},
		{
			name:     "no overlap returns all next",
			prev:     []string{"x", "y"},
			next:     []string{"a", "b", "c"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "duplicate last line resolved by suffix sequence match",
			prev:     []string{"a", "b"},
			next:     []string{"a", "b", "b", "c"},
			expected: []string{"b", "c"},
		},
		{
			name:     "single-line prev falls back to last-line match",
			prev:     []string{"b"},
			next:     []string{"b", "x", "b", "y"},
			expected: []string{"y"},
		},
		{
			name:     "suffix sequence anchors correctly with repeating lines",
			prev:     []string{"x", "x", "x"},
			next:     []string{"x", "x", "x", "x", "x", "new"},
			expected: []string{"new"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, diffLines(tc.prev, tc.next))
		})
	}
}

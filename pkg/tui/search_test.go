// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
)

func TestHighlightSubstring(t *testing.T) {
	t.Parallel()

	bg := lipgloss.Color("#ffff00")

	tests := []struct {
		name              string
		line              string
		query             string
		expectContains    []string
		expectSame        bool // if true, result should equal line exactly
		expectHighlighted bool // if true, output must differ from input and contain ANSI escapes
	}{
		{
			name:       "empty query returns original",
			line:       "hello world",
			query:      "",
			expectSame: true,
		},
		{
			name:           "no match returns line with all original segments",
			line:           "hello world",
			query:          "xyz",
			expectContains: []string{"hello world"},
		},
		{
			name:              "case insensitive match wraps with style",
			line:              "Hello World",
			query:             "hello",
			expectContains:    []string{"Hello", "World"},
			expectHighlighted: true,
		},
		{
			name:              "multiple matches all highlighted",
			line:              "foo bar foo baz foo",
			query:             "foo",
			expectContains:    []string{"foo", "bar", "baz"},
			expectHighlighted: true,
		},
		{
			name:              "match at end of line",
			line:              "prefix match",
			query:             "match",
			expectContains:    []string{"prefix", "match"},
			expectHighlighted: true,
		},
		{
			name:              "match at start of line",
			line:              "start of line",
			query:             "start",
			expectContains:    []string{"start", "of line"},
			expectHighlighted: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lowerQuery := ""
			if tc.query != "" {
				lq := make([]byte, len(tc.query))
				for i, c := range []byte(tc.query) {
					if c >= 'A' && c <= 'Z' {
						lq[i] = c + 32
					} else {
						lq[i] = c
					}
				}
				lowerQuery = string(lq)
			}
			result := highlightSubstring(tc.line, tc.query, lowerQuery, bg)

			if tc.expectSame {
				assert.Equal(t, tc.line, result)
				return
			}
			for _, substr := range tc.expectContains {
				assert.Contains(t, result, substr)
			}
			// When highlighting is expected, the output must differ from the
			// original line and contain ANSI escape sequences.
			if tc.expectHighlighted {
				assert.NotEqual(t, tc.line, result, "highlighting should modify the output")
				assert.Contains(t, result, "\x1b[", "highlighted output must contain ANSI escape sequences")
			}
		})
	}
}

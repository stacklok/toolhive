// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
)

func TestSanitizeRegistryName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "dots and slashes replaced",
			input:    "io.github.stacklok/fetch",
			expected: "io-github-stacklok-fetch",
		},
		{
			name:     "multiple consecutive dots",
			input:    "a..b",
			expected: "a--b",
		},
		{
			name:     "no special characters",
			input:    "simple-name",
			expected: "simple-name",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "mixed dots slashes and dashes",
			input:    "io.github/org/tool.v2",
			expected: "io-github-org-tool-v2",
		},
		{
			name:     "only dots",
			input:    "...",
			expected: "---",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, sanitizeRegistryName(tc.input))
		})
	}
}

func TestFilterRegistryItems(t *testing.T) {
	t.Parallel()

	items := []regtypes.ServerMetadata{
		&regtypes.RemoteServerMetadata{BaseServerMetadata: regtypes.BaseServerMetadata{Name: "fetch-tool", Description: "Fetches web pages"}},
		&regtypes.RemoteServerMetadata{BaseServerMetadata: regtypes.BaseServerMetadata{Name: "github-search", Description: "Search GitHub repos"}},
		&regtypes.RemoteServerMetadata{BaseServerMetadata: regtypes.BaseServerMetadata{Name: "postgres-db", Description: "PostgreSQL database connector"}},
	}

	tests := []struct {
		name          string
		query         string
		expectedCount int
		expectNames   []string
	}{
		{
			name:          "empty query returns all",
			query:         "",
			expectedCount: 3,
		},
		{
			name:          "match by name case-insensitive",
			query:         "FETCH",
			expectedCount: 1,
			expectNames:   []string{"fetch-tool"},
		},
		{
			name:          "match by description",
			query:         "github",
			expectedCount: 1,
			expectNames:   []string{"github-search"},
		},
		{
			name:          "no match returns empty",
			query:         "nonexistent",
			expectedCount: 0,
		},
		{
			name:          "partial match across name and description",
			query:         "post",
			expectedCount: 1,
			expectNames:   []string{"postgres-db"},
		},
		{
			name:          "query matches multiple items via description",
			query:         "search",
			expectedCount: 1,
			expectNames:   []string{"github-search"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := filterRegistryItems(items, tc.query)
			assert.Len(t, result, tc.expectedCount)
			if tc.expectNames != nil {
				for i, name := range tc.expectNames {
					assert.Equal(t, name, result[i].GetName())
				}
			}
		})
	}
}

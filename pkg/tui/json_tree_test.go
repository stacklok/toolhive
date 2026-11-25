// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseJSONTree(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		input      string
		expectNil  bool
		expectKind jsonNodeKind
	}{
		{
			name:      "empty string",
			input:     "",
			expectNil: true,
		},
		{
			name:      "scalar string",
			input:     `"hello"`,
			expectNil: true,
		},
		{
			name:      "scalar number",
			input:     `42`,
			expectNil: true,
		},
		{
			name:      "invalid JSON",
			input:     `{broken`,
			expectNil: true,
		},
		{
			name:       "valid object",
			input:      `{"key": "value"}`,
			expectKind: kindObject,
		},
		{
			name:       "valid array",
			input:      `[1, 2, 3]`,
			expectKind: kindArray,
		},
		{
			name:       "empty object",
			input:      `{}`,
			expectKind: kindObject,
		},
		{
			name:       "whitespace around valid object",
			input:      `  {"a": 1}  `,
			expectKind: kindObject,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := parseJSONTree(tc.input)
			if tc.expectNil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, tc.expectKind, result.kind)
			}
		})
	}
}

func TestFlattenVisible(t *testing.T) {
	t.Parallel()

	t.Run("flat object", func(t *testing.T) {
		t.Parallel()
		root := parseJSONTree(`{"a": 1, "b": "two"}`)
		require.NotNil(t, root)

		vis := flattenVisible(root)
		// root opening + 2 children + root closing = 4
		assert.Len(t, vis, 4)
		// First item is the root object opening
		assert.Equal(t, kindObject, vis[0].node.kind)
		assert.False(t, vis[0].closingBracket)
		// Last item is the closing bracket
		assert.True(t, vis[len(vis)-1].closingBracket)
	})

	t.Run("nested object", func(t *testing.T) {
		t.Parallel()
		root := parseJSONTree(`{"outer": {"inner": 1}}`)
		require.NotNil(t, root)

		vis := flattenVisible(root)
		// root{ + outer{ + inner + outer} + root} = 5
		assert.Len(t, vis, 5)
		// Check depths
		assert.Equal(t, 0, vis[0].depth) // root opening
		assert.Equal(t, 1, vis[1].depth) // outer opening
		assert.Equal(t, 2, vis[2].depth) // inner value
		assert.Equal(t, 1, vis[3].depth) // outer closing
		assert.Equal(t, 0, vis[4].depth) // root closing
	})

	t.Run("collapsed nodes skip children", func(t *testing.T) {
		t.Parallel()
		root := parseJSONTree(`{"a": {"b": 1}, "c": 2}`)
		require.NotNil(t, root)

		// Collapse the "a" child (first child of root)
		root.children[0].collapsed = true

		vis := flattenVisible(root)
		// root{ + collapsed-a + c + root} = 4 (no "b", no closing for "a")
		assert.Len(t, vis, 4)
	})
}

func TestToggleCollapse(t *testing.T) {
	t.Parallel()

	t.Run("toggle on object works", func(t *testing.T) {
		t.Parallel()
		root := parseJSONTree(`{"a": 1}`)
		require.NotNil(t, root)
		vis := flattenVisible(root)

		assert.False(t, root.collapsed)
		toggleCollapse(vis, 0) // toggle root object
		assert.True(t, root.collapsed)
		toggleCollapse(vis, 0) // toggle back
		assert.False(t, root.collapsed)
	})

	t.Run("toggle on scalar is noop", func(t *testing.T) {
		t.Parallel()
		root := parseJSONTree(`{"a": 1}`)
		require.NotNil(t, root)
		vis := flattenVisible(root)

		// vis[1] is the "a": 1 scalar child
		child := vis[1].node
		assert.Equal(t, kindNumber, child.kind)
		toggleCollapse(vis, 1) // should be noop
		assert.False(t, child.collapsed)
	})

	t.Run("out of bounds is safe", func(t *testing.T) {
		t.Parallel()
		root := parseJSONTree(`{"a": 1}`)
		require.NotNil(t, root)
		vis := flattenVisible(root)

		// These should not panic
		toggleCollapse(vis, -1)
		toggleCollapse(vis, len(vis))
		toggleCollapse(nil, 0)
	})
}

func TestNodeToJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "simple object roundtrip",
			input: `{"name":"test","value":42}`,
		},
		{
			name:  "array roundtrip",
			input: `[1,2,3]`,
		},
		{
			name:  "nested structure roundtrip",
			input: `{"items":[{"id":1},{"id":2}],"total":2}`,
		},
		{
			name:  "booleans and null",
			input: `{"active":true,"deleted":false,"meta":null}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := parseJSONTree(tc.input)
			require.NotNil(t, root)

			output := nodeToJSON(root)

			// Parse both to compare structurally (formatting may differ)
			var expected, actual any
			require.NoError(t, json.Unmarshal([]byte(tc.input), &expected))
			require.NoError(t, json.Unmarshal([]byte(output), &actual))
			assert.Equal(t, expected, actual)
		})
	}
}

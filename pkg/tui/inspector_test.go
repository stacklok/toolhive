// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestBuildRequiredSet(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		schema   map[string]any
		expected map[string]bool
	}{
		{
			name:     "missing required key",
			schema:   map[string]any{"properties": map[string]any{}},
			expected: map[string]bool{},
		},
		{
			name:     "empty required array",
			schema:   map[string]any{"required": []any{}},
			expected: map[string]bool{},
		},
		{
			name: "valid required strings",
			schema: map[string]any{
				"required": []any{"name", "url"},
			},
			expected: map[string]bool{"name": true, "url": true},
		},
		{
			name: "non-string elements skipped",
			schema: map[string]any{
				"required": []any{"name", 42, true, nil, "url"},
			},
			expected: map[string]bool{"name": true, "url": true},
		},
		{
			name:     "required is wrong type entirely",
			schema:   map[string]any{"required": "not-an-array"},
			expected: map[string]bool{},
		},
		{
			name:     "nil schema",
			schema:   nil,
			expected: map[string]bool{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, buildRequiredSet(tc.schema))
		})
	}
}

func TestInspFieldValues(t *testing.T) {
	t.Parallel()

	makeField := func(name, value string) formField {
		ti := textinput.New()
		ti.SetValue(value)
		return formField{input: ti, name: name}
	}

	tests := []struct {
		name     string
		fields   []formField
		expected map[string]any
	}{
		{
			name:     "empty fields",
			fields:   nil,
			expected: map[string]any{},
		},
		{
			name: "empty values skipped",
			fields: []formField{
				makeField("a", ""),
				makeField("b", "   "),
			},
			expected: map[string]any{},
		},
		{
			name: "whitespace trimmed",
			fields: []formField{
				makeField("url", "  https://example.com  "),
			},
			expected: map[string]any{"url": "https://example.com"},
		},
		{
			name: "multiple fields collected",
			fields: []formField{
				makeField("name", "test"),
				makeField("empty", ""),
				makeField("count", "42"),
			},
			expected: map[string]any{"name": "test", "count": "42"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, inspFieldValues(tc.fields))
		})
	}
}

func TestShellEscapeSingleQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no quotes", "hello", "hello"},
		{"single quote", "it's", `it'"'"'s`},
		{"multiple quotes", "a'b'c", `a'"'"'b'"'"'c`},
		{"empty string", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, shellEscapeSingleQuote(tc.input))
		})
	}
}

func TestBuildCurlStr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		workload *core.Workload
		toolName string
		args     map[string]any
		check    func(t *testing.T, result string)
	}{
		{
			name:     "nil workload returns empty",
			workload: nil,
			check:    func(t *testing.T, result string) { assert.Empty(t, result) },
		},
		{
			name:     "single quote in arg value is escaped",
			workload: &core.Workload{Name: "test", URL: "http://localhost:8080/sse", Port: 8080},
			toolName: "echo",
			args:     map[string]any{"msg": "it's dangerous"},
			check: func(t *testing.T, result string) {
				assert.NotContains(t, result, "'it's", "unescaped single quote in payload")
				assert.Contains(t, result, "curl -X POST")
			},
		},
		{
			name:     "single quote in URL is escaped",
			workload: &core.Workload{Name: "test", URL: "http://localhost:8080/path'inject", Port: 8080},
			toolName: "echo",
			args:     map[string]any{},
			check: func(t *testing.T, result string) {
				assert.NotContains(t, result, "'http://localhost:8080/path'inject'",
					"unescaped single quote in URL")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := buildCurlStr(tc.workload, tc.toolName, tc.args)
			tc.check(t, result)
		})
	}
}

func TestFormatInspResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		result   *vmcp.ToolCallResult
		expected string
	}{
		{
			name:     "nil result",
			result:   nil,
			expected: "",
		},
		{
			name: "single text content",
			result: &vmcp.ToolCallResult{
				Content: []vmcp.Content{
					{Type: "text", Text: "hello world"},
				},
			},
			expected: "hello world",
		},
		{
			name: "multiple text contents joined",
			result: &vmcp.ToolCallResult{
				Content: []vmcp.Content{
					{Type: "text", Text: "line1"},
					{Type: "text", Text: "line2"},
				},
			},
			expected: "line1\nline2",
		},
		{
			name: "non-text content serialized as JSON",
			result: &vmcp.ToolCallResult{
				Content: []vmcp.Content{
					{Type: "image", Data: "base64data", MimeType: "image/png"},
				},
			},
		},
		{
			name: "empty content falls back to full result JSON",
			result: &vmcp.ToolCallResult{
				Content: []vmcp.Content{},
				IsError: true,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatInspResult(tc.result)
			if tc.expected != "" {
				assert.Equal(t, tc.expected, got)
			} else if tc.result != nil {
				// For non-text and empty-content cases, just verify it returns non-empty valid output
				require.NotEmpty(t, got)
			}
		})
	}
}

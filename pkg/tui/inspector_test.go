// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/core"
)

func TestBuildRequiredSetFromSlice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		required []string
		expected map[string]bool
	}{
		{
			name:     "nil slice",
			required: nil,
			expected: map[string]bool{},
		},
		{
			name:     "empty slice",
			required: []string{},
			expected: map[string]bool{},
		},
		{
			name:     "valid required strings",
			required: []string{"name", "url"},
			expected: map[string]bool{"name": true, "url": true},
		},
		{
			name:     "single entry",
			required: []string{"id"},
			expected: map[string]bool{"id": true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, buildRequiredSetFromSlice(tc.required))
		})
	}
}

func TestInspFieldValues(t *testing.T) {
	t.Parallel()

	makeField := func(name, value, typeName string, required bool) formField {
		ti := textinput.New()
		ti.SetValue(value)
		return formField{input: ti, name: name, typeName: typeName, required: required}
	}

	tests := []struct {
		name         string
		fields       []formField
		expected     map[string]any
		expectErr    string
		expectErrIdx int
	}{
		{
			name:     "empty fields",
			fields:   nil,
			expected: map[string]any{},
		},
		{
			name: "empty optional values skipped",
			fields: []formField{
				makeField("a", "", "string", false),
				makeField("b", "   ", "string", false),
			},
			expected: map[string]any{},
		},
		{
			name: "whitespace trimmed",
			fields: []formField{
				makeField("url", "  https://example.com  ", "string", false),
			},
			expected: map[string]any{"url": "https://example.com"},
		},
		{
			name: "integer coerced",
			fields: []formField{
				makeField("count", "42", "integer", false),
			},
			expected: map[string]any{"count": int64(42)},
		},
		{
			name: "number coerced",
			fields: []formField{
				makeField("rate", "3.14", "number", false),
			},
			expected: map[string]any{"rate": 3.14},
		},
		{
			name: "boolean coerced",
			fields: []formField{
				makeField("draft", "true", "boolean", false),
			},
			expected: map[string]any{"draft": true},
		},
		{
			name: "array coerced",
			fields: []formField{
				makeField("tags", `["a","b"]`, "array", false),
			},
			expected: map[string]any{"tags": []any{"a", "b"}},
		},
		{
			name: "object coerced",
			fields: []formField{
				makeField("meta", `{"k":"v"}`, "object", false),
			},
			expected: map[string]any{"meta": map[string]any{"k": "v"}},
		},
		{
			name: "mixed types collected",
			fields: []formField{
				makeField("name", "test", "string", false),
				makeField("empty", "", "string", false),
				makeField("count", "42", "integer", false),
			},
			expected: map[string]any{"name": "test", "count": int64(42)},
		},
		{
			name: "required empty field errors",
			fields: []formField{
				makeField("title", "", "string", true),
			},
			expectErr:    `field "title" is required`,
			expectErrIdx: 0,
		},
		{
			name: "required error returns field index",
			fields: []formField{
				makeField("name", "ok", "string", false),
				makeField("title", "", "string", true),
			},
			expectErr:    `field "title" is required`,
			expectErrIdx: 1,
		},
		{
			name: "invalid integer errors",
			fields: []formField{
				makeField("count", "abc", "integer", false),
			},
			expectErr:    `field "count"`,
			expectErrIdx: 0,
		},
		{
			name: "invalid boolean errors",
			fields: []formField{
				makeField("flag", "maybe", "boolean", false),
			},
			expectErr:    `field "flag"`,
			expectErrIdx: 0,
		},
		{
			name: "invalid JSON array errors",
			fields: []formField{
				makeField("tags", "not json", "array", false),
			},
			expectErr:    `field "tags"`,
			expectErrIdx: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, errIdx, err := inspFieldValues(tc.fields)
			if tc.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectErr)
				assert.Equal(t, tc.expectErrIdx, errIdx)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestParseFieldValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		value    string
		typeName string
		expected any
		wantErr  bool
	}{
		{name: "string passthrough", value: "hello", typeName: "string", expected: "hello"},
		{name: "unknown type defaults to string", value: "hello", typeName: "custom", expected: "hello"},
		{name: "empty type defaults to string", value: "hello", typeName: "", expected: "hello"},
		{name: "valid integer", value: "42", typeName: "integer", expected: int64(42)},
		{name: "negative integer", value: "-7", typeName: "integer", expected: int64(-7)},
		{name: "invalid integer", value: "3.5", typeName: "integer", wantErr: true},
		{name: "non-numeric integer", value: "abc", typeName: "integer", wantErr: true},
		{name: "valid number", value: "3.14", typeName: "number", expected: 3.14},
		{name: "integer as number", value: "42", typeName: "number", expected: float64(42)},
		{name: "invalid number", value: "abc", typeName: "number", wantErr: true},
		{name: "boolean true", value: "true", typeName: "boolean", expected: true},
		{name: "boolean false", value: "false", typeName: "boolean", expected: false},
		{name: "boolean 1", value: "1", typeName: "boolean", expected: true},
		{name: "invalid boolean", value: "maybe", typeName: "boolean", wantErr: true},
		{name: "valid array", value: `[1,2,3]`, typeName: "array", expected: []any{float64(1), float64(2), float64(3)}},
		{name: "invalid array", value: "not json", typeName: "array", wantErr: true},
		{name: "valid object", value: `{"a":1}`, typeName: "object", expected: map[string]any{"a": float64(1)}},
		{name: "invalid object", value: "{bad}", typeName: "object", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseFieldValue(tc.value, tc.typeName)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, got)
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
			check: func(t *testing.T, result string) {
				t.Helper()
				assert.Empty(t, result)
			},
		},
		{
			name:     "single quote in arg value is escaped",
			workload: &core.Workload{Name: "test", URL: "http://localhost:8080/sse", Port: 8080},
			toolName: "echo",
			args:     map[string]any{"msg": "it's dangerous"},
			check: func(t *testing.T, result string) {
				t.Helper()
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
				t.Helper()
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
		result   *mcp.CallToolResult
		expected string
	}{
		{
			name:     "nil result",
			result:   nil,
			expected: "",
		},
		{
			name: "single text content",
			result: &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.TextContent{Type: "text", Text: "hello world"},
				},
			},
			expected: "hello world",
		},
		{
			name: "multiple text contents joined",
			result: &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.TextContent{Type: "text", Text: "line1"},
					mcp.TextContent{Type: "text", Text: "line2"},
				},
			},
			expected: "line1\nline2",
		},
		{
			name: "non-text content serialized as JSON",
			result: &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.ImageContent{Type: "image", Data: "base64data", MIMEType: "image/png"},
				},
			},
		},
		{
			name: "empty content falls back to full result JSON",
			result: &mcp.CallToolResult{
				Content: []mcp.Content{},
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

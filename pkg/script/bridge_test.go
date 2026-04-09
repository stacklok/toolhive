// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

type mockToolCaller struct {
	calls   []mockToolCall
	results map[string]*CallToolResult
	err     error
}

type mockToolCall struct {
	Name      string
	Arguments map[string]interface{}
}

func (m *mockToolCaller) CallTool(_ context.Context, toolName string, arguments map[string]interface{}) (*CallToolResult, error) {
	m.calls = append(m.calls, mockToolCall{Name: toolName, Arguments: arguments})
	if m.err != nil {
		return nil, m.err
	}
	if r, ok := m.results[toolName]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("tool %q not found", toolName)
}

func TestSanitizeToolName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "fetch_prs", "fetch_prs"},
		{"hyphens", "github-fetch-prs", "github_fetch_prs"},
		{"dots", "my.tool.name", "my_tool_name"},
		{"leading digit", "3d-render", "_3d_render"},
		{"special chars", "tool@v2!", "tool_v2_"},
		{"empty string", "", "_"},
		{"already clean", "measure_length", "measure_length"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sanitizeToolName(tt.input))
		})
	}
}

func TestParseToolResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  *CallToolResult
		want    interface{}
		wantErr string
	}{
		{
			name: "JSON object in text content",
			result: &CallToolResult{
				Content: []ContentItem{{Type: "text", Text: `{"id": 1, "name": "test"}`}},
			},
			want: map[string]interface{}{"id": float64(1), "name": "test"},
		},
		{
			name: "JSON array in text content",
			result: &CallToolResult{
				Content: []ContentItem{{Type: "text", Text: `[1, 2, 3]`}},
			},
			want: []interface{}{float64(1), float64(2), float64(3)},
		},
		{
			name: "JSON number in text content",
			result: &CallToolResult{
				Content: []ContentItem{{Type: "text", Text: `42`}},
			},
			want: float64(42),
		},
		{
			name: "plain string in text content",
			result: &CallToolResult{
				Content: []ContentItem{{Type: "text", Text: "hello world"}},
			},
			want: "hello world",
		},
		{
			name: "structured content preferred over text",
			result: &CallToolResult{
				Content:           []ContentItem{{Type: "text", Text: `"ignored"`}},
				StructuredContent: map[string]interface{}{"key": "from_structured"},
			},
			want: map[string]interface{}{"key": "from_structured"},
		},
		{
			name: "structured content result wrapper unwrapped",
			result: &CallToolResult{
				Content:           []ContentItem{{Type: "text", Text: "the text"}},
				StructuredContent: map[string]interface{}{"result": "the text"},
			},
			want: "the text",
		},
		{
			name: "isError returns error",
			result: &CallToolResult{
				Content: []ContentItem{{Type: "text", Text: "something went wrong"}},
				IsError: true,
			},
			wantErr: "something went wrong",
		},
		{
			name: "isError with empty content",
			result: &CallToolResult{
				IsError: true,
			},
			wantErr: "tool execution error",
		},
		{
			name:   "empty content returns nil",
			result: &CallToolResult{},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseToolResult(tt.result)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildGlobals_ToolCallFlow(t *testing.T) {
	t.Parallel()

	caller := &mockToolCaller{
		results: map[string]*CallToolResult{
			"fetch_prs": {
				Content: []ContentItem{{Type: "text", Text: `[{"id": 1, "title": "PR1"}]`}},
			},
		},
	}

	tools := []ToolInfo{{Name: "fetch_prs", Description: "Fetch pull requests"}}
	globals := BuildGlobals(context.Background(), tools, caller, nil)

	// Tool should be available as a global
	_, ok := globals["fetch_prs"]
	assert.True(t, ok, "fetch_prs should be in globals")

	// call_tool should always be present
	_, ok = globals["call_tool"]
	assert.True(t, ok, "call_tool should be in globals")
}

func TestBuildGlobals_DataArguments(t *testing.T) {
	t.Parallel()

	caller := &mockToolCaller{results: map[string]*CallToolResult{}}
	data := map[string]interface{}{
		"author":   "jerm-dro",
		"count":    float64(5),
		"tags":     []interface{}{"go", "starlark"},
		"metadata": map[string]interface{}{"key": "value"},
	}

	globals := BuildGlobals(context.Background(), nil, caller, data)

	// Verify data arguments are injected
	v, ok := globals["author"]
	require.True(t, ok)
	assert.Equal(t, starlark.String("jerm-dro"), v)

	v, ok = globals["count"]
	require.True(t, ok)
	assert.Equal(t, starlark.MakeInt(5), v)
}

func TestBuildGlobals_HyphenatedToolName(t *testing.T) {
	t.Parallel()

	caller := &mockToolCaller{
		results: map[string]*CallToolResult{
			"github-fetch-prs": {
				Content: []ContentItem{{Type: "text", Text: `"ok"`}},
			},
		},
	}

	tools := []ToolInfo{{Name: "github-fetch-prs", Description: "Fetch PRs"}}
	globals := BuildGlobals(context.Background(), tools, caller, nil)

	// Should be available as sanitized name
	_, ok := globals["github_fetch_prs"]
	assert.True(t, ok, "github_fetch_prs should be in globals")
}

func TestCallTool_ViaScript(t *testing.T) {
	t.Parallel()

	caller := &mockToolCaller{
		results: map[string]*CallToolResult{
			"fetch_data": {
				Content: []ContentItem{{Type: "text", Text: `{"value": 42}`}},
			},
		},
	}

	tools := []ToolInfo{{Name: "fetch_data", Description: "Fetch data"}}
	globals := BuildGlobals(context.Background(), tools, caller, nil)

	result, err := Execute(`return fetch_data(key="test")`, globals, 0)
	require.NoError(t, err)

	got := starlarkToGo(result.Value)
	assert.Equal(t, map[string]interface{}{"value": int64(42)}, got)

	// Verify the caller received correct arguments
	require.Len(t, caller.calls, 1)
	assert.Equal(t, "fetch_data", caller.calls[0].Name)
	assert.Equal(t, map[string]interface{}{"key": "test"}, caller.calls[0].Arguments)
}

func TestCallTool_GenericCallTool(t *testing.T) {
	t.Parallel()

	caller := &mockToolCaller{
		results: map[string]*CallToolResult{
			"github-fetch-prs": {
				Content: []ContentItem{{Type: "text", Text: `"result"`}},
			},
		},
	}

	tools := []ToolInfo{{Name: "github-fetch-prs", Description: "Fetch PRs"}}
	globals := BuildGlobals(context.Background(), tools, caller, nil)

	result, err := Execute(`return call_tool("github-fetch-prs", author="jerm")`, globals, 0)
	require.NoError(t, err)

	got := starlarkToGo(result.Value)
	assert.Equal(t, "result", got)

	require.Len(t, caller.calls, 1)
	assert.Equal(t, "github-fetch-prs", caller.calls[0].Name)
}

func TestCallTool_ErrorPropagation(t *testing.T) {
	t.Parallel()

	caller := &mockToolCaller{
		results: map[string]*CallToolResult{
			"failing_tool": {
				Content: []ContentItem{{Type: "text", Text: "access denied"}},
				IsError: true,
			},
		},
	}

	tools := []ToolInfo{{Name: "failing_tool", Description: "A tool that fails"}}
	globals := BuildGlobals(context.Background(), tools, caller, nil)

	_, err := Execute(`return failing_tool()`, globals, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
}

func TestStarlarkToGoRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input interface{}
	}{
		{"nil", nil},
		{"bool true", true},
		{"bool false", false},
		{"int", int64(42)},
		{"float", 3.14},
		{"string", "hello"},
		{"empty list", []interface{}{}},
		{"int list", []interface{}{int64(1), int64(2), int64(3)}},
		{"map", map[string]interface{}{"a": int64(1), "b": "two"}},
		{"nested", map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{"id": int64(1)},
			},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sv, err := goToStarlark(tt.input)
			require.NoError(t, err)
			got := starlarkToGo(sv)
			assert.Equal(t, tt.input, got)
		})
	}
}

func TestGoToStarlark_JSONFloat64AsInt(t *testing.T) {
	t.Parallel()

	// JSON numbers come as float64 — integers should be converted to Starlark int
	sv, err := goToStarlark(float64(42))
	require.NoError(t, err)
	_, ok := sv.(starlark.Int)
	assert.True(t, ok, "float64(42) should become starlark.Int, got %T", sv)
}

func TestParallel_ViaScript(t *testing.T) {
	t.Parallel()

	caller := &mockToolCaller{
		results: map[string]*CallToolResult{
			"tool_a": {Content: []ContentItem{{Type: "text", Text: `"result_a"`}}},
			"tool_b": {Content: []ContentItem{{Type: "text", Text: `"result_b"`}}},
			"tool_c": {Content: []ContentItem{{Type: "text", Text: `"result_c"`}}},
		},
	}

	tools := []ToolInfo{
		{Name: "tool_a", Description: "A"},
		{Name: "tool_b", Description: "B"},
		{Name: "tool_c", Description: "C"},
	}
	globals := BuildGlobals(context.Background(), tools, caller, nil)

	script := `
results = parallel([
    lambda: tool_a(),
    lambda: tool_b(),
    lambda: tool_c(),
])
return results
`
	result, err := Execute(script, globals, 0)
	require.NoError(t, err)

	got := starlarkToGo(result.Value)
	gotList, ok := got.([]interface{})
	require.True(t, ok, "expected list, got %T", got)
	require.Len(t, gotList, 3)
	assert.Equal(t, "result_a", gotList[0])
	assert.Equal(t, "result_b", gotList[1])
	assert.Equal(t, "result_c", gotList[2])

	// All three tools should have been called
	require.Len(t, caller.calls, 3)
}

func TestParallel_ErrorPropagation(t *testing.T) {
	t.Parallel()

	caller := &mockToolCaller{
		results: map[string]*CallToolResult{
			"good_tool": {Content: []ContentItem{{Type: "text", Text: `"ok"`}}},
		},
	}

	tools := []ToolInfo{
		{Name: "good_tool", Description: "works"},
		{Name: "bad_tool", Description: "missing"},
	}
	globals := BuildGlobals(context.Background(), tools, caller, nil)

	script := `
results = parallel([
    lambda: good_tool(),
    lambda: bad_tool(),
])
return results
`
	_, err := Execute(script, globals, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parallel: task 1 failed")
}

func TestParallel_EmptyList(t *testing.T) {
	t.Parallel()

	globals := BuildGlobals(context.Background(), nil, &mockToolCaller{results: map[string]*CallToolResult{}}, nil)

	result, err := Execute(`return parallel([])`, globals, 0)
	require.NoError(t, err)

	got := starlarkToGo(result.Value)
	assert.Equal(t, []interface{}{}, got)
}

func TestGenerateToolDescription(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{Name: "measure_length", Description: "Measure the length of text"},
		{Name: "random_number", Description: "Generate a random number"},
	}

	desc := GenerateToolDescription(tools)
	assert.Contains(t, desc, "measure_length")
	assert.Contains(t, desc, "random_number")
	assert.Contains(t, desc, "Measure the length of text")
	assert.Contains(t, desc, "call_tool")
}

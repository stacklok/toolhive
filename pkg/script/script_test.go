// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

func TestExecutor(t *testing.T) {
	t.Parallel()

	echoTool := Tool{
		Name:        "echo",
		Description: "Returns arguments as JSON",
		Call: func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
			b, _ := json.Marshal(args)
			return mcp.NewToolResultText(string(b)), nil
		},
	}

	statusTool := Tool{
		Name:        "check-status",
		Description: "Check service status",
		Call: func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
			svc, _ := args["service"].(string)
			status := "healthy"
			if svc == "db" {
				status = "degraded"
			}
			return mcp.NewToolResultText(fmt.Sprintf(`{"service": "%s", "status": "%s"}`, svc, status)), nil
		},
	}

	fetchTool := Tool{
		Name:        "fetch",
		Description: "Fetch data by ID",
		Call: func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(fmt.Sprintf(`"result-%v"`, args["id"])), nil
		},
	}

	hyphenatedTool := Tool{
		Name:        "my-hyphenated-tool",
		Description: "A tool with hyphens",
		Call: func(_ context.Context, _ map[string]interface{}) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(`"called"`), nil
		},
	}

	tests := []struct {
		name   string
		tools  []Tool
		config *Config
		script string
		data   map[string]interface{}
		check  func(t *testing.T, result *mcp.CallToolResult)
		errMsg string
	}{
		{
			name:  "multi-tool script with loops and conditionals",
			tools: []Tool{statusTool},
			script: `
services = ["api", "db", "cache"]
degraded = []
for svc in services:
    status = check_status(service=svc)
    if status["status"] != "healthy":
        degraded.append(status["service"])
return degraded
`,
			check: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				var parsed []interface{}
				require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &parsed))
				require.Equal(t, []interface{}{"db"}, parsed)
			},
		},
		{
			name:  "JSON text automatically parsed into structured data",
			tools: []Tool{echoTool},
			script: `
result = echo(name="alice", count=42)
return {"name": result["name"], "count": result["count"]}
`,
			check: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				var parsed map[string]interface{}
				require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &parsed))
				require.Equal(t, "alice", parsed["name"])
				require.Equal(t, float64(42), parsed["count"])
			},
		},
		{
			name:  "parallel fan-out returns ordered results",
			tools: []Tool{fetchTool},
			script: `
results = parallel([
    lambda: fetch(id=1),
    lambda: fetch(id=2),
    lambda: fetch(id=3),
])
return results
`,
			check: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				var parsed []interface{}
				require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &parsed))
				require.Len(t, parsed, 3)
			},
		},
		{
			name:  "data arguments injected as top-level variables",
			tools: []Tool{echoTool},
			script: `
return echo(name=user_name)
`,
			data: map[string]interface{}{"user_name": "Alice"},
			check: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				require.Contains(t, extractText(t, result), "Alice")
			},
		},
		{
			name:   "call_tool dispatches by original name",
			tools:  []Tool{hyphenatedTool},
			script: `return call_tool("my-hyphenated-tool")`,
			check: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				require.Contains(t, extractText(t, result), "called")
			},
		},
		{
			name:   "step limit exceeded",
			config: &Config{StepLimit: 100},
			script: `
x = 0
for i in range(10000):
    x = x + 1
return x
`,
			errMsg: "too many steps",
		},
		{
			name: "script logs appear as second content item",
			script: `
print("log line 1")
print("log line 2")
return "done"
`,
			check: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				require.Len(t, result.Content, 2, "should have result text + logs")
				logsContent, ok := mcp.AsTextContent(result.Content[1])
				require.True(t, ok)
				require.Contains(t, logsContent.Text, "log line 1")
				require.Contains(t, logsContent.Text, "log line 2")
			},
		},
		{
			name:   "data argument shadowing builtin rejected",
			tools:  []Tool{echoTool},
			script: `return 1`,
			data:   map[string]interface{}{"call_tool": "shadow"},
			errMsg: "conflicts with",
		},
		{
			name:   "data argument shadowing tool rejected",
			tools:  []Tool{echoTool},
			script: `return 1`,
			data:   map[string]interface{}{"echo": "shadow"},
			errMsg: "conflicts with",
		},
		{
			name:   "invalid data argument type rejected",
			script: `return 1`,
			data:   map[string]interface{}{"bad": struct{}{}},
			errMsg: `data argument "bad"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			exec := New(tt.tools, tt.config)
			result, err := exec.Execute(context.Background(), tt.script, tt.data)

			if tt.errMsg != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
				return
			}

			require.NoError(t, err)
			require.False(t, result.IsError)
			tt.check(t, result)
		})
	}
}

func TestExecutor_ToolDescription(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		{Name: "tool-a", Description: "Does A"},
		{Name: "tool-b", Description: "Does B"},
	}

	exec := New(tools, nil)
	desc := exec.ToolDescription()

	require.Contains(t, desc, "tool-a")
	require.Contains(t, desc, "tool-b")
	require.Contains(t, desc, "parallel")
}

// extractText gets the first text content from a CallToolResult.
func extractText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, result.Content)
	tc, ok := mcp.AsTextContent(result.Content[0])
	require.True(t, ok, "expected text content")
	return tc.Text
}

func TestExecute_StepLimitErrorIsDetectable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		script        string
		wantStepLimit bool
	}{
		{
			name: "top-level loop",
			script: `
x = 0
for i in range(10000):
    x = x + 1
return x
`,
			wantStepLimit: true,
		},
		{
			name: "loop inside parallel()",
			script: `
def spin():
    x = 0
    for i in range(10000):
        x = x + 1
    return x
return parallel([spin, spin])
`,
			wantStepLimit: true,
		},
		{
			name:          "runtime error is not a step limit",
			script:        `return 1 // 0`,
			wantStepLimit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			exec := New(nil, &Config{StepLimit: 200})
			_, err := exec.Execute(context.Background(), tt.script, nil)
			require.Error(t, err)
			assert.Equal(t, tt.wantStepLimit, errors.Is(err, ErrStepLimitExceeded), "error: %v", err)
			if tt.wantStepLimit {
				assert.Contains(t, err.Error(), "too many steps", "message is unchanged")
			}
		})
	}
}

func TestExecute_OnStats(t *testing.T) {
	t.Parallel()

	tool := Tool{Name: "ping", Call: func(context.Context, map[string]interface{}) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`"pong"`), nil
	}}

	tests := []struct {
		name              string
		script            string
		data              map[string]interface{}
		wantErr           bool
		wantSteps         bool
		wantParallelCalls int
		wantParallelTasks int
	}{
		{name: "no parallel", script: `return ping()`, wantSteps: true},
		{
			name: "nested parallel() calls are all counted",
			script: `
def inner():
    return parallel([ping, ping])
return parallel([inner, ping, ping])`,
			wantSteps:         true,
			wantParallelCalls: 2,
			wantParallelTasks: 5,
		},
		{name: "runtime error still reports", script: `return 1 // 0`, wantErr: true, wantSteps: true},
		{
			name:    "data conflict reports zero",
			script:  `return 1`,
			data:    map[string]interface{}{"ping": 1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []Stats
			exec := New([]Tool{tool}, &Config{OnStats: func(s Stats) { got = append(got, s) }})
			_, err := exec.Execute(context.Background(), tt.script, tt.data)
			assert.Equal(t, tt.wantErr, err != nil, "error: %v", err)

			require.Len(t, got, 1, "OnStats runs exactly once per Execute")
			assert.Equal(t, tt.wantSteps, got[0].Steps > 0, "steps: %d", got[0].Steps)
			assert.Equal(t, tt.wantParallelCalls, got[0].ParallelCalls)
			assert.Equal(t, tt.wantParallelTasks, got[0].ParallelTasks)
		})
	}
}

func TestExecute_OnStatsCountsChildSteps(t *testing.T) {
	t.Parallel()

	steps := func(src string) uint64 {
		var s Stats
		_, err := New(nil, &Config{OnStats: func(got Stats) { s = got }}).Execute(context.Background(), src, nil)
		require.NoError(t, err)
		return s.Steps
	}
	body := "def work():\n    x = 0\n    for i in range(50):\n        x = x + i\n    return x\n"
	once := steps(body + "return work()")
	viaParallel := steps(body + "return parallel([work, work, work])")
	assert.Greater(t, viaParallel, 2*once, "steps run in parallel() children are added to the total")
}

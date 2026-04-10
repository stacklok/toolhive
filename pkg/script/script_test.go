// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestExecutor_MultiToolScript(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		{
			Name:        "get-user",
			Description: "Get user by ID",
			Call: func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText(fmt.Sprintf(`{"name": "user-%v"}`, args["id"])), nil
			},
		},
		{
			Name:        "get-posts",
			Description: "Get posts by user",
			Call: func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText(fmt.Sprintf(`[{"title": "post by %v"}]`, args["user"])), nil
			},
		},
	}

	exec := New(tools, nil)
	result, err := exec.Execute(context.Background(), `
user = get_user(id=1)
posts = get_posts(user=user["name"])
return {"user": user, "posts": posts}
`, nil)

	require.NoError(t, err)
	require.False(t, result.IsError)

	text := extractText(t, result)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &parsed))
	require.Contains(t, parsed, "user")
	require.Contains(t, parsed, "posts")
}

func TestExecutor_LoopsAndConditionals(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		{
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
		},
	}

	exec := New(tools, nil)
	result, err := exec.Execute(context.Background(), `
services = ["api", "db", "cache"]
degraded = []
for svc in services:
    status = check_status(service=svc)
    if status["status"] != "healthy":
        degraded.append(status["service"])
return degraded
`, nil)

	require.NoError(t, err)

	text := extractText(t, result)
	var parsed []interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &parsed))
	require.Equal(t, []interface{}{"db"}, parsed)
}

func TestExecutor_Parallel(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		{
			Name:        "fetch",
			Description: "Fetch data",
			Call: func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText(fmt.Sprintf(`"result-%v"`, args["id"])), nil
			},
		},
	}

	exec := New(tools, nil)
	result, err := exec.Execute(context.Background(), `
results = parallel([
    lambda: fetch(id=1),
    lambda: fetch(id=2),
    lambda: fetch(id=3),
])
return results
`, nil)

	require.NoError(t, err)

	text := extractText(t, result)
	var parsed []interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &parsed))
	require.Len(t, parsed, 3)
}

func TestExecutor_DataArguments(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		{
			Name:        "greet",
			Description: "Greet someone",
			Call: func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText(fmt.Sprintf(`"Hello, %v!"`, args["name"])), nil
			},
		},
	}

	exec := New(tools, nil)
	result, err := exec.Execute(context.Background(), `
return greet(name=user_name)
`, map[string]interface{}{"user_name": "Alice"})

	require.NoError(t, err)

	text := extractText(t, result)
	require.Contains(t, text, "Alice")
}

func TestExecutor_CallToolByOriginalName(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		{
			Name:        "my-hyphenated-tool",
			Description: "A tool with hyphens",
			Call: func(_ context.Context, _ map[string]interface{}) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText(`"called"`), nil
			},
		},
	}

	exec := New(tools, nil)
	result, err := exec.Execute(context.Background(), `
return call_tool("my-hyphenated-tool")
`, nil)

	require.NoError(t, err)
	text := extractText(t, result)
	require.Contains(t, text, "called")
}

func TestExecutor_StepLimitExceeded(t *testing.T) {
	t.Parallel()

	exec := New(nil, &Config{StepLimit: 100})
	_, err := exec.Execute(context.Background(), `
x = 0
for i in range(10000):
    x = x + 1
return x
`, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "too many steps")
}

func TestExecutor_ScriptLogs(t *testing.T) {
	t.Parallel()

	exec := New(nil, nil)
	result, err := exec.Execute(context.Background(), `
print("log line 1")
print("log line 2")
return "done"
`, nil)

	require.NoError(t, err)
	require.Len(t, result.Content, 2, "should have result text + logs")

	logsContent, ok := mcp.AsTextContent(result.Content[1])
	require.True(t, ok)
	require.Contains(t, logsContent.Text, "log line 1")
	require.Contains(t, logsContent.Text, "log line 2")
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

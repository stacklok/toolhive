// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAcceptance_ScriptOrchestratesMultipleTools is the motivating example from the RFC:
// fetch PRs by an author, then filter to only those where a specific reviewer commented.
func TestAcceptance_ScriptOrchestratesMultipleTools(t *testing.T) {
	t.Parallel()

	// Mock data: two PRs by jerm-dro, only PR 1 has a comment from yrobla
	prsData := []map[string]interface{}{
		{"id": 1, "title": "Add script middleware", "author": "jerm-dro"},
		{"id": 2, "title": "Fix linting", "author": "jerm-dro"},
	}
	commentsData := map[float64][]map[string]interface{}{
		1: {
			{"author": "yrobla", "body": "lgtm"},
			{"author": "someone-else", "body": "nice"},
		},
		2: {
			{"author": "someone-else", "body": "needs work"},
		},
	}

	backend := mockMCPBackend(map[string]func(map[string]interface{}) string{
		"fetch_prs": func(args map[string]interface{}) string {
			// Filter by author if provided
			author, _ := args["author"].(string)
			var filtered []map[string]interface{}
			for _, pr := range prsData {
				if author == "" || pr["author"] == author {
					filtered = append(filtered, pr)
				}
			}
			b, _ := json.Marshal(filtered)
			return string(b)
		},
		"fetch_comments": func(args map[string]interface{}) string {
			prID, _ := args["pr_id"].(float64)
			comments := commentsData[prID]
			if comments == nil {
				comments = []map[string]interface{}{}
			}
			b, _ := json.Marshal(comments)
			return string(b)
		},
	})

	middleware := NewMiddleware()(backend)

	// The motivating script: find PRs where a specific reviewer commented
	script := `
prs = fetch_prs(author=author_name)
output = []
for pr in prs:
    comments = fetch_comments(pr_id=pr["id"])
    for c in comments:
        if c["author"] == reviewer:
            output.append(pr)
            break
return output
`

	t.Run("script filters PRs by reviewer comments", func(t *testing.T) {
		t.Parallel()

		rec := sendJSONRPC(t, middleware, "tools/call", map[string]interface{}{
			"name": ExecuteToolScriptName,
			"arguments": map[string]interface{}{
				"script": script,
				"data": map[string]interface{}{
					"author_name": "jerm-dro",
					"reviewer":    "yrobla",
				},
			},
		})

		require.Equal(t, http.StatusOK, rec.Code)

		var resp jsonRPCResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Result, "expected a result")
		require.Nil(t, resp.Error, "expected no error")

		var resultMap map[string]interface{}
		require.NoError(t, json.Unmarshal(*resp.Result, &resultMap))

		content := resultMap["content"].([]interface{})
		require.NotEmpty(t, content)

		textItem := content[0].(map[string]interface{})
		require.Equal(t, "text", textItem["type"])

		var prs []map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(textItem["text"].(string)), &prs))

		// Only PR 1 should be in the result (the one where yrobla commented)
		require.Len(t, prs, 1)
		assert.Equal(t, float64(1), prs[0]["id"])
		assert.Equal(t, "Add script middleware", prs[0]["title"])
	})

	t.Run("tools/list includes execute_tool_script with dynamic description", func(t *testing.T) {
		t.Parallel()

		rec := sendJSONRPC(t, middleware, "tools/list", map[string]interface{}{})

		require.Equal(t, http.StatusOK, rec.Code)

		var resp jsonRPCResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

		var resultMap map[string]interface{}
		require.NoError(t, json.Unmarshal(*resp.Result, &resultMap))

		toolsRaw := resultMap["tools"].([]interface{})

		names := make([]string, 0, len(toolsRaw))
		var scriptToolDesc string
		for _, item := range toolsRaw {
			tm := item.(map[string]interface{})
			name := tm["name"].(string)
			names = append(names, name)
			if name == ExecuteToolScriptName {
				scriptToolDesc = tm["description"].(string)
			}
		}

		assert.Contains(t, names, "fetch_prs")
		assert.Contains(t, names, "fetch_comments")
		assert.Contains(t, names, ExecuteToolScriptName)

		// Dynamic description should mention the available tools
		assert.Contains(t, scriptToolDesc, "fetch_prs")
		assert.Contains(t, scriptToolDesc, "fetch_comments")
	})

	t.Run("empty script returns null", func(t *testing.T) {
		t.Parallel()

		rec := sendJSONRPC(t, middleware, "tools/call", map[string]interface{}{
			"name": ExecuteToolScriptName,
			"arguments": map[string]interface{}{
				"script": "x = 1",
			},
		})

		require.Equal(t, http.StatusOK, rec.Code)

		var resp jsonRPCResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Result)

		var resultMap map[string]interface{}
		require.NoError(t, json.Unmarshal(*resp.Result, &resultMap))

		content := resultMap["content"].([]interface{})
		textItem := content[0].(map[string]interface{})
		assert.Equal(t, "null", textItem["text"])
	})

	t.Run("step limit exceeded returns error", func(t *testing.T) {
		t.Parallel()

		rec := sendJSONRPC(t, middleware, "tools/call", map[string]interface{}{
			"name": ExecuteToolScriptName,
			"arguments": map[string]interface{}{
				"script": "while True:\n    pass",
			},
		})

		require.Equal(t, http.StatusOK, rec.Code)

		var resp jsonRPCResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Error, "should return error for infinite loop")
	})

	t.Run("data arguments accessible as script globals", func(t *testing.T) {
		t.Parallel()

		rec := sendJSONRPC(t, middleware, "tools/call", map[string]interface{}{
			"name": ExecuteToolScriptName,
			"arguments": map[string]interface{}{
				"script": "return {\"name\": user_name, \"count\": item_count}",
				"data": map[string]interface{}{
					"user_name":  "test-user",
					"item_count": 42,
				},
			},
		})

		require.Equal(t, http.StatusOK, rec.Code)

		var resp jsonRPCResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Result)

		var resultMap map[string]interface{}
		require.NoError(t, json.Unmarshal(*resp.Result, &resultMap))

		content := resultMap["content"].([]interface{})
		textItem := content[0].(map[string]interface{})

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(textItem["text"].(string)), &result))
		assert.Equal(t, "test-user", result["name"])
		assert.Equal(t, float64(42), result["count"])
	})
}

// TestAcceptance_RawHTTPFlow verifies the complete HTTP flow with a real httptest.Server.
func TestAcceptance_RawHTTPFlow(t *testing.T) {
	t.Parallel()

	backend := mockMCPBackend(map[string]func(map[string]interface{}) string{
		"add": func(args map[string]interface{}) string {
			a, _ := args["a"].(float64)
			b, _ := args["b"].(float64)
			result, _ := json.Marshal(a + b)
			return string(result)
		},
	})

	handler := NewMiddleware()(backend)
	server := httptest.NewServer(handler)
	defer server.Close()

	// Send a real HTTP request
	body := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": ExecuteToolScriptName,
			"arguments": map[string]interface{}{
				"script": "return add(a=x, b=y)",
				"data":   map[string]interface{}{"x": 10, "y": 32},
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	resp, err := http.Post(server.URL, "application/json", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	defer func() {
		_ = resp.Body.Close()
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var rpcResp jsonRPCResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rpcResp))
	require.NotNil(t, rpcResp.Result)

	var resultMap map[string]interface{}
	require.NoError(t, json.Unmarshal(*rpcResp.Result, &resultMap))

	content := resultMap["content"].([]interface{})
	textItem := content[0].(map[string]interface{})
	assert.Equal(t, "42", textItem["text"])
}

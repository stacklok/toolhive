// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

func TestMiddleware(t *testing.T) {
	t.Parallel()

	backend := mockBackend(map[string]mockTool{
		"echo": {fn: func(args map[string]interface{}) string {
			b, _ := json.Marshal(args)
			return string(b)
		}},
		"greet": {fn: func(args map[string]interface{}) string {
			return fmt.Sprintf(`"Hello, %v!"`, args["name"])
		}},
	})

	tests := []struct {
		name    string
		method  string
		params  map[string]interface{}
		headers map[string]string
		check   func(t *testing.T, body map[string]interface{})
		wantErr string
	}{
		{
			name:   "tools/list includes execute_tool_script",
			method: "tools/list",
			params: map[string]interface{}{},
			check: func(t *testing.T, body map[string]interface{}) {
				t.Helper()
				result := body["result"].(map[string]interface{})
				tools := result["tools"].([]interface{})
				names := extractToolNames(tools)
				require.Contains(t, names, ExecuteToolScriptName)
				require.Contains(t, names, "echo")
				require.Contains(t, names, "greet")
			},
		},
		{
			name:   "execute script calls multiple tools",
			method: "tools/call",
			params: map[string]interface{}{
				"name": ExecuteToolScriptName,
				"arguments": map[string]interface{}{
					"script": `
result = echo(msg="hello")
greeting = greet(name="Alice")
return {"echo": result, "greeting": greeting}
`,
				},
			},
			check: func(t *testing.T, body map[string]interface{}) {
				t.Helper()
				result := body["result"].(map[string]interface{})
				content := result["content"].([]interface{})
				require.NotEmpty(t, content)
				text := content[0].(map[string]interface{})["text"].(string)
				var parsed map[string]interface{}
				require.NoError(t, json.Unmarshal([]byte(text), &parsed))
				require.Contains(t, parsed, "echo")
				require.Contains(t, parsed, "greeting")
			},
		},
		{
			name:   "non-script tools/call passes through",
			method: "tools/call",
			params: map[string]interface{}{
				"name":      "echo",
				"arguments": map[string]interface{}{"msg": "direct"},
			},
			check: func(t *testing.T, body map[string]interface{}) {
				t.Helper()
				result := body["result"].(map[string]interface{})
				content := result["content"].([]interface{})
				text := content[0].(map[string]interface{})["text"].(string)
				require.Contains(t, text, "direct")
			},
		},
		{
			name:   "missing script argument returns error",
			method: "tools/call",
			params: map[string]interface{}{
				"name":      ExecuteToolScriptName,
				"arguments": map[string]interface{}{},
			},
			wantErr: "missing required argument: script",
		},
		{
			name:   "non-string script argument returns error",
			method: "tools/call",
			params: map[string]interface{}{
				"name":      ExecuteToolScriptName,
				"arguments": map[string]interface{}{"script": 42},
			},
			wantErr: "script argument must be a string",
		},
		{
			name:   "non-object data argument returns error",
			method: "tools/call",
			params: map[string]interface{}{
				"name":      ExecuteToolScriptName,
				"arguments": map[string]interface{}{"script": "return 1", "data": "not-an-object"},
			},
			wantErr: "data argument must be an object",
		},
		{
			name:   "script execution error returns generic message",
			method: "tools/call",
			params: map[string]interface{}{
				"name":      ExecuteToolScriptName,
				"arguments": map[string]interface{}{"script": "return ]["},
			},
			wantErr: "script execution failed",
		},
		{
			name:   "other methods pass through",
			method: "initialize",
			params: map[string]interface{}{},
			check: func(t *testing.T, body map[string]interface{}) {
				t.Helper()
				require.Contains(t, body, "result")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Script middleware composes its own parsing — no external parser needed
			handler := NewMiddleware(nil)(backend)

			body := sendJSONRPCWithHeaders(t, handler, tt.method, tt.params, tt.headers)

			if tt.wantErr != "" {
				errObj, ok := body["error"].(map[string]interface{})
				require.True(t, ok, "expected JSON-RPC error, got: %v", body)
				require.Contains(t, errObj["message"], tt.wantErr)
				return
			}

			require.NotContains(t, body, "error", "unexpected JSON-RPC error: %v", body["error"])
			tt.check(t, body)
		})
	}
}

func TestMiddleware_ConfigToggle(t *testing.T) {
	t.Parallel()

	backend := mockBackend(map[string]mockTool{
		"echo": {fn: func(_ map[string]interface{}) string { return `"ok"` }},
	})

	t.Run("disabled by default", func(t *testing.T) {
		t.Parallel()
		// No script middleware — just backend (with parsing for the test)
		handler := mcpparser.ParsingMiddleware(backend)
		body := sendJSONRPC(t, handler, "tools/list", map[string]interface{}{})
		result := body["result"].(map[string]interface{})
		tools := result["tools"].([]interface{})
		names := extractToolNames(tools)
		require.NotContains(t, names, ExecuteToolScriptName)
	})

	t.Run("enabled with middleware", func(t *testing.T) {
		t.Parallel()
		handler := NewMiddleware(nil)(backend)
		body := sendJSONRPC(t, handler, "tools/list", map[string]interface{}{})
		result := body["result"].(map[string]interface{})
		tools := result["tools"].([]interface{})
		names := extractToolNames(tools)
		require.Contains(t, names, ExecuteToolScriptName)
	})
}

func TestMiddleware_InnerToolCallParsedRequest(t *testing.T) {
	t.Parallel()

	var capturedParsed *mcpparser.ParsedMCPRequest
	backend := mockBackend(map[string]mockTool{
		"target-tool": {
			fn: func(args map[string]interface{}) string {
				b, _ := json.Marshal(args)
				return string(b)
			},
			onCall: func(r *http.Request, _ string) {
				capturedParsed = mcpparser.GetParsedMCPRequest(r.Context())
			},
		},
	})

	handler := NewMiddleware(nil)(backend)

	body := sendJSONRPC(t, handler, "tools/call", map[string]interface{}{
		"name": ExecuteToolScriptName,
		"arguments": map[string]interface{}{
			"script": `return target_tool(key="value", count=42)`,
		},
	})

	require.NotContains(t, body, "error", "unexpected error: %v", body["error"])
	require.NotNil(t, capturedParsed, "inner tool call should have a ParsedMCPRequest in context")
	require.Equal(t, "tools/call", capturedParsed.Method)
	require.Equal(t, "target-tool", capturedParsed.ResourceID)
	require.Equal(t, "value", capturedParsed.Arguments["key"])
}

func TestMiddleware_InnerToolCallPreservesAuthHeaders(t *testing.T) {
	t.Parallel()

	var capturedAuthHeader string
	backend := mockBackend(map[string]mockTool{
		"secured-tool": {
			fn: func(_ map[string]interface{}) string { return `"ok"` },
			onCall: func(r *http.Request, _ string) {
				capturedAuthHeader = r.Header.Get("Authorization")
			},
		},
	})

	handler := NewMiddleware(nil)(backend)

	body := sendJSONRPCWithHeaders(t, handler, "tools/call", map[string]interface{}{
		"name": ExecuteToolScriptName,
		"arguments": map[string]interface{}{
			"script": `return secured_tool()`,
		},
	}, map[string]string{
		"Authorization": "Bearer test-token-123",
	})

	require.NotContains(t, body, "error", "unexpected error: %v", body["error"])
	require.Equal(t, "Bearer test-token-123", capturedAuthHeader,
		"inner tool call should preserve Authorization header from original request")
}

// mockTool defines a tool's behavior and optional request inspection hook.
type mockTool struct {
	fn     func(map[string]interface{}) string
	onCall func(r *http.Request, toolName string) // nil = no-op
}

// mockBackend creates an HTTP handler that simulates an MCP backend.
// Each tool has a response function and an optional onCall hook for
// inspecting the request during dispatch.
func mockBackend(tools map[string]mockTool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var req struct {
			JSONRPC string           `json:"jsonrpc"`
			ID      json.RawMessage  `json:"id"`
			Method  string           `json:"method"`
			Params  *json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch req.Method {
		case "tools/list":
			toolList := make([]map[string]interface{}, 0, len(tools))
			for name := range tools {
				toolList = append(toolList, map[string]interface{}{
					"name":        name,
					"description": "Mock tool: " + name,
					"inputSchema": map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					},
				})
			}
			result, _ := json.Marshal(map[string]interface{}{"tools": toolList})
			raw := json.RawMessage(result)
			//nolint:errcheck,gosec // test helper
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": &raw,
			})

		case "tools/call":
			var params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			if req.Params != nil {
				_ = json.Unmarshal(*req.Params, &params)
			}
			tool, ok := tools[params.Name]
			if !ok {
				writeTestError(w, req.ID, -32601, fmt.Sprintf("unknown tool: %s", params.Name))
				return
			}
			if tool.onCall != nil {
				tool.onCall(r, params.Name)
			}
			text := tool.fn(params.Arguments)
			result, _ := json.Marshal(map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": text},
				},
			})
			raw := json.RawMessage(result)
			//nolint:errcheck,gosec // test helper
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": &raw,
			})

		default:
			result, _ := json.Marshal(map[string]interface{}{"status": "ok"})
			raw := json.RawMessage(result)
			//nolint:errcheck,gosec // test helper
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": &raw,
			})
		}
	})
}

func writeTestError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	errObj, _ := json.Marshal(map[string]interface{}{"code": code, "message": msg})
	raw := json.RawMessage(errObj)
	//nolint:errcheck,gosec // test helper
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "error": &raw,
	})
}

func sendJSONRPC(t *testing.T, handler http.Handler, method string, params interface{}) map[string]interface{} {
	t.Helper()
	return sendJSONRPCWithHeaders(t, handler, method, params, nil)
}

func sendJSONRPCWithHeaders(
	t *testing.T, handler http.Handler, method string,
	params interface{}, headers map[string]string,
) map[string]interface{} {
	t.Helper()
	body := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	bodyBytes, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/mcp", strings.NewReader(string(bodyBytes)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func extractToolNames(tools []interface{}) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		tm, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		if name, ok := tm["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

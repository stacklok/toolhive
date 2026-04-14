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

	// Mock backend that serves tools/list and tools/call
	backend := mockBackend(map[string]func(map[string]interface{}) string{
		"echo": func(args map[string]interface{}) string {
			b, _ := json.Marshal(args)
			return string(b)
		},
		"greet": func(args map[string]interface{}) string {
			return fmt.Sprintf(`"Hello, %v!"`, args["name"])
		},
	})

	tests := []struct {
		name    string
		method  string
		params  map[string]interface{}
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
			name:   "script syntax error returns error",
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
				// Should see the passthrough response from mock backend
				require.Contains(t, body, "result")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Build middleware chain: parser → script → backend
			handler := mcpparser.ParsingMiddleware(
				NewMiddleware(nil)(backend),
			)

			body := sendJSONRPC(t, handler, tt.method, tt.params)

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

	backend := mockBackend(map[string]func(map[string]interface{}) string{
		"echo": func(_ map[string]interface{}) string { return `"ok"` },
	})

	t.Run("disabled by default", func(t *testing.T) {
		t.Parallel()
		// No script middleware — just parser + backend
		handler := mcpparser.ParsingMiddleware(backend)
		body := sendJSONRPC(t, handler, "tools/list", map[string]interface{}{})
		result := body["result"].(map[string]interface{})
		tools := result["tools"].([]interface{})
		names := extractToolNames(tools)
		require.NotContains(t, names, ExecuteToolScriptName)
	})

	t.Run("enabled with middleware", func(t *testing.T) {
		t.Parallel()
		handler := mcpparser.ParsingMiddleware(
			NewMiddleware(nil)(backend),
		)
		body := sendJSONRPC(t, handler, "tools/list", map[string]interface{}{})
		result := body["result"].(map[string]interface{})
		tools := result["tools"].([]interface{})
		names := extractToolNames(tools)
		require.Contains(t, names, ExecuteToolScriptName)
	})
}

// mockBackend creates an HTTP handler that simulates an MCP backend.
func mockBackend(tools map[string]func(map[string]interface{}) string) http.Handler {
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
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  &raw,
			}
			//nolint:errcheck,gosec // test helper
			json.NewEncoder(w).Encode(resp)

		case "tools/call":
			var params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			if req.Params != nil {
				_ = json.Unmarshal(*req.Params, &params)
			}
			fn, ok := tools[params.Name]
			if !ok {
				writeTestError(w, req.ID, -32601, fmt.Sprintf("unknown tool: %s", params.Name))
				return
			}
			text := fn(params.Arguments)
			result, _ := json.Marshal(map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": text},
				},
			})
			raw := json.RawMessage(result)
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  &raw,
			}
			//nolint:errcheck,gosec // test helper
			json.NewEncoder(w).Encode(resp)

		default:
			result, _ := json.Marshal(map[string]interface{}{"status": "ok"})
			raw := json.RawMessage(result)
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  &raw,
			}
			//nolint:errcheck,gosec // test helper
			json.NewEncoder(w).Encode(resp)
		}
	})
}

func writeTestError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	errObj, _ := json.Marshal(map[string]interface{}{"code": code, "message": msg})
	raw := json.RawMessage(errObj)
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   &raw,
	}
	//nolint:errcheck,gosec // test helper
	json.NewEncoder(w).Encode(resp)
}

func sendJSONRPC(t *testing.T, handler http.Handler, method string, params interface{}) map[string]interface{} {
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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMCPBackend simulates an MCP server that handles tools/list and tools/call.
func mockMCPBackend(tools map[string]func(map[string]interface{}) string) http.Handler {
	toolsList := make([]map[string]interface{}, 0, len(tools))
	for name := range tools {
		toolsList = append(toolsList, map[string]interface{}{
			"name":        name,
			"description": "Test tool: " + name,
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch req.Method {
		case "tools/list":
			result := map[string]interface{}{"tools": toolsList}
			resultBytes, _ := json.Marshal(result)
			raw := json.RawMessage(resultBytes)
			resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: &raw}
			//nolint:errcheck
			json.NewEncoder(w).Encode(resp)

		case "tools/call":
			var params toolCallParams
			if req.Params != nil {
				//nolint:errcheck
				json.Unmarshal(*req.Params, &params)
			}
			handler, ok := tools[params.Name]
			if !ok {
				writeJSONRPCError(w, req.ID, -32601, "tool not found: "+params.Name)
				return
			}
			text := handler(params.Arguments)
			result := map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": text},
				},
			}
			resultBytes, _ := json.Marshal(result)
			raw := json.RawMessage(resultBytes)
			resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: &raw}
			//nolint:errcheck
			json.NewEncoder(w).Encode(resp)

		default:
			writeJSONRPCError(w, req.ID, -32601, "unknown method")
		}
	})
}

func sendJSONRPC(t *testing.T, handler http.Handler, method string, params interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	bodyBytes, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestMiddleware_NonScriptPassthrough(t *testing.T) {
	t.Parallel()

	var backendCalled bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalled = true
		w.WriteHeader(http.StatusOK)
	})

	middleware := NewMiddleware()(backend)
	rec := sendJSONRPC(t, middleware, "tools/call", map[string]interface{}{
		"name": "some_other_tool",
	})

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, backendCalled, "backend should be called for non-script tools")
}

func TestMiddleware_GETPassthrough(t *testing.T) {
	t.Parallel()

	var backendCalled bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalled = true
		w.WriteHeader(http.StatusOK)
	})

	middleware := NewMiddleware()(backend)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	middleware.ServeHTTP(rec, req)

	assert.True(t, backendCalled, "GET should pass through")
}

func TestMiddleware_ToolsListInjection(t *testing.T) {
	t.Parallel()

	backend := mockMCPBackend(map[string]func(map[string]interface{}) string{
		"measure_length": func(_ map[string]interface{}) string { return `5` },
	})

	middleware := NewMiddleware()(backend)
	rec := sendJSONRPC(t, middleware, "tools/list", map[string]interface{}{})

	require.Equal(t, http.StatusOK, rec.Code)

	var resp jsonRPCResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Result)

	var resultMap map[string]interface{}
	require.NoError(t, json.Unmarshal(*resp.Result, &resultMap))

	toolsRaw, ok := resultMap["tools"].([]interface{})
	require.True(t, ok)

	// Should have original tool + execute_tool_script
	require.Len(t, toolsRaw, 2)

	names := make([]string, 0, len(toolsRaw))
	for _, item := range toolsRaw {
		tm := item.(map[string]interface{})
		names = append(names, tm["name"].(string))
	}
	assert.Contains(t, names, "measure_length")
	assert.Contains(t, names, ExecuteToolScriptName)

	// Check dynamic description mentions measure_length
	for _, item := range toolsRaw {
		tm := item.(map[string]interface{})
		if tm["name"] == ExecuteToolScriptName {
			desc := tm["description"].(string)
			assert.Contains(t, desc, "measure_length")
		}
	}
}

func TestMiddleware_ScriptExecution(t *testing.T) {
	t.Parallel()

	backend := mockMCPBackend(map[string]func(map[string]interface{}) string{
		"get_value": func(_ map[string]interface{}) string { return `42` },
	})

	middleware := NewMiddleware()(backend)
	rec := sendJSONRPC(t, middleware, "tools/call", map[string]interface{}{
		"name": ExecuteToolScriptName,
		"arguments": map[string]interface{}{
			"script": `return get_value()`,
		},
	})

	require.Equal(t, http.StatusOK, rec.Code)

	var resp jsonRPCResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Result)
	require.Nil(t, resp.Error)

	var resultMap map[string]interface{}
	require.NoError(t, json.Unmarshal(*resp.Result, &resultMap))

	content, ok := resultMap["content"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, content)

	firstItem := content[0].(map[string]interface{})
	assert.Equal(t, "text", firstItem["type"])
	assert.Equal(t, "42", firstItem["text"])
}

func TestMiddleware_ScriptWithDataArgs(t *testing.T) {
	t.Parallel()

	backend := mockMCPBackend(map[string]func(map[string]interface{}) string{
		"echo": func(args map[string]interface{}) string {
			msg, _ := args["msg"].(string)
			return `"` + msg + `"`
		},
	})

	middleware := NewMiddleware()(backend)
	rec := sendJSONRPC(t, middleware, "tools/call", map[string]interface{}{
		"name": ExecuteToolScriptName,
		"arguments": map[string]interface{}{
			"script": `return echo(msg=greeting)`,
			"data":   map[string]interface{}{"greeting": "hello"},
		},
	})

	require.Equal(t, http.StatusOK, rec.Code)

	var resp jsonRPCResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Result)

	var resultMap map[string]interface{}
	require.NoError(t, json.Unmarshal(*resp.Result, &resultMap))

	content := resultMap["content"].([]interface{})
	firstItem := content[0].(map[string]interface{})
	assert.Equal(t, `"hello"`, firstItem["text"])
}

func TestMiddleware_ScriptError(t *testing.T) {
	t.Parallel()

	backend := mockMCPBackend(map[string]func(map[string]interface{}) string{})

	middleware := NewMiddleware()(backend)
	rec := sendJSONRPC(t, middleware, "tools/call", map[string]interface{}{
		"name": ExecuteToolScriptName,
		"arguments": map[string]interface{}{
			"script": `return !!!`,
		},
	})

	require.Equal(t, http.StatusOK, rec.Code)

	var resp jsonRPCResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error, "should return JSON-RPC error for bad script")
}

func TestMiddleware_MissingScript(t *testing.T) {
	t.Parallel()

	backend := mockMCPBackend(map[string]func(map[string]interface{}) string{})

	middleware := NewMiddleware()(backend)
	rec := sendJSONRPC(t, middleware, "tools/call", map[string]interface{}{
		"name":      ExecuteToolScriptName,
		"arguments": map[string]interface{}{},
	})

	require.Equal(t, http.StatusOK, rec.Code)

	var resp jsonRPCResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error, "should error when script argument is missing")
}

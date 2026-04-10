// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// ExecuteToolScriptName is the name of the virtual tool exposed by this middleware.
	ExecuteToolScriptName = "execute_tool_script"
	// MiddlewareType is the middleware type identifier for registration.
	MiddlewareType = "script"
)

// Middleware implements the types.Middleware interface.
type Middleware struct {
	middleware types.MiddlewareFunction
}

// Handler returns the middleware function.
func (s *Middleware) Handler() types.MiddlewareFunction {
	return s.middleware
}

// Close is a no-op for the script middleware.
func (*Middleware) Close() error {
	return nil
}

// CreateMiddleware is the factory function for registering the script middleware.
func CreateMiddleware(_ *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	mw := &Middleware{middleware: NewMiddleware()}
	runner.AddMiddleware(MiddlewareType, mw)
	return nil
}

// NewMiddleware returns a middleware function that intercepts execute_tool_script
// calls and injects the virtual tool into tools/list responses.
func NewMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}

			contentType := r.Header.Get("Content-Type")
			if !strings.HasPrefix(contentType, "application/json") {
				next.ServeHTTP(w, r)
				return
			}

			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

			var req jsonRPCRequest
			if err := json.Unmarshal(bodyBytes, &req); err != nil {
				next.ServeHTTP(w, r)
				return
			}

			switch {
			case req.Method == "tools/call" && isScriptToolCall(&req):
				handleScriptExecution(w, r, next, &req)
			case req.Method == "tools/list":
				handleToolsListInjection(w, r, next)
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

type jsonRPCRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Method  string           `json:"method"`
	Params  *json.RawMessage `json:"params,omitempty"`
}

type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

func isScriptToolCall(req *jsonRPCRequest) bool {
	if req.Params == nil {
		return false
	}
	var params toolCallParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		return false
	}
	return params.Name == ExecuteToolScriptName
}

func handleScriptExecution(w http.ResponseWriter, r *http.Request, next http.Handler, req *jsonRPCRequest) {
	var params toolCallParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, -32602, "invalid params")
		return
	}

	scriptRaw, ok := params.Arguments["script"]
	if !ok {
		writeJSONRPCError(w, req.ID, -32602, "missing required argument: script")
		return
	}
	script, ok := scriptRaw.(string)
	if !ok {
		writeJSONRPCError(w, req.ID, -32602, "script argument must be a string")
		return
	}

	var data map[string]interface{}
	if dataRaw, ok := params.Arguments["data"]; ok {
		data, _ = dataRaw.(map[string]interface{})
	}

	// Fetch authorized tool list
	tools, err := fetchToolList(r, next)
	if err != nil {
		slog.Error("failed to fetch tool list for script execution", "error", err)
		writeJSONRPCError(w, req.ID, -32000, "failed to fetch available tools")
		return
	}

	// Build caller and globals
	caller := &innerToolCaller{next: next, origReq: r}
	globals := BuildGlobals(r.Context(), tools, caller, data)

	// Execute script
	result, err := Execute(script, globals, 0)
	if err != nil {
		writeJSONRPCError(w, req.ID, -32000, err.Error())
		return
	}

	// Convert result to JSON
	resultJSON, err := ResultToJSON(result.Value)
	if err != nil {
		writeJSONRPCError(w, req.ID, -32000, fmt.Sprintf("failed to serialize result: %v", err))
		return
	}

	writeJSONRPCResult(w, req.ID, resultJSON, result.Logs)
}

func handleToolsListInjection(w http.ResponseWriter, r *http.Request, next http.Handler) {
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, r)

	// Copy status and headers
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}

	body := rec.Body.Bytes()

	var resp jsonRPCResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		w.WriteHeader(rec.Code)
		//nolint:errcheck,gosec // best-effort write
		w.Write(body)
		return
	}

	if resp.Result == nil {
		w.WriteHeader(rec.Code)
		//nolint:errcheck,gosec // best-effort write
		w.Write(body)
		return
	}

	var resultMap map[string]interface{}
	if err := json.Unmarshal(*resp.Result, &resultMap); err != nil {
		w.WriteHeader(rec.Code)
		//nolint:errcheck,gosec // best-effort write
		w.Write(body)
		return
	}

	toolsRaw, ok := resultMap["tools"]
	if !ok {
		w.WriteHeader(rec.Code)
		//nolint:errcheck,gosec // best-effort write
		w.Write(body)
		return
	}

	toolsSlice, ok := toolsRaw.([]interface{})
	if !ok {
		w.WriteHeader(rec.Code)
		//nolint:errcheck,gosec // best-effort write
		w.Write(body)
		return
	}

	// Extract tool info for dynamic description
	var toolInfos []ToolInfo
	for _, t := range toolsSlice {
		tm, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := tm["name"].(string)
		desc, _ := tm["description"].(string)
		if name != "" {
			toolInfos = append(toolInfos, ToolInfo{Name: name, Description: desc})
		}
	}

	// Append the virtual tool
	scriptTool := buildScriptToolDefinition(toolInfos)
	toolsSlice = append(toolsSlice, scriptTool)
	resultMap["tools"] = toolsSlice

	resultBytes, err := json.Marshal(resultMap)
	if err != nil {
		w.WriteHeader(rec.Code)
		//nolint:errcheck,gosec // best-effort write
		w.Write(body)
		return
	}

	resp.Result = (*json.RawMessage)(&resultBytes)
	modified, err := json.Marshal(resp)
	if err != nil {
		w.WriteHeader(rec.Code)
		//nolint:errcheck,gosec // best-effort write
		w.Write(body)
		return
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(modified)))
	w.WriteHeader(rec.Code)
	//nolint:errcheck,gosec // best-effort write
	w.Write(modified)
}

func buildScriptToolDefinition(tools []ToolInfo) map[string]interface{} {
	return map[string]interface{}{
		"name":        ExecuteToolScriptName,
		"description": GenerateToolDescription(tools),
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"script": map[string]interface{}{
					"type":        "string",
					"description": "Starlark script body. Use 'return' to produce output.",
				},
				"data": map[string]interface{}{
					"type":                 "object",
					"description":          "Named data arguments injected as top-level Starlark variables",
					"additionalProperties": true,
				},
			},
			"required": []string{"script"},
		},
	}
}

// fetchToolList sends a synthetic tools/list request through the middleware chain.
func fetchToolList(origReq *http.Request, next http.Handler) ([]ToolInfo, error) {
	listBody := `{"jsonrpc":"2.0","id":0,"method":"tools/list","params":{}}`

	ctx := context.WithValue(origReq.Context(), mcp.MCPRequestContextKey, nil)
	innerReq, err := http.NewRequestWithContext(ctx, http.MethodPost, origReq.URL.String(), strings.NewReader(listBody))
	if err != nil {
		return nil, err
	}
	innerReq.Header = origReq.Header.Clone()
	innerReq.Header.Set("Content-Type", "application/json")
	innerReq.ContentLength = int64(len(listBody))

	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, innerReq)

	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("tools/list returned status %d", rec.Code)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse tools/list response: %w", err)
	}

	if resp.Result == nil {
		return nil, fmt.Errorf("tools/list response has no result")
	}

	var resultMap map[string]interface{}
	if err := json.Unmarshal(*resp.Result, &resultMap); err != nil {
		return nil, fmt.Errorf("failed to parse tools/list result: %w", err)
	}

	toolsRaw, ok := resultMap["tools"]
	if !ok {
		return nil, nil
	}

	toolsSlice, ok := toolsRaw.([]interface{})
	if !ok {
		return nil, nil
	}

	var tools []ToolInfo
	for _, t := range toolsSlice {
		tm, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := tm["name"].(string)
		desc, _ := tm["description"].(string)
		if name != "" {
			tools = append(tools, ToolInfo{Name: name, Description: desc})
		}
	}

	return tools, nil
}

type jsonRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *json.RawMessage `json:"error,omitempty"`
}

func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	//nolint:errcheck,gosec // best-effort write
	json.NewEncoder(w).Encode(resp)
}

func writeJSONRPCResult(w http.ResponseWriter, id json.RawMessage, resultJSON string, logs []string) {
	content := []map[string]interface{}{
		{"type": "text", "text": resultJSON},
	}
	if len(logs) > 0 {
		content = append(content, map[string]interface{}{
			"type": "text",
			"text": "Script logs:\n" + strings.Join(logs, "\n"),
		})
	}

	result := map[string]interface{}{
		"content": content,
	}
	resultBytes, _ := json.Marshal(result)
	raw := json.RawMessage(resultBytes)

	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  &raw,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	//nolint:errcheck,gosec // best-effort write
	json.NewEncoder(w).Encode(resp)
}

// innerToolCaller calls tools through the inner middleware chain.
type innerToolCaller struct {
	next    http.Handler
	origReq *http.Request
}

func (c *innerToolCaller) CallTool(
	ctx context.Context, toolName string, arguments map[string]interface{},
) (*CallToolResult, error) {
	params := map[string]interface{}{
		"name":      toolName,
		"arguments": arguments,
	}
	body := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  params,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tool call: %w", err)
	}

	// Clear parsed MCP request so the parser re-parses
	innerCtx := context.WithValue(ctx, mcp.MCPRequestContextKey, nil)
	innerReq, err := http.NewRequestWithContext(innerCtx, http.MethodPost, c.origReq.URL.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	innerReq.Header = c.origReq.Header.Clone()
	innerReq.Header.Set("Content-Type", "application/json")
	innerReq.ContentLength = int64(len(bodyBytes))

	rec := httptest.NewRecorder()
	c.next.ServeHTTP(rec, innerReq)

	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("tool %q returned HTTP %d", toolName, rec.Code)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse tool response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("tool %q returned JSON-RPC error: %s", toolName, string(*resp.Error))
	}

	if resp.Result == nil {
		return &CallToolResult{}, nil
	}

	var resultMap map[string]interface{}
	if err := json.Unmarshal(*resp.Result, &resultMap); err != nil {
		return nil, fmt.Errorf("failed to parse tool result: %w", err)
	}

	result := &CallToolResult{}

	// Extract isError
	if isErr, ok := resultMap["isError"].(bool); ok {
		result.IsError = isErr
	}

	// Extract structured content
	if sc, ok := resultMap["structuredContent"].(map[string]interface{}); ok {
		result.StructuredContent = sc
	}

	// Extract content array
	if contentRaw, ok := resultMap["content"].([]interface{}); ok {
		for _, item := range contentRaw {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			ci := ContentItem{}
			ci.Type, _ = itemMap["type"].(string)
			ci.Text, _ = itemMap["text"].(string)
			result.Content = append(result.Content, ci)
		}
	}

	return result, nil
}

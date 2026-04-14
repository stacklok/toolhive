// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// NewMiddleware returns an HTTP middleware function that intercepts
// execute_tool_script tool calls and injects the virtual tool into
// tools/list responses. The config controls execution parameters
// (step limit, parallel concurrency).
//
// The middleware uses the ParsedMCPRequest from context (set by the
// MCP parsing middleware earlier in the chain) to inspect requests
// without re-reading the body.
func NewMiddleware(cfg *Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parsed := mcpparser.GetParsedMCPRequest(r.Context())
			if parsed == nil || !parsed.IsRequest {
				next.ServeHTTP(w, r)
				return
			}

			switch {
			case parsed.Method == "tools/call" && parsed.ResourceID == ExecuteToolScriptName:
				handleScriptExecution(w, r, next, parsed, cfg)
			case parsed.Method == "tools/list":
				handleToolsListInjection(w, r, next, cfg)
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

// handleScriptExecution intercepts an execute_tool_script call,
// constructs an Executor with tool bindings that route through the
// middleware chain, and returns the script result.
func handleScriptExecution(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	parsed *mcpparser.ParsedMCPRequest, cfg *Config,
) {
	scriptRaw, ok := parsed.Arguments["script"]
	if !ok {
		writeJSONRPCError(w, parsed.ID, -32602, "missing required argument: script")
		return
	}
	scriptStr, ok := scriptRaw.(string)
	if !ok {
		writeJSONRPCError(w, parsed.ID, -32602, "script argument must be a string")
		return
	}

	var data map[string]interface{}
	if dataRaw, exists := parsed.Arguments["data"]; exists {
		data, _ = dataRaw.(map[string]interface{})
	}

	// Fetch the authorized tool list to build executor bindings
	tools, err := fetchToolList(r, next)
	if err != nil {
		slog.Error("failed to fetch tool list for script execution", "error", err)
		writeJSONRPCError(w, parsed.ID, -32000, "failed to fetch available tools")
		return
	}

	// Build script.Tool wrappers that dispatch through the middleware chain
	scriptTools := buildScriptTools(r, next, tools)

	// Create and run executor
	exec := New(scriptTools, cfg)
	result, err := exec.Execute(r.Context(), scriptStr, data)
	if err != nil {
		writeJSONRPCError(w, parsed.ID, -32000, err.Error())
		return
	}

	writeCallToolResult(w, parsed.ID, result)
}

// handleToolsListInjection captures the tools/list response and appends
// the execute_tool_script virtual tool definition.
func handleToolsListInjection(
	w http.ResponseWriter, r *http.Request, next http.Handler, cfg *Config,
) {
	capture := newResponseCapture()
	next.ServeHTTP(capture, r)

	body := capture.body.Bytes()

	var resp jsonRPCResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		writePassthrough(w, capture, body)
		return
	}

	if resp.Result == nil {
		writePassthrough(w, capture, body)
		return
	}

	var resultMap map[string]interface{}
	if err := json.Unmarshal(*resp.Result, &resultMap); err != nil {
		writePassthrough(w, capture, body)
		return
	}

	toolsRaw, ok := resultMap["tools"]
	if !ok {
		writePassthrough(w, capture, body)
		return
	}

	toolsSlice, ok := toolsRaw.([]interface{})
	if !ok {
		writePassthrough(w, capture, body)
		return
	}

	// Extract tool info for dynamic description
	var toolInfos []Tool
	for _, t := range toolsSlice {
		tm, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := tm["name"].(string)
		desc, _ := tm["description"].(string)
		if name != "" {
			toolInfos = append(toolInfos, Tool{Name: name, Description: desc})
		}
	}

	// Build and append the virtual tool using a temporary executor for description
	exec := New(toolInfos, cfg)
	scriptTool := buildScriptToolDefinition(exec.ToolDescription())
	toolsSlice = append(toolsSlice, scriptTool)
	resultMap["tools"] = toolsSlice

	resultBytes, err := json.Marshal(resultMap)
	if err != nil {
		writePassthrough(w, capture, body)
		return
	}

	resp.Result = (*json.RawMessage)(&resultBytes)
	modified, err := json.Marshal(resp)
	if err != nil {
		writePassthrough(w, capture, body)
		return
	}

	copyHeaders(w, capture)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(modified)))
	w.WriteHeader(capture.status)
	//nolint:errcheck,gosec // best-effort write
	w.Write(modified)
}

// buildScriptTools creates script.Tool wrappers for each discovered tool.
// Each tool's Call closure dispatches through the middleware chain via next.
func buildScriptTools(origReq *http.Request, next http.Handler, tools []toolInfo) []Tool {
	scriptTools := make([]Tool, len(tools))
	for i, t := range tools {
		toolName := t.name
		scriptTools[i] = Tool{
			Name:        t.name,
			Description: t.description,
			Call: func(ctx context.Context, arguments map[string]interface{}) (*mcp.CallToolResult, error) {
				return dispatchToolCall(ctx, origReq, next, toolName, arguments)
			},
		}
	}
	return scriptTools
}

// dispatchToolCall sends a synthetic tools/call request through the middleware
// chain and parses the response into a CallToolResult.
func dispatchToolCall(
	ctx context.Context, origReq *http.Request, next http.Handler,
	toolName string, arguments map[string]interface{},
) (*mcp.CallToolResult, error) {
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

	// Clear parsed MCP request so the parser re-parses the synthetic request
	innerCtx := context.WithValue(ctx, mcpparser.MCPRequestContextKey, nil)
	innerReq, err := http.NewRequestWithContext(innerCtx, http.MethodPost, origReq.URL.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	innerReq.Header = origReq.Header.Clone()
	innerReq.Header.Set("Content-Type", "application/json")
	innerReq.ContentLength = int64(len(bodyBytes))

	capture := newResponseCapture()
	next.ServeHTTP(capture, innerReq)

	if capture.status != http.StatusOK {
		return nil, fmt.Errorf("tool %q returned HTTP %d", toolName, capture.status)
	}

	return parseToolCallResponse(capture.body.Bytes(), toolName)
}

// fetchToolList sends a synthetic tools/list request through the chain.
func fetchToolList(origReq *http.Request, next http.Handler) ([]toolInfo, error) {
	listBody := `{"jsonrpc":"2.0","id":0,"method":"tools/list","params":{}}`

	ctx := context.WithValue(origReq.Context(), mcpparser.MCPRequestContextKey, nil)
	innerReq, err := http.NewRequestWithContext(ctx, http.MethodPost, origReq.URL.String(), strings.NewReader(listBody))
	if err != nil {
		return nil, err
	}
	innerReq.Header = origReq.Header.Clone()
	innerReq.Header.Set("Content-Type", "application/json")
	innerReq.ContentLength = int64(len(listBody))

	capture := newResponseCapture()
	next.ServeHTTP(capture, innerReq)

	if capture.status != http.StatusOK {
		return nil, fmt.Errorf("tools/list returned status %d", capture.status)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(capture.body.Bytes(), &resp); err != nil {
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

	var tools []toolInfo
	for _, t := range toolsSlice {
		tm, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := tm["name"].(string)
		desc, _ := tm["description"].(string)
		if name != "" {
			tools = append(tools, toolInfo{name: name, description: desc})
		}
	}

	return tools, nil
}

// toolInfo holds tool metadata extracted from a tools/list response.
type toolInfo struct {
	name        string
	description string
}

// parseToolCallResponse extracts a CallToolResult from a JSON-RPC response.
func parseToolCallResponse(body []byte, toolName string) (*mcp.CallToolResult, error) {
	var resp jsonRPCResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse tool response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("tool %q returned JSON-RPC error: %s", toolName, string(*resp.Error))
	}

	if resp.Result == nil {
		return &mcp.CallToolResult{}, nil
	}

	result, err := mcp.ParseCallToolResult(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tool result: %w", err)
	}

	return result, nil
}

// buildScriptToolDefinition creates the MCP tool definition for the virtual tool.
func buildScriptToolDefinition(description string) map[string]interface{} {
	return map[string]interface{}{
		"name":        ExecuteToolScriptName,
		"description": description,
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

// JSON-RPC helpers

type jsonRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *json.RawMessage `json:"error,omitempty"`
}

func writeJSONRPCError(w http.ResponseWriter, id interface{}, code int, message string) {
	idBytes, _ := json.Marshal(id)
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(idBytes),
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

func writeCallToolResult(w http.ResponseWriter, id interface{}, result *mcp.CallToolResult) {
	resultBytes, _ := json.Marshal(result)
	raw := json.RawMessage(resultBytes)
	idBytes, _ := json.Marshal(id)

	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      idBytes,
		Result:  &raw,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	//nolint:errcheck,gosec // best-effort write
	json.NewEncoder(w).Encode(resp)
}

func writePassthrough(w http.ResponseWriter, capture *responseCapture, body []byte) {
	copyHeaders(w, capture)
	w.WriteHeader(capture.status)
	//nolint:errcheck,gosec // best-effort write
	w.Write(body)
}

func copyHeaders(w http.ResponseWriter, capture *responseCapture) {
	for k, v := range capture.header {
		w.Header()[k] = v
	}
}

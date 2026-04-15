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

const (
	methodToolsCall = "tools/call"
	methodToolsList = "tools/list"
)

// NewMiddleware returns an HTTP middleware function that intercepts
// execute_tool_script tool calls and injects the virtual tool into
// tools/list responses. The config controls execution parameters
// (step limit, parallel concurrency).
//
// The middleware composes its own MCP parsing middleware for both
// inbound request detection and inner tool dispatch, so it does not
// depend on any upstream parser in the middleware chain.
//
// Inner tool calls dispatched from scripts flow through next (which
// includes authz, discovery, etc.) but NOT through this middleware,
// so recursive execute_tool_script calls are naturally prevented.
func NewMiddleware(cfg *Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// Wrap next with parsing so synthetic inner requests (tools/call,
		// tools/list) get a fresh ParsedMCPRequest from their body.
		parsingNext := mcpparser.ParsingMiddleware(next)

		scriptHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parsed := mcpparser.GetParsedMCPRequest(r.Context())
			if parsed == nil || !parsed.IsRequest {
				next.ServeHTTP(w, r)
				return
			}

			switch {
			case parsed.Method == methodToolsCall && parsed.ResourceID == ExecuteToolScriptName:
				handleScriptExecution(w, r, parsingNext, parsed, cfg)
			case parsed.Method == methodToolsList:
				handleToolsListInjection(w, r, next, cfg)
			default:
				next.ServeHTTP(w, r)
			}
		})

		// Wrap the script handler with parsing so this middleware has a
		// ParsedMCPRequest available regardless of upstream composition.
		return mcpparser.ParsingMiddleware(scriptHandler)
	}
}

// handleScriptExecution intercepts an execute_tool_script call,
// constructs an Executor with tool bindings that route through the
// middleware chain, and returns the script result.
func handleScriptExecution(
	w http.ResponseWriter, r *http.Request, parsingNext http.Handler,
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
		var dataOk bool
		data, dataOk = dataRaw.(map[string]interface{})
		if !dataOk {
			writeJSONRPCError(w, parsed.ID, -32602, "data argument must be an object")
			return
		}
	}

	// Fetch the authorized tool list to build executor bindings
	tools, err := fetchToolList(r, parsingNext)
	if err != nil {
		slog.Error("failed to fetch tool list for script execution", "error", err)
		writeJSONRPCError(w, parsed.ID, -32000, "failed to fetch available tools")
		return
	}

	// Build script.Tool wrappers that dispatch through the middleware chain
	scriptTools := buildScriptTools(r, parsingNext, tools)

	// Create and run executor
	exec := New(scriptTools, cfg)
	result, err := exec.Execute(r.Context(), scriptStr, data)
	if err != nil {
		slog.Warn("script execution failed", "error", err)
		writeJSONRPCError(w, parsed.ID, -32000, "script execution failed")
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
// Each tool's Call closure dispatches through parsingNext so the synthetic
// request gets a fresh ParsedMCPRequest.
func buildScriptTools(origReq *http.Request, parsingNext http.Handler, tools []toolInfo) []Tool {
	scriptTools := make([]Tool, len(tools))
	for i, t := range tools {
		toolName := t.name
		scriptTools[i] = Tool{
			Name:        t.name,
			Description: t.description,
			Call: func(ctx context.Context, arguments map[string]interface{}) (*mcp.CallToolResult, error) {
				return dispatchToolCall(ctx, origReq, parsingNext, toolName, arguments)
			},
		}
	}
	return scriptTools
}

// dispatchToolCall sends a synthetic tools/call request through parsingNext
// and parses the response into a CallToolResult. The parsing middleware in
// parsingNext ensures the synthetic request gets its own ParsedMCPRequest.
func dispatchToolCall(
	ctx context.Context, origReq *http.Request, parsingNext http.Handler,
	toolName string, arguments map[string]interface{},
) (*mcp.CallToolResult, error) {
	params := map[string]interface{}{
		"name":      toolName,
		"arguments": arguments,
	}
	body := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  methodToolsCall,
		"params":  params,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tool call: %w", err)
	}

	// Clear the parsed MCP request so parsingNext re-parses the synthetic body.
	innerCtx := mcpparser.ClearParsedMCPRequest(ctx)
	innerReq, err := http.NewRequestWithContext(innerCtx, http.MethodPost, origReq.URL.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	innerReq.Header = origReq.Header.Clone()
	innerReq.Header.Set("Content-Type", "application/json")
	innerReq.ContentLength = int64(len(bodyBytes))

	capture := newResponseCapture()
	parsingNext.ServeHTTP(capture, innerReq)

	if capture.status != http.StatusOK {
		return nil, fmt.Errorf("tool %q returned HTTP %d", toolName, capture.status)
	}

	return parseToolCallResponse(capture.body.Bytes(), toolName)
}

// fetchToolList sends a synthetic tools/list request through parsingNext.
func fetchToolList(origReq *http.Request, parsingNext http.Handler) ([]toolInfo, error) {
	listBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":0,"method":"%s","params":{}}`, methodToolsList)

	// Clear the parsed MCP request so parsingNext re-parses the synthetic body.
	innerCtx := mcpparser.ClearParsedMCPRequest(origReq.Context())
	innerReq, err := http.NewRequestWithContext(innerCtx, http.MethodPost, origReq.URL.String(), strings.NewReader(listBody))
	if err != nil {
		return nil, err
	}
	innerReq.Header = origReq.Header.Clone()
	innerReq.Header.Set("Content-Type", "application/json")
	innerReq.ContentLength = int64(len(listBody))

	capture := newResponseCapture()
	parsingNext.ServeHTTP(capture, innerReq)

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

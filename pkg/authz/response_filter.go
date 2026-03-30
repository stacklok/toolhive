// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authz provides authorization utilities for MCP servers.
package authz

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
)

var errBug = errors.New("there's a bug")

// ResponseFilteringWriter wraps an http.ResponseWriter to intercept and filter responses
type ResponseFilteringWriter struct {
	http.ResponseWriter
	authorizer       authorizers.Authorizer
	request          *http.Request
	method           string
	buffer           *bytes.Buffer
	statusCode       int
	annotationCache  *AnnotationCache
	passThroughTools map[string]struct{}
}

// NewResponseFilteringWriter creates a new response filtering writer.
// The annotationCache parameter is optional; pass nil to disable annotation caching.
// The passThroughTools parameter is optional; tools whose names appear in this set
// bypass policy filtering because authorization is enforced elsewhere (e.g., inside
// the optimizer decorator for find_tool/call_tool).
func NewResponseFilteringWriter(
	w http.ResponseWriter, authorizer authorizers.Authorizer, r *http.Request, method string,
	annotationCache *AnnotationCache, passThroughTools map[string]struct{},
) *ResponseFilteringWriter {
	return &ResponseFilteringWriter{
		ResponseWriter:   w,
		authorizer:       authorizer,
		request:          r,
		method:           method,
		buffer:           &bytes.Buffer{},
		statusCode:       http.StatusOK,
		annotationCache:  annotationCache,
		passThroughTools: passThroughTools,
	}
}

// Write captures the response body for filtering
func (rfw *ResponseFilteringWriter) Write(data []byte) (int, error) {
	return rfw.buffer.Write(data)
}

// WriteHeader captures the status code
func (rfw *ResponseFilteringWriter) WriteHeader(statusCode int) {
	rfw.statusCode = statusCode
}

// FlushAndFilter processes the captured response and applies filtering if needed.
// Returns an error if filtering or writing fails.
func (rfw *ResponseFilteringWriter) FlushAndFilter() error {
	// If it's not a successful response, just pass it through
	if rfw.statusCode != http.StatusOK && rfw.statusCode != http.StatusAccepted {
		rfw.ResponseWriter.WriteHeader(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rfw.buffer.Bytes()) //nolint:gosec // G705 - JSON-RPC response, not rendered as HTML
		return err
	}

	// Check if this response needs filtering
	if !requiresResponseFiltering(rfw.method) {
		rfw.ResponseWriter.WriteHeader(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rfw.buffer.Bytes()) //nolint:gosec // G705 - JSON-RPC response, not rendered as HTML
		return err
	}

	rawResponse := rfw.buffer.Bytes()

	// Skip filtering for empty responses (common in SSE scenarios where actual data comes via SSE stream)
	if len(rawResponse) == 0 {
		rfw.ResponseWriter.WriteHeader(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rawResponse) //nolint:gosec // G705 - JSON-RPC response, not rendered as HTML
		return err
	}

	mimeType := strings.Split(rfw.ResponseWriter.Header().Get("Content-Type"), ";")[0]

	switch mimeType {
	case "application/json":
		// Remove the upstream Content-Length header. The reverse proxy copies it
		// from the backend response via Header() (which we don't override), but
		// filtering changes the body size. Without this, Go's HTTP server detects
		// the mismatch and tears down the connection.
		rfw.ResponseWriter.Header().Del("Content-Length")
		return rfw.processJSONResponse(rawResponse)
	case "text/event-stream":
		// Same issue: filtering changes the SSE payload size.
		rfw.ResponseWriter.Header().Del("Content-Length")
		return rfw.processSSEResponse(rawResponse)
	default:
		rfw.ResponseWriter.WriteHeader(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rawResponse)
		return err
	}
}

// Flush implements http.Flusher if the underlying ResponseWriter supports it.
// This method is required for streaming support (SSE, streamable-http).
//
// We must delete the Content-Length header before flushing because
// httputil.ReverseProxy (with FlushInterval: -1) calls Flush() after copying
// the backend response. The first Flush() on the underlying writer triggers an
// implicit WriteHeader(200), sending headers to the wire. If the stale
// Content-Length is still present at that point, it's too late to remove it in
// FlushAndFilter().
func (rfw *ResponseFilteringWriter) Flush() {
	if flusher, ok := rfw.ResponseWriter.(http.Flusher); ok {
		rfw.ResponseWriter.Header().Del("Content-Length")
		flusher.Flush()
	}
}

func (rfw *ResponseFilteringWriter) processJSONResponse(rawResponse []byte) error {
	message, err := jsonrpc2.DecodeMessage(rawResponse)
	if err != nil {
		rfw.ResponseWriter.WriteHeader(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rawResponse)
		return err
	}

	response, ok := message.(*jsonrpc2.Response)
	if !ok {
		rfw.ResponseWriter.WriteHeader(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rawResponse)
		return err
	}

	filteredResponse, err := rfw.filterListResponse(response)
	if err != nil {
		return rfw.writeErrorResponse(response.ID, err)
	}

	filteredData, err := jsonrpc2.EncodeMessage(filteredResponse)
	if err != nil {
		return rfw.writeErrorResponse(response.ID, err)
	}

	rfw.ResponseWriter.WriteHeader(rfw.statusCode)
	_, err = rfw.ResponseWriter.Write(filteredData)
	return err
}

//nolint:gocyclo
func (rfw *ResponseFilteringWriter) processSSEResponse(rawResponse []byte) error {
	// Note: this routine is adapted from the one in pkg/mcp/tool_filter.go.
	// I don't see an obvious way to factor out the commonalities, so I'm
	// duplicating it here, but we should refactor response parsing
	// respecting mime types to a common routine.
	var linesep []byte
	if bytes.Contains(rawResponse, []byte("\r\n")) {
		linesep = []byte("\r\n")
	} else if bytes.Contains(rawResponse, []byte("\n")) {
		linesep = []byte("\n")
	} else if bytes.Contains(rawResponse, []byte("\r")) {
		linesep = []byte("\r")
	} else {
		return fmt.Errorf("unsupported separator: %s", string(rawResponse))
	}

	var linesepTotal, linesepCount int
	linesepTotal = bytes.Count(rawResponse, linesep)
	lines := bytes.Split(rawResponse, linesep)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var written bool
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			message, err := jsonrpc2.DecodeMessage(data)
			if err != nil {
				rfw.ResponseWriter.WriteHeader(rfw.statusCode)
				_, err := rfw.ResponseWriter.Write(rawResponse)
				return err
			}

			response, ok := message.(*jsonrpc2.Response)
			if !ok {
				rfw.ResponseWriter.WriteHeader(rfw.statusCode)
				_, err := rfw.ResponseWriter.Write(rawResponse)
				return err
			}

			filteredResponse, err := rfw.filterListResponse(response)
			if err != nil {
				return rfw.writeErrorResponse(response.ID, err)
			}

			filteredData, err := jsonrpc2.EncodeMessage(filteredResponse)
			if err != nil {
				return rfw.writeErrorResponse(response.ID, err)
			}

			_, err = rfw.ResponseWriter.Write([]byte("data: " + string(filteredData) + "\n"))
			if err != nil {
				return fmt.Errorf("%w: %w", errBug, err)
			}

			written = true
		}

		if !written {
			_, err := rfw.ResponseWriter.Write(line)
			if err != nil {
				return fmt.Errorf("%w: %w", errBug, err)
			}
		}

		_, err := rfw.ResponseWriter.Write(linesep)
		if err != nil {
			return fmt.Errorf("%w: %w", errBug, err)
		}
		linesepCount++
	}

	// This ensures we don't send too few line separators, which might break
	// SSE parsing.
	if linesepCount < linesepTotal {
		_, err := rfw.ResponseWriter.Write(linesep)
		if err != nil {
			return fmt.Errorf("%w: %w", errBug, err)
		}
	}

	return nil
}

// requiresResponseFiltering reports whether the method needs response filtering.
// This covers the three MCP list operations and the optimizer's find_tool call,
// whose response embeds a filtered tool list inside a CallToolResult.
func requiresResponseFiltering(method string) bool {
	return method == string(mcp.MethodToolsList) ||
		method == string(mcp.MethodPromptsList) ||
		method == string(mcp.MethodResourcesList) ||
		method == optimizerdec.FindToolName
}

// filterListResponse filters the list response based on authorization policies
func (rfw *ResponseFilteringWriter) filterListResponse(response *jsonrpc2.Response) (*jsonrpc2.Response, error) {
	if response.Error != nil {
		// If there's an error in the response, don't filter
		return response, nil
	}

	if response.Result == nil {
		// If there's no result, don't filter
		return response, nil
	}

	// Filter based on the method
	switch rfw.method {
	case string(mcp.MethodToolsList):
		return rfw.filterToolsResponse(response)
	case string(mcp.MethodPromptsList):
		return rfw.filterPromptsResponse(response)
	case string(mcp.MethodResourcesList):
		return rfw.filterResourcesResponse(response)
	case optimizerdec.FindToolName:
		return rfw.filterFindToolResponse(response)
	default:
		// Unknown method, just return as-is
		return response, nil
	}
}

// filterToolsResponse filters tools based on call_tool authorization
func (rfw *ResponseFilteringWriter) filterToolsResponse(response *jsonrpc2.Response) (*jsonrpc2.Response, error) {
	// Parse the result as a ListToolsResult
	var listResult mcp.ListToolsResult
	if err := json.Unmarshal(response.Result, &listResult); err != nil {
		// If we can't parse it as a list response, just return it as-is
		return response, nil
	}

	// Populate annotation cache from tools/list response so that
	// subsequent tools/call requests can look up annotations.
	rfw.annotationCache.SetFromToolsList(listResult.Tools)

	// When the optimizer is enabled, its meta-tools (find_tool, call_tool) appear
	// in tools/list instead of real backend tools. These meta-tools won't match
	// any operator-written Cedar policy (which references real tool names), so
	// default-deny would filter them out — leaving the client with zero tools.
	// Authorization for the underlying backend tools is enforced by the authz
	// middleware: call_tool requests are intercepted and the inner tool_name
	// argument is authorized against Cedar policy before the request is served.
	// See: https://github.com/stacklok/toolhive/issues/4373
	passThrough := []mcp.Tool{}
	regular := []mcp.Tool{}
	for _, t := range listResult.Tools {
		if _, ok := rfw.passThroughTools[t.Name]; ok {
			passThrough = append(passThrough, t)
		} else {
			regular = append(regular, t)
		}
	}

	// filterToolsByPolicy checks each tool against the caller's Cedar policies
	// (injecting annotations into context for when-clause evaluation) and returns
	// only tools the caller is authorized to call.
	policyFiltered := filterToolsByPolicy(rfw.request.Context(), rfw.authorizer, regular)
	filteredTools := make([]mcp.Tool, 0, len(passThrough)+len(policyFiltered))
	filteredTools = append(filteredTools, passThrough...)
	filteredTools = append(filteredTools, policyFiltered...)

	// Create a new result with filtered tools
	filteredResult := mcp.ListToolsResult{
		PaginatedResult: listResult.PaginatedResult,
		Tools:           filteredTools,
	}

	// Marshal the filtered result back
	filteredResultData, err := json.Marshal(filteredResult)
	if err != nil {
		return nil, err
	}

	// Create a new response with the filtered result
	filteredResponse := &jsonrpc2.Response{
		ID:     response.ID,
		Result: json.RawMessage(filteredResultData),
	}

	return filteredResponse, nil
}

// filterPromptsResponse filters prompts based on get_prompt authorization
func (rfw *ResponseFilteringWriter) filterPromptsResponse(response *jsonrpc2.Response) (*jsonrpc2.Response, error) {
	// Parse the result as a ListPromptsResult
	var listResult mcp.ListPromptsResult
	if err := json.Unmarshal(response.Result, &listResult); err != nil {
		// If we can't parse it as a list response, just return it as-is
		return response, nil
	}

	// Note: instantiating the list ensures that no null value is sent over the wire.
	// This is basically defensive programming, but for clients.
	filteredPrompts := []mcp.Prompt{}
	for _, prompt := range listResult.Prompts {
		// Check if the user is authorized to get this prompt
		authorized, err := rfw.authorizer.AuthorizeWithJWTClaims(
			rfw.request.Context(),
			authorizers.MCPFeaturePrompt,
			authorizers.MCPOperationGet,
			prompt.Name,
			nil, // No arguments for the authorization check
		)
		if err != nil {
			slog.Warn("Authorization check failed for prompt, skipping",
				"prompt", prompt.Name, "error", err)
			continue
		}

		if authorized {
			filteredPrompts = append(filteredPrompts, prompt)
		}
	}

	// Create a new result with filtered prompts
	filteredResult := mcp.ListPromptsResult{
		PaginatedResult: listResult.PaginatedResult,
		Prompts:         filteredPrompts,
	}

	// Marshal the filtered result back
	filteredResultData, err := json.Marshal(filteredResult)
	if err != nil {
		return nil, err
	}

	// Create a new response with the filtered result
	filteredResponse := &jsonrpc2.Response{
		ID:     response.ID,
		Result: json.RawMessage(filteredResultData),
	}

	return filteredResponse, nil
}

// filterResourcesResponse filters resources based on read_resource authorization
func (rfw *ResponseFilteringWriter) filterResourcesResponse(response *jsonrpc2.Response) (*jsonrpc2.Response, error) {
	// Parse the result as a ListResourcesResult
	var listResult mcp.ListResourcesResult
	if err := json.Unmarshal(response.Result, &listResult); err != nil {
		// If we can't parse it as a list response, just return it as-is
		return response, nil
	}

	// Note: instantiating the list ensures that no null value is sent over the wire.
	// This is basically defensive programming, but for clients.
	filteredResources := []mcp.Resource{}
	for _, resource := range listResult.Resources {
		// Check if the user is authorized to read this resource
		authorized, err := rfw.authorizer.AuthorizeWithJWTClaims(
			rfw.request.Context(),
			authorizers.MCPFeatureResource,
			authorizers.MCPOperationRead,
			resource.URI,
			nil, // No arguments for the authorization check
		)
		if err != nil {
			slog.Warn("Authorization check failed for resource, skipping",
				"resource", resource.URI, "error", err)
			continue
		}

		if authorized {
			filteredResources = append(filteredResources, resource)
		}
	}

	// Create a new result with filtered resources
	filteredResult := mcp.ListResourcesResult{
		PaginatedResult: listResult.PaginatedResult,
		Resources:       filteredResources,
	}

	// Marshal the filtered result back
	filteredResultData, err := json.Marshal(filteredResult)
	if err != nil {
		return nil, err
	}

	// Create a new response with the filtered result
	filteredResponse := &jsonrpc2.Response{
		ID:     response.ID,
		Result: json.RawMessage(filteredResultData),
	}

	return filteredResponse, nil
}

// writeErrorResponse writes an error response
func (rfw *ResponseFilteringWriter) writeErrorResponse(id jsonrpc2.ID, err error) error {
	errorResponse := &jsonrpc2.Response{
		ID:    id,
		Error: jsonrpc2.NewError(500, fmt.Sprintf("Error filtering response: %v", err)),
	}

	errorData, marshalErr := json.Marshal(errorResponse)
	if marshalErr != nil {
		// If we can't even marshal the error, write a simple error
		rfw.ResponseWriter.WriteHeader(http.StatusInternalServerError)
		_, writeErr := rfw.ResponseWriter.Write([]byte(`{"error": "Internal server error"}`))
		return writeErr
	}

	rfw.ResponseWriter.WriteHeader(http.StatusInternalServerError)
	_, writeErr := rfw.ResponseWriter.Write(errorData)
	return writeErr
}

// filterFindToolResponse filters the tools list embedded in a find_tool tools/call
// response. The response is a CallToolResult whose first text content item contains
// a JSON-encoded optimizer.FindToolOutput. Only tools the caller is authorized to
// call are retained.
//
// mcp.CallToolResult is used directly with its built-in UnmarshalJSON so that the
// Content interface slice is deserialized correctly into concrete types
// (TextContent, ImageContent, etc.) without a bespoke minimal struct.
//
// To identify which content item carries the find_tool output, each TextContent item
// is tentatively unmarshaled as optimizer.FindToolOutput. A successful unmarshal is a
// stronger signal than checking tc.Type == "text" alone — it confirms the item actually
// carries a find_tool result rather than an arbitrary text payload (e.g. an error string).
func (rfw *ResponseFilteringWriter) filterFindToolResponse(response *jsonrpc2.Response) (*jsonrpc2.Response, error) {
	// Use mcp.CallToolResult's built-in UnmarshalJSON for correct Content interface dispatch.
	var callResult mcp.CallToolResult
	if err := json.Unmarshal(response.Result, &callResult); err != nil || callResult.IsError {
		return response, nil
	}

	// Find the first TextContent item that successfully unmarshals as optimizer.FindToolOutput.
	textIdx := -1
	var output optimizer.FindToolOutput
	for i, c := range callResult.Content {
		tc, ok := c.(mcp.TextContent)
		if !ok {
			continue
		}
		if err := json.Unmarshal([]byte(tc.Text), &output); err == nil {
			textIdx = i
			break
		}
	}
	if textIdx == -1 {
		return response, nil
	}

	// Populate annotation cache before filtering, mirroring filterToolsResponse.
	// Subsequent call_tool requests use these annotations for Cedar when-clause evaluation
	// (e.g. resource.readOnlyHint). The cache is populated from the unfiltered list so
	// that annotations are available even for tools that Cedar will deny.
	rfw.annotationCache.SetFromToolsList(output.Tools)

	output.Tools = filterToolsByPolicy(rfw.request.Context(), rfw.authorizer, output.Tools)

	filteredText, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("re-encoding find_tool output: %w", err)
	}
	original := callResult.Content[textIdx].(mcp.TextContent)
	callResult.Content[textIdx] = mcp.TextContent{Type: original.Type, Text: string(filteredText)}

	filteredResult, err := json.Marshal(callResult)
	if err != nil {
		return nil, fmt.Errorf("re-encoding call result: %w", err)
	}

	return &jsonrpc2.Response{
		ID:     response.ID,
		Result: json.RawMessage(filteredResult),
	}, nil
}

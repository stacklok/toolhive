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
)

var errBug = errors.New("there's a bug")

// ResponseFilteringWriter wraps an http.ResponseWriter to intercept and filter responses
type ResponseFilteringWriter struct {
	http.ResponseWriter
	authorizer    authorizers.Authorizer
	request       *http.Request
	method        string
	buffer        *bytes.Buffer
	statusCode    int
	headerWritten bool // Track if headers have been written
}

// NewResponseFilteringWriter creates a new response filtering writer
func NewResponseFilteringWriter(
	w http.ResponseWriter, authorizer authorizers.Authorizer, r *http.Request, method string,
) *ResponseFilteringWriter {
	return &ResponseFilteringWriter{
		ResponseWriter: w,
		authorizer:     authorizer,
		request:        r,
		method:         method,
		buffer:         &bytes.Buffer{},
		statusCode:     http.StatusOK,
		headerWritten:  false,
	}
}

// Write captures the response body for filtering
func (rfw *ResponseFilteringWriter) Write(data []byte) (int, error) {
	return rfw.buffer.Write(data)
}

// WriteHeader captures the status code and forwards to underlying writer.
// Deletes Content-Length since filtered response size may differ.
func (rfw *ResponseFilteringWriter) WriteHeader(statusCode int) {
	rfw.statusCode = statusCode
	if !rfw.headerWritten {
		// Delete Content-Length before writing headers - filtered response size will differ
		rfw.ResponseWriter.Header().Del("Content-Length")
		rfw.ResponseWriter.WriteHeader(statusCode)
		rfw.headerWritten = true
	}
}

// FlushAndFilter processes the captured response and applies filtering if needed.
// Returns an error if filtering or writing fails.
func (rfw *ResponseFilteringWriter) FlushAndFilter() error {
	// Delete Content-Length to prevent mismatch with filtered response
	rfw.ResponseWriter.Header().Del("Content-Length")

	// If it's not a successful response, just pass it through
	if rfw.statusCode != http.StatusOK && rfw.statusCode != http.StatusAccepted {
		rfw.writeHeaderOnce(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rfw.buffer.Bytes()) //nolint:gosec // G705 - JSON-RPC response, not rendered as HTML
		return err
	}

	// Check if this is a list operation that needs filtering
	if !isListOperation(rfw.method) {
		rfw.writeHeaderOnce(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rfw.buffer.Bytes()) //nolint:gosec // G705 - JSON-RPC response, not rendered as HTML
		return err
	}

	rawResponse := rfw.buffer.Bytes()

	// Skip filtering for empty responses (common in SSE scenarios where actual data comes via SSE stream)
	if len(rawResponse) == 0 {
		rfw.writeHeaderOnce(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rawResponse) //nolint:gosec // G705 - JSON-RPC response, not rendered as HTML
		return err
	}

	mimeType := strings.Split(rfw.ResponseWriter.Header().Get("Content-Type"), ";")[0]

	switch mimeType {
	case "application/json":
		return rfw.processJSONResponse(rawResponse)
	case "text/event-stream":
		return rfw.processSSEResponse(rawResponse)
	default:
		rfw.writeHeaderOnce(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rawResponse)
		return err
	}
}

// writeHeaderOnce writes the status code only if not already written
func (rfw *ResponseFilteringWriter) writeHeaderOnce(statusCode int) {
	if !rfw.headerWritten {
		rfw.ResponseWriter.Header().Del("Content-Length")
		rfw.ResponseWriter.WriteHeader(statusCode)
		rfw.headerWritten = true
	}
}

// Flush implements http.Flusher. For SSE responses with list operations,
// it processes buffered data when a complete event is available.
func (rfw *ResponseFilteringWriter) Flush() {
	// Always flush the underlying writer at the end
	defer func() {
		if flusher, ok := rfw.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
	}()

	// If no data buffered, nothing to process
	if rfw.buffer.Len() == 0 {
		return
	}

	// For non-list operations, write through immediately
	if !isListOperation(rfw.method) {
		rfw.ResponseWriter.Header().Del("Content-Length")
		_, _ = rfw.ResponseWriter.Write(rfw.buffer.Bytes())
		rfw.buffer.Reset()
		return
	}

	mimeType := strings.Split(rfw.ResponseWriter.Header().Get("Content-Type"), ";")[0]

	// For SSE, wait until we have a complete event (ends with \n\n)
	if mimeType == "text/event-stream" {
		rawResponse := rfw.buffer.Bytes()
		hasCompleteEvent := bytes.Contains(rawResponse, []byte("\n\n")) ||
			bytes.Contains(rawResponse, []byte("\r\n\r\n"))
		if !hasCompleteEvent {
			// Keep buffering until we have a complete event
			return
		}

		// Process complete SSE event
		rfw.ResponseWriter.Header().Del("Content-Length")
		if err := rfw.processSSEResponse(rawResponse); err != nil {
			slog.Error("error processing SSE response in Flush", "error", err)
			// On error, write original data
			_, _ = rfw.ResponseWriter.Write(rawResponse)
		}
		rfw.buffer.Reset()
		return
	}

	// For other content types (like JSON), process immediately
	rfw.ResponseWriter.Header().Del("Content-Length")
	if err := rfw.processJSONResponse(rfw.buffer.Bytes()); err != nil {
		slog.Error("error processing JSON response in Flush", "error", err)
		_, _ = rfw.ResponseWriter.Write(rfw.buffer.Bytes())
	}
	rfw.buffer.Reset()
}

func (rfw *ResponseFilteringWriter) processJSONResponse(rawResponse []byte) error {
	message, err := jsonrpc2.DecodeMessage(rawResponse)
	if err != nil {
		rfw.writeHeaderOnce(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rawResponse)
		return err
	}

	response, ok := message.(*jsonrpc2.Response)
	if !ok {
		rfw.writeHeaderOnce(rfw.statusCode)
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

	rfw.writeHeaderOnce(rfw.statusCode)
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
			// Trim leading space after "data:" (optional in SSE spec)
			data = bytes.TrimLeft(data, " ")

			message, err := jsonrpc2.DecodeMessage(data)
			if err != nil {
				// Can't decode, write line as-is
				_, _ = rfw.ResponseWriter.Write(line)
				_, _ = rfw.ResponseWriter.Write(linesep)
				linesepCount++
				continue
			}

			response, ok := message.(*jsonrpc2.Response)
			if !ok {
				// Not a response, write line as-is
				_, _ = rfw.ResponseWriter.Write(line)
				_, _ = rfw.ResponseWriter.Write(linesep)
				linesepCount++
				continue
			}

			filteredResponse, err := rfw.filterListResponse(response)
			if err != nil {
				return rfw.writeErrorResponse(response.ID, err)
			}

			filteredData, err := jsonrpc2.EncodeMessage(filteredResponse)
			if err != nil {
				return rfw.writeErrorResponse(response.ID, err)
			}

			// Trim trailing whitespace from encoded data for consistent format
			filteredData = bytes.TrimRight(filteredData, " \t\r\n")

			_, err = rfw.ResponseWriter.Write([]byte("data: "))
			if err != nil {
				return fmt.Errorf("%w: %w", errBug, err)
			}
			_, err = rfw.ResponseWriter.Write(filteredData)
			if err != nil {
				return fmt.Errorf("%w: %w", errBug, err)
			}
			_, err = rfw.ResponseWriter.Write(linesep)
			if err != nil {
				return fmt.Errorf("%w: %w", errBug, err)
			}
			linesepCount++

			written = true
		}

		if !written {
			_, err := rfw.ResponseWriter.Write(line)
			if err != nil {
				return fmt.Errorf("%w: %w", errBug, err)
			}
			_, err = rfw.ResponseWriter.Write(linesep)
			if err != nil {
				return fmt.Errorf("%w: %w", errBug, err)
			}
			linesepCount++
		}
	}

	// This ensures we don't send too few line separators, which might break
	// SSE parsing.
	for linesepCount < linesepTotal {
		_, err := rfw.ResponseWriter.Write(linesep)
		if err != nil {
			return fmt.Errorf("%w: %w", errBug, err)
		}
		linesepCount++
	}

	return nil
}

// isListOperation checks if the method is a list operation
func isListOperation(method string) bool {
	return method == string(mcp.MethodToolsList) ||
		method == string(mcp.MethodPromptsList) ||
		method == string(mcp.MethodResourcesList)
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
	default:
		// Unknown list method, just return as-is
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

	// Note: instantiating the list ensures that no null value is sent over the wire.
	// This is basically defensive programming, but for clients.
	filteredTools := []mcp.Tool{}
	for _, tool := range listResult.Tools {
		// Check if the user is authorized to call this tool
		authorized, err := rfw.authorizer.AuthorizeWithJWTClaims(
			rfw.request.Context(),
			authorizers.MCPFeatureTool,
			authorizers.MCPOperationCall,
			tool.Name,
			nil, // No arguments for the authorization check
		)
		if err != nil {
			slog.Warn("Authorization check failed for tool, skipping",
				"tool", tool.Name, "error", err)
			continue
		}

		if authorized {
			filteredTools = append(filteredTools, tool)
		}
	}

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

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

var errToolNameNotFound = errors.New("tool name not found")
var errBug = errors.New("there's a bug")
var errKeepBuffering = errors.New("keep buffering")

// toolOverrideEntry is a struct that represents a tool override entry.
type toolOverrideEntry struct {
	ActualName          string
	OverrideName        string
	OverrideDescription string
}

// toolMiddlewareConfig is a helper struct used to configure the tool middleware,
// and it's meant to map from a tool's actual name to a config entry.
//
// The two separate structs are necessary because it must be possible to specify
// tool overrides without tool filtering.
//
// Assume a User only specified an override for a single tool out of a list of
// n tools; in such a case, it would become unclear whether the tool is the only
// one allowed or is the only one overridden.
//
// Sufficient information could be represented in a more complex structure, but
// this gets the job and is easy enough to understand.
type toolMiddlewareConfig struct {
	filterTools          map[string]struct{}
	actualToUserOverride map[string]toolOverrideEntry
	userToActualOverride map[string]toolOverrideEntry
}

func (c *toolMiddlewareConfig) isToolInFilter(toolName string) bool {
	if len(c.filterTools) == 0 {
		return true
	}

	_, ok := c.filterTools[toolName]
	return ok
}

func (c *toolMiddlewareConfig) getToolCallActualName(toolName string) (string, bool) {
	if len(c.userToActualOverride) == 0 {
		return "", false
	}

	entry, ok := c.userToActualOverride[toolName]
	return entry.ActualName, ok
}

func (c *toolMiddlewareConfig) getToolListOverride(toolName string) (*toolOverrideEntry, bool) {
	if len(c.actualToUserOverride) == 0 {
		return nil, false
	}

	entry, ok := c.actualToUserOverride[toolName]
	return &entry, ok
}

// ToolMiddlewareOption is a function that can be used to configure the tool
// middleware.
type ToolMiddlewareOption func(*toolMiddlewareConfig) error

// SimpleTool represents a minimal tool with name and description.
// This is used by ApplyToolFiltering to work with tools in a generic way.
type SimpleTool struct {
	Name        string
	Description string
}

// ApplyToolFiltering applies filtering and overriding to a list of tools.
// This is the core logic used by both the HTTP middleware and other components
// that need to apply the same filtering/overriding behavior.
//
// Returns the filtered and overridden tools.
func ApplyToolFiltering(opts []ToolMiddlewareOption, tools []SimpleTool) ([]SimpleTool, error) {
	config := &toolMiddlewareConfig{
		filterTools:          make(map[string]struct{}),
		actualToUserOverride: make(map[string]toolOverrideEntry),
		userToActualOverride: make(map[string]toolOverrideEntry),
	}

	// Apply options to build config
	for _, opt := range opts {
		if err := opt(config); err != nil {
			return nil, err
		}
	}

	// Use the shared core logic
	return applyFilteringAndOverrides(config, tools), nil
}

// WithToolsFilter is a function that can be used to configure the tool
// middleware to use a filter list of tools.
func WithToolsFilter(toolsFilter ...string) ToolMiddlewareOption {
	return func(mw *toolMiddlewareConfig) error {
		for _, tf := range toolsFilter {
			if tf == "" {
				return fmt.Errorf("tool name cannot be empty")
			}

			mw.filterTools[tf] = struct{}{}
		}

		return nil
	}
}

// WithToolsOverride is a function that can be used to configure the tool
// middleware to use a map of tools to override the actual list of tools.
//
// If an empty string is provided for either overrideName or overrideDescription,
// that field will be left unchanged. An error is returned if actualName is empty.
func WithToolsOverride(actualName string, overrideName string, overrideDescription string) ToolMiddlewareOption {
	return func(mw *toolMiddlewareConfig) error {
		if actualName == "" {
			return fmt.Errorf("tool name cannot be empty")
		}

		if overrideName == "" && overrideDescription == "" {
			return fmt.Errorf("override name and description cannot both be empty")
		}

		entry := toolOverrideEntry{
			ActualName:          actualName,
			OverrideName:        overrideName,        // empty string means no override
			OverrideDescription: overrideDescription, // empty string means no override
		}
		mw.actualToUserOverride[actualName] = entry
		mw.userToActualOverride[overrideName] = entry

		return nil
	}
}

// NewListToolsMappingMiddleware creates an HTTP middleware that parses SSE responses
// and plain JSON objects to extract tool names from JSON-RPC messages containing
// tool lists or tool calls.
//
// The middleware looks for SSE events with:
// - event: message
// - data: {"jsonrpc":"2.0","id":X,"result":{"tools":[...]}}
//
// This middleware is designed to be used ONLY when tool filtering or
// override are enabled, and expects the list of tools to be "correct"
// (i.e. not empty and not containing nonexisting tools).
func NewListToolsMappingMiddleware(opts ...ToolMiddlewareOption) (types.MiddlewareFunction, error) {
	config := &toolMiddlewareConfig{
		filterTools:          make(map[string]struct{}),
		actualToUserOverride: make(map[string]toolOverrideEntry),
		userToActualOverride: make(map[string]toolOverrideEntry),
	}
	for _, opt := range opts {
		if err := opt(config); err != nil {
			return nil, err
		}
	}

	if len(config.filterTools) == 0 && len(config.actualToUserOverride) == 0 {
		return nil, fmt.Errorf("tools list for filtering or overriding is empty")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// NOTE: this middleware only checks the response body, whose
			// format at this point is not yet known and might be either a
			// JSON payload or an SSE stream.
			//
			// The way this is implemented is that we wrap the response writer
			// in order to buffer the response body. Once Flush() is called, we
			// process the buffer according to its content type and possibly
			// modify it before returning it to the client.
			rw := &toolFilterWriter{
				ResponseWriter: w,
				config:         config,
			}

			// Call the next handler
			next.ServeHTTP(rw, r)
		})
	}, nil
}

// NewToolCallMappingMiddleware creates an HTTP middleware that parses tool call
// requests and filters out tools that are not in the filter list.
//
// The middleware looks for JSON-RPC messages with:
// - method: tool/call
// - params: {"name": "tool_name"}
//
// This middleware is designed to be used ONLY when tool filtering or override
// is enabled, and expects the list of tools to be "correct" (i.e. not empty
// and not containing nonexisting tools).
func NewToolCallMappingMiddleware(opts ...ToolMiddlewareOption) (types.MiddlewareFunction, error) {
	config := &toolMiddlewareConfig{
		filterTools:          make(map[string]struct{}),
		actualToUserOverride: make(map[string]toolOverrideEntry),
		userToActualOverride: make(map[string]toolOverrideEntry),
	}
	for _, opt := range opts {
		if err := opt(config); err != nil {
			return nil, err
		}
	}

	if len(config.filterTools) == 0 && len(config.actualToUserOverride) == 0 {
		return nil, fmt.Errorf("tools list for filtering or overriding is empty")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Read the request body
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				// If we can't read the body, let the next handler deal with it
				next.ServeHTTP(w, r)
				return
			}

			// Restore the request body for downstream handlers
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

			// Try to parse the request as a tool call request. If it succeeds,
			// check if the tool is in the filter. If it is not a tool call request,
			// just pass it through.
			var toolCallRequest toolCallRequest
			err = json.Unmarshal(bodyBytes, &toolCallRequest)
			if err == nil && toolCallRequest.Method == "tools/call" {
				fix := processToolCallRequest(config, toolCallRequest)

				switch fix := fix.(type) {

				// If the tool call request is allowed, and the tool name is not overridden,
				// we just pass it through unmodified.
				case *toolCallNoAction:
					next.ServeHTTP(w, r)
					return

				// NOTE: ideally, trying to call that was filtered out by config should be
				// equivalent to calling a nonexisting tool; in such cases and when the SSE
				// transport is used, the behaviour of the official Python SDK is to return
				// a 202 Accepted to THIS call and return an success message in the SSE
				// stream saying that the tool does not exist.
				//
				// It basically fails successfully.
				//
				// Unfortunately, implementing this behaviour is not trivial and requires
				// session management, as the SSE stream is managed by the proxy in an entirely
				// different thread of execution. As a consequence, the best thing we can
				// do that is still compliant with the spec is to return a 400 Bad Request
				// to the client.
				case *toolCallFilter:
					w.WriteHeader(http.StatusBadRequest)
					return

				// In case of a tool name override, we need to fix the tool call request
				// and then forward it to the next handler.
				case *toolCallOverride:
					(*toolCallRequest.Params)["name"] = fix.Name()
					bodyBytes, err = json.Marshal(toolCallRequest)
					if err != nil {
						logger.Errorf("Error marshalling tool call request: %v", err)
						next.ServeHTTP(w, r)
						return
					}

					r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
					// TODO: find a reasonable way to test this
					r.ContentLength = int64(len(bodyBytes))

				// According to the current version of the MCP spec at
				// https://modelcontextprotocol.io/specification/2025-06-18/schema#calltoolrequest
				// this case can only happen if the request is malformed. The proxied MCP
				// server should be able to process the request, but since we detect it here
				// we short-circuit returning an error.
				case *toolCallBogus:
					w.WriteHeader(http.StatusBadRequest)
					return

				// This should never happen, but we handle it just in case.
				default:
					logger.Errorf("Error processing tool call of a filtered tool: %v", err)
					next.ServeHTTP(w, r)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}, nil
}

// toolFilterWriter wraps http.ResponseWriter to capture and process SSE responses
type toolFilterWriter struct {
	http.ResponseWriter
	buffer []byte
	config *toolMiddlewareConfig
}

// WriteHeader captures the status code
func (rw *toolFilterWriter) WriteHeader(statusCode int) {
	rw.ResponseWriter.WriteHeader(statusCode)
}

// Write captures the response body and processes SSE events
func (rw *toolFilterWriter) Write(data []byte) (int, error) {
	rw.buffer = append(rw.buffer, data...)
	return len(data), nil
}

// Flush processes any remaining buffered data and writes it to the underlying ResponseWriter
func (rw *toolFilterWriter) Flush() {
	if len(rw.buffer) > 0 {
		mimeType := strings.Split(rw.ResponseWriter.Header().Get("Content-Type"), ";")[0]

		if mimeType == "" {
			_, err := rw.ResponseWriter.Write(rw.buffer)
			if err != nil {
				logger.Errorf("Error writing buffer: %v", err)
			}
			return
		}

		var b bytes.Buffer
		err := processBuffer(rw.config, rw.buffer, mimeType, &b)
		if errors.Is(err, errKeepBuffering) {
			logger.Debugf("Buffered %d so far, keep buffering...", len(rw.buffer))
			return
		}
		if err != nil {
			logger.Errorf("Error flushing response: %v", err)
		}

		logger.Debugf("Flushing %d bytes...", len(b.Bytes()))
		_, err = rw.ResponseWriter.Write(b.Bytes())
		if err != nil {
			logger.Errorf("Error writing buffer: %v", err)
		}
		rw.buffer = rw.buffer[:0] // Reset buffer
	}

	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

type toolsListResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  struct {
		Tools *[]map[string]any `json:"tools"`
	} `json:"result"`
}

type toolCallRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  *map[string]any `json:"params,omitempty"`
}

// processSSEBuffer processes any complete SSE events in the buffer
func processBuffer(
	config *toolMiddlewareConfig,
	buffer []byte,
	mimeType string,
	w io.Writer,
) error {
	if len(buffer) == 0 {
		return nil
	}

	switch mimeType {
	case "application/json":
		var toolsListResponse toolsListResponse
		var syntaxError *json.SyntaxError
		err := json.Unmarshal(buffer, &toolsListResponse)
		if errors.As(err, &syntaxError) {
			return fmt.Errorf("%w: %w", errKeepBuffering, err)
		}
		if err == nil && toolsListResponse.Result.Tools != nil {
			return processToolsListResponse(config, toolsListResponse, w)
		}
	case "text/event-stream":
		return processEventStream(config, buffer, w)
	default:
		// NOTE: Content-Type header is mandatory in the spec, and as of the
		// time of this writing, the only allowed content types are
		// * application/json, and
		// * text/event-stream
		//
		// As a result, we should never get here and it is safe to return an
		// error.
		return fmt.Errorf("unsupported mime type: %s", mimeType)
	}

	// If we get this far, we have a valid buffer that we cannot process
	// in any other way, so we just write it to the underlying writer.
	_, err := w.Write(buffer)
	return err
}

//nolint:gocyclo
func processEventStream(
	config *toolMiddlewareConfig,
	buffer []byte,
	w io.Writer,
) error {
	if len(buffer) > 1 && buffer[len(buffer)-1] != '\n' && buffer[len(buffer)-1] != '\r' {
		return fmt.Errorf("%w: %v", errKeepBuffering, "event separator not found")
	}

	// NOTE: this looks uglier, but is more efficient than scanning the whole buffer
	var linesep []byte
	if len(buffer) >= 2 && bytes.Equal(buffer[len(buffer)-2:], []byte("\r\n")) {
		linesep = []byte("\r\n")
	} else if len(buffer) >= 1 && buffer[len(buffer)-1] == '\n' {
		linesep = []byte("\n")
	} else if len(buffer) >= 1 && buffer[len(buffer)-1] == '\r' {
		linesep = []byte("\r")
	} else {
		return fmt.Errorf("unsupported separator: %s", string(buffer))
	}

	var linesepTotal, linesepCount int
	linesepTotal = bytes.Count(buffer, linesep)
	lines := bytes.Split(buffer, linesep)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var written bool
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			var toolsListResponse toolsListResponse
			if err := json.Unmarshal(data, &toolsListResponse); err == nil && toolsListResponse.Result.Tools != nil {
				// We got to the point of processing a real tools list response,
				// so we need to write the "data: " prefix first.
				_, err := w.Write([]byte("data: "))
				if err != nil {
					return fmt.Errorf("%w: %w", errBug, err)
				}

				if err := processToolsListResponse(config, toolsListResponse, w); err != nil {
					return err
				}
				written = true
			}
		}

		if !written {
			_, err := w.Write(line)
			if err != nil {
				return fmt.Errorf("%w: %w", errBug, err)
			}
		}

		_, err := w.Write(linesep)
		if err != nil {
			return fmt.Errorf("%w: %w", errBug, err)
		}
		linesepCount++
	}

	// This ensures we don't send too few line separators, which might break
	// SSE parsing.
	if linesepCount < linesepTotal {
		_, err := w.Write(linesep)
		if err != nil {
			return fmt.Errorf("%w: %w", errBug, err)
		}
	}

	return nil
}

// processToolsListResponse processes a tools list response filtering out
// tools that are not in the filter list.
func processToolsListResponse(
	config *toolMiddlewareConfig,
	toolsListResponse toolsListResponse,
	w io.Writer,
) error {
	// Convert to SimpleTool format for shared processing
	simpleTools := make([]SimpleTool, 0, len(*toolsListResponse.Result.Tools))
	toolMaps := make([]map[string]any, 0, len(*toolsListResponse.Result.Tools))

	for _, tool := range *toolsListResponse.Result.Tools {
		// NOTE: the spec does not allow for name to be missing.
		toolName, ok := tool["name"].(string)
		if !ok {
			return errToolNameNotFound
		}

		// NOTE: the spec does not allow for empty tool names.
		if toolName == "" {
			return errToolNameNotFound
		}

		// Get description if present (optional in MCP spec)
		description, _ := tool["description"].(string)

		simpleTools = append(simpleTools, SimpleTool{
			Name:        toolName,
			Description: description,
		})
		toolMaps = append(toolMaps, tool)
	}

	// Apply the shared filtering/override logic
	processedTools := applyFilteringAndOverrides(config, simpleTools)

	// Build the filtered response by matching processed tools with their original maps
	// Note: This is O(n²) complexity, but acceptable because:
	// - Tool lists are typically small (< 100 tools per backend)
	// - Only runs once during tool list retrieval (not in hot path)
	// - Inner loop breaks early on match
	filteredTools := make([]map[string]any, 0, len(processedTools))
	for _, processed := range processedTools {
		// Find the original tool map by matching names
		for i, simple := range simpleTools {
			if simple.Name == processed.Name || simple.Name == findOriginalName(config, processed.Name) {
				// Clone the original map and update name/description
				toolCopy := make(map[string]any, len(toolMaps[i]))
				for k, v := range toolMaps[i] {
					toolCopy[k] = v
				}
				toolCopy["name"] = processed.Name
				if processed.Description != "" {
					toolCopy["description"] = processed.Description
				}
				filteredTools = append(filteredTools, toolCopy)
				break
			}
		}
	}

	toolsListResponse.Result.Tools = &filteredTools
	if err := json.NewEncoder(w).Encode(toolsListResponse); err != nil {
		return fmt.Errorf("%w: %w", errBug, err)
	}

	return nil
}

// applyFilteringAndOverrides is the core logic for filtering and overriding tools.
// This implements the exact same logic as before but is now extracted for reuse.
func applyFilteringAndOverrides(config *toolMiddlewareConfig, tools []SimpleTool) []SimpleTool {
	result := make([]SimpleTool, 0, len(tools))
	for _, tool := range tools {
		description := tool.Description

		// If the tool is overridden, we need to use the override name and description.
		if entry, ok := config.getToolListOverride(tool.Name); ok {
			if entry.OverrideName != "" {
				tool.Name = entry.OverrideName
			}
			if entry.OverrideDescription != "" {
				description = entry.OverrideDescription
			}
		}

		// If the tool is in the filter, we add it to the filtered tools list.
		// Note that lookup is done using the user-known name (tool.Name after override).
		if config.isToolInFilter(tool.Name) {
			result = append(result, SimpleTool{
				Name:        tool.Name,
				Description: description,
			})
		}
	}
	return result
}

// findOriginalName attempts to find the original tool name before override.
func findOriginalName(config *toolMiddlewareConfig, overriddenName string) string {
	// Iterate through overrides to find reverse mapping
	for actualName, entry := range config.actualToUserOverride {
		if entry.OverrideName == overriddenName {
			return actualName
		}
	}
	return overriddenName
}

// toolCallFix mimics a sum type in Go. The actual types represent the
// possible manipulations to perform on the tool call request, namely:
// - filter the tool call request
// - override the tool call request
// - return a bogus tool call request
// - do nothing
//
// The actual types are not exported, and the only way to get a value of a specific type
// is to use a type assertion.
//
// Technical note: it might be tempting to build this into toolMiddlewareConfig, but this
// would leave out the case in which the request is malformed, scenario that does not
// belong to the logic implementing config.
type toolCallFixAction interface{}

// toolCallFilter is a struct that represents a tool call filter, i.e.
// the tool call request is not allowed.
type toolCallFilter struct{}

// toolCallOverride is a struct that represents a tool call override, i.e.
// the tool call request is allowed, but the tool name is overridden.
type toolCallOverride struct {
	actualName string
}

// Name returns the actual name of the tool.
func (t *toolCallOverride) Name() string {
	return t.actualName
}

// toolCallBogus is a struct that represents a bogus tool call request, i.e.
// the tool call request is not allowed and the tool name is not overridden.
type toolCallBogus struct{}

// toolCallNoAction is a struct that represents a tool call no action, i.e.
// the tool call request is allowed and the tool name is not overridden.
type toolCallNoAction struct{}

// processToolCallRequest processes a tool call request checking if the tool
// is in the filter list. Note that the tool name received in the toolCallRequest
// is going to be the user-provided name, which might be different from the actual
// tool name.
func processToolCallRequest(
	config *toolMiddlewareConfig,
	toolCallRequest toolCallRequest,
) toolCallFixAction {
	// NOTE: the spec does not allow for nil params.
	if toolCallRequest.Params == nil {
		return &toolCallBogus{}
	}

	// NOTE: the spec does not allow for name to be missing.
	toolName, ok := (*toolCallRequest.Params)["name"].(string)
	if !ok {
		return &toolCallBogus{}
	}

	// NOTE: the spec does not allow for empty tool names.
	if toolName == "" {
		return &toolCallBogus{}
	}

	// If the tool is not in the filter list, return an error.
	// Note that the tool name we use here is the user-provided name, which
	// might be different from the actual tool name, but filters are expressed
	// in terms of tool names as known to the user, so this is correct.
	if !config.isToolInFilter(toolName) {
		return &toolCallFilter{}
	}

	// If the tool is allowed by the filter, and has an override, return the
	// actual name to fix the tool call request.
	if actualName, ok := config.getToolCallActualName(toolName); ok {
		return &toolCallOverride{actualName: actualName}
	}

	// If the tool is allowed by the filter, and does not have an override,
	// return an empty string and no error, signaling the fact that the tool
	// call request is ok as is.
	return &toolCallNoAction{}
}

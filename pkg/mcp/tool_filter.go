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
var errToolNotInFilter = errors.New("tool not in filter")
var errBug = errors.New("there's a bug")

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

// ToolMiddlewareOption is a function that can be used to configure the tool
// middleware.
type ToolMiddlewareOption func(*toolMiddlewareConfig) error

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
				filterTools:    config.filterTools,
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
			if err == nil && toolCallRequest.Params != nil && toolCallRequest.Method == "tools/call" {
				err = processToolCallRequest(config.filterTools, toolCallRequest)

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
				if errors.Is(err, errToolNotInFilter) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if err != nil {
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
	buffer      []byte
	filterTools map[string]struct{}
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
		if err := processBuffer(rw.filterTools, rw.buffer, mimeType, &b); err != nil {
			logger.Errorf("Error flushing response: %v", err)
		}

		_, err := rw.ResponseWriter.Write(b.Bytes())
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
func processBuffer(filterTools map[string]struct{}, buffer []byte, mimeType string, w io.Writer) error {
	if len(buffer) == 0 {
		return nil
	}

	switch mimeType {
	case "application/json":
		var toolsListResponse toolsListResponse
		err := json.Unmarshal(buffer, &toolsListResponse)
		if err == nil && toolsListResponse.Result.Tools != nil {
			return processToolsListResponse(filterTools, toolsListResponse, w)
		}
	case "text/event-stream":
		return processSSEEvents(filterTools, buffer, w)
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

	return fmt.Errorf("%w: tool filtering middleware", errBug)
}

//nolint:gocyclo
func processSSEEvents(filterTools map[string]struct{}, buffer []byte, w io.Writer) error {
	var linesep []byte
	if bytes.Contains(buffer, []byte("\r\n")) {
		linesep = []byte("\r\n")
	} else if bytes.Contains(buffer, []byte("\n")) {
		linesep = []byte("\n")
	} else if bytes.Contains(buffer, []byte("\r")) {
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
					return fmt.Errorf("%w: %v", errBug, err)
				}

				if err := processToolsListResponse(filterTools, toolsListResponse, w); err != nil {
					return err
				}
				written = true
			}
		}

		if !written {
			_, err := w.Write(line)
			if err != nil {
				return fmt.Errorf("%w: %v", errBug, err)
			}
		}

		_, err := w.Write(linesep)
		if err != nil {
			return fmt.Errorf("%w: %v", errBug, err)
		}
		linesepCount++
	}

	// This ensures we don't send too few line separators, which might break
	// SSE parsing.
	if linesepCount < linesepTotal {
		_, err := w.Write(linesep)
		if err != nil {
			return fmt.Errorf("%w: %v", errBug, err)
		}
	}

	return nil
}

// processToolsListResponse processes a tools list response filtering out
// tools that are not in the filter list.
func processToolsListResponse(filterTools map[string]struct{}, toolsListResponse toolsListResponse, w io.Writer) error {
	filteredTools := []map[string]any{}
	for _, tool := range *toolsListResponse.Result.Tools {
		toolName, ok := tool["name"].(string)
		if !ok {
			return errToolNameNotFound
		}

		if isToolInFilter(filterTools, toolName) {
			filteredTools = append(filteredTools, tool)
		}
	}

	toolsListResponse.Result.Tools = &filteredTools
	if err := json.NewEncoder(w).Encode(toolsListResponse); err != nil {
		return fmt.Errorf("%w: %v", errBug, err)
	}

	return nil
}

// processToolCallRequest processes a tool call request checking if the tool
// is in the filter list.
func processToolCallRequest(filterTools map[string]struct{}, toolCallRequest toolCallRequest) error {
	toolName, ok := (*toolCallRequest.Params)["name"].(string)
	if !ok {
		return errToolNameNotFound
	}

	if isToolInFilter(filterTools, toolName) {
		return nil
	}

	return errToolNotInFilter
}

// isToolInFilter checks if a tool name is in the filter
func isToolInFilter(filterTools map[string]struct{}, toolName string) bool {
	_, ok := filterTools[toolName]
	return ok
}

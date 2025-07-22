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

// NewToolFilterMiddleware creates an HTTP middleware that parses SSE responses
// to extract tool names from JSON-RPC messages containing tool lists.
//
// The middleware looks for SSE events with:
// - event: message
// - data: {"jsonrpc":"2.0","id":X,"result":{"tools":[...]}}
//
// When it finds such messages, it prints the name of each tool in the list.
// If filterTools is provided, only tools in that list will be logged.
// If filterTools is nil or empty, all tools will be logged.
func NewToolFilterMiddleware(filterTools []string) (types.Middleware, error) {
	if len(filterTools) == 0 {
		return nil, fmt.Errorf("tools list for filtering is empty")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Create a response writer that captures SSE responses
			rw := &toolFilterWriter{
				ResponseWriter: w,
				filterTools:    filterTools,
			}

			// Call the next handler
			next.ServeHTTP(rw, r)
		})
	}, nil
}

// toolFilterWriter wraps http.ResponseWriter to capture and process SSE responses
type toolFilterWriter struct {
	http.ResponseWriter
	buffer      []byte
	filterTools []string
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
	} `json:"result,omitempty"`
}

type toolCallRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  *map[string]any `json:"params,omitempty"`
}

// processSSEBuffer processes any complete SSE events in the buffer
func processBuffer(filterTools []string, buffer []byte, mimeType string, w io.Writer) error {
	if len(buffer) == 0 {
		return nil
	}

	switch mimeType {
	case "application/json":
		var toolsListResponse toolsListResponse
		if err := json.Unmarshal(buffer, &toolsListResponse); err == nil && toolsListResponse.Result.Tools != nil {
			return processToolsListResponse(filterTools, toolsListResponse, w)
		}
		var toolCallRequest toolCallRequest
		if err := json.Unmarshal(buffer, &toolCallRequest); err == nil && toolCallRequest.Params != nil {
			if toolCallRequest.Method == "tool/call" {
				return processToolCallRequest(filterTools, toolCallRequest, w)
			}
		}
	case "text/event-stream":
		return processSSEEvents(filterTools, buffer, w)
	default:
		return fmt.Errorf("unsupported mime type: %s", mimeType)
	}

	return fmt.Errorf("%w: tool filtering middleware", errBug)
}

//nolint:gocyclo
func processSSEEvents(filterTools []string, buffer []byte, w io.Writer) error {
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
				if err := processToolsListResponse(filterTools, toolsListResponse, w); err != nil {
					return err
				}
				written = true
			}
			var toolCallRequest toolCallRequest
			if err := json.Unmarshal(data, &toolCallRequest); err == nil && toolCallRequest.Params != nil {
				if toolCallRequest.Method == "tool/call" {
					if err := processToolCallRequest(filterTools, toolCallRequest, w); err != nil {
						return err
					}
					written = true
				}
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

	if linesepCount < linesepTotal {
		_, err := w.Write(linesep)
		if err != nil {
			return fmt.Errorf("%w: %v", errBug, err)
		}
	}

	return nil
}

func processToolsListResponse(filterTools []string, toolsListResponse toolsListResponse, w io.Writer) error {
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

func processToolCallRequest(filterTools []string, toolCallRequest toolCallRequest, w io.Writer) error {
	toolName, ok := (*toolCallRequest.Params)["name"].(string)
	if !ok {
		return errToolNameNotFound
	}

	if isToolInFilter(filterTools, toolName) {
		if err := json.NewEncoder(w).Encode(toolCallRequest); err != nil {
			return fmt.Errorf("%w: %v", errBug, err)
		}
		return nil
	}

	return errToolNotInFilter
}

// isToolInFilter checks if a tool name is in the filter list
func isToolInFilter(filterTools []string, toolName string) bool {
	for _, filterTool := range filterTools {
		if filterTool == toolName {
			return true
		}
	}
	return false
}

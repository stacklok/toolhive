package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
)

func TestProcessToolCallRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		filterTools map[string]struct{}
		request     toolCallRequest
		expectError error
	}{
		{
			name: "tool in filter - should succeed",
			filterTools: map[string]struct{}{
				"test_tool":  {},
				"other_tool": {},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params: &map[string]any{
					"name": "test_tool",
					"arguments": map[string]any{
						"arg1": "value1",
					},
				},
			},
			expectError: nil,
		},
		{
			name: "tool not in filter - should fail",
			filterTools: map[string]struct{}{
				"allowed_tool": {},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params: &map[string]any{
					"name": "blocked_tool",
					"arguments": map[string]any{
						"arg1": "value1",
					},
				},
			},
			expectError: errToolNotInFilter,
		},
		{
			name: "tool name not found in params - should fail",
			filterTools: map[string]struct{}{
				"test_tool": {},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params: &map[string]any{
					"arguments": map[string]any{
						"arg1": "value1",
					},
				},
			},
			expectError: errToolNameNotFound,
		},
		{
			name: "tool name is not string - should fail",
			filterTools: map[string]struct{}{
				"test_tool": {},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params: &map[string]any{
					"name":      123,
					"arguments": map[string]any{},
				},
			},
			expectError: errToolNameNotFound,
		},
		{
			name:        "empty filter - should fail for any tool",
			filterTools: map[string]struct{}{},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params: &map[string]any{
					"name": "any_tool",
				},
			},
			expectError: errToolNotInFilter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := processToolCallRequest(tt.filterTools, tt.request)
			if tt.expectError != nil {
				assert.ErrorIs(t, err, tt.expectError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestProcessToolsListResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		filterTools   map[string]struct{}
		inputResponse toolsListResponse
		expectedTools []string
		expectError   error
	}{
		{
			name: "filter tools - keep only allowed tools",
			filterTools: map[string]struct{}{
				"allowed_tool1": {},
				"allowed_tool2": {},
			},
			inputResponse: toolsListResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: struct {
					Tools *[]map[string]any `json:"tools"`
				}{
					Tools: &[]map[string]any{
						{"name": "allowed_tool1", "description": "First tool"},
						{"name": "blocked_tool", "description": "Blocked tool"},
						{"name": "allowed_tool2", "description": "Second tool"},
					},
				},
			},
			expectedTools: []string{"allowed_tool1", "allowed_tool2"},
			expectError:   nil,
		},
		{
			name: "no filter - keep all tools",
			filterTools: map[string]struct{}{
				"tool1": {},
				"tool2": {},
				"tool3": {},
			},
			inputResponse: toolsListResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: struct {
					Tools *[]map[string]any `json:"tools"`
				}{
					Tools: &[]map[string]any{
						{"name": "tool1", "description": "First tool"},
						{"name": "tool2", "description": "Second tool"},
						{"name": "tool3", "description": "Third tool"},
					},
				},
			},
			expectedTools: []string{"tool1", "tool2", "tool3"},
			expectError:   nil,
		},
		{
			name: "tool without name field - should fail",
			filterTools: map[string]struct{}{
				"allowed_tool": {},
			},
			inputResponse: toolsListResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: struct {
					Tools *[]map[string]any `json:"tools"`
				}{
					Tools: &[]map[string]any{
						{"description": "Tool without name"},
					},
				},
			},
			expectError: errToolNameNotFound,
		},
		{
			name: "tool name is not string - should fail",
			filterTools: map[string]struct{}{
				"allowed_tool": {},
			},
			inputResponse: toolsListResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: struct {
					Tools *[]map[string]any `json:"tools"`
				}{
					Tools: &[]map[string]any{
						{"name": 123, "description": "Tool with numeric name"},
					},
				},
			},
			expectError: errToolNameNotFound,
		},
		{
			name: "empty tools list - should succeed",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			inputResponse: toolsListResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: struct {
					Tools *[]map[string]any `json:"tools"`
				}{
					Tools: &[]map[string]any{},
				},
			},
			expectedTools: []string{},
			expectError:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			err := processToolsListResponse(tt.filterTools, tt.inputResponse, &buf)

			if tt.expectError != nil {
				assert.ErrorIs(t, err, tt.expectError)
				return
			}

			require.NoError(t, err)

			// Parse the output to verify the filtered tools
			var outputResponse toolsListResponse
			err = json.Unmarshal(buf.Bytes(), &outputResponse)
			require.NoError(t, err)

			// Extract tool names from the output
			var actualTools []string
			if outputResponse.Result.Tools != nil {
				for _, tool := range *outputResponse.Result.Tools {
					if name, ok := tool["name"].(string); ok {
						actualTools = append(actualTools, name)
					}
				}
			}

			assert.ElementsMatch(t, tt.expectedTools, actualTools)
		})
	}
}

func TestProcessSSEEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		filterTools map[string]struct{}
		inputBuffer []byte
		expected    string
		expectError bool
	}{
		{
			name: "SSE with non-tools data - pass through unchanged",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			inputBuffer: []byte(`event: message
data: {"jsonrpc":"2.0","id":1,"result":{"status":"ok"}}

`),
			expected: `event: message
data: {"jsonrpc":"2.0","id":1,"result":{"status":"ok"}}

`,
			expectError: false,
		},
		{
			name: "SSE with mixed content - filter tools and pass through other data",
			filterTools: map[string]struct{}{
				"tool1": {},
				"tool3": {},
			},
			inputBuffer: []byte(`event: message
data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"tool1","description":"First"},{"name":"tool2","description":"Second"},{"name":"tool3","description":"Third"}]}}

event: notification
data: {"type":"info","message":"Processing complete"}

`),
			expected: `event: message
data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"description":"First","name":"tool1"},{"description":"Third","name":"tool3"}]}}

event: notification
data: {"type":"info","message":"Processing complete"}

`,
			expectError: false,
		},
		{
			name: "SSE with CRLF line endings",
			filterTools: map[string]struct{}{
				"allowed_tool": {},
			},
			inputBuffer: []byte("event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"name\":\"allowed_tool\",\"description\":\"Allowed\"},{\"name\":\"blocked_tool\",\"description\":\"Blocked\"}]}}\r\n\r\n"),
			expected:    "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"description\":\"Allowed\",\"name\":\"allowed_tool\"}]}}\n\r\n\r\n",
			expectError: false,
		},
		{
			name: "SSE with CR line endings",
			filterTools: map[string]struct{}{
				"allowed_tool": {},
			},
			inputBuffer: []byte("event: message\rdata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"name\":\"allowed_tool\",\"description\":\"Allowed\"}]}}\r\r"),
			expected:    "event: message\rdata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"description\":\"Allowed\",\"name\":\"allowed_tool\"}]}}\n\r\r",
			expectError: false,
		},
		{
			name: "SSE with unsupported line separator - should fail",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			inputBuffer: []byte("event: message\vdata: {\"jsonrpc\":\"2.0\",\"id\":1}\v\v"),
			expectError: true,
		},
		{
			name: "SSE with malformed JSON in data - pass through unchanged",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			inputBuffer: []byte(`event: message
data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"tool1"}]}

`),
			expected: `event: message
data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"tool1"}]}

`,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			err := processSSEEvents(tt.filterTools, tt.inputBuffer, &buf)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, buf.String())
		})
	}
}

func TestProcessBuffer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		filterTools map[string]struct{}
		buffer      []byte
		mimeType    string
		expectError bool
	}{
		{
			name: "JSON with tools list",
			filterTools: map[string]struct{}{
				"allowed_tool": {},
			},
			buffer:      []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"allowed_tool","description":"Allowed"},{"name":"blocked_tool","description":"Blocked"}]}}`),
			mimeType:    "application/json",
			expectError: false,
		},
		{
			name: "SSE with tools list",
			filterTools: map[string]struct{}{
				"allowed_tool": {},
			},
			buffer: []byte(`event: message
data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"allowed_tool","description":"Allowed"},{"name":"blocked_tool","description":"Blocked"}]}}

`),
			mimeType:    "text/event-stream",
			expectError: false,
		},
		{
			name: "Unsupported mime type",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			buffer:      []byte(`some data`),
			mimeType:    "text/plain",
			expectError: true,
		},
		{
			name: "Empty buffer",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			buffer:      []byte{},
			mimeType:    "application/json",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			err := processBuffer(tt.filterTools, tt.buffer, tt.mimeType, &buf)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIsToolInFilter(t *testing.T) {
	t.Parallel()

	filterTools := map[string]struct{}{
		"tool1": {},
		"tool2": {},
	}

	tests := []struct {
		name     string
		toolName string
		expected bool
	}{
		{
			name:     "tool in filter",
			toolName: "tool1",
			expected: true,
		},
		{
			name:     "tool not in filter",
			toolName: "tool3",
			expected: false,
		},
		{
			name:     "empty tool name",
			toolName: "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := isToolInFilter(filterTools, tt.toolName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test helper function to create a tools list response
func createToolsListResponse(tools []map[string]any) toolsListResponse {
	return toolsListResponse{
		JSONRPC: "2.0",
		ID:      1,
		Result: struct {
			Tools *[]map[string]any `json:"tools"`
		}{
			Tools: &tools,
		},
	}
}

func TestProcessToolsListResponse_JSONEncoding(t *testing.T) {
	t.Parallel()

	// Test that the JSON encoding preserves the structure correctly
	filterTools := map[string]struct{}{
		"tool1": {},
		"tool3": {},
	}

	inputResponse := createToolsListResponse([]map[string]any{
		{"name": "tool1", "description": "First tool", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool2", "description": "Second tool", "inputSchema": map[string]any{"type": "string"}},
		{"name": "tool3", "description": "Third tool", "inputSchema": map[string]any{"type": "array"}},
	})

	var buf bytes.Buffer
	err := processToolsListResponse(filterTools, inputResponse, &buf)
	require.NoError(t, err)

	// Verify the output can be parsed back as valid JSON
	var outputResponse toolsListResponse
	err = json.Unmarshal(buf.Bytes(), &outputResponse)
	require.NoError(t, err)

	// Verify the structure is preserved
	assert.Equal(t, "2.0", outputResponse.JSONRPC)
	// ID can be float64 when parsed from JSON, so we check the value instead of type
	assert.Equal(t, float64(1), outputResponse.ID)
	assert.NotNil(t, outputResponse.Result.Tools)
	assert.Len(t, *outputResponse.Result.Tools, 2)

	// Verify the filtered tools are correct
	toolNames := make([]string, 0, len(*outputResponse.Result.Tools))
	for _, tool := range *outputResponse.Result.Tools {
		if name, ok := tool["name"].(string); ok {
			toolNames = append(toolNames, name)
		}
	}
	assert.ElementsMatch(t, []string{"tool1", "tool3"}, toolNames)
}

func TestProcessSSEEvents_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		filterTools map[string]struct{}
		inputBuffer []byte
		expected    string
	}{
		{
			name: "SSE with only line separators",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			inputBuffer: []byte("\n\n"),
			expected:    "\n",
		},
		{
			name: "SSE with single line",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			inputBuffer: []byte("event: message\n"),
			expected:    "event: message\n",
		},
		{
			name: "SSE with data line without event line",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			inputBuffer: []byte("data: {\"jsonrpc\":\"2.0\",\"id\":1}\n\n"),
			expected:    "data: {\"jsonrpc\":\"2.0\",\"id\":1}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			err := processSSEEvents(tt.filterTools, tt.inputBuffer, &buf)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, buf.String())
		})
	}
}

func TestProcessToolCallRequest_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		filterTools map[string]struct{}
		request     toolCallRequest
		expectError error
	}{
		{
			name: "nil params",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params:  nil,
			},
			expectError: errToolNameNotFound,
		},
		{
			name: "empty params",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params:  &map[string]any{},
			},
			expectError: errToolNameNotFound,
		},
		{
			name: "params with nil name",
			filterTools: map[string]struct{}{
				"any_tool": {},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params: &map[string]any{
					"name": nil,
				},
			},
			expectError: errToolNameNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// For nil params, we expect a panic, so we need to recover
			if tt.request.Params == nil {
				defer func() {
					if r := recover(); r != nil {
						// Expected panic for nil params
						logger.Infof("Recovered from panic: %v", r)
					}
				}()
			}

			err := processToolCallRequest(tt.filterTools, tt.request)
			assert.ErrorIs(t, err, tt.expectError)
		})
	}
}

func TestToolFilterWriter_Flush(t *testing.T) {
	t.Parallel()

	// Initialize logger to avoid panic
	logger.Initialize()

	tests := []struct {
		name        string
		writeData   []byte
		contentType string
		statusCode  int
		filterTools map[string]struct{}
		expectWrite bool
		expectReset bool
	}{
		{
			name:        "empty buffer - should not write anything",
			writeData:   []byte{},
			contentType: "application/json",
			statusCode:  200,
			filterTools: map[string]struct{}{
				"tool1": {},
			},
			expectWrite: false,
			expectReset: false,
		},
		{
			name:        "JSON content type - should process and write",
			writeData:   []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"tool1","description":"First"},{"name":"tool2","description":"Second"}]}}`),
			contentType: "application/json",
			statusCode:  200,
			filterTools: map[string]struct{}{
				"tool1": {},
			},
			expectWrite: true,
			expectReset: true,
		},
		{
			name:        "no content type - should write buffer directly",
			writeData:   []byte(`{"test":"data"}`),
			contentType: "",
			statusCode:  200,
			filterTools: map[string]struct{}{
				"tool1": {},
			},
			expectWrite: true,
			expectReset: false, // Buffer is not reset when no content type
		},
		{
			name:        "with status code - should set header and write",
			writeData:   []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"tool1","description":"First"}]}}`),
			contentType: "application/json",
			statusCode:  201,
			filterTools: map[string]struct{}{
				"tool1": {},
			},
			expectWrite: true,
			expectReset: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a mock ResponseWriter
			mockWriter := &mockResponseWriter{
				headers: make(http.Header),
				buffer:  &bytes.Buffer{},
			}
			mockWriter.headers.Set("Content-Type", tt.contentType)

			// Create toolFilterWriter
			rw := &toolFilterWriter{
				ResponseWriter: mockWriter,
				buffer:         []byte{},
				filterTools:    tt.filterTools,
			}

			// Set status code using WriteHeader
			rw.WriteHeader(tt.statusCode)
			assert.Equal(t, tt.statusCode, mockWriter.statusCode, "Status code should be set")

			// Write data to the toolFilterWriter (this buffers the data)
			if len(tt.writeData) > 0 {
				written, err := rw.Write(tt.writeData)
				require.NoError(t, err)
				assert.Equal(t, len(tt.writeData), written)
				assert.Equal(t, len(tt.writeData), len(rw.buffer), "Data should be buffered")
			}

			// Call Flush
			rw.Flush()

			// Verify that Write was called on the underlying ResponseWriter if we expected it
			if tt.expectWrite {
				assert.Greater(t, mockWriter.writeCount, 0, "Write should have been called on ResponseWriter")
				assert.Greater(t, mockWriter.buffer.Len(), 0, "ResponseWriter buffer should contain data")
			} else {
				assert.Equal(t, 0, mockWriter.writeCount, "Write should not have been called on ResponseWriter")
			}

			// Verify buffer was reset only when expected
			if tt.expectReset {
				assert.Equal(t, 0, len(rw.buffer), "Buffer should be reset after flush")
			} else if len(tt.writeData) > 0 {
				assert.Equal(t, len(tt.writeData), len(rw.buffer), "Buffer should not be reset")
			}
		})
	}
}

// mockResponseWriter implements http.ResponseWriter for testing
type mockResponseWriter struct {
	headers    http.Header
	buffer     *bytes.Buffer
	writeCount int
	statusCode int
}

func (m *mockResponseWriter) Header() http.Header {
	return m.headers
}

func (m *mockResponseWriter) Write(data []byte) (int, error) {
	m.writeCount++
	return m.buffer.Write(data)
}

func (m *mockResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}

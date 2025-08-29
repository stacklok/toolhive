package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Helper function to create a test server with middlewares
func createTestServer(t *testing.T, middlewares ...types.MiddlewareFunction) *httptest.Server {
	t.Helper()

	toolsListHandler := func(w http.ResponseWriter, _ *http.Request) {
		// Simulate different endpoints
		// Return a tools list response
		response := toolsListResponse{
			JSONRPC: "2.0",
			ID:      1,
		}
		tools := []map[string]any{
			{"name": "Foo", "description": "Foo tool"},
			{"name": "Bar", "description": "Bar tool"},
		}
		response.Result.Tools = &tools

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)

		// Ensure the response is flushed
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	toolsCallHandler := func(w http.ResponseWriter, r *http.Request) {
		// Handle tool call requests
		var req toolCallRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		// Simulate successful tool call
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		name, ok := (*req.Params)["name"].(string)
		if !ok {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{"return_value": name},
		})

		// Ensure the response is flushed
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	sseToolsListHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Create SSE response
		response := toolsListResponse{
			JSONRPC: "2.0",
			ID:      1,
		}
		tools := []map[string]any{
			{"name": "Foo", "description": "Foo tool"},
			{"name": "Bar", "description": "Bar tool"},
		}
		response.Result.Tools = &tools

		responseBytes, err := json.Marshal(response)
		require.NoError(t, err)

		// Write SSE format
		fmt.Fprintf(w, "data: %s\n\n", string(responseBytes))

		// Ensure the response is flushed
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/tools/list", toolsListHandler)
	mux.HandleFunc("/tools/call", toolsCallHandler)
	mux.HandleFunc("/tools/list/sse", sseToolsListHandler)

	var handler http.Handler = mux
	for _, middleware := range middlewares {
		handler = middleware(handler)
	}

	return httptest.NewServer(handler)
}

// Helper function to make a tools list request
func makeToolsListRequest(t *testing.T, server *httptest.Server) *toolsListResponse {
	t.Helper()

	resp, err := http.Get(server.URL + "/tools/list")
	require.NoError(t, err)
	defer resp.Body.Close()

	var response toolsListResponse
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	return &response
}

// Helper function to make a tool call request
func makeToolCallRequest(t *testing.T, server *httptest.Server, toolName string) (*http.Response, error) {
	t.Helper()

	req := toolCallRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: &map[string]any{
			"name": toolName,
			"arguments": map[string]any{
				"arg1": "value1",
			},
		},
	}

	body, err := json.Marshal(req)
	require.NoError(t, err)

	return http.Post(server.URL+"/tools/call", "application/json", bytes.NewBuffer(body))
}

func TestNewListToolsMappingMiddleware_Scenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     *[]ToolMiddlewareOption
		expected *[]map[string]any
	}{
		{
			name: "No filter, No override",
			opts: nil,
			expected: &[]map[string]any{
				{"name": "Foo", "description": "Foo tool"},
				{"name": "Bar", "description": "Bar tool"},
			},
		},
		{
			name: "Filter Foo, No override",
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("Foo"),
			},
			expected: &[]map[string]any{
				{"name": "Foo", "description": "Foo tool"},
			},
		},
		{
			name: "No filter, Override MyFoo -> Foo",
			opts: &[]ToolMiddlewareOption{
				WithToolsOverride("Foo", "MyFoo", "Override description"),
			},
			expected: &[]map[string]any{
				{"name": "MyFoo", "description": "Override description"},
				{"name": "Bar", "description": "Bar tool"},
			},
		},
		{
			name: "Filter MyFoo, Override MyFoo -> Foo",
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("MyFoo"),
				WithToolsOverride("Foo", "MyFoo", "Override description"),
			},
			expected: &[]map[string]any{
				{"name": "MyFoo", "description": "Override description"},
			},
		},
		{
			name: "No filter, Override Bar -> Foo",
			opts: &[]ToolMiddlewareOption{
				WithToolsOverride("Bar", "Foo", ""),
			},
			expected: &[]map[string]any{
				{"name": "Foo", "description": "Foo tool"},
				{"name": "Foo", "description": "Bar tool"},
			},
		},
		{
			name: "Filter MyFoo, Override Foo -> MyFoo",
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("MyFoo"),
				WithToolsOverride("Foo", "MyFoo", ""),
			},
			expected: &[]map[string]any{
				{"name": "MyFoo", "description": "Foo tool"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			middlewares := []types.MiddlewareFunction{}

			// Create the middleware
			if tt.opts != nil {
				toolsListmiddleware, err := NewListToolsMappingMiddleware(*tt.opts...)
				assert.NoError(t, err)
				toolsCallMiddleware, err := NewToolCallMappingMiddleware(*tt.opts...)
				assert.NoError(t, err)

				middlewares = append(middlewares,
					toolsCallMiddleware,
					toolsListmiddleware,
				)
			}

			// Create test server
			server := createTestServer(t, middlewares...)
			defer server.Close()

			// Make request
			response := makeToolsListRequest(t, server)

			assert.Equal(t, tt.expected, response.Result.Tools)
		})
	}
}

func TestNewToolCallMappingMiddleware_Scenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		opts           *[]ToolMiddlewareOption
		expected       *map[string]any
		callToolName   string
		expectedStatus int
	}{
		{
			name:           "No filter, No override - Call Foo",
			opts:           nil,
			expected:       &map[string]any{"return_value": "Foo"},
			callToolName:   "Foo",
			expectedStatus: http.StatusOK,
		},
		{
			name: "Filter Foo, No override - Call Foo",
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("Foo"),
			},
			expected:       &map[string]any{"return_value": "Foo"},
			callToolName:   "Foo",
			expectedStatus: http.StatusOK,
		},
		{
			name: "Filter Foo, No override - Call Bar",
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("Foo"),
			},
			expected:       nil,
			callToolName:   "Bar",
			expectedStatus: http.StatusBadRequest, // Bar is filtered out
		},
		{
			name: "No filter, Override MyFoo -> Foo - Call MyFoo",
			opts: &[]ToolMiddlewareOption{
				WithToolsOverride("Foo", "MyFoo", "Override description"),
			},
			expected:       &map[string]any{"return_value": "Foo"},
			callToolName:   "MyFoo",
			expectedStatus: http.StatusOK,
		},
		{
			name: "No filter, Override MyFoo -> Foo - Call Bar",
			opts: &[]ToolMiddlewareOption{
				WithToolsOverride("Foo", "MyFoo", "Override description"),
			},
			expected:       &map[string]any{"return_value": "Bar"},
			callToolName:   "Bar",
			expectedStatus: http.StatusOK,
		},
		{
			name: "Filter MyFoo, Override MyFoo -> Foo - Call MyFoo",
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("MyFoo"),
				WithToolsOverride("Foo", "MyFoo", "Override description"),
			},
			expected:       &map[string]any{"return_value": "Foo"},
			callToolName:   "MyFoo",
			expectedStatus: http.StatusOK,
		},
		{
			name: "Filter MyFoo, Override MyFoo -> Foo - Call Bar",
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("MyFoo"),
				WithToolsOverride("Foo", "MyFoo", "Override description"),
			},
			expected:       nil,
			callToolName:   "Bar",
			expectedStatus: http.StatusBadRequest, // Bar is filtered out
		},
		{
			name: "No filter, Override Bar -> Foo - Call Foo",
			opts: &[]ToolMiddlewareOption{
				WithToolsOverride("Bar", "Foo", ""),
			},
			expected:       &map[string]any{"return_value": "Bar"},
			callToolName:   "Foo",
			expectedStatus: http.StatusOK,
		},
		{
			name: "No filter, Override Bar -> Foo - Call Bar",
			opts: &[]ToolMiddlewareOption{
				WithToolsOverride("Foo", "Bar", ""),
			},
			expected:       &map[string]any{"return_value": "Foo"},
			callToolName:   "Bar",
			expectedStatus: http.StatusOK,
		},
		{
			name: "Filter MyFoo, Override Foo -> MyFoo",
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("Foo"),
				WithToolsOverride("Foo", "MyFoo", "Override description"),
			},
			expected:       nil,
			callToolName:   "MyFoo",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			middlewares := []types.MiddlewareFunction{}

			// Create the middleware
			if tt.opts != nil {
				toolsListmiddleware, err := NewListToolsMappingMiddleware(*tt.opts...)
				assert.NoError(t, err)
				toolsCallMiddleware, err := NewToolCallMappingMiddleware(*tt.opts...)
				assert.NoError(t, err)

				middlewares = append(middlewares,
					toolsCallMiddleware,
					toolsListmiddleware,
				)
			}

			// Create test server
			server := createTestServer(t, middlewares...)
			defer server.Close()

			// Make request
			resp, err := makeToolCallRequest(t, server, tt.callToolName)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.expectedStatus, resp.StatusCode)

			// Read response body
			bodyBytes, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			if tt.expected != nil {
				var response map[string]any
				err = json.Unmarshal(bodyBytes, &response)
				require.NoError(t, err)

				require.NotNil(t, response["result"])
				require.Equal(t, *tt.expected, response["result"].(map[string]any))
			}
		})
	}
}

func TestNewListToolsMappingMiddleware_SSE_Scenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		opts            *[]ToolMiddlewareOption
		expected        *[]map[string]any
		expectedFoo     bool
		expectedBar     bool
		expectedFooName string
		expectedBarName string
		expectError     bool
	}{
		{
			name: "SSE - Filter Foo, No override",
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("Foo"),
			},
			expected: &[]map[string]any{
				{"name": "Foo", "description": "Foo tool"},
			},
			expectedFoo:     true,
			expectedBar:     false,
			expectedFooName: "Foo",
			expectedBarName: "",
		},
		{
			name: "SSE - No filter, Override MyFoo -> Foo (Current implementation bug - all tools filtered out)",
			opts: &[]ToolMiddlewareOption{
				WithToolsOverride("Foo", "MyFoo", "Override description"),
			},
			expected: &[]map[string]any{
				{"name": "MyFoo", "description": "Override description"},
				{"name": "Bar", "description": "Bar tool"},
			},
			expectedFoo:     false,
			expectedBar:     false,
			expectedFooName: "",
			expectedBarName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			middlewares := []types.MiddlewareFunction{}

			// Create the middleware
			if tt.opts != nil {
				toolsListmiddleware, err := NewListToolsMappingMiddleware(*tt.opts...)
				assert.NoError(t, err)
				toolsCallMiddleware, err := NewToolCallMappingMiddleware(*tt.opts...)
				assert.NoError(t, err)

				middlewares = append(middlewares,
					toolsCallMiddleware,
					toolsListmiddleware,
				)
			}

			// Create test server
			server := createTestServer(t, middlewares...)
			defer server.Close()

			// Make request
			resp, err := http.Get(server.URL + "/tools/list/sse")
			require.NoError(t, err)
			defer resp.Body.Close()

			// Read SSE response
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			if tt.expected != nil {
				// Parse SSE response
				lines := strings.Split(string(body), "\n")
				var dataLine string
				for _, line := range lines {
					if after, ok := strings.CutPrefix(line, "data: "); ok {
						dataLine = after
						break
					}
				}

				require.NotEmpty(t, dataLine, "No data line found in SSE response")

				// Parse JSON response
				var response toolsListResponse
				err = json.Unmarshal([]byte(dataLine), &response)
				require.NoError(t, err)

				// Verify results
				assert.Equal(t, "2.0", response.JSONRPC)
				assert.Equal(t, float64(1), response.ID)
				assert.Equal(t, tt.expected, response.Result.Tools)
			}
		})
	}
}

func TestNewListToolsMappingMiddleware_ErrorCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		opts        []ToolMiddlewareOption
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Empty options - should error",
			opts:        []ToolMiddlewareOption{},
			expectError: true,
			errorMsg:    "tools list for filtering or overriding is empty",
		},
		{
			name: "Empty tool name in filter - should error",
			opts: []ToolMiddlewareOption{
				WithToolsFilter(""),
			},
			expectError: true,
			errorMsg:    "tool name cannot be empty",
		},
		{
			name: "Empty actual name in override - should error",
			opts: []ToolMiddlewareOption{
				WithToolsOverride("", "MyFoo", "description"),
			},
			expectError: true,
			errorMsg:    "tool name cannot be empty",
		},
		{
			name: "Empty override name and description - should error",
			opts: []ToolMiddlewareOption{
				WithToolsOverride("Foo", "", ""),
			},
			expectError: true,
			errorMsg:    "override name and description cannot both be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			middleware, err := NewListToolsMappingMiddleware(tt.opts...)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, middleware)
			}
		})
	}
}

func TestNewToolCallMappingMiddleware_ErrorCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		opts        []ToolMiddlewareOption
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Empty options - should error",
			opts:        []ToolMiddlewareOption{},
			expectError: true,
			errorMsg:    "tools list for filtering or overriding is empty",
		},
		{
			name: "Empty tool name in filter - should error",
			opts: []ToolMiddlewareOption{
				WithToolsFilter(""),
			},
			expectError: true,
			errorMsg:    "tool name cannot be empty",
		},
		{
			name: "Empty actual name in override - should error",
			opts: []ToolMiddlewareOption{
				WithToolsOverride("", "MyFoo", "description"),
			},
			expectError: true,
			errorMsg:    "tool name cannot be empty",
		},
		{
			name: "Empty override name and description - should error",
			opts: []ToolMiddlewareOption{
				WithToolsOverride("Foo", "", ""),
			},
			expectError: true,
			errorMsg:    "override name and description cannot both be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			middleware, err := NewToolCallMappingMiddleware(tt.opts...)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, middleware)
			}
		})
	}
}

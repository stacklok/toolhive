package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/testkit"
)

// Helper function to make a tools list request
func makeToolsListRequest(t *testing.T, url string) *toolsListResponse {
	t.Helper()

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}

	body, err := json.Marshal(req)
	require.NoError(t, err)

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	var response toolsListResponse
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	return &response
}

// Helper function to make a tool call request
func makeToolCallRequest(t *testing.T, url string, toolName string) (*http.Response, error) {
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

	return http.Post(url, "application/json", bytes.NewBuffer(body))
}

func TestNewListToolsMappingMiddleware_Scenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		serverOpts []testkit.TestMCPServerOption
		opts       *[]ToolMiddlewareOption
		expected   *[]map[string]any
	}{
		{
			name: "No filter, No override",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
			opts: nil,
			expected: &[]map[string]any{
				{"name": "Foo", "description": "Foo tool"},
				{"name": "Bar", "description": "Bar tool"},
			},
		},
		{
			name: "Filter Foo, No override",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("Foo"),
			},
			expected: &[]map[string]any{
				{"name": "Foo", "description": "Foo tool"},
			},
		},
		{
			name: "No filter, Override MyFoo -> Foo",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
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
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
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
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
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
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
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
			middlewares := []func(http.Handler) http.Handler{}

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
			serverOpts := append(tt.serverOpts, testkit.WithMiddlewares(middlewares...))
			server, err := testkit.NewStreamableTestServer(
				serverOpts...,
			)
			require.NoError(t, err)
			defer server.Close()

			// Make request
			response := makeToolsListRequest(t, server.URL+"/mcp-json")
			assert.Equal(t, tt.expected, response.Result.Tools)
		})
	}
}

func TestNewToolCallMappingMiddleware_Scenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		serverOpts     []testkit.TestMCPServerOption
		opts           *[]ToolMiddlewareOption
		expected       any
		callToolName   string
		expectedStatus int
	}{
		{
			name: "No filter, No override - Call Foo",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
			opts:           nil,
			expected:       "Foo",
			callToolName:   "Foo",
			expectedStatus: http.StatusOK,
		},
		{
			name: "Filter Foo, No override - Call Foo",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("Foo"),
			},
			expected:       "Foo",
			callToolName:   "Foo",
			expectedStatus: http.StatusOK,
		},
		{
			name: "Filter Foo, No override - Call Bar",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("Foo"),
			},
			expected:       nil,
			callToolName:   "Bar",
			expectedStatus: http.StatusBadRequest, // Bar is filtered out
		},
		{
			name: "No filter, Override MyFoo -> Foo - Call MyFoo",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
			opts: &[]ToolMiddlewareOption{
				WithToolsOverride("Foo", "MyFoo", "Override description"),
			},
			expected:       "Foo",
			callToolName:   "MyFoo",
			expectedStatus: http.StatusOK,
		},
		{
			name: "No filter, Override MyFoo -> Foo - Call Bar",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
			opts: &[]ToolMiddlewareOption{
				WithToolsOverride("Foo", "MyFoo", "Override description"),
			},
			expected:       "Bar",
			callToolName:   "Bar",
			expectedStatus: http.StatusOK,
		},
		{
			name: "Filter MyFoo, Override MyFoo -> Foo - Call MyFoo",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
			opts: &[]ToolMiddlewareOption{
				WithToolsFilter("MyFoo"),
				WithToolsOverride("Foo", "MyFoo", "Override description"),
			},
			expected:       "Foo",
			callToolName:   "MyFoo",
			expectedStatus: http.StatusOK,
		},
		{
			name: "Filter MyFoo, Override MyFoo -> Foo - Call Bar",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
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
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
			opts: &[]ToolMiddlewareOption{
				WithToolsOverride("Bar", "Foo", ""),
			},
			expected:       "Bar",
			callToolName:   "Foo",
			expectedStatus: http.StatusOK,
		},
		{
			name: "No filter, Override Bar -> Foo - Call Bar",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
			opts: &[]ToolMiddlewareOption{
				WithToolsOverride("Foo", "Bar", ""),
			},
			expected:       "Foo",
			callToolName:   "Bar",
			expectedStatus: http.StatusOK,
		},
		{
			name: "Filter MyFoo, Override Foo -> MyFoo",
			serverOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
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
			middlewares := []func(http.Handler) http.Handler{}

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
			serverOpts := append(tt.serverOpts, testkit.WithMiddlewares(middlewares...))
			server, err := testkit.NewStreamableTestServer(
				serverOpts...,
			)
			require.NoError(t, err)
			defer server.Close()

			// Make request
			resp, err := makeToolCallRequest(t, server.URL+"/mcp-json", tt.callToolName)
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

				result, ok := response["result"].(map[string]any)
				require.True(t, ok)
				require.NotNil(t, result["content"])
				require.Len(t, result["content"], 1)

				contents, ok := result["content"].([]any)
				require.True(t, ok)

				content, ok := contents[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "text", content["type"])
				require.Equal(t, tt.expected, content["text"])
			}
		})
	}
}

func TestNewListToolsMappingMiddleware_SSE_Scenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		serverOpts      []testkit.TestMCPServerOption
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
			serverOpts: []testkit.TestMCPServerOption{
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
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
			serverOpts: []testkit.TestMCPServerOption{
				testkit.WithTool("Foo", "Foo tool", func() string { return "Foo" }),
				testkit.WithTool("Bar", "Bar tool", func() string { return "Bar" }),
			},
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
			middlewares := []func(http.Handler) http.Handler{}

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
			serverOpts := append(tt.serverOpts, testkit.WithMiddlewares(middlewares...))
			server, err := testkit.NewSSETestServer(
				serverOpts...,
			)
			require.NoError(t, err)
			defer server.Close()

			var wg sync.WaitGroup

			wg.Add(1)
			go func() {
				defer wg.Done()

				// Make request
				resp, err := http.Get(server.URL + "/sse")
				require.NoError(t, err)
				defer resp.Body.Close()
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

				// Read SSE response
				if tt.expected != nil {
					bodyBytes, err := io.ReadAll(resp.Body)
					require.NoError(t, err)

					// Parse SSE response
					dataLine := sseParser(t, bodyBytes)
					require.NotEmpty(t, dataLine, "No data line found in SSE response: '%+v'", bodyBytes)

					// Parse JSON response
					var response toolsListResponse
					err = json.Unmarshal(dataLine, &response)
					require.NoError(t, err)

					// Verify results
					assert.Equal(t, "2.0", response.JSONRPC)
					assert.Equal(t, float64(1), response.ID)
					assert.Equal(t, tt.expected, response.Result.Tools)
				}
			}()

			wg.Add(1)
			go func() {
				defer wg.Done()

				commandResp, err := http.Post(server.URL+"/command", "application/json", bytes.NewBufferString(`{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}`))
				require.NoError(t, err)
				defer commandResp.Body.Close()

				respBody, err := io.ReadAll(commandResp.Body)
				require.NoError(t, err)
				require.Equal(t, http.StatusAccepted, commandResp.StatusCode)
				require.Equal(t, "Accepted", string(respBody))
			}()

			wg.Wait()
		})
	}
}

// Note: we might want to move some variation of this to testkit.
func sseParser(t *testing.T, body []byte) []byte {
	t.Helper()

	var result []byte

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Split(testkit.NewSplitSSE(testkit.LFSep))
	for scanner.Scan() {
		require.NoError(t, scanner.Err())
		for line := range strings.SplitSeq(scanner.Text(), "\n") {
			dataLine, ok := strings.CutPrefix(line, "data:")
			if !ok {
				continue
			}

			result = []byte(dataLine)
		}
	}

	return result
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

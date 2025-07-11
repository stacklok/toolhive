package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/logger"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

func init() {
	// Initialize logger for tests
	logger.Initialize()
}

func TestToolFilteringMiddleware(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		allowedTools  []string
		inputTools    []mcp.Tool
		expectedTools []mcp.Tool
	}{
		{
			name:         "no tools configured - allow all",
			allowedTools: []string{},
			inputTools: []mcp.Tool{
				{Name: "weather", Description: "Get weather info"},
				{Name: "calculator", Description: "Calculate math"},
			},
			expectedTools: []mcp.Tool{
				{Name: "weather", Description: "Get weather info"},
				{Name: "calculator", Description: "Calculate math"},
			},
		},
		{
			name:         "specific tools configured - filter",
			allowedTools: []string{"weather"},
			inputTools: []mcp.Tool{
				{Name: "weather", Description: "Get weather info"},
				{Name: "calculator", Description: "Calculate math"},
				{Name: "translator", Description: "Translate text"},
			},
			expectedTools: []mcp.Tool{
				{Name: "weather", Description: "Get weather info"},
			},
		},
		{
			name:         "multiple tools configured - filter",
			allowedTools: []string{"weather", "calculator"},
			inputTools: []mcp.Tool{
				{Name: "weather", Description: "Get weather info"},
				{Name: "calculator", Description: "Calculate math"},
				{Name: "translator", Description: "Translate text"},
			},
			expectedTools: []mcp.Tool{
				{Name: "weather", Description: "Get weather info"},
				{Name: "calculator", Description: "Calculate math"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create a test handler that returns the input tools
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// Create a tools list response
				result := mcp.ListToolsResult{
					Tools: tt.inputTools,
				}

				// Create JSON-RPC response
				response := &jsonrpc2.Response{
					ID: jsonrpc2.StringID("1"),
					Result: func() json.RawMessage {
						data, _ := json.Marshal(result)
						return data
					}(),
				}

				// Write response
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(response)
			})

			var authorizer *CedarAuthorizer
			var err error
			if len(tt.allowedTools) > 0 {
				authorizer, err = CreateToolFilteringAuthorizer(tt.allowedTools)
				require.NoError(t, err)
			} else {
				// When no tools specified, we can't create an authorizer - this should be handled by the caller
				// For this test, we'll use nil authorizer which means no filtering
				authorizer = nil
			}
			middleware := ToolFilteringMiddleware(authorizer)
			filteredHandler := middleware(handler)

			// Create a test request
			req, err := http.NewRequest("POST", "/messages", bytes.NewBuffer([]byte(`{
				"jsonrpc": "2.0",
				"id": "1",
				"method": "tools/list",
				"params": {}
			}`)))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")

			// Add parsed MCP request to context
			parsedRequest := &mcpparser.ParsedMCPRequest{
				ID:     "1",
				Method: string(mcp.MethodToolsList),
			}
			ctx := context.WithValue(req.Context(), mcpparser.MCPRequestContextKey, parsedRequest)

			// Add JWT claims to context for authorization
			claims := jwt.MapClaims{
				"sub":  "testuser",
				"name": "Test User",
				"role": "user",
			}
			ctx = context.WithValue(ctx, auth.ClaimsContextKey{}, claims)
			req = req.WithContext(ctx)

			// Create response recorder
			rr := httptest.NewRecorder()

			// Call the handler
			filteredHandler.ServeHTTP(rr, req)

			// Check response
			assert.Equal(t, http.StatusOK, rr.Code)

			// Parse the response
			var response jsonrpc2.Response
			err = json.Unmarshal(rr.Body.Bytes(), &response)
			require.NoError(t, err)

			// Parse the result
			var result mcp.ListToolsResult
			err = json.Unmarshal(response.Result, &result)
			require.NoError(t, err)

			// Check that the tools are filtered correctly
			assert.Equal(t, len(tt.expectedTools), len(result.Tools))

			for i, expectedTool := range tt.expectedTools {
				assert.Equal(t, expectedTool.Name, result.Tools[i].Name)
				assert.Equal(t, expectedTool.Description, result.Tools[i].Description)
			}
		})
	}
}

func TestToolFilteringMiddleware_NonToolsListRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		method       string
		params       map[string]any
		allowedTools []string
		expectedCode int
		expectedBody string
		description  string
	}{
		{
			name:         "tools/call request passes through unchanged when no authorizer",
			method:       string(mcp.MethodToolsCall),
			params:       map[string]any{"name": "weather"},
			allowedTools: []string{},
			expectedCode: http.StatusOK,
			expectedBody: "success",
			description:  "When no authorizer is configured, tools/call requests should pass through",
		},
		{
			name:         "tools/call request for allowed tool passes through",
			method:       string(mcp.MethodToolsCall),
			params:       map[string]any{"name": "weather"},
			allowedTools: []string{"weather"},
			expectedCode: http.StatusOK,
			expectedBody: "success",
			description:  "When tool is in allowed list, tools/call request should pass through",
		},
		{
			name:         "tools/call request for blocked tool is denied",
			method:       string(mcp.MethodToolsCall),
			params:       map[string]any{"name": "calculator"},
			allowedTools: []string{"weather"},
			expectedCode: http.StatusOK,
			expectedBody: `{"Result":null,"Error":{"code":-32602,"message":"Unauthorized"},"ID":{}}`,
			description:  "When tool is not in allowed list, tools/call request should be denied with Invalid Params error",
		},
		{
			name:         "tools/call request for multiple allowed tools passes through",
			method:       string(mcp.MethodToolsCall),
			params:       map[string]any{"name": "calculator"},
			allowedTools: []string{"weather", "calculator", "translator"},
			expectedCode: http.StatusOK,
			expectedBody: "success",
			description:  "When tool is in multiple allowed tools list, tools/call request should pass through",
		},
		{
			name:         "tools/call request for unauthorized tool in multiple allowed list is denied",
			method:       string(mcp.MethodToolsCall),
			params:       map[string]any{"name": "unauthorized_tool"},
			allowedTools: []string{"weather", "calculator", "translator"},
			expectedCode: http.StatusOK,
			expectedBody: `{"Result":null,"Error":{"code":-32602,"message":"Unauthorized"},"ID":{}}`,
			description:  "When tool is not in multiple allowed tools list, tools/call request should be denied with Invalid Params error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create a test handler that returns success
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("success"))
			})

			// Create authorizer if tools are specified
			var authorizer *CedarAuthorizer
			var err error
			if len(tt.allowedTools) > 0 {
				authorizer, err = CreateToolFilteringAuthorizer(tt.allowedTools)
				require.NoError(t, err)
			}

			// Create middleware - use ToolFilteringMiddleware for authorization
			middleware := ToolFilteringMiddleware(authorizer)

			filteredHandler := middleware(handler)

			// Create request body
			requestBody := map[string]any{
				"jsonrpc": "2.0",
				"id":      "1",
				"method":  tt.method,
				"params":  tt.params,
			}
			requestJSON, _ := json.Marshal(requestBody)

			req, err := http.NewRequest("POST", "/messages", bytes.NewBuffer(requestJSON))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")

			// Add parsed MCP request to context
			parsedRequest := &mcpparser.ParsedMCPRequest{
				ID:         "1",
				Method:     tt.method,
				ResourceID: tt.params["name"].(string),
				Arguments:  tt.params,
			}
			ctx := context.WithValue(req.Context(), mcpparser.MCPRequestContextKey, parsedRequest)

			// Add JWT claims to context for authorization
			claims := jwt.MapClaims{
				"sub":  "testuser",
				"name": "Test User",
				"role": "user",
			}
			ctx = context.WithValue(ctx, auth.ClaimsContextKey{}, claims)
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			filteredHandler.ServeHTTP(rr, req)

			// Check response
			assert.Equal(t, tt.expectedCode, rr.Code, tt.description)

			if tt.expectedCode == http.StatusOK {
				assert.Equal(t, tt.expectedBody, rr.Body.String(), tt.description)
			} else {
				// For error responses, check that it's a JSON-RPC error with Invalid Params error code
				responseBody := rr.Body.String()
				assert.Contains(t, responseBody, "Error", tt.description)
				assert.Contains(t, responseBody, "-32602", tt.description)
				assert.Contains(t, responseBody, "Unauthorized", tt.description)
			}
		})
	}
}

func TestToolFilteringMiddleware_NonPOSTRequest(t *testing.T) {
	t.Parallel()
	// Test that non-POST requests pass through unchanged
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	middleware := ToolFilteringMiddleware(nil)
	filteredHandler := middleware(handler)

	req, err := http.NewRequest("GET", "/messages", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	filteredHandler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "success", rr.Body.String())
}

func TestCreateToolFilteringAuthorizer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		allowedTools []string
		expectError  bool
	}{
		{
			name:         "no tools specified - should error",
			allowedTools: []string{},
			expectError:  true,
		},
		{
			name:         "single tool specified",
			allowedTools: []string{"weather"},
			expectError:  false,
		},
		{
			name:         "multiple tools specified",
			allowedTools: []string{"weather", "calculator", "translator"},
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			authorizer, err := CreateToolFilteringAuthorizer(tt.allowedTools)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, authorizer)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, authorizer)

			// Test that the authorizer can authorize tool calls using IsAuthorized directly
			// When specific tools specified, should only allow those tools
			for _, tool := range tt.allowedTools {
				authorizer, err := CreateToolFilteringAuthorizer([]string{tool})
				assert.NoError(t, err)
				assert.NotNil(t, authorizer)
				principal := "Client::testuser"
				action := "Action::call_tool"
				resource := fmt.Sprintf("Tool::%s", tool)
				authorized, err := authorizer.IsAuthorized(
					principal,
					action,
					resource,
					map[string]any{},
				)
				assert.NoError(t, err)
				assert.True(t, authorized, "Should authorize allowed tool: %s", tool)
			}

			// Test that unauthorized tools are denied
			unauthorizedTool := "unauthorized_tool"
			authorized, err := authorizer.IsAuthorized(
				"Client::testuser",
				"Action::call_tool",
				fmt.Sprintf("Tool::%s", unauthorizedTool),
				map[string]any{},
			)
			assert.NoError(t, err)
			assert.False(t, authorized, "Should deny unauthorized tool: %s", unauthorizedTool)
		})
	}
}

// TestCedarPolicyTemplateComprehensive tests the Cedar policy template functionality comprehensively
// including rendering, entities, integration, and error handling
func TestCedarPolicyTemplateComprehensive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		allowedTools    []string
		testTools       []string
		expectedResults []bool
		expectError     bool
		testType        string // "rendering", "entities", "integration", "error"
	}{
		// Rendering test cases
		{
			name:            "rendering - no tools specified - should error",
			allowedTools:    []string{},
			testTools:       []string{"any_tool", "another_tool"},
			expectedResults: []bool{true, true},
			expectError:     true,
			testType:        "rendering",
		},
		{
			name:            "rendering - single tool specified - allow only that tool",
			allowedTools:    []string{"weather"},
			testTools:       []string{"weather", "calculator", "translator"},
			expectedResults: []bool{true, false, false},
			expectError:     false,
			testType:        "rendering",
		},
		{
			name:            "rendering - multiple tools specified - allow only those tools",
			allowedTools:    []string{"weather", "calculator", "translator"},
			testTools:       []string{"weather", "calculator", "translator", "unauthorized"},
			expectedResults: []bool{true, true, true, false},
			expectError:     false,
			testType:        "rendering",
		},
		{
			name:            "rendering - tools with special characters",
			allowedTools:    []string{"tool-with-dash", "tool_with_underscore", "tool.with.dot"},
			testTools:       []string{"tool-with-dash", "tool_with_underscore", "tool.with.dot", "normal_tool"},
			expectedResults: []bool{true, true, true, false},
			expectError:     false,
			testType:        "rendering",
		},

		// Entities test cases
		{
			name:            "entities - no tools specified - should error",
			allowedTools:    []string{},
			testTools:       []string{"any_tool", "another_tool"},
			expectedResults: []bool{true, true},
			expectError:     true,
			testType:        "entities",
		},
		{
			name:            "entities - single tool specified - allow only that tool",
			allowedTools:    []string{"weather"},
			testTools:       []string{"weather", "calculator", "translator"},
			expectedResults: []bool{true, false, false},
			expectError:     false,
			testType:        "entities",
		},
		{
			name:            "entities - multiple tools specified - allow only those tools",
			allowedTools:    []string{"weather", "calculator", "translator"},
			testTools:       []string{"weather", "calculator", "translator", "unauthorized"},
			expectedResults: []bool{true, true, true, false},
			expectError:     false,
			testType:        "entities",
		},
		{
			name:            "entities - tools with special characters",
			allowedTools:    []string{"tool-with-dash", "tool_with_underscore", "tool.with.dot"},
			testTools:       []string{"tool-with-dash", "tool_with_underscore", "tool.with.dot", "normal_tool"},
			expectedResults: []bool{true, true, true, false},
			expectError:     false,
			testType:        "entities",
		},

		// Integration test cases
		{
			name:            "integration - no tools specified - should error",
			allowedTools:    []string{},
			testTools:       []string{"any_tool", "another_tool", "third_tool"},
			expectedResults: []bool{true, true, true},
			expectError:     true,
			testType:        "integration",
		},
		{
			name:            "integration - specific tools - allow only specified",
			allowedTools:    []string{"weather", "calculator"},
			testTools:       []string{"weather", "calculator", "translator", "unauthorized"},
			expectedResults: []bool{true, true, false, false},
			expectError:     false,
			testType:        "integration",
		},
		{
			name:            "integration - single tool - allow only that tool",
			allowedTools:    []string{"weather"},
			testTools:       []string{"weather", "calculator", "translator"},
			expectedResults: []bool{true, false, false},
			expectError:     false,
			testType:        "integration",
		},

		// Error handling test cases
		{
			name:         "error - no tools specified - should error",
			allowedTools: []string{},
			testTools:    []string{},
			expectError:  true,
			testType:     "error",
		},
		{
			name:         "error - empty tool name",
			allowedTools: []string{""},
			testTools:    []string{},
			expectError:  false, // Should handle empty strings gracefully
			testType:     "error",
		},
		{
			name:         "error - tool with quotes",
			allowedTools: []string{"tool\"with\"quotes"},
			testTools:    []string{},
			expectError:  true, // Should error because quotes break Cedar policy syntax
			testType:     "error",
		},
		{
			name:         "error - tool with newlines",
			allowedTools: []string{"tool\nwith\nnewlines"},
			testTools:    []string{},
			expectError:  true, // Should error because newlines break Cedar policy syntax
			testType:     "error",
		},
		{
			name:         "error - tool with template syntax",
			allowedTools: []string{"tool{{with}}template"},
			testTools:    []string{},
			expectError:  false, // Should handle template-like syntax in tool names
			testType:     "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			authorizer, err := CreateToolFilteringAuthorizer(tt.allowedTools)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, authorizer)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, authorizer)

			// For error handling tests, we only test that the authorizer was created successfully
			if tt.testType == "error" {
				return
			}

			// Test each tool to verify the policy and entities work correctly
			for i, tool := range tt.testTools {
				authorized, err := authorizer.IsAuthorized(
					"Client::testuser",
					"Action::call_tool",
					fmt.Sprintf("Tool::%s", tool),
					map[string]any{},
				)
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResults[i], authorized,
					"Tool %s should be %v", tool, tt.expectedResults[i])
			}
		})
	}
}

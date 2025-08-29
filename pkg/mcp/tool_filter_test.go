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
		name           string
		config         *toolMiddlewareConfig
		request        toolCallRequest
		expectedResult string // "filter", "override", "bogus", "noaction"
		expectedName   string // only relevant for override case
	}{
		{
			name: "tool in filter - should succeed",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"test_tool":  {},
					"other_tool": {},
				},
				actualToUserOverride: map[string]toolOverrideEntry{},
				userToActualOverride: map[string]toolOverrideEntry{},
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
			expectedResult: "noaction",
			expectedName:   "",
		},
		{
			name: "tool not in filter - should fail",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool": {},
				},
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
			expectedResult: "filter",
			expectedName:   "",
		},
		{
			name: "tool name not found in params - should fail",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"test_tool": {},
				},
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
			expectedResult: "bogus",
			expectedName:   "",
		},
		{
			name: "tool name is not string - should fail",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"test_tool": {},
				},
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
			expectedResult: "bogus",
			expectedName:   "",
		},
		{
			name: "empty filter - should succeed",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params: &map[string]any{
					"name": "any_tool",
				},
			},
			expectedResult: "noaction",
			expectedName:   "",
		},
		{
			name: "empty params",
			config: &toolMiddlewareConfig{
				filterTools:          map[string]struct{}{"any_tool": {}},
				actualToUserOverride: map[string]toolOverrideEntry{},
				userToActualOverride: map[string]toolOverrideEntry{},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params:  &map[string]any{},
			},
			expectedResult: "bogus",
			expectedName:   "",
		},
		{
			name: "params with nil name",
			config: &toolMiddlewareConfig{
				filterTools:          map[string]struct{}{"any_tool": {}},
				actualToUserOverride: map[string]toolOverrideEntry{},
				userToActualOverride: map[string]toolOverrideEntry{},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params: &map[string]any{
					"name": nil,
				},
			},
			expectedResult: "bogus",
			expectedName:   "",
		},
		{
			name: "tool with override - should return override",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"user_tool": {},
				},
				actualToUserOverride: map[string]toolOverrideEntry{
					"actual_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly name",
					},
				},
				userToActualOverride: map[string]toolOverrideEntry{
					"user_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly name",
					},
				},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params: &map[string]any{
					"name": "user_tool",
				},
			},
			expectedResult: "override",
			expectedName:   "actual_tool",
		},
		{
			name: "empty tool name - should fail",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params: &map[string]any{
					"name": "",
				},
			},
			expectedResult: "bogus",
			expectedName:   "",
		},
		{
			name: "nil params - should fail",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
			},
			request: toolCallRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params:  nil,
			},
			expectedResult: "bogus",
			expectedName:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := processToolCallRequest(tt.config, tt.request)

			switch tt.expectedResult {
			case "filter":
				_, ok := result.(*toolCallFilter)
				assert.True(t, ok, "Expected toolCallFilter result")
			case "override":
				override, ok := result.(*toolCallOverride)
				assert.True(t, ok, "Expected toolCallOverride result")
				assert.Equal(t, tt.expectedName, override.Name())
			case "bogus":
				_, ok := result.(*toolCallBogus)
				assert.True(t, ok, "Expected toolCallBogus result")
			case "noaction":
				_, ok := result.(*toolCallNoAction)
				assert.True(t, ok, "Expected toolCallNoAction result")
			default:
				t.Errorf("Unknown expected result: %s", tt.expectedResult)
			}
		})
	}
}

func TestProcessToolsListResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		config               *toolMiddlewareConfig
		inputResponse        toolsListResponse
		expectedTools        []string
		expectedDescriptions map[string]string // map of tool name to expected description
		expectError          error
	}{
		{
			name: "filter tools - keep only allowed tools",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool1": {},
					"allowed_tool2": {},
				},
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
			expectedDescriptions: map[string]string{
				"allowed_tool1": "First tool",
				"allowed_tool2": "Second tool",
			},
			expectError: nil,
		},
		{
			name: "no filter - keep all tools",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"tool1": {},
					"tool2": {},
					"tool3": {},
				},
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
			expectedDescriptions: map[string]string{
				"tool1": "First tool",
				"tool2": "Second tool",
				"tool3": "Third tool",
			},
			expectError: nil,
		},
		{
			name: "tool without name field - should fail",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool": {},
				},
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
			expectedDescriptions: nil,
			expectError:          errToolNameNotFound,
		},
		{
			name: "tool name is not string - should fail",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool": {},
				},
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
			expectedDescriptions: nil,
			expectError:          errToolNameNotFound,
		},
		{
			name: "empty tool name - should fail",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
			},
			inputResponse: toolsListResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: struct {
					Tools *[]map[string]any `json:"tools"`
				}{
					Tools: &[]map[string]any{
						{"name": "", "description": "Tool with empty name"},
					},
				},
			},
			expectedDescriptions: nil,
			expectError:          errToolNameNotFound,
		},
		{
			name: "tool with override - name and description changed",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"user_friendly_name": {},
				},
				actualToUserOverride: map[string]toolOverrideEntry{
					"actual_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_friendly_name",
						OverrideDescription: "User friendly description",
					},
				},
				userToActualOverride: map[string]toolOverrideEntry{
					"user_friendly_name": {
						ActualName:          "actual_tool",
						OverrideName:        "user_friendly_name",
						OverrideDescription: "User friendly description",
					},
				},
			},
			inputResponse: toolsListResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: struct {
					Tools *[]map[string]any `json:"tools"`
				}{
					Tools: &[]map[string]any{
						{"name": "actual_tool", "description": "Original description"},
					},
				},
			},
			expectedTools: []string{"user_friendly_name"},
			expectedDescriptions: map[string]string{
				"user_friendly_name": "User friendly description",
			},
			expectError: nil,
		},
		{
			name: "tool with override - filtered out after override",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool": {},
				},
				actualToUserOverride: map[string]toolOverrideEntry{
					"actual_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "blocked_tool",
						OverrideDescription: "Blocked tool description",
					},
				},
				userToActualOverride: map[string]toolOverrideEntry{
					"blocked_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "blocked_tool",
						OverrideDescription: "Blocked tool description",
					},
				},
			},
			inputResponse: toolsListResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: struct {
					Tools *[]map[string]any `json:"tools"`
				}{
					Tools: &[]map[string]any{
						{"name": "actual_tool", "description": "Original description"},
						{"name": "allowed_tool", "description": "Allowed tool"},
					},
				},
			},
			expectedTools: []string{"allowed_tool"},
			expectedDescriptions: map[string]string{
				"allowed_tool": "Allowed tool",
			},
			expectError: nil,
		},
		{
			name: "empty tools list - should succeed",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
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
			expectedTools:        []string{},
			expectedDescriptions: map[string]string{},
			expectError:          nil,
		},
		{
			name: "multiple tools with overrides",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"user_tool1": {},
					"user_tool2": {},
				},
				actualToUserOverride: map[string]toolOverrideEntry{
					"actual_tool1": {
						ActualName:          "actual_tool1",
						OverrideName:        "user_tool1",
						OverrideDescription: "User friendly tool 1",
					},
					"actual_tool2": {
						ActualName:          "actual_tool2",
						OverrideName:        "user_tool2",
						OverrideDescription: "User friendly tool 2",
					},
				},
				userToActualOverride: map[string]toolOverrideEntry{
					"user_tool1": {
						ActualName:          "actual_tool1",
						OverrideName:        "user_tool1",
						OverrideDescription: "User friendly tool 1",
					},
					"user_tool2": {
						ActualName:          "actual_tool2",
						OverrideName:        "user_tool2",
						OverrideDescription: "User friendly tool 2",
					},
				},
			},
			inputResponse: toolsListResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: struct {
					Tools *[]map[string]any `json:"tools"`
				}{
					Tools: &[]map[string]any{
						{"name": "actual_tool1", "description": "Original description 1"},
						{"name": "actual_tool2", "description": "Original description 2"},
						{"name": "other_tool", "description": "Other tool"},
					},
				},
			},
			expectedTools: []string{"user_tool1", "user_tool2"},
			expectedDescriptions: map[string]string{
				"user_tool1": "User friendly tool 1",
				"user_tool2": "User friendly tool 2",
			},
			expectError: nil,
		},
		{
			name: "tool override with description verification",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"user_tool": {},
				},
				actualToUserOverride: map[string]toolOverrideEntry{
					"actual_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly description",
					},
				},
				userToActualOverride: map[string]toolOverrideEntry{
					"user_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly description",
					},
				},
			},
			inputResponse: toolsListResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: struct {
					Tools *[]map[string]any `json:"tools"`
				}{
					Tools: &[]map[string]any{
						{"name": "actual_tool", "description": "Original description", "inputSchema": map[string]any{"type": "object"}},
					},
				},
			},
			expectedTools: []string{"user_tool"},
			expectedDescriptions: map[string]string{
				"user_tool": "User friendly description",
			},
			expectError: nil,
		},
		{
			name: "verify description override",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"user_tool": {},
				},
				actualToUserOverride: map[string]toolOverrideEntry{
					"actual_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly description",
					},
				},
				userToActualOverride: map[string]toolOverrideEntry{
					"user_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly description",
					},
				},
			},
			inputResponse: toolsListResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: struct {
					Tools *[]map[string]any `json:"tools"`
				}{
					Tools: &[]map[string]any{
						{"name": "actual_tool", "description": "Original description", "inputSchema": map[string]any{"type": "object"}},
					},
				},
			},
			expectedTools: []string{"user_tool"},
			expectedDescriptions: map[string]string{
				"user_tool": "User friendly description",
			},
			expectError: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			err := processToolsListResponse(tt.config, tt.inputResponse, &buf)

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

			// Only compare expected tools if we're not expecting an error
			if tt.expectError == nil {
				assert.ElementsMatch(t, tt.expectedTools, actualTools)

				// Verify descriptions if expectedDescriptions is provided
				if tt.expectedDescriptions != nil {
					require.NotNil(t, outputResponse.Result.Tools)

					// Create a map of actual tool descriptions for easy lookup
					actualDescriptions := make(map[string]string)
					for _, tool := range *outputResponse.Result.Tools {
						if name, ok := tool["name"].(string); ok {
							if description, ok := tool["description"].(string); ok {
								actualDescriptions[name] = description
							}
						}
					}

					// Verify each expected description
					for toolName, expectedDescription := range tt.expectedDescriptions {
						actualDescription, exists := actualDescriptions[toolName]
						assert.True(t, exists, "Tool %s should exist in output", toolName)
						assert.Equal(t, expectedDescription, actualDescription,
							"Description for tool %s should match expected", toolName)
					}

					// For test cases with inputSchema, verify that other fields are preserved
					if len(*outputResponse.Result.Tools) == 1 {
						tool := (*outputResponse.Result.Tools)[0]
						if _, hasInputSchema := tool["inputSchema"]; hasInputSchema {
							// Verify that other fields are preserved
							assert.Equal(t, map[string]any{"type": "object"}, tool["inputSchema"])
						}
					}
				}
			}
		})
	}
}

func TestProcessSSEEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *toolMiddlewareConfig
		inputBuffer []byte
		expected    string
		expectError bool
	}{
		{
			name: "SSE with non-tools data - pass through unchanged",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
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
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"tool1": {},
					"tool3": {},
				},
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
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool": {},
				},
			},
			inputBuffer: []byte("event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"name\":\"allowed_tool\",\"description\":\"Allowed\"},{\"name\":\"blocked_tool\",\"description\":\"Blocked\"}]}}\r\n\r\n"),
			expected:    "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"description\":\"Allowed\",\"name\":\"allowed_tool\"}]}}\n\r\n\r\n",
			expectError: false,
		},
		{
			name: "SSE with CR line endings",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool": {},
				},
			},
			inputBuffer: []byte("event: message\rdata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"name\":\"allowed_tool\",\"description\":\"Allowed\"}]}}\r\r"),
			expected:    "event: message\rdata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"description\":\"Allowed\",\"name\":\"allowed_tool\"}]}}\n\r\r",
			expectError: false,
		},
		{
			name: "SSE with unsupported line separator - should fail",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
			},
			inputBuffer: []byte("event: message\vdata: {\"jsonrpc\":\"2.0\",\"id\":1}\v\v"),
			expectError: true,
		},
		{
			name: "SSE with malformed JSON in data - pass through unchanged",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
			},
			inputBuffer: []byte(`event: message
data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"tool1"}]}

`),
			expected: `event: message
data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"tool1"}]}

`,
			expectError: false,
		},
		{
			name: "SSE with only line separators",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
			},
			inputBuffer: []byte("\n\n"),
			expected:    "\n",
			expectError: false,
		},
		{
			name: "SSE with single line",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
			},
			inputBuffer: []byte("event: message\n"),
			expected:    "event: message\n",
			expectError: false,
		},
		{
			name: "SSE with data line without event line",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
			},
			inputBuffer: []byte("data: {\"jsonrpc\":\"2.0\",\"id\":1}\n\n"),
			expected:    "data: {\"jsonrpc\":\"2.0\",\"id\":1}\n\n",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			err := processEventStream(tt.config, tt.inputBuffer, &buf)

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
		config      *toolMiddlewareConfig
		buffer      []byte
		mimeType    string
		expectError bool
	}{
		{
			name: "JSON with tools list",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool": {},
				},
			},
			buffer:      []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"allowed_tool","description":"Allowed"},{"name":"blocked_tool","description":"Blocked"}]}}`),
			mimeType:    "application/json",
			expectError: false,
		},
		{
			name: "SSE with tools list",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool": {},
				},
			},
			buffer: []byte(`event: message
data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"allowed_tool","description":"Allowed"},{"name":"blocked_tool","description":"Blocked"}]}}

`),
			mimeType:    "text/event-stream",
			expectError: false,
		},
		{
			name: "Unsupported mime type",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
			},
			buffer:      []byte(`some data`),
			mimeType:    "text/plain",
			expectError: true,
		},
		{
			name: "Empty buffer",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"any_tool": {},
				},
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
			err := processBuffer(tt.config, tt.buffer, tt.mimeType, &buf)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestToolMiddlewareConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         *toolMiddlewareConfig
		toolName       string
		expectedFilter bool
		expectedCall   string
		expectedList   *toolOverrideEntry
	}{
		{
			name: "tool in filter - should be allowed",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool": {},
					"other_tool":   {},
				},
			},
			toolName:       "allowed_tool",
			expectedFilter: true,
			expectedCall:   "",
			expectedList:   nil,
		},
		{
			name: "tool not in filter - should be blocked",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool": {},
				},
			},
			toolName:       "blocked_tool",
			expectedFilter: false,
			expectedCall:   "",
			expectedList:   nil,
		},
		{
			name: "nil filter - all tools allowed",
			config: &toolMiddlewareConfig{
				filterTools: nil,
			},
			toolName:       "any_tool",
			expectedFilter: true,
			expectedCall:   "",
			expectedList:   nil,
		},
		{
			name: "tool call override - should return actual name",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"user_tool": {},
				},
				actualToUserOverride: map[string]toolOverrideEntry{
					"actual_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly description",
					},
				},
				userToActualOverride: map[string]toolOverrideEntry{
					"user_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly description",
					},
				},
			},
			toolName:       "user_tool",
			expectedFilter: true,
			expectedCall:   "actual_tool",
			expectedList:   nil,
		},
		{
			name: "tool list override - should return override entry",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"user_tool": {},
				},
				actualToUserOverride: map[string]toolOverrideEntry{
					"actual_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly description",
					},
				},
				userToActualOverride: map[string]toolOverrideEntry{
					"user_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly description",
					},
				},
			},
			toolName:       "actual_tool",
			expectedFilter: false, // actual_tool not in filter, only user_tool is
			expectedCall:   "",
			expectedList: &toolOverrideEntry{
				ActualName:          "actual_tool",
				OverrideName:        "user_tool",
				OverrideDescription: "User friendly description",
			},
		},
		{
			name: "no override found - should return empty",
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"allowed_tool": {},
				},
				actualToUserOverride: map[string]toolOverrideEntry{
					"actual_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly description",
					},
				},
				userToActualOverride: map[string]toolOverrideEntry{
					"user_tool": {
						ActualName:          "actual_tool",
						OverrideName:        "user_tool",
						OverrideDescription: "User friendly description",
					},
				},
			},
			toolName:       "unknown_tool",
			expectedFilter: false,
			expectedCall:   "",
			expectedList:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Test isToolInFilter
			result := tt.config.isToolInFilter(tt.toolName)
			assert.Equal(t, tt.expectedFilter, result, "isToolInFilter should return expected result")

			// Test getToolCallActualName
			actualName, found := tt.config.getToolCallActualName(tt.toolName)
			if tt.expectedCall != "" {
				assert.True(t, found, "getToolCallActualName should find override")
				assert.Equal(t, tt.expectedCall, actualName, "getToolCallActualName should return expected actual name")
			} else {
				assert.False(t, found, "getToolCallActualName should not find override")
				assert.Equal(t, "", actualName, "getToolCallActualName should return empty string when no override")
			}

			// Test getToolListOverride
			overrideEntry, found := tt.config.getToolListOverride(tt.toolName)
			if tt.expectedList != nil {
				assert.True(t, found, "getToolListOverride should find override")
				assert.Equal(t, tt.expectedList.ActualName, overrideEntry.ActualName, "ActualName should match")
				assert.Equal(t, tt.expectedList.OverrideName, overrideEntry.OverrideName, "OverrideName should match")
				assert.Equal(t, tt.expectedList.OverrideDescription, overrideEntry.OverrideDescription, "OverrideDescription should match")
			} else {
				assert.False(t, found, "getToolListOverride should not find override")
				// When no override is found, it returns nil if the map is nil, or a pointer to zero-value struct
				if tt.config.actualToUserOverride == nil {
					assert.Nil(t, overrideEntry, "getToolListOverride should return nil when map is nil")
				} else {
					assert.NotNil(t, overrideEntry, "getToolListOverride should return a pointer (even if to zero-value)")
					assert.Equal(t, "", overrideEntry.ActualName, "ActualName should be empty when no override")
					assert.Equal(t, "", overrideEntry.OverrideName, "OverrideName should be empty when no override")
					assert.Equal(t, "", overrideEntry.OverrideDescription, "OverrideDescription should be empty when no override")
				}
			}
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
	config := &toolMiddlewareConfig{
		filterTools: map[string]struct{}{
			"tool1": {},
			"tool3": {},
		},
	}

	inputResponse := createToolsListResponse([]map[string]any{
		{"name": "tool1", "description": "First tool", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool2", "description": "Second tool", "inputSchema": map[string]any{"type": "string"}},
		{"name": "tool3", "description": "Third tool", "inputSchema": map[string]any{"type": "array"}},
	})

	var buf bytes.Buffer
	err := processToolsListResponse(config, inputResponse, &buf)
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

func TestToolFilterWriter_Flush(t *testing.T) {
	t.Parallel()

	// Initialize logger to avoid panic
	logger.Initialize()

	tests := []struct {
		name        string
		writeData   []byte
		contentType string
		statusCode  int
		config      *toolMiddlewareConfig
		expectWrite bool
		expectReset bool
	}{
		{
			name:        "empty buffer - should not write anything",
			writeData:   []byte{},
			contentType: "application/json",
			statusCode:  200,
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"tool1": {},
				},
			},
			expectWrite: false,
			expectReset: false,
		},
		{
			name:        "JSON content type - should process and write",
			writeData:   []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"tool1","description":"First"},{"name":"tool2","description":"Second"}]}}`),
			contentType: "application/json",
			statusCode:  200,
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"tool1": {},
				},
			},
			expectWrite: true,
			expectReset: true,
		},
		{
			name:        "no content type - should write buffer directly",
			writeData:   []byte(`{"test":"data"}`),
			contentType: "",
			statusCode:  200,
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"tool1": {},
				},
			},
			expectWrite: true,
			expectReset: false, // Buffer is not reset when no content type
		},
		{
			name:        "with status code - should set header and write",
			writeData:   []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"tool1","description":"First"}]}}`),
			contentType: "application/json",
			statusCode:  201,
			config: &toolMiddlewareConfig{
				filterTools: map[string]struct{}{
					"tool1": {},
				},
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
				config:         tt.config,
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

func TestNewToolFilterMiddleware(t *testing.T) {
	t.Parallel()

	// Initialize logger to avoid panic
	logger.Initialize()

	tests := []struct {
		name        string
		opts        []ToolMiddlewareOption
		expectError bool
	}{
		{
			name: "valid tools filter",
			opts: []ToolMiddlewareOption{
				WithToolsFilter("tool1", "tool2"),
			},
			expectError: false,
		},
		{
			name: "empty tools filter - should fail",
			opts: []ToolMiddlewareOption{
				WithToolsFilter(),
			},
			expectError: true,
		},
		{
			name:        "no options - should fail",
			opts:        []ToolMiddlewareOption{},
			expectError: true,
		},
		{
			name: "multiple options",
			opts: []ToolMiddlewareOption{
				WithToolsFilter("tool1", "tool2"),
				WithToolsOverride("tool3", "my-tool3", "My Tool3 Description"),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			middleware, err := NewListToolsMappingMiddleware(tt.opts...)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, middleware)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, middleware)
			}
		})
	}
}

func TestWithToolsFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		toolsFilter []string
		expectError bool
	}{
		{
			name:        "valid tools filter",
			toolsFilter: []string{"tool1", "tool2", "tool3"},
			expectError: false,
		},
		{
			name:        "empty tools filter",
			toolsFilter: []string{},
			expectError: false,
		},
		{
			name:        "nil tools filter",
			toolsFilter: nil,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opt := WithToolsFilter(tt.toolsFilter...)
			assert.NotNil(t, opt)

			config := &toolMiddlewareConfig{
				filterTools: make(map[string]struct{}),
			}
			err := opt(config)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				for _, tool := range tt.toolsFilter {
					assert.NotNil(t, config.filterTools[tool])
				}
			}
		})
	}
}

func TestWithToolsOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		toolActualName          string
		toolOverrideName        string
		toolOverrideDescription string
		expectError             bool
	}{
		{
			name:                    "valid tools override",
			toolActualName:          "tool1",
			toolOverrideName:        "my-tool1",
			toolOverrideDescription: "My Tool1 Description",
			expectError:             false,
		},
		{
			name:                    "empty tools override",
			toolActualName:          "tool1",
			toolOverrideName:        "",
			toolOverrideDescription: "",
			expectError:             true,
		},
		{
			name:                    "empty tools override",
			toolActualName:          "",
			toolOverrideName:        "",
			toolOverrideDescription: "",
			expectError:             true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opt := WithToolsOverride(tt.toolActualName, tt.toolOverrideName, tt.toolOverrideDescription)
			assert.NotNil(t, opt)

			config := &toolMiddlewareConfig{
				actualToUserOverride: make(map[string]toolOverrideEntry),
				userToActualOverride: make(map[string]toolOverrideEntry),
			}
			err := opt(config)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				assert.Equal(t, tt.toolActualName, config.actualToUserOverride[tt.toolActualName].ActualName)
				assert.Equal(t, tt.toolOverrideName, config.actualToUserOverride[tt.toolActualName].OverrideName)
				assert.Equal(t, tt.toolOverrideDescription, config.actualToUserOverride[tt.toolActualName].OverrideDescription)

				assert.Equal(t, tt.toolActualName, config.userToActualOverride[tt.toolOverrideName].ActualName)
				assert.Equal(t, tt.toolOverrideName, config.userToActualOverride[tt.toolOverrideName].OverrideName)
				assert.Equal(t, tt.toolOverrideDescription, config.userToActualOverride[tt.toolOverrideName].OverrideDescription)
			}
		})
	}
}

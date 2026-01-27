// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/logger"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
	"github.com/stacklok/toolhive/test/testkit"
)

func TestMiddleware(t *testing.T) {
	t.Parallel()

	// Initialize logger for tests
	logger.Initialize()

	// Create a Cedar authorizer
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
			`permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`,
			`permit(principal, action == Action::"read_resource", resource == Resource::"data");`,
		},
		EntitiesJSON: `[]`,
	})
	require.NoError(t, err, "Failed to create Cedar authorizer")

	// Test cases
	testCases := []struct {
		name             string
		method           string
		params           map[string]interface{}
		claims           jwt.MapClaims
		expectStatus     int
		expectAuthorized bool
	}{
		{
			name:   "Authorized tool call",
			method: "tools/call",
			params: map[string]interface{}{
				"name": "weather",
				"arguments": map[string]interface{}{
					"location": "New York",
				},
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Unauthorized tool call",
			method: "tools/call",
			params: map[string]interface{}{
				"name": "calculator",
				"arguments": map[string]interface{}{
					"operation": "add",
					"value1":    5,
					"value2":    10,
				},
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
		{
			name:   "Authorized prompt get",
			method: "prompts/get",
			params: map[string]interface{}{
				"name": "greeting",
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Unauthorized prompt get",
			method: "prompts/get",
			params: map[string]interface{}{
				"name": "farewell",
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
		{
			name:   "Authorized resource read",
			method: "resources/read",
			params: map[string]interface{}{
				"uri": "data",
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Unauthorized resource read",
			method: "resources/read",
			params: map[string]interface{}{
				"uri": "secret",
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
		{
			name:   "Ping is always allowed",
			method: "ping",
			params: map[string]interface{}{},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Initialize is always allowed",
			method: "initialize",
			params: map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]interface{}{
					"roots": map[string]interface{}{
						"listChanged": true,
					},
					"sampling": map[string]interface{}{},
				},
				"clientInfo": map[string]interface{}{
					"name":    "ExampleClient",
					"version": "1.0.0",
				},
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Tools list is always allowed but filtered",
			method: string(mcp.MethodToolsList),
			params: map[string]interface{}{},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Prompts list is always allowed but filtered",
			method: string(mcp.MethodPromptsList),
			params: map[string]interface{}{},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Resources list is always allowed but filtered",
			method: string(mcp.MethodResourcesList),
			params: map[string]interface{}{},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Resources subscribe requires authorization",
			method: "resources/subscribe",
			params: map[string]interface{}{
				"uri": "data",
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Resources unsubscribe requires authorization",
			method: "resources/unsubscribe",
			params: map[string]interface{}{
				"uri": "secret",
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
		{
			name:   "Resources templates list is authorized and filtered",
			method: "resources/templates/list",
			params: map[string]interface{}{},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Roots list is always allowed",
			method: "roots/list",
			params: map[string]interface{}{},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Logging setLevel is always allowed",
			method: "logging/setLevel",
			params: map[string]interface{}{
				"level": "debug",
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Completion complete is always allowed",
			method: "completion/complete",
			params: map[string]interface{}{
				"ref": map[string]interface{}{
					"name": "greeting",
				},
				"argument": map[string]interface{}{
					"name":  "name",
					"value": "Jo",
				},
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Notifications are always allowed",
			method: "notifications/message",
			params: map[string]interface{}{
				"method": "test",
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Unknown method is denied by default",
			method: "unknown/method",
			params: map[string]interface{}{},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
		{
			name:   "Sampling createMessage is denied by default (security-sensitive)",
			method: "sampling/createMessage",
			params: map[string]interface{}{
				"messages": []interface{}{
					map[string]interface{}{
						"role":    "user",
						"content": map[string]interface{}{"type": "text", "text": "Hello"},
					},
				},
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
		{
			name:   "Elicitation create is denied by default",
			method: "elicitation/create",
			params: map[string]interface{}{
				"message": "Enter your name",
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
		{
			name:   "Tasks list is denied by default",
			method: "tasks/list",
			params: map[string]interface{}{},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
		{
			name:   "Tasks get is denied by default",
			method: "tasks/get",
			params: map[string]interface{}{
				"taskId": "task-123",
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
	}

	// Run test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create a JSON-RPC request
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err, "Failed to marshal params")

			request, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), tc.method, json.RawMessage(paramsJSON))
			require.NoError(t, err, "Failed to create JSON-RPC request")

			// Marshal the request to JSON
			requestJSON, err := jsonrpc2.EncodeMessage(request)
			require.NoError(t, err, "Failed to encode JSON-RPC request")

			// Create an HTTP request
			req, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(requestJSON))
			require.NoError(t, err, "Failed to create HTTP request")
			req.Header.Set("Content-Type", "application/json")

			// Add claims to the request context
			identity := &auth.Identity{Subject: "test-user", Claims: tc.claims}
			req = req.WithContext(auth.WithIdentity(req.Context(), identity))

			// Create a response recorder
			rr := httptest.NewRecorder()

			// Create a handler that records if it was called
			var handlerCalled bool
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			})

			// Apply the middleware chain: MCP parsing first, then authorization
			middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, handler))

			// Serve the request
			middleware.ServeHTTP(rr, req)

			// Check the response
			assert.Equal(t, tc.expectStatus, rr.Code, "Response status code does not match expected")
			assert.Equal(t, tc.expectAuthorized, handlerCalled, "Handler called status does not match expected")
		})
	}
}

// TestMiddlewareWithGETRequest tests that the middleware doesn't panic with GET requests.
func TestMiddlewareWithGETRequest(t *testing.T) {
	t.Parallel()
	// Create a Cedar authorizer
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		},
		EntitiesJSON: `[]`,
	})
	require.NoError(t, err, "Failed to create Cedar authorizer")

	// Create a handler that records if it was called
	var handlerCalled bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	// Apply the middleware chain: MCP parsing first, then authorization
	middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, handler))

	// Create a GET request
	req, err := http.NewRequest(http.MethodGet, "/messages", nil)
	require.NoError(t, err, "Failed to create HTTP request")

	// Create a response recorder
	rr := httptest.NewRecorder()

	// Serve the request
	middleware.ServeHTTP(rr, req)

	// Check that the handler was called and the response is OK
	assert.True(t, handlerCalled, "Handler should be called for GET requests")
	assert.Equal(t, http.StatusOK, rr.Code, "Response status code should be OK")
}

func TestFactoryCreateMiddleware(t *testing.T) {
	t.Parallel()

	// Initialize logger for tests
	logger.Initialize()

	t.Run("create middleware with config data", func(t *testing.T) {
		t.Parallel()

		// Create config data using the new API
		configData := mustNewConfig(t, cedar.Config{
			Version: "1.0",
			Type:    cedar.ConfigType,
			Options: &cedar.ConfigOptions{
				Policies: []string{
					`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
				},
				EntitiesJSON: "[]",
			},
		})

		// Create middleware parameters with ConfigData
		params := FactoryMiddlewareParams{
			ConfigData: configData,
		}

		// Create middleware config
		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		// Create mock runner and config
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockConfig := mocks.NewMockRunnerConfig(ctrl)
		mockConfig.EXPECT().GetName().Return("test-server").AnyTimes()
		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockRunner.EXPECT().GetConfig().Return(mockConfig).AnyTimes()
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)

		// Test CreateMiddleware
		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.NoError(t, err)
	})

	t.Run("create middleware with config path (backwards compatibility)", func(t *testing.T) {
		t.Parallel()

		// Create a temporary config file using the new API
		configData := mustNewConfig(t, cedar.Config{
			Version: "1.0",
			Type:    cedar.ConfigType,
			Options: &cedar.ConfigOptions{
				Policies: []string{
					`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
				},
				EntitiesJSON: "[]",
			},
		})

		tmpFile, err := os.CreateTemp("", "authz_config_*.json")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		configJSON, err := json.Marshal(configData)
		require.NoError(t, err)

		_, err = tmpFile.Write(configJSON)
		require.NoError(t, err)
		tmpFile.Close()

		// Create middleware parameters with ConfigPath
		params := FactoryMiddlewareParams{
			ConfigPath: tmpFile.Name(),
		}

		// Create middleware config
		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		// Create mock runner and config
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockConfig := mocks.NewMockRunnerConfig(ctrl)
		mockConfig.EXPECT().GetName().Return("test-server").AnyTimes()
		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockRunner.EXPECT().GetConfig().Return(mockConfig).AnyTimes()
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)

		// Test CreateMiddleware
		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.NoError(t, err)
	})

	t.Run("config data takes precedence over config path", func(t *testing.T) {
		t.Parallel()

		// Create config data using the new API
		configData := mustNewConfig(t, cedar.Config{
			Version: "1.0",
			Type:    cedar.ConfigType,
			Options: &cedar.ConfigOptions{
				Policies: []string{
					`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
				},
				EntitiesJSON: "[]",
			},
		})

		// Create middleware parameters with both ConfigData and ConfigPath
		// ConfigData should take precedence, so ConfigPath can be invalid
		params := FactoryMiddlewareParams{
			ConfigData: configData,
			ConfigPath: "/nonexistent/path/should/not/be/used.json",
		}

		// Create middleware config
		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		// Create mock runner and config
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockConfig := mocks.NewMockRunnerConfig(ctrl)
		mockConfig.EXPECT().GetName().Return("test-server").AnyTimes()
		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockRunner.EXPECT().GetConfig().Return(mockConfig).AnyTimes()
		mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Times(1)

		// Test CreateMiddleware - should succeed even with invalid path because ConfigData takes precedence
		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.NoError(t, err)
	})

	t.Run("error when neither config data nor path provided", func(t *testing.T) {
		t.Parallel()

		// Create middleware parameters without ConfigData or ConfigPath
		params := FactoryMiddlewareParams{}

		// Create middleware config
		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		// Create mock runner
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		// Should not call AddMiddleware since creation should fail

		// Test CreateMiddleware - should fail
		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "either config_data or config_path is required")
	})

	t.Run("error when config path is invalid", func(t *testing.T) {
		t.Parallel()

		// Create middleware parameters with invalid ConfigPath
		params := FactoryMiddlewareParams{
			ConfigPath: "/nonexistent/invalid/path.json",
		}

		// Create middleware config
		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		// Create mock runner
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		// Should not call AddMiddleware since creation should fail

		// Test CreateMiddleware - should fail
		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load authorization configuration")
	})

	t.Run("error when config data is invalid", func(t *testing.T) {
		t.Parallel()

		// Create invalid config data (missing required fields)
		configData := &Config{
			// Missing Version and Type
		}

		// Create middleware parameters with invalid ConfigData
		params := FactoryMiddlewareParams{
			ConfigData: configData,
		}

		// Create middleware config
		middlewareConfig, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		// Create mock runner and config (GetConfig is called before validation)
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockConfig := mocks.NewMockRunnerConfig(ctrl)
		mockConfig.EXPECT().GetName().Return("test-server").AnyTimes()
		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		mockRunner.EXPECT().GetConfig().Return(mockConfig).AnyTimes()
		// Should not call AddMiddleware since creation should fail

		// Test CreateMiddleware - should fail
		err = CreateMiddleware(middlewareConfig, mockRunner)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create authorization middleware")
	})

	t.Run("error with malformed middleware config parameters", func(t *testing.T) {
		t.Parallel()

		// Create middleware config with invalid parameters
		middlewareConfig := &types.MiddlewareConfig{
			Type:       MiddlewareType,
			Parameters: []byte(`{"invalid_json": `), // Malformed JSON
		}

		// Create mock runner
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
		// Should not call AddMiddleware since creation should fail

		// Test CreateMiddleware - should fail
		err := CreateMiddleware(middlewareConfig, mockRunner)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal authorization middleware parameters")
	})
}

func TestMiddlewareToolsListTestkit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		teskitOpts []testkit.TestMCPServerOption
		policies   []string
		expected   []any
	}{
		// application/json tests
		{
			name: "application/json - all allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("foo", "A test tool", func() string { return "Foo" }),
				testkit.WithJSONClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected: []any{
				map[string]any{"name": "foo", "description": "A test tool"},
			},
		},
		{
			name: "application/json - one allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("foo", "A test tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("bar", "A test tool", func() string { return "Bar" }),
				testkit.WithJSONClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected: []any{
				map[string]any{"name": "foo", "description": "A test tool"},
			},
		},
		{
			name: "application/json - none allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("bar", "A test tool", func() string { return "Bar" }),
				testkit.WithJSONClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected: []any{},
		},

		// text/event-stream tests
		{
			name: "text/event-stream - all allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("foo", "A test tool", func() string { return "Foo" }),
				testkit.WithSSEClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected: []any{
				map[string]any{"name": "foo", "description": "A test tool"},
			},
		},
		{
			name: "text/event-stream - one allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("foo", "A test tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("bar", "A test tool", func() string { return "Bar" }),
				testkit.WithSSEClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected: []any{
				map[string]any{"name": "foo", "description": "A test tool"},
			},
		},
		{
			name: "text/event-stream - none allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("bar", "A test tool", func() string { return "Bar" }),
				testkit.WithSSEClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected: []any{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a Cedar authorizer
			authorizer, err := cedar.NewCedarAuthorizer(
				cedar.ConfigOptions{
					Policies:     tc.policies,
					EntitiesJSON: `[]`,
				},
			)
			require.NoError(t, err, "Failed to create Cedar authorizer")

			claims := jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			}

			opts := tc.teskitOpts
			opts = append(opts, testkit.WithMiddlewares(
				func(h http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						identity := &auth.Identity{
							Subject: claims["sub"].(string),
							Name:    claims["name"].(string),
							Claims:  claims,
						}
						r = r.WithContext(auth.WithIdentity(r.Context(), identity))
						h.ServeHTTP(w, r)
					})
				},
				mcpparser.ParsingMiddleware,
				func(h http.Handler) http.Handler { return Middleware(authorizer, h) },
			))
			server, client, err := testkit.NewStreamableTestServer(opts...)
			require.NoError(t, err)
			defer server.Close()

			respBody, err := client.ToolsList()
			require.NoError(t, err)

			var rpc map[string]any
			err = json.Unmarshal(respBody, &rpc)
			require.NoError(t, err)

			assert.Equal(t, "2.0", rpc["jsonrpc"])
			require.NotNil(t, rpc["result"])

			result, ok := rpc["result"].(map[string]any)
			require.True(t, ok)

			tools, ok := result["tools"].([]any)
			require.True(t, ok)
			require.Equal(t, len(tc.expected), len(tools), "Tool count should match: '%+v' '%+v'", tc.expected, tools)

			for _, expected := range tc.expected {
				expected, ok := expected.(map[string]any)
				require.True(t, ok)
				found := false

				for _, tool := range tools {
					tool, ok := tool.(map[string]any)
					require.True(t, ok)

					if tool["name"] == expected["name"] {
						found = true
						assert.Equal(t, expected["description"], tool["description"])
						assert.Equal(t, expected["name"], tool["name"])
					}
				}

				require.True(t, found, "Tool %s not found", expected["name"])
			}
		})
	}
}

func TestMiddlewareToolsCallTestkit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		teskitOpts    []testkit.TestMCPServerOption
		policies      []string
		expected      any
		expectedError bool
	}{
		// application/json tests
		{
			name: "application/json - all allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("foo", "A test tool", func() string { return "Foo" }),
				testkit.WithJSONClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected: "Foo",
		},
		{
			name: "application/json - one allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("foo", "A test tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("bar", "A test tool", func() string { return "Bar" }),
				testkit.WithJSONClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected: "Foo",
		},
		{
			name: "application/json - none allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("bar", "A test tool", func() string { return "Bar" }),
				testkit.WithJSONClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected:      nil,
			expectedError: true,
		},

		// text/event-stream tests
		{
			name: "text/event-stream - all allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("foo", "A test tool", func() string { return "Foo" }),
				testkit.WithSSEClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected: "Foo",
		},
		{
			name: "text/event-stream - one allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("foo", "A test tool", func() string { return "Foo" }),
				//nolint:goconst
				testkit.WithTool("bar", "A test tool", func() string { return "Bar" }),
				testkit.WithSSEClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected: "Foo",
		},
		{
			name: "text/event-stream - none allowed",
			teskitOpts: []testkit.TestMCPServerOption{
				//nolint:goconst
				testkit.WithTool("bar", "A test tool", func() string { return "Bar" }),
				testkit.WithSSEClientType(),
			},
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"foo");`,
			},
			expected:      nil,
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a Cedar authorizer
			authorizer, err := cedar.NewCedarAuthorizer(
				cedar.ConfigOptions{
					Policies:     tc.policies,
					EntitiesJSON: `[]`,
				},
			)
			require.NoError(t, err, "Failed to create Cedar authorizer")

			claims := jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			}

			opts := tc.teskitOpts
			opts = append(opts, testkit.WithMiddlewares(
				func(h http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						identity := &auth.Identity{
							Subject: claims["sub"].(string),
							Name:    claims["name"].(string),
							Claims:  claims,
						}
						r = r.WithContext(auth.WithIdentity(r.Context(), identity))
						h.ServeHTTP(w, r)
					})
				},
				mcpparser.ParsingMiddleware,
				func(h http.Handler) http.Handler { return Middleware(authorizer, h) },
			))
			server, client, err := testkit.NewStreamableTestServer(opts...)
			require.NoError(t, err)
			defer server.Close()

			respBody, err := client.ToolsCall("foo")
			require.NoError(t, err)

			var rpc map[string]any
			err = json.Unmarshal(respBody, &rpc)
			require.NoError(t, err)

			assert.Equal(t, "2.0", rpc["jsonrpc"])

			if tc.expected != nil {
				require.NotNil(t, rpc["result"], "Result is nil: %+v", string(respBody))

				result, ok := rpc["result"].(map[string]any)
				require.True(t, ok)

				tools, ok := result["content"].([]any)
				require.True(t, ok)

				toolRes, ok := tools[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, tc.expected, toolRes["text"])
			}
			if tc.expectedError {
				require.NotNil(t, rpc["error"])
			}
		})
	}
}

// TestConvertToJSONRPC2ID tests the convertToJSONRPC2ID function with various ID types
func TestConvertToJSONRPC2ID(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		input       interface{}
		expectError bool
	}{
		{
			name:        "nil ID",
			input:       nil,
			expectError: false,
		},
		{
			name:        "string ID",
			input:       "test-id",
			expectError: false,
		},
		{
			name:        "int ID",
			input:       42,
			expectError: false,
		},
		{
			name:        "int64 ID",
			input:       int64(123456789),
			expectError: false,
		},
		{
			name:        "float64 ID (JSON number)",
			input:       float64(99.0),
			expectError: false,
		},
		{
			name:        "unsupported type (slice)",
			input:       []string{"invalid"},
			expectError: true,
		},
		{
			name:        "unsupported type (map)",
			input:       map[string]string{"key": "value"},
			expectError: true,
		},
		{
			name:        "unsupported type (struct)",
			input:       struct{ Name string }{Name: "test"},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := convertToJSONRPC2ID(tc.input)

			if tc.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported ID type")
			} else {
				assert.NoError(t, err)
				// For nil input, we expect an empty ID
				if tc.input == nil {
					assert.Equal(t, jsonrpc2.ID{}, result)
				} else {
					// For other valid inputs, we just verify no error
					assert.NotNil(t, result)
				}
			}
		})
	}
}

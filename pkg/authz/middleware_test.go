// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/test/testkit"
)

// stubAuthorizer is a minimal Authorizer for unit tests, avoiding Cedar setup overhead.
type stubAuthorizer struct {
	allowed    bool
	err        error
	lastToolID string
	lastCtx    context.Context
}

func (s *stubAuthorizer) AuthorizeWithJWTClaims(
	ctx context.Context,
	_ authorizers.MCPFeature,
	_ authorizers.MCPOperation,
	resourceID string,
	_ map[string]interface{},
) (bool, error) {
	s.lastToolID = resourceID
	s.lastCtx = ctx
	return s.allowed, s.err
}

func TestMiddleware(t *testing.T) {
	t.Parallel()

	// Create a Cedar authorizer
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
			`permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`,
			`permit(principal, action == Action::"read_resource", resource == Resource::"data");`,
		},
		EntitiesJSON: `[]`,
	}, "")
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
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "test-user", Claims: tc.claims}}
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
			middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, handler, nil))

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
	}, "")
	require.NoError(t, err, "Failed to create Cedar authorizer")

	// Create a handler that records if it was called
	var handlerCalled bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	// Apply the middleware chain: MCP parsing first, then authorization
	middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, handler, nil))

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
				}, "",
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
						identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
							Subject: claims["sub"].(string),
							Name:    claims["name"].(string),
							Claims:  claims,
						}}
						r = r.WithContext(auth.WithIdentity(r.Context(), identity))
						h.ServeHTTP(w, r)
					})
				},
				mcpparser.ParsingMiddleware,
				func(h http.Handler) http.Handler { return Middleware(authorizer, h, nil) },
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
				}, "",
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
						identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
							Subject: claims["sub"].(string),
							Name:    claims["name"].(string),
							Claims:  claims,
						}}
						r = r.WithContext(auth.WithIdentity(r.Context(), identity))
						h.ServeHTTP(w, r)
					})
				},
				mcpparser.ParsingMiddleware,
				func(h http.Handler) http.Handler { return Middleware(authorizer, h, nil) },
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

// TestMiddlewareOptimizerMetaTools tests the optimizer meta-tool interception logic.
// When a tool is in the passThroughTools set, the middleware handles it specially:
//   - call_tool (has "tool_name" in arguments): authorize the inner backend tool
//   - find_tool (no "tool_name" in arguments): allow through as a discovery operation
func TestMiddlewareOptimizerMetaTools(t *testing.T) {
	t.Parallel()

	// Cedar policy that only permits "allowed_backend" — not "call_tool" or "find_tool".
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"allowed_backend");`,
		},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	passThroughTools := map[string]struct{}{
		"call_tool": {},
		"find_tool": {},
	}

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "test-user",
		Claims:  jwt.MapClaims{"sub": "test-user"},
	}}

	makeReq := func(t *testing.T, toolName string, arguments map[string]interface{}) *http.Request {
		t.Helper()
		params := map[string]interface{}{
			"name":      toolName,
			"arguments": arguments,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)
		call, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), "tools/call", json.RawMessage(paramsJSON))
		require.NoError(t, err)
		body, err := jsonrpc2.EncodeMessage(call)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		return req.WithContext(auth.WithIdentity(req.Context(), identity))
	}

	testCases := []struct {
		name             string
		toolName         string
		arguments        map[string]interface{}
		expectStatus     int
		expectHandlerHit bool
	}{
		{
			name:     "call_tool with authorized inner tool passes through",
			toolName: "call_tool",
			arguments: map[string]interface{}{
				"tool_name":  "allowed_backend",
				"parameters": map[string]interface{}{},
			},
			expectStatus:     http.StatusOK,
			expectHandlerHit: true,
		},
		{
			name:     "call_tool with unauthorized inner tool is blocked",
			toolName: "call_tool",
			arguments: map[string]interface{}{
				"tool_name":  "forbidden_backend",
				"parameters": map[string]interface{}{},
			},
			expectStatus:     http.StatusForbidden,
			expectHandlerHit: false,
		},
		{
			name:     "find_tool request reaches handler (response filtering applied separately)",
			toolName: "find_tool",
			arguments: map[string]interface{}{
				"tool_description": "search for web tools",
			},
			expectStatus:     http.StatusOK,
			expectHandlerHit: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var handlerCalled bool
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			})

			mw := mcpparser.ParsingMiddleware(Middleware(authorizer, handler, passThroughTools))
			rr := httptest.NewRecorder()
			mw.ServeHTTP(rr, makeReq(t, tc.toolName, tc.arguments))

			assert.Equal(t, tc.expectStatus, rr.Code)
			assert.Equal(t, tc.expectHandlerHit, handlerCalled)
		})
	}
}

// TestMiddlewareOptimizerCallToolJSONRoundTrip verifies that the middleware correctly
// extracts tool_name from call_tool arguments that have been serialized via
// optimizer.CallToolInput. This catches argument key mismatches between the struct's
// JSON tag ("tool_name") and what the middleware looks up in the parsed arguments map.
func TestMiddlewareOptimizerCallToolJSONRoundTrip(t *testing.T) {
	t.Parallel()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"backend_fetch");`,
		},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	passThroughTools := map[string]struct{}{
		"call_tool": {},
		"find_tool": {},
	}

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "test-user",
		Claims:  jwt.MapClaims{"sub": "test-user"},
	}}

	// Simulate what the optimizer client sends on the wire: marshal CallToolInput to
	// JSON, then unmarshal into the generic arguments map that the middleware receives.
	input := optimizer.CallToolInput{
		ToolName:   "backend_fetch",
		Parameters: map[string]any{"url": "https://example.com"},
	}
	inputJSON, err := json.Marshal(input)
	require.NoError(t, err)
	var arguments map[string]interface{}
	require.NoError(t, json.Unmarshal(inputJSON, &arguments))

	params := map[string]interface{}{
		"name":      "call_tool",
		"arguments": arguments,
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)
	call, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), "tools/call", json.RawMessage(paramsJSON))
	require.NoError(t, err)
	body, err := jsonrpc2.EncodeMessage(call)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	var handlerCalled bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	mw := mcpparser.ParsingMiddleware(Middleware(authorizer, handler, passThroughTools))
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code, "authorized call_tool should pass through")
	assert.True(t, handlerCalled, "handler should be called for authorized call_tool")
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

			result, err := mcpparser.ConvertToJSONRPC2ID(tc.input)

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

func TestAuthorizeAndServe(t *testing.T) {
	t.Parallel()

	featureOp := featureOperation{Feature: authorizers.MCPFeatureTool, Operation: authorizers.MCPOperationCall}

	testCases := []struct {
		name              string
		allowed           bool
		authErr           error
		cacheAnnotation   *authorizers.ToolAnnotations // nil = no cache entry
		expectHandlerHit  bool
		expectStatus      int
		expectAnnotations bool // whether annotations should be in handler context
	}{
		{
			name:             "authorized with cache miss — next called, no annotations",
			allowed:          true,
			expectHandlerHit: true,
			expectStatus:     http.StatusOK,
		},
		{
			name:              "authorized with cache hit — next called and annotations injected",
			allowed:           true,
			cacheAnnotation:   &authorizers.ToolAnnotations{ReadOnlyHint: boolPtr(true)},
			expectHandlerHit:  true,
			expectStatus:      http.StatusOK,
			expectAnnotations: true,
		},
		{
			name:             "unauthorized (deny) — 403, next not called",
			allowed:          false,
			expectHandlerHit: false,
			expectStatus:     http.StatusForbidden,
		},
		{
			name:             "authorizer error — 403, next not called",
			allowed:          false,
			authErr:          errors.New("policy evaluation failed"),
			expectHandlerHit: false,
			expectStatus:     http.StatusForbidden,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cache := NewAnnotationCache()
			if tc.cacheAnnotation != nil {
				cache.Set("weather", tc.cacheAnnotation)
			}

			stub := &stubAuthorizer{allowed: tc.allowed, err: tc.authErr}

			var (
				handlerCalled bool
				ctxInHandler  context.Context
			)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				ctxInHandler = r.Context()
				w.WriteHeader(http.StatusOK)
			})

			req, err := http.NewRequest(http.MethodPost, "/messages", nil)
			require.NoError(t, err)
			rr := httptest.NewRecorder()

			authorizeAndServe(rr, req, stub, cache, featureOp.Feature, featureOp.Operation, 1, "weather", nil, next)

			assert.Equal(t, tc.expectHandlerHit, handlerCalled)
			assert.Equal(t, tc.expectStatus, rr.Code)
			if tc.expectAnnotations {
				ann := authorizers.ToolAnnotationsFromContext(ctxInHandler)
				require.NotNil(t, ann)
				assert.Equal(t, tc.cacheAnnotation, ann)
			}
		})
	}
}

func TestHandleToolsCall(t *testing.T) {
	t.Parallel()

	featureOp := featureOperation{Feature: authorizers.MCPFeatureTool, Operation: authorizers.MCPOperationCall}

	passThroughTools := map[string]struct{}{
		"call_tool": {},
		"find_tool": {},
	}

	testCases := []struct {
		name              string
		toolName          string // parsedRequest.ResourceID
		arguments         map[string]interface{}
		allowed           bool
		cacheAnnotation   *authorizers.ToolAnnotations // keyed by inner tool name
		expectHandlerHit  bool
		expectStatus      int
		expectAnnotations bool
	}{
		{
			name:     "call_tool with authorized inner tool — next called",
			toolName: "call_tool",
			arguments: map[string]interface{}{
				"tool_name":  "allowed_backend",
				"parameters": map[string]interface{}{"k": "v"},
			},
			allowed:          true,
			expectHandlerHit: true,
			expectStatus:     http.StatusOK,
		},
		{
			name:     "call_tool with unauthorized inner tool — 403",
			toolName: "call_tool",
			arguments: map[string]interface{}{
				"tool_name":  "forbidden_backend",
				"parameters": map[string]interface{}{},
			},
			allowed:          false,
			expectHandlerHit: false,
			expectStatus:     http.StatusForbidden,
		},
		{
			name:     "call_tool injects inner tool annotations from cache",
			toolName: "call_tool",
			arguments: map[string]interface{}{
				"tool_name":  "annotated_backend",
				"parameters": map[string]interface{}{},
			},
			allowed:           true,
			cacheAnnotation:   &authorizers.ToolAnnotations{DestructiveHint: boolPtr(true)},
			expectHandlerHit:  true,
			expectStatus:      http.StatusOK,
			expectAnnotations: true,
		},
		{
			// call_tool with no tool_name arg falls through to the find_tool path:
			// it is allowed through as a discovery operation with no auth check.
			name:     "call_tool with empty tool_name — passes through as discovery",
			toolName: "call_tool",
			arguments: map[string]interface{}{
				"tool_name": "",
			},
			allowed:          false, // auth would deny, but it should never be reached
			expectHandlerHit: true,
			expectStatus:     http.StatusOK,
		},
		{
			// find_tool has no tool_name argument — the request reaches the handler
			// and the response is filtered by Cedar before being returned.
			name:     "find_tool — request reaches handler, response filtering applied",
			toolName: "find_tool",
			arguments: map[string]interface{}{
				"tool_description": "search for web tools",
			},
			allowed:          false, // auth is not checked on the request itself
			expectHandlerHit: true,
			expectStatus:     http.StatusOK,
		},
		{
			name:     "normal tool (not pass-through) — authorized, next called",
			toolName: "weather",
			arguments: map[string]interface{}{
				"location": "NYC",
			},
			allowed:          true,
			expectHandlerHit: true,
			expectStatus:     http.StatusOK,
		},
		{
			name:     "normal tool (not pass-through) — denied, 403",
			toolName: "weather",
			arguments: map[string]interface{}{
				"location": "NYC",
			},
			allowed:          false,
			expectHandlerHit: false,
			expectStatus:     http.StatusForbidden,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cache := NewAnnotationCache()
			// For call_tool tests that expect annotations, cache them under the inner tool name.
			if tc.cacheAnnotation != nil {
				innerName, _ := tc.arguments["tool_name"].(string)
				cache.Set(innerName, tc.cacheAnnotation)
			}

			stub := &stubAuthorizer{allowed: tc.allowed}

			var (
				handlerCalled bool
				ctxInHandler  context.Context
			)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				ctxInHandler = r.Context()
				w.WriteHeader(http.StatusOK)
			})

			params := map[string]interface{}{
				"name":      tc.toolName,
				"arguments": tc.arguments,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)
			call, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), "tools/call", json.RawMessage(paramsJSON))
			require.NoError(t, err)
			body, err := jsonrpc2.EncodeMessage(call)
			require.NoError(t, err)
			req, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")

			parsedReq := &mcpparser.ParsedMCPRequest{
				Method:     "tools/call",
				ResourceID: tc.toolName,
				Arguments:  tc.arguments,
				ID:         float64(1),
			}

			rr := httptest.NewRecorder()
			handleToolsCall(rr, req, stub, parsedReq, featureOp, cache, passThroughTools, next)

			assert.Equal(t, tc.expectHandlerHit, handlerCalled)
			assert.Equal(t, tc.expectStatus, rr.Code)
			if tc.expectAnnotations {
				ann := authorizers.ToolAnnotationsFromContext(ctxInHandler)
				require.NotNil(t, ann)
				assert.Equal(t, tc.cacheAnnotation, ann)
			}
		})
	}
}

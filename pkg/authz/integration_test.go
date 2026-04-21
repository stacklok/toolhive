// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// makeUnsignedJWT creates a JWT with the given claims using the "none" algorithm.
// Used only in tests; mirrors the helper in the cedar package tests.
func makeUnsignedJWT(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err, "makeUnsignedJWT")
	return signed
}

// TestIntegrationListFiltering demonstrates the complete authorization flow
// with realistic policies and shows how list responses are filtered
func TestIntegrationListFiltering(t *testing.T) {
	t.Parallel()

	// Create a realistic Cedar authorizer with role-based policies
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			// Basic users can only access weather and news tools
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather") when { principal.claim_role == "user" };`,
			`permit(principal, action == Action::"call_tool", resource == Tool::"news") when { principal.claim_role == "user" };`,

			// Admin users can access all tools
			`permit(principal, action == Action::"call_tool", resource) when { principal.claim_role == "admin" };`,

			// Basic users can only access public prompts
			`permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting") when { principal.claim_role == "user" };`,
			`permit(principal, action == Action::"get_prompt", resource == Prompt::"help") when { principal.claim_role == "user" };`,

			// Admin users can access all prompts
			`permit(principal, action == Action::"get_prompt", resource) when { principal.claim_role == "admin" };`,

			// Only admin users can access sensitive resources
			`permit(principal, action == Action::"read_resource", resource == Resource::"public_data") when { principal.claim_role == "user" };`,
			`permit(principal, action == Action::"read_resource", resource) when { principal.claim_role == "admin" };`,
		},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err, "Failed to create Cedar authorizer")

	testCases := []struct {
		name          string
		userRole      string
		method        string
		mockResponse  interface{}
		expectedItems []string
		description   string
	}{
		{
			name:     "Basic user sees filtered tools list",
			userRole: "user",
			method:   string(mcp.MethodToolsList),
			mockResponse: mcp.ListToolsResult{
				Tools: []mcp.Tool{
					{Name: "weather", Description: "Get weather information"},
					{Name: "news", Description: "Get latest news"},
					{Name: "admin_tool", Description: "Admin-only tool"},
					{Name: "calculator", Description: "Perform calculations"},
					{Name: "database", Description: "Database access"},
				},
			},
			expectedItems: []string{"weather", "news"},
			description:   "Basic user should only see weather and news tools",
		},
		{
			name:     "Admin user sees all tools",
			userRole: "admin",
			method:   string(mcp.MethodToolsList),
			mockResponse: mcp.ListToolsResult{
				Tools: []mcp.Tool{
					{Name: "weather", Description: "Get weather information"},
					{Name: "news", Description: "Get latest news"},
					{Name: "admin_tool", Description: "Admin-only tool"},
					{Name: "calculator", Description: "Perform calculations"},
					{Name: "database", Description: "Database access"},
				},
			},
			expectedItems: []string{"weather", "news", "admin_tool", "calculator", "database"},
			description:   "Admin user should see all tools",
		},
		{
			name:     "Basic user sees filtered prompts list",
			userRole: "user",
			method:   string(mcp.MethodPromptsList),
			mockResponse: mcp.ListPromptsResult{
				Prompts: []mcp.Prompt{
					{Name: "greeting", Description: "Generate greetings"},
					{Name: "help", Description: "Generate help text"},
					{Name: "admin_prompt", Description: "Admin-only prompt"},
					{Name: "system_prompt", Description: "System configuration prompt"},
				},
			},
			expectedItems: []string{"greeting", "help"},
			description:   "Basic user should only see public prompts",
		},
		{
			name:     "Admin user sees all prompts",
			userRole: "admin",
			method:   string(mcp.MethodPromptsList),
			mockResponse: mcp.ListPromptsResult{
				Prompts: []mcp.Prompt{
					{Name: "greeting", Description: "Generate greetings"},
					{Name: "help", Description: "Generate help text"},
					{Name: "admin_prompt", Description: "Admin-only prompt"},
					{Name: "system_prompt", Description: "System configuration prompt"},
				},
			},
			expectedItems: []string{"greeting", "help", "admin_prompt", "system_prompt"},
			description:   "Admin user should see all prompts",
		},
		{
			name:     "Basic user sees filtered resources list",
			userRole: "user",
			method:   string(mcp.MethodResourcesList),
			mockResponse: mcp.ListResourcesResult{
				Resources: []mcp.Resource{
					{URI: "public_data", Name: "Public Data"},
					{URI: "private_data", Name: "Private Data"},
					{URI: "admin_config", Name: "Admin Configuration"},
					{URI: "user_logs", Name: "User Logs"},
				},
			},
			expectedItems: []string{"public_data"},
			description:   "Basic user should only see public resources",
		},
		{
			name:     "Admin user sees all resources",
			userRole: "admin",
			method:   string(mcp.MethodResourcesList),
			mockResponse: mcp.ListResourcesResult{
				Resources: []mcp.Resource{
					{URI: "public_data", Name: "Public Data"},
					{URI: "private_data", Name: "Private Data"},
					{URI: "admin_config", Name: "Admin Configuration"},
					{URI: "user_logs", Name: "User Logs"},
				},
			},
			expectedItems: []string{"public_data", "private_data", "admin_config", "user_logs"},
			description:   "Admin user should see all resources",
		},
		{
			name:     "Unknown user with no permissions sees empty tools list",
			userRole: "guest",
			method:   string(mcp.MethodToolsList),
			mockResponse: mcp.ListToolsResult{
				Tools: []mcp.Tool{
					{Name: "weather", Description: "Get weather information"},
					{Name: "news", Description: "Get latest news"},
					{Name: "admin_tool", Description: "Admin-only tool"},
					{Name: "calculator", Description: "Perform calculations"},
					{Name: "database", Description: "Database access"},
				},
			},
			expectedItems: []string{}, // Empty list - no permissions
			description:   "Guest user with no defined permissions should see no tools",
		},
		{
			name:     "Unknown user with no permissions sees empty prompts list",
			userRole: "guest",
			method:   string(mcp.MethodPromptsList),
			mockResponse: mcp.ListPromptsResult{
				Prompts: []mcp.Prompt{
					{Name: "greeting", Description: "Generate greetings"},
					{Name: "help", Description: "Generate help text"},
					{Name: "admin_prompt", Description: "Admin-only prompt"},
					{Name: "system_prompt", Description: "System configuration prompt"},
				},
			},
			expectedItems: []string{}, // Empty list - no permissions
			description:   "Guest user with no defined permissions should see no prompts",
		},
		{
			name:     "Unknown user with no permissions sees empty resources list",
			userRole: "guest",
			method:   string(mcp.MethodResourcesList),
			mockResponse: mcp.ListResourcesResult{
				Resources: []mcp.Resource{
					{URI: "public_data", Name: "Public Data"},
					{URI: "private_data", Name: "Private Data"},
					{URI: "admin_config", Name: "Admin Configuration"},
					{URI: "user_logs", Name: "User Logs"},
				},
			},
			expectedItems: []string{}, // Empty list - no permissions
			description:   "Guest user with no defined permissions should see no resources",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create JWT claims for the test user
			claims := jwt.MapClaims{
				"sub":  "testuser123",
				"name": "Test User",
				"role": tc.userRole,
			}

			// Create a mock MCP server response
			responseData, err := json.Marshal(tc.mockResponse)
			require.NoError(t, err, "Failed to marshal mock response")

			jsonrpcResponse := &jsonrpc2.Response{
				ID:     jsonrpc2.Int64ID(1),
				Result: json.RawMessage(responseData),
			}

			responseBytes, err := jsonrpc2.EncodeMessage(jsonrpcResponse)
			require.NoError(t, err, "Failed to encode JSON-RPC response")

			// Create a mock MCP server that returns the test data
			// TODO: we should port this to testkit
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, err := w.Write(responseBytes)
				require.NoError(t, err, "Failed to write mock response")
			}))
			defer mockServer.Close()

			// Create a JSON-RPC request for the list operation
			listRequest, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), tc.method, json.RawMessage(`{}`))
			require.NoError(t, err, "Failed to create JSON-RPC request")

			requestJSON, err := jsonrpc2.EncodeMessage(listRequest)
			require.NoError(t, err, "Failed to encode JSON-RPC request")

			// Create an HTTP request with JWT claims in context
			req, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(requestJSON))
			require.NoError(t, err, "Failed to create HTTP request")
			req.Header.Set("Content-Type", "application/json")
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: claims["sub"].(string), Claims: claims}}
			req = req.WithContext(auth.WithIdentity(req.Context(), identity))

			// Create a response recorder
			rr := httptest.NewRecorder()

			// Create a mock handler that simulates the MCP server response
			mockHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, err := w.Write(responseBytes)
				require.NoError(t, err, "Failed to write mock handler response")
			})

			// Apply the middleware chain: MCP parsing first, then authorization
			middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, mockHandler, nil))

			// Execute the request through the middleware
			middleware.ServeHTTP(rr, req)

			// Verify the response was successful
			assert.Equal(t, http.StatusOK, rr.Code, "Response should be successful")

			// Parse the filtered response
			var message jsonrpc2.Message
			message, err = jsonrpc2.DecodeMessage(rr.Body.Bytes())
			require.NoError(t, err, "Failed to decode JSON-RPC response")

			filteredResponse, ok := message.(*jsonrpc2.Response)
			require.True(t, ok, "Response should be a JSON-RPC response")

			// Verify no error in the response
			assert.Nil(t, filteredResponse.Error, "Response should not have an error")
			assert.NotNil(t, filteredResponse.Result, "Response should have a result")

			// Parse and verify the filtered items based on the method type
			switch tc.method {
			case string(mcp.MethodToolsList):
				var result mcp.ListToolsResult
				err = json.Unmarshal(filteredResponse.Result, &result)
				require.NoError(t, err, "Failed to unmarshal tools result")

				actualNames := make([]string, len(result.Tools))
				for i, tool := range result.Tools {
					actualNames[i] = tool.Name
				}

				assert.ElementsMatch(t, tc.expectedItems, actualNames,
					"Filtered tools should match expected items. %s", tc.description)

			case string(mcp.MethodPromptsList):
				var result mcp.ListPromptsResult
				err = json.Unmarshal(filteredResponse.Result, &result)
				require.NoError(t, err, "Failed to unmarshal prompts result")

				actualNames := make([]string, len(result.Prompts))
				for i, prompt := range result.Prompts {
					actualNames[i] = prompt.Name
				}

				assert.ElementsMatch(t, tc.expectedItems, actualNames,
					"Filtered prompts should match expected items. %s", tc.description)

			case string(mcp.MethodResourcesList):
				var result mcp.ListResourcesResult
				err = json.Unmarshal(filteredResponse.Result, &result)
				require.NoError(t, err, "Failed to unmarshal resources result")

				actualURIs := make([]string, len(result.Resources))
				for i, resource := range result.Resources {
					actualURIs[i] = resource.URI
				}

				assert.ElementsMatch(t, tc.expectedItems, actualURIs,
					"Filtered resources should match expected items. %s", tc.description)
			}

			t.Logf("✅ %s: Expected %d items, got %d items",
				tc.description, len(tc.expectedItems), len(tc.expectedItems))
		})
	}
}

// TestIntegrationNonListOperations verifies that non-list operations still work correctly
func TestIntegrationNonListOperations(t *testing.T) {
	t.Parallel()
	// Create a Cedar authorizer with specific permissions
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather") when { principal.claim_role == "user" };`,
			`permit(principal, action == Action::"call_tool", resource) when { principal.claim_role == "admin" };`,
		},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err, "Failed to create Cedar authorizer")

	testCases := []struct {
		name          string
		userRole      string
		method        string
		toolName      string
		expectAllowed bool
		description   string
	}{
		{
			name:          "Basic user can call allowed tool",
			userRole:      "user",
			method:        string(mcp.MethodToolsCall),
			toolName:      "weather",
			expectAllowed: true,
			description:   "Basic user should be able to call weather tool",
		},
		{
			name:          "Basic user cannot call restricted tool",
			userRole:      "user",
			method:        string(mcp.MethodToolsCall),
			toolName:      "admin_tool",
			expectAllowed: false,
			description:   "Basic user should not be able to call admin tool",
		},
		{
			name:          "Admin user can call any tool",
			userRole:      "admin",
			method:        string(mcp.MethodToolsCall),
			toolName:      "admin_tool",
			expectAllowed: true,
			description:   "Admin user should be able to call any tool",
		},
		{
			name:          "Guest user with no permissions cannot call any tool",
			userRole:      "guest",
			method:        string(mcp.MethodToolsCall),
			toolName:      "weather",
			expectAllowed: false,
			description:   "Guest user with no defined permissions should not be able to call any tool",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create JWT claims for the test user
			claims := jwt.MapClaims{
				"sub":  "testuser123",
				"name": "Test User",
				"role": tc.userRole,
			}

			// Create a JSON-RPC request for the tool call
			params := map[string]interface{}{
				"name": tc.toolName,
				"arguments": map[string]interface{}{
					"location": "New York",
				},
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err, "Failed to marshal params")

			callRequest, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), tc.method, json.RawMessage(paramsJSON))
			require.NoError(t, err, "Failed to create JSON-RPC request")

			requestJSON, err := jsonrpc2.EncodeMessage(callRequest)
			require.NoError(t, err, "Failed to encode JSON-RPC request")

			// Create an HTTP request with JWT claims in context
			req, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(requestJSON))
			require.NoError(t, err, "Failed to create HTTP request")
			req.Header.Set("Content-Type", "application/json")
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: claims["sub"].(string), Claims: claims}}
			req = req.WithContext(auth.WithIdentity(req.Context(), identity))

			// Create a response recorder
			rr := httptest.NewRecorder()

			// Create a mock handler that would be called if authorized
			var handlerCalled bool
			mockHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
				_, err := w.Write([]byte(`{"result": "success"}`))
				require.NoError(t, err, "Failed to write mock response")
			})

			// Apply the middleware chain: MCP parsing first, then authorization
			middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, mockHandler, nil))

			// Execute the request through the middleware
			middleware.ServeHTTP(rr, req)

			// Verify the authorization result
			if tc.expectAllowed {
				assert.Equal(t, http.StatusOK, rr.Code, "Request should be authorized")
				assert.True(t, handlerCalled, "Handler should be called for authorized request")
				t.Logf("✅ %s: Request was correctly authorized", tc.description)
			} else {
				assert.Equal(t, http.StatusForbidden, rr.Code, "Request should be forbidden")
				assert.False(t, handlerCalled, "Handler should not be called for unauthorized request")
				t.Logf("✅ %s: Request was correctly denied", tc.description)
			}
		})
	}
}

// TestIntegrationGroupBasedListFiltering tests the complete middleware pipeline
// with group-based Cedar policies (principal in THVGroup::"...") and realistic
// IDP claim formats. This exercises a fundamentally different Cedar evaluation
// path than TestIntegrationListFiltering: entity hierarchy vs context record.
func TestIntegrationGroupBasedListFiltering(t *testing.T) {
	t.Parallel()

	// marshalMockResponse builds the JSON-RPC response bytes for a given result type.
	marshalMockResponse := func(t *testing.T, result interface{}) []byte {
		t.Helper()
		responseData, err := json.Marshal(result)
		require.NoError(t, err)
		jsonrpcResponse := &jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: json.RawMessage(responseData)}
		responseBytes, err := jsonrpc2.EncodeMessage(jsonrpcResponse)
		require.NoError(t, err)
		return responseBytes
	}

	allTools := mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "deploy", Description: "Deploy service"},
			{Name: "rollback", Description: "Rollback deployment"},
			{Name: "logs", Description: "View logs"},
		},
	}
	allPrompts := mcp.ListPromptsResult{
		Prompts: []mcp.Prompt{
			{Name: "greeting", Description: "Generate greetings"},
			{Name: "farewell", Description: "Generate farewells"},
		},
	}
	allResources := mcp.ListResourcesResult{
		Resources: []mcp.Resource{
			{URI: "public_data", Name: "Public Data"},
			{URI: "private_data", Name: "Private Data"},
		},
	}

	testCases := []struct {
		name              string
		groupClaim        string
		roleClaim         string
		entitiesJSON      string
		policies          []string
		claims            jwt.MapClaims
		method            string
		mockResponseBytes []byte
		expectedItems     []string
	}{
		{
			name:       "Entra-like dual claim: role grants access",
			groupClaim: "groups",
			roleClaim:  "roles",
			policies: []string{
				`permit(principal in THVGroup::"developer", action == Action::"call_tool", resource == Tool::"deploy");`,
			},
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
				"roles":  []interface{}{"developer"},
			},
			method:        string(mcp.MethodToolsList),
			expectedItems: []string{"deploy"},
		},
		{
			name:       "Entra groups only: UUID match",
			groupClaim: "groups",
			policies: []string{
				`permit(principal in THVGroup::"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", action == Action::"call_tool", resource);`,
			},
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "11111111-2222-3333-4444-555555555555"},
			},
			method:        string(mcp.MethodToolsList),
			expectedItems: []string{"deploy", "rollback", "logs"},
		},
		{
			name:       "Okta-like: URI claim with display names",
			groupClaim: "https://example.com/groups",
			policies: []string{
				`permit(principal in THVGroup::"platform", action == Action::"call_tool", resource == Tool::"deploy");`,
				`permit(principal in THVGroup::"platform", action == Action::"call_tool", resource == Tool::"logs");`,
			},
			claims: jwt.MapClaims{
				"sub":                        "user1",
				"https://example.com/groups": []interface{}{"platform", "frontend"},
			},
			method:        string(mcp.MethodToolsList),
			expectedItems: []string{"deploy", "logs"},
		},
		{
			name:       "Keycloak-like: nested dot-notation claim",
			groupClaim: "realm_access.roles",
			policies: []string{
				`permit(principal in THVGroup::"editor", action == Action::"call_tool", resource);`,
			},
			claims: jwt.MapClaims{
				"sub": "user1",
				"realm_access": map[string]interface{}{
					"roles": []interface{}{"editor", "viewer"},
				},
			},
			method:        string(mcp.MethodToolsList),
			expectedItems: []string{"deploy", "rollback", "logs"},
		},
		{
			name:       "No matching group: empty filtered list",
			groupClaim: "groups",
			policies: []string{
				`permit(principal in THVGroup::"admin", action == Action::"call_tool", resource);`,
			},
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"viewer"},
			},
			method:        string(mcp.MethodToolsList),
			expectedItems: []string{},
		},
		{
			name:       "Prompts list: group grants access to specific prompt",
			groupClaim: "groups",
			policies: []string{
				`permit(principal in THVGroup::"devs", action == Action::"get_prompt", resource == Prompt::"greeting");`,
			},
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"devs"},
			},
			method:        string(mcp.MethodPromptsList),
			expectedItems: []string{"greeting"},
		},
		{
			name:       "Resources list: group grants access to specific resource",
			groupClaim: "groups",
			policies: []string{
				`permit(principal in THVGroup::"devs", action == Action::"read_resource", resource == Resource::"public_data");`,
			},
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"devs"},
			},
			method:        string(mcp.MethodResourcesList),
			expectedItems: []string{"public_data"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			entitiesJSON := tc.entitiesJSON
			if entitiesJSON == "" {
				entitiesJSON = "[]"
			}

			authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
				Policies:       tc.policies,
				EntitiesJSON:   entitiesJSON,
				GroupClaimName: tc.groupClaim,
				RoleClaimName:  tc.roleClaim,
			}, "")
			require.NoError(t, err)

			// Build mock response based on method type if not already provided.
			responseBytes := tc.mockResponseBytes
			if responseBytes == nil {
				switch tc.method {
				case string(mcp.MethodPromptsList):
					responseBytes = marshalMockResponse(t, allPrompts)
				case string(mcp.MethodResourcesList):
					responseBytes = marshalMockResponse(t, allResources)
				default:
					responseBytes = marshalMockResponse(t, allTools)
				}
			}

			mockHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(responseBytes)
			})

			// Build request.
			listReq, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), tc.method, json.RawMessage(`{}`))
			require.NoError(t, err)
			reqJSON, err := jsonrpc2.EncodeMessage(listReq)
			require.NoError(t, err)

			httpReq, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(reqJSON))
			require.NoError(t, err)
			httpReq.Header.Set("Content-Type", "application/json")

			identity := &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Subject: tc.claims["sub"].(string), Claims: tc.claims},
			}
			httpReq = httpReq.WithContext(auth.WithIdentity(httpReq.Context(), identity))

			rr := httptest.NewRecorder()
			middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, mockHandler, nil))
			middleware.ServeHTTP(rr, httpReq)

			assert.Equal(t, http.StatusOK, rr.Code)

			msg, err := jsonrpc2.DecodeMessage(rr.Body.Bytes())
			require.NoError(t, err)
			resp, ok := msg.(*jsonrpc2.Response)
			require.True(t, ok)
			require.Nil(t, resp.Error)

			// Parse and verify the filtered items based on the method type.
			switch tc.method {
			case string(mcp.MethodToolsList):
				var result mcp.ListToolsResult
				err = json.Unmarshal(resp.Result, &result)
				require.NoError(t, err)

				actualNames := make([]string, len(result.Tools))
				for i, tool := range result.Tools {
					actualNames[i] = tool.Name
				}
				assert.ElementsMatch(t, tc.expectedItems, actualNames)

			case string(mcp.MethodPromptsList):
				var result mcp.ListPromptsResult
				err = json.Unmarshal(resp.Result, &result)
				require.NoError(t, err)

				actualNames := make([]string, len(result.Prompts))
				for i, prompt := range result.Prompts {
					actualNames[i] = prompt.Name
				}
				assert.ElementsMatch(t, tc.expectedItems, actualNames)

			case string(mcp.MethodResourcesList):
				var result mcp.ListResourcesResult
				err = json.Unmarshal(resp.Result, &result)
				require.NoError(t, err)

				actualURIs := make([]string, len(result.Resources))
				for i, resource := range result.Resources {
					actualURIs[i] = resource.URI
				}
				assert.ElementsMatch(t, tc.expectedItems, actualURIs)
			}
		})
	}
}

// TestIntegrationGroupBasedNonListOperations tests group-based allow/deny
// decisions for tool calls, prompt gets, and resource reads through the
// full middleware stack.
func TestIntegrationGroupBasedNonListOperations(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		groupClaim    string
		roleClaim     string
		policies      []string
		claims        jwt.MapClaims
		method        string
		params        map[string]interface{}
		expectAllowed bool
	}{
		{
			name:       "Entra dual claim: role grants tool call",
			groupClaim: "groups",
			roleClaim:  "roles",
			policies:   []string{`permit(principal in THVGroup::"developer", action == Action::"call_tool", resource == Tool::"deploy");`},
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"some-uuid"},
				"roles":  []interface{}{"developer"},
			},
			method:        string(mcp.MethodToolsCall),
			params:        map[string]interface{}{"name": "deploy", "arguments": map[string]interface{}{}},
			expectAllowed: true,
		},
		{
			name:       "Entra groups: UUID match allows",
			groupClaim: "groups",
			policies:   []string{`permit(principal in THVGroup::"aaa-bbb", action == Action::"call_tool", resource);`},
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"aaa-bbb"},
			},
			method:        string(mcp.MethodToolsCall),
			params:        map[string]interface{}{"name": "anything", "arguments": map[string]interface{}{}},
			expectAllowed: true,
		},
		{
			name:       "Okta URI claim: display name match",
			groupClaim: "https://example.com/groups",
			policies:   []string{`permit(principal in THVGroup::"platform", action == Action::"call_tool", resource == Tool::"deploy");`},
			claims: jwt.MapClaims{
				"sub":                        "user1",
				"https://example.com/groups": []interface{}{"platform"},
			},
			method:        string(mcp.MethodToolsCall),
			params:        map[string]interface{}{"name": "deploy", "arguments": map[string]interface{}{}},
			expectAllowed: true,
		},
		{
			name:       "Keycloak nested: dot-notation grants access",
			groupClaim: "realm_access.roles",
			policies:   []string{`permit(principal in THVGroup::"editor", action == Action::"call_tool", resource);`},
			claims: jwt.MapClaims{
				"sub": "user1",
				"realm_access": map[string]interface{}{
					"roles": []interface{}{"editor"},
				},
			},
			method:        string(mcp.MethodToolsCall),
			params:        map[string]interface{}{"name": "any-tool", "arguments": map[string]interface{}{}},
			expectAllowed: true,
		},
		{
			name:       "Wrong group: tool call denied",
			groupClaim: "groups",
			policies:   []string{`permit(principal in THVGroup::"admin", action == Action::"call_tool", resource);`},
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"viewer"},
			},
			method:        string(mcp.MethodToolsCall),
			params:        map[string]interface{}{"name": "deploy", "arguments": map[string]interface{}{}},
			expectAllowed: false,
		},
		{
			name:       "No groups claim: tool call denied",
			groupClaim: "groups",
			policies:   []string{`permit(principal in THVGroup::"admin", action == Action::"call_tool", resource);`},
			claims: jwt.MapClaims{
				"sub": "user1",
			},
			method:        string(mcp.MethodToolsCall),
			params:        map[string]interface{}{"name": "deploy", "arguments": map[string]interface{}{}},
			expectAllowed: false,
		},
		{
			name:       "Group grants prompt get",
			groupClaim: "groups",
			policies:   []string{`permit(principal in THVGroup::"devs", action == Action::"get_prompt", resource == Prompt::"greeting");`},
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"devs"},
			},
			method:        string(mcp.MethodPromptsGet),
			params:        map[string]interface{}{"name": "greeting"},
			expectAllowed: true,
		},
		{
			name:       "Group grants resource read",
			groupClaim: "groups",
			policies:   []string{`permit(principal in THVGroup::"devs", action == Action::"read_resource", resource);`},
			claims: jwt.MapClaims{
				"sub":    "user1",
				"groups": []interface{}{"devs"},
			},
			method:        string(mcp.MethodResourcesRead),
			params:        map[string]interface{}{"uri": "public_data"},
			expectAllowed: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
				Policies:       tc.policies,
				EntitiesJSON:   "[]",
				GroupClaimName: tc.groupClaim,
				RoleClaimName:  tc.roleClaim,
			}, "")
			require.NoError(t, err)

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)
			callReq, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), tc.method, json.RawMessage(paramsJSON))
			require.NoError(t, err)
			reqJSON, err := jsonrpc2.EncodeMessage(callReq)
			require.NoError(t, err)

			httpReq, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(reqJSON))
			require.NoError(t, err)
			httpReq.Header.Set("Content-Type", "application/json")

			identity := &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Subject: tc.claims["sub"].(string), Claims: tc.claims},
			}
			httpReq = httpReq.WithContext(auth.WithIdentity(httpReq.Context(), identity))

			rr := httptest.NewRecorder()
			var handlerCalled bool
			mockHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result": "ok"}`))
			})

			middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, mockHandler, nil))
			middleware.ServeHTTP(rr, httpReq)

			if tc.expectAllowed {
				assert.Equal(t, http.StatusOK, rr.Code)
				assert.True(t, handlerCalled, "handler should be called for allowed request")
			} else {
				assert.Equal(t, http.StatusForbidden, rr.Code)
				assert.False(t, handlerCalled, "handler should not be called for denied request")
			}
		})
	}
}

// TestIntegrationTransitiveGroupHierarchy tests that the full middleware stack
// preserves the transitive entity hierarchy from entities_json. When
// THVGroup::"eng" is a child of THVRole::"developer" in static entities, a
// policy requiring "principal in THVRole::developer" must still work for a
// user whose JWT contains groups: ["eng"].
func TestIntegrationTransitiveGroupHierarchy(t *testing.T) {
	t.Parallel()

	entitiesJSON := `[
		{
			"uid": {"type": "THVGroup", "id": "eng"},
			"attrs": {},
			"parents": [{"type": "THVRole", "id": "developer"}]
		},
		{
			"uid": {"type": "THVRole", "id": "developer"},
			"attrs": {},
			"parents": []
		}
	]`

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal in THVRole::"developer", action == Action::"call_tool", resource == Tool::"deploy");`,
		},
		EntitiesJSON:   entitiesJSON,
		GroupClaimName: "groups",
	}, "")
	require.NoError(t, err)

	tests := []struct {
		name          string
		groups        []interface{}
		expectAllowed bool
	}{
		{
			name:          "eng_group_reaches_developer_role_transitively",
			groups:        []interface{}{"eng"},
			expectAllowed: true,
		},
		{
			name:          "wrong_group_denied",
			groups:        []interface{}{"marketing"},
			expectAllowed: false,
		},
		{
			name:          "no_groups_denied",
			groups:        nil,
			expectAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			claims := jwt.MapClaims{"sub": "user1"}
			if tt.groups != nil {
				claims["groups"] = tt.groups
			}

			params, err := json.Marshal(map[string]interface{}{"name": "deploy", "arguments": map[string]interface{}{}})
			require.NoError(t, err)
			callReq, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), string(mcp.MethodToolsCall), json.RawMessage(params))
			require.NoError(t, err)
			reqJSON, err := jsonrpc2.EncodeMessage(callReq)
			require.NoError(t, err)

			httpReq, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(reqJSON))
			require.NoError(t, err)
			httpReq.Header.Set("Content-Type", "application/json")

			identity := &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Subject: "user1", Claims: claims},
			}
			httpReq = httpReq.WithContext(auth.WithIdentity(httpReq.Context(), identity))

			rr := httptest.NewRecorder()
			var handlerCalled bool
			mockHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result": "ok"}`))
			})

			middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, mockHandler, nil))
			middleware.ServeHTTP(rr, httpReq)

			if tt.expectAllowed {
				assert.Equal(t, http.StatusOK, rr.Code)
				assert.True(t, handlerCalled)
			} else {
				assert.Equal(t, http.StatusForbidden, rr.Code)
				assert.False(t, handlerCalled)
			}
		})
	}
}

// TestIntegrationUpstreamProviderGroupAuth verifies that when
// PrimaryUpstreamProvider is configured, group extraction uses the upstream
// token's claims — not the direct request claims — through the full middleware.
func TestIntegrationUpstreamProviderGroupAuth(t *testing.T) {
	t.Parallel()

	const providerName = "idp"

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal in THVGroup::"platform-eng", action == Action::"call_tool", resource);`,
		},
		EntitiesJSON:            "[]",
		GroupClaimName:          "groups",
		PrimaryUpstreamProvider: providerName,
	}, "")
	require.NoError(t, err)

	tests := []struct {
		name          string
		directClaims  jwt.MapClaims
		upstreamToken string
		expectAllowed bool
	}{
		{
			name:         "upstream_groups_authorize",
			directClaims: jwt.MapClaims{"sub": "thv-user"},
			upstreamToken: makeUnsignedJWT(t, jwt.MapClaims{
				"sub":    "upstream-user",
				"groups": []interface{}{"platform-eng"},
			}),
			expectAllowed: true,
		},
		{
			name:         "upstream_wrong_group_denies",
			directClaims: jwt.MapClaims{"sub": "thv-user"},
			upstreamToken: makeUnsignedJWT(t, jwt.MapClaims{
				"sub":    "upstream-user",
				"groups": []interface{}{"marketing"},
			}),
			expectAllowed: false,
		},
		{
			name: "direct_groups_ignored_when_upstream_configured",
			directClaims: jwt.MapClaims{
				"sub":    "thv-user",
				"groups": []interface{}{"platform-eng"},
			},
			upstreamToken: makeUnsignedJWT(t, jwt.MapClaims{
				"sub":    "upstream-user",
				"groups": []interface{}{"other"},
			}),
			expectAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			params, err := json.Marshal(map[string]interface{}{"name": "deploy", "arguments": map[string]interface{}{}})
			require.NoError(t, err)
			callReq, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), string(mcp.MethodToolsCall), json.RawMessage(params))
			require.NoError(t, err)
			reqJSON, err := jsonrpc2.EncodeMessage(callReq)
			require.NoError(t, err)

			httpReq, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(reqJSON))
			require.NoError(t, err)
			httpReq.Header.Set("Content-Type", "application/json")

			identity := &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Subject: tt.directClaims["sub"].(string),
					Claims:  tt.directClaims,
				},
				UpstreamTokens: map[string]string{providerName: tt.upstreamToken},
			}
			httpReq = httpReq.WithContext(auth.WithIdentity(httpReq.Context(), identity))

			rr := httptest.NewRecorder()
			var handlerCalled bool
			mockHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result": "ok"}`))
			})

			middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, mockHandler, nil))
			middleware.ServeHTTP(rr, httpReq)

			if tt.expectAllowed {
				assert.Equal(t, http.StatusOK, rr.Code)
				assert.True(t, handlerCalled)
			} else {
				assert.Equal(t, http.StatusForbidden, rr.Code)
				assert.False(t, handlerCalled)
			}
		})
	}
}

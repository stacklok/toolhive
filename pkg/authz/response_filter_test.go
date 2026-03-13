// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
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

func TestResponseFilteringWriter(t *testing.T) {
	t.Parallel()

	// Create a Cedar authorizer with specific tool permissions
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
			`permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`,
			`permit(principal, action == Action::"read_resource", resource == Resource::"data");`,
		},
		EntitiesJSON: `[]`,
	})
	require.NoError(t, err, "Failed to create Cedar authorizer")

	testCases := []struct {
		name           string
		method         string
		responseData   interface{}
		claims         jwt.MapClaims
		expectedResult interface{}
	}{
		{
			name:   "Filter tools list - user can access weather tool only",
			method: string(mcp.MethodToolsList),
			responseData: mcp.ListToolsResult{
				Tools: []mcp.Tool{
					{Name: "weather", Description: "Get weather information"},
					{Name: "calculator", Description: "Perform calculations"},
					{Name: "translator", Description: "Translate text"},
				},
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectedResult: mcp.ListToolsResult{
				Tools: []mcp.Tool{
					{Name: "weather", Description: "Get weather information"},
				},
			},
		},
		{
			name:   "Filter prompts list - user can access greeting prompt only",
			method: string(mcp.MethodPromptsList),
			responseData: mcp.ListPromptsResult{
				Prompts: []mcp.Prompt{
					{Name: "greeting", Description: "Generate greetings"},
					{Name: "farewell", Description: "Generate farewells"},
				},
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectedResult: mcp.ListPromptsResult{
				Prompts: []mcp.Prompt{
					{Name: "greeting", Description: "Generate greetings"},
				},
			},
		},
		{
			name:   "Filter resources list - user can access data resource only",
			method: string(mcp.MethodResourcesList),
			responseData: mcp.ListResourcesResult{
				Resources: []mcp.Resource{
					{URI: "data", Name: "Data Resource"},
					{URI: "secret", Name: "Secret Resource"},
				},
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectedResult: mcp.ListResourcesResult{
				Resources: []mcp.Resource{
					{URI: "data", Name: "Data Resource"},
				},
			},
		},
		{
			name:   "Empty tools list when user has no permissions",
			method: string(mcp.MethodToolsList),
			responseData: mcp.ListToolsResult{
				Tools: []mcp.Tool{
					{Name: "calculator", Description: "Perform calculations"},
					{Name: "translator", Description: "Translate text"},
				},
			},
			claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectedResult: mcp.ListToolsResult{
				Tools: []mcp.Tool{}, // Empty list since user can't access any of these tools
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create a JSON-RPC response with the test data
			responseData, err := json.Marshal(tc.responseData)
			require.NoError(t, err, "Failed to marshal response data")

			jsonrpcResponse := &jsonrpc2.Response{
				ID:     jsonrpc2.Int64ID(1),
				Result: json.RawMessage(responseData),
			}

			responseBytes, err := jsonrpc2.EncodeMessage(jsonrpcResponse)
			require.NoError(t, err, "Failed to marshal JSON-RPC response")

			// Create an HTTP request with claims in context
			req, err := http.NewRequest(http.MethodPost, "/messages", nil)
			require.NoError(t, err, "Failed to create HTTP request")
			sub := tc.claims["sub"].(string)
			name, _ := tc.claims["name"].(string)
			identity := &auth.Identity{Subject: sub, Name: name, Claims: tc.claims}
			req = req.WithContext(auth.WithIdentity(req.Context(), identity))

			// Create a response recorder
			rr := httptest.NewRecorder()

			// Create the response filtering writer
			filteringWriter := NewResponseFilteringWriter(rr, authorizer, req, tc.method, nil)
			filteringWriter.ResponseWriter.Header().Set("Content-Type", "application/json")

			// Write the response data
			_, err = filteringWriter.Write(responseBytes)
			require.NoError(t, err, "Failed to write response data")

			// Flush the response
			err = filteringWriter.FlushAndFilter()
			require.NoError(t, err, "Failed to flush response")

			// Parse the filtered response
			var message jsonrpc2.Message
			message, err = jsonrpc2.DecodeMessage(rr.Body.Bytes())
			require.NoError(t, err, "Failed to unmarshal filtered response")

			filteredResponse, ok := message.(*jsonrpc2.Response)
			require.True(t, ok, "Response should be a JSON-RPC response")

			// Verify the response was filtered correctly
			assert.Nil(t, filteredResponse.Error, "Response should not have an error")
			assert.NotNil(t, filteredResponse.Result, "Response should have a result")

			// Parse the result based on the method type
			switch tc.method {
			case string(mcp.MethodToolsList):
				var actualResult mcp.ListToolsResult
				err = json.Unmarshal(filteredResponse.Result, &actualResult)
				require.NoError(t, err, "Failed to unmarshal tools result")

				expectedResult := tc.expectedResult.(mcp.ListToolsResult)
				assert.Equal(t, len(expectedResult.Tools), len(actualResult.Tools), "Tool count should match")
				for i, expectedTool := range expectedResult.Tools {
					if i < len(actualResult.Tools) {
						assert.Equal(t, expectedTool.Name, actualResult.Tools[i].Name, "Tool name should match")
						assert.Equal(t, expectedTool.Description, actualResult.Tools[i].Description, "Tool description should match")
					}
				}

			case string(mcp.MethodPromptsList):
				var actualResult mcp.ListPromptsResult
				err = json.Unmarshal(filteredResponse.Result, &actualResult)
				require.NoError(t, err, "Failed to unmarshal prompts result")

				expectedResult := tc.expectedResult.(mcp.ListPromptsResult)
				assert.Equal(t, len(expectedResult.Prompts), len(actualResult.Prompts), "Prompt count should match")
				for i, expectedPrompt := range expectedResult.Prompts {
					if i < len(actualResult.Prompts) {
						assert.Equal(t, expectedPrompt.Name, actualResult.Prompts[i].Name, "Prompt name should match")
						assert.Equal(t, expectedPrompt.Description, actualResult.Prompts[i].Description, "Prompt description should match")
					}
				}

			case string(mcp.MethodResourcesList):
				var actualResult mcp.ListResourcesResult
				err = json.Unmarshal(filteredResponse.Result, &actualResult)
				require.NoError(t, err, "Failed to unmarshal resources result")

				expectedResult := tc.expectedResult.(mcp.ListResourcesResult)
				assert.Equal(t, len(expectedResult.Resources), len(actualResult.Resources), "Resource count should match")
				for i, expectedResource := range expectedResult.Resources {
					if i < len(actualResult.Resources) {
						assert.Equal(t, expectedResource.URI, actualResult.Resources[i].URI, "Resource URI should match")
						assert.Equal(t, expectedResource.Name, actualResult.Resources[i].Name, "Resource name should match")
					}
				}
			}
		})
	}
}

func TestResponseFilteringWriter_NonListOperations(t *testing.T) {
	t.Parallel()
	// Create a Cedar authorizer
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		},
		EntitiesJSON: `[]`,
	})
	require.NoError(t, err, "Failed to create Cedar authorizer")

	// Test that non-list operations pass through unchanged
	testData := map[string]interface{}{
		"result": "some result data",
	}

	responseData, err := json.Marshal(testData)
	require.NoError(t, err, "Failed to marshal response data")

	jsonrpcResponse := &jsonrpc2.Response{
		ID:     jsonrpc2.Int64ID(1),
		Result: json.RawMessage(responseData),
	}

	responseBytes, err := json.Marshal(jsonrpcResponse)
	require.NoError(t, err, "Failed to marshal JSON-RPC response")

	// Create an HTTP request
	req, err := http.NewRequest(http.MethodPost, "/messages", nil)
	require.NoError(t, err, "Failed to create HTTP request")

	// Create a response recorder
	rr := httptest.NewRecorder()

	// Create the response filtering writer for a non-list operation
	filteringWriter := NewResponseFilteringWriter(rr, authorizer, req, "tools/call", nil)

	// Write the response data
	_, err = filteringWriter.Write(responseBytes)
	require.NoError(t, err, "Failed to write response data")

	// Flush the response
	err = filteringWriter.FlushAndFilter()
	require.NoError(t, err, "Failed to flush response")

	// Verify the response passed through unchanged
	assert.Equal(t, responseBytes, rr.Body.Bytes(), "Non-list response should pass through unchanged")
}

func TestResponseFilteringWriter_ErrorResponse(t *testing.T) {
	t.Parallel()
	// Create a Cedar authorizer
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		},
		EntitiesJSON: `[]`,
	})
	require.NoError(t, err, "Failed to create Cedar authorizer")

	// Create an error response
	jsonrpcResponse := &jsonrpc2.Response{
		ID:    jsonrpc2.Int64ID(1),
		Error: jsonrpc2.NewError(404, "Not found"),
	}

	responseBytes, err := json.Marshal(jsonrpcResponse)
	require.NoError(t, err, "Failed to marshal JSON-RPC response")

	// Create an HTTP request
	req, err := http.NewRequest(http.MethodPost, "/messages", nil)
	require.NoError(t, err, "Failed to create HTTP request")

	// Create a response recorder
	rr := httptest.NewRecorder()

	// Create the response filtering writer
	filteringWriter := NewResponseFilteringWriter(rr, authorizer, req, "tools/list", nil)

	// Write the response data
	_, err = filteringWriter.Write(responseBytes)
	require.NoError(t, err, "Failed to write response data")

	// Flush the response
	err = filteringWriter.FlushAndFilter()
	require.NoError(t, err, "Failed to flush response")

	// Verify the error response passed through unchanged
	assert.Equal(t, responseBytes, rr.Body.Bytes(), "Error response should pass through unchanged")
}

// TestResponseFilteringWriter_ContentLengthMismatch reproduces a bug where
// httputil.ReverseProxy copies the backend's Content-Length header to the
// underlying ResponseWriter via Header() (which ResponseFilteringWriter does
// NOT override). When FlushAndFilter later writes a filtered (shorter) body,
// the Content-Length no longer matches the actual body, causing Go's HTTP
// server to produce a truncated or corrupt response.
//
// The bug requires a real HTTP server to manifest because httptest.NewRecorder
// does not enforce Content-Length consistency the way net/http.Server does.
func TestResponseFilteringWriter_ContentLengthMismatch(t *testing.T) {
	t.Parallel()

	// Create a Cedar authorizer that only permits the "weather" tool.
	// The backend will return 3 tools, so filtering will shrink the response.
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		},
		EntitiesJSON: `[]`,
	})
	require.NoError(t, err, "Failed to create Cedar authorizer")

	// Build the backend response: a tools/list result with 3 tools.
	backendResult := mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "calculator", Description: "Perform calculations"},
			{Name: "translator", Description: "Translate text between languages"},
		},
	}
	resultData, err := json.Marshal(backendResult)
	require.NoError(t, err)

	backendRPCResponse := &jsonrpc2.Response{
		ID:     jsonrpc2.Int64ID(1),
		Result: json.RawMessage(resultData),
	}
	backendBody, err := jsonrpc2.EncodeMessage(backendRPCResponse)
	require.NoError(t, err)

	// Create the backend server that returns the full tools/list response
	// with an accurate Content-Length header (as a real MCP server would).
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(backendBody)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(backendBody)
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)

	// Create the frontend server that:
	// 1. Injects identity + parsed MCP request into context (normally done by
	//    auth and parser middleware).
	// 2. Wraps the ResponseWriter with ResponseFilteringWriter (as the authz
	//    middleware does).
	// 3. Proxies to the backend via httputil.ReverseProxy.
	// 4. Calls FlushAndFilter after the proxy returns.
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inject identity into context (Cedar authorizer reads claims from it).
		identity := &auth.Identity{
			Subject: "user123",
			Name:    "Test User",
			Claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "Test User",
			},
		}
		ctx := auth.WithIdentity(r.Context(), identity)

		// Inject parsed MCP request into context (authz middleware reads method from it).
		parsed := &mcpparser.ParsedMCPRequest{
			Method: string(mcp.MethodToolsList),
			ID:     float64(1),
		}
		ctx = context.WithValue(ctx, mcpparser.MCPRequestContextKey, parsed)
		r = r.WithContext(ctx)

		// Wrap the real ResponseWriter with ResponseFilteringWriter,
		// exactly as the authz middleware does in middleware.go.
		filteringWriter := NewResponseFilteringWriter(w, authorizer, r, string(mcp.MethodToolsList), nil)

		// Proxy to the backend. ReverseProxy will call w.Header() to copy
		// the backend's Content-Length into the response header map. Since
		// ResponseFilteringWriter does not override Header(), this goes
		// directly to the real http.ResponseWriter.
		//
		// FlushInterval: -1 matches the production transparent proxy
		// (transparent_proxy.go), which flushes after every write. This is
		// critical: the flush triggers an implicit WriteHeader on the real
		// writer, sending headers (including any stale Content-Length) to
		// the wire before FlushAndFilter() runs.
		proxy := httputil.NewSingleHostReverseProxy(backendURL)
		proxy.FlushInterval = -1
		proxy.ServeHTTP(filteringWriter, r)

		// Flush the filtered (shorter) response to the real writer.
		if flushErr := filteringWriter.FlushAndFilter(); flushErr != nil {
			t.Errorf("FlushAndFilter returned error: %v", flushErr)
		}
	}))
	defer frontend.Close()

	// Build a JSON-RPC tools/list request.
	rpcRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}
	reqBody, err := json.Marshal(rpcRequest)
	require.NoError(t, err)

	// Send the request to the frontend.
	resp, err := http.Post(
		frontend.URL+"/mcp",
		"application/json",
		strings.NewReader(string(reqBody)),
	)
	require.NoError(t, err, "HTTP request to frontend should succeed")
	defer resp.Body.Close()

	// Read the full response body. Because of the Content-Length mismatch bug,
	// Go's HTTP server may tear down the connection, causing an unexpected EOF
	// on the client side. We tolerate read errors here so we can inspect
	// whichever failure mode manifests.
	body, readErr := io.ReadAll(resp.Body)

	// ---- Bug assertion ----
	// The bug manifests in one of two ways:
	//
	// 1. The client gets an "unexpected EOF" because Go's HTTP server detects
	//    that the handler wrote fewer bytes than the declared Content-Length
	//    and aborts the connection.
	//
	// 2. The Content-Length header (copied from the backend's unfiltered
	//    response) does not match the actual body length.
	//
	// Either condition proves the bug exists. A correct implementation would
	// let the client read the complete filtered body with a matching
	// Content-Length (or no Content-Length at all, letting chunked encoding
	// handle it).

	if readErr != nil {
		// Failure mode 1: connection was torn down due to Content-Length mismatch.
		// The client could not even read the full response.
		t.Fatalf("BUG: client received read error due to Content-Length mismatch: %v\n"+
			"The backend's Content-Length header leaked through ResponseFilteringWriter.\n"+
			"The filtered body is shorter than the declared Content-Length, so Go's HTTP\n"+
			"server aborted the connection.", readErr)
	}

	// If we got here, the body was readable. Check Content-Length consistency.
	clHeader := resp.Header.Get("Content-Length")
	if clHeader != "" {
		declaredLength, convErr := strconv.Atoi(clHeader)
		require.NoError(t, convErr, "Content-Length should be a valid integer")

		// Failure mode 2: Content-Length does not match actual body length.
		require.Equal(t, len(body), declaredLength,
			"BUG: Content-Length header (%d) does not match actual body length (%d).\n"+
				"The backend's unfiltered Content-Length leaked through ResponseFilteringWriter.\n"+
				"After filtering removed 2 of 3 tools, the body shrank but the header was not updated.",
			declaredLength, len(body))
	}

	// If we somehow got past both checks, verify the response is valid and
	// correctly filtered.
	message, err := jsonrpc2.DecodeMessage(body)
	require.NoError(t, err, "Response body should be valid JSON-RPC")

	rpcResp, ok := message.(*jsonrpc2.Response)
	require.True(t, ok, "Should be a JSON-RPC response")
	require.Nil(t, rpcResp.Error, "Response should not contain an error")

	var toolsResult mcp.ListToolsResult
	err = json.Unmarshal(rpcResp.Result, &toolsResult)
	require.NoError(t, err, "Should unmarshal tools list result")

	assert.Len(t, toolsResult.Tools, 1, "Only the permitted 'weather' tool should remain")
	if len(toolsResult.Tools) > 0 {
		assert.Equal(t, "weather", toolsResult.Tools[0].Name)
	}
}

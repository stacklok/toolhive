// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
)

// buildFindToolJSONRPCResponse creates a JSON-RPC tools/call response whose content
// text is a serialised find_tool output containing the given tools.
func buildFindToolJSONRPCResponse(t *testing.T, tools []mcp.Tool) []byte {
	t.Helper()
	output := optimizer.FindToolOutput{Tools: tools}
	outputJSON, err := json.Marshal(output)
	require.NoError(t, err)

	callResult := map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": string(outputJSON)},
		},
		"isError": false,
	}
	resultJSON, err := json.Marshal(callResult)
	require.NoError(t, err)

	resp := &jsonrpc2.Response{
		ID:     jsonrpc2.Int64ID(1),
		Result: json.RawMessage(resultJSON),
	}
	encoded, err := jsonrpc2.EncodeMessage(resp)
	require.NoError(t, err)
	return encoded
}

// decodeFindToolOutput decodes a JSON-RPC response produced by buildFindToolJSONRPCResponse
// and returns the optimizer.FindToolOutput embedded in the first text content item.
func decodeFindToolOutput(t *testing.T, body []byte) optimizer.FindToolOutput {
	t.Helper()
	msg, err := jsonrpc2.DecodeMessage(body)
	require.NoError(t, err)
	rpcResp, ok := msg.(*jsonrpc2.Response)
	require.True(t, ok)
	require.Nil(t, rpcResp.Error)

	var callResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(rpcResp.Result, &callResult))
	require.NotEmpty(t, callResult.Content)

	var output optimizer.FindToolOutput
	require.NoError(t, json.Unmarshal([]byte(callResult.Content[0].Text), &output))
	return output
}

// TestFindToolResponseFilter verifies that find_tool results are filtered by Cedar
// policy before being returned to the caller.
func TestFindToolResponseFilter(t *testing.T) {
	t.Parallel()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "user1",
		Claims:  map[string]interface{}{"sub": "user1"},
	}}
	newReq := func(t *testing.T) *http.Request {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, "/messages", nil)
		require.NoError(t, err)
		return req.WithContext(auth.WithIdentity(req.Context(), identity))
	}
	newWriter := func(t *testing.T, cache *AnnotationCache) (*httptest.ResponseRecorder, *ResponseFilteringWriter) {
		t.Helper()
		rr := httptest.NewRecorder()
		rr.Header().Set("Content-Type", "application/json")
		fw := NewResponseFilteringWriter(rr, authorizer, newReq(t), optimizerdec.FindToolName, cache, nil)
		fw.ResponseWriter.Header().Set("Content-Type", "application/json")
		return rr, fw
	}

	t.Run("Cedar policy filters unauthorized tools", func(t *testing.T) {
		t.Parallel()

		// The optimizer returns two tools but the caller is only permitted "weather".
		responseBytes := buildFindToolJSONRPCResponse(t, []mcp.Tool{
			{Name: "weather", Description: "Get weather"},
			{Name: "admin_tool", Description: "Admin operations"},
		})

		rr, fw := newWriter(t, nil)
		_, err := fw.Write(responseBytes)
		require.NoError(t, err)
		require.NoError(t, fw.FlushAndFilter())

		output := decodeFindToolOutput(t, rr.Body.Bytes())
		require.Len(t, output.Tools, 1, "only the permitted tool should remain")
		assert.Equal(t, "weather", output.Tools[0].Name)
	})

	t.Run("isError response passes through unfiltered", func(t *testing.T) {
		t.Parallel()

		// Build a CallToolResult with IsError set — the filter must not touch it.
		errorResult := map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "tool execution failed"},
			},
			"isError": true,
		}
		resultJSON, err := json.Marshal(errorResult)
		require.NoError(t, err)
		resp := &jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: json.RawMessage(resultJSON)}
		responseBytes, err := jsonrpc2.EncodeMessage(resp)
		require.NoError(t, err)

		rr, fw := newWriter(t, nil)
		_, err = fw.Write(responseBytes)
		require.NoError(t, err)
		require.NoError(t, fw.FlushAndFilter())

		assert.Equal(t, responseBytes, rr.Body.Bytes(), "error response must pass through unchanged")
	})

	t.Run("response with no text content passes through unfiltered", func(t *testing.T) {
		t.Parallel()

		// A CallToolResult with no content items at all.
		emptyResult := map[string]interface{}{"content": []interface{}{}, "isError": false}
		resultJSON, err := json.Marshal(emptyResult)
		require.NoError(t, err)
		resp := &jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: json.RawMessage(resultJSON)}
		responseBytes, err := jsonrpc2.EncodeMessage(resp)
		require.NoError(t, err)

		rr, fw := newWriter(t, nil)
		_, err = fw.Write(responseBytes)
		require.NoError(t, err)
		require.NoError(t, fw.FlushAndFilter())

		assert.Equal(t, responseBytes, rr.Body.Bytes(), "response with no content must pass through unchanged")
	})

	t.Run("text content that is not a FindToolOutput passes through unfiltered", func(t *testing.T) {
		t.Parallel()

		// A plain text content item that is not a valid FindToolOutput JSON.
		plainText := map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "this is a plain string, not a find_tool result"},
			},
			"isError": false,
		}
		resultJSON, err := json.Marshal(plainText)
		require.NoError(t, err)
		resp := &jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: json.RawMessage(resultJSON)}
		responseBytes, err := jsonrpc2.EncodeMessage(resp)
		require.NoError(t, err)

		rr, fw := newWriter(t, nil)
		_, err = fw.Write(responseBytes)
		require.NoError(t, err)
		require.NoError(t, fw.FlushAndFilter())

		assert.Equal(t, responseBytes, rr.Body.Bytes(), "non-FindToolOutput text content must pass through unchanged")
	})

	t.Run("annotation cache is populated from unfiltered tool list", func(t *testing.T) {
		t.Parallel()

		readOnly := true
		responseBytes := buildFindToolJSONRPCResponse(t, []mcp.Tool{
			{
				Name:        "weather",
				Description: "Get weather",
				Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly},
			},
			// admin_tool is not permitted by Cedar, but its annotations must still
			// be cached so that a subsequent call_tool request can evaluate Cedar
			// when-clauses against them.
			{
				Name:        "admin_tool",
				Description: "Admin operations",
				Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly},
			},
		})

		cache := NewAnnotationCache()
		_, fw := newWriter(t, cache)
		_, err := fw.Write(responseBytes)
		require.NoError(t, err)
		require.NoError(t, fw.FlushAndFilter())

		// Both tools must be in the cache even though admin_tool is filtered from the response.
		assert.NotNil(t, cache.Get("weather"), "permitted tool annotation must be cached")
		assert.NotNil(t, cache.Get("admin_tool"), "denied tool annotation must still be cached for future call_tool Cedar evaluation")
	})
}

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
	}, "")
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
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: sub, Name: name, Claims: tc.claims}}
			req = req.WithContext(auth.WithIdentity(req.Context(), identity))

			// Create a response recorder
			rr := httptest.NewRecorder()

			// Create the response filtering writer
			filteringWriter := NewResponseFilteringWriter(rr, authorizer, req, tc.method, nil, nil)
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
	}, "")
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
	filteringWriter := NewResponseFilteringWriter(rr, authorizer, req, "tools/call", nil, nil)

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
	}, "")
	require.NoError(t, err, "Failed to create Cedar authorizer")

	// Create an error response in wire format (json.Marshal on the struct would
	// produce Go field names, not a JSON-RPC frame).
	jsonrpcResponse := &jsonrpc2.Response{
		ID:    jsonrpc2.Int64ID(1),
		Error: jsonrpc2.NewError(404, "Not found"),
	}

	responseBytes, err := jsonrpc2.EncodeMessage(jsonrpcResponse)
	require.NoError(t, err, "Failed to marshal JSON-RPC response")

	// Create an HTTP request
	req, err := http.NewRequest(http.MethodPost, "/messages", nil)
	require.NoError(t, err, "Failed to create HTTP request")

	// Create a response recorder
	rr := httptest.NewRecorder()

	// Create the response filtering writer
	filteringWriter := NewResponseFilteringWriter(rr, authorizer, req, "tools/list", nil, nil)
	filteringWriter.ResponseWriter.Header().Set("Content-Type", "application/json")

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
	}, "")
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
		identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
			Subject: "user123",
			Name:    "Test User",
			Claims: jwt.MapClaims{
				"sub":  "user123",
				"name": "Test User",
			},
		}}
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
		filteringWriter := NewResponseFilteringWriter(w, authorizer, r, string(mcp.MethodToolsList), nil, nil)

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

// TestOptimizerPassThroughToolsInResponseFilter verifies the scenario where an
// operator enables the optimizer alongside Cedar authorization policies.
//
// Scenario:
//   - The optimizer replaces real backend tools with two meta-tools: find_tool
//     and call_tool. These appear in tools/list instead of real tool names.
//   - The operator's Cedar policies only reference real backend tool names
//     (e.g., Tool::"weather"), not the optimizer meta-tool names.
//   - Without pass-through, Cedar default-deny filters out find_tool and
//     call_tool from tools/list because no policy permits them, leaving the
//     client with zero tools.
//   - With pass-through, the meta-tools appear in tools/list regardless of
//     Cedar policies. Cedar enforcement for the underlying backend tools is
//     handled inside the optimizer decorator (find_tool filters results,
//     call_tool gates invocations).
//
// See: https://github.com/stacklok/toolhive/issues/4373
func TestOptimizerPassThroughToolsInResponseFilter(t *testing.T) {
	t.Parallel()

	// Cedar policy: only "weather" is permitted. No policy mentions find_tool or call_tool.
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		},
		EntitiesJSON: "[]",
	}, "")
	require.NoError(t, err)

	// Build a tools/list response as the optimizer would produce it:
	// only find_tool and call_tool, no real backend tools.
	toolsList := mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "find_tool", Description: "Find a tool by description"},
			{Name: "call_tool", Description: "Call a backend tool by name"},
		},
	}
	result, err := json.Marshal(toolsList)
	require.NoError(t, err)

	response := &jsonrpc2.Response{
		ID:     jsonrpc2.Int64ID(1),
		Result: json.RawMessage(result),
	}
	responseBytes, err := jsonrpc2.EncodeMessage(response)
	require.NoError(t, err)

	// Identity needed for Cedar evaluation.
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "user1",
		Claims:  map[string]interface{}{"sub": "user1"},
	}}

	req, err := http.NewRequest(http.MethodPost, "/messages", nil)
	require.NoError(t, err)
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	// Optimizer meta-tools that should pass through without policy checks.
	passThroughTools := map[string]struct{}{
		"find_tool": {},
		"call_tool": {},
	}

	// decodeToolsListResponse is a helper that decodes a JSON-RPC response from
	// the recorder and returns the tools list.
	decodeToolsListResponse := func(t *testing.T, rr *httptest.ResponseRecorder) []mcp.Tool {
		t.Helper()
		msg, err := jsonrpc2.DecodeMessage(rr.Body.Bytes())
		require.NoError(t, err)
		rpcResp, ok := msg.(*jsonrpc2.Response)
		require.True(t, ok)
		require.Nil(t, rpcResp.Error)
		var result mcp.ListToolsResult
		require.NoError(t, json.Unmarshal(rpcResp.Result, &result))
		return result.Tools
	}

	t.Run("with pass-through both meta-tools appear in tools/list", func(t *testing.T) {
		t.Parallel()

		rr := httptest.NewRecorder()
		fw := NewResponseFilteringWriter(rr, authorizer, req, "tools/list", nil, passThroughTools)
		fw.ResponseWriter.Header().Set("Content-Type", "application/json")

		_, err := fw.Write(responseBytes)
		require.NoError(t, err)
		require.NoError(t, fw.FlushAndFilter())

		tools := decodeToolsListResponse(t, rr)

		// Both meta-tools should survive despite no Cedar policy permitting them.
		require.Len(t, tools, 2, "both optimizer meta-tools must pass through")
		names := []string{tools[0].Name, tools[1].Name}
		assert.Contains(t, names, "find_tool")
		assert.Contains(t, names, "call_tool")
	})

	t.Run("without pass-through both meta-tools are filtered out", func(t *testing.T) {
		t.Parallel()

		rr := httptest.NewRecorder()
		// nil passThroughTools = no pass-through, standard Cedar filtering.
		fw := NewResponseFilteringWriter(rr, authorizer, req, "tools/list", nil, nil)
		fw.ResponseWriter.Header().Set("Content-Type", "application/json")

		_, err := fw.Write(responseBytes)
		require.NoError(t, err)
		require.NoError(t, fw.FlushAndFilter())

		tools := decodeToolsListResponse(t, rr)

		// Without pass-through, Cedar default-deny removes both meta-tools.
		assert.Empty(t, tools,
			"without pass-through, meta-tools should be filtered out by Cedar default-deny")
	})

	t.Run("pass-through only affects listed meta-tools not real tools", func(t *testing.T) {
		t.Parallel()

		// Mix of optimizer meta-tools and real backend tools in tools/list.
		// In practice this shouldn't happen (optimizer replaces all real tools),
		// but this validates that pass-through is selective.
		mixedToolsList := mcp.ListToolsResult{
			Tools: []mcp.Tool{
				{Name: "find_tool", Description: "Find a tool"},
				{Name: "call_tool", Description: "Call a tool"},
				{Name: "weather", Description: "Get weather"},        // permitted by policy
				{Name: "admin_tool", Description: "Admin only tool"}, // NOT permitted
			},
		}
		mixedResult, err := json.Marshal(mixedToolsList)
		require.NoError(t, err)
		mixedResponse := &jsonrpc2.Response{
			ID:     jsonrpc2.Int64ID(2),
			Result: json.RawMessage(mixedResult),
		}
		mixedResponseBytes, err := jsonrpc2.EncodeMessage(mixedResponse)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		fw := NewResponseFilteringWriter(rr, authorizer, req, "tools/list", nil, passThroughTools)
		fw.ResponseWriter.Header().Set("Content-Type", "application/json")

		_, err = fw.Write(mixedResponseBytes)
		require.NoError(t, err)
		require.NoError(t, fw.FlushAndFilter())

		tools := decodeToolsListResponse(t, rr)

		// find_tool + call_tool pass through, weather is permitted, admin_tool is denied.
		require.Len(t, tools, 3)
		names := make([]string, len(tools))
		for i, tool := range tools {
			names[i] = tool.Name
		}
		assert.Contains(t, names, "find_tool")
		assert.Contains(t, names, "call_tool")
		assert.Contains(t, names, "weather")
		assert.NotContains(t, names, "admin_tool",
			"admin_tool has no permit policy and is not a pass-through tool")
	})
}

// TestCarriesResult pins carriesResult's (and, through it, valueCarriesResult's)
// classification of the shapes the SSE and JSON filters route through it: no
// clean single Response, but the caller needs to know whether a "result" key
// is hiding somewhere in the payload regardless.
func TestCarriesResult(t *testing.T) {
	t.Parallel()

	const notification = `{"jsonrpc":"2.0","method":"notifications/message","params":{}}`
	const result = `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`

	testCases := []struct {
		name string
		data string
		want bool
	}{
		{
			name: "concatenated notification then result",
			// The exact shape an SSE event's assembled data takes when two
			// data: lines (a notification, then a result-bearing frame)
			// concatenate into one payload: DecodeMessage rejects the whole
			// thing as a single value, but the embedded result must still be
			// caught.
			data: notification + "\n" + result,
			want: true,
		},
		{
			name: "heterogeneous batch array",
			// A batch whose first element isn't an object: Unmarshal-ing the
			// whole array into a single probe struct would fail outright,
			// missing the result-bearing element further down.
			data: `[1,` + result + `]`,
			want: true,
		},
		{
			name: "undecodable prefix before a result stops at the first value",
			// Deliberate: json.Decoder stops at the first value it can't
			// decode ("garbage") and never reaches the result-bearing value
			// after it. This is safe rather than a missed catch, because no
			// strict client can read past that point either. Don't "fix"
			// this into scanning past bad input looking for a result.
			data: "garbage\n" + result,
			want: false,
		},
		{
			name: "plain notification carries no result",
			data: notification,
			want: false,
		},
		{
			name: "empty input",
			data: "",
			want: false,
		},
		{
			name: "nested array is not recursed into",
			// Deliberate: a JSON-RPC batch is a flat array of message
			// objects, so that's the only legitimate batch surface. Recursing
			// into nested arrays is an O(d^2) amplification handle on
			// encoding/json's own nesting cap for something no client
			// actually flattens. Don't "fix" this into recursive descent.
			data: `[[` + result + `]]`,
			want: false,
		},
		{
			name: "explicit null result still counts as carrying one",
			// Deliberate, the fail-closed direction: RawMessage's
			// UnmarshalJSON runs even for a JSON null, so a "result" key
			// present with a null value is still treated as carrying a
			// result. Don't "fix" this into skipping nulls.
			data: `{"jsonrpc":"2.0","id":1,"result":null}`,
			want: true,
		},
		{
			name: "top-level scalar carries no result",
			data: `"just a string"`,
			want: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, carriesResult([]byte(tc.data)))
		})
	}
}

// newWeatherOnlyAuthorizer builds the Cedar authorizer most SSE regression
// tests below share: a single policy permitting call_tool on "weather" plus
// any extraPolicies the caller needs for other resource types.
func newWeatherOnlyAuthorizer(t *testing.T, extraPolicies ...string) authorizers.Authorizer {
	t.Helper()
	policies := append([]string{
		`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
	}, extraPolicies...)
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     policies,
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)
	return authorizer
}

// newUser1Request builds a POST /messages request carrying the "user1"
// identity most SSE regression tests below share.
func newUser1Request(t *testing.T) *http.Request {
	t.Helper()
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "user1",
		Claims:  map[string]interface{}{"sub": "user1"},
	}}
	req, err := http.NewRequest(http.MethodPost, "/messages", nil)
	require.NoError(t, err)
	return req.WithContext(auth.WithIdentity(req.Context(), identity))
}

// newParsedUser1Request is newUser1Request plus mcpparser.ParsingMiddleware,
// for tests whose error envelope must correlate against the request's real
// JSON-RPC id: requestID() reads the parsed request back out of context, so
// a plain request (no parsed MCP request in context) would always produce a
// zero, id-less envelope regardless of what the fix is supposed to do.
func newParsedUser1Request(t *testing.T, reqBody string) *http.Request {
	t.Helper()
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "user1",
		Claims:  map[string]interface{}{"sub": "user1"},
	}}
	req, err := http.NewRequest(http.MethodPost, "/messages", strings.NewReader(reqBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	var parsedReq *http.Request
	mcpparser.ParsingMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		parsedReq = r
	})).ServeHTTP(httptest.NewRecorder(), req)
	require.NotNil(t, parsedReq)
	// ParsingMiddleware calls next even when parsing fails, so a nil parsedReq
	// alone wouldn't catch a typo'd reqBody that fails to parse — that would
	// silently degrade every caller to the zero-ID path. A notification (no
	// "id" in reqBody) still parses to a non-nil ParsedMCPRequest with a nil
	// ID, so this assertion holds for every caller, not just id-bearing ones.
	require.NotNil(t, mcpparser.GetParsedMCPRequest(parsedReq.Context()),
		"reqBody must have parsed into a ParsedMCPRequest, or requestID() silently falls back to the zero ID")
	return parsedReq
}

// TestResponseFilteringWriter_SSE_PerEventFallthrough is a regression test for
// issue #5257: when an SSE upstream interleaves a non-Response event (e.g. an
// MCP notification) or an undecodable event with a real list response, the
// filter previously wrote the entire raw upstream payload and returned,
// leaking the unfiltered list past Cedar. It must instead pass only the
// offending event through and continue filtering the rest of the stream.
//
// The same code path runs for every method covered by
// requiresResponseFiltering, so each of tools/list, prompts/list, and
// resources/list is exercised below.
func TestResponseFilteringWriter_SSE_PerEventFallthrough(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t,
		`permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`,
		`permit(principal, action == Action::"read_resource", resource == Resource::"data");`,
	)

	// encodeListResponse marshals a list result type into a JSON-RPC Response
	// data line.
	encodeListResponse := func(t *testing.T, result interface{}) string {
		t.Helper()
		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		encoded, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{
			ID:     jsonrpc2.Int64ID(1),
			Result: json.RawMessage(resultJSON),
		})
		require.NoError(t, err)
		return "data: " + string(encoded)
	}

	// methodCase describes how to build a filterable response for one MCP
	// list method and how to read the filtered names out of the wire output.
	type methodCase struct {
		name             string
		method           string
		respLine         string
		authorizedName   string
		unauthorizedName string
		extractNames     func(t *testing.T, result json.RawMessage) []string
	}

	methodCases := []methodCase{
		{
			name:   "tools/list",
			method: string(mcp.MethodToolsList),
			respLine: encodeListResponse(t, mcp.ListToolsResult{
				Tools: []mcp.Tool{
					{Name: "weather", Description: "Get weather information"},
					{Name: "admin_tool", Description: "Sensitive admin operations"},
				},
			}),
			authorizedName:   "weather",
			unauthorizedName: "admin_tool",
			extractNames: func(t *testing.T, result json.RawMessage) []string {
				t.Helper()
				var r mcp.ListToolsResult
				require.NoError(t, json.Unmarshal(result, &r))
				names := make([]string, len(r.Tools))
				for i, tool := range r.Tools {
					names[i] = tool.Name
				}
				return names
			},
		},
		{
			name:   "prompts/list",
			method: string(mcp.MethodPromptsList),
			respLine: encodeListResponse(t, mcp.ListPromptsResult{
				Prompts: []mcp.Prompt{
					{Name: "greeting", Description: "Generate greetings"},
					{Name: "admin_prompt", Description: "Sensitive admin prompt"},
				},
			}),
			authorizedName:   "greeting",
			unauthorizedName: "admin_prompt",
			extractNames: func(t *testing.T, result json.RawMessage) []string {
				t.Helper()
				var r mcp.ListPromptsResult
				require.NoError(t, json.Unmarshal(result, &r))
				names := make([]string, len(r.Prompts))
				for i, p := range r.Prompts {
					names[i] = p.Name
				}
				return names
			},
		},
		{
			name:   "resources/list",
			method: string(mcp.MethodResourcesList),
			respLine: encodeListResponse(t, mcp.ListResourcesResult{
				Resources: []mcp.Resource{
					{URI: "data", Name: "Data Resource"},
					{URI: "secret", Name: "Sensitive Resource"},
				},
			}),
			authorizedName:   "data",
			unauthorizedName: "secret",
			extractNames: func(t *testing.T, result json.RawMessage) []string {
				t.Helper()
				var r mcp.ListResourcesResult
				require.NoError(t, json.Unmarshal(result, &r))
				names := make([]string, len(r.Resources))
				for i, res := range r.Resources {
					names[i] = res.URI
				}
				return names
			},
		},
	}

	precedingLineCases := []struct {
		name string
		line string
	}{
		{
			name: "non-response data line",
			// A notifications/* frame is a valid JSON-RPC notification
			// (no id), so jsonrpc2.DecodeMessage returns a non-Response
			// message. The buggy path treated this as a signal to dump
			// rawResponse and return.
			line: `data: {"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"warming up"}}`,
		},
		{
			name: "undecodable data line",
			line: `data: this is not json at all`,
		},
	}

	for _, mc := range methodCases {
		for _, plc := range precedingLineCases {
			mc, plc := mc, plc
			t.Run(mc.name+"/"+plc.name, func(t *testing.T) {
				t.Parallel()

				req := newUser1Request(t)

				rr := httptest.NewRecorder()
				rfw := NewResponseFilteringWriter(rr, authorizer, req, mc.method, nil, nil)
				rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

				// A blank line separates plc.line and mc.respLine into two
				// distinct SSE events, matching how a real MCP server frames
				// SSE (one JSON-RPC message per event): otherwise, with no
				// blank line between them, they'd be two data: fields of the
				// SAME event and their values would concatenate, which is not
				// what this test means to model. The trailing "" pair
				// terminates the second event.
				body := strings.Join([]string{plc.line, "", mc.respLine, "", ""}, "\n")
				_, err := rfw.Write([]byte(body))
				require.NoError(t, err)

				require.NoError(t, rfw.FlushAndFilter())

				out := rr.Body.String()

				// The preceding line must still appear verbatim; pass-through
				// is the whole point of the fix.
				assert.Contains(t, out, plc.line,
					"non-response/undecodable preceding line must pass through unchanged")

				// The real list response must have been filtered. Pull the
				// last JSON-RPC Response data line out and decode it.
				var filteredLine string
				for _, line := range strings.Split(out, "\n") {
					if strings.HasPrefix(line, "data: {\"jsonrpc\"") && strings.Contains(line, `"result"`) {
						filteredLine = line
					}
				}
				require.NotEmpty(t, filteredLine, "no JSON-RPC Response data line found in output")

				payload := strings.TrimPrefix(filteredLine, "data: ")
				msg, err := jsonrpc2.DecodeMessage([]byte(payload))
				require.NoError(t, err)
				resp, ok := msg.(*jsonrpc2.Response)
				require.True(t, ok)

				names := mc.extractNames(t, resp.Result)
				assert.Contains(t, names, mc.authorizedName, "authorized entry must be retained")
				assert.NotContains(t, names, mc.unauthorizedName,
					"unauthorized entry must be filtered; presence indicates the cedar bypass from #5257 is back")

				// And the raw unfiltered payload (the bug used to dump it)
				// must not appear in the wire output.
				assert.NotContains(t, out, `"`+mc.unauthorizedName+`"`,
					"unfiltered list payload leaked into SSE output")

				// The blank-line event terminator must survive the rewrite:
				// processSSEResponse's structural invariant is that every
				// line the input had, including blank lines, is written
				// exactly once in its original position.
				assert.True(t, strings.HasSuffix(out, "\n\n"),
					"output must end with a blank-line SSE terminator")
			})
		}
	}
}

// TestResponseFilteringWriter_SSE_DisguisedResponseFrame is a regression test
// for a residual of issue #5257: a frame that carries both a method field (so
// jsonrpc2.DecodeMessage classifies it as a request/notification rather than a
// Response) and a result field smuggling a real list. Such a frame must fail
// closed with an error envelope in its place — not pass the smuggled list
// through, and not silently drop the event's data (which per WHATWG SSE
// semantics leaves the client waiting on a dispatch that never carries a
// usable payload; see #6037).
//
// The envelope must also be one the client can actually correlate: it's
// decoded and checked for a non-nil Error carrying the *request's* id, not
// just a substring match, because an id-less envelope (the zero jsonrpc2.ID,
// which EncodeMessage omits from the wire) reproduces the #6037 hang even
// though an "error" key is present on the wire.
func TestResponseFilteringWriter_SSE_DisguisedResponseFrame(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)

	// A genuine, filterable tools/list response on a later line.
	realResultJSON, err := json.Marshal(mcp.ListToolsResult{
		Tools: []mcp.Tool{{Name: "weather", Description: "Get weather information"}},
	})
	require.NoError(t, err)
	realResp, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{
		ID:     jsonrpc2.Int64ID(2),
		Result: json.RawMessage(realResultJSON),
	})
	require.NoError(t, err)

	// Each frame smuggles a tools/list result outside a clean Response, via a
	// different upstream-controlled shape. DecodeMessage either classifies them
	// as non-Response or rejects them outright, so the pre-fix code passed them
	// through unfiltered. All must fail closed into an error envelope instead.
	frames := []struct {
		name    string
		payload string
	}{
		{"method+result non-response", `data: {"jsonrpc":"2.0","method":"notifications/initialized","id":1,"result":{"tools":[{"name":"admin_tool"}]}}`},
		{"missing jsonrpc tag", `data: {"id":1,"result":{"tools":[{"name":"admin_tool"}]}}`},
		{"no id", `data: {"jsonrpc":"2.0","result":{"tools":[{"name":"admin_tool"}]}}`},
		{"non-scalar id", `data: {"jsonrpc":"2.0","id":{"x":1},"result":{"tools":[{"name":"admin_tool"}]}}`},
		{"batch array", `data: [{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"admin_tool"}]}}]`},
		// A batch array whose first element isn't an object: Unmarshal-ing
		// the whole array into []struct{Result} would previously fail
		// outright, missing the result-bearing element further down.
		{"heterogeneous batch array", `data: [1,{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"admin_tool"}]}}]`},
	}

	for _, f := range frames {
		f := f
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()

			// Run the real MCP parsing middleware so the request context
			// carries a ParsedMCPRequest with a real id, matching production:
			// requestID() reads it back out to correlate the error envelope.
			parsedReq := newParsedUser1Request(t, `{"jsonrpc":"2.0","id":99,"method":"tools/list"}`)

			rr := httptest.NewRecorder()
			rfw := NewResponseFilteringWriter(rr, authorizer, parsedReq, string(mcp.MethodToolsList), nil, nil)
			rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

			// A blank line separates the disguised frame from the genuine
			// response into two distinct SSE events, matching how a real MCP
			// server frames SSE (one JSON-RPC message per event).
			body := strings.Join([]string{f.payload, "", "data: " + string(realResp), "", ""}, "\n")
			_, err := rfw.Write([]byte(body))
			require.NoError(t, err)
			require.NoError(t, rfw.FlushAndFilter())

			out := rr.Body.String()
			assert.NotContains(t, out, "admin_tool",
				"smuggled result leaked past the filter")
			assert.Contains(t, out, "weather",
				"genuine list response in a later event must still be delivered")

			// The disguised frame's event is first on the wire; decode it and
			// require an error envelope the client can correlate. TrimSuffix
			// guards a CRLF body leaving a trailing \r on the line.
			firstLine := strings.TrimSuffix(strings.SplitN(out, "\n", 2)[0], "\r")
			payload := strings.TrimPrefix(firstLine, "data: ")
			msg, err := jsonrpc2.DecodeMessage([]byte(payload))
			require.NoError(t, err, "the fail-closed envelope must itself be valid JSON-RPC")
			resp, ok := msg.(*jsonrpc2.Response)
			require.True(t, ok, "the fail-closed envelope must be a clean Response")
			require.NotNil(t, resp.Error, "the disguised frame's event must fail closed into an error envelope, not vanish silently")
			assert.Equal(t, jsonrpc2.Int64ID(99), resp.ID,
				"the error envelope must carry the request's id so the client can correlate it, or the #6037 hang reproduces")
		})
	}
}

// TestResponseFilteringWriter_SSE_FailClosedDropsEventName is a regression
// test for #6037: the go-sdk streamable client only dispatches unnamed
// ("message"-typed) events to its MCP request handler, so substituting a
// fail-closed error envelope in place of a NAMED event's data (while leaving
// its "event:" field untouched) meant the envelope was never delivered and
// the caller hung on its own timeout -- the very hang the #6037 envelope was
// meant to prevent. This asserts what parseSSEStream (the go-sdk client
// model, not the WHATWG grammar) actually dispatches, since that's what
// determines delivery.
func TestResponseFilteringWriter_SSE_FailClosedDropsEventName(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)
	parsedReq := newParsedUser1Request(t, `{"jsonrpc":"2.0","id":99,"method":"tools/list"}`)

	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, parsedReq, string(mcp.MethodToolsList), nil, nil)
	rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	// A method+result frame that fails closed (see
	// TestResponseFilteringWriter_SSE_DisguisedResponseFrame), framed under a
	// custom event name the way a real backend might tag a tool-list push.
	body := "event: toolsUpdate\n" +
		`data: {"jsonrpc":"2.0","method":"notifications/initialized","id":1,` +
		`"result":{"tools":[{"name":"admin_tool"}]}}` + "\n\n"
	_, err := rfw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, rfw.FlushAndFilter())

	out := rr.Body.String()
	assert.NotContains(t, out, "admin_tool", "smuggled result leaked past the filter")
	assert.NotContains(t, out, "event:",
		"the offending event's event: field must be dropped, or the go-sdk client never dispatches the fail-closed envelope")

	events := parseSSEStream(t, rr.Body.Bytes())
	require.Len(t, events, 1, "body: %q", out)
	assert.Empty(t, events[0].eventType,
		"the go-sdk client only dispatches unnamed/'message' events to its MCP handler; a named event here reproduces the #6037 hang")

	msg, err := jsonrpc2.DecodeMessage([]byte(events[0].data))
	require.NoError(t, err, "the dispatched envelope must itself be valid JSON-RPC")
	resp, ok := msg.(*jsonrpc2.Response)
	require.True(t, ok, "the dispatched envelope must be a clean Response")
	require.NotNil(t, resp.Error, "the event must fail closed into an error envelope")
	assert.Equal(t, jsonrpc2.Int64ID(99), resp.ID,
		"the error envelope must carry the request's id so the client can correlate it")
}

// TestResponseFilteringWriter_SSE_ConcatenatedEventBypass is a regression test
// for a #5257-class leak the event-based rewrite itself introduced: two
// data: fields in ONE event (a notification followed by a result-bearing
// frame) assemble, per WHATWG semantics, into "{notif}\n{result}". That
// payload fails jsonrpc2.DecodeMessage (trailing data after the first JSON
// value), and carriesResult must still catch the embedded result — Unmarshal
// on the whole concatenated payload rejects it the same way DecodeMessage
// does, which would otherwise route it to the "pass through unfiltered"
// branch and leak the unauthorized tool straight through.
func TestResponseFilteringWriter_SSE_ConcatenatedEventBypass(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)

	notificationLine := `data: {"jsonrpc":"2.0","method":"notifications/message","params":{}}`
	resultLine := `data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"admin_tool"}]}}`

	// Run the real MCP parsing middleware so the request context carries a
	// ParsedMCPRequest with a real id, matching production: requestID() reads
	// it back out to correlate the error envelope. Without this, requestID()
	// falls back to the zero ID, the envelope carries no "id", and this test
	// would accept an envelope no client could actually correlate.
	req := newParsedUser1Request(t, `{"jsonrpc":"2.0","id":99,"method":"tools/list"}`)

	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
	rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	// No blank line between the two data: lines: one event, two fields.
	body := strings.Join([]string{notificationLine, resultLine, "", ""}, "\n")
	_, err := rfw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, rfw.FlushAndFilter())

	out := rr.Body.String()
	assert.NotContains(t, out, "admin_tool",
		"a result concatenated after a notification within one event must not bypass the filter")

	// NotContains alone would also pass if the event were dropped silently
	// (#6037's hang); require the fail-closed envelope actually went out and
	// carries the request's id so the client can correlate it.
	firstLine := strings.TrimSuffix(strings.SplitN(out, "\n", 2)[0], "\r")
	payload := strings.TrimPrefix(firstLine, "data: ")
	msg, err := jsonrpc2.DecodeMessage([]byte(payload))
	require.NoError(t, err, "the fail-closed envelope must itself be valid JSON-RPC")
	resp, ok := msg.(*jsonrpc2.Response)
	require.True(t, ok, "the fail-closed envelope must be a clean Response")
	require.NotNil(t, resp.Error, "the concatenated event must fail closed into an error envelope, not vanish silently")
	assert.Equal(t, jsonrpc2.Int64ID(99), resp.ID,
		"the error envelope must carry the request's id so the client can correlate it, or the #6037 hang reproduces")
}

// TestResponseFilteringWriter_SSE_MixedSeparatorsBypass is a regression test
// for a #5257-class leak: SSE permits CR, LF, and CRLF mixed within one
// stream, but sniffing a single stream-wide separator (the pre-fix approach)
// disagrees with how a spec-compliant client actually splits the body into
// lines and events. Here the body sniffs as CRLF, so a conformant client's
// LF-terminated lines collapse into one giant "line" for the sniffing filter,
// whose data: prefix strip leaves an undecodable payload that falls through
// unfiltered — while the client itself correctly sees three separate events,
// the third a clean Response carrying the unauthorized tool.
func TestResponseFilteringWriter_SSE_MixedSeparatorsBypass(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)
	req := newUser1Request(t)

	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
	rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	resultJSON, err := json.Marshal(mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "admin_tool", Description: "Sensitive admin operations"},
		},
	})
	require.NoError(t, err)
	resultLine := "data: " + `{"jsonrpc":"2.0","id":1,"result":` + string(resultJSON) + "}"

	body := "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\r\n" +
		"\r\n" +
		"data: x\n" +
		"\n" +
		resultLine + "\n" +
		"\n"
	_, err = rfw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, rfw.FlushAndFilter())

	out := rr.Body.String()
	assert.NotContains(t, out, "admin_tool",
		"mixed line-terminator conventions within one SSE body must not bypass the filter")
	// NotContains alone would also pass if the event were dropped rather than
	// filtered (#6037's hang); prove it was actually filtered, not eaten.
	assert.Contains(t, out, "weather", "the authorized tool must survive filtering")
}

// TestResponseFilteringWriter_SSE_LeadingBOMBypass is a regression test for a
// #5257-class leak: a client strips a leading UTF-8 BOM per the WHATWG decode
// algorithm before parsing lines, but the filter compared the raw first line
// against "data:". The BOM byte prefix made that comparison fail, so the
// event (and the unauthorized tool inside it) passed through unfiltered.
func TestResponseFilteringWriter_SSE_LeadingBOMBypass(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)
	req := newUser1Request(t)

	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
	rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	resultJSON, err := json.Marshal(mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "admin_tool", Description: "Sensitive admin operations"},
		},
	})
	require.NoError(t, err)
	body := "\xEF\xBB\xBF" + `data: {"jsonrpc":"2.0","id":1,"result":` + string(resultJSON) + "}\n\n"

	_, err = rfw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, rfw.FlushAndFilter())

	out := rr.Body.String()
	assert.NotContains(t, out, "admin_tool",
		"a leading UTF-8 BOM must not bypass the filter")
	assert.Contains(t, out, "weather", "the authorized tool must survive filtering")
	assert.False(t, strings.HasPrefix(out, "\xEF\xBB\xBF"),
		"the leading BOM must be dropped from the output, not stripped-for-matching then re-emitted")
}

// TestResponseFilteringWriter_JSON_LeadingBOMBypass is a regression test for a
// #5257-class leak: a client strips a leading UTF-8 BOM per the WHATWG decode
// algorithm before parsing JSON, but the filter previously decoded the raw
// body. The BOM made both the decoder and result probe reject the response, so
// the unauthorized tool passed through unfiltered.
func TestResponseFilteringWriter_JSON_LeadingBOMBypass(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)
	req := newUser1Request(t)
	resultJSON, err := json.Marshal(mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "admin_tool", Description: "Sensitive admin operations"},
		},
	})
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
	rfw.ResponseWriter.Header().Set("Content-Type", "application/json")
	body := "\xEF\xBB\xBF" + `{"jsonrpc":"2.0","id":1,"result":` + string(resultJSON) + "}"

	_, err = rfw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, rfw.FlushAndFilter())

	out := rr.Body.String()
	assert.NotContains(t, out, "admin_tool",
		"a leading UTF-8 BOM must not bypass the filter")
	assert.Contains(t, out, "weather", "the authorized tool must survive filtering")
	assert.False(t, strings.HasPrefix(out, "\xEF\xBB\xBF"),
		"the leading BOM must be dropped from the output, not stripped-for-matching then re-emitted")
}

// TestResponseFilteringWriter_SSE_ErrorAndResultBypass is a regression test
// for a #5257-class leak: jsonrpc2.DecodeMessage and EncodeMessage both
// populate/re-emit "error" and "result" together on one Response, and
// filterListResponse's `response.Error != nil` check returned the whole
// response, list intact, as soon as an error field was present. The reference
// TS SDK's zod schema silently strips the unknown "error" key rather than
// rejecting the message, so it would accept this as a successful response
// carrying the full unfiltered list.
//
// The error-only row distinguishes "failed closed" from "passed through
// unfiltered": both leave no admin_tool on the wire, so a substring check
// alone can't tell a fix from a regression that fails closed on every
// legitimate upstream error. Only the error *code* does — mcpparser.CodeInternalError
// is our envelope, anything else is the upstream's own error surviving untouched.
func TestResponseFilteringWriter_SSE_ErrorAndResultBypass(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)

	testCases := []struct {
		name          string
		body          string
		wantErrorCode float64
	}{
		{
			name: "error and result together fails closed",
			body: `data: {"jsonrpc":"2.0","id":1,"error":{"code":1,"message":"x"},` +
				`"result":{"tools":[{"name":"admin_tool"}]}}` + "\n\n",
			wantErrorCode: float64(mcpparser.CodeInternalError), // our envelope, not the upstream's code 1
		},
		{
			name:          "error alone passes through unfiltered",
			body:          `data: {"jsonrpc":"2.0","id":1,"error":{"code":1,"message":"x"}}` + "\n\n",
			wantErrorCode: 1, // the upstream's own error, untouched
		},
		{
			// A literal `"result":null` is exempted from the both-error-
			// and-result fail-closed rule (see filterListResponse): a null
			// result can never carry a list, so failing closed on it only
			// destroys the upstream's real error code for no security
			// benefit. Regression for that exemption.
			name:          "error with explicit null result passes through unfiltered",
			body:          `data: {"jsonrpc":"2.0","id":1,"error":{"code":404,"message":"not found"},"result":null}` + "\n\n",
			wantErrorCode: 404, // the upstream's own error, untouched
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := newUser1Request(t)

			rr := httptest.NewRecorder()
			rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
			rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

			_, err := rfw.Write([]byte(tc.body))
			require.NoError(t, err)
			require.NoError(t, rfw.FlushAndFilter())

			out := rr.Body.String()
			assert.NotContains(t, out, "admin_tool",
				"a Response carrying both error and result must not pass the list through under cover of the error field")

			firstLine := strings.TrimSuffix(strings.SplitN(out, "\n", 2)[0], "\r")
			var decoded struct {
				Error struct {
					Code float64 `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(firstLine, "data: ")), &decoded))
			assert.Equal(t, tc.wantErrorCode, decoded.Error.Code,
				"the error code distinguishes fail-closed (mcpparser.CodeInternalError, our envelope) from pass-through (the upstream's own code)")
		})
	}
}

// TestResponseFilteringWriter_JSON_DisguisedResponseFrame is the application/json
// counterpart: the disguised-result bypass is transport-independent, so the JSON
// path must also fail closed on a smuggled result rather than write it through.
//
// It also pins the writeErrorResponse envelope: the error body written on this
// path must be conformant JSON-RPC (lowercase "jsonrpc"/"id"/"error" keys, no
// "id" key for a notification) and must carry the *original request's* id, not
// an empty one, so the client can still correlate the denial.
func TestResponseFilteringWriter_JSON_DisguisedResponseFrame(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)

	// A batch array smuggling a tools/list result. DecodeMessage rejects the
	// array, so the pre-fix JSON path wrote it through raw.
	const smuggled = `[{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"admin_tool"}]}}]`

	// writeErrorResponse logs the carriesResult error server-side and sends the
	// client a fixed generic message alongside the standard JSON-RPC Internal
	// Error code (mcpparser.CodeInternalError), not the HTTP status.
	wantErr := `"error":{"code":` + strconv.FormatInt(mcpparser.CodeInternalError, 10) + `,"message":"internal error"}`

	testCases := []struct {
		name      string
		reqIDJSON string // raw "id" field on the *incoming* request; "" omits it (notification)
		wantIDKey string // expected "id" fragment in the error envelope; "" if id must be absent
	}{
		{
			name:      "int id is recovered onto the error envelope",
			reqIDJSON: `"id":42,`,
			wantIDKey: `"id":42,`,
		},
		{
			name:      "string id is recovered onto the error envelope",
			reqIDJSON: `"id":"abc",`,
			wantIDKey: `"id":"abc",`,
		},
		{
			name:      "notification (no id) omits id entirely",
			reqIDJSON: "",
			wantIDKey: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Run the real MCP parsing middleware so the request context carries a
			// ParsedMCPRequest, matching how the request would look at this point
			// in the real middleware chain. writeErrorResponse's id-recovery path
			// reads the request id back out of that context.
			reqBody := `{"jsonrpc":"2.0",` + tc.reqIDJSON + `"method":"tools/list"}`
			parsedReq := newParsedUser1Request(t, reqBody)

			rr := httptest.NewRecorder()
			rfw := NewResponseFilteringWriter(rr, authorizer, parsedReq, string(mcp.MethodToolsList), nil, nil)
			rfw.ResponseWriter.Header().Set("Content-Type", "application/json")

			_, err := rfw.Write([]byte(smuggled))
			require.NoError(t, err)
			require.NoError(t, rfw.FlushAndFilter())

			assert.Equal(t, http.StatusInternalServerError, rr.Code)

			body := rr.Body.String()
			assert.NotContains(t, body, "admin_tool",
				"smuggled result on the application/json transport leaked past the filter")

			// assert.JSONEq is key-case-sensitive, so this single assertion catches
			// "Error"/"ID" substituted for "error"/"id" (the historical bug), a
			// missing "jsonrpc" tag, and a wrong or missing id all at once.
			wantBody := `{"jsonrpc":"2.0",` + tc.wantIDKey + wantErr + `}`
			assert.JSONEq(t, wantBody, body)

			var decoded map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &decoded))
			_, hasID := decoded["id"]
			assert.Equal(t, tc.wantIDKey != "", hasID, "id key presence mismatch")
			_, hasResult := decoded["result"]
			assert.False(t, hasResult, "error response must not contain a result key")
		})
	}
}

// TestErrorResponseBody pins the wire shape of the JSON-RPC error envelope
// errorResponseBody produces for a filter/encode failure: a "2.0" jsonrpc
// tag, the standard JSON-RPC internal-error code (mcpparser.CodeInternalError)
// with a fixed generic client-visible message that never echoes the wrapped
// error's text (#6066 — that error can originate in policy evaluation and
// name tools or resources), and an id that round-trips for a real request id
// but is entirely ABSENT (not present and null) for the zero-value id.
// Absence matters because MCP types the error response id as optional
// (id?: RequestId), and the reference TypeScript SDK's strict schema admits
// undefined but not null — a null id would make that client throw inside
// its transport.
func TestErrorResponseBody(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)
	// A distinctive sentinel, not a generic message: its absence from the
	// encoded body is what actually pins the #6066 scrub, rather than
	// passing by the coincidence of a short or common wrapped error string.
	wrapped := errors.New("leaky-tool-name-sentinel")

	testCases := []struct {
		name   string
		id     jsonrpc2.ID
		wantID any // nil means the id key must be absent
	}{
		{name: "int64 id round-trips", id: jsonrpc2.Int64ID(7), wantID: float64(7)},
		{name: "string id round-trips", id: jsonrpc2.StringID("abc"), wantID: "abc"},
		{name: "zero-value id key is absent, not null", id: jsonrpc2.ID{}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := newUser1Request(t)
			rfw := NewResponseFilteringWriter(httptest.NewRecorder(), authorizer, req, string(mcp.MethodToolsList), nil, nil)
			body := rfw.errorResponseBody(tc.id, wrapped)

			var decoded map[string]any
			require.NoError(t, json.Unmarshal(body, &decoded))

			assert.Equal(t, "2.0", decoded["jsonrpc"])

			errObj, ok := decoded["error"].(map[string]any)
			require.True(t, ok, "error field must decode as an object")
			assert.Equal(t, float64(mcpparser.CodeInternalError), errObj["code"])
			assert.Equal(t, "internal error", errObj["message"],
				"the client-visible message must be the fixed generic string, not the wrapped error")
			assert.NotContains(t, string(body), wrapped.Error(),
				"the wrapped error's text must never reach the wire (#6066)")

			idValue, hasID := decoded["id"]
			require.Equal(t, tc.wantID != nil, hasID, "id key presence mismatch")
			if tc.wantID != nil {
				assert.Equal(t, tc.wantID, idValue)
			}
		})
	}
}

// TestResponseFilteringWriter_SSE_SplitPayloadAcrossDataLines is a regression
// test for the #6037 rewrite: SSE data fields belonging to the same event
// concatenate (per WHATWG semantics) before the event is decoded, so a
// JSON-RPC frame split across two data: lines within one event must still be
// reconstructed and filtered, not smuggled past the filter because each half
// independently fails to decode.
//
// The split point is chosen right after a comma (a whitespace-legal position
// in JSON grammar), so each half is independently invalid JSON on its own,
// but the two halves, reassembled with the LF the SSE spec mandates between
// concatenated data values, form the original valid JSON-RPC frame again.
func TestResponseFilteringWriter_SSE_SplitPayloadAcrossDataLines(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)

	resultJSON, err := json.Marshal(mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "admin_tool", Description: "Sensitive admin operations"},
		},
	})
	require.NoError(t, err)
	encoded, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{
		ID:     jsonrpc2.Int64ID(1),
		Result: json.RawMessage(resultJSON),
	})
	require.NoError(t, err)

	splitAt := bytes.Index(encoded, []byte(`,"result"`)) + 1
	require.Greater(t, splitAt, 0, "test fixture assumption broken: no comma before \"result\" in the encoded frame")
	firstHalf, secondHalf := encoded[:splitAt], encoded[splitAt:]

	// Each half is independently undecodable and carries no result of its
	// own, so the pre-rewrite per-line filter passed both through unfiltered.
	_, err = jsonrpc2.DecodeMessage(firstHalf)
	require.Error(t, err, "test fixture assumption broken: first half must not decode on its own")
	require.False(t, carriesResult(firstHalf), "test fixture assumption broken: first half must not independently carry a result")
	_, err = jsonrpc2.DecodeMessage(secondHalf)
	require.Error(t, err, "test fixture assumption broken: second half must not decode on its own")
	require.False(t, carriesResult(secondHalf), "test fixture assumption broken: second half must not independently carry a result")

	body := strings.Join([]string{"data: " + string(firstHalf), "data: " + string(secondHalf), "", ""}, "\n")

	req := newUser1Request(t)

	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
	rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	_, err = rfw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, rfw.FlushAndFilter())

	out := rr.Body.String()
	assert.NotContains(t, out, "admin_tool",
		"a JSON-RPC frame split across two data: lines within one event must still be filtered")
	assert.Contains(t, out, "weather", "the authorized tool must survive filtering")
}

// TestResponseFilteringWriter_SSE_MultiEventBody is a regression test for the
// #6037 rewrite: a body carrying two complete SSE events (each terminated by
// its own blank line) must deliver both events, with the blank line between
// them preserved, and the second event's result still filtered. The
// pre-rewrite per-line implementation dropped blank lines outright and
// reconciled the deficit with at most one substitute separator, collapsing
// any two-event body into something that looks like one malformed event.
func TestResponseFilteringWriter_SSE_MultiEventBody(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)

	notificationLine := `data: {"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"warming up"}}`

	resultJSON, err := json.Marshal(mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "admin_tool", Description: "Sensitive admin operations"},
		},
	})
	require.NoError(t, err)
	encoded, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{
		ID:     jsonrpc2.Int64ID(1),
		Result: json.RawMessage(resultJSON),
	})
	require.NoError(t, err)
	respLine := "data: " + string(encoded)

	// Two complete events, each terminated by its own blank line.
	body := strings.Join([]string{notificationLine, "", respLine, "", ""}, "\n")

	req := newUser1Request(t)

	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
	rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	_, err = rfw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, rfw.FlushAndFilter())

	out := rr.Body.String()

	// Both events must survive as separate events: the notification verbatim,
	// separated by its own blank line from the (filtered) list response.
	assert.Contains(t, out, notificationLine+"\n\n",
		"the notification event must be delivered with its terminating blank line intact")
	assert.NotContains(t, out, "admin_tool", "the second event's result must still be filtered")
	assert.Contains(t, out, "weather", "the authorized tool must survive filtering")
}

// TestResponseFilteringWriter_SSE_StructuralRoundTrip verifies the core
// structural invariant of the #6037 rewrite: a body needing no filtering
// (here, an interleaved notification whose event carries no result) is
// reproduced byte-for-byte, regardless of how the body's separators trail.
func TestResponseFilteringWriter_SSE_StructuralRoundTrip(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)

	// The notification event carries no result, so it never reaches Cedar
	// evaluation; the policy content is irrelevant to this test.
	const dataLine = `data: {"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"hi"}}`

	testCases := []struct {
		name string
		body string
	}{
		{name: "ends with a blank line", body: "event: message\n" + dataLine + "\n\n"},
		{name: "ends with a single separator, no blank line", body: "event: message\n" + dataLine + "\n"},
		{name: "no separator anywhere in the body", body: dataLine},
		{name: "CRLF body ending with a blank line", body: "event: message\r\n" + dataLine + "\r\n\r\n"},
		// One event terminated by CRLF then a lone-LF blank line, followed by
		// a second event terminated by a lone CR then a CRLF blank line: all
		// three SSE-legal terminators appear in one body, which per WHATWG an
		// upstream may do freely.
		{name: "mixed CRLF, LF and CR within one body", body: dataLine + "\r\n" + "\n" + "event: ping" + "\r" + "\r\n"},
		{name: "consecutive blank lines", body: dataLine + "\n\n\n"},
		{name: "blank lines only", body: "\n\n\n"},
		// Guards the idx+1 < len(rawResponse) bounds check: a lone trailing
		// CR with nothing after it must not index out of range.
		{name: "ends with a lone CR", body: dataLine + "\r"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := newUser1Request(t)

			rr := httptest.NewRecorder()
			rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
			rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

			_, err := rfw.Write([]byte(tc.body))
			require.NoError(t, err)
			require.NoError(t, rfw.FlushAndFilter())

			assert.Equal(t, tc.body, rr.Body.String(),
				"a body needing no filtering must be reproduced byte-for-byte")
		})
	}
}

// TestResponseFilteringWriter_SSE_FilteredEventTerminators covers a hole
// TestResponseFilteringWriter_SSE_StructuralRoundTrip leaves open: every row
// there asserts byte-identity for a body that needs no filtering, so nothing
// ever reaches resolveSSEEvent's replacement branch. Swapping that branch's
// `term: l.term` for a hardcoded []byte("\n") would still pass the whole
// suite. These cases exercise a body that IS filtered, so they assert the
// (changed) output rather than a reproduced input and don't belong under the
// byte-identity test's name.
func TestResponseFilteringWriter_SSE_FilteredEventTerminators(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)

	resultJSON, err := json.Marshal(mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "admin_tool", Description: "Sensitive admin operations"},
		},
	})
	require.NoError(t, err)
	encoded, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{
		ID:     jsonrpc2.Int64ID(1),
		Result: json.RawMessage(resultJSON),
	})
	require.NoError(t, err)

	t.Run("CRLF body: filtered data line and blank line stay CRLF-terminated", func(t *testing.T) {
		t.Parallel()

		req := newUser1Request(t)
		rr := httptest.NewRecorder()
		rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
		rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

		body := "data: " + string(encoded) + "\r\n\r\n"
		_, err := rfw.Write([]byte(body))
		require.NoError(t, err)
		require.NoError(t, rfw.FlushAndFilter())

		out := rr.Body.String()
		assert.NotContains(t, out, "admin_tool", "the filtered event must not leak the denied tool")
		assert.Contains(t, out, "weather", "the authorized tool must survive filtering")
		require.True(t, strings.HasSuffix(out, "\r\n\r\n"),
			"the replacement data line and the following blank line must both stay CRLF-terminated: %q", out)

		// Every LF in the output must be immediately preceded by a CR: a bare
		// LF proves the replacement line (or the blank line after it) fell
		// back to a hardcoded "\n" instead of the CRLF the input actually used.
		for i := 0; i < len(out); i++ {
			if out[i] == '\n' {
				require.True(t, i > 0 && out[i-1] == '\r', "found a bare LF at byte %d in %q", i, out)
			}
		}
	})

	t.Run("trailing filtered event with no closing blank line", func(t *testing.T) {
		t.Parallel()

		req := newUser1Request(t)
		rr := httptest.NewRecorder()
		rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
		rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

		// No trailing terminator at all: the backend closed the stream
		// mid-event, same fixture as the unterminated-body case documented
		// in processSSEResponse.
		body := "data: " + string(encoded)
		_, err := rfw.Write([]byte(body))
		require.NoError(t, err)
		require.NoError(t, rfw.FlushAndFilter())

		out := rr.Body.String()
		assert.NotContains(t, out, "admin_tool", "the filtered event must not leak the denied tool")
		assert.Contains(t, out, "weather", "the authorized tool must survive filtering")
		assert.False(t, strings.HasSuffix(out, "\n") || strings.HasSuffix(out, "\r"),
			"an unterminated input must not gain a fabricated terminator: %q", out)
	})
}

// TestResponseFilteringWriter_FilterBypassAttempts verifies that list results a
// backend tries to slip past the filter -- via media-type casing or whitespace
// (RFC 9110 makes both legal), a non-200 2xx status (fetch-based clients accept
// any 2xx as ok), or an unrecognized Content-Type on a result-carrying body --
// are still filtered, while responses with nothing to filter pass through.
func TestResponseFilteringWriter_FilterBypassAttempts(t *testing.T) {
	t.Parallel()

	// The policy permits only "weather"; "admin_tool" must never reach the client.
	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	resultData, err := json.Marshal(mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "admin_tool", Description: "Admin operations"},
		},
	})
	require.NoError(t, err)
	listBody, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{
		ID:     jsonrpc2.Int64ID(1),
		Result: json.RawMessage(resultData),
	})
	require.NoError(t, err)
	sseListBody := []byte("event: message\ndata: " + string(listBody) + "\n\n")

	testCases := []struct {
		name        string
		contentType string // "" leaves the Content-Type header unset
		statusCode  int
		body        []byte
		// wantFiltered: the denied tool is stripped from the client-visible
		// body; otherwise the body passes through byte-for-byte.
		wantFiltered bool
	}{
		{
			name:         "uppercase media type is normalized and filtered",
			contentType:  "Application/JSON",
			statusCode:   http.StatusOK,
			body:         listBody,
			wantFiltered: true,
		},
		{
			name:         "whitespace before media type parameters is normalized and filtered",
			contentType:  "application/json ; charset=utf-8",
			statusCode:   http.StatusOK,
			body:         listBody,
			wantFiltered: true,
		},
		{
			name:         "2xx status other than 200 and 202 is filtered",
			contentType:  "application/json",
			statusCode:   http.StatusCreated,
			body:         listBody,
			wantFiltered: true,
		},
		{
			name:         "unrecognized media type carrying a JSON result is filtered",
			contentType:  "text/plain",
			statusCode:   http.StatusOK,
			body:         listBody,
			wantFiltered: true,
		},
		{
			name:         "missing media type carrying an SSE result is filtered",
			contentType:  "",
			statusCode:   http.StatusOK,
			body:         sseListBody,
			wantFiltered: true,
		},
		{
			name:         "unrecognized media type with no JSON-RPC result passes through",
			contentType:  "text/plain",
			statusCode:   http.StatusOK,
			body:         []byte("plain text, nothing to filter"),
			wantFiltered: false,
		},
		{
			name:         "non-2xx error response passes through unfiltered",
			contentType:  "application/json",
			statusCode:   http.StatusInternalServerError,
			body:         listBody,
			wantFiltered: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequest(http.MethodPost, "/messages", nil)
			require.NoError(t, err)
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
				Subject: "user123",
				Claims:  jwt.MapClaims{"sub": "user123"},
			}}
			req = req.WithContext(auth.WithIdentity(req.Context(), identity))

			rr := httptest.NewRecorder()
			fw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
			if tc.contentType != "" {
				fw.ResponseWriter.Header().Set("Content-Type", tc.contentType)
			}
			fw.WriteHeader(tc.statusCode)
			_, err = fw.Write(tc.body)
			require.NoError(t, err)
			require.NoError(t, fw.FlushAndFilter())

			assert.Equal(t, tc.statusCode, rr.Code, "original status code should be preserved")
			if tc.wantFiltered {
				body := rr.Body.String()
				assert.Contains(t, body, "weather", "permitted tool should remain in the response")
				assert.NotContains(t, body, "admin_tool", "denied tool leaked to the client")
			} else {
				assert.Equal(t, tc.body, rr.Body.Bytes(), "response should pass through unchanged")
			}
		})
	}
}

// TestResponseFilteringWriter_SSE_LeadingNonJSONWhitespaceStillFiltered is a
// regression test for the assembled-payload TrimSpace fix: Go's JSON scanner
// only treats SP/TAB/CR/LF as whitespace, but unicode.IsSpace (and the
// go-sdk client's own buffer trim, mcp/event.go) also covers U+000B, U+000C,
// U+0085, U+00A0, U+1680 and U+3000. Any of those left on the front of the
// assembled payload made DecodeMessage and carriesResult both reject it while
// the client parsed it fine, so the event fell to the undecodable-passthrough
// branch and the unfiltered list went out (#5257).
//
// NotContains on admin_tool alone would also be satisfied by the event
// failing closed, which would silently degrade every legitimate response
// that happens to carry leading whitespace -- so each case also requires the
// permitted tool to survive, proving the event was filtered, not dropped.
func TestResponseFilteringWriter_SSE_LeadingNonJSONWhitespaceStillFiltered(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)

	resultJSON, err := json.Marshal(mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "admin_tool", Description: "Sensitive admin operations"},
		},
	})
	require.NoError(t, err)
	encoded, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{
		ID:     jsonrpc2.Int64ID(1),
		Result: json.RawMessage(resultJSON),
	})
	require.NoError(t, err)

	testCases := []struct {
		name   string
		prefix string
	}{
		{name: "no prefix (baseline)", prefix: ""},
		{name: "U+000B line tabulation", prefix: "\u000B"},
		{name: "U+000C form feed", prefix: "\u000C"},
		{name: "U+0085 next line", prefix: "\u0085"},
		{name: "U+00A0 no-break space", prefix: "\u00A0"},
		{name: "U+1680 ogham space mark", prefix: "\u1680"},
		{name: "U+3000 ideographic space", prefix: "\u3000"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := newUser1Request(t)
			rr := httptest.NewRecorder()
			rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
			rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

			body := "data: " + tc.prefix + string(encoded) + "\n\n"
			_, err := rfw.Write([]byte(body))
			require.NoError(t, err)
			require.NoError(t, rfw.FlushAndFilter())

			out := rr.Body.String()
			assert.NotContains(t, out, "admin_tool",
				"a leading non-JSON-whitespace byte before the payload must not bypass the filter")
			assert.Contains(t, out, "weather",
				"the permitted tool must survive filtering, proving the event was filtered rather than dropped")
		})
	}
}

// TestResponseFilteringWriter_SSE_PerValueProbeCatchesConcatenatedGarbage is a
// regression test for the per-value probe added to resolveSSEEvent: one
// malformed data: value anywhere in an event used to give up filtering for
// the WHOLE event. Concretely, "garbage" followed (no blank line, so same
// event) by a genuine result-bearing frame assembles into
// "garbage\n{...result...}"; jsonrpc2.DecodeMessage stops at "garbage" and
// carriesResult on the whole joined payload does too, so both lines went out
// unfiltered. The fix probes each data: value independently before
// conceding the event needs no filtering.
//
// NotContains(admin_tool) alone would also be satisfied by the event being
// dropped silently (the #6037 hang), so this also requires the fail-closed
// error envelope to actually go out, correlated to the request's real id via
// newParsedUser1Request -- an unparsed request would let requestID() fall
// back to the zero ID and this test would accept an envelope no client could
// actually correlate.
func TestResponseFilteringWriter_SSE_PerValueProbeCatchesConcatenatedGarbage(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)

	resultJSON, err := json.Marshal(mcp.ListToolsResult{
		Tools: []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "admin_tool", Description: "Sensitive admin operations"},
		},
	})
	require.NoError(t, err)
	encoded, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{
		ID:     jsonrpc2.Int64ID(1),
		Result: json.RawMessage(resultJSON),
	})
	require.NoError(t, err)

	testCases := []struct {
		name    string
		garbage string
	}{
		{name: "plain garbage prefix", garbage: "garbage"},
		{name: "comment-like prefix", garbage: "//comment"},
		{name: "bare NaN prefix", garbage: "NaN"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			joined := tc.garbage + "\n" + string(encoded)
			_, decErr := jsonrpc2.DecodeMessage([]byte(joined))
			require.Error(t, decErr,
				"test fixture assumption broken: assembled payload must not decode as a single Response")
			require.False(t, carriesResult([]byte(joined)),
				"test fixture assumption broken: the whole-payload scan must not itself catch the smuggled result "+
					"-- otherwise this test would not exercise the per-value probe at all")

			parsedReq := newParsedUser1Request(t, `{"jsonrpc":"2.0","id":99,"method":"tools/list"}`)
			rr := httptest.NewRecorder()
			rfw := NewResponseFilteringWriter(rr, authorizer, parsedReq, string(mcp.MethodToolsList), nil, nil)
			rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

			// One event, two data: lines with no blank line between them, so
			// WHATWG assembly joins them with LF into one payload the
			// decoder gives up on at the first byte.
			body := "data: " + tc.garbage + "\n" + "data: " + string(encoded) + "\n\n"
			_, err := rfw.Write([]byte(body))
			require.NoError(t, err)
			require.NoError(t, rfw.FlushAndFilter())

			out := rr.Body.String()
			assert.NotContains(t, out, "admin_tool",
				"a result-bearing data: line following a malformed one in the same event must not bypass the filter")

			firstLine := strings.TrimSuffix(strings.SplitN(out, "\n", 2)[0], "\r")
			payload := strings.TrimPrefix(firstLine, "data: ")
			msg, decErr := jsonrpc2.DecodeMessage([]byte(payload))
			require.NoError(t, decErr, "the fail-closed envelope must itself be valid JSON-RPC")
			resp, ok := msg.(*jsonrpc2.Response)
			require.True(t, ok, "the fail-closed envelope must be a clean Response")
			require.NotNil(t, resp.Error, "the event must fail closed into an error envelope, not vanish silently")
			assert.Equal(t, jsonrpc2.Int64ID(99), resp.ID,
				"the error envelope must carry the request's id so the client can correlate it, or the #6037 hang reproduces")
		})
	}
}

// TestResponseFilteringWriter_SSE_InteriorBareCRNotALineBreak is a regression
// test for a client-divergence bypass: the client that actually consumes
// these streams (github.com/modelcontextprotocol/go-sdk's mcp/event.go)
// splits lines on LF only, so a bare "\r" sitting inside a single data:
// payload is insignificant JSON whitespace to it, not a line break. Splitting
// on "\r" as well (as strict WHATWG grammar does) cuts the line there
// instead, leaving a "data:"-less second half that contributes nothing to
// the assembled payload -- so the first half (truncated, undecodable) is
// classified as carrying no result and re-emitted verbatim, leaking
// admin_tool. NotContains(admin_tool) alone would also pass if the whole
// event were merely dropped, so this also requires weather to survive,
// proving the event was filtered rather than eaten.
func TestResponseFilteringWriter_SSE_InteriorBareCRNotALineBreak(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)
	req := newUser1Request(t)

	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
	rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	// A bare "\r" sits between "id":1, and "result", inside what a
	// non-conformant-to-WHATWG (but client-accurate) reader treats as ONE
	// data: line. "\r" is legal JSON whitespace between a comma and the next
	// key, so the client parses this payload intact.
	body := "data: {\"jsonrpc\":\"2.0\",\"id\":1,\r\"result\":" +
		`{"tools":[{"name":"weather"},{"name":"admin_tool"}]}}` + "\n\n"

	_, err := rfw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, rfw.FlushAndFilter())

	out := rr.Body.String()
	assert.NotContains(t, out, "admin_tool",
		"a bare CR inside a single data: payload must not split the line and truncate it past the filter")
	assert.Contains(t, out, "weather",
		"the authorized tool must survive filtering, proving the event was filtered rather than dropped")
}

// TestResponseFilteringWriter_SSE_InteriorExoticWhitespaceAcrossDataLines is a
// regression test for a client-divergence bypass distinct from the leading-
// whitespace case above: the go-sdk client TrimSpaces each data: value
// independently before joining them with LF (mcp/event.go), but a single
// whole-buffer trim (applied once, after joining) only reaches the very
// front and back of the assembled payload. An exotic unicode.IsSpace byte
// (one of U+000B, U+000C, U+0085, U+00A0, U+1680, U+3000 -- whitespace to the
// client's trim but not to Go's own JSON scanner) sitting at the boundary
// BETWEEN two data: lines of the same event survives a whole-buffer trim,
// because it's never at either edge of the joined result. That leaves an
// illegal byte mid-token for our decoder while the client's own per-value
// trim removes it before joining, so the client parses fine while ours
// rejects the payload as undecodable and (pre-fix) re-emitted it raw.
func TestResponseFilteringWriter_SSE_InteriorExoticWhitespaceAcrossDataLines(t *testing.T) {
	t.Parallel()

	exoticBytes := []struct {
		name   string
		suffix string
	}{
		{name: "U+000B line tabulation", suffix: "\u000B"},
		{name: "U+000C form feed", suffix: "\u000C"},
		{name: "U+0085 next line", suffix: "\u0085"},
		{name: "U+00A0 no-break space", suffix: "\u00A0"},
		{name: "U+1680 ogham space mark", suffix: "\u1680"},
		{name: "U+3000 ideographic space", suffix: "\u3000"},
	}

	for _, tc := range exoticBytes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			authorizer := newWeatherOnlyAuthorizer(t)
			req := newUser1Request(t)

			rr := httptest.NewRecorder()
			rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
			rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

			// Two data: lines, no blank line between them, so they belong to
			// the SAME event and their values concatenate. The exotic byte
			// trails the first value, sitting exactly at the interior join
			// boundary once assembled -- never at the front or back of the
			// whole buffer.
			body := `data: {"jsonrpc":"2.0","id":1,` + tc.suffix + "\n" +
				`data: "result":{"tools":[{"name":"weather"},{"name":"admin_tool"}]}}` + "\n\n"

			_, err := rfw.Write([]byte(body))
			require.NoError(t, err)
			require.NoError(t, rfw.FlushAndFilter())

			out := rr.Body.String()
			assert.NotContains(t, out, "admin_tool",
				"exotic whitespace at an interior data: value boundary must not bypass the filter")
			assert.Contains(t, out, "weather",
				"the authorized tool must survive filtering, proving the event was filtered rather than dropped")
		})
	}
}

// TestResponseFilteringWriter_SSE_FailClosedDoesNotLeakSubsequentEvents pins
// the #6037 fail-closed property that neither the event-based rewrite nor the
// TrimSpace fix's tests cover: after a mid-stream event fails to filter and
// is replaced with an error envelope, the loop must keep filtering every
// event that follows, not fall back to passing the remainder of the stream
// through raw. failingFrame is a Response carrying both "error" and "result"
// (see TestResponseFilteringWriter_SSE_ErrorAndResultBypass), a real
// reachable trigger for the fail-closed branch on this path.
func TestResponseFilteringWriter_SSE_FailClosedDoesNotLeakSubsequentEvents(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)

	failingFrame := `{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":"x"},` +
		`"result":{"tools":[{"name":"admin_tool"}]}}`

	// The second event carries an authorized tool alongside the denied one on
	// purpose. A NotContains check alone would also be satisfied by the loop
	// TRUNCATING the stream after the failure — writing nothing downstream — so
	// it would pass under exactly the regression this test exists to catch.
	// Asserting the authorized tool survives is what proves the loop kept
	// filtering rather than giving up. Do not reduce this to one assertion.
	stream := "data: " + failingFrame + "\n\n" +
		"data: " + `{"jsonrpc":"2.0","id":9,"result":{"tools":[{"name":"weather"},{"name":"admin_tool"}]}}` + "\n\n"

	req := newUser1Request(t)
	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
	rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	_, err := rfw.Write([]byte(stream))
	require.NoError(t, err)
	require.NoError(t, rfw.FlushAndFilter())

	out := rr.Body.String()
	assert.NotContains(t, out, "admin_tool",
		"a smuggled-result event after a mid-stream filter failure must still be filtered, not passed through raw")
	assert.Contains(t, out, "weather",
		"the authorized tool proves the loop kept filtering after the failure instead of truncating the stream")
	assert.Contains(t, out, `"error"`,
		"the failing event must still emit its correlatable error envelope")
}

// sseEvent is one event dispatched by parseSSEStream.
type sseEvent struct {
	eventType string
	data      string
}

// parseSSEStream implements the WHATWG EventSource parsing grammar strictly,
// so tests assert actual client-visible delivery rather than merely
// conformant-looking bytes: after #6066 the error payload can look correct
// on the wire while never being dispatched to a client at all (e.g. a bare
// JSON object with no "data:" prefix). Only what this dispatches counts as
// delivered.
//
// Per the grammar: lines end at CRLF, LF, or CR; a leading ':' marks a
// comment line with no effect; a "name:value" line strips exactly one
// leading space from the value; a line with no colon is a field with an
// empty value; only "data" and "event" have any effect, every other field
// name is ignored outright; a blank line dispatches the event only if the
// data buffer is non-empty (and is reset either way); any data left
// buffered at EOF is discarded, never dispatched.
func parseSSEStream(t *testing.T, body []byte) []sseEvent {
	t.Helper()

	var events []sseEvent
	var eventType string
	var dataLines []string

	dispatch := func() {
		if len(dataLines) > 0 {
			events = append(events, sseEvent{eventType: eventType, data: strings.Join(dataLines, "\n")})
		}
		eventType = ""
		dataLines = nil
	}

	for len(body) > 0 {
		idx := bytes.IndexAny(body, "\r\n")
		var line []byte
		switch {
		case idx == -1:
			line, body = body, nil
		case body[idx] == '\r' && idx+1 < len(body) && body[idx+1] == '\n':
			line, body = body[:idx], body[idx+2:]
		default:
			line, body = body[:idx], body[idx+1:]
		}

		if len(line) == 0 {
			dispatch()
			continue
		}
		if line[0] == ':' {
			continue
		}

		field, value, hasColon := bytes.Cut(line, []byte(":"))
		if !hasColon {
			field, value = line, nil
		}
		value = bytes.TrimPrefix(value, []byte(" "))

		switch string(field) {
		case "data":
			dataLines = append(dataLines, string(value))
		case "event":
			eventType = string(value)
		}
		// Every other field name is ignored outright.
	}
	// Pending data at EOF is never dispatched.

	return events
}

// TestParseSSEStream_DeliversOnlyDispatchedEvents guards the SSE error path
// with the strict grammar parser rather than a raw byte/substring check: it
// asserts the fail-closed envelope is actually dispatched as a single
// default-typed event, which is what an MCP client (default/"message" event
// handler only) will actually see.
func TestParseSSEStream_DeliversOnlyDispatchedEvents(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)
	parsedReq := newParsedUser1Request(t, `{"jsonrpc":"2.0","id":99,"method":"tools/list"}`)

	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, parsedReq, string(mcp.MethodToolsList), nil, nil)
	rfw.ResponseWriter.Header().Set("Content-Type", "text/event-stream")

	body := `data: {"jsonrpc":"2.0","method":"notifications/initialized","id":1,` +
		`"result":{"tools":[{"name":"admin_tool"}]}}` + "\n\n"
	_, err := rfw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, rfw.FlushAndFilter())

	wantErrorJSON := string(rfw.errorResponseBody(jsonrpc2.Int64ID(99),
		errors.New("dropped a frame carrying a result outside a clean Response")))

	events := parseSSEStream(t, rr.Body.Bytes())
	require.Len(t, events, 1, "body: %q", rr.Body.String())
	assert.Empty(t, events[0].eventType, "MCP clients only process default/'message' events")
	assert.JSONEq(t, wantErrorJSON, events[0].data)
}

// TestParseSSEStream_RejectsBareJSONWithoutDataPrefix guards the guard: a
// bare JSON-RPC envelope with no "data:" prefix -- exactly what the pre-#6066
// code wrote directly to the response body -- must never be recognized as a
// dispatched SSE event, with or without a trailing newline. If
// parseSSEStream ever loosened enough to accept bare JSON, this fails first.
func TestParseSSEStream_RejectsBareJSONWithoutDataPrefix(t *testing.T) {
	t.Parallel()

	authorizer := newWeatherOnlyAuthorizer(t)
	req := newUser1Request(t)
	rfw := NewResponseFilteringWriter(httptest.NewRecorder(), authorizer, req, string(mcp.MethodToolsList), nil, nil)

	bareBody := rfw.errorResponseBody(jsonrpc2.Int64ID(1), errors.New("x"))

	testCases := []struct {
		name string
		body []byte
	}{
		{name: "no trailing newline", body: bareBody},
		{name: "with trailing newline", body: append(append([]byte{}, bareBody...), '\n')},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := parseSSEStream(t, tc.body)
			assert.Empty(t, events, "a bare JSON envelope with no data: prefix must never dispatch as an SSE event")
		})
	}
}

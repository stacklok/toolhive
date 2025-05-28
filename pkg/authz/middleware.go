// Package authz provides authorization utilities using Cedar policies.
package authz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
)

// MCPMethodToFeatureOperation maps MCP method names to feature and operation pairs.
var MCPMethodToFeatureOperation = map[string]struct {
	Feature   MCPFeature
	Operation MCPOperation
}{
	"tools/call":      {Feature: MCPFeatureTool, Operation: MCPOperationCall},
	"tools/list":      {Feature: MCPFeatureTool, Operation: MCPOperationList},
	"prompts/get":     {Feature: MCPFeaturePrompt, Operation: MCPOperationGet},
	"prompts/list":    {Feature: MCPFeaturePrompt, Operation: MCPOperationList},
	"resources/read":  {Feature: MCPFeatureResource, Operation: MCPOperationRead},
	"resources/list":  {Feature: MCPFeatureResource, Operation: MCPOperationList},
	"features/list":   {Feature: "", Operation: MCPOperationList},
	"ping":            {Feature: "", Operation: ""}, // Always allowed
	"progress/update": {Feature: "", Operation: ""}, // Always allowed
	"initialize":      {Feature: "", Operation: ""}, // Always allowed
}

// shouldSkipInitialAuthorization checks if the request should skip authorization
// before reading the request body.
func shouldSkipInitialAuthorization(r *http.Request) bool {
	// Skip authorization for non-POST requests and non-JSON content types
	if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return true
	}

	// Skip authorization for the SSE endpoint
	if strings.HasSuffix(r.URL.Path, ssecommon.HTTPSSEEndpoint) {
		return true
	}

	return false
}

// shouldSkipSubsequentAuthorization checks if the request should skip authorization
// after parsing the JSON-RPC message.
func shouldSkipSubsequentAuthorization(method string) bool {
	// Skip authorization for methods that don't require it
	if method == "ping" || method == "progress/update" || method == "initialize" {
		return true
	}

	return false
}

// extractResourceAndArguments extracts the resource ID and arguments from the params.
func extractResourceAndArguments(method string, params json.RawMessage) (string, map[string]interface{}) {
	var resourceID string
	var arguments map[string]interface{}

	// Parse the params based on the method
	if params != nil {
		var paramsMap map[string]interface{}
		if err := json.Unmarshal(params, &paramsMap); err == nil {
			// Extract resource ID based on the method
			switch method {
			case "tools/call":
				if name, ok := paramsMap["name"].(string); ok {
					resourceID = name
				}
				if args, ok := paramsMap["arguments"].(map[string]interface{}); ok {
					arguments = args
				}
			case "prompts/get":
				if name, ok := paramsMap["name"].(string); ok {
					resourceID = name
				}
			case "resources/read":
				if uri, ok := paramsMap["uri"].(string); ok {
					resourceID = uri
				}
			}
		}
	}

	return resourceID, arguments
}

// convertToJSONRPC2ID converts an interface{} ID to jsonrpc2.ID
func convertToJSONRPC2ID(id interface{}) (jsonrpc2.ID, error) {
	if id == nil {
		return jsonrpc2.ID{}, nil
	}

	switch v := id.(type) {
	case string:
		return jsonrpc2.StringID(v), nil
	case int:
		return jsonrpc2.Int64ID(int64(v)), nil
	case int64:
		return jsonrpc2.Int64ID(v), nil
	case float64:
		// JSON numbers are often unmarshaled as float64
		return jsonrpc2.Int64ID(int64(v)), nil
	default:
		return jsonrpc2.ID{}, fmt.Errorf("unsupported ID type: %T", id)
	}
}

// handleUnauthorized handles unauthorized requests.
func handleUnauthorized(w http.ResponseWriter, msgID interface{}, err error) {
	// Create an error response
	errorMsg := "Unauthorized"
	if err != nil {
		errorMsg = err.Error()
	}

	// Create a JSON-RPC error response
	id, err := convertToJSONRPC2ID(msgID)
	if err != nil {
		id = jsonrpc2.ID{} // Use empty ID if conversion fails
	}

	errorResponse := &jsonrpc2.Response{
		ID:    id,
		Error: jsonrpc2.NewError(403, errorMsg),
	}

	// Set the response headers
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)

	// Write the error response
	if err := json.NewEncoder(w).Encode(errorResponse); err != nil {
		// If we can't encode the error response, log it and return a simple error
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// Middleware creates an HTTP middleware that authorizes MCP requests using Cedar policies.
// This middleware extracts the MCP message from the request, determines the feature,
// operation, and resource ID, and authorizes the request using Cedar policies.
//
// For list operations (tools/list, prompts/list, resources/list), the middleware allows
// the request to proceed but intercepts the response to filter out items that the user
// is not authorized to access based on the corresponding call/get/read policies.
//
// Example usage:
//
//	// Create a Cedar authorizer with a policy that covers all tools and resources
//	cedarAuthorizer, _ := authz.NewCedarAuthorizer(authz.CedarAuthorizerConfig{
//	    Policies: []string{
//	        `permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
//	        `permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`,
//	        `permit(principal, action == Action::"read_resource", resource == Resource::"data");`,
//	    },
//	})
//
//	// Create a transport with the middleware
//	middlewares := []types.Middleware{
//	    jwtValidator.Middleware, // JWT middleware should be applied first
//	    cedarAuthorizer.Middleware, // Cedar middleware is applied second
//	}
//
//	proxy := httpsse.NewHTTPSSEProxy(8080, "my-container", middlewares...)
//	proxy.Start(context.Background())
func (a *CedarAuthorizer) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if we should skip authorization before reading the request body
		if shouldSkipInitialAuthorization(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Read the request body
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error reading request body: %v", err), http.StatusBadRequest)
			return
		}

		// Replace the request body for downstream handlers
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		// Parse the JSON-RPC message
		msg, err := jsonrpc2.DecodeMessage(bodyBytes)
		if err != nil {
			// If we can't parse the message, let the next handler deal with it
			next.ServeHTTP(w, r)
			return
		}

		// Skip authorization for non-request messages
		req, ok := msg.(*jsonrpc2.Request)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		// Check if we should skip authorization after parsing the message
		if shouldSkipSubsequentAuthorization(req.Method) {
			next.ServeHTTP(w, r)
			return
		}

		// Get the feature and operation from the method
		featureOp, ok := MCPMethodToFeatureOperation[req.Method]
		if !ok {
			// Unknown method, let the next handler deal with it
			next.ServeHTTP(w, r)
			return
		}

		// Handle list operations differently - allow them through but filter the response
		if featureOp.Operation == MCPOperationList {
			// Create a response filtering writer to intercept and filter the response
			filteringWriter := NewResponseFilteringWriter(w, a, r, req.Method)

			// Call the next handler with the filtering writer
			next.ServeHTTP(filteringWriter, r)

			// Flush the filtered response
			if err := filteringWriter.Flush(); err != nil {
				// If flushing fails, we've already started writing the response,
				// so we can't return an error response. Just log it.
				// In a real application, you might want to use a proper logger here.
				fmt.Printf("Error flushing filtered response: %v\n", err)
			}
			return
		}

		// For non-list operations, perform authorization as before
		// Extract resource ID and arguments from the params
		resourceID, arguments := extractResourceAndArguments(req.Method, req.Params)

		// Authorize the request
		authorized, err := a.AuthorizeWithJWTClaims(
			r.Context(),
			featureOp.Feature,
			featureOp.Operation,
			resourceID,
			arguments,
		)

		// Handle unauthorized requests
		if err != nil || !authorized {
			handleUnauthorized(w, req.ID.Raw(), err)
			return
		}

		// Call the next handler
		next.ServeHTTP(w, r)
	})
}

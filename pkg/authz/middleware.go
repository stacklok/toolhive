// Package authz provides authorization utilities using Cedar policies.
package authz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/stacklok/vibetool/pkg/transport"
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
	if strings.HasSuffix(r.URL.Path, transport.HTTPSSEEndpoint) {
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

// handleUnauthorized handles unauthorized requests.
func handleUnauthorized(w http.ResponseWriter, msgID interface{}, err error) {
	// Create an error response
	errorMsg := "Unauthorized"
	if err != nil {
		errorMsg = err.Error()
	}

	// Create a JSON-RPC error response
	errorResponse, _ := transport.NewErrorMessage(
		msgID,
		403, // Forbidden
		errorMsg,
		nil,
	)

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
//	middlewares := []transport.Middleware{
//	    jwtValidator.Middleware, // JWT middleware should be applied first
//	    cedarAuthorizer.Middleware, // Cedar middleware is applied second
//	}
//
//	proxy := transport.NewHTTPSSEProxy(8080, "my-container", middlewares...)
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
		var msg transport.JSONRPCMessage
		if err := json.Unmarshal(bodyBytes, &msg); err != nil {
			// If we can't parse the message, let the next handler deal with it
			next.ServeHTTP(w, r)
			return
		}

		// Skip authorization for non-request messages
		if !msg.IsRequest() {
			next.ServeHTTP(w, r)
			return
		}

		// Check if we should skip authorization after parsing the message
		if shouldSkipSubsequentAuthorization(msg.Method) {
			next.ServeHTTP(w, r)
			return
		}

		// Get the feature and operation from the method
		featureOp, ok := MCPMethodToFeatureOperation[msg.Method]
		if !ok {
			// Unknown method, let the next handler deal with it
			next.ServeHTTP(w, r)
			return
		}

		// Extract resource ID and arguments from the params
		resourceID, arguments := extractResourceAndArguments(msg.Method, msg.Params)

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
			handleUnauthorized(w, msg.ID, err)
			return
		}

		// Call the next handler
		next.ServeHTTP(w, r)
	})
}

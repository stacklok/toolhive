// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authz provides authorization utilities for MCP servers.
// It supports a pluggable authorizer architecture where different authorization
// backends (e.g., Cedar, OPA) can be registered and used based on configuration.
package authz

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// MCPMethodToFeatureOperation maps MCP method names to feature and operation pairs.
// Methods with empty Feature and Operation are always allowed (protocol-level).
// Methods not in this map are denied by default for security.
var MCPMethodToFeatureOperation = map[string]struct {
	Feature   authorizers.MCPFeature
	Operation authorizers.MCPOperation
}{
	// Core protocol methods - always allowed
	"initialize":      {Feature: "", Operation: ""}, // Protocol initialization
	"ping":            {Feature: "", Operation: ""}, // Health check
	"progress/update": {Feature: "", Operation: ""}, // Progress reporting

	// Tool operations - require authorization
	"tools/call": {Feature: authorizers.MCPFeatureTool, Operation: authorizers.MCPOperationCall},
	"tools/list": {Feature: authorizers.MCPFeatureTool, Operation: authorizers.MCPOperationList},

	// Prompt operations - require authorization
	"prompts/get":  {Feature: authorizers.MCPFeaturePrompt, Operation: authorizers.MCPOperationGet},
	"prompts/list": {Feature: authorizers.MCPFeaturePrompt, Operation: authorizers.MCPOperationList},

	// Resource operations - require authorization
	"resources/read":           {Feature: authorizers.MCPFeatureResource, Operation: authorizers.MCPOperationRead},
	"resources/list":           {Feature: authorizers.MCPFeatureResource, Operation: authorizers.MCPOperationList},
	"resources/templates/list": {Feature: authorizers.MCPFeatureResource, Operation: authorizers.MCPOperationList},
	"resources/subscribe":      {Feature: authorizers.MCPFeatureResource, Operation: authorizers.MCPOperationRead},
	"resources/unsubscribe":    {Feature: authorizers.MCPFeatureResource, Operation: authorizers.MCPOperationRead},

	// Discovery and capability methods - always allowed
	"features/list": {Feature: "", Operation: authorizers.MCPOperationList}, // Capability discovery
	"roots/list":    {Feature: "", Operation: ""},                           // Root directory discovery

	// Logging and client preferences - always allowed
	"logging/setLevel": {Feature: "", Operation: ""}, // Client preference for server logging

	// Argument completion - always allowed (UX feature)
	"completion/complete": {Feature: "", Operation: ""}, // Argument completion for prompts/resources

	// Notifications (server-to-client, informational) - always allowed
	"notifications/message":                {Feature: "", Operation: ""}, // General notifications
	"notifications/initialized":            {Feature: "", Operation: ""}, // Initialization complete
	"notifications/progress":               {Feature: "", Operation: ""}, // Progress updates
	"notifications/cancelled":              {Feature: "", Operation: ""}, // Request cancellation
	"notifications/roots/list_changed":     {Feature: "", Operation: ""}, // Roots changed
	"notifications/tools/list_changed":     {Feature: "", Operation: ""}, // Tools changed
	"notifications/prompts/list_changed":   {Feature: "", Operation: ""}, // Prompts changed
	"notifications/resources/list_changed": {Feature: "", Operation: ""}, // Resources changed
	"notifications/resources/updated":      {Feature: "", Operation: ""}, // Resource updated
	"notifications/tasks/status":           {Feature: "", Operation: ""}, // Task status update

	// NOTE: The following MCP methods are NOT included and will be DENIED by default:
	// - elicitation/create: User input prompting (requires new authorization feature)
	// - sampling/createMessage: LLM text generation (security-sensitive, requires new authorization feature)
	// - tasks/list, tasks/get, tasks/cancel, tasks/result: Task management (requires new authorization feature)
	//
	// To enable these methods, add appropriate authorization features/operations or add them
	// to the always-allowed list above after security review.
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

// Middleware creates an HTTP middleware that authorizes MCP requests.
// This middleware extracts the MCP message from the request, determines the feature,
// operation, and resource ID, and authorizes the request using the configured authorizer.
//
// For list operations (tools/list, prompts/list, resources/list), the middleware allows
// the request to proceed but intercepts the response to filter out items that the user
// is not authorized to access based on the corresponding call/get/read policies.
//
// The authorizer parameter should implement the authorizers.Authorizer interface,
// which can be created using authz.CreateMiddlewareFromConfig() or directly
// from an authorizer package (e.g., cedar.NewCedarAuthorizer()).
func Middleware(a authorizers.Authorizer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if we should skip authorization before checking parsed data
		if shouldSkipInitialAuthorization(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Get parsed MCP request from context (set by parsing middleware)
		parsedRequest := mcp.GetParsedMCPRequest(r.Context())
		if parsedRequest == nil {
			// No parsed MCP request available for a request that should have been parsed
			// This indicates either a malformed request or missing parsing middleware
			http.Error(w, "Invalid or malformed MCP request", http.StatusBadRequest)
			return
		}

		// Check if we should skip authorization after parsing the message
		if shouldSkipSubsequentAuthorization(parsedRequest.Method) {
			next.ServeHTTP(w, r)
			return
		}

		// Get the feature and operation from the method
		featureOp, ok := MCPMethodToFeatureOperation[parsedRequest.Method]
		if !ok {
			// Unknown method - deny by default for security
			// Methods must be explicitly added to MCPMethodToFeatureOperation to be allowed
			handleUnauthorized(w, parsedRequest.ID,
				fmt.Errorf("unknown MCP method: %s (not configured for authorization)", parsedRequest.Method))
			return
		}

		// Methods with empty feature and operation are always allowed (protocol-level)
		if featureOp.Feature == "" && featureOp.Operation == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Handle list operations differently - allow them through but filter the response
		if featureOp.Operation == authorizers.MCPOperationList {

			// Create a response filtering writer to intercept and filter the response
			filteringWriter := NewResponseFilteringWriter(w, a, r, parsedRequest.Method)

			// Call the next handler with the filtering writer
			next.ServeHTTP(filteringWriter, r)

			// Flush the filtered response
			if err := filteringWriter.FlushAndFilter(); err != nil {
				// If flushing fails, we've already started writing the response,
				// so we can't return an error response. Just log it.
				slog.Warn("Error flushing filtered response", "error", err)
			}
			return
		}

		// For non-list operations, perform authorization using parsed data
		// Authorize the request
		authorized, err := a.AuthorizeWithJWTClaims(
			r.Context(),
			featureOp.Feature,
			featureOp.Operation,
			parsedRequest.ResourceID,
			parsedRequest.Arguments,
		)

		// Handle unauthorized requests
		if err != nil || !authorized {
			handleUnauthorized(w, parsedRequest.ID, err)
			return
		}

		// Call the next handler
		next.ServeHTTP(w, r)
	})
}

// Factory middleware type constant
const (
	MiddlewareType = "authorization"
)

// FactoryMiddlewareParams represents the parameters for authorization middleware
type FactoryMiddlewareParams struct {
	ConfigPath string  `json:"config_path,omitempty"` // Kept for backwards compatibility
	ConfigData *Config `json:"config_data,omitempty"` // New field for config contents
}

// FactoryMiddleware wraps authorization middleware functionality for factory pattern
type FactoryMiddleware struct {
	middleware types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *FactoryMiddleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (*FactoryMiddleware) Close() error {
	// Authorization middleware doesn't need cleanup
	return nil
}

// CreateMiddleware factory function for authorization middleware
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {

	var params FactoryMiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal authorization middleware parameters: %w", err)
	}

	var authzConfig *Config
	var err error

	if params.ConfigData != nil {
		// Use provided config data (preferred method)
		authzConfig = params.ConfigData
	} else if params.ConfigPath != "" {
		// Load config from file (backwards compatibility)
		authzConfig, err = LoadConfig(params.ConfigPath)
		if err != nil {
			return fmt.Errorf("failed to load authorization configuration: %w", err)
		}
	} else {
		return fmt.Errorf("either config_data or config_path is required for authorization middleware")
	}

	middleware, err := CreateMiddlewareFromConfig(authzConfig, runner.GetConfig().GetName())
	if err != nil {
		return fmt.Errorf("failed to create authorization middleware: %w", err)
	}

	authzMw := &FactoryMiddleware{middleware: middleware}
	runner.AddMiddleware(config.Type, authzMw)
	return nil
}

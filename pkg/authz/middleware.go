// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authz provides authorization utilities for MCP servers.
// It supports a pluggable authorizer architecture where different authorization
// backends (e.g., Cedar, OPA) can be registered and used based on configuration.
package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/schema"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
)

// featureOperation pairs an MCP feature with an operation for authorization checks.
type featureOperation struct {
	Feature   authorizers.MCPFeature
	Operation authorizers.MCPOperation
}

// MCPMethodToFeatureOperation maps MCP method names to feature and operation pairs.
// Methods with empty Feature and Operation are always allowed (protocol-level).
// Methods not in this map are denied by default for security.
var MCPMethodToFeatureOperation = map[string]featureOperation{
	// Core protocol methods - always allowed
	"initialize": {Feature: "", Operation: ""}, // Protocol initialization
	"ping":       {Feature: "", Operation: ""}, // Health check
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

	// Skill operations - list responses are filtered by get authorization.
	"skills/get":  {Feature: authorizers.MCPFeatureSkill, Operation: authorizers.MCPOperationGet},
	"skills/list": {Feature: authorizers.MCPFeatureSkill, Operation: authorizers.MCPOperationList},

	// Discovery and capability methods - always allowed
	"features/list": {Feature: "", Operation: authorizers.MCPOperationList}, // Capability discovery
	"roots/list":    {Feature: "", Operation: ""},                           // Root directory discovery

	// server/discover, Modern's (2026-07-28) replacement for initialize+capability
	// negotiation, is always-allowed on THIS path (the single-server pkg/runner HTTP
	// authz Middleware -- vMCP's Modern dispatcher never consults this map at all, it
	// re-homes admission through core.Check*/core.List* directly). The always-allowed
	// choice rests on initialize parity, not on any per-request filtering this map
	// enforces: DiscoverResult carries the exact same Capabilities *ServerCapabilities
	// (+ Instructions) shape InitializeResult does, and "initialize" above has always
	// been always-allowed in this map. discover therefore adds no new exposure class --
	// note ServerCapabilities.Experimental/.Extensions (arbitrary backend-authored maps)
	// and Instructions (free text) are already freeform fields a backend can populate on
	// the always-allowed initialize response today, so "no descriptors" is a property of
	// how vMCP's dispatcher happens to build the value, not a guarantee this wire shape
	// makes on its own. Classifying it as MCPOperationList with an empty Feature
	// would be safe too: response_filter.go's authoritative classifier does not
	// assign server/discover a filter, so protocol-only list methods pass through
	// unchanged. The always-allowed classification is simpler and equally safe here.
	"server/discover": {Feature: "", Operation: ""},

	// Logging and client preferences - always allowed
	"logging/setLevel": {Feature: "", Operation: ""}, // Client preference for server logging

	// NOTE: completion/complete and subscriptions/listen are deliberately absent.
	// Both name a capability policy can already decide on, but not as one static
	// feature/operation pair, so they are classified by derivedAuthorizers
	// (derived_authz.go) instead. Middleware consults that map first, so a
	// duplicate static entry here would not shadow derived checks. The
	// regression to avoid is removing those classifiers and restoring an empty
	// feature/operation pair in this map — that is the original always-allow
	// bypass.

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
// before reading the request body. Content-Type is deliberately NOT consulted
// here: the middleware body refuses non-JSON POSTs with an explicit early
// return before this function is reached.
func shouldSkipInitialAuthorization(r *http.Request) bool {
	return r.Method != http.MethodPost
}

// shouldSkipSubsequentAuthorization checks if the request should skip authorization
// after parsing the JSON-RPC message.
func shouldSkipSubsequentAuthorization(method string) bool {
	// Skip authorization for methods that don't require it
	if method == "ping" || method == "initialize" {
		return true
	}

	return false
}

// invalidSkillGet reports whether a skills/get request lacks a valid URI.
func invalidSkillGet(featureOp featureOperation, resourceID string) bool {
	if featureOp.Feature != authorizers.MCPFeatureSkill || featureOp.Operation != authorizers.MCPOperationGet {
		return false
	}
	return resourceID == ""
}

// handleUnauthorized handles unauthorized requests. The client always sees the fixed
// "Unauthorized" message -- err (an authorizer failure) can carry policy detail that
// security.md forbids returning to callers, so it is logged server-side instead.
// Cedar's evaluation context can embed decoded JWT claim values (see the claim-keys-
// only rule in authorizers/cedar/core.go), so err must not be surfaced to the client,
// nor copied into additional log lines or fields beyond the single Warn below.
func handleUnauthorized(w http.ResponseWriter, msgID interface{}, err error) {
	if err != nil {
		slog.Warn("authorization denied", "error", err)
	}

	// Create a JSON-RPC error response
	id, convErr := mcp.ConvertToJSONRPC2ID(msgID)
	if convErr != nil {
		id = jsonrpc2.ID{} // Use empty ID if conversion fails
	}

	errorResponse := &jsonrpc2.Response{
		ID:    id,
		Error: jsonrpc2.NewError(mcp.JSONRPCCodeDenied, "Unauthorized"),
	}

	// The helper encodes before writing any header, so a marshal failure never
	// leaves a half-written response (e.g. a 403 header followed by a second
	// 500 write). Nothing to do on a write error for a denial body.
	_ = mcp.WriteJSONRPCError(w, http.StatusForbidden, errorResponse)
}

// rejectInvalidMCPRequest writes the 400 response for requests that arrive
// without a parsed MCP message: non-JSON POSTs refused by the middleware
// (the load-bearing security refusal) and malformed JSON POSTs.
func rejectInvalidMCPRequest(w http.ResponseWriter) {
	http.Error(w, "Invalid or malformed MCP request", http.StatusBadRequest)
}

// Middleware creates an HTTP middleware that authorizes MCP requests.
// This middleware extracts the MCP message from the request, determines the feature,
// operation, and resource ID, and authorizes the request using the configured authorizer.
//
// For protected list operations (tools/list, prompts/list, resources/list,
// resources/templates/list, and skills/list), the middleware allows the request to
// proceed only when a response filter is registered, then filters out items that
// the user is not authorized to access based on the corresponding call/get/read
// policies. In particular, skills/list entries are filtered individually by
// get_skill authorization.
//
// An in-memory annotation cache is maintained per middleware instance. When a
// tools/list response passes through, tool annotations are captured. When a
// subsequent tools/call request arrives, the cached annotations are injected into
// the request context so that authorizers can use them for policy decisions.
//
// The authorizer parameter should implement the authorizers.Authorizer interface,
// which can be created using authz.CreateMiddlewareFromConfig() or directly
// from an authorizer package (e.g., cedar.NewCedarAuthorizer()).
func Middleware(a authorizers.Authorizer, next http.Handler, passThroughTools map[string]struct{}) http.Handler {
	// Cache is shared across requests for the same proxy.
	// Populated from tools/list responses, read during tools/call.
	annotationCache := NewAnnotationCache()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Non-JSON POSTs are rejected deliberately and must never be passed
		// through. Such a request is not parsed as MCP, so message-level
		// authorization cannot run, but the proxy still forwards the body
		// verbatim and MCP backends parse JSON-RPC without checking
		// Content-Type. This early return is load-bearing for security: it is
		// the only point that keeps a JSON-RPC body smuggled under text/plain
		// from reaching the backend un-authorized. The marker lets the outer
		// audit middleware record the refusal as a denial, not a 400 failure.
		if r.Method == http.MethodPost && !mcp.RequestHasJSONContentType(r) {
			if marker, ok := mcp.AuthzDenialMarkerFromContext(r.Context()); ok {
				marker.Denied = true
			}
			rejectInvalidMCPRequest(w)
			return
		}

		// Check if we should skip authorization before checking parsed data
		if shouldSkipInitialAuthorization(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Get parsed MCP request from context (set by parsing middleware)
		parsedRequest := mcp.GetParsedMCPRequest(r.Context())
		if parsedRequest == nil {
			handleUnparsedMCPRequest(w, r, next)
			return
		}

		// Check if we should skip authorization after parsing the message
		if shouldSkipSubsequentAuthorization(parsedRequest.Method) {
			next.ServeHTTP(w, r)
			return
		}

		// Methods whose authorization target is derived from the request body are
		// resolved first, ahead of the static map, so no always-allowed entry can
		// ever shadow their per-capability checks.
		if derive, ok := derivedAuthorizers[parsedRequest.Method]; ok {
			authorizeDerivedAndServe(w, r, a, parsedRequest, derive, next)
			return
		}

		// Get the feature and operation from the method
		featureOp, ok := MCPMethodToFeatureOperation[parsedRequest.Method]
		if !ok {
			// Unknown method - deny by default for security. Methods must be
			// explicitly added to MCPMethodToFeatureOperation to be allowed.
			// This is expected traffic (e.g. a newer-spec client), not an
			// operational failure, so it's Debug rather than the Warn
			// handleUnauthorized logs for an actual authorizer error.
			slog.Debug("MCP method denied by default", "method", parsedRequest.Method)
			handleUnauthorized(w, parsedRequest.ID, nil)
			return
		}

		// Methods with empty feature and operation are always allowed (protocol-level)
		if featureOp.Feature == "" && featureOp.Operation == "" {
			next.ServeHTTP(w, r)
			return
		}

		// skills/get identifies its target only by params.uri. An absent, empty,
		// or non-string URI must never reach an authorizer, whose policy might
		// otherwise accidentally permit an empty identifier.
		if invalidSkillGet(featureOp, parsedRequest.ResourceID) {
			handleUnauthorized(w, parsedRequest.ID, nil)
			return
		}

		// Handle list operations differently: protected methods require a registered
		// response filter, while protocol-only methods pass through unchanged.
		if featureOp.Operation == authorizers.MCPOperationList {
			authorizeListAndServe(w, r, a, parsedRequest, featureOp, annotationCache, passThroughTools, next)
			return
		}

		// For tools/call, look up annotations and handle pass-through meta-tools.
		if featureOp.Feature == authorizers.MCPFeatureTool && featureOp.Operation == authorizers.MCPOperationCall {
			handleToolsCall(w, r, a, parsedRequest, featureOp, annotationCache, passThroughTools, next)
			return
		}

		// For non-list, non-tool operations, perform authorization using parsed data.
		authorizeAndServe(w, r, a, annotationCache,
			featureOp.Feature, featureOp.Operation,
			parsedRequest.ID, parsedRequest.ResourceID, parsedRequest.Arguments, next)
	})
}

// handleUnparsedMCPRequest forwards valid client responses to server-initiated
// requests and rejects requests that could not be parsed as MCP messages.
func handleUnparsedMCPRequest(w http.ResponseWriter, r *http.Request, next http.Handler) {
	// A JSON-RPC response or error answers a request the SERVER initiated
	// (ping, elicitation, sampling). It names no method and reaches no tool,
	// so there is nothing to authorize, and streamable HTTP requires the
	// transport to accept it with 202. Rejecting it tears the client's
	// session down (#5009).
	if mcp.IsClientResponse(r.Context()) {
		next.ServeHTTP(w, r)
		return
	}

	// Non-JSON POSTs are already rejected by the early return above, so a nil
	// parsed request here means a malformed JSON body or missing parsing
	// middleware. This is only a fallback behind the content-type refusal.
	rejectInvalidMCPRequest(w)
}

// authorizeListAndServe intercepts a list response and applies its registered
// per-item authorization filter. Protected methods without a filter fail closed
// before backend dispatch; protocol-only list methods remain pass-through.
func authorizeListAndServe(
	w http.ResponseWriter,
	r *http.Request,
	a authorizers.Authorizer,
	parsedRequest *mcp.ParsedMCPRequest,
	featureOp featureOperation,
	annotationCache *AnnotationCache,
	passThroughTools map[string]struct{},
	next http.Handler,
) {
	// A protected list operation without a response filter would expose every
	// descriptor returned by the backend. Deny before dispatch so additions to
	// MCPMethodToFeatureOperation cannot silently create another filter bypass.
	if featureOp.Feature != "" && !requiresResponseFiltering(parsedRequest.Method) {
		slog.Error("protected MCP list method has no response filter; denying request",
			"method", parsedRequest.Method)
		handleUnauthorized(w, parsedRequest.ID, nil)
		return
	}

	filteringWriter := NewResponseFilteringWriter(
		w, a, r, parsedRequest.Method, annotationCache, passThroughTools,
	)
	next.ServeHTTP(filteringWriter, r)

	if err := filteringWriter.FlushAndFilter(); err != nil {
		// The response may already be committed, so filtering errors can only be logged here.
		slog.Warn("error flushing filtered response", "error", err)
	}
}

// maxDerivedAuthzBudget bounds the total wall-clock time one request may spend
// making authorization decisions, across all of its derived checks.
//
// The count limit alone does not bound this. authorizers/http issues one
// outbound POST per decision with its own per-call timeout (30s by default), so
// a PDP that hangs turns N decisions into N times that timeout -- at the
// subscription limit, over twenty minutes of a request held open. The budget
// makes the worst case independent of N.
//
// It is set to the HTTP authorizer's own default per-call timeout on purpose:
// one request may spend no longer authorizing than a single decision was
// already allowed to take. That makes this a ceiling on fan-out rather than a
// new constraint on how slow an individual decision may be.
const maxDerivedAuthzBudget = 30 * time.Second

// authorizeChecks runs every check in order under one shared deadline, stopping
// at the first that does not pass. It reports whether all of them passed.
//
// The budget is applied only when there is more than one decision to make. A
// single decision has no fan-out to bound and is already limited by the
// authorizer's own per-call timeout, which an operator may have configured
// deliberately; wrapping it here would silently override that and make, say, a
// completion's get_prompt decision stricter than the identical decision for
// prompts/get.
//
// A budget overrun surfaces as an error from the authorizer call, which the
// caller maps to the same fail-closed denial any other authorizer error
// produces.
func authorizeChecks(
	ctx context.Context,
	a authorizers.Authorizer,
	checks []resourceCheck,
	budget time.Duration,
) (bool, error) {
	if len(checks) > 1 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}

	for i, check := range checks {
		// Arguments are deliberately nil, NOT the request's parsed arguments.
		//
		// Cedar prefixes every argument key with "arg_" and merges the result into
		// both the resource entity's attributes and the evaluation context
		// (authorizers/cedar/core.go preprocessArguments). The "arg_" namespace
		// therefore means "the arguments the authorized operation will run with".
		//
		// A derived method's params are NOT those arguments. pkg/mcp hands
		// completion/complete its ENTIRE params map as Arguments, so forwarding it
		// would let a caller put any key at the top level of a completion request
		// and have it evaluated as a prompt argument: a policy reading
		// context.arg_env would be satisfied by an attacker-chosen "env" that the
		// real prompts/get (which passes only params.arguments) could never forge.
		// For resources it is worse -- resources/read passes nil, so an
		// arg_-conditioned permit is unreachable there but would become reachable
		// through completion.
		//
		// nil is the fail-closed choice: an arg_-conditioned permit simply does not
		// match, so the request is denied rather than wrongly allowed. Passing real
		// completion context would need its own namespace, not this one.
		authorized, err := a.AuthorizeWithJWTClaims(ctx, check.Feature, check.Operation, check.resourceID, nil)
		if err != nil {
			// Counts, never identifiers: how far the request got is the useful
			// diagnostic for a budget overrun, and the URIs are caller-chosen.
			return false, fmt.Errorf("authorizing decision %d of %d: %w", i+1, len(checks), err)
		}
		if !authorized {
			return false, nil
		}
	}
	return true, nil
}

// authorizeDerivedAndServe authorizes a method whose targets are derived from the
// request body, dispatching only once every derived check passes.
//
// Checks are evaluated in order and the first denial ends the request, so a
// request naming several resources is admitted as a unit or not at all. A derive
// error is a denial too: it means no authorization target could be established,
// which must never be treated as "nothing to authorize".
func authorizeDerivedAndServe(
	w http.ResponseWriter,
	r *http.Request,
	a authorizers.Authorizer,
	parsedRequest *mcp.ParsedMCPRequest,
	derive deriveChecks,
	next http.Handler,
) {
	checks, err := derive(parsedRequest.Params)
	if err != nil {
		// WARN, not Debug: every derive failure is a request-SHAPE rejection (a
		// legacy bare-string ref, an unrecognised notifications member, a list over
		// the cap, undecodable params), not a policy decision. Each one means a
		// client or a policy needs updating, which is the repo's stated use for WARN
		// -- and it is the only signal an operator gets that an upgrade started
		// refusing traffic that previously passed. Policy denials stay silent, so
		// this does not log ordinary authorization outcomes.
		//
		// The reason is logged but never returned: the response body stays the fixed
		// "Unauthorized" so a prober cannot learn how its probe was malformed. Values
		// interpolated into these errors are truncated at the source, bounding what a
		// caller can write per line.
		slog.Warn("MCP request denied: no resolvable authorization target",
			"method", parsedRequest.Method, "error", err)
		handleUnauthorized(w, parsedRequest.ID, nil)
		return
	}

	authorized, err := authorizeChecks(r.Context(), a, checks, maxDerivedAuthzBudget)
	if err != nil || !authorized {
		handleUnauthorized(w, parsedRequest.ID, err)
		return
	}

	next.ServeHTTP(w, r)
}

// authorizeAndServe injects tool annotations from the cache, authorizes the request,
// and calls next if authorized. It handles both the unauthorized response and the
// successful serve path, so callers do not need to do either after calling this.
func authorizeAndServe(
	w http.ResponseWriter,
	r *http.Request,
	a authorizers.Authorizer,
	annotationCache *AnnotationCache,
	feature authorizers.MCPFeature,
	operation authorizers.MCPOperation,
	msgID interface{},
	toolName string,
	args map[string]interface{},
	next http.Handler,
) {
	if ann := annotationCache.Get(toolName); ann != nil {
		r = r.WithContext(authorizers.WithToolAnnotations(r.Context(), ann))
	}
	authorized, err := a.AuthorizeWithJWTClaims(r.Context(), feature, operation, toolName, args)
	if err != nil || !authorized {
		handleUnauthorized(w, msgID, err)
		return
	}
	next.ServeHTTP(w, r)
}

// handleToolsCall handles tools/call authorization, including pass-through meta-tools.
// It always fully handles the request (authorization, unauthorized response, or serving).
//
// For pass-through meta-tools (find_tool, call_tool):
//   - call_tool: authorizes the real inner tool name, decoded from the request
//     arguments exactly as dispatch decodes them, so the two cannot disagree about
//     which tool a request names. Arguments that do not decode are denied.
//   - find_tool (and other pass-through tools without a tool_name): allowed through
//     as a discovery operation with no policy check.
//
// For normal tools: injects annotations from the cache and authorizes before serving.
func handleToolsCall(
	w http.ResponseWriter,
	r *http.Request,
	a authorizers.Authorizer,
	parsedRequest *mcp.ParsedMCPRequest,
	featureOp featureOperation,
	annotationCache *AnnotationCache,
	passThroughTools map[string]struct{},
	next http.Handler,
) {
	if _, isPassThrough := passThroughTools[parsedRequest.ResourceID]; isPassThrough {
		// Decode with the same call the two call_tool dispatch sites use rather than
		// indexing the arguments map. encoding/json matches struct fields
		// case-insensitively, so a map index on "tool_name" misses a request carrying
		// "Tool_Name" that dispatch resolves and runs. Going through CallToolInput
		// also applies the nested-tool_name hoist, so the name authorized here is the
		// one that will execute.
		input, err := schema.Translate[optimizer.CallToolInput](parsedRequest.Arguments)
		if err != nil {
			// The arguments are not a decodable call_tool payload, so the tool they
			// target cannot be established. Deny rather than pass through; dispatch
			// decodes the same map with the same call and rejects it too, so no
			// legitimate invocation is lost.
			slog.Warn("denying pass-through tool call with undecodable arguments",
				"tool", parsedRequest.ResourceID, "error", err)
			handleUnauthorized(w, parsedRequest.ID, nil)
			return
		}
		if input.ToolName != "" {
			// call_tool: authorize the real backend tool name.
			authorizeAndServe(w, r, a, annotationCache,
				featureOp.Feature, featureOp.Operation,
				parsedRequest.ID, input.ToolName, input.Parameters, next)
			return
		}
		// find_tool: allow through but filter the tools list in the response so
		// callers cannot discover tools they are not authorized to call.
		if parsedRequest.ResourceID == optimizerdec.FindToolName {
			filteringWriter := NewResponseFilteringWriter(w, a, r, optimizerdec.FindToolName, annotationCache, passThroughTools)
			next.ServeHTTP(filteringWriter, r)
			if err := filteringWriter.FlushAndFilter(); err != nil {
				slog.Warn("error filtering find_tool response", "error", err)
			}
			return
		}
		// Other pass-through tools without a wrapped toolName: allow through.
		next.ServeHTTP(w, r)
		return
	}

	// Normal tool: inject annotations and authorize.
	authorizeAndServe(w, r, a, annotationCache,
		featureOp.Feature, featureOp.Operation,
		parsedRequest.ID, parsedRequest.ResourceID, parsedRequest.Arguments, next)
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

	middleware, err := CreateMiddlewareFromConfig(authzConfig, runner.GetConfig().GetName(), nil)
	if err != nil {
		return fmt.Errorf("failed to create authorization middleware: %w", err)
	}

	authzMw := &FactoryMiddleware{middleware: middleware}
	runner.AddMiddleware(config.Type, authzMw)
	return nil
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Standard JSON-RPC 2.0 reserved error codes (spec-fixed, never change). Kept
// as a local block, used by both this file and modern_envelope.go's
// writeModernError status mapping, rather than imported from mcpcompat (which
// defines equivalents like mcp.METHOD_NOT_FOUND): mcpcompat is the SDK's
// wire-protocol vocabulary, while the Modern vMCP layer already sources its
// own codes from two other places -- mcpparser.JSONRPCCodeDenied (403, shared
// with the Legacy call gate) and the classifier's app-space -3202x codes.
// Pulling these four from mcpcompat too would split one small, unchanging set
// of constants across three packages for no benefit.
const (
	jsonRPCCodeInvalidRequest = -32600
	jsonRPCCodeMethodNotFound = -32601
	jsonRPCCodeInvalidParams  = -32602
	jsonRPCCodeInternalError  = -32603
)

// dispatchModern serves a single MCP 2026-07-28 ("Modern") stateless request
// by dispatching directly to the stateless vMCP core, bypassing the SDK
// Serve/session layer entirely. classifyingHandler routes here for every
// well-formed Modern request.
//
// Because this path bypasses the SDK server, it re-homes the SDK's
// pre-dispatch authorization gate itself (see the per-method blocks below) --
// mirroring authzCallGate exactly, including its fail-open posture on
// non-authorization Check* errors. Do not add a case here without also
// deciding its gating: an ungated write would let a Cedar-denied call reach a
// backend.
func (s *Server) dispatchModern(w http.ResponseWriter, r *http.Request, parsed *mcpparser.ParsedMCPRequest) {
	// A notification (no id) MUST get 202 with no body and no dispatch, per the
	// Streamable HTTP spec's handling of a POST body containing only
	// responses/notifications. parser.go's real parse path sets IsRequest true
	// for every decoded jsonrpc2.Request -- calls AND notifications alike -- so
	// IsRequest cannot distinguish them; absent id (nil) is the actual
	// notification signal (parseMCPRequest leaves ParsedMCPRequest.ID nil when
	// the JSON-RPC id is absent).
	if parsed.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// ponytail: defensive/unreachable today -- ParsingMiddleware rejects a
	// JSON-RPC batch (leading '[') with HTTP 400 / -32600 before a
	// ParsedMCPRequest is ever built (parser.go's IsBatchRequest check, ~line
	// 119), so dispatchModern never sees one and IsBatch is hardcoded false
	// (parser.go ~line 61-65). This also closes the batch blind spot
	// call_gate.go used to document. Do not build batch parsing here; this
	// guard is just a backstop if the parser ever stops rejecting batches
	// upstream.
	if parsed.IsBatch {
		writeModernError(w, parsed.ID, jsonRPCCodeInvalidRequest, "batch requests are not supported")
		return
	}

	ctx := r.Context()
	// Sanctioned transport-boundary identity read (matches authzCallGate and
	// every Serve-path handler). ctx itself is passed unmodified into every
	// core.* call below: the stateless backend client reads forwarded headers
	// off this exact context per call, so detaching or wrapping it would
	// silently break forwarded-header backend auth.
	identity, _ := auth.IdentityFromContext(ctx)

	switch parsed.Method {
	case "tools/list":
		s.dispatchModernToolsList(ctx, w, parsed, identity)
	case "resources/list":
		s.dispatchModernResourcesList(ctx, w, parsed, identity)
	case "resources/templates/list":
		s.dispatchModernResourceTemplatesList(ctx, w, parsed, identity)
	case "prompts/list":
		s.dispatchModernPromptsList(ctx, w, parsed, identity)
	case "server/discover":
		s.dispatchModernDiscover(ctx, w, parsed, identity)
	case "tools/call":
		s.dispatchModernToolCall(ctx, w, parsed, identity)
	case "resources/read":
		s.dispatchModernResourceRead(ctx, w, parsed, identity)
	case "prompts/get":
		s.dispatchModernPromptGet(ctx, w, parsed, identity)
	case "completion/complete":
		s.dispatchModernComplete(ctx, w, parsed, identity)
	case "ping":
		// ping is deliberately ungated (unauthenticated liveness, same bucket
		// as initialize -- no Check*) and carries NEITHER resultType NOR
		// _meta.serverInfo on the wire: the SDK's ping handler returns
		// emptyResult, and both annotateServerInfo and setCompleteResultType
		// early-return/no-op for it (go-sdk server.go:1929-1945,1992). Do not
		// route this through the envelope builders above -- a bare {} is the
		// correct, spec-matching result.
		writeModernResult(w, parsed.ID, struct{}{})
	default:
		writeModernError(w, parsed.ID, jsonRPCCodeMethodNotFound, "method not found")
	}
}

// The four list-dispatch helpers below (tools/list, resources/list,
// resources/templates/list, prompts/list) always return the full
// admission-filtered set from the matching core.List* and never set the
// envelope's nextCursor (it's omitempty) -- client-facing cursor pagination
// is unimplemented, and any cursor a Modern client sends is ignored. This is
// unrelated to the aggregator's UPSTREAM cursor-following for internal
// discovery (#5851); that's a different layer.
func (s *Server) dispatchModernToolsList(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	tools, err := s.core.ListTools(ctx, identity)
	if err != nil {
		writeModernError(w, parsed.ID, jsonRPCCodeInternalError, err.Error())
		return
	}
	result, err := newModernToolsList(tools, s.config.Name, s.config.Version)
	if err != nil {
		writeModernError(w, parsed.ID, jsonRPCCodeInternalError, err.Error())
		return
	}
	writeModernResult(w, parsed.ID, result)
}

func (s *Server) dispatchModernResourcesList(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	resources, err := s.core.ListResources(ctx, identity)
	if err != nil {
		writeModernError(w, parsed.ID, jsonRPCCodeInternalError, err.Error())
		return
	}
	writeModernResult(w, parsed.ID, newModernResourcesList(resources, s.config.Name, s.config.Version))
}

func (s *Server) dispatchModernResourceTemplatesList(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	templates, err := s.core.ListResourceTemplates(ctx, identity)
	if err != nil {
		writeModernError(w, parsed.ID, jsonRPCCodeInternalError, err.Error())
		return
	}
	writeModernResult(w, parsed.ID, newModernResourceTemplatesList(templates, s.config.Name, s.config.Version))
}

func (s *Server) dispatchModernPromptsList(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	prompts, err := s.core.ListPrompts(ctx, identity)
	if err != nil {
		writeModernError(w, parsed.ID, jsonRPCCodeInternalError, err.Error())
		return
	}
	writeModernResult(w, parsed.ID, newModernPromptsList(prompts, s.config.Name, s.config.Version))
}

// dispatchModernDiscover serves server/discover, Modern's replacement for
// initialize+capability negotiation, as a post-admission capability-flags
// envelope: it calls core.Discover, which applies the same admission-filtered
// code paths the four list verbs use and collapses each to presence/absence,
// returning NO descriptor arrays -- so the response reflects only what this
// identity may reach (ListBackends's filterUnauthorized=true is the existing
// post-admission-presence precedent, core_vmcp.go:446). Like the list verbs,
// this is ungated: there is no separate Check* for discover, since it leaks
// no more than tools/list already does.
//
// This runs ONE backend fan-out per call via core.Discover (aggregatedView is
// uncached, so calling ListTools/ListResources/ListResourceTemplates/
// ListPrompts independently here used to cost four -- and those four weren't
// even a consistent snapshot of the aggregated view). A single fan-out per
// request is fine for now, but a probe the spec expects to be cheap across
// requests too. ponytail: no cross-request cache; add a short-TTL
// per-identity capability cache only if profiling shows the per-request
// fan-out cost matters (#5761, tracked separately, not blocking here).
func (s *Server) dispatchModernDiscover(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	caps, err := s.core.Discover(ctx, identity)
	if err != nil {
		writeModernError(w, parsed.ID, jsonRPCCodeInternalError, err.Error())
		return
	}
	result := newModernDiscover(
		caps.HasTools, caps.HasResources, caps.HasResourceTemplates, caps.HasPrompts,
		s.config.Name, s.config.Version,
	)
	writeModernResult(w, parsed.ID, result)
}

// dispatchModernToolCall re-homes authzCallGate's tools/call branch plus the
// post-dispatch TOCTOU reclassification (see writeModernDispatchError).
func (s *Server) dispatchModernToolCall(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	if hasNonObjectArguments(parsed.Params) {
		writeModernError(w, parsed.ID, jsonRPCCodeInvalidParams, "arguments must be an object")
		return
	}
	if s.authzGateEnabled && gateDenied(ctx, parsed.Method,
		s.core.CheckToolCall(ctx, identity, parsed.ResourceID, parsed.Arguments)) {
		writeModernDenied(w, parsed.ID, vmcp.DenyMessageToolCall)
		return
	}
	result, err := s.core.CallTool(ctx, identity, parsed.ResourceID, parsed.Arguments, parsed.Meta)
	if err != nil {
		writeModernDispatchError(w, parsed.ID, vmcp.DenyMessageToolCall, err)
		return
	}
	// Label the audit backend on the success path only. The stateless dispatcher
	// cannot pre-resolve the backend (routing is core-internal), so unlike the
	// Legacy handlers -- which set the label before the call and thus keep it on
	// backend-call failures -- a Modern backend-call failure audits without
	// backend_name. Accepted: the event still records the tool and the outcome.
	// (Same applies to resources/read and prompts/get below.)
	if result.BackendID != "" {
		if bi, ok := audit.BackendInfoFromContext(ctx); ok && bi != nil {
			bi.BackendName = s.backendDisplayName(ctx, result.BackendID)
		}
	}
	writeModernResult(w, parsed.ID, newModernCallToolResult(result, s.config.Name, s.config.Version))
}

// dispatchModernResourceRead re-homes authzCallGate's resources/read branch
// plus the post-dispatch TOCTOU reclassification (see writeModernDispatchError).
func (s *Server) dispatchModernResourceRead(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	if s.authzGateEnabled && gateDenied(ctx, parsed.Method,
		s.core.CheckResourceRead(ctx, identity, parsed.ResourceID)) {
		writeModernDenied(w, parsed.ID, vmcp.DenyMessageResourceRead)
		return
	}
	result, err := s.core.ReadResource(ctx, identity, parsed.ResourceID)
	if err != nil {
		writeModernDispatchError(w, parsed.ID, vmcp.DenyMessageResourceRead, err)
		return
	}
	if result.BackendID != "" {
		if bi, ok := audit.BackendInfoFromContext(ctx); ok && bi != nil {
			bi.BackendName = s.backendDisplayName(ctx, result.BackendID)
		}
	}
	writeModernResult(w, parsed.ID, newModernReadResourceResult(result, s.config.Name, s.config.Version))
}

// dispatchModernPromptGet re-homes authzCallGate's prompts/get branch plus the
// post-dispatch TOCTOU reclassification (see writeModernDispatchError).
func (s *Server) dispatchModernPromptGet(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	// hasNonObjectArguments only checks object-ness, not per-value typing: the
	// SDK's GetPromptParams.Arguments is map[string]string, so it also rejects
	// a non-string argument VALUE (e.g. {"x":123}) at decode. Modern accepts
	// that shape -- narrower parity (object-shape only), not a behavior gap
	// worth closing here.
	if hasNonObjectArguments(parsed.Params) {
		writeModernError(w, parsed.ID, jsonRPCCodeInvalidParams, "arguments must be an object")
		return
	}
	if s.authzGateEnabled && gateDenied(ctx, parsed.Method,
		s.core.CheckPromptGet(ctx, identity, parsed.ResourceID)) {
		writeModernDenied(w, parsed.ID, vmcp.DenyMessagePromptGet)
		return
	}
	result, err := s.core.GetPrompt(ctx, identity, parsed.ResourceID, parsed.Arguments)
	if err != nil {
		writeModernDispatchError(w, parsed.ID, vmcp.DenyMessagePromptGet, err)
		return
	}
	if result.BackendID != "" {
		if bi, ok := audit.BackendInfoFromContext(ctx); ok && bi != nil {
			bi.BackendName = s.backendDisplayName(ctx, result.BackendID)
		}
	}
	writeModernResult(w, parsed.ID, newModernGetPromptResult(result, s.config.Name, s.config.Version))
}

// modernCompleteWireParams is the completion/complete request params, decoded
// directly from parsed.Params. It mirrors go-sdk's CompleteParams/
// CompleteReference/CompleteParamsArgument/CompleteContext field-for-field
// (protocol.go:577-648 in go-sdk@v1.7.0-pre.3) rather than reusing an SDK
// type: mcp-go v1.6.1 (ToolHive's import) predates the Modern completion
// shapes, so there is nothing to reuse. It stays local rather than adding
// JSON tags to vmcp.CompletionRef -- the domain type intentionally carries no
// wire coupling (anti-pattern #5, no mcp-go types crossing the core boundary).
type modernCompleteWireParams struct {
	Ref *struct {
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
		URI  string `json:"uri,omitempty"`
	} `json:"ref"`
	Argument struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"argument"`
	Context *struct {
		Arguments map[string]string `json:"arguments,omitempty"`
	} `json:"context,omitempty"`
}

// dispatchModernComplete serves completion/complete. Unlike tools/call,
// resources/read, and prompts/get, there is no pre-dispatch Check* gate here
// -- call_gate.go documents this as a conscious choice: core.Complete
// authorizes the underlying prompt/resource ref at dispatch (the same
// get/read decision GetPrompt/ReadResource enforce), so gating on the wire
// would just duplicate that check ahead of an admission decision that isn't
// argument-conditional the way the gate's fast path assumes. An admission
// denial from core.Complete still reclassifies to 403 via
// writeModernDispatchError, exactly like the three gated verbs.
//
// This handler does not label the audit BackendInfo the way the three gated
// verbs above do: the Legacy coreCompletionHandler never set backend_name
// either, so completion is a pre-existing gap on both paths, not something
// introduced here.
func (s *Server) dispatchModernComplete(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	var params modernCompleteWireParams
	if err := json.Unmarshal(parsed.Params, &params); err != nil || params.Ref == nil || params.Ref.Type == "" {
		writeModernError(w, parsed.ID, jsonRPCCodeInvalidParams, "invalid completion/complete params: missing ref")
		return
	}
	if params.Argument.Name == "" {
		writeModernError(w, parsed.ID, jsonRPCCodeInvalidParams, "invalid completion/complete params: missing argument.name")
		return
	}
	ref := vmcp.CompletionRef{Type: params.Ref.Type, Name: params.Ref.Name, URI: params.Ref.URI}

	var contextArgs map[string]string
	if params.Context != nil {
		contextArgs = params.Context.Arguments
	}

	result, err := s.core.Complete(ctx, identity, ref, params.Argument.Name, params.Argument.Value, contextArgs)
	if err != nil {
		writeModernDispatchError(w, parsed.ID, completionDenyMessage(ref.Type), err)
		return
	}
	writeModernResult(w, parsed.ID, newModernComplete(result, s.config.Name, s.config.Version))
}

// hasNonObjectArguments reports whether parsed.Params carries an "arguments"
// field that is present but NOT a JSON object (e.g. a string or array).
//
// The parser (handleNamedResourceMethod, parser.go:307) type-asserts
// paramsMap["arguments"].(map[string]interface{}) and silently drops the
// value to nil on a mismatch -- indistinguishable from "arguments absent" by
// the time ParsedMCPRequest.Arguments is built. The SDK path also rejects
// this shape before authz/the core is ever reached (coreToolHandler
// shape-checks req.Params.Arguments in serve_handlers.go; prompts/get gets
// the same pre-dispatch rejection for free because mcpcompat's
// GetPromptParams.Arguments is a concrete map[string]string, so a non-object
// value fails JSON decode before the handler runs) -- this function matches
// that TIMING, not the SDK's wire shape: the SDK's tools/call rejection
// surfaces as a 200 IsError tool result (conversion path), whereas this is a
// genuine JSON-RPC -32602, consistent with Modern's other protocol-level
// rejections (-32600/-32601). Modern must reject the same shape here, on the
// raw params, before that type information is lost -- otherwise a non-object
// arguments value silently authorizes and dispatches as a no-args call,
// diverging from the SDK path and potentially changing an
// argument-conditional authz decision. An absent or explicit-null "arguments"
// is a legitimate no-args call and is not rejected.
func hasNonObjectArguments(params json.RawMessage) bool {
	var raw struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &raw); err != nil || raw.Arguments == nil {
		return false
	}
	var obj map[string]any
	return json.Unmarshal(raw.Arguments, &obj) != nil
}

// gateDenied runs the PRE-dispatch admission classification for a gated
// method's Check* result, mirroring authzCallGate exactly: only an
// errors.Is(checkErr, vmcp.ErrAuthorizationFailed) denial returns true. Any
// other error is infrastructure (aggregation/backend plumbing), so the gate
// fails OPEN -- it logs and admits, rather than converting an authorizer
// outage into a false 403. This WARN is the only operational signal of that
// outage admitting traffic; do not remove it.
func gateDenied(ctx context.Context, method string, checkErr error) bool {
	if checkErr == nil {
		return false
	}
	if errors.Is(checkErr, vmcp.ErrAuthorizationFailed) {
		return true
	}
	slog.WarnContext(ctx, "vmcp authz gate: non-authorization error, admitting request",
		"method", method, "error", checkErr)
	return false
}

// writeModernDispatchError classifies a POST-dispatch error from
// CallTool/ReadResource/GetPrompt. Check* and the real call each re-aggregate
// independently (documented "aggregates twice" on CheckToolCall), so a
// concurrent backend health flip, cache refresh, or annotation change
// (TOCTOU) can have Check* allow and the call itself deny. That denial MUST
// still surface as 403 + denyMsg -- the same as the pre-dispatch gate -- so
// the audit middleware logs it as "denied" rather than "failure"; it is
// therefore tested FIRST, before falling through to the generic internal
// error.
//
// The -32603 message reuses err.Error() verbatim. This matches the SDK path's
// existing posture rather than inventing a new one: conversion.ErrorToToolResult's
// generic branch, and the resources/read/prompts/get Serve handlers
// (serve_handlers.go), already surface the raw error text for a non-coded,
// non-authz error. Re-sanitizing here would just diverge from what the SDK
// path already exposes for the identical failure.
func writeModernDispatchError(w http.ResponseWriter, id any, denyMsg string, err error) {
	if errors.Is(err, vmcp.ErrAuthorizationFailed) {
		writeModernDenied(w, id, denyMsg)
		return
	}
	writeModernError(w, id, jsonRPCCodeInternalError, err.Error())
}

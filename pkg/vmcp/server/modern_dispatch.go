// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	// Defensive/unreachable today -- ParsingMiddleware rejects a
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
	case methodServerDiscover:
		s.dispatchModernDiscover(ctx, w, parsed, identity)
	case "tools/call":
		s.dispatchModernToolCall(ctx, w, parsed, identity)
	case "resources/read":
		s.dispatchModernResourceRead(ctx, w, parsed, identity)
	case "prompts/get":
		s.dispatchModernPromptGet(ctx, w, parsed, identity)
	case "completion/complete":
		s.dispatchModernComplete(ctx, w, parsed, identity)
	case methodSubscriptionsListen:
		// Ungated, same bucket as the list verbs and discover -- see
		// dispatchModernSubscriptionsListen for why, and for the one future
		// change that would require gating it.
		s.dispatchModernSubscriptionsListen(ctx, w, parsed, identity)
	case "ping":
		// ping does NOT exist in 2026-07-28: `ping` appears nowhere in
		// schema/draft/schema.ts, and go-sdk lists it among the methods removed
		// for this revision (server.go:1880), answering -32601. So answering it
		// at all is deliberate LENIENCY toward a client that pings anyway, not
		// spec conformance -- an earlier version of this comment claimed the
		// latter, which was wrong twice over: the method is gone, and the bare {}
		// below omits `resultType`, which schema.ts's Result MUSTs on every
		// result ("Servers implementing this protocol version MUST include this
		// field").
		//
		// Kept as-is rather than switched to -32601 because that is a behavior
		// change to a pre-existing path, out of scope for the change that
		// introduced this comment fix. It is inert in practice: a go-sdk Modern
		// client never pings (startKeepalive sits past an early return), so
		// nothing observable depends on either answer today. If you are here to
		// tighten it, -32601 is the conformant answer.
		//
		// It is ungated (unauthenticated liveness, same bucket as initialize --
		// no Check*). Do not route it through the envelope builders above: they
		// would stamp resultType and _meta.serverInfo, which the Legacy SDK path
		// does not do for ping either (annotateServerInfo and
		// setCompleteResultType both early-return for it, go-sdk
		// server.go:1929-1945,1992), so the bare {} at least keeps the two paths
		// answering alike.
		writeModernResult(w, parsed.ID, struct{}{})
	default:
		writeModernError(w, parsed.ID, jsonRPCCodeMethodNotFound, "method not found")
	}
}

// The four list-dispatch helpers below (tools/list, resources/list,
// resources/templates/list, prompts/list) each page the full
// admission-filtered set from the matching core.List* through paginateModern,
// emitting a nextCursor while items remain. The cursor is stateless keyset
// pagination over the aggregated ordering -- see modern_pagination.go for the
// wire contract and why a self-describing cursor is what a sessionless
// revision requires. This is unrelated to the aggregator's UPSTREAM
// cursor-following for internal discovery (#5851); that's a different layer.
//
// A List*/Discover failure logs the full error server-side and returns a
// generic -32603 message to the client (writeModernListError below): unlike
// the call/read/get verbs, these errors come from aggregation and routing
// plumbing (backend IDs, upstream addressing), and security.md forbids
// leaking that detail to callers. An invalid CURSOR is the exception: it is
// caller input rather than a server-side fault, so writeModernPageError maps
// it to -32602 instead.
func (s *Server) dispatchModernToolsList(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	tools, err := s.core.ListTools(ctx, identity)
	if err != nil {
		writeModernListError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	cursor, err := modernRequestCursor(parsed.Params)
	if err != nil {
		writeModernPageError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	page, next, err := paginateModern(tools, func(t vmcp.Tool) string { return t.Name },
		cursorKindTools, cursor)
	if err != nil {
		writeModernPageError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	result, err := newModernToolsList(page, s.config.Name, s.config.Version, next)
	if err != nil {
		writeModernListError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	writeModernResult(w, parsed.ID, result)
}

func (s *Server) dispatchModernResourcesList(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	resources, err := s.core.ListResources(ctx, identity)
	if err != nil {
		writeModernListError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	cursor, err := modernRequestCursor(parsed.Params)
	if err != nil {
		writeModernPageError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	page, next, err := paginateModern(resources, func(r vmcp.Resource) string { return r.URI },
		cursorKindResources, cursor)
	if err != nil {
		writeModernPageError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	writeModernResult(w, parsed.ID, newModernResourcesList(page, s.config.Name, s.config.Version, next))
}

func (s *Server) dispatchModernResourceTemplatesList(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	templates, err := s.core.ListResourceTemplates(ctx, identity)
	if err != nil {
		writeModernListError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	cursor, err := modernRequestCursor(parsed.Params)
	if err != nil {
		writeModernPageError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	page, next, err := paginateModern(templates, func(t vmcp.ResourceTemplate) string { return t.URITemplate },
		cursorKindResourceTemplates, cursor)
	if err != nil {
		writeModernPageError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	writeModernResult(w, parsed.ID, newModernResourceTemplatesList(page, s.config.Name, s.config.Version, next))
}

func (s *Server) dispatchModernPromptsList(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	prompts, err := s.core.ListPrompts(ctx, identity)
	if err != nil {
		writeModernListError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	cursor, err := modernRequestCursor(parsed.Params)
	if err != nil {
		writeModernPageError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	page, next, err := paginateModern(prompts, func(p vmcp.Prompt) string { return p.Name },
		cursorKindPrompts, cursor)
	if err != nil {
		writeModernPageError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	writeModernResult(w, parsed.ID, newModernPromptsList(page, s.config.Name, s.config.Version, next))
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
// requests too. There is no cross-request cache; add a short-TTL per-identity
// capability cache only if profiling shows the per-request fan-out cost
// matters (#5761, tracked separately, not blocking here).
func (s *Server) dispatchModernDiscover(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	caps, err := s.core.Discover(ctx, identity)
	if err != nil {
		writeModernListError(ctx, w, parsed.ID, parsed.Method, err)
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
	// The refusal recorder classifies a backend tool that demanded mid-call
	// elicitation/sampling with no downstream session to forward it to — see
	// modern_capability_refusal.go and writeModernCallFailure.
	ctx, refusal := withCapabilityRefusalRecorder(ctx)
	result, err := s.core.CallTool(ctx, identity, parsed.ResourceID, parsed.Arguments, parsed.Meta)
	if err != nil {
		writeModernCallFailure(w, parsed, refusal, vmcp.DenyMessageToolCall, err)
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
	ctx, refusal := withCapabilityRefusalRecorder(ctx)
	result, err := s.core.ReadResource(ctx, identity, parsed.ResourceID)
	if err != nil {
		writeModernCallFailure(w, parsed, refusal, vmcp.DenyMessageResourceRead, err)
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
	ctx, refusal := withCapabilityRefusalRecorder(ctx)
	result, err := s.core.GetPrompt(ctx, identity, parsed.ResourceID, parsed.Arguments)
	if err != nil {
		writeModernCallFailure(w, parsed, refusal, vmcp.DenyMessagePromptGet, err)
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
// type: mcpcompat/mcp (ToolHive's import, now built on go-sdk v1.7.0-pre.3)
// carries an equivalent CompleteParams, but its Ref field is untyped (any), so
// there is no strongly-typed Modern reference shape to decode into directly.
// It stays local rather than adding JSON tags to vmcp.CompletionRef -- the
// domain type intentionally carries no wire coupling (anti-pattern #5, no
// mcp-go types crossing the core boundary).
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
// other error falls through to the WARN+admit branch below, but only
// CheckToolCall can actually produce one: it re-aggregates
// (c.aggregatedView) and returns that error unwrapped on failure, so a
// tools/call gate can fail OPEN on an aggregation/backend-plumbing outage.
// CheckResourceRead and CheckPromptGet need no aggregated view and always
// wrap their error as vmcp.ErrAuthorizationFailed (core_checks.go), so their
// gates never take this fail-open path in practice. This WARN is the only
// operational signal of that fail-open outage admitting traffic; do not
// remove it.
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

// writeModernListError logs a List*/Discover failure server-side with the
// full error and writes a generic -32603 message to the client. Unlike
// writeModernDispatchError's call/read/get verbs, these errors surface
// aggregation and routing plumbing (backend IDs, upstream addressing), and
// security.md forbids exposing that detail to callers.
func writeModernListError(ctx context.Context, w http.ResponseWriter, id any, method string, err error) {
	slog.ErrorContext(ctx, "vmcp modern dispatch: list/discover failed", "method", method, "error", err)
	writeModernError(w, id, jsonRPCCodeInternalError, "internal error")
}

// writeModernPageError classifies a paginateModern failure. An invalid cursor is
// bad caller input, so it gets -32602 -- matching the spec's "handle invalid
// cursors gracefully" and go-sdk, which returns ErrInvalidParams for a cursor it
// cannot decode. The message deliberately does not say WHY the cursor was
// rejected: clients must treat cursors as opaque, so describing the internal
// encoding would invite them to construct one. Anything else here is an encode
// failure, i.e. a server-side fault, and falls through to -32603.
func writeModernPageError(ctx context.Context, w http.ResponseWriter, id any, method string, err error) {
	if errors.Is(err, errInvalidModernCursor) {
		writeModernError(w, id, jsonRPCCodeInvalidParams, "invalid cursor")
		return
	}
	writeModernListError(ctx, w, id, method, err)
}

// writeModernCallFailure classifies a POST-dispatch error from the three call
// verbs (tools/call, resources/read, prompts/get), layering the mid-call
// capability-refusal case on top of writeModernDispatchError's authz/generic
// classification:
//
//   - Authorization denials keep absolute priority (403 + denyMsg, so the
//     audit middleware still logs "denied") — a refusal can never mask one.
//   - A recorded refusal with the capability NOT declared in the request's
//     _meta clientCapabilities is the draft schema's
//     MissingRequiredClientCapabilityError: code -32021 with
//     data.requiredCapabilities naming what the server needed (an actionable
//     code a client can surface, where -32603 is opaque) — served at HTTP
//     200, a deliberate, documented deviation from the spec-mandated 400;
//     see writeModernMissingCapability.
//   - A recorded refusal with the capability DECLARED is deliberately NOT
//     -32021: the client did declare it, so blaming the client would be
//     wrong, and declaring the capability on a retry cannot help. The spec's answer for a declared
//     capability is resultType "input_required" (multi-round retrieval,
//     SEP-2322), which this dispatcher does not implement — and the
//     2026-07-28 error vocabulary has no "operation not supported" code, so
//     there is no conformant code for this case at all (see the "unavailable
//     to Modern clients" limitation in
//     docs/arch/10-virtual-mcp-architecture.md). It stays -32603, with a
//     vMCP-owned message that names the real cause instead of the backend's
//     laundered error string.
//
// Do not fold this into writeModernDispatchError: completion/complete also
// uses that helper but never installs a refusal recorder (SEP-2322 does not
// extend to completion/complete, so an input_required-shaped failure there
// has no spec-defined classification).
func writeModernCallFailure(
	w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, refusal *capabilityRefusalRecorder,
	denyMsg string, err error,
) {
	if capName := refusal.refused(); capName != "" && !errors.Is(err, vmcp.ErrAuthorizationFailed) {
		if !modernClientDeclaredCapability(parsed.Meta, capName) {
			writeModernMissingCapability(w, parsed.ID, capName)
			return
		}
		writeModernError(w, parsed.ID, jsonRPCCodeInternalError, fmt.Sprintf(
			"backend requires the %q client capability mid-call; serving it on protocol %s requires "+
				"multi-round retrieval (SEP-2322), which this server does not implement",
			capName, mcpparser.MCPVersionModern))
		return
	}
	writeModernDispatchError(w, parsed.ID, denyMsg, err)
}

// writeModernMissingCapability writes the MissingRequiredClientCapabilityError
// (-32021, data.requiredCapabilities typed as a ClientCapabilities object) at
// HTTP 200.
//
// STATUS DEVIATION, deliberate and load-bearing: SEP-2575 MUSTs HTTP 400 for
// this error, but go-sdk's streamable client (v1.7.0-pre.3) treats any
// non-transient 4xx as a CONNECTION failure, not a call failure — its
// transient set is only 500/502/503/504/429, so a 400 falls through
// checkResponse to fail(), which closes the session permanently. One refused
// elicitation would kill every subsequent request on the client. The JSON-RPC
// body below stays fully conformant (which is what a non-go-sdk client
// parses); only the transport status deviates. Tracked upstream as
// go-sdk#1117 — revisit the 200 when that is fixed.
//
// This is written directly rather than through mcpparser.WriteClassificationError
// on purpose: that helper hard-codes HTTP 400, which is correct for its other
// callers (pre-dispatch classification rejections a client never retries on a
// live session) and must not be changed for this one.
//
// The message names both the capability AND the gateway limitation, so a
// caller that reacts to -32021 by declaring the capability on a retry
// learns immediately that doing so will not help here (the declared case is served by
// writeModernCallFailure's -32603 branch, not by MRTR).
func writeModernMissingCapability(w http.ResponseWriter, id any, capName string) {
	writeModernEnvelope(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code": mcpparser.CodeMissingClientCapability,
			"message": fmt.Sprintf(
				"this tool requires the %q client capability mid-call, which the request did not declare; "+
					"note that declaring it will not help on protocol %s — serving mid-call capability "+
					"requests needs multi-round retrieval (SEP-2322), which this server does not implement",
				capName, mcpparser.MCPVersionModern),
			"data": map[string]any{
				"requiredCapabilities": map[string]any{capName: map[string]any{}},
			},
		},
	})
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
// A domain error that carries its own stable JSON-RPC code and data
// (mcpparser.CodedError — e.g. the rate limiter's -32029 with
// data.retryAfterSeconds) is written with that code rather than laundered
// into -32603. This is the Modern counterpart of the SDK path's
// conversion.ErrorToToolResult, whose CodedError branch preserves the same
// code/data in an IsError tool result's structuredContent because the SDK
// tool-handler seam cannot emit a custom JSON-RPC error object. This
// dispatcher owns the envelope, so it emits the real thing (mirroring how
// #6061 classified mid-call capability refusals as -32021 instead of
// -32603). Authorization denials are still tested first: a coded error can
// never mask a denial's 403.
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
	var coded mcpparser.CodedError
	if errors.As(err, &coded) {
		writeModernCodedError(w, id, err, coded)
		return
	}
	writeModernError(w, id, jsonRPCCodeInternalError, err.Error())
}

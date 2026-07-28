// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// authzCallGate builds the pre-dispatch CallGate consulted once per POST on the
// Streamable HTTP transport, BEFORE session validation and BEFORE the message
// reaches the SDK. It re-runs the core admission decision for the gated methods so
// a Cedar-denied direct tools/call, resources/read, or prompts/get is rejected on
// the wire as HTTP 403 + JSON-RPC error code 403 — instead of the SDK's 200/"not
// found" (a filtered tool) or the tool-result 200/IsError fallback.
//
// It reads only context the host middleware already populated (the parsed request
// and the authenticated identity), matching the seam's contract; it never mutates
// context and never logs the identity. The gate is installed only when authz is
// configured (see Handler), so this closure runs solely on authorized deployments.
//
// The gate and the subsequent dispatch (core.CallTool/ReadResource/GetPrompt) both
// authorize, and they invoke the SAME core admission helper (Check* and Call*/Read*/
// Get* share one extracted decision — see pkg/vmcp/core), so the DECISION LOGIC
// cannot diverge. The gate is the wire-level representation of that decision (it
// turns a denial into 403 before dispatch); the call-path check remains as
// defense-in-depth for embedders that bypass this transport.
//
// For argument-conditional policies (tools/call), the gated decision and the enforced
// decision derive from the SAME parse by construction: the gate authorizes on
// parsed.Arguments (pkg/mcp's transport parse) and dispatch prefers that same map via
// gateParsedArgs before re-authorizing and forwarding (see serve_handlers.go). Where
// no matching parse exists (batch, embedders bypassing this transport, method/tool
// mismatch), dispatch falls back to the SDK decode and makes a single decision on that
// single map — so no path can produce an allow-then-deny split between gate and call.
func (s *Server) authzCallGate() server.CallGate {
	return func(ctx context.Context, _ *http.Request) *server.Denial {
		// An unparsable body or a batch leaves no ParsedMCPRequest: admit and let the
		// SDK handle it. The batch blind spot matches the pre-existing single-server
		// parity gap (cross-ref #5745); it is not widened here.
		parsed := mcpparser.GetParsedMCPRequest(ctx)
		if parsed == nil {
			return nil
		}

		// Sanctioned transport-boundary identity read (same as the Serve handlers):
		// the core takes identity as an explicit parameter, so the gate resolves it
		// once here from the context the auth middleware populated. A nil identity is
		// anonymous — the core admission seam decides what that may do.
		identity, _ := auth.IdentityFromContext(ctx)

		var err error
		var message string
		switch parsed.Method {
		case "tools/call":
			err = s.core.CheckToolCall(ctx, identity, parsed.ResourceID, parsed.Arguments)
			message = vmcp.DenyMessageToolCall
		case "resources/read":
			err = s.core.CheckResourceRead(ctx, identity, parsed.ResourceID)
			message = vmcp.DenyMessageResourceRead
		case "prompts/get":
			err = s.core.CheckPromptGet(ctx, identity, parsed.ResourceID)
			message = vmcp.DenyMessagePromptGet
		default:
			// Non-gated method (initialize, tools/list, ping, ...): admit unchanged.
			//
			// This default is deliberately fail-OPEN, unlike pkg/authz's
			// MCPMethodToFeatureOperation ("methods not in this map are denied by
			// default"). The gate mirrors the core admission seam. For methods with
			// NO admission decision at all (elicitation/create, sampling/createMessage,
			// tasks/*), denying here would diverge from the core rather than enforce a
			// real policy.
			//
			// Two methods DO carry a core admission decision but are intentionally
			// NOT wire-gated here — this is a conscious choice, not an oversight:
			//   - completion/complete: core.Complete authorizes the underlying prompt/
			//     resource ref at dispatch.
			//   - resources/subscribe: core validates the URI via LookupResource at
			//     dispatch.
			// Both are enforced at dispatch instead of on the wire because they are not
			// argument-conditional in the way the gate's parsed-args fast path assumes,
			// and their denials already surface through the core's error mapping. To
			// gate a NEW verb on the wire: add a Check* method on core.VMCP for it and a
			// case above — do NOT flip this default to deny, which would reject protocol
			// methods (initialize/ping/list/notifications) that must always pass.
			return nil
		}

		if err == nil {
			return nil
		}

		// Only an authorization denial (or a fail-closed authorizer error, already
		// classified as ErrAuthorizationFailed inside Check*) becomes a 403. Any other
		// error is infrastructure (aggregation/backend plumbing): admit so the call
		// path hits the same failure and surfaces it through the existing mapping —
		// the gate must not convert infra faults into 403s.
		if !errors.Is(err, vmcp.ErrAuthorizationFailed) {
			slog.WarnContext(ctx, "vmcp authz gate: non-authorization error, admitting request",
				"method", parsed.Method, "error", err)
			return nil
		}

		// HTTPStatus left zero ⇒ the shim writes HTTP 403 (see server.Denial).
		return &server.Denial{Code: mcpparser.JSONRPCCodeDenied, Message: message}
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"fmt"
	"log/slog"

	"golang.org/x/exp/jsonrpc2"
)

// listChangedNotificationMethods is the set of server->client notification
// methods that describe a server-wide capability change (not tied to any
// particular request or subscription), so delivering one copy to every
// connected session's standalone stream is correct and safe.
var listChangedNotificationMethods = map[string]bool{
	"notifications/tools/list_changed":     true,
	"notifications/resources/list_changed": true,
	"notifications/prompts/list_changed":   true,
}

// dispatchResponses routes container messages arriving on responseCh to the
// appropriate destination based on their concrete JSON-RPC type:
//   - *jsonrpc2.Response routes to the waiter correlated by its composite ID
//     (see routeResponseToWaiter).
//   - *jsonrpc2.Request with no valid ID is a server->client notification,
//     routed by method (see routeNotification).
//   - *jsonrpc2.Request with a valid ID is a server->client REQUEST (e.g.
//     sampling/createMessage, elicitation/create). It is rejected back to the
//     backend rather than forwarded to any client (see
//     rejectServerRequestToBackend).
func (p *HTTPProxy) dispatchResponses() {
	for {
		select {
		case <-p.shutdownCh:
			return
		case msg := <-p.responseCh:
			switch m := msg.(type) {
			case *jsonrpc2.Response:
				p.routeResponseToWaiter(m)
			case *jsonrpc2.Request:
				if !m.ID.IsValid() {
					p.routeNotification(m)
					continue
				}
				p.rejectServerRequestToBackend(m)
			default:
				slog.Warn("received invalid message that is not a valid response",
					"type", fmt.Sprintf("%T", msg))
			}
		}
	}
}

// routeNotification delivers a server->client notification (m.ID invalid) to
// its correct destination based on m.Method, per the shared-backend routing
// design (see #5744 and docs/arch/03-transport-architecture.md):
//
//   - */list_changed (listChangedNotificationMethods): GLOBAL, no
//     session-specific payload -- broadcast to every connected session's
//     standalone GET stream.
//   - notifications/progress: request-scoped -- delivered ONLY to the
//     originating request's POST-SSE stream, via routeProgress's
//     progress-token correlation. Never broadcast or fanned out.
//   - notifications/resources/updated: subscription-scoped -- delivered ONLY
//     to sessions currently subscribed to the notification's uri, via
//     routeResourceUpdated.
//   - notifications/message (logging): SECURE-DROP. The shared backend is one
//     process serving every session; a log message it emits carries no
//     session attribution, so there is no way to deliver it to the session
//     that caused it without either guessing or leaking it to every other
//     connected session. Dropping it is the only safe choice until per-session
//     backends (see the vMCP architecture) are in the request path.
//   - anything else: dropped, logged at Debug.
func (p *HTTPProxy) routeNotification(m *jsonrpc2.Request) {
	switch {
	case listChangedNotificationMethods[m.Method]:
		p.serverStreams.broadcast(m)
	case m.Method == "notifications/progress":
		p.routeProgress(m)
	case m.Method == "notifications/resources/updated":
		p.routeResourceUpdated(m)
	case m.Method == "notifications/message":
		// SECURE-DROP: shared-backend log content is unattributable to any
		// one session and forwarding it (to all sessions, or a guessed one)
		// would leak cross-session information. Deferred to per-session
		// backend work (#5744).
		slog.Debug("dropping logging notification; shared backend cannot attribute it to a session")
	default:
		//nolint:gosec // G706: method is from parsed JSON-RPC request
		slog.Debug("dropping unrecognized server->client notification", "method", m.Method)
	}
}

// routeProgress delivers a notifications/progress message to the single
// in-flight POST-SSE request that asked for it, correlated by the raw
// (proxy-minted) progressToken the backend echoes back (see
// rewriteMetaProgressToken and handleSingleRequestSSE's setupProgressRouting).
// If the token is missing or not currently registered (the request already
// completed, or the token was never ours), the notification is dropped: there
// is no session to deliver it to without guessing, and guessing risks
// cross-session leakage.
func (p *HTTPProxy) routeProgress(m *jsonrpc2.Request) {
	ptGlobal, ok := extractStringParam(m.Params, "progressToken")
	if !ok || ptGlobal == "" {
		slog.Debug("dropping progress notification with no progressToken")
		return
	}

	route, found := p.routing.lookupProgressToken(ptGlobal)
	if !found {
		slog.Debug("dropping progress notification for unknown or expired progress token")
		return
	}

	restored, err := rewriteRequestParam(m, "progressToken", route.originalToken)
	if err != nil {
		slog.Error("failed to restore client progressToken on progress notification", "error", err)
		return
	}

	select {
	case route.deliver <- restored:
	default:
		slog.Warn("progress delivery channel full; dropping progress notification")
	}
}

// routeResourceUpdated delivers a notifications/resources/updated message to
// every session currently subscribed to its uri (see
// sessionRouter.subscribersOf), and only those sessions. A notification for a
// uri with no recorded subscriber is dropped.
func (p *HTTPProxy) routeResourceUpdated(m *jsonrpc2.Request) {
	uri, ok := extractStringParam(m.Params, "uri")
	if !ok || uri == "" {
		slog.Debug("dropping resources/updated notification with no uri")
		return
	}

	subscribers := p.routing.subscribersOf(uri)
	if len(subscribers) == 0 {
		//nolint:gosec // G706: uri is from parsed JSON-RPC notification
		slog.Debug("dropping resources/updated notification with no subscribers", "uri", uri)
		return
	}

	p.serverStreams.deliverToMany(subscribers, m)
}

// rejectServerRequestToBackend responds to a server-initiated request (a
// *jsonrpc2.Request with a valid ID, e.g. sampling/createMessage or
// elicitation/create) with a JSON-RPC error, written back to the BACKEND (not
// to any client) via SendMessageToDestination, so the backend's own blocking
// call unblocks with an error instead of hanging until its own timeout.
//
// The shared-backend streamable proxy has no way to route a client's reply
// back to the correct originating backend call when multiple sessions share
// one backend process, so it cannot forward this to any client without either
// guessing (misdelivery) or broadcasting (cross-session leakage). Per-session
// backends (see the vMCP architecture, docs/arch/10-virtual-mcp-architecture.md)
// are the correct place to support server->client requests; see #5744.
func (p *HTTPProxy) rejectServerRequestToBackend(m *jsonrpc2.Request) {
	resp := &jsonrpc2.Response{
		ID: m.ID,
		Error: jsonrpc2.NewError(-32601, fmt.Sprintf(
			"server-initiated %s is not supported by the shared streamable proxy; requires a per-session backend deployment",
			m.Method,
		)),
	}
	if err := p.SendMessageToDestination(resp); err != nil {
		//nolint:gosec // G706: method is from parsed JSON-RPC request
		slog.Error("failed to send server-request-rejected error back to backend", "method", m.Method, "error", err)
	}
}

// routeResponseToWaiter delivers resp to the waiter channel correlated by its
// composite request ID (compositeKey(sessID, idKey)), if one is registered.
func (p *HTTPProxy) routeResponseToWaiter(resp *jsonrpc2.Response) {
	if !resp.ID.IsValid() {
		slog.Warn("received invalid message that is not a valid response",
			"type", fmt.Sprintf("%T", resp))
		return
	}

	rawID := resp.ID.Raw()
	// Composite-only routing: responses must carry composite ID (sessID|idKey)
	sID, ok := rawID.(string)
	if !ok {
		slog.Warn("non-string response id (expected composite string); dropping",
			"raw_id", fmt.Sprintf("%v", rawID))
		return
	}

	chVal, ok := p.waiters.Load(sID)
	if !ok {
		slog.Warn("no waiter found for composite key; dropping", "composite_key", sID)
		return
	}
	ch, ok := chVal.(chan jsonrpc2.Message)
	if !ok {
		slog.Warn("no waiter found for composite key; dropping", "composite_key", sID)
		return
	}

	select {
	case ch <- resp:
	default:
		slog.Warn("waiter channel full; dropping response", "composite_key", sID)
	}
}

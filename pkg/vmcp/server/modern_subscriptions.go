// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// This file implements MCP 2026-07-28 ("Modern") subscriptions/listen, the
// revision's ONLY server->client push channel. Modern removed the standalone
// HTTP GET stream, so a client that wants unsolicited notifications opens one
// of these instead.
//
// WHY IT EXISTS EVEN THOUGH vMCP HAS NOTHING TO PUSH. Without a handler for
// this method a go-sdk v1.7 client cannot talk to vMCP at all: its Connect is
// Modern-first, and once server/discover succeeds it opens a listen stream
// whenever any list-changed handler is registered -- which mcpcompat's
// Initialize does unconditionally (installNotificationHandlers). A -32601 there
// fails Connect outright and tears the session down. So this handler is what
// stands between a Modern client and a usable session; it is not a facility
// nobody reaches.
//
// WHAT IT HONORS: nothing, today, and it says so on the wire. The honored set is
// computed by intersecting the client's requested notification types against
// newModernCapabilities -- the same builder server/discover publishes -- every
// push-related flag of which is deliberately false (see its doc comment for why,
// per flag). An empty honored set is the spec's own way to say "I support none
// of these", not a stub: SEP-2575 specifies the acknowledgement as carrying
// "the subset of the requested notifications the server has agreed to honor",
// and requires unsupported types to be omitted from it. go-sdk's reference
// server does exactly this, filtering the requested set through its own
// capabilities (allowedSubscriptions, server.go:1254+ in go-sdk@v1.7.0-pre.3).
//
// The distinction that matters: this is a truthful NEGATIVE declaration, not an
// empty success. It never reports a subscription as established and then
// silently drops notifications -- it enumerates, in the acknowledgement, that
// nothing is honored. Real delivery is tracked separately (#5743) and requires
// vMCP to first start ADVERTISING a push capability in newModernCapabilities;
// until then there is deliberately nothing for this stream to carry.
//
// Known limitation worth stating plainly: go-sdk's client ignores the
// acknowledgement (callSubscriptionsAckHandler returns (nil, nil),
// client.go:1457-1459), so against that client this honest declaration is not
// observable in behavior. It is correct on the wire regardless, and the
// alternative -- holding a stream open forever delivering nothing -- is strictly
// worse.

// Modern subscription method and notification names, plus the reserved _meta
// key deliveries are tagged with.
//
// All three are NORMATIVE in schema/draft/schema.ts -- do not weaken them to
// "SDK convention". The relevant declarations:
//
//   - SubscriptionsListenResultMeta requires (non-optional)
//     "io.modelcontextprotocol/subscriptionId", whose value is "the JSON-RPC ID
//     of the subscriptions/listen request that opened the stream (and equals
//     this response's id)" -- so request-id-as-subscription-identity is
//     specified, not invented.
//   - SubscriptionsAcknowledgedNotification: "This notification MUST be the
//     first message the server sends carrying the subscription's ID ... The
//     server MUST NOT send any notification on the subscription before
//     acknowledging it."
//   - NotificationMetaObject: "The server MUST include this key on every
//     notification delivered via a subscriptions/listen stream." That third one
//     binds a DELIVERY implementation rather than this handler (which delivers
//     nothing); it is restated at the #5743 hand-off below so it is not lost.
//
// go-sdk agrees and can be read as a cross-check (MetaKeySubscriptionID at
// protocol.go:2377-2379; server.go:1187-1250), but the schema is the authority.
// An earlier version of this comment attributed these to the SDK because the
// SEP-2575 prose does not restate them -- the schema does, and it governs.
const (
	methodSubscriptionsListen      = "subscriptions/listen"
	notificationSubscriptionsAcked = "notifications/subscriptions/acknowledged"
	modernSubscriptionIDKey        = "io.modelcontextprotocol/subscriptionId"
)

// notificationSubscriptions is the per-type, per-URI opt-in set carried by both
// subscriptions/listen params and the acknowledgement. It mirrors go-sdk's
// NotificationSubscriptions field-for-field (protocol.go:2070-2082); mcpcompat
// does not re-export the type, so like the rest of the Modern envelope this is a
// hand-rolled parallel serializer.
//
// These four are the ENTIRE subscribable universe under SEP-2575. Progress,
// logging, elicitation, and sampling are structurally absent -- they are not
// notification types a client can opt into here, which is why this channel
// cannot carry them however it is implemented.
type notificationSubscriptions struct {
	ToolsListChanged      bool     `json:"toolsListChanged,omitempty"`
	PromptsListChanged    bool     `json:"promptsListChanged,omitempty"`
	ResourcesListChanged  bool     `json:"resourcesListChanged,omitempty"`
	ResourceSubscriptions []string `json:"resourceSubscriptions,omitempty"`
}

// isEmpty reports whether nothing at all is subscribed.
func (n notificationSubscriptions) isEmpty() bool {
	return !n.ToolsListChanged && !n.PromptsListChanged &&
		!n.ResourcesListChanged && len(n.ResourceSubscriptions) == 0
}

// subscriptionsListenParams is the decoded subscriptions/listen request params.
// Notifications is a pointer so an absent field is distinguishable from an
// explicit empty object: the spec makes it REQUIRED, and go-sdk rejects a nil
// one with invalid-params (server.go:1193-1195), so vMCP must too.
type subscriptionsListenParams struct {
	Notifications *notificationSubscriptions `json:"notifications"`
}

// subscriptionsAcknowledgedParams is the acknowledgement notification's params:
// the honored subset, plus the subscription id this stream is keyed by.
type subscriptionsAcknowledgedParams struct {
	Notifications notificationSubscriptions `json:"notifications"`
	Meta          map[string]any            `json:"_meta"`
}

// subscriptionsListenResult is the response to subscriptions/listen, signalling
// that the subscription ended gracefully. Mirrors go-sdk's
// SubscriptionsListenResult (protocol.go:2110-2118): resultType "complete" plus
// a subscriptionId-tagged _meta.
type subscriptionsListenResult struct {
	ResultType string         `json:"resultType"`
	Meta       map[string]any `json:"_meta"`
}

// dispatchModernSubscriptionsListen serves subscriptions/listen.
//
// It is UNGATED, in the same bucket as the four list verbs and server/discover:
// there is no Check* for it because it performs no write, reaches no backend,
// and discloses nothing beyond what server/discover already does -- the honored
// set it returns is derived from core.Discover, which is itself
// admission-filtered per identity. Should this ever begin honoring a
// subscription (i.e. actually delivering resource updates), revisit that: a
// per-URI resource subscription that delivers content WOULD need
// CheckResourceRead-equivalent gating per URI.
//
// The response is an SSE stream, not a single JSON body, because the protocol
// requires two messages on it: the acknowledgement notification first, then the
// result. go-sdk forces SSE for this method for the same reason
// (streamable.go:1645-1650). The stream is closed as soon as both are written
// because the honored set is empty and there is consequently nothing to keep it
// open for -- matching go-sdk's reference server, which blocks on the request
// context only when it has agreed to honor at least one subscription
// (server.go:1246-1250).
func (s *Server) dispatchModernSubscriptionsListen(
	ctx context.Context, w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, identity *auth.Identity,
) {
	var params subscriptionsListenParams
	if err := json.Unmarshal(parsed.Params, &params); err != nil || params.Notifications == nil {
		writeModernError(w, parsed.ID, jsonRPCCodeInvalidParams,
			"invalid subscriptions/listen params: missing required 'notifications' field")
		return
	}

	// Resolve what this identity may reach, then shape it through the same
	// capability builder server/discover publishes.
	//
	// The pre-check first: core.Discover is an un-rate-limited backend fan-out
	// (ratelimit/decorator.go meters CallTool only), and while no push capability
	// is advertised its result cannot change the answer -- every possible
	// DiscoverCapabilities combination yields the same empty honored set, because
	// the has* booleans only decide whether a capability POINTER is non-nil, never
	// the listChanged/subscribe flags inside it. Calling it anyway meant N no-op
	// requests cost N fan-outs, and that got worse once list verbs began paging.
	// So probe the ceiling -- everything present -- and skip the fan-out when even
	// that honors nothing. This is a pure optimisation: it cannot change the
	// response, only what it costs.
	ceiling := honoredSubscriptions(*params.Notifications, newModernCapabilities(true, true, true, true))
	honored := ceiling
	if !ceiling.isEmpty() {
		caps, err := s.core.Discover(ctx, identity)
		if err != nil {
			writeModernListError(ctx, w, parsed.ID, parsed.Method, err)
			return
		}
		advertised := newModernCapabilities(
			caps.HasTools, caps.HasResources, caps.HasResourceTemplates, caps.HasPrompts)
		honored = honoredSubscriptions(*params.Notifications, advertised)
	}

	// The subscription id is the listen request's own JSON-RPC id. Modern has no
	// sessions, so this -- not an Mcp-Session-Id -- is what a delivery
	// implementation would key streams by.
	subscriptionMeta := map[string]any{modernSubscriptionIDKey: parsed.ID}

	// Build first: a marshal failure here is still reportable as -32603, because
	// nothing has been written yet.
	frames, err := buildModernListenFrames(parsed.ID, honored, subscriptionMeta)
	if err != nil {
		writeModernListError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}

	if err := writeModernListenStream(w, frames); err != nil {
		// Only reachable mid-stream: the frames were built successfully and the
		// status line is already sent, so there is no way to turn this into a
		// JSON-RPC error. Log it and let the connection drop.
		slog.DebugContext(ctx, "vmcp modern dispatch: subscriptions/listen stream ended early",
			"error", err)
		return
	}

	if !honored.isEmpty() {
		// Unreachable while newModernCapabilities advertises no push capability,
		// and deliberately not built out here: keeping a stream open is only half
		// of honoring a subscription, and the delivery half does not exist yet.
		// Whoever flips a capability flag owns adding both (#5743) -- this WARN
		// exists so that, if a flag is flipped without it, the gap is loud in the
		// logs instead of presenting as a silently idle client subscription.
		//
		// Hand-off note for that work, since it is a MUST and easy to miss: per
		// schema.ts's NotificationMetaObject, the server MUST include
		// io.modelcontextprotocol/subscriptionId on EVERY notification delivered
		// via a subscriptions/listen stream -- not just on the acknowledgement
		// this handler already tags. A delivery implementation that pushes a bare
		// notifications/tools/list_changed onto the stream is non-conformant even
		// though the subscription itself was acknowledged correctly.
		//
		// Logged by COUNT, never by URI: resourceSubscriptions is client-supplied
		// and only length-capped, so echoing the array into the log sink on an
		// unmetered verb would let a caller choose how much it writes per request.
		slog.WarnContext(ctx, "vmcp modern dispatch: subscriptions/listen honored a subscription "+
			"but vMCP has no delivery mechanism; notifications will not arrive",
			"subscription_id", parsed.ID,
			"tools_list_changed", honored.ToolsListChanged,
			"prompts_list_changed", honored.PromptsListChanged,
			"resources_list_changed", honored.ResourcesListChanged,
			"resource_subscription_count", len(honored.ResourceSubscriptions))
	}
}

// honoredSubscriptions intersects a client's requested notification set with
// what vMCP advertises, returning only the types it can actually honor.
//
// This is the whole of the per-type AND per-URI opt-in filtering SEP-2575
// mandates ("the server MUST NOT send notification types the client has not
// explicitly requested"): a type survives only if the client asked for it AND
// the matching capability is advertised. Because it takes capabilities as a
// parameter rather than reading a package-level constant, it needs no edit when
// a capability flips -- and it is independently testable across combinations
// the live advertisement cannot currently produce.
//
// resourceSubscriptions is filtered as a unit rather than per-URI: the
// Subscribe capability is server-wide, so it either gates all requested URIs or
// none. Per-URI admission (dropping URIs this identity may not read) belongs
// with delivery, since it is only observable once updates actually flow.
func honoredSubscriptions(
	want notificationSubscriptions, advertised mcp.ServerCapabilities,
) notificationSubscriptions {
	var honored notificationSubscriptions
	if want.ToolsListChanged && advertised.Tools != nil && advertised.Tools.ListChanged {
		honored.ToolsListChanged = true
	}
	if want.PromptsListChanged && advertised.Prompts != nil && advertised.Prompts.ListChanged {
		honored.PromptsListChanged = true
	}
	if want.ResourcesListChanged && advertised.Resources != nil && advertised.Resources.ListChanged {
		honored.ResourcesListChanged = true
	}
	if len(want.ResourceSubscriptions) > 0 && advertised.Resources != nil && advertised.Resources.Subscribe {
		honored.ResourceSubscriptions = want.ResourceSubscriptions
	}
	return honored
}

// buildModernListenFrames marshals the two frames the listen response carries:
// the mandatory initial acknowledgement notification, then the terminating
// result.
//
// It is separate from writeModernListenStream so that everything which can fail
// BEFORE any byte is written stays on a path that can still produce a proper
// JSON-RPC error. Folding these into the writer meant a marshal failure returned
// after headers were nominally "committed" when in fact nothing had been written
// at all, so the client received a bare HTTP 200 with an empty body for a request
// carrying an id. Now the caller maps a build failure to -32603, matching
// writeModernEnvelope's own build-then-write ordering, and only a genuine
// mid-stream write failure is unreportable.
func buildModernListenFrames(
	id any, honored notificationSubscriptions, subscriptionMeta map[string]any,
) ([][]byte, error) {
	ack, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  notificationSubscriptionsAcked,
		"params": subscriptionsAcknowledgedParams{
			Notifications: honored,
			Meta:          subscriptionMeta,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling subscriptions acknowledgement: %w", err)
	}
	result, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": subscriptionsListenResult{
			ResultType: modernResultTypeComplete,
			Meta:       subscriptionMeta,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling subscriptions listen result: %w", err)
	}
	return [][]byte{ack, result}, nil
}

// writeModernListenStream writes pre-marshalled frames as an SSE response and
// closes it. Everything it can return is a mid-stream failure -- the frames are
// already built and the status line is already gone -- so its error is only ever
// loggable, never reportable to the client.
func writeModernListenStream(w http.ResponseWriter, frames [][]byte) error {
	// http.ResponseController rather than a w.(http.Flusher) type assertion.
	//
	// The assertion would not establish what it appears to. Every ResponseWriter
	// wrapper in vMCP's middleware chain -- audit/auditor.go,
	// telemetry/middleware.go, bodylimit/middleware.go, mcp/tool_filter.go --
	// defines Flush() UNCONDITIONALLY and forwards only if the writer it wraps
	// supports it. So the assertion succeeds whenever a wrapper is present,
	// whatever the writer underneath can do, and says nothing about whether a
	// flush will actually reach the socket. ResponseController unwraps the chain
	// (via Unwrap) and reports a real answer.
	//
	// The tradeoff, stated because it costs something: there is no longer a
	// PRE-write capability check. ResponseController exposes no "can you flush"
	// predicate -- the only way to find out is to call Flush, which implicitly
	// commits the header -- so an unflushable writer now surfaces as a mid-stream
	// error rather than a -32603 before anything is written. That is deliberate:
	// the old assertion answered early but WRONGLY, and an early wrong answer on a
	// path no deployment can reach is worse than a correct late one. If Go ever
	// grows a flushability predicate, move this back ahead of WriteHeader.
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	// no-store as well as no-cache: no-cache still permits a shared cache to
	// STORE the response and revalidate, which for a per-identity stream is wrong.
	w.Header().Set("Cache-Control", "no-cache, no-store, no-transform, private")
	w.Header().Set("Connection", "keep-alive")
	// Draft Streamable HTTP, "When initiating an SSE stream, servers SHOULD
	// include the X-Accel-Buffering: no header", so a reverse proxy (nginx and
	// friends) does not accumulate events before forwarding them. This is the
	// only SSE-emitting path in the codebase, so there is nowhere else to copy
	// the header from.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	for _, frame := range frames {
		// Frame format is load-bearing, not cosmetic: an SSE event is terminated
		// by a BLANK line, so the trailing "\n\n" is what makes a parser dispatch
		// the event. Emitting a single "\n" leaves both frames inside one
		// never-dispatched event -- the client connects and then silently receives
		// nothing, which is exactly the mutation that escaped both this package's
		// tests and the real-client integration suite.
		//
		// The "event: message" line is NOT mandated: the draft Streamable HTTP
		// page specifies the content type and the stream lifecycle but never an
		// SSE event name, and it explicitly drops resumability ("Resumable SSE
		// streams via Last-Event-ID are not supported"), which is why no "id:"
		// field is emitted either. The name is kept for symmetry with go-sdk's
		// framing; do not add an "id:" to match it.
		if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", frame); err != nil {
			return fmt.Errorf("writing subscriptions/listen frame: %w", err)
		}
		// A writer that genuinely cannot flush would buffer both frames until the
		// handler returned, which for a stream read incrementally is
		// indistinguishable from a hang. Report it rather than hanging.
		if err := rc.Flush(); err != nil {
			return fmt.Errorf("flushing subscriptions/listen frame: %w", err)
		}
	}
	return nil
}

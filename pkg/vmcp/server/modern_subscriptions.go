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
// Provenance, since it differs per constant: the two method/notification names
// and the per-type opt-in shape are specified in SEP-2575 prose. The
// subscriptionId key and the convention of using the listen request's own
// JSON-RPC id as the subscription identity are NOT in the spec text -- they are
// taken from go-sdk@v1.7.0-pre.3 (MetaKeySubscriptionID at protocol.go:2377-2379;
// server.go:1187-1250 keys subscriptions by the request id and stamps it into
// both the acknowledgement and the final result). That is the reference
// implementation rather than normative prose, so treat it as the de facto wire
// contract and re-check it when the SEP lands.
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
	// capability builder server/discover publishes. Costs one backend fan-out,
	// like dispatchModernDiscover; see its note on the absent cross-request cache.
	caps, err := s.core.Discover(ctx, identity)
	if err != nil {
		writeModernListError(ctx, w, parsed.ID, parsed.Method, err)
		return
	}
	advertised := newModernCapabilities(caps.HasTools, caps.HasResources, caps.HasResourceTemplates, caps.HasPrompts)
	honored := honoredSubscriptions(*params.Notifications, advertised)

	// The subscription id is the listen request's own JSON-RPC id. Modern has no
	// sessions, so this -- not an Mcp-Session-Id -- is what a delivery
	// implementation would key streams by.
	subscriptionMeta := map[string]any{modernSubscriptionIDKey: parsed.ID}

	if err := writeModernListenStream(w, parsed.ID, honored, subscriptionMeta); err != nil {
		// The client hung up or the stream could not be flushed. Nothing is
		// recoverable at this point (headers are already committed), so log and
		// return rather than attempting an error envelope on a dead stream.
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
		slog.WarnContext(ctx, "vmcp modern dispatch: subscriptions/listen honored a subscription "+
			"but vMCP has no delivery mechanism; notifications will not arrive",
			"subscription_id", parsed.ID, "honored", honored)
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

// writeModernListenStream writes the two-frame SSE response: the mandatory
// initial acknowledgement notification, then the terminating result.
//
// Both frames are marshalled before any header is written, so a marshal failure
// cannot leave a half-written response -- the same build-then-write ordering
// writeModernEnvelope uses.
func writeModernListenStream(
	w http.ResponseWriter, id any, honored notificationSubscriptions, subscriptionMeta map[string]any,
) error {
	ack, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  notificationSubscriptionsAcked,
		"params": subscriptionsAcknowledgedParams{
			Notifications: honored,
			Meta:          subscriptionMeta,
		},
	})
	if err != nil {
		return fmt.Errorf("marshalling subscriptions acknowledgement: %w", err)
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
		return fmt.Errorf("marshalling subscriptions listen result: %w", err)
	}

	// A ResponseWriter that cannot flush would buffer both frames until the
	// handler returned, which for a stream the client reads incrementally is
	// indistinguishable from a hang. Fail before committing headers instead.
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("response writer does not support streaming")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	for _, frame := range [][]byte{ack, result} {
		if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", frame); err != nil {
			return fmt.Errorf("writing subscriptions/listen frame: %w", err)
		}
		flusher.Flush()
	}
	return nil
}

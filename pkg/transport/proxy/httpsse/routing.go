// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httpsse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
)

// Routed IDs carry the originating session inside each request's wire ID so
// the shared stdio backend's echoed response can be delivered to that session
// alone (GHSA-wm2j-ch74-276r). Wire format: "<sessionID>|n:<int64>" or
// "<sessionID>|s:<string>". Session IDs are proxy-minted UUIDs and never
// contain the separator, so cutting at the first "|" is unambiguous even when
// the client's own string ID contains one; the type tag lets the original ID
// be restored with its exact type. Routing is stateless because the
// destination, the session's SSE stream, is already indexed by session ID in
// liveSSESessions. Design and the full dispatch table:
// docs/arch/03-transport-architecture.md, "Server->Client Routing (Legacy SSE)".

const (
	routedIDSeparator = "|"
	routedIDNumberTag = "n:"
	routedIDStringTag = "s:"

	// methodCancelled is the client->server notification that names an
	// in-flight request by id in params.requestId. The backend only knows
	// that request by its routed id, so the proxy rewrites the parameter too
	// (see rewriteCancelledRequestID).
	methodCancelled = "notifications/cancelled"

	// methodPing is the liveness check either MCP party may issue. A backend
	// ping needs no session attribution, so the proxy answers it itself (see
	// answerBackendPing) instead of rejecting it like other server requests.
	methodPing = "ping"

	// maxWarnOnceKeys bounds the set of distinct events warnOnce remembers.
	// Real backends emit a handful of notification methods; the cap only
	// matters for a backend inventing method names.
	maxWarnOnceKeys = 64
)

// listChangedNotificationMethods is the set of server->client notification
// methods that describe a server-wide capability change with no
// session-specific payload. Delivering one copy to every connected session is
// correct and safe; every other notification the shared backend emits is
// unattributable and is dropped (see routeNotification). Mirrors the
// streamable proxy's set of the same name.
var listChangedNotificationMethods = map[string]bool{
	"notifications/tools/list_changed":     true,
	"notifications/resources/list_changed": true,
	"notifications/prompts/list_changed":   true,
}

// encodeRoutedID returns the wire ID to send upstream for a client request
// with id, issued on sessionID. id must be valid (a call, not a notification).
// It fails rather than guess if id carries a type jsonrpc2 does not construct
// today, so a library change surfaces as a rejected request instead of a
// silently retyped response the client would never match.
func encodeRoutedID(sessionID string, id jsonrpc2.ID) (string, error) {
	switch v := id.Raw().(type) {
	case int64:
		return sessionID + routedIDSeparator + routedIDNumberTag + strconv.FormatInt(v, 10), nil
	case string:
		return sessionID + routedIDSeparator + routedIDStringTag + v, nil
	default:
		return "", fmt.Errorf("unsupported JSON-RPC id type %T", v)
	}
}

// decodeRoutedID parses a wire ID the backend echoed back. It reports ok=false
// for any ID the proxy did not mint via encodeRoutedID (a non-string ID, a
// missing separator or type tag, or a malformed number), so callers drop such
// responses rather than guess a destination.
func decodeRoutedID(id jsonrpc2.ID) (sessionID string, original jsonrpc2.ID, ok bool) {
	raw, isString := id.Raw().(string)
	if !isString {
		return "", jsonrpc2.ID{}, false
	}
	sessionID, rest, found := strings.Cut(raw, routedIDSeparator)
	if !found || sessionID == "" {
		return "", jsonrpc2.ID{}, false
	}
	switch {
	case strings.HasPrefix(rest, routedIDNumberTag):
		n, err := strconv.ParseInt(strings.TrimPrefix(rest, routedIDNumberTag), 10, 64)
		if err != nil {
			return "", jsonrpc2.ID{}, false
		}
		return sessionID, jsonrpc2.Int64ID(n), true
	case strings.HasPrefix(rest, routedIDStringTag):
		return sessionID, jsonrpc2.StringID(strings.TrimPrefix(rest, routedIDStringTag)), true
	default:
		return "", jsonrpc2.ID{}, false
	}
}

// exactRequestID returns the id of the call in body exactly as the client
// sent it. jsonrpc2.DecodeMessage parses numeric ids through float64, which
// rounds integers above 2^53 and truncates fractions, so the restored
// response id could differ from the request's and the client would never
// match it. String ids are already exact and are returned as decoded. A
// fractional or out-of-range number is rejected: JSON-RPC ids are integers or
// strings, and a truncated id could not be matched by the client either.
func exactRequestID(body []byte, decoded jsonrpc2.ID) (jsonrpc2.ID, error) {
	if _, isString := decoded.Raw().(string); isString {
		return decoded, nil
	}
	var envelope struct {
		ID json.Number `json:"id"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&envelope); err != nil {
		return jsonrpc2.ID{}, fmt.Errorf("failed to read request id: %w", err)
	}
	n, err := envelope.ID.Int64()
	if err != nil {
		return jsonrpc2.ID{}, fmt.Errorf("request id must be an integer or a string: %w", err)
	}
	return jsonrpc2.Int64ID(n), nil
}

// rewriteCancelledRequestID returns a copy of the notifications/cancelled
// notification n whose params.requestId is the routed id the backend was sent
// for that request on sessionID. It never mutates n: params are freshly
// decoded from n.Params. A cancellation with no requestId, non-object params,
// or a requestId that is not a JSON-RPC id (string or integer) is returned as
// is for the backend to ignore; the proxy does not police MCP semantics.
func rewriteCancelledRequestID(sessionID string, n *jsonrpc2.Request) (*jsonrpc2.Request, error) {
	if len(n.Params) == 0 {
		return n, nil
	}
	params := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(n.Params))
	dec.UseNumber() // keep integer request ids exact; float64 would round above 2^53
	if err := dec.Decode(&params); err != nil {
		// Params are valid JSON (DecodeMessage already parsed the envelope)
		// but not an object, e.g. positional params; nothing to route on.
		return n, nil
	}
	id, ok := requestIDParam(params["requestId"])
	if !ok {
		return n, nil
	}
	routed, err := encodeRoutedID(sessionID, id)
	if err != nil {
		return nil, err
	}
	params["requestId"] = routed
	data, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to encode cancellation params: %w", err)
	}
	return &jsonrpc2.Request{Method: n.Method, Params: data}, nil
}

// requestIDParam converts a decoded params.requestId into a jsonrpc2.ID. Only
// strings and integers are JSON-RPC ids; anything else reports ok=false.
func requestIDParam(v any) (jsonrpc2.ID, bool) {
	switch id := v.(type) {
	case string:
		return jsonrpc2.StringID(id), true
	case json.Number:
		n, err := id.Int64()
		if err != nil {
			return jsonrpc2.ID{}, false
		}
		return jsonrpc2.Int64ID(n), true
	default:
		return jsonrpc2.ID{}, false
	}
}

// routeResponse delivers a backend response to the one session whose routed
// ID it carries, with the client's original ID restored. Anything else is
// dropped at Debug: an ID the proxy never minted (a health-check ping echo, or
// a backend inventing IDs), or a session that disconnected before its
// response arrived. Dropped responses are never queued or broadcast.
func (p *HTTPSSEProxy) routeResponse(resp *jsonrpc2.Response) error {
	sessionID, originalID, ok := decodeRoutedID(resp.ID)
	if !ok {
		slog.Debug("dropping response whose id carries no session route")
		return nil
	}
	sess, live := p.liveSession(sessionID)
	if !live {
		slog.Debug("dropping response for a session that is no longer connected", "session_id", sessionID)
		return nil
	}

	restored := &jsonrpc2.Response{ID: originalID, Result: resp.Result, Error: resp.Error}
	data, err := jsonrpc2.EncodeMessage(restored)
	if err != nil {
		return fmt.Errorf("failed to encode JSON-RPC response: %w", err)
	}
	// A disconnected session or a full channel drops the response, matching
	// sendSSEEvent's per-client policy.
	if err := sess.SendMessage(ssecommon.NewSSEMessage("message", string(data)).ToSSEString()); err != nil {
		slog.Debug("failed to deliver response to session", "session_id", sessionID, "error", err)
	}
	return nil
}

// routeNotification handles a server->client notification (a request with no
// ID). list_changed notifications (listChangedNotificationMethods) are
// broadcast to every live session as a payload-free notification rebuilt from
// the method alone: the spec allows params._meta on them, and anything the
// backend put there could belong to one session. With no session connected
// they reach nobody, as there is no replay buffer. Every other notification
// is dropped: the shared backend cannot say which session caused a notifications/progress,
// notifications/message, or notifications/resources/updated, and forwarding
// it to all sessions, or a guessed one, would leak one session's activity to
// another. Routing progress by a proxy-minted token, as the streamable proxy
// does, is a follow-up. The first drop of each method is logged at Warn so
// the missing feature is diagnosable; later drops at Debug.
func (p *HTTPSSEProxy) routeNotification(n *jsonrpc2.Request) error {
	if !listChangedNotificationMethods[n.Method] {
		p.warnOnce("drop:"+n.Method,
			"dropping server notification the shared SSE proxy cannot attribute to a session; "+
				"use streamable-http proxy mode for per-session delivery", "method", n.Method)
		return nil
	}

	stripped, err := jsonrpc2.NewNotification(n.Method, nil)
	if err != nil {
		return fmt.Errorf("failed to build JSON-RPC notification: %w", err)
	}
	data, err := jsonrpc2.EncodeMessage(stripped)
	if err != nil {
		return fmt.Errorf("failed to encode JSON-RPC notification: %w", err)
	}
	return p.sendSSEEvent(ssecommon.NewSSEMessage("message", string(data)))
}

// answerBackendPing replies to a backend's ping with an empty result, written
// back to the backend. A ping is a liveness check with no session-specific
// content, and MCP requires the receiver to answer promptly; rejecting it
// like other server-initiated requests would read as a failed check.
func (p *HTTPSSEProxy) answerBackendPing(req *jsonrpc2.Request) error {
	resp := &jsonrpc2.Response{ID: req.ID, Result: json.RawMessage("{}")}
	if err := p.SendMessageToDestination(resp); err != nil {
		return fmt.Errorf("failed to answer backend ping: %w", err)
	}
	return nil
}

// rejectServerRequest answers a server-initiated request other than ping (a
// request with a valid ID, e.g. sampling/createMessage or elicitation/create) with a JSON-RPC
// error written back to the backend, so its blocking call fails fast instead
// of hanging. The shared proxy has no way to pick which session should answer
// without guessing or broadcasting, and a request emitted by a session that
// has since disconnected would otherwise land on whoever remains connected.
// Same policy and error code as the streamable proxy's rejectServerRequestToBackend.
func (p *HTTPSSEProxy) rejectServerRequest(req *jsonrpc2.Request) error {
	p.warnOnce("reject:"+req.Method,
		"rejecting server-initiated request; the shared SSE proxy cannot attribute it to a session", "method", req.Method)
	resp := &jsonrpc2.Response{
		ID: req.ID,
		Error: jsonrpc2.NewError(-32601, fmt.Sprintf(
			"server-initiated %s is not supported by the shared SSE proxy; requires a per-session backend deployment",
			req.Method,
		)),
	}
	if err := p.SendMessageToDestination(resp); err != nil {
		return fmt.Errorf("failed to reject server-initiated %s: %w", req.Method, err)
	}
	return nil
}

// warnOnce logs the first occurrence of key at Warn, so an operator can see
// that a fail-closed drop or rejection is happening, and every later
// occurrence at Debug, so a chatty backend cannot flood the log.
func (p *HTTPSSEProxy) warnOnce(key, msg string, args ...any) {
	if p.firstOccurrence(key) {
		slog.Warn(msg, args...)
		return
	}
	slog.Debug(msg, args...)
}

// firstOccurrence reports whether key has not been seen before and records
// it. Once maxWarnOnceKeys distinct keys are recorded, every new key reports
// false, so a backend inventing method names cannot grow the set.
func (p *HTTPSSEProxy) firstOccurrence(key string) bool {
	p.warnedMutex.Lock()
	defer p.warnedMutex.Unlock()
	if _, seen := p.warnedKeys[key]; seen || len(p.warnedKeys) >= maxWarnOnceKeys {
		return false
	}
	p.warnedKeys[key] = struct{}{}
	return true
}

// liveSession returns the live SSE session for sessionID on this instance.
func (p *HTTPSSEProxy) liveSession(sessionID string) (*session.SSESession, bool) {
	val, ok := p.liveSSESessions.Load(sessionID)
	if !ok {
		return nil, false
	}
	sess, ok := val.(*session.SSESession)
	return sess, ok
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httpsse

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// waitTimeout bounds every blocking receive in these tests.
const waitTimeout = 5 * time.Second

// harness is a started proxy whose auth middleware maps the Authorization
// header verbatim to the subject claim, so each distinct token is an
// independently authenticated principal owning its own sessions. The test
// plays the shared stdio backend by reading proxy.messageCh and calling
// ForwardResponseToClients.
type harness struct {
	t     *testing.T
	proxy *HTTPSSEProxy
	base  string
	http  *http.Client
}

// sseClient is one principal's live SSE connection.
type sseClient struct {
	token     string
	sessionID string
	endpoint  string
	messages  <-chan string
	close     func()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	authenticate := types.NamedMiddleware{Name: "test-auth", Function: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := r.Header.Get("Authorization")
			if token == "" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			caller := &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": "issuer", "sub": token}},
				Token:         token,
			}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), caller)))
		})
	}}
	proxy := NewHTTPSSEProxy("127.0.0.1", 0, false, nil, []types.NamedMiddleware{authenticate})
	require.NoError(t, proxy.Start(t.Context()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		require.NoError(t, proxy.Stop(ctx))
	})

	// A private transport keeps another parallel test's server shutdown from
	// tearing down this test's idle connections.
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	return &harness{t: t, proxy: proxy, base: "http://" + proxy.server.Addr, http: &http.Client{Transport: transport}}
}

// connect opens /sse as the given principal, waits for the endpoint event,
// and streams every subsequent `event: message` payload on messages.
func (h *harness) connect(token string) sseClient {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodGet, h.base+"/sse", nil)
	require.NoError(h.t, err)
	req.Header.Set("Authorization", token)
	resp, err := h.http.Do(req)
	require.NoError(h.t, err)
	h.t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(h.t, http.StatusOK, resp.StatusCode)

	endpointCh := make(chan string, 1)
	messages := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		event := ""
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if event == "endpoint" {
					endpointCh <- data
				} else {
					messages <- data
				}
			}
		}
	}()

	select {
	case endpoint := <-endpointCh:
		if strings.HasPrefix(endpoint, "/") {
			endpoint = h.base + endpoint
		}
		u, err := url.Parse(endpoint)
		require.NoError(h.t, err)
		sessionID := u.Query().Get("session_id")
		require.NotEmpty(h.t, sessionID)
		return sseClient{
			token:     token,
			sessionID: sessionID,
			endpoint:  endpoint,
			messages:  messages,
			close:     func() { _ = resp.Body.Close() },
		}
	case <-time.After(waitTimeout):
		h.t.Fatal("no endpoint event received")
		return sseClient{}
	}
}

// post sends a raw JSON-RPC body to the client's own endpoint as its
// principal and requires the proxy to accept it.
func (h *harness) post(c sseClient, body string) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodPost, c.endpoint, strings.NewReader(body))
	require.NoError(h.t, err)
	req.Header.Set("Authorization", c.token)
	resp, err := h.http.Do(req)
	require.NoError(h.t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(h.t, resp.Body.Close())
	require.Equal(h.t, http.StatusAccepted, resp.StatusCode)
}

// backendMessage returns the next message the proxy forwarded to the backend.
func (h *harness) backendMessage() jsonrpc2.Message {
	h.t.Helper()
	select {
	case msg := <-h.proxy.messageCh:
		return msg
	case <-time.After(waitTimeout):
		h.t.Fatal("message never reached the backend")
		return nil
	}
}

// backendRequest is backendMessage narrowed to a *jsonrpc2.Request.
func (h *harness) backendRequest() *jsonrpc2.Request {
	h.t.Helper()
	msg := h.backendMessage()
	req, ok := msg.(*jsonrpc2.Request)
	require.True(h.t, ok, "backend expected a request, got %T", msg)
	return req
}

// respond echoes the backend's response for req with the given result,
// exactly as the stdio transport hands it to the proxy.
func (h *harness) respond(req *jsonrpc2.Request, result any) {
	h.t.Helper()
	resp, err := jsonrpc2.NewResponse(req.ID, result, nil)
	require.NoError(h.t, err)
	require.NoError(h.t, h.proxy.ForwardResponseToClients(h.t.Context(), resp))
}

// expectNext requires the next message on c's stream to contain want, and
// that the proxy's routed-ID prefix for c's own session never reaches c.
func (h *harness) expectNext(c sseClient, want string) string {
	h.t.Helper()
	select {
	case msg := <-c.messages:
		require.Contains(h.t, msg, want)
		require.NotContains(h.t, msg, c.sessionID+routedIDSeparator, "routed id must be restored before delivery")
		return msg
	case <-time.After(waitTimeout):
		h.t.Fatalf("expected a message containing %q on the SSE stream, got none", want)
		return ""
	}
}

// expectOnlySentinel proves nothing was delivered to c: it routes a sentinel
// response to c's own session and requires it to be the very next message.
// Per-session SSE streams are FIFO, so anything leaked earlier would arrive
// first, deterministically, with no timing window.
func (h *harness) expectOnlySentinel(c sseClient) {
	h.t.Helper()
	routed, err := encodeRoutedID(c.sessionID, jsonrpc2.StringID("sentinel"))
	require.NoError(h.t, err)
	resp, err := jsonrpc2.NewResponse(jsonrpc2.StringID(routed), "sentinel", nil)
	require.NoError(h.t, err)
	require.NoError(h.t, h.proxy.ForwardResponseToClients(h.t.Context(), resp))
	h.expectNext(c, `"id":"sentinel"`)
}

// TestResponseReachesOnlyOriginatingSession is the regression test for
// GHSA-wm2j-ch74-276r: two independently authenticated principals each hold a
// legitimate session, and a backend response to one must never appear on the
// other's stream.
func TestResponseReachesOnlyOriginatingSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	victim := h.connect("victim")
	attacker := h.connect("attacker")

	h.post(victim, `{"jsonrpc":"2.0","id":8426,"method":"tools/call","params":{"name":"secret"}}`)
	h.respond(h.backendRequest(), map[string]any{"content": "VICTIM_PRIVATE_RESULT_8426"})

	got := h.expectNext(victim, "VICTIM_PRIVATE_RESULT_8426")
	require.Contains(t, got, `"id":8426`, "original numeric request id must be restored")
	h.expectOnlySentinel(attacker)
}

// TestCollidingRequestIDsStayIsolated covers the advisory's second half:
// independent clients reuse ordinary id sequences, so two sessions with an
// in-flight id 1 must each receive only their own result, even when the
// backend answers them out of order.
func TestCollidingRequestIDsStayIsolated(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	victim := h.connect("victim")
	attacker := h.connect("attacker")

	h.post(victim, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"victim-tool"}}`)
	h.post(attacker, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"attacker-tool"}}`)

	first, second := h.backendRequest(), h.backendRequest()
	byParams := map[string]*jsonrpc2.Request{string(first.Params): first, string(second.Params): second}
	victimReq, attackerReq := byParams[`{"name":"victim-tool"}`], byParams[`{"name":"attacker-tool"}`]
	require.NotNil(t, victimReq)
	require.NotNil(t, attackerReq)

	// Attacker's response is emitted first so a first-match-wins confusion on
	// the victim side would be exposed as well.
	h.respond(attackerReq, "ATTACKER_RESULT")
	h.respond(victimReq, "VICTIM_RESULT")

	victimGot := h.expectNext(victim, "VICTIM_RESULT")
	require.Contains(t, victimGot, `"id":1`)
	require.NotContains(t, victimGot, "ATTACKER_RESULT")
	h.expectOnlySentinel(victim)

	attackerGot := h.expectNext(attacker, "ATTACKER_RESULT")
	require.Contains(t, attackerGot, `"id":1`)
	require.NotContains(t, attackerGot, "VICTIM_RESULT")
	h.expectOnlySentinel(attacker)
}

// TestClientCannotForgeAnotherSessionsRoute: a client whose own string id is
// spelled exactly like the victim's routed id must get that string back as
// its own id, and the victim must see nothing. The proxy prepends the
// validated session before the client-controlled part, so the client never
// influences the route.
func TestClientCannotForgeAnotherSessionsRoute(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	victim := h.connect("victim")
	attacker := h.connect("attacker")

	forged := victim.sessionID + "|n:1"
	h.post(attacker, `{"jsonrpc":"2.0","id":"`+forged+`","method":"tools/call","params":{}}`)
	req := h.backendRequest()
	require.Equal(t, attacker.sessionID+"|s:"+forged, req.ID.Raw())
	h.respond(req, "ATTACKER_OWN_RESULT")

	got := h.expectNext(attacker, "ATTACKER_OWN_RESULT")
	require.Contains(t, got, `"id":"`+forged+`"`, "attacker gets back exactly the string id it chose")
	h.expectOnlySentinel(victim)
}

// TestDepartedSessionResponseIsDropped: a response arriving after its session
// disconnected is dropped, not handed to whoever is still connected, and not
// queued for whoever connects next.
func TestDepartedSessionResponseIsDropped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	victim := h.connect("victim")
	bystander := h.connect("bystander")

	h.post(victim, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{}}`)
	req := h.backendRequest()

	victim.close()
	require.Eventually(t, func() bool {
		_, live := h.proxy.liveSession(victim.sessionID)
		return !live
	}, waitTimeout, 10*time.Millisecond, "victim session should be removed on disconnect")

	h.respond(req, "LATE_RESULT")
	h.expectOnlySentinel(bystander)
}

// TestCancelledNotificationCarriesRoutedRequestID: notifications/cancelled
// names the request to cancel in params.requestId. The backend only knows the
// routed id, so the proxy must rewrite requestId the same way it rewrote the
// call's id, or client cancellation silently stops working.
func TestCancelledNotificationCarriesRoutedRequestID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		callID string
		cancel string
	}{
		{"numeric request id", `8426`,
			`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":8426,"reason":"user"}}`},
		{"string request id", `"req-7"`,
			`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"req-7","reason":"user"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			victim := h.connect("victim")

			h.post(victim, `{"jsonrpc":"2.0","id":`+tc.callID+`,"method":"tools/call","params":{}}`)
			call := h.backendRequest()
			h.post(victim, tc.cancel)
			cancel := h.backendRequest()

			require.False(t, cancel.ID.IsValid(), "cancellation must remain a notification")
			require.Equal(t, "notifications/cancelled", cancel.Method)
			var params struct {
				RequestID any    `json:"requestId"`
				Reason    string `json:"reason"`
			}
			require.NoError(t, json.Unmarshal(cancel.Params, &params))
			require.Equal(t, call.ID.Raw(), params.RequestID, "requestId must be the routed id the backend knows")
			require.Equal(t, "user", params.Reason)
		})
	}
}

// TestCancelledNotificationWithoutRequestIDPassesThrough pins that the proxy
// does not police MCP semantics: a cancellation naming no request is
// forwarded untouched for the backend to ignore.
func TestCancelledNotificationWithoutRequestIDPassesThrough(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	victim := h.connect("victim")

	h.post(victim, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"reason":"no id"}}`)
	cancel := h.backendRequest()
	require.JSONEq(t, `{"reason":"no id"}`, string(cancel.Params))
}

// TestRewriteCancelledRequestIDLeavesUnroutableParamsAlone pins the
// pass-through cases: a cancellation the proxy cannot rewrite is forwarded
// exactly as received, as the same object, for the backend to ignore.
func TestRewriteCancelledRequestIDLeavesUnroutableParamsAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		params string
	}{
		{"no params", ""},
		{"positional params", `[1, 2]`},
		{"no requestId", `{"reason":"x"}`},
		{"non-integer requestId", `{"requestId":1.5}`},
		{"object requestId", `{"requestId":{"x":1}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := &jsonrpc2.Request{Method: methodCancelled, Params: json.RawMessage(tc.params)}
			got, err := rewriteCancelledRequestID("3f2a", n)
			require.NoError(t, err)
			require.Same(t, n, got)
		})
	}
}

// TestRouteResponseToUnwritableSession: a live session whose stream is full
// or already disconnected drops the response without error or panic; the
// session's own disconnect path owns cleanup.
func TestRouteResponseToUnwritableSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(*session.SSESession)
	}{
		{"channel full", func(s *session.SSESession) { s.MessageCh <- "occupied" }},
		{"disconnected", func(s *session.SSESession) { s.Disconnect() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			proxy := NewHTTPSSEProxy("localhost", 0, false, nil, nil)
			const sessionID = "eeeeeeee-0009-0009-0009-000000000009"
			sess := session.NewSSESessionWithClient(sessionID, &ssecommon.SSEClient{MessageCh: make(chan string, 1)})
			proxy.liveSSESessions.Store(sessionID, sess)
			tc.setup(sess)

			routed, err := encodeRoutedID(sessionID, jsonrpc2.Int64ID(1))
			require.NoError(t, err)
			resp, err := jsonrpc2.NewResponse(jsonrpc2.StringID(routed), "late", nil)
			require.NoError(t, err)
			require.NoError(t, proxy.ForwardResponseToClients(t.Context(), resp))
		})
	}
}

// TestRejectServerRequestFailsWhenBackendChannelFull: the -32601 rejection
// is written to the same channel clients use; when it is full the failure is
// returned so the stdio transport logs it instead of losing it silently.
func TestRejectServerRequestFailsWhenBackendChannelFull(t *testing.T) {
	t.Parallel()
	proxy := NewHTTPSSEProxy("localhost", 0, false, nil, nil)
	filler, err := jsonrpc2.NewNotification("notifications/initialized", nil)
	require.NoError(t, err)
	for i := 0; i < cap(proxy.messageCh); i++ {
		proxy.messageCh <- filler
	}

	req, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), "sampling/createMessage", nil)
	require.NoError(t, err)
	require.Error(t, proxy.ForwardResponseToClients(t.Context(), req))
}

// TestFirstOccurrence pins the warn-once bookkeeping: a key is first exactly
// once, and the set stops admitting new keys at maxWarnOnceKeys.
func TestFirstOccurrence(t *testing.T) {
	t.Parallel()
	proxy := NewHTTPSSEProxy("localhost", 0, false, nil, nil)

	require.True(t, proxy.firstOccurrence("drop:a"))
	require.False(t, proxy.firstOccurrence("drop:a"), "a repeat is never first")

	for i := 1; i < maxWarnOnceKeys; i++ {
		require.True(t, proxy.firstOccurrence("drop:"+strconv.Itoa(i)))
	}
	require.False(t, proxy.firstOccurrence("drop:overflow"), "new keys beyond the cap are not admitted")
	require.False(t, proxy.firstOccurrence("drop:a"), "known keys still report seen")
}

// TestBackendMessageDispatch covers every row of the dispatch table for a
// message the backend emits while two sessions are connected: who receives
// it, whether anything is queued, and what the backend is sent back.
func TestBackendMessageDispatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		build        func(t *testing.T, victimSess string) jsonrpc2.Message
		victimGets   string   // substring the victim must receive; "" means nothing
		attackerGets string   // substring the attacker must receive; "" means nothing
		backendGets  []string // substrings the backend must be sent; nil means nothing
	}{
		{
			name: "response for a disconnected session is dropped, not delivered to anyone",
			build: func(t *testing.T, _ string) jsonrpc2.Message {
				t.Helper()
				gone, err := encodeRoutedID("00000000-dead-4000-8000-000000000000", jsonrpc2.Int64ID(3))
				require.NoError(t, err)
				resp, err := jsonrpc2.NewResponse(jsonrpc2.StringID(gone), "STALE", nil)
				require.NoError(t, err)
				return resp
			},
		},
		{
			name: "response with an unrouted id is dropped",
			build: func(t *testing.T, _ string) jsonrpc2.Message {
				t.Helper()
				resp, err := jsonrpc2.NewResponse(jsonrpc2.StringID("ping_1"), "PONG", nil)
				require.NoError(t, err)
				return resp
			},
		},
		{
			name: "error response is routed with its original id restored",
			build: func(t *testing.T, victimSess string) jsonrpc2.Message {
				t.Helper()
				routed, err := encodeRoutedID(victimSess, jsonrpc2.Int64ID(9))
				require.NoError(t, err)
				return &jsonrpc2.Response{ID: jsonrpc2.StringID(routed), Error: jsonrpc2.NewError(-32602, "bad params")}
			},
			victimGets: `"id":9,"error":{"code":-32602`,
		},
		{
			name: "list_changed notification is broadcast to every session",
			build: func(t *testing.T, _ string) jsonrpc2.Message {
				t.Helper()
				n, err := jsonrpc2.NewNotification("notifications/tools/list_changed", nil)
				require.NoError(t, err)
				return n
			},
			victimGets:   "notifications/tools/list_changed",
			attackerGets: "notifications/tools/list_changed",
		},
		{
			name: "logging notification is dropped",
			build: func(t *testing.T, _ string) jsonrpc2.Message {
				t.Helper()
				n, err := jsonrpc2.NewNotification("notifications/message",
					map[string]any{"level": "info", "data": "victim ran secret tool"})
				require.NoError(t, err)
				return n
			},
		},
		{
			name: "progress notification is dropped",
			build: func(t *testing.T, _ string) jsonrpc2.Message {
				t.Helper()
				n, err := jsonrpc2.NewNotification("notifications/progress",
					map[string]any{"progressToken": 1, "progress": 50, "message": "half way"})
				require.NoError(t, err)
				return n
			},
		},
		{
			name: "server-initiated request is rejected back to the backend",
			build: func(t *testing.T, _ string) jsonrpc2.Message {
				t.Helper()
				req, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(77), "sampling/createMessage", map[string]any{})
				require.NoError(t, err)
				return req
			},
			backendGets: []string{`"id":77`, `"code":-32601`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			victim := h.connect("victim")
			attacker := h.connect("attacker")

			require.NoError(t, h.proxy.ForwardResponseToClients(t.Context(), tc.build(t, victim.sessionID)))

			if tc.victimGets == "" {
				h.expectOnlySentinel(victim)
			} else {
				h.expectNext(victim, tc.victimGets)
			}
			if tc.attackerGets == "" {
				h.expectOnlySentinel(attacker)
			} else {
				h.expectNext(attacker, tc.attackerGets)
			}
			if tc.backendGets == nil {
				require.Empty(t, h.proxy.messageCh, "backend must not be sent anything")
				return
			}
			data, err := jsonrpc2.EncodeMessage(h.backendMessage())
			require.NoError(t, err)
			for _, want := range tc.backendGets {
				require.Contains(t, string(data), want)
			}
		})
	}
}

// TestNothingIsReplayedToALaterClient: whatever the backend emits while no
// client is connected reaches nobody, then or later. There is no replay
// buffer, so a client connecting afterwards cannot be handed another
// session's response or a stale notification. Matches the streamable proxy.
func TestNothingIsReplayedToALaterClient(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	gone, err := encodeRoutedID("00000000-dead-4000-8000-000000000000", jsonrpc2.Int64ID(1))
	require.NoError(t, err)
	resp, err := jsonrpc2.NewResponse(jsonrpc2.StringID(gone), "LATE", nil)
	require.NoError(t, err)
	require.NoError(t, h.proxy.ForwardResponseToClients(t.Context(), resp))

	n, err := jsonrpc2.NewNotification("notifications/tools/list_changed", nil)
	require.NoError(t, err)
	require.NoError(t, h.proxy.ForwardResponseToClients(t.Context(), n))

	late := h.connect("late")
	h.expectOnlySentinel(late)
}

// TestRoutedIDRoundTrip pins the wire format: the session and the client's
// original ID, including its type, must survive encode and decode exactly.
func TestRoutedIDRoundTrip(t *testing.T) {
	t.Parallel()
	const sess = "3f2a6c1e-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		name string
		id   jsonrpc2.ID
		wire string
	}{
		{"positive number", jsonrpc2.Int64ID(8426), sess + "|n:8426"},
		{"zero", jsonrpc2.Int64ID(0), sess + "|n:0"},
		{"negative number", jsonrpc2.Int64ID(-7), sess + "|n:-7"},
		{"max int64", jsonrpc2.Int64ID(math.MaxInt64), sess + "|n:9223372036854775807"},
		{"min int64", jsonrpc2.Int64ID(math.MinInt64), sess + "|n:-9223372036854775808"},
		{"string", jsonrpc2.StringID("req-7"), sess + "|s:req-7"},
		{"numeric-looking string keeps string type", jsonrpc2.StringID("8426"), sess + "|s:8426"},
		{"string containing the separator", jsonrpc2.StringID("a|n:5"), sess + "|s:a|n:5"},
		{"empty string", jsonrpc2.StringID(""), sess + "|s:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wire, err := encodeRoutedID(sess, tc.id)
			require.NoError(t, err)
			require.Equal(t, tc.wire, wire)
			gotSess, gotID, ok := decodeRoutedID(jsonrpc2.StringID(wire))
			require.True(t, ok)
			require.Equal(t, sess, gotSess)
			require.Equal(t, tc.id.Raw(), gotID.Raw(), "raw value and Go type must match")
		})
	}
}

// TestDecodeRoutedIDRejectsForeignIDs covers every ID shape the proxy did not
// mint; each must be reported as unroutable so the response is dropped.
func TestDecodeRoutedIDRejectsForeignIDs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		id   jsonrpc2.ID
	}{
		{"numeric id", jsonrpc2.Int64ID(1)},
		{"health-check ping id", jsonrpc2.StringID("ping_1700000000")},
		{"missing separator", jsonrpc2.StringID("3f2a-n:1")},
		{"empty session", jsonrpc2.StringID("|n:1")},
		{"unknown type tag", jsonrpc2.StringID("3f2a|x:1")},
		{"missing type tag", jsonrpc2.StringID("3f2a|1")},
		{"malformed number", jsonrpc2.StringID("3f2a|n:1.5")},
		{"invalid id", jsonrpc2.ID{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, ok := decodeRoutedID(tc.id)
			require.False(t, ok)
		})
	}
}

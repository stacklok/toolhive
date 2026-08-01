// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

// newDispatcherTestProxy creates an HTTPProxy with dispatchResponses running in
// the background, without starting the HTTP server. t.Cleanup stops it.
func newDispatcherTestProxy(t *testing.T) *HTTPProxy {
	t.Helper()
	proxy := NewHTTPProxy("localhost", 0, nil, nil)
	go proxy.dispatchResponses()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})
	return proxy
}

// TestDispatchResponses_ResponseRoutesToWaiter verifies a *jsonrpc2.Response
// carrying a known composite ID is delivered to its registered waiter.
func TestDispatchResponses_ResponseRoutesToWaiter(t *testing.T) {
	t.Parallel()
	proxy := newDispatcherTestProxy(t)

	waitCh, cleanup := proxy.createWaiter("sess-1", jsonrpc2.StringID("req-1"))
	defer cleanup()
	ck := compositeKey("sess-1", idKeyFromID(jsonrpc2.StringID("req-1")))

	resp, err := jsonrpc2.NewResponse(jsonrpc2.StringID(ck), "result", nil)
	require.NoError(t, err)

	proxy.responseCh <- resp

	select {
	case got := <-waitCh:
		assert.Equal(t, resp, got)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response to reach waiter")
	}
}

// TestDispatchResponses_UnknownCompositeKeyResponseIsDropped verifies a
// *jsonrpc2.Response whose composite ID has no registered waiter is dropped
// without blocking or panicking.
func TestDispatchResponses_UnknownCompositeKeyResponseIsDropped(t *testing.T) {
	t.Parallel()
	proxy := newDispatcherTestProxy(t)

	resp, err := jsonrpc2.NewResponse(jsonrpc2.StringID("no-such-waiter|nil"), "result", nil)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		proxy.responseCh <- resp
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending response with unknown composite key")
	}

	// Give the dispatcher goroutine a moment to process; there is nothing to
	// assert beyond "did not panic/block", since there is no waiter to observe.
	time.Sleep(50 * time.Millisecond)
}

// TestDispatchResponses_ListChangedBroadcastsToEverySession verifies every
// */list_changed notification (listChangedNotificationMethods) is broadcast
// to every connected session's standalone GET stream.
func TestDispatchResponses_ListChangedBroadcastsToEverySession(t *testing.T) {
	t.Parallel()

	methods := []string{
		"notifications/tools/list_changed",
		"notifications/resources/list_changed",
		"notifications/prompts/list_changed",
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			proxy := newDispatcherTestProxy(t)

			s1 := proxy.serverStreams.register("session-1")
			s2 := proxy.serverStreams.register("session-2")
			t.Cleanup(func() {
				proxy.serverStreams.deregister("session-1", s1)
				proxy.serverStreams.deregister("session-2", s2)
			})

			notification, err := jsonrpc2.NewNotification(method, nil)
			require.NoError(t, err)
			proxy.responseCh <- notification

			assert.Equal(t, notification, recvWithTimeout(t, s1.data))
			assert.Equal(t, notification, recvWithTimeout(t, s2.data))
		})
	}
}

// TestDispatchResponses_ProgressDeliveredOnlyToRecordedRoute verifies a
// notifications/progress message is delivered ONLY to the progress route
// recorded for its progressToken (with the client's original token restored),
// and that neither a different token nor no recorded route at all delivers
// anything -- proving progress can never leak across sessions/requests.
func TestDispatchResponses_ProgressDeliveredOnlyToRecordedRoute(t *testing.T) {
	t.Parallel()
	proxy := newDispatcherTestProxy(t)

	deliverA := make(chan jsonrpc2.Message, 1)
	proxy.routing.recordProgressToken("ptGlobal-A", progressRoute{deliver: deliverA, originalToken: "client-token-A"})
	t.Cleanup(func() { proxy.routing.dropProgressToken("ptGlobal-A") })

	// Unregistered token: must not be delivered anywhere.
	progressUnknown, err := jsonrpc2.NewNotification("notifications/progress",
		map[string]any{"progressToken": "ptGlobal-unknown", "progress": 1})
	require.NoError(t, err)
	proxy.responseCh <- progressUnknown

	// Recorded token: must be delivered on deliverA, with the ORIGINAL client
	// token restored (not the internal ptGlobal-A).
	progressA, err := jsonrpc2.NewNotification("notifications/progress",
		map[string]any{"progressToken": "ptGlobal-A", "progress": 42})
	require.NoError(t, err)
	proxy.responseCh <- progressA

	got := recvWithTimeout(t, deliverA)
	gotReq, ok := got.(*jsonrpc2.Request)
	require.True(t, ok)
	token, ok := extractStringParam(gotReq.Params, "progressToken")
	require.True(t, ok)
	assert.Equal(t, "client-token-A", token, "delivered notification must carry the client's original token, not ptGlobal")

	// Prove the unknown-token notification was never delivered (it would have
	// arrived first, before progressA, if it had been).
	assertNoDelivery(t, deliverA)
}

// TestDispatchResponses_ResourcesUpdatedRoutesToSubscribersOnly verifies a
// notifications/resources/updated message reaches only sessions subscribed to
// its uri, delivered on their standalone GET stream -- and NOT a connected
// session that never subscribed.
func TestDispatchResponses_ResourcesUpdatedRoutesToSubscribersOnly(t *testing.T) {
	t.Parallel()
	proxy := newDispatcherTestProxy(t)

	subscriber := proxy.serverStreams.register("subscriber-session")
	nonSubscriber := proxy.serverStreams.register("non-subscriber-session")
	t.Cleanup(func() {
		proxy.serverStreams.deregister("subscriber-session", subscriber)
		proxy.serverStreams.deregister("non-subscriber-session", nonSubscriber)
	})

	proxy.routing.addSubscription("file:///watched.txt", "subscriber-session")

	notification, err := jsonrpc2.NewNotification("notifications/resources/updated",
		map[string]any{"uri": "file:///watched.txt"})
	require.NoError(t, err)
	proxy.responseCh <- notification

	assert.Equal(t, notification, recvWithTimeout(t, subscriber.data))
	assertNoDelivery(t, nonSubscriber.data)
}

// TestDispatchResponses_LoggingMessageIsDropped verifies notifications/message
// (logging) is dropped rather than delivered to any connected session's
// stream: the shared backend cannot attribute log content to a session, so
// forwarding it (to any session) would be a cross-session leak.
func TestDispatchResponses_LoggingMessageIsDropped(t *testing.T) {
	t.Parallel()
	proxy := newDispatcherTestProxy(t)

	s := proxy.serverStreams.register("session-1")
	t.Cleanup(func() { proxy.serverStreams.deregister("session-1", s) })

	logMsg, err := jsonrpc2.NewNotification("notifications/message",
		map[string]any{"level": "info", "data": "secret log line"})
	require.NoError(t, err)
	proxy.responseCh <- logMsg

	// Race the dropped notification against a sentinel GLOBAL notification: if
	// the log message had been delivered, it would arrive on s.data first.
	sentinel, err := jsonrpc2.NewNotification("notifications/tools/list_changed", nil)
	require.NoError(t, err)
	proxy.responseCh <- sentinel

	got := recvWithTimeout(t, s.data)
	assert.Equal(t, sentinel, got, "the dropped logging notification must not appear on any stream")
}

// TestDispatchResponses_ServerRequestRejectedToBackend verifies a
// server->client REQUEST with a valid ID (sampling/elicitation) is never
// delivered to any client stream, and instead gets a JSON-RPC error response
// written back to the BACKEND (via SendMessageToDestination/messageCh) so the
// backend's blocking call unblocks.
func TestDispatchResponses_ServerRequestRejectedToBackend(t *testing.T) {
	t.Parallel()
	proxy := newDispatcherTestProxy(t)

	s := proxy.serverStreams.register("session-1")
	t.Cleanup(func() { proxy.serverStreams.deregister("session-1", s) })

	req, err := jsonrpc2.NewCall(jsonrpc2.StringID("sampling-1"), "sampling/createMessage", nil)
	require.NoError(t, err)
	proxy.responseCh <- req

	// The error response is written back to the backend on messageCh.
	select {
	case msg := <-proxy.GetMessageChannel():
		resp, ok := msg.(*jsonrpc2.Response)
		require.True(t, ok, "expected a *jsonrpc2.Response written back to the backend")
		assert.Equal(t, "sampling-1", resp.ID.Raw())
		require.Error(t, resp.Error)
		assert.Contains(t, resp.Error.Error(), "sampling/createMessage")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error response to reach the backend")
	}

	// Prove it did NOT reach any client stream by racing it against a GLOBAL
	// notification sent right after.
	sentinel, err := jsonrpc2.NewNotification("notifications/tools/list_changed", nil)
	require.NoError(t, err)
	proxy.responseCh <- sentinel

	got := recvWithTimeout(t, s.data)
	assert.Equal(t, sentinel, got, "the rejected server->client request must not appear on any client stream")
}

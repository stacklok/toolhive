// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	"golang.org/x/exp/jsonrpc2"
	"golang.org/x/sync/singleflight"

	sdkmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// errUnexpectedInitializeReply is returned when the backend answers initialize
// with something that is not a *jsonrpc2.Response, so it cannot be re-addressed
// to the calling client.
var errUnexpectedInitializeReply = errors.New("backend returned an unexpected reply to initialize")

// initializeCache holds the InitializeResult the backend produced for the
// first client handshake, so later handshakes are answered locally instead of
// reaching the backend again.
//
// A stdio MCP server is one process and therefore exactly one MCP session,
// while this proxy fans an arbitrary number of HTTP client sessions onto it.
// Forwarding every client's initialize to that single session means only the
// first can succeed: go-sdk v1.7+ rejects a second handshake on an
// already-initialized session with `duplicate "initialize" received`, which
// takes down every client after the first -- and, behind vMCP, every health
// check and tool call as well (see #5890, #1982).
//
// Answering from a cache restores the behaviour go-sdk v1.6.1 and earlier had,
// where a repeated initialize returned the original result, but places it in
// the component that actually owns the multiplexing rather than asking every
// MCP server to tolerate duplicate handshakes.
//
// The cached value is process-scoped rather than session-scoped: an
// InitializeResult carries the backend's protocol version, capabilities,
// instructions and server info, none of which vary by client.
type initializeCache struct {
	// flight collapses concurrent handshakes into a single upstream call whose
	// outcome every waiter shares. Serializing them behind a mutex instead
	// would queue each waiter's own round trip: with an unresponsive backend
	// the Nth client waits N*requestTimeout (60s each) before it may even
	// start.
	flight singleflight.Group

	// result is the raw InitializeResult of the first successful handshake,
	// nil until one completes.
	//
	// UNSYNCHRONISED BY DESIGN: only the flight callback in interceptInitialize
	// may read or write this field. singleflight runs at most one callback per
	// key at a time and a later callback starts only after the previous one has
	// completed through the group's own mutex, so callback-only access can
	// never overlap. Reading it anywhere else -- a status endpoint, a helper
	// accessor -- reintroduces a data race, which is why no accessor exists.
	// Once assigned it is never mutated, so the same bytes may be handed to
	// concurrent responses.
	result json.RawMessage

	// initializedForwarded records whether a notifications/initialized has
	// already been passed to the backend. This deliberately does NOT use the
	// flight: singleflight de-duplicates rather than serializes, so two
	// concurrent claims would share one callback's answer and both forward.
	// CompareAndSwap gives exactly one winner.
	initializedForwarded atomic.Bool
}

// handshake is the id-free half of an initialize outcome, shared by a flight
// with all of its waiters. Exactly one of the fields is set. Sharing this
// rather than a whole *jsonrpc2.Response means each caller builds its own
// reply, so a waiter cannot receive the forwarding caller's JSON-RPC id.
type handshake struct {
	result json.RawMessage
	rpcErr error
}

// initializeFlightKey is the sole singleflight key: the cached handshake is
// process-scoped, so all callers share one flight.
const initializeFlightKey = "initialize"

// newInitializeCache creates an empty initializeCache.
func newInitializeCache() *initializeCache {
	return &initializeCache{}
}

// interceptInitialize answers a client's initialize request: the first caller's
// request is forwarded to the backend and its result cached, and every later
// caller is served that cached result without an upstream round trip.
//
// Only a successful handshake is cached. A transport failure or a JSON-RPC
// error from the backend leaves the cache empty so the next client retries the
// handshake for real, rather than pinning every future client to one bad
// response.
//
// Concurrent first handshakes are collapsed by singleflight, so exactly one
// reaches the backend and the rest share its outcome. The upstream call runs on
// a context detached from the caller's request (context.WithoutCancel): the
// result belongs to every waiter, so the first caller disconnecting must not
// cancel the handshake for the others. forwardUpstream still applies
// p.requestTimeout to that detached context, so it cannot run unbounded.
//
// Every response is rebuilt with the calling request's own JSON-RPC id. A
// shared flight result carries the id of whichever caller performed the
// forward, and returning it verbatim would hand later waiters an id they never
// sent.
//
// One client-visible consequence: the protocol version negotiated by the first
// client is replayed to every later client. A client that asked for a
// different version receives the first client's instead, and per the MCP
// lifecycle spec should disconnect if it cannot speak it. This is inherent to
// collapsing N client sessions onto one backend session, and matches what
// go-sdk v1.6.1 and earlier did when replaying a cached result.
func (p *HTTPProxy) interceptInitialize(
	ctx context.Context, sessID string, req *jsonrpc2.Request,
) jsonrpc2.Message {
	c := p.initialize
	v, err, _ := c.flight.Do(initializeFlightKey, func() (any, error) {
		// A handshake already succeeded: replay it, no backend round trip.
		if c.result != nil {
			return handshake{result: c.result}, nil
		}

		// Detached from the caller's context: this result belongs to every
		// waiter, so whichever caller performs the forward must not cancel the
		// handshake for the others by disconnecting. forwardUpstream still
		// bounds it with p.requestTimeout.
		msg, ferr := p.forwardUpstream(context.WithoutCancel(ctx), sessID, req)
		if ferr != nil {
			return nil, ferr // nothing cached; the next client retries for real
		}

		resp, ok := msg.(*jsonrpc2.Response)
		if !ok {
			return nil, errUnexpectedInitializeReply
		}
		if resp.Error != nil {
			// Shared with this flight's waiters but deliberately not cached, so
			// a transient failure cannot pin every future client to one
			// response.
			return handshake{rpcErr: resp.Error}, nil
		}
		if len(resp.Result) == 0 {
			return nil, errUnexpectedInitializeReply // no result and no error
		}

		c.result = resp.Result
		return handshake{result: resp.Result}, nil
	})
	if err != nil {
		return upstreamErrorResponse(req.ID, err)
	}

	h := v.(handshake) // the callback returns a handshake or a non-nil error
	return &jsonrpc2.Response{ID: req.ID, Result: h.result, Error: h.rpcErr}
}

// claimInitializedForward reports whether a client's notifications/initialized
// should be passed to the backend, and records that it has been.
//
// The backend completes a single handshake and so expects a single initialized
// notification; the first client's is forwarded and every later one is
// swallowed. Swallowed notifications are still acknowledged with 202, which is
// all the sending client expects -- notifications carry no response.
func (p *HTTPProxy) claimInitializedForward() bool {
	return p.initialize.initializedForwarded.CompareAndSwap(false, true)
}

// isInitializedNotification reports whether msg is a client's
// notifications/initialized (a JSON-RPC notification, so no id).
func isInitializedNotification(msg jsonrpc2.Message) bool {
	req, ok := msg.(*jsonrpc2.Request)
	if !ok {
		return false
	}
	return req.ID.Raw() == nil && req.Method == string(sdkmcp.MethodNotificationInitialized)
}

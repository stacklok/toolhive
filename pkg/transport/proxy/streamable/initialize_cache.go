// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"context"
	"encoding/json"
	"sync"

	"golang.org/x/exp/jsonrpc2"

	sdkmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

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
	// mu guards both fields below and additionally serializes the first
	// upstream handshake (see HTTPProxy.interceptInitialize), so a concurrent
	// second initialize waits for the first to finish and then reads the
	// cache instead of racing another handshake to the backend.
	mu sync.Mutex

	// result is the raw InitializeResult of the first successful handshake,
	// nil until one completes. Once assigned it is never mutated, so it is
	// safe to hand the same bytes to concurrent responses.
	result json.RawMessage

	// initializedForwarded records whether a notifications/initialized has
	// already been passed to the backend.
	initializedForwarded bool
}

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
// mu is held across the upstream round trip on the first call. That is what
// makes the forward single-flight, and it is bounded: forwardUpstream applies
// p.requestTimeout, so an unresponsive backend cannot block other sessions'
// handshakes indefinitely -- the same reasoning uriLocks relies on for
// resources/subscribe.
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
	p.initialize.mu.Lock()
	defer p.initialize.mu.Unlock()

	if p.initialize.result != nil {
		return &jsonrpc2.Response{ID: req.ID, Result: p.initialize.result}
	}

	msg, err := p.forwardUpstream(ctx, sessID, req)
	if err != nil {
		return upstreamErrorResponse(req.ID, err)
	}
	if resp, ok := msg.(*jsonrpc2.Response); ok && resp.Error == nil && len(resp.Result) > 0 {
		p.initialize.result = resp.Result
	}
	return msg
}

// claimInitializedForward reports whether a client's notifications/initialized
// should be passed to the backend, and records that it has been.
//
// The backend completes a single handshake and so expects a single initialized
// notification; the first client's is forwarded and every later one is
// swallowed. Swallowed notifications are still acknowledged with 202, which is
// all the sending client expects -- notifications carry no response.
func (p *HTTPProxy) claimInitializedForward() bool {
	p.initialize.mu.Lock()
	defer p.initialize.mu.Unlock()

	if p.initialize.initializedForwarded {
		return false
	}
	p.initialize.initializedForwarded = true
	return true
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

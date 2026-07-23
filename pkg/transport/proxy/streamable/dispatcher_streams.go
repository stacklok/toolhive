// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"log/slog"
	"sync"

	"golang.org/x/exp/jsonrpc2"
)

// serverStreamBufferSize is the buffer size for each registered stream's data
// channel.
const serverStreamBufferSize = 100

// serverStream is a single MCP session's standalone server->client stream.
// data delivers unsolicited notifications to the stream's consumer (the GET
// handler in streamable_proxy.go) and is NEVER closed -- eviction and
// shutdown are signaled exclusively via stop, so a concurrent dispatch can
// never race a close of data (see registry method docs for the panic this
// avoids).
type serverStream struct {
	// data is buffered (serverStreamBufferSize) and never closed.
	data chan jsonrpc2.Message
	// stop is closed exactly once, either by an evicting register call or by
	// closeAll, to signal the consumer to stop reading data and return.
	stop chan struct{}
}

// serverStreamRegistry is a transport-agnostic registry of live server->client
// streams, used to deliver unsolicited container notifications to the
// standalone stream for each MCP session. Per the MCP spec, a server MUST NOT
// deliver the same notification more than once per session: this registry
// enforces that by keying on session ID and allowing at most one stream per
// session. It has no knowledge of HTTP: callers (the GET handler in
// streamable_proxy.go) own translating a registered stream's messages into
// wire frames.
//
// Forward-compatibility seam: the 2026-07-28 (Modern) revision's
// "subscriptions/listen" method (see mcp.MCPVersionModern in
// pkg/mcp/revision.go) is expected to be served by a future POST handler that
// keeps its response stream open and registers with this SAME registry,
// rather than introducing a second fan-out mechanism. Keep this type free of
// HTTP/GET-specific assumptions so that reuse is possible without a rewrite.
//
// All fields are guarded by mu; do not add another synchronization primitive
// over streams (see the "one synchronization primitive per data structure"
// style rule). The only channel operation performed while mu is held is a
// non-blocking select-send in broadcast, dispatchTo, and deliverToMany, which
// cannot deadlock.
type serverStreamRegistry struct {
	mu      sync.Mutex
	streams map[string]*serverStream
}

// newServerStreamRegistry creates an empty serverStreamRegistry.
func newServerStreamRegistry() *serverStreamRegistry {
	return &serverStreamRegistry{streams: make(map[string]*serverStream)}
}

// register creates and stores a new stream for the given MCP session ID,
// returning it for the caller to consume from (data) and select on (stop).
// If a stream is already registered for sessionID (e.g. a second concurrent
// GET for the same session), the prior stream is evicted: its stop channel is
// closed so its consumer goroutine observes the eviction and returns, and the
// new stream replaces it in the map (last-writer-wins, one stream per
// session). The caller MUST call deregister(sessionID, s) when the stream
// ends (client disconnect, proxy shutdown) to avoid leaking the map entry.
func (r *serverStreamRegistry) register(sessionID string) *serverStream {
	r.mu.Lock()
	defer r.mu.Unlock()

	if old, ok := r.streams[sessionID]; ok {
		close(old.stop)
	}

	s := &serverStream{
		data: make(chan jsonrpc2.Message, serverStreamBufferSize),
		stop: make(chan struct{}),
	}
	r.streams[sessionID] = s
	return s
}

// deregister removes the stream for sessionID, but only if s is still the
// currently registered stream for that session (identity check). This makes
// a late deregister from an already-evicted handler (see register) a correct
// no-op instead of deleting the newer stream that replaced it. It never
// closes any channel: data is never closed (see serverStream's doc comment),
// and stop is closed exactly once by whichever of register (eviction) or
// closeAll observes it first.
func (r *serverStreamRegistry) deregister(sessionID string, s *serverStream) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.streams[sessionID] == s {
		delete(r.streams, sessionID)
	}
}

// broadcast delivers msg once to EVERY currently registered session's stream.
// It is used only for GLOBAL notifications (see the */list_changed handling
// in dispatcher.go) that describe a server-wide capability change with no
// session-specific payload, so fanning out one copy to every session is
// correct and safe.
//
// Delivery is non-blocking and best-effort per the locked product decision: a
// stream whose data channel is full has a slow or stuck consumer, and
// dropping the message for that stream is preferable to blocking delivery to
// every other session's stream. Because data is never closed, this select can
// never panic on a send to a closed channel, even if a concurrent deregister
// or closeAll is racing this call.
func (r *serverStreamRegistry) broadcast(msg jsonrpc2.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for sessionID, s := range r.streams {
		select {
		case s.data <- msg:
		default:
			slog.Warn("server stream channel full; dropping notification", "session_id", sessionID)
		}
	}
}

// dispatchTo delivers msg to the single stream registered for routeKey (a
// session ID), if one is currently connected. Delivery is non-blocking and
// best-effort, matching broadcast: an absent stream (no GET SSE connection for
// that session right now) or a full one both simply drop msg, logged at
// Debug/Warn respectively, rather than blocking or erroring. Callers use this
// for messages that are correlated to exactly one session (e.g. a
// resources/updated notification's subscribers) and MUST NOT ever reach a
// different session's stream.
func (r *serverStreamRegistry) dispatchTo(routeKey string, msg jsonrpc2.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.streams[routeKey]
	if !ok {
		slog.Debug("no server stream registered for route key; dropping notification", "route_key", routeKey)
		return
	}
	select {
	case s.data <- msg:
	default:
		slog.Warn("server stream channel full; dropping notification", "route_key", routeKey)
	}
}

// deliverToMany delivers msg to each of routeKeys' streams that is currently
// registered, taking mu once for the whole batch rather than once per key.
// Delivery to each present stream is non-blocking and best-effort, matching
// broadcast/dispatchTo. Callers use this for messages correlated to a known,
// bounded set of sessions (e.g. a resource's subscribers) -- routeKeys not
// present in the registry are silently skipped (no connected GET stream right
// now), not an error.
func (r *serverStreamRegistry) deliverToMany(routeKeys []string, msg jsonrpc2.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, routeKey := range routeKeys {
		s, ok := r.streams[routeKey]
		if !ok {
			slog.Debug("no server stream registered for route key; dropping notification", "route_key", routeKey)
			continue
		}
		select {
		case s.data <- msg:
		default:
			slog.Warn("server stream channel full; dropping notification", "route_key", routeKey)
		}
	}
}

// streamCount reports how many sessions currently have a registered stream.
func (r *serverStreamRegistry) streamCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.streams)
}

// closeAll signals every currently registered stream to stop (by closing its
// stop channel) and resets the registry to empty. It is used on proxy Stop so
// that in-flight GET handlers observe a closed per-stream stop signal and
// return, in addition to (not instead of) selecting on shutdownCh.
func (r *serverStreamRegistry) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, s := range r.streams {
		close(s.stop)
	}
	r.streams = make(map[string]*serverStream)
}

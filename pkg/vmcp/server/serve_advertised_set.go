// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"

	lru "github.com/hashicorp/golang-lru/v2"
)

// defaultAdvertisedSetCacheCapacity bounds the Serve-path advertised-set cache
// when the session manager's CacheCapacity is unset. It mirrors the session
// manager's own default (sessionmanager.defaultCacheCapacity = 1000) so the two
// node-local caches hold roughly the same number of live sessions and the
// advertised set rarely evicts ahead of its MultiSession.
const defaultAdvertisedSetCacheCapacity = 1000

// advertisedSet is the per-session name→backend mapping that audit
// backend-enrichment uses to label an MCP request with the backend serving it.
// It is derived once, at session registration, from the core's
// admission-filtered ListTools/ListResources aggregation — the same single
// aggregation that builds the session's advertised capability set — so reading
// it costs only a map lookup instead of a fresh re-aggregation.
//
// Only the capability kinds whose audit events carry a backend label are
// recorded: tools/call (keyed by tool name) and resources/read (keyed by URI).
// prompts/get is intentionally absent — the Serve path does not advertise
// prompts (see injectCoreSessionCapabilities), so no prompt is callable and
// there is nothing to label.
//
// The maps are populated when the set is built and never mutated afterwards, so
// a stored *advertisedSet is safe for concurrent reads without a lock.
type advertisedSet struct {
	tools     map[string]string // advertised tool name -> backend display name
	resources map[string]string // advertised resource URI -> backend display name
}

// backendName returns the display name of the backend serving the capability
// named in an MCP request, or "" when the method carries no backend label or
// the capability is not in the advertised set.
func (a *advertisedSet) backendName(method string, params map[string]any) string {
	switch method {
	case "tools/call":
		if name, ok := params["name"].(string); ok {
			return a.tools[name]
		}
	case "resources/read":
		if uri, ok := params["uri"].(string); ok {
			return a.resources[uri]
		}
	}
	return ""
}

// advertisedSetCache is the Serve-path, per-session cache of advertised
// name→backend mappings. It implements the "Serve caches, core is stateless"
// pattern: audit backend-enrichment reads a cached mapping instead of calling
// core.Lookup*, which — by the core's deliberate stateless design —
// re-aggregates every backend's capabilities (and, with remote discovery,
// performs a per-request remote fan-out) on every call (issue #5493).
//
// # Lifecycle
//
// An entry is stored once per session when its advertised set is built
// (injectCoreSessionCapabilities) and removed when the transport observes the
// session ending (terminateOnBindingFailure and the registration-failure
// cleanup). The remaining session-end paths — a client DELETE and TTL/capacity
// eviction — are owned by the session manager, which evicts lazily and does not
// notify the transport, so the cache is bounded (LRU): it can never grow without
// limit even when one of those paths leaves an entry behind for a now-dead
// session ID. Such a stale entry is harmless (session IDs are UUIDs and never
// reused) and is evicted under capacity pressure.
//
// # Cross-replica behaviour
//
// Entries are in-process and node-local, mirroring the MultiSession runtime
// layer. A request that lands on a replica that did not register the session
// (no session affinity) misses; enrichment then records the event without a
// backend label rather than re-aggregating. A miss therefore degrades the audit
// label gracefully and NEVER falls back to a second aggregation, so Serve-path
// enrichment is aggregation-free regardless of cache state.
//
// All methods are safe for concurrent use (golang-lru is internally
// synchronised) and on a nil receiver — the legacy server.New path leaves the
// cache nil and its shared session-cleanup code calls evict unconditionally.
type advertisedSetCache struct {
	sets *lru.Cache[string, *advertisedSet]
}

// newAdvertisedSetCache builds a bounded advertised-set cache. A capacity of 0
// uses defaultAdvertisedSetCacheCapacity, so a zero value never silently enables
// unbounded growth; a negative capacity is rejected. This matches the session
// manager's validation of the same CacheCapacity field (sessionmanager.New), since
// both caches are sized from it — the two must not disagree on what a value means.
func newAdvertisedSetCache(capacity int) (*advertisedSetCache, error) {
	if capacity < 0 {
		return nil, fmt.Errorf("advertised-set cache: capacity must be >= 0 (got %d)", capacity)
	}
	if capacity == 0 {
		capacity = defaultAdvertisedSetCacheCapacity
	}
	sets, err := lru.New[string, *advertisedSet](capacity)
	if err != nil {
		return nil, fmt.Errorf("advertised-set cache: %w", err)
	}
	return &advertisedSetCache{sets: sets}, nil
}

// store records the advertised set for sessionID, replacing any existing entry.
func (c *advertisedSetCache) store(sessionID string, set *advertisedSet) {
	if c == nil || set == nil {
		return
	}
	c.sets.Add(sessionID, set)
}

// get returns the cached advertised set for sessionID, or (nil, false) on a miss.
func (c *advertisedSetCache) get(sessionID string) (*advertisedSet, bool) {
	if c == nil {
		return nil, false
	}
	return c.sets.Get(sessionID)
}

// evict removes the cached advertised set for sessionID. Removing an absent key
// is a no-op, so callers on the legacy path (nil cache) need no guard.
func (c *advertisedSetCache) evict(sessionID string) {
	if c == nil {
		return
	}
	c.sets.Remove(sessionID)
}

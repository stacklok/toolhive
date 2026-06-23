// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
)

// cachingAggregator wraps an Aggregator and memoizes AggregateCapabilities for a bounded
// TTL, so the Serve path does not re-sweep every backend's tools/list on every tool call.
//
// On the New/Serve path, core.CallTool/ReadResource re-derive the advertised view on every
// call (the core holds no per-session cache); without this, a session with M calls performs
// ~M+1 full backend sweeps instead of the legacy ~1-per-session. This decorator restores
// once-per-(identity, TTL) freshness without coupling the core or Serve to a cache — it sits
// transparently below the core, wrapping the Aggregator the core already calls.
//
// Security: backend enumeration is identity-dependent — what each backend returns depends on
// the credential presented to it — so the cache key MUST include the identity and the
// forwarded credentials, not just the backend set. Keying on (subject, forwarded headers,
// backend IDs) ensures one caller's capability view is never served to another, while a
// single caller's view is shared across their sessions. The key is a SHA-256 digest, so raw
// credential values are not retained as map keys. The cache is node-local and never persisted
// (it would be a credentialed view in shared state otherwise).
//
// Freshness/eviction: a TTL bounds staleness (tighter than legacy's once-per-session, which
// could be stale for the whole session lifetime). Expired entries are evicted on the next
// miss, so the map is bounded by the number of distinct (identity, credential, backend-set)
// keys seen within one TTL window.
type cachingAggregator struct {
	// Aggregator is the wrapped aggregator; embedding delegates every method except the
	// AggregateCapabilities override below.
	Aggregator

	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	caps *AggregatedCapabilities
	at   time.Time
}

// NewCachingAggregator wraps next so AggregateCapabilities results are memoized per identity
// for ttl. A ttl <= 0 disables caching (next is returned unwrapped) so a misconfiguration
// cannot silently serve permanently-stale capabilities. A nil next is returned as-is so the
// downstream nil-aggregator validation (core.New) still fires rather than being masked by a
// non-nil wrapper.
func NewCachingAggregator(next Aggregator, ttl time.Duration) Aggregator {
	if next == nil || ttl <= 0 {
		return next
	}
	return &cachingAggregator{
		Aggregator: next,
		ttl:        ttl,
		entries:    make(map[string]cacheEntry),
	}
}

// AggregateCapabilities returns a cached view when a fresh entry exists for the caller's
// identity + forwarded credentials + backend set, and otherwise sweeps the backends (via the
// wrapped aggregator) and caches the result. Errors are never cached. The returned value is
// treated as immutable by the core (it derives fresh per-call routers from it), so the cached
// pointer is shared rather than deep-copied.
func (c *cachingAggregator) AggregateCapabilities(
	ctx context.Context, backends []vmcp.Backend,
) (*AggregatedCapabilities, error) {
	key := cacheKey(ctx, backends)
	now := time.Now()

	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Sub(e.at) < c.ttl {
		c.mu.Unlock()
		return e.caps, nil
	}
	c.mu.Unlock()

	// Miss/expiry: sweep outside the lock so concurrent callers with different keys are not
	// serialized behind one backend sweep. Concurrent misses for the same key may each sweep
	// once (last writer wins) — acceptable for a cold/expired key.
	caps, err := c.Aggregator.AggregateCapabilities(ctx, backends)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.evictExpiredLocked(now)
	c.entries[key] = cacheEntry{caps: caps, at: now}
	c.mu.Unlock()
	return caps, nil
}

// evictExpiredLocked removes entries older than the TTL. Caller must hold c.mu.
func (c *cachingAggregator) evictExpiredLocked(now time.Time) {
	for k, e := range c.entries {
		if now.Sub(e.at) >= c.ttl {
			delete(c.entries, k)
		}
	}
}

// cacheKey derives a collision-resistant key from the inputs that drive backend enumeration:
// the caller's subject, the forwarded headers (passthrough credentials/scopes), and the
// backend ID set. Hashing keeps raw credential values out of the map keys.
func cacheKey(ctx context.Context, backends []vmcp.Backend) string {
	h := sha256.New()

	if id, ok := auth.IdentityFromContext(ctx); ok && id != nil {
		_, _ = io.WriteString(h, id.Subject)
	}
	_, _ = h.Write([]byte{0})

	fwd := headerforward.ForwardedHeadersFromContext(ctx)
	fwdKeys := make([]string, 0, len(fwd))
	for k := range fwd {
		fwdKeys = append(fwdKeys, k)
	}
	sort.Strings(fwdKeys)
	for _, k := range fwdKeys {
		_, _ = io.WriteString(h, k)
		_, _ = h.Write([]byte{'='})
		_, _ = io.WriteString(h, fwd[k])
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write([]byte{0})

	ids := make([]string, 0, len(backends))
	for _, b := range backends {
		ids = append(ids, b.ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		_, _ = io.WriteString(h, id)
		_, _ = h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil))
}

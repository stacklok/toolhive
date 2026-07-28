// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
)

// capabilityCacheMaxEntries bounds the per-identity capability cache so it cannot grow
// without limit (one entry per distinct identity + forwarded-credential + backend-set key).
// Beyond it, the LRU evicts the least-recently-used entry. 1024 distinct active keys per
// node is generous for a vMCP instance; tune if real workloads exceed it.
const capabilityCacheMaxEntries = 1024

// cachingAggregator wraps an Aggregator and memoizes AggregateCapabilities for a bounded
// TTL, so the Serve path does not re-sweep every backend's tools/list on every tool call.
//
// On the New/Serve path, core.CallTool/ReadResource re-derive the advertised view on every
// call (the core holds no per-session cache); without this, a session with M calls performs
// ~M+1 full backend sweeps instead of the legacy ~1-per-session. This decorator restores
// once-per-(identity, TTL) freshness without coupling the core or Serve to a cache — it sits
// transparently below the core, wrapping the Aggregator the core already calls.
//
// It delegates the cache mechanics (LRU eviction, size bounding, thread-safety) to
// hashicorp/golang-lru and adds only a lazy TTL check on read. The base (non-expirable) LRU
// is used deliberately: the expirable variant runs a perpetual background cleanup goroutine
// with no Stop, which a per-server cache would leak (the repo's tests run under goleak).
//
// Security: backend enumeration is identity-dependent — what each backend returns depends on
// the credential presented to it — so the cache key MUST include the identity and the
// forwarded credentials, not just the backend set. Keying on (subject, forwarded headers,
// backend IDs) ensures one caller's capability view is never served to another, while a
// single caller's view is shared across their sessions. The key is a SHA-256 digest, so raw
// credential values are not retained as map keys. The cache is node-local and never persisted
// (it would be a credentialed view in shared state otherwise).
type cachingAggregator struct {
	// Aggregator is the wrapped aggregator; embedding delegates every method except the
	// AggregateCapabilities override below.
	Aggregator

	ttl   time.Duration
	cache *lru.Cache[string, cacheEntry]
}

type cacheEntry struct {
	caps *AggregatedCapabilities
	at   time.Time
}

// NewCachingAggregator wraps next so AggregateCapabilities results are memoized per identity
// for ttl, backed by a size-bounded LRU. A ttl <= 0 disables caching (next is returned
// unwrapped) so a misconfiguration cannot silently serve permanently-stale capabilities. A
// nil next is returned as-is so the downstream nil-aggregator validation (core.New) still
// fires rather than being masked by a non-nil wrapper.
func NewCachingAggregator(next Aggregator, ttl time.Duration) Aggregator {
	if next == nil || ttl <= 0 {
		return next
	}
	cache, err := lru.New[string, cacheEntry](capabilityCacheMaxEntries)
	if err != nil {
		// lru.New only errors on a non-positive size, which is a positive constant here, so
		// this is unreachable; degrade to the uncached aggregator rather than panicking.
		return next
	}
	return &cachingAggregator{Aggregator: next, ttl: ttl, cache: cache}
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
	if e, ok := c.cache.Get(key); ok && time.Since(e.at) < c.ttl {
		return e.caps, nil
	}

	// Miss/expiry: sweep with the lock released (Get/Add are individually locked) so callers
	// with different keys are not serialized behind one backend sweep. Concurrent misses for
	// the same key may each sweep once (last writer wins) — acceptable for a cold/expired key.
	caps, err := c.Aggregator.AggregateCapabilities(ctx, backends)
	if err != nil {
		return nil, err
	}
	c.cache.Add(key, cacheEntry{caps: caps, at: time.Now()})
	return caps, nil
}

// Compile-time assertion: cachingAggregator implements CacheInvalidator.
var _ CacheInvalidator = (*cachingAggregator)(nil)

// InvalidateAll implements CacheInvalidator by purging every cached entry, so the
// next AggregateCapabilities call for any identity re-sweeps the backends rather
// than serving a cached view until its TTL expires. See CacheInvalidator's doc
// for why this is coarse (whole-cache) rather than per-backend.
func (c *cachingAggregator) InvalidateAll() {
	c.cache.Purge()
}

// cacheKey derives a collision-resistant key from the inputs that drive backend enumeration:
// the caller's subject, the forwarded headers (passthrough credentials/scopes), and the
// backend ID set. Hashing keeps raw credential values out of the cache keys.
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

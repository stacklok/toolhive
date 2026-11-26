// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"sort"
	"strconv"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

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
// hashicorp/golang-lru and adds a TTL check on read plus a prune of expired entries after
// every successful sweep. The base (non-expirable) LRU is used deliberately: the expirable
// variant runs a perpetual background cleanup goroutine with no Stop, which a per-server
// cache would leak (the repo's tests run under goleak). Pruning on the miss path instead
// stops a key that is never requested again from keeping its full capability view resident
// until LRU pressure evicts it, without a background goroutine.
//
// Concurrent misses for the same key are deduplicated with singleflight: a burst of
// requests from one caller runs one backend sweep and shares its result, rather than one
// full sweep (and one full capability copy) per request. See AggregateCapabilities for the
// cancellation semantics.
//
// Security: backend enumeration is identity-dependent — what each backend returns depends on
// the credential presented to it — so the cache key MUST include the identity and the
// forwarded credentials, not just the backend set. Keying on (subject, forwarded headers,
// backend IDs) ensures one caller's capability view is never served to another, while a
// single caller's view is shared across their sessions. The same key scopes the singleflight
// group, so a shared sweep only serves callers with the same identity, forwarded credentials
// and backend set. The key is a SHA-256 digest, so raw credential values are not retained as
// map keys. The cache is node-local and never persisted (it would be a credentialed view in
// shared state otherwise).
type cachingAggregator struct {
	// Aggregator is the wrapped aggregator; embedding delegates every method except the
	// AggregateCapabilities override below.
	Aggregator

	ttl    time.Duration
	now    func() time.Time
	cache  *lru.Cache[string, cacheEntry]
	flight singleflight.Group

	// mu guards gen and makes "check gen, then Add" (store) atomic with respect to
	// "bump gen, then Purge" (InvalidateAll). Without it, a sweep that started before an
	// InvalidateAll could pass its gen check, lose the CPU to InvalidateAll, and then Add
	// its pre-invalidation result into the freshly purged cache.
	mu sync.Mutex
	// gen counts InvalidateAll calls. A sweep records gen before calling the wrapped
	// aggregator and only caches its result if gen is unchanged when it finishes.
	gen uint64
}

type cacheEntry struct {
	caps *AggregatedCapabilities
	at   time.Time
}

// leaderContextError marks a shared sweep that failed because the context of the caller
// that started it (the singleflight leader) was cancelled or timed out. Callers that joined
// the flight with a live context must not inherit that failure; they run their own sweep.
type leaderContextError struct {
	err error
}

func (e *leaderContextError) Error() string { return e.err.Error() }

func (e *leaderContextError) Unwrap() error { return e.err }

// NewCachingAggregator wraps next so AggregateCapabilities results are memoized per identity
// for ttl, backed by a size-bounded LRU, with concurrent misses for the same key sharing one
// backend sweep. A ttl <= 0 disables caching (next is returned unwrapped) so a
// misconfiguration cannot silently serve permanently-stale capabilities. A nil next is
// returned as-is so the downstream nil-aggregator validation (core.New) still fires rather
// than being masked by a non-nil wrapper.
//
// core.New does not install this cache; only server.New wraps the configured aggregator
// with it. Embedders that build the core with core.New and serve it themselves must wrap
// their aggregator with NewCachingAggregator to get caching and burst deduplication, and
// must only do so if their aggregator's result depends on nothing in the context beyond the
// cache key inputs (the identity's subject, the forwarded headers and the backend IDs). The
// cache and the shared sweep hand one caller's result to every other caller with the same
// key.
func NewCachingAggregator(next Aggregator, ttl time.Duration) Aggregator {
	return newCachingAggregator(next, ttl, time.Now)
}

// AggregateCapabilities returns a cached view when a fresh entry exists for the caller's
// identity + forwarded credentials + backend set, and otherwise sweeps the backends (via the
// wrapped aggregator) and caches the result. Errors are never cached. The returned value is
// treated as immutable by the core (it derives fresh per-call routers from it), so the cached
// pointer is shared rather than deep-copied.
//
// Concurrent misses for the same key share one sweep (singleflight). The shared sweep runs
// with the context of the caller that started it (the leader); every caller waits on its own
// context, so a caller whose request is cancelled returns ctx.Err() promptly rather than
// waiting on someone else's sweep. If the shared sweep fails because the leader's context was
// cancelled or timed out, a caller whose own context is still live runs its own sweep with
// its own context instead of failing with the leader's cancellation. The same applies when
// the shared sweep succeeds after the leader's context ended: the wrapped aggregator may
// have skipped backends it could not reach in time, so a view built under a cancelled
// leader context is neither cached nor shared. That fallback is not deduplicated. The
// shared sweep deliberately keeps the leader's cancellation (no
// context.WithoutCancel): otherwise a hung backend would pin the flight and every later
// caller for the key would join it.
//
// A successful sweep also prunes every expired entry from the cache. A sweep that overlaps
// an InvalidateAll returns its result to its callers but does not cache it, and callers that
// miss after InvalidateAll start a new sweep rather than joining one that started before it.
func (c *cachingAggregator) AggregateCapabilities(
	ctx context.Context, backends []vmcp.Backend,
) (*AggregatedCapabilities, error) {
	key := cacheKey(ctx, backends)
	if caps, ok := c.lookup(key); ok {
		return caps, nil
	}

	// The generation is part of the flight key, so callers that miss after an InvalidateAll
	// never join a sweep that started before it.
	gen := c.generation()
	ch := c.flight.DoChan(flightKey(gen, key), func() (any, error) {
		return c.sharedSweep(ctx, key, gen, backends)
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err == nil {
			caps, _ := res.Val.(*AggregatedCapabilities)
			return caps, nil
		}
		var leaderErr *leaderContextError
		if !errors.As(res.Err, &leaderErr) {
			return nil, res.Err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// The leader's context ended but ours is live: sweep on our own context.
		return c.sweep(ctx, key, c.generation(), backends)
	}
}

// Compile-time assertion: cachingAggregator implements CacheInvalidator.
var _ CacheInvalidator = (*cachingAggregator)(nil)

// InvalidateAll implements CacheInvalidator by purging every cached entry, so the
// next AggregateCapabilities call for any identity re-sweeps the backends rather
// than serving a cached view until its TTL expires. See CacheInvalidator's doc
// for why this is coarse (whole-cache) rather than per-backend.
//
// It also bumps the cache generation, so a sweep already in flight when InvalidateAll
// runs cannot repopulate the cache with a pre-invalidation result, and callers that miss
// afterwards start a fresh sweep instead of joining the in-flight one.
func (c *cachingAggregator) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.cache.Purge()
}

// newCachingAggregator is NewCachingAggregator with an injectable clock.
func newCachingAggregator(next Aggregator, ttl time.Duration, now func() time.Time) Aggregator {
	if next == nil || ttl <= 0 {
		return next
	}
	cache, err := lru.New[string, cacheEntry](capabilityCacheMaxEntries)
	if err != nil {
		// lru.New only errors on a non-positive size, which is a positive constant here, so
		// this is unreachable; degrade to the uncached aggregator rather than panicking.
		return next
	}
	return &cachingAggregator{Aggregator: next, ttl: ttl, now: now, cache: cache}
}

// lookup returns the cached view for key if it exists and is younger than the TTL.
func (c *cachingAggregator) lookup(key string) (*AggregatedCapabilities, bool) {
	e, ok := c.cache.Get(key)
	if !ok || c.now().Sub(e.at) >= c.ttl {
		return nil, false
	}
	return e.caps, true
}

// generation returns the current invalidation generation.
func (c *cachingAggregator) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// sharedSweep is the singleflight body, run with the leader's ctx. It re-checks the cache
// first, so a caller that missed just before a previous flight stored its entry does not
// sweep again. A sweep error caused by the leader's own context ending is wrapped in
// leaderContextError so waiters with a live context know to sweep on their own context.
// A sweep that succeeds after the leader's context ended is treated the same way: its view
// may be partial, so it is neither cached (see sweep) nor shared with the waiters.
//
// DoChan runs this on its own goroutine and turns a panic into an unrecoverable crash of
// the whole process, so a panic in the wrapped aggregator is recovered here and returned as
// an error. Before deduplication the panic unwound the request goroutine only.
func (c *cachingAggregator) sharedSweep(
	ctx context.Context, key string, gen uint64, backends []vmcp.Backend,
) (val any, err error) {
	defer func() {
		if r := recover(); r != nil {
			// The returned error carries only the panic value, so log the stack
			// here; it is the only record of where the panic happened.
			slog.Error("capability aggregation panicked",
				"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			val = nil
			err = fmt.Errorf("capability aggregation panicked: %v", r)
		}
	}()

	if caps, ok := c.lookup(key); ok {
		return caps, nil
	}
	caps, err := c.sweep(ctx, key, gen, backends)
	if err != nil {
		if ctx.Err() != nil {
			// Any failure after the leader's context ended may come from it, even one
			// that is not a context error: DefaultAggregator skips each backend that
			// fails, cancellation included, and then reports that no backend answered.
			return nil, &leaderContextError{err: err}
		}
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		// The wrapped aggregator can succeed with a partial view when the context ends
		// mid-sweep (DefaultAggregator skips backends that fail, cancellation included), so
		// the result is not shared: waiters with a live context sweep on their own.
		return nil, &leaderContextError{err: ctxErr}
	}
	return caps, nil
}

// sweep calls the wrapped aggregator with ctx and, on success, caches the result under key
// (unless an InvalidateAll ran since gen was read) and prunes expired entries. Errors are
// returned uncached. A result produced while ctx was cancelled or timed out is returned to
// the caller but not cached, because the wrapped aggregator may have skipped the backends
// it could not reach before the context ended and returned a partial view without error.
func (c *cachingAggregator) sweep(
	ctx context.Context, key string, gen uint64, backends []vmcp.Backend,
) (*AggregatedCapabilities, error) {
	caps, err := c.Aggregator.AggregateCapabilities(ctx, backends)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return caps, nil
	}
	c.store(key, gen, caps)
	c.pruneExpired()
	return caps, nil
}

// store caches caps under key if no InvalidateAll has run since gen was read. Holding mu
// across the check and the Add keeps an InvalidateAll from landing between them.
func (c *cachingAggregator) store(key string, gen uint64, caps *AggregatedCapabilities) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return
	}
	c.cache.Add(key, cacheEntry{caps: caps, at: c.now()})
}

// pruneExpired removes every entry whose age is at least the TTL, so a key that is never
// requested again does not keep its capability view resident until LRU pressure evicts it.
// Peek is used so the walk does not disturb recency. The walk covers at most
// capabilityCacheMaxEntries entries and only runs after a successful sweep, which already
// costs a full backend round, so its overhead is negligible.
//
// The walk is not atomic with concurrent writers: an entry refreshed by another sweep
// between its Peek and its Remove may be dropped. That costs the key one extra sweep on its
// next call and never serves a wrong result.
func (c *cachingAggregator) pruneExpired() {
	now := c.now()
	for _, k := range c.cache.Keys() {
		if e, ok := c.cache.Peek(k); ok && now.Sub(e.at) >= c.ttl {
			c.cache.Remove(k)
		}
	}
}

// flightKey scopes a singleflight key to an invalidation generation.
func flightKey(gen uint64, key string) string {
	return strconv.FormatUint(gen, 10) + ":" + key
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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cache provides a generic, capacity-bounded cache with singleflight
// deduplication and per-hit liveness validation.
package cache

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"
)

// ErrExpired is returned by the check function passed to New to signal that a
// cached entry has definitively expired and should be evicted.
var ErrExpired = errors.New("cache entry expired")

// evictedEntry is a key/value pair buffered for deferred onEvict invocation.
type evictedEntry[K comparable, V any] struct {
	key K
	val V
}

// ValidatingCache is a node-local write-through cache backed by a
// capacity-bounded LRU map, with singleflight-deduplicated Get operations and
// lazy liveness validation on cache hit.
//
// Type parameter K is the key type (must be comparable).
// Type parameter V is the cached value type.
//
// The entire Get operation — cache hit validation and miss load — runs under a
// singleflight group so at most one operation executes concurrently per key.
// Concurrent callers for the same key share the result, coalescing both
// liveness checks and storage round-trips into a single operation per key.
//
// # onEvict runs off the cache lock
//
// The user onEvict may perform slow teardown (e.g. closing network
// connections). To keep that work off the shared cache lock, the LRU's own
// eviction callback only *buffers* each evicted entry (bufferEvicted); every
// mutating operation then drains the buffer and invokes the user onEvict via
// drainEvictions after releasing mu. onEvict therefore never runs while mu is
// held, so a slow onEvict never blocks Get/Set/RemoveMatching on other keys.
type ValidatingCache[K comparable, V any] struct {
	lruCache *lru.Cache[K, V]
	flight   singleflight.Group
	load     func(ctx context.Context, key K) (V, error)
	check    func(ctx context.Context, key K, val V) error
	onEvict  func(K, V)

	// mu serializes Set against the conditional eviction in getHit and the
	// per-entry eviction in RemoveMatching. check() runs outside the lock to
	// avoid holding it during I/O; the lock is only held for short
	// Peek+Remove / Add sequences, never across onEvict.
	mu sync.Mutex

	// evictMu guards pending. It is only ever acquired on its own (in
	// bufferEvicted and drainEvictions), never while another lock is being
	// acquired, so it introduces no lock-ordering cycle with mu even though
	// bufferEvicted may run while mu is held.
	evictMu sync.Mutex
	// pending holds entries evicted by the LRU but not yet handed to the user
	// onEvict. Drained (and the user onEvict invoked) by drainEvictions after
	// mu is released. Always nil when onEvict is nil.
	pending []evictedEntry[K, V]
}

// New creates a ValidatingCache with the given capacity and callbacks.
//
// capacity is the maximum number of entries; it must be >= 1. When the cache
// is full and a new entry must be stored, the least-recently-used entry is
// evicted first. Values less than 1 panic.
//
// load is called on a cache miss to restore the value; it must not be nil.
// check is called on every cache hit to confirm liveness. It receives both the
// key and the cached value so callers can inspect the value without a separate
// read. Returning ErrExpired evicts the entry; any other error is transient
// (cached value returned unchanged). It must not be nil.
// onEvict is called after any eviction (LRU or expiry); it may be nil. It is
// always invoked outside the cache lock (see the type doc), so it is safe for
// onEvict to perform slow teardown without blocking other cache operations.
func New[K comparable, V any](
	capacity int,
	load func(context.Context, K) (V, error),
	check func(context.Context, K, V) error,
	onEvict func(K, V),
) *ValidatingCache[K, V] {
	if capacity < 1 {
		panic(fmt.Sprintf("cache.New: capacity must be >= 1, got %d", capacity))
	}
	if load == nil {
		panic("cache.New: load must not be nil")
	}
	if check == nil {
		panic("cache.New: check must not be nil")
	}

	c := &ValidatingCache[K, V]{
		load:    load,
		check:   check,
		onEvict: onEvict,
	}

	// When onEvict is nil there is nothing to defer, so the LRU gets no
	// callback and evicted values are simply dropped. Otherwise the LRU
	// callback only buffers; drainEvictions runs the real onEvict off the lock.
	var lruEvict func(K, V)
	if onEvict != nil {
		lruEvict = c.bufferEvicted
	}
	lruCache, err := lru.NewWithEvict(capacity, lruEvict)
	if err != nil {
		// Only possible if size < 0, which we have already ruled out above.
		panic(fmt.Sprintf("cache.New: lru.NewWithEvict: %v", err))
	}
	c.lruCache = lruCache
	return c
}

// bufferEvicted is the LRU's eviction callback. The LRU fires it from Add/Remove
// while the caller may still hold mu (getHit, Set, RemoveMatching), so it must
// not run the user onEvict inline. It records the entry for drainEvictions to
// hand to onEvict after mu is released. Only installed when onEvict is non-nil.
func (c *ValidatingCache[K, V]) bufferEvicted(key K, val V) {
	c.evictMu.Lock()
	c.pending = append(c.pending, evictedEntry[K, V]{key: key, val: val})
	c.evictMu.Unlock()
}

// drainEvictions invokes the user onEvict for every entry buffered since the
// last drain. Callers MUST NOT hold mu, so onEvict runs off the cache lock. It
// is safe to call when onEvict is nil (nothing is ever buffered) and when there
// is nothing pending.
func (c *ValidatingCache[K, V]) drainEvictions() {
	if c.onEvict == nil {
		return
	}
	c.evictMu.Lock()
	pending := c.pending
	c.pending = nil
	c.evictMu.Unlock()

	for _, e := range pending {
		c.onEvict(e.key, e.val)
	}
}

// getHit validates a known-present cache entry and returns its value.
// If the entry has definitively expired it is removed (its eviction buffered
// for the caller to drain) and (zero, false) is returned. Transient check
// errors leave the entry in place and return the cached value.
func (c *ValidatingCache[K, V]) getHit(ctx context.Context, key K, val V) (V, bool) {
	if err := c.check(ctx, key, val); err != nil {
		if errors.Is(err, ErrExpired) {
			// check() ran outside the lock to avoid holding it during I/O.
			// Re-verify under the lock that the entry hasn't been replaced by a
			// concurrent Set before removing it; otherwise we would evict a
			// freshly-written value that the caller intended to keep.
			c.mu.Lock()
			if current, ok := c.lruCache.Peek(key); ok && sameEntry(current, val) {
				// Remove buffers the eviction; Get drains it after mu is released.
				c.lruCache.Remove(key)
			}
			c.mu.Unlock()
			var zero V
			return zero, false
		}
	}
	return val, true
}

// Get returns the value for key, loading it on a cache miss. The entire
// operation — cache hit validation and miss load — runs under a singleflight
// group so at most one operation executes concurrently per key. Concurrent
// callers for the same key share the result.
//
// ctx is forwarded to the load and check callbacks. When concurrent calls for
// the same key are coalesced by singleflight, only the first caller's context
// is forwarded; later callers' contexts are not used.
//
// On a cache hit the entry's liveness is validated via the check function
// provided to New: ErrExpired evicts the entry and falls through to load;
// transient errors return the cached value unchanged. On a cache miss, load
// is called to restore the value.
//
// The returned bool is false whenever the value is unavailable — either
// because load returned an error or because the key does not exist in the
// backing store. Callers cannot distinguish these two cases.
func (c *ValidatingCache[K, V]) Get(ctx context.Context, key K) (V, bool) {
	type result struct{ v V }
	// fmt.Sprint(key) is the singleflight key. For string keys this is
	// exact. For other types, distinct values with identical string
	// representations would be incorrectly coalesced — avoid non-string K
	// types unless their fmt.Sprint output is guaranteed unique.
	raw, err, _ := c.flight.Do(fmt.Sprint(key), func() (any, error) {
		// Cache hit path: validate liveness.
		if val, ok := c.lruCache.Get(key); ok {
			v, alive := c.getHit(ctx, key, val)
			if alive {
				return result{v: v}, nil
			}
			// Entry expired and evicted; fall through to load.
		}

		// Cache miss (or expired): load the value and store it.
		v, loadErr := c.load(ctx, key)
		if loadErr != nil {
			return nil, loadErr
		}

		// Guard against a concurrent Set that occurred while load() was running.
		// ContainsOrAdd stores only if absent; if a concurrent Set got in first,
		// their value wins and we return it instead.
		if alreadySet, _ := c.lruCache.ContainsOrAdd(key, v); alreadySet {
			if winner, ok := c.lruCache.Get(key); ok {
				// Winner confirmed: v is definitively discarded — release its
				// resources. v was never stored, so it is not buffered; invoke
				// onEvict directly (this runs outside mu, so it is off the lock).
				if c.onEvict != nil {
					c.onEvict(key, v)
				}
				return result{v: winner}, nil
			}
			// The concurrent winner was itself evicted by LRU pressure between
			// ContainsOrAdd and Get. Fall back to storing v — do NOT release v
			// since it has not been discarded and is still valid.
			c.lruCache.Add(key, v)
		}
		return result{v: v}, nil
	})
	// Drain any evictions buffered by getHit's Remove or the miss path's
	// ContainsOrAdd/Add (LRU capacity eviction). mu is not held here, so onEvict
	// runs off the lock. Coalesced singleflight waiters also drain; a drain that
	// finds nothing pending is a cheap no-op.
	c.drainEvictions()
	if err != nil {
		var zero V
		return zero, false
	}
	r, ok := raw.(result)
	return r.v, ok
}

// Set stores value under key, moving the entry to the MRU position. If the
// cache is at capacity, the least-recently-used entry is evicted first and
// onEvict is called for it — after mu is released, so a slow onEvict does not
// block concurrent cache operations.
func (c *ValidatingCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	c.lruCache.Add(key, value)
	c.mu.Unlock()
	c.drainEvictions()
}

// Len returns the number of entries currently in the cache.
func (c *ValidatingCache[K, V]) Len() int {
	return c.lruCache.Len()
}

// RemoveMatching evicts every entry for which pred reports true, invoking the
// user onEvict for each removed entry, and returns the number removed.
//
// pred runs under the cache lock (the same lock Set contends for) and must not
// call back into the cache. onEvict, however, runs via drainEvictions after all
// removals complete and the lock is released, so a slow onEvict (e.g. closing a
// hung backend connection) delays only the drain, never the shared cache lock:
// a concurrent Set/Get on any key proceeds while a slow teardown is in flight.
// This is intended for infrequent bulk eviction (e.g. reconciling sessions
// after a backend is removed from the registry), not a hot path.
func (c *ValidatingCache[K, V]) RemoveMatching(pred func(K, V) bool) int {
	// Snapshot the candidate keys under a short lock. Keys() returns a copy and
	// Peek does not update recency, so evaluating pred here leaves the LRU order
	// of surviving entries unchanged.
	c.mu.Lock()
	var candidates []K
	for _, key := range c.lruCache.Keys() {
		if val, ok := c.lruCache.Peek(key); ok && pred(key, val) {
			candidates = append(candidates, key)
		}
	}
	c.mu.Unlock()

	var removed int
	for _, key := range candidates {
		c.mu.Lock()
		// Re-check under the lock: a concurrent Set may have replaced the entry
		// with a value pred no longer selects, or another path may have evicted
		// it. Peek+pred both guards that race and keeps eviction correct.
		if val, ok := c.lruCache.Peek(key); ok && pred(key, val) {
			c.lruCache.Remove(key) // buffers the eviction; drained below
			removed++
		}
		c.mu.Unlock()
	}
	// Run onEvict for the removed entries off the lock, so a slow teardown of one
	// session never blocks Get/Set for the rest of the node.
	c.drainEvictions()
	return removed
}

// sameEntry reports whether a and b are the same cache entry.
// For pointer types it compares addresses (identity), so a concurrent Set that
// stores a distinct new value is never mistaken for the stale entry. For
// non-pointer types it falls back to reflect.DeepEqual, which is safe for all
// comparable and non-comparable types.
func sameEntry[V any](a, b V) bool {
	ra := reflect.ValueOf(any(a))
	if ra.IsValid() {
		switch ra.Kind() { //nolint:exhaustive
		case reflect.Pointer, reflect.UnsafePointer:
			rb := reflect.ValueOf(any(b))
			return rb.IsValid() && ra.Pointer() == rb.Pointer()
		}
	}
	return reflect.DeepEqual(a, b)
}

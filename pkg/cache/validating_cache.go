// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cache provides a generic, capacity-bounded cache with singleflight
// deduplication and per-hit liveness validation.
package cache

import (
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
type ValidatingCache[K comparable, V any] struct {
	lruCache *lru.Cache[K, V]
	flight   singleflight.Group
	load     func(key K) (V, error)
	check    func(key K, val V) error
	onEvict  func(K, V)
	// mu serializes Set against the conditional eviction in getHit.
	// check() runs outside the lock to avoid holding it during I/O; the lock
	// is only held for the short Peek+Remove sequence.
	mu sync.Mutex
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
// onEvict is called after any eviction (LRU or expiry); it may be nil.
func New[K comparable, V any](
	capacity int,
	load func(K) (V, error),
	check func(K, V) error,
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

	c, err := lru.NewWithEvict(capacity, onEvict)
	if err != nil {
		// Only possible if size < 0, which we have already ruled out above.
		panic(fmt.Sprintf("cache.New: lru.NewWithEvict: %v", err))
	}

	return &ValidatingCache[K, V]{
		lruCache: c,
		load:     load,
		check:    check,
		onEvict:  onEvict,
	}
}

// getHit validates a known-present cache entry and returns its value.
// If the entry has definitively expired it is evicted and (zero, false) is
// returned. Transient check errors leave the entry in place and return the
// cached value.
func (c *ValidatingCache[K, V]) getHit(key K, val V) (V, bool) {
	if err := c.check(key, val); err != nil {
		if errors.Is(err, ErrExpired) {
			// check() ran outside the lock to avoid holding it during I/O.
			// Re-verify under the lock that the entry hasn't been replaced by a
			// concurrent Set before removing it; otherwise we would evict a
			// freshly-written value that the caller intended to keep.
			c.mu.Lock()
			if current, ok := c.lruCache.Peek(key); ok && sameEntry(current, val) {
				// Remove fires the eviction callback automatically.
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
// On a cache hit the entry's liveness is validated via the check function
// provided to New: ErrExpired evicts the entry and falls through to load;
// transient errors return the cached value unchanged. On a cache miss, load
// is called to restore the value.
//
// The returned bool is false whenever the value is unavailable — either
// because load returned an error or because the key does not exist in the
// backing store. Callers cannot distinguish these two cases.
func (c *ValidatingCache[K, V]) Get(key K) (V, bool) {
	type result struct{ v V }
	// fmt.Sprint(key) is the singleflight key. For string keys this is
	// exact. For other types, distinct values with identical string
	// representations would be incorrectly coalesced — avoid non-string K
	// types unless their fmt.Sprint output is guaranteed unique.
	raw, err, _ := c.flight.Do(fmt.Sprint(key), func() (any, error) {
		// Cache hit path: validate liveness.
		if val, ok := c.lruCache.Get(key); ok {
			v, alive := c.getHit(key, val)
			if alive {
				return result{v: v}, nil
			}
			// Entry expired and evicted; fall through to load.
		}

		// Cache miss (or expired): load the value and store it.
		v, loadErr := c.load(key)
		if loadErr != nil {
			return nil, loadErr
		}

		// Guard against a concurrent Set that occurred while load() was running.
		// ContainsOrAdd stores only if absent; if a concurrent Set got in first,
		// their value wins and we return it instead.
		if alreadySet, _ := c.lruCache.ContainsOrAdd(key, v); alreadySet {
			if winner, ok := c.lruCache.Get(key); ok {
				// Winner confirmed: v is definitively discarded — release its resources.
				if c.onEvict != nil {
					c.onEvict(key, v)
				}
				return result{v: winner}, nil
			}
			// The concurrent winner was itself evicted by LRU pressure between
			// ContainsOrAdd and Get. Fall back to storing v — do NOT call onEvict
			// since v has not been released and is still valid.
			c.lruCache.Add(key, v)
		}
		return result{v: v}, nil
	})
	if err != nil {
		var zero V
		return zero, false
	}
	r, ok := raw.(result)
	return r.v, ok
}

// Set stores value under key, moving the entry to the MRU position. If the
// cache is at capacity, the least-recently-used entry is evicted first and
// onEvict is called for it.
func (c *ValidatingCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lruCache.Add(key, value)
}

// Len returns the number of entries currently in the cache.
func (c *ValidatingCache[K, V]) Len() int {
	return c.lruCache.Len()
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
		case reflect.Ptr, reflect.UnsafePointer:
			rb := reflect.ValueOf(any(b))
			return rb.IsValid() && ra.Pointer() == rb.Pointer()
		}
	}
	return reflect.DeepEqual(a, b)
}

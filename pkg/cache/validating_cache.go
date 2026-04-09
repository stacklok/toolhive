// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cache provides a generic, capacity-bounded cache with singleflight
// deduplication and per-hit liveness validation.
package cache

import (
	"container/list"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/singleflight"
)

// ErrExpired is returned by the check function passed to New to signal that a
// cached entry has definitively expired and should be evicted.
var ErrExpired = errors.New("cache entry expired")

// ValidatingCache is a node-local write-through cache backed by a
// capacity-bounded LRU map, with singleflight-deduplicated restore on cache
// miss and lazy liveness validation on cache hit.
//
// Type parameter K is the key type (must be comparable).
// Type parameter V is the cached value type.
//
// Every operation on V is fully typed; there are no sentinel markers or
// untyped any parameters. The no-resurrection invariant (preventing a
// concurrent restore from overwriting a deletion) is enforced at the storage
// layer via a conditional-write Update call rather than by a cache-level
// tombstone.
type ValidatingCache[K comparable, V any] struct {
	mu       sync.Mutex
	entries  map[K]*cacheEntry[K, V]
	lru      *list.List // front=MRU, back=LRU; elements hold *cacheEntry[K, V]
	capacity int        // 0 means unlimited

	flight singleflight.Group

	// load is called on a cache miss. Return (value, nil) on success.
	// A successful result is stored in the cache before being returned.
	load func(key K) (V, error)

	// check is called on every cache hit to confirm liveness. Returning nil
	// means the entry is alive. Returning ErrExpired means it has definitively
	// expired (the entry is evicted and onEvict is called). Any other error is
	// treated as a transient failure and the cached value is returned unchanged.
	check func(key K, val V) error

	// onEvict is called after a confirmed-expired or LRU-evicted entry has been
	// removed from the cache. It is always called outside the internal mutex.
	// The evicted key and value are passed to allow resource cleanup
	// (e.g. closing connections). May be nil.
	onEvict func(key K, v V)
}

// cacheEntry holds a single key-value pair and its position in the LRU list.
type cacheEntry[K comparable, V any] struct {
	key  K
	val  V
	elem *list.Element // pointer to this entry's position in ValidatingCache.lru
}

// New creates a ValidatingCache with the given capacity and callbacks.
//
// capacity is the maximum number of entries. When the cache is full and a new
// entry must be stored, the least-recently-used entry is evicted first.
// A capacity of 0 disables the limit (the cache grows without bound).
// Negative values panic.
//
// load is called on a cache miss to restore the value; it must not be nil.
// check is called on every cache hit to confirm liveness; it must not be nil.
// onEvict is called after any eviction (LRU or expiry); it may be nil.
func New[K comparable, V any](
	capacity int,
	load func(K) (V, error),
	check func(K, V) error,
	onEvict func(K, V),
) *ValidatingCache[K, V] {
	if capacity < 0 {
		panic(fmt.Sprintf("cache.New: capacity must be >= 0, got %d", capacity))
	}
	if load == nil {
		panic("cache.New: load must not be nil")
	}
	if check == nil {
		panic("cache.New: check must not be nil")
	}
	return &ValidatingCache[K, V]{
		entries:  make(map[K]*cacheEntry[K, V]),
		lru:      list.New(),
		capacity: capacity,
		load:     load,
		check:    check,
		onEvict:  onEvict,
	}
}

// getHit validates a known-present cache entry and returns its value.
// If the entry has definitively expired it is evicted and (zero, false) is
// returned. Transient check errors leave the entry in place and return the
// cached value.
func (c *ValidatingCache[K, V]) getHit(key K, e *cacheEntry[K, V]) (V, bool) {
	if err := c.check(key, e.val); err != nil {
		if errors.Is(err, ErrExpired) {
			var evicted bool
			c.mu.Lock()
			// Only evict if the entry hasn't been replaced by a concurrent writer.
			if cur, exists := c.entries[key]; exists && cur == e {
				c.evictEntryLocked(e)
				evicted = true
			}
			c.mu.Unlock()
			if evicted && c.onEvict != nil {
				c.onEvict(key, e.val)
			}
			var zero V
			return zero, false
		}
	}
	return e.val, true
}

// Get returns the value for key, loading it on a cache miss. On a cache hit
// the entry's liveness is validated via the check function provided to New:
// ErrExpired evicts the entry and returns (zero, false); transient errors
// return the cached value unchanged. On a cache miss, load is called under a
// singleflight group so at most one restore runs concurrently per key.
func (c *ValidatingCache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	e, hit := c.entries[key]
	if hit {
		c.lru.MoveToFront(e.elem)
	}
	c.mu.Unlock()

	if hit {
		return c.getHit(key, e)
	}

	// Cache miss: use singleflight to prevent concurrent restores for the same key.
	type result struct{ v V }
	raw, err, _ := c.flight.Do(fmt.Sprint(key), func() (any, error) {
		// Re-check the cache: a concurrent singleflight group may have stored
		// the value between our miss check above and acquiring this group.
		c.mu.Lock()
		if existing, ok := c.entries[key]; ok {
			c.lru.MoveToFront(existing.elem)
			v := existing.val
			c.mu.Unlock()
			return result{v: v}, nil
		}
		c.mu.Unlock()

		v, loadErr := c.load(key)
		if loadErr != nil {
			return nil, loadErr
		}

		// Guard against a concurrent Set/Delete that occurred while load() was
		// running. Store only if the key is still absent; if another writer got
		// in first, return their value and discard ours via onEvict.
		c.mu.Lock()
		if existing, exists := c.entries[key]; exists {
			winner := existing.val
			c.lru.MoveToFront(existing.elem)
			c.mu.Unlock()
			if c.onEvict != nil {
				c.onEvict(key, v)
			}
			return result{v: winner}, nil
		}
		evicted := c.storeLocked(key, v)
		c.mu.Unlock()

		if evicted != nil && c.onEvict != nil {
			c.onEvict(evicted.key, evicted.val)
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
	// If key already exists, update in place without changing capacity.
	if e, ok := c.entries[key]; ok {
		e.val = value
		c.lru.MoveToFront(e.elem)
		c.mu.Unlock()
		return
	}
	evicted := c.storeLocked(key, value)
	c.mu.Unlock()

	if evicted != nil && c.onEvict != nil {
		c.onEvict(evicted.key, evicted.val)
	}
}

// Len returns the number of entries currently in the cache.
func (c *ValidatingCache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// storeLocked inserts a new entry and returns the evicted LRU entry (or nil if
// no eviction was needed). Must be called with c.mu held.
func (c *ValidatingCache[K, V]) storeLocked(key K, value V) *cacheEntry[K, V] {
	var evicted *cacheEntry[K, V]
	if c.capacity > 0 && len(c.entries) >= c.capacity {
		evicted = c.evictLRULocked()
	}
	e := &cacheEntry[K, V]{key: key, val: value}
	e.elem = c.lru.PushFront(e)
	c.entries[key] = e
	return evicted
}

// evictEntryLocked removes the given entry from the map and LRU list.
// Must be called with c.mu held.
func (c *ValidatingCache[K, V]) evictEntryLocked(e *cacheEntry[K, V]) {
	c.lru.Remove(e.elem)
	delete(c.entries, e.key)
}

// evictLRULocked removes and returns the least-recently-used entry.
// Returns nil if the cache is empty. Must be called with c.mu held.
func (c *ValidatingCache[K, V]) evictLRULocked() *cacheEntry[K, V] {
	back := c.lru.Back()
	if back == nil {
		return nil
	}
	e := back.Value.(*cacheEntry[K, V])
	c.evictEntryLocked(e)
	return e
}

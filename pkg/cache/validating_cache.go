// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cache provides a generic, capacity-bounded cache with singleflight
// deduplication and per-hit liveness validation.
package cache

import (
	"errors"
	"fmt"

	lru "github.com/hashicorp/golang-lru/v2"
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
// The no-resurrection invariant (preventing a concurrent restore from
// overwriting a deletion) is enforced via ContainsOrAdd: if a concurrent
// writer stored a value between load() returning and the cache being updated,
// the prior writer's value wins and the just-loaded value is discarded via
// onEvict.
type ValidatingCache[K comparable, V any] struct {
	lruCache *lru.Cache[K, V]
	flight   singleflight.Group
	load     func(key K) (V, error)
	check    func(key K, val V) error
	// onEvict is kept here so we can call it when discarding a concurrently
	// loaded value that lost the race to a prior writer.
	onEvict func(key K, val V)
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
			// Remove fires the eviction callback automatically.
			c.lruCache.Remove(key)
			var zero V
			return zero, false
		}
	}
	return val, true
}

// Get returns the value for key, loading it on a cache miss. On a cache hit
// the entry's liveness is validated via the check function provided to New:
// ErrExpired evicts the entry and returns (zero, false); transient errors
// return the cached value unchanged. On a cache miss, load is called under a
// singleflight group so at most one restore runs concurrently per key.
func (c *ValidatingCache[K, V]) Get(key K) (V, bool) {
	if val, ok := c.lruCache.Get(key); ok {
		return c.getHit(key, val)
	}

	// Cache miss: use singleflight to prevent concurrent restores for the same key.
	type result struct{ v V }
	raw, err, _ := c.flight.Do(fmt.Sprint(key), func() (any, error) {
		// Re-check the cache: a concurrent singleflight group may have stored
		// the value between our miss check above and acquiring this group.
		if existing, ok := c.lruCache.Get(key); ok {
			return result{v: existing}, nil
		}

		v, loadErr := c.load(key)
		if loadErr != nil {
			return nil, loadErr
		}

		// Guard against a concurrent Set/Delete that occurred while load() was
		// running. ContainsOrAdd stores only if absent; if another writer got
		// in first, their value wins and we discard ours via onEvict.
		ok, _ := c.lruCache.ContainsOrAdd(key, v)
		if ok {
			// Another writer stored a value first; retrieve the winner's value.
			winner, _ := c.lruCache.Get(key)
			if c.onEvict != nil {
				c.onEvict(key, v)
			}
			return result{v: winner}, nil
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
	c.lruCache.Add(key, value)
}

// Len returns the number of entries currently in the cache.
func (c *ValidatingCache[K, V]) Len() int {
	return c.lruCache.Len()
}

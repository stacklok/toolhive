// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/singleflight"
)

// ErrExpired is returned by the check function passed to newRestorableCache to
// signal that a cached entry has definitively expired and should be evicted.
var ErrExpired = errors.New("cache entry expired")

// errSentinelFound is returned inside the singleflight load function when a
// non-V value (e.g. terminatedSentinel) is present in the map. Returning an
// error aborts the load and causes Get to return (zero, false), consistent
// with the behaviour of the initial-hit path that also returns (zero, false)
// for non-V values.
var errSentinelFound = errors.New("sentinel stored in cache")

// RestorableCache is a node-local write-through cache backed by a sync.Map,
// with singleflight-deduplicated restore on cache miss and lazy liveness
// validation on cache hit.
//
// Type parameter K is the key type (must be comparable).
// Type parameter V is the cached value type.
//
// Values are stored internally as any, which allows callers to place sentinel
// markers alongside V entries (e.g. a tombstone during teardown). Get performs
// a type assertion to V and treats non-V entries as "not found". Peek and
// Store expose raw any access for sentinel use.
type RestorableCache[K comparable, V any] struct {
	m      sync.Map
	flight singleflight.Group

	// load is called on a cache miss. Return (value, nil) on success.
	// A successful result is stored in the cache before being returned.
	load func(key K) (V, error)

	// check is called on every cache hit to confirm liveness. Returning nil
	// means the entry is alive. Returning ErrExpired means it has definitively
	// expired (the entry is evicted). Any other error is treated as a transient
	// failure and the cached value is returned unchanged.
	check func(key K) error

	// onEvict is called after a confirmed-expired entry has been removed. The
	// evicted value is passed to allow resource cleanup (e.g. closing
	// connections). May be nil.
	onEvict func(key K, v V)
}

// TODO: add an age-based sweep to bound the lifetime of entries that are
// never accessed again after their storage TTL expires. The sweep would range
// over m, compare each entry's insertion time against a caller-supplied maxAge,
// and call onEvict for entries that are too old — all without touching storage.
// Until then, entries for idle sessions leak backend connections until the
// process restarts or the session ID is queried again.

func newRestorableCache[K comparable, V any](
	load func(K) (V, error),
	check func(K) error,
	onEvict func(K, V),
) *RestorableCache[K, V] {
	return &RestorableCache[K, V]{
		load:    load,
		check:   check,
		onEvict: onEvict,
	}
}

// Get returns the cached V value for key.
//
// On a cache hit, check is run first: ErrExpired evicts the entry and returns
// (zero, false); transient errors return the cached value unchanged. Non-V
// values stored via Store (e.g. sentinels) return (zero, false) without
// triggering a restore.
//
// On a cache miss, load is called under a singleflight group so at most one
// restore runs concurrently per key.
func (c *RestorableCache[K, V]) Get(key K) (V, bool) {
	if raw, ok := c.m.Load(key); ok {
		v, isV := raw.(V)
		if !isV {
			var zero V
			return zero, false
		}
		if err := c.check(key); err != nil {
			if errors.Is(err, ErrExpired) {
				c.m.Delete(key)
				if c.onEvict != nil {
					c.onEvict(key, v)
				}
				var zero V
				return zero, false
			}
			// Transient error — keep the cached value.
		}
		return v, true
	}

	// Cache miss: use singleflight to prevent concurrent restores for the same key.
	type result struct{ v V }
	raw, err, _ := c.flight.Do(fmt.Sprint(key), func() (any, error) {
		// Re-check the cache: a concurrent singleflight group may have stored
		// the value between our miss check above and acquiring this group.
		if stored, ok := c.m.Load(key); ok {
			if v, isV := stored.(V); isV {
				return result{v: v}, nil
			}
			// Non-V sentinel present (e.g. terminatedSentinel). Treat as a
			// hard stop: do not call load() and do not overwrite the sentinel.
			return nil, errSentinelFound
		}
		v, loadErr := c.load(key)
		if loadErr != nil {
			return nil, loadErr
		}
		// Guard against a sentinel being stored between load() completing and
		// this Store call (Terminate() running concurrently). LoadOrStore is
		// atomic: if a sentinel got in, we discard the freshly loaded value
		// via onEvict rather than silently overwriting the sentinel.
		if _, loaded := c.m.LoadOrStore(key, v); loaded {
			if c.onEvict != nil {
				c.onEvict(key, v)
			}
			return nil, errSentinelFound
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

// Store sets key to value. value may be any type, including sentinel markers.
func (c *RestorableCache[K, V]) Store(key K, value any) {
	c.m.Store(key, value)
}

// Delete removes key from the cache.
func (c *RestorableCache[K, V]) Delete(key K) {
	c.m.Delete(key)
}

// Peek returns the raw value stored under key without type assertion, liveness
// check, or restore. Used for sentinel inspection.
func (c *RestorableCache[K, V]) Peek(key K) (any, bool) {
	return c.m.Load(key)
}

// CompareAndSwap atomically replaces the value stored under key from old to
// new. Both old and new may be any type, including sentinels.
func (c *RestorableCache[K, V]) CompareAndSwap(key K, old, replacement any) bool {
	return c.m.CompareAndSwap(key, old, replacement)
}

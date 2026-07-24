// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import "sync"

// keyedMutex provides per-key mutual exclusion: two callers locking the SAME
// key are fully serialized (the second blocks until the first releases),
// while callers locking DIFFERENT keys proceed concurrently, never blocking
// on each other. HTTPProxy uses it (as uriLocks) to order a
// resources/subscribe|unsubscribe ref-count decision (see
// sessionRouter.addSubscription/removeSubscription) atomically with the
// upstream forward it may trigger, per uri -- see handlePost and FIX 1 in
// #5744's review: without this, a concurrent last-unsubscribe and
// first-subscribe for the SAME uri from different sessions could reach the
// backend out of order, leaving the backend desynchronized from the routing
// table's ref count.
//
// Unlike sessionRouter's own mutexes (subMu et al.), which only ever guard a
// map lookup/mutation and are never held across a blocking call, a key's
// entry here IS deliberately held across a blocking upstream request/response
// round trip -- that is the whole point of this type. Do not reuse it to
// guard plain in-memory state that has no blocking call in its critical
// section; a plain mutex is simpler and sufficient there.
//
// Entries are ref-counted and removed once no caller holds or is waiting for
// them, bounding keyedMutex's memory footprint to the number of CURRENTLY
// in-flight keys rather than the number of keys ever seen (see the
// resource-leak style rule). This differs deliberately from, e.g.,
// pluginsvc's never-evicted pluginLock: that type's key cardinality is small
// and bounded by installed plugins, but uris here are arbitrary and
// client-supplied over the life of the proxy, so never evicting would leak
// memory.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedMutexEntry
}

// keyedMutexEntry is one key's mutex plus a count of callers that currently
// hold or are waiting to acquire it. The count lets lock's returned unlock
// func safely delete the map entry once it reaches zero, without racing a
// concurrent lock(key) call that is (or is about to be) waiting on the same
// entry -- see lock's doc comment for why this two-phase acquire (register
// intent under mu, then block on the entry's own mutex) is race-free.
type keyedMutexEntry struct {
	mu       sync.Mutex
	refCount int
}

// newKeyedMutex creates an empty keyedMutex.
func newKeyedMutex() *keyedMutex {
	return &keyedMutex{locks: make(map[string]*keyedMutexEntry)}
}

// lock acquires exclusive ownership of key, blocking until any other caller
// currently holding (or already waiting for) the same key releases it. It
// returns an unlock func the caller MUST call exactly once (typically via
// defer, so it runs on every return/error path) to release key and, if no
// other caller is currently holding or waiting for it, remove its entry.
//
// Acquisition is two-phase: first, under k.mu, the entry is looked up (or
// created) and its refCount incremented -- this is what makes a concurrent
// unlock's decrement-and-maybe-delete race-free, since any new lock(key)
// caller either observes the entry before it is deleted (and increments it,
// preventing deletion) or observes it already deleted (and creates a fresh
// one), never a stale, about-to-be-deleted entry. Second, k.mu is released
// and the caller blocks on the entry's own mutex, so holding one key never
// blocks callers locking a different key.
//
// lock is reentrancy-free: calling lock(key) again for the same key from the
// same goroutine before releasing the first would deadlock, exactly like a
// plain sync.Mutex -- callers must never nest calls for the same key.
//
// The returned unlock is wrapped in sync.Once as defense-in-depth: it must
// still be called, but an accidental double-call becomes a no-op instead of
// unlocking a mutex a different caller now holds (releasing someone else's
// critical section) and driving refCount negative (a spurious later eviction).
func (k *keyedMutex) lock(key string) (unlock func()) {
	k.mu.Lock()
	e, ok := k.locks[key]
	if !ok {
		e = &keyedMutexEntry{}
		k.locks[key] = e
	}
	e.refCount++
	k.mu.Unlock()

	e.mu.Lock()

	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Unlock()

			k.mu.Lock()
			e.refCount--
			if e.refCount == 0 {
				delete(k.locks, key)
			}
			k.mu.Unlock()
		})
	}
}

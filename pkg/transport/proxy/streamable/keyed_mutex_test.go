// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKeyedMutex_DifferentKeysProceedConcurrently pins the whole point of the
// type: two callers holding DIFFERENT keys must not block each other. A
// regression to a single global lock would still pass every integration test
// (just serialized), so this is asserted directly here.
func TestKeyedMutex_DifferentKeysProceedConcurrently(t *testing.T) {
	t.Parallel()
	km := newKeyedMutex()

	// Hold key "a" for the duration.
	unlockA := km.lock("a")
	t.Cleanup(unlockA)

	// Locking a different key must succeed while "a" is still held.
	got := make(chan func(), 1)
	go func() { got <- km.lock("b") }()

	select {
	case unlockB := <-got:
		unlockB()
	case <-time.After(2 * time.Second):
		t.Fatal("lock on a different key blocked behind an unrelated held key")
	}
}

// TestKeyedMutex_SameKeySerializes verifies mutual exclusion for the same key:
// a second lock blocks until the first releases.
func TestKeyedMutex_SameKeySerializes(t *testing.T) {
	t.Parallel()
	km := newKeyedMutex()

	unlock1 := km.lock("k")

	acquired := make(chan struct{})
	go func() {
		unlock2 := km.lock("k")
		close(acquired)
		unlock2()
	}()

	// Must not acquire while the first holder is active.
	select {
	case <-acquired:
		t.Fatal("second lock on the same key acquired before the first released")
	case <-time.After(100 * time.Millisecond):
	}

	unlock1()

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second lock on the same key never acquired after release")
	}
}

// TestKeyedMutex_EvictsAtZeroRefCount verifies the documented memory-leak
// guard: an entry is removed once no caller holds or is waiting for it. URIs
// are client-supplied and unbounded, so failing to evict would leak.
func TestKeyedMutex_EvictsAtZeroRefCount(t *testing.T) {
	t.Parallel()
	km := newKeyedMutex()

	unlock := km.lock("k")

	km.mu.Lock()
	_, present := km.locks["k"]
	km.mu.Unlock()
	require.True(t, present, "entry must exist while the key is held")

	unlock()

	km.mu.Lock()
	_, present = km.locks["k"]
	remaining := len(km.locks)
	km.mu.Unlock()
	assert.False(t, present, "entry must be evicted once refCount reaches 0")
	assert.Equal(t, 0, remaining, "no entries should remain after all locks are released")
}

// TestKeyedMutex_DoubleUnlockIsNoop verifies the sync.Once guard: calling the
// returned unlock more than once is a safe no-op — it must not panic, must not
// drive refCount negative, and must not release a lock a later caller holds.
func TestKeyedMutex_DoubleUnlockIsNoop(t *testing.T) {
	t.Parallel()
	km := newKeyedMutex()

	unlock := km.lock("k")
	unlock()
	assert.NotPanics(t, unlock, "second unlock must be a no-op, not a panic")

	// A fresh acquisition must see a clean refCount of exactly 1 (a stray
	// double-unlock decrementing a live entry would skew this and corrupt
	// eviction).
	u2 := km.lock("k")
	km.mu.Lock()
	rc := km.locks["k"].refCount
	km.mu.Unlock()
	assert.Equal(t, 1, rc, "refCount must reflect exactly one active holder")

	u2()
	km.mu.Lock()
	remaining := len(km.locks)
	km.mu.Unlock()
	assert.Equal(t, 0, remaining, "entry must be evicted after the fresh holder releases")
}

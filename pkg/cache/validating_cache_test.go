// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStringCache builds a ValidatingCache[string, string] for tests.
func newStringCache(
	load func(string) (string, error),
	check func(string, string) error,
	evict func(string, string),
) *ValidatingCache[string, string] {
	return New(1000, load, check, evict)
}

// alwaysAliveCheck returns a check function that always reports the entry as alive.
func alwaysAliveCheck(_ string, _ string) error { return nil }

// ---------------------------------------------------------------------------
// Construction invariants
// ---------------------------------------------------------------------------

func TestValidatingCache_New_PanicsOnZeroCapacity(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		New(0, func(_ string) (string, error) { return "", nil }, alwaysAliveCheck, nil)
	})
}

func TestValidatingCache_New_PanicsOnNegativeCapacity(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		New(-1, func(_ string) (string, error) { return "", nil }, alwaysAliveCheck, nil)
	})
}

func TestValidatingCache_New_PanicsOnNilLoad(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		New[string, string](1, nil, alwaysAliveCheck, nil)
	})
}

func TestValidatingCache_New_PanicsOnNilCheck(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		New(1, func(_ string) (string, error) { return "", nil }, nil, nil)
	})
}

// ---------------------------------------------------------------------------
// Cache miss / restore
// ---------------------------------------------------------------------------

func TestValidatingCache_CacheMiss_CallsLoad(t *testing.T) {
	t.Parallel()

	loaded := false
	c := newStringCache(
		func(key string) (string, error) {
			loaded = true
			return "value-" + key, nil
		},
		alwaysAliveCheck,
		nil,
	)

	v, ok := c.Get("k")
	require.True(t, ok)
	assert.Equal(t, "value-k", v)
	assert.True(t, loaded)
}

func TestValidatingCache_CacheMiss_StoresResult(t *testing.T) {
	t.Parallel()

	calls := 0
	c := newStringCache(
		func(_ string) (string, error) {
			calls++
			return "v", nil
		},
		alwaysAliveCheck,
		nil,
	)

	c.Get("k")
	c.Get("k")
	assert.Equal(t, 1, calls, "load should be called only once after caching")
}

func TestValidatingCache_CacheMiss_LoadError_ReturnsNotFound(t *testing.T) {
	t.Parallel()

	loadErr := errors.New("not found")
	c := newStringCache(
		func(_ string) (string, error) { return "", loadErr },
		alwaysAliveCheck,
		nil,
	)

	v, ok := c.Get("k")
	assert.False(t, ok)
	assert.Empty(t, v)
}

// ---------------------------------------------------------------------------
// Cache hit / liveness
// ---------------------------------------------------------------------------

func TestValidatingCache_CacheHit_AliveCheck_ReturnsCached(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(key string) (string, error) { return "loaded-" + key, nil },
		alwaysAliveCheck,
		nil,
	)
	c.Get("k") // prime the cache

	// Second Get should return cached value without calling load again.
	v, ok := c.Get("k")
	require.True(t, ok)
	assert.Equal(t, "loaded-k", v)
}

func TestValidatingCache_CacheHit_Expired_EvictsAndCallsOnEvict(t *testing.T) {
	t.Parallel()

	evictedKey := ""
	evictedVal := ""
	c := newStringCache(
		func(_ string) (string, error) { return "v", nil },
		func(_ string, _ string) error { return ErrExpired },
		func(key, val string) {
			evictedKey = key
			evictedVal = val
		},
	)
	c.Get("k") // prime the cache

	// With singleflight wrapping the full Get, an expired hit evicts the entry
	// and falls through to load within the same operation, returning the fresh value.
	v, ok := c.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v", v)
	assert.Equal(t, "k", evictedKey)
	assert.Equal(t, "v", evictedVal)
}

func TestValidatingCache_CacheHit_Expired_EntryRemovedFromCache(t *testing.T) {
	t.Parallel()

	calls := 0
	expired := false
	c := newStringCache(
		func(_ string) (string, error) {
			calls++
			return "v", nil
		},
		func(_ string, _ string) error {
			if expired {
				return ErrExpired
			}
			return nil
		},
		nil,
	)

	c.Get("k") // prime the cache; check returns alive
	expired = true
	c.Get("k") // check returns ErrExpired → evict
	expired = false
	c.Get("k") // cache miss again → load called

	assert.Equal(t, 2, calls, "load should be called twice: initial + after eviction")
}

func TestValidatingCache_CacheHit_TransientCheckError_ReturnsCached(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(_ string) (string, error) { return "v", nil },
		func(_ string, _ string) error { return errors.New("transient storage error") },
		nil,
	)
	c.Get("k") // prime the cache

	v, ok := c.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v", v, "transient check error should keep cached value")
}

// ---------------------------------------------------------------------------
// Set
// ---------------------------------------------------------------------------

func TestValidatingCache_Set_StoresValue(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(_ string) (string, error) { return "", errors.New("should not call load") },
		alwaysAliveCheck,
		nil,
	)

	c.Set("k", "v")

	v, ok := c.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v", v)
}

func TestValidatingCache_Set_UpdatesExisting(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(_ string) (string, error) { return "loaded", nil },
		alwaysAliveCheck,
		nil,
	)
	c.Get("k") // prime with "loaded"
	c.Set("k", "updated")

	v, ok := c.Get("k")
	require.True(t, ok)
	assert.Equal(t, "updated", v)
}

// ---------------------------------------------------------------------------
// LRU capacity
// ---------------------------------------------------------------------------

func TestValidatingCache_LRU_EvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	var evictedKeys []string
	var mu sync.Mutex

	// capacity=2: inserting a third entry evicts the LRU.
	c := New(2,
		func(key string) (string, error) { return "val-" + key, nil },
		alwaysAliveCheck,
		func(key, _ string) {
			mu.Lock()
			evictedKeys = append(evictedKeys, key)
			mu.Unlock()
		},
	)

	c.Get("a") // a=MRU
	c.Get("b") // b=MRU, a=LRU
	c.Get("c") // c=MRU, b, a=LRU → evicts a

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"a"}, evictedKeys, "LRU entry (a) should be evicted")

	// a is evicted; b and c remain.
	_, bPresent := c.Get("b")
	assert.True(t, bPresent)
	_, cPresent := c.Get("c")
	assert.True(t, cPresent)
}

func TestValidatingCache_LRU_GetRefreshesMRUPosition(t *testing.T) {
	t.Parallel()

	var evictedKeys []string
	var mu sync.Mutex

	c := New(2,
		func(key string) (string, error) { return "val-" + key, nil },
		alwaysAliveCheck,
		func(key, _ string) {
			mu.Lock()
			evictedKeys = append(evictedKeys, key)
			mu.Unlock()
		},
	)

	c.Get("a") // a loaded (MRU)
	c.Get("b") // b loaded (MRU), a=LRU
	c.Get("a") // a accessed → a becomes MRU, b=LRU
	c.Get("c") // c loaded → evicts b (LRU), not a

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"b"}, evictedKeys, "b should be evicted (LRU after a was re-accessed)")

	_, aPresent := c.Get("a")
	assert.True(t, aPresent, "a should still be in cache")
}

func TestValidatingCache_LRU_SetRefreshesMRUPosition(t *testing.T) {
	t.Parallel()

	var evictedKeys []string
	var mu sync.Mutex

	c := New(2,
		func(key string) (string, error) { return "val-" + key, nil },
		alwaysAliveCheck,
		func(key, _ string) {
			mu.Lock()
			evictedKeys = append(evictedKeys, key)
			mu.Unlock()
		},
	)

	c.Get("a")      // a=MRU
	c.Get("b")      // b=MRU, a=LRU
	c.Set("a", "x") // Set refreshes a to MRU; b becomes LRU
	c.Get("c")      // c loaded → evicts b

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"b"}, evictedKeys)
}

func TestValidatingCache_LRU_CapacityOne(t *testing.T) {
	t.Parallel()

	var evictedKeys []string
	var mu sync.Mutex

	c := New(1,
		func(key string) (string, error) { return "val-" + key, nil },
		alwaysAliveCheck,
		func(key, _ string) {
			mu.Lock()
			evictedKeys = append(evictedKeys, key)
			mu.Unlock()
		},
	)

	c.Get("a")
	c.Get("b") // evicts a
	c.Get("c") // evicts b

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"a", "b"}, evictedKeys)
}

func TestValidatingCache_LRU_LargeCapacityNoEviction(t *testing.T) {
	t.Parallel()

	const n = 100
	c := New(n+1,
		func(key string) (string, error) { return "val-" + key, nil },
		alwaysAliveCheck,
		func(key, _ string) {
			t.Errorf("unexpected eviction for key %s", key)
		},
	)

	for i := range n {
		c.Get(fmt.Sprintf("k%d", i))
	}
	assert.Equal(t, n, c.Len(), "no entries should be evicted when under capacity")
}

func TestValidatingCache_LRU_Len(t *testing.T) {
	t.Parallel()

	c := New(5,
		func(_ string) (string, error) { return "v", nil },
		alwaysAliveCheck,
		nil,
	)

	assert.Equal(t, 0, c.Len())
	c.Get("a")
	assert.Equal(t, 1, c.Len())
	c.Get("b")
	assert.Equal(t, 2, c.Len())
}

// ---------------------------------------------------------------------------
// Re-check inside singleflight (TOCTOU prevention)
// ---------------------------------------------------------------------------

// TestValidatingCache_Singleflight_SetBeforeLoadReturns verifies that when
// Set is called for a key before the in-flight load completes, the Set value
// wins: ContainsOrAdd does not overwrite the writer's value, and the caller
// receives the Set value.
func TestValidatingCache_Singleflight_SetBeforeLoadReturns(t *testing.T) {
	t.Parallel()

	var loadCount atomic.Int32

	// loadReached is closed once load has definitely started, so the test can
	// inject a concurrent Set before load returns.
	loadReached := make(chan struct{})
	allowReturn := make(chan struct{})

	c := newStringCache(
		func(_ string) (string, error) {
			close(loadReached) // signal: load is now in-flight
			<-allowReturn      // block until test injects the concurrent Set
			loadCount.Add(1)
			return "from-load", nil
		},
		alwaysAliveCheck,
		nil,
	)

	var (
		wg     sync.WaitGroup
		result string
		ok     bool
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, ok = c.Get("k")
	}()

	// Wait until load is definitely executing, then write via Set so that
	// ContainsOrAdd inside the miss path finds the key already present.
	<-loadReached
	c.Set("k", "external-value")
	close(allowReturn) // let load return "from-load"
	wg.Wait()

	require.True(t, ok)
	assert.Equal(t, "external-value", result, "Set value should win over concurrent load")
	assert.Equal(t, int32(1), loadCount.Load(), "load is called but its value is discarded")
}

// TestValidatingCache_Singleflight_DeduplicatesConcurrentLivenessChecks verifies
// that concurrent Gets on an expired entry coalesce into a single load call.
//
// Design: load blocks until all goroutines have signalled they are about to
// call Get. Because expired.Store(false) runs inside the singleflight callback
// (before it returns), goroutines that arrive late — after load() has already
// returned — find either:
//
//	(a) the singleflight still in progress (they join it and share the result), or
//	(b) a live entry in the cache (expired=false, check passes, no load needed).
//
// Either way loadCount == 1 is an invariant enforced by the implementation, not
// by timing luck.
func TestValidatingCache_Singleflight_DeduplicatesConcurrentLivenessChecks(t *testing.T) {
	t.Parallel()

	const goroutines = 10
	var (
		loadCount  atomic.Int32
		allStarted sync.WaitGroup
		wg         sync.WaitGroup
		results    = make([]string, goroutines)
		oks        = make([]bool, goroutines)
	)

	var expired atomic.Bool

	c := newStringCache(
		func(_ string) (string, error) {
			// Wait until every goroutine has signalled it is about to call Get.
			// allStarted.Done() is called before Get(), so this unblocks once
			// the goroutine scheduler has scheduled all callers — not necessarily
			// once they've all entered flight.Do. That is fine: goroutines
			// arriving after load() returns find a live entry (expired is cleared
			// below) and return early via the cache-hit path. loadCount = 1
			// either way.
			allStarted.Wait()
			loadCount.Add(1)
			expired.Store(false) // refresh: late arrivals see a live entry
			return "reloaded", nil
		},
		func(_ string, _ string) error {
			if expired.Load() {
				return ErrExpired
			}
			return nil
		},
		nil,
	)

	// Prime the cache with a live entry. allStarted has count 0 here, so
	// Wait() inside load returns immediately — no deadlock.
	_, ok := c.Get("k")
	require.True(t, ok)
	assert.Equal(t, int32(1), loadCount.Load())

	// Reset state: add the goroutine count first, then mark expired so load
	// will block waiting for goroutines to pile up.
	loadCount.Store(0)
	allStarted.Add(goroutines)
	expired.Store(true)

	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			allStarted.Done() // signal: about to call Get
			results[i], oks[i] = c.Get("k")
		}(i)
	}

	// Use the test deadline as a safeguard so a future refactor that breaks
	// the allStarted synchronisation causes a fast failure rather than a hang.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	deadline, ok := t.Deadline()
	if !ok {
		deadline = time.Now().Add(10 * time.Second)
	}
	select {
	case <-done:
	case <-time.After(time.Until(deadline)):
		t.Fatal("timed out waiting for goroutines — possible deadlock in load synchronisation")
	}

	assert.Equal(t, int32(1), loadCount.Load(), "concurrent expired-entry Gets should coalesce to a single load")
	for i := range goroutines {
		assert.True(t, oks[i], "all goroutines should get ok=true")
		assert.Equal(t, "reloaded", results[i])
	}
}

// ---------------------------------------------------------------------------
// Singleflight deduplication
// ---------------------------------------------------------------------------

func TestValidatingCache_Singleflight_DeduplicatesConcurrentMisses(t *testing.T) {
	t.Parallel()

	const goroutines = 10
	var (
		loadCount  atomic.Int32
		allStarted sync.WaitGroup
		wg         sync.WaitGroup
		results    = make([]string, goroutines)
		oks        = make([]bool, goroutines)
	)
	allStarted.Add(goroutines)

	c := newStringCache(
		func(_ string) (string, error) {
			// Block until all goroutines have signalled they are about to call
			// Get. While blocked the cache entry has not been stored, so
			// every goroutine that reaches the miss path is deduplicated via
			// singleflight.Do. Goroutines delayed past our return find the
			// stored value via the cache-hit path. Either way loadCount = 1.
			allStarted.Wait()
			loadCount.Add(1)
			return "v", nil
		},
		alwaysAliveCheck,
		nil,
	)

	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			allStarted.Done() // signal: about to call Get
			results[i], oks[i] = c.Get("k")
		}(i)
	}

	wg.Wait()

	assert.Equal(t, int32(1), loadCount.Load(), "load should be called exactly once")
	for i := range goroutines {
		assert.True(t, oks[i], "all goroutines should get ok=true")
		assert.Equal(t, "v", results[i])
	}
}

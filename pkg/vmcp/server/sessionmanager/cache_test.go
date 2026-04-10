// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentinel type used to test that non-V values stored via Store are
// invisible to Get without triggering a restore.
type testSentinel struct{}

// newStringCache builds a RestorableCache[string, string] for tests.
func newStringCache(
	load func(string) (string, error),
	check func(string) error,
	evict func(string, string),
) *RestorableCache[string, string] {
	return newRestorableCache(load, check, evict)
}

// alwaysAliveCheck returns a check function that always reports the entry as alive.
func alwaysAliveCheck(_ string) error { return nil }

// ---------------------------------------------------------------------------
// Cache miss / restore
// ---------------------------------------------------------------------------

func TestRestorableCache_CacheMiss_CallsLoad(t *testing.T) {
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

func TestRestorableCache_CacheMiss_StoresResult(t *testing.T) {
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

	c.Get("k") //nolint:errcheck
	c.Get("k") //nolint:errcheck
	assert.Equal(t, 1, calls, "load should be called only once after caching")
}

func TestRestorableCache_CacheMiss_LoadError_ReturnsNotFound(t *testing.T) {
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

func TestRestorableCache_CacheHit_AliveCheck_ReturnsCached(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(key string) (string, error) { return "loaded-" + key, nil },
		alwaysAliveCheck,
		nil,
	)
	c.Get("k") //nolint:errcheck // prime the cache

	// Second Get should return cached value without calling load again.
	v, ok := c.Get("k")
	require.True(t, ok)
	assert.Equal(t, "loaded-k", v)
}

func TestRestorableCache_CacheHit_Expired_EvictsAndCallsOnEvict(t *testing.T) {
	t.Parallel()

	evictedKey := ""
	evictedVal := ""
	c := newStringCache(
		func(_ string) (string, error) { return "v", nil },
		func(_ string) error { return ErrExpired },
		func(key, val string) {
			evictedKey = key
			evictedVal = val
		},
	)
	c.Get("k") //nolint:errcheck // prime the cache

	v, ok := c.Get("k")
	assert.False(t, ok)
	assert.Empty(t, v)
	assert.Equal(t, "k", evictedKey)
	assert.Equal(t, "v", evictedVal)
}

func TestRestorableCache_CacheHit_Expired_EntryRemovedFromCache(t *testing.T) {
	t.Parallel()

	calls := 0
	expired := false
	c := newStringCache(
		func(_ string) (string, error) {
			calls++
			return "v", nil
		},
		func(_ string) error {
			if expired {
				return ErrExpired
			}
			return nil
		},
		nil,
	)

	c.Get("k") //nolint:errcheck // prime the cache; check returns alive
	expired = true
	c.Get("k") //nolint:errcheck // check returns ErrExpired → evict
	expired = false
	c.Get("k") //nolint:errcheck // cache miss again → load called

	assert.Equal(t, 2, calls, "load should be called twice: initial + after eviction")
}

func TestRestorableCache_CacheHit_TransientCheckError_ReturnsCached(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(_ string) (string, error) { return "v", nil },
		func(_ string) error { return errors.New("transient storage error") },
		nil,
	)
	c.Get("k") //nolint:errcheck // prime the cache

	v, ok := c.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v", v, "transient check error should keep cached value")
}

// ---------------------------------------------------------------------------
// Sentinel / raw access
// ---------------------------------------------------------------------------

func TestRestorableCache_Sentinel_GetReturnsNotFound(t *testing.T) {
	t.Parallel()

	loadCalled := false
	c := newRestorableCache(
		func(_ string) (string, error) {
			loadCalled = true
			return "", errors.New("should not be called")
		},
		alwaysAliveCheck,
		nil,
	)

	c.Store("k", testSentinel{})

	v, ok := c.Get("k")
	assert.False(t, ok, "sentinel should not satisfy type assertion to V")
	assert.Empty(t, v)
	assert.False(t, loadCalled, "load should not be called when a sentinel is present")
}

func TestRestorableCache_Peek_ReturnsSentinel(t *testing.T) {
	t.Parallel()

	c := newRestorableCache(
		func(string) (string, error) { return "", nil },
		alwaysAliveCheck,
		nil,
	)

	c.Store("k", testSentinel{})

	raw, ok := c.Peek("k")
	require.True(t, ok)
	_, isSentinel := raw.(testSentinel)
	assert.True(t, isSentinel)
}

// TestRestorableCache_Sentinel_StoredDuringLoad verifies that a sentinel stored
// concurrently during load() is respected: load() should not overwrite the
// sentinel, and the loaded value should be discarded via onEvict.
func TestRestorableCache_Sentinel_StoredDuringLoad(t *testing.T) {
	t.Parallel()

	var evictedKeys []string
	var mu sync.Mutex

	sentinelReady := make(chan struct{})
	loadStarted := make(chan struct{})

	c := newRestorableCache(
		func(_ string) (string, error) {
			// Signal that load has started, then wait for the sentinel to be stored.
			close(loadStarted)
			<-sentinelReady
			return "loaded-value", nil
		},
		alwaysAliveCheck,
		func(key, _ string) {
			mu.Lock()
			evictedKeys = append(evictedKeys, key)
			mu.Unlock()
		},
	)

	done := make(chan struct{})
	go func() {
		defer close(done)
		v, ok := c.Get("k")
		// The sentinel should have blocked the store; Get returns not-found.
		assert.False(t, ok)
		assert.Empty(t, v)
	}()

	// Wait until load() has started, then inject a sentinel before it stores.
	<-loadStarted
	c.Store("k", testSentinel{})
	close(sentinelReady)
	<-done

	// The sentinel must still be in the cache (not overwritten by the loaded value).
	raw, ok := c.Peek("k")
	require.True(t, ok)
	_, isSentinel := raw.(testSentinel)
	assert.True(t, isSentinel, "sentinel must not be overwritten by the restore")

	// onEvict must have been called for the discarded loaded value.
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"k"}, evictedKeys, "loaded value must be evicted when sentinel is present")
}

// TestRestorableCache_Sentinel_BlocksRestoreViaInitialHit verifies that a
// sentinel already present in the cache when Get is called causes load() to be
// skipped and Get to return not-found. This exercises the initial-hit branch
// (the outer c.m.Load check), which short-circuits before entering the
// singleflight group.
//
// The singleflight re-check branch (c.m.Load inside flight.Do) has structurally
// identical logic: if the stored value is not a V, errSentinelFound is returned
// and load is not called. That branch cannot be targeted deterministically from
// outside without code instrumentation, because the re-check runs in the same
// goroutine as the initial miss with no synchronisation point between them.
// The sentinel-stored-during-load path (TestRestorableCache_Sentinel_StoredDuringLoad)
// and the LoadOrStore guard cover the concurrent-store window that follows.
func TestRestorableCache_Sentinel_BlocksRestoreViaInitialHit(t *testing.T) {
	t.Parallel()

	loadCalled := false
	c := newRestorableCache(
		func(_ string) (string, error) {
			loadCalled = true
			return "loaded", nil
		},
		alwaysAliveCheck,
		nil,
	)

	// Sentinel is present before Get is called: the initial c.m.Load hit path
	// returns (zero, false) without entering the singleflight group.
	c.Store("k", testSentinel{})

	v, ok := c.Get("k")
	assert.False(t, ok, "Get must return not-found when sentinel is present")
	assert.Empty(t, v)
	assert.False(t, loadCalled, "load must not be called when a sentinel is in the cache")
}

func TestRestorableCache_Peek_MissingKey_ReturnsFalse(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(string) (string, error) { return "", nil },
		alwaysAliveCheck,
		nil,
	)

	_, ok := c.Peek("absent")
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// CompareAndSwap
// ---------------------------------------------------------------------------

func TestRestorableCache_CompareAndSwap_Success(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(_ string) (string, error) { return "v1", nil },
		alwaysAliveCheck,
		nil,
	)
	c.Get("k") //nolint:errcheck // prime with "v1"

	swapped := c.CompareAndSwap("k", "v1", "v2")
	require.True(t, swapped)

	raw, ok := c.Peek("k")
	require.True(t, ok)
	assert.Equal(t, "v2", raw)
}

func TestRestorableCache_CompareAndSwap_WrongOld_Fails(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(_ string) (string, error) { return "v1", nil },
		alwaysAliveCheck,
		nil,
	)
	c.Get("k") //nolint:errcheck

	swapped := c.CompareAndSwap("k", "wrong", "v2")
	assert.False(t, swapped)
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestRestorableCache_Delete_RemovesEntry(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(_ string) (string, error) { return "v", nil },
		alwaysAliveCheck,
		nil,
	)
	c.Get("k") //nolint:errcheck

	c.Delete("k")

	_, ok := c.Peek("k")
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// Re-check inside singleflight (TOCTOU prevention)
// ---------------------------------------------------------------------------

func TestRestorableCache_Singleflight_ReCheckReturnsPreStoredValue(t *testing.T) {
	t.Parallel()

	// Simulate the TOCTOU window: a goroutine sees a cache miss, then the
	// value is stored externally before it enters the singleflight group.
	// The re-check inside the group should find the value and skip load.
	var loadCount atomic.Int32

	// The load function is gated: it waits until we signal that an external
	// Store has been applied, mimicking a value written by another goroutine
	// between the miss check and the singleflight group.
	storeApplied := make(chan struct{})

	c := newStringCache(
		func(_ string) (string, error) {
			<-storeApplied // wait until external Store is applied
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

	// Store the value externally to simulate a concurrent writer, then release
	// the load function. The re-check at the top of the singleflight function
	// fires first and finds "external-value", so load is never called.
	c.Store("k", "external-value")
	close(storeApplied)
	wg.Wait()

	require.True(t, ok)
	assert.Equal(t, "external-value", result)
	assert.Equal(t, int32(0), loadCount.Load(), "re-check should short-circuit before load is called")
}

// ---------------------------------------------------------------------------
// Singleflight deduplication
// ---------------------------------------------------------------------------

func TestRestorableCache_Singleflight_DeduplicatesConcurrentMisses(t *testing.T) {
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

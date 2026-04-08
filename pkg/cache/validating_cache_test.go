// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStringCache builds a ValidatingCache[string, string] for tests.
func newStringCache(
	load func(string) (string, error),
	check func(string) error,
	evict func(string, string),
) *ValidatingCache[string, string] {
	return New(0, load, check, evict)
}

// alwaysAliveCheck returns a check function that always reports the entry as alive.
func alwaysAliveCheck(_ string) error { return nil }

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

	c.Get("k") //nolint:errcheck
	c.Get("k") //nolint:errcheck
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
	c.Get("k") //nolint:errcheck // prime the cache

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

func TestValidatingCache_CacheHit_Expired_EntryRemovedFromCache(t *testing.T) {
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

func TestValidatingCache_CacheHit_TransientCheckError_ReturnsCached(t *testing.T) {
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
// Set / Delete / Peek / CompareAndSwap
// ---------------------------------------------------------------------------

func TestValidatingCache_Set_StoresValue(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(_ string) (string, error) { return "", errors.New("should not call load") },
		alwaysAliveCheck,
		nil,
	)

	c.Set("k", "v")

	v, ok := c.Peek("k")
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
	c.Get("k") //nolint:errcheck // prime with "loaded"
	c.Set("k", "updated")

	v, ok := c.Peek("k")
	require.True(t, ok)
	assert.Equal(t, "updated", v)
}

func TestValidatingCache_Delete_RemovesEntry(t *testing.T) {
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

func TestValidatingCache_Peek_MissingKey_ReturnsFalse(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(string) (string, error) { return "", nil },
		alwaysAliveCheck,
		nil,
	)

	_, ok := c.Peek("absent")
	assert.False(t, ok)
}

func TestValidatingCache_CompareAndSwap_Success(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(_ string) (string, error) { return "v1", nil },
		alwaysAliveCheck,
		nil,
	)
	c.Get("k") //nolint:errcheck // prime with "v1"

	swapped := c.CompareAndSwap("k", "v1", "v2")
	require.True(t, swapped)

	v, ok := c.Peek("k")
	require.True(t, ok)
	assert.Equal(t, "v2", v)
}

func TestValidatingCache_CompareAndSwap_WrongOld_Fails(t *testing.T) {
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

func TestValidatingCache_CompareAndSwap_MissingKey_Fails(t *testing.T) {
	t.Parallel()

	c := newStringCache(
		func(_ string) (string, error) { return "", errors.New("not found") },
		alwaysAliveCheck,
		nil,
	)

	swapped := c.CompareAndSwap("absent", "old", "new")
	assert.False(t, swapped)
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

	c.Get("a") //nolint:errcheck // a=MRU
	c.Get("b") //nolint:errcheck // b=MRU, a=LRU
	c.Get("c") //nolint:errcheck // c=MRU, b, a=LRU → evicts a

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"a"}, evictedKeys, "LRU entry (a) should be evicted")

	// a is evicted; b and c remain.
	_, aPresent := c.Peek("a")
	assert.False(t, aPresent, "a should have been evicted")
	_, bPresent := c.Peek("b")
	assert.True(t, bPresent)
	_, cPresent := c.Peek("c")
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

	c.Get("a") //nolint:errcheck // a loaded (MRU)
	c.Get("b") //nolint:errcheck // b loaded (MRU), a=LRU
	c.Get("a") //nolint:errcheck // a accessed → a becomes MRU, b=LRU
	c.Get("c") //nolint:errcheck // c loaded → evicts b (LRU), not a

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"b"}, evictedKeys, "b should be evicted (LRU after a was re-accessed)")

	_, aPresent := c.Peek("a")
	assert.True(t, aPresent, "a should still be in cache")
	_, bPresent := c.Peek("b")
	assert.False(t, bPresent, "b should have been evicted")
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

	c.Get("a")      //nolint:errcheck // a=MRU
	c.Get("b")      //nolint:errcheck // b=MRU, a=LRU
	c.Set("a", "x") // Set refreshes a to MRU; b becomes LRU
	c.Get("c")      //nolint:errcheck // c loaded → evicts b

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

	c.Get("a") //nolint:errcheck
	c.Get("b") //nolint:errcheck // evicts a
	c.Get("c") //nolint:errcheck // evicts b

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"a", "b"}, evictedKeys)
}

func TestValidatingCache_LRU_UnlimitedCapacity(t *testing.T) {
	t.Parallel()

	const n = 100
	c := New(0, // 0 = unlimited
		func(key string) (string, error) { return "val-" + key, nil },
		alwaysAliveCheck,
		func(key, _ string) {
			t.Errorf("unexpected eviction for key %s", key)
		},
	)

	for i := range n {
		c.Get(fmt.Sprintf("k%d", i)) //nolint:errcheck
	}
	assert.Equal(t, n, c.Len(), "all entries should be present with unlimited capacity")
}

func TestValidatingCache_LRU_Len(t *testing.T) {
	t.Parallel()

	c := New(5,
		func(_ string) (string, error) { return "v", nil },
		alwaysAliveCheck,
		nil,
	)

	assert.Equal(t, 0, c.Len())
	c.Get("a") //nolint:errcheck
	assert.Equal(t, 1, c.Len())
	c.Get("b") //nolint:errcheck
	assert.Equal(t, 2, c.Len())
	c.Delete("a")
	assert.Equal(t, 1, c.Len())
}

// ---------------------------------------------------------------------------
// Re-check inside singleflight (TOCTOU prevention)
// ---------------------------------------------------------------------------

func TestValidatingCache_Singleflight_ReCheckReturnsPreStoredValue(t *testing.T) {
	t.Parallel()

	var loadCount atomic.Int32

	// The load function is gated: it waits until we signal that an external
	// Set has been applied, mimicking a value written by another goroutine
	// between the miss check and the singleflight group.
	storeApplied := make(chan struct{})

	c := newStringCache(
		func(_ string) (string, error) {
			<-storeApplied // wait until external Set is applied
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

	// Set the value externally to simulate a concurrent writer, then release
	// the load function. The re-check at the top of the singleflight function
	// fires first and finds "external-value", so load is never called.
	c.Set("k", "external-value")
	close(storeApplied)
	wg.Wait()

	require.True(t, ok)
	assert.Equal(t, "external-value", result)
	assert.Equal(t, int32(0), loadCount.Load(), "re-check should short-circuit before load is called")
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

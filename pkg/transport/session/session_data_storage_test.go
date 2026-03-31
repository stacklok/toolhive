// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// runDataStorageTests runs the DataStorage contract tests against any
// DataStorage implementation. Both LocalSessionDataStorage and
// RedisSessionDataStorage must pass the same suite.
func runDataStorageTests(t *testing.T, newStorage func(t *testing.T) DataStorage) {
	t.Helper()

	t.Run("Store and Load round-trip", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		meta := map[string]string{"key": "value", "env": "test"}
		require.NoError(t, s.Store(ctx, "sess-1", meta))

		loaded, err := s.Load(ctx, "sess-1")
		require.NoError(t, err)
		assert.Equal(t, "value", loaded["key"])
		assert.Equal(t, "test", loaded["env"])
	})

	t.Run("Store overwrites existing entry", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "sess-upsert", map[string]string{"v": "1"}))
		require.NoError(t, s.Store(ctx, "sess-upsert", map[string]string{"v": "2"}))

		loaded, err := s.Load(ctx, "sess-upsert")
		require.NoError(t, err)
		assert.Equal(t, "2", loaded["v"])
	})

	t.Run("Store nil metadata is treated as empty map", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "sess-nil-meta", nil))
		loaded, err := s.Load(ctx, "sess-nil-meta")
		require.NoError(t, err)
		assert.NotNil(t, loaded)
	})

	t.Run("Store with empty ID returns error", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		err := s.Store(ctx, "", map[string]string{})
		assert.Error(t, err)
	})

	t.Run("Load miss returns ErrSessionNotFound", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		_, err := s.Load(ctx, "does-not-exist")
		assert.ErrorIs(t, err, ErrSessionNotFound)
	})

	t.Run("Load with empty ID returns error", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		_, err := s.Load(ctx, "")
		assert.Error(t, err)
	})

	t.Run("Exists returns true for present key", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "sess-exists", map[string]string{}))

		ok, err := s.Exists(ctx, "sess-exists")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("Exists returns false for absent key", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		ok, err := s.Exists(ctx, "sess-absent")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("Exists with empty ID returns error", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		_, err := s.Exists(ctx, "")
		assert.Error(t, err)
	})

	t.Run("StoreIfAbsent creates entry when absent", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		stored, err := s.StoreIfAbsent(ctx, "sess-new", map[string]string{"x": "1"})
		require.NoError(t, err)
		assert.True(t, stored, "should return true when entry was created")

		loaded, err := s.Load(ctx, "sess-new")
		require.NoError(t, err)
		assert.Equal(t, "1", loaded["x"])
	})

	t.Run("StoreIfAbsent does not overwrite existing entry", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "sess-existing", map[string]string{"x": "original"}))

		stored, err := s.StoreIfAbsent(ctx, "sess-existing", map[string]string{"x": "overwrite"})
		require.NoError(t, err)
		assert.False(t, stored, "should return false when entry already existed")

		loaded, err := s.Load(ctx, "sess-existing")
		require.NoError(t, err)
		assert.Equal(t, "original", loaded["x"], "original value must be preserved")
	})

	t.Run("StoreIfAbsent is atomic under concurrent callers", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		type result struct {
			stored bool
			err    error
		}
		const goroutines = 20
		results := make(chan result, goroutines)
		for i := range goroutines {
			go func(val string) {
				stored, err := s.StoreIfAbsent(ctx, "concurrent-key", map[string]string{"winner": val})
				results <- result{stored: stored, err: err}
			}(fmt.Sprintf("contender-%d", i))
		}

		var winCount int
		for range goroutines {
			r := <-results
			require.NoError(t, r.err)
			if r.stored {
				winCount++
			}
		}
		assert.Equal(t, 1, winCount, "exactly one goroutine should have stored the entry")

		loaded, err := s.Load(ctx, "concurrent-key")
		require.NoError(t, err)
		assert.NotEmpty(t, loaded["winner"], "stored value must be one of the contenders")
	})

	t.Run("StoreIfAbsent with empty ID returns error", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		_, err := s.StoreIfAbsent(ctx, "", map[string]string{})
		assert.Error(t, err)
	})

	t.Run("Delete removes entry; subsequent Load returns ErrSessionNotFound", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "sess-del", map[string]string{}))
		require.NoError(t, s.Delete(ctx, "sess-del"))

		_, err := s.Load(ctx, "sess-del")
		assert.ErrorIs(t, err, ErrSessionNotFound)
	})

	t.Run("Delete is idempotent for absent key", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		err := s.Delete(ctx, "sess-never-stored")
		assert.NoError(t, err)
	})

	t.Run("Delete with empty ID returns error", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		err := s.Delete(ctx, "")
		assert.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// LocalSessionDataStorage
// ---------------------------------------------------------------------------

func TestLocalSessionDataStorage(t *testing.T) {
	t.Parallel()

	newLocal := func(t *testing.T) DataStorage {
		t.Helper()
		s, err := NewLocalSessionDataStorage(time.Hour)
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		return s
	}

	runDataStorageTests(t, newLocal)

	t.Run("TTL eviction removes idle entries", func(t *testing.T) {
		t.Parallel()
		const ttl = time.Hour
		s, err := NewLocalSessionDataStorage(ttl)
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "ttl-sess", map[string]string{"k": "v"}))
		backdateLocalEntry(t, s, "ttl-sess", ttl+time.Millisecond)
		s.deleteExpired()

		_, err = s.Load(ctx, "ttl-sess")
		assert.ErrorIs(t, err, ErrSessionNotFound, "idle session should be evicted after TTL")
	})

	t.Run("Load refreshes TTL so active entries survive eviction", func(t *testing.T) {
		t.Parallel()
		const ttl = time.Hour
		s, err := NewLocalSessionDataStorage(ttl)
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "active-sess", map[string]string{}))
		backdateLocalEntry(t, s, "active-sess", ttl+time.Millisecond)

		// Load should refresh the entry's timestamp.
		_, err = s.Load(ctx, "active-sess")
		require.NoError(t, err)

		// Eviction run should not remove the entry because Load just refreshed it.
		s.deleteExpired()

		_, err = s.Load(ctx, "active-sess")
		assert.NoError(t, err, "actively loaded session should not be evicted")
	})

	t.Run("Exists does not refresh TTL", func(t *testing.T) {
		t.Parallel()
		const ttl = time.Hour
		s, err := NewLocalSessionDataStorage(ttl)
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "probe-sess", map[string]string{}))
		backdateLocalEntry(t, s, "probe-sess", ttl+time.Millisecond)

		// Exists must not refresh the timestamp.
		ok, err := s.Exists(ctx, "probe-sess")
		require.NoError(t, err)
		require.True(t, ok)

		// Eviction run should remove the entry because Exists did not refresh it.
		s.deleteExpired()

		_, err = s.Load(ctx, "probe-sess")
		assert.ErrorIs(t, err, ErrSessionNotFound, "Exists should not have extended the TTL")
	})
}

// backdateLocalEntry moves the last-access timestamp of id back by age,
// simulating an entry that has been idle for that duration.
func backdateLocalEntry(t *testing.T, s *LocalSessionDataStorage, id string, age time.Duration) {
	t.Helper()
	val, ok := s.sessions.Load(id)
	require.True(t, ok, "entry %q not found for backdating", id)
	val.(*localDataEntry).lastAccessNano.Store(time.Now().Add(-age).UnixNano())
}

// ---------------------------------------------------------------------------
// RedisSessionDataStorage
// ---------------------------------------------------------------------------

func newTestRedisDataStorage(t *testing.T) (*RedisSessionDataStorage, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := &RedisSessionDataStorage{
		client:    client,
		keyPrefix: "test:data:",
		ttl:       30 * time.Minute,
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, mr
}

func TestRedisSessionDataStorage(t *testing.T) {
	t.Parallel()

	newRedis := func(t *testing.T) DataStorage {
		t.Helper()
		s, _ := newTestRedisDataStorage(t)
		return s
	}

	runDataStorageTests(t, newRedis)

	t.Run("Load refreshes TTL via GETEX", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisDataStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "ttl-refresh", map[string]string{}))
		mr.FastForward(29 * time.Minute)

		_, err := s.Load(ctx, "ttl-refresh")
		require.NoError(t, err, "load should succeed before expiry")

		// Advance past the original TTL; load should have reset the clock.
		mr.FastForward(2 * time.Minute)

		_, err = s.Load(ctx, "ttl-refresh")
		assert.NoError(t, err, "session should still be alive after TTL reset by Load")
	})

	t.Run("Exists does not refresh TTL", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisDataStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "no-refresh", map[string]string{}))
		mr.FastForward(29 * time.Minute)

		ok, err := s.Exists(ctx, "no-refresh")
		require.NoError(t, err)
		require.True(t, ok)

		// Advance past TTL without any Load; Exists should not have reset the clock.
		mr.FastForward(2 * time.Minute)

		_, err = s.Load(ctx, "no-refresh")
		assert.ErrorIs(t, err, ErrSessionNotFound, "Exists should not have refreshed the TTL")
	})

	t.Run("Key expires after TTL when not refreshed", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisDataStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "expiring", map[string]string{}))
		mr.FastForward(31 * time.Minute)

		_, err := s.Load(ctx, "expiring")
		assert.ErrorIs(t, err, ErrSessionNotFound)
	})

	t.Run("StoreIfAbsent uses SET NX — key format is {prefix}{id}", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisDataStorage(t)
		ctx := context.Background()

		stored, err := s.StoreIfAbsent(ctx, "nx-test", map[string]string{"a": "b"})
		require.NoError(t, err)
		require.True(t, stored)

		val, err := mr.Get("test:data:nx-test")
		require.NoError(t, err)
		assert.NotEmpty(t, val)
	})
}

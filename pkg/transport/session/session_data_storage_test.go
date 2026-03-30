// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
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
		s := NewLocalSessionDataStorage(time.Hour)
		t.Cleanup(func() { _ = s.Close() })
		return s
	}

	runDataStorageTests(t, newLocal)

	t.Run("TTL eviction removes idle entries", func(t *testing.T) {
		t.Parallel()
		s := NewLocalSessionDataStorage(50 * time.Millisecond)
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "ttl-sess", map[string]string{"k": "v"}))

		// Background cleanup runs at TTL/2 (~25ms). Wait long enough for it to fire.
		time.Sleep(200 * time.Millisecond)

		_, err := s.Load(ctx, "ttl-sess")
		assert.ErrorIs(t, err, ErrSessionNotFound, "idle session should be evicted after TTL")
	})

	t.Run("Load refreshes TTL so active entries survive cleanup", func(t *testing.T) {
		t.Parallel()
		ttl := 100 * time.Millisecond
		s := NewLocalSessionDataStorage(ttl)
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "active-sess", map[string]string{}))

		// Repeatedly load to keep the entry fresh, then verify it outlives the TTL.
		for range 3 {
			time.Sleep(ttl / 3)
			_, err := s.Load(ctx, "active-sess")
			require.NoError(t, err)
		}

		// Entry should still be present because each Load refreshed it.
		_, err := s.Load(ctx, "active-sess")
		assert.NoError(t, err, "actively loaded session should not be evicted")
	})

	t.Run("Exists does not refresh TTL", func(t *testing.T) {
		t.Parallel()
		ttl := 60 * time.Millisecond
		s := NewLocalSessionDataStorage(ttl)
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()

		require.NoError(t, s.Store(ctx, "probe-sess", map[string]string{}))

		// Call Exists only (no Load) and wait for the entry to age past TTL.
		for range 3 {
			time.Sleep(ttl / 4)
			ok, err := s.Exists(ctx, "probe-sess")
			require.NoError(t, err)
			require.True(t, ok)
		}

		// Give the background cleanup goroutine time to evict it.
		time.Sleep(ttl * 2)

		_, err := s.Load(ctx, "probe-sess")
		assert.ErrorIs(t, err, ErrSessionNotFound, "Exists should not have extended the TTL")
	})
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

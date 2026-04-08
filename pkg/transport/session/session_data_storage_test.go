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

	t.Run("Upsert and Load round-trip", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		meta := map[string]string{"key": "value", "env": "test"}
		require.NoError(t, s.Upsert(ctx, "sess-1", meta))

		loaded, err := s.Load(ctx, "sess-1")
		require.NoError(t, err)
		assert.Equal(t, "value", loaded["key"])
		assert.Equal(t, "test", loaded["env"])
	})

	t.Run("Upsert overwrites existing entry", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Upsert(ctx, "sess-upsert", map[string]string{"v": "1"}))
		require.NoError(t, s.Upsert(ctx, "sess-upsert", map[string]string{"v": "2"}))

		loaded, err := s.Load(ctx, "sess-upsert")
		require.NoError(t, err)
		assert.Equal(t, "2", loaded["v"])
	})

	t.Run("Upsert nil metadata is treated as empty map", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Upsert(ctx, "sess-nil-meta", nil))
		loaded, err := s.Load(ctx, "sess-nil-meta")
		require.NoError(t, err)
		assert.NotNil(t, loaded)
	})

	t.Run("Upsert with empty ID returns error", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		err := s.Upsert(ctx, "", map[string]string{})
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

	t.Run("Create creates entry when absent", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		stored, err := s.Create(ctx, "sess-new", map[string]string{"x": "1"})
		require.NoError(t, err)
		assert.True(t, stored, "should return true when entry was created")

		loaded, err := s.Load(ctx, "sess-new")
		require.NoError(t, err)
		assert.Equal(t, "1", loaded["x"])
	})

	t.Run("Create does not overwrite existing entry", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Upsert(ctx, "sess-existing", map[string]string{"x": "original"}))

		stored, err := s.Create(ctx, "sess-existing", map[string]string{"x": "overwrite"})
		require.NoError(t, err)
		assert.False(t, stored, "should return false when entry already existed")

		loaded, err := s.Load(ctx, "sess-existing")
		require.NoError(t, err)
		assert.Equal(t, "original", loaded["x"], "original value must be preserved")
	})

	t.Run("Create is atomic under concurrent callers", func(t *testing.T) {
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
				stored, err := s.Create(ctx, "concurrent-key", map[string]string{"winner": val})
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

	t.Run("Create with empty ID returns error", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		_, err := s.Create(ctx, "", map[string]string{})
		assert.Error(t, err)
	})

	t.Run("Delete removes entry; subsequent Load returns ErrSessionNotFound", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Upsert(ctx, "sess-del", map[string]string{}))
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

	t.Run("Update overwrites existing entry and returns true", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Upsert(ctx, "sess-update", map[string]string{"v": "original"}))

		updated, err := s.Update(ctx, "sess-update", map[string]string{"v": "updated"})
		require.NoError(t, err)
		assert.True(t, updated, "should return true when key exists")

		loaded, err := s.Load(ctx, "sess-update")
		require.NoError(t, err)
		assert.Equal(t, "updated", loaded["v"])
	})

	t.Run("Update on missing key returns (false, nil) without creating it", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		updated, err := s.Update(ctx, "sess-absent", map[string]string{"v": "new"})
		require.NoError(t, err)
		assert.False(t, updated, "should return false when key does not exist")

		// The key must not have been created.
		_, err = s.Load(ctx, "sess-absent")
		assert.ErrorIs(t, err, ErrSessionNotFound, "Update must not create a missing key")
	})

	t.Run("Update after Delete returns (false, nil)", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Upsert(ctx, "sess-deleted", map[string]string{"v": "1"}))
		require.NoError(t, s.Delete(ctx, "sess-deleted"))

		updated, err := s.Update(ctx, "sess-deleted", map[string]string{"v": "2"})
		require.NoError(t, err)
		assert.False(t, updated, "should return false after key was deleted")
	})

	t.Run("Update with empty ID returns error", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		_, err := s.Update(ctx, "", map[string]string{})
		assert.Error(t, err)
	})

	t.Run("Update nil metadata is treated as empty map", func(t *testing.T) {
		t.Parallel()
		s := newStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Upsert(ctx, "sess-update-nil", map[string]string{"v": "original"}))

		updated, err := s.Update(ctx, "sess-update-nil", nil)
		require.NoError(t, err)
		assert.True(t, updated)

		loaded, err := s.Load(ctx, "sess-update-nil")
		require.NoError(t, err)
		assert.NotNil(t, loaded)
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

		require.NoError(t, s.Upsert(ctx, "ttl-sess", map[string]string{"k": "v"}))
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

		require.NoError(t, s.Upsert(ctx, "active-sess", map[string]string{}))
		backdateLocalEntry(t, s, "active-sess", ttl+time.Millisecond)

		// Load should refresh the entry's timestamp.
		_, err = s.Load(ctx, "active-sess")
		require.NoError(t, err)

		// Eviction run should not remove the entry because Load just refreshed it.
		s.deleteExpired()

		_, err = s.Load(ctx, "active-sess")
		assert.NoError(t, err, "actively loaded session should not be evicted")
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

		require.NoError(t, s.Upsert(ctx, "ttl-refresh", map[string]string{}))
		mr.FastForward(29 * time.Minute)

		_, err := s.Load(ctx, "ttl-refresh")
		require.NoError(t, err, "load should succeed before expiry")

		// Advance past the original TTL; load should have reset the clock.
		mr.FastForward(2 * time.Minute)

		_, err = s.Load(ctx, "ttl-refresh")
		assert.NoError(t, err, "session should still be alive after TTL reset by Load")
	})

	t.Run("Key expires after TTL when not refreshed", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisDataStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Upsert(ctx, "expiring", map[string]string{}))
		mr.FastForward(31 * time.Minute)

		_, err := s.Load(ctx, "expiring")
		assert.ErrorIs(t, err, ErrSessionNotFound)
	})

	t.Run("Create uses SET NX — key format is {prefix}{id}", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisDataStorage(t)
		ctx := context.Background()

		stored, err := s.Create(ctx, "nx-test", map[string]string{"a": "b"})
		require.NoError(t, err)
		require.True(t, stored)

		val, err := mr.Get("test:data:nx-test")
		require.NoError(t, err)
		assert.NotEmpty(t, val)
	})

	t.Run("Update refreshes TTL via SET XX", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisDataStorage(t)
		ctx := context.Background()

		require.NoError(t, s.Upsert(ctx, "ttl-update", map[string]string{"v": "1"}))
		mr.FastForward(29 * time.Minute)

		updated, err := s.Update(ctx, "ttl-update", map[string]string{"v": "2"})
		require.NoError(t, err)
		assert.True(t, updated)

		// Advance past the original TTL; Update should have reset the clock.
		mr.FastForward(2 * time.Minute)

		loaded, err := s.Load(ctx, "ttl-update")
		require.NoError(t, err, "session should still be alive after TTL reset by Update")
		assert.Equal(t, "2", loaded["v"])
	})
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/redisconn"
)

func proxyFactory(id string) Session { return NewProxySession(id) }

// newManagerWithRedis builds a Manager over a RedisStorage with a one-hour TTL the same way
// production callers compose NewRedisStorage and NewManagerWithStorage.
func newManagerWithRedis(
	ctx context.Context,
	factory Factory,
	cfg redisconn.Config,
	keyPrefix string,
) (*Manager, error) {
	storage, err := NewRedisStorage(ctx, cfg, keyPrefix, time.Hour)
	if err != nil {
		return nil, err
	}
	return NewManagerWithStorage(time.Hour, factory, storage), nil
}

func TestManagerWithRedisStorage(t *testing.T) {
	t.Parallel()

	t.Run("valid config returns manager", func(t *testing.T) {
		t.Parallel()
		mr := miniredis.RunT(t)
		defer mr.Close()

		m, err := newManagerWithRedis(
			context.Background(),
			proxyFactory,
			redisconn.Config{Addr: mr.Addr()},
			"test:mgr:",
		)
		require.NoError(t, err)
		require.NotNil(t, m)
		defer m.Stop()

		_, isRedis := m.storage.(*RedisStorage)
		assert.True(t, isRedis, "storage should be *RedisStorage")
	})

	t.Run("missing key prefix returns error", func(t *testing.T) {
		t.Parallel()
		// Empty keyPrefix fails session-invariant check before Ping.
		m, err := newManagerWithRedis(
			context.Background(),
			proxyFactory,
			redisconn.Config{Addr: "localhost:6379"},
			"",
		)
		require.Error(t, err)
		assert.Nil(t, m)
	})

	t.Run("round-trip via AddWithID and Get", func(t *testing.T) {
		t.Parallel()
		mr := miniredis.RunT(t)
		defer mr.Close()

		m, err := newManagerWithRedis(
			context.Background(),
			proxyFactory,
			redisconn.Config{Addr: mr.Addr()},
			"test:mgr:",
		)
		require.NoError(t, err)
		defer m.Stop()

		const rtSessionID = "bbbbbbbb-0001-0001-0001-000000000001"
		require.NoError(t, m.AddWithID(rtSessionID))

		sess, ok := m.Get(rtSessionID)
		require.True(t, ok)
		assert.Equal(t, rtSessionID, sess.ID())
	})

	t.Run("Stop closes Redis client", func(t *testing.T) {
		t.Parallel()
		mr := miniredis.RunT(t)
		defer mr.Close()

		m, err := newManagerWithRedis(
			context.Background(),
			proxyFactory,
			redisconn.Config{Addr: mr.Addr()},
			"test:mgr:",
		)
		require.NoError(t, err)

		require.NoError(t, m.AddWithID("bbbbbbbb-0002-0001-0001-000000000002"))
		require.NoError(t, m.Stop())

		// After Stop, storage is closed; further operations should fail with a Redis error.
		err = m.AddWithID("bbbbbbbb-0003-0001-0001-000000000003")
		assert.Error(t, err)
	})
}

func TestNewManagerUsesLocalStorage(t *testing.T) {
	t.Parallel()

	m := NewManager(time.Hour, proxyFactory)
	defer m.Stop()

	_, isLocal := m.storage.(*LocalStorage)
	assert.True(t, isLocal, "NewManager should use *LocalStorage")
}

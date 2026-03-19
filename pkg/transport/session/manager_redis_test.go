// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func proxyFactory(id string) Session { return NewProxySession(id) }

func TestNewManagerWithRedis(t *testing.T) {
	t.Parallel()

	t.Run("valid config returns manager", func(t *testing.T) {
		t.Parallel()
		mr := miniredis.RunT(t)
		defer mr.Close()

		m, err := NewManagerWithRedis(time.Hour, proxyFactory, RedisConfig{
			Addr:      mr.Addr(),
			KeyPrefix: "test:mgr:",
		})
		require.NoError(t, err)
		require.NotNil(t, m)
		defer m.Stop()

		_, isRedis := m.storage.(*RedisStorage)
		assert.True(t, isRedis, "storage should be *RedisStorage")
	})

	t.Run("invalid config returns error", func(t *testing.T) {
		t.Parallel()
		// Missing KeyPrefix → validateRedisConfig fails before Ping
		m, err := NewManagerWithRedis(time.Hour, proxyFactory, RedisConfig{
			Addr: "localhost:6379",
		})
		require.Error(t, err)
		assert.Nil(t, m)
	})

	t.Run("invalid TLS CA cert returns error", func(t *testing.T) {
		t.Parallel()
		m, err := NewManagerWithRedis(time.Hour, proxyFactory, RedisConfig{
			Addr:      "localhost:6379",
			KeyPrefix: "test:mgr:",
			TLS:       &RedisTLSConfig{CACert: []byte("not-valid-pem")},
		})
		require.Error(t, err)
		assert.Nil(t, m)
	})

	t.Run("round-trip via AddWithID and Get", func(t *testing.T) {
		t.Parallel()
		mr := miniredis.RunT(t)
		defer mr.Close()

		m, err := NewManagerWithRedis(time.Hour, proxyFactory, RedisConfig{
			Addr:      mr.Addr(),
			KeyPrefix: "test:mgr:",
		})
		require.NoError(t, err)
		defer m.Stop()

		require.NoError(t, m.AddWithID("rt-session"))

		sess, ok := m.Get("rt-session")
		require.True(t, ok)
		assert.Equal(t, "rt-session", sess.ID())
	})

	t.Run("Stop closes Redis client", func(t *testing.T) {
		t.Parallel()
		mr := miniredis.RunT(t)
		defer mr.Close()

		m, err := NewManagerWithRedis(time.Hour, proxyFactory, RedisConfig{
			Addr:      mr.Addr(),
			KeyPrefix: "test:mgr:",
		})
		require.NoError(t, err)

		require.NoError(t, m.AddWithID("pre-stop"))
		require.NoError(t, m.Stop())

		// After Stop, storage is closed; further operations should fail
		err = m.AddWithID("post-stop")
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

func TestNewTypedManagerUsesLocalStorage(t *testing.T) {
	t.Parallel()

	m := NewTypedManager(time.Hour, SessionTypeMCP)
	defer m.Stop()

	_, isLocal := m.storage.(*LocalStorage)
	assert.True(t, isLocal, "NewTypedManager should use *LocalStorage")
}

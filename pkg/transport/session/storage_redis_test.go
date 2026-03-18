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

// --- Test Helpers ---

func newTestRedisStorage(t *testing.T) (*RedisStorage, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	storage := newRedisStorageWithClient(client, "test:session:", 30*time.Minute)
	return storage, mr
}

// --- Constructor Tests ---

func TestNewRedisStorage_Standalone(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)

	cfg := RedisConfig{
		Addr:      mr.Addr(),
		KeyPrefix: "thv:session:",
	}
	s, err := NewRedisStorage(cfg, 30*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, s)
	defer s.Close()
}

func TestNewRedisStorage_MissingAddr(t *testing.T) {
	t.Parallel()
	cfg := RedisConfig{
		KeyPrefix: "thv:session:",
	}
	_, err := NewRedisStorage(cfg, 30*time.Minute)
	require.Error(t, err)
}

func TestNewRedisStorage_MissingKeyPrefix(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	cfg := RedisConfig{
		Addr: mr.Addr(),
	}
	_, err := NewRedisStorage(cfg, 30*time.Minute)
	require.Error(t, err)
}

// --- Store Tests ---

func TestRedisStorage_Store_NilSession(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)
	defer storage.Close()

	err := storage.Store(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil session")
}

func TestRedisStorage_Store_EmptyID(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)
	defer storage.Close()

	sess := NewProxySession("")
	err := storage.Store(context.Background(), sess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty ID")
}

// --- Load Tests ---

func TestRedisStorage_Load_EmptyID(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)
	defer storage.Close()

	_, err := storage.Load(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty ID")
}

func TestRedisStorage_Load_NotFound(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)
	defer storage.Close()

	_, err := storage.Load(context.Background(), "does-not-exist")
	require.ErrorIs(t, err, ErrSessionNotFound)
}

// --- Round-Trip Tests ---

func TestRedisStorage_StoreLoad_ProxySession(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)
	defer storage.Close()

	sess := NewProxySession("proxy-1")
	sess.SetMetadata("env", "production")

	ctx := context.Background()
	require.NoError(t, storage.Store(ctx, sess))

	loaded, err := storage.Load(ctx, "proxy-1")
	require.NoError(t, err)
	assert.Equal(t, "proxy-1", loaded.ID())
	assert.Equal(t, SessionTypeMCP, loaded.Type())
	assert.Equal(t, "production", loaded.GetMetadata()["env"])
}

func TestRedisStorage_StoreLoad_SSESession(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)
	defer storage.Close()

	sess := NewSSESession("sse-1")
	sess.SetMetadata("client", "browser")

	ctx := context.Background()
	require.NoError(t, storage.Store(ctx, sess))

	loaded, err := storage.Load(ctx, "sse-1")
	require.NoError(t, err)
	assert.Equal(t, "sse-1", loaded.ID())
	assert.Equal(t, SessionTypeSSE, loaded.Type())
	assert.Equal(t, "browser", loaded.GetMetadata()["client"])
}

func TestRedisStorage_StoreLoad_StreamableSession(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)
	defer storage.Close()

	sess := NewStreamableSession("stream-1")
	sess.SetMetadata("protocol", "http")

	ctx := context.Background()
	require.NoError(t, storage.Store(ctx, sess))

	loaded, err := storage.Load(ctx, "stream-1")
	require.NoError(t, err)
	assert.Equal(t, "stream-1", loaded.ID())
	assert.Equal(t, SessionTypeStreamable, loaded.Type())
	assert.Equal(t, "http", loaded.GetMetadata()["protocol"])
}

func TestRedisStorage_Store_Idempotent(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)
	defer storage.Close()

	ctx := context.Background()
	sess := NewProxySession("dup-1")
	sess.SetMetadata("v", "1")

	require.NoError(t, storage.Store(ctx, sess))

	sess.SetMetadata("v", "2")
	require.NoError(t, storage.Store(ctx, sess))

	loaded, err := storage.Load(ctx, "dup-1")
	require.NoError(t, err)
	assert.Equal(t, "2", loaded.GetMetadata()["v"])
}

// --- Delete Tests ---

func TestRedisStorage_Delete(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)
	defer storage.Close()

	ctx := context.Background()
	sess := NewProxySession("del-1")
	require.NoError(t, storage.Store(ctx, sess))

	require.NoError(t, storage.Delete(ctx, "del-1"))

	_, err := storage.Load(ctx, "del-1")
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestRedisStorage_Delete_NotFound(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)
	defer storage.Close()

	// Deleting a non-existent key must not return an error
	err := storage.Delete(context.Background(), "ghost")
	assert.NoError(t, err)
}

// --- DeleteExpired Tests ---

func TestRedisStorage_DeleteExpired_IsNoop(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)
	defer storage.Close()

	ctx := context.Background()
	sess := NewProxySession("expire-1")
	require.NoError(t, storage.Store(ctx, sess))

	// DeleteExpired must return nil and leave the key untouched
	err := storage.DeleteExpired(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)

	_, err = storage.Load(ctx, "expire-1")
	assert.NoError(t, err, "session should still exist: Redis TTL handles expiry, not DeleteExpired")
}

// --- TTL Tests ---

func TestRedisStorage_Store_RefreshesTTL(t *testing.T) {
	t.Parallel()
	storage, mr := newTestRedisStorage(t)
	defer storage.Close()

	const ttl = 10 * time.Minute
	storage.ttl = ttl

	ctx := context.Background()
	sess := NewProxySession("ttl-1")
	require.NoError(t, storage.Store(ctx, sess))

	// Advance clock by half the TTL
	mr.FastForward(ttl / 2)

	// Re-storing refreshes the TTL
	require.NoError(t, storage.Store(ctx, sess))

	// Advance by another half TTL (total = 1× TTL from first store, 0.5× from second)
	// The key should still be alive because the TTL was refreshed
	mr.FastForward(ttl / 2)

	_, err := storage.Load(ctx, "ttl-1")
	assert.NoError(t, err, "session should still be alive after TTL refresh")
}

func TestRedisStorage_TTL_ExpiresWithoutRefresh(t *testing.T) {
	t.Parallel()
	storage, mr := newTestRedisStorage(t)
	defer storage.Close()

	const ttl = 10 * time.Minute
	storage.ttl = ttl

	ctx := context.Background()
	sess := NewProxySession("ttl-2")
	require.NoError(t, storage.Store(ctx, sess))

	// Advance past the full TTL
	mr.FastForward(ttl + time.Second)

	_, err := storage.Load(ctx, "ttl-2")
	assert.ErrorIs(t, err, ErrSessionNotFound, "session should have expired")
}

// --- Key Format Test ---

func TestRedisStorage_KeyFormat(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	storage := newRedisStorageWithClient(client, "thv:vmcp:session:", 30*time.Minute)
	defer storage.Close()

	ctx := context.Background()
	sess := NewProxySession("abc123")
	require.NoError(t, storage.Store(ctx, sess))

	// Verify the raw key in miniredis
	assert.True(t, mr.Exists("thv:vmcp:session:abc123"), "key must be {prefix}{id}")
}

// --- Close Test ---

func TestRedisStorage_Close(t *testing.T) {
	t.Parallel()
	storage, _ := newTestRedisStorage(t)

	err := storage.Close()
	assert.NoError(t, err)
}

// --- TLS Config Tests ---

func TestBuildSessionTLSConfig_Nil(t *testing.T) {
	t.Parallel()
	cfg, err := buildTLSConfig(nil)
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestBuildSessionTLSConfig_InsecureSkipVerify(t *testing.T) {
	t.Parallel()
	cfg, err := buildTLSConfig(&RedisTLSConfig{InsecureSkipVerify: true})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, cfg.InsecureSkipVerify)
}

func TestBuildSessionTLSConfig_InvalidCACert(t *testing.T) {
	t.Parallel()
	_, err := buildTLSConfig(&RedisTLSConfig{CACert: []byte("not-valid-pem")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CA cert")
}

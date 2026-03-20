// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Tests use the withRedisStorage helper which calls t.Parallel() internally,
// making all subtests parallel despite not having explicit t.Parallel() calls.
//
//nolint:paralleltest // parallel execution handled by withRedisStorage helper
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

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	storage := newRedisStorageWithClient(client, "test:session:", 30*time.Minute)
	return storage, mr
}

func withRedisStorage(t *testing.T, fn func(context.Context, *RedisStorage, *miniredis.Miniredis)) {
	t.Helper()
	t.Parallel()
	storage, mr := newTestRedisStorage(t)
	defer func() {
		_ = storage.Close()
		mr.Close()
	}()
	fn(context.Background(), storage, mr)
}

// --- Config Validation Tests ---

func TestValidateRedisConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     RedisConfig
		wantErr string
	}{
		{
			name:    "both Addr and SentinelConfig set",
			cfg:     RedisConfig{Addr: "localhost:6379", SentinelConfig: &SentinelConfig{MasterName: "m", SentinelAddrs: []string{"s:26379"}}, KeyPrefix: "p:"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "neither Addr nor SentinelConfig set",
			cfg:     RedisConfig{KeyPrefix: "p:"},
			wantErr: "one of Addr",
		},
		{
			name:    "Sentinel missing MasterName",
			cfg:     RedisConfig{SentinelConfig: &SentinelConfig{SentinelAddrs: []string{"s:26379"}}, KeyPrefix: "p:"},
			wantErr: "MasterName",
		},
		{
			name:    "Sentinel missing SentinelAddrs",
			cfg:     RedisConfig{SentinelConfig: &SentinelConfig{MasterName: "m"}, KeyPrefix: "p:"},
			wantErr: "SentinelAddrs",
		},
		{
			name:    "empty KeyPrefix",
			cfg:     RedisConfig{Addr: "localhost:6379"},
			wantErr: "KeyPrefix",
		},
		{
			name: "valid standalone",
			cfg:  RedisConfig{Addr: "localhost:6379", KeyPrefix: "thv:vmcp:session:"},
		},
		{
			name: "valid sentinel",
			cfg:  RedisConfig{SentinelConfig: &SentinelConfig{MasterName: "m", SentinelAddrs: []string{"s:26379"}}, KeyPrefix: "thv:vmcp:session:"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateRedisConfig(&tc.cfg)
			if tc.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestNewRedisStorageTTLValidation(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	cfg := RedisConfig{Addr: mr.Addr(), KeyPrefix: "test:"}

	_, err := NewRedisStorage(context.Background(), cfg, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ttl")

	_, err = NewRedisStorage(context.Background(), cfg, -1*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ttl")
}

// --- Unit Tests ---

func TestRedisStorage(t *testing.T) {
	t.Parallel()

	t.Run("Store and Load round-trip", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			session := NewProxySession("round-trip-1")
			session.SetMetadata("key1", "value1")

			require.NoError(t, s.Store(ctx, session))

			loaded, err := s.Load(ctx, "round-trip-1")
			require.NoError(t, err)
			assert.Equal(t, session.ID(), loaded.ID())
			assert.Equal(t, session.Type(), loaded.Type())
			assert.Equal(t, "value1", loaded.GetMetadata()["key1"])
		})
	})

	t.Run("Store with nil session returns error", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			err := s.Store(ctx, nil)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "nil session")
		})
	})

	t.Run("Store with empty session ID returns error", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			session := &ProxySession{} // empty ID
			err := s.Store(ctx, session)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "empty ID")
		})
	})

	t.Run("Load non-existent key returns ErrSessionNotFound", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			loaded, err := s.Load(ctx, "does-not-exist")
			assert.Equal(t, ErrSessionNotFound, err)
			assert.Nil(t, loaded)
		})
	})

	t.Run("Load with empty ID returns error", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			loaded, err := s.Load(ctx, "")
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "empty ID")
			assert.Nil(t, loaded)
		})
	})

	t.Run("Delete removes key; subsequent Load returns ErrSessionNotFound", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			session := NewProxySession("delete-me")
			require.NoError(t, s.Store(ctx, session))

			require.NoError(t, s.Delete(ctx, "delete-me"))

			_, err := s.Load(ctx, "delete-me")
			assert.Equal(t, ErrSessionNotFound, err)
		})
	})

	t.Run("Delete non-existent key returns nil", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			err := s.Delete(ctx, "non-existent")
			assert.NoError(t, err)
		})
	})

	t.Run("Delete with empty ID returns error", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			err := s.Delete(ctx, "")
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "empty ID")
		})
	})

	t.Run("DeleteExpired is a no-op", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			session := NewProxySession("no-op-session")
			require.NoError(t, s.Store(ctx, session))

			err := s.DeleteExpired(ctx, time.Now().Add(1*time.Hour))
			assert.NoError(t, err)

			// Key should still exist — DeleteExpired is a no-op
			_, err = s.Load(ctx, "no-op-session")
			assert.NoError(t, err)
		})
	})

	t.Run("TTL is refreshed on Store", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			session := NewProxySession("ttl-session")
			require.NoError(t, s.Store(ctx, session))

			// Advance time by almost the full TTL
			mr.FastForward(29 * time.Minute)

			// Store again to refresh the TTL
			require.NoError(t, s.Store(ctx, session))

			// Advance past the original expiry
			mr.FastForward(2 * time.Minute)

			// Key should still be alive because TTL was refreshed
			_, err := s.Load(ctx, "ttl-session")
			assert.NoError(t, err)
		})
	})

	t.Run("Load refreshes TTL via GETEX", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			session := NewProxySession("load-refresh-ttl")
			require.NoError(t, s.Store(ctx, session))

			// Advance time by almost the full TTL
			mr.FastForward(29 * time.Minute)

			// Load refreshes the TTL (GETEX)
			_, err := s.Load(ctx, "load-refresh-ttl")
			require.NoError(t, err)

			// Advance past the original expiry; key should still be alive
			mr.FastForward(2 * time.Minute)

			_, err = s.Load(ctx, "load-refresh-ttl")
			assert.NoError(t, err)
		})
	})

	t.Run("Key expires after TTL when not refreshed", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			session := NewProxySession("expiring-session")
			require.NoError(t, s.Store(ctx, session))

			// Advance past TTL without refreshing
			mr.FastForward(31 * time.Minute)

			_, err := s.Load(ctx, "expiring-session")
			assert.Equal(t, ErrSessionNotFound, err)
		})
	})

	t.Run("Store is idempotent upsert", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			session := NewProxySession("upsert-session")
			session.SetMetadata("v", "1")
			require.NoError(t, s.Store(ctx, session))

			session.SetMetadata("v", "2")
			require.NoError(t, s.Store(ctx, session))

			loaded, err := s.Load(ctx, "upsert-session")
			require.NoError(t, err)
			assert.Equal(t, "2", loaded.GetMetadata()["v"])
		})
	})

	t.Run("Key format is {KeyPrefix}{sessionID}", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			session := NewProxySession("key-format-test")
			require.NoError(t, s.Store(ctx, session))

			val, err := mr.Get("test:session:key-format-test")
			require.NoError(t, err)
			assert.NotEmpty(t, val)
		})
	})

	t.Run("Close closes client; subsequent operations return error", func(t *testing.T) {
		// Not using withRedisStorage so we control Close timing
		t.Parallel()
		storage, mr := newTestRedisStorage(t)
		defer mr.Close()

		// Store something to confirm it works before close
		ctx := context.Background()
		session := NewProxySession("before-close")
		require.NoError(t, storage.Store(ctx, session))

		require.NoError(t, storage.Close())

		// After close, operations should fail
		err := storage.Store(ctx, session)
		assert.Error(t, err)
	})

	t.Run("SSESession round-trip", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			session := NewSSESession("sse-rt-1")
			session.SetMetadata("client", "browser")
			require.NoError(t, s.Store(ctx, session))

			loaded, err := s.Load(ctx, "sse-rt-1")
			require.NoError(t, err)
			assert.Equal(t, SessionTypeSSE, loaded.Type())
			assert.Equal(t, "browser", loaded.GetMetadata()["client"])
		})
	})

	t.Run("StreamableSession round-trip", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			session := NewStreamableSession("stream-rt-1")
			session.SetMetadata("protocol", "http")
			require.NoError(t, s.Store(ctx, session))

			loaded, err := s.Load(ctx, "stream-rt-1")
			require.NoError(t, err)
			assert.Equal(t, SessionTypeStreamable, loaded.Type())
			assert.Equal(t, "http", loaded.GetMetadata()["protocol"])
		})
	})

	t.Run("MCPSession round-trip", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			session := NewTypedProxySession("mcp-rt-1", SessionTypeMCP)
			session.SetMetadata("env", "prod")
			require.NoError(t, s.Store(ctx, session))

			loaded, err := s.Load(ctx, "mcp-rt-1")
			require.NoError(t, err)
			assert.Equal(t, SessionTypeMCP, loaded.Type())
			assert.Equal(t, "prod", loaded.GetMetadata()["env"])
		})
	})
}

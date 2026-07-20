// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	tcredis "github.com/stacklok/toolhive-core/redis"
)

// RedisStorage implements the Storage interface backed by Redis.
type RedisStorage struct {
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration
}

// NewRedisStorage constructs a RedisStorage. Connection-mode topology, timeouts,
// TLS, and credentials are configured through cfg; keyPrefix is the per-tenant
// key prefix (e.g. "thv:proxy:session:") and must end with ':' to avoid key
// collisions. ttl is the sliding-window expiry applied to every key on Store
// and refreshed on every Load (sliding window): sessions remain valid
// indefinitely while actively used; revocation requires an explicit Delete call.
//
// Connection-mode validation, timeout defaults, client construction (standalone,
// cluster, or sentinel), TLS plumbing, and connectivity verification are
// delegated to the shared toolhive-core redis package.
func NewRedisStorage(ctx context.Context, cfg tcredis.Config, keyPrefix string, ttl time.Duration) (*RedisStorage, error) {
	if err := validateSessionInvariants(keyPrefix, ttl); err != nil {
		return nil, err
	}
	client, err := tcredis.NewClient(ctx, &cfg)
	if err != nil {
		return nil, err
	}
	return &RedisStorage{
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       ttl,
	}, nil
}

// newRedisStorageWithClient creates a RedisStorage with a pre-configured client.
// Intended for tests only (bypasses Ping); production callers must use NewRedisStorage.
func newRedisStorageWithClient(client redis.UniversalClient, keyPrefix string, ttl time.Duration) *RedisStorage {
	return &RedisStorage{
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       ttl,
	}
}

// validateSessionInvariants checks the per-storage invariants that the shared
// toolhive-core redis package does not enforce (key-prefix conventions, TTL
// bounds).
func validateSessionInvariants(keyPrefix string, ttl time.Duration) error {
	if keyPrefix == "" {
		return errors.New("invalid redis configuration: key prefix is required")
	}
	if keyPrefix[len(keyPrefix)-1] != ':' {
		return errors.New(
			`invalid redis configuration: key prefix must end with ':' to avoid key collisions (e.g. "thv:vmcp:session:")`,
		)
	}
	if ttl <= 0 {
		return fmt.Errorf("ttl must be a positive duration")
	}
	return nil
}

func (s *RedisStorage) key(id string) string {
	return s.keyPrefix + id
}

// Store serializes the session and persists it with SET … EX, refreshing the TTL on every call.
func (s *RedisStorage) Store(ctx context.Context, session Session) error {
	if session == nil {
		return fmt.Errorf("cannot store nil session")
	}
	if session.ID() == "" {
		return fmt.Errorf("cannot store session with empty ID")
	}

	data, err := serializeSession(session)
	if err != nil {
		return fmt.Errorf("failed to serialize session: %w", err)
	}

	return s.client.Set(ctx, s.key(session.ID()), data, s.ttl).Err()
}

// Load retrieves a session by ID. Returns ErrSessionNotFound when the key does not exist.
// The Redis eviction TTL is refreshed atomically via GETEX on every read so that active
// sessions are not evicted between accesses. The session's UpdatedAt timestamp is not modified.
//
// Lifetime note: this implements a sliding-window TTL. A session accessed at least once per
// TTL window will never expire and can live indefinitely while the client keeps making requests.
// Session revocation therefore depends entirely on explicit Delete calls (e.g. on logout or
// token invalidation); there is no absolute maximum session lifetime enforced here. If a hard
// cap is required in future, a MaxLifetime field checked against the session's CreatedAt would
// be the path forward.
func (s *RedisStorage) Load(ctx context.Context, id string) (Session, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot load session with empty ID")
	}

	data, err := s.client.GetEx(ctx, s.key(id), s.ttl).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("failed to load session: %w", err)
	}

	session, err := deserializeSession(data)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize session: %w", err)
	}

	return session, nil
}

// Delete removes the Redis key. A missing key is not an error.
func (s *RedisStorage) Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("cannot delete session with empty ID")
	}

	if err := s.client.Del(ctx, s.key(id)).Err(); err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	return nil
}

// DeleteExpired is a no-op. Redis TTL handles key expiry natively.
func (*RedisStorage) DeleteExpired(_ context.Context, _ time.Time) error {
	return nil
}

// Close closes the underlying Redis client connection.
func (s *RedisStorage) Close() error {
	return s.client.Close()
}

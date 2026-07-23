// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	tcredis "github.com/stacklok/toolhive-core/redis"
)

// RedisSessionDataStorage implements DataStorage backed by Redis/Valkey.
//
// Metadata is serialized as a JSON object and stored with a sliding-window TTL:
// each Create/Update/Load call refreshes the key's expiry so that active sessions
// never expire while they are in use.
//
// Because only plain map[string]string is stored, there are no type assertions
// or deserialization tricks — Redis round-trips the map cleanly.
type RedisSessionDataStorage struct {
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration
}

// NewRedisSessionDataStorage constructs a RedisSessionDataStorage. Connection-mode
// topology, timeouts, TLS, and credentials are configured through cfg; keyPrefix
// is the per-tenant key prefix (e.g. "thv:vmcp:session:") and must end with ':'
// to avoid key collisions. ttl is the sliding-window expiry applied on every
// Create/Update and Load. The caller must call Close when done.
//
// Connection-mode validation, timeout defaults, client construction (standalone,
// cluster, or sentinel), TLS plumbing, and connectivity verification are
// delegated to the shared toolhive-core redis package.
func NewRedisSessionDataStorage(
	ctx context.Context,
	cfg tcredis.Config,
	keyPrefix string,
	ttl time.Duration,
) (*RedisSessionDataStorage, error) {
	if err := validateSessionInvariants(keyPrefix, ttl); err != nil {
		return nil, err
	}
	client, err := tcredis.NewClient(ctx, &cfg)
	if err != nil {
		return nil, err
	}
	return &RedisSessionDataStorage{
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       ttl,
	}, nil
}

func (s *RedisSessionDataStorage) key(id string) string {
	return s.keyPrefix + id
}

// RedisAccess exposes the underlying Redis client and per-tenant key prefix so
// the untrusted-mode wiring (pkg/vmcp/session/untrusted) can back its admission
// counters and pod leases from the same connection and ':'-terminated prefix as
// the session store, rather than a second connection that could drift from the
// session store's coordinates. The returned client is the same instance owned
// by this storage — callers must NOT close it (Close remains the storage's
// responsibility).
//
// A composition root that constructs the RedisSessionDataStorage itself (e.g.
// an enterprise build wiring the untrusted stack before the server exists) uses
// this to hand NewStack the shared client. The OSS vMCP CLI builds its own
// connection from cfg.SessionStorage instead because the storage is created
// inside Serve, after the session factory (which carries the resolver) exists.
func (s *RedisSessionDataStorage) RedisAccess() (redis.UniversalClient, string) {
	return s.client, s.keyPrefix
}

// Load retrieves metadata from Redis and refreshes the key's TTL via GETEX.
// Returns ErrSessionNotFound if the key does not exist.
func (s *RedisSessionDataStorage) Load(ctx context.Context, id string) (map[string]string, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot load session data with empty ID")
	}
	data, err := s.client.GetEx(ctx, s.key(id), s.ttl).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("failed to load session metadata: %w", err)
	}
	var metadata map[string]string
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to deserialize session metadata: %w", err)
	}
	return metadata, nil
}

// Update overwrites session metadata only if the key already exists.
// Uses Redis SET XX (set-if-exists) to prevent resurrecting a session that
// was deleted by a concurrent Delete call (e.g. from another pod).
// Returns (true, nil) if updated, (false, nil) if the key was not found.
func (s *RedisSessionDataStorage) Update(ctx context.Context, id string, metadata map[string]string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot write session data with empty ID")
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return false, fmt.Errorf("failed to serialize session metadata: %w", err)
	}
	// Mode "XX" means "only set if the key already exists".
	res, err := s.client.SetArgs(ctx, s.key(id), data, redis.SetArgs{
		Mode: "XX",
		TTL:  s.ttl,
	}).Result()
	if err != nil {
		// go-redis surfaces the "key does not exist" nil bulk reply as redis.Nil.
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, fmt.Errorf("failed to conditionally update session metadata: %w", err)
	}
	// SetArgs with Mode "XX" returns "" when the key does not exist and "OK"
	// when the write succeeded.
	return res == "OK", nil
}

// Create atomically creates session metadata only if the key does not
// already exist. Uses Redis SET NX (set-if-not-exists) to avoid the
// read-then-write TOCTOU race that a Load-then-unconditional-write pattern
// would introduce in multi-pod deployments.
func (s *RedisSessionDataStorage) Create(ctx context.Context, id string, metadata map[string]string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot write session data with empty ID")
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return false, fmt.Errorf("failed to serialize session metadata: %w", err)
	}
	ok, err := s.client.SetNX(ctx, s.key(id), data, s.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("failed to atomically store session metadata: %w", err)
	}
	return ok, nil
}

// Delete removes the Redis key. A missing key is not an error.
func (s *RedisSessionDataStorage) Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("cannot delete session data with empty ID")
	}
	if err := s.client.Del(ctx, s.key(id)).Err(); err != nil {
		return fmt.Errorf("failed to delete session metadata: %w", err)
	}
	return nil
}

// Close closes the underlying Redis client.
func (s *RedisSessionDataStorage) Close() error {
	return s.client.Close()
}

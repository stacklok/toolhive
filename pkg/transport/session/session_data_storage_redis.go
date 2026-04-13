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
)

// RedisSessionDataStorage implements DataStorage backed by Redis/Valkey.
//
// Metadata is serialized as a JSON object and stored with a sliding-window TTL:
// each Upsert/Load call refreshes the key's expiry so that active sessions
// never expire while they are in use.
//
// Because only plain map[string]string is stored, there are no type assertions
// or deserialization tricks — Redis round-trips the map cleanly.
type RedisSessionDataStorage struct {
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration
}

// NewRedisSessionDataStorage constructs a RedisSessionDataStorage.
// cfg provides connection parameters; ttl is the sliding-window expiry applied
// on every Upsert and Load. The caller must call Close when done.
func NewRedisSessionDataStorage(ctx context.Context, cfg RedisConfig, ttl time.Duration) (*RedisSessionDataStorage, error) {
	if err := validateRedisConfig(&cfg); err != nil {
		return nil, fmt.Errorf("invalid redis configuration: %w", err)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("ttl must be a positive duration")
	}
	client, err := buildRedisClient(ctx, &cfg)
	if err != nil {
		return nil, err
	}
	return &RedisSessionDataStorage{
		client:    client,
		keyPrefix: cfg.KeyPrefix,
		ttl:       ttl,
	}, nil
}

func (s *RedisSessionDataStorage) key(id string) string {
	return s.keyPrefix + id
}

// Upsert serializes metadata as JSON and writes it to Redis with a sliding TTL.
func (s *RedisSessionDataStorage) Upsert(ctx context.Context, id string, metadata map[string]string) error {
	if id == "" {
		return fmt.Errorf("cannot write session data with empty ID")
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to serialize session metadata: %w", err)
	}
	return s.client.Set(ctx, s.key(id), data, s.ttl).Err()
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
// already exist. Uses Redis SET NX (set-if-not-exists) to eliminate the
// TOCTOU race between Load and Upsert in multi-pod deployments.
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

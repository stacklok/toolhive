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

// RedisSessionDataStorage implements SessionDataStorage backed by Redis/Valkey.
//
// Metadata is serialised as a JSON object and stored with a sliding-window TTL:
// each Load call refreshes the key's expiry via GETEX so that active sessions
// never expire while they are in use.
//
// Because only plain map[string]string is stored, there are no type assertions
// or deserialisation tricks — Redis round-trips the map cleanly.
type RedisSessionDataStorage struct {
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration
}

// NewRedisSessionDataStorage constructs a RedisSessionDataStorage.
// cfg provides connection parameters; ttl is the sliding-window expiry applied
// on every Store and Load. The caller must call Close when done.
func NewRedisSessionDataStorage(ctx context.Context, cfg RedisConfig, ttl time.Duration) (*RedisSessionDataStorage, error) {
	if err := validateRedisConfig(&cfg); err != nil {
		return nil, fmt.Errorf("invalid redis configuration: %w", err)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("ttl must be a positive duration")
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = DefaultReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = DefaultWriteTimeout
	}

	tlsCfg, err := buildRedisTLSConfig(cfg.TLS)
	if err != nil {
		return nil, fmt.Errorf("tls configuration error: %w", err)
	}

	var client redis.UniversalClient
	if cfg.SentinelConfig != nil {
		client = redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:    cfg.SentinelConfig.MasterName,
			SentinelAddrs: cfg.SentinelConfig.SentinelAddrs,
			Username:      cfg.Username,
			Password:      cfg.Password,
			DB:            cfg.DB,
			DialTimeout:   cfg.DialTimeout,
			ReadTimeout:   cfg.ReadTimeout,
			WriteTimeout:  cfg.WriteTimeout,
			TLSConfig:     tlsCfg,
		})
	} else {
		client = redis.NewClient(&redis.Options{
			Addr:         cfg.Addr,
			Username:     cfg.Username,
			Password:     cfg.Password,
			DB:           cfg.DB,
			DialTimeout:  cfg.DialTimeout,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
			TLSConfig:    tlsCfg,
		})
	}

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
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

// Store serialises metadata as JSON and writes it to Redis with a sliding TTL.
func (s *RedisSessionDataStorage) Store(ctx context.Context, id string, metadata map[string]string) error {
	if id == "" {
		return fmt.Errorf("cannot store session data with empty ID")
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to serialise session metadata: %w", err)
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
		return nil, fmt.Errorf("failed to deserialise session metadata: %w", err)
	}
	return metadata, nil
}

// Exists checks whether the Redis key exists without refreshing its TTL.
// Uses the Redis EXISTS command, which does not extend key expiry.
func (s *RedisSessionDataStorage) Exists(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot check existence of session data with empty ID")
	}
	count, err := s.client.Exists(ctx, s.key(id)).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check session existence: %w", err)
	}
	return count > 0, nil
}

// StoreIfAbsent atomically creates session metadata only if the key does not
// already exist. Uses Redis SET NX (set-if-not-exists) to eliminate the
// TOCTOU race between Exists and Store in multi-pod deployments.
func (s *RedisSessionDataStorage) StoreIfAbsent(ctx context.Context, id string, metadata map[string]string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot store session data with empty ID")
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return false, fmt.Errorf("failed to serialise session metadata: %w", err)
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
	return s.client.Del(ctx, s.key(id)).Err()
}

// Close closes the underlying Redis client.
func (s *RedisSessionDataStorage) Close() error {
	return s.client.Close()
}

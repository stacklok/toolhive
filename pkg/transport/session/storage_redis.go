// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStorage implements the Storage interface backed by Redis.
type RedisStorage struct {
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration
}

func validateRedisConfig(cfg *RedisConfig) error {
	if cfg.Addr != "" && cfg.SentinelConfig != nil {
		return errors.New("addr and SentinelConfig are mutually exclusive")
	}
	if cfg.Addr == "" && cfg.SentinelConfig == nil {
		return errors.New("one of Addr (standalone) or SentinelConfig (sentinel) must be set")
	}
	if cfg.SentinelConfig != nil {
		if cfg.SentinelConfig.MasterName == "" {
			return errors.New("SentinelConfig.MasterName is required")
		}
		if len(cfg.SentinelConfig.SentinelAddrs) == 0 {
			return errors.New("SentinelConfig.SentinelAddrs must not be empty")
		}
	}
	if cfg.KeyPrefix == "" {
		return errors.New("KeyPrefix is required")
	}
	return nil
}

// NewRedisStorage constructs a RedisStorage from a RedisConfig.
// ttl is the expiry applied to every key on Store.
func NewRedisStorage(ctx context.Context, cfg RedisConfig, ttl time.Duration) (*RedisStorage, error) {
	if err := validateRedisConfig(&cfg); err != nil {
		return nil, fmt.Errorf("invalid redis configuration: %w", err)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("ttl must be a positive duration")
	}

	// Apply timeout defaults
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
		opts := &redis.FailoverOptions{
			MasterName:    cfg.SentinelConfig.MasterName,
			SentinelAddrs: cfg.SentinelConfig.SentinelAddrs,
			Password:      cfg.Password,
			DB:            cfg.DB,
			DialTimeout:   cfg.DialTimeout,
			ReadTimeout:   cfg.ReadTimeout,
			WriteTimeout:  cfg.WriteTimeout,
			TLSConfig:     tlsCfg,
		}
		client = redis.NewFailoverClient(opts)
	} else {
		opts := &redis.Options{
			Addr:         cfg.Addr,
			Password:     cfg.Password,
			DB:           cfg.DB,
			DialTimeout:  cfg.DialTimeout,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
			TLSConfig:    tlsCfg,
		}
		client = redis.NewClient(opts)
	}

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &RedisStorage{
		client:    client,
		keyPrefix: cfg.KeyPrefix,
		ttl:       ttl,
	}, nil
}

// newRedisStorageWithClient creates a RedisStorage with a pre-configured client.
// Intended for tests only (bypasses config validation and Ping); production callers
// must use NewRedisStorage.
func newRedisStorageWithClient(client redis.UniversalClient, keyPrefix string, ttl time.Duration) *RedisStorage {
	return &RedisStorage{
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       ttl,
	}
}

// buildRedisTLSConfig creates a *tls.Config from a RedisTLSConfig.
func buildRedisTLSConfig(cfg *RedisTLSConfig) (*tls.Config, error) {
	if cfg == nil {
		return nil, nil
	}
	tc := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // G402: configurable per-deployment
	}
	if len(cfg.CACert) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CACert) {
			return nil, fmt.Errorf("failed to parse CA certificate PEM data")
		}
		tc.RootCAs = pool
	}
	return tc, nil
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

// Touch refreshes the Redis TTL for the given session ID without loading the
// session data. Uses EXPIRE which is O(1) and far cheaper than GETEX.
// Returns ErrSessionNotFound when the key does not exist (EXPIRE returns false),
// so that RoutingStorage can detect and evict stale local-cache entries.
func (s *RedisStorage) Touch(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("cannot touch session with empty ID")
	}
	existed, err := s.client.Expire(ctx, s.key(id), s.ttl).Result()
	if err != nil {
		return fmt.Errorf("failed to touch session: %w", err)
	}
	if !existed {
		return ErrSessionNotFound
	}
	return nil
}

// Close closes the underlying Redis client connection.
func (s *RedisStorage) Close() error {
	return s.client.Close()
}

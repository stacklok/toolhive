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
	if cfg.KeyPrefix[len(cfg.KeyPrefix)-1] != ':' {
		return errors.New("KeyPrefix must end with ':' to avoid key collisions (e.g. \"thv:vmcp:session:\")")
	}
	return nil
}

// NewRedisStorage constructs a RedisStorage from a RedisConfig.
// ttl is the expiry applied to every key on Store and refreshed on every Load (sliding window).
// Because TTL is sliding, sessions remain valid indefinitely while actively used; revocation
// requires an explicit Delete call. There is no absolute maximum session lifetime.
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
			Username:      cfg.Username,
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
			Username:     cfg.Username,
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

// Exists checks whether a session key exists in Redis without refreshing its TTL.
// Uses EXISTS rather than GETEX so that idle sessions are not kept alive by the
// eviction-loop probes in sessionmanager.
func (s *RedisStorage) Exists(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot check session existence with empty ID")
	}
	n, err := s.client.Exists(ctx, s.key(id)).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check session existence: %w", err)
	}
	return n > 0, nil
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

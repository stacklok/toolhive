// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStorage implements the Storage interface using Redis as the backend.
// It supports standalone and Sentinel failover modes, enabling session sharing
// across multiple proxyrunner replicas for horizontal scaling.
type RedisStorage struct {
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration
}

// NewRedisStorage creates a Redis-backed session storage.
// cfg.Addr is used for standalone mode; cfg.SentinelConfig activates Sentinel failover.
// Returns an error if configuration validation fails or the connection cannot be established.
func NewRedisStorage(cfg RedisConfig, ttl time.Duration) (*RedisStorage, error) {
	if err := validateRedisConfig(&cfg); err != nil {
		return nil, fmt.Errorf("invalid redis configuration: %w", err)
	}

	// Apply timeout defaults
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultRedisDialTimeout
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = DefaultRedisReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = DefaultRedisWriteTimeout
	}

	var client redis.UniversalClient

	if cfg.SentinelConfig != nil {
		masterTLSCfg, err := buildTLSConfig(cfg.TLS)
		if err != nil {
			return nil, fmt.Errorf("master TLS config: %w", err)
		}
		sentinelTLSCfg, err := buildTLSConfig(cfg.SentinelConfig.SentinelTLS)
		if err != nil {
			return nil, fmt.Errorf("sentinel TLS config: %w", err)
		}
		opts := &redis.FailoverOptions{
			MasterName:    cfg.SentinelConfig.MasterName,
			SentinelAddrs: cfg.SentinelConfig.SentinelAddrs,
			DB:            cfg.SentinelConfig.DB,
			Password:      cfg.Password,
			DialTimeout:   cfg.DialTimeout,
			ReadTimeout:   cfg.ReadTimeout,
			WriteTimeout:  cfg.WriteTimeout,
		}
		// Use a per-connection dialer so master and sentinel can have different TLS configs.
		if masterTLSCfg != nil || sentinelTLSCfg != nil {
			opts.Dialer = sentinelAwareTLSDialer(masterTLSCfg, sentinelTLSCfg, opts.SentinelAddrs, cfg.DialTimeout)
		}
		client = redis.NewFailoverClient(opts)
	} else {
		tlsCfg, err := buildTLSConfig(cfg.TLS)
		if err != nil {
			return nil, fmt.Errorf("TLS config: %w", err)
		}
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

	// Verify connectivity
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return newRedisStorageWithClient(client, cfg.KeyPrefix, ttl), nil
}

// newRedisStorageWithClient creates a RedisStorage with a pre-configured client.
// Intended for use in tests (e.g. with miniredis).
func newRedisStorageWithClient(client redis.UniversalClient, keyPrefix string, ttl time.Duration) *RedisStorage {
	return &RedisStorage{
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       ttl,
	}
}

// buildTLSConfig creates a *tls.Config from a RedisTLSConfig.
// Returns nil when cfg is nil (plaintext). Returns an error if the CA cert is unparseable.
func buildTLSConfig(cfg *RedisTLSConfig) (*tls.Config, error) {
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
			return nil, fmt.Errorf("failed to parse CA cert PEM data")
		}
		tc.RootCAs = pool
	}
	return tc, nil
}

// sentinelAwareTLSDialer returns a dialer that applies different TLS configs to
// sentinel addresses vs the master. When a config is nil, plaintext is used for
// that connection type.
func sentinelAwareTLSDialer(
	masterTLS, sentinelTLS *tls.Config,
	sentinelAddrs []string,
	timeout time.Duration,
) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(_ context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: timeout}
		var tlsCfg *tls.Config
		if slices.Contains(sentinelAddrs, addr) {
			tlsCfg = sentinelTLS
		} else {
			tlsCfg = masterTLS
		}
		if tlsCfg == nil {
			return d.Dial(network, addr)
		}
		return tls.DialWithDialer(d, network, addr, tlsCfg)
	}
}

func validateRedisConfig(cfg *RedisConfig) error {
	if cfg.SentinelConfig == nil && cfg.Addr == "" {
		return errors.New("Addr is required when SentinelConfig is nil")
	}
	if cfg.KeyPrefix == "" {
		return errors.New("KeyPrefix is required")
	}
	return nil
}

func (s *RedisStorage) key(id string) string {
	return s.keyPrefix + id
}

// Store serializes and saves the session in Redis with the configured TTL.
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

// Load retrieves and deserializes a session from Redis.
// Returns ErrSessionNotFound if the key does not exist or has expired.
func (s *RedisStorage) Load(ctx context.Context, id string) (Session, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot load session with empty ID")
	}

	data, err := s.client.Get(ctx, s.key(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session from redis: %w", err)
	}

	sess, err := deserializeSession(data)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize session: %w", err)
	}
	return sess, nil
}

// Delete removes a session from Redis. A missing key is not an error.
func (s *RedisStorage) Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("cannot delete session with empty ID")
	}
	return s.client.Del(ctx, s.key(id)).Err()
}

// DeleteExpired is a no-op for Redis storage; Redis handles expiry via key TTLs.
func (s *RedisStorage) DeleteExpired(_ context.Context, _ time.Time) error {
	return nil
}

// Close releases the underlying Redis client connection.
func (s *RedisStorage) Close() error {
	return s.client.Close()
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import "time"

// Default timeouts for Redis operations, matching pkg/authserver/storage/redis.go.
const (
	DefaultRedisDialTimeout  = 5 * time.Second
	DefaultRedisReadTimeout  = 3 * time.Second
	DefaultRedisWriteTimeout = 3 * time.Second
)

// RedisConfig configures the Redis storage backend for session storage.
// Set Addr for standalone mode, or set SentinelConfig for Sentinel mode (mutually exclusive).
type RedisConfig struct {
	// Addr is the Redis server address ("host:port") for standalone mode.
	// Required when SentinelConfig is nil.
	Addr string

	// SentinelConfig activates Redis Sentinel failover mode.
	// When non-nil, Addr is ignored.
	SentinelConfig *SentinelConfig

	// Password is the Redis AUTH password (optional).
	Password string //nolint:gosec // G117: field legitimately holds sensitive data

	// DB is the Redis database number (default 0).
	DB int

	// KeyPrefix is prepended to every session key (e.g. "thv:vmcp:session:").
	// Required — prevents collisions when multiple components share a Redis instance.
	KeyPrefix string

	// DialTimeout is the timeout for establishing a connection (default 5s).
	DialTimeout time.Duration

	// ReadTimeout is the timeout for socket reads (default 3s).
	ReadTimeout time.Duration

	// WriteTimeout is the timeout for socket writes (default 3s).
	WriteTimeout time.Duration

	// TLS configures TLS for connections to the Redis master.
	// When nil, connections are plaintext.
	TLS *RedisTLSConfig
}

// SentinelConfig contains Redis Sentinel connection details.
type SentinelConfig struct {
	// MasterName is the name of the Sentinel master set.
	MasterName string

	// SentinelAddrs is the list of Sentinel node addresses ("host:port").
	SentinelAddrs []string

	// DB is the Redis database number on the master (default 0).
	DB int

	// SentinelTLS configures TLS for connections to Sentinel nodes.
	// When nil, Sentinel connections are plaintext.
	SentinelTLS *RedisTLSConfig
}

// RedisTLSConfig holds TLS configuration for a Redis connection.
// Presence of this struct enables TLS for that connection type.
type RedisTLSConfig struct {
	// InsecureSkipVerify disables certificate verification.
	InsecureSkipVerify bool

	// CACert is PEM-encoded CA certificate data for server verification.
	// When nil, the system root CAs are used.
	CACert []byte
}

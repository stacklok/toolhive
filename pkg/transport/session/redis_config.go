// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import "time"

// RedisPasswordEnvVar is the environment variable name for the Redis session storage password.
// The operator injects this as a SecretKeyRef when sessionStorage.provider is "redis"
// and passwordRef is set.
// #nosec G101 -- This is an environment variable name, not a hardcoded credential
const RedisPasswordEnvVar = "THV_SESSION_REDIS_PASSWORD"

// Default timeouts for Redis operations.
const (
	DefaultDialTimeout  = 5 * time.Second
	DefaultReadTimeout  = 3 * time.Second
	DefaultWriteTimeout = 3 * time.Second
)

// RedisConfig configures the Redis storage backend for session storage.
// Addr is used for standalone; SentinelConfig activates Sentinel mode (mutually exclusive).
type RedisConfig struct {
	// Addr is the Redis server address for standalone mode (e.g., "host:port").
	Addr string

	// SentinelConfig, when non-nil, activates Sentinel mode. Mutually exclusive with Addr.
	SentinelConfig *SentinelConfig

	// Username is the Redis ACL username (Redis 6.0+). When non-empty, both
	// Username and Password are sent as ACL credentials (AUTH username password).
	// Leave empty to authenticate as the default user (legacy AUTH password).
	Username string

	// Password is the Redis AUTH password. Used with Username for ACL auth,
	// or alone for legacy AUTH with the default user.
	Password string //nolint:gosec // G101: not a hardcoded credential

	// DB is the Redis database index.
	DB int

	// KeyPrefix namespaces all session keys (e.g., "thv:vmcp:session:").
	KeyPrefix string

	// DialTimeout is the timeout for establishing a connection. Defaults to 5s.
	DialTimeout time.Duration

	// ReadTimeout is the timeout for read operations. Defaults to 3s.
	ReadTimeout time.Duration

	// WriteTimeout is the timeout for write operations. Defaults to 3s.
	WriteTimeout time.Duration

	// TLS configures TLS for Redis connections. nil means plaintext.
	TLS *RedisTLSConfig
}

// SentinelConfig contains Redis Sentinel configuration.
type SentinelConfig struct {
	MasterName    string
	SentinelAddrs []string
}

// RedisTLSConfig holds TLS configuration for Redis connections.
// Presence of this struct enables TLS for the connection.
type RedisTLSConfig struct {
	// InsecureSkipVerify skips TLS certificate verification.
	InsecureSkipVerify bool

	// CACert is the PEM-encoded CA certificate for verifying the server.
	// When nil, the system root CAs are used.
	CACert []byte
}

// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import "time"

// Type defines the type of storage backend.
type Type string

const (
	// TypeMemory uses in-memory storage (default).
	TypeMemory Type = "memory"

	// TypeRedis uses Redis for persistent storage.
	TypeRedis Type = "redis"

	// DefaultCleanupInterval is how often the background cleanup runs.
	DefaultCleanupInterval = 5 * time.Minute

	// DefaultAccessTokenTTL is the default TTL for access tokens when not extractable from session.
	DefaultAccessTokenTTL = 1 * time.Hour

	// DefaultRefreshTokenTTL is the default TTL for refresh tokens when not extractable from session.
	DefaultRefreshTokenTTL = 30 * 24 * time.Hour // 30 days

	// DefaultRedisKeyPrefix is the default key prefix for Redis storage.
	DefaultRedisKeyPrefix = "thv:authserver:"

	// DefaultAuthCodeTTL is the default TTL for authorization codes (RFC 6749 recommendation).
	DefaultAuthCodeTTL = 10 * time.Minute

	// DefaultInvalidatedCodeTTL is how long invalidated codes are kept for replay detection.
	DefaultInvalidatedCodeTTL = 30 * time.Minute

	// DefaultPKCETTL is the default TTL for PKCE requests (same as auth codes).
	DefaultPKCETTL = 10 * time.Minute
)

// Config configures the storage backend.
type Config struct {
	// Type specifies the storage backend type. Defaults to memory.
	Type Type

	// CleanupInterval for expired entries (memory storage only).
	CleanupInterval time.Duration

	// RedisURL is the Redis connection URL (e.g., redis://localhost:6379/0).
	// Required when Type is TypeRedis.
	RedisURL string

	// RedisPassword is the Redis password.
	RedisPassword string

	// KeyPrefix is the prefix for all Redis keys.
	// Defaults to DefaultRedisKeyPrefix if not set.
	KeyPrefix string
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Type:            TypeMemory,
		CleanupInterval: DefaultCleanupInterval,
	}
}

// RunConfig is the serializable storage configuration for RunConfig.
// This is used when the config needs to be passed across process boundaries
// (e.g., in Kubernetes operator).
type RunConfig struct {
	// Type specifies the storage backend type. Defaults to "memory".
	Type string `json:"type,omitempty" yaml:"type,omitempty"`

	// RedisURL is the Redis connection URL.
	RedisURL string `json:"redis_url,omitempty" yaml:"redis_url,omitempty"`

	// RedisPassword is the Redis password (direct value, for non-sensitive environments).
	RedisPassword string `json:"redis_password,omitempty" yaml:"redis_password,omitempty"`

	// RedisPasswordFile is the path to a file containing the Redis password.
	// Takes precedence over RedisPassword.
	RedisPasswordFile string `json:"redis_password_file,omitempty" yaml:"redis_password_file,omitempty"`

	// KeyPrefix is the prefix for all Redis keys.
	KeyPrefix string `json:"key_prefix,omitempty" yaml:"key_prefix,omitempty"`
}

// RedisPasswordEnvVar is the environment variable for Redis password.
//
//nolint:gosec // G101: This is an environment variable name, not a credential
const RedisPasswordEnvVar = "TOOLHIVE_AUTHSERVER_REDIS_PASSWORD"

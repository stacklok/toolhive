// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import "time"

// Type defines the type of storage backend.
type Type string

const (
	// TypeMemory uses in-memory storage (default).
	TypeMemory Type = "memory"

	// TypeRedis uses Redis Sentinel-backed storage for distributed deployments.
	TypeRedis Type = "redis"

	// DefaultCleanupInterval is how often the background cleanup runs.
	DefaultCleanupInterval = 5 * time.Minute

	// DefaultAccessTokenTTL is the default TTL for access tokens when not extractable from session.
	DefaultAccessTokenTTL = 1 * time.Hour

	// DefaultRefreshTokenTTL is the default TTL for refresh tokens when not extractable from session.
	DefaultRefreshTokenTTL = 30 * 24 * time.Hour // 30 days

	// DefaultAuthCodeTTL is the default TTL for authorization codes (RFC 6749 recommendation).
	DefaultAuthCodeTTL = 10 * time.Minute

	// DefaultInvalidatedCodeTTL is how long invalidated codes are kept for replay detection.
	DefaultInvalidatedCodeTTL = 30 * time.Minute

	// DefaultPKCETTL is the default TTL for PKCE requests (same as auth codes).
	DefaultPKCETTL = 10 * time.Minute

	// DefaultPublicClientTTL is the TTL for dynamically registered public clients.
	// This prevents unbounded growth from DCR. Confidential clients don't expire.
	DefaultPublicClientTTL = 30 * 24 * time.Hour // 30 days
)

// Config configures the storage backend.
type Config struct {
	// Type specifies the storage backend type. Defaults to memory.
	Type Type
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Type: TypeMemory,
	}
}

// RunConfig is the serializable storage configuration for RunConfig.
// This is used when the config needs to be passed across process boundaries
// (e.g., in Kubernetes operator).
type RunConfig struct {
	// Type specifies the storage backend type. Defaults to "memory".
	Type string `json:"type,omitempty" yaml:"type,omitempty"`

	// RedisConfig is the Redis-specific configuration when Type is "redis".
	RedisConfig *RedisRunConfig `json:"redisConfig,omitempty" yaml:"redisConfig,omitempty"`
}

// RedisRunConfig is the serializable Redis configuration for RunConfig.
// This is designed for Sentinel-only deployments with ACL user authentication.
type RedisRunConfig struct {
	// DeploymentMode must be "sentinel" - only Sentinel deployments are supported.
	DeploymentMode string `json:"deploymentMode" yaml:"deploymentMode"`

	// SentinelConfig contains Sentinel-specific configuration.
	SentinelConfig *SentinelRunConfig `json:"sentinelConfig,omitempty" yaml:"sentinelConfig,omitempty"`

	// AuthType must be "aclUser" - only ACL user authentication is supported.
	AuthType string `json:"authType" yaml:"authType"`

	// ACLUserConfig contains ACL user authentication configuration.
	ACLUserConfig *ACLUserRunConfig `json:"aclUserConfig,omitempty" yaml:"aclUserConfig,omitempty"`

	// KeyPrefix for multi-tenancy, typically "thv:auth:{ns}:{name}:".
	KeyPrefix string `json:"keyPrefix" yaml:"keyPrefix"`

	// DialTimeout is the timeout for establishing connections (e.g., "5s").
	DialTimeout string `json:"dialTimeout,omitempty" yaml:"dialTimeout,omitempty"`

	// ReadTimeout is the timeout for read operations (e.g., "3s").
	ReadTimeout string `json:"readTimeout,omitempty" yaml:"readTimeout,omitempty"`

	// WriteTimeout is the timeout for write operations (e.g., "3s").
	WriteTimeout string `json:"writeTimeout,omitempty" yaml:"writeTimeout,omitempty"`
}

// SentinelRunConfig contains Redis Sentinel configuration.
type SentinelRunConfig struct {
	// MasterName is the name of the Redis Sentinel master.
	MasterName string `json:"masterName" yaml:"masterName"`

	// SentinelAddrs is the list of Sentinel addresses (host:port).
	SentinelAddrs []string `json:"sentinelAddrs" yaml:"sentinelAddrs"`

	// DB is the Redis database number (default: 0).
	DB int `json:"db,omitempty" yaml:"db,omitempty"`
}

// ACLUserRunConfig contains Redis ACL user authentication configuration.
// Credentials are read from environment variables for security.
type ACLUserRunConfig struct {
	// UsernameEnvVar is the environment variable containing the Redis username.
	UsernameEnvVar string `json:"usernameEnvVar" yaml:"usernameEnvVar"`

	// PasswordEnvVar is the environment variable containing the Redis password.
	PasswordEnvVar string `json:"passwordEnvVar" yaml:"passwordEnvVar"`
}

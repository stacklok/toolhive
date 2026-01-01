package authserver

import "time"

// StorageType defines the type of storage backend.
type StorageType string

const (
	// StorageTypeMemory uses in-memory storage (default).
	StorageTypeMemory StorageType = "memory"

	// StorageTypeRedis uses Redis for persistent storage.
	StorageTypeRedis StorageType = "redis"

	// DefaultCleanupInterval is how often the background cleanup runs.
	DefaultCleanupInterval = 5 * time.Minute

	// DefaultAccessTokenTTL is the default TTL for access tokens when not extractable from session.
	DefaultAccessTokenTTL = 1 * time.Hour

	// DefaultRefreshTokenTTL is the default TTL for refresh tokens when not extractable from session.
	DefaultRefreshTokenTTL = 30 * 24 * time.Hour // 30 days

	// DefaultRedisKeyPrefix is the default key prefix for Redis storage.
	DefaultRedisKeyPrefix = "thv:authserver:"
)

// StorageConfig configures the storage backend.
type StorageConfig struct {
	// Type specifies the storage backend type. Defaults to memory.
	Type StorageType

	// CleanupInterval for expired entries (memory storage only).
	CleanupInterval time.Duration

	// RedisURL is the Redis connection URL (e.g., redis://localhost:6379/0).
	// Required when Type is StorageTypeRedis.
	RedisURL string

	// RedisPassword is the Redis password.
	RedisPassword string

	// KeyPrefix is the prefix for all Redis keys.
	// Defaults to DefaultRedisKeyPrefix if not set.
	KeyPrefix string
}

// DefaultStorageConfig returns sensible defaults.
func DefaultStorageConfig() *StorageConfig {
	return &StorageConfig{
		Type:            StorageTypeMemory,
		CleanupInterval: DefaultCleanupInterval,
	}
}

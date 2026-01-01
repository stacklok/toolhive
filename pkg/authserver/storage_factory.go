package authserver

import (
	"fmt"
	"os"
	"strings"
)

// NewStorage creates a Storage implementation based on config.
// If config is nil, defaults to in-memory storage.
func NewStorage(config *StorageConfig) (Storage, error) {
	if config == nil {
		config = DefaultStorageConfig()
	}

	switch config.Type {
	case StorageTypeMemory, "":
		opts := []MemoryStorageOption{}
		if config.CleanupInterval > 0 {
			opts = append(opts, WithCleanupInterval(config.CleanupInterval))
		}
		return NewMemoryStorage(opts...), nil

	case StorageTypeRedis:
		if config.RedisURL == "" {
			return nil, fmt.Errorf("redis_url is required for Redis storage")
		}

		opts := []RedisStorageOption{}
		if config.KeyPrefix != "" {
			opts = append(opts, WithKeyPrefix(config.KeyPrefix))
		}

		return NewRedisStorage(config.RedisURL, config.RedisPassword, opts...)

	default:
		return nil, fmt.Errorf("unknown storage type: %s", config.Type)
	}
}

// NewStorageFromRunConfig creates a Storage implementation from a RunConfig.
// It handles password resolution from files and applies sensible defaults.
func NewStorageFromRunConfig(cfg *StorageRunConfig) (Storage, error) {
	if cfg == nil {
		// Default to in-memory storage
		return NewStorage(nil)
	}

	storageType := StorageType(cfg.Type)
	if storageType == "" {
		storageType = StorageTypeMemory
	}

	switch storageType {
	case StorageTypeMemory:
		return NewStorage(nil)

	case StorageTypeRedis:
		if cfg.RedisURL == "" {
			return nil, fmt.Errorf("redis_url is required for Redis storage")
		}

		// Resolve password from file if specified
		password, err := resolveRedisPassword(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve Redis password: %w", err)
		}

		// Apply key prefix default
		keyPrefix := cfg.KeyPrefix
		if keyPrefix == "" {
			keyPrefix = DefaultRedisKeyPrefix
		}

		config := &StorageConfig{
			Type:          StorageTypeRedis,
			RedisURL:      cfg.RedisURL,
			RedisPassword: password,
			KeyPrefix:     keyPrefix,
		}

		return NewStorage(config)

	default:
		return nil, fmt.Errorf("unknown storage type: %s", cfg.Type)
	}
}

// resolveRedisPassword resolves the Redis password from the RunConfig.
// Priority: direct value > file > environment variable
func resolveRedisPassword(cfg *StorageRunConfig) (string, error) {
	// Direct value takes precedence
	if cfg.RedisPassword != "" {
		return cfg.RedisPassword, nil
	}

	// Read from file if specified
	if cfg.RedisPasswordFile != "" {
		data, err := os.ReadFile(cfg.RedisPasswordFile) // #nosec G304 - file path is provided by user via config
		if err != nil {
			return "", fmt.Errorf("failed to read Redis password file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}

	// Fallback to environment variable
	if envPassword := os.Getenv(RedisPasswordEnvVar); envPassword != "" {
		return envPassword, nil
	}

	return "", nil
}

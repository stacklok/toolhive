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

import (
	"fmt"
	"os"
	"strings"
)

// New creates a Storage implementation based on config.
// If config is nil, defaults to in-memory storage.
// For Redis storage, a sessionFactory should be provided via options.
func New(config *Config, opts ...RedisOption) (Storage, error) {
	if config == nil {
		config = DefaultConfig()
	}

	switch config.Type {
	case TypeMemory, "":
		memOpts := []MemoryStorageOption{}
		if config.CleanupInterval > 0 {
			memOpts = append(memOpts, WithCleanupInterval(config.CleanupInterval))
		}
		return NewMemoryStorage(memOpts...), nil

	case TypeRedis:
		if config.RedisURL == "" {
			return nil, fmt.Errorf("redis_url is required for Redis storage")
		}

		redisOpts := opts
		if config.KeyPrefix != "" {
			redisOpts = append(redisOpts, WithRedisKeyPrefix(config.KeyPrefix))
		}

		return NewRedisStorage(config.RedisURL, config.RedisPassword, redisOpts...)

	default:
		return nil, fmt.Errorf("unknown storage type: %s", config.Type)
	}
}

// NewFromRunConfig creates a Storage implementation from a RunConfig.
// It handles password resolution from files and applies sensible defaults.
// For Redis storage, a sessionFactory should be provided via options.
func NewFromRunConfig(cfg *RunConfig, opts ...RedisOption) (Storage, error) {
	if cfg == nil {
		// Default to in-memory storage
		return New(nil)
	}

	storageType := Type(cfg.Type)
	if storageType == "" {
		storageType = TypeMemory
	}

	switch storageType {
	case TypeMemory:
		return New(nil)

	case TypeRedis:
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

		config := &Config{
			Type:          TypeRedis,
			RedisURL:      cfg.RedisURL,
			RedisPassword: password,
			KeyPrefix:     keyPrefix,
		}

		return New(config, opts...)

	default:
		return nil, fmt.Errorf("unknown storage type: %s", cfg.Type)
	}
}

// resolveRedisPassword resolves the Redis password from the RunConfig.
// Priority: direct value > file > environment variable
func resolveRedisPassword(cfg *RunConfig) (string, error) {
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

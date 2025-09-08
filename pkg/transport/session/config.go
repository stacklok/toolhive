// Package session provides session management with pluggable storage backends.
package session

import (
	"fmt"
	"time"
)

// Config defines the configuration for session management.
type Config struct {
	// Storage backend selection
	StorageType string `json:"storage_type" yaml:"storage_type"` // "local" or "redis"

	// Common settings
	TTL time.Duration `json:"ttl" yaml:"ttl"`

	// Redis configuration (only if StorageType is "redis")
	Redis *RedisConfig `json:"redis,omitempty" yaml:"redis,omitempty"`
}

// CreateStorage creates a storage backend based on the configuration.
func (c *Config) CreateStorage() (Storage, error) {
	// Set defaults
	if c.StorageType == "" {
		c.StorageType = "local"
	}
	if c.TTL == 0 {
		c.TTL = 30 * time.Minute
	}

	switch c.StorageType {
	case "local":
		return NewLocalStorage(), nil
	case "redis", "valkey": // Valkey is Redis-compatible
		if c.Redis == nil {
			return nil, fmt.Errorf("redis configuration required")
		}
		c.Redis.TTL = c.TTL // Ensure TTL is consistent
		return NewRedisStorage(c.Redis)
	default:
		return nil, fmt.Errorf("unknown storage type: %s", c.StorageType)
	}
}

// CreateManagerFromConfig creates a session manager from configuration.
func CreateManagerFromConfig(config *Config) (*Manager, error) {
	storage, err := config.CreateStorage()
	if err != nil {
		return nil, err
	}

	factory := func(id string) Session {
		return NewProxySession(id)
	}

	return NewManager(config.TTL, factory, storage), nil
}

// CreateTypedManagerFromConfig creates a typed session manager from configuration.
func CreateTypedManagerFromConfig(config *Config, sessionType SessionType) (*Manager, error) {
	storage, err := config.CreateStorage()
	if err != nil {
		return nil, err
	}

	return NewTypedManager(config.TTL, sessionType, storage), nil
}

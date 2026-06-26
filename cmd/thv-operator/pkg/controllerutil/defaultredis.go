// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import "os"

// DefaultRedisConfig holds global Redis defaults read from the operator's own
// environment variables. These are injected by the Helm chart when
// operator.defaultRedis.addr (or global.redis.host) is set.
type DefaultRedisConfig struct {
	// Addr is the Redis server address (host:port).
	Addr string
	// SecretName is the Kubernetes Secret name containing the Redis password.
	// Empty when no password is configured.
	SecretName string
	// SecretKey is the key within SecretName that holds the password.
	// Defaults to "redis-password" when SecretName is set and no key is provided.
	SecretKey string
}

// ReadDefaultRedisConfig returns the global Redis defaults from the operator's
// environment, or nil when TOOLHIVE_DEFAULT_REDIS_ADDR is not set.
func ReadDefaultRedisConfig() *DefaultRedisConfig {
	addr := os.Getenv("TOOLHIVE_DEFAULT_REDIS_ADDR")
	if addr == "" {
		return nil
	}
	cfg := &DefaultRedisConfig{
		Addr:       addr,
		SecretName: os.Getenv("TOOLHIVE_DEFAULT_REDIS_SECRET_NAME"),
		SecretKey:  os.Getenv("TOOLHIVE_DEFAULT_REDIS_SECRET_KEY"),
	}
	if cfg.SecretName != "" && cfg.SecretKey == "" {
		cfg.SecretKey = "redis-password"
	}
	return cfg
}

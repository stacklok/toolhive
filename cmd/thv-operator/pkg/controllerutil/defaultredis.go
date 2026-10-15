// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"fmt"
	"os"
	"strconv"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

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
	// TLS configures verified transport security for the global Redis fallback.
	// A referenced CA Secret must exist in each workload's namespace.
	TLS *mcpv1beta1.RedisTLSConfig
}

// ReadDefaultRedisConfig returns the global Redis defaults from the operator's
// environment, or nil when TOOLHIVE_DEFAULT_REDIS_ADDR is not set. Invalid TLS
// settings panic rather than silently falling back to plaintext; the operator
// calls ValidateDefaultRedisConfig at startup to report these errors gracefully.
func ReadDefaultRedisConfig() *DefaultRedisConfig {
	cfg, err := readDefaultRedisConfig()
	if err != nil {
		panic(err)
	}
	return cfg
}

// ValidateDefaultRedisConfig validates the global Redis environment before the
// operator starts its controllers. It validates references, not Secret contents;
// CA Secrets are projected separately into each workload's namespace.
func ValidateDefaultRedisConfig() error {
	_, err := readDefaultRedisConfig()
	return err
}

func readDefaultRedisConfig() (*DefaultRedisConfig, error) {
	enabled := false
	if raw := os.Getenv("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED must be a boolean: %w", err)
		}
		enabled = parsed
	}
	caName := os.Getenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_NAME")
	caKey := os.Getenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY")
	if (caName == "") != (caKey == "") {
		return nil, fmt.Errorf("default Redis TLS CA Secret name and key must both be set")
	}
	if caName != "" && !enabled {
		return nil, fmt.Errorf("default Redis TLS CA Secret requires TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED=true")
	}
	addr := os.Getenv("TOOLHIVE_DEFAULT_REDIS_ADDR")
	if addr == "" {
		if enabled {
			return nil, fmt.Errorf("default Redis TLS requires TOOLHIVE_DEFAULT_REDIS_ADDR")
		}
		return nil, nil
	}
	cfg := &DefaultRedisConfig{
		Addr:       addr,
		SecretName: os.Getenv("TOOLHIVE_DEFAULT_REDIS_SECRET_NAME"),
		SecretKey:  os.Getenv("TOOLHIVE_DEFAULT_REDIS_SECRET_KEY"),
	}
	if cfg.SecretName != "" && cfg.SecretKey == "" {
		cfg.SecretKey = "redis-password"
	}
	if enabled {
		cfg.TLS = &mcpv1beta1.RedisTLSConfig{}
		if caName != "" {
			cfg.TLS.CACertSecretRef = &mcpv1beta1.SecretKeyRef{Name: caName, Key: caKey}
		}
	}
	return cfg, nil
}

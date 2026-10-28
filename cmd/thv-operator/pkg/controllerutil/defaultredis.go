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
// operator.defaultRedis.addr (or global.redis.host) is set. Controllers share the
// startup snapshot and must treat it as read-only.
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

// LoadDefaultRedisConfigFromEnv reads and validates the global Redis defaults at
// operator startup, or returns nil when no default address is configured. Pass
// the returned snapshot to controllers instead of reading the environment during
// reconciliation. Validation checks references, not Secret contents; Secrets must
// exist in each workload's namespace and are projected separately by Kubernetes.
func LoadDefaultRedisConfigFromEnv() (*DefaultRedisConfig, error) {
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

// SessionStorage returns the global address and TLS settings as an existing
// session storage API type, or nil when no default is configured. Password Secret
// references are injected separately by deployment builders. The snapshot is
// read-only; copying TLS keeps callers from mutating the shared default.
func (d *DefaultRedisConfig) SessionStorage() *mcpv1beta1.SessionStorageConfig {
	if d == nil {
		return nil
	}
	return &mcpv1beta1.SessionStorageConfig{
		Provider: mcpv1beta1.SessionStorageProviderRedis,
		Address:  d.Addr,
		TLS:      d.TLS.DeepCopy(),
	}
}

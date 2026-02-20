// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package registry provides MCP server registry management functionality.
// It supports multiple registry sources including embedded data, local files,
// remote URLs, and API endpoints, with optional caching and conversion capabilities.
package registry

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry/auth"
	"github.com/stacklok/toolhive/pkg/secrets"
)

var (
	defaultProvider     Provider
	defaultProviderOnce sync.Once
	defaultProviderErr  error
	// defaultProviderMu protects the ResetDefaultProvider operation
	// to prevent race conditions when resetting the sync.Once.
	// The mutex is NOT needed for GetDefaultProviderWithConfig since
	// sync.Once already provides thread-safety for initialization.
	defaultProviderMu sync.Mutex
)

// NewRegistryProvider creates a new registry provider based on the configuration.
// Returns an error if a custom registry is configured but cannot be reached.
func NewRegistryProvider(cfg *config.Config) (Provider, error) {
	// Priority order:
	// 1. API URL (if configured) - for live MCP Registry API queries
	// 2. Remote URL (if configured) - for static JSON over HTTP
	// 3. Local file path (if configured) - for local JSON file
	// 4. Default - embedded registry data

	// Create token source if registry auth is configured
	tokenSource := resolveTokenSource(cfg)

	if cfg != nil && len(cfg.RegistryApiUrl) > 0 {
		provider, err := NewCachedAPIRegistryProvider(cfg.RegistryApiUrl, cfg.AllowPrivateRegistryIp, true, tokenSource)
		if err != nil {
			return nil, fmt.Errorf("custom registry API at %s is not reachable: %w", cfg.RegistryApiUrl, err)
		}
		return provider, nil
	}
	if cfg != nil && len(cfg.RegistryUrl) > 0 {
		provider, err := NewRemoteRegistryProvider(cfg.RegistryUrl, cfg.AllowPrivateRegistryIp)
		if err != nil {
			return nil, fmt.Errorf("custom registry at %s is not reachable: %w", cfg.RegistryUrl, err)
		}
		return provider, nil
	}
	if cfg != nil && len(cfg.LocalRegistryPath) > 0 {
		return NewLocalRegistryProvider(cfg.LocalRegistryPath), nil
	}
	return NewLocalRegistryProvider(), nil
}

// GetDefaultProvider returns the default registry provider instance
// This maintains backward compatibility with the existing singleton pattern
func GetDefaultProvider() (Provider, error) {
	return GetDefaultProviderWithConfig(config.NewDefaultProvider())
}

// GetDefaultProviderWithConfig returns a registry provider using the given config provider
// This allows tests to inject their own config provider
func GetDefaultProviderWithConfig(configProvider config.Provider) (Provider, error) {
	defaultProviderOnce.Do(func() {
		cfg, err := configProvider.LoadOrCreateConfig()
		if err != nil {
			defaultProviderErr = err
			return
		}
		defaultProvider, defaultProviderErr = NewRegistryProvider(cfg)
	})

	return defaultProvider, defaultProviderErr
}

// ResetDefaultProvider clears the cached default provider instance
// This allows the provider to be recreated with updated configuration.
// This function is thread-safe and can be called concurrently.
// The mutex is required here because we're modifying the sync.Once itself,
// which is not a thread-safe operation.
func ResetDefaultProvider() {
	defaultProviderMu.Lock()
	defer defaultProviderMu.Unlock()

	// Reset the sync.Once to allow re-initialization
	defaultProviderOnce = sync.Once{}
	defaultProvider = nil
	defaultProviderErr = nil
}

// resolveTokenSource creates a TokenSource from the config if registry auth is configured.
// Returns nil if no auth is configured or if token source creation fails (logs warning).
func resolveTokenSource(cfg *config.Config) auth.TokenSource {
	if cfg == nil || cfg.RegistryAuth.Type != "oauth" || cfg.RegistryAuth.OAuth == nil {
		return nil
	}

	// Try to create secrets provider for token persistence
	var secretsProvider secrets.Provider
	providerType, err := cfg.Secrets.GetProviderType()
	if err == nil {
		secretsProvider, err = secrets.CreateSecretProvider(providerType)
		if err != nil {
			slog.Debug("Failed to create secrets provider for registry auth, tokens will not be persisted", "error", err)
		}
	}

	tokenSource, err := auth.NewTokenSource(cfg.RegistryAuth.OAuth, secretsProvider)
	if err != nil {
		slog.Warn("Failed to create registry auth token source", "error", err)
		return nil
	}

	return tokenSource
}

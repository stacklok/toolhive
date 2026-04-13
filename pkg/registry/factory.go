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
	"sync/atomic"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry/auth"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// providerState groups the sync.Once with the values it initialises.
// Storing all three together behind an atomic pointer means ResetDefaultProvider
// can swap in a fresh struct without ever writing to a struct that another
// goroutine may be reading — eliminating the data race between Reset and Do.
type providerState struct {
	once     sync.Once
	provider Provider
	err      error
}

// currentProviderState is the live singleton state. Replaced atomically by
// ResetDefaultProvider; never mutated after creation except inside once.Do.
var currentProviderState atomic.Pointer[providerState]

func init() {
	currentProviderState.Store(&providerState{})
}

// ProviderOption configures optional behavior for NewRegistryProvider.
type ProviderOption func(*providerOptions)

type providerOptions struct {
	interactive bool
}

// WithInteractive sets whether browser-based OAuth flows are allowed.
// Defaults to true (CLI mode). Pass false for headless/serve mode.
func WithInteractive(interactive bool) ProviderOption {
	return func(o *providerOptions) { o.interactive = interactive }
}

// NewRegistryProvider creates a new registry provider based on the configuration.
// Returns an error if a custom registry is configured but cannot be reached.
func NewRegistryProvider(cfg *config.Config, opts ...ProviderOption) (Provider, error) {
	options := &providerOptions{interactive: true}
	for _, opt := range opts {
		opt(options)
	}

	// Priority order:
	// 1. API URL (if configured) - for live MCP Registry API queries
	// 2. Remote URL (if configured) - for static JSON over HTTP
	// 3. Local file path (if configured) - for local JSON file
	// 4. Default - embedded registry data

	// Create token source if registry auth is configured.
	// Auth only applies to API registry providers; remote URL and local file
	// providers do not support authentication.
	tokenSource := resolveTokenSource(cfg, options.interactive)

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

// GetDefaultProvider returns the default registry provider instance.
// config.NewProvider() is called inside the sync.Once closure so that any
// registered ProviderFactory is invoked at most once, not on every call.
func GetDefaultProvider() (Provider, error) {
	s := currentProviderState.Load()
	s.once.Do(func() {
		cfg, err := config.NewProvider().LoadOrCreateConfig()
		if err != nil {
			s.err = err
			return
		}
		s.provider, s.err = NewRegistryProvider(cfg)
	})
	return s.provider, s.err
}

// GetDefaultProviderWithConfig returns a registry provider using the given config provider.
// This allows tests to inject their own config provider.
// The interactive flag controls whether browser-based OAuth flows are allowed.
// Pass true for CLI contexts, false for headless/serve mode.
func GetDefaultProviderWithConfig(configProvider config.Provider, opts ...ProviderOption) (Provider, error) {
	s := currentProviderState.Load()
	s.once.Do(func() {
		cfg, err := configProvider.LoadOrCreateConfig()
		if err != nil {
			s.err = err
			return
		}
		s.provider, s.err = NewRegistryProvider(cfg, opts...)
	})
	return s.provider, s.err
}

// ResetDefaultProvider clears the cached default provider instance so the
// next call to GetDefaultProvider or GetDefaultProviderWithConfig creates a
// fresh one. The atomic swap is safe to call concurrently: goroutines that
// already hold a reference to the old state finish against that state cleanly,
// while goroutines that load after the swap initialise against the new state.
func ResetDefaultProvider() {
	currentProviderState.Store(&providerState{})
}

// resolveTokenSource creates a TokenSource from the config if registry auth is configured.
// Returns nil if no auth is configured or if token source creation fails (logs warning).
func resolveTokenSource(cfg *config.Config, interactive bool) auth.TokenSource {
	if cfg == nil || cfg.RegistryAuth.Type != config.RegistryAuthTypeOAuth || cfg.RegistryAuth.OAuth == nil {
		return nil
	}

	// Try to create secrets provider for token persistence
	var secretsProvider secrets.Provider
	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		slog.Debug("Secrets provider not available for registry auth token persistence",
			"error", err)
	} else {
		secretsProvider, err = secrets.CreateSecretProvider(providerType)
		if err != nil {
			slog.Warn("Failed to create secrets provider for registry auth, tokens will not be persisted",
				"error", err)
		} else {
			slog.Debug("Secrets provider created for registry auth token persistence",
				"provider_type", providerType)
		}
	}

	tokenSource, err := auth.NewTokenSource(
		cfg.RegistryAuth.OAuth, cfg.RegistryApiUrl, secretsProvider,
		interactive, auth.RegistryConfigUpdater(),
	)
	if err != nil {
		slog.Warn("Failed to create registry auth token source", "error", err)
		return nil
	}

	return tokenSource
}

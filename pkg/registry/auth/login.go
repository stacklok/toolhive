// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/adrg/xdg"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// Login performs an interactive OAuth login against the configured registry.
func Login(ctx context.Context, configProvider config.Provider, secretsProvider secrets.Provider) error {
	cfg, err := configProvider.LoadOrCreateConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := validateOAuthConfig(cfg); err != nil {
		return err
	}

	registryURL := registryURLFromConfig(cfg)

	ts, err := NewTokenSource(cfg.RegistryAuth.OAuth, registryURL, secretsProvider, true)
	if err != nil {
		return fmt.Errorf("creating token source: %w", err)
	}
	if _, err := ts.Token(ctx); err != nil {
		return fmt.Errorf("obtaining token: %w", err)
	}
	return nil
}

// Logout clears cached OAuth credentials for the configured registry.
func Logout(ctx context.Context, configProvider config.Provider, secretsProvider secrets.Provider) error {
	cfg, err := configProvider.LoadOrCreateConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := validateOAuthConfig(cfg); err != nil {
		return err
	}

	if ref := cfg.RegistryAuth.OAuth.CachedRefreshTokenRef; ref != "" {
		if err := secretsProvider.DeleteSecret(ctx, ref); err != nil && !secrets.IsNotFoundError(err) {
			return fmt.Errorf("deleting cached token: %w", err)
		}
	}

	// Clear the persistent registry cache so authenticated data doesn't
	// remain on disk after logout.
	registryURL := registryURLFromConfig(cfg)
	if err := clearRegistryCache(registryURL); err != nil {
		slog.Debug("failed to clear registry cache", "error", err)
	}

	return configProvider.UpdateConfig(func(c *config.Config) {
		if c.RegistryAuth.OAuth != nil {
			c.RegistryAuth.OAuth.CachedRefreshTokenRef = ""
			c.RegistryAuth.OAuth.CachedTokenExpiry = time.Time{}
		}
	})
}

// validateOAuthConfig checks that registry OAuth authentication is configured.
func validateOAuthConfig(cfg *config.Config) error {
	if cfg.RegistryAuth.Type != config.RegistryAuthTypeOAuth || cfg.RegistryAuth.OAuth == nil {
		return fmt.Errorf(
			"registry OAuth authentication is not configured; run 'thv config set-registry-auth' first: %w",
			ErrRegistryAuthRequired,
		)
	}
	return nil
}

// NewSecretsProvider creates a secrets provider from the given config provider.
func NewSecretsProvider(configProvider config.Provider) (secrets.Provider, error) {
	cfg, err := configProvider.LoadOrCreateConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return nil, fmt.Errorf("getting secrets provider type: %w", err)
	}
	return secrets.CreateSecretProvider(providerType)
}

// registryURLFromConfig returns the registry URL, preferring RegistryApiUrl.
func registryURLFromConfig(cfg *config.Config) string {
	if cfg.RegistryApiUrl != "" {
		return cfg.RegistryApiUrl
	}
	return cfg.RegistryUrl
}

// clearRegistryCache removes the persistent cache file for the given registry URL.
// Uses the same path derivation as CachedAPIRegistryProvider.
func clearRegistryCache(registryURL string) error {
	if registryURL == "" {
		return nil
	}
	hash := sha256.Sum256([]byte(registryURL))
	cacheFile, err := xdg.CacheFile(fmt.Sprintf("toolhive/cache/registry-%x.json", hash[:4]))
	if err != nil {
		return fmt.Errorf("resolving cache path: %w", err)
	}
	if err := os.Remove(cacheFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing cache file: %w", err)
	}
	return nil
}

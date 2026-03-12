// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/adrg/xdg"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// LoginOptions holds optional flag-based overrides for Login.
// When provided, these values are validated and saved to config before
// proceeding with the OAuth flow.
type LoginOptions struct {
	// RegistryURL is the registry endpoint to save if none is configured.
	RegistryURL string
	// Issuer is the OIDC issuer URL to save if OAuth config is missing.
	Issuer string
	// ClientID is the OAuth client ID to save if OAuth config is missing.
	ClientID string
	// Audience is the OAuth audience (optional).
	Audience string
	// Scopes overrides the default OAuth scopes (defaults to ["openid", "offline_access"]).
	Scopes []string
}

// Login performs an interactive OAuth login against the configured registry.
// If opts supplies registry URL or OAuth fields that are not yet configured,
// Login validates and persists them before proceeding.
func Login(
	ctx context.Context, configProvider config.Provider, secretsProvider secrets.Provider, opts LoginOptions,
) error {
	cfg, err := configProvider.LoadOrCreateConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Check all missing configuration upfront so the user gets a single
	// error listing everything they need to provide.
	if err := checkMissingLoginConfig(cfg, opts); err != nil {
		return err
	}

	// Save any flag-supplied values that aren't yet in config.
	if err := ensureRegistryURL(cfg, configProvider, opts); err != nil {
		return err
	}
	if err := ensureOAuthConfig(ctx, cfg, configProvider, opts); err != nil {
		return err
	}

	// Reload config after potential saves so the rest of the flow sees updated values.
	cfg, err = configProvider.LoadOrCreateConfig()
	if err != nil {
		return fmt.Errorf("reloading config: %w", err)
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

// validateOAuthConfig checks that registry OAuth authentication is configured.
func validateOAuthConfig(cfg *config.Config) error {
	if cfg.RegistryAuth.Type != config.RegistryAuthTypeOAuth || cfg.RegistryAuth.OAuth == nil {
		return fmt.Errorf(
			"registry OAuth authentication is not configured; run 'thv registry login' first: %w",
			ErrRegistryAuthRequired,
		)
	}
	return nil
}

// checkMissingLoginConfig inspects the current config and opts, and returns a
// single formatted error listing every flag the user still needs to provide.
func checkMissingLoginConfig(cfg *config.Config, opts LoginOptions) error {
	hasRegistry := cfg.RegistryApiUrl != "" || cfg.RegistryUrl != "" || cfg.LocalRegistryPath != ""
	hasOAuth := cfg.RegistryAuth.Type == config.RegistryAuthTypeOAuth && cfg.RegistryAuth.OAuth != nil

	var missing []string
	if !hasRegistry && opts.RegistryURL == "" {
		missing = append(missing, "  --registry <url>       Registry URL")
	}
	if !hasOAuth && opts.Issuer == "" {
		missing = append(missing, "  --issuer <url>         OIDC issuer URL")
	}
	if !hasOAuth && opts.ClientID == "" {
		missing = append(missing, "  --client-id <id>       OAuth client ID")
	}

	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf(
		"missing required configuration\n\n"+
			"The following flags are needed:\n\n"+
			"%s\n\n"+
			"Example:\n\n"+
			"  thv registry login --registry <url> --issuer <url> --client-id <id>\n: %w",
		strings.Join(missing, "\n"),
		ErrRegistryAuthRequired,
	)
}

// ensureRegistryURL checks whether a registry URL is already configured and,
// if not, attempts to save the one supplied via opts. Returns an actionable
// error when no URL can be determined.
func ensureRegistryURL(cfg *config.Config, configProvider config.Provider, opts LoginOptions) error {
	hasRegistry := cfg.RegistryApiUrl != "" || cfg.RegistryUrl != "" || cfg.LocalRegistryPath != ""
	if hasRegistry {
		return nil
	}

	if opts.RegistryURL == "" {
		return fmt.Errorf("no registry URL configured and --registry not provided: %w", ErrRegistryAuthRequired)
	}

	registryType, cleanPath := config.DetectRegistryType(opts.RegistryURL, false)
	switch registryType {
	case config.RegistryTypeAPI:
		if err := configProvider.SetRegistryAPI(cleanPath, false); err != nil {
			return fmt.Errorf("saving registry API URL: %w", err)
		}
	case config.RegistryTypeURL:
		if err := configProvider.SetRegistryURL(cleanPath, false); err != nil {
			return fmt.Errorf("saving registry URL: %w", err)
		}
	case config.RegistryTypeFile:
		if err := configProvider.SetRegistryFile(cleanPath); err != nil {
			return fmt.Errorf("saving registry file: %w", err)
		}
	default:
		return fmt.Errorf("unsupported registry type %q for %q", registryType, opts.RegistryURL)
	}
	return nil
}

// ensureOAuthConfig checks whether OAuth auth is already configured and,
// if not, attempts to configure it from the supplied opts (issuer + clientID).
// Returns an actionable error when auth cannot be determined.
func ensureOAuthConfig(
	ctx context.Context, cfg *config.Config, configProvider config.Provider, opts LoginOptions,
) error {
	if cfg.RegistryAuth.Type == config.RegistryAuthTypeOAuth && cfg.RegistryAuth.OAuth != nil {
		return nil
	}

	if opts.Issuer == "" || opts.ClientID == "" {
		return fmt.Errorf("OAuth config missing and --issuer/--client-id not provided: %w", ErrRegistryAuthRequired)
	}

	// Validate the OIDC issuer with a reasonable timeout.
	discoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := oauth.DiscoverOIDCEndpoints(discoveryCtx, opts.Issuer); err != nil {
		return fmt.Errorf("OIDC discovery failed for issuer %s: %w", opts.Issuer, err)
	}

	scopes := func() []string {
		if len(opts.Scopes) > 0 {
			return opts.Scopes
		}
		return []string{"openid", "offline_access"}
	}()

	return configProvider.UpdateConfig(func(c *config.Config) {
		c.RegistryAuth = config.RegistryAuth{
			Type: config.RegistryAuthTypeOAuth,
			OAuth: &config.RegistryOAuthConfig{
				Issuer:       opts.Issuer,
				ClientID:     opts.ClientID,
				Scopes:       scopes,
				Audience:     opts.Audience,
				CallbackPort: remote.DefaultCallbackPort,
			},
		}
	})
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

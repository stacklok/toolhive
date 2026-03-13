// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// DefaultOAuthScopes returns the default OAuth scopes for registry authentication.
// openid is required for OIDC, offline_access is required for refresh tokens.
func DefaultOAuthScopes() []string {
	return []string{"openid", "offline_access"}
}

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

	// Reject local file registries early -- OAuth login makes no sense for them.
	if cfg.LocalRegistryPath != "" && cfg.RegistryApiUrl == "" && cfg.RegistryUrl == "" {
		return fmt.Errorf(
			"OAuth login is not supported for local file registries (path: %s); "+
				"use a remote registry URL with --registry instead",
			cfg.LocalRegistryPath,
		)
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

	registryURL := registryURLFromConfig(cfg)

	if ref := cfg.RegistryAuth.OAuth.CachedRefreshTokenRef; ref != "" {
		if err := secretsProvider.DeleteSecret(ctx, ref); err != nil && !secrets.IsNotFoundError(err) {
			return fmt.Errorf("deleting cached token: %w", err)
		}
	}

	// Also attempt cleanup using the derived key as a fallback. If
	// updateConfigTokenRef failed previously (it only logs a warning),
	// the secret may exist under this key even when CachedRefreshTokenRef
	// is empty or points to a different reference.
	if cfg.RegistryAuth.OAuth.Issuer != "" {
		derivedKey := DeriveSecretKey(registryURL, cfg.RegistryAuth.OAuth.Issuer)
		if derivedKey != cfg.RegistryAuth.OAuth.CachedRefreshTokenRef {
			if err := secretsProvider.DeleteSecret(ctx, derivedKey); err != nil && !secrets.IsNotFoundError(err) {
				slog.Debug("failed to delete derived secret key", "error", err)
			}
		}
	}

	// Clear the persistent registry cache so authenticated data doesn't
	// remain on disk after logout.
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
			"registry OAuth authentication is not configured; run 'thv registry login' first: %w",
			ErrRegistryAuthRequired,
		)
	}
	return nil
}

// hasRegistryConfig reports whether any registry source is configured.
func hasRegistryConfig(cfg *config.Config) bool {
	return cfg.RegistryApiUrl != "" || cfg.RegistryUrl != "" || cfg.LocalRegistryPath != ""
}

// checkMissingLoginConfig inspects the current config and opts, and returns a
// single formatted error listing every flag the user still needs to provide.
func checkMissingLoginConfig(cfg *config.Config, opts LoginOptions) error {
	hasRegistry := hasRegistryConfig(cfg)
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
			"  thv registry login --registry <url> --issuer <url> --client-id <id>: %w",
		strings.Join(missing, "\n"),
		ErrRegistryAuthRequired,
	)
}

// ensureRegistryURL checks whether a registry URL is already configured and,
// if not, attempts to save the one supplied via opts. Returns an actionable
// error when no URL can be determined.
func ensureRegistryURL(cfg *config.Config, configProvider config.Provider, opts LoginOptions) error {
	if hasRegistryConfig(cfg) {
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

	updateFn, err := ConfigureOAuth(ctx, opts.Issuer, opts.ClientID, opts.Audience, opts.Scopes)
	if err != nil {
		return err
	}
	return configProvider.UpdateConfig(updateFn)
}

// registryURLFromConfig returns the registry URL, preferring RegistryApiUrl.
func registryURLFromConfig(cfg *config.Config) string {
	if cfg.RegistryApiUrl != "" {
		return cfg.RegistryApiUrl
	}
	return cfg.RegistryUrl
}

// ConfigureOAuth validates the OIDC issuer, resolves default scopes, and returns
// a config update function that persists the OAuth settings. This is the shared
// implementation used by both Login and AuthManager.SetOAuthAuth.
func ConfigureOAuth(
	ctx context.Context, issuer, clientID, audience string, scopes []string,
) (func(*config.Config), error) {
	if err := validateIssuerURL(issuer); err != nil {
		return nil, err
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := oauth.DiscoverOIDCEndpoints(discoveryCtx, issuer); err != nil {
		return nil, fmt.Errorf("OIDC discovery failed for issuer %s: %w", issuer, err)
	}

	resolvedScopes := func() []string {
		if len(scopes) > 0 {
			return scopes
		}
		return DefaultOAuthScopes()
	}()

	return func(c *config.Config) {
		c.RegistryAuth = config.RegistryAuth{
			Type: config.RegistryAuthTypeOAuth,
			OAuth: &config.RegistryOAuthConfig{
				Issuer:       issuer,
				ClientID:     clientID,
				Scopes:       resolvedScopes,
				Audience:     audience,
				CallbackPort: remote.DefaultCallbackPort,
			},
		}
	}, nil
}

// clearRegistryCache removes the persistent cache file for the given registry URL.
func clearRegistryCache(registryURL string) error {
	if registryURL == "" {
		return nil
	}
	cacheFile := registryCachePath(registryURL)
	if err := os.Remove(cacheFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing cache file: %w", err)
	}
	return nil
}

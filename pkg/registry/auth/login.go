// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
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
// openid is required for OIDC (some IdPs enforce it based on client/policy configuration),
// offline_access is required for the provider to return a refresh token.
func DefaultOAuthScopes() []string {
	return []string{"openid", "offline_access"}
}

// LoginOptions holds optional flag-based overrides for Login.
// When provided, these values are validated and saved to config before
// proceeding with the OAuth flow.
type LoginOptions struct {
	// RegistryURL is the registry endpoint.
	RegistryURL string
	// Issuer is the OIDC issuer URL.
	Issuer string
	// ClientID is the OAuth client ID.
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

	// Reject local-file-only registries early -- OAuth login makes no sense for them.
	if hasOnlyFileRegistries(cfg) {
		return fmt.Errorf(
			"OAuth login is not supported for local file registries; " +
				"use a remote registry URL with --registry instead",
		)
	}

	// Check all missing configuration upfront so the user gets a single
	// error listing everything they need to provide.
	if err := checkMissingLoginConfig(cfg, opts); err != nil {
		return err
	}

	// Save any flag-supplied values that aren't yet in config.
	if err := ensureRegistryURL(configProvider, opts); err != nil {
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

	oauthCfg := findDefaultOAuthConfig(cfg)
	if oauthCfg == nil {
		return fmt.Errorf("OAuth config not found after save: %w", ErrRegistryAuthRequired)
	}

	ts, err := NewTokenSource(oauthCfg, registryURL, secretsProvider, true)
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

	oauthCfg := findDefaultOAuthConfig(cfg)
	if oauthCfg == nil {
		return fmt.Errorf(
			"registry OAuth authentication is not configured; run 'thv registry login' first: %w",
			ErrRegistryAuthRequired,
		)
	}

	registryURL := registryURLFromConfig(cfg)

	if ref := oauthCfg.CachedRefreshTokenRef; ref != "" {
		if err := secretsProvider.DeleteSecret(ctx, ref); err != nil && !secrets.IsNotFoundError(err) {
			return fmt.Errorf("deleting cached token: %w", err)
		}
	}

	// Also attempt cleanup using the derived key as a fallback.
	if oauthCfg.Issuer != "" {
		derivedKey := DeriveSecretKey(registryURL, oauthCfg.Issuer)
		if derivedKey != oauthCfg.CachedRefreshTokenRef {
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
		defaultName := c.EffectiveDefaultRegistry()
		src := c.FindRegistry(defaultName)
		if src != nil && src.Auth != nil && src.Auth.OAuth != nil {
			src.Auth.OAuth.CachedRefreshTokenRef = ""
			src.Auth.OAuth.CachedTokenExpiry = time.Time{}
		}
	})
}

// findDefaultOAuthConfig returns the OAuth config from the default registry, or nil.
func findDefaultOAuthConfig(cfg *config.Config) *config.RegistryOAuthConfig {
	defaultName := cfg.EffectiveDefaultRegistry()
	src := cfg.FindRegistry(defaultName)
	if src == nil || src.Auth == nil || src.Auth.Type != config.RegistryAuthTypeOAuth {
		return nil
	}
	return src.Auth.OAuth
}

// hasOnlyFileRegistries returns true if the only configured registries are file-based.
func hasOnlyFileRegistries(cfg *config.Config) bool {
	if len(cfg.Registries) == 0 {
		return false
	}
	for _, reg := range cfg.Registries {
		if reg.Type != config.RegistrySourceTypeFile {
			return false
		}
	}
	return true
}

// hasRegistryConfig reports whether any registry source is configured.
func hasRegistryConfig(cfg *config.Config) bool {
	return len(cfg.Registries) > 0
}

// checkMissingLoginConfig inspects the current config and opts, and returns a
// single formatted error listing every flag the user still needs to provide.
func checkMissingLoginConfig(cfg *config.Config, opts LoginOptions) error {
	hasRegistry := hasRegistryConfig(cfg)
	hasOAuth := findDefaultOAuthConfig(cfg) != nil

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

// ensureRegistryURL saves the registry URL from opts when provided.
// When no URL is provided via opts, existing config is used unchanged.
func ensureRegistryURL(configProvider config.Provider, opts LoginOptions) error {
	if opts.RegistryURL == "" {
		return nil
	}

	sourceType := detectSourceType(opts.RegistryURL)

	// Clear any existing auth on the default registry to prevent tokens
	// from being sent to the wrong server.
	if err := configProvider.UpdateConfig(func(c *config.Config) {
		defaultName := c.EffectiveDefaultRegistry()
		src := c.FindRegistry(defaultName)
		if src != nil {
			src.Auth = nil
		}
	}); err != nil {
		return fmt.Errorf("clearing stale auth config: %w", err)
	}

	source := config.RegistrySource{
		Name:     "default",
		Type:     sourceType,
		Location: opts.RegistryURL,
	}
	if err := configProvider.AddRegistry(source); err != nil {
		return fmt.Errorf("saving registry: %w", err)
	}
	if err := configProvider.SetDefaultRegistry("default"); err != nil {
		return fmt.Errorf("setting default registry: %w", err)
	}
	return nil
}

// detectSourceType determines the registry source type from the input string.
func detectSourceType(input string) config.RegistrySourceType {
	if strings.HasPrefix(input, "file://") {
		return config.RegistrySourceTypeFile
	}
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		if strings.HasSuffix(input, ".json") {
			return config.RegistrySourceTypeURL
		}
		return config.RegistrySourceTypeAPI
	}
	return config.RegistrySourceTypeFile
}

// ensureOAuthConfig ensures OAuth auth is configured on the default registry.
// When --issuer/--client-id flags are provided they are always applied (override semantics),
// allowing the caller to update auth even when an existing config is present.
// When no flags are supplied, existing config is used as-is.
func ensureOAuthConfig(
	ctx context.Context, cfg *config.Config, configProvider config.Provider, opts LoginOptions,
) error {
	if opts.Issuer != "" && opts.ClientID != "" {
		updateFn, err := ConfigureOAuth(ctx, opts.Issuer, opts.ClientID, opts.Audience, opts.Scopes)
		if err != nil {
			return err
		}
		return configProvider.UpdateConfig(updateFn)
	}

	// No flags provided — use existing config if present.
	if findDefaultOAuthConfig(cfg) != nil {
		return nil
	}

	return fmt.Errorf("OAuth config missing and --issuer/--client-id not provided: %w", ErrRegistryAuthRequired)
}

// registryURLFromConfig returns the URL of the default registry.
func registryURLFromConfig(cfg *config.Config) string {
	defaultName := cfg.EffectiveDefaultRegistry()
	src := cfg.FindRegistry(defaultName)
	if src == nil {
		return ""
	}
	return src.Location
}

// ConfigureOAuth validates the OIDC issuer, resolves default scopes, and returns
// a config update function that persists the OAuth settings on the default registry.
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
		defaultName := c.EffectiveDefaultRegistry()
		src := c.FindRegistry(defaultName)
		if src == nil {
			return
		}
		src.Auth = &config.RegistryAuth{
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

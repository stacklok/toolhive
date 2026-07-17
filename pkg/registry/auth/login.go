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
	// AllowPrivateIP permits RegistryURL to resolve to a private IP address.
	// Mirrors the `--allow-private-ip` flag on `thv config set-registry`; only
	// honored when RegistryURL is supplied (no-op when reusing stored config).
	AllowPrivateIP bool
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
//
// Every secret under the registry scope is deleted, not just the refresh-token
// key the current config points at. This is intentional: the token source also
// stores a cached access token under "<key>_AT" (see pkg/auth/tokensource),
// and a registry/issuer change can leave stale entries under derived keys the
// current config no longer points at. A targeted delete would miss both and
// allow the next login to short-circuit through tier 2/3 of the token source
// instead of triggering a fresh browser flow.
func Logout(ctx context.Context, configProvider config.Provider, secretsProvider secrets.Provider) error {
	cfg, err := configProvider.LoadOrCreateConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := validateOAuthConfig(cfg); err != nil {
		return err
	}

	if err := deleteAllScopedSecrets(ctx, secretsProvider); err != nil {
		return fmt.Errorf("deleting cached tokens: %w", err)
	}

	// Clear the persistent registry cache so authenticated data doesn't
	// remain on disk after logout.
	if err := clearRegistryCache(registryURLFromConfig(cfg)); err != nil {
		slog.Debug("failed to clear registry cache", "error", err)
	}

	return configProvider.UpdateConfig(func(c *config.Config) error {
		if c.RegistryAuth.OAuth != nil {
			c.RegistryAuth.OAuth.CachedRefreshTokenRef = ""
			c.RegistryAuth.OAuth.CachedTokenExpiry = time.Time{}
		}
		return nil
	})
}

// deleteAllScopedSecrets removes every secret visible to the given (already
// scope-restricted) provider. Mirrors pkg/llm.DeleteCachedTokens — providers
// that cannot list or delete (e.g. the environment provider) cannot hold cached
// tokens, so the operation is a no-op there.
func deleteAllScopedSecrets(ctx context.Context, provider secrets.Provider) error {
	caps := provider.Capabilities()
	if !caps.CanList || !caps.CanDelete {
		return nil
	}
	descs, err := provider.ListSecrets(ctx)
	if err != nil {
		return fmt.Errorf("listing cached tokens: %w", err)
	}
	if len(descs) == 0 {
		return nil
	}
	names := make([]string, len(descs))
	for i, d := range descs {
		names[i] = d.Key
	}
	return provider.DeleteSecrets(ctx, names)
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

// ensureRegistryURL saves the registry URL from opts when provided.
// Existing auth is always cleared when a URL flag is given, to prevent tokens
// from being sent to the wrong server after a registry change.
// When no URL is provided via opts, existing config is used unchanged.
func ensureRegistryURL(configProvider config.Provider, opts LoginOptions) error {
	if opts.RegistryURL == "" {
		// No override — use whatever is already in config.
		return nil
	}

	registryType, cleanPath := config.DetectRegistryType(opts.RegistryURL, opts.AllowPrivateIP)

	// Always clear auth when a registry URL is explicitly provided, so that
	// tokens are never sent to the wrong server.
	if err := configProvider.UpdateConfig(func(c *config.Config) error {
		c.RegistryAuth = config.RegistryAuth{}
		return nil
	}); err != nil {
		return fmt.Errorf("clearing stale auth config: %w", err)
	}

	switch registryType {
	case config.RegistryTypeAPI:
		if err := configProvider.SetRegistryAPI(cleanPath, opts.AllowPrivateIP); err != nil {
			return fmt.Errorf("saving registry API URL: %w", err)
		}
	case config.RegistryTypeURL:
		if err := configProvider.SetRegistryURL(cleanPath, opts.AllowPrivateIP); err != nil {
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

// ensureOAuthConfig ensures OAuth auth is configured.
// When --issuer/--client-id flags are provided they are always applied (override semantics),
// allowing the caller to update auth even when an existing config is present.
// When no flags are supplied, existing config is used as-is.
// Returns an actionable error when auth cannot be determined.
func ensureOAuthConfig(
	ctx context.Context, cfg *config.Config, configProvider config.Provider, opts LoginOptions,
) error {
	// If auth flags were explicitly provided, always apply them.
	if opts.Issuer != "" && opts.ClientID != "" {
		updateFn, err := ConfigureOAuth(ctx, opts.Issuer, opts.ClientID, opts.Audience, opts.Scopes)
		if err != nil {
			return err
		}
		return configProvider.UpdateConfig(updateFn)
	}

	// No flags provided — use existing config if present.
	if cfg.RegistryAuth.Type == config.RegistryAuthTypeOAuth && cfg.RegistryAuth.OAuth != nil {
		return nil
	}

	return fmt.Errorf("OAuth config missing and --issuer/--client-id not provided: %w", ErrRegistryAuthRequired)
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
) (func(*config.Config) error, error) {
	if err := validateIssuerURL(issuer); err != nil {
		return nil, err
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// blockPrivateIPs=false preserves this call path's existing behavior:
	// issuer is an operator-supplied flag here, not remote-server-derived
	// discovery input, unlike the CLI DCR flow's use of DiscoverOIDCEndpoints.
	if _, err := oauth.DiscoverOIDCEndpoints(discoveryCtx, issuer, false); err != nil {
		return nil, fmt.Errorf("OIDC discovery failed for issuer %s: %w", issuer, err)
	}

	resolvedScopes := func() []string {
		if len(scopes) > 0 {
			return scopes
		}
		return DefaultOAuthScopes()
	}()

	//nolint:unparam // error return is part of the UpdateConfig callback contract; this closure always succeeds
	return func(c *config.Config) error {
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
		return nil
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

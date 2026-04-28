// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package auth provides authentication support for MCP server registries.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/stacklok/toolhive/pkg/auth/tokensource"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// ErrRegistryAuthRequired is returned when registry authentication is required
// but no cached tokens are available in a non-interactive context.
var ErrRegistryAuthRequired = errors.New("registry authentication required: run 'thv registry login' to authenticate")

// TokenSource provides authentication tokens for registry HTTP requests.
type TokenSource interface {
	// Token returns a valid access token string, or empty string if no auth.
	// Implementations should handle token refresh transparently.
	Token(ctx context.Context) (string, error)
}

// NewTokenSource creates a TokenSource from registry OAuth configuration.
// Returns nil, nil if oauth config is nil (no auth required).
// The registryURL is used to derive a unique secret key for token storage.
// The secrets provider may be nil if secret storage is not available.
// The interactive flag controls whether browser-based OAuth flows are allowed.
func NewTokenSource(
	cfg *config.RegistryOAuthConfig,
	registryURL string,
	secretsProvider secrets.Provider,
	interactive bool,
) (TokenSource, error) {
	if cfg == nil {
		return nil, nil
	}

	return tokensource.New(tokensource.Options{
		OIDC: tokensource.OIDCParams{
			Issuer:       cfg.Issuer,
			ClientID:     cfg.ClientID,
			Scopes:       cfg.Scopes,
			Audience:     cfg.Audience,
			CallbackPort: cfg.CallbackPort,
		},
		SecretsProvider: secretsProvider,
		Interactive:     interactive,
		KeyProvider: func() string {
			if cfg.CachedRefreshTokenRef != "" {
				return cfg.CachedRefreshTokenRef
			}
			return DeriveSecretKey(registryURL, cfg.Issuer)
		},
		ConfigPersister: updateRegistryTokenRef,
		FallbackErr:     ErrRegistryAuthRequired,
	}), nil
}

// DeriveSecretKey computes the secret key for storing a registry's refresh token.
// The key follows the formula: REGISTRY_OAUTH_<8 hex chars>
// where the hex is derived from sha256(registryURL + "\x00" + issuer)[:4].
func DeriveSecretKey(registryURL, issuer string) string {
	return tokensource.DeriveSecretKey("REGISTRY_OAUTH_", registryURL, issuer)
}

// updateRegistryTokenRef persists the refresh token key and expiry into the
// global config under RegistryAuth.OAuth.
func updateRegistryTokenRef(key string, expiry time.Time) {
	if err := config.UpdateConfig(func(c *config.Config) error {
		if c.RegistryAuth.OAuth != nil {
			c.RegistryAuth.OAuth.CachedRefreshTokenRef = key
			c.RegistryAuth.OAuth.CachedTokenExpiry = expiry
		}
		return nil
	}); err != nil {
		slog.Warn("failed to update registry config with token reference", "error", err)
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package auth provides authentication support for MCP server registries.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

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

// NewTokenSource creates a TokenSource from OAuth configuration.
// Returns nil, nil if oauth config is nil (no auth required).
// The serviceURL is used to derive a unique secret key for token storage.
// The secrets provider may be nil if secret storage is not available.
// The interactive flag controls whether browser-based OAuth flows are allowed.
// configUpdater is called whenever a token ref or expiry needs to be persisted
// back to the caller's config store; pass nil to skip config persistence.
func NewTokenSource(
	cfg *config.OAuthConfig,
	serviceURL string,
	secretsProvider secrets.Provider,
	interactive bool,
	configUpdater func(tokenRef string, expiry time.Time),
) (TokenSource, error) {
	if cfg == nil {
		return nil, nil
	}

	return &oauthTokenSource{
		oauthCfg:        cfg,
		serviceURL:      serviceURL,
		secretsProvider: secretsProvider,
		interactive:     interactive,
		configUpdater:   configUpdater,
	}, nil
}

// RegistryConfigUpdater returns a configUpdater callback that persists OAuth
// token references back to the toolhive on-disk config under RegistryAuth.OAuth.
// Pass this to NewTokenSource when using it for registry authentication.
func RegistryConfigUpdater() func(tokenRef string, expiry time.Time) {
	return func(tokenRef string, expiry time.Time) {
		if err := config.UpdateConfig(func(cfg *config.Config) {
			if cfg.RegistryAuth.OAuth != nil {
				cfg.RegistryAuth.OAuth.CachedRefreshTokenRef = tokenRef
				cfg.RegistryAuth.OAuth.CachedTokenExpiry = expiry
			}
		}); err != nil {
			slog.Warn("Failed to update config with token reference", "error", err)
		}
	}
}

// DeriveSecretKey computes the secret key for storing a registry's refresh token.
// The key follows the formula: REGISTRY_OAUTH_<8 hex chars>
// where the hex is derived from sha256(registryURL + "\x00" + issuer)[:4].
func DeriveSecretKey(registryURL, issuer string) string {
	h := sha256.Sum256([]byte(registryURL + "\x00" + issuer))
	return "REGISTRY_OAUTH_" + hex.EncodeToString(h[:4])
}

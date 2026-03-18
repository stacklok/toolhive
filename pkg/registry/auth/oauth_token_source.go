// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// oauthTokenSource implements TokenSource using OAuth/OIDC browser-based flow.
type oauthTokenSource struct {
	oauthCfg        *config.RegistryOAuthConfig
	registryURL     string
	secretsProvider secrets.Provider
	interactive     bool
	mu              sync.Mutex
	tokenSource     oauth2.TokenSource
}

// Token returns a valid access token string, handling refresh and browser flow as needed.
func (o *oauthTokenSource) Token(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Try cached token source first (auto-refreshes)
	if o.tokenSource != nil {
		token, err := o.tokenSource.Token()
		if err == nil && token.Valid() {
			return token.AccessToken, nil
		}
		// Token source failed or expired, try to restore or re-authenticate
		o.tokenSource = nil
	}

	// Try to restore from secrets manager
	if err := o.tryRestoreFromCache(ctx); err == nil && o.tokenSource != nil {
		token, err := o.tokenSource.Token()
		if err == nil && token.Valid() {
			return token.AccessToken, nil
		}
		o.tokenSource = nil
	}

	// In non-interactive mode, return error instead of triggering browser flow
	if !o.interactive {
		return "", ErrRegistryAuthRequired
	}

	// Trigger browser-based OAuth flow
	if err := o.performOAuthFlow(ctx); err != nil {
		return "", fmt.Errorf("oauth flow failed: %w", err)
	}

	token, err := o.tokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("failed to get token after oauth flow: %w", err)
	}

	return token.AccessToken, nil
}

// tryRestoreFromCache attempts to restore token source from cached refresh token.
func (o *oauthTokenSource) tryRestoreFromCache(ctx context.Context) error {
	if o.secretsProvider == nil {
		return fmt.Errorf("no secrets provider available")
	}

	refreshTokenKey := o.refreshTokenKey()

	refreshToken, err := o.secretsProvider.GetSecret(ctx, refreshTokenKey)
	if err != nil {
		return fmt.Errorf("failed to get cached refresh token: %w", err)
	}
	if refreshToken == "" {
		return fmt.Errorf("no cached refresh token found")
	}

	oauth2Cfg, err := o.buildOAuth2Config(ctx)
	if err != nil {
		return fmt.Errorf("failed to create oauth2 config: %w", err)
	}

	o.tokenSource = remote.CreateTokenSourceFromCached(oauth2Cfg, refreshToken, o.oauthCfg.CachedTokenExpiry)
	return nil
}

// performOAuthFlow executes the browser-based OAuth flow and persists the result.
func (o *oauthTokenSource) performOAuthFlow(ctx context.Context) error {
	oauthCfg, err := o.buildOAuthFlowConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to create oauth config: %w", err)
	}

	flow, err := oauth.NewFlow(oauthCfg)
	if err != nil {
		return fmt.Errorf("failed to create oauth flow: %w", err)
	}

	tokenResult, err := flow.Start(ctx, false)
	if err != nil {
		return fmt.Errorf("oauth flow start failed: %w", err)
	}

	baseTokenSource := flow.TokenSource()

	// Wrap with persisting token source if secrets provider available
	if o.secretsProvider == nil {
		slog.Debug("No secrets provider available, refresh token will not be persisted")
	} else {
		refreshTokenKey := o.refreshTokenKey()
		baseTokenSource = remote.NewPersistingTokenSource(
			baseTokenSource,
			o.createTokenPersister(refreshTokenKey),
		)

		// Persist initial refresh token
		if tokenResult.RefreshToken != "" {
			if err := o.secretsProvider.SetSecret(ctx, refreshTokenKey, tokenResult.RefreshToken); err != nil {
				slog.Warn("Failed to persist initial refresh token", "error", err)
			} else {
				slog.Debug("Persisted initial refresh token", "key", refreshTokenKey)
			}
		} else {
			slog.Debug("OAuth provider did not return a refresh token, token will not be persisted")
		}

		// Update config with token ref
		o.updateConfigTokenRef(refreshTokenKey, tokenResult.Expiry)
	}

	o.tokenSource = baseTokenSource
	return nil
}

// buildOAuthFlowConfig creates an oauth.Config for the browser-based flow via OIDC discovery.
// PKCE is always enabled (S256) per OAuth 2.1 requirements for public clients.
func (o *oauthTokenSource) buildOAuthFlowConfig(ctx context.Context) (*oauth.Config, error) {
	callbackPort := o.oauthCfg.CallbackPort
	if callbackPort == 0 {
		callbackPort = remote.DefaultCallbackPort
	}

	scopes := ensureOfflineAccess(o.oauthCfg.Scopes)

	return oauth.CreateOAuthConfigFromOIDC(
		ctx,
		o.oauthCfg.Issuer,
		o.oauthCfg.ClientID,
		"", // Public client — no client secret (PKCE is used instead)
		scopes,
		true, // Always use PKCE (S256)
		callbackPort,
		o.oauthCfg.Audience,
	)
}

// ensureOfflineAccess returns scopes with "offline_access" included.
// This scope is required for the provider to return a refresh token.
func ensureOfflineAccess(scopes []string) []string {
	for _, s := range scopes {
		if s == "offline_access" {
			return scopes
		}
	}
	return append(scopes[:len(scopes):len(scopes)], "offline_access")
}

// buildOAuth2Config creates an oauth2.Config for token refresh via OIDC discovery.
func (o *oauthTokenSource) buildOAuth2Config(ctx context.Context) (*oauth2.Config, error) {
	oauthCfg, err := o.buildOAuthFlowConfig(ctx)
	if err != nil {
		return nil, err
	}

	return &oauth2.Config{
		ClientID:     oauthCfg.ClientID,
		ClientSecret: oauthCfg.ClientSecret,
		Scopes:       oauthCfg.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   oauthCfg.AuthURL,
			TokenURL:  oauthCfg.TokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}, nil
}

// createTokenPersister returns a remote.TokenPersister function that stores
// refresh tokens in the secrets manager.
func (o *oauthTokenSource) createTokenPersister(refreshTokenKey string) remote.TokenPersister {
	return func(refreshToken string, expiry time.Time) error {
		// The TokenPersister signature does not accept a context, and this callback
		// is invoked asynchronously during token refresh, so we use Background.
		ctx := context.Background()
		if err := o.secretsProvider.SetSecret(ctx, refreshTokenKey, refreshToken); err != nil {
			return fmt.Errorf("failed to persist refresh token: %w", err)
		}
		o.updateConfigTokenRef(refreshTokenKey, expiry)
		return nil
	}
}

// updateConfigTokenRef updates the config with the refresh token reference and expiry.
func (*oauthTokenSource) updateConfigTokenRef(refreshTokenKey string, expiry time.Time) {
	if err := config.UpdateConfig(func(cfg *config.Config) {
		if cfg.RegistryAuth.OAuth != nil {
			cfg.RegistryAuth.OAuth.CachedRefreshTokenRef = refreshTokenKey
			cfg.RegistryAuth.OAuth.CachedTokenExpiry = expiry
		}
	}); err != nil {
		slog.Warn("Failed to update config with token reference", "error", err)
	}
}

// refreshTokenKey returns the key used to store the refresh token in the secrets manager.
// Uses the RFC-specified derivation formula if no cached reference exists.
func (o *oauthTokenSource) refreshTokenKey() string {
	if o.oauthCfg.CachedRefreshTokenRef != "" {
		return o.oauthCfg.CachedRefreshTokenRef
	}
	return DeriveSecretKey(o.registryURL, o.oauthCfg.Issuer)
}

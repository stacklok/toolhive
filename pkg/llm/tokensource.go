// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// ErrTokenRequired is returned when a fresh token is needed but no cached or
// refreshable token exists and the caller is non-interactive (browser flow
// disabled). The caller should run "thv llm setup" to perform initial login.
var ErrTokenRequired = errors.New(
	"LLM gateway authentication required: run \"thv llm setup\" to log in",
)

// preemptiveRefreshWindow is how far before actual expiry a token is treated as
// expired, triggering a proactive refresh before the gateway rejects it.
const preemptiveRefreshWindow = 30 * time.Second

// TokenRefUpdater is a callback invoked after a successful browser flow or token
// refresh to persist the refresh token key and expiry into the application config.
// The callback is called with the secret key and token expiry time.
// Callers typically wire this to config.UpdateConfig.
type TokenRefUpdater func(refreshTokenKey string, expiry time.Time)

// TokenSource provides fresh LLM gateway access tokens using a three-tier strategy:
//
//  1. In-memory cached oauth2.TokenSource (auto-refreshes transparently)
//  2. Refresh token stored in the secrets provider (restores across CLI invocations)
//  3. Browser-based OIDC+PKCE flow (only when interactive is true)
//
// Access tokens are held in memory only and are never written to disk or logged.
type TokenSource struct {
	cfg             *Config
	secretsProvider secrets.Provider
	interactive     bool
	tokenRefUpdater TokenRefUpdater
	mu              sync.Mutex
	tokenSource     oauth2.TokenSource
}

// NewTokenSource creates a TokenSource for the LLM gateway.
// secretsProvider may be nil if the secrets store is unavailable (tier 2 is skipped).
// tokenRefUpdater is called after login/refresh to persist the token reference into
// config — pass nil to skip config persistence (useful in tests).
// Set interactive to false for non-interactive callers such as thv llm token.
func NewTokenSource(
	cfg *Config, secretsProvider secrets.Provider, interactive bool, tokenRefUpdater TokenRefUpdater,
) *TokenSource {
	return &TokenSource{
		cfg:             cfg,
		secretsProvider: secretsProvider,
		interactive:     interactive,
		tokenRefUpdater: tokenRefUpdater,
	}
}

// Token returns a valid LLM gateway access token.
// It is safe for concurrent use.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Tier 1: in-memory token source handles auto-refresh transparently.
	if t.tokenSource != nil {
		tok, err := t.tokenSource.Token()
		if err == nil && tok.Valid() {
			return tok.AccessToken, nil
		}
		t.tokenSource = nil
	}

	// Tier 2: restore from a cached refresh token in the secrets provider.
	if err := t.tryRestoreFromCache(ctx); err == nil && t.tokenSource != nil {
		tok, err := t.tokenSource.Token()
		if err == nil && tok.Valid() {
			return tok.AccessToken, nil
		}
		t.tokenSource = nil
	}

	// Tier 3: browser OIDC+PKCE flow — only in interactive mode.
	if !t.interactive {
		return "", ErrTokenRequired
	}
	if err := t.performBrowserFlow(ctx); err != nil {
		return "", fmt.Errorf("OIDC browser flow failed: %w", err)
	}
	tok, err := t.tokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("failed to get token after browser flow: %w", err)
	}
	return tok.AccessToken, nil
}

// tryRestoreFromCache attempts to build a token source from the cached refresh
// token stored in the secrets provider.
func (t *TokenSource) tryRestoreFromCache(ctx context.Context) error {
	if t.secretsProvider == nil {
		return fmt.Errorf("no secrets provider available")
	}
	key := t.refreshTokenKey()
	refreshToken, err := t.secretsProvider.GetSecret(ctx, key)
	if err != nil || refreshToken == "" {
		return fmt.Errorf("no cached refresh token")
	}

	oauth2Cfg, err := t.buildOAuth2Config(ctx)
	if err != nil {
		return fmt.Errorf("building oauth2 config for cache restore: %w", err)
	}

	// Subtract preemptiveRefreshWindow so the oauth2 library considers the token
	// expired 30 s before its actual expiry, triggering proactive refresh.
	expiry := t.cfg.OIDC.CachedTokenExpiry
	if !expiry.IsZero() {
		expiry = expiry.Add(-preemptiveRefreshWindow)
	}

	t.tokenSource = remote.CreateTokenSourceFromCached(oauth2Cfg, refreshToken, expiry, "")
	return nil
}

// performBrowserFlow runs the interactive OIDC+PKCE browser flow and persists
// the resulting refresh token for future non-interactive use.
func (t *TokenSource) performBrowserFlow(ctx context.Context) error {
	flowCfg, err := t.buildFlowConfig(ctx)
	if err != nil {
		return err
	}

	flow, err := oauth.NewFlow(flowCfg)
	if err != nil {
		return fmt.Errorf("creating OAuth flow: %w", err)
	}

	tokenResult, err := flow.Start(ctx, false)
	if err != nil {
		return fmt.Errorf("OAuth flow start failed: %w", err)
	}

	base := flow.TokenSource()
	key := t.refreshTokenKey()

	if t.secretsProvider != nil {
		base = remote.NewPersistingTokenSource(base, t.makeTokenPersister(key))
		if tokenResult.RefreshToken != "" {
			if err := t.secretsProvider.SetSecret(ctx, key, tokenResult.RefreshToken); err != nil {
				slog.Warn("failed to persist initial LLM gateway refresh token", "error", err)
			}
		} else {
			slog.Debug("OIDC provider did not return a refresh token; token will not be persisted")
		}
		t.updateConfigTokenRef(key, tokenResult.Expiry)
	}

	t.tokenSource = base
	return nil
}

// buildFlowConfig creates an oauth.Config for the interactive browser flow.
// PKCE (S256) is always enabled per OAuth 2.1 requirements for public clients.
func (t *TokenSource) buildFlowConfig(ctx context.Context) (*oauth.Config, error) {
	return oauth.CreateOAuthConfigFromOIDC(
		ctx,
		t.cfg.OIDC.Issuer,
		t.cfg.OIDC.ClientID,
		"", // public client — no client secret
		ensureOfflineAccess(t.cfg.OIDC.EffectiveScopes()),
		true, // always use PKCE
		t.cfg.OIDC.CallbackPort,
		t.cfg.OIDC.Audience,
	)
}

// buildOAuth2Config creates a minimal oauth2.Config suitable for token refresh
// via the cached refresh token path (no browser required).
func (t *TokenSource) buildOAuth2Config(ctx context.Context) (*oauth2.Config, error) {
	flowCfg, err := t.buildFlowConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &oauth2.Config{
		ClientID: flowCfg.ClientID,
		Scopes:   flowCfg.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   flowCfg.AuthURL,
			TokenURL:  flowCfg.TokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}, nil
}

// makeTokenPersister returns a remote.TokenPersister that stores the refresh
// token in the secrets provider and updates the config expiry reference.
func (t *TokenSource) makeTokenPersister(key string) remote.TokenPersister {
	return func(refreshToken string, expiry time.Time) error {
		ctx := context.Background()
		if err := t.secretsProvider.SetSecret(ctx, key, refreshToken); err != nil {
			return fmt.Errorf("persisting LLM gateway refresh token: %w", err)
		}
		t.updateConfigTokenRef(key, expiry)
		return nil
	}
}

// updateConfigTokenRef calls the injected tokenRefUpdater (if set) to persist
// the refresh token key and expiry so future invocations can restore the session.
func (t *TokenSource) updateConfigTokenRef(key string, expiry time.Time) {
	if t.tokenRefUpdater != nil {
		t.tokenRefUpdater(key, expiry)
	}
}

// refreshTokenKey returns the secrets-provider key for the LLM refresh token.
// If a key was previously persisted in config, that key is reused; otherwise a
// new key is derived deterministically from the gateway URL and issuer.
func (t *TokenSource) refreshTokenKey() string {
	if t.cfg.OIDC.CachedRefreshTokenRef != "" {
		return t.cfg.OIDC.CachedRefreshTokenRef
	}
	return DeriveSecretKey(t.cfg.GatewayURL, t.cfg.OIDC.Issuer)
}

// DeriveSecretKey computes the secrets-provider key for an LLM gateway refresh
// token. The formula is: LLM_OAUTH_<8 hex chars> where the hex is derived from
// sha256(gatewayURL + "\x00" + issuer)[:4], matching the pattern used by the
// registry auth package.
func DeriveSecretKey(gatewayURL, issuer string) string {
	h := sha256.Sum256([]byte(gatewayURL + "\x00" + issuer))
	return "LLM_OAUTH_" + hex.EncodeToString(h[:4])
}

// ensureOfflineAccess returns scopes with "offline_access" appended if absent.
// This scope is required for the provider to return a refresh token.
func ensureOfflineAccess(scopes []string) []string {
	for _, s := range scopes {
		if s == "offline_access" {
			return scopes
		}
	}
	return append(scopes[:len(scopes):len(scopes)], "offline_access")
}

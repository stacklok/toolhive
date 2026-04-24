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
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// errCacheMiss is a sentinel used internally to distinguish "no token in cache"
// from real backend errors. It is never exposed to callers.
var errCacheMiss = errors.New("no cached refresh token")

// ErrTokenRequired is returned when a fresh token is needed but no cached or
// refreshable token exists and the caller is non-interactive (browser flow
// disabled). The user must first complete an interactive login so that a
// refresh token is persisted for subsequent non-interactive calls.
var ErrTokenRequired = errors.New(
	"LLM gateway authentication required: no cached credentials found; " +
		"complete an interactive login first (\"thv llm setup\" — coming soon)",
)

// preemptiveRefreshWindow is how far before actual expiry a token is treated as
// expired, triggering a proactive refresh before the gateway rejects it.
const preemptiveRefreshWindow = 30 * time.Second

// TokenRefUpdater is a callback invoked when the refresh token changes — either
// after a successful browser flow (initial login) or when the OIDC provider
// rotates the refresh token during a refresh. It persists the secret key and
// the new token expiry into the application config so future CLI invocations
// can restore the session. It is NOT called on routine access-token refreshes
// where the refresh token is unchanged.
// Callers typically wire this to config.UpdateConfig.
type TokenRefUpdater func(refreshTokenKey string, expiry time.Time)

// TokenSource provides fresh LLM gateway access tokens using a three-tier strategy:
//
//  1. In-memory cached oauth2.TokenSource (auto-refreshes transparently)
//  2. Secrets-provider cached access token (cross-process reuse — avoids racing
//     on the refresh token when multiple short-lived processes run concurrently)
//  3. Refresh token stored in the secrets provider (restores across CLI invocations)
//  4. Browser-based OIDC+PKCE flow (only when interactive is true)
//
// Both the refresh token and the short-lived access token are stored in the
// secrets provider (system keychain). The access token is cached alongside the
// refresh token to prevent concurrent processes from racing to exchange the same
// refresh token — a race that causes invalid_grant on providers that rotate
// refresh tokens on use. Access tokens are never written to log output.
type TokenSource struct {
	cfg             *Config
	secretsProvider secrets.Provider
	interactive     bool
	tokenRefUpdater TokenRefUpdater
	mu              sync.Mutex
	tokenSource     oauth2.TokenSource
}

// NewTokenSource creates a TokenSource for the LLM gateway.
// secretsProvider may be nil if the secrets store is unavailable; in that case
// tier 2 returns an actionable error ("no secrets provider available") rather
// than the generic ErrTokenRequired, so the caller sees the real cause.
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

	// lastErr tracks the most recent actionable error from tiers 1/2. In
	// non-interactive mode we return it instead of the generic ErrTokenRequired
	// so the caller sees the real cause (e.g. invalid_grant, keyring locked).
	var lastErr error

	if tok, found, err := t.tryInMemoryToken(); found {
		return tok, nil
	} else if err != nil {
		lastErr = err
	}

	// Tier 1.5: secrets-cached access token — avoids IdP exchange when the
	// access token is still fresh. Concurrent short-lived processes (e.g., thv
	// llm token) share this cached value instead of all racing to exchange the
	// same refresh token, preventing invalid_grant from OIDC providers that
	// rotate refresh tokens on use.
	if tok, found := t.tryAccessTokenCache(ctx); found {
		return tok, nil
	}

	if tok, found, err := t.tryCachedToken(ctx); found {
		return tok, nil
	} else if err != nil {
		lastErr = err
	}

	// Tier 3: browser OIDC+PKCE flow — only in interactive mode.
	if !t.interactive {
		if lastErr != nil {
			return "", lastErr
		}
		return "", ErrTokenRequired
	}
	if err := t.performBrowserFlow(ctx); err != nil {
		return "", fmt.Errorf("OIDC browser flow failed: %w", err)
	}
	tok, err := t.tokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("failed to get token after browser flow: %w", err)
	}
	t.cacheAccessToken(ctx, tok.AccessToken, tok.Expiry)
	return tok.AccessToken, nil
}

// tryInMemoryToken tries the in-memory token source (tier 1).
func (t *TokenSource) tryInMemoryToken() (string, bool, error) {
	if t.tokenSource == nil {
		return "", false, nil
	}
	tok, err := t.tokenSource.Token()
	if err == nil && tok.Valid() {
		return tok.AccessToken, true, nil
	}
	t.tokenSource = nil
	return "", false, err
}

// tryCachedToken restores a token source from the secrets provider (tier 2)
// and tries to obtain a valid token from it.
func (t *TokenSource) tryCachedToken(ctx context.Context) (string, bool, error) {
	if err := t.tryRestoreFromCache(ctx); err != nil {
		if errors.Is(err, errCacheMiss) {
			return "", false, nil
		}
		return "", false, err
	}
	tok, err := t.tokenSource.Token()
	if err == nil && tok.Valid() {
		t.cacheAccessToken(ctx, tok.AccessToken, tok.Expiry)
		return tok.AccessToken, true, nil
	}
	t.tokenSource = nil
	return "", false, err
}

// tryRestoreFromCache attempts to build a token source from the cached refresh
// token stored in the secrets provider.
func (t *TokenSource) tryRestoreFromCache(ctx context.Context) error {
	if t.secretsProvider == nil {
		return fmt.Errorf("no secrets provider available")
	}
	key := t.refreshTokenKey()
	refreshToken, err := t.secretsProvider.GetSecret(ctx, key)
	if err != nil {
		if secrets.IsNotFoundError(err) {
			return errCacheMiss
		}
		return fmt.Errorf("reading cached refresh token: %w", err)
	}
	if refreshToken == "" {
		return errCacheMiss
	}

	oauth2Cfg, err := t.buildOAuth2Config(ctx)
	if err != nil {
		return fmt.Errorf("building oauth2 config for cache restore: %w", err)
	}

	base := remote.CreateTokenSourceFromCached(oauth2Cfg, refreshToken, t.cfg.OIDC.CachedTokenExpiry, t.cfg.OIDC.Audience)

	// Persist rotated refresh tokens back to the secrets provider so future CLI
	// invocations can still restore the session if the provider invalidates the
	// old token on refresh (common with OIDC providers that rotate refresh tokens).
	base = remote.NewPersistingTokenSource(base, t.makeTokenPersister(key))

	// Wrap with preemptive refresh so tokens are renewed 30 s before real expiry
	// on every subsequent refresh, not just the first restore.
	t.tokenSource = withPreemptiveRefresh(base)
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
			} else {
				t.updateConfigTokenRef(key, tokenResult.Expiry)
			}
		} else {
			slog.Debug("OIDC provider did not return a refresh token; token will not be persisted")
		}
	}

	// Wrap with preemptive refresh so tokens are renewed 30 s before real expiry.
	t.tokenSource = withPreemptiveRefresh(base)
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

// accessTokenCacheKey returns the secrets-provider key for the cached access token.
func (t *TokenSource) accessTokenCacheKey() string {
	return t.refreshTokenKey() + "_AT"
}

// tryAccessTokenCache reads a previously cached access token from the secrets
// provider and returns it if still valid. The stored expiry already accounts for
// preemptiveRefreshWindow, so a plain time.Now().Before(expiry) check suffices.
// All errors are treated as cache misses — a missing or corrupt entry is harmless.
func (t *TokenSource) tryAccessTokenCache(ctx context.Context) (string, bool) {
	if t.secretsProvider == nil {
		return "", false
	}
	raw, err := t.secretsProvider.GetSecret(ctx, t.accessTokenCacheKey())
	if err != nil || raw == "" {
		return "", false
	}
	idx := strings.LastIndex(raw, "|")
	if idx < 0 {
		return "", false
	}
	expiry, err := time.Parse(time.RFC3339, raw[idx+1:])
	if err != nil {
		return "", false
	}
	if time.Now().Before(expiry) {
		return raw[:idx], true
	}
	return "", false
}

// cacheAccessToken writes the access token and its expiry to the secrets
// provider so concurrent short-lived processes can reuse it without hitting the
// IdP. The expiry stored is the (already preemptively-shifted) expiry returned
// by the token source, so callers can use a plain time.Now().Before(expiry)
// check to decide validity. Errors are logged at debug level and suppressed —
// a write failure degrades gracefully to a full refresh on the next call.
func (t *TokenSource) cacheAccessToken(ctx context.Context, token string, expiry time.Time) {
	if t.secretsProvider == nil || token == "" || expiry.IsZero() {
		return
	}
	val := token + "|" + expiry.UTC().Format(time.RFC3339)
	if err := t.secretsProvider.SetSecret(ctx, t.accessTokenCacheKey(), val); err != nil {
		slog.Debug("failed to cache access token", "error", err)
	}
}

// DeriveSecretKey computes the secrets-provider key for an LLM gateway refresh
// token. The formula is: LLM_OAUTH_<8 hex chars> where the hex is derived from
// sha256(gatewayURL + "\x00" + issuer)[:4], matching the pattern used by the
// registry auth package.
func DeriveSecretKey(gatewayURL, issuer string) string {
	h := sha256.Sum256([]byte(gatewayURL + "\x00" + issuer))
	return "LLM_OAUTH_" + hex.EncodeToString(h[:4])
}

// preemptiveTokenSource wraps an oauth2.TokenSource and shifts each returned
// token's expiry back by preemptiveRefreshWindow. This causes oauth2.ReuseTokenSource
// to treat the token as expired 30 s before the gateway would, triggering a
// proactive refresh on both the browser-flow and refresh-token-restore paths.
type preemptiveTokenSource struct {
	inner oauth2.TokenSource
}

func (p *preemptiveTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.inner.Token()
	if err != nil {
		return nil, err
	}
	if !tok.Expiry.IsZero() {
		shifted := *tok
		shifted.Expiry = tok.Expiry.Add(-preemptiveRefreshWindow)
		return &shifted, nil
	}
	return tok, nil
}

// withPreemptiveRefresh wraps src so tokens appear expired preemptiveRefreshWindow
// before they actually expire, then re-wraps with ReuseTokenSource so the refresh
// is only triggered once per window.
func withPreemptiveRefresh(src oauth2.TokenSource) oauth2.TokenSource {
	return oauth2.ReuseTokenSource(nil, &preemptiveTokenSource{inner: src})
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

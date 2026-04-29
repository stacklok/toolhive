// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tokensource provides a shared OIDC-backed OAuth token source used by
// both the LLM gateway client and the registry authentication client.
//
// The shared OAuthTokenSource implements a four-tier token strategy:
//
//  1. In-memory cached oauth2.TokenSource (auto-refreshes transparently)
//  2. Secrets-provider cached access token (cross-process reuse to avoid racing)
//  3. Refresh token stored in the secrets provider (restores across CLI invocations)
//  4. Browser-based OIDC+PKCE flow (only when interactive is true)
//
// Callers parameterise the source via Options: an OIDC config struct, a key
// provider function (which determines the secrets-provider key and its prefix),
// and a ConfigPersister callback that persists the token reference into the
// application config.
package tokensource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

// preemptiveRefreshWindow is how far before actual expiry a token is treated as
// expired, triggering a proactive refresh before the upstream rejects it.
const preemptiveRefreshWindow = 30 * time.Second

// OIDCParams holds the OIDC connection parameters for a token source.
type OIDCParams struct {
	Issuer       string
	ClientID     string
	Scopes       []string // "offline_access" is appended automatically if absent
	Audience     string
	CallbackPort int
}

// ConfigPersister is called when the refresh token key or expiry changes —
// after a successful browser flow (initial login) or when the OIDC provider
// rotates the refresh token during a refresh. Callers wire this to their config
// persistence layer. It is NOT called on routine access-token refreshes where
// the refresh token is unchanged.
type ConfigPersister func(refreshTokenKey string, expiry time.Time)

// Options configures an OAuthTokenSource.
type Options struct {
	OIDC OIDCParams
	// SecretsProvider is used to persist and restore refresh and access tokens.
	// May be nil; in that case tier 2/3 return an actionable error rather than
	// the FallbackErr, so the caller sees the real cause.
	SecretsProvider secrets.Provider
	// Interactive controls whether the browser OIDC+PKCE flow is allowed.
	Interactive bool
	// KeyProvider returns the secrets-provider key for the refresh token.
	// May be called multiple times per Token() invocation; must be deterministic
	// and free of side effects. Typically returns a cached config ref if set,
	// otherwise derives a key from url+issuer.
	KeyProvider func() string
	// ConfigPersister is called when a new refresh token is persisted (login or
	// rotation). May be nil to skip config persistence.
	ConfigPersister ConfigPersister
	// FallbackErr is returned in non-interactive mode when no cached credentials
	// exist and no actionable lastErr is available. Defaults to a generic error.
	FallbackErr error
}

// OAuthTokenSource provides OIDC-backed access tokens via a four-tier strategy.
// It is safe for concurrent use.
type OAuthTokenSource struct {
	opts        Options
	mu          sync.Mutex
	tokenSource oauth2.TokenSource
}

// New creates an OAuthTokenSource from the given Options.
// It panics if opts.KeyProvider is nil, as this is a required field.
// If opts.OIDC.CallbackPort is zero, it defaults to remote.DefaultCallbackPort.
func New(opts Options) *OAuthTokenSource {
	if opts.KeyProvider == nil {
		panic("tokensource.New: Options.KeyProvider must not be nil")
	}
	if opts.FallbackErr == nil {
		opts.FallbackErr = errors.New("authentication required: no cached credentials found; complete an interactive login first")
	}
	if opts.OIDC.CallbackPort == 0 {
		opts.OIDC.CallbackPort = remote.DefaultCallbackPort
	}
	return &OAuthTokenSource{opts: opts}
}

// Token returns a valid access token string.
// It is safe for concurrent use.
func (t *OAuthTokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// lastErr tracks the most recent actionable error from tiers 1/2. In
	// non-interactive mode it is returned instead of FallbackErr so the caller
	// sees the real cause (e.g. invalid_grant, keyring locked).
	var lastErr error

	if tok, err := t.tryInMemoryToken(); err == nil {
		return tok, nil
	} else if !errors.Is(err, errCacheMiss) {
		lastErr = err
	}

	// Tier 1.5: secrets-cached access token — avoids IdP exchange when the
	// access token is still fresh. Concurrent short-lived processes share this
	// cached value instead of all racing to exchange the same refresh token,
	// preventing invalid_grant from OIDC providers that rotate refresh tokens.
	if tok, found := t.tryAccessTokenCache(ctx); found {
		return tok, nil
	}

	if tok, err := t.tryCachedToken(ctx); err == nil {
		return tok, nil
	} else if !errors.Is(err, errCacheMiss) {
		lastErr = err
	}

	// Tier 3: browser OIDC+PKCE flow — only in interactive mode.
	if !t.opts.Interactive {
		if lastErr != nil {
			return "", lastErr
		}
		return "", t.opts.FallbackErr
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
// Returns (token, nil) on success, ("", errCacheMiss) when no source is set,
// or ("", err) for a real token-endpoint error.
func (t *OAuthTokenSource) tryInMemoryToken() (string, error) {
	if t.tokenSource == nil {
		return "", errCacheMiss
	}
	tok, err := t.tokenSource.Token()
	if err == nil && tok.Valid() {
		return tok.AccessToken, nil
	}
	t.tokenSource = nil
	if err != nil {
		return "", err
	}
	return "", errCacheMiss
}

// tryCachedToken restores a token source from the secrets provider (tier 2/3)
// and tries to obtain a valid token from it.
// Returns (token, nil) on success, ("", errCacheMiss) on a cache miss,
// or ("", err) for a real error.
func (t *OAuthTokenSource) tryCachedToken(ctx context.Context) (string, error) {
	if err := t.tryRestoreFromCache(ctx); err != nil {
		return "", err // errCacheMiss propagates transparently
	}
	tok, err := t.tokenSource.Token()
	if err == nil && tok.Valid() {
		t.cacheAccessToken(ctx, tok.AccessToken, tok.Expiry)
		return tok.AccessToken, nil
	}
	t.tokenSource = nil
	if err != nil {
		return "", err
	}
	return "", errCacheMiss
}

// tryRestoreFromCache attempts to build a token source from the cached refresh
// token stored in the secrets provider.
func (t *OAuthTokenSource) tryRestoreFromCache(ctx context.Context) error {
	if t.opts.SecretsProvider == nil {
		return fmt.Errorf("no secrets provider available")
	}
	key := t.opts.KeyProvider()
	refreshToken, err := t.opts.SecretsProvider.GetSecret(ctx, key)
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

	// Use a non-caching refresher as the innermost source.
	//
	// oauth2Cfg.TokenSource caches internally and only refreshes when the real
	// expiry passes. When the outer ReuseTokenSource enters the preemptive window
	// (30 s before real expiry) it calls preemptiveTokenSource, which calls the
	// inner source; the inner source sees the real token as still valid and returns
	// it unchanged; preemptiveTokenSource shifts the expiry back by 30 s, producing
	// an already-expired token; the outer ReuseTokenSource then re-enters the chain
	// on every subsequent call — an infinite non-refreshing loop.
	//
	// A non-caching refresher always performs a network round-trip when called, so
	// the first call inside the preemptive window obtains a fresh token with a
	// real-future expiry. The outer ReuseTokenSource caches the shifted result and
	// serves it until the next window — exactly one refresh per window.
	//
	// Target stacking: ReuseTokenSource(nil, preemptive{PersistingTokenSource{nonCachingRefresher}})
	rawRefresher := oauth.NewNonCachingRefresher(oauth2Cfg, refreshToken, t.opts.OIDC.Audience)

	// Persist rotated refresh tokens back to the secrets provider so future
	// invocations can still restore the session if the provider invalidates the
	// old token on refresh (common with OIDC providers that rotate refresh tokens).
	base := remote.NewPersistingTokenSource(rawRefresher, t.makeTokenPersister(key))

	// Wrap with preemptive refresh so tokens are renewed 30 s before real expiry.
	t.tokenSource = withPreemptiveRefresh(base)
	return nil
}

// performBrowserFlow runs the interactive OIDC+PKCE browser flow and persists
// the resulting refresh token for future non-interactive use.
func (t *OAuthTokenSource) performBrowserFlow(ctx context.Context) error {
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

	// Build a non-caching refresher as the innermost source (see tryRestoreFromCache
	// for a detailed explanation of why caching inner sources cause an infinite loop
	// inside the preemptive window). Reuse the already-discovered flowCfg to avoid
	// a second OIDC round-trip.
	oauth2Cfg := oauth2ConfigFrom(flowCfg)
	initialToken := &oauth2.Token{
		AccessToken:  tokenResult.AccessToken,
		RefreshToken: tokenResult.RefreshToken,
		Expiry:       tokenResult.Expiry,
		TokenType:    tokenResult.TokenType,
	}

	var base oauth2.TokenSource = oauth.NewNonCachingRefresher(oauth2Cfg, initialToken.RefreshToken, flowCfg.Resource)

	if t.opts.SecretsProvider != nil {
		key := t.opts.KeyProvider()
		base = remote.NewPersistingTokenSource(base, t.makeTokenPersister(key))
		if tokenResult.RefreshToken != "" {
			if err := t.opts.SecretsProvider.SetSecret(ctx, key, tokenResult.RefreshToken); err != nil {
				slog.Warn("failed to persist initial refresh token", "error", err)
			} else {
				t.persistConfig(key, tokenResult.Expiry)
			}
		} else {
			slog.Debug("OIDC provider did not return a refresh token; token will not be persisted")
		}
	}

	// Pre-seed the outer ReuseTokenSource with the shifted initial token so the
	// just-obtained access token is served without an immediate network round-trip.
	t.tokenSource = withPreemptiveRefreshFrom(initialToken, base)
	return nil
}

// buildFlowConfig creates an oauth.Config for the interactive browser flow.
// PKCE (S256) is always enabled per OAuth 2.1 requirements for public clients.
func (t *OAuthTokenSource) buildFlowConfig(ctx context.Context) (*oauth.Config, error) {
	return oauth.CreateOAuthConfigFromOIDC(
		ctx,
		t.opts.OIDC.Issuer,
		t.opts.OIDC.ClientID,
		"", // public client — no client secret
		EnsureOfflineAccess(t.opts.OIDC.Scopes),
		true, // always use PKCE
		t.opts.OIDC.CallbackPort,
		t.opts.OIDC.Audience,
	)
}

// buildOAuth2Config creates a minimal oauth2.Config suitable for token refresh
// via the cached refresh token path (no browser required).
func (t *OAuthTokenSource) buildOAuth2Config(ctx context.Context) (*oauth2.Config, error) {
	flowCfg, err := t.buildFlowConfig(ctx)
	if err != nil {
		return nil, err
	}
	return oauth2ConfigFrom(flowCfg), nil
}

// oauth2ConfigFrom converts an oauth.Config (from OIDC discovery) into the
// minimal oauth2.Config used for headless token refresh.
func oauth2ConfigFrom(flowCfg *oauth.Config) *oauth2.Config {
	return &oauth2.Config{
		ClientID: flowCfg.ClientID,
		Scopes:   flowCfg.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   flowCfg.AuthURL,
			TokenURL:  flowCfg.TokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// makeTokenPersister returns a remote.TokenPersister that stores the refresh
// token in the secrets provider and calls ConfigPersister. It also invalidates
// the access-token cache so the next Token() call does not serve a token minted
// against the now-rotated refresh token.
func (t *OAuthTokenSource) makeTokenPersister(key string) remote.TokenPersister {
	return func(refreshToken string, expiry time.Time) error {
		ctx := context.Background()
		if err := t.opts.SecretsProvider.SetSecret(ctx, key, refreshToken); err != nil {
			return fmt.Errorf("persisting refresh token: %w", err)
		}
		t.persistConfig(key, expiry)
		// Invalidate the access-token cache so the next call does not serve a
		// token minted against the now-rotated refresh token. Order is intentional:
		// the refresh token (durable) is written first, then the AT cache is cleared.
		// Use key+"_AT" directly rather than accessTokenCacheKey() to guarantee we
		// clear the same key that was just persisted — ConfigPersister may have
		// updated CachedRefreshTokenRef, which would change KeyProvider's return
		// value and cause accessTokenCacheKey() to resolve a different key.
		// We write "" rather than calling DeleteSecret for provider compatibility.
		if err := t.opts.SecretsProvider.SetSecret(ctx, key+"_AT", ""); err != nil {
			slog.Warn("failed to invalidate access-token cache after refresh token rotation",
				"error", err)
		}
		return nil
	}
}

// persistConfig calls the ConfigPersister (if set) to record the token ref and expiry.
func (t *OAuthTokenSource) persistConfig(key string, expiry time.Time) {
	if t.opts.ConfigPersister != nil {
		t.opts.ConfigPersister(key, expiry)
	}
}

// accessTokenCacheKey returns the secrets-provider key for the cached access token.
func (t *OAuthTokenSource) accessTokenCacheKey() string {
	return t.opts.KeyProvider() + "_AT"
}

// tryAccessTokenCache reads a previously cached access token from the secrets
// provider and returns it if still valid.
func (t *OAuthTokenSource) tryAccessTokenCache(ctx context.Context) (string, bool) {
	if t.opts.SecretsProvider == nil {
		return "", false
	}
	raw, err := t.opts.SecretsProvider.GetSecret(ctx, t.accessTokenCacheKey())
	if err != nil || raw == "" {
		return "", false
	}
	idx := strings.LastIndex(raw, "|")
	if idx <= 0 {
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
// provider so concurrent short-lived processes can reuse it. Errors are logged
// at debug level and suppressed — a write failure degrades gracefully to a full
// refresh on the next call.
func (t *OAuthTokenSource) cacheAccessToken(ctx context.Context, token string, expiry time.Time) {
	if t.opts.SecretsProvider == nil || token == "" || expiry.IsZero() {
		return
	}
	val := token + "|" + expiry.UTC().Format(time.RFC3339)
	if err := t.opts.SecretsProvider.SetSecret(ctx, t.accessTokenCacheKey(), val); err != nil {
		slog.Debug("failed to cache access token", "error", err)
	}
}

// DeriveSecretKey computes a secrets-provider key for an OAuth refresh token.
// The formula is: <prefix><8 hex chars> where the hex is derived from
// sha256(resourceURL + "\x00" + issuer)[:4].
func DeriveSecretKey(prefix, resourceURL, issuer string) string {
	h := sha256.Sum256([]byte(resourceURL + "\x00" + issuer))
	return prefix + hex.EncodeToString(h[:4])
}

// EnsureOfflineAccess returns scopes with "offline_access" appended if absent.
// This scope is required for the provider to return a refresh token.
func EnsureOfflineAccess(scopes []string) []string {
	if slices.Contains(scopes, "offline_access") {
		return scopes
	}
	return append(scopes[:len(scopes):len(scopes)], "offline_access")
}

// preemptiveTokenSource wraps an oauth2.TokenSource and shifts each returned
// token's expiry back by preemptiveRefreshWindow.
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
	return withPreemptiveRefreshFrom(nil, src)
}

// withPreemptiveRefreshFrom is like withPreemptiveRefresh but pre-seeds the outer
// ReuseTokenSource with a shifted copy of initial (if non-nil and valid).
func withPreemptiveRefreshFrom(initial *oauth2.Token, src oauth2.TokenSource) oauth2.TokenSource {
	var seeded *oauth2.Token
	if initial != nil && initial.Valid() && !initial.Expiry.IsZero() {
		shifted := *initial
		shifted.Expiry = initial.Expiry.Add(-preemptiveRefreshWindow)
		if shifted.Valid() {
			seeded = &shifted
		}
	}
	return oauth2.ReuseTokenSource(seeded, &preemptiveTokenSource{inner: src})
}

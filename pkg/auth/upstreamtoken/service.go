// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

const (
	// idTokenExpirySkew is how far ahead of the ID token's `exp` the
	// ID-token trigger fires, so a token is not handed out moments before it
	// expires.
	idTokenExpirySkew = 30 * time.Second

	// defaultIDTokenSuppression bounds how long the ID-token trigger stays
	// suppressed for a row whose access token asserts no expiry and whose
	// session expiry is unknown, so the suppression map cannot grow without
	// bound.
	defaultIDTokenSuppression = time.Hour

	// idTokenRefreshFailureBackoff is how long the ID-token trigger stays
	// suppressed after a failed ID-token-triggered refresh. It is short so a
	// transient upstream error does not reopen the stale-ID-token window for
	// the rest of the access token's lifetime.
	idTokenRefreshFailureBackoff = time.Minute

	// idTokenSuppressionSweepInterval rate-limits the full sweep of expired
	// suppression entries, which runs under mu. Entries are also dropped
	// lazily when read after expiry.
	idTokenSuppressionSweepInterval = time.Minute
)

// InProcessService implements the Service interface for in-process use.
// It composes storage (read) and refresher (refresh + persist) to provide a
// single GetValidTokens call. Concurrent-refresh deduplication is delegated to
// the refresher's own singleflight.Group keyed by an opaque storage-row identity,
// so the same Group covers both the handler's chain-walk path and the runtime
// token-swap path within this process.
//
// Refresh is triggered by an expired access token or by an expired ID token
// (its `exp` claim, read without re-verifying the signature it was validated
// with when stored). The ID-token trigger serves providers whose ID-token
// lifetime is shorter than their access-token lifetime (e.g. Microsoft Entra
// ID), which would otherwise hand consumers an expired ID token for part of
// every access-token lifetime. It never fires when the row has no refresh
// token, or for an empty, unparseable or `exp`-less ID token.
//
// It cannot help when the provider omits id_token on refresh (OIDC Core 1.0
// §12.2): the expired ID token is then carried forward, and the trigger is
// suppressed for that row until the access token next expires so reads do not
// loop on refresh. After a failed ID-token-triggered refresh the trigger backs
// off briefly (idTokenRefreshFailureBackoff). Consumers that need a currently
// valid ID token must therefore still check `exp` themselves.
//
// Suppression state is held in process memory: replicas sharing storage each
// keep their own, so each may perform one extra refresh per access-token
// lifetime for a provider that omits id_token. Likewise, refresh
// deduplication is per process; replicas that refresh the same row at the same
// moment can each redeem its refresh token (see upstreamTokenRefresher), which
// a provider enforcing single-use refresh-token rotation may treat as reuse.
// The ID-token trigger adds refresh events, but no new race shape.
type InProcessService struct {
	storage   storage.UpstreamTokenStorage
	refresher storage.UpstreamTokenRefresher

	// mu guards idTokenSuppressions and lastSuppressionSweep.
	mu sync.Mutex
	// idTokenSuppressions records rows for which an ID-token-triggered refresh
	// should not be attempted again until the access token next expires,
	// because a previous refresh left the ID token expired (the provider
	// omitted id_token, or the refresh failed). Keyed by suppressionKey.
	idTokenSuppressions  map[string]idTokenSuppression
	lastSuppressionSweep time.Time
}

// idTokenSuppression pins a suppression to the exact stale ID token it was
// recorded for, so a rotated ID token is never suppressed.
type idTokenSuppression struct {
	idToken string
	until   time.Time
}

// Compile-time checks.
var (
	_ Service     = (*InProcessService)(nil)
	_ TokenReader = (*InProcessService)(nil)
)

// NewInProcessService creates a new InProcessService.
// The refresher may be nil if upstream token refresh is not configured;
// expired tokens will return ErrNoRefreshToken in that case.
func NewInProcessService(
	stor storage.UpstreamTokenStorage,
	refresher storage.UpstreamTokenRefresher,
) *InProcessService {
	return &InProcessService{
		storage:             stor,
		refresher:           refresher,
		idTokenSuppressions: make(map[string]idTokenSuppression),
	}
}

// GetValidTokens returns a valid upstream credential for a session and provider.
// It transparently refreshes expired access tokens using the refresh token,
// and refreshes expired ID tokens where possible (see InProcessService).
func (s *InProcessService) GetValidTokens(ctx context.Context, sessionID, providerName string) (*UpstreamCredential, error) {
	tokens, err := s.storage.GetUpstreamTokens(ctx, sessionID, providerName)
	if err != nil {
		// ErrExpired returns tokens (including refresh token) alongside the error.
		// Attempt a refresh before giving up.
		if errors.Is(err, storage.ErrExpired) {
			if tokens != nil {
				return s.refreshOrFail(ctx, sessionID, providerName, tokens)
			}
			// Expired but storage returned nil tokens — can't refresh.
			return nil, ErrNoRefreshToken
		}
		if errors.Is(err, storage.ErrNotFound) {
			return nil, ErrSessionNotFound
		}
		if errors.Is(err, storage.ErrInvalidBinding) {
			return nil, ErrInvalidBinding
		}
		return nil, fmt.Errorf("failed to get upstream tokens: %w", err)
	}

	// Defense in depth: some storage implementations may return tokens
	// without checking expiry (the interface does not require it).
	if !tokens.ExpiresAt.IsZero() && tokens.IsExpired(time.Now()) {
		return s.refreshOrFail(ctx, sessionID, providerName, tokens)
	}

	if s.shouldRefreshForIDToken(sessionID, providerName, tokens) {
		return s.refreshForIDToken(ctx, sessionID, providerName, tokens)
	}

	return &UpstreamCredential{AccessToken: tokens.AccessToken, IDToken: tokens.IDToken}, nil
}

// GetAllUpstreamCredentials returns access tokens and ID tokens for all upstream
// providers in a session in a single storage round-trip. Expired access tokens
// are refreshed transparently; providers whose access token cannot be refreshed
// are included in the returned failed slice so downstream middleware can return
// a clean 401 for the access-token use case. The IDToken on a returned entry is
// the rotated ID token when a refresh produced one (OIDC Core 1.0 §12.2),
// otherwise the original JWT captured at the initial OIDC login; it is not
// independently validated for freshness and may be empty if the upstream login
// never yielded one. An expired ID token also triggers a refresh (see
// InProcessService); a failed ID-token-triggered refresh does not put the
// provider in the failed slice, since its access token is still valid. Callers
// that PRESENT it as a credential must expect expiry to be rejected; callers
// that only READ ITS CLAIMS are unaffected by expiry. See
// Identity.UpstreamIDTokens for both consumers.
//
// Returns an empty map and nil failed slice (not error) for unknown sessions.
func (s *InProcessService) GetAllUpstreamCredentials(
	ctx context.Context, sessionID string,
) (map[string]UpstreamCredential, []string, error) {
	allTokens, err := s.storage.GetAllUpstreamTokens(ctx, sessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("bulk read upstream tokens: %w", err)
	}

	if len(allTokens) == 0 {
		return map[string]UpstreamCredential{}, nil, nil
	}

	result := make(map[string]UpstreamCredential, len(allTokens))
	var failed []string
	// TODO(auth): Refresh providers in parallel using errgroup to avoid
	// worst-case latency of N refreshes when multiple providers need refresh.
	for providerName, tokens := range allTokens {
		if tokens == nil {
			continue
		}

		// If token is not expired, use it directly.
		if tokens.ExpiresAt.IsZero() || !tokens.IsExpired(time.Now()) {
			if s.shouldRefreshForIDToken(sessionID, providerName, tokens) {
				cred, idErr := s.refreshForIDToken(ctx, sessionID, providerName, tokens)
				if idErr != nil {
					slog.WarnContext(ctx, "upstream token row removed by failed refresh; provider will require re-authentication",
						"session_id", sessionID,
						"provider", providerName,
						"error", idErr,
					)
					failed = append(failed, providerName)
					continue
				}
				result[providerName] = *cred
				continue
			}
			result[providerName] = UpstreamCredential{
				AccessToken: tokens.AccessToken,
				IDToken:     tokens.IDToken,
			}
			continue
		}

		// Token is expired — attempt refresh.
		refreshed, refreshErr := s.refreshOrFail(ctx, sessionID, providerName, tokens)
		if refreshErr != nil {
			slog.WarnContext(ctx, "upstream token refresh failed; provider will require re-authentication",
				"session_id", sessionID,
				"provider", providerName,
				"error", refreshErr,
			)
			failed = append(failed, providerName)
			continue
		}
		// refreshOrFail carries through the rotated ID token when the provider
		// issued one, otherwise the original login ID token (see its doc).
		result[providerName] = *refreshed
	}

	// The returned ID tokens are deliberately not checked for expiry here. Neither
	// of today's two consumers wants that pre-check: one presents the token as an
	// RFC 8693 subject_token and relies on the IdP to reject an expired assertion
	// (pkg/vmcp/auth/strategies surfaces the resulting invalid_grant), and the other
	// only reads its claims, for which expiry is irrelevant. Enforcing it here would
	// break the second while duplicating what the IdP already does for the first.
	// A future consumer that presents the token to a sink which does NOT validate
	// `exp` would change that calculus — such a caller must check it itself.
	// An expired ID token is refreshed when the provider can rotate it, but this
	// still does not filter what it returns: an ID token the
	// provider declined to rotate is passed through expired.
	// See Identity.UpstreamIDTokens for both.
	return result, failed, nil
}

// refreshOrFail attempts a refresh via the shared refresher and maps errors to
// the service's sentinel errors. Deduplication of concurrent refreshes for the
// same logical upstream-token row is handled inside the refresher using an
// opaque storage-row identity.
func (s *InProcessService) refreshOrFail(
	ctx context.Context,
	sessionID string,
	providerName string,
	expired *storage.UpstreamTokens,
) (*UpstreamCredential, error) {
	if expired.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}

	if s.refresher == nil {
		slog.Debug("token refresher not configured, cannot refresh upstream tokens",
			"session_id", sessionID,
			"provider", providerName,
		)
		return nil, ErrNoRefreshToken
	}

	refreshed, err := s.refresher.RefreshAndStore(ctx, sessionID, expired)
	if err != nil {
		slog.Warn("upstream token refresh failed",
			"session_id", sessionID,
			"provider", providerName,
			"error", err,
		)
		return nil, fmt.Errorf("%w: %w", ErrRefreshFailed, err)
	}

	if refreshed == nil {
		return nil, ErrRefreshFailed
	}

	// Prefer the ID token from the refresh response when the provider rotated
	// it (OIDC Core 1.0 §12.2 permits — but does not require — a new id_token on
	// refresh). Fall back to the original login ID token when the refresh response
	// omitted one. The primary carry-forward into storage is in
	// upstreamTokenRefresher.refreshAndStore; this fallback is defense-in-depth
	// so the caller never sees an empty subject token.
	idToken := refreshed.IDToken
	if idToken == "" {
		idToken = expired.IDToken
	}

	// A refresh that still leaves the ID token expired (the provider omitted id_token) must not be retried on every read.
	// Suppress the trigger for this exact ID token until the new access token
	// expires, when the access-token trigger refreshes anyway.
	if idTokenExpired(idToken, time.Now()) {
		s.suppressIDTokenRefresh(sessionID, providerName, idToken, refreshed.ExpiresAt, refreshed.SessionExpiresAt)
	}

	return &UpstreamCredential{AccessToken: refreshed.AccessToken, IDToken: idToken}, nil
}

// shouldRefreshForIDToken reports whether the ID-token trigger applies to a
// row whose access token is still valid: a refresh token exists, the stored ID token is expired, and the trigger is not
// suppressed for that ID token.
func (s *InProcessService) shouldRefreshForIDToken(
	sessionID, providerName string, tokens *storage.UpstreamTokens,
) bool {
	if tokens.RefreshToken == "" {
		return false
	}
	now := time.Now()
	if !idTokenExpired(tokens.IDToken, now) {
		return false
	}
	return !s.idTokenRefreshSuppressed(sessionID, providerName, tokens.IDToken, now)
}

// refreshForIDToken refreshes a row whose access token is still valid but whose
// ID token has expired. A failed refresh is deliberately not fatal: the stored
// credential is returned unchanged, since its access token is still usable and
// a transient upstream error should not force re-authentication. The trigger
// then backs off for idTokenRefreshFailureBackoff (or until the access token
// expires, if sooner) so a failing upstream is not hammered on every read; no
// backoff is recorded when the failure is the caller's own context ending.
//
// The one fatal case is a row the failed refresh removed: the refresher deletes
// it when the provider rotated the refresh token but the rotation could not be
// persisted, leaving the old one dead. Serving the stored copy would then hand
// out a credential that no longer exists, so the row is re-read and
// ErrRefreshFailed returned if it is gone.
func (s *InProcessService) refreshForIDToken(
	ctx context.Context, sessionID, providerName string, tokens *storage.UpstreamTokens,
) (*UpstreamCredential, error) {
	refreshed, err := s.refreshOrFail(ctx, sessionID, providerName, tokens)
	if err == nil {
		return refreshed, nil
	}
	// refreshOrFail has already logged the failure at WARN.
	if ctx.Err() != nil {
		return &UpstreamCredential{AccessToken: tokens.AccessToken, IDToken: tokens.IDToken}, nil
	}
	if _, readErr := s.storage.GetUpstreamTokens(ctx, sessionID, providerName); errors.Is(readErr, storage.ErrNotFound) {
		return nil, err
	}
	slog.DebugContext(ctx, "returning stored upstream tokens after failed refresh for expired ID token",
		"session_id", sessionID,
		"provider", providerName,
	)
	until := time.Now().Add(idTokenRefreshFailureBackoff)
	if !tokens.ExpiresAt.IsZero() && tokens.ExpiresAt.Before(until) {
		until = tokens.ExpiresAt
	}
	s.suppressIDTokenRefresh(sessionID, providerName, tokens.IDToken, until, time.Time{})
	return &UpstreamCredential{AccessToken: tokens.AccessToken, IDToken: tokens.IDToken}, nil
}

// suppressIDTokenRefresh records that the ID-token trigger must not fire for
// idToken on this row until the access token expires. When the access token
// asserts no expiry, the session expiry (or defaultIDTokenSuppression) bounds
// the entry instead. Expired entries are swept at most once per
// idTokenSuppressionSweepInterval, which keeps the map bounded by the rows
// suppressed within roughly the last interval plus those still live.
func (s *InProcessService) suppressIDTokenRefresh(
	sessionID, providerName, idToken string, accessExpiresAt, sessionExpiresAt time.Time,
) {
	now := time.Now()
	until := accessExpiresAt
	if until.IsZero() {
		until = sessionExpiresAt
	}
	if until.IsZero() {
		until = now.Add(defaultIDTokenSuppression)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.lastSuppressionSweep) >= idTokenSuppressionSweepInterval {
		for k, v := range s.idTokenSuppressions {
			if !now.Before(v.until) {
				delete(s.idTokenSuppressions, k)
			}
		}
		s.lastSuppressionSweep = now
	}
	s.idTokenSuppressions[suppressionKey(sessionID, providerName)] = idTokenSuppression{idToken: idToken, until: until}
}

// idTokenRefreshSuppressed reports whether the ID-token trigger is suppressed
// for idToken on this row at now. An entry found expired is dropped.
func (s *InProcessService) idTokenRefreshSuppressed(sessionID, providerName, idToken string, now time.Time) bool {
	key := suppressionKey(sessionID, providerName)
	s.mu.Lock()
	defer s.mu.Unlock()
	sup, ok := s.idTokenSuppressions[key]
	if !ok {
		return false
	}
	if !now.Before(sup.until) {
		delete(s.idTokenSuppressions, key)
		return false
	}
	return sup.idToken == idToken
}

func suppressionKey(sessionID, providerName string) string {
	return sessionID + "\x00" + providerName
}

// idTokenExpired reports whether idToken carries an `exp` claim that has
// passed, allowing idTokenExpirySkew. The token's signature is not verified:
// it was validated when stored, and only its expiry is read here. An empty or
// unparseable token, or one without `exp`, is not treated as expired, so the
// trigger never fires for a row it cannot reason about.
func idTokenExpired(idToken string, now time.Time) bool {
	if idToken == "" {
		return false
	}
	parsed, _, err := jwt.NewParser().ParseUnverified(idToken, jwt.MapClaims{})
	if err != nil {
		return false
	}
	exp, err := parsed.Claims.GetExpirationTime()
	if err != nil || exp == nil {
		return false
	}
	return !now.Add(idTokenExpirySkew).Before(exp.Time)
}

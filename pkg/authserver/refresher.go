// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// refreshTimeout bounds how long a singleflight-deduplicated token refresh
// may take before being cancelled. It is deliberately detached from the
// triggering request's context so that waiting callers are not abandoned.
const refreshTimeout = 30 * time.Second

// upstreamStoreMaxAttempts is the maximum number of attempts to store refreshed
// upstream tokens before giving up.
const upstreamStoreMaxAttempts = 3

// upstreamStoreRetryBackoff is the fixed delay between store retry attempts.
const upstreamStoreRetryBackoff = 50 * time.Millisecond

// upstreamDeleteTimeout bounds how long the best-effort per-provider delete may
// take. A fresh detached context is used so a ctx deadline/cancel on the store
// attempts does not also kill the delete, which would leave the stale row behind.
const upstreamDeleteTimeout = 5 * time.Second

// upstreamTokenRefresher implements storage.UpstreamTokenRefresher by wrapping
// a set of upstream OAuth2Providers (keyed by provider name) and
// UpstreamTokenStorage (for persisting the refreshed tokens). On each refresh
// call it dispatches to the correct provider based on the expired token's
// ProviderID.
//
// Two layers cooperate to make redeeming a single-use, rotating upstream
// refresh token safe under concurrency:
//   - sfGroup deduplicates concurrent refreshes by the opaque storage-row
//     identity returned by UpstreamTokenStorage, but only WITHIN THIS PROCESS
//     — it saves redundant upstream calls and redis round-trips when this
//     process alone receives several concurrent requests for the same row.
//   - CompareAndSwapUpstreamTokens (see refreshAndStore) is what actually
//     protects correctness ACROSS PROCESSES: every write is conditioned on
//     the refresh token this call redeemed still being the one stored, so a
//     replica that loses a cross-process race fails its write instead of
//     clobbering a winning replica's rotated token. singleflight is an
//     optimization layered on top of that guarantee, not a substitute for it.
type upstreamTokenRefresher struct {
	providers            map[string]upstream.OAuth2Provider
	storage              storage.UpstreamTokenStorage
	refreshTokenLifespan time.Duration
	sfGroup              singleflight.Group
}

// Compile-time check that upstreamTokenRefresher implements storage.UpstreamTokenRefresher.
var _ storage.UpstreamTokenRefresher = (*upstreamTokenRefresher)(nil)

// RefreshAndStore validates deterministic input errors before resolving the
// opaque storage-row identity used to deduplicate a refresh within this process.
// The leader re-reads that row under a detached timeout so it never redeems a
// stale refresh token supplied by a caller. It only short-circuits on that
// re-read when the row is actually unexpired; a storage backend that returns
// tokens without checking expiry does not fool it into skipping the refresh.
func (r *upstreamTokenRefresher) RefreshAndStore(
	ctx context.Context,
	sessionID string,
	expired *storage.UpstreamTokens,
) (*storage.UpstreamTokens, error) {
	if expired == nil {
		return nil, errors.New("expired tokens are required")
	}
	if expired.RefreshToken == "" {
		return nil, errors.New("no refresh token available for upstream token refresh")
	}
	if _, ok := r.providers[expired.ProviderID]; !ok {
		return nil, fmt.Errorf("no upstream provider configured for %q", expired.ProviderID)
	}

	rowID, err := r.storage.ResolveUpstreamTokenRowID(ctx, sessionID, expired.ProviderID)
	if err != nil {
		return nil, err
	}
	if rowID == "" {
		return nil, errors.New("upstream token row ID cannot be empty")
	}

	result, err, _ := r.sfGroup.Do(string(rowID), func() (any, error) {
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		defer cancel()

		authoritative, readErr := r.storage.GetUpstreamTokens(refreshCtx, sessionID, expired.ProviderID)
		if readErr == nil {
			// Some storage implementations may return tokens without checking
			// access-token expiry (the interface does not require it; see
			// storage.UpstreamTokenStorage.GetUpstreamTokens). Only short-circuit
			// when the re-read row is actually unexpired; otherwise fall through
			// and refresh using it as the authoritative row.
			if authoritative == nil {
				return nil, fmt.Errorf(
					"upstream token row missing on re-read for session %q provider %q",
					sessionID, expired.ProviderID,
				)
			}
			if !authoritative.IsExpired(time.Now()) {
				return authoritative, nil
			}
			return r.refreshAndStore(refreshCtx, sessionID, authoritative)
		}
		if !errors.Is(readErr, storage.ErrExpired) || authoritative == nil {
			return nil, readErr
		}
		return r.refreshAndStore(refreshCtx, sessionID, authoritative)
	})
	if err != nil {
		return nil, err
	}
	refreshed, ok := result.(*storage.UpstreamTokens)
	if !ok || refreshed == nil {
		return nil, errors.New("unexpected nil result from upstream token refresh")
	}
	refreshedCopy := *refreshed
	return &refreshedCopy, nil
}

// refreshAndStore performs the actual token refresh and storage write.
func (r *upstreamTokenRefresher) refreshAndStore(
	ctx context.Context,
	sessionID string,
	expired *storage.UpstreamTokens,
) (*storage.UpstreamTokens, error) {
	if expired.RefreshToken == "" {
		return nil, errors.New("no refresh token available for upstream token refresh")
	}

	slog.Debug("attempting upstream token refresh",
		"session_id", sessionID,
		"provider_id", expired.ProviderID,
	)

	// Look up the provider that issued this token
	provider, ok := r.providers[expired.ProviderID]
	if !ok {
		return nil, fmt.Errorf("no upstream provider configured for %q", expired.ProviderID)
	}

	// Refresh tokens via the upstream provider
	newTokens, err := provider.RefreshTokens(ctx, expired.RefreshToken, expired.UpstreamSubject)
	if err != nil {
		return nil, fmt.Errorf("upstream token refresh failed: %w", err)
	}

	// Defensive re-anchor of SessionExpiresAt: the post-PR callback write sets
	// SessionExpiresAt unconditionally so it can be carried forward here as a
	// storage TTL bound. Pre-PR rows persisted without that field decode as
	// zero. If such a legacy row is refreshed and the upstream rotation drops
	// expires_in, both ExpiresAt and SessionExpiresAt would be zero, the row
	// would be stored without any TTL bound, and the Memory backend would
	// retain it indefinitely. Re-anchor to now+RefreshTokenLifespan to restore
	// the invariant. The Redis 30-day per-key TTL also caps the legacy
	// behavior, but Memory has no such backstop.
	sessionExpiresAt := expired.SessionExpiresAt
	if sessionExpiresAt.IsZero() && newTokens.ExpiresAt.IsZero() {
		sessionExpiresAt = time.Now().Add(r.refreshTokenLifespan)
		slog.Debug("re-anchored zero SessionExpiresAt on refresh of legacy upstream token row",
			"session_id", sessionID,
			"provider_id", expired.ProviderID,
			"refresh_token_lifespan", r.refreshTokenLifespan,
		)
	}

	// Build updated storage tokens preserving binding fields from the original
	updated := &storage.UpstreamTokens{
		ProviderID:       expired.ProviderID,
		AccessToken:      newTokens.AccessToken,
		RefreshToken:     newTokens.RefreshToken,
		IDToken:          newTokens.IDToken,
		ExpiresAt:        newTokens.ExpiresAt,
		SessionExpiresAt: sessionExpiresAt,
		UserID:           expired.UserID,
		UpstreamSubject:  expired.UpstreamSubject,
		ClientID:         expired.ClientID,
	}

	// Detect rotation BEFORE the old-RT backfill below so the original
	// (provider-issued) value is compared, not the backfilled fallback.
	rotated := updated.RefreshToken != "" && updated.RefreshToken != expired.RefreshToken

	// If the provider didn't rotate the refresh token, keep the original.
	if updated.RefreshToken == "" {
		updated.RefreshToken = expired.RefreshToken
	}

	// OIDC Core 1.0 §12.2 permits but does not require a new id_token on refresh.
	// When the provider omits one, keep the ID token captured at the initial login
	// so it is not erased from storage. The write below replaces the whole row,
	// so without this the persisted IDToken would be overwritten with "" and the
	// original login ID token would be lost for the remainder of the session.
	// Mirrors the RefreshToken carry-forward above.
	if updated.IDToken == "" {
		updated.IDToken = expired.IDToken
	}

	// expectedRefreshToken is the value this call actually redeemed with the
	// upstream provider. Writing through CompareAndSwapUpstreamTokens (rather
	// than an unconditional StoreUpstreamTokens) protects against another
	// process — a different replica of this auth server sharing this storage —
	// having already redeemed and persisted a rotation of the SAME refresh
	// token concurrently: only the replica whose expected value still matches
	// the stored row may write, so a losing replica cannot clobber the
	// winner's rotated token with its own now-stale redemption.
	expectedRefreshToken := expired.RefreshToken

	if err := r.compareAndSwapWithRetry(ctx, sessionID, expired.ProviderID, expectedRefreshToken, updated); err != nil {
		if errors.Is(err, storage.ErrConcurrentRefresh) {
			return r.resolveConcurrentRefreshConflict(ctx, sessionID, expired.ProviderID)
		}

		if !rotated {
			// The old refresh token is still valid in storage; the caller can
			// proceed with the refreshed access token for this request.
			slog.Warn("failed to persist refreshed upstream tokens; old refresh token still valid",
				"session_id", sessionID,
				"provider_id", expired.ProviderID,
				"error", err,
			)
			return updated, nil
		}

		// The IdP rotated the refresh token: the new RT was redeemed but could
		// not be persisted. The old RT is now dead in storage and will trigger
		// reuse-detection lockout on the next refresh attempt.
		//
		// Residual window: if the process crashes here, the dead old RT stays in
		// storage. ErrNotFound on the next refresh attempt forces re-auth, which
		// is the backstop. Store-intent-before-redeem is out of scope for this fix.
		deleteCtx, deleteCancel := context.WithTimeout(context.WithoutCancel(ctx), upstreamDeleteTimeout)
		defer deleteCancel()
		if delErr := r.storage.DeleteUpstreamTokensForProvider(deleteCtx, sessionID, expired.ProviderID); delErr != nil {
			slog.Error("failed to delete stale upstream token row after rotation persist failure",
				"session_id", sessionID,
				"provider_id", expired.ProviderID,
				"error", delErr,
			)
		}
		return nil, fmt.Errorf(
			"failed to persist rotated upstream refresh token for session %q provider %q: %w",
			sessionID, expired.ProviderID, err,
		)
	}

	slog.Debug("upstream tokens refreshed successfully",
		"session_id", sessionID,
		"provider_id", expired.ProviderID,
	)

	return updated, nil
}

// resolveConcurrentRefreshConflict is reached when this call's redemption
// lost a compare-and-swap race against another process (typically a
// different replica of this auth server) that refreshed and persisted the same
// row concurrently — CompareAndSwapUpstreamTokens returned
// storage.ErrConcurrentRefresh because the stored refresh token had already
// moved past the value this call redeemed with.
//
// It re-reads the authoritative row: if that read is unexpired, the other
// process's write is the correct result for this call to hand back — this
// call's own redemption produced an access/refresh token pair the IdP (or the
// winning replica) has already superseded, so it must not be used or written.
// If the re-read is still expired, missing, or otherwise unusable, the loss is
// unrecoverable for this attempt: the refresh token this call redeemed is now
// dead both at the IdP (which enforces single-use) and in storage (the winner
// overwrote it before this call's CAS ran), so the caller must surface an
// error — retrying would only redeem the same already-consumed refresh token
// again.
func (r *upstreamTokenRefresher) resolveConcurrentRefreshConflict(
	ctx context.Context, sessionID, providerID string,
) (*storage.UpstreamTokens, error) {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()

	winner, err := r.storage.GetUpstreamTokens(readCtx, sessionID, providerID)
	if err == nil && winner != nil && !winner.IsExpired(time.Now()) {
		slog.Debug("lost concurrent upstream token refresh race; using winning replica's tokens",
			"session_id", sessionID,
			"provider_id", providerID,
		)
		return winner, nil
	}

	slog.Error("lost concurrent upstream token refresh race and re-read found no usable winner; "+
		"the redeemed refresh token is unrecoverable for this attempt",
		"session_id", sessionID,
		"provider_id", providerID,
		"error", err,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"concurrent upstream token refresh for session %q provider %q: %w: %w",
			sessionID, providerID, storage.ErrConcurrentRefresh, err,
		)
	}
	return nil, fmt.Errorf(
		"concurrent upstream token refresh for session %q provider %q: %w",
		sessionID, providerID, storage.ErrConcurrentRefresh,
	)
}

// compareAndSwapWithRetry attempts CompareAndSwapUpstreamTokens up to
// upstreamStoreMaxAttempts times, waiting upstreamStoreRetryBackoff between
// attempts. Returns nil on first success. A storage.ErrConcurrentRefresh
// result is returned immediately without retrying: the comparison failed
// because expectedRefreshToken is now definitively stale, and retrying the
// identical compare-and-swap cannot change that outcome. For any other error,
// returns the last error after all attempts are exhausted; ctx-cancellation
// short-circuits between attempts and returns the last store error (not
// ctx.Err()).
func (r *upstreamTokenRefresher) compareAndSwapWithRetry(
	ctx context.Context,
	sessionID, providerID, expectedRefreshToken string,
	updated *storage.UpstreamTokens,
) error {
	var lastErr error
	for attempt := 1; attempt <= upstreamStoreMaxAttempts; attempt++ {
		casErr := r.storage.CompareAndSwapUpstreamTokens(ctx, sessionID, providerID, expectedRefreshToken, updated)
		if casErr == nil {
			return nil
		}
		if errors.Is(casErr, storage.ErrConcurrentRefresh) {
			return casErr
		}
		lastErr = casErr
		slog.Debug("failed to store refreshed upstream tokens",
			"session_id", sessionID,
			"provider_id", providerID,
			"attempt", attempt,
			"error", lastErr,
		)
		if attempt < upstreamStoreMaxAttempts {
			select {
			case <-ctx.Done():
				return lastErr
			case <-time.After(upstreamStoreRetryBackoff):
			}
		}
	}
	return lastErr
}

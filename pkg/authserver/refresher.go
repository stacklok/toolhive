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
// ProviderID, deduplicating concurrent refreshes for the same
// (session, provider) pair via sfGroup.
type upstreamTokenRefresher struct {
	providers            map[string]upstream.OAuth2Provider
	storage              storage.UpstreamTokenStorage
	refreshTokenLifespan time.Duration
	sfGroup              singleflight.Group
}

// Compile-time check that upstreamTokenRefresher implements storage.UpstreamTokenRefresher.
var _ storage.UpstreamTokenRefresher = (*upstreamTokenRefresher)(nil)

// RefreshAndStore deduplicates concurrent refreshes for the same (session,
// provider) pair, then delegates to refreshAndStore. The detached-context
// timeout ensures waiting callers are not abandoned if the initiating request
// cancels before the upstream round-trip completes.
func (r *upstreamTokenRefresher) RefreshAndStore(
	ctx context.Context,
	sessionID string,
	expired *storage.UpstreamTokens,
) (*storage.UpstreamTokens, error) {
	if expired == nil {
		return nil, errors.New("expired tokens are required")
	}

	// providerName == ProviderID throughout the authserver; both originate from
	// UpstreamConfig.Name and are stored verbatim in UpstreamTokens.ProviderID.
	key := sessionID + ":" + expired.ProviderID
	result, err, _ := r.sfGroup.Do(key, func() (any, error) {
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		defer cancel()
		return r.refreshAndStore(refreshCtx, sessionID, expired)
	})
	if err != nil {
		return nil, err
	}
	refreshed, ok := result.(*storage.UpstreamTokens)
	if !ok || refreshed == nil {
		return nil, errors.New("unexpected nil result from upstream token refresh")
	}
	return refreshed, nil
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
	// so it is not erased from storage. StoreUpstreamTokens replaces the whole row,
	// so without this the persisted IDToken would be overwritten with "" and the
	// original login ID token would be lost for the remainder of the session.
	// Mirrors the RefreshToken carry-forward above.
	if updated.IDToken == "" {
		updated.IDToken = expired.IDToken
	}

	if err := r.storeWithRetry(ctx, sessionID, expired.ProviderID, updated); err != nil {
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

// storeWithRetry attempts to store updated tokens up to upstreamStoreMaxAttempts times,
// waiting upstreamStoreRetryBackoff between attempts. Returns nil on first success.
// Returns the last error after all attempts are exhausted. Ctx-cancellation short-circuits
// between attempts and returns the last store error (not ctx.Err()).
func (r *upstreamTokenRefresher) storeWithRetry(
	ctx context.Context,
	sessionID, providerID string,
	updated *storage.UpstreamTokens,
) error {
	var lastErr error
	for attempt := 1; attempt <= upstreamStoreMaxAttempts; attempt++ {
		storeErr := r.storage.StoreUpstreamTokens(ctx, sessionID, providerID, updated)
		if storeErr == nil {
			return nil
		}
		lastErr = storeErr
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

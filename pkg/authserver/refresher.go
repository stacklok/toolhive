// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// upstreamTokenRefresher implements storage.UpstreamTokenRefresher by wrapping
// a set of upstream OAuth2Providers (keyed by provider name) and
// UpstreamTokenStorage (for persisting the refreshed tokens). On each refresh
// call it dispatches to the correct provider based on the expired token's
// ProviderID.
//
// refreshTokenLifespan is used as the defensive re-anchor value for
// SessionExpiresAt when the persisted row pre-dates the unconditional callback
// write (legacy data) and the upstream provider drops expires_in on the
// refresh response. Without this, both bounds would be zero and the row would
// be persisted with no TTL — see RefreshAndStore.
type upstreamTokenRefresher struct {
	providers            map[string]upstream.OAuth2Provider
	storage              storage.UpstreamTokenStorage
	refreshTokenLifespan time.Duration
}

// Compile-time check that upstreamTokenRefresher implements storage.UpstreamTokenRefresher.
var _ storage.UpstreamTokenRefresher = (*upstreamTokenRefresher)(nil)

// RefreshAndStore refreshes expired upstream tokens using the stored refresh token,
// persists the new tokens, and returns them.
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

	// If the provider didn't rotate the refresh token, keep the original
	if updated.RefreshToken == "" {
		updated.RefreshToken = expired.RefreshToken
	}

	// Store the refreshed tokens
	if err := r.storage.StoreUpstreamTokens(ctx, sessionID, expired.ProviderID, updated); err != nil {
		// Log but still return the refreshed tokens — the current request can
		// proceed even if storage fails. The next request will retry the refresh.
		slog.Warn("failed to store refreshed upstream tokens",
			"session_id", sessionID,
			"error", err,
		)
		return updated, nil
	}

	slog.Debug("upstream tokens refreshed successfully",
		"session_id", sessionID,
		"provider_id", expired.ProviderID,
	)

	return updated, nil
}

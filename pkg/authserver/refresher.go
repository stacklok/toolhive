// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// upstreamTokenRefresher implements storage.UpstreamTokenRefresher by wrapping
// an upstream OAuth2Provider (for token refresh) and UpstreamTokenStorage (for
// persisting the refreshed tokens).
type upstreamTokenRefresher struct {
	provider upstream.OAuth2Provider
	storage  storage.UpstreamTokenStorage
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

	// Refresh tokens via the upstream provider
	newTokens, err := r.provider.RefreshTokens(ctx, expired.RefreshToken, expired.UpstreamSubject)
	if err != nil {
		return nil, fmt.Errorf("upstream token refresh failed: %w", err)
	}

	// Build updated storage tokens preserving binding fields from the original
	updated := &storage.UpstreamTokens{
		ProviderID:      expired.ProviderID,
		AccessToken:     newTokens.AccessToken,
		RefreshToken:    newTokens.RefreshToken,
		IDToken:         newTokens.IDToken,
		ExpiresAt:       newTokens.ExpiresAt,
		UserID:          expired.UserID,
		UpstreamSubject: expired.UpstreamSubject,
		ClientID:        expired.ClientID,
	}

	// If the provider didn't rotate the refresh token, keep the original
	if updated.RefreshToken == "" {
		updated.RefreshToken = expired.RefreshToken
	}

	// Store the refreshed tokens
	if err := r.storage.StoreUpstreamTokens(ctx, sessionID, updated); err != nil {
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

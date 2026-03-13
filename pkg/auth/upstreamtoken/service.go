// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// refreshTimeout bounds how long a singleflight-deduplicated token refresh
// may take before being cancelled. It is deliberately detached from the
// triggering request's context so that waiting callers are not abandoned.
const refreshTimeout = 30 * time.Second

// InProcessService implements the Service interface for in-process use.
// It composes storage (read), refresher (refresh + persist), and singleflight
// (dedup) to provide a single GetValidTokens call.
type InProcessService struct {
	storage   storage.UpstreamTokenStorage
	refresher storage.UpstreamTokenRefresher
	sfGroup   singleflight.Group
}

// Compile-time check.
var _ Service = (*InProcessService)(nil)

// NewInProcessService creates a new InProcessService.
// The refresher may be nil if upstream token refresh is not configured;
// expired tokens will return ErrNoRefreshToken in that case.
func NewInProcessService(
	stor storage.UpstreamTokenStorage,
	refresher storage.UpstreamTokenRefresher,
) *InProcessService {
	return &InProcessService{
		storage:   stor,
		refresher: refresher,
	}
}

// GetValidTokens returns a valid upstream credential for a session.
// It transparently refreshes expired access tokens using the refresh token.
func (s *InProcessService) GetValidTokens(ctx context.Context, sessionID string) (*UpstreamCredential, error) {
	tokens, err := s.storage.GetUpstreamTokens(ctx, sessionID)
	if err != nil {
		// ErrExpired returns tokens (including refresh token) alongside the error.
		// Attempt a refresh before giving up.
		if errors.Is(err, storage.ErrExpired) {
			if tokens != nil {
				return s.refreshOrFail(ctx, sessionID, tokens)
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
		return s.refreshOrFail(ctx, sessionID, tokens)
	}

	return &UpstreamCredential{AccessToken: tokens.AccessToken}, nil
}

// refreshOrFail attempts a singleflight-deduplicated refresh and maps errors
// to the service's sentinel errors.
func (s *InProcessService) refreshOrFail(
	ctx context.Context,
	sessionID string,
	expired *storage.UpstreamTokens,
) (*UpstreamCredential, error) {
	if expired.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}

	if s.refresher == nil {
		slog.Debug("token refresher not configured, cannot refresh upstream tokens")
		return nil, ErrNoRefreshToken
	}

	result, err, _ := s.sfGroup.Do(sessionID, func() (any, error) {
		// Detach from the triggering request's context so that if the first
		// caller disconnects, the refresh still completes for waiting callers.
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		defer cancel()

		refreshed, refreshErr := s.refresher.RefreshAndStore(refreshCtx, sessionID, expired)
		if refreshErr != nil {
			return nil, refreshErr
		}
		return refreshed, nil
	})
	if err != nil {
		slog.Warn("upstream token refresh failed",
			"session_id", sessionID,
			"error", err,
		)
		return nil, fmt.Errorf("%w: %w", ErrRefreshFailed, err)
	}

	refreshed, ok := result.(*storage.UpstreamTokens)
	if !ok || refreshed == nil {
		return nil, ErrRefreshFailed
	}

	return &UpstreamCredential{AccessToken: refreshed.AccessToken}, nil
}

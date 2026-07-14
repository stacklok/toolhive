// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// InProcessService implements the Service interface for in-process use.
// It composes storage (read) and refresher (refresh + persist) to provide a
// single GetValidTokens call. Concurrent-refresh deduplication is delegated to
// the refresher's own singleflight.Group so the same Group covers both the
// handler's chain-walk path and the runtime token-swap path.
type InProcessService struct {
	storage   storage.UpstreamTokenStorage
	refresher storage.UpstreamTokenRefresher
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
		storage:   stor,
		refresher: refresher,
	}
}

// GetValidTokens returns a valid upstream credential for a session and provider.
// It transparently refreshes expired access tokens using the refresh token.
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
// never yielded one. Callers MUST check its exp claim before use.
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

	// TODO(auth): the "check exp" contract on UpstreamCredential.IDToken is
	// documented but not enforced here. Enforcement belongs at the RFC 8693
	// token-exchange consumer when it receives the credential.
	return result, failed, nil
}

// refreshOrFail attempts a refresh via the shared refresher and maps errors to
// the service's sentinel errors. Deduplication of concurrent refreshes for the
// same (session, provider) pair is handled inside the refresher.
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
	return &UpstreamCredential{AccessToken: refreshed.AccessToken, IDToken: idToken}, nil
}

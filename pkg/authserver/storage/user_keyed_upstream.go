// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/identity"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// userRowSessionPrefix turns a user into the session ID the wrapped backend
// sees, so no backend change is needed. In Redis, rows land at
// "upstream:user:{escaped uid}:{provider}" and the per-session index at
// "upstream:idx:user:{escaped uid}"; the memory backend keys the row by the
// same session ID.
const userRowSessionPrefix = "user:"

// errDeleteAllUnsupported is returned by DeleteUpstreamTokens. Deleting by
// session cannot be answered when rows are keyed by user, and silently
// succeeding would let a caller believe a logout removed credentials.
var errDeleteAllUnsupported = fmt.Errorf(
	"storage: DeleteUpstreamTokens is not supported with user-keyed upstream storage: %w", errors.ErrUnsupported)

// UserKeyedUpstreamStorage stores upstream tokens per user instead of per
// login session, so every client of one user shares one row per provider.
//
// The user is read only from the authorization server's own canonical-user
// context key, never from an authenticated Identity: a missing user is
// ErrNoCanonicalUser, never a fallback to another key. The sessionID argument of
// every upstream-token method is ignored.
//
// Every read compares the row's UserID with the context user before returning
// it, and before any expiry is reported, because an expired row is handed back
// together with its refresh token.
//
// GetLatestUpstreamTokensForUser passes through unchanged: it is keyed by its
// userID argument, not by the context, and can return session-keyed rows written
// before the wrapper was enabled. The wrapper adds no check against the context
// user; the backends check the row's UserID against the userID argument.
//
// DeleteUser deletes the user through the wrapped storage, which removes the
// user's upstream rows: the memory backend deletes every row whose UserID
// matches, and Redis deletes the rows listed in the user's reverse-lookup set. The
// wrapper then deletes the per-user index set "upstream:idx:user:{escaped user
// ID}" in Redis, including entries for providers no longer configured. That
// cleanup is best-effort and logged at WARN on failure. Both backends return
// ErrNotFound without removing anything when the user record does not exist, so
// the rows of a synthetic user (which has no user record) are not removed this
// way; they expire by their TTL and keep the session bound.
//
// The wrapper is meant to be applied by the authorization server itself, around
// its own storage. Callers outside the auth server do not set the canonical user,
// and embedders must not wrap the storage they pass to the auth server or runner
// constructors themselves.
//
// Do not wrap storage that already keys rows by user: rows would be keyed twice.
type UserKeyedUpstreamStorage struct {
	Storage
	logger *slog.Logger
}

// NewUserKeyedUpstreamStorage wraps inner so upstream tokens are keyed by user.
// Everything other than the upstream-token methods, including
// ConsumeAssertionJWT and Unwrap, passes through to inner. It returns an error
// when inner is nil.
func NewUserKeyedUpstreamStorage(inner Storage) (*UserKeyedUpstreamStorage, error) {
	if inner == nil {
		return nil, errors.New("user-keyed upstream storage requires a non-nil inner storage")
	}
	return &UserKeyedUpstreamStorage{Storage: inner}, nil
}

// Unwrap returns the wrapped storage.
func (s *UserKeyedUpstreamStorage) Unwrap() Storage { return s.Storage }

// IsUserKeyed reports whether stor is a UserKeyedUpstreamStorage or reaches one
// by following Unwrap() Storage or Unwrap() UpstreamTokenStorage.
func IsUserKeyed(stor UpstreamTokenStorage) bool {
	for stor != nil {
		if _, ok := stor.(*UserKeyedUpstreamStorage); ok {
			return true
		}
		switch u := stor.(type) {
		case interface{ Unwrap() Storage }:
			inner := u.Unwrap()
			if inner == nil {
				return false
			}
			stor = inner
		case interface{ Unwrap() UpstreamTokenStorage }:
			stor = u.Unwrap()
		default:
			return false
		}
	}
	return false
}

// ConsumeAssertionJWT delegates to the wrapped storage, failing closed when it
// does not provide replay consumption rather than skipping a layer via Unwrap.
func (s *UserKeyedUpstreamStorage) ConsumeAssertionJWT(
	ctx context.Context, purpose, issuer, jti string, exp time.Time,
) error {
	consumer, ok := s.Storage.(AssertionJWTConsumer)
	if !ok {
		return fmt.Errorf("wrapped storage %T does not support assertion JWT replay consumption", s.Storage)
	}
	return consumer.ConsumeAssertionJWT(ctx, purpose, issuer, jti, exp)
}

// ResolveUpstreamTokenRowID returns the row ID of the context user's row, so
// two sessions of one user resolve to the same ID.
func (s *UserKeyedUpstreamStorage) ResolveUpstreamTokenRowID(
	ctx context.Context, _, providerName string,
) (UpstreamTokenRowID, error) {
	userID, err := canonicalUser(ctx)
	if err != nil {
		return "", err
	}
	return s.Storage.ResolveUpstreamTokenRowID(ctx, userRowSessionID(userID), providerName)
}

// StoreUpstreamTokens writes the user's row for providerName. It refuses nil
// tokens, tokens with no UserID, and tokens owned by someone other than the
// context user.
func (s *UserKeyedUpstreamStorage) StoreUpstreamTokens(
	ctx context.Context, _, providerName string, tokens *UpstreamTokens,
) error {
	sessionID, stored, err := userRowForWrite(ctx, tokens)
	if err != nil {
		return err
	}
	return s.Storage.StoreUpstreamTokens(ctx, sessionID, providerName, stored)
}

// CompareAndSwapUpstreamTokens is StoreUpstreamTokens with the inner store's
// compare-and-swap contract on the refresh token.
func (s *UserKeyedUpstreamStorage) CompareAndSwapUpstreamTokens(
	ctx context.Context, _, providerName, expectedRefreshToken string, tokens *UpstreamTokens,
) error {
	sessionID, stored, err := userRowForWrite(ctx, tokens)
	if err != nil {
		return err
	}
	return s.Storage.CompareAndSwapUpstreamTokens(ctx, sessionID, providerName, expectedRefreshToken, stored)
}

// GetUpstreamTokens returns the context user's row for providerName. A row
// owned by anyone else, or by nobody, is ErrInvalidBinding and is never returned,
// even when it is also expired.
func (s *UserKeyedUpstreamStorage) GetUpstreamTokens(
	ctx context.Context, _, providerName string,
) (*UpstreamTokens, error) {
	userID, err := canonicalUser(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.Storage.GetUpstreamTokens(ctx, userRowSessionID(userID), providerName)
	if err != nil && !errors.Is(err, ErrExpired) {
		return nil, err
	}
	if row == nil {
		return nil, err
	}
	if row.UserID != userID {
		return nil, ErrInvalidBinding
	}
	return row, err
}

// GetAllUpstreamTokens returns the context user's rows. A row owned by anyone
// else is skipped and logged at ERROR, so the caller treats that provider as not
// connected instead of failing the whole read.
func (s *UserKeyedUpstreamStorage) GetAllUpstreamTokens(
	ctx context.Context, _ string,
) (map[string]*UpstreamTokens, error) {
	userID, err := canonicalUser(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.Storage.GetAllUpstreamTokens(ctx, userRowSessionID(userID))
	if err != nil {
		return nil, err
	}
	owned := make(map[string]*UpstreamTokens, len(all))
	for provider, row := range all {
		if row != nil && row.UserID != userID {
			// The context user is left out: it is the caller's own identity,
			// already in the request logs, and logging it here trips CodeQL's
			// clear-text-logging check.
			s.log().ErrorContext(ctx, "skipping upstream token row owned by another user",
				"got_user_id", row.UserID, "provider", provider)
			continue
		}
		owned[provider] = row
	}
	return owned, nil
}

// DeleteUpstreamTokens always returns an error matching errors.ErrUnsupported:
// deleting by session cannot be answered when rows are keyed by user, and
// silently succeeding would let a caller believe a logout removed credentials.
func (*UserKeyedUpstreamStorage) DeleteUpstreamTokens(context.Context, string) error {
	return errDeleteAllUnsupported
}

// DeleteUpstreamTokensForProvider removes the context user's row for providerName.
func (s *UserKeyedUpstreamStorage) DeleteUpstreamTokensForProvider(
	ctx context.Context, _, providerName string,
) error {
	userID, err := canonicalUser(ctx)
	if err != nil {
		return err
	}
	return s.Storage.DeleteUpstreamTokensForProvider(ctx, userRowSessionID(userID), providerName)
}

// DeleteUser deletes the user through the wrapped storage, then removes what is
// left of the user's upstream rows: the per-user index. That cleanup is
// best-effort; a failure is logged and never fails the call.
func (s *UserKeyedUpstreamStorage) DeleteUser(ctx context.Context, id string) error {
	if err := s.Storage.DeleteUser(ctx, id); err != nil {
		return err
	}
	if err := s.Storage.DeleteUpstreamTokens(ctx, userRowSessionID(id)); err != nil && !errors.Is(err, ErrNotFound) {
		s.log().WarnContext(ctx, "best-effort upstream token index cleanup failed after deleting user", "error", err)
	}
	return nil
}

func (s *UserKeyedUpstreamStorage) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// canonicalUser treats a stored empty string as missing.
func canonicalUser(ctx context.Context) (string, error) {
	userID, ok := identity.CanonicalUserFromContext(ctx)
	if !ok || userID == "" {
		return "", ErrNoCanonicalUser
	}
	return userID, nil
}

// userRowSessionID escapes the user ID so no user ID can address another
// user's key: ':' never appears in the escaped form, so the session ID holds
// exactly one separator, and the escaping is injective.
func userRowSessionID(userID string) string {
	return userRowSessionPrefix + url.QueryEscape(userID)
}

// userRowForWrite validates a write and returns the inner session ID and the
// tokens to persist. The returned tokens are a copy; the caller's are not mutated.
func userRowForWrite(ctx context.Context, tokens *UpstreamTokens) (string, *UpstreamTokens, error) {
	if tokens == nil {
		return "", nil, fosite.ErrInvalidRequest.WithHint("upstream tokens cannot be nil for user-keyed storage")
	}
	ctxUser, err := canonicalUser(ctx)
	if err != nil {
		return "", nil, err
	}
	if tokens.UserID == "" {
		return "", nil, fosite.ErrInvalidRequest.WithHint("upstream tokens must carry a user ID")
	}
	if tokens.UserID != ctxUser {
		return "", nil, ErrInvalidBinding
	}

	stored := cloneUpstreamTokens(tokens)
	// A token that never expires gets no storage lifetime. Dropping the session
	// bound is what removes it, since backends fall back to it when ExpiresAt is
	// zero. A synthetic user keeps the bound: its subject changes on every login,
	// so its rows would otherwise never be reused or removed. The check runs on
	// the internal UserID, which works because the callback uses the synthesized
	// subject directly as the user ID. A real ID that starts with "tk-" only
	// keeps a bounded lifetime, which fails safe.
	if stored.ExpiresAt.IsZero() && !upstream.IsSynthesizedSubject(stored.UserID) {
		stored.SessionExpiresAt = time.Time{}
	}
	return userRowSessionID(stored.UserID), stored, nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// ErrUpstreamCannotCreateUsers is returned by ResolveUser when the identity has no
// link yet and the upstream is not the one allowed to create users (see
// WithCreateOnlyFor).
var ErrUpstreamCannotCreateUsers = errors.New("upstream is not allowed to create users")

// UserResolver handles finding or creating users based on provider identity.
// It manages the mapping between upstream provider subjects and internal user IDs.
type UserResolver struct {
	storage storage.UserStorage
	// createOnlyFor, when set, is the only provider whose identities may create users.
	createOnlyFor string
}

// ResolverOption configures a UserResolver.
type ResolverOption func(*UserResolver)

// WithCreateOnlyFor restricts user and identity-link creation to providerID.
// Looking up an existing link works for every provider, so a later upstream whose
// identity is already linked still resolves to its user. For any other provider
// with no link, ResolveUser returns ErrUpstreamCannotCreateUsers and writes
// nothing. This keeps one person from getting a second user through a later
// upstream. Without the option any provider may create users.
//
// The resolver does not know the order of the login chain; the caller names the
// provider that establishes users.
func WithCreateOnlyFor(providerID string) ResolverOption {
	return func(r *UserResolver) { r.createOnlyFor = providerID }
}

// NewUserResolver creates a new UserResolver with the given storage.
func NewUserResolver(stor storage.UserStorage, opts ...ResolverOption) *UserResolver {
	r := &UserResolver{storage: stor}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// ResolveUser finds an existing user or creates a new one for the provider identity.
// Returns the user whose ID will be the "sub" claim in our JWTs.
//
// The resolution process:
//  1. Look up existing identity by (providerID, providerSubject)
//  2. If found, return the linked user
//  3. If not found, create a new user and link the identity (only for the provider
//     named by WithCreateOnlyFor, when set)
func (r *UserResolver) ResolveUser(
	ctx context.Context,
	providerID string,
	providerSubject string,
) (*storage.User, error) {
	if providerID == "" {
		return nil, errors.New("provider ID cannot be empty")
	}
	if providerSubject == "" {
		return nil, errors.New("provider subject cannot be empty")
	}

	// Try to find existing identity link
	identity, err := r.storage.GetProviderIdentity(ctx, providerID, providerSubject)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("failed to lookup provider identity: %w", err)
		}
		// No existing identity — create new user and link, if this provider may.
		if r.createOnlyFor != "" && providerID != r.createOnlyFor {
			return nil, fmt.Errorf("%w: %q (users are created through %q)",
				ErrUpstreamCannotCreateUsers, providerID, r.createOnlyFor)
		}
		return r.createUserWithIdentity(ctx, providerID, providerSubject)
	}

	// Found existing identity, get the user
	user, err := r.storage.GetUser(ctx, identity.UserID)
	if err != nil {
		return nil, fmt.Errorf("identity exists but user not found: %w", err)
	}
	return user, nil
}

// createUserWithIdentity creates a new user and links the provider identity.
// This is called when no existing identity is found for the provider subject.
func (r *UserResolver) createUserWithIdentity(
	ctx context.Context,
	providerID string,
	providerSubject string,
) (*storage.User, error) {
	now := time.Now()

	user := &storage.User{
		ID:        uuid.New().String(),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := r.storage.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	identity := &storage.ProviderIdentity{
		UserID:          user.ID,
		ProviderID:      providerID,
		ProviderSubject: providerSubject,
		LinkedAt:        now,
		LastUsedAt:      now,
	}

	if err := r.storage.CreateProviderIdentity(ctx, identity); err != nil {
		return r.resolveLinkFailure(ctx, user, providerID, providerSubject, err)
	}

	slog.Debug("created new user with provider identity",
		"user_id", user.ID,
		"provider_id", providerID,
	)

	return user, nil
}

// resolveLinkFailure settles a failed CreateProviderIdentity. The identity link is
// the single source of truth for which user owns an upstream identity, so for every
// failure the link is read before anything is deleted. A failed call does not mean
// the write failed: the write can commit and its reply be lost, and a retried
// command then sees its own earlier write as "already exists".
//
// If the link points at our own candidate, the write committed and the candidate
// is the owner: it must not be rolled back, whatever error came back. If the link
// points at another user, a concurrent first login won and we adopt its user.
// Otherwise nothing is deleted and the original error is returned. No link does
// not prove our write failed: a write whose reply timed out can still commit after
// the read. If the link cannot be read, ownership is unknown. An unlinked
// candidate row is harmless, deleting the owner is not.
func (r *UserResolver) resolveLinkFailure(
	ctx context.Context,
	candidate *storage.User,
	providerID string,
	providerSubject string,
	linkErr error,
) (*storage.User, error) {
	identity, err := r.storage.GetProviderIdentity(ctx, providerID, providerSubject)
	switch {
	case err == nil && identity.UserID == candidate.ID:
		return candidate, nil
	case err == nil:
		r.rollbackUser(ctx, candidate.ID)
		user, getErr := r.storage.GetUser(ctx, identity.UserID)
		if getErr != nil {
			return nil, fmt.Errorf("failed to re-read winner of identity link race: %w", getErr)
		}
		return user, nil
	case errors.Is(err, storage.ErrNotFound):
		return nil, fmt.Errorf("failed to link provider identity: %w", linkErr)
	default:
		return nil, fmt.Errorf("failed to link provider identity: %w (re-reading the link also failed: %w)", linkErr, err)
	}
}

// rollbackUser deletes a user this call created after another user won the
// identity link. If the delete fails, or the request is cancelled first, the
// user row is left without a link, which is harmless: nothing resolves to it.
func (r *UserResolver) rollbackUser(ctx context.Context, userID string) {
	if err := r.storage.DeleteUser(ctx, userID); err != nil {
		slog.Warn("failed to rollback user creation", "error", err)
	}
}

// UpdateLastAuthenticated updates the last authentication timestamp for a provider identity.
// This supports OIDC max_age parameter enforcement by tracking when users last authenticated.
// Errors are logged but not fatal - callers should continue with authorization.
func (r *UserResolver) UpdateLastAuthenticated(
	ctx context.Context,
	providerID string,
	providerSubject string,
) {
	if err := r.storage.UpdateProviderIdentityLastUsed(ctx, providerID, providerSubject, time.Now()); err != nil {
		slog.Warn("failed to update identity last used timestamp", "error", err)
	}
}

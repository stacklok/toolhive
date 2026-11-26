// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// ErrUpstreamCannotCreateUsers is returned by ResolveUser when the identity has no
// link yet and the upstream is not the one allowed to create users (see
// WithCreateOnlyFor).
var ErrUpstreamCannotCreateUsers = errors.New("upstream is not allowed to create users")

type identityBinding struct {
	issuer       string
	subjectClaim string
}

// UserResolver handles finding or creating users based on provider identity.
// It manages the mapping between upstream provider subjects and internal user IDs.
type UserResolver struct {
	storage storage.UserStorage
	swapper storage.ProviderIdentitySwapper
	// createOnlyFor, when set, is the only provider whose identities may create users.
	createOnlyFor string
	// bindings maps providerID -> expected (issuer, subjectClaim) binding.
	// Immutable after construction.
	bindings          map[string]identityBinding
	unstampedWarnOnce sync.Once
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

// WithIdentityBinding binds providerID's identity links to the given non-empty
// issuer and subject claim. New links are stamped with both values, legacy
// unstamped rows are stamped on first match, and rows stamped with a different
// issuer or subject claim are treated as not found (replacing the link with a
// new user when this provider may create users).
//
// Panics if providerID, issuer, or subjectClaim is empty.
func WithIdentityBinding(providerID, issuer, subjectClaim string) ResolverOption {
	if providerID == "" || issuer == "" || subjectClaim == "" {
		panic("handlers.WithIdentityBinding: providerID, issuer, and subjectClaim must be non-empty")
	}
	return func(r *UserResolver) {
		if r.bindings == nil {
			r.bindings = make(map[string]identityBinding)
		}
		r.bindings[providerID] = identityBinding{issuer: issuer, subjectClaim: subjectClaim}
	}
}

// NewUserResolver creates a new UserResolver with the given storage.
func NewUserResolver(stor storage.UserStorage, opts ...ResolverOption) *UserResolver {
	r := &UserResolver{
		storage: stor,
		swapper: unwrapProviderIdentitySwapper(stor),
	}
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
//  2. If found and its binding matches (or no binding is configured), return the linked user
//  3. If found and unstamped (pre-upgrade row), load the linked user, stamp the current
//     binding atomically when supported, and return the user
//  4. If not found, or found with a mismatching binding, create a new user and link/replace
//     the identity (only for the provider named by WithCreateOnlyFor, when set)
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

	binding, hasBinding := r.bindings[providerID]

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
		return r.createUserWithIdentity(ctx, providerID, providerSubject, binding, hasBinding)
	}

	if hasBinding && !identity.MatchesBinding(binding.issuer, binding.subjectClaim) {
		if identity.IsUnstamped() {
			// Decision A1: load the user first (never stamp an orphaned link), then stamp on first match.
			user, getErr := r.storage.GetUser(ctx, identity.UserID)
			if getErr != nil {
				return nil, fmt.Errorf("identity exists but user not found: %w", getErr)
			}
			r.stampUnstampedIdentity(ctx, identity, binding)
			return user, nil
		}

		// Decision B1: mismatching binding counts as not found — create a new user
		// and atomically replace the row, leaving the old user untouched.
		if r.createOnlyFor != "" && providerID != r.createOnlyFor {
			return nil, fmt.Errorf("%w: %q (users are created through %q)",
				ErrUpstreamCannotCreateUsers, providerID, r.createOnlyFor)
		}
		if r.swapper == nil {
			return nil, fmt.Errorf(
				"provider identity for %q was created under a different issuer or subject claim and storage does not support atomic replacement",
				providerID,
			)
		}
		return r.replaceMismatchedIdentity(ctx, identity, binding)
	}

	// Found existing matching identity, get the user
	user, err := r.storage.GetUser(ctx, identity.UserID)
	if err != nil {
		return nil, fmt.Errorf("identity exists but user not found: %w", err)
	}
	return user, nil
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

// matchesIdentityBinding reports whether identity is acceptable for providerID's
// configured binding: true when no binding is configured, when it matches the
// binding, or when it is a legacy unstamped row (decision A1).
func (r *UserResolver) matchesIdentityBinding(providerID string, identity *storage.ProviderIdentity) bool {
	if identity == nil {
		return false
	}
	binding, hasBinding := r.bindings[providerID]
	if !hasBinding {
		return true
	}
	return identity.MatchesBinding(binding.issuer, binding.subjectClaim) || identity.IsUnstamped()
}

// unwrapProviderIdentitySwapper discovers the optional CAS capability on stor.
// When stor is a full storage.Storage, it peels ToolHive's own storage decorators
// via storage.Unwrap; external decorators that intentionally omit Unwrap remain
// unpeeled so we never bypass them.
func unwrapProviderIdentitySwapper(stor storage.UserStorage) storage.ProviderIdentitySwapper {
	if full, ok := stor.(storage.Storage); ok {
		swapper, _ := storage.Unwrap(full).(storage.ProviderIdentitySwapper)
		return swapper
	}
	swapper, _ := stor.(storage.ProviderIdentitySwapper)
	return swapper
}

// stampUnstampedIdentity stamps a pre-upgrade unstamped row with the current
// upstream binding when the backend supports atomic CAS, logging once per resolver.
func (r *UserResolver) stampUnstampedIdentity(
	ctx context.Context,
	existing *storage.ProviderIdentity,
	binding identityBinding,
) {
	if r.swapper == nil {
		r.unstampedWarnOnce.Do(func() {
			slog.Debug("matched unstamped provider identity on storage without swapper; skipping stamp",
				"provider_id", existing.ProviderID,
				"issuer", binding.issuer,
				"subject_claim", binding.subjectClaim,
			)
		})
		return
	}

	stamped := *existing
	stamped.Issuer = binding.issuer
	stamped.SubjectClaim = binding.subjectClaim
	err := r.swapper.CompareAndSwapProviderIdentity(ctx, existing, &stamped)
	switch {
	case err == nil:
		r.unstampedWarnOnce.Do(func() {
			slog.Warn(
				"stamped legacy provider identity without issuer and subject claim; "+
					"existing rows are bound to current upstream configuration on first match",
				"provider_id", existing.ProviderID,
				"issuer", binding.issuer,
				"subject_claim", binding.subjectClaim,
			)
		})
	case errors.Is(err, storage.ErrProviderIdentityChanged):
		slog.Debug("concurrent request already updated unstamped provider identity",
			"provider_id", existing.ProviderID,
		)
	default:
		slog.Warn("failed to stamp legacy provider identity; will retry on next sign-in",
			"provider_id", existing.ProviderID,
			"error", err,
		)
	}
}

// createUserWithIdentity creates a new user and links the provider identity.
// This is called when no existing identity is found for the provider subject.
func (r *UserResolver) createUserWithIdentity(
	ctx context.Context,
	providerID string,
	providerSubject string,
	binding identityBinding,
	hasBinding bool,
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
		Issuer:          binding.issuer,
		SubjectClaim:    binding.subjectClaim,
		LinkedAt:        now,
		LastUsedAt:      now,
	}

	if err := r.storage.CreateProviderIdentity(ctx, identity); err != nil {
		return r.resolveLinkFailure(ctx, user, providerID, providerSubject, binding, hasBinding, err)
	}

	slog.Debug("created new user with provider identity",
		"user_id", user.ID,
		"provider_id", providerID,
	)

	return user, nil
}

// replaceMismatchedIdentity creates a new user and atomically replaces a
// mismatching provider identity row, leaving the previous user untouched.
func (r *UserResolver) replaceMismatchedIdentity(
	ctx context.Context,
	existing *storage.ProviderIdentity,
	binding identityBinding,
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

	replacement := &storage.ProviderIdentity{
		UserID:          user.ID,
		ProviderID:      existing.ProviderID,
		ProviderSubject: existing.ProviderSubject,
		Issuer:          binding.issuer,
		SubjectClaim:    binding.subjectClaim,
		LinkedAt:        now,
		LastUsedAt:      now,
	}

	err := r.swapper.CompareAndSwapProviderIdentity(ctx, existing, replacement)
	if errors.Is(err, storage.ErrNotFound) {
		// The mismatching row may have been removed concurrently; try linking as absent.
		err = r.storage.CreateProviderIdentity(ctx, replacement)
	}
	if err != nil {
		return r.resolveLinkFailure(ctx, user, existing.ProviderID, existing.ProviderSubject, binding, true, err)
	}

	slog.Debug("replaced mismatched provider identity with new user",
		"user_id", user.ID,
		"provider_id", existing.ProviderID,
		"issuer", binding.issuer,
		"subject_claim", binding.subjectClaim,
	)

	return user, nil
}

// resolveLinkFailure settles a failed CreateProviderIdentity or
// CompareAndSwapProviderIdentity. The identity link is the single source of
// truth for which user owns an upstream identity, so for every failure the link
// is read before anything is deleted. A failed call does not mean the write
// failed: the write can commit and its reply be lost, and a retried command then
// sees its own earlier write as "already exists".
//
// If the link points at our own candidate, the write committed and the candidate
// is the owner: it must not be rolled back, whatever error came back. If the link
// points at another user whose binding matches ours (or is unstamped), a
// concurrent login won and we adopt its user. If the link still belongs to a
// mismatching issuer or subject claim, we never adopt it: we roll back our
// candidate only when linkErr proves our write did not commit, and return an
// error. No link or a failed read deletes nothing and returns the error: a write
// whose reply timed out can still commit after the read. An unlinked candidate
// row is harmless; deleting the owner is not.
func (r *UserResolver) resolveLinkFailure(
	ctx context.Context,
	candidate *storage.User,
	providerID string,
	providerSubject string,
	binding identityBinding,
	hasBinding bool,
	linkErr error,
) (*storage.User, error) {
	identity, err := r.storage.GetProviderIdentity(ctx, providerID, providerSubject)
	switch {
	case err == nil && identity.UserID == candidate.ID:
		return candidate, nil
	case err == nil && (!hasBinding || identity.MatchesBinding(binding.issuer, binding.subjectClaim) || identity.IsUnstamped()):
		r.rollbackUser(ctx, candidate.ID)
		user, getErr := r.storage.GetUser(ctx, identity.UserID)
		if getErr != nil {
			return nil, fmt.Errorf("failed to re-read winner of identity link race: %w", getErr)
		}
		if hasBinding && identity.IsUnstamped() {
			r.stampUnstampedIdentity(ctx, identity, binding)
		}
		return user, nil
	case err == nil:
		// Stored row still carries a mismatching binding; never adopt its user.
		// Roll back candidate only when linkErr confirms our write did not commit.
		if errors.Is(linkErr, storage.ErrAlreadyExists) || errors.Is(linkErr, storage.ErrProviderIdentityChanged) {
			r.rollbackUser(ctx, candidate.ID)
		}
		return nil, fmt.Errorf("failed to link provider identity: %w", linkErr)
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

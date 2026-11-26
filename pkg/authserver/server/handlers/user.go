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

// ErrIdentityBindingMismatch is returned by ResolveUser when the identity link
// was created under a different issuer or subject claim than the upstream's
// current ones and the resolver may not replace it: the storage has no
// ProviderIdentitySwapper, the upstream is not the one allowed to create
// users, or a concurrent sign-in bound the link to another issuer or subject
// claim while this one was settling a lost link write.
var ErrIdentityBindingMismatch = errors.New("identity link was created under a different issuer or subject claim")

// ErrRememberedIdentityMismatch is returned by ResolveExpectedUser when the
// provider identity does not resolve to the expected user under the upstream's
// current binding, or has no link at all.
var ErrRememberedIdentityMismatch = errors.New("provider identity does not resolve to the expected user")

var errStampRaceLost = errors.New("provider identity changed again while it was being stamped")

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
	bindings         map[string]identityBinding
	unstampedLogOnce sync.Once
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
// issuer and subject claim. New links are stamped with both values, and rows
// created before binding existed (carrying neither) are stamped on the first
// match, so an upgrade does not give existing users new accounts.
//
// A row stamped with a different issuer or subject claim is treated as not
// found. When this provider may create users and the storage supports atomic
// replacement (storage.ProviderIdentitySwapper), ResolveUser replaces the link
// with a new user. Without a swapper, and for a provider that may not create
// users, ResolveUser refuses with ErrIdentityBindingMismatch instead.
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
		swapper: storage.ProviderIdentitySwapperFor(stor),
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
//  3. If found and unstamped (a row from before binding existed), load the linked user and
//     stamp the current binding atomically when the storage supports it. If the stamp loses
//     to a concurrent write, the row is read again and decided once more; a second loss is
//     an error
//  4. If not found, create a new user and link the identity (only for the provider named by
//     WithCreateOnlyFor, when set)
//  5. If found with a mismatching binding, create a new user and atomically replace the
//     identity (same provider restriction). The old user is left untouched. When the storage
//     has no ProviderIdentitySwapper, or the provider may not create users, it returns
//     ErrIdentityBindingMismatch: adopting the old row's user would hand the old account to
//     a different person.
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
	return r.resolveUser(ctx, providerID, providerSubject, 0)
}

// ResolveExpectedUser looks up the user a remembered approval was granted to. It
// never creates or replaces anything: it reads the provider identity once and
// accepts it only when its binding matches (or the row is unstamped), the row
// belongs to expectedUserID and that user still exists. An unstamped row is
// stamped like ResolveUser does, including the one re-read after a lost stamp.
//
// A missing row, a mismatching binding, a different owner or a missing user
// return ErrRememberedIdentityMismatch. Storage failures are returned as is.
func (r *UserResolver) ResolveExpectedUser(
	ctx context.Context,
	providerID string,
	providerSubject string,
	expectedUserID string,
) (*storage.User, error) {
	if providerID == "" || providerSubject == "" || expectedUserID == "" {
		return nil, errors.New("provider ID, provider subject and expected user ID cannot be empty")
	}
	binding, hasBinding := r.bindings[providerID]
	for attempt := 0; ; attempt++ {
		identity, err := r.storage.GetProviderIdentity(ctx, providerID, providerSubject)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, ErrRememberedIdentityMismatch
			}
			return nil, fmt.Errorf("failed to lookup provider identity: %w", err)
		}
		if identity.UserID != expectedUserID || !identityAcceptable(identity, binding, hasBinding) {
			return nil, ErrRememberedIdentityMismatch
		}
		user, lost, err := r.loadAndStamp(ctx, identity, binding, hasBinding)
		if errors.Is(err, storage.ErrNotFound) {
			return nil, ErrRememberedIdentityMismatch
		}
		if err != nil {
			return nil, err
		}
		if !lost {
			return user, nil
		}
		if attempt > 0 {
			return nil, errStampRaceLost
		}
	}
}

func (r *UserResolver) resolveUser(
	ctx context.Context,
	providerID string,
	providerSubject string,
	attempt int,
) (*storage.User, error) {
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
			// Load the user first so an orphaned link is never stamped.
			user, lost, stampErr := r.loadAndStamp(ctx, identity, binding, hasBinding)
			if stampErr != nil {
				return nil, stampErr
			}
			if !lost {
				return user, nil
			}
			if attempt > 0 {
				return nil, errStampRaceLost
			}
			return r.resolveUser(ctx, providerID, providerSubject, attempt+1)
		}

		// A mismatching binding counts as not found: create a new user and
		// atomically replace the row, leaving the old user untouched.
		if r.createOnlyFor != "" && providerID != r.createOnlyFor {
			return nil, fmt.Errorf("%w: %q", ErrIdentityBindingMismatch, providerID)
		}
		if r.swapper == nil {
			// Refuse instead of adopting the old row's user: adopting would
			// hand the old account to a different person.
			return nil, fmt.Errorf("%w: %q (storage cannot replace the link)", ErrIdentityBindingMismatch, providerID)
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

// identityAcceptable reports whether identity may be used under binding: true
// when no binding is configured, when the binding matches, or when the row is
// unstamped (it was written before binding existed and is stamped on first use).
func identityAcceptable(identity *storage.ProviderIdentity, binding identityBinding, hasBinding bool) bool {
	return !hasBinding || identity.MatchesBinding(binding.issuer, binding.subjectClaim) || identity.IsUnstamped()
}

// loadAndStamp loads the user an acceptable identity points at and, when the
// row is unstamped, stamps the current binding. lost reports that the stamp
// lost to a concurrent write: the caller must read the row again and decide
// once more. The user is loaded first so an orphaned link is never stamped.
func (r *UserResolver) loadAndStamp(
	ctx context.Context,
	identity *storage.ProviderIdentity,
	binding identityBinding,
	hasBinding bool,
) (user *storage.User, lost bool, err error) {
	user, err = r.storage.GetUser(ctx, identity.UserID)
	if err != nil {
		return nil, false, fmt.Errorf("identity exists but user not found: %w", err)
	}
	if hasBinding && identity.IsUnstamped() {
		lost = r.stampUnstampedIdentity(ctx, identity, binding)
	}
	return user, lost, nil
}

// adoptLinkedUser returns the user a link points at when another request's
// link won a race. An unstamped row is stamped; if that loses, the row is read
// again and decided once more, and a second loss is an error. A row that then
// carries a mismatching binding is refused with ErrIdentityBindingMismatch.
func (r *UserResolver) adoptLinkedUser(
	ctx context.Context,
	identity *storage.ProviderIdentity,
	binding identityBinding,
	hasBinding bool,
) (*storage.User, error) {
	for attempt := 0; ; attempt++ {
		if !identityAcceptable(identity, binding, hasBinding) {
			return nil, fmt.Errorf("%w: %q", ErrIdentityBindingMismatch, identity.ProviderID)
		}
		user, lost, err := r.loadAndStamp(ctx, identity, binding, hasBinding)
		if err != nil {
			return nil, err
		}
		if !lost {
			return user, nil
		}
		if attempt > 0 {
			return nil, errStampRaceLost
		}
		identity, err = r.storage.GetProviderIdentity(ctx, identity.ProviderID, identity.ProviderSubject)
		if err != nil {
			return nil, fmt.Errorf("failed to re-read provider identity after a lost stamp: %w", err)
		}
	}
}

// stampUnstampedIdentity stamps a row written before binding existed with the
// current upstream binding when the storage supports atomic compare-and-swap.
// It reports lost when the stamp lost to a concurrent write (the row changed or
// disappeared), in which case the caller reads the row again. Without a
// swapper, and after any other failure, the row stays unstamped and the sign-in
// continues: stamping is best effort.
func (r *UserResolver) stampUnstampedIdentity(
	ctx context.Context,
	existing *storage.ProviderIdentity,
	binding identityBinding,
) (lost bool) {
	if r.swapper == nil {
		slog.Debug("matched unstamped provider identity on storage without swapper; leaving it unstamped",
			"provider_id", existing.ProviderID,
		)
		return false
	}

	stamped := *existing
	stamped.Issuer = binding.issuer
	stamped.SubjectClaim = binding.subjectClaim
	err := r.swapper.CompareAndSwapProviderIdentity(ctx, existing, &stamped)
	switch {
	case err == nil:
		r.unstampedLogOnce.Do(func() {
			slog.Warn(
				"stamped legacy provider identity without issuer and subject claim; "+
					"existing rows are bound to current upstream configuration on first match",
				"provider_id", existing.ProviderID,
				"issuer", binding.issuer,
				"subject_claim", binding.subjectClaim,
			)
		})
		return false
	case errors.Is(err, storage.ErrProviderIdentityChanged), errors.Is(err, storage.ErrNotFound):
		slog.Debug("concurrent request changed unstamped provider identity; reading it again",
			"provider_id", existing.ProviderID,
		)
		return true
	default:
		slog.Warn("failed to stamp provider identity; will retry on next sign-in",
			"provider_id", existing.ProviderID,
			"error", err,
		)
		return false
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

	slog.Warn("replaced mismatched provider identity with new user",
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
// concurrent login won: we roll back our candidate and adopt its user
// (adoptLinkedUser re-reads the row once if stamping it loses a race). If the
// link still belongs to a mismatching issuer or subject claim we never adopt
// it, and if the link is gone there is nothing to adopt. In those two cases we
// roll back our candidate only when linkErr proves our write did not commit
// (ErrAlreadyExists or ErrProviderIdentityChanged), and return an error. A
// failed read deletes nothing and returns the error: a write whose
// reply timed out can still commit after the read. An unlinked candidate row is
// harmless; deleting the owner is not.
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
	case err == nil && identityAcceptable(identity, binding, hasBinding):
		r.rollbackUser(ctx, candidate.ID)
		user, adoptErr := r.adoptLinkedUser(ctx, identity, binding, hasBinding)
		if adoptErr != nil {
			return nil, fmt.Errorf("failed to adopt the winner of the identity link race: %w", adoptErr)
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
		// The row is gone. These two errors prove our own write did not land,
		// so the candidate is unlinked. Any other error leaves that unknown, and
		// the candidate is kept.
		if errors.Is(linkErr, storage.ErrAlreadyExists) || errors.Is(linkErr, storage.ErrProviderIdentityChanged) {
			r.rollbackUser(ctx, candidate.ID)
		}
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

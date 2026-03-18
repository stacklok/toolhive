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

// UserResolver handles finding or creating users based on provider identity.
// It manages the mapping between upstream provider subjects and internal user IDs.
type UserResolver struct {
	storage          storage.UserStorage
	legacyProviderID string
}

// NewUserResolver creates a new UserResolver with the given storage.
// legacyProviderID is the protocol-based provider ID ("oidc" or "oauth2") used before
// multi-upstream support. It scopes legacy migration to only the correct upstream type,
// preventing cross-provider account merge when two providers share a subject value.
func NewUserResolver(stor storage.UserStorage, legacyProviderID string) *UserResolver {
	return &UserResolver{storage: stor, legacyProviderID: legacyProviderID}
}

// ResolveUser finds an existing user or creates a new one for the provider identity.
// Returns the user whose ID will be the "sub" claim in our JWTs.
//
// The resolution process:
// 1. Look up existing identity by (providerID, providerSubject)
// 2. If found, return the linked user
// 3. If not found, create a new user and link the identity
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

	// First, try to find existing identity link
	identity, err := r.storage.GetProviderIdentity(ctx, providerID, providerSubject)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("failed to lookup provider identity: %w", err)
		}
		// Not found under current provider ID — check legacy IDs before creating new user.
		// TODO: Remove legacy migration once all deployments have migrated.
		legacyIdentity, legacyErr := r.findLegacyProviderIdentity(ctx, providerID, providerSubject)
		if legacyErr != nil && !errors.Is(legacyErr, storage.ErrNotFound) {
			return nil, legacyErr
		}
		if legacyErr == nil {
			r.linkMigratedIdentity(ctx, providerID, providerSubject, legacyIdentity.UserID)
			user, userErr := r.storage.GetUser(ctx, legacyIdentity.UserID)
			if userErr != nil {
				return nil, fmt.Errorf("migrated identity but user not found: %w", userErr)
			}
			return user, nil
		}
		// No existing or legacy identity — create new user and link
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
		// Rollback user creation on identity link failure
		if deleteErr := r.storage.DeleteUser(ctx, user.ID); deleteErr != nil {
			slog.Warn("failed to rollback user creation", "error", deleteErr)
		}
		return nil, fmt.Errorf("failed to link provider identity: %w", err)
	}

	slog.Info("created new user with provider identity",
		"user_id", user.ID,
		"provider_id", providerID,
	)

	return user, nil
}

// findLegacyProviderIdentity checks the single legacy protocol-based provider ID
// for an existing identity, enabling transparent migration to logical provider names.
// Only the legacy ID matching this upstream's type is checked, preventing cross-provider
// account merge when two upstreams share a subject value.
func (r *UserResolver) findLegacyProviderIdentity(
	ctx context.Context,
	currentProviderID string,
	providerSubject string,
) (*storage.ProviderIdentity, error) {
	if r.legacyProviderID == "" {
		return nil, storage.ErrNotFound
	}
	if r.legacyProviderID == currentProviderID {
		return nil, storage.ErrNotFound
	}
	identity, err := r.storage.GetProviderIdentity(ctx, r.legacyProviderID, providerSubject)
	if err != nil {
		return nil, err
	}
	slog.Info("found legacy provider identity",
		"legacy_provider_id", r.legacyProviderID,
		"new_provider_id", currentProviderID,
		"user_id", identity.UserID)
	return identity, nil
}

// linkMigratedIdentity creates a new provider identity under the current provider
// ID pointing to the same user, preserving internal user ID continuity.
// Best-effort: errors are logged but do not fail the request.
func (r *UserResolver) linkMigratedIdentity(
	ctx context.Context,
	providerID, providerSubject, userID string,
) {
	identity := &storage.ProviderIdentity{
		UserID:          userID,
		ProviderID:      providerID,
		ProviderSubject: providerSubject,
		LinkedAt:        time.Now(),
		LastUsedAt:      time.Now(),
	}
	if err := r.storage.CreateProviderIdentity(ctx, identity); err != nil {
		if !errors.Is(err, storage.ErrAlreadyExists) {
			slog.Warn("failed to create migrated provider identity",
				"user_id", userID, "provider_id", providerID, "error", err)
		}
	} else {
		slog.Info("migrated provider identity to new provider name",
			"user_id", userID, "provider_id", providerID)
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

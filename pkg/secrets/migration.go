// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// KeyMigration describes a single key rename operation from OldKey to NewKey.
type KeyMigration struct {
	OldKey string
	NewKey string
}

// SystemKeyPrefixMappings maps known bare key prefixes to their target scope.
// Used by DiscoverMigrations to find keys that need migrating.
var SystemKeyPrefixMappings = []struct {
	Prefix string
	Scope  SecretScope
}{
	{"BEARER_TOKEN_", ScopeWorkloads},
	{"OAUTH_CLIENT_SECRET_", ScopeWorkloads},
	{"OAUTH_REFRESH_TOKEN_", ScopeWorkloads},
	{"registry-user-", ScopeWorkloads},
	{"registry-default-", ScopeWorkloads},
	{"BUILD_AUTH_FILE_", ScopeWorkloads},
	{"REGISTRY_OAUTH_", ScopeRegistry},
}

// MigrateSystemKeys renames keys from OldKey to NewKey in provider.
// The migration is idempotent: if the scoped key already exists the bare key
// is deleted without overwriting the scoped value, so a repeated or partial run
// never clobbers data that was already written under the new name.
// Write-before-delete ordering ensures that a crash between the two operations
// leaves the secret reachable under the new key. Keys that do not exist in
// provider are silently skipped, making the function safe to retry.
func MigrateSystemKeys(ctx context.Context, provider Provider, keyMigrations []KeyMigration) error {
	for _, m := range keyMigrations {
		// If the scoped key already exists (e.g. from a partial prior run),
		// skip the write and just clean up the bare key.
		_, err := provider.GetSecret(ctx, m.NewKey)
		if err == nil {
			slog.Debug("migration: scoped key already exists, skipping write",
				"old_key", m.OldKey, "new_key", m.NewKey)
			if delErr := provider.DeleteSecret(ctx, m.OldKey); delErr != nil && !IsNotFoundError(delErr) {
				return fmt.Errorf("migration: deleting stale bare key %q: %w", m.OldKey, delErr)
			}
			continue
		}
		if !IsNotFoundError(err) {
			return fmt.Errorf("migration: checking scoped key %q: %w", m.NewKey, err)
		}

		val, err := provider.GetSecret(ctx, m.OldKey)
		if IsNotFoundError(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("migration: reading %q: %w", m.OldKey, err)
		}

		if err := provider.SetSecret(ctx, m.NewKey, val); err != nil {
			return fmt.Errorf("migration: writing %q: %w", m.NewKey, err)
		}

		if err := provider.DeleteSecret(ctx, m.OldKey); err != nil {
			return fmt.Errorf("migration: deleting %q: %w", m.OldKey, err)
		}
	}
	return nil
}

// DiscoverMigrations lists all secrets in provider and returns the set of
// migrations needed to move system-owned keys into their scoped namespaces.
// Keys that already start with SystemKeyPrefix are skipped (already migrated).
func DiscoverMigrations(ctx context.Context, provider Provider) ([]KeyMigration, error) {
	all, err := provider.ListSecrets(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration discovery: listing secrets: %w", err)
	}

	var keyMigrations []KeyMigration
	for _, desc := range all {
		key := desc.Key
		// Skip already-migrated keys.
		if IsSystemKey(key) {
			continue
		}
		for _, mapping := range SystemKeyPrefixMappings {
			if strings.HasPrefix(key, mapping.Prefix) {
				keyMigrations = append(keyMigrations, KeyMigration{
					OldKey: key,
					NewKey: SystemKeyPrefix + string(mapping.Scope) + "_" + key,
				})
				break
			}
		}
	}
	return keyMigrations, nil
}

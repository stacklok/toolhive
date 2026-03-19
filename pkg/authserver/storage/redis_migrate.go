// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// MigrationResult holds counts of items migrated during bulk migration.
type MigrationResult struct {
	TokensMigrated     int
	TokensSkipped      int
	TokensFailed       int
	IdentitiesMigrated int
	IdentitiesSkipped  int
	IdentitiesFailed   int
}

// isLegacyUpstreamProviderID reports whether id is a legacy protocol-type provider
// ID that may be migrated to a logical provider name. Only tokens stored under a
// legacy protocol-type ID should be claimed by the migration path; tokens that
// already carry a logical name must not be re-labelled.
func isLegacyUpstreamProviderID(id string) bool {
	return id == "" || id == "oidc" || id == "oauth2"
}

// MigrateLegacyUpstreamData performs a one-shot bulk migration of legacy upstream
// token keys and provider identity keys to the new multi-upstream format.
//
// Token key migration: renames "upstream:{sessionID}" keys (legacy, no provider
// suffix) to "upstream:{sessionID}:{providerName}" and patches ProviderID.
//
// Provider identity migration: duplicates identities stored under legacy
// protocol-type IDs (e.g. "oidc", "oauth2") to the new logical provider name.
// Legacy identity keys are NOT deleted to allow safe rollback.
//
// The migration is idempotent and crash-safe: each key is migrated independently,
// and existing new-format keys are never overwritten.
//
// Returns an error if any keys fail to migrate, since the request path no longer
// has inline fallbacks for legacy data.
//
// TODO(migration): Remove once all deployments have upgraded past this version.
func (s *RedisStorage) MigrateLegacyUpstreamData(ctx context.Context, providerName, legacyProviderID string) error {
	result := &MigrationResult{}

	if err := s.migrateUpstreamTokenKeys(ctx, providerName, result); err != nil {
		return fmt.Errorf("token key migration: %w", err)
	}

	if err := s.migrateProviderIdentityKeys(ctx, providerName, legacyProviderID, result); err != nil {
		return fmt.Errorf("provider identity migration: %w", err)
	}

	if result.TokensFailed > 0 || result.IdentitiesFailed > 0 {
		return fmt.Errorf("migration incomplete: %d token(s) and %d identity(ies) failed — "+
			"the request path has no inline fallback for unmigrated legacy data",
			result.TokensFailed, result.IdentitiesFailed)
	}

	if result.TokensMigrated > 0 || result.IdentitiesMigrated > 0 {
		slog.Info("legacy data migration complete",
			"tokens_migrated", result.TokensMigrated,
			"tokens_skipped", result.TokensSkipped,
			"identities_migrated", result.IdentitiesMigrated,
			"identities_skipped", result.IdentitiesSkipped,
		)
	}

	return nil
}

// migrateUpstreamTokenKeys scans for legacy upstream token keys and migrates them
// to the new per-provider key format.
func (s *RedisStorage) migrateUpstreamTokenKeys(ctx context.Context, providerName string, result *MigrationResult) error {
	// Scan for all upstream:* keys under this prefix
	pattern := s.keyPrefix + KeyTypeUpstream + ":*"
	// The upstream key prefix length helps distinguish legacy from new-format keys.
	// Legacy: "{prefix}upstream:{sessionID}" — remainder after prefix+upstream: has NO colon
	// New:    "{prefix}upstream:{sessionID}:{providerName}" — remainder has a colon
	// Index:  "{prefix}upstream:idx:{sessionID}" — starts with "idx:"
	upstreamPrefixLen := len(s.keyPrefix) + len(KeyTypeUpstream) + 1 // +1 for the colon after "upstream"

	var cursor uint64
	for {
		keys, nextCursor, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("SCAN failed: %w", err)
		}

		for _, key := range keys {
			if len(key) <= upstreamPrefixLen {
				continue
			}
			remainder := key[upstreamPrefixLen:]

			// Skip index keys (upstream:idx:...)
			if strings.HasPrefix(remainder, "idx:") {
				continue
			}

			// Distinguish legacy keys (no colon in remainder) from new-format keys (have colon)
			if strings.Contains(remainder, ":") {
				// Already new-format key, skip
				continue
			}

			// This is a legacy key: upstream:{sessionID}
			sessionID := remainder
			if err := s.migrateSingleUpstreamToken(ctx, key, sessionID, providerName, result); err != nil {
				slog.Warn("failed to migrate legacy upstream token",
					"key", key, "session_id", sessionID, "error", err)
				result.TokensFailed++
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return nil
}

// migrateSingleUpstreamToken migrates one legacy upstream token key to the new format.
func (s *RedisStorage) migrateSingleUpstreamToken(
	ctx context.Context,
	legacyKey, sessionID, providerName string,
	result *MigrationResult,
) error {
	// Check if new-format key already exists (idempotent)
	newKey := redisUpstreamKey(s.keyPrefix, sessionID, providerName)
	exists, err := s.client.Exists(ctx, newKey).Result()
	if err != nil {
		return fmt.Errorf("EXISTS check failed for %s: %w", newKey, err)
	}
	if exists > 0 {
		// New key exists — clean up the legacy key so it doesn't re-appear on next startup.
		warnOnCleanupErr(s.client.Del(ctx, legacyKey).Err(), "Del", legacyKey)
		result.TokensSkipped++
		return nil
	}

	// Read the legacy data
	data, err := s.client.Get(ctx, legacyKey).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			result.TokensSkipped++
			return nil
		}
		return fmt.Errorf("GET failed for %s: %w", legacyKey, err)
	}

	// Handle null marker
	if string(data) == nullMarker {
		result.TokensSkipped++
		return nil
	}

	// Deserialize to check ProviderID
	var stored storedUpstreamTokens
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("unmarshal failed for %s: %w", legacyKey, err)
	}

	// Only migrate tokens with legacy protocol-type IDs
	if !isLegacyUpstreamProviderID(stored.ProviderID) {
		slog.Debug("skipping legacy upstream token: has logical provider name",
			"session_id", sessionID, "provider_id", stored.ProviderID)
		result.TokensSkipped++
		return nil
	}

	// Patch ProviderID to the logical provider name
	stored.ProviderID = providerName
	newData, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}

	// Preserve TTL from the legacy key
	ttl, err := s.client.PTTL(ctx, legacyKey).Result()
	if err != nil {
		return fmt.Errorf("PTTL failed for %s: %w", legacyKey, err)
	}
	// PTTL returns -1 for no expiry, -2 for key not found
	if ttl == -2*time.Millisecond {
		result.TokensSkipped++
		return nil
	}

	// Build the session index key
	idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, sessionID)

	// Pipeline: SET new key + SADD to session index + SADD to user reverse index + DEL legacy key.
	// The user:upstream set must be updated so DeleteUser cascade deletion includes migrated tokens.
	pipe := s.client.TxPipeline()
	if ttl > 0 {
		pipe.Set(ctx, newKey, newData, ttl)
	} else {
		pipe.Set(ctx, newKey, newData, 0)
	}
	pipe.SAdd(ctx, idxKey, newKey)
	if ttl > 0 {
		pipe.PExpire(ctx, idxKey, ttl)
	}
	if stored.UserID != "" {
		userUpstreamKey := redisSetKey(s.keyPrefix, KeyTypeUserUpstream, stored.UserID)
		pipe.SAdd(ctx, userUpstreamKey, newKey)
	}
	pipe.Del(ctx, legacyKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("pipeline exec failed: %w", err)
	}

	slog.Debug("migrated legacy upstream token",
		"session_id", sessionID, "provider_name", providerName)
	result.TokensMigrated++
	return nil
}

// migrateProviderIdentityKeys scans for provider identity keys stored under the
// legacy protocol-type ID and duplicates them under the new logical provider name.
func (s *RedisStorage) migrateProviderIdentityKeys(
	ctx context.Context,
	providerName, legacyProviderID string,
	result *MigrationResult,
) error {
	// Skip if legacyProviderID is the same as providerName (nothing to migrate)
	if legacyProviderID == providerName {
		return nil
	}

	// Skip if legacyProviderID is empty (no legacy identity to look up)
	if legacyProviderID == "" {
		return nil
	}

	// Scan for provider identity keys under the legacy ID.
	// Provider key format: "{prefix}provider:{len(providerID)}:{providerID}:{providerSubject}"
	pattern := fmt.Sprintf("%s%s:%d:%s:*", s.keyPrefix, KeyTypeProvider, len(legacyProviderID), legacyProviderID)

	var cursor uint64
	for {
		keys, nextCursor, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("SCAN failed: %w", err)
		}

		for _, key := range keys {
			if err := s.migrateSingleProviderIdentity(ctx, key, providerName, legacyProviderID, result); err != nil {
				slog.Warn("failed to migrate legacy provider identity",
					"key", key, "error", err)
				result.IdentitiesFailed++
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return nil
}

// migrateSingleProviderIdentity duplicates a legacy provider identity under the
// new logical provider name. The legacy key is NOT deleted for safe rollback.
func (s *RedisStorage) migrateSingleProviderIdentity(
	ctx context.Context,
	legacyKey, providerName, legacyProviderID string,
	result *MigrationResult,
) error {
	// Read the legacy identity
	data, err := s.client.Get(ctx, legacyKey).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			result.IdentitiesSkipped++
			return nil
		}
		return fmt.Errorf("GET failed for %s: %w", legacyKey, err)
	}

	var stored storedProviderIdentity
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("unmarshal failed for %s: %w", legacyKey, err)
	}

	// Build the new key under the logical provider name
	newKey := redisProviderKey(s.keyPrefix, providerName, stored.ProviderSubject)

	// Duplicate the identity with the new provider ID, using SetNX for idempotency
	newStored := storedProviderIdentity{
		UserID:          stored.UserID,
		ProviderID:      providerName,
		ProviderSubject: stored.ProviderSubject,
		LinkedAt:        stored.LinkedAt,
		LastUsedAt:      stored.LastUsedAt,
	}

	newData, err := json.Marshal(newStored) //nolint:gosec // G117 - internal Redis storage serialization
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}

	// SetNX: only write if new key does not exist (idempotent)
	created, err := s.client.SetNX(ctx, newKey, newData, 0).Result()
	if err != nil {
		return fmt.Errorf("SetNX failed for %s: %w", newKey, err)
	}

	if !created {
		slog.Debug("skipping legacy provider identity: new key already exists",
			"legacy_provider_id", legacyProviderID,
			"provider_name", providerName,
			"provider_subject", stored.ProviderSubject)
		result.IdentitiesSkipped++
		return nil
	}

	// Update the user's provider set to include the new key
	userProviderSetKey := redisSetKey(s.keyPrefix, KeyTypeUserProviders, stored.UserID)
	warnOnCleanupErr(s.client.SAdd(ctx, userProviderSetKey, newKey).Err(), "SAdd", userProviderSetKey)

	slog.Debug("migrated legacy provider identity",
		"legacy_provider_id", legacyProviderID,
		"provider_name", providerName,
		"provider_subject", stored.ProviderSubject,
		"user_id", stored.UserID)
	result.IdentitiesMigrated++
	return nil
}

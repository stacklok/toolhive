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

	"github.com/stacklok/toolhive/pkg/authserver/tokenenc"
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
//
//nolint:gocyclo // sequential guard clauses over one row; splitting hurts readability
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

	// Legacy keys predate the per-provider key format and therefore predate
	// envelope encryption; their values are always plaintext JSON. If an
	// envelope ever shows up here (e.g. an operator manually renamed a sealed
	// row to a legacy key name), decryption would fail on the AAD mismatch —
	// surface that as a migration failure for the row rather than migrating
	// garbage.
	if !tokenenc.IsLegacyValue(data) {
		return fmt.Errorf("unexpected encrypted value at legacy key %s", legacyKey)
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

	// Seal the rewritten row: migration doubles as a free encryption pass for
	// pre-existing plaintext rows. The AAD is the NEW key the value lands under.
	if s.tokenEnc != nil {
		newData, err = tokenenc.Seal(s.tokenEnc, newKey, newData)
		if err != nil {
			return fmt.Errorf("failed to encrypt migrated upstream token: %w", err)
		}
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

// MigrateUpstreamTokenEncryption performs a one-shot sweep that converges
// upstream token rows onto the current encryption posture: plaintext rows are
// sealed, and envelopes sealed under a retired key ID are re-sealed with the
// active key. It is optional — reads already lazy-re-encrypt stale envelopes
// and writes always seal — and intended for operators who want immediate
// convergence after enabling encryption or rotating keys.
//
// It is a no-op when encryption is not configured (keyring nil): plaintext
// rows are the desired end state in that posture. Tombstones and rows that
// fail to decrypt (unknown key ID, corruption) are skipped with a WARN and do
// not fail the sweep. The sweep is idempotent and safe to re-run; it mirrors
// the SCAN pattern of MigrateLegacyUpstreamData.
func (s *RedisStorage) MigrateUpstreamTokenEncryption(ctx context.Context) error {
	if s.tokenEnc == nil {
		return nil
	}

	pattern := s.keyPrefix + KeyTypeUpstream + ":*"
	upstreamPrefixLen := len(s.keyPrefix) + len(KeyTypeUpstream) + 1

	var cursor uint64
	var resealed, skipped, failed int
	for {
		keys, nextCursor, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("token encryption sweep: SCAN failed: %w", err)
		}

		for _, key := range keys {
			if len(key) <= upstreamPrefixLen {
				continue
			}
			// Skip index sets (upstream:idx:...) — only token rows hold values.
			if strings.HasPrefix(key[upstreamPrefixLen:], "idx:") {
				continue
			}

			done, err := s.resealUpstreamRow(ctx, key)
			switch {
			case err != nil:
				slog.Warn("token encryption sweep: failed to re-seal row", "key", key, "error", err)
				failed++
			case done:
				resealed++
			default:
				skipped++
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	if failed > 0 {
		return fmt.Errorf("token encryption sweep: %d row(s) failed to re-seal", failed)
	}
	if resealed > 0 {
		slog.Info("token encryption sweep complete", "resealed", resealed, "skipped", skipped)
	}
	return nil
}

// resealUpstreamRow re-seals one upstream token row when it is plaintext or
// sealed under a retired key ID. It reports whether the row was rewritten.
// Tombstones, missing keys, and already-current envelopes are no-ops.
func (s *RedisStorage) resealUpstreamRow(ctx context.Context, key string) (bool, error) {
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, fmt.Errorf("GET failed for %s: %w", key, err)
	}
	if string(data) == nullMarker {
		return false, nil
	}
	if !tokenenc.IsLegacyValue(data) && !tokenenc.NeedsRotation(s.tokenEnc, data) {
		return false, nil // already sealed under the active key
	}

	plaintext, _, err := tokenenc.Open(s.tokenEnc, key, data)
	if err != nil {
		return false, fmt.Errorf("decrypt failed for %s: %w", key, err)
	}

	sealed, err := tokenenc.Seal(s.tokenEnc, key, plaintext)
	if err != nil {
		return false, fmt.Errorf("seal failed for %s: %w", key, err)
	}

	// Preserve the row's TTL. PTTL returns -1ns (no expiry) or -2ns (vanished)
	// — go-redis passes the sentinel integers through as durations.
	pttl, err := s.client.PTTL(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("PTTL failed for %s: %w", key, err)
	}
	switch {
	case pttl > 0:
		err = s.client.Set(ctx, key, sealed, pttl).Err()
	case pttl == -1:
		err = s.client.Set(ctx, key, sealed, 0).Err()
	default:
		return false, nil // vanished between GET and re-seal
	}
	if err != nil {
		return false, fmt.Errorf("SET failed for %s: %w", key, err)
	}
	return true, nil
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

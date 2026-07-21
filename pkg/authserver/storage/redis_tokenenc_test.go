// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Tests for envelope encryption of upstream tokens at rest
// (WithTokenEncryption). Security assertions here are real: raw Redis values
// are inspected for plaintext leakage, AAD binding, rotation convergence, and
// fail-loud behavior with encryption disabled.

//nolint:paralleltest // parallel execution handled by withRedisStorage helper
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/tokenenc"
)

// encTestKey returns a deterministic 32-byte KEK for tests.
func encTestKey(seed byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

func newEncTestKeyring(t *testing.T, activeID string, keys map[string][]byte) tokenenc.Keyring {
	t.Helper()
	kr, err := tokenenc.NewStaticKeyring(activeID, keys)
	require.NoError(t, err)
	return kr
}

// newEncryptedTestRedisStorage returns miniredis-backed storage with envelope
// encryption enabled under a single key "k1", plus the raw client for direct
// value inspection.
func newEncryptedTestRedisStorage(t *testing.T) (*RedisStorage, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	kr := newEncTestKeyring(t, "k1", map[string][]byte{"k1": encTestKey(1)})
	s := NewRedisStorageWithClient(client, "test:auth:", WithTokenEncryption(kr))
	return s, mr, client
}

func withEncryptedRedisStorage(t *testing.T, fn func(context.Context, *RedisStorage, *miniredis.Miniredis, *redis.Client)) {
	t.Helper()
	t.Parallel()
	s, mr, client := newEncryptedTestRedisStorage(t)
	t.Cleanup(func() {
		_ = s.Close()
		mr.Close()
	})
	fn(context.Background(), s, mr, client)
}

// encTestTokens builds a token fixture with recognizable secret material.
func encTestTokens() *UpstreamTokens {
	return &UpstreamTokens{
		ProviderID:      "provider-a",
		AccessToken:     "enc-access-token-SECRET",
		RefreshToken:    "enc-refresh-token-SECRET",
		IDToken:         "enc-id-token-SECRET",
		ExpiresAt:       time.Now().Add(time.Hour),
		UserID:          "user-enc",
		UpstreamSubject: "upstream-subject-enc",
		ClientID:        "client-enc",
	}
}

// rawUpstreamValue fetches the raw stored value for direct inspection.
func rawUpstreamValue(t *testing.T, ctx context.Context, client *redis.Client, key string) string { //nolint:revive // ctx after t is the test-helper convention used across this file
	t.Helper()
	raw, err := client.Get(ctx, key).Result()
	require.NoError(t, err)
	return raw
}

func TestRedisStorage_TokenEncryption(t *testing.T) {
	t.Parallel()

	// Matrix item 9: stored value is an envelope containing no token plaintext.
	t.Run("raw value is envelope with no token substrings", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
			tokens := encTestTokens()
			require.NoError(t, s.StoreUpstreamTokens(ctx, "enc-session", "provider-a", tokens))

			key := redisUpstreamKey(s.keyPrefix, "enc-session", "provider-a")
			raw := rawUpstreamValue(t, ctx, client, key)

			assert.True(t, json.Valid([]byte(raw)))
			assert.Contains(t, raw, `"v":1`)
			assert.Contains(t, raw, `"kid":"k1"`)
			assert.Contains(t, raw, `"edek"`)
			assert.Contains(t, raw, `"ct"`)
			assert.NotContains(t, raw, tokens.AccessToken)
			assert.NotContains(t, raw, tokens.RefreshToken)
			assert.NotContains(t, raw, tokens.IDToken)
			assert.NotContains(t, raw, "access_token", "field names must not leak either")
			assert.NotContains(t, raw, "refresh_token")
		})
	})

	// Matrix item 10: full round-trip through every read path.
	t.Run("round-trip via all read paths", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, _ *redis.Client) {
			tokens := encTestTokens()
			require.NoError(t, s.StoreUpstreamTokens(ctx, "enc-session", "provider-a", tokens))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "enc-session", "provider-b", &UpstreamTokens{
				ProviderID:   "provider-b",
				AccessToken:  "enc-access-b-SECRET",
				RefreshToken: "enc-refresh-b-SECRET",
				ExpiresAt:    time.Now().Add(time.Hour),
				UserID:       "user-enc",
			}))

			got, err := s.GetUpstreamTokens(ctx, "enc-session", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, tokens.AccessToken, got.AccessToken)
			assert.Equal(t, tokens.RefreshToken, got.RefreshToken)
			assert.Equal(t, tokens.IDToken, got.IDToken)
			assert.Equal(t, tokens.UserID, got.UserID)
			assert.Equal(t, tokens.UpstreamSubject, got.UpstreamSubject)
			assert.Equal(t, tokens.ClientID, got.ClientID)

			all, err := s.GetAllUpstreamTokens(ctx, "enc-session", nil)
			require.NoError(t, err)
			require.Len(t, all, 2)
			assert.Equal(t, tokens.AccessToken, all["provider-a"].AccessToken)
			assert.Equal(t, "enc-access-b-SECRET", all["provider-b"].AccessToken)

			latest, err := s.GetLatestUpstreamTokensForUser(ctx, "user-enc", "provider-a")
			require.NoError(t, err)
			assert.Equal(t, tokens.AccessToken, latest.AccessToken)
			assert.Equal(t, tokens.RefreshToken, latest.RefreshToken)
		})
	})

	// Matrix item 10 (binding interplay): decrypt → unmarshal → binding check.
	t.Run("binding validation applies to encrypted rows", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, _ *redis.Client) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "enc-session", "provider-a", encTestTokens()))

			_, err := s.GetUpstreamTokens(ctx, "enc-session", "provider-a",
				&ExpectedBinding{UserID: "different-user"})
			assert.ErrorIs(t, err, ErrInvalidBinding)

			got, err := s.GetUpstreamTokens(ctx, "enc-session", "provider-a",
				&ExpectedBinding{UserID: "user-enc", ClientID: "client-enc"})
			require.NoError(t, err)
			assert.Equal(t, "enc-access-token-SECRET", got.AccessToken)

			// Mismatched rows are excluded from bulk reads, not fatal.
			all, err := s.GetAllUpstreamTokens(ctx, "enc-session",
				&ExpectedBinding{UserID: "different-user"})
			require.NoError(t, err)
			assert.Empty(t, all)
		})
	})

	// Matrix item 11: legacy plaintext rows remain readable with encryption on.
	t.Run("legacy plaintext row readable with encryption on", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
			// Inject a plaintext row directly, bypassing the sealing write path.
			key := redisUpstreamKey(s.keyPrefix, "legacy-session", "provider-a")
			legacy := `{"provider_id":"provider-a","access_token":"legacy-access-SECRET",` +
				`"refresh_token":"legacy-refresh-SECRET","id_token":"","expires_at":0,` +
				`"session_expires_at":0,"user_id":"legacy-user","upstream_subject":"","client_id":""}`
			require.NoError(t, client.Set(ctx, key, legacy, 0).Err())

			got, err := s.GetUpstreamTokens(ctx, "legacy-session", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, "legacy-access-SECRET", got.AccessToken)
			assert.Equal(t, "legacy-refresh-SECRET", got.RefreshToken)
			assert.Equal(t, "legacy-user", got.UserID)

			// The next write seals the row.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "legacy-session", "provider-a", &UpstreamTokens{
				ProviderID:   "provider-a",
				AccessToken:  "rewritten-SECRET",
				RefreshToken: "rewritten-refresh-SECRET",
				ExpiresAt:    time.Now().Add(time.Hour),
				UserID:       "legacy-user",
			}))
			raw := rawUpstreamValue(t, ctx, client, key)
			assert.Contains(t, raw, `"kid":"k1"`)
			assert.NotContains(t, raw, "rewritten-SECRET")
		})
	})

	// Matrix item 12: lazy re-encryption on read after rotation.
	t.Run("rotation: read of stale kid row re-seals with active key", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis, client *redis.Client) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "rot-session", "provider-a", encTestTokens()))
			key := redisUpstreamKey(s.keyPrefix, "rot-session", "provider-a")
			assert.Contains(t, rawUpstreamValue(t, ctx, client, key), `"kid":"k1"`)

			// Rotate: k2 active, k1 retired.
			kr2 := newEncTestKeyring(t, "k2", map[string][]byte{"k1": encTestKey(1), "k2": encTestKey(2)})
			rotated := NewRedisStorageWithClient(client, "test:auth:", WithTokenEncryption(kr2))

			got, err := rotated.GetUpstreamTokens(ctx, "rot-session", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, "enc-access-token-SECRET", got.AccessToken)

			// The row was lazily re-sealed under k2, TTL preserved.
			raw := rawUpstreamValue(t, ctx, client, key)
			assert.Contains(t, raw, `"kid":"k2"`)
			assert.NotContains(t, raw, "enc-access-token-SECRET")
			assert.Greater(t, mr.TTL(key), time.Duration(0), "lazy re-seal must preserve the row TTL")

			// A keyring that only knows k2 can now read the row.
			kr2only := newEncTestKeyring(t, "k2", map[string][]byte{"k2": encTestKey(2)})
			converged := NewRedisStorageWithClient(client, "test:auth:", WithTokenEncryption(kr2only))
			got, err = converged.GetUpstreamTokens(ctx, "rot-session", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, "enc-refresh-token-SECRET", got.RefreshToken)
		})
	})

	// Matrix item 12 (bulk path): lazy re-encryption also fires on bulk and user reads.
	t.Run("rotation converges via bulk and user reads", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "rot-bulk", "provider-a", encTestTokens()))
			key := redisUpstreamKey(s.keyPrefix, "rot-bulk", "provider-a")

			kr2 := newEncTestKeyring(t, "k2", map[string][]byte{"k1": encTestKey(1), "k2": encTestKey(2)})
			rotated := NewRedisStorageWithClient(client, "test:auth:", WithTokenEncryption(kr2))

			all, err := rotated.GetAllUpstreamTokens(ctx, "rot-bulk", nil)
			require.NoError(t, err)
			require.Len(t, all, 1)
			assert.Contains(t, rawUpstreamValue(t, ctx, client, key), `"kid":"k2"`)
		})
	})

	// Matrix item 13: encryption disabled + plaintext fleet = bit-for-bit current behavior.
	t.Run("encryption disabled stores and reads plaintext", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			tokens := encTestTokens()
			require.NoError(t, s.StoreUpstreamTokens(ctx, "plain-session", "provider-a", tokens))

			key := redisUpstreamKey(s.keyPrefix, "plain-session", "provider-a")
			raw, err := mr.Get(key)
			require.NoError(t, err)
			assert.Contains(t, raw, tokens.AccessToken, "disabled encryption must keep plaintext behavior")
			assert.NotContains(t, raw, `"edek"`)

			got, err := s.GetUpstreamTokens(ctx, "plain-session", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, tokens.AccessToken, got.AccessToken)
		})
	})

	// Matrix item 14: encryption disabled + envelope value = hard error.
	t.Run("encryption disabled with envelope value fails loudly", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "enc-session", "provider-a", encTestTokens()))

			// Same fleet, storage without a keyring.
			plain := NewRedisStorageWithClient(client, "test:auth:")

			_, err := plain.GetUpstreamTokens(ctx, "enc-session", "provider-a", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no keyring configured")

			// Bulk and user-index reads skip the row rather than returning garbage.
			all, err := plain.GetAllUpstreamTokens(ctx, "enc-session", nil)
			require.NoError(t, err)
			assert.Empty(t, all)

			_, err = plain.GetLatestUpstreamTokensForUser(ctx, "user-enc", "provider-a")
			requireRedisNotFoundError(t, err)
		})
	})

	// Matrix item 15: reverse-index cleanup extracts UserID from encrypted rows.
	t.Run("deletes clean reverse index from encrypted rows", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "del-session", "provider-a", encTestTokens()))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "del-session", "provider-b", &UpstreamTokens{
				ProviderID:   "provider-b",
				AccessToken:  "enc-b-SECRET",
				RefreshToken: "enc-b-refresh-SECRET",
				ExpiresAt:    time.Now().Add(time.Hour),
				UserID:       "user-enc",
			}))

			// DeleteUpstreamTokensForProvider cleans the user reverse index.
			require.NoError(t, s.DeleteUpstreamTokensForProvider(ctx, "del-session", "provider-b"))
			_, err := s.GetLatestUpstreamTokensForUser(ctx, "user-enc", "provider-b")
			requireRedisNotFoundError(t, err)

			// DeleteUpstreamTokens (whole session) cleans it too.
			require.NoError(t, s.DeleteUpstreamTokens(ctx, "del-session"))
			_, err = s.GetLatestUpstreamTokensForUser(ctx, "user-enc", "provider-a")
			requireRedisNotFoundError(t, err)

			userSetKey := redisSetKey(s.keyPrefix, KeyTypeUserUpstream, "user-enc")
			members, err := client.SMembers(ctx, userSetKey).Result()
			require.NoError(t, err)
			assert.Empty(t, members)
		})
	})

	// Matrix item 16: corrupt ciphertext in a bulk read skips the row, keeps siblings.
	t.Run("corrupt ciphertext skipped in bulk read", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "corrupt-session", "provider-a", encTestTokens()))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "corrupt-session", "provider-b", &UpstreamTokens{
				ProviderID:   "provider-b",
				AccessToken:  "enc-b-SECRET",
				RefreshToken: "enc-b-refresh-SECRET",
				ExpiresAt:    time.Now().Add(time.Hour),
				UserID:       "user-enc",
			}))

			// Corrupt provider-a's envelope value in place.
			badKey := redisUpstreamKey(s.keyPrefix, "corrupt-session", "provider-a")
			raw := rawUpstreamValue(t, ctx, client, badKey)
			corrupted := strings.Replace(raw, `"ct":"`, `"ct":"AAAA`, 1)
			require.NotEqual(t, raw, corrupted)
			require.NoError(t, client.Set(ctx, badKey, corrupted, 0).Err())

			all, err := s.GetAllUpstreamTokens(ctx, "corrupt-session", nil)
			require.NoError(t, err)
			require.Len(t, all, 1, "corrupt row skipped, sibling returned")
			assert.Equal(t, "enc-b-SECRET", all["provider-b"].AccessToken)

			// Point read of the corrupt row errors.
			_, err = s.GetUpstreamTokens(ctx, "corrupt-session", "provider-a", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "failed to decrypt upstream tokens")

			// User-index read skips the corrupt row, finds the sibling.
			latest, err := s.GetLatestUpstreamTokensForUser(ctx, "user-enc", "provider-b")
			require.NoError(t, err)
			assert.Equal(t, "enc-b-SECRET", latest.AccessToken)
		})
	})

	// Matrix items 4 + 16 at storage level: AAD binds ciphertext to its key.
	t.Run("row copied to another key fails decryption", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "victim-session", "provider-a", encTestTokens()))
			srcKey := redisUpstreamKey(s.keyPrefix, "victim-session", "provider-a")
			raw := rawUpstreamValue(t, ctx, client, srcKey)

			// Attacker with Redis write access copies the row to their own key.
			attackerKey := redisUpstreamKey(s.keyPrefix, "attacker-session", "provider-a")
			require.NoError(t, client.Set(ctx, attackerKey, raw, 0).Err())

			_, err := s.GetUpstreamTokens(ctx, "attacker-session", "provider-a", nil)
			require.Error(t, err, "AAD binding must defeat cut-and-paste row copies")
			assert.Contains(t, err.Error(), "failed to decrypt upstream tokens")
		})
	})

	// Matrix items 6 + 17: tombstones stay verbatim "null" with encryption on.
	t.Run("tombstone written verbatim and read as nil with encryption on", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "tomb-session", "provider-a", nil))

			key := redisUpstreamKey(s.keyPrefix, "tomb-session", "provider-a")
			raw := rawUpstreamValue(t, ctx, client, key)
			assert.Equal(t, nullMarker, raw, "tombstone must be written verbatim, never sealed")

			got, err := s.GetUpstreamTokens(ctx, "tomb-session", "provider-a", nil)
			require.NoError(t, err)
			assert.Nil(t, got)

			// Tombstone in a bulk read yields a nil-tokens entry and no error,
			// matching the plaintext-fleet behavior.
			all, err := s.GetAllUpstreamTokens(ctx, "tomb-session", nil)
			require.NoError(t, err)
			require.Contains(t, all, "provider-a")
			assert.Nil(t, all["provider-a"])
		})
	})

	// Matrix item 18: legacy key migration re-seals plaintext rows.
	t.Run("legacy migration re-seals plaintext rows", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
			// Plant a legacy-format key (no provider suffix) with a plaintext value.
			legacyKey := redisKey(s.keyPrefix, KeyTypeUpstream, "migrate-session")
			legacy := `{"provider_id":"oidc","access_token":"migrate-access-SECRET",` +
				`"refresh_token":"migrate-refresh-SECRET","id_token":"","expires_at":0,` +
				`"session_expires_at":0,"user_id":"migrate-user","upstream_subject":"","client_id":""}`
			require.NoError(t, client.Set(ctx, legacyKey, legacy, 0).Err())

			require.NoError(t, s.MigrateLegacyUpstreamData(ctx, "provider-a", "oidc"))

			newKey := redisUpstreamKey(s.keyPrefix, "migrate-session", "provider-a")
			raw := rawUpstreamValue(t, ctx, client, newKey)
			assert.Contains(t, raw, `"kid":"k1"`, "migrated row must be sealed")
			assert.NotContains(t, raw, "migrate-access-SECRET")
			assert.NotContains(t, raw, "migrate-refresh-SECRET")

			// The migrated row reads back through the decrypting path.
			got, err := s.GetUpstreamTokens(ctx, "migrate-session", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, "migrate-access-SECRET", got.AccessToken)
			assert.Equal(t, "migrate-refresh-SECRET", got.RefreshToken)
			assert.Equal(t, "provider-a", got.ProviderID)
			assert.Equal(t, "migrate-user", got.UserID)
		})
	})

	// The rewrite path (old-user reverse-index cleanup) works across encrypted rows.
	t.Run("rewrite from user1 to user2 cleans old reverse index", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, _ *redis.Client) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "rewrite-session", "provider-a", &UpstreamTokens{
				ProviderID:   "provider-a",
				AccessToken:  "first-SECRET",
				RefreshToken: "first-refresh-SECRET",
				ExpiresAt:    time.Now().Add(time.Hour),
				UserID:       "user-old",
			}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "rewrite-session", "provider-a", &UpstreamTokens{
				ProviderID:   "provider-a",
				AccessToken:  "second-SECRET",
				RefreshToken: "second-refresh-SECRET",
				ExpiresAt:    time.Now().Add(time.Hour),
				UserID:       "user-new",
			}))

			// The old user's reverse index no longer references the row.
			_, err := s.GetLatestUpstreamTokensForUser(ctx, "user-old", "provider-a")
			requireRedisNotFoundError(t, err)

			latest, err := s.GetLatestUpstreamTokensForUser(ctx, "user-new", "provider-a")
			require.NoError(t, err)
			assert.Equal(t, "second-SECRET", latest.AccessToken)
		})
	})
}

// TestRedisStorage_MigrateUpstreamTokenEncryption covers the optional
// operator sweep that converges plaintext and stale-kid rows immediately.
func TestRedisStorage_MigrateUpstreamTokenEncryption(t *testing.T) {
	t.Parallel()

	t.Run("sweep seals plaintext and re-seals stale kid rows", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
			// One encrypted row (k1) and one plaintext row.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sweep-a", "provider-a", encTestTokens()))
			plainKey := redisUpstreamKey(s.keyPrefix, "sweep-b", "provider-a")
			legacy := `{"provider_id":"provider-a","access_token":"sweep-plain-SECRET",` +
				`"refresh_token":"sweep-plain-refresh-SECRET","id_token":"","expires_at":0,` +
				`"session_expires_at":0,"user_id":"sweep-user","upstream_subject":"","client_id":""}`
			require.NoError(t, client.Set(ctx, plainKey, legacy, 0).Err())

			// Rotate to k2, then sweep.
			kr2 := newEncTestKeyring(t, "k2", map[string][]byte{"k1": encTestKey(1), "k2": encTestKey(2)})
			rotated := NewRedisStorageWithClient(client, "test:auth:", WithTokenEncryption(kr2))
			require.NoError(t, rotated.MigrateUpstreamTokenEncryption(ctx))

			for _, key := range []string{
				redisUpstreamKey(s.keyPrefix, "sweep-a", "provider-a"),
				plainKey,
			} {
				raw := rawUpstreamValue(t, ctx, client, key)
				assert.Contains(t, raw, `"kid":"k2"`, "key %s must converge to k2", key)
			}
			raw := rawUpstreamValue(t, ctx, client, plainKey)
			assert.NotContains(t, raw, "sweep-plain-SECRET")

			// Both rows read back correctly.
			got, err := rotated.GetUpstreamTokens(ctx, "sweep-a", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, "enc-access-token-SECRET", got.AccessToken)
			got, err = rotated.GetUpstreamTokens(ctx, "sweep-b", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, "sweep-plain-SECRET", got.AccessToken)

			// Sweep is idempotent.
			require.NoError(t, rotated.MigrateUpstreamTokenEncryption(ctx))
		})
	})

	t.Run("sweep is a no-op without a keyring", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "noop", "provider-a", encTestTokens()))
			require.NoError(t, s.MigrateUpstreamTokenEncryption(ctx))

			key := redisUpstreamKey(s.keyPrefix, "noop", "provider-a")
			require.NoError(t, s.MigrateUpstreamTokenEncryption(ctx))
			raw, err := s.client.Get(ctx, key).Result()
			require.NoError(t, err)
			assert.Contains(t, raw, "enc-access-token-SECRET", "plaintext fleet untouched")
		})
	})

	t.Run("sweep skips tombstones and unknown-kid rows without failing", func(t *testing.T) {
		withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sweep-tomb", "provider-a", nil))

			// Row sealed under a key the sweep's keyring does not know.
			foreignKR := newEncTestKeyring(t, "kz", map[string][]byte{"kz": encTestKey(9)})
			foreign := NewRedisStorageWithClient(client, "test:auth:", WithTokenEncryption(foreignKR))
			require.NoError(t, foreign.StoreUpstreamTokens(ctx, "sweep-foreign", "provider-a", encTestTokens()))

			err := s.MigrateUpstreamTokenEncryption(ctx)
			require.Error(t, err, "undecryptable rows are reported")

			// Tombstone survived verbatim.
			raw := rawUpstreamValue(t, ctx, client, redisUpstreamKey(s.keyPrefix, "sweep-tomb", "provider-a"))
			assert.Equal(t, nullMarker, raw)
		})
	})
}

// TestRedisStorage_TokenEncryption_UserIDCleanupWarns documents that a row
// whose UserID cannot be extracted (undecryptable under the current keyring)
// still deletes cleanly — the reverse-index cleanup is skipped, not fatal.
func TestRedisStorage_TokenEncryption_UserIDCleanupWarns(t *testing.T) {
	withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
		require.NoError(t, s.StoreUpstreamTokens(ctx, "warn-session", "provider-a", encTestTokens()))

		// Storage that cannot decrypt the row (different key).
		otherKR := newEncTestKeyring(t, "kx", map[string][]byte{"kx": encTestKey(8)})
		other := NewRedisStorageWithClient(client, "test:auth:", WithTokenEncryption(otherKR))

		require.NoError(t, other.DeleteUpstreamTokensForProvider(ctx, "warn-session", "provider-a"),
			"delete must succeed even when UserID extraction fails")

		_, err := s.GetUpstreamTokens(ctx, "warn-session", "provider-a", nil)
		requireRedisNotFoundError(t, err)
	})
}

// TestRedisStorage_TokenEncryption_IndexTTLInvariant pins that the extra
// read in the encrypted write path does not disturb the Lua script's index
// TTL bookkeeping.
func TestRedisStorage_TokenEncryption_IndexTTLInvariant(t *testing.T) {
	withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis, _ *redis.Client) {
		require.NoError(t, s.StoreUpstreamTokens(ctx, "ttl-session", "provider-expiring", &UpstreamTokens{
			ProviderID:   "provider-expiring",
			AccessToken:  "ttl-expiring-SECRET",
			RefreshToken: "ttl-refresh-SECRET",
			ExpiresAt:    time.Now().Add(time.Hour),
		}))
		require.NoError(t, s.StoreUpstreamTokens(ctx, "ttl-session", "provider-nonexpiring", &UpstreamTokens{
			ProviderID:  "provider-nonexpiring",
			AccessToken: "ttl-nonexpiring-SECRET",
		}))

		idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, "ttl-session")
		assert.Equal(t, time.Duration(0), mr.TTL(idxKey),
			"index set must go persistent when a non-expiring member is added")

		memberKey := redisUpstreamKey(s.keyPrefix, "ttl-session", "provider-expiring")
		assert.Greater(t, mr.TTL(memberKey), time.Duration(0))
	})
}

// TestRedisStorage_TokenEncryption_ExpiredTokens checks ErrExpired still
// surfaces with decrypted rows (refresh path must keep working).
func TestRedisStorage_TokenEncryption_ExpiredTokens(t *testing.T) {
	withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, _ *redis.Client) {
		require.NoError(t, s.StoreUpstreamTokens(ctx, "expired-enc", "provider-a", &UpstreamTokens{
			ProviderID:   "provider-a",
			AccessToken:  "expired-enc-access-SECRET",
			RefreshToken: "expired-enc-refresh-SECRET",
			ExpiresAt:    time.Now().Add(-time.Hour),
		}))

		got, err := s.GetUpstreamTokens(ctx, "expired-enc", "provider-a", nil)
		assert.ErrorIs(t, err, ErrExpired)
		require.NotNil(t, got)
		assert.Equal(t, "expired-enc-refresh-SECRET", got.RefreshToken)
	})
}

// TestRedisStorage_TokenEncryption_LazyResealPreservesNoExpiry pins the
// PTTL == -1 branch: non-expiring rows stay non-expiring after lazy re-seal.
func TestRedisStorage_TokenEncryption_LazyResealPreservesNoExpiry(t *testing.T) {
	withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis, client *redis.Client) {
		require.NoError(t, s.StoreUpstreamTokens(ctx, "noexp-rot", "provider-a", &UpstreamTokens{
			ProviderID:   "provider-a",
			AccessToken:  "noexp-access-SECRET",
			RefreshToken: "noexp-refresh-SECRET",
		}))
		key := redisUpstreamKey(s.keyPrefix, "noexp-rot", "provider-a")
		require.Equal(t, time.Duration(0), mr.TTL(key))

		kr2 := newEncTestKeyring(t, "k2", map[string][]byte{"k1": encTestKey(1), "k2": encTestKey(2)})
		rotated := NewRedisStorageWithClient(client, "test:auth:", WithTokenEncryption(kr2))
		_, err := rotated.GetUpstreamTokens(ctx, "noexp-rot", "provider-a", nil)
		require.NoError(t, err)

		assert.Contains(t, rawUpstreamValue(t, ctx, client, key), `"kid":"k2"`)
		assert.Equal(t, time.Duration(0), mr.TTL(key), "non-expiring row must stay non-expiring after re-seal")
	})
}

// TestRedisStorage_TokenEncryption_BindingBeforeExpiry pins the ordering in an
// encrypted fleet: an expired AND binding-mismatched row must surface
// ErrInvalidBinding (not ErrExpired) and release no refresh material — the
// refresh path must never be entered for a row that isn't the caller's.
func TestRedisStorage_TokenEncryption_BindingBeforeExpiry(t *testing.T) {
	withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, _ *redis.Client) {
		require.NoError(t, s.StoreUpstreamTokens(ctx, "bind-exp", "provider-a", &UpstreamTokens{
			ProviderID:   "provider-a",
			AccessToken:  "stale-enc-access-SECRET",
			RefreshToken: "stale-enc-refresh-SECRET",
			ExpiresAt:    time.Now().Add(-time.Hour),
			UserID:       "user-enc",
			ClientID:     "client-enc",
		}))

		got, err := s.GetUpstreamTokens(ctx, "bind-exp", "provider-a",
			&ExpectedBinding{UserID: "different-user"})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidBinding)
		assert.NotErrorIs(t, err, ErrExpired, "binding must win over expiry")
		assert.Nil(t, got, "no refresh material may be released for a mismatched row")

		// Control: the same row with the matching binding surfaces ErrExpired
		// and its refresh material, proving the ordering above is what refused
		// the mismatched read.
		got, err = s.GetUpstreamTokens(ctx, "bind-exp", "provider-a",
			&ExpectedBinding{UserID: "user-enc", ClientID: "client-enc"})
		assert.ErrorIs(t, err, ErrExpired)
		require.NotNil(t, got)
		assert.Equal(t, "stale-enc-refresh-SECRET", got.RefreshToken)
	})
}

// TestRedisStorage_TokenEncryption_LazyResealFailure makes the post-read
// re-SET fail and pins the degraded behavior: the WARN fires but the read
// still returns the decrypted tokens (lazy re-seal is best-effort; the next
// read retries it).
//
//nolint:paralleltest // captures slog default
func TestRedisStorage_TokenEncryption_LazyResealFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})
	ctx := context.Background()

	kr1 := newEncTestKeyring(t, "k1", map[string][]byte{"k1": encTestKey(1)})
	s := NewRedisStorageWithClient(client, "test:auth:", WithTokenEncryption(kr1))
	require.NoError(t, s.StoreUpstreamTokens(ctx, "reseal-fail", "provider-a", encTestTokens()))
	key := redisUpstreamKey(s.keyPrefix, "reseal-fail", "provider-a")

	// Rotate: reads through this instance want to re-seal k1 rows under k2.
	kr2 := newEncTestKeyring(t, "k2", map[string][]byte{"k1": encTestKey(1), "k2": encTestKey(2)})
	rotated := NewRedisStorageWithClient(client, "test:auth:", WithTokenEncryption(kr2))

	// Fail only the post-read re-SET: PTTL must keep working so lazyReseal
	// reaches the SET branch.
	out := captureWarnLogs(t)
	mr.Server().SetPreHook(func(c *server.Peer, cmd string, _ ...string) bool {
		if strings.EqualFold(cmd, "set") {
			c.WriteError("write disabled")
			return true
		}
		return false
	})
	t.Cleanup(func() { mr.Server().SetPreHook(nil) })

	got, err := rotated.GetUpstreamTokens(ctx, "reseal-fail", "provider-a", nil)
	require.NoError(t, err, "a failed lazy re-seal must not fail the read")
	assert.Equal(t, "enc-access-token-SECRET", got.AccessToken)
	assert.Contains(t, out.String(), "lazy token re-encryption failed")
	assert.Contains(t, out.String(), key)

	// The row was NOT re-sealed (still under k1) — the next read retries.
	assert.Contains(t, rawUpstreamValue(t, ctx, client, key), `"kid":"k1"`)
}

// TestRedisStorage_TokenEncryption_SecondInstanceSameKeyring verifies two
// storage instances sharing a fleet and keyring interoperate (multi-replica
// auth-server deployments).
func TestRedisStorage_TokenEncryption_SecondInstanceSameKeyring(t *testing.T) {
	withEncryptedRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis, client *redis.Client) {
		require.NoError(t, s.StoreUpstreamTokens(ctx, "shared-session", "provider-a", encTestTokens()))

		replicaKR := newEncTestKeyring(t, "k1", map[string][]byte{"k1": encTestKey(1)})
		replica := NewRedisStorageWithClient(client, "test:auth:", WithTokenEncryption(replicaKR))

		got, err := replica.GetUpstreamTokens(ctx, "shared-session", "provider-a", nil)
		require.NoError(t, err)
		assert.Equal(t, "enc-access-token-SECRET", got.AccessToken)

		// Writes from the replica are readable by the first instance.
		require.NoError(t, replica.StoreUpstreamTokens(ctx, "shared-session", "provider-a", &UpstreamTokens{
			ProviderID:   "provider-a",
			AccessToken:  fmt.Sprintf("replica-written-%s", "SECRET"),
			RefreshToken: "replica-refresh-SECRET",
			ExpiresAt:    time.Now().Add(time.Hour),
			UserID:       "user-enc",
		}))
		got, err = s.GetUpstreamTokens(ctx, "shared-session", "provider-a", nil)
		require.NoError(t, err)
		assert.Equal(t, "replica-written-SECRET", got.AccessToken)
	})
}

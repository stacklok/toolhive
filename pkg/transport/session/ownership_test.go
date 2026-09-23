// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLookupOwnerDoesNotRefreshTTL(t *testing.T) {
	t.Parallel()

	t.Run("local", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		manager := NewManagerWithStorage(time.Hour, func(id string) Session { return NewProxySession(id) }, storage)
		t.Cleanup(func() { require.NoError(t, manager.Stop()) })
		id := uuid.NewString()
		sess := NewProxySession(id)
		sess.SetMetadata(MetadataKeyIdentityBinding, "issuer\x00subject")
		storeAged(storage, sess)

		owner, err := manager.LookupOwner(t.Context(), id)
		require.NoError(t, err)
		require.Equal(t, "issuer\x00subject", owner)
		require.NoError(t, storage.DeleteExpired(t.Context(), time.Now().Add(-time.Hour)))
		_, err = storage.LoadMetadata(t.Context(), id)
		require.ErrorIs(t, err, ErrSessionNotFound)
	})

	t.Run("redis", func(t *testing.T) {
		t.Parallel()
		storage, mr := newTestRedisStorage(t)
		manager := NewManagerWithStorage(time.Hour, func(id string) Session { return NewProxySession(id) }, storage)
		t.Cleanup(func() { require.NoError(t, manager.Stop()) })
		id := uuid.NewString()
		sess := NewProxySession(id)
		sess.SetMetadata(MetadataKeyIdentityBinding, "issuer\x00subject")
		require.NoError(t, manager.AddSession(sess))
		mr.FastForward(29 * time.Minute)

		owner, err := manager.LookupOwner(t.Context(), id)
		require.NoError(t, err)
		require.Equal(t, "issuer\x00subject", owner)
		mr.FastForward(2 * time.Minute)
		_, err = storage.LoadMetadata(t.Context(), id)
		require.ErrorIs(t, err, ErrSessionNotFound)
	})
}

func TestLoadIfOwnerReturnsValidatedSnapshotAndRefreshesOnlyMatches(t *testing.T) {
	t.Parallel()

	t.Run("local", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		manager := NewManagerWithStorage(time.Hour, func(id string) Session { return NewProxySession(id) }, storage)
		t.Cleanup(func() { require.NoError(t, manager.Stop()) })

		ownedID := uuid.NewString()
		owned := NewProxySession(ownedID)
		owned.SetMetadata(MetadataKeyIdentityBinding, "issuer\x00subject")
		owned.SetMetadata("version", "owned-snapshot")
		storeAged(storage, owned)
		loaded, err := manager.LoadIfOwner(ownedID, "issuer\x00subject")
		require.NoError(t, err)
		require.Equal(t, "owned-snapshot", loaded.GetMetadata()["version"])
		require.NoError(t, storage.DeleteExpired(t.Context(), time.Now().Add(-time.Hour)))
		_, err = storage.LoadMetadata(t.Context(), ownedID)
		require.NoError(t, err, "successful owner load must refresh last access")

		foreignID := uuid.NewString()
		foreign := NewProxySession(foreignID)
		foreign.SetMetadata(MetadataKeyIdentityBinding, "issuer\x00foreign")
		storeAged(storage, foreign)
		_, err = manager.LoadIfOwner(foreignID, "issuer\x00subject")
		require.ErrorIs(t, err, ErrSessionNotFound)
		require.NoError(t, storage.DeleteExpired(t.Context(), time.Now().Add(-time.Hour)))
		_, err = storage.LoadMetadata(t.Context(), foreignID)
		require.ErrorIs(t, err, ErrSessionNotFound, "foreign mismatch must not refresh last access")
	})

	t.Run("redis", func(t *testing.T) {
		t.Parallel()
		storage, mr := newTestRedisStorage(t)
		manager := NewManagerWithStorage(time.Hour, func(id string) Session { return NewProxySession(id) }, storage)
		t.Cleanup(func() { require.NoError(t, manager.Stop()) })

		ownedID := uuid.NewString()
		owned := NewProxySession(ownedID)
		owned.SetMetadata(MetadataKeyIdentityBinding, "issuer\x00subject")
		owned.SetMetadata("version", "owned-snapshot")
		require.NoError(t, manager.AddSession(owned))
		mr.FastForward(29 * time.Minute)
		loaded, err := manager.LoadIfOwner(ownedID, "issuer\x00subject")
		require.NoError(t, err)
		require.Equal(t, "owned-snapshot", loaded.GetMetadata()["version"])
		mr.FastForward(2 * time.Minute)
		_, err = storage.LoadMetadata(t.Context(), ownedID)
		require.NoError(t, err, "successful owner load must refresh TTL")

		foreignID := uuid.NewString()
		foreign := NewProxySession(foreignID)
		foreign.SetMetadata(MetadataKeyIdentityBinding, "issuer\x00foreign")
		require.NoError(t, manager.AddSession(foreign))
		mr.FastForward(29 * time.Minute)
		_, err = manager.LoadIfOwner(foreignID, "issuer\x00subject")
		require.ErrorIs(t, err, ErrSessionNotFound)
		mr.FastForward(2 * time.Minute)
		_, err = storage.LoadMetadata(t.Context(), foreignID)
		require.ErrorIs(t, err, ErrSessionNotFound, "foreign mismatch must not refresh TTL")
	})
}

func TestOwnerConditionalMutationsPreserveRacedRecord(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			var storage Storage
			if backend == "redis" {
				storage, _ = newTestRedisStorage(t)
			} else {
				storage = NewLocalStorage()
			}
			manager := NewManagerWithStorage(time.Hour, func(id string) Session { return NewProxySession(id) }, storage)
			t.Cleanup(func() { require.NoError(t, manager.Stop()) })
			id := uuid.NewString()
			original := NewProxySession(id)
			original.SetMetadata(MetadataKeyIdentityBinding, "issuer\x00original")
			require.NoError(t, manager.AddSession(original))

			raced := NewProxySession(id)
			raced.SetMetadata(MetadataKeyIdentityBinding, "issuer\x00foreign")
			raced.SetMetadata("version", "raced")
			require.NoError(t, storage.Store(t.Context(), raced))

			require.ErrorIs(t, manager.DeleteIfOwner(id, "issuer\x00original"), ErrSessionNotFound)
			replacement := NewProxySession(id)
			replacement.SetMetadata(MetadataKeyIdentityBinding, "issuer\x00original")
			replacement.SetMetadata("version", "stale-update")
			require.ErrorIs(t, manager.UpsertSessionIfOwner(replacement, "issuer\x00original"), ErrSessionNotFound)

			metadata, err := storage.LoadMetadata(t.Context(), id)
			require.NoError(t, err)
			require.Equal(t, "issuer\x00foreign", metadata[MetadataKeyIdentityBinding])
			require.Equal(t, "raced", metadata["version"])

			currentReplacement := NewProxySession(id)
			currentReplacement.SetMetadata(MetadataKeyIdentityBinding, "issuer\x00foreign")
			currentReplacement.SetMetadata("version", "updated")
			require.NoError(t, manager.UpsertSessionIfOwner(currentReplacement, "issuer\x00foreign"))
			require.NoError(t, manager.DeleteIfOwner(id, "issuer\x00foreign"))
			_, err = storage.LoadMetadata(t.Context(), id)
			require.ErrorIs(t, err, ErrSessionNotFound)
		})
	}
}

func TestConditionalCreationPreservesOwner(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			var storage Storage
			if backend == "redis" {
				storage, _ = newTestRedisStorage(t)
			} else {
				storage = NewLocalStorage()
			}
			manager := NewManagerWithStorage(time.Hour, func(id string) Session { return NewProxySession(id) }, storage)
			t.Cleanup(func() { require.NoError(t, manager.Stop()) })
			id := uuid.NewString()
			results := make(chan error, 16)
			for i := 0; i < 16; i++ {
				go func() {
					sess := NewProxySession(id)
					sess.SetMetadata(MetadataKeyIdentityBinding, "issuer\x00subject")
					results <- manager.AddSession(sess)
				}()
			}
			created := 0
			for i := 0; i < 16; i++ {
				select {
				case err := <-results:
					if err == nil {
						created++
					} else {
						require.ErrorIs(t, err, ErrSessionAlreadyExists)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("conditional creation timed out")
				}
			}
			require.Equal(t, 1, created)
			foreign := NewProxySession(id)
			foreign.SetMetadata(MetadataKeyIdentityBinding, "other\x00subject")
			require.ErrorIs(t, manager.AddSession(foreign), ErrSessionAlreadyExists)
			require.True(t, errors.Is(manager.AddWithID(id), ErrSessionAlreadyExists))
			owner, err := manager.LookupOwner(t.Context(), id)
			require.NoError(t, err)
			require.Equal(t, "issuer\x00subject", owner, "metadata must survive Redis serialization and colliding writes")
		})
	}
}

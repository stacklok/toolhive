// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// userIdentityStoreFactory returns a fresh, empty store.
type userIdentityStoreFactory func(t *testing.T) UserStorage

const (
	identityRaceGoroutines = 16
	identityRaceRounds     = 100
)

func contractUser(t *testing.T, store UserStorage, id string) {
	t.Helper()
	now := time.Now()
	require.NoError(t, store.CreateUser(context.Background(), &User{ID: id, CreatedAt: now, UpdatedAt: now}))
}

func contractIdentity(userID, providerID, subject string) *ProviderIdentity {
	now := time.Now()
	return &ProviderIdentity{
		UserID:          userID,
		ProviderID:      providerID,
		ProviderSubject: subject,
		LinkedAt:        now,
		LastUsedAt:      now,
	}
}

// runUserIdentityStorageContract checks that a (provider, subject) pair links
// to at most one user, including under concurrent creation.
func runUserIdentityStorageContract(t *testing.T, newStore userIdentityStoreFactory) {
	t.Helper()
	ctx := context.Background()

	t.Run("second link for the same identity is refused and the first stays", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name       string
			secondProv string
			secondSub  string
			wantErr    bool
		}{
			{name: "same provider and subject", secondProv: "provider-a", secondSub: "subject-1", wantErr: true},
			{name: "different subject", secondProv: "provider-a", secondSub: "subject-2"},
			{name: "different provider", secondProv: "provider-b", secondSub: "subject-1"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				store := newStore(t)
				contractUser(t, store, "user-1")
				contractUser(t, store, "user-2")
				require.NoError(t, store.CreateProviderIdentity(ctx, contractIdentity("user-1", "provider-a", "subject-1")))

				err := store.CreateProviderIdentity(ctx, contractIdentity("user-2", tc.secondProv, tc.secondSub))
				if tc.wantErr {
					require.Error(t, err)
					assert.True(t, errors.Is(err, ErrAlreadyExists), "got %v", err)
				} else {
					require.NoError(t, err)
				}

				got, err := store.GetProviderIdentity(ctx, "provider-a", "subject-1")
				require.NoError(t, err)
				assert.Equal(t, "user-1", got.UserID)
			})
		}
	})

	t.Run("link to a missing user is refused and writes nothing", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			// deleteFirst creates the user and deletes it before the link write,
			// as a concurrent rollback would.
			deleteFirst bool
		}{
			{name: "user never created"},
			{name: "user deleted before the link write", deleteFirst: true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				store := newStore(t)
				if tc.deleteFirst {
					contractUser(t, store, "ghost")
					require.NoError(t, store.DeleteUser(ctx, "ghost"))
				}

				err := store.CreateProviderIdentity(ctx, contractIdentity("ghost", "provider-a", "subject-1"))
				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrNotFound), "got %v", err)

				_, err = store.GetProviderIdentity(ctx, "provider-a", "subject-1")
				assert.True(t, errors.Is(err, ErrNotFound), "got %v", err)
			})
		}
	})

	t.Run("concurrent creates for one identity yield exactly one link", func(t *testing.T) {
		t.Parallel()
		store := newStore(t)

		for round := 0; round < identityRaceRounds; round++ {
			subject := fmt.Sprintf("subject-%d", round)
			userIDs := make([]string, identityRaceGoroutines)
			for i := range userIDs {
				userIDs[i] = fmt.Sprintf("user-%d-%d", round, i)
				contractUser(t, store, userIDs[i])
			}

			start := make(chan struct{})
			errs := make([]error, identityRaceGoroutines)
			var wg sync.WaitGroup
			for i := range userIDs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					errs[i] = store.CreateProviderIdentity(ctx, contractIdentity(userIDs[i], "provider-a", subject))
				}()
			}
			close(start)
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("timeout waiting for concurrent callers")
			}

			winner := ""
			for i, err := range errs {
				if err == nil {
					require.Empty(t, winner, "round %d: more than one create succeeded", round)
					winner = userIDs[i]
					continue
				}
				require.True(t, errors.Is(err, ErrAlreadyExists), "round %d: got %v", round, err)
			}
			require.NotEmpty(t, winner, "round %d: no create succeeded", round)

			got, err := store.GetProviderIdentity(ctx, "provider-a", subject)
			require.NoError(t, err)
			require.Equal(t, winner, got.UserID, "round %d", round)
		}
	})

	t.Run("binding fields round-trip and survive last-used updates", func(t *testing.T) {
		t.Parallel()
		store := newStore(t)
		contractUser(t, store, "user-1")

		id := contractIdentity("user-1", "provider-a", "subject-1")
		id.Issuer = "https://idp.example.com"
		id.SubjectClaim = "oid"
		require.NoError(t, store.CreateProviderIdentity(ctx, id))

		got, err := store.GetProviderIdentity(ctx, "provider-a", "subject-1")
		require.NoError(t, err)
		assert.Equal(t, "https://idp.example.com", got.Issuer)
		assert.Equal(t, "oid", got.SubjectClaim)

		byUser, err := store.GetUserProviderIdentities(ctx, "user-1")
		require.NoError(t, err)
		require.Len(t, byUser, 1)
		assert.Equal(t, "https://idp.example.com", byUser[0].Issuer)
		assert.Equal(t, "oid", byUser[0].SubjectClaim)

		later := id.LastUsedAt.Add(time.Hour).Truncate(time.Second)
		require.NoError(t, store.UpdateProviderIdentityLastUsed(ctx, "provider-a", "subject-1", later))

		afterUpdate, err := store.GetProviderIdentity(ctx, "provider-a", "subject-1")
		require.NoError(t, err)
		assert.Equal(t, "https://idp.example.com", afterUpdate.Issuer)
		assert.Equal(t, "oid", afterUpdate.SubjectClaim)
		assert.Equal(t, later.Unix(), afterUpdate.LastUsedAt.Unix())
	})

	t.Run("create rejects half-stamped identity", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name   string
			issuer string
			claim  string
		}{
			{name: "issuer without claim", issuer: "https://idp.example.com"},
			{name: "claim without issuer", claim: "sub"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				store := newStore(t)
				contractUser(t, store, "user-1")
				id := contractIdentity("user-1", "provider-a", "subject-1")
				id.Issuer = tc.issuer
				id.SubjectClaim = tc.claim
				require.Error(t, store.CreateProviderIdentity(ctx, id))
			})
		}
	})

	t.Run("compare-and-swap stamps unstamped row preserving timestamps", func(t *testing.T) {
		t.Parallel()
		store := newStore(t)
		swapper, ok := store.(ProviderIdentitySwapper)
		require.True(t, ok, "store %T must implement ProviderIdentitySwapper", store)

		contractUser(t, store, "user-1")
		linkedAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
		lastUsed := time.Now().Add(-time.Hour).Truncate(time.Second)
		require.NoError(t, store.CreateProviderIdentity(ctx, &ProviderIdentity{
			UserID: "user-1", ProviderID: "provider-a", ProviderSubject: "subject-1",
			LinkedAt: linkedAt, LastUsedAt: lastUsed,
		}))

		// Advance LastUsedAt concurrently before stamping to verify stamp does not clobber it.
		newerLastUsed := time.Now().Truncate(time.Second)
		require.NoError(t, store.UpdateProviderIdentityLastUsed(ctx, "provider-a", "subject-1", newerLastUsed))

		expected := &ProviderIdentity{UserID: "user-1", ProviderID: "provider-a", ProviderSubject: "subject-1"}
		stamped := &ProviderIdentity{
			UserID: "user-1", ProviderID: "provider-a", ProviderSubject: "subject-1",
			Issuer: "https://idp.example.com", SubjectClaim: "sub",
			LinkedAt: time.Now(), LastUsedAt: time.Now().Add(-10 * time.Hour),
		}
		require.NoError(t, swapper.CompareAndSwapProviderIdentity(ctx, expected, stamped))

		got, err := store.GetProviderIdentity(ctx, "provider-a", "subject-1")
		require.NoError(t, err)
		assert.Equal(t, "user-1", got.UserID)
		assert.Equal(t, "https://idp.example.com", got.Issuer)
		assert.Equal(t, "sub", got.SubjectClaim)
		assert.Equal(t, linkedAt.Unix(), got.LinkedAt.Unix(), "stamp must preserve LinkedAt")
		assert.Equal(t, newerLastUsed.Unix(), got.LastUsedAt.Unix(), "stamp must preserve LastUsedAt")

		// A second stamp against the old unstamped expectation fails with ErrProviderIdentityChanged.
		err = swapper.CompareAndSwapProviderIdentity(ctx, expected, stamped)
		require.ErrorIs(t, err, ErrProviderIdentityChanged)
	})

	t.Run("compare-and-swap replaces mismatched row and detaches old user", func(t *testing.T) {
		t.Parallel()
		store := newStore(t)
		swapper, ok := store.(ProviderIdentitySwapper)
		require.True(t, ok, "store %T must implement ProviderIdentitySwapper", store)

		contractUser(t, store, "user-old")
		contractUser(t, store, "user-new")

		oldID := contractIdentity("user-old", "provider-a", "subject-1")
		oldID.Issuer = "https://old-idp.example.com"
		oldID.SubjectClaim = "sub"
		require.NoError(t, store.CreateProviderIdentity(ctx, oldID))

		newID := contractIdentity("user-new", "provider-a", "subject-1")
		newID.Issuer = "https://new-idp.example.com"
		newID.SubjectClaim = "oid"
		require.NoError(t, swapper.CompareAndSwapProviderIdentity(ctx, oldID, newID))

		got, err := store.GetProviderIdentity(ctx, "provider-a", "subject-1")
		require.NoError(t, err)
		assert.Equal(t, "user-new", got.UserID)
		assert.Equal(t, "https://new-idp.example.com", got.Issuer)
		assert.Equal(t, "oid", got.SubjectClaim)

		// Old user still exists untouched, with no identities pointing at it.
		oldUser, err := store.GetUser(ctx, "user-old")
		require.NoError(t, err)
		assert.Equal(t, "user-old", oldUser.ID)
		oldLinks, err := store.GetUserProviderIdentities(ctx, "user-old")
		require.NoError(t, err)
		assert.Empty(t, oldLinks)

		newLinks, err := store.GetUserProviderIdentities(ctx, "user-new")
		require.NoError(t, err)
		require.Len(t, newLinks, 1)
		assert.Equal(t, "user-new", newLinks[0].UserID)

		// Deleting old user later must NOT delete new user's live identity link.
		require.NoError(t, store.DeleteUser(ctx, "user-old"))
		afterDelete, err := store.GetProviderIdentity(ctx, "provider-a", "subject-1")
		require.NoError(t, err)
		assert.Equal(t, "user-new", afterDelete.UserID)
	})

	t.Run("compare-and-swap error cases", func(t *testing.T) {
		t.Parallel()
		store := newStore(t)
		swapper, ok := store.(ProviderIdentitySwapper)
		require.True(t, ok, "store %T must implement ProviderIdentitySwapper", store)

		contractUser(t, store, "user-1")
		contractUser(t, store, "user-2")
		orig := contractIdentity("user-1", "provider-a", "subject-1")
		orig.Issuer = "https://idp-1.example.com"
		orig.SubjectClaim = "sub"
		require.NoError(t, store.CreateProviderIdentity(ctx, orig))

		validNew := contractIdentity("user-2", "provider-a", "subject-1")
		validNew.Issuer = "https://idp-2.example.com"
		validNew.SubjectClaim = "sub"

		t.Run("missing row returns ErrNotFound", func(t *testing.T) {
			exp := contractIdentity("user-1", "provider-a", "absent")
			rep := contractIdentity("user-2", "provider-a", "absent")
			rep.Issuer = "https://idp-2.example.com"
			rep.SubjectClaim = "sub"
			err := swapper.CompareAndSwapProviderIdentity(ctx, exp, rep)
			require.ErrorIs(t, err, ErrNotFound)
		})

		t.Run("missing replacement user returns ErrNotFound and keeps row", func(t *testing.T) {
			rep := contractIdentity("ghost", "provider-a", "subject-1")
			rep.Issuer = "https://idp-2.example.com"
			rep.SubjectClaim = "sub"
			err := swapper.CompareAndSwapProviderIdentity(ctx, orig, rep)
			require.ErrorIs(t, err, ErrNotFound)

			got, getErr := store.GetProviderIdentity(ctx, "provider-a", "subject-1")
			require.NoError(t, getErr)
			assert.Equal(t, "user-1", got.UserID)
		})

		t.Run("wrong expected issuer returns ErrProviderIdentityChanged", func(t *testing.T) {
			wrongExp := *orig
			wrongExp.Issuer = "https://wrong.example.com"
			err := swapper.CompareAndSwapProviderIdentity(ctx, &wrongExp, validNew)
			require.ErrorIs(t, err, ErrProviderIdentityChanged)
		})

		t.Run("wrong expected claim returns ErrProviderIdentityChanged", func(t *testing.T) {
			wrongExp := *orig
			wrongExp.SubjectClaim = "oid"
			err := swapper.CompareAndSwapProviderIdentity(ctx, &wrongExp, validNew)
			require.ErrorIs(t, err, ErrProviderIdentityChanged)
		})

		t.Run("wrong expected user returns ErrProviderIdentityChanged", func(t *testing.T) {
			wrongExp := *orig
			wrongExp.UserID = "user-2"
			err := swapper.CompareAndSwapProviderIdentity(ctx, &wrongExp, validNew)
			require.ErrorIs(t, err, ErrProviderIdentityChanged)
		})

		t.Run("invalid inputs are rejected", func(t *testing.T) {
			require.Error(t, swapper.CompareAndSwapProviderIdentity(ctx, nil, validNew))
			require.Error(t, swapper.CompareAndSwapProviderIdentity(ctx, orig, nil))

			unstampedNew := contractIdentity("user-2", "provider-a", "subject-1")
			require.Error(t, swapper.CompareAndSwapProviderIdentity(ctx, orig, unstampedNew))

			mismatchedKey := contractIdentity("user-2", "provider-b", "subject-1")
			mismatchedKey.Issuer = "https://idp-2.example.com"
			mismatchedKey.SubjectClaim = "sub"
			require.Error(t, swapper.CompareAndSwapProviderIdentity(ctx, orig, mismatchedKey))
		})
	})

	t.Run("concurrent compare-and-swap replacements yield exactly one winner", func(t *testing.T) {
		t.Parallel()
		store := newStore(t)
		swapper, ok := store.(ProviderIdentitySwapper)
		require.True(t, ok, "store %T must implement ProviderIdentitySwapper", store)

		for round := 0; round < identityRaceRounds; round++ {
			subject := fmt.Sprintf("cas-subject-%d", round)
			oldUserID := fmt.Sprintf("cas-old-%d", round)
			contractUser(t, store, oldUserID)
			oldRow := contractIdentity(oldUserID, "provider-a", subject)
			oldRow.Issuer = "https://old.example.com"
			oldRow.SubjectClaim = "sub"
			require.NoError(t, store.CreateProviderIdentity(ctx, oldRow))

			userIDs := make([]string, identityRaceGoroutines)
			for i := range userIDs {
				userIDs[i] = fmt.Sprintf("cas-new-%d-%d", round, i)
				contractUser(t, store, userIDs[i])
			}

			start := make(chan struct{})
			errs := make([]error, identityRaceGoroutines)
			var wg sync.WaitGroup
			for i := range userIDs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					rep := contractIdentity(userIDs[i], "provider-a", subject)
					rep.Issuer = "https://new.example.com"
					rep.SubjectClaim = "oid"
					errs[i] = swapper.CompareAndSwapProviderIdentity(ctx, oldRow, rep)
				}()
			}
			close(start)
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("timeout waiting for concurrent CAS callers")
			}

			winner := ""
			for i, err := range errs {
				if err == nil {
					require.Empty(t, winner, "round %d: more than one CAS succeeded", round)
					winner = userIDs[i]
					continue
				}
				require.ErrorIs(t, err, ErrProviderIdentityChanged, "round %d: got %v", round, err)
			}
			require.NotEmpty(t, winner, "round %d: no CAS succeeded", round)

			got, err := store.GetProviderIdentity(ctx, "provider-a", subject)
			require.NoError(t, err)
			require.Equal(t, winner, got.UserID, "round %d", round)
			require.Equal(t, "https://new.example.com", got.Issuer, "round %d", round)
			require.Equal(t, "oid", got.SubjectClaim, "round %d", round)
		}
	})
}

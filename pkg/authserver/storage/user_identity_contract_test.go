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
}

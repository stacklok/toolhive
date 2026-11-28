// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

const (
	resolveRaceGoroutines = 16
	resolveRaceRounds     = 50
	redisTestPrefix       = "resolve:"
)

// resolveBackend is a real store plus counters for the users and identity links
// it holds. The counters read backend internals so production code needs no
// list API.
type resolveBackend struct {
	store     storage.UserStorage
	userCount func(t *testing.T) int
	linkCount func(t *testing.T) int
}

func newMemoryResolveBackend(t *testing.T) resolveBackend {
	t.Helper()
	mem := storage.NewMemoryStorage()
	t.Cleanup(func() { require.NoError(t, mem.Close()) })
	return resolveBackend{
		store:     mem,
		userCount: func(*testing.T) int { return mem.Stats().Users },
		linkCount: func(*testing.T) int { return mem.Stats().ProviderIdentities },
	}
}

func newRedisResolveBackend(t *testing.T) resolveBackend {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	count := func(keyPrefix string, exact bool) int {
		n := 0
		for _, key := range mr.Keys() {
			rest, ok := strings.CutPrefix(key, keyPrefix)
			if !ok {
				continue
			}
			// "user:providers:" and "user:upstream:" share the "user:" prefix;
			// user IDs are UUIDs and contain no colon.
			if exact && strings.Contains(rest, ":") {
				continue
			}
			n++
		}
		return n
	}
	return resolveBackend{
		store:     storage.NewRedisStorageWithClient(client, redisTestPrefix),
		userCount: func(*testing.T) int { return count(redisTestPrefix+storage.KeyTypeUser+":", true) },
		linkCount: func(*testing.T) int { return count(redisTestPrefix+storage.KeyTypeProvider+":", false) },
	}
}

func resolveBackends() map[string]func(*testing.T) resolveBackend {
	return map[string]func(*testing.T) resolveBackend{
		"memory": newMemoryResolveBackend,
		"redis":  newRedisResolveBackend,
	}
}

func TestUserResolver_ConcurrentFirstLogins_OneUser(t *testing.T) {
	t.Parallel()
	for name, newBackend := range resolveBackends() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBackend(t)
			resolver := NewUserResolver(b.store)
			ctx := context.Background()

			for round := range resolveRaceRounds {
				subject := fmt.Sprintf("subject-%d", round)
				start := make(chan struct{})
				ids := make([]string, resolveRaceGoroutines)
				errs := make([]error, resolveRaceGoroutines)
				var wg sync.WaitGroup
				for i := range resolveRaceGoroutines {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						user, err := resolver.ResolveUser(ctx, "upstream", subject)
						errs[i] = err
						if err == nil {
							ids[i] = user.ID
						}
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

				for i := range ids {
					require.NoError(t, errs[i], "round %d caller %d", round, i)
					require.NotEmpty(t, ids[i])
					require.Equal(t, ids[0], ids[i], "round %d: callers resolved different users", round)
				}

				identities, err := b.store.GetUserProviderIdentities(ctx, ids[0])
				require.NoError(t, err)
				require.Len(t, identities, 1, "round %d: winner must have exactly one linked identity", round)
				// Every round adds one user and one link; losers' users are rolled back.
				require.Equal(t, round+1, b.userCount(t), "round %d: orphaned or duplicate user", round)
				require.Equal(t, round+1, b.linkCount(t), "round %d: duplicate link", round)
			}
		})
	}
}

func TestUserResolver_RepeatLogins_SameUser(t *testing.T) {
	t.Parallel()
	for name, newBackend := range resolveBackends() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBackend(t)
			resolver := NewUserResolver(b.store)
			ctx := context.Background()

			first, err := resolver.ResolveUser(ctx, "upstream", "subject")
			require.NoError(t, err)
			other, err := resolver.ResolveUser(ctx, "upstream", "other-subject")
			require.NoError(t, err)
			require.NotEqual(t, first.ID, other.ID)

			for range 5 {
				again, err := resolver.ResolveUser(ctx, "upstream", "subject")
				require.NoError(t, err)
				assert.Equal(t, first.ID, again.ID)
			}
			assert.Equal(t, 2, b.userCount(t))
			assert.Equal(t, 2, b.linkCount(t))
		})
	}
}

func TestUserResolver_ConcurrentMismatchedReplacements_OneNewUser(t *testing.T) {
	t.Parallel()
	for name, newBackend := range resolveBackends() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBackend(t)
			ctx := context.Background()

			oldResolver := NewUserResolver(b.store,
				WithCreateOnlyFor("upstream"),
				WithIdentityBinding("upstream", "https://old-idp.example.com", "sub"),
			)
			newResolver := NewUserResolver(b.store,
				WithCreateOnlyFor("upstream"),
				WithIdentityBinding("upstream", "https://new-idp.example.com", "oid"),
			)

			for round := range resolveRaceRounds {
				subject := fmt.Sprintf("replace-subject-%d", round)
				oldUser, err := oldResolver.ResolveUser(ctx, "upstream", subject)
				require.NoError(t, err)

				start := make(chan struct{})
				ids := make([]string, resolveRaceGoroutines)
				errs := make([]error, resolveRaceGoroutines)
				var wg sync.WaitGroup
				for i := range resolveRaceGoroutines {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						user, rErr := newResolver.ResolveUser(ctx, "upstream", subject)
						errs[i] = rErr
						if rErr == nil {
							ids[i] = user.ID
						}
					}()
				}
				close(start)
				done := make(chan struct{})
				go func() { wg.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("timeout waiting for concurrent replace callers")
				}

				for i := range ids {
					require.NoError(t, errs[i], "round %d caller %d", round, i)
					require.NotEmpty(t, ids[i])
					require.NotEqual(t, oldUser.ID, ids[i], "round %d: must not adopt old user", round)
					require.Equal(t, ids[0], ids[i], "round %d: callers resolved different new users", round)
				}

				// Old user remains untouched with zero linked identities; new user owns the single link.
				oldKept, err := b.store.GetUser(ctx, oldUser.ID)
				require.NoError(t, err)
				require.Equal(t, oldUser.ID, oldKept.ID)

				oldLinks, err := b.store.GetUserProviderIdentities(ctx, oldUser.ID)
				require.NoError(t, err)
				require.Empty(t, oldLinks, "round %d: old user must have no links after replace", round)

				newLinks, err := b.store.GetUserProviderIdentities(ctx, ids[0])
				require.NoError(t, err)
				require.Len(t, newLinks, 1, "round %d: winner must have exactly one linked identity", round)
				require.Equal(t, "https://new-idp.example.com", newLinks[0].Issuer)
				require.Equal(t, "oid", newLinks[0].SubjectClaim)

				// Every round keeps the untouched old user + 1 new user (2 users per round) and 1 link.
				require.Equal(t, 2*(round+1), b.userCount(t), "round %d: orphaned or duplicate user", round)
				require.Equal(t, round+1, b.linkCount(t), "round %d: duplicate link", round)
			}
		})
	}
}

func TestUserResolver_ConcurrentUnstampedLogins_StampOnceSameUser(t *testing.T) {
	t.Parallel()
	for name, newBackend := range resolveBackends() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBackend(t)
			ctx := context.Background()

			legacyResolver := NewUserResolver(b.store)
			boundResolver := NewUserResolver(b.store,
				WithCreateOnlyFor("upstream"),
				WithIdentityBinding("upstream", "https://idp.example.com", "sub"),
			)

			for round := range resolveRaceRounds {
				subject := fmt.Sprintf("stamp-subject-%d", round)
				legacyUser, err := legacyResolver.ResolveUser(ctx, "upstream", subject)
				require.NoError(t, err)

				start := make(chan struct{})
				ids := make([]string, resolveRaceGoroutines)
				errs := make([]error, resolveRaceGoroutines)
				var wg sync.WaitGroup
				for i := range resolveRaceGoroutines {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						user, rErr := boundResolver.ResolveUser(ctx, "upstream", subject)
						errs[i] = rErr
						if rErr == nil {
							ids[i] = user.ID
						}
					}()
				}
				close(start)
				done := make(chan struct{})
				go func() { wg.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("timeout waiting for concurrent stamp callers")
				}

				for i := range ids {
					require.NoError(t, errs[i], "round %d caller %d", round, i)
					require.Equal(t, legacyUser.ID, ids[i], "round %d: must preserve legacy user", round)
				}

				row, err := b.store.GetProviderIdentity(ctx, "upstream", subject)
				require.NoError(t, err)
				require.Equal(t, "https://idp.example.com", row.Issuer)
				require.Equal(t, "sub", row.SubjectClaim)
			}
		})
	}
}

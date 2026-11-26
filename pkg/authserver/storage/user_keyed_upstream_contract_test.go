// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/identity"
)

// userKeyedFactory returns the wrapper under test, the store beneath it, and a
// function reporting the storage lifetime of one user's row (false when the row
// has none). Foreign, ownerless and placeholder rows cannot be written through
// the wrapper, which keys on tokens.UserID, so tests plant them in the base
// store under the key the wrapper reads.
type userKeyedFactory func(t *testing.T) (
	dec *UserKeyedUpstreamStorage, base Storage, ttl func(userID, provider string) (time.Duration, bool),
)

const (
	ukOwner    = "owner-user"
	ukIntruder = "intruder-user"
	ukProvider = "provider-a"
)

func ukCtx(user string) context.Context {
	return identity.WithCanonicalUser(context.Background(), user)
}

func ukRow(user string, expiresAt time.Time) *UpstreamTokens {
	return &UpstreamTokens{
		ProviderID:       ukProvider,
		AccessToken:      "access-" + user,
		RefreshToken:     "refresh-" + user,
		UserID:           user,
		ExpiresAt:        expiresAt,
		SessionExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}
}

func runUserKeyedUpstreamContract(t *testing.T, newStore userKeyedFactory) {
	t.Helper()
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)

	t.Run("owner reads own row; session ID is ignored", func(t *testing.T) {
		t.Parallel()
		dec, _, _ := newStore(t)
		ctx := ukCtx(ukOwner)
		require.NoError(t, dec.StoreUpstreamTokens(ctx, "tsid-1", ukProvider, ukRow(ukOwner, future)))

		for _, tsid := range []string{"tsid-1", "tsid-2", ""} {
			got, err := dec.GetUpstreamTokens(ctx, tsid, ukProvider)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, "access-"+ukOwner, got.AccessToken)
		}
		all, err := dec.GetAllUpstreamTokens(ctx, "anything")
		require.NoError(t, err)
		require.Contains(t, all, ukProvider)
	})

	t.Run("single read", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name         string
			plant        *UpstreamTokens
			planted      bool
			wantRefresh  string
			wantErr      error
			notErr       error
			wantUnchange bool
		}{
			{name: "owner gets own expired row with its refresh token", plant: ukRow(ukOwner, past), planted: true,
				wantRefresh: "refresh-" + ukOwner, wantErr: ErrExpired},
			{name: "expired foreign row is a binding error", plant: ukRow(ukIntruder, past), planted: true,
				wantErr: ErrInvalidBinding, notErr: ErrExpired, wantUnchange: true},
			{name: "row with no stored owner is a binding error", plant: ukRow("", future), planted: true,
				wantErr: ErrInvalidBinding},
			{name: "placeholder row reads as no tokens", plant: nil, planted: true},
			{name: "missing row is not found", wantErr: ErrNotFound},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				dec, base, _ := newStore(t)
				sid := userRowSessionID(ukOwner)
				if tt.planted {
					require.NoError(t, base.StoreUpstreamTokens(context.Background(), sid, ukProvider, tt.plant))
				}

				got, err := dec.GetUpstreamTokens(ukCtx(ukOwner), "s", ukProvider)
				if tt.wantErr != nil {
					assert.ErrorIs(t, err, tt.wantErr)
				} else {
					assert.NoError(t, err)
				}
				if tt.notErr != nil {
					assert.NotErrorIs(t, err, tt.notErr)
				}
				if tt.wantRefresh == "" {
					assert.Nil(t, got)
				} else {
					require.NotNil(t, got)
					assert.Equal(t, tt.wantRefresh, got.RefreshToken)
				}

				if tt.wantUnchange {
					stored, err := base.GetUpstreamTokens(context.Background(), sid, ukProvider)
					assert.ErrorIs(t, err, ErrExpired)
					require.NotNil(t, stored)
					assert.Equal(t, ukIntruder, stored.UserID)
					assert.Equal(t, "refresh-"+ukIntruder, stored.RefreshToken)
				}
			})
		}
	})

	t.Run("bulk read returns a placeholder row as nil", func(t *testing.T) {
		t.Parallel()
		dec, base, _ := newStore(t)
		require.NoError(t, base.StoreUpstreamTokens(context.Background(), userRowSessionID(ukOwner), ukProvider, nil))

		all, err := dec.GetAllUpstreamTokens(ukCtx(ukOwner), "s")
		require.NoError(t, err)
		require.Contains(t, all, ukProvider)
		assert.Nil(t, all[ukProvider])
	})

	t.Run("bulk read skips foreign and ownerless rows with one ERROR each", func(t *testing.T) {
		t.Parallel()
		dec, base, _ := newStore(t)
		rec := &capturedLogs{}
		dec.logger = slog.New(rec)
		ctx := ukCtx(ukOwner)
		sid := userRowSessionID(ukOwner)

		require.NoError(t, dec.StoreUpstreamTokens(ctx, "s", "mine", ukRow(ukOwner, future)))
		require.NoError(t, base.StoreUpstreamTokens(context.Background(), sid, "foreign", ukRow(ukIntruder, future)))
		require.NoError(t, base.StoreUpstreamTokens(context.Background(), sid, "ownerless", ukRow("", future)))

		all, err := dec.GetAllUpstreamTokens(ctx, "s")
		require.NoError(t, err)
		assert.Len(t, all, 1)
		assert.Contains(t, all, "mine")

		logged := rec.recordsAt(slog.LevelError)
		require.Len(t, logged, 2)
		byProvider := map[string]map[string]any{}
		for _, l := range logged {
			byProvider[l["provider"].(string)] = l
		}
		assert.Equal(t, ukIntruder, byProvider["foreign"]["got_user_id"])
		assert.Equal(t, "", byProvider["ownerless"]["got_user_id"])
		assert.NotContains(t, byProvider["foreign"], "expected_user_id")
	})

	t.Run("writes are refused for nil tokens, empty UserID and another user", func(t *testing.T) {
		t.Parallel()
		dec, base, _ := newStore(t)
		ctx := ukCtx(ukOwner)

		cases := map[string]struct {
			tokens *UpstreamTokens
			target error
		}{
			"nil tokens":       {nil, fosite.ErrInvalidRequest},
			"empty UserID":     {ukRow("", future), fosite.ErrInvalidRequest},
			"context mismatch": {ukRow(ukIntruder, future), ErrInvalidBinding},
		}
		for name, tc := range cases {
			assert.ErrorIs(t, dec.StoreUpstreamTokens(ctx, "s", ukProvider, tc.tokens), tc.target, "store: "+name)
			assert.ErrorIs(t, dec.CompareAndSwapUpstreamTokens(ctx, "s", ukProvider, "", tc.tokens), tc.target, "cas: "+name)
		}
		for _, u := range []string{ukOwner, ukIntruder} {
			_, err := base.GetUpstreamTokens(context.Background(), userRowSessionID(u), ukProvider)
			assert.ErrorIs(t, err, ErrNotFound, "nothing was written for %q", u)
		}
	})

	t.Run("compare-and-swap with empty expected creates, then needs the matching token", func(t *testing.T) {
		t.Parallel()
		dec, _, _ := newStore(t)
		ctx := ukCtx(ukOwner)
		first := ukRow(ukOwner, future)
		first.RefreshToken = "rt-1"
		second := ukRow(ukOwner, future)
		second.RefreshToken = "rt-2"

		require.NoError(t, dec.CompareAndSwapUpstreamTokens(ctx, "s", ukProvider, "", first))
		assert.ErrorIs(t, dec.CompareAndSwapUpstreamTokens(ctx, "s", ukProvider, "stale", second), ErrConcurrentRefresh)
		require.NoError(t, dec.CompareAndSwapUpstreamTokens(ctx, "s", ukProvider, "rt-1", second))
	})

	t.Run("storage lifetime", func(t *testing.T) {
		t.Parallel()
		dec, _, ttl := newStore(t)
		ctx := ukCtx(ukOwner)

		expiringAt := time.Now().Add(time.Hour)
		require.NoError(t, dec.StoreUpstreamTokens(ctx, "s", "expiring", ukRow(ukOwner, expiringAt)))
		d, ok := ttl(ukOwner, "expiring")
		require.True(t, ok)
		assert.InDelta(t, (time.Hour + DefaultRefreshTokenTTL).Seconds(), d.Seconds(), 2)

		// The fixture sets SessionExpiresAt: a token that never expires must
		// still get no lifetime.
		require.NoError(t, dec.StoreUpstreamTokens(ctx, "s", "forever", ukRow(ukOwner, time.Time{})))
		_, ok = ttl(ukOwner, "forever")
		assert.False(t, ok, "non-expiring token must have no storage lifetime")

		syn := "tk-0123456789abcdef0123456789abcdef"
		require.NoError(t, dec.StoreUpstreamTokens(ukCtx(syn), "s", "syn", ukRow(syn, time.Time{})))
		d, ok = ttl(syn, "syn")
		require.True(t, ok, "synthetic user's non-expiring row keeps the session bound")
		assert.InDelta(t, (7*24*time.Hour + DefaultRefreshTokenTTL).Seconds(), d.Seconds(), 2)
	})

	t.Run("caller's tokens are not mutated", func(t *testing.T) {
		t.Parallel()
		dec, _, _ := newStore(t)
		row := ukRow(ukOwner, time.Time{})
		require.NoError(t, dec.StoreUpstreamTokens(ukCtx(ukOwner), "s", ukProvider, row))
		assert.False(t, row.SessionExpiresAt.IsZero())
	})

	t.Run("user IDs with separators address different rows", func(t *testing.T) {
		t.Parallel()
		dec, _, _ := newStore(t)
		pairs := [][2]string{{"a:b", "c"}, {"a", "b:c"}, {"a%3Ab", "c"}}
		ids := map[UpstreamTokenRowID]bool{}
		for _, p := range pairs {
			ctx := ukCtx(p[0])
			row := ukRow(p[0], future)
			row.AccessToken = p[0] + "|" + p[1]
			require.NoError(t, dec.StoreUpstreamTokens(ctx, "s", p[1], row))
			id, err := dec.ResolveUpstreamTokenRowID(ctx, "s", p[1])
			require.NoError(t, err)
			ids[id] = true
		}
		assert.Len(t, ids, 3)
		for _, p := range pairs {
			got, err := dec.GetUpstreamTokens(ukCtx(p[0]), "s", p[1])
			require.NoError(t, err)
			assert.Equal(t, p[0]+"|"+p[1], got.AccessToken)
		}
	})

	t.Run("row ID follows the user, not the session", func(t *testing.T) {
		t.Parallel()
		dec, _, _ := newStore(t)
		a1, err := dec.ResolveUpstreamTokenRowID(ukCtx(ukOwner), "tsid-1", ukProvider)
		require.NoError(t, err)
		a2, err := dec.ResolveUpstreamTokenRowID(ukCtx(ukOwner), "tsid-2", ukProvider)
		require.NoError(t, err)
		b, err := dec.ResolveUpstreamTokenRowID(ukCtx(ukIntruder), "tsid-1", ukProvider)
		require.NoError(t, err)
		assert.Equal(t, a1, a2)
		assert.NotEqual(t, a1, b)
	})

	t.Run("every keyed method needs a canonical user", func(t *testing.T) {
		t.Parallel()
		dec, _, _ := newStore(t)
		shadowed := context.WithValue(context.Background(), identity.CanonicalUserContextKey{}, "")
		for name, ctx := range map[string]context.Context{"absent": context.Background(), "empty": shadowed} {
			_, err := dec.ResolveUpstreamTokenRowID(ctx, "s", ukProvider)
			assert.ErrorIs(t, err, ErrNoCanonicalUser, name)
			assert.ErrorIs(t, dec.StoreUpstreamTokens(ctx, "s", ukProvider, ukRow(ukOwner, future)), ErrNoCanonicalUser, name)
			assert.ErrorIs(t, dec.CompareAndSwapUpstreamTokens(ctx, "s", ukProvider, "", ukRow(ukOwner, future)),
				ErrNoCanonicalUser, name)
			_, err = dec.GetUpstreamTokens(ctx, "s", ukProvider)
			assert.ErrorIs(t, err, ErrNoCanonicalUser, name)
			_, err = dec.GetAllUpstreamTokens(ctx, "s")
			assert.ErrorIs(t, err, ErrNoCanonicalUser, name)
			assert.ErrorIs(t, dec.DeleteUpstreamTokensForProvider(ctx, "s", ukProvider), ErrNoCanonicalUser, name)
		}
	})

	t.Run("methods that carry no user key still work without one", func(t *testing.T) {
		t.Parallel()
		dec, _, _ := newStore(t)
		ctx := context.Background()
		require.NoError(t, dec.CreateUser(ctx, &User{ID: "u1"}))
		u, err := dec.GetUser(ctx, "u1")
		require.NoError(t, err)
		assert.Equal(t, "u1", u.ID)
		_, err = dec.GetProviderIdentity(ctx, "p", "s")
		assert.ErrorIs(t, err, ErrNotFound)
		require.NoError(t, dec.CreateProviderIdentity(ctx, &ProviderIdentity{UserID: "u1", ProviderID: "p", ProviderSubject: "s"}))
		pi, err := dec.GetProviderIdentity(ctx, "p", "s")
		require.NoError(t, err)
		require.NotNil(t, pi)
		require.NoError(t, dec.ConsumeAssertionJWT(ctx, "purpose", "iss", "jti", time.Now().Add(time.Minute)))
		assert.ErrorIs(t, dec.ConsumeAssertionJWT(ctx, "purpose", "iss", "jti", time.Now().Add(time.Minute)), fosite.ErrJTIKnown)
	})

	t.Run("delete by session is refused", func(t *testing.T) {
		t.Parallel()
		dec, _, _ := newStore(t)
		ctx := ukCtx(ukOwner)
		require.NoError(t, dec.StoreUpstreamTokens(ctx, "s", ukProvider, ukRow(ukOwner, future)))
		err := dec.DeleteUpstreamTokens(ctx, "s")
		assert.ErrorIs(t, err, errDeleteAllUnsupported)
		assert.ErrorIs(t, err, errors.ErrUnsupported)
		_, err = dec.GetUpstreamTokens(ctx, "s", ukProvider)
		assert.NoError(t, err)
	})

	t.Run("DeleteUser removes the user's rows and index", func(t *testing.T) {
		t.Parallel()
		dec, _, _ := newStore(t)
		u := "doomed-user"
		ctx := ukCtx(u)
		require.NoError(t, dec.CreateUser(context.Background(), &User{ID: u}))
		require.NoError(t, dec.StoreUpstreamTokens(ctx, "s", "p1", ukRow(u, future)))
		require.NoError(t, dec.StoreUpstreamTokens(ctx, "s", "p2", ukRow(u, time.Time{})))

		require.NoError(t, dec.DeleteUser(context.Background(), u))

		for _, p := range []string{"p1", "p2"} {
			_, err := dec.GetUpstreamTokens(ctx, "s", p)
			assert.ErrorIs(t, err, ErrNotFound, p)
		}
		all, err := dec.GetAllUpstreamTokens(ctx, "s")
		require.NoError(t, err)
		assert.Empty(t, all)
	})

	t.Run("DeleteUser without a user record is not found and keeps the rows", func(t *testing.T) {
		t.Parallel()
		dec, base, _ := newStore(t)
		u := "synthetic-user"
		require.NoError(t, base.StoreUpstreamTokens(
			context.Background(), userRowSessionID(u), ukProvider, ukRow(u, future)))

		assert.ErrorIs(t, dec.DeleteUser(context.Background(), u), ErrNotFound)

		got, err := dec.GetUpstreamTokens(ukCtx(u), "s", ukProvider)
		require.NoError(t, err)
		assert.Equal(t, "access-"+u, got.AccessToken)
	})

	t.Run("delete for provider removes only the user's row", func(t *testing.T) {
		t.Parallel()
		dec, _, _ := newStore(t)
		require.NoError(t, dec.StoreUpstreamTokens(ukCtx(ukOwner), "s", ukProvider, ukRow(ukOwner, future)))
		require.NoError(t, dec.StoreUpstreamTokens(ukCtx(ukIntruder), "s", ukProvider, ukRow(ukIntruder, future)))
		require.NoError(t, dec.DeleteUpstreamTokensForProvider(ukCtx(ukOwner), "s", ukProvider))
		_, err := dec.GetUpstreamTokens(ukCtx(ukOwner), "s", ukProvider)
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = dec.GetUpstreamTokens(ukCtx(ukIntruder), "s", ukProvider)
		assert.NoError(t, err)
	})

	t.Run("Unwrap reaches the base store", func(t *testing.T) {
		t.Parallel()
		dec, base, _ := newStore(t)
		assert.Same(t, base, dec.Unwrap())
		assert.Same(t, base, Unwrap(dec))
	})
}

func TestMemoryStorage_UserKeyedUpstreamContract(t *testing.T) {
	t.Parallel()
	runUserKeyedUpstreamContract(t, func(t *testing.T) (
		*UserKeyedUpstreamStorage, Storage, func(string, string) (time.Duration, bool),
	) {
		t.Helper()
		s := NewMemoryStorage()
		t.Cleanup(func() { _ = s.Close() })
		ttl := func(userID, provider string) (time.Duration, bool) {
			s.mu.RLock()
			defer s.mu.RUnlock()
			entry, ok := s.upstreamTokens[upstreamKey{userRowSessionID(userID), provider}]
			if !ok || entry.expiresAt.IsZero() {
				return 0, false
			}
			return time.Until(entry.expiresAt), true
		}
		dec, err := NewUserKeyedUpstreamStorage(s)
		require.NoError(t, err)
		return dec, s, ttl
	})
}

func TestRedisStorage_UserKeyedUpstreamContract(t *testing.T) {
	t.Parallel()
	runUserKeyedUpstreamContract(t, func(t *testing.T) (
		*UserKeyedUpstreamStorage, Storage, func(string, string) (time.Duration, bool),
	) {
		t.Helper()
		s, mr := newTestRedisStorage(t)
		t.Cleanup(func() {
			_ = s.Close()
			mr.Close()
		})
		ttl := func(userID, provider string) (time.Duration, bool) {
			d := mr.TTL(redisUpstreamKey(s.keyPrefix, userRowSessionID(userID), provider))
			return d, d > 0
		}
		dec, err := NewUserKeyedUpstreamStorage(s)
		require.NoError(t, err)
		return dec, s, ttl
	})
}

func TestRedisStorage_UserKeyedUpstreamKeys(t *testing.T) {
	t.Parallel()

	t.Run("rows and index live under the user key; index TTL follows the rows", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisStorage(t)
		t.Cleanup(func() { _ = s.Close(); mr.Close() })
		dec, err := NewUserKeyedUpstreamStorage(s)
		require.NoError(t, err)
		ctx := ukCtx("a:b")

		require.NoError(t, dec.StoreUpstreamTokens(ctx, "tsid", "p1", ukRow("a:b", time.Now().Add(time.Hour))))
		assert.True(t, mr.Exists(s.keyPrefix+"upstream:user:a%3Ab:p1"))
		idx := s.keyPrefix + "upstream:idx:user:a%3Ab"
		assert.True(t, mr.Exists(idx))
		assert.Greater(t, mr.TTL(idx), time.Duration(0))

		require.NoError(t, dec.StoreUpstreamTokens(ctx, "tsid", "p2", ukRow("a:b", time.Time{})))
		assert.Equal(t, time.Duration(0), mr.TTL(idx), "a never-expiring row keeps the index persistent")

		for _, k := range mr.Keys() {
			assert.NotContains(t, k, "tsid")
		}
	})

	t.Run("DeleteUser removes the index and the user's reverse-lookup set", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisStorage(t)
		t.Cleanup(func() { _ = s.Close(); mr.Close() })
		dec, err := NewUserKeyedUpstreamStorage(s)
		require.NoError(t, err)
		u := "a:b"
		escaped := "a%3Ab"
		ctx := ukCtx(u)
		require.NoError(t, dec.CreateUser(context.Background(), &User{ID: u}))
		require.NoError(t, dec.StoreUpstreamTokens(ctx, "s", "p1", ukRow(u, time.Time{})))
		require.True(t, mr.Exists(s.keyPrefix+"upstream:idx:user:"+escaped))
		require.True(t, mr.Exists(redisSetKey(s.keyPrefix, KeyTypeUserUpstream, u)))

		require.NoError(t, dec.DeleteUser(context.Background(), u))

		assert.False(t, mr.Exists(s.keyPrefix+"upstream:idx:user:"+escaped))
		assert.False(t, mr.Exists(redisSetKey(s.keyPrefix, KeyTypeUserUpstream, u)))
		assert.False(t, mr.Exists(s.keyPrefix+"upstream:user:"+escaped+":p1"))
	})

	t.Run("legacy migration leaves user rows alone", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisStorage(t)
		t.Cleanup(func() { _ = s.Close(); mr.Close() })
		dec, err := NewUserKeyedUpstreamStorage(s)
		require.NoError(t, err)
		ctx := ukCtx("u")
		require.NoError(t, dec.StoreUpstreamTokens(ctx, "s", "p", ukRow("u", time.Now().Add(time.Hour))))

		require.NoError(t, s.MigrateLegacyUpstreamData(context.Background(), "default", "oidc"))

		got, err := dec.GetUpstreamTokens(ctx, "s", "p")
		require.NoError(t, err)
		assert.Equal(t, "access-u", got.AccessToken)
	})
}

type noConsumeStorage struct{ Storage }

func TestUserKeyedUpstreamStorage_ConsumeAssertionJWTFailsClosed(t *testing.T) {
	t.Parallel()
	m := NewMemoryStorage()
	t.Cleanup(func() { _ = m.Close() })
	dec, err := NewUserKeyedUpstreamStorage(noConsumeStorage{m})
	require.NoError(t, err)
	assert.Error(t, dec.ConsumeAssertionJWT(context.Background(), "p", "i", "j", time.Now().Add(time.Minute)))
}

func TestNewUserKeyedUpstreamStorage_RejectsNilInner(t *testing.T) {
	t.Parallel()
	dec, err := NewUserKeyedUpstreamStorage(nil)
	require.Error(t, err)
	assert.Nil(t, dec)
}

type fixedAllStorage struct {
	Storage
	all map[string]*UpstreamTokens
}

func (f fixedAllStorage) GetAllUpstreamTokens(context.Context, string) (map[string]*UpstreamTokens, error) {
	return f.all, nil
}

func TestUserKeyedUpstreamStorage_GetAllDoesNotMutateInnerResult(t *testing.T) {
	t.Parallel()
	m := NewMemoryStorage()
	t.Cleanup(func() { _ = m.Close() })
	inner := map[string]*UpstreamTokens{
		"mine":    ukRow("u", time.Now().Add(time.Hour)),
		"foreign": ukRow("someone-else", time.Now().Add(time.Hour)),
	}
	dec, err := NewUserKeyedUpstreamStorage(fixedAllStorage{Storage: m, all: inner})
	require.NoError(t, err)

	got, err := dec.GetAllUpstreamTokens(ukCtx("u"), "ignored")
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Contains(t, got, "mine")
	assert.Len(t, inner, 2, "the inner store's map must not be modified")
}

type failingAllStorage struct {
	Storage
	err error
}

func (f failingAllStorage) GetAllUpstreamTokens(context.Context, string) (map[string]*UpstreamTokens, error) {
	return nil, f.err
}

func TestUserKeyedUpstreamStorage_GetAllReturnsInnerErrorUnchanged(t *testing.T) {
	t.Parallel()
	m := NewMemoryStorage()
	t.Cleanup(func() { _ = m.Close() })
	boom := errors.New("backend down")
	dec, err := NewUserKeyedUpstreamStorage(failingAllStorage{Storage: m, err: boom})
	require.NoError(t, err)

	all, err := dec.GetAllUpstreamTokens(ukCtx(ukOwner), "s")
	assert.Nil(t, all)
	assert.Equal(t, boom, err)
}

type failingDeleteAllStorage struct {
	Storage
	err error
}

func (f failingDeleteAllStorage) DeleteUpstreamTokens(context.Context, string) error { return f.err }

func TestUserKeyedUpstreamStorage_DeleteUserCleanupFailureIsLoggedNotReturned(t *testing.T) {
	t.Parallel()
	m := NewMemoryStorage()
	t.Cleanup(func() { _ = m.Close() })
	require.NoError(t, m.CreateUser(context.Background(), &User{ID: "u"}))
	dec, err := NewUserKeyedUpstreamStorage(failingDeleteAllStorage{Storage: m, err: errors.New("redis unavailable")})
	require.NoError(t, err)
	rec := &capturedLogs{}
	dec.logger = slog.New(rec)

	require.NoError(t, dec.DeleteUser(context.Background(), "u"))

	assert.Len(t, rec.recordsAt(slog.LevelWarn), 1)
	_, err = m.GetUser(context.Background(), "u")
	assert.ErrorIs(t, err, ErrNotFound)
}

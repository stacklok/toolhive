// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upstreamTokenStoreFactory returns a fresh, empty upstream token store.
type upstreamTokenStoreFactory func(t *testing.T) UpstreamTokenStorage

const (
	// contractOwner owns every row this suite stores.
	contractOwner = "owner-user"
	// contractIntruder is a second user who owns nothing here. Every read it
	// makes is the attack the owner comparison exists to stop: it knows the
	// session ID, because a client can read that out of its own token, and
	// knowing it must not be enough.
	contractIntruder = "intruder-user"

	contractSession  = "session-under-test"
	contractProvider = "provider-a"
)

func contractRow(expiresAt time.Time) *UpstreamTokens {
	return &UpstreamTokens{
		ProviderID:      contractProvider,
		AccessToken:     "upstream-access",
		RefreshToken:    "upstream-refresh",
		IDToken:         "upstream-id",
		UserID:          contractOwner,
		UpstreamSubject: "upstream-subject",
		ExpiresAt:       expiresAt,
	}
}

// runUpstreamTokenReadContract checks the ownership contract of the
// session-keyed upstream token reads against one backend. Both run it, because
// the comparison is only worth anything if it holds wherever the rows are
// kept: a deployment that moves from memory to Redis must not quietly lose it.
//
// The rules under test are the ones in UpstreamTokenStorage and
// ErrInvalidBinding: the session ID addresses a row, the expected owner is
// what entitles a caller to it, and a caller who is not the owner learns
// nothing and gets nothing.
func runUpstreamTokenReadContract(t *testing.T, newStore upstreamTokenStoreFactory) {
	t.Helper()
	ctx := context.Background()

	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)

	t.Run("the owner gets the row", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		require.NoError(t, s.StoreUpstreamTokens(ctx, contractSession, contractProvider, contractRow(future)))

		got, err := s.GetUpstreamTokens(ctx, contractSession, contractProvider, contractOwner)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "upstream-access", got.AccessToken)

		all, err := s.GetAllUpstreamTokens(ctx, contractSession, contractOwner)
		require.NoError(t, err)
		require.Contains(t, all, contractProvider)
		assert.Equal(t, "upstream-access", all[contractProvider].AccessToken)
	})

	t.Run("another user gets nothing from either read", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		require.NoError(t, s.StoreUpstreamTokens(ctx, contractSession, contractProvider, contractRow(future)))

		got, err := s.GetUpstreamTokens(ctx, contractSession, contractProvider, contractIntruder)
		assert.ErrorIs(t, err, ErrInvalidBinding)
		assert.Nil(t, got, "a refused read must not hand back the row it refused")

		all, err := s.GetAllUpstreamTokens(ctx, contractSession, contractIntruder)
		assert.ErrorIs(t, err, ErrInvalidBinding)
		assert.Nil(t, all, "one foreign row fails the whole read, not just that row")
	})

	t.Run("one foreign row fails a read of the session's other rows", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		mine := contractRow(future)
		theirs := contractRow(future)
		theirs.ProviderID = "provider-b"
		theirs.UserID = contractIntruder
		require.NoError(t, s.StoreUpstreamTokens(ctx, contractSession, contractProvider, mine))
		require.NoError(t, s.StoreUpstreamTokens(ctx, contractSession, "provider-b", theirs))

		// Returning only the matching row would still disclose that the
		// session holds a row this caller may not have.
		all, err := s.GetAllUpstreamTokens(ctx, contractSession, contractOwner)
		assert.ErrorIs(t, err, ErrInvalidBinding)
		assert.Nil(t, all)
	})

	t.Run("a row with no stored owner belongs to nobody", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		unowned := contractRow(future)
		unowned.UserID = ""
		require.NoError(t, s.StoreUpstreamTokens(ctx, contractSession, contractProvider, unowned))

		got, err := s.GetUpstreamTokens(ctx, contractSession, contractProvider, contractOwner)
		assert.ErrorIs(t, err, ErrInvalidBinding, "an unowned row is not a row everyone owns")
		assert.Nil(t, got)

		all, err := s.GetAllUpstreamTokens(ctx, contractSession, contractOwner)
		assert.ErrorIs(t, err, ErrInvalidBinding)
		assert.Nil(t, all)
	})

	t.Run("an empty expected owner is refused, not waved through", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		require.NoError(t, s.StoreUpstreamTokens(ctx, contractSession, contractProvider, contractRow(future)))

		_, err := s.GetUpstreamTokens(ctx, contractSession, contractProvider, "")
		assert.ErrorIs(t, err, fosite.ErrInvalidRequest,
			"a caller that names no owner must not be served the row")

		_, err = s.GetAllUpstreamTokens(ctx, contractSession, "")
		assert.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})

	t.Run("a missing row is not found, whoever asks", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)

		_, err := s.GetUpstreamTokens(ctx, "no-such-session", contractProvider, contractOwner)
		assert.ErrorIs(t, err, ErrNotFound)

		// Not ErrInvalidBinding: an absent row has no owner to disagree with,
		// and reporting it as foreign would tell the caller it exists.
		_, err = s.GetUpstreamTokens(ctx, "no-such-session", contractProvider, contractIntruder)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("an expired row is refused by owner before expiry", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		require.NoError(t, s.StoreUpstreamTokens(ctx, contractSession, contractProvider, contractRow(past)))

		// The owner check has to come first. ErrExpired hands the row back so
		// the caller can refresh it, and that row carries the refresh token: a
		// caller who is not the owner would otherwise be given the one
		// credential that can mint new ones.
		got, err := s.GetUpstreamTokens(ctx, contractSession, contractProvider, contractIntruder)
		assert.ErrorIs(t, err, ErrInvalidBinding)
		assert.NotErrorIs(t, err, ErrExpired)
		assert.Nil(t, got)

		// The refused read changed nothing.
		stillThere, err := s.GetUpstreamTokens(ctx, contractSession, contractProvider, contractOwner)
		assert.ErrorIs(t, err, ErrExpired)
		require.NotNil(t, stillThere)
		assert.Equal(t, "upstream-refresh", stillThere.RefreshToken)
	})

	t.Run("the owner still gets an expired row and its refresh token", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		require.NoError(t, s.StoreUpstreamTokens(ctx, contractSession, contractProvider, contractRow(past)))

		got, err := s.GetUpstreamTokens(ctx, contractSession, contractProvider, contractOwner)
		assert.ErrorIs(t, err, ErrExpired)
		require.NotNil(t, got)
		assert.Equal(t, "upstream-refresh", got.RefreshToken)
	})

	t.Run("a null-marker row is returned as no row, not as a refusal", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		// Written to record that a provider was offered and declined. It
		// records no owner because it holds no credentials.
		require.NoError(t, s.StoreUpstreamTokens(ctx, contractSession, contractProvider, nil))

		got, err := s.GetUpstreamTokens(ctx, contractSession, contractProvider, contractOwner)
		require.NoError(t, err)
		assert.Nil(t, got)

		all, err := s.GetAllUpstreamTokens(ctx, contractSession, contractOwner)
		require.NoError(t, err)
		assert.Contains(t, all, contractProvider)
		assert.Nil(t, all[contractProvider])
	})

	t.Run("the user-keyed read agrees on who owns what", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		require.NoError(t, s.StoreUpstreamTokens(ctx, contractSession, contractProvider, contractRow(future)))

		// GetLatestUpstreamTokensForUser has always filtered by owner. The
		// session-keyed reads now answer the same question the same way, so a
		// caller cannot reach through one what the other refuses.
		mine, err := s.GetLatestUpstreamTokensForUser(ctx, contractOwner, contractProvider)
		require.NoError(t, err)
		require.NotNil(t, mine)
		assert.Equal(t, "upstream-access", mine.AccessToken)

		_, err = s.GetLatestUpstreamTokensForUser(ctx, contractIntruder, contractProvider)
		assert.ErrorIs(t, err, ErrNotFound)

		_, err = s.GetUpstreamTokens(ctx, contractSession, contractProvider, contractIntruder)
		assert.ErrorIs(t, err, ErrInvalidBinding)
	})

	t.Run("sibling sessions of different users stay apart", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		theirs := contractRow(future)
		theirs.UserID = contractIntruder
		require.NoError(t, s.StoreUpstreamTokens(ctx, contractSession, contractProvider, contractRow(future)))
		require.NoError(t, s.StoreUpstreamTokens(ctx, "their-session", contractProvider, theirs))

		got, err := s.GetUpstreamTokens(ctx, "their-session", contractProvider, contractIntruder)
		require.NoError(t, err)
		require.NotNil(t, got)

		_, err = s.GetUpstreamTokens(ctx, "their-session", contractProvider, contractOwner)
		assert.ErrorIs(t, err, ErrInvalidBinding)
	})
}

// TestMemoryStorage_UpstreamTokenReadContract runs the ownership contract
// against the in-memory backend.
func TestMemoryStorage_UpstreamTokenReadContract(t *testing.T) {
	t.Parallel()
	runUpstreamTokenReadContract(t, func(t *testing.T) UpstreamTokenStorage {
		t.Helper()
		s := NewMemoryStorage()
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

// TestRedisStorage_UpstreamTokenReadContract runs the same contract against
// the Redis backend, where the rows a multi-replica deployment actually shares
// live.
func TestRedisStorage_UpstreamTokenReadContract(t *testing.T) {
	t.Parallel()
	runUpstreamTokenReadContract(t, func(t *testing.T) UpstreamTokenStorage {
		t.Helper()
		s, mr := newTestRedisStorage(t)
		t.Cleanup(func() {
			_ = s.Close()
			mr.Close()
		})
		return s
	})
}

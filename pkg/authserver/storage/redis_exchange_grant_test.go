// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const exchangeGrantTestPrefix = "thv:auth:{ns:name}:"

// newClockedExchangeGrantRedis returns a store whose clock is a manual clock
// starting at contractEpoch, plus the miniredis behind it and an advance
// function that moves the clock and miniredis's key TTLs together.
func newClockedExchangeGrantRedis(t *testing.T) (*RedisStorage, *miniredis.Miniredis, func(time.Duration)) {
	t.Helper()
	mr := miniredis.RunT(t)
	clock := &manualClock{now: contractEpoch}
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewRedisStorageWithClient(client, exchangeGrantTestPrefix, withRedisClock(clock.Now))
	t.Cleanup(func() { _ = s.Close() })
	return s, mr, func(d time.Duration) { advance(clock, mr, d) }
}

func TestRedisStorage_ExchangeGrantContract(t *testing.T) {
	t.Parallel()
	runExchangeGrantStorageContract(t, func(t *testing.T) (ExchangeGrantStorage, func(time.Duration)) {
		t.Helper()
		s, _, adv := newClockedExchangeGrantRedis(t)
		return s, adv
	})
}

func TestRedisStorage_ExchangeGrantTTLs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, mr, _ := newClockedExchangeGrantRedis(t)

	g := contractGrant("sig-1")
	require.NoError(t, s.CreateExchangeGrant(ctx, g))

	recordKey := redisKey(s.keyPrefix, KeyTypeExchangeGrant, "sig-1")
	markerKey := redisAssertionJWTKey(s.keyPrefix, "exchange-grant", g.SourceIssuer, g.SourceJTI)

	wantRecord := time.Hour + exchangeGrantRedisSlack
	wantMarker := 10*time.Minute + ExchangeGrantMarkerMargin + exchangeGrantRedisSlack
	assert.Equal(t, wantRecord, mr.TTL(recordKey))
	assert.Equal(t, wantMarker, mr.TTL(markerKey))

	require.NoError(t, s.RevokeExchangeGrant(ctx, "sig-1", contractEpoch.Add(time.Minute)))
	assert.Equal(t, wantRecord, mr.TTL(recordKey), "revoke must not change the record TTL")
	assert.Equal(t, wantMarker, mr.TTL(markerKey), "revoke must not change the marker TTL")

	marker, err := mr.Get(markerKey)
	require.NoError(t, err)
	assert.Equal(t, "sig-1", marker, "the marker holds the grant signature")
}

func TestRedisStorage_ExchangeGrantKeysUseStorePrefix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, mr, _ := newClockedExchangeGrantRedis(t)

	require.NoError(t, s.CreateExchangeGrant(ctx, contractGrant("parent")))
	require.NoError(t, s.CreateExchangeGrant(ctx, contractChild("child")))
	require.NoError(t, s.RevokeExchangeGrant(ctx, "child", contractEpoch))

	keys := mr.Keys()
	require.Len(t, keys, 3) // two records and one marker
	for _, k := range keys {
		assert.True(t, strings.HasPrefix(k, exchangeGrantTestPrefix), k)
	}
}

func TestRedisStorage_ExchangeGrantChildWritesNoMarker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, mr, _ := newClockedExchangeGrantRedis(t)

	require.NoError(t, s.CreateExchangeGrant(ctx, contractGrant("parent")))
	markerPrefix := s.keyPrefix + KeyTypeAssertionJWT + ":"
	countMarkers := func() int {
		n := 0
		for _, k := range mr.Keys() {
			if strings.HasPrefix(k, markerPrefix) {
				n++
			}
		}
		return n
	}
	require.Equal(t, 1, countMarkers())

	require.NoError(t, s.CreateExchangeGrant(ctx, contractChild("child-1")))
	require.NoError(t, s.CreateExchangeGrant(ctx, contractChild("child-2")))
	assert.Equal(t, 1, countMarkers(), "a child grant must not add a marker")
}

func TestRedisStorage_ExchangeGrantWrongTypeKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("record key of the wrong type", func(t *testing.T) {
		t.Parallel()
		s, mr, _ := newClockedExchangeGrantRedis(t)
		recordKey := redisKey(s.keyPrefix, KeyTypeExchangeGrant, "sig-1")
		require.NoError(t, mr.Set(recordKey, "not a hash"))

		err := s.CreateExchangeGrant(ctx, contractGrant("sig-1"))
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrAlreadyExists)
	})

	t.Run("marker key of the wrong type writes no record", func(t *testing.T) {
		t.Parallel()
		s, mr, _ := newClockedExchangeGrantRedis(t)
		g := contractGrant("sig-1")
		markerKey := redisAssertionJWTKey(s.keyPrefix, "exchange-grant", g.SourceIssuer, g.SourceJTI)
		_, err := mr.SetAdd(markerKey, "member")
		require.NoError(t, err)

		require.Error(t, s.CreateExchangeGrant(ctx, g))
		assert.False(t, mr.Exists(redisKey(s.keyPrefix, KeyTypeExchangeGrant, "sig-1")))
	})
}

func TestRedisStorage_ExchangeGrantFarFutureExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, mr, _ := newClockedExchangeGrantRedis(t)

	// Year 9999 is far beyond the range of time.Duration (about 292 years), so
	// Sub saturates. The marker must still be written and still refuse replay.
	far := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	g := contractGrant("sig-1")
	g.SourceExpiresAt = far
	g.ExpiresAt = far
	require.NoError(t, s.CreateExchangeGrant(ctx, g))

	markerKey := redisAssertionJWTKey(s.keyPrefix, "exchange-grant", g.SourceIssuer, g.SourceJTI)
	assert.True(t, mr.Exists(markerKey), "the marker must be written")
	assert.Positive(t, mr.TTL(markerKey))
	assert.Positive(t, mr.TTL(redisKey(s.keyPrefix, KeyTypeExchangeGrant, "sig-1")))

	reuse := contractGrant("sig-2")
	reuse.SourceJTI = g.SourceJTI
	assert.ErrorIs(t, s.CreateExchangeGrant(ctx, reuse), fosite.ErrJTIKnown)

	got, err := s.GetExchangeGrant(ctx, "sig-1")
	require.NoError(t, err)
	assert.True(t, far.Equal(got.ExpiresAt))
}

func TestRedisStorage_ExchangeGrantAlreadyExpiredCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, mr, _ := newClockedExchangeGrantRedis(t)

	g := contractGrant("sig-1")
	g.ExpiresAt = contractEpoch.Add(-time.Hour)
	g.SourceExpiresAt = contractEpoch.Add(-time.Hour)
	require.NoError(t, s.CreateExchangeGrant(ctx, g))

	recordKey := redisKey(s.keyPrefix, KeyTypeExchangeGrant, "sig-1")
	assert.Equal(t, time.Millisecond, mr.TTL(recordKey), "an expired record gets the 1 ms TTL floor")
	assert.Equal(t, 0, countKeysWithPrefix(mr, s.keyPrefix+KeyTypeAssertionJWT+":"),
		"a marker whose window has passed is not written")

	_, err := s.GetExchangeGrant(ctx, "sig-1")
	assert.ErrorIs(t, err, ErrNotFound)
}

func countKeysWithPrefix(mr *miniredis.Miniredis, prefix string) int {
	n := 0
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

func TestRedisStorage_ExchangeGrantCorruptRecords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	revokeAt := contractEpoch.Add(time.Minute)

	cases := []struct {
		name string
		seed func(t *testing.T, mr *miniredis.Miniredis, key string)
	}{
		{"bad JSON in data", func(_ *testing.T, mr *miniredis.Miniredis, key string) {
			mr.HSet(key, "data", "{not json")
		}},
		{"bad rev_ms", func(t *testing.T, mr *miniredis.Miniredis, key string) {
			t.Helper()
			good, err := json.Marshal(exchangeGrantRecord{
				Signature: "sig-1", ExpiresAt: contractEpoch.Add(time.Hour),
			})
			require.NoError(t, err)
			mr.HSet(key, "data", string(good), "rev_ms", "yesterday")
		}},
		{"wrong-type key", func(t *testing.T, mr *miniredis.Miniredis, key string) {
			t.Helper()
			require.NoError(t, mr.Set(key, "not a hash"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, mr, _ := newClockedExchangeGrantRedis(t)
			tc.seed(t, mr, redisKey(s.keyPrefix, KeyTypeExchangeGrant, "sig-1"))

			got, err := s.GetExchangeGrant(ctx, "sig-1")
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrNotFound)
			assert.Nil(t, got)

			err = s.RevokeExchangeGrant(ctx, "sig-1", revokeAt)
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrNotFound)
		})
	}

	t.Run("hash without data is not found", func(t *testing.T) {
		t.Parallel()
		s, mr, _ := newClockedExchangeGrantRedis(t)
		mr.HSet(redisKey(s.keyPrefix, KeyTypeExchangeGrant, "sig-1"), "rev_ms", "1")
		_, err := s.GetExchangeGrant(ctx, "sig-1")
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestRedisStorage_ExchangeGrantSignatureComesFromLookup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, mr, _ := newClockedExchangeGrantRedis(t)

	data, err := json.Marshal(exchangeGrantRecord{Signature: "other", ExpiresAt: contractEpoch.Add(time.Hour)})
	require.NoError(t, err)
	mr.HSet(redisKey(s.keyPrefix, KeyTypeExchangeGrant, "sig-1"), "data", string(data))

	got, err := s.GetExchangeGrant(ctx, "sig-1")
	require.NoError(t, err)
	assert.Equal(t, "sig-1", got.Signature)
}

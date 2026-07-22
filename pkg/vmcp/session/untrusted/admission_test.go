// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) (*redisStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	store, err := newRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "thv:vmcp:session:", "toolhive")
	require.NoError(t, err)
	return store, mr
}

func testAdmissionConfig() AdmissionConfig {
	return AdmissionConfig{
		PerUserPodQuota:   3,
		PerUserCreateRate: 2,
		PerMCPServerCap:   4,
		GlobalCapFactor:   0.8,
		CacheCapacity:     10,
	}
}

func TestNewRedisStoreValidation(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	_, err := newRedisStore(nil, "thv:vmcp:session:", "ns")
	require.Error(t, err, "nil client must be rejected")

	_, err = newRedisStore(c, "no-trailing-colon", "ns")
	require.Error(t, err, "prefix without ':' must be rejected")

	_, err = newRedisStore(c, "", "ns")
	require.Error(t, err, "empty prefix must be rejected")

	_, err = newRedisStore(c, "thv:vmcp:session:", "")
	require.Error(t, err, "empty namespace must be rejected")
}

func TestAdmissionConfigValidation(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	_, err := newAdmission(store, AdmissionConfig{GlobalCapFactor: 1.5, CacheCapacity: 10}, time.Now)
	require.Error(t, err, "GlobalCapFactor > 1 must be rejected")

	_, err = newAdmission(store, AdmissionConfig{CacheCapacity: 0}, time.Now)
	require.Error(t, err, "CacheCapacity 0 must be rejected")

	_, err = newAdmission(store, testAdmissionConfig(), time.Now)
	require.NoError(t, err)
}

func TestAdmissionCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userKey := "iss\x00sub"
	serverUID := "uid-1"

	setup := func(t *testing.T) (*admission, *redisStore, *miniredis.Miniredis) {
		t.Helper()
		store, mr := newTestStore(t)
		adm, err := newAdmission(store, testAdmissionConfig(), time.Now)
		require.NoError(t, err)
		return adm, store, mr
	}

	t.Run("admits under all limits", func(t *testing.T) {
		t.Parallel()
		adm, _, _ := setup(t)
		require.NoError(t, adm.Check(ctx, userKey, serverUID))
	})

	t.Run("denies at per-user quota", func(t *testing.T) {
		t.Parallel()
		adm, store, _ := setup(t)
		require.NoError(t, store.client.Set(ctx, store.userQuotaKey(userKey), 3, time.Minute).Err())
		err := adm.Check(ctx, userKey, serverUID)
		require.ErrorIs(t, err, ErrQuotaExceeded)
		assert.Contains(t, err.Error(), "per-user pod quota")
	})

	t.Run("denies above per-user create rate", func(t *testing.T) {
		t.Parallel()
		adm, _, _ := setup(t)
		require.NoError(t, adm.Check(ctx, userKey, serverUID))
		require.NoError(t, adm.Check(ctx, userKey, serverUID))
		err := adm.Check(ctx, userKey, serverUID)
		require.ErrorIs(t, err, ErrQuotaExceeded)
		assert.Contains(t, err.Error(), "create rate")
	})

	t.Run("denies at per-server cap", func(t *testing.T) {
		t.Parallel()
		adm, store, _ := setup(t)
		require.NoError(t, store.client.SAdd(ctx, store.serverPodsSetKey(serverUID), "p1", "p2", "p3", "p4").Err())
		err := adm.Check(ctx, userKey, serverUID)
		require.ErrorIs(t, err, ErrQuotaExceeded)
		assert.Contains(t, err.Error(), "per-MCPServer cap")
	})

	t.Run("denies at global capacity factor", func(t *testing.T) {
		t.Parallel()
		adm, store, _ := setup(t)
		// Cap = 10 * 0.8 = 8.
		members := []any{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8"}
		require.NoError(t, store.client.SAdd(ctx, store.podsSetKey(), members...).Err())
		err := adm.Check(ctx, userKey, serverUID)
		require.ErrorIs(t, err, ErrQuotaExceeded)
		assert.Contains(t, err.Error(), "global untrusted pod capacity")
	})

	t.Run("rate bucket expires", func(t *testing.T) {
		t.Parallel()
		adm, store, mr := setup(t)
		require.NoError(t, adm.Check(ctx, userKey, serverUID))
		require.NoError(t, adm.Check(ctx, userKey, serverUID))
		require.Error(t, adm.Check(ctx, userKey, serverUID))
		// Advance past the 2-minute bucket TTL.
		mr.FastForward(3 * time.Minute)
		rateKey := store.rateLimitKey(userKey, time.Now().Add(-3*time.Minute))
		assert.Equal(t, int64(0), store.client.Exists(ctx, rateKey).Val())
	})

	t.Run("fails closed when Redis is down", func(t *testing.T) {
		t.Parallel()
		adm, _, mr := setup(t)
		mr.Close()
		err := adm.Check(ctx, userKey, serverUID)
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrQuotaExceeded), "infra failure must not masquerade as quota denial")
		assert.Contains(t, err.Error(), "failing closed")
	})
}

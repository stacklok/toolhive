// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

//nolint:paralleltest // parallel execution handled by withCappedIntegrationStorage
package storage

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withCappedIntegrationStorage runs fn against two RedisStorage instances
// sharing one real Redis and one key prefix, both capped at maxDCR, the way
// two replicas of one auth server are.
func withCappedIntegrationStorage(t *testing.T, maxDCR int, fn func(context.Context, *RedisStorage, *RedisStorage)) {
	t.Helper()
	if testCluster == nil {
		t.Skip("Redis Sentinel cluster not available")
	}
	t.Parallel()

	prefix := DeriveKeyPrefix("inttest", sanitizeTestName(t.Name()))
	a := NewRedisStorageWithClient(testCluster.newTestClient(), prefix, WithRedisMaxDCRClients(maxDCR))
	b := NewRedisStorageWithClient(testCluster.newTestClient(), prefix, WithRedisMaxDCRClients(maxDCR))
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	fn(ctx, a, b)
}

func TestIntegration_DCRCap(t *testing.T) {
	t.Parallel()

	t.Run("rejects at the cap without writing a row", func(t *testing.T) {
		withCappedIntegrationStorage(t, 3, func(ctx context.Context, s, _ *RedisStorage) {
			start := time.Now()
			for i := range 3 {
				require.NoError(t, s.RegisterClient(ctx, newDCRClient(t, fmt.Sprintf("dcr-%d", i), "none", "")))
			}
			require.NoError(t, s.ReconcileConfiguredClient(ctx, &fosite.DefaultClient{ID: "configured"}))

			err := s.RegisterClient(ctx, newDCRClient(t, "dcr-rejected", "none", ""))
			require.ErrorIs(t, err, ErrClientCapacity)
			var capErr *ClientCapacityError
			require.ErrorAs(t, err, &capErr)
			assert.InDelta(t, (DefaultUnusedDCRClientTTL - time.Since(start)).Seconds(), capErr.RetryAfter.Seconds(), 5,
				"Retry-After must come from the oldest index score")

			n, err := s.client.Exists(ctx, redisKey(s.keyPrefix, KeyTypeClient, "dcr-rejected")).Result()
			require.NoError(t, err)
			assert.Zero(t, n, "a rejected registration must write no row")
			_, err = s.GetClient(ctx, "configured")
			require.NoError(t, err, "a configured client is unaffected by the cap")
		})
	})

	t.Run("concurrent replicas never exceed the cap", func(t *testing.T) {
		const capN, attempts = 5, 40
		withCappedIntegrationStorage(t, capN, func(ctx context.Context, a, b *RedisStorage) {
			replicas := []*RedisStorage{a, b}
			var wg sync.WaitGroup
			var succeeded atomic.Int32
			for i := range attempts {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if replicas[i%2].RegisterClient(ctx, newDCRClient(t, fmt.Sprintf("dcr-%d", i), "none", "")) == nil {
						succeeded.Add(1)
					}
				}()
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("timeout waiting for concurrent registrations")
			}

			assert.Equal(t, int32(capN), succeeded.Load())
			count, err := a.CountDCRClients(ctx)
			require.NoError(t, err)
			assert.Equal(t, int64(capN), count)
		})
	})

	t.Run("TTL and index score follow registration and first use", func(t *testing.T) {
		withCappedIntegrationStorage(t, 10, func(ctx context.Context, s, _ *RedisStorage) {
			client := newDCRClient(t, "dcr-ttl", "none", "")
			key := redisKey(s.keyPrefix, KeyTypeClient, "dcr-ttl")

			require.NoError(t, s.RegisterClient(ctx, client))
			pttl, err := s.client.PTTL(ctx, key).Result()
			require.NoError(t, err)
			assert.InDelta(t, DefaultUnusedDCRClientTTL.Seconds(), pttl.Seconds(), 5)

			require.NoError(t, s.RenewClientTTL(ctx, client))
			pttl, err = s.client.PTTL(ctx, key).Result()
			require.NoError(t, err)
			assert.InDelta(t, DefaultDCRClientTTL.Seconds(), pttl.Seconds(), 5)

			score, err := s.client.ZScore(ctx, s.dcrClientIndexKey(), "dcr-ttl").Result()
			require.NoError(t, err)
			assert.InDelta(t, float64(time.Now().Add(pttl).UnixMilli()), score, 5000,
				"the index score must match the row's new expiry")
		})
	})

	t.Run("backfill indexes rows written before the index", func(t *testing.T) {
		withCappedIntegrationStorage(t, 2, func(ctx context.Context, s, _ *RedisStorage) {
			writeStoredClientRow(t, s, "legacy-1", true, 48*time.Hour)
			writeStoredClientRow(t, s, "legacy-2", true, time.Hour)
			writeStoredClientRow(t, s, "configured", false, 0)

			require.NoError(t, s.BackfillDCRClientIndex(ctx))

			members, err := s.client.ZRangeWithScores(ctx, s.dcrClientIndexKey(), 0, -1).Result()
			require.NoError(t, err)
			require.Len(t, members, 2)
			assert.Equal(t, "legacy-2", members[0].Member)
			assert.InDelta(t, float64(time.Now().Add(time.Hour).UnixMilli()), members[0].Score, 5000)
			assert.Equal(t, "legacy-1", members[1].Member)
			assert.InDelta(t, float64(time.Now().Add(48*time.Hour).UnixMilli()), members[1].Score, 5000)

			require.ErrorIs(t, s.RegisterClient(ctx, newDCRClient(t, "new", "none", "")), ErrClientCapacity,
				"the cap counts backfilled rows")
		})
	})
}

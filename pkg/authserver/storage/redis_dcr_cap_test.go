// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// newCappedRedisStorage returns a RedisStorage on mr capped at maxDCR whose
// index scores come from clock. Move time only with advance, which keeps the
// clock and miniredis's key TTLs in step.
func newCappedRedisStorage(t *testing.T, mr *miniredis.Miniredis, maxDCR int, clock *manualClock) *RedisStorage {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewRedisStorageWithClient(client, "test:auth:", WithRedisMaxDCRClients(maxDCR), withRedisClock(clock.Now))
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// advance moves the clock and miniredis's key TTLs forward together.
func advance(clock *manualClock, mr *miniredis.Miniredis, d time.Duration) {
	clock.Advance(d)
	mr.FastForward(d)
}

// clientRowCount returns the number of client rows under the test prefix.
func clientRowCount(t *testing.T, mr *miniredis.Miniredis) int {
	t.Helper()
	n := 0
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, "test:auth:client:") {
			n++
		}
	}
	return n
}

// indexScore returns the DCR cap index score for id as a time.
func indexScore(t *testing.T, mr *miniredis.Miniredis, s *RedisStorage, id string) time.Time {
	t.Helper()
	score, err := mr.ZScore(s.dcrClientIndexKey(), id)
	require.NoError(t, err)
	return time.UnixMilli(int64(score)).UTC()
}

func TestRedisStorage_DCRCap_RejectsAtCap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mr := miniredis.RunT(t)
	clock := newManualClock()
	s := newCappedRedisStorage(t, mr, 3, clock)

	for i := range 3 {
		require.NoError(t, s.RegisterClient(ctx, newDCRClient(t, fmt.Sprintf("dcr-%d", i), "none", "")))
		advance(clock, mr, time.Hour)
	}
	before := clientRowCount(t, mr)

	err := s.RegisterClient(ctx, newDCRClient(t, "dcr-rejected", "none", ""))
	require.ErrorIs(t, err, ErrClientCapacity)
	var capErr *ClientCapacityError
	require.ErrorAs(t, err, &capErr)
	// dcr-0 was registered 3h ago with a 24h TTL.
	assert.Equal(t, DefaultUnusedDCRClientTTL-3*time.Hour, capErr.RetryAfter)
	assert.Equal(t, 3, capErr.Cap)

	assert.Equal(t, before, clientRowCount(t, mr), "a rejected registration must write no row")
	assert.False(t, mr.Exists(redisKey(s.keyPrefix, KeyTypeClient, "dcr-rejected")))
	for i := range 3 {
		_, err := s.GetClient(ctx, fmt.Sprintf("dcr-%d", i))
		require.NoError(t, err, "no existing client may be evicted")
	}

	// An existing ID is a conflict even at the cap, as on the memory store.
	require.ErrorIs(t, s.RegisterClient(ctx, newDCRClient(t, "dcr-0", "none", "")), ErrAlreadyExists)
}

func TestRedisStorage_DCRCap_IgnoresUncountedClients(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mr := miniredis.RunT(t)
	clock := newManualClock()
	s := newCappedRedisStorage(t, mr, 2, clock)

	require.NoError(t, s.ReconcileConfiguredClient(ctx, &fosite.DefaultClient{ID: "configured-1"}))
	require.NoError(t, s.UpsertDCRIssuedClient(ctx,
		newDCRClient(t, "https://cimd.example/client.json", "none", "")))
	require.NoError(t, s.RegisterClient(ctx, newDCRClient(t, "dcr-1", "none", "")))
	require.NoError(t, s.RegisterClient(ctx, newDCRClient(t, "dcr-2", "none", "")),
		"configured and CIMD clients must not use up the cap")
	require.ErrorIs(t, s.RegisterClient(ctx, newDCRClient(t, "dcr-3", "none", "")), ErrClientCapacity)

	// At the cap, uncounted clients are still accepted.
	require.NoError(t, s.ReconcileConfiguredClient(ctx, &fosite.DefaultClient{ID: "configured-2"}))
	require.NoError(t, s.UpsertDCRIssuedClient(ctx,
		newDCRClient(t, "https://cimd.example/other.json", "none", "")))

	count, err := s.CountDCRClients(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
	members, err := mr.ZMembers(s.dcrClientIndexKey())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"dcr-1", "dcr-2"}, members)
}

func TestRedisStorage_DCRCap_FreesOnExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mr := miniredis.RunT(t)
	clock := newManualClock()
	s := newCappedRedisStorage(t, mr, 2, clock)

	require.NoError(t, s.RegisterClient(ctx, newDCRClient(t, "oldest", "none", "")))
	advance(clock, mr, time.Hour)
	require.NoError(t, s.RegisterClient(ctx, newDCRClient(t, "newer", "none", "")))
	require.ErrorIs(t, s.RegisterClient(ctx, newDCRClient(t, "rejected", "none", "")), ErrClientCapacity)

	advance(clock, mr, DefaultUnusedDCRClientTTL-time.Hour)
	require.NoError(t, s.RegisterClient(ctx, newDCRClient(t, "admitted", "none", "")))

	members, err := mr.ZMembers(s.dcrClientIndexKey())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"newer", "admitted"}, members, "the expired entry must be trimmed")
}

func TestRedisStorage_DCRCap_UnlimitedAndDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mr := miniredis.RunT(t)
	clock := newManualClock()
	s := newCappedRedisStorage(t, mr, 0, clock)
	for i := range 50 {
		require.NoError(t, s.RegisterClient(ctx, newDCRClient(t, fmt.Sprintf("dcr-%d", i), "none", "")))
	}

	def := NewRedisStorageWithClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "test:auth:")
	t.Cleanup(func() { _ = def.Close() })
	assert.Equal(t, DefaultMaxDCRClients, def.maxDCRClients)
}

// TestRedisStorage_DCRCap_ConcurrentReplicas races registrations from two
// storage instances sharing one Redis and checks the count never passes the
// cap.
func TestRedisStorage_DCRCap_ConcurrentReplicas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mr := miniredis.RunT(t)
	clock := newManualClock()
	const capN, attempts = 5, 40
	replicas := []*RedisStorage{
		newCappedRedisStorage(t, mr, capN, clock),
		newCappedRedisStorage(t, mr, capN, clock),
	}

	var wg sync.WaitGroup
	var succeeded atomic.Int32
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := replicas[i%2].RegisterClient(ctx, newDCRClient(t, fmt.Sprintf("dcr-%d", i), "none", ""))
			if err == nil {
				succeeded.Add(1)
				return
			}
			assert.ErrorIs(t, err, ErrClientCapacity)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for concurrent registrations")
	}

	assert.Equal(t, int32(capN), succeeded.Load())
	assert.Equal(t, capN, clientRowCount(t, mr))
	count, err := replicas[0].CountDCRClients(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(capN), count)
}

func TestRedisStorage_DCRClientTTLAndIndexScore(t *testing.T) {
	t.Parallel()

	t.Run("new client gets the unused TTL", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		mr := miniredis.RunT(t)
		clock := newManualClock()
		s := newCappedRedisStorage(t, mr, 10, clock)

		require.NoError(t, s.RegisterClient(ctx, newDCRClient(t, "fresh", "none", "")))
		key := redisKey(s.keyPrefix, KeyTypeClient, "fresh")
		assert.Equal(t, DefaultUnusedDCRClientTTL, mr.TTL(key))
		assert.Equal(t, clock.Now().Add(DefaultUnusedDCRClientTTL), indexScore(t, mr, s, "fresh"))
	})

	t.Run("first token exchange raises the TTL and the score", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		mr := miniredis.RunT(t)
		clock := newManualClock()
		s := newCappedRedisStorage(t, mr, 10, clock)

		client := newDCRClient(t, "used", "none", "")
		require.NoError(t, s.RegisterClient(ctx, client))
		advance(clock, mr, time.Hour)
		require.NoError(t, s.RenewClientTTL(ctx, client))

		key := redisKey(s.keyPrefix, KeyTypeClient, "used")
		assert.Equal(t, DefaultDCRClientTTL, mr.TTL(key))
		assert.Equal(t, clock.Now().Add(DefaultDCRClientTTL), indexScore(t, mr, s, "used"))
	})

	t.Run("unused client no longer resolves after 24h", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		mr := miniredis.RunT(t)
		clock := newManualClock()
		s := newCappedRedisStorage(t, mr, 10, clock)

		require.NoError(t, s.RegisterClient(ctx, newDCRClient(t, "unused", "none", "")))
		advance(clock, mr, DefaultUnusedDCRClientTTL)
		_, err := s.GetClient(ctx, "unused")
		requireNotFoundError(t, err)
	})

	t.Run("renewing an expired client does not re-index it", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		mr := miniredis.RunT(t)
		clock := newManualClock()
		s := newCappedRedisStorage(t, mr, 10, clock)

		client := newDCRClient(t, "gone", "none", "")
		require.NoError(t, s.RegisterClient(ctx, client))
		advance(clock, mr, DefaultUnusedDCRClientTTL)
		before := indexScore(t, mr, s, "gone")
		require.NoError(t, s.RenewClientTTL(ctx, client))
		assert.Equal(t, before, indexScore(t, mr, s, "gone"), "the stale entry must not move")
		count, err := s.CountDCRClients(ctx)
		require.NoError(t, err)
		assert.Zero(t, count)
	})

	t.Run("CIMD renewal does not touch the index", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		mr := miniredis.RunT(t)
		clock := newManualClock()
		s := newCappedRedisStorage(t, mr, 10, clock)

		client := newDCRClient(t, "https://cimd.example/client.json", "none", "")
		require.NoError(t, s.UpsertDCRIssuedClient(ctx, client))
		require.NoError(t, s.RenewClientTTL(ctx, client))
		assert.False(t, mr.Exists(s.dcrClientIndexKey()))
	})

	t.Run("upsert refuses a counted client", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		mr := miniredis.RunT(t)
		clock := newManualClock()
		s := newCappedRedisStorage(t, mr, 10, clock)

		err := s.UpsertDCRIssuedClient(ctx, newDCRClient(t, "counted", "none", ""))
		require.ErrorContains(t, err, "must use RegisterClient")
		assert.False(t, mr.Exists(redisKey(s.keyPrefix, KeyTypeClient, "counted")))
		assert.False(t, mr.Exists(s.dcrClientIndexKey()))
	})

	t.Run("configured client reconcile drops a stale index entry", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		mr := miniredis.RunT(t)
		clock := newManualClock()
		s := newCappedRedisStorage(t, mr, 10, clock)

		_, err := mr.ZAdd(s.dcrClientIndexKey(), float64(clock.Now().Add(time.Hour).UnixMilli()), "reused-id")
		require.NoError(t, err)
		require.NoError(t, s.ReconcileConfiguredClient(ctx, &fosite.DefaultClient{ID: "reused-id"}))
		count, err := s.CountDCRClients(ctx)
		require.NoError(t, err)
		assert.Zero(t, count)
	})
}

// writeStoredClientRow writes client as a raw row with ttl and no index entry,
// the way a binary predating the DCR cap index did.
func writeStoredClientRow(t *testing.T, s *RedisStorage, id string, dcr bool, ttl time.Duration) {
	t.Helper()
	var stored storedClient
	if dcr {
		stored = buildStoredClient(newDCRClient(t, id, oauthproto.TokenEndpointAuthMethodNone, ""))
	} else {
		stored = buildStoredClient(&mockClient{id: id})
	}
	data, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NoError(t, s.client.Set(context.Background(), redisKey(s.keyPrefix, KeyTypeClient, id), data, ttl).Err())
}

func TestRedisStorage_BackfillDCRClientIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mr := miniredis.RunT(t)
	clock := newManualClock()
	s := newCappedRedisStorage(t, mr, 3, clock)

	writeStoredClientRow(t, s, "legacy-1", true, 10*24*time.Hour)
	writeStoredClientRow(t, s, "legacy-2", true, 2*24*time.Hour)
	writeStoredClientRow(t, s, "legacy-3", true, time.Hour)
	writeStoredClientRow(t, s, "configured", false, 0)
	writeStoredClientRow(t, s, "https://cimd.example/client.json", true, DefaultDCRClientTTL)

	require.NoError(t, s.BackfillDCRClientIndex(ctx))

	members, err := mr.ZMembers(s.dcrClientIndexKey())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"legacy-1", "legacy-2", "legacy-3"}, members)
	assert.Equal(t, clock.Now().Add(10*24*time.Hour), indexScore(t, mr, s, "legacy-1"))
	assert.Equal(t, clock.Now().Add(2*24*time.Hour), indexScore(t, mr, s, "legacy-2"))
	assert.Equal(t, clock.Now().Add(time.Hour), indexScore(t, mr, s, "legacy-3"))

	// The cap counts backfilled rows.
	require.ErrorIs(t, s.RegisterClient(ctx, newDCRClient(t, "new", "none", "")), ErrClientCapacity)

	// A second run is a no-op: ZADD NX never moves an existing entry.
	advance(clock, mr, time.Minute)
	require.NoError(t, s.BackfillDCRClientIndex(ctx))
	assert.Equal(t, clock.Now().Add(-time.Minute).Add(time.Hour), indexScore(t, mr, s, "legacy-3"))
}

// TestRedisStorage_BackfillDCRClientIndex_WarnsAtCap checks that the backfill
// logs one WARN naming the setting when the backfilled count already fills the
// cap, and stays silent below it or when the cap is unlimited.
//
//nolint:paralleltest // captures the process-global slog default
func TestRedisStorage_BackfillDCRClientIndex_WarnsAtCap(t *testing.T) {
	tests := []struct {
		name     string
		maxDCR   int
		wantWarn bool
	}{
		{name: "at the cap", maxDCR: 2, wantWarn: true},
		{name: "above the cap", maxDCR: 1, wantWarn: true},
		{name: "below the cap", maxDCR: 3},
		{name: "unlimited", maxDCR: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureWarnLogs(t)
			mr := miniredis.RunT(t)
			s := newCappedRedisStorage(t, mr, tc.maxDCR, newManualClock())
			writeStoredClientRow(t, s, "legacy-1", true, time.Hour)
			writeStoredClientRow(t, s, "legacy-2", true, time.Hour)

			require.NoError(t, s.BackfillDCRClientIndex(context.Background()))

			if !tc.wantWarn {
				assert.Empty(t, logs.String())
				return
			}
			out := logs.String()
			assert.Equal(t, 1, strings.Count(out, "DCR client cap already reached"), out)
			assert.Contains(t, out, `"dcr_clients":2`)
			assert.Contains(t, out, fmt.Sprintf(`"max_dcr_clients":%d`, tc.maxDCR))
			assert.Contains(t, out, "storage.max_dcr_clients")
		})
	}
}

// TestRedisStorage_BackfillDCRClientIndex_SkipsUnparsableRows checks that a
// row the backfill cannot parse is skipped rather than failing startup.
func TestRedisStorage_BackfillDCRClientIndex_SkipsUnparsableRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mr := miniredis.RunT(t)
	clock := newManualClock()
	s := newCappedRedisStorage(t, mr, 10, clock)

	writeStoredClientRow(t, s, "good", true, time.Hour)
	require.NoError(t, s.client.Set(ctx, redisKey(s.keyPrefix, KeyTypeClient, "garbage"), "{not json", time.Hour).Err())
	mismatched := buildStoredClient(newDCRClient(t, "other-id", oauthproto.TokenEndpointAuthMethodNone, ""))
	data, err := json.Marshal(mismatched)
	require.NoError(t, err)
	require.NoError(t, s.client.Set(ctx, redisKey(s.keyPrefix, KeyTypeClient, "mismatched"), data, time.Hour).Err())

	require.NoError(t, s.BackfillDCRClientIndex(ctx))

	members, err := mr.ZMembers(s.dcrClientIndexKey())
	require.NoError(t, err)
	assert.Equal(t, []string{"good"}, members)
}

// TestRedisStorage_BackfillDCRClientIndex_ClusterClient checks the backfill
// scans through a ClusterClient, where SCAN must go to the node owning the
// prefix's slot. miniredis answers CLUSTER SLOTS as a single node owning
// every slot.
func TestRedisStorage_BackfillDCRClientIndex_ClusterClient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mr := miniredis.RunT(t)
	clock := newManualClock()
	writer := newCappedRedisStorage(t, mr, 10, clock)
	writeStoredClientRow(t, writer, "legacy", true, time.Hour)

	cc := redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{mr.Addr()}})
	s := NewRedisStorageWithClient(cc, "test:auth:", withRedisClock(clock.Now))
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.BackfillDCRClientIndex(ctx))
	assert.Equal(t, clock.Now().Add(time.Hour), indexScore(t, mr, s, "legacy"))
}

// clusterHashTag returns the part of key Redis Cluster hashes: the text
// between the first '{' and the next '}', when non-empty, else the whole key.
func clusterHashTag(key string) string {
	if start := strings.IndexByte(key, '{'); start >= 0 {
		if end := strings.IndexByte(key[start+1:], '}'); end > 0 {
			return key[start+1 : start+1+end]
		}
	}
	return key
}

// TestRedisStorage_DCRClientIndexKeyLayout pins that the index shares the
// client rows' hash slot, which the register script needs on Redis Cluster,
// and that the backfill's client:* SCAN pattern does not match the index.
func TestRedisStorage_DCRClientIndexKeyLayout(t *testing.T) {
	t.Parallel()
	s := NewRedisStorageWithClient(redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()}),
		DeriveKeyPrefix("ns", "server"))
	t.Cleanup(func() { _ = s.Close() })

	rowKey := redisKey(s.keyPrefix, KeyTypeClient, "some-client")
	assert.Equal(t, "ns:server", clusterHashTag(rowKey))
	assert.Equal(t, clusterHashTag(rowKey), clusterHashTag(s.dcrClientIndexKey()))

	matched, err := path.Match(s.keyPrefix+KeyTypeClient+":*", s.dcrClientIndexKey())
	require.NoError(t, err)
	assert.False(t, matched, "the client SCAN pattern must not match the index key")
}

func TestMaxDCRClientOptions_RejectNegative(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "max DCR clients must not be negative, got -1", func() { WithRedisMaxDCRClients(-1) })
	assert.PanicsWithValue(t, "max DCR clients must not be negative, got -1", func() { WithMaxDCRClients(-1) })
}

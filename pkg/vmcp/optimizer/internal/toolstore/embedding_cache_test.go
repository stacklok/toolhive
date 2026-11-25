// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toolstore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/redisconn"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

const testSharedPrefix = "test:vmcp:"

// newSharedCache returns an EmbeddingCache on its own miniredis, which a test
// can break with SetError to model Redis failing after startup.
func newSharedCache(t *testing.T) (EmbeddingCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	cache, err := NewRedisEmbeddingCache(context.Background(), redisconn.Config{Addr: mr.Addr()}, testSharedPrefix)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })
	return cache, mr
}

// newReplica builds a store over its own database sharing cache, as one vMCP
// replica of several would. Each replica gets its own in-memory database
// because separate processes never share one.
func newReplica(t *testing.T, client types.EmbeddingClient, cache EmbeddingCache) sqliteToolStore {
	t.Helper()
	dsn := fmt.Sprintf("file:replica_%d?mode=memory&cache=shared", testDBCounter.Add(1))
	store, err := newSQLiteToolStore(dsn, client, nil, WithEmbeddingCache(cache))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// reusableRows counts the rows a later build of this store can reuse.
func reusableRows(t *testing.T, store sqliteToolStore) int {
	t.Helper()
	var n int
	require.NoError(t, store.db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM llm_capabilities WHERE embedding IS NOT NULL AND content_hash IS NOT NULL").Scan(&n))
	return n
}

func TestRedisEmbeddingCache_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache, mr := newSharedCache(t)

	require.NoError(t, cache.PutEmbeddings(ctx, map[string][]byte{"a": {1, 2, 3, 4}, "b": {5, 6, 7, 8}}))

	got, err := cache.GetEmbeddings(ctx, []string{"a", "missing", "b"})
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{"a": {1, 2, 3, 4}, "b": {5, 6, 7, 8}}, got,
		"absent keys are omitted, present ones round-trip byte for byte")

	assert.ElementsMatch(t,
		[]string{testSharedPrefix + "optimizer:embedding:a", testSharedPrefix + "optimizer:embedding:b"}, mr.Keys(),
		"vectors live in their own namespace under the key prefix")
	assert.Equal(t, sharedEmbeddingTTL, mr.TTL(testSharedPrefix+"optimizer:embedding:a"),
		"every vector carries the shared TTL")

	require.NoError(t, cache.Close())
	require.NoError(t, cache.Close(), "closing twice is not an error")
}

func TestNewRedisEmbeddingCache_Rejects(t *testing.T) {
	t.Parallel()

	closed := miniredis.RunT(t)
	closedAddr := closed.Addr()
	closed.Close()

	for _, tc := range []struct {
		name      string
		addr      string
		keyPrefix string
		wantErr   string
	}{
		{name: "prefix without separator", addr: "127.0.0.1:6379", keyPrefix: "thv", wantErr: "must end with ':'"},
		{name: "empty prefix", addr: "127.0.0.1:6379", keyPrefix: "", wantErr: "must end with ':'"},
		{name: "unreachable redis", addr: closedAddr, keyPrefix: testSharedPrefix, wantErr: "failed to connect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := NewRedisEmbeddingCache(ctx, redisconn.Config{Addr: tc.addr}, tc.keyPrefix)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestSQLiteToolStore_SharedEmbeddings_ReplicaReusesVectors is the production
// shape the shared cache exists for: after a deploy, each replica's first
// sessions would otherwise embed the whole catalogue again.
func TestSQLiteToolStore_SharedEmbeddings_ReplicaReusesVectors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache, _ := newSharedCache(t)
	tools := catalog(10)

	first := newCountingEmbeddingClient()
	require.NoError(t, newReplica(t, first, cache).UpsertTools(ctx, tools))
	require.Equal(t, len(tools), first.textsEmbedded())

	second := newCountingEmbeddingClient()
	replica := newReplica(t, second, cache)
	require.NoError(t, replica.UpsertTools(ctx, tools))
	assert.Zero(t, second.textsEmbedded(), "a second replica must reuse the vectors the first one shared")
	assert.Equal(t, len(tools), reusableRows(t, replica),
		"shared vectors are committed with their keys, so the replica's own later builds reuse them locally")
}

// TestSQLiteToolStore_SharedEmbeddings_UnverifiedVectorsStayLocal pins that a
// vector embedded while the model id could not be read is never published. It
// is committed hashless locally for the same reason (see fillMisses); shared
// under a key, it would poison every replica instead of one.
func TestSQLiteToolStore_SharedEmbeddings_UnverifiedVectorsStayLocal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache, mr := newSharedCache(t)

	unverified := newInfoDownClient()
	unverified.infoDown.Store(true)
	require.NoError(t, newReplica(t, unverified, cache).UpsertTools(ctx, catalog(5)))

	assert.Empty(t, mr.Keys(), "no vector may be shared under an unverified identity")
}

// TestSQLiteToolStore_SharedEmbeddings_IgnoresUnusableValues covers values that
// cannot be a vector this backend produced. They are recomputed rather than
// reused, since a reused vector is re-stored on every rebuild.
func TestSQLiteToolStore_SharedEmbeddings_IgnoresUnusableValues(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		blob []byte
		// knowWidth has the replica embed once first, so it knows the width.
		knowWidth bool
	}{
		{name: "empty", blob: []byte{}},
		{name: "not whole float32s", blob: []byte{1, 2, 3}},
		{name: "wrong width", blob: make([]byte, 8), knowWidth: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			cache, _ := newSharedCache(t)
			client := newCountingEmbeddingClient()
			replica := newReplica(t, client, cache)
			if tc.knowWidth {
				require.NoError(t, replica.UpsertTools(ctx, makeTools(
					mcp.NewTool("warmup", mcp.WithDescription("Teaches the store the vector width")))))
			}
			before := client.textsEmbedded()

			tools := catalog(1)
			identity, _ := replica.embeddingIdentity(ctx)
			key := embeddingCacheKey(identity, embeddedText(tools[0].Tool.Name, tools[0].Tool.Description))
			require.NoError(t, cache.PutEmbeddings(ctx, map[string][]byte{key: tc.blob}))

			require.NoError(t, replica.UpsertTools(ctx, tools))
			assert.Equal(t, before+1, client.textsEmbedded(), "an unusable shared value must be recomputed")
		})
	}
}

// TestSQLiteToolStore_SharedEmbeddings_RedisFailureDoesNotFailBuilds pins that
// the shared cache only ever costs reuse: with Redis failing, builds embed
// locally and succeed.
func TestSQLiteToolStore_SharedEmbeddings_RedisFailureDoesNotFailBuilds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache, mr := newSharedCache(t)
	mr.SetError("ERR injected failure")

	client := newCountingEmbeddingClient()
	replica := newReplica(t, client, cache)
	require.NoError(t, replica.UpsertTools(ctx, catalog(5)), "a failing shared cache must not fail the build")
	assert.Equal(t, 5, client.textsEmbedded())
	assert.Equal(t, 5, reusableRows(t, replica), "local reuse is unaffected")
}

// TestSQLiteToolStore_UpsertTools_ConcurrentColdBuildsEmbedOnce covers the
// first sessions after a deploy: several build the same catalogue at once on a
// cold store. Each used to miss and embed it in full; taking turns lets every
// build after the first find the committed rows.
func TestSQLiteToolStore_UpsertTools_ConcurrentColdBuildsEmbedOnce(t *testing.T) {
	t.Parallel()

	client := &slowEmbeddingClient{countingEmbeddingClient: newCountingEmbeddingClient(), delay: 100 * time.Millisecond}
	store := newTestStore(t, client, nil)
	tools := catalog(10)

	const sessions = 4
	errs := make(chan error, sessions)
	var wg sync.WaitGroup
	for range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.UpsertTools(context.Background(), tools)
		}()
	}
	waitOrFail(t, &wg, "concurrent cold builds")
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	assert.Equal(t, 1, client.batchCalls(), "the catalogue must be embedded by one build only")
	assert.Equal(t, len(tools), client.textsEmbedded())
}

// TestSQLiteToolStore_UpsertTools_FailedBuildReleasesTurn pins that a build
// that fails while holding the embedding turn gives it back; otherwise every
// later build with a miss would wait forever.
func TestSQLiteToolStore_UpsertTools_FailedBuildReleasesTurn(t *testing.T) {
	t.Parallel()

	client := newFlakyEmbeddingClient()
	store := newTestStore(t, client, nil)

	client.down.Store(true)
	require.Error(t, store.UpsertTools(context.Background(), catalog(3)))

	client.down.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, store.UpsertTools(ctx, catalog(3)), "the next build must get the turn")
}

// hungEmbeddingClient blocks its first EmbedBatch until release is closed,
// modelling a backend that hangs on the build holding the embedding turn.
// Later calls answer at once. entered is closed once the first call blocks.
type hungEmbeddingClient struct {
	*countingEmbeddingClient
	hung    atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func newHungEmbeddingClient() *hungEmbeddingClient {
	return &hungEmbeddingClient{
		countingEmbeddingClient: newCountingEmbeddingClient(),
		entered:                 make(chan struct{}),
		release:                 make(chan struct{}),
	}
}

func (c *hungEmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if c.hung.CompareAndSwap(false, true) {
		close(c.entered)
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.countingEmbeddingClient.EmbedBatch(ctx, texts)
}

// startHungHolder starts a build that takes the embedding turn and hangs in
// the backend until client.release is closed. It returns once the build holds
// the turn, with a channel that receives the build's result.
func startHungHolder(t *testing.T, store sqliteToolStore, client *hungEmbeddingClient) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- store.UpsertTools(context.Background(), catalog(3)) }()
	select {
	case <-client.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for the first build to reach the backend")
	}
	return done
}

// TestSQLiteToolStore_UpsertTools_TurnWaitIsBounded pins that the embedding
// turn only ever saves work: a build stuck on a hung backend delays the builds
// behind it by the turn wait, after which they embed on their own, and a
// waiter whose caller gives up fails without waiting for the holder.
func TestSQLiteToolStore_UpsertTools_TurnWaitIsBounded(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		turnWait      time.Duration
		callerTimeout time.Duration
		wantErr       error
		wantBatches   int
	}{
		{name: "a waiter that times out embeds on its own",
			turnWait: 50 * time.Millisecond, callerTimeout: 10 * time.Second, wantBatches: 2},
		{name: "a waiter whose caller gives up fails",
			turnWait: time.Minute, callerTimeout: 50 * time.Millisecond, wantErr: context.DeadlineExceeded, wantBatches: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := newHungEmbeddingClient()
			dsn := fmt.Sprintf("file:turnwait_%d?mode=memory&cache=shared", testDBCounter.Add(1))
			store, err := newSQLiteToolStore(dsn, client, nil, withEmbedTurnWait(tc.turnWait))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })

			holder := startHungHolder(t, store, client)

			ctx, cancel := context.WithTimeout(context.Background(), tc.callerTimeout)
			defer cancel()
			err = store.UpsertTools(ctx, catalog(3))
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err, "the waiter must not wait for the hung holder")
			}

			close(client.release)
			select {
			case err := <-holder:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("timeout waiting for the holder to finish")
			}
			assert.Equal(t, tc.wantBatches, client.batchCalls())
		})
	}
}

// observedCache wraps an EmbeddingCache, counting lookups and, when stallPut
// is set, holding the first write until release is closed, as a slow Redis
// would. putEntered is closed once that write is held.
type observedCache struct {
	EmbeddingCache
	gets       atomic.Int64
	stallPut   bool
	stalled    atomic.Bool
	putEntered chan struct{}
	release    chan struct{}
}

func newObservedCache(t *testing.T, stallPut bool) *observedCache {
	t.Helper()
	inner, _ := newSharedCache(t)
	return &observedCache{
		EmbeddingCache: inner,
		stallPut:       stallPut,
		putEntered:     make(chan struct{}),
		release:        make(chan struct{}),
	}
}

func (c *observedCache) GetEmbeddings(ctx context.Context, keys []string) (map[string][]byte, error) {
	c.gets.Add(1)
	return c.EmbeddingCache.GetEmbeddings(ctx, keys)
}

func (c *observedCache) PutEmbeddings(ctx context.Context, entries map[string][]byte) error {
	if c.stallPut && c.stalled.CompareAndSwap(false, true) {
		close(c.putEntered)
		<-c.release
	}
	return c.EmbeddingCache.PutEmbeddings(ctx, entries)
}

// TestSQLiteToolStore_SharedEmbeddings_StayOutOfTheTurn pins that no build
// calls the shared cache while it holds the embedding turn, so a slow Redis
// delays only the build that is calling it.
func TestSQLiteToolStore_SharedEmbeddings_StayOutOfTheTurn(t *testing.T) {
	t.Parallel()

	t.Run("a slow publish does not hold the turn", func(t *testing.T) {
		t.Parallel()

		cache := newObservedCache(t, true)
		store := newReplica(t, newCountingEmbeddingClient(), cache)

		first := make(chan error, 1)
		go func() { first <- store.UpsertTools(context.Background(), catalog(3)) }()
		select {
		case <-cache.putEntered:
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for the first build to publish")
		}

		// This build misses and so needs the turn the first one took.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, store.UpsertTools(ctx, makeTools(
			mcp.NewTool("other_tool", mcp.WithDescription("Not in the first build")))),
			"a build must not wait for another build's publish")

		close(cache.release)
		select {
		case err := <-first:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for the first build to finish")
		}
	})

	t.Run("the look after the turn reads only this store", func(t *testing.T) {
		t.Parallel()

		cache := newObservedCache(t, false)
		client := newHungEmbeddingClient()
		store := newReplica(t, client, cache)

		holder := startHungHolder(t, store, client)

		// A superset of the holder's tools: after its turn this build still
		// misses one, which it must embed without asking the cache again.
		waiter := make(chan error, 1)
		go func() { waiter <- store.UpsertTools(context.Background(), catalog(4)) }()
		close(client.release)
		for _, done := range []<-chan error{holder, waiter} {
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("timeout waiting for the builds to finish")
			}
		}

		assert.Equal(t, int64(2), cache.gets.Load(), "each build reads the cache once, before the turn")
		assert.Equal(t, 4, client.textsEmbedded(), "the waiter reuses the holder's rows and embeds only its extra tool")
	})
}

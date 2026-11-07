// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toolstore

import (
	"context"
	"fmt"
	"sync"
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

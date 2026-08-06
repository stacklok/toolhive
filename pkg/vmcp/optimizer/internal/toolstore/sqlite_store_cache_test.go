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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

// countingEmbeddingClient wraps fakeEmbeddingClient and records how many texts
// were sent to the embedding backend, so tests can assert on embedding work
// avoided rather than on wall-clock time.
type countingEmbeddingClient struct {
	*fakeEmbeddingClient
	texts atomic.Int64
	calls atomic.Int64

	mu       sync.Mutex
	embedded []string
}

// countingClientDim is the embedding width used by the counting client. Tests
// that need to vary the width exercise dimension handling directly with
// newFakeEmbeddingClient instead.
const countingClientDim = 384

func newCountingEmbeddingClient() *countingEmbeddingClient {
	return &countingEmbeddingClient{fakeEmbeddingClient: newFakeEmbeddingClient(countingClientDim)}
}

func (c *countingEmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	c.calls.Add(1)
	c.texts.Add(int64(len(texts)))
	c.mu.Lock()
	c.embedded = append(c.embedded, texts...)
	c.mu.Unlock()
	return c.fakeEmbeddingClient.EmbedBatch(ctx, texts)
}

// textsEmbedded returns the total number of texts sent to the backend.
func (c *countingEmbeddingClient) textsEmbedded() int { return int(c.texts.Load()) }

// batchCalls returns the number of EmbedBatch round-trips made to the backend.
func (c *countingEmbeddingClient) batchCalls() int { return int(c.calls.Load()) }

// embeddedTexts returns a copy of every text sent to the backend, in order.
func (c *countingEmbeddingClient) embeddedTexts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.embedded...)
}

// slowEmbeddingClient holds each EmbedBatch open long enough for concurrent
// builds to overlap, modelling a real embedding backend where a batch takes
// seconds. Without the delay a lock conflict between concurrent builds is a
// race that usually resolves before it can be observed.
type slowEmbeddingClient struct {
	*countingEmbeddingClient
	delay time.Duration
}

func (c *slowEmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case <-time.After(c.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return c.countingEmbeddingClient.EmbedBatch(ctx, texts)
}

// TestSQLiteToolStore_UpsertTools_ConcurrentBuilds covers the production shape
// the embedding cache targets: one shared store, several sessions registering
// at once, each building over its own tool set against a slow backend.
//
// The existing concurrency tests pass a nil embedding client, so they never
// reach the embedding path at all.
func TestSQLiteToolStore_UpsertTools_ConcurrentBuilds(t *testing.T) {
	t.Parallel()

	client := &slowEmbeddingClient{countingEmbeddingClient: newCountingEmbeddingClient(), delay: 250 * time.Millisecond}
	store := newTestStore(t, client, nil)
	ctx := context.Background()

	const sessions = 4
	errs := make(chan error, sessions)
	var wg sync.WaitGroup
	for i := range sessions {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tools := makeTools(mcp.NewTool(
				fmt.Sprintf("session_%d_tool", idx),
				mcp.WithDescription(fmt.Sprintf("Tool contributed by session %d", idx)),
			))
			errs <- store.UpsertTools(ctx, tools)
		}(i)
	}
	waitOrFail(t, &wg, "concurrent session builds")
	close(errs)

	for err := range errs {
		require.NoError(t, err, "concurrent session builds must not contend on the store")
	}
}

// waitOrFail blocks until wg completes, failing instead of hanging if it does
// not. A store deadlock — the failure these concurrency tests exist to catch —
// would otherwise stall the run until the global go test timeout, with no
// indication of which test was stuck.
func waitOrFail(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("timeout waiting for %s to finish", what)
	}
}

// newTestStoreDSN builds a store over an explicit database name so several
// stores can share one database, as successive processes would.
func newTestStoreDSN(t *testing.T, dsn string, client types.EmbeddingClient) sqliteToolStore {
	t.Helper()
	store, err := newSQLiteToolStore(dsn, client, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func catalog(n int) []server.ServerTool {
	tools := make([]server.ServerTool, n)
	for i := range n {
		tools[i] = server.ServerTool{
			Tool: mcp.Tool{
				Name:        fmt.Sprintf("tool_%04d", i),
				Description: fmt.Sprintf("Tool number %d performing operation %d", i, i%20),
			},
		}
	}
	return tools
}

// TestSQLiteToolStore_UpsertTools_ReusesCachedEmbeddings locks in the core
// guarantee behind stacklok/toolhive#5847: re-upserting an unchanged tool set
// must not re-embed it.
//
// The Serve path builds a per-session optimizer, and every build calls
// UpsertTools over the session's whole tool set. Without embedding reuse the
// cost is O(tools x sessions) and lands on the client's initialize/tools/list
// round-trip; with reuse it is O(tools whose text changed).
func TestSQLiteToolStore_UpsertTools_ReusesCachedEmbeddings(t *testing.T) {
	t.Parallel()

	client := newCountingEmbeddingClient()
	store := newTestStore(t, client, nil)
	ctx := context.Background()

	tools := catalog(10)

	require.NoError(t, store.UpsertTools(ctx, tools), "first build")
	require.Equal(t, 10, client.textsEmbedded(), "the first build must embed every tool")

	// Three more sessions registering against an unchanged catalog.
	for i := range 3 {
		require.NoError(t, store.UpsertTools(ctx, tools), "rebuild %d", i)
	}

	assert.Equal(t, 10, client.textsEmbedded(),
		"an unchanged tool set must not be re-embedded on subsequent builds")
	assert.Equal(t, 1, client.batchCalls(),
		"rebuilds over an unchanged tool set must not reach the embedding backend at all")
}

// TestSQLiteToolStore_UpsertTools_ReEmbedsOnlyChangedTools asserts the cache is
// keyed on tool content, not merely on presence: a tool whose description
// changed must be re-embedded, and only that tool.
func TestSQLiteToolStore_UpsertTools_ReEmbedsOnlyChangedTools(t *testing.T) {
	t.Parallel()

	client := newCountingEmbeddingClient()
	store := newTestStore(t, client, nil)
	ctx := context.Background()

	tools := catalog(10)
	require.NoError(t, store.UpsertTools(ctx, tools))
	require.Equal(t, 10, client.textsEmbedded())

	// One backend redeploys with a reworded description.
	changed := catalog(10)
	changed[4].Tool.Description = "Reworded description for tool 4"
	require.NoError(t, store.UpsertTools(ctx, changed))

	assert.Equal(t, 11, client.textsEmbedded(),
		"only the tool whose embedded text changed may be re-embedded")

	embedded := client.embeddedTexts()
	assert.Contains(t, embedded[len(embedded)-1], "Reworded description for tool 4",
		"the re-embedded text must be the changed tool")
}

// TestSQLiteToolStore_UpsertTools_EmbeddingIdentityInvalidatesCache asserts
// that stored vectors are never reused across a change of embedding backend.
//
// An embedding is only interchangeable with another produced by the same
// provider, endpoint, and model. Keying the cache on the tool text alone would
// serve vectors from a different semantic space after an operator repoints the
// embedding service — silently degrading find_tool rather than failing.
func TestSQLiteToolStore_UpsertTools_EmbeddingIdentityInvalidatesCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		next *types.OptimizerConfig
		// reuse is true when the second store must reuse the first store's vectors.
		reuse bool
	}{
		{
			name:  "same backend reuses",
			next:  &types.OptimizerConfig{EmbeddingProvider: "tei", EmbeddingService: "http://tei:8080"},
			reuse: true,
		},
		{
			name:  "different endpoint re-embeds",
			next:  &types.OptimizerConfig{EmbeddingProvider: "tei", EmbeddingService: "http://other:8080"},
			reuse: false,
		},
		{
			name:  "different provider re-embeds",
			next:  &types.OptimizerConfig{EmbeddingProvider: "openai", EmbeddingService: "http://tei:8080"},
			reuse: false,
		},
		{
			name: "different model re-embeds",
			next: &types.OptimizerConfig{
				EmbeddingProvider: "tei", EmbeddingService: "http://tei:8080", EmbeddingModel: "bge-large",
			},
			reuse: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			dsn := fmt.Sprintf("file:identitydb_%d?mode=memory&cache=shared", testDBCounter.Add(1))
			tools := catalog(5)

			first := &types.OptimizerConfig{EmbeddingProvider: "tei", EmbeddingService: "http://tei:8080"}
			store, err := newSQLiteToolStore(dsn, newCountingEmbeddingClient(), first)
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			require.NoError(t, store.UpsertTools(ctx, tools))

			// A second store over the same database, as after a config change.
			nextClient := newCountingEmbeddingClient()
			nextStore, err := newSQLiteToolStore(dsn, nextClient, tc.next)
			require.NoError(t, err)
			t.Cleanup(func() { _ = nextStore.Close() })
			require.NoError(t, nextStore.UpsertTools(ctx, tools))

			if tc.reuse {
				assert.Zero(t, nextClient.textsEmbedded(), "an unchanged backend must reuse stored vectors")
				return
			}
			assert.Equal(t, len(tools), nextClient.textsEmbedded(),
				"a changed embedding backend must re-embed every tool")
		})
	}
}

// TestSQLiteToolStore_UpsertTools_RepairsStaleDimension asserts that a stored
// vector left behind by a previous model is recomputed rather than reused
// forever.
//
// Reuse would otherwise make the damage permanent: the stale vector is handed
// back and re-stored on every rebuild while search discards it as
// incomparable, so the tool disappears from semantic results for good. Before
// embedding reuse the next session re-embedded everything and self-healed.
func TestSQLiteToolStore_UpsertTools_RepairsStaleDimension(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := fmt.Sprintf("file:repairdb_%d?mode=memory&cache=shared", testDBCounter.Add(1))
	tools := makeTools(mcp.NewTool("archive_file", mcp.WithDescription("Archive a file to cold storage")))

	// Indexed by the outgoing model.
	old, err := newSQLiteToolStore(dsn, newFakeEmbeddingClient(384), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = old.Close() })
	require.NoError(t, old.UpsertTools(ctx, tools))

	// The embedding container is replaced by a model of a different width, with
	// no config change: same provider, same service URL.
	store, err := newSQLiteToolStore(dsn, newFakeEmbeddingClient(768), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	allowed := []string{"archive_file"}

	// The first search cannot rank the stale vector, but it does reveal the
	// backend's current width to the store.
	before, err := store.searchSemantic(ctx, "store a file", allowed, DefaultMaxToolsToReturn)
	require.NoError(t, err)
	require.Empty(t, before, "a vector of the previous width must not be ranked")

	require.NoError(t, store.UpsertTools(ctx, tools), "rebuild after the model change")

	after, err := store.searchSemantic(ctx, "store a file", allowed, DefaultMaxToolsToReturn)
	require.NoError(t, err)
	assert.Equal(t, []string{"archive_file"}, matchNames(after),
		"the rebuild must recompute the stale vector so the tool returns to semantic search")
}

// TestSQLiteToolStore_UpsertTools_IgnoresEmptyStoredEmbedding asserts that a
// zero-length stored vector is recomputed and, above all, that no tool is ever
// handed a different tool's embedding.
//
// An empty blob is not reusable but still matches on content_hash. Treated as a
// hit it would leave a gap that a positional lookup could fill with an unrelated
// vector, silently corrupting search for that tool.
//
// The rebuild deliberately runs on a store that has not yet observed the
// backend's vector width, which is the only state where the width check cannot
// mask an empty blob — a fresh process whose first build is all cache hits.
func TestSQLiteToolStore_UpsertTools_IgnoresEmptyStoredEmbedding(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := fmt.Sprintf("file:emptydb_%d?mode=memory&cache=shared", testDBCounter.Add(1))

	tools := makeTools(
		mcp.NewTool("alpha", mcp.WithDescription("First tool")),
		mcp.NewTool("bravo", mcp.WithDescription("Second tool")),
		mcp.NewTool("charlie", mcp.WithDescription("Third tool")),
	)

	seed, err := newSQLiteToolStore(dsn, newCountingEmbeddingClient(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = seed.Close() })
	require.NoError(t, seed.UpsertTools(ctx, tools))

	// Corrupt one row the way a backend returning an empty vector would.
	_, err = seed.db.ExecContext(ctx, "UPDATE llm_capabilities SET embedding = ? WHERE name = ?", []byte{}, "alpha")
	require.NoError(t, err)

	// A restarted process: same database, no observed vector width yet.
	client := newCountingEmbeddingClient()
	store, err := newSQLiteToolStore(dsn, client, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.Zero(t, store.embeddingDim.Load(), "the width must still be unknown for this test to be meaningful")

	require.NoError(t, store.UpsertTools(ctx, tools), "rebuild over the corrupted row")

	var alpha, charlie []byte
	require.NoError(t, store.db.QueryRowContext(ctx,
		"SELECT embedding FROM llm_capabilities WHERE name = ?", "alpha").Scan(&alpha))
	require.NoError(t, store.db.QueryRowContext(ctx,
		"SELECT embedding FROM llm_capabilities WHERE name = ?", "charlie").Scan(&charlie))

	assert.NotEmpty(t, alpha, "an unusable stored vector must be recomputed")
	assert.NotEqual(t, charlie, alpha, "a tool must never be given another tool's embedding")

	// The expected text is spelled out rather than built with embeddedText: the
	// cache key is only meaningful if that format is exactly this, so deriving
	// the expectation from the code under test would hide a format change.
	want, err := client.Embed(ctx, "name: alpha description: First tool")
	require.NoError(t, err)
	assert.Equal(t, encodeEmbedding(want), alpha, "the recomputed vector must be alpha's own")
}

// shiftedEmbeddingClient produces vectors of the configured width that differ
// from fakeEmbeddingClient's for the same text, standing in for a replacement
// model of identical width — the swap neither the content hash nor the
// dimension check can see.
type shiftedEmbeddingClient struct {
	*fakeEmbeddingClient
	calls atomic.Int64
}

func newShiftedEmbeddingClient() *shiftedEmbeddingClient {
	return &shiftedEmbeddingClient{fakeEmbeddingClient: newFakeEmbeddingClient(countingClientDim)}
}

func (c *shiftedEmbeddingClient) Embed(ctx context.Context, text string) ([]float32, error) {
	vec, err := c.fakeEmbeddingClient.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	for i := range vec {
		vec[i] = -vec[i]
	}
	return vec, nil
}

func (c *shiftedEmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	c.calls.Add(1)
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := c.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

// swappableEmbeddingClient returns one model's vectors until swap() is called,
// then a different model's — same width, same client, same URL.
//
// This is what redeploying the embedding service with a different model looks
// like to a running vmcp: the Service URL is unchanged, so the store keeps the
// same client and never learns the backend moved.
type swappableEmbeddingClient struct {
	*fakeEmbeddingClient
	swapped atomic.Bool
	texts   atomic.Int64
}

func newSwappableEmbeddingClient() *swappableEmbeddingClient {
	return &swappableEmbeddingClient{fakeEmbeddingClient: newFakeEmbeddingClient(countingClientDim)}
}

func (c *swappableEmbeddingClient) swap() { c.swapped.Store(true) }

// Embed counts every text, including the backend probe, which is issued here
// rather than through EmbedBatch.
func (c *swappableEmbeddingClient) Embed(ctx context.Context, text string) ([]float32, error) {
	c.texts.Add(1)
	vec, err := c.fakeEmbeddingClient.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	if c.swapped.Load() {
		for i := range vec {
			vec[i] = -vec[i]
		}
	}
	return vec, nil
}

func (c *swappableEmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := c.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

func (c *swappableEmbeddingClient) embedded() int { return int(c.texts.Load()) }

// TestSQLiteToolStore_BackendChange_DiscardsStaleEmbeddings is the regression
// test for the failure this cache would otherwise introduce.
//
// A replacement model of the same width is invisible to the content hash (the
// config is unchanged) and to the dimension check (the width is unchanged), so
// without the backend probe the stale vectors would be reused and re-stored on
// every later build — permanently, where re-embedding every session used to heal
// it on its own.
//
// The topology matters: production creates ONE store per process
// (NewOptimizerFactory) and every session build calls UpsertTools on it, so the
// swap must be exercised as successive builds on a single store. Two stores over
// one DSN would test a configuration that does not exist.
func TestSQLiteToolStore_BackendChange_DiscardsStaleEmbeddings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newSwappableEmbeddingClient()
	store := newTestStore(t, client, nil)
	tools := catalog(5)

	require.NoError(t, store.UpsertTools(ctx, tools), "first build")
	afterCold := client.embedded()
	require.Equal(t, len(tools)+1, afterCold, "cold build embeds every tool plus the probe")

	require.NoError(t, store.UpsertTools(ctx, tools), "second build, backend unchanged")
	afterWarm := client.embedded()
	require.Equal(t, afterCold+1, afterWarm,
		"an unchanged backend re-embeds only the probe")

	// The embedding service is redeployed with a different model. Same URL, same
	// client, same store — only the vectors change.
	client.swap()
	require.NoError(t, store.UpsertTools(ctx, tools), "build after the model swap")

	assert.Equal(t, afterWarm+1+len(tools), client.embedded(),
		"a changed backend must re-embed every tool, not just the probe")

	var stored []byte
	require.NoError(t, store.db.QueryRowContext(ctx,
		"SELECT embedding FROM llm_capabilities WHERE name = ?", tools[0].Tool.Name).Scan(&stored))
	want, err := client.Embed(ctx, embeddedText(tools[0].Tool.Name, tools[0].Tool.Description))
	require.NoError(t, err)
	assert.Equal(t, encodeEmbedding(want), stored,
		"stored vectors must come from the current backend, not the previous one")
}

// TestSQLiteToolStore_BackendUnreachable_StillServesTools pins the availability
// property this cache buys: once warm, a build survives the embedding backend
// being completely down.
//
// Measured live on 2026-07-25 — all four TEI pods deleted, a session built
// normally from cache. It is asserted here because that scenario cannot run in
// CI, and because it is fragile: making the probe refuse reuse on failure
// silently destroys it, turning a working build into a client with no tools.
// That is the original stacklok/toolhive#5847 symptom.
func TestSQLiteToolStore_BackendUnreachable_StillServesTools(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newFlakyEmbeddingClient()
	store := newTestStore(t, client, nil)
	tools := catalog(20)

	require.NoError(t, store.UpsertTools(ctx, tools), "warm the cache while the backend is up")

	// The embedding backend goes away entirely.
	client.down.Store(true)
	before := client.embedded()

	require.NoError(t, store.UpsertTools(ctx, tools),
		"a warm build must survive the embedding backend being unreachable")
	assert.Equal(t, before, client.embedded(),
		"nothing may be re-embedded while the backend is down")

	var stored int
	require.NoError(t, store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM llm_capabilities WHERE embedding IS NOT NULL").Scan(&stored))
	assert.Equal(t, len(tools), stored,
		"an unverifiable backend must not cause stored vectors to be discarded")
}

// flakyEmbeddingClient fails every call once down is set, modelling the
// embedding service being unreachable.
type flakyEmbeddingClient struct {
	*fakeEmbeddingClient
	down  atomic.Bool
	texts atomic.Int64
}

func newFlakyEmbeddingClient() *flakyEmbeddingClient {
	return &flakyEmbeddingClient{fakeEmbeddingClient: newFakeEmbeddingClient(countingClientDim)}
}

func (c *flakyEmbeddingClient) Embed(ctx context.Context, text string) ([]float32, error) {
	if c.down.Load() {
		return nil, fmt.Errorf("embedding backend unreachable")
	}
	c.texts.Add(1)
	return c.fakeEmbeddingClient.Embed(ctx, text)
}

func (c *flakyEmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := c.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

func (c *flakyEmbeddingClient) embedded() int { return int(c.texts.Load()) }

// blockingProbeClient holds the probe open until released, so a test can
// observe what a concurrent build does while a probe is in flight.
type blockingProbeClient struct {
	*fakeEmbeddingClient
	entered chan struct{}
	release chan struct{}
}

func newBlockingProbeClient() *blockingProbeClient {
	return &blockingProbeClient{
		fakeEmbeddingClient: newFakeEmbeddingClient(countingClientDim),
		entered:             make(chan struct{}, 1),
		release:             make(chan struct{}),
	}
}

func (c *blockingProbeClient) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == canaryText {
		select {
		case c.entered <- struct{}{}:
		default:
		}
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.fakeEmbeddingClient.Embed(ctx, text)
}

func (c *blockingProbeClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := c.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

// TestSQLiteToolStore_ConcurrentBuilds_OrderedAfterProbe asserts no build reads
// the cache while a probe is in flight.
//
// A probe may be about to discard the vectors a concurrent build is reading. If
// that build is allowed to proceed, its INSERT OR REPLACE writes the stale
// vector AND its content hash back after the discard commits. The identity is
// unchanged on a same-width backend swap, so later builds recompute the same
// hash, find the restored row, and reuse it — while the freshly written probe
// certifies the store as current, so nothing re-checks. Permanently stale, with
// nothing logged.
//
// Found by cross-model review; the earlier TryLock implementation had exactly
// this hole.
func TestSQLiteToolStore_ConcurrentBuilds_OrderedAfterProbe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newBlockingProbeClient()
	store := newTestStore(t, client, nil)

	go func() { _ = store.UpsertTools(ctx, catalog(3)) }()
	<-client.entered // a probe is now in flight and holding the lock

	done := make(chan struct{})
	go func() {
		_ = store.UpsertTools(ctx, makeTools(
			mcp.NewTool("concurrent", mcp.WithDescription("Indexed while a probe is in flight"))))
		close(done)
	}()

	select {
	case <-done:
		close(client.release)
		t.Fatal("a build completed while a probe was in flight; it can write pre-discard state back")
	case <-time.After(600 * time.Millisecond):
		// Correct: the second build is ordered behind the probe.
	}
	close(client.release)

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for the queued build to finish")
	}
}

// TestSQLiteToolStore_BackendUnchanged_KeepsReuse asserts the probe does not
// invalidate the cache when the backend is the same, which would silently undo
// the reuse this cache exists for.
func TestSQLiteToolStore_BackendUnchanged_KeepsReuse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := fmt.Sprintf("file:canarysame_%d?mode=memory&cache=shared", testDBCounter.Add(1))
	tools := catalog(5)

	first := newTestStoreDSN(t, dsn, newCountingEmbeddingClient())
	require.NoError(t, first.UpsertTools(ctx, tools))

	second := newCountingEmbeddingClient()
	rebuilt := newTestStoreDSN(t, dsn, second)
	require.NoError(t, rebuilt.UpsertTools(ctx, tools))

	assert.Zero(t, second.textsEmbedded(),
		"an unchanged backend must keep every stored vector reusable")
}

// TestSQLiteToolStore_BackendChange_PreservesKeywordSearch asserts the stale
// vectors are cleared without removing the rows, which also back the
// external-content FTS5 index.
func TestSQLiteToolStore_BackendChange_PreservesKeywordSearch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := fmt.Sprintf("file:canaryfts_%d?mode=memory&cache=shared", testDBCounter.Add(1))
	indexed := newTestStoreDSN(t, dsn, newCountingEmbeddingClient())
	require.NoError(t, indexed.UpsertTools(ctx, makeTools(
		mcp.NewTool("archive_file", mcp.WithDescription("Archive a file to cold storage")))))

	// A different backend, and a build whose tool set does not include the
	// previously indexed tool.
	swapped := newTestStoreDSN(t, dsn, newShiftedEmbeddingClient())
	require.NoError(t, swapped.UpsertTools(ctx, makeTools(
		mcp.NewTool("unrelated_tool", mcp.WithDescription("Something else entirely")))))

	found, err := swapped.searchFTS5(ctx, "archive", []string{"archive_file"}, DefaultMaxToolsToReturn)
	require.NoError(t, err)
	assert.Equal(t, []string{"archive_file"}, matchNames(found),
		"discarding stale vectors must not remove the rows backing keyword search")
}

// probeCountingClient counts probe embeds and holds each one open, so a test
// can tell whether concurrent builds queue behind the probe or skip it.
type probeCountingClient struct {
	*fakeEmbeddingClient
	probes atomic.Int64
	delay  time.Duration
}

func (c *probeCountingClient) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == canaryText {
		c.probes.Add(1)
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.fakeEmbeddingClient.Embed(ctx, text)
}

func (c *probeCountingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := c.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

// TestSQLiteToolStore_ConcurrentBuilds_ShareOneProbe asserts that concurrent
// builds skip the probe when one is already in flight, rather than queueing
// behind it.
//
// The probe runs on every build and costs a network round-trip. Serializing it
// would put one round-trip per concurrent session on the tools/list path:
// measured at 4 builds x a 200ms probe = 805ms before this, 201ms after. The
// probe is idempotent, so its result applies to every build racing it.
func TestSQLiteToolStore_ConcurrentBuilds_ShareOneProbe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &probeCountingClient{
		fakeEmbeddingClient: newFakeEmbeddingClient(countingClientDim),
		delay:               200 * time.Millisecond,
	}
	store := newTestStore(t, client, nil)

	// Warm first so the concurrent builds do the probe and nothing else.
	require.NoError(t, store.UpsertTools(ctx, catalog(5)))
	client.probes.Store(0)

	const builds = 4
	var wg sync.WaitGroup
	errs := make(chan error, builds)
	for i := range builds {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs <- store.UpsertTools(ctx, makeTools(mcp.NewTool(
				fmt.Sprintf("concurrent_%d", idx), mcp.WithDescription("A concurrently indexed tool"))))
		}(i)
	}
	waitOrFail(t, &wg, "concurrent probing builds")
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	assert.Less(t, int(client.probes.Load()), builds,
		"concurrent builds must skip an in-flight probe, not queue behind it")
}

// TestSQLiteToolStore_BackendProbe_RunsOnce asserts concurrent first builds
// probe the backend once rather than once each.
func TestSQLiteToolStore_BackendProbe_RunsOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newCountingEmbeddingClient()
	store := newTestStore(t, client, nil)

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := range 4 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs <- store.UpsertTools(ctx, makeTools(mcp.NewTool(
				fmt.Sprintf("tool_%d", idx), mcp.WithDescription("A concurrently indexed tool"))))
		}(i)
	}
	waitOrFail(t, &wg, "concurrent first builds")
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var canaries int
	require.NoError(t, store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM embedding_canary").Scan(&canaries))
	assert.Equal(t, 1, canaries, "the probe must record exactly one canary")
}

// TestEmbeddedText pins the exact string sent to the embedding backend. Every
// stored cache key is a hash of it, so changing the format silently invalidates
// every entry — it must be a deliberate edit here, with a cacheKeyVersion bump.
func TestEmbeddedText(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "name: read_file description: Read a file", embeddedText("read_file", "Read a file"))
}

// TestEmbeddingCacheKey_Injective asserts that distinct inputs never share a
// cache key.
//
// The parts are length-prefixed rather than delimiter-joined precisely because
// a delimiter is ambiguous: with "v1\x00"+identity+"\x00"+text, moving a NUL
// across the boundary produces an identical byte stream. Tool names and
// descriptions come from aggregated backend servers, so a backend could craft a
// description that collides with another tool's key and take over its embedding.
func TestEmbeddingCacheKey_Injective(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		aID, aText      string
		bID, bText      string
		wantSameAsFirst bool
	}{
		{
			name: "identical inputs agree",
			aID:  "tei", aText: "name: A description: B",
			bID: "tei", bText: "name: A description: B",
			wantSameAsFirst: true,
		},
		{
			name: "NUL shifted across the identity/text boundary",
			aID:  "tei\x00http://x\x00", aText: "name: A description: B",
			bID: "tei\x00http://x", bText: "\x00name: A description: B",
		},
		{
			name: "content moved between fields",
			aID:  "abc", aText: "def",
			bID: "ab", bText: "cdef",
		},
		{
			name: "different identity, same text",
			aID:  "tei", aText: "name: A description: B",
			bID: "openai", bText: "name: A description: B",
		},
		{
			name: "same identity, different text",
			aID:  "tei", aText: "name: A description: B",
			bID: "tei", bText: "name: A description: C",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := embeddingCacheKey(tc.aID, tc.aText)
			b := embeddingCacheKey(tc.bID, tc.bText)
			if tc.wantSameAsFirst {
				assert.Equal(t, a, b, "identical inputs must produce one key")
				return
			}
			assert.NotEqual(t, a, b, "distinct inputs must not share a cache key")
		})
	}
}

// FuzzEmbeddingCacheKey checks the same injectivity property over arbitrary
// inputs: two key derivations agree only when their inputs do.
func FuzzEmbeddingCacheKey(f *testing.F) {
	f.Add("tei", "name: A description: B", "tei", "name: A description: B")
	f.Add("tei\x00svc\x00", "name: A", "tei\x00svc", "\x00name: A")
	f.Add("", "", "", "")

	f.Fuzz(func(t *testing.T, aID, aText, bID, bText string) {
		sameKey := embeddingCacheKey(aID, aText) == embeddingCacheKey(bID, bText)
		sameInput := aID == bID && aText == bText
		if sameKey != sameInput {
			t.Fatalf("key equality %v but input equality %v for (%q,%q) vs (%q,%q)",
				sameKey, sameInput, aID, aText, bID, bText)
		}
	})
}

// TestSQLiteToolStore_UpsertTools_FTS5OnlyStoresNoHash covers the branch taken
// when no embedding client is configured: rows must carry neither an embedding
// nor a content hash, so nothing can later be mistaken for a reusable vector.
func TestSQLiteToolStore_UpsertTools_FTS5OnlyStoresNoHash(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore(t, nil, nil)
	require.NoError(t, store.UpsertTools(ctx, makeTools(
		mcp.NewTool("read_file", mcp.WithDescription("Read a file")))))

	var embedding, hash any
	require.NoError(t, store.db.QueryRowContext(ctx,
		"SELECT embedding, content_hash FROM llm_capabilities WHERE name = ?", "read_file").
		Scan(&embedding, &hash))

	assert.Nil(t, embedding, "FTS5-only mode must store no embedding")
	assert.Nil(t, hash, "FTS5-only mode must store no content hash")
}

// TestSQLiteToolStore_SearchSemantic_SkipsMismatchedDimensions asserts that a
// stored vector whose dimension differs from the query's is dropped from the
// ranking instead of crashing the search.
//
// Cosine distance indexes both slices positionally, so a shorter stored vector
// panics. Before embedding reuse every vector was rewritten on each session and
// dimensions could not diverge; with reuse, a vector can outlive the model that
// produced it.
func TestSQLiteToolStore_SearchSemantic_SkipsMismatchedDimensions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := fmt.Sprintf("file:dimdb_%d?mode=memory&cache=shared", testDBCounter.Add(1))

	stale := makeTools(mcp.NewTool("stale_tool", mcp.WithDescription("Indexed by the previous model")))
	store, err := newSQLiteToolStore(dsn, newFakeEmbeddingClient(384), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.UpsertTools(ctx, stale))

	// The model is replaced by one with a different output dimension.
	wider, err := newSQLiteToolStore(dsn, newFakeEmbeddingClient(768), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = wider.Close() })

	fresh := makeTools(mcp.NewTool("fresh_tool", mcp.WithDescription("Indexed by the current model")))
	require.NoError(t, wider.UpsertTools(ctx, fresh))

	allowed := []string{"stale_tool", "fresh_tool"}
	require.NotPanics(t, func() {
		results, searchErr := wider.searchSemantic(ctx, "indexed by a model", allowed, DefaultMaxToolsToReturn)
		require.NoError(t, searchErr)
		assert.Equal(t, []string{"fresh_tool"}, matchNames(results),
			"only vectors comparable with the query may be ranked")
	})
}

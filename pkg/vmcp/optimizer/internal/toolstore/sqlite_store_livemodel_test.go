// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toolstore

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/similarity"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

// Environment variables gating the live model-swap tests. Two models are
// required: one whose embedding width differs from the baseline, and one whose
// width is identical, since the two swaps are handled by different mechanisms.
const (
	liveEmbedURLEnv       = "VMCP_LIVE_EMBEDDING_URL"
	liveModelBaseEnv      = "VMCP_LIVE_MODEL_BASE"
	liveModelSameWidthEnv = "VMCP_LIVE_MODEL_SAME_WIDTH"
	liveModelDiffWidthEnv = "VMCP_LIVE_MODEL_DIFF_WIDTH"
)

// liveModelStore builds a store against a real OpenAI-compatible embedding
// endpoint, sharing dsn so successive stores see the same rows.
func liveModelStore(t *testing.T, dsn, endpoint, model string) sqliteToolStore {
	t.Helper()
	cfg := &types.OptimizerConfig{
		EmbeddingService:        endpoint,
		EmbeddingProvider:       types.EmbeddingProviderOpenAI,
		EmbeddingModel:          model,
		EmbeddingServiceTimeout: time.Minute,
	}
	client, err := similarity.NewEmbeddingClient(cfg)
	require.NoError(t, err)
	store, err := newSQLiteToolStore(dsn, client, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// liveModelEndpoint returns the configured endpoint, or skips the test.
func liveModelEndpoint(t *testing.T) string {
	t.Helper()
	endpoint := os.Getenv(liveEmbedURLEnv)
	if endpoint == "" {
		t.Skipf("%s not set; skipping live embedding model test", liveEmbedURLEnv)
	}
	return endpoint
}

// TestLiveModelSwap_DifferentWidth verifies against real models that swapping
// the embedding model to one of a different width does not corrupt search: the
// stale vector must be discarded, then repaired on the next build.
//
// The unit tests cover this with a fake client, which cannot show that real
// vectors of two widths coexist in one column without panicking cosine distance.
func TestLiveModelSwap_DifferentWidth(t *testing.T) {
	t.Parallel()

	endpoint := liveModelEndpoint(t)
	base := cmp.Or(os.Getenv(liveModelBaseEnv), "bge-m3")
	other := cmp.Or(os.Getenv(liveModelDiffWidthEnv), "all-minilm")

	ctx := context.Background()
	dsn := fmt.Sprintf("file:livediff_%d?mode=memory&cache=shared", testDBCounter.Add(1))
	tools := makeTools(mcp.NewTool("archive_file", mcp.WithDescription("Archive a file to cold storage")))

	indexed := liveModelStore(t, dsn, endpoint, base)
	require.NoError(t, indexed.UpsertTools(ctx, tools))

	swapped := liveModelStore(t, dsn, endpoint, other)
	allowed := []string{"archive_file"}

	before, err := swapped.searchSemantic(ctx, "store a file", allowed, DefaultMaxToolsToReturn)
	require.NoError(t, err, "a stale-width vector must not panic cosine distance")
	assert.Empty(t, before, "a vector of the previous width must not be ranked")

	require.NoError(t, swapped.UpsertTools(ctx, tools), "rebuild after the model change")

	after, err := swapped.searchSemantic(ctx, "store a file", allowed, DefaultMaxToolsToReturn)
	require.NoError(t, err)
	assert.Equal(t, []string{"archive_file"}, matchNames(after),
		"the rebuild must recompute the stale vector so the tool returns to semantic search")
}

// TestLiveModelSwap_SameWidth verifies against two real models of identical
// width that a swap invisible to both the content hash and the dimension check
// is still caught, and the stale vectors recomputed.
//
// For the TEI provider the model is fixed by the running container rather than
// by config, so this swap changes nothing the cache key can see; equal widths
// hide it from the dimension check too. Only re-embedding the probe detects it.
//
// It also measures how far apart the two spaces are, which is what makes the
// probe's tolerance safe: the same model repeats bit-identically, while two
// different models put the same text roughly a full unit apart.
func TestLiveModelSwap_SameWidth(t *testing.T) {
	t.Parallel()

	endpoint := liveModelEndpoint(t)
	base := cmp.Or(os.Getenv(liveModelBaseEnv), "bge-m3")
	other := os.Getenv(liveModelSameWidthEnv)
	if other == "" {
		t.Skipf("%s not set; skipping same-width model swap test", liveModelSameWidthEnv)
	}

	ctx := context.Background()
	text := embeddedText("archive_file", "Archive a file to cold storage")

	// Confirm the premise: both models must produce the same width, or this test
	// is silently measuring the different-width case instead.
	baseVec := liveEmbed(ctx, t, endpoint, base, text)
	otherVec := liveEmbed(ctx, t, endpoint, other, text)
	require.Equal(t, len(baseVec), len(otherVec),
		"this test requires two models of equal width (%s=%d, %s=%d)", base, len(baseVec), other, len(otherVec))

	// The same text embedded by two different models: how different are the spaces?
	dist := similarity.CosineDistance(baseVec, otherVec)
	t.Logf("same text under %s vs %s: cosine distance %.4f (width %d)", base, other, dist, len(baseVec))

	dsn := fmt.Sprintf("file:livesame_%d?mode=memory&cache=shared", testDBCounter.Add(1))
	tools := makeTools(mcp.NewTool("archive_file", mcp.WithDescription("Archive a file to cold storage")))

	indexed := liveModelStore(t, dsn, endpoint, base)
	require.NoError(t, indexed.UpsertTools(ctx, tools))

	// A TEI-style swap: the model changes behind an unchanged service URL, so
	// the identity the cache key is derived from is byte-identical. The store is
	// configured with `base` while the client actually embeds with `other`.
	swapped := liveModelStore(t, dsn, endpoint, other)
	swapped.embeddingIdentity = indexed.embeddingIdentity
	require.NoError(t, swapped.UpsertTools(ctx, tools))

	var stored []byte
	require.NoError(t, swapped.db.QueryRowContext(ctx,
		"SELECT embedding FROM llm_capabilities WHERE name = ?", "archive_file").Scan(&stored))

	assert.Equal(t, encodeEmbedding(otherVec), stored,
		"a same-width model swap must be detected and the stale vector recomputed")
	assert.NotEqual(t, encodeEmbedding(baseVec), stored,
		"the previous model's vector must not survive the swap")
}

// TestLiveModelSwap_StaleDistanceDistribution measures how many stale vectors
// survive the semantic distance filter after an undetectable same-width model
// swap, across a realistic catalogue rather than a single pair.
//
// A single aggregate distance is misleading here. Two unrelated embedding spaces
// give a cosine similarity centred on zero, so per-tool distances scatter around
// 1.0 — and DefaultSemanticDistanceThreshold is exactly 1.0. Whether the filter
// is a real backstop or a coin flip therefore depends on the spread, which only
// a distribution can show.
func TestLiveModelSwap_StaleDistanceDistribution(t *testing.T) {
	t.Parallel()

	endpoint := liveModelEndpoint(t)
	base := cmp.Or(os.Getenv(liveModelBaseEnv), "bge-m3")
	other := os.Getenv(liveModelSameWidthEnv)
	if other == "" {
		t.Skipf("%s not set; skipping stale-distance distribution measurement", liveModelSameWidthEnv)
	}

	ctx := context.Background()
	backends := []string{"grafana", "datadog", "argocd", "k8s", "gitnexus", "dbhub", "firecrawl", "context7"}
	const toolCount = 64

	texts := make([]string, toolCount)
	for i := range toolCount {
		b := backends[i%len(backends)]
		texts[i] = embeddedText(
			fmt.Sprintf("%s_operation_%03d", b, i),
			fmt.Sprintf("Perform operation %d against the %s backend, applying the requested "+
				"filters and returning the matching records with their metadata.", i, b),
		)
	}

	// Tools indexed by the outgoing model; queries embedded by the incoming one.
	staleVecs := liveEmbedBatch(ctx, t, endpoint, base, texts)
	queries := []string{
		"search dashboards", "list kubernetes pods", "run a SQL query",
		"fetch a web page", "look up library documentation", "check deployment sync status",
	}
	queryVecs := liveEmbedBatch(ctx, t, endpoint, other, queries)
	require.Equal(t, len(staleVecs[0]), len(queryVecs[0]), "the two models must share a width")

	var dists []float64
	for _, q := range queryVecs {
		for _, s := range staleVecs {
			dists = append(dists, similarity.CosineDistance(q, s))
		}
	}
	sort.Float64s(dists)

	belowThreshold := 0
	for _, d := range dists {
		if d <= DefaultSemanticDistanceThreshold {
			belowThreshold++
		}
	}
	pct := 100 * float64(belowThreshold) / float64(len(dists))

	t.Logf("stale-vector distances after a same-width swap (%s indexed, %s querying), n=%d:",
		base, other, len(dists))
	t.Logf("  min %.4f | p50 %.4f | max %.4f", dists[0], dists[len(dists)/2], dists[len(dists)-1])
	t.Logf("  %d/%d (%.1f%%) fall at or below the %.1f distance threshold and would be ranked",
		belowThreshold, len(dists), pct, DefaultSemanticDistanceThreshold)

	// No pass/fail on the fraction: it is a property of the model pair, not of
	// this code. The assertion is only that the measurement is meaningful.
	require.NotEmpty(t, dists)
}

// liveEmbedBatch returns embeddings for several texts from one model.
func liveEmbedBatch(ctx context.Context, t *testing.T, endpoint, model string, texts []string) [][]float32 {
	t.Helper()
	client, err := similarity.NewEmbeddingClient(&types.OptimizerConfig{
		EmbeddingService:        endpoint,
		EmbeddingProvider:       types.EmbeddingProviderOpenAI,
		EmbeddingModel:          model,
		EmbeddingServiceTimeout: 5 * time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	vecs, err := client.EmbedBatch(ctx, texts)
	require.NoError(t, err)
	return vecs
}

// liveEmbed returns one embedding from the live endpoint for the given model.
func liveEmbed(ctx context.Context, t *testing.T, endpoint, model, text string) []float32 {
	t.Helper()
	client, err := similarity.NewEmbeddingClient(&types.OptimizerConfig{
		EmbeddingService:        endpoint,
		EmbeddingProvider:       types.EmbeddingProviderOpenAI,
		EmbeddingModel:          model,
		EmbeddingServiceTimeout: time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	vec, err := client.Embed(ctx, text)
	require.NoError(t, err)
	return vec
}

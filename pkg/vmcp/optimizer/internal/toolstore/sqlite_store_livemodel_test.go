// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toolstore

import (
	"cmp"
	"context"
	"fmt"
	"os"
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
// width that the swap is caught through the model id in the cache key, and the
// stale vectors recomputed.
//
// For the TEI provider the model is a property of the running container rather
// than of the config, so this swap changes nothing the configured identity can
// see, and equal widths hide it from the dimension check too. The configured
// halves of the two identities are forced equal below to reproduce that; only
// the live model id separates them.
//
// It also measures how far apart the two spaces are — the same text under two
// models lands roughly a full unit apart — which is the size of the corruption
// a missed swap would serve.
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
	dist, err := similarity.CosineDistance(baseVec, otherVec)
	require.NoError(t, err)
	t.Logf("same text under %s vs %s: cosine distance %.4f (width %d)", base, other, dist, len(baseVec))

	dsn := fmt.Sprintf("file:livesame_%d?mode=memory&cache=shared", testDBCounter.Add(1))
	tools := makeTools(mcp.NewTool("archive_file", mcp.WithDescription("Archive a file to cold storage")))

	indexed := liveModelStore(t, dsn, endpoint, base)
	require.NoError(t, indexed.UpsertTools(ctx, tools))

	// A TEI-style swap: the model changes behind an unchanged service URL, so
	// the configured half of the identity is byte-identical. Only the model id
	// the client reports separates the two stores.
	swapped := liveModelStore(t, dsn, endpoint, other)
	swapped.configIdentity = indexed.configIdentity
	require.NoError(t, swapped.UpsertTools(ctx, tools))

	var stored []byte
	require.NoError(t, swapped.db.QueryRowContext(ctx,
		"SELECT embedding FROM llm_capabilities WHERE name = ?", "archive_file").Scan(&stored))

	assert.Equal(t, encodeEmbedding(otherVec), stored,
		"a same-width model swap must be detected and the stale vector recomputed")
	assert.NotEqual(t, encodeEmbedding(baseVec), stored,
		"the previous model's vector must not survive the swap")
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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package similarity

import (
	"cmp"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenAIClient_Live exercises the real /embeddings wire path against an
// OpenAI-compatible endpoint. It is skipped unless OPENAI_API_KEY is set, so the
// default `task test` run stays green. Override OPENAI_EMBEDDING_BASE_URL and
// OPENAI_EMBEDDING_MODEL to point it at a compatible gateway.
func TestOpenAIClient_Live(t *testing.T) {
	t.Parallel()

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set; skipping live OpenAI embedding test")
	}

	baseURL := cmp.Or(os.Getenv("OPENAI_EMBEDDING_BASE_URL"), "https://api.openai.com/v1")
	model := cmp.Or(os.Getenv("OPENAI_EMBEDDING_MODEL"), "text-embedding-3-small")

	client, err := newOpenAIClient(baseURL, model, apiKey, nil, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()

	vec, err := client.Embed(ctx, "the quick brown fox")
	require.NoError(t, err)
	require.NotEmpty(t, vec, "embedding vector must not be empty")

	// Repeat one input so we can confirm results land back in request order:
	// identical inputs must produce identical vectors at their own indices.
	inputs := []string{"the quick brown fox", "lorem ipsum", "the quick brown fox"}
	batch, err := client.EmbedBatch(ctx, inputs)
	require.NoError(t, err)
	require.Len(t, batch, len(inputs))
	for i, e := range batch {
		require.Lenf(t, e, len(vec), "embedding %d has unexpected dimension", i)
	}
	require.Equal(t, batch[0], batch[2], "identical inputs must map to identical embeddings (order preserved)")
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFakeEmbeddingClient_Determinism(t *testing.T) {
	t.Parallel()
	client := NewFakeEmbeddingClient(384)
	ctx := context.Background()

	vec1, err := client.Embed(ctx, "hello world")
	require.NoError(t, err)

	vec2, err := client.Embed(ctx, "hello world")
	require.NoError(t, err)

	require.Equal(t, vec1, vec2, "same input must produce same output")
}

func TestFakeEmbeddingClient_DifferentInputs(t *testing.T) {
	t.Parallel()
	client := NewFakeEmbeddingClient(384)
	ctx := context.Background()

	vec1, err := client.Embed(ctx, "read a file")
	require.NoError(t, err)

	vec2, err := client.Embed(ctx, "send an email")
	require.NoError(t, err)

	require.NotEqual(t, vec1, vec2, "different inputs should produce different vectors")
}

func TestFakeEmbeddingClient_Dimension(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, dim := range []int{128, 384, 768} {
		t.Run(fmt.Sprintf("dim_%d", dim), func(t *testing.T) {
			t.Parallel()
			client := NewFakeEmbeddingClient(dim)
			require.Equal(t, dim, client.Dimension())

			vec, err := client.Embed(ctx, "test")
			require.NoError(t, err)
			require.Len(t, vec, dim)
		})
	}
}

func TestFakeEmbeddingClient_UnitNormalized(t *testing.T) {
	t.Parallel()
	client := NewFakeEmbeddingClient(384)
	ctx := context.Background()

	vec, err := client.Embed(ctx, "test vector normalization")
	require.NoError(t, err)

	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)

	require.InDelta(t, 1.0, norm, 1e-5, "vector should be unit-normalized")
}

func TestFakeEmbeddingClient_EmbedBatch(t *testing.T) {
	t.Parallel()
	client := NewFakeEmbeddingClient(384)
	ctx := context.Background()

	texts := []string{"alpha", "beta", "gamma"}
	batch, err := client.EmbedBatch(ctx, texts)
	require.NoError(t, err)
	require.Len(t, batch, 3)

	// Each batch result should match individual Embed calls
	for i, text := range texts {
		individual, err := client.Embed(ctx, text)
		require.NoError(t, err)
		require.Equal(t, individual, batch[i], "batch[%d] should match individual Embed for %q", i, text)
	}
}

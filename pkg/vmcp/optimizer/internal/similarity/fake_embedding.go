// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package similarity

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"math/rand"
)

// FakeEmbeddingClient is a deterministic embedding client for testing.
// It hashes input text with SHA-256 and uses the hash as a seed to generate
// reproducible float32 vectors. The vectors are L2-normalized to unit length.
type FakeEmbeddingClient struct {
	dim int
}

// NewFakeEmbeddingClient creates a FakeEmbeddingClient that produces vectors
// of the given dimension.
func NewFakeEmbeddingClient(dimension int) *FakeEmbeddingClient {
	return &FakeEmbeddingClient{dim: dimension}
}

// Embed returns a deterministic, unit-normalized vector for the given text.
func (f *FakeEmbeddingClient) Embed(_ context.Context, text string) ([]float32, error) {
	hash := sha256.Sum256([]byte(text))
	//nolint:gosec // overflow is acceptable for seeding a non-crypto RNG
	seed := int64(binary.LittleEndian.Uint64(hash[:8]))
	//nolint:gosec // deterministic RNG is intentional for fake embeddings
	rng := rand.New(rand.NewSource(seed))

	vec := make([]float32, f.dim)
	var norm float64
	for i := range vec {
		v := rng.Float32()*2 - 1 // [-1, 1]
		vec[i] = v
		norm += float64(v) * float64(v)
	}

	// L2-normalize
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}

	return vec, nil
}

// EmbedBatch returns deterministic embeddings for each input text.
func (f *FakeEmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := f.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		result[i] = vec
	}
	return result, nil
}

// Close is a no-op for the fake client.
func (*FakeEmbeddingClient) Close() error {
	return nil
}

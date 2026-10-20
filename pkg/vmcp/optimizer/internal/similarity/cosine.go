// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package similarity provides vector distance functions for semantic search.
package similarity

import (
	"fmt"
	"math"
)

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns a value in [-1, 1] where 1 means identical direction,
// 0 means orthogonal, and -1 means opposite direction.
//
// Vectors of different lengths return an error. The guard lives here rather
// than at the call sites because the loop below indexes both slices
// positionally: a shorter b would panic and a longer b would silently ignore
// its tail, and a caller that forgets its own check gets one or the other.
func CosineSimilarity(a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vectors have different dimensions: %d and %d", len(a), len(b))
	}

	var dot, normA, normB float64
	for i := range a {
		ai := float64(a[i])
		bi := float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}

	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0, nil
	}
	return dot / denom, nil
}

// CosineDistance computes the cosine distance between two vectors.
// Returns a value in [0, 2] where 0 means identical direction and 2 means
// opposite direction. Lower values indicate more similar vectors.
// Vectors of different lengths return an error (see CosineSimilarity).
func CosineDistance(a, b []float32) (float64, error) {
	sim, err := CosineSimilarity(a, b)
	if err != nil {
		return 0, err
	}
	return 1 - sim, nil
}

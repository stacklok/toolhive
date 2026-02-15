// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package similarity provides vector distance functions for semantic search.
package similarity

import "math"

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns a value in [-1, 1] where 1 means identical direction,
// 0 means orthogonal, and -1 means opposite direction.
// Both vectors must have the same length.
func CosineSimilarity(a, b []float32) float64 {
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
		return 0
	}
	return dot / denom
}

// CosineDistance computes the cosine distance between two vectors.
// Returns a value in [0, 2] where 0 means identical direction and 2 means
// opposite direction. Lower values indicate more similar vectors.
func CosineDistance(a, b []float32) float64 {
	return 1 - CosineSimilarity(a, b)
}

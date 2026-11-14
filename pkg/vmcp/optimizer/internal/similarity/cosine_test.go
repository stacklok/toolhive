// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package similarity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCosineSimilarity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{name: "identical vectors", a: []float32{1, 2, 3}, b: []float32{1, 2, 3}, want: 1.0},
		{name: "orthogonal vectors", a: []float32{1, 0, 0}, b: []float32{0, 1, 0}, want: 0.0},
		{name: "opposite vectors", a: []float32{1, 2, 3}, b: []float32{-1, -2, -3}, want: -1.0},
		{name: "zero vector", a: []float32{0, 0, 0}, b: []float32{1, 2, 3}, want: 0.0},
		// cos([1,0], [1,1]) = 1 / (1 * sqrt(2)) ≈ 0.7071
		{name: "known angle", a: []float32{1, 0}, b: []float32{1, 1}, want: 0.7071067811865476},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := CosineSimilarity(tc.a, tc.b)
			require.NoError(t, err)
			require.InDelta(t, tc.want, got, 1e-7)
		})
	}
}

// TestCosineSimilarity_DimensionMismatch asserts mismatched widths are refused
// rather than computed. Without the guard, a shorter b panics on indexing and
// a longer b silently ignores its tail — a wrong answer, not an error.
func TestCosineSimilarity_DimensionMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b []float32
	}{
		{name: "b shorter would panic", a: []float32{1, 2, 3}, b: []float32{1, 2}},
		{name: "b longer would be silently truncated", a: []float32{1, 2}, b: []float32{1, 2, 3}},
		{name: "empty against non-empty", a: nil, b: []float32{1}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := CosineSimilarity(tc.a, tc.b)
			require.Error(t, err)

			_, err = CosineDistance(tc.a, tc.b)
			require.Error(t, err)
		})
	}
}

func TestCosineDistance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{name: "identical vectors", a: []float32{1, 2, 3}, b: []float32{1, 2, 3}, want: 0.0},
		{name: "orthogonal vectors", a: []float32{1, 0, 0}, b: []float32{0, 1, 0}, want: 1.0},
		{name: "opposite vectors", a: []float32{1, 2, 3}, b: []float32{-1, -2, -3}, want: 2.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := CosineDistance(tc.a, tc.b)
			require.NoError(t, err)
			require.InDelta(t, tc.want, got, 1e-7)
		})
	}
}

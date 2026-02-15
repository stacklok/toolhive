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
			require.InDelta(t, tc.want, CosineSimilarity(tc.a, tc.b), 1e-7)
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
			require.InDelta(t, tc.want, CosineDistance(tc.a, tc.b), 1e-7)
		})
	}
}

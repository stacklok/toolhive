// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package similarity

import (
	"math/rand"
	"testing"
)

func randomVector(n int) []float32 {
	vec := make([]float32, n)
	for i := range vec {
		//nolint:gosec // deterministic RNG is fine for benchmarks
		vec[i] = rand.Float32()*2 - 1
	}
	return vec
}

func BenchmarkCosineDistance_384(b *testing.B) {
	a := randomVector(384)
	v := randomVector(384)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		CosineDistance(a, v)
	}
}

func BenchmarkCosineDistance_768(b *testing.B) {
	a := randomVector(768)
	v := randomVector(768)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		CosineDistance(a, v)
	}
}

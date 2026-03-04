// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package security_test

import (
	"testing"

	"github.com/stacklok/toolhive/pkg/security"
)

func TestConstantTimeHashCompare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		hashA         string
		hashB         string
		normalizedLen int
		want          bool
	}{
		{
			name:          "identical SHA256 hashes",
			hashA:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
			hashB:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
			normalizedLen: 64,
			want:          true,
		},
		{
			name:          "different SHA256 hashes",
			hashA:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
			hashB:         "b665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
			normalizedLen: 64,
			want:          false,
		},
		{
			name:          "one byte difference at end",
			hashA:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
			hashB:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae4",
			normalizedLen: 64,
			want:          false,
		},
		{
			name:          "empty strings",
			hashA:         "",
			hashB:         "",
			normalizedLen: 64,
			want:          true,
		},
		{
			name:          "one empty string",
			hashA:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
			hashB:         "",
			normalizedLen: 64,
			want:          false,
		},
		{
			name:          "different lengths",
			hashA:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
			hashB:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae",
			normalizedLen: 64,
			want:          false,
		},
		{
			name:          "short identical strings (length mismatch)",
			hashA:         "abc123",
			hashB:         "abc123",
			normalizedLen: 64,
			want:          false, // Lengths don't match normalizedLen - security fix
		},
		{
			name:          "short different strings",
			hashA:         "abc123",
			hashB:         "abc124",
			normalizedLen: 64,
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := security.ConstantTimeHashCompare(tt.hashA, tt.hashB, tt.normalizedLen)
			if got != tt.want {
				t.Errorf("ConstantTimeHashCompare() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestConstantTimeHashCompare_Symmetry verifies that the comparison is symmetric.
func TestConstantTimeHashCompare_Symmetry(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		hashA string
		hashB string
	}{
		{
			hashA: "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
			hashB: "b665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
		},
		{
			hashA: "",
			hashB: "abc123",
		},
		{
			hashA: "short",
			hashB: "longer_string",
		},
	}

	for _, tc := range testCases {
		resultAB := security.ConstantTimeHashCompare(tc.hashA, tc.hashB, 64)
		resultBA := security.ConstantTimeHashCompare(tc.hashB, tc.hashA, 64)

		if resultAB != resultBA {
			t.Errorf("Comparison is not symmetric: compare(%q, %q) = %v, but compare(%q, %q) = %v",
				tc.hashA, tc.hashB, resultAB, tc.hashB, tc.hashA, resultBA)
		}
	}
}

// TestConstantTimeHashCompare_DifferentNormalizedLengths tests with various normalized lengths.
func TestConstantTimeHashCompare_DifferentNormalizedLengths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		hashA         string
		hashB         string
		normalizedLen int
		want          bool
	}{
		{
			name:          "SHA256 with correct length",
			hashA:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
			hashB:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
			normalizedLen: 64,
			want:          true,
		},
		{
			name:          "SHA1 length (40 chars)",
			hashA:         "356a192b7913b04c54574d18c28d46e6395428ab",
			hashB:         "356a192b7913b04c54574d18c28d46e6395428ab",
			normalizedLen: 40,
			want:          true,
		},
		{
			name:          "MD5 length (32 chars)",
			hashA:         "5d41402abc4b2a76b9719d911017c592",
			hashB:         "5d41402abc4b2a76b9719d911017c592",
			normalizedLen: 32,
			want:          true,
		},
		{
			name:          "short strings with small normalized length (length mismatch)",
			hashA:         "abc",
			hashB:         "abc",
			normalizedLen: 10,
			want:          false, // Lengths don't match normalizedLen - security fix
		},
		{
			name:          "truncation attack: same prefix, different suffix",
			hashA:         "abc" + "x000000000000000000000000000000000000000000000000000000000000000" + "foo",
			hashB:         "abc" + "x000000000000000000000000000000000000000000000000000000000000000" + "bar",
			normalizedLen: 64,
			want:          false, // Prevented: lengths > normalizedLen should not match on prefix
		},
		{
			name:          "truncation attack: both longer than normalized length, same prefix",
			hashA:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3" + "extra",
			hashB:         "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3" + "different",
			normalizedLen: 64,
			want:          false, // Prevented: must reject inputs longer than normalizedLen
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := security.ConstantTimeHashCompare(tt.hashA, tt.hashB, tt.normalizedLen)
			if got != tt.want {
				t.Errorf("ConstantTimeHashCompare() = %v, want %v", got, tt.want)
			}
		})
	}
}

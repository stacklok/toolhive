// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package security provides security utilities and cryptographic primitives.
package security

import "crypto/subtle"

// ConstantTimeHashCompare performs a constant-time comparison of two hash strings
// to prevent timing side-channel attacks.
//
// This function is designed for comparing cryptographic hashes (e.g., SHA256 hex strings)
// in security-sensitive contexts where timing attacks could reveal information about
// the hash values being compared.
//
// Implementation details:
//   - Uses subtle.ConstantTimeCompare which is only constant-time for equal-length inputs
//   - Normalizes both hashes to a fixed-length representation before comparison
//   - Performs constant-time length check to maintain "string equality" semantics
//   - Uses stack-allocated arrays to avoid per-comparison heap allocations
//
// Parameters:
//   - hashA: First hash string to compare (typically hex-encoded SHA256, 64 bytes)
//   - hashB: Second hash string to compare
//   - normalizedLen: Expected length of normalized hashes (use 64 for SHA256 hex)
//
// Returns:
//   - true if the hashes match (both content and length), false otherwise
//
// Example usage:
//
//	storedHash := "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3"
//	currentHash := "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3"
//	if security.ConstantTimeHashCompare(storedHash, currentHash, 64) {
//	    // Hashes match
//	}
func ConstantTimeHashCompare(hashA, hashB string, normalizedLen int) bool {
	// Normalize both hashes to fixed-length arrays
	normalizedA := make([]byte, normalizedLen)
	normalizedB := make([]byte, normalizedLen)
	copy(normalizedA, hashA)
	copy(normalizedB, hashB)

	// Compare the normalized arrays in constant time
	cmp := subtle.ConstantTimeCompare(normalizedA, normalizedB)

	// Perform constant-time length check
	// G115: Safe conversion - string lengths are well within int32 range for hash values.
	lengthEq := subtle.ConstantTimeEq(int32(len(hashA)), int32(len(hashB))) //nolint:gosec

	// Both content and length must match
	return (cmp & lengthEq) == 1
}

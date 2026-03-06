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
//   - Uses subtle.ConstantTimeEq for constant-time length checks
//   - Uses subtle.ConstantTimeCompare for constant-time content comparison
//   - Enforces exact length matching: both inputs must be exactly normalizedLen bytes
//   - Special case: empty strings are allowed only when both are empty (for anonymous sessions)
//   - No normalization/padding: inputs longer or shorter than normalizedLen are rejected
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
	lenA := len(hashA)
	lenB := len(hashB)

	// Check conditions in constant-time:
	// 1. Both empty (special case for anonymous sessions)
	// G115: Safe conversion - string lengths are well within int32 range for hash values.
	bothEmpty := subtle.ConstantTimeEq(int32(lenA), 0) & subtle.ConstantTimeEq(int32(lenB), 0) //nolint:gosec

	// 2. Both have the expected length (prevents truncation attacks where inputs
	// longer than normalizedLen could match on prefix alone)
	lengthAOk := subtle.ConstantTimeEq(int32(lenA), int32(normalizedLen)) //nolint:gosec
	lengthBOk := subtle.ConstantTimeEq(int32(lenB), int32(normalizedLen)) //nolint:gosec
	bothCorrectLen := lengthAOk & lengthBOk

	// Fast path: both empty (anonymous case) - no allocation needed
	if bothEmpty == 1 {
		return true
	}

	// Fast path: both correct length - compare directly without normalization
	// This avoids allocating and copying into fixed-size arrays
	if bothCorrectLen == 1 {
		return subtle.ConstantTimeCompare([]byte(hashA), []byte(hashB)) == 1
	}

	// Invalid case: lengths don't match or are incorrect
	return false
}

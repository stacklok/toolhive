// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package security_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/session/security"
)

const (
	testToken = "my-token"
)

// TestGenerateSalt verifies that GenerateSalt produces cryptographically random salts.
func TestGenerateSalt(t *testing.T) {
	t.Parallel()

	// Generate multiple salts
	salt1, err := security.GenerateSalt()
	require.NoError(t, err)
	require.NotNil(t, salt1)
	assert.Len(t, salt1, 16, "salt should be 16 bytes")

	salt2, err := security.GenerateSalt()
	require.NoError(t, err)
	require.NotNil(t, salt2)
	assert.Len(t, salt2, 16, "salt should be 16 bytes")

	// Salts should be different (extremely high probability)
	assert.NotEqual(t, salt1, salt2, "consecutive salts should be unique")

	// Salt should not be all zeros (indicates crypto/rand failure)
	allZeros := make([]byte, 16)
	assert.NotEqual(t, allZeros, salt1, "salt should not be all zeros")
	assert.NotEqual(t, allZeros, salt2, "salt should not be all zeros")
}

// TestHashToken_BasicFunctionality verifies HMAC-SHA256 token hashing.
func TestHashToken_BasicFunctionality(t *testing.T) {
	t.Parallel()

	secret := []byte("test-hmac-secret-32-bytes-long!!")
	salt := []byte("test-salt-16byte")
	token := "my-bearer-token-12345"

	hash := security.HashToken(token, secret, salt)

	// SHA256 produces 32 bytes = 64 hex characters
	assert.Len(t, hash, security.SHA256HexLen, "hash should be 64 hex characters")

	// Hash should be valid hex
	_, err := hex.DecodeString(hash)
	assert.NoError(t, err, "hash should be valid hex encoding")

	// Hash should be deterministic (same inputs → same output)
	hash2 := security.HashToken(token, secret, salt)
	assert.Equal(t, hash, hash2, "hashing should be deterministic")
}

// TestHashToken_EmptyToken verifies that empty tokens return empty hash.
func TestHashToken_EmptyToken(t *testing.T) {
	t.Parallel()

	secret := []byte("test-secret")
	salt := []byte("test-salt")

	hash := security.HashToken("", secret, salt)

	assert.Equal(t, "", hash, "empty token should produce empty hash")
}

// TestHashToken_DifferentInputs verifies that different inputs produce different hashes.
func TestHashToken_DifferentInputs(t *testing.T) {
	t.Parallel()

	secret := []byte("test-hmac-secret")
	salt := []byte("test-salt")

	token1 := "token-one"
	token2 := "token-two"

	hash1 := security.HashToken(token1, secret, salt)
	hash2 := security.HashToken(token2, secret, salt)

	assert.NotEqual(t, hash1, hash2, "different tokens should produce different hashes")
	assert.Len(t, hash1, security.SHA256HexLen)
	assert.Len(t, hash2, security.SHA256HexLen)
}

// TestHashToken_DifferentSecrets verifies that different secrets produce different hashes.
func TestHashToken_DifferentSecrets(t *testing.T) {
	t.Parallel()

	token := testToken
	salt := []byte("same-salt")

	secret1 := []byte("secret-one")
	secret2 := []byte("secret-two")

	hash1 := security.HashToken(token, secret1, salt)
	hash2 := security.HashToken(token, secret2, salt)

	assert.NotEqual(t, hash1, hash2, "different secrets should produce different hashes")
}

// TestHashToken_DifferentSalts verifies that different salts produce different hashes.
func TestHashToken_DifferentSalts(t *testing.T) {
	t.Parallel()

	token := testToken
	secret := []byte("same-secret")

	salt1 := []byte("salt-one")
	salt2 := []byte("salt-two")

	hash1 := security.HashToken(token, secret, salt1)
	hash2 := security.HashToken(token, secret, salt2)

	assert.NotEqual(t, hash1, hash2, "different salts should produce different hashes")
}

// TestHashToken_EmptySecret verifies behavior with empty HMAC secret.
func TestHashToken_EmptySecret(t *testing.T) {
	t.Parallel()

	token := testToken
	salt := []byte("test-salt")
	emptySecret := []byte{}

	// Should still produce a hash (HMAC allows empty key, though not recommended)
	hash := security.HashToken(token, emptySecret, salt)

	assert.Len(t, hash, security.SHA256HexLen, "should produce valid hash even with empty secret")
	assert.NotEqual(t, "", hash, "hash should not be empty")
}

// TestHashToken_EmptySalt verifies behavior with empty salt.
func TestHashToken_EmptySalt(t *testing.T) {
	t.Parallel()

	token := testToken
	secret := []byte("test-secret")
	emptySalt := []byte{}

	// Should still produce a hash (salt is optional for HMAC, though not recommended)
	hash := security.HashToken(token, secret, emptySalt)

	assert.Len(t, hash, security.SHA256HexLen, "should produce valid hash even with empty salt")
	assert.NotEqual(t, "", hash, "hash should not be empty")
}

// TestHashToken_NilInputs verifies behavior with nil secret/salt.
func TestHashToken_NilInputs(t *testing.T) {
	t.Parallel()

	token := testToken

	// Nil secret and salt should still work (treated as empty)
	hash := security.HashToken(token, nil, nil)

	assert.Len(t, hash, security.SHA256HexLen, "should produce valid hash with nil inputs")
	assert.NotEqual(t, "", hash, "hash should not be empty")
}

// TestHashToken_LongToken verifies behavior with very long tokens.
func TestHashToken_LongToken(t *testing.T) {
	t.Parallel()

	secret := []byte("test-secret")
	salt := []byte("test-salt")

	// Very long token (10KB)
	longToken := strings.Repeat("a", 10000)

	hash := security.HashToken(longToken, secret, salt)

	// HMAC-SHA256 always produces 64-character hex output regardless of input length
	assert.Len(t, hash, security.SHA256HexLen, "hash length should be constant regardless of input length")
}

// TestHashToken_SpecialCharacters verifies handling of tokens with special characters.
func TestHashToken_SpecialCharacters(t *testing.T) {
	t.Parallel()

	secret := []byte("test-secret")
	salt := []byte("test-salt")

	tests := []struct {
		name  string
		token string
	}{
		{"unicode", "token-with-üñíçödé-😀"},
		{"whitespace", "token with spaces\t\n"},
		{"symbols", "token!@#$%^&*()_+-={}[]|\\:;\"'<>,.?/"},
		{"null_bytes", "token\x00with\x00nulls"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hash := security.HashToken(tt.token, secret, salt)
			assert.Len(t, hash, security.SHA256HexLen, "should handle special characters")
			assert.NotEqual(t, "", hash, "should produce non-empty hash")

			// Verify hex encoding is valid
			_, err := hex.DecodeString(hash)
			assert.NoError(t, err, "hash should be valid hex")
		})
	}
}

// TestSHA256HexLen_Constant verifies the constant value is correct.
func TestSHA256HexLen_Constant(t *testing.T) {
	t.Parallel()

	// SHA256 produces 32 bytes = 64 hex characters
	assert.Equal(t, 64, security.SHA256HexLen, "SHA256HexLen should be 64")
}

// TestHashToken_Consistency verifies that the same inputs always produce the same hash
// across multiple invocations (regression test for determinism).
func TestHashToken_Consistency(t *testing.T) {
	t.Parallel()

	secret := []byte("consistent-secret")
	salt := []byte("consistent-salt")
	token := "consistent-token"

	// Hash the same input 100 times
	var hashes []string
	for i := 0; i < 100; i++ {
		hashes = append(hashes, security.HashToken(token, secret, salt))
	}

	// All hashes should be identical
	firstHash := hashes[0]
	for i, hash := range hashes {
		assert.Equal(t, firstHash, hash, "hash at index %d should match first hash", i)
	}
}

// TestHashToken_NoCollisions verifies that different tokens produce different hashes.
func TestHashToken_NoCollisions(t *testing.T) {
	t.Parallel()

	secret := []byte("test-secret")
	salt := []byte("test-salt")

	// Generate hashes for many different tokens
	seen := make(map[string]string)
	for i := 0; i < 1000; i++ {
		token := hex.EncodeToString([]byte{byte(i / 256), byte(i % 256)})
		hash := security.HashToken(token, secret, salt)

		// Check for collision
		if existingToken, exists := seen[hash]; exists {
			t.Errorf("collision detected: tokens %q and %q produced same hash %q",
				existingToken, token, hash)
		}
		seen[hash] = token
	}

	assert.Len(t, seen, 1000, "should have 1000 unique hashes")
}

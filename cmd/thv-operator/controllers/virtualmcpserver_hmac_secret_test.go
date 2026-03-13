// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerateHMACSecret tests the HMAC secret generation function.
func TestGenerateHMACSecret(t *testing.T) {
	t.Parallel()

	t.Run("generates valid base64 encoded secret", func(t *testing.T) {
		t.Parallel()

		secret, err := generateHMACSecret()
		require.NoError(t, err)
		require.NotEmpty(t, secret)

		// Verify it's valid base64
		decoded, err := base64.StdEncoding.DecodeString(secret)
		require.NoError(t, err)
		assert.Len(t, decoded, 32, "decoded secret should be exactly 32 bytes")
	})

	t.Run("generates unique secrets", func(t *testing.T) {
		t.Parallel()

		secret1, err := generateHMACSecret()
		require.NoError(t, err)

		secret2, err := generateHMACSecret()
		require.NoError(t, err)

		// Two generated secrets should be different
		assert.NotEqual(t, secret1, secret2, "consecutive secrets should be unique")
	})

	t.Run("generates cryptographically secure random data", func(t *testing.T) {
		t.Parallel()

		secret, err := generateHMACSecret()
		require.NoError(t, err)

		decoded, err := base64.StdEncoding.DecodeString(secret)
		require.NoError(t, err)

		// Check that it's not all zeros (would indicate failure of crypto/rand)
		allZeros := make([]byte, 32)
		assert.NotEqual(t, allZeros, decoded, "secret should not be all zeros")
	})

	t.Run("generates multiple valid secrets", func(t *testing.T) {
		t.Parallel()

		// Generate 100 secrets to ensure consistency
		secrets := make(map[string]bool)
		for i := 0; i < 100; i++ {
			secret, err := generateHMACSecret()
			require.NoError(t, err)

			// Verify base64 decoding
			decoded, err := base64.StdEncoding.DecodeString(secret)
			require.NoError(t, err)
			assert.Len(t, decoded, 32)

			// Track uniqueness
			secrets[secret] = true
		}

		// All secrets should be unique
		assert.Len(t, secrets, 100, "all generated secrets should be unique")
	})
}

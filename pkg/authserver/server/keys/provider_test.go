// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package keys

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writePEM writes a PEM-encoded EC key to a temp file and returns the filename.
func writePEM(t *testing.T, dir, filename string, der []byte) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	data := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	require.NoError(t, os.WriteFile(path, data, 0600))
	return filename
}

// generateTestKey generates an ECDSA P-256 key for testing.
func generateTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return key
}

// TestFileProvider tests the FileProvider implementation.
func TestFileProvider(t *testing.T) {
	t.Parallel()

	t.Run("loads valid EC key", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ecKey := generateTestKey(t)
		der, err := x509.MarshalECPrivateKey(ecKey)
		require.NoError(t, err)
		keyFile := writePEM(t, dir, "signing.pem", der)

		provider, err := NewFileProvider(Config{
			KeyDir:         dir,
			SigningKeyFile: keyFile,
		})
		require.NoError(t, err)

		key, err := provider.SigningKey(context.Background())
		require.NoError(t, err)
		assert.NotEmpty(t, key.KeyID)
		assert.Equal(t, "ES256", key.Algorithm)
		assert.NotNil(t, key.Key)

		pubKeys, err := provider.PublicKeys(context.Background())
		require.NoError(t, err)
		require.Len(t, pubKeys, 1)
		assert.Equal(t, key.KeyID, pubKeys[0].KeyID)
		assert.Equal(t, key.Algorithm, pubKeys[0].Algorithm)
	})

	t.Run("fails for non-existent file", func(t *testing.T) {
		t.Parallel()
		_, err := NewFileProvider(Config{
			KeyDir:         "/nonexistent",
			SigningKeyFile: "key.pem",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load signing key")
	})

	t.Run("fails for invalid PEM", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "invalid.pem")
		require.NoError(t, os.WriteFile(path, []byte("not a valid pem"), 0600))

		_, err := NewFileProvider(Config{
			KeyDir:         dir,
			SigningKeyFile: "invalid.pem",
		})
		require.Error(t, err)
	})

	t.Run("fails when signing key file is empty", func(t *testing.T) {
		t.Parallel()
		_, err := NewFileProvider(Config{
			KeyDir:         "/some/dir",
			SigningKeyFile: "",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "signing key file is required")
	})

	t.Run("loads multiple keys", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		// Create three keys
		key1 := generateTestKey(t)
		der1, err := x509.MarshalECPrivateKey(key1)
		require.NoError(t, err)
		signingFile := writePEM(t, dir, "signing.pem", der1)

		key2 := generateTestKey(t)
		der2, err := x509.MarshalECPrivateKey(key2)
		require.NoError(t, err)
		fallback1 := writePEM(t, dir, "old1.pem", der2)

		key3 := generateTestKey(t)
		der3, err := x509.MarshalECPrivateKey(key3)
		require.NoError(t, err)
		fallback2 := writePEM(t, dir, "old2.pem", der3)

		provider, err := NewFileProvider(Config{
			KeyDir:           dir,
			SigningKeyFile:   signingFile,
			FallbackKeyFiles: []string{fallback1, fallback2},
		})
		require.NoError(t, err)

		// SigningKey should return the first key
		signingKey, err := provider.SigningKey(context.Background())
		require.NoError(t, err)
		assert.NotEmpty(t, signingKey.KeyID)
		assert.Equal(t, "ES256", signingKey.Algorithm)

		// PublicKeys should return all three keys
		pubKeys, err := provider.PublicKeys(context.Background())
		require.NoError(t, err)
		require.Len(t, pubKeys, 3)

		// First public key should match the signing key
		assert.Equal(t, signingKey.KeyID, pubKeys[0].KeyID)

		// All keys should have unique key IDs
		keyIDs := make(map[string]bool)
		for _, pk := range pubKeys {
			assert.False(t, keyIDs[pk.KeyID], "duplicate key ID found")
			keyIDs[pk.KeyID] = true
		}
	})

	t.Run("signing key returns first key only", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		key1 := generateTestKey(t)
		der1, err := x509.MarshalECPrivateKey(key1)
		require.NoError(t, err)
		signingFile := writePEM(t, dir, "signing.pem", der1)

		key2 := generateTestKey(t)
		der2, err := x509.MarshalECPrivateKey(key2)
		require.NoError(t, err)
		fallbackFile := writePEM(t, dir, "old.pem", der2)

		provider, err := NewFileProvider(Config{
			KeyDir:           dir,
			SigningKeyFile:   signingFile,
			FallbackKeyFiles: []string{fallbackFile},
		})
		require.NoError(t, err)

		signingKey, err := provider.SigningKey(context.Background())
		require.NoError(t, err)

		pubKeys, err := provider.PublicKeys(context.Background())
		require.NoError(t, err)
		require.Len(t, pubKeys, 2)

		// Verify signing key matches the first public key (same key ID)
		assert.Equal(t, signingKey.KeyID, pubKeys[0].KeyID)

		// Verify the second public key is different
		assert.NotEqual(t, signingKey.KeyID, pubKeys[1].KeyID)
	})

	t.Run("fails when fallback key file is invalid", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		// Valid signing key
		key1 := generateTestKey(t)
		der1, err := x509.MarshalECPrivateKey(key1)
		require.NoError(t, err)
		signingFile := writePEM(t, dir, "signing.pem", der1)

		// Invalid fallback key
		require.NoError(t, os.WriteFile(filepath.Join(dir, "invalid.pem"), []byte("not a valid pem"), 0600))

		_, err = NewFileProvider(Config{
			KeyDir:           dir,
			SigningKeyFile:   signingFile,
			FallbackKeyFiles: []string{"invalid.pem"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load fallback key")
	})

	t.Run("fails when fallback key file does not exist", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		key1 := generateTestKey(t)
		der1, err := x509.MarshalECPrivateKey(key1)
		require.NoError(t, err)
		signingFile := writePEM(t, dir, "signing.pem", der1)

		_, err = NewFileProvider(Config{
			KeyDir:           dir,
			SigningKeyFile:   signingFile,
			FallbackKeyFiles: []string{"nonexistent.pem"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load fallback key")
	})

	t.Run("works with no fallback keys", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		key1 := generateTestKey(t)
		der1, err := x509.MarshalECPrivateKey(key1)
		require.NoError(t, err)
		signingFile := writePEM(t, dir, "signing.pem", der1)

		provider, err := NewFileProvider(Config{
			KeyDir:         dir,
			SigningKeyFile: signingFile,
		})
		require.NoError(t, err)

		signingKey, err := provider.SigningKey(context.Background())
		require.NoError(t, err)
		assert.NotEmpty(t, signingKey.KeyID)

		pubKeys, err := provider.PublicKeys(context.Background())
		require.NoError(t, err)
		require.Len(t, pubKeys, 1)
		assert.Equal(t, signingKey.KeyID, pubKeys[0].KeyID)
	})
}

// TestGeneratingProvider tests the GeneratingProvider implementation.
func TestGeneratingProvider(t *testing.T) {
	t.Parallel()

	t.Run("generates key on first access", func(t *testing.T) {
		t.Parallel()
		provider := NewGeneratingProvider("ES256")

		key, err := provider.SigningKey(context.Background())
		require.NoError(t, err)
		assert.NotEmpty(t, key.KeyID)
		assert.Equal(t, "ES256", key.Algorithm)
		assert.NotNil(t, key.Key)
	})

	t.Run("returns same key on subsequent calls", func(t *testing.T) {
		t.Parallel()
		provider := NewGeneratingProvider("ES256")

		key1, err := provider.SigningKey(context.Background())
		require.NoError(t, err)

		key2, err := provider.SigningKey(context.Background())
		require.NoError(t, err)

		// Keys should have identical values (copies of the same internal key)
		assert.Equal(t, key1.KeyID, key2.KeyID)
		assert.Equal(t, key1.Algorithm, key2.Algorithm)
		assert.Equal(t, key1.Key, key2.Key)
		assert.Equal(t, key1.CreatedAt, key2.CreatedAt)
	})

	t.Run("uses default algorithm when empty", func(t *testing.T) {
		t.Parallel()
		provider := NewGeneratingProvider("")

		key, err := provider.SigningKey(context.Background())
		require.NoError(t, err)
		assert.Equal(t, DefaultAlgorithm, key.Algorithm)
	})

	t.Run("supports ES384", func(t *testing.T) {
		t.Parallel()
		provider := NewGeneratingProvider("ES384")

		key, err := provider.SigningKey(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "ES384", key.Algorithm)
	})

	t.Run("supports ES512", func(t *testing.T) {
		t.Parallel()
		provider := NewGeneratingProvider("ES512")

		key, err := provider.SigningKey(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "ES512", key.Algorithm)
	})

	t.Run("fails for unsupported algorithm", func(t *testing.T) {
		t.Parallel()
		provider := NewGeneratingProvider("RS256") // RSA not supported for generation

		_, err := provider.SigningKey(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported algorithm")
	})

	t.Run("PublicKeys generates key if needed", func(t *testing.T) {
		t.Parallel()
		provider := NewGeneratingProvider("ES256")

		pubKeys, err := provider.PublicKeys(context.Background())
		require.NoError(t, err)
		require.Len(t, pubKeys, 1)

		// Verify the signing key was also generated
		key, err := provider.SigningKey(context.Background())
		require.NoError(t, err)
		assert.Equal(t, key.KeyID, pubKeys[0].KeyID)
	})

	t.Run("thread-safe concurrent access", func(t *testing.T) {
		t.Parallel()
		provider := NewGeneratingProvider("ES256")

		var wg sync.WaitGroup
		var keys [10]*SigningKeyData
		var errs [10]error

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				keys[idx], errs[idx] = provider.SigningKey(context.Background())
			}(i)
		}

		wg.Wait()

		// All should succeed with the same key
		for i := 0; i < 10; i++ {
			require.NoError(t, errs[i])
			assert.Equal(t, keys[0].KeyID, keys[i].KeyID)
		}
	})
}

// TestNewProviderFromConfig tests the factory function.
func TestNewProviderFromConfig(t *testing.T) {
	t.Parallel()

	t.Run("creates FileProvider from config", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ecKey := generateTestKey(t)
		der, err := x509.MarshalECPrivateKey(ecKey)
		require.NoError(t, err)
		keyFile := writePEM(t, dir, "signing.pem", der)

		provider, err := NewProviderFromConfig(Config{
			KeyDir:         dir,
			SigningKeyFile: keyFile,
		})
		require.NoError(t, err)

		key, err := provider.SigningKey(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "ES256", key.Algorithm)
	})

	t.Run("creates GeneratingProvider when no key dir configured", func(t *testing.T) {
		t.Parallel()
		provider, err := NewProviderFromConfig(Config{})
		require.NoError(t, err)

		// Should be a GeneratingProvider
		_, ok := provider.(*GeneratingProvider)
		assert.True(t, ok, "expected GeneratingProvider")
	})

	t.Run("fails with invalid key file", func(t *testing.T) {
		t.Parallel()
		_, err := NewProviderFromConfig(Config{
			KeyDir:         "/nonexistent",
			SigningKeyFile: "key.pem",
		})
		require.Error(t, err)
	})

	t.Run("creates FileProvider with fallback keys", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		// Create signing key
		key1 := generateTestKey(t)
		der1, err := x509.MarshalECPrivateKey(key1)
		require.NoError(t, err)
		signingFile := writePEM(t, dir, "signing.pem", der1)

		// Create fallback key
		key2 := generateTestKey(t)
		der2, err := x509.MarshalECPrivateKey(key2)
		require.NoError(t, err)
		fallbackFile := writePEM(t, dir, "old.pem", der2)

		provider, err := NewProviderFromConfig(Config{
			KeyDir:           dir,
			SigningKeyFile:   signingFile,
			FallbackKeyFiles: []string{fallbackFile},
		})
		require.NoError(t, err)

		pubKeys, err := provider.PublicKeys(context.Background())
		require.NoError(t, err)
		require.Len(t, pubKeys, 2)
	})

	t.Run("fails with invalid fallback key", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		key1 := generateTestKey(t)
		der1, err := x509.MarshalECPrivateKey(key1)
		require.NoError(t, err)
		signingFile := writePEM(t, dir, "signing.pem", der1)

		_, err = NewProviderFromConfig(Config{
			KeyDir:           dir,
			SigningKeyFile:   signingFile,
			FallbackKeyFiles: []string{"nonexistent.pem"},
		})
		require.Error(t, err)
	})
}

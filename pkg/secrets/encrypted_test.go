// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/secrets/aes"
)

// Helper functions specific to encrypted tests
func generateRandomPassword(t *testing.T) []byte {
	t.Helper()
	password := make([]byte, 32)
	_, err := rand.Read(password)
	require.NoError(t, err, "Generating a random password should not return an error")
	return password
}

func createEncryptedManager(t *testing.T, filePath string, password []byte) *EncryptedManager {
	t.Helper()
	manager, err := NewEncryptedManager(filePath, password)
	require.NoError(t, err, "Creating an EncryptedManager should not return an error")
	require.IsType(t, &EncryptedManager{}, manager, "The manager should be an EncryptedManager")
	return manager.(*EncryptedManager)
}

func TestEncryptedManager_GetSecret(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// Create a temporary file for testing
	tempFile := createTempFile(t)

	// Create an EncryptedManager
	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	// Test getting a non-existent secret
	_, err := manager.GetSecret(ctx, "non-existent")
	assert.Error(t, err, "Getting a non-existent secret should return an error")
	assert.Contains(t, err.Error(), "not found", "Error message should indicate the secret was not found")

	// Test getting a secret with an empty name
	_, err = manager.GetSecret(ctx, "")
	assert.Error(t, err, "Getting a secret with an empty name should return an error")
	assert.Contains(t, err.Error(), "cannot be empty", "Error message should indicate the name cannot be empty")

	// Set a secret
	err = manager.SetSecret(ctx, "test-key", "test-value")
	require.NoError(t, err, "Setting a secret should not return an error")

	// Test getting an existing secret
	value, err := manager.GetSecret(ctx, "test-key")
	assert.NoError(t, err, "Getting an existing secret should not return an error")
	assert.Equal(t, "test-value", value, "The retrieved value should match the set value")
}

func TestEncryptedManager_SetSecret(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// Create a temporary file for testing
	tempFile := createTempFile(t)

	// Create an EncryptedManager
	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	// Test setting a secret with an empty name
	err := manager.SetSecret(ctx, "", "test-value")
	assert.Error(t, err, "Setting a secret with an empty name should return an error")
	assert.Contains(t, err.Error(), "cannot be empty", "Error message should indicate the name cannot be empty")

	// Test setting a new secret
	err = manager.SetSecret(ctx, "test-key", "test-value")
	assert.NoError(t, err, "Setting a new secret should not return an error")

	// Verify the secret was set
	value, err := manager.GetSecret(ctx, "test-key")
	assert.NoError(t, err, "Getting the set secret should not return an error")
	assert.Equal(t, "test-value", value, "The retrieved value should match the set value")

	// Test updating an existing secret
	err = manager.SetSecret(ctx, "test-key", "updated-value")
	assert.NoError(t, err, "Updating an existing secret should not return an error")

	// Verify the secret was updated
	value, err = manager.GetSecret(ctx, "test-key")
	assert.NoError(t, err, "Getting the updated secret should not return an error")
	assert.Equal(t, "updated-value", value, "The retrieved value should match the updated value")

	// Verify the file was updated by creating a new manager with the same password and file
	newManager := createEncryptedManager(t, tempFile, password)
	value, err = newManager.GetSecret(ctx, "test-key")
	assert.NoError(t, err, "Getting the secret from a new manager should not return an error")
	assert.Equal(t, "updated-value", value, "The retrieved value should match the updated value")
}

func TestEncryptedManager_DeleteSecret(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// Create a temporary file for testing
	tempFile := createTempFile(t)

	// Create an EncryptedManager
	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	// Test deleting a non-existent secret
	err := manager.DeleteSecret(ctx, "non-existent")
	assert.Error(t, err, "Deleting a non-existent secret should return an error")
	assert.ErrorIs(t, err, ErrSecretNotFound, "Error should be ErrSecretNotFound for a non-existent secret")

	// Test deleting a secret with an empty name
	err = manager.DeleteSecret(ctx, "")
	assert.Error(t, err, "Deleting a secret with an empty name should return an error")
	assert.Contains(t, err.Error(), "cannot be empty", "Error message should indicate the name cannot be empty")

	// Set a secret
	err = manager.SetSecret(ctx, "test-key", "test-value")
	require.NoError(t, err, "Setting a secret should not return an error")

	// Test deleting an existing secret
	err = manager.DeleteSecret(ctx, "test-key")
	assert.NoError(t, err, "Deleting an existing secret should not return an error")

	// Verify the secret was deleted
	_, err = manager.GetSecret(ctx, "test-key")
	assert.Error(t, err, "Getting a deleted secret should return an error")
	assert.Contains(t, err.Error(), "not found", "Error message should indicate the secret was not found")

	// Verify the file was updated by creating a new manager with the same password and file
	newManager := createEncryptedManager(t, tempFile, password)
	_, err = newManager.GetSecret(ctx, "test-key")
	assert.Error(t, err, "Getting a deleted secret from a new manager should return an error")
	assert.Contains(t, err.Error(), "not found", "Error message should indicate the secret was not found")
}

func TestEncryptedManager_ListSecrets(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// Create a temporary file for testing
	tempFile := createTempFile(t)

	// Create an EncryptedManager
	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	// Test listing secrets when there are none
	secrets, err := manager.ListSecrets(ctx)
	assert.NoError(t, err, "Listing secrets should not return an error")
	assert.Empty(t, secrets, "There should be no secrets initially")

	// Set some secrets
	require.NoError(t, manager.SetSecret(ctx, "key1", "value1"), "Setting a secret should not return an error")
	require.NoError(t, manager.SetSecret(ctx, "key2", "value2"), "Setting a secret should not return an error")
	require.NoError(t, manager.SetSecret(ctx, "key3", "value3"), "Setting a secret should not return an error")

	// Test listing secrets
	secrets, err = manager.ListSecrets(ctx)
	assert.NoError(t, err, "Listing secrets should not return an error")
	assert.Len(t, secrets, 3, "There should be 3 secrets")

	// Helper function to check if a key exists in the secrets list
	containsKey := func(key string) bool {
		for _, secret := range secrets {
			if secret.Key == key {
				return true
			}
		}
		return false
	}

	assert.True(t, containsKey("key1"), "The list should contain key1")
	assert.True(t, containsKey("key2"), "The list should contain key2")
	assert.True(t, containsKey("key3"), "The list should contain key3")

	// Verify the file was updated by creating a new manager with the same password and file
	newManager := createEncryptedManager(t, tempFile, password)
	secrets, err = newManager.ListSecrets(ctx)
	assert.NoError(t, err, "Listing secrets from a new manager should not return an error")
	assert.Len(t, secrets, 3, "There should be 3 secrets")

	// Helper function to check if a key exists in the secrets list
	containsKeyInNewManager := func(key string) bool {
		for _, secret := range secrets {
			if secret.Key == key {
				return true
			}
		}
		return false
	}

	assert.True(t, containsKeyInNewManager("key1"), "The list should contain key1")
	assert.True(t, containsKeyInNewManager("key2"), "The list should contain key2")
	assert.True(t, containsKeyInNewManager("key3"), "The list should contain key3")
}

func TestEncryptedManager_Cleanup(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// Create a temporary file for testing
	tempFile := createTempFile(t)

	// Create an EncryptedManager
	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	// Set some secrets
	require.NoError(t, manager.SetSecret(ctx, "key1", "value1"), "Setting a secret should not return an error")
	require.NoError(t, manager.SetSecret(ctx, "key2", "value2"), "Setting a secret should not return an error")

	// Verify the secrets were set
	secrets, err := manager.ListSecrets(ctx)
	assert.NoError(t, err, "Listing secrets should not return an error")
	assert.Len(t, secrets, 2, "There should be 2 secrets")

	// Test cleaning up all secrets
	err = manager.Cleanup()
	assert.NoError(t, err, "Cleaning up should not return an error")

	// Verify all secrets were removed
	secrets, err = manager.ListSecrets(ctx)
	assert.NoError(t, err, "Listing secrets should not return an error")
	assert.Empty(t, secrets, "There should be no secrets after cleanup")

	// Verify the file was updated by creating a new manager with the same password and file
	newManager := createEncryptedManager(t, tempFile, password)
	secrets, err = newManager.ListSecrets(ctx)
	assert.NoError(t, err, "Listing secrets from a new manager should not return an error")
	assert.Empty(t, secrets, "There should be no secrets after cleanup")
}

func TestNewEncryptedManager(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// Create a temporary file for testing
	tempFile := createTempFile(t)

	// Generate a random password
	password := generateRandomPassword(t)

	// Test creating an EncryptedManager with a valid file path and password
	manager, err := NewEncryptedManager(tempFile, password)
	assert.NoError(t, err, "Creating an EncryptedManager with a valid file path and password should not return an error")
	assert.NotNil(t, manager, "The manager should not be nil")
	assert.IsType(t, &EncryptedManager{}, manager, "The manager should be an EncryptedManager")

	// Test creating an EncryptedManager with a non-existent directory
	nonExistentFile := filepath.Join(t.TempDir(), "non-existent-dir", "secrets.json")
	_, err = NewEncryptedManager(nonExistentFile, password)
	assert.Error(t, err, "Creating an EncryptedManager with a non-existent directory should return an error")
	assert.Contains(t, err.Error(), "failed to open secrets file", "Error message should indicate the file could not be opened")

	// Test creating an EncryptedManager with an empty password
	_, err = NewEncryptedManager(tempFile, nil)
	assert.Error(t, err, "Creating an EncryptedManager with an empty password should return an error")
	assert.Contains(t, err.Error(), "password cannot be empty", "Error message should indicate the password cannot be empty")

	// Test creating an EncryptedManager with an existing file that contains valid encrypted data
	// First, create a manager and add a secret
	manager, err = NewEncryptedManager(tempFile, password)
	require.NoError(t, err, "Creating an EncryptedManager should not return an error")
	err = manager.SetSecret(ctx, "test-key", "test-value")
	require.NoError(t, err, "Setting a secret should not return an error")

	// Now create a new manager with the same file and password
	newManager, err := NewEncryptedManager(tempFile, password)
	assert.NoError(t, err, "Creating an EncryptedManager with an existing file should not return an error")
	assert.NotNil(t, newManager, "The manager should not be nil")

	// Verify the secret was loaded
	value, err := newManager.GetSecret(ctx, "test-key")
	assert.NoError(t, err, "Getting the secret should not return an error")
	assert.Equal(t, "test-value", value, "The retrieved value should match the set value")

	// Test creating an EncryptedManager with an existing file but wrong password
	wrongPassword := generateRandomPassword(t)
	_, err = NewEncryptedManager(tempFile, wrongPassword)
	assert.Error(t, err, "Creating an EncryptedManager with a wrong password should return an error")
	assert.Contains(t, err.Error(), "unable to decrypt", "Error message should indicate decryption failed")
}

func TestEncryptedManager_Concurrency(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// Create a temporary file for testing
	tempFile := createTempFile(t)

	// Create an EncryptedManager
	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	// Set a secret
	err := manager.SetSecret(ctx, "test-key", "test-value")
	require.NoError(t, err, "Setting a secret should not return an error")

	// Test concurrent access to the manager
	// This is a basic test that just ensures no race conditions occur
	// For a more thorough test, we would need to use the race detector
	const numGoroutines = 10
	done := make(chan bool)

	for i := 0; i < numGoroutines; i++ {
		go func(i int) {
			// Get the secret
			value, err := manager.GetSecret(ctx, "test-key")
			assert.NoError(t, err, "Getting the secret should not return an error")
			assert.Equal(t, "test-value", value, "The retrieved value should match the set value")

			// Set a new secret
			err = manager.SetSecret(ctx, fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i))
			assert.NoError(t, err, "Setting a secret should not return an error")

			done <- true
		}(i)
	}

	// Wait for all goroutines to finish
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify all secrets were set in memory
	secrets, err := manager.ListSecrets(ctx)
	assert.NoError(t, err, "Listing secrets should not return an error")
	assert.Len(t, secrets, numGoroutines+1, "There should be numGoroutines+1 secrets")

	// Helper function to check if a key exists in the secrets list
	containsKey := func(list []SecretDescription, key string) bool {
		for _, secret := range list {
			if secret.Key == key {
				return true
			}
		}
		return false
	}

	// Check if the original key exists
	assert.True(t, containsKey(secrets, "test-key"), "The list should contain the original key")

	// Check if all the keys created in the goroutines exist
	for i := 0; i < numGoroutines; i++ {
		keyName := fmt.Sprintf("key-%d", i)
		assert.True(t, containsKey(secrets, keyName), "The list should contain %s", keyName)
	}

	// Verify file-level consistency: reload from disk and confirm all secrets are present
	reloaded := createEncryptedManager(t, tempFile, password)
	reloadedSecrets, err := reloaded.ListSecrets(ctx)
	require.NoError(t, err, "Listing secrets from reloaded manager should not return an error")
	assert.Len(t, reloadedSecrets, numGoroutines+1, "Reloaded manager should have numGoroutines+1 secrets")

	assert.True(t, containsKey(reloadedSecrets, "test-key"), "Reloaded list should contain the original key")
	for i := 0; i < numGoroutines; i++ {
		keyName := fmt.Sprintf("key-%d", i)
		assert.True(t, containsKey(reloadedSecrets, keyName), "Reloaded list should contain %s", keyName)
	}
}

func TestEncryptedManager_DeleteSecrets_deletesSpecifiedKeys(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tempFile := createTempFile(t)

	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	require.NoError(t, manager.SetSecret(ctx, "key1", "value1"))
	require.NoError(t, manager.SetSecret(ctx, "key2", "value2"))
	require.NoError(t, manager.SetSecret(ctx, "key3", "value3"))

	err := manager.DeleteSecrets(ctx, []string{"key1", "key2"})
	require.NoError(t, err)

	_, err = manager.GetSecret(ctx, "key1")
	assert.Error(t, err, "key1 should have been deleted")
	assert.Contains(t, err.Error(), "not found")

	_, err = manager.GetSecret(ctx, "key2")
	assert.Error(t, err, "key2 should have been deleted")
	assert.Contains(t, err.Error(), "not found")

	value3, err := manager.GetSecret(ctx, "key3")
	require.NoError(t, err, "key3 should still exist")
	assert.Equal(t, "value3", value3)
}

func TestEncryptedManager_DeleteSecrets_persistsToDisk(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tempFile := createTempFile(t)

	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	require.NoError(t, manager.SetSecret(ctx, "key1", "value1"))
	require.NoError(t, manager.SetSecret(ctx, "key2", "value2"))
	require.NoError(t, manager.SetSecret(ctx, "key3", "value3"))

	require.NoError(t, manager.DeleteSecrets(ctx, []string{"key1", "key2"}))

	reloaded := createEncryptedManager(t, tempFile, password)

	_, err := reloaded.GetSecret(ctx, "key1")
	assert.Error(t, err, "key1 should be gone after reload")

	_, err = reloaded.GetSecret(ctx, "key2")
	assert.Error(t, err, "key2 should be gone after reload")

	reloadedValue3, err := reloaded.GetSecret(ctx, "key3")
	require.NoError(t, err, "key3 should persist across reload")
	assert.Equal(t, "value3", reloadedValue3)
}

func TestEncryptedManager_DeleteSecrets_emptyListIsNoop(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tempFile := createTempFile(t)

	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	require.NoError(t, manager.SetSecret(ctx, "key1", "value1"))

	require.NoError(t, manager.DeleteSecrets(ctx, []string{}))

	remaining, err := manager.ListSecrets(ctx)
	require.NoError(t, err)
	assert.Len(t, remaining, 1, "key1 should remain after no-op delete")
}

func TestEncryptedManager_DeleteSecrets_nonExistentKeysNoError(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tempFile := createTempFile(t)

	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	err := manager.DeleteSecrets(ctx, []string{"does-not-exist"})
	assert.NoError(t, err)
}

// Helper functions
func createTempFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "secrets-test.json")
}

// writeLegacySecretsFile writes a secrets file in the pre-framing format: the
// bare output of aes.Encrypt keyed with an unsalted SHA-256 of the password,
// with no header. This is what existing installations have on disk.
func writeLegacySecretsFile(t *testing.T, filePath string, password []byte, secrets map[string]string) {
	t.Helper()

	contents, err := json.Marshal(fileStructure{Secrets: secrets})
	require.NoError(t, err, "Marshalling the legacy secrets file should not return an error")

	legacyKey := sha256.Sum256(password)
	body, err := aes.Encrypt(contents, legacyKey[:])
	require.NoError(t, err, "Encrypting the legacy secrets file should not return an error")

	require.NoError(t, os.WriteFile(filePath, body, 0600), "Writing the legacy secrets file should not return an error")
}

// readLegacySecretsFile decrypts a file using the legacy algorithm only, so
// tests can prove ordinary operations did not silently upgrade it.
func readLegacySecretsFile(t *testing.T, filePath string, password []byte) map[string]string {
	t.Helper()

	body, err := os.ReadFile(filePath) // #nosec G304: test-controlled path
	require.NoError(t, err, "Reading the legacy secrets file should not return an error")
	require.NotEqual(t, secretsFileMagic, string(body[:len(secretsFileMagic)]), "The file should remain legacy")

	legacyKey := sha256.Sum256(password)
	decrypted, err := aes.Decrypt(body, legacyKey[:])
	require.NoError(t, err, "Decrypting the legacy secrets file should not return an error")

	var contents fileStructure
	require.NoError(t, json.Unmarshal(decrypted, &contents), "Decoding the legacy secrets file should not return an error")
	if contents.Secrets == nil {
		return make(map[string]string)
	}
	return contents.Secrets
}

// readFramedFile reads a secrets file and parses its header, requiring that the
// file is in the framed format.
func readFramedFile(t *testing.T, filePath string) ([]byte, []byte) {
	t.Helper()

	data, err := os.ReadFile(filePath) // #nosec G304: test-controlled path
	require.NoError(t, err, "Reading the secrets file should not return an error")
	require.Greater(t, len(data), headerLength, "The secrets file should be longer than the header")
	require.Equal(t, secretsFileMagic, string(data[:len(secretsFileMagic)]), "The secrets file should carry the magic prefix")

	salt, body, err := parseSecretsHeader(data)
	require.NoError(t, err, "Parsing the secrets file header should not return an error")
	return salt, body
}

func TestEncryptedManager_OpeningLegacyFileDoesNotChangeBytes(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tempFile := createTempFile(t)
	password := generateRandomPassword(t)
	writeLegacySecretsFile(t, tempFile, password, map[string]string{"legacy-key": "legacy-value"})
	original, err := os.ReadFile(tempFile)
	require.NoError(t, err)

	manager := createEncryptedManager(t, tempFile, password)
	value, err := manager.GetSecret(ctx, "legacy-key")
	require.NoError(t, err)
	assert.Equal(t, "legacy-value", value)
	current, err := os.ReadFile(tempFile)
	require.NoError(t, err)
	assert.Equal(t, original, current)
}

func TestEncryptedManager_OrdinaryOperationsPreserveLegacyFormat(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tests := []struct {
		name      string
		secrets   map[string]string
		operation func(*EncryptedManager) error
		expected  map[string]string
	}{
		{
			name:    "set",
			secrets: map[string]string{"existing": "value"},
			operation: func(manager *EncryptedManager) error {
				return manager.SetSecret(ctx, "new", "value")
			},
			expected: map[string]string{"existing": "value", "new": "value"},
		},
		{
			name:    "update",
			secrets: map[string]string{"existing": "value"},
			operation: func(manager *EncryptedManager) error {
				return manager.SetSecret(ctx, "existing", "updated")
			},
			expected: map[string]string{"existing": "updated"},
		},
		{
			name:    "delete",
			secrets: map[string]string{"remove": "value", "keep": "value"},
			operation: func(manager *EncryptedManager) error {
				return manager.DeleteSecret(ctx, "remove")
			},
			expected: map[string]string{"keep": "value"},
		},
		{
			name:    "delete multiple",
			secrets: map[string]string{"one": "value", "two": "value", "keep": "value"},
			operation: func(manager *EncryptedManager) error {
				return manager.DeleteSecrets(ctx, []string{"one", "two"})
			},
			expected: map[string]string{"keep": "value"},
		},
		{
			name:    "cleanup",
			secrets: map[string]string{"one": "value"},
			operation: func(manager *EncryptedManager) error {
				return manager.Cleanup()
			},
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempFile := createTempFile(t)
			password := generateRandomPassword(t)
			writeLegacySecretsFile(t, tempFile, password, tt.secrets)
			manager := createEncryptedManager(t, tempFile, password)

			require.NoError(t, tt.operation(manager))
			assert.Equal(t, tt.expected, readLegacySecretsFile(t, tempFile, password))
		})
	}
}

func TestEncryptedManager_UpgradeProtection(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tempFile := createTempFile(t)
	password := generateRandomPassword(t)
	writeLegacySecretsFile(t, tempFile, password, map[string]string{"legacy-key": "legacy-value"})
	manager := createEncryptedManager(t, tempFile, password)

	require.NoError(t, manager.UpgradeProtection())
	readFramedFile(t, tempFile)
	value, err := manager.GetSecret(ctx, "legacy-key")
	require.NoError(t, err)
	assert.Equal(t, "legacy-value", value)

	legacyKey := sha256.Sum256(password)
	upgraded, err := os.ReadFile(tempFile)
	require.NoError(t, err)
	_, err = aes.Decrypt(upgraded, legacyKey[:])
	assert.Error(t, err, "The legacy reader must not decrypt an upgraded store")
	require.NoError(t, manager.UpgradeProtection())
}

func TestEncryptedManager_UpgradeProtectionFailurePreservesLegacyFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can bypass directory permissions")
	}
	t.Parallel()

	tempFile := createTempFile(t)
	password := generateRandomPassword(t)
	want := map[string]string{"legacy-key": "legacy-value"}
	writeLegacySecretsFile(t, tempFile, password, want)
	original, err := os.ReadFile(tempFile)
	require.NoError(t, err)
	manager := createEncryptedManager(t, tempFile, password)

	// Pre-create the lock file so WithFileLock's own lock acquisition
	// doesn't need directory write permission. The failure this test wants
	// to force is inside UpgradeProtection's install step (an install that
	// can't create its temp file, e.g. a full disk or a read-only mount),
	// not in acquiring the lock — the fix in pkg/lockfile means acquisition
	// needs to create the lock file, which read-only-directory would also
	// block if it didn't already exist.
	require.NoError(t, os.WriteFile(tempFile+".lock", nil, 0600))

	directory := filepath.Dir(tempFile)
	require.NoError(t, os.Chmod(directory, 0500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(directory, 0700)) })

	require.Error(t, manager.UpgradeProtection())
	require.NoError(t, os.Chmod(directory, 0700))

	current, err := os.ReadFile(tempFile)
	require.NoError(t, err)
	assert.Equal(t, original, current)
	assert.Equal(t, want, readLegacySecretsFile(t, tempFile, password))
}

// TestEncryptedManager_VerifyUpgradeOrRollback_RestoresOnMismatch exercises the
// verify/rollback half of UpgradeProtection directly. A real verification
// failure only happens if the file is corrupted between the install write and
// the verify read; since UpgradeProtection holds the file lock across both,
// nothing else can do that to it in practice. Calling installUpgrade and
// verifyUpgradeOrRollback as separate steps — exactly what UpgradeProtection
// does internally — lets the test corrupt the installed file in between,
// deterministically reaching the rollback branch without adding a
// production-only test hook.
func TestEncryptedManager_VerifyUpgradeOrRollback_RestoresOnMismatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tempFile := createTempFile(t)
	password := generateRandomPassword(t)
	want := map[string]string{"legacy-key": "legacy-value"}
	writeLegacySecretsFile(t, tempFile, password, want)
	original, err := os.ReadFile(tempFile)
	require.NoError(t, err)
	manager := createEncryptedManager(t, tempFile, password)

	newSalt := make([]byte, saltLength)
	_, err = rand.Read(newSalt)
	require.NoError(t, err)
	require.NoError(t, manager.installUpgrade(want, fileFormat{version: fileFormatV1, salt: newSalt}))
	readFramedFile(t, tempFile) // sanity: install really produced a framed v1 file

	// Simulate the file being corrupted or interrupted between install and
	// verify (e.g. a crash mid-write on a future run, or bit rot).
	require.NoError(t, os.WriteFile(tempFile, []byte("corrupted"), 0600))

	err = manager.verifyUpgradeOrRollback(original, want)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restored original encrypted bytes")

	current, err := os.ReadFile(tempFile)
	require.NoError(t, err)
	assert.Equal(t, original, current, "rollback must restore the original encrypted bytes")

	// The restored file must still be usable as the legacy format it was.
	assert.Equal(t, want, readLegacySecretsFile(t, tempFile, password))
	value, err := manager.GetSecret(ctx, "legacy-key")
	require.NoError(t, err)
	assert.Equal(t, "legacy-value", value)
}

func TestEncryptedManager_StaleManagerPreservesUpgradedFormat(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tempFile := createTempFile(t)
	password := generateRandomPassword(t)
	writeLegacySecretsFile(t, tempFile, password, map[string]string{"legacy": "value"})

	staleManager := createEncryptedManager(t, tempFile, password)
	upgradingManager := createEncryptedManager(t, tempFile, password)
	require.NoError(t, upgradingManager.UpgradeProtection())

	require.NoError(t, staleManager.SetSecret(ctx, "new", "value"))
	readFramedFile(t, tempFile)

	value, err := staleManager.GetSecret(ctx, "new")
	require.NoError(t, err)
	assert.Equal(t, "value", value)
}

func TestEncryptedManager_NewFileIsFramed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tempFile := createTempFile(t)
	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	require.NoError(t, manager.SetSecret(ctx, "key1", "value1"), "Setting a secret should not return an error")
	require.NoError(t, manager.SetSecret(ctx, "key2", "value2"), "Setting a secret should not return an error")

	salt, _ := readFramedFile(t, tempFile)
	assert.Len(t, salt, saltLength, "A new file should carry a full-length salt")

	// Round-trip through a fresh manager: the derived key must be reproducible
	// from the password plus the salt stored in the file.
	reloaded := createEncryptedManager(t, tempFile, password)

	value, err := reloaded.GetSecret(ctx, "key1")
	require.NoError(t, err, "Getting a secret after reload should not return an error")
	assert.Equal(t, "value1", value, "The reloaded value should match the stored value")

	listed, err := reloaded.ListSecrets(ctx)
	require.NoError(t, err, "Listing secrets after reload should not return an error")
	assert.Len(t, listed, 2, "Both secrets should be listed after reload")

	require.NoError(t, reloaded.DeleteSecret(ctx, "key1"), "Deleting a secret should not return an error")
	_, err = reloaded.GetSecret(ctx, "key1")
	assert.ErrorIs(t, err, ErrSecretNotFound, "The deleted secret should no longer be found")
}

func TestEncryptedManager_WrongPasswordOnFramedFile(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tempFile := createTempFile(t)
	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)
	require.NoError(t, manager.SetSecret(ctx, "key1", "value1"), "Setting a secret should not return an error")

	// A wrong password derives a different key, so GCM authentication fails
	// rather than yielding garbage plaintext.
	_, err := NewEncryptedManager(tempFile, generateRandomPassword(t))
	require.Error(t, err, "Opening a framed file with the wrong password should return an error")
	assert.Contains(t, err.Error(), "unable to decrypt", "Error message should indicate decryption failed")

	// The failed attempt must not have damaged the file.
	reloaded := createEncryptedManager(t, tempFile, password)
	value, err := reloaded.GetSecret(ctx, "key1")
	require.NoError(t, err, "The correct password should still open the file")
	assert.Equal(t, "value1", value, "The stored value should be unchanged")
}

func TestEncryptedManager_MalformedHeader(t *testing.T) {
	t.Parallel()

	// withBody appends a plausible encrypted body so that any failure comes
	// from the header alone.
	withBody := func(header []byte) []byte {
		return append(header, make([]byte, 32)...)
	}

	// encodeSecretsHeader returns a fresh slice, so mutating this copy is safe.
	badVersion := withBody(encodeSecretsHeader(make([]byte, saltLength)))
	badVersion[len(secretsFileMagic)] = 0x02

	tests := []struct {
		name    string
		data    []byte
		wantMsg string
		wantErr error
	}{
		{
			name:    "truncated below the header",
			data:    append([]byte(secretsFileMagic), 0x01, 0x00),
			wantMsg: "header is truncated",
			wantErr: ErrMalformedSecretsFile,
		},
		{
			name:    "unsupported format version",
			data:    badVersion,
			wantMsg: "unsupported secrets file format version",
			wantErr: ErrUnsupportedSecretsFileVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempFile := createTempFile(t)
			require.NoError(t, os.WriteFile(tempFile, tt.data, 0600))

			var err error
			require.NotPanics(t, func() {
				_, err = NewEncryptedManager(tempFile, []byte("test-password"))
			}, "A malformed header must not panic")

			require.Error(t, err, "A malformed header should return an error")
			assert.ErrorIs(t, err, tt.wantErr, "The error should have the expected classification")
			assert.Contains(t, err.Error(), tt.wantMsg, "Error message should describe the defect")
		})
	}
}

func TestEncryptedManager_SaltIsStableAcrossWrites(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tempFile := createTempFile(t)
	password := generateRandomPassword(t)
	manager := createEncryptedManager(t, tempFile, password)

	require.NoError(t, manager.SetSecret(ctx, "key1", "value1"), "Setting a secret should not return an error")
	firstSalt, firstBody := readFramedFile(t, tempFile)

	// Write the identical value again: the salt is per-file, so it must be
	// reused, but the body must differ because GCM uses a fresh nonce.
	require.NoError(t, manager.SetSecret(ctx, "key1", "value1"), "Rewriting a secret should not return an error")
	secondSalt, secondBody := readFramedFile(t, tempFile)

	assert.Equal(t, firstSalt, secondSalt, "The salt should be reused between writes to the same file")
	assert.NotEqual(t, firstBody, secondBody, "Each write should produce a fresh nonce and therefore a different body")
}

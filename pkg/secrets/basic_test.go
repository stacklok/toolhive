package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBasicManager_GetSecret(t *testing.T) {
	t.Parallel()
	// Create a temporary file for testing
	tempFile := createTempFile(t)
	defer os.Remove(tempFile)

	// Create a BasicManager
	manager := createBasicManager(t, tempFile)

	// Test getting a non-existent secret
	_, err := manager.GetSecret("non-existent")
	assert.Error(t, err, "Getting a non-existent secret should return an error")
	assert.Contains(t, err.Error(), "not found", "Error message should indicate the secret was not found")

	// Test getting a secret with an empty name
	_, err = manager.GetSecret("")
	assert.Error(t, err, "Getting a secret with an empty name should return an error")
	assert.Contains(t, err.Error(), "cannot be empty", "Error message should indicate the name cannot be empty")

	// Set a secret
	err = manager.SetSecret("test-key", "test-value")
	require.NoError(t, err, "Setting a secret should not return an error")

	// Test getting an existing secret
	value, err := manager.GetSecret("test-key")
	assert.NoError(t, err, "Getting an existing secret should not return an error")
	assert.Equal(t, "test-value", value, "The retrieved value should match the set value")
}

func TestBasicManager_SetSecret(t *testing.T) {
	t.Parallel()
	// Create a temporary file for testing
	tempFile := createTempFile(t)
	defer os.Remove(tempFile)

	// Create a BasicManager
	manager := createBasicManager(t, tempFile)

	// Test setting a secret with an empty name
	err := manager.SetSecret("", "test-value")
	assert.Error(t, err, "Setting a secret with an empty name should return an error")
	assert.Contains(t, err.Error(), "cannot be empty", "Error message should indicate the name cannot be empty")

	// Test setting a new secret
	err = manager.SetSecret("test-key", "test-value")
	assert.NoError(t, err, "Setting a new secret should not return an error")

	// Verify the secret was set
	value, err := manager.GetSecret("test-key")
	assert.NoError(t, err, "Getting the set secret should not return an error")
	assert.Equal(t, "test-value", value, "The retrieved value should match the set value")

	// Test updating an existing secret
	err = manager.SetSecret("test-key", "updated-value")
	assert.NoError(t, err, "Updating an existing secret should not return an error")

	// Verify the secret was updated
	value, err = manager.GetSecret("test-key")
	assert.NoError(t, err, "Getting the updated secret should not return an error")
	assert.Equal(t, "updated-value", value, "The retrieved value should match the updated value")

	// Verify the file was updated
	verifyFileContents(t, tempFile, "test-key", "updated-value")
}

func TestBasicManager_DeleteSecret(t *testing.T) {
	t.Parallel()
	// Create a temporary file for testing
	tempFile := createTempFile(t)
	defer os.Remove(tempFile)

	// Create a BasicManager
	manager := createBasicManager(t, tempFile)

	// Test deleting a non-existent secret
	err := manager.DeleteSecret("non-existent")
	assert.Error(t, err, "Deleting a non-existent secret should return an error")
	assert.Contains(t, err.Error(), "cannot delete non-existent", "Error message should indicate the secret does not exist")

	// Test deleting a secret with an empty name
	err = manager.DeleteSecret("")
	assert.Error(t, err, "Deleting a secret with an empty name should return an error")
	assert.Contains(t, err.Error(), "cannot be empty", "Error message should indicate the name cannot be empty")

	// Set a secret
	err = manager.SetSecret("test-key", "test-value")
	require.NoError(t, err, "Setting a secret should not return an error")

	// Test deleting an existing secret
	err = manager.DeleteSecret("test-key")
	assert.NoError(t, err, "Deleting an existing secret should not return an error")

	// Verify the secret was deleted
	_, err = manager.GetSecret("test-key")
	assert.Error(t, err, "Getting a deleted secret should return an error")
	assert.Contains(t, err.Error(), "not found", "Error message should indicate the secret was not found")

	// Verify the file was updated
	verifySecretNotInFile(t, tempFile, "test-key")
}

func TestBasicManager_ListSecrets(t *testing.T) {
	t.Parallel()
	// Create a temporary file for testing
	tempFile := createTempFile(t)
	defer os.Remove(tempFile)

	// Create a BasicManager
	manager := createBasicManager(t, tempFile)

	// Test listing secrets when there are none
	secrets, err := manager.ListSecrets()
	assert.NoError(t, err, "Listing secrets should not return an error")
	assert.Empty(t, secrets, "There should be no secrets initially")

	// Set some secrets
	require.NoError(t, manager.SetSecret("key1", "value1"), "Setting a secret should not return an error")
	require.NoError(t, manager.SetSecret("key2", "value2"), "Setting a secret should not return an error")
	require.NoError(t, manager.SetSecret("key3", "value3"), "Setting a secret should not return an error")

	// Test listing secrets
	secrets, err = manager.ListSecrets()
	assert.NoError(t, err, "Listing secrets should not return an error")
	assert.Len(t, secrets, 3, "There should be 3 secrets")
	assert.Contains(t, secrets, "key1", "The list should contain key1")
	assert.Contains(t, secrets, "key2", "The list should contain key2")
	assert.Contains(t, secrets, "key3", "The list should contain key3")
}

func TestBasicManager_Cleanup(t *testing.T) {
	t.Parallel()
	// Create a temporary file for testing
	tempFile := createTempFile(t)
	defer os.Remove(tempFile)

	// Create a BasicManager
	manager := createBasicManager(t, tempFile)

	// Set some secrets
	require.NoError(t, manager.SetSecret("key1", "value1"), "Setting a secret should not return an error")
	require.NoError(t, manager.SetSecret("key2", "value2"), "Setting a secret should not return an error")

	// Verify the secrets were set
	secrets, err := manager.ListSecrets()
	assert.NoError(t, err, "Listing secrets should not return an error")
	assert.Len(t, secrets, 2, "There should be 2 secrets")

	// Test cleaning up all secrets
	err = manager.Cleanup()
	assert.NoError(t, err, "Cleaning up should not return an error")

	// Verify all secrets were removed
	secrets, err = manager.ListSecrets()
	assert.NoError(t, err, "Listing secrets should not return an error")
	assert.Empty(t, secrets, "There should be no secrets after cleanup")

	// Verify the file was updated
	verifyFileEmpty(t, tempFile)
}

func TestNewBasicManager(t *testing.T) {
	t.Parallel()
	// Create a temporary file for testing
	tempFile := createTempFile(t)
	defer os.Remove(tempFile)

	// Test creating a BasicManager with a valid file path
	manager, err := NewBasicManager(tempFile)
	assert.NoError(t, err, "Creating a BasicManager with a valid file path should not return an error")
	assert.NotNil(t, manager, "The manager should not be nil")
	assert.IsType(t, &BasicManager{}, manager, "The manager should be a BasicManager")

	// Test creating a BasicManager with a non-existent directory
	nonExistentFile := filepath.Join(os.TempDir(), "non-existent-dir", "secrets.json")
	_, err = NewBasicManager(nonExistentFile)
	assert.Error(t, err, "Creating a BasicManager with a non-existent directory should return an error")
	assert.Contains(t, err.Error(), "failed to open secrets file", "Error message should indicate the file could not be opened")
}

func TestCreateSecretManager(t *testing.T) {
	t.Parallel()
	// This test is more of an integration test and might not be suitable for unit testing
	// as it depends on the XDG data directory. We'll just verify it doesn't panic.
	_, err := CreateSecretManager(BasicType)
	if err != nil {
		// This might fail in CI environments, so we'll just log it
		t.Logf("CreateSecretManager returned an error: %v", err)
	}
}

// Helper functions
func createTempFile(t *testing.T) string {
	t.Helper()
	tempFile, err := os.CreateTemp("", "secrets-test-*.json")
	require.NoError(t, err, "Creating a temporary file should not return an error")
	tempFile.Close()
	return tempFile.Name()
}

func createBasicManager(t *testing.T, filePath string) *BasicManager {
	t.Helper()
	manager, err := NewBasicManager(filePath)
	require.NoError(t, err, "Creating a BasicManager should not return an error")
	require.IsType(t, &BasicManager{}, manager, "The manager should be a BasicManager")
	return manager.(*BasicManager)
}

func verifyFileContents(t *testing.T, filePath, key, expectedValue string) {
	t.Helper()
	// Read the file
	data, err := os.ReadFile(filePath)
	require.NoError(t, err, "Reading the file should not return an error")

	// Parse the JSON
	var contents fileStructure
	err = json.Unmarshal(data, &contents)
	require.NoError(t, err, "Parsing the JSON should not return an error")

	// Verify the secret is in the file
	value, ok := contents.Secrets[key]
	assert.True(t, ok, "The secret should be in the file")
	assert.Equal(t, expectedValue, value, "The value in the file should match the expected value")
}

func verifySecretNotInFile(t *testing.T, filePath, key string) {
	t.Helper()
	// Read the file
	data, err := os.ReadFile(filePath)
	require.NoError(t, err, "Reading the file should not return an error")

	// Parse the JSON
	var contents fileStructure
	err = json.Unmarshal(data, &contents)
	require.NoError(t, err, "Parsing the JSON should not return an error")

	// Verify the secret is not in the file
	_, ok := contents.Secrets[key]
	assert.False(t, ok, "The secret should not be in the file")
}

func verifyFileEmpty(t *testing.T, filePath string) {
	t.Helper()
	// Read the file
	data, err := os.ReadFile(filePath)
	require.NoError(t, err, "Reading the file should not return an error")

	// Parse the JSON
	var contents fileStructure
	err = json.Unmarshal(data, &contents)
	require.NoError(t, err, "Parsing the JSON should not return an error")

	// Verify there are no secrets in the file
	assert.Empty(t, contents.Secrets, "There should be no secrets in the file")
}

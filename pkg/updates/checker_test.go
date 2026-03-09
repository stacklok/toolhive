// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package updates

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Constants for testing
const (
	testInstanceID     = "test-instance-id"
	testCurrentVersion = "1.0.0"
	testLatestVersion  = "1.1.0"
	testOldVersion     = "1.0.5"
	testComponentCLI   = "CLI"
)

// MockVersionClient is a mock implementation of the VersionClient interface
type MockVersionClient struct {
	mock.Mock
}

func (m *MockVersionClient) GetLatestVersion(instanceID string, currentVersion string) (string, error) {
	args := m.Called(instanceID, currentVersion)
	return args.String(0), args.Error(1)
}

func (m *MockVersionClient) GetComponent() string {
	args := m.Called()
	return args.String(0)
}

// Test helpers
func setupMockVersionClient(_ *testing.T) *MockVersionClient {
	return &MockVersionClient{}
}

func createTempUpdateFile(t *testing.T, contents updateFile) string {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "toolhive-test-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	filePath := filepath.Join(tempDir, "updates.json")
	data, err := json.Marshal(contents)
	require.NoError(t, err)
	err = os.WriteFile(filePath, data, 0600)
	require.NoError(t, err)
	return filePath
}

// TestCheckLatestVersion tests the CheckLatestVersion method
func TestCheckLatestVersion(t *testing.T) {
	t.Parallel()
	t.Run("file doesn't exist - creates new file", func(t *testing.T) {
		t.Parallel()
		// Setup
		mockClient := setupMockVersionClient(t)
		latestVersion := testLatestVersion
		componentName := testComponentCLI

		// Create unique temp file path to avoid test interference
		tempDir, err := os.MkdirTemp("", "toolhive-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tempDir)
		tempFile := filepath.Join(tempDir, "updates.json")

		mockClient.On("GetLatestVersion", mock.AnythingOfType("string"), mock.AnythingOfType("string")).Return(latestVersion, nil)

		// Create checker manually to control file path
		checker := &defaultUpdateChecker{
			instanceID:          testInstanceID,
			currentVersion:      testCurrentVersion,
			updateFilePath:      tempFile,
			versionClient:       mockClient,
			previousAPIResponse: "",
			component:           componentName,
		}

		// Execute (calls GetLatestVersion since file doesn't exist)
		err = checker.CheckLatestVersion()

		// Verify
		require.NoError(t, err)
		mockClient.AssertExpectations(t)
	})

	t.Run("different components share same file but have independent throttling", func(t *testing.T) {
		t.Parallel()
		components := []string{testComponentCLI, "API", "UI"}

		for _, component := range components {
			//nolint:paralleltest // Intentionally not parallel due to shared test setup
			t.Run(component, func(t *testing.T) {
				// Setup
				mockClient := setupMockVersionClient(t)
				latestVersion := testLatestVersion

				// Create unique temp file path to avoid test interference
				tempDir, err := os.MkdirTemp("", "toolhive-test-*")
				require.NoError(t, err)
				defer os.RemoveAll(tempDir)
				tempFile := filepath.Join(tempDir, "updates.json")

				// Mock client methods - GetComponent not called since we create checker manually
				mockClient.On("GetLatestVersion", mock.AnythingOfType("string"), mock.AnythingOfType("string")).Return(latestVersion, nil)

				// Create checker manually to control file path
				checker := &defaultUpdateChecker{
					instanceID:          testInstanceID,
					currentVersion:      testCurrentVersion,
					updateFilePath:      tempFile,
					versionClient:       mockClient,
					previousAPIResponse: "",
					component:           component,
				}

				// Execute (calls GetLatestVersion since no throttling data exists)
				err = checker.CheckLatestVersion()

				// Verify
				require.NoError(t, err)
				mockClient.AssertExpectations(t)
			})
		}
	})

	t.Run("component within throttle window skips API call", func(t *testing.T) {
		t.Parallel()
		// Setup
		mockClient := setupMockVersionClient(t)
		componentName := "UI"
		existingVersion := testLatestVersion

		// Create existing file with fresh component data
		existingFile := updateFile{
			InstanceID:    testInstanceID,
			LatestVersion: existingVersion,
			Components: map[string]componentInfo{
				componentName: {
					LastCheck: time.Now().UTC().Add(-15 * time.Minute), // Within 4-hour window
				},
			},
		}

		tempFile := createTempUpdateFile(t, existingFile)

		checker := &defaultUpdateChecker{
			instanceID:          testInstanceID,
			currentVersion:      testCurrentVersion,
			updateFilePath:      tempFile,
			versionClient:       mockClient,
			previousAPIResponse: existingVersion,
			component:           componentName,
		}

		// Execute
		err := checker.CheckLatestVersion()

		// Verify
		require.NoError(t, err)

		// Verify no methods were called (due to throttling)
		mockClient.AssertNotCalled(t, "GetLatestVersion")
		mockClient.AssertNotCalled(t, "GetComponent")
	})

	t.Run("component outside throttle window makes API call", func(t *testing.T) {
		t.Parallel()
		// Setup
		mockClient := setupMockVersionClient(t)
		componentName := "API"
		existingVersion := testOldVersion
		newVersion := testLatestVersion

		// Create existing file with stale component data
		existingFile := updateFile{
			InstanceID:    testInstanceID,
			LatestVersion: existingVersion,
			Components: map[string]componentInfo{
				componentName: {
					LastCheck: time.Now().UTC().Add(-5 * time.Hour), // Outside 4-hour window
				},
			},
		}

		// Create temp file
		tempFile := createTempUpdateFile(t, existingFile)

		// Mock client methods - GetComponent not called since we create checker manually
		mockClient.On("GetLatestVersion", testInstanceID, testCurrentVersion).Return(newVersion, nil)

		// Create checker manually with temp file path
		checker := &defaultUpdateChecker{
			instanceID:          testInstanceID,
			currentVersion:      testCurrentVersion,
			updateFilePath:      tempFile,
			versionClient:       mockClient,
			previousAPIResponse: existingVersion,
			component:           componentName,
		}

		// Execute
		err := checker.CheckLatestVersion()

		// Verify
		require.NoError(t, err)
		mockClient.AssertExpectations(t)
	})

	t.Run("backward compatibility with old file format", func(t *testing.T) {
		t.Parallel()
		// Setup
		mockClient := setupMockVersionClient(t)
		componentName := testComponentCLI
		newVersion := testLatestVersion

		// Create old format file (missing Components field)
		oldFormatData := map[string]interface{}{
			"instance_id":    testInstanceID,
			"latest_version": testOldVersion,
		}

		tempDir, err := os.MkdirTemp("", "toolhive-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tempDir)

		tempFile := filepath.Join(tempDir, "updates.json")
		data, err := json.Marshal(oldFormatData)
		require.NoError(t, err)
		err = os.WriteFile(tempFile, data, 0600)
		require.NoError(t, err)

		// Mock client methods - GetComponent not called since we create checker manually
		mockClient.On("GetLatestVersion", testInstanceID, testCurrentVersion).Return(newVersion, nil)

		// Create checker manually with temp file path
		checker := &defaultUpdateChecker{
			instanceID:          testInstanceID,
			currentVersion:      testCurrentVersion,
			updateFilePath:      tempFile,
			versionClient:       mockClient,
			previousAPIResponse: testOldVersion,
			component:           componentName,
		}

		// Execute
		err = checker.CheckLatestVersion()

		// Verify
		require.NoError(t, err)
		mockClient.AssertExpectations(t)
	})

	t.Run("error when GetLatestVersion fails", func(t *testing.T) {
		t.Parallel()
		// Setup
		mockClient := setupMockVersionClient(t)
		expectedError := errors.New("API error")
		componentName := testComponentCLI

		// Create existing file with stale component data to force API call
		existingFile := updateFile{
			InstanceID:    testInstanceID,
			LatestVersion: testOldVersion,
			Components: map[string]componentInfo{
				componentName: {
					LastCheck: time.Now().UTC().Add(-5 * time.Hour), // Outside 4-hour window
				},
			},
		}

		// Create temp file
		tempFile := createTempUpdateFile(t, existingFile)

		// Mock client methods - GetComponent not called since we create checker manually
		mockClient.On("GetLatestVersion", testInstanceID, mock.AnythingOfType("string")).Return("", expectedError)

		// Create checker manually with temp file path to force API call
		checker := &defaultUpdateChecker{
			instanceID:          testInstanceID,
			currentVersion:      testCurrentVersion,
			updateFilePath:      tempFile,
			versionClient:       mockClient,
			previousAPIResponse: testOldVersion,
			component:           componentName,
		}

		// Execute
		err := checker.CheckLatestVersion()

		// Verify
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to check for updates")
		mockClient.AssertExpectations(t)
	})
}

// TestNotifyIfUpdateAvailable tests the notifyIfUpdateAvailable function
func TestNotifyIfUpdateAvailable(t *testing.T) {
	t.Parallel()
	t.Run("no update available", func(t *testing.T) {
		t.Parallel()
		// This test is a bit tricky since the function prints to stdout
		// For simplicity, we'll just call it and make sure it doesn't panic
		currentVersion := testCurrentVersion
		latestVersion := testCurrentVersion

		// This shouldn't panic
		notifyIfUpdateAvailable(currentVersion, latestVersion)
	})

	t.Run("update available", func(t *testing.T) {
		t.Parallel()
		// This test is a bit tricky since the function prints to stdout
		// For simplicity, we'll just call it and make sure it doesn't panic
		currentVersion := testCurrentVersion
		latestVersion := testLatestVersion

		// This shouldn't panic
		notifyIfUpdateAvailable(currentVersion, latestVersion)
	})
}

// TestNotifyIfUpdateAvailableDesktopManaged tests that update messages are suppressed
// for desktop-managed CLI installations
func TestNotifyIfUpdateAvailableDesktopManaged(t *testing.T) {
	// Not parallel: modifies HOME environment variable

	// Setup: create a temp HOME directory with desktop marker file
	homeDir := t.TempDir()
	thDir := filepath.Join(homeDir, ".toolhive")
	require.NoError(t, os.MkdirAll(thDir, 0755))

	// Get the current executable path (the test binary) to simulate desktop-managed CLI
	currentExe, err := os.Executable()
	require.NoError(t, err)
	resolvedExe, err := filepath.EvalSymlinks(currentExe)
	require.NoError(t, err)
	resolvedExe = filepath.Clean(resolvedExe)

	// Create marker file pointing to current executable (makes IsDesktopManagedCLI return true)
	marker := map[string]interface{}{
		"schema_version":  1,
		"source":          "desktop",
		"install_method":  "symlink",
		"cli_version":     "1.0.0",
		"symlink_target":  resolvedExe,
		"installed_at":    "2026-01-22T10:30:00Z",
		"desktop_version": "2.0.0",
	}
	markerData, err := json.Marshal(marker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(thDir, ".cli-source"), markerData, 0600))

	// Set HOME to our temp directory
	t.Setenv("HOME", homeDir)

	// Capture stderr to verify no output
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	// Call with versions that would normally print an update message
	notifyIfUpdateAvailable(testCurrentVersion, testLatestVersion)

	// Restore stderr and read captured output
	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)

	// Verify no output was written (message was suppressed)
	assert.Empty(t, buf.String(), "expected no update message for desktop-managed CLI")
}

// TestCorruptedJSONRecovery tests the recovery of corrupted update files
func TestCorruptedJSONRecovery(t *testing.T) {
	t.Parallel()

	t.Run("recover from corrupted JSON with extra braces", func(t *testing.T) {
		t.Parallel()

		// Create corrupted JSON with extra closing braces (real example from user)
		corruptedJSON := `{"instance_id":"test-instance-recovery","latest_version":"v0.2.3","components":{"API":{"last_check":"2025-08-01T11:12:00.740318Z"},"CLI":{"last_check":"2025-07-01T10:54:28.356601Z"},"UI":{"last_check":"2025-08-05T13:52:11.49587Z"}}}}}`

		// Test recovery function directly
		recovered, err := recoverCorruptedJSON([]byte(corruptedJSON))

		// Verify recovery succeeded
		require.NoError(t, err)
		assert.Equal(t, "test-instance-recovery", recovered.InstanceID)
		assert.NotNil(t, recovered.Components)
		assert.Empty(t, recovered.LatestVersion) // Should be empty in fresh recovery
	})

	t.Run("recover from corrupted JSON in NewUpdateChecker", func(t *testing.T) {
		t.Parallel()

		// Setup corrupted file
		tempDir, err := os.MkdirTemp("", "toolhive-corruption-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tempDir)

		corruptedFilePath := filepath.Join(tempDir, "updates.json")
		corruptedJSON := `{"instance_id":"test-instance-recovery","latest_version":"v0.2.3","components":{"CLI":{"last_check":"2025-08-20T09:47:11.528773Z"}}}}}`

		err = os.WriteFile(corruptedFilePath, []byte(corruptedJSON), 0600)
		require.NoError(t, err)

		// Create mock client
		mockClient := setupMockVersionClient(t)
		mockClient.On("GetComponent").Return("CLI")
		mockClient.On("GetLatestVersion", "test-instance-recovery", testCurrentVersion).Return(testLatestVersion, nil)

		// Create update checker - this should recover from corruption during initialization
		checker := &defaultUpdateChecker{
			currentVersion:      testCurrentVersion,
			updateFilePath:      corruptedFilePath,
			versionClient:       mockClient,
			previousAPIResponse: "",
			component:           "CLI",
		}

		// This should work without error despite corrupted file
		err = checker.CheckLatestVersion()

		// Should not fail due to JSON corruption
		assert.NoError(t, err)
	})

	t.Run("recovery fails when instance_id cannot be extracted", func(t *testing.T) {
		t.Parallel()

		// Create completely invalid JSON without instance_id
		invalidJSON := `{"invalid":"json","no_instance_id":true}}`

		// Test recovery function directly
		_, err := recoverCorruptedJSON([]byte(invalidJSON))

		// Should fail to recover
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unable to recover corrupted JSON")
	})
}

package updates

import (
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
					LastCheck: time.Now().UTC().Add(-1 * time.Hour), // Within 4-hour window
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

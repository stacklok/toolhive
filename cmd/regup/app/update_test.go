package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/registry"
)

//nolint:paralleltest // This test manages temporary directories and cannot run in parallel
func TestUpdateCmdFunc(t *testing.T) {

	tests := []struct {
		name        string
		count       int
		serverName  string
		dryRun      bool
		expectError bool
		errorMsg    string
		setup       func(t *testing.T) (string, func())
	}{
		{
			name:        "default behavior - update oldest server",
			count:       1,
			serverName:  "",
			dryRun:      true,
			expectError: false,
			setup:       setupTestRegistryWithMultipleServers,
		},
		{
			name:        "count behavior - update multiple oldest servers",
			count:       2,
			serverName:  "",
			dryRun:      true,
			expectError: false,
			setup:       setupTestRegistryWithMultipleServers,
		},
		{
			name:        "server behavior - update specific server",
			count:       1,
			serverName:  "github",
			dryRun:      true,
			expectError: false,
			setup:       setupTestRegistryWithMultipleServers,
		},
		{
			name:        "invalid server name",
			count:       1,
			serverName:  "nonexistent",
			dryRun:      true,
			expectError: true,
			errorMsg:    "server 'nonexistent' not found in registry",
			setup:       setupTestRegistryWithMultipleServers,
		},
		{
			name:        "empty registry",
			count:       1,
			serverName:  "",
			dryRun:      true,
			expectError: false,
			setup:       setupEmptyTestRegistry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test environment
			originalDir, cleanup := tt.setup(t)
			defer cleanup()

			// Change to test directory
			originalWd, err := os.Getwd()
			require.NoError(t, err)
			err = os.Chdir(originalDir)
			require.NoError(t, err)
			defer func() {
				err := os.Chdir(originalWd)
				require.NoError(t, err)
			}()

			// Set test variables
			originalCount := count
			originalServerName := serverName
			originalDryRun := dryRun
			originalGithubToken := githubToken

			count = tt.count
			serverName = tt.serverName
			dryRun = tt.dryRun
			githubToken = "test-token" // Set a test token to avoid API calls

			defer func() {
				count = originalCount
				serverName = originalServerName
				dryRun = originalDryRun
				githubToken = originalGithubToken
			}()

			// Run the function
			err = updateCmdFunc(nil, nil)

			// Check results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestServerSelection(t *testing.T) {
	t.Parallel()
	// Test that server selection works correctly
	testDir, cleanup := setupTestRegistryWithMultipleServers(t)
	defer cleanup()

	// Change to test directory
	originalWd, err := os.Getwd()
	require.NoError(t, err)
	err = os.Chdir(testDir)
	require.NoError(t, err)
	defer func() {
		err := os.Chdir(originalWd)
		require.NoError(t, err)
	}()

	// Read the registry
	registryPath := filepath.Join("pkg", "registry", "data", "registry.json")
	data, err := os.ReadFile(registryPath)
	require.NoError(t, err)

	var reg registry.Registry
	err = json.Unmarshal(data, &reg)
	require.NoError(t, err)

	// Test server exists
	_, exists := reg.Servers["github"]
	assert.True(t, exists, "github server should exist in test registry")

	_, exists = reg.Servers["gitlab"]
	assert.True(t, exists, "gitlab server should exist in test registry")

	_, exists = reg.Servers["nonexistent"]
	assert.False(t, exists, "nonexistent server should not exist in test registry")
}

// Helper functions

func setupTestRegistryWithMultipleServers(t *testing.T) (string, func()) {
	t.Helper()
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "regup-test-*")
	require.NoError(t, err)

	// Create registry directory structure
	registryDir := filepath.Join(tempDir, "pkg", "registry", "data")
	err = os.MkdirAll(registryDir, 0755)
	require.NoError(t, err)

	// Create test registry with multiple servers
	testRegistry := &registry.Registry{
		LastUpdated: "2025-06-16T12:00:00Z",
		Servers: map[string]*registry.ImageMetadata{
			"github": {
				BaseServerMetadata: registry.BaseServerMetadata{
					Name:          "github",
					Description:   "GitHub MCP server",
					RepositoryURL: "https://github.com/github/github-mcp-server",
					Metadata: &registry.Metadata{
						Stars:       100,
						Pulls:       5000,
						LastUpdated: "2025-06-16T12:00:00Z", // Older
					},
				},
				Image: "ghcr.io/github/github-mcp-server:latest",
			},
			"gitlab": {
				BaseServerMetadata: registry.BaseServerMetadata{
					Name:          "gitlab",
					Description:   "GitLab MCP server",
					RepositoryURL: "https://github.com/example/gitlab-mcp-server",
					Metadata: &registry.Metadata{
						Stars:       50,
						Pulls:       2000,
						LastUpdated: "2025-06-17T12:00:00Z", // Newer
					},
				},
				Image: "mcp/gitlab:latest",
			},
			"fetch": {
				BaseServerMetadata: registry.BaseServerMetadata{
					Name:          "fetch",
					Description:   "Fetch MCP server",
					RepositoryURL: "https://github.com/example/fetch-mcp-server",
					Metadata: &registry.Metadata{
						Stars:       25,
						Pulls:       1000,
						LastUpdated: "2025-06-15T12:00:00Z", // Oldest
					},
				},
				Image: "mcp/fetch:latest",
			},
		},
	}

	// Write registry file
	registryPath := filepath.Join(registryDir, "registry.json")
	data, err := json.MarshalIndent(testRegistry, "", "  ")
	require.NoError(t, err)
	err = os.WriteFile(registryPath, data, 0644)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return tempDir, cleanup
}

func setupEmptyTestRegistry(t *testing.T) (string, func()) {
	t.Helper()
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "regup-test-empty-*")
	require.NoError(t, err)

	// Create registry directory structure
	registryDir := filepath.Join(tempDir, "pkg", "registry", "data")
	err = os.MkdirAll(registryDir, 0755)
	require.NoError(t, err)

	// Create empty test registry
	testRegistry := &registry.Registry{
		LastUpdated: "2025-06-16T12:00:00Z",
		Servers:     map[string]*registry.ImageMetadata{},
	}

	// Write registry file
	registryPath := filepath.Join(registryDir, "registry.json")
	data, err := json.MarshalIndent(testRegistry, "", "  ")
	require.NoError(t, err)
	err = os.WriteFile(registryPath, data, 0644)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return tempDir, cleanup
}

func TestExtractOwnerRepo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		url           string
		expectedOwner string
		expectedRepo  string
		expectError   bool
	}{
		{
			name:          "standard github url",
			url:           "https://github.com/owner/repo",
			expectedOwner: "owner",
			expectedRepo:  "repo",
			expectError:   false,
		},
		{
			name:          "github url with .git suffix",
			url:           "https://github.com/owner/repo.git",
			expectedOwner: "owner",
			expectedRepo:  "repo",
			expectError:   false,
		},
		{
			name:        "invalid url",
			url:         "invalid",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner, repo, err := extractOwnerRepo(tt.url)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedOwner, owner)
				assert.Equal(t, tt.expectedRepo, repo)
			}
		})
	}
}

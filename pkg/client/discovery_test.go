package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/logger"
)

// createTestClientIntegrations creates fake client integrations for testing
// These match the file structure that the tests create
func createTestClientIntegrations() []mcpClientConfig {
	return []mcpClientConfig{
		{
			ClientType:   ClaudeCode,
			Description:  "Claude Code CLI (Test)",
			SettingsFile: ".claude.json",
			RelPath:      []string{}, // File is directly in home directory
			Extension:    JSON,
		},
		{
			ClientType:   Cursor,
			Description:  "Cursor editor (Test)",
			SettingsFile: ".cursor",
			RelPath:      []string{}, // File is directly in home directory
			Extension:    JSON,
		},
		{
			ClientType:   VSCode,
			Description:  "VS Code (Test)",
			SettingsFile: "settings.json",
			RelPath:      []string{}, // For test simplicity, no nested path
			Extension:    JSON,
		},
	}
}

// createTestConfigProvider creates a config provider for testing with the provided configuration.
func createTestConfigProvider(t *testing.T, cfg *config.Config) (config.Provider, func()) {
	t.Helper()

	// Create a temporary directory for the test
	tempDir := t.TempDir()

	// Create the config directory structure
	configDir := filepath.Join(tempDir, "toolhive")
	err := os.MkdirAll(configDir, 0755)
	require.NoError(t, err)

	// Set up the config file path
	configPath := filepath.Join(configDir, "config.yaml")

	// Create a path-based config provider
	provider := config.NewPathProvider(configPath)

	// Write the config file if one is provided
	if cfg != nil {
		err = provider.UpdateConfig(func(c *config.Config) { *c = *cfg })
		require.NoError(t, err)
	}

	return provider, func() {
		// Cleanup is handled by t.TempDir()
	}
}

func TestGetClientStatus(t *testing.T) {
	t.Parallel()

	// Setup a temporary home directory for testing
	tempHome, err := os.MkdirTemp("", "toolhive-test-home")
	require.NoError(t, err)
	defer os.RemoveAll(tempHome)

	// Create mock config with registered clients
	mockConfig := &config.Config{
		Clients: config.Clients{
			RegisteredClients: []string{string(ClaudeCode)},
		},
	}
	configProvider, cleanup := createTestConfigProvider(t, mockConfig)
	defer cleanup()

	// Create a mock Cursor config file
	_, err = os.Create(filepath.Join(tempHome, ".cursor"))
	require.NoError(t, err)

	// Create a mock ClaudeCode config file
	_, err = os.Create(filepath.Join(tempHome, ".claude.json"))
	require.NoError(t, err)

	// Create explicit client integrations for this test to avoid race conditions with global variable
	clientIntegrations := []mcpClientConfig{
		{
			ClientType:   ClaudeCode,
			Description:  "Claude Code CLI (Test)",
			SettingsFile: ".claude.json",
			RelPath:      []string{}, // Empty RelPath means check just the settings file
			Extension:    JSON,
		},
		{
			ClientType:   Cursor,
			Description:  "Cursor editor (Test)",
			SettingsFile: "mcp.json",
			RelPath:      []string{".cursor"}, // Check .cursor directory
			Extension:    JSON,
		},
		{
			ClientType:   VSCode,
			Description:  "Visual Studio Code (Test)",
			SettingsFile: "mcp.json",
			RelPath:      []string{".config", "Code", "User"}, // This path won't exist in test
			Extension:    JSON,
		},
	}

	// Use ClientManager with test dependencies - no groups manager to avoid system dependencies
	manager := NewTestClientManager(tempHome, nil, clientIntegrations, configProvider)
	statuses, err := manager.GetClientStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, statuses)

	// Create a map for easier testing
	statusMap := make(map[MCPClient]MCPClientStatus)
	for _, status := range statuses {
		statusMap[status.ClientType] = status
	}

	claudeStatus, exists := statusMap[ClaudeCode]
	assert.True(t, exists)
	assert.True(t, claudeStatus.Installed)
	assert.True(t, claudeStatus.Registered)

	cursorStatus, exists := statusMap[Cursor]
	assert.True(t, exists)
	assert.True(t, cursorStatus.Installed)
	assert.False(t, cursorStatus.Registered)

	vscodeStatus, exists := statusMap[VSCode]
	assert.True(t, exists)
	assert.False(t, vscodeStatus.Installed)
	assert.False(t, vscodeStatus.Registered)
}

func TestGetClientStatus_Sorting(t *testing.T) {
	t.Parallel()

	// Setup a temporary home directory for testing
	tempHome, err := os.MkdirTemp("", "toolhive-test-home")
	require.NoError(t, err)
	defer os.RemoveAll(tempHome)

	// Create mock config with no registered clients
	mockConfig := &config.Config{
		Clients: config.Clients{
			RegisteredClients: []string{},
		},
	}
	configProvider, cleanup := createTestConfigProvider(t, mockConfig)
	defer cleanup()

	// Use fake test data instead of real client integrations to avoid race conditions
	testClientIntegrations := createTestClientIntegrations()

	// Use ClientManager with test dependencies - no groups manager to avoid system dependencies
	manager := NewTestClientManager(tempHome, nil, testClientIntegrations, configProvider)
	statuses, err := manager.GetClientStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, statuses)
	require.Greater(t, len(statuses), 1, "Need at least 2 clients to test sorting")

	// Verify that the statuses are sorted alphabetically by ClientType
	for i := 1; i < len(statuses); i++ {
		prevClient := string(statuses[i-1].ClientType)
		currClient := string(statuses[i].ClientType)
		assert.True(t, prevClient < currClient,
			"Client statuses should be sorted alphabetically: %s should come before %s",
			prevClient, currClient)
	}
}

func TestGetClientStatus_WithGroups(t *testing.T) {
	t.Parallel()
	// Initialize logger to prevent panic
	logger.Initialize()

	// Set up a temporary home directory for testing (for dependency injection only)
	tempHome := t.TempDir()

	// Create mock client config files
	_, err := os.Create(filepath.Join(tempHome, ".cursor"))
	require.NoError(t, err)

	_, err = os.Create(filepath.Join(tempHome, ".claude.json"))
	require.NoError(t, err)

	// Create a mock groups manager instead of a real one to avoid modifying host configuration
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGroupManager := mocks.NewMockManager(ctrl)

	// Set up mock expectations
	ctx := context.Background()
	mockGroups := []*groups.Group{
		{
			Name:              "test-dev-group",
			RegisteredClients: []string{string(ClaudeCode), string(Cursor)},
		},
	}

	mockGroupManager.EXPECT().List(ctx).Return(mockGroups, nil).AnyTimes()

	// Now test GetClientStatus using ClientManager with dependency injection
	// Use explicit client integrations for this test to avoid race conditions with global variable
	clientIntegrations := []mcpClientConfig{
		{
			ClientType:   ClaudeCode,
			Description:  "Claude Code CLI (Test)",
			SettingsFile: ".claude.json",
			RelPath:      []string{}, // Empty RelPath means check just the settings file
			Extension:    JSON,
		},
		{
			ClientType:   Cursor,
			Description:  "Cursor editor (Test)",
			SettingsFile: "mcp.json",
			RelPath:      []string{".cursor"}, // Check .cursor directory
			Extension:    JSON,
		},
	}

	// Create a test config provider instead of using the default one
	testConfig := &config.Config{
		Clients: config.Clients{
			RegisteredClients: []string{}, // Empty to test group-based registration
		},
	}
	configProvider, cleanup := createTestConfigProvider(t, testConfig)
	defer cleanup()

	manager := NewTestClientManager(tempHome, mockGroupManager, clientIntegrations, configProvider)
	statuses, err := manager.GetClientStatus(ctx)
	require.NoError(t, err)
	require.NotNil(t, statuses)

	// Create a map for easier testing
	statusMap := make(map[MCPClient]MCPClientStatus)
	for _, status := range statuses {
		statusMap[status.ClientType] = status
	}

	// ClaudeCode should be registered (from groups) and installed
	claudeStatus, exists := statusMap[ClaudeCode]
	assert.True(t, exists)
	assert.True(t, claudeStatus.Installed)
	assert.True(t, claudeStatus.Registered, "ClaudeCode should be registered via groups")

	// Cursor should be registered (from groups) and installed
	cursorStatus, exists := statusMap[Cursor]
	assert.True(t, exists)
	assert.True(t, cursorStatus.Installed)
	assert.True(t, cursorStatus.Registered, "Cursor should be registered via groups")
}

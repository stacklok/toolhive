package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
)

func TestGetClientStatus(t *testing.T) {
	// Setup a temporary home directory for testing
	tempHome, err := os.MkdirTemp("", "toolhive-test-home")
	require.NoError(t, err)
	defer os.RemoveAll(tempHome)

	t.Setenv("HOME", tempHome)

	// Create mock config with registered clients
	mockConfig := &config.Config{
		Clients: config.Clients{
			RegisteredClients: []string{string(ClaudeCode)},
		},
	}
	cleanup := MockConfig(t, mockConfig)
	defer cleanup()

	// Create a mock Cursor config file
	_, err = os.Create(filepath.Join(tempHome, ".cursor"))
	require.NoError(t, err)

	// Create a mock ClaudeCode config file
	_, err = os.Create(filepath.Join(tempHome, ".claude.json"))
	require.NoError(t, err)

	statuses, err := GetClientStatus(context.Background())
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
	// Setup a temporary home directory for testing
	origHome := os.Getenv("HOME")
	tempHome, err := os.MkdirTemp("", "toolhive-test-home")
	require.NoError(t, err)
	defer os.RemoveAll(tempHome)

	t.Setenv("HOME", tempHome)
	defer t.Setenv("HOME", origHome)

	// Create mock config with no registered clients
	mockConfig := &config.Config{
		Clients: config.Clients{
			RegisteredClients: []string{},
		},
	}
	cleanup := MockConfig(t, mockConfig)
	defer cleanup()

	statuses, err := GetClientStatus(context.Background())
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
	// Initialize logger to prevent panic
	logger.Initialize()

	// Set up a temporary home directory for testing
	tempHome, err := os.MkdirTemp("", "toolhive-test-home")
	require.NoError(t, err)
	defer os.RemoveAll(tempHome)

	t.Setenv("HOME", tempHome)

	// Create mock client config files
	_, err = os.Create(filepath.Join(tempHome, ".cursor"))
	require.NoError(t, err)

	_, err = os.Create(filepath.Join(tempHome, ".claude.json"))
	require.NoError(t, err)

	// Create a real groups manager for testing
	groupManager, err := groups.NewManager()
	require.NoError(t, err)

	// Clean up any existing test groups
	ctx := context.Background()
	existingGroups, _ := groupManager.List(ctx)
	for _, group := range existingGroups {
		if group.Name == "test-dev-group" || group.Name == "test-prod-group" {
			groupManager.Delete(ctx, group.Name)
		}
	}

	// Create test groups with registered clients
	err = groupManager.Create(ctx, "test-dev-group")
	require.NoError(t, err)
	defer groupManager.Delete(ctx, "test-dev-group")

	// Register clients with groups
	err = groupManager.RegisterClients(ctx, []string{"test-dev-group"}, []string{string(ClaudeCode), string(Cursor)})
	require.NoError(t, err)

	// Now test GetClientStatus
	statuses, err := GetClientStatus(ctx)
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

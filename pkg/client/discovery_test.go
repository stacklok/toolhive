package client

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	log "github.com/stacklok/toolhive/pkg/logger"
)

func TestGetClientStatus(t *testing.T) {
	logger := log.NewLogger()

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
	cleanup := MockConfig(t, mockConfig, logger)
	defer cleanup()

	// Create a mock Cursor config file
	_, err = os.Create(filepath.Join(tempHome, ".cursor"))
	require.NoError(t, err)

	// Create a mock ClaudeCode config file
	_, err = os.Create(filepath.Join(tempHome, ".claude.json"))
	require.NoError(t, err)

	statuses, err := GetClientStatus(logger)
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
	logger := log.NewLogger()

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
	cleanup := MockConfig(t, mockConfig, logger)
	defer cleanup()

	statuses, err := GetClientStatus(logger)
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

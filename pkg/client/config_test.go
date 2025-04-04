// Package client provides utilities for managing client configurations
// and interacting with MCP servers.
package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/vibetool/pkg/transport/ssecommon"
)

// setupTestConfig creates a temporary directory and config file for testing
// It returns the temp directory path, config file path, and the loaded config
func setupTestConfig(t *testing.T, testName string) (string, string, ConfigFile) {
	t.Helper()

	// Create a temporary file
	tempDir, err := os.MkdirTemp("", "vibetool-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create a test config file with existing MCP servers
	configPath := filepath.Join(tempDir, fmt.Sprintf("config-%s.json", testName))
	testConfig := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"existing-server": map[string]interface{}{
				"url": "http://localhost:12345/sse",
			},
			"postgres": map[string]interface{}{
				"command": "node",
				"args": []interface{}{
					"F://node_modules//node_modules//@modelcontextprotocol//server-postgres//dist//index.js",
					"postgresql://postgres:postgres@localhost/novel",
				},
				"alwaysAllow": []interface{}{
					"query",
				},
			},
		},
	}

	// Write the test config to the file
	data, err := json.MarshalIndent(testConfig, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal JSON: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Read the config file
	config, err := readConfigFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config file: %v", err)
	}

	return tempDir, configPath, config
}

// getMCPServers reads the config file and returns the mcpServers map
func getMCPServers(t *testing.T, configPath string) map[string]interface{} {
	t.Helper()

	// Read the config file
	updatedConfig, err := readConfigFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read updated config file: %v", err)
	}

	// Check if the servers were updated correctly
	mcpServers, ok := updatedConfig.Contents["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("mcpServers is not a map")
	}

	return mcpServers
}

// testUpdateExistingServer tests updating an existing server
func testUpdateExistingServer(t *testing.T, config ConfigFile, configPath string) {
	t.Helper()
	// Test updating an existing server with lock
	expectedURL := "http://localhost:54321" + ssecommon.HTTPSSEEndpoint + "#test-container"
	err := config.SaveWithLock("existing-server", expectedURL, &StandardConfigEditor{})
	if err != nil {
		t.Fatalf("Failed to update MCP server config: %v", err)
	}

	// Get the updated servers
	mcpServers := getMCPServers(t, configPath)

	// Check existing server
	existingServer, ok := mcpServers["existing-server"].(map[string]interface{})
	if !ok {
		t.Fatalf("existing-server is not a map")
	}
	existingURL, ok := existingServer["url"].(string)
	if !ok {
		t.Fatalf("url is not a string")
	}
	if existingURL != expectedURL {
		t.Fatalf("Unexpected URL for existing-server: %s, expected: %s", existingURL, expectedURL)
	}
}

// testAddNewServer tests adding a new server
func testAddNewServer(t *testing.T, config ConfigFile, configPath string) {
	t.Helper()
	// Test adding a new server with lock
	expectedURL := "http://localhost:9876" + ssecommon.HTTPSSEEndpoint + "#new-container"
	err := config.SaveWithLock("new-server", expectedURL, &StandardConfigEditor{})
	if err != nil {
		t.Fatalf("Failed to add new MCP server config: %v", err)
	}

	// Get the updated servers
	mcpServers := getMCPServers(t, configPath)

	// Check new server
	newServer, ok := mcpServers["new-server"].(map[string]interface{})
	if !ok {
		t.Fatalf("new-server is not a map")
	}
	newURL, ok := newServer["url"].(string)
	if !ok {
		t.Fatalf("url is not a string")
	}
	if newURL != expectedURL {
		t.Fatalf("Unexpected URL for new-server: %s, expected: %s", newURL, expectedURL)
	}
}

// testPreserveExistingConfig tests that existing configurations are preserved
func testPreserveExistingConfig(t *testing.T, configPath string) {
	t.Helper()
	// Get the updated servers
	mcpServers := getMCPServers(t, configPath)

	// Check postgres server (should be unchanged)
	postgresServer, ok := mcpServers["postgres"].(map[string]interface{})
	if !ok {
		t.Fatalf("postgres is not a map")
	}
	command, ok := postgresServer["command"].(string)
	if !ok {
		t.Fatalf("command is not a string")
	}
	if command != "node" {
		t.Fatalf("Unexpected command for postgres: %s", command)
	}
	args, ok := postgresServer["args"].([]interface{})
	if !ok {
		t.Fatalf("args is not a slice")
	}
	if len(args) != 2 {
		t.Fatalf("Unexpected args length for postgres: %d", len(args))
	}
	alwaysAllow, ok := postgresServer["alwaysAllow"].([]interface{})
	if !ok {
		t.Fatalf("alwaysAllow is not a slice")
	}
	if len(alwaysAllow) != 1 || alwaysAllow[0].(string) != "query" {
		t.Fatalf("Unexpected alwaysAllow for postgres: %v", alwaysAllow)
	}
}

func TestUpdateMCPServerConfig(t *testing.T) {
	t.Parallel()

	// Run subtests
	t.Run("UpdateExistingServer", func(t *testing.T) {
		t.Parallel()

		// Setup test environment for this subtest
		tempDir, configPath, config := setupTestConfig(t, "update")
		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})

		testUpdateExistingServer(t, config, configPath)
	})

	t.Run("AddNewServer", func(t *testing.T) {
		t.Parallel()

		// Setup test environment for this subtest
		tempDir, configPath, config := setupTestConfig(t, "add")
		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})

		testAddNewServer(t, config, configPath)
	})

	t.Run("PreserveExistingConfig", func(t *testing.T) {
		t.Parallel()

		// Setup test environment for this subtest
		tempDir, configPath, _ := setupTestConfig(t, "preserve")
		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})

		testPreserveExistingConfig(t, configPath)
	})
}

func TestGenerateMCPServerURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		host          string
		port          int
		containerName string
		expected      string
	}{
		{
			name:          "Standard URL",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			expected:      "http://localhost:12345" + ssecommon.HTTPSSEEndpoint + "#test-container",
		},
		{
			name:          "Different host",
			host:          "192.168.1.100",
			port:          54321,
			containerName: "another-container",
			expected:      "http://192.168.1.100:54321" + ssecommon.HTTPSSEEndpoint + "#another-container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			url := GenerateMCPServerURL(tt.host, tt.port, tt.containerName)
			if url != tt.expected {
				t.Errorf("GenerateMCPServerURL() = %v, want %v", url, tt.expected)
			}
		})
	}
}

package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/vibetool/pkg/transport"
)

func TestUpdateMCPServerConfig(t *testing.T) {
	// Create a temporary file
	tempDir, err := os.MkdirTemp("", "vibetool-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test config file with existing MCP servers
	configPath := filepath.Join(tempDir, "config.json")
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
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Read the config file
	config, err := readConfigFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config file: %v", err)
	}

	// Test updating an existing server with lock
	err = config.SaveWithLock("existing-server", "http://localhost:54321"+transport.HTTPSSEEndpoint+"#test-container")
	if err != nil {
		t.Fatalf("Failed to update MCP server config: %v", err)
	}

	// Test adding a new server with lock
	err = config.SaveWithLock("new-server", "http://localhost:9876"+transport.HTTPSSEEndpoint+"#new-container")
	if err != nil {
		t.Fatalf("Failed to add new MCP server config: %v", err)
	}

	// Read the config file again
	updatedConfig, err := readConfigFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read updated config file: %v", err)
	}

	// Check if the servers were updated correctly
	mcpServers, ok := updatedConfig.Contents["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("mcpServers is not a map")
	}

	// Check existing server
	existingServer, ok := mcpServers["existing-server"].(map[string]interface{})
	if !ok {
		t.Fatalf("existing-server is not a map")
	}
	existingURL, ok := existingServer["url"].(string)
	if !ok {
		t.Fatalf("url is not a string")
	}
	expectedURL := "http://localhost:54321" + transport.HTTPSSEEndpoint + "#test-container"
	if existingURL != expectedURL {
		t.Fatalf("Unexpected URL for existing-server: %s, expected: %s", existingURL, expectedURL)
	}

	// Check new server
	newServer, ok := mcpServers["new-server"].(map[string]interface{})
	if !ok {
		t.Fatalf("new-server is not a map")
	}
	newURL, ok := newServer["url"].(string)
	if !ok {
		t.Fatalf("url is not a string")
	}
	expectedNewURL := "http://localhost:9876" + transport.HTTPSSEEndpoint + "#new-container"
	if newURL != expectedNewURL {
		t.Fatalf("Unexpected URL for new-server: %s, expected: %s", newURL, expectedNewURL)
	}

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

func TestGenerateMCPServerURL(t *testing.T) {
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
			expected:      "http://localhost:12345" + transport.HTTPSSEEndpoint + "#test-container",
		},
		{
			name:          "Different host",
			host:          "192.168.1.100",
			port:          54321,
			containerName: "another-container",
			expected:      "http://192.168.1.100:54321" + transport.HTTPSSEEndpoint + "#another-container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := GenerateMCPServerURL(tt.host, tt.port, tt.containerName)
			if url != tt.expected {
				t.Errorf("GenerateMCPServerURL() = %v, want %v", url, tt.expected)
			}
		})
	}
}
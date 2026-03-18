// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

func TestUpsertMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mcpServerPatchPath string // the path used by the patch operation
		mcpServerKeyPath   string // the path used to retrieve the value from the config file (for testing purposes)
		mcpServerName      string // the name of the MCP server to remove
	}{
		{mcpServerPatchPath: "/mcp/servers", mcpServerKeyPath: "mcp.servers", mcpServerName: "testMcpServerUpdate"},
		{mcpServerPatchPath: "/mcpServers", mcpServerKeyPath: "mcpServers", mcpServerName: "testMcpServerUpdate"},
	}

	for _, tt := range tests {

		t.Run("AddNewMCPServer", func(t *testing.T) {
			t.Parallel()

			uniqueId := uuid.New().String()
			tempDir, configPath := setupEmptyTestConfig(t, uniqueId)

			jsu := JSONConfigUpdater{
				Path:                 configPath,
				MCPServersPathPrefix: tt.mcpServerPatchPath,
			}

			mcpServer := MCPServer{
				Url: fmt.Sprintf("test-url-%s", uniqueId),
			}

			err := jsu.Upsert(tt.mcpServerName, mcpServer)
			if err != nil {
				t.Fatalf("Failed to update config: %v", err)
			}

			testMcpServer := getMCPServerFromFile(t, configPath, tt.mcpServerKeyPath+"."+tt.mcpServerName)

			assert.Equal(t, mcpServer.Url, testMcpServer.Url, "The retrieved value should match the set value")

			t.Cleanup(func() {
				if err := os.RemoveAll(tempDir); err != nil {
					t.Logf("Failed to remove temp dir: %v", err)
				}
			})
		})
	}

	// Run subtests

	for _, tt := range tests {

		t.Run("UpdateExistingMCPServer", func(t *testing.T) {
			t.Parallel()

			uniqueId := uuid.New().String()
			tempDir, configPath := setupEmptyTestConfig(t, uniqueId)

			jsu := JSONConfigUpdater{
				Path:                 configPath,
				MCPServersPathPrefix: tt.mcpServerPatchPath,
			}

			// add an MCP server so we can update it
			mcpServer := MCPServer{
				Url: fmt.Sprintf("test-url-%s-before-update", uniqueId),
			}
			err := jsu.Upsert(tt.mcpServerName, mcpServer)
			if err != nil {
				t.Fatalf("Failed to add mcp server to config: %v", err)
			}
			testMcpServer := getMCPServerFromFile(t, configPath, tt.mcpServerKeyPath+"."+tt.mcpServerName)
			assert.Equal(t, mcpServer.Url, testMcpServer.Url, "The retrieved value should match the set value")

			// now we update the mcp server
			mcpServerUpdated := MCPServer{
				Url: fmt.Sprintf("test-url-%s-after-update", uniqueId),
			}
			err = jsu.Upsert(tt.mcpServerName, mcpServerUpdated)
			if err != nil {
				t.Fatalf("Failed to update mcp server inconfig: %v", err)
			}
			// we make sure to get the same mcp server that we created and then updated
			testMcpServerUpdate := getMCPServerFromFile(t, configPath, tt.mcpServerKeyPath+"."+tt.mcpServerName)
			assert.Equal(t, mcpServerUpdated.Url, testMcpServerUpdate.Url, "The retrieved value should match the set value")

			if err != nil {
				t.Fatalf("Failed to update config: %v", err)
			}

			t.Cleanup(func() {
				if err := os.RemoveAll(tempDir); err != nil {
					t.Logf("Failed to remove temp dir: %v", err)
				}
			})
		})
	}
}

func TestRemoveMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mcpServerPatchPath string // the path used by the patch operation
		mcpServerKeyPath   string // the path used to retrieve the value from the config file (for testing purposes)
		mcpServerName      string // the name of the MCP server to remove
	}{
		{mcpServerPatchPath: "/mcp/servers", mcpServerKeyPath: "mcp.servers", mcpServerName: "testMcpServerRemove"},
		{mcpServerPatchPath: "/mcpServers", mcpServerKeyPath: "mcpServers", mcpServerName: "testMcpServerRemove"},
	}

	for _, tt := range tests {
		t.Run("DeleteMCPServer", func(t *testing.T) {
			t.Parallel()

			uniqueId := uuid.New().String()
			tempDir, configPath := setupEmptyTestConfig(t, uniqueId)

			jsu := JSONConfigUpdater{
				Path:                 configPath,
				MCPServersPathPrefix: tt.mcpServerPatchPath,
			}

			// add an MCP server so we can remove it
			mcpServer := MCPServer{
				Url: fmt.Sprintf("test-url-%s-before-removal", uniqueId),
			}
			err := jsu.Upsert(tt.mcpServerName, mcpServer)
			if err != nil {
				t.Fatalf("Failed to add mcp server to config: %v", err)
			}
			testMcpServer := getMCPServerFromFile(t, configPath, tt.mcpServerKeyPath+"."+tt.mcpServerName)
			assert.Equal(t, mcpServer.Url, testMcpServer.Url, "The retrieved value should match the set value")

			// remove both mcp servers
			err = jsu.Remove(tt.mcpServerName)
			if err != nil {
				t.Fatalf("Failed to remove mcp server testMcpServer from config: %v", err)
			}

			// read the config file and check that the mcp servers are removed
			content, err := os.ReadFile(configPath)
			if err != nil {
				log.Fatalf("Failed to read file: %v", err)
			}

			testMcpServerJson := gjson.GetBytes(content, tt.mcpServerKeyPath+"."+tt.mcpServerName).Raw
			if testMcpServerJson != "" {
				t.Fatalf("Failed to remove mcp server testMcpServer from config: %v", testMcpServerJson)
			}

			t.Cleanup(func() {
				if err := os.RemoveAll(tempDir); err != nil {
					t.Logf("Failed to remove temp dir: %v", err)
				}
			})
		})
	}
}

// setupEmptyTestConfig creates a temporary directory and an empty config file for testing
// It returns the temp directory path, config file path, and the loaded config
// The logs are created in "/var/folders/2k/jvn73p4d2nn_j6tvc40vj4r00000gn/T/toolhive-test4175700918/config-9f74ab6d-0b4e-4956-b818-315bf16aa803.json"
func setupEmptyTestConfig(t *testing.T, testName string) (string, string) {
	t.Helper()

	// Create a temporary file
	tempDir, err := os.MkdirTemp("", "toolhive-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create a test config file with existing MCP servers
	configPath := filepath.Join(tempDir, fmt.Sprintf("config-%s.json", testName))
	testConfig := map[string]interface{}{}

	// // Write the test config to the file
	data, err := json.MarshalIndent(testConfig, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal JSON: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	return tempDir, configPath
}

// getMCPServerFromFile reads the config file and returns a mcpServer object
func getMCPServerFromFile(t *testing.T, configPath string, key string) MCPServer {
	t.Helper()

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	testMcpServerJson := gjson.GetBytes(content, key).Raw

	var testMcpServer MCPServer
	err = json.Unmarshal([]byte(testMcpServerJson), &testMcpServer)
	if err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	return testMcpServer
}

func TestEnsurePathExists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		description    string
		content        []byte
		path           string
		expectedResult []byte
	}{
		{
			name:           "EmptyContent",
			description:    "Should create path in empty JSON object",
			content:        []byte("{}"),
			path:           "/mcp/servers",
			expectedResult: []byte("{\"mcp\": {\"servers\": {}}}\n"),
		},
		{
			name:           "ExistingPath",
			description:    "Should return existing path",
			content:        []byte(`{"mcp": {"servers": {"existing": "value"}}}`),
			path:           "/mcp/servers",
			expectedResult: []byte("{\"mcp\": {\"servers\": {\"existing\": \"value\"}}}\n"),
		},
		{
			name:           "PartialExistingPath",
			description:    "Should create missing nested path when parent exists",
			content:        []byte(`{"misc": {}}`),
			path:           "/misc/mcp/servers",
			expectedResult: []byte("{\"misc\": {\"mcp\": {\"servers\": {}}}}\n"),
		},
		{
			name:           "PathWithDots",
			description:    "Should handle paths with dots correctly",
			content:        []byte(`{"agent.support": {"mcp.servers": {"existing": "value"}}}`),
			path:           "/agent.support/mcp.servers",
			expectedResult: []byte("{\"agent.support\": {\"mcp.servers\": {\"existing\": \"value\"}}}\n"),
		},
		{
			name:           "RootPath",
			description:    "Should handle root path",
			content:        []byte(`{"server1": {"some": "config"}, "server2": {"some": "other_config"}}`),
			path:           "/",
			expectedResult: []byte("{\"server1\": {\"some\": \"config\"}, \"server2\": {\"some\": \"other_config\"}}\n"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := ensurePathExists(tt.content, tt.path)

			if !reflect.DeepEqual(result, tt.expectedResult) {
				t.Errorf("JSON config content = %v, want %v", result, tt.expectedResult)
			}
		})
	}
}

func TestYAMLConfigUpdaterUpsert(t *testing.T) {
	t.Parallel()

	t.Run("AddNewMCPServerToEmptyYAML", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupEmptyTestYAMLConfig(t, uniqueId)

		ycu := YAMLConfigUpdater{
			Path:      configPath,
			Converter: NewGenericYAMLConverter(createGooseConfig()),
		}

		mcpServer := MCPServer{
			Url:  fmt.Sprintf("test-url-%s", uniqueId),
			Type: "mcp",
		}

		serverName := "testServer"
		err := ycu.Upsert(serverName, mcpServer)
		if err != nil {
			t.Fatalf("Failed to update YAML config: %v", err)
		}

		// Verify the YAML content
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read YAML file: %v", err)
		}

		var config map[string]interface{}
		err = yaml.Unmarshal(content, &config)
		if err != nil {
			t.Fatalf("Failed to unmarshal YAML: %v", err)
		}

		extensions, ok := config["extensions"].(map[string]interface{})
		assert.True(t, ok, "Extensions should be a map")
		extension, exists := extensions[serverName].(map[string]interface{})
		assert.True(t, exists, "Extension should exist")
		assert.Equal(t, mcpServer.Url, extension["uri"], "URI should match")
		assert.Equal(t, mcpServer.Type, extension["type"], "Type should match")
		assert.Equal(t, serverName, extension["name"], "Name should match")
		assert.Equal(t, true, extension["enabled"], "Should be enabled")
		assert.Equal(t, 60, extension["timeout"], "Timeout should match")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("PreserveExistingFieldsWhenUpserting", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, fmt.Sprintf("test-config-%s.yaml", uniqueId))

		// Create a YAML file with existing fields that should be preserved
		initialConfig := `GOOSE_PROVIDER: anthropic
ANTHROPIC_HOST: https://api.anthropic.com
extensions:
  existingServer:
    name: existingServer
    enabled: true
    type: mcp
    uri: existing-url
    timeout: 60
    description: ""
`

		if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		ycu := YAMLConfigUpdater{
			Path:      configPath,
			Converter: NewGenericYAMLConverter(createGooseConfig()),
		}

		// Add a new MCP server
		newServer := MCPServer{
			Url:  fmt.Sprintf("new-url-%s", uniqueId),
			Type: "mcp",
		}
		err := ycu.Upsert("newServer", newServer)
		if err != nil {
			t.Fatalf("Failed to upsert new server: %v", err)
		}

		// Read the updated config as a generic map to check all fields
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read updated YAML file: %v", err)
		}

		var config map[string]interface{}
		err = yaml.Unmarshal(content, &config)
		if err != nil {
			t.Fatalf("Failed to unmarshal YAML: %v", err)
		}

		// Verify original fields are preserved
		assert.Equal(t, "anthropic", config["GOOSE_PROVIDER"], "GOOSE_PROVIDER should be preserved")
		assert.Equal(t, "https://api.anthropic.com", config["ANTHROPIC_HOST"], "ANTHROPIC_HOST should be preserved")

		// Verify extensions section contains both old and new servers
		extensions, ok := config["extensions"].(map[string]interface{})
		assert.True(t, ok, "Extensions should be a map")

		// Check existing server is still there
		existingServer, exists := extensions["existingServer"].(map[string]interface{})
		assert.True(t, exists, "Existing server should still exist")
		assert.Equal(t, "existing-url", existingServer["uri"], "Existing server URI should be preserved")

		// Check new server was added
		newServerData, exists := extensions["newServer"].(map[string]interface{})
		assert.True(t, exists, "New server should exist")
		assert.Equal(t, newServer.Url, newServerData["uri"], "New server URI should match")
		assert.Equal(t, newServer.Type, newServerData["type"], "New server type should match")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})
}

func TestYAMLConfigUpdaterRemove(t *testing.T) {
	t.Parallel()

	t.Run("RemoveExistingMCPServerFromYAML", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupExistingTestYAMLConfig(t, uniqueId)

		ycu := YAMLConfigUpdater{
			Path:      configPath,
			Converter: NewGenericYAMLConverter(createGooseConfig()),
		}

		serverName := "existingServer"
		err := ycu.Remove(serverName)
		if err != nil {
			t.Fatalf("Failed to remove server from YAML config: %v", err)
		}

		// Verify removal
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read YAML file: %v", err)
		}

		var config map[string]interface{}
		err = yaml.Unmarshal(content, &config)
		if err != nil {
			t.Fatalf("Failed to unmarshal YAML: %v", err)
		}

		if extensions, ok := config["extensions"].(map[string]interface{}); ok {
			_, exists := extensions[serverName]
			assert.False(t, exists, "Extension should not exist after removal")
		}

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("RemoveNonExistentMCPServerFromYAML", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupExistingTestYAMLConfig(t, uniqueId)

		ycu := YAMLConfigUpdater{
			Path:      configPath,
			Converter: NewGenericYAMLConverter(createGooseConfig()),
		}

		// Try to remove non-existent server
		err := ycu.Remove("nonExistentServer")
		if err != nil {
			t.Fatalf("Should not error when removing non-existent server: %v", err)
		}

		// Verify existing server is still there
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read YAML file: %v", err)
		}

		var config map[string]interface{}
		err = yaml.Unmarshal(content, &config)
		if err != nil {
			t.Fatalf("Failed to unmarshal YAML: %v", err)
		}

		if extensions, ok := config["extensions"].(map[string]interface{}); ok {
			_, exists := extensions["existingServer"]
			assert.True(t, exists, "Existing extension should still exist")
		} else {
			t.Fatal("Extensions not found in config")
		}

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("RemoveFromEmptyYAMLFile", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupEmptyTestYAMLConfig(t, uniqueId)

		ycu := YAMLConfigUpdater{
			Path:      configPath,
			Converter: NewGenericYAMLConverter(createGooseConfig()),
		}

		// Try to remove from empty file
		err := ycu.Remove("anyServer")
		if err != nil {
			t.Fatalf("Should not error when removing from empty file: %v", err)
		}

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})
}

// setupEmptyTestYAMLConfig creates a temporary directory and an empty YAML config file for testing
func setupEmptyTestYAMLConfig(t *testing.T, testName string) (string, string) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "toolhive-yaml-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	configPath := filepath.Join(tempDir, fmt.Sprintf("config-%s.yaml", testName))

	// Create an empty YAML file
	if err := os.WriteFile(configPath, []byte(""), 0600); err != nil {
		t.Fatalf("Failed to write empty YAML file: %v", err)
	}

	return tempDir, configPath
}

// setupExistingTestYAMLConfig creates a temporary directory and a YAML config file with existing data
func setupExistingTestYAMLConfig(t *testing.T, testName string) (string, string) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "toolhive-yaml-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	configPath := filepath.Join(tempDir, fmt.Sprintf("config-%s.yaml", testName))

	// Create a YAML config with existing extension
	testConfig := map[string]interface{}{
		"extensions": map[string]interface{}{
			"existingServer": map[string]interface{}{
				"name":        "existingServer",
				"enabled":     true,
				"type":        "existing-type",
				"timeout":     60,
				"description": "",
				"uri":         fmt.Sprintf("existing-url-%s", testName),
			},
		},
	}

	yamlData, err := yaml.Marshal(&testConfig)
	if err != nil {
		t.Fatalf("Failed to marshal test YAML: %v", err)
	}

	if err := os.WriteFile(configPath, yamlData, 0600); err != nil {
		t.Fatalf("Failed to write test YAML file: %v", err)
	}

	return tempDir, configPath
}

const testServerName = "testServer"

func TestTOMLConfigUpdaterUpsert(t *testing.T) {
	t.Parallel()

	t.Run("AddNewMCPServerToEmptyTOML", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupEmptyTestTOMLConfig(t, uniqueId)

		tcu := TOMLConfigUpdater{
			Path:            configPath,
			ServersKey:      "mcp_servers",
			IdentifierField: "name",
			URLField:        "url",
		}

		mcpServer := MCPServer{
			Url:  fmt.Sprintf("http://localhost:%s", uniqueId),
			Type: "http",
		}

		err := tcu.Upsert(testServerName, mcpServer)
		if err != nil {
			t.Fatalf("Failed to update TOML config: %v", err)
		}

		// Verify the TOML content
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		// Verify content contains expected values
		// Note: go-toml/v2 uses single quotes for string values
		contentStr := string(content)
		assert.Contains(t, contentStr, "[[mcp_servers]]", "Should contain array-of-tables syntax")
		assert.Contains(t, contentStr, fmt.Sprintf("name = '%s'", testServerName), "Should contain server name")
		assert.Contains(t, contentStr, fmt.Sprintf("url = '%s'", mcpServer.Url), "Should contain URL")
		assert.Contains(t, contentStr, fmt.Sprintf("transport = '%s'", mcpServer.Type), "Should contain transport type")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("UpdateExistingServerInTOML", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupExistingTestTOMLConfig(t, uniqueId)

		tcu := TOMLConfigUpdater{
			Path:            configPath,
			ServersKey:      "mcp_servers",
			IdentifierField: "name",
			URLField:        "url",
		}

		// Update the existing server
		updatedServer := MCPServer{
			Url:  "http://localhost:9999/updated",
			Type: "http",
		}

		err := tcu.Upsert("existingServer", updatedServer)
		if err != nil {
			t.Fatalf("Failed to update TOML config: %v", err)
		}

		// Verify the TOML content was updated
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		// Note: go-toml/v2 uses single quotes for string values
		contentStr := string(content)
		assert.Contains(t, contentStr, "url = 'http://localhost:9999/updated'", "Should contain updated URL")
		// Ensure there's only one mcp_servers entry (updated, not appended)
		assert.Equal(t, 1, countOccurrences(contentStr, "[[mcp_servers]]"), "Should have only one server entry")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("AddSecondServerToExistingTOML", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupExistingTestTOMLConfig(t, uniqueId)

		tcu := TOMLConfigUpdater{
			Path:            configPath,
			ServersKey:      "mcp_servers",
			IdentifierField: "name",
			URLField:        "url",
		}

		// Add a new server
		newServer := MCPServer{
			Url:  "http://localhost:8888/new",
			Type: "http",
		}

		err := tcu.Upsert("newServer", newServer)
		if err != nil {
			t.Fatalf("Failed to add new server to TOML config: %v", err)
		}

		// Verify both servers exist
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		// Note: go-toml/v2 uses single quotes for string values
		contentStr := string(content)
		assert.Equal(t, 2, countOccurrences(contentStr, "[[mcp_servers]]"), "Should have two server entries")
		assert.Contains(t, contentStr, "name = 'existingServer'", "Should contain existing server")
		assert.Contains(t, contentStr, "name = 'newServer'", "Should contain new server")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("PreserveOtherFieldsWhenUpserting", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, fmt.Sprintf("config-%s.toml", uniqueId))

		// Create a TOML file with extra top-level fields
		initialConfig := `# Some comment
version = "1.0"

[settings]
debug = true

[[mcp_servers]]
name = "existingServer"
url = "http://localhost:8080"
transport = "http"
`
		if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
			t.Fatalf("Failed to write test TOML file: %v", err)
		}

		tcu := TOMLConfigUpdater{
			Path:            configPath,
			ServersKey:      "mcp_servers",
			IdentifierField: "name",
			URLField:        "url",
		}

		// Add a new server
		newServer := MCPServer{
			Url:  "http://localhost:9090/new",
			Type: "http",
		}

		err := tcu.Upsert("newServer", newServer)
		if err != nil {
			t.Fatalf("Failed to add new server: %v", err)
		}

		// Verify other fields are preserved
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		// Note: go-toml/v2 uses single quotes for string values
		contentStr := string(content)
		assert.Contains(t, contentStr, "version =", "Should preserve version field")
		assert.Contains(t, contentStr, "[settings]", "Should preserve settings section")
		assert.Contains(t, contentStr, "debug =", "Should preserve debug setting")
		assert.Contains(t, contentStr, "name = 'newServer'", "Should contain new server")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})
}

func TestTOMLConfigUpdaterRemove(t *testing.T) {
	t.Parallel()

	t.Run("RemoveExistingServerFromTOML", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupExistingTestTOMLConfig(t, uniqueId)

		tcu := TOMLConfigUpdater{
			Path:            configPath,
			ServersKey:      "mcp_servers",
			IdentifierField: "name",
			URLField:        "url",
		}

		err := tcu.Remove("existingServer")
		if err != nil {
			t.Fatalf("Failed to remove server from TOML config: %v", err)
		}

		// Verify removal
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		contentStr := string(content)
		assert.NotContains(t, contentStr, "existingServer", "Should not contain removed server")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("RemoveNonExistentServerFromTOML", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupExistingTestTOMLConfig(t, uniqueId)

		tcu := TOMLConfigUpdater{
			Path:            configPath,
			ServersKey:      "mcp_servers",
			IdentifierField: "name",
			URLField:        "url",
		}

		// Try to remove non-existent server
		err := tcu.Remove("nonExistentServer")
		if err != nil {
			t.Fatalf("Should not error when removing non-existent server: %v", err)
		}

		// Verify existing server is still there
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		contentStr := string(content)
		assert.Contains(t, contentStr, "existingServer", "Existing server should still exist")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("RemoveFromEmptyTOMLFile", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupEmptyTestTOMLConfig(t, uniqueId)

		tcu := TOMLConfigUpdater{
			Path:            configPath,
			ServersKey:      "mcp_servers",
			IdentifierField: "name",
			URLField:        "url",
		}

		// Try to remove from empty file
		err := tcu.Remove("anyServer")
		if err != nil {
			t.Fatalf("Should not error when removing from empty file: %v", err)
		}

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("RemoveFromNonExistentFile", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "nonexistent.toml")

		tcu := TOMLConfigUpdater{
			Path:            configPath,
			ServersKey:      "mcp_servers",
			IdentifierField: "name",
			URLField:        "url",
		}

		// Try to remove from non-existent file
		err := tcu.Remove("anyServer")
		if err != nil {
			t.Fatalf("Should not error when file doesn't exist: %v", err)
		}
	})
}

// setupEmptyTestTOMLConfig creates a temporary directory and an empty TOML config file for testing
func setupEmptyTestTOMLConfig(t *testing.T, testName string) (string, string) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "toolhive-toml-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	configPath := filepath.Join(tempDir, fmt.Sprintf("config-%s.toml", testName))

	// Create an empty TOML file
	if err := os.WriteFile(configPath, []byte(""), 0600); err != nil {
		t.Fatalf("Failed to write empty TOML file: %v", err)
	}

	return tempDir, configPath
}

// setupExistingTestTOMLConfig creates a temporary directory and a TOML config file with existing data
func setupExistingTestTOMLConfig(t *testing.T, testName string) (string, string) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "toolhive-toml-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	configPath := filepath.Join(tempDir, fmt.Sprintf("config-%s.toml", testName))

	// Create a TOML config with existing server using array-of-tables syntax
	testConfig := fmt.Sprintf(`[[mcp_servers]]
name = "existingServer"
url = "http://localhost:8080/existing-%s"
transport = "http"
`, testName)

	if err := os.WriteFile(configPath, []byte(testConfig), 0600); err != nil {
		t.Fatalf("Failed to write test TOML file: %v", err)
	}

	return tempDir, configPath
}

// countOccurrences counts how many times substr appears in s
func countOccurrences(s, substr string) int {
	count := 0
	idx := 0
	for {
		i := indexOf(s[idx:], substr)
		if i == -1 {
			break
		}
		count++
		idx += i + len(substr)
	}
	return count
}

// indexOf returns the index of substr in s, or -1 if not found
func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestTOMLMapConfigUpdaterUpsert(t *testing.T) {
	t.Parallel()

	t.Run("AddNewMCPServerToEmptyTOML", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupEmptyTestTOMLConfig(t, uniqueId)

		tmu := TOMLMapConfigUpdater{
			Path:       configPath,
			ServersKey: "mcp_servers",
			URLField:   "url",
		}

		mcpServer := MCPServer{
			Url: fmt.Sprintf("http://localhost:%s", uniqueId),
		}

		err := tmu.Upsert(testServerName, mcpServer)
		if err != nil {
			t.Fatalf("Failed to update TOML config: %v", err)
		}

		// Verify the TOML content
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		// Verify content contains expected nested table format
		contentStr := string(content)
		assert.Contains(t, contentStr, "[mcp_servers."+testServerName+"]", "Should contain nested table syntax")
		assert.Contains(t, contentStr, fmt.Sprintf("url = '%s'", mcpServer.Url), "Should contain URL")
		// Should NOT contain array-of-tables format
		assert.NotContains(t, contentStr, "[[mcp_servers]]", "Should NOT contain array-of-tables syntax")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("UpdateExistingServerInTOMLMap", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupExistingTestTOMLMapConfig(t, uniqueId)

		tmu := TOMLMapConfigUpdater{
			Path:       configPath,
			ServersKey: "mcp_servers",
			URLField:   "url",
		}

		// Update the existing server
		updatedServer := MCPServer{
			Url: "http://localhost:9999/updated",
		}

		err := tmu.Upsert("existingServer", updatedServer)
		if err != nil {
			t.Fatalf("Failed to update TOML config: %v", err)
		}

		// Verify the TOML content was updated
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		contentStr := string(content)
		assert.Contains(t, contentStr, "url = 'http://localhost:9999/updated'", "Should contain updated URL")
		// Ensure there's still only one server section
		assert.Equal(t, 1, countOccurrences(contentStr, "[mcp_servers."), "Should have only one server entry")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("AddSecondServerToExistingTOMLMap", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupExistingTestTOMLMapConfig(t, uniqueId)

		tmu := TOMLMapConfigUpdater{
			Path:       configPath,
			ServersKey: "mcp_servers",
			URLField:   "url",
		}

		// Add a new server
		newServer := MCPServer{
			Url: "http://localhost:8888/new",
		}

		err := tmu.Upsert("newServer", newServer)
		if err != nil {
			t.Fatalf("Failed to add new server to TOML config: %v", err)
		}

		// Verify both servers exist
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		contentStr := string(content)
		assert.Contains(t, contentStr, "[mcp_servers.existingServer]", "Should contain existing server")
		assert.Contains(t, contentStr, "[mcp_servers.newServer]", "Should contain new server")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("PreserveOtherFieldsWhenUpserting", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, fmt.Sprintf("config-%s.toml", uniqueId))

		// Create a TOML file with extra top-level fields
		initialConfig := `# Codex config
model = "gpt-4"

[settings]
debug = true

[mcp_servers.existingServer]
url = "http://localhost:8080"
`
		if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
			t.Fatalf("Failed to write test TOML file: %v", err)
		}

		tmu := TOMLMapConfigUpdater{
			Path:       configPath,
			ServersKey: "mcp_servers",
			URLField:   "url",
		}

		// Add a new server
		newServer := MCPServer{
			Url: "http://localhost:9090/new",
		}

		err := tmu.Upsert("newServer", newServer)
		if err != nil {
			t.Fatalf("Failed to add new server: %v", err)
		}

		// Verify other fields are preserved
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		contentStr := string(content)
		assert.Contains(t, contentStr, "model =", "Should preserve model field")
		assert.Contains(t, contentStr, "[settings]", "Should preserve settings section")
		assert.Contains(t, contentStr, "debug =", "Should preserve debug setting")
		assert.Contains(t, contentStr, "[mcp_servers.newServer]", "Should contain new server")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("AddServerWithTransportType", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupEmptyTestTOMLConfig(t, uniqueId)

		tmu := TOMLMapConfigUpdater{
			Path:       configPath,
			ServersKey: "mcp_servers",
			URLField:   "url",
		}

		mcpServer := MCPServer{
			Url:  "http://localhost:8080",
			Type: "http",
		}

		err := tmu.Upsert(testServerName, mcpServer)
		if err != nil {
			t.Fatalf("Failed to update TOML config: %v", err)
		}

		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		contentStr := string(content)
		assert.Contains(t, contentStr, "transport = 'http'", "Should contain transport type")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})
}

func TestTOMLMapConfigUpdaterRemove(t *testing.T) {
	t.Parallel()

	t.Run("RemoveExistingServerFromTOMLMap", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupExistingTestTOMLMapConfig(t, uniqueId)

		tmu := TOMLMapConfigUpdater{
			Path:       configPath,
			ServersKey: "mcp_servers",
			URLField:   "url",
		}

		err := tmu.Remove("existingServer")
		if err != nil {
			t.Fatalf("Failed to remove server from TOML config: %v", err)
		}

		// Verify removal
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		contentStr := string(content)
		assert.NotContains(t, contentStr, "existingServer", "Should not contain removed server")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("RemoveNonExistentServerFromTOMLMap", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupExistingTestTOMLMapConfig(t, uniqueId)

		tmu := TOMLMapConfigUpdater{
			Path:       configPath,
			ServersKey: "mcp_servers",
			URLField:   "url",
		}

		// Try to remove non-existent server
		err := tmu.Remove("nonExistentServer")
		if err != nil {
			t.Fatalf("Should not error when removing non-existent server: %v", err)
		}

		// Verify existing server is still there
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read TOML file: %v", err)
		}

		contentStr := string(content)
		assert.Contains(t, contentStr, "existingServer", "Existing server should still exist")

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("RemoveFromEmptyTOMLMapFile", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupEmptyTestTOMLConfig(t, uniqueId)

		tmu := TOMLMapConfigUpdater{
			Path:       configPath,
			ServersKey: "mcp_servers",
			URLField:   "url",
		}

		// Try to remove from empty file
		err := tmu.Remove("anyServer")
		if err != nil {
			t.Fatalf("Should not error when removing from empty file: %v", err)
		}

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("RemoveFromNonExistentFile", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "nonexistent.toml")

		tmu := TOMLMapConfigUpdater{
			Path:       configPath,
			ServersKey: "mcp_servers",
			URLField:   "url",
		}

		// Try to remove from non-existent file
		err := tmu.Remove("anyServer")
		if err != nil {
			t.Fatalf("Should not error when file doesn't exist: %v", err)
		}
	})
}

// setupExistingTestTOMLMapConfig creates a temporary directory and a TOML config file with existing data
// using the map-based format [section.servername]
func setupExistingTestTOMLMapConfig(t *testing.T, testName string) (string, string) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "toolhive-toml-map-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	configPath := filepath.Join(tempDir, fmt.Sprintf("config-%s.toml", testName))

	// Create a TOML config with existing server using nested table syntax
	testConfig := fmt.Sprintf(`[mcp_servers.existingServer]
url = "http://localhost:8080/existing-%s"
`, testName)

	if err := os.WriteFile(configPath, []byte(testConfig), 0600); err != nil {
		t.Fatalf("Failed to write test TOML file: %v", err)
	}

	return tempDir, configPath
}

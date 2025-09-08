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

	"github.com/stacklok/toolhive/pkg/logger"
)

func TestUpsertMCPServerConfig(t *testing.T) {
	t.Parallel()

	logger.Initialize()

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

func TestRemoveMCPServerConfigNew(t *testing.T) {
	t.Parallel()

	logger.Initialize()

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

	logger.Initialize()

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

	logger.Initialize()

	t.Run("AddNewMCPServerToEmptyYAML", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupEmptyTestYAMLConfig(t, uniqueId)

		ycu := YAMLConfigUpdater{
			Path:      configPath,
			Converter: &GooseYAMLConverter{},
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

		var config GooseConfig
		err = yaml.Unmarshal(content, &config)
		if err != nil {
			t.Fatalf("Failed to unmarshal YAML: %v", err)
		}

		extension, exists := config.Extensions[serverName]
		assert.True(t, exists, "Extension should exist")
		assert.Equal(t, mcpServer.Url, extension.Uri, "URI should match")
		assert.Equal(t, mcpServer.Type, extension.Type, "Type should match")
		assert.Equal(t, serverName, extension.Name, "Name should match")
		assert.Equal(t, true, extension.Enabled, "Should be enabled")
		assert.Equal(t, GooseTimeout, extension.Timeout, "Timeout should match")

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
`

		if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		ycu := YAMLConfigUpdater{
			Path:      configPath,
			Converter: &GooseYAMLConverter{},
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

	logger.Initialize()

	t.Run("RemoveExistingMCPServerFromYAML", func(t *testing.T) {
		t.Parallel()

		uniqueId := uuid.New().String()
		tempDir, configPath := setupExistingTestYAMLConfig(t, uniqueId)

		ycu := YAMLConfigUpdater{
			Path:      configPath,
			Converter: &GooseYAMLConverter{},
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

		var config GooseConfig
		err = yaml.Unmarshal(content, &config)
		if err != nil {
			t.Fatalf("Failed to unmarshal YAML: %v", err)
		}

		_, exists := config.Extensions[serverName]
		assert.False(t, exists, "Extension should not exist after removal")

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
			Converter: &GooseYAMLConverter{},
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

		var config GooseConfig
		err = yaml.Unmarshal(content, &config)
		if err != nil {
			t.Fatalf("Failed to unmarshal YAML: %v", err)
		}

		_, exists := config.Extensions["existingServer"]
		assert.True(t, exists, "Existing extension should still exist")

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
			Converter: &GooseYAMLConverter{},
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
	testConfig := GooseConfig{
		Extensions: map[string]GooseExtension{
			"existingServer": {
				Name:    "existingServer",
				Enabled: true,
				Type:    "existing-type",
				Timeout: GooseTimeout,
				Uri:     fmt.Sprintf("existing-url-%s", testName),
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

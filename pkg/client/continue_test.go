package client

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestContinueYAMLConfig(t *testing.T) {
	t.Parallel()

	// Test updating an existing server
	t.Run("UpdateExistingServer", func(t *testing.T) {
		t.Parallel()

		// Create a temporary directory for the test
		tempDir, err := os.MkdirTemp("", "continue-test-update")
		require.NoError(t, err)
		defer os.RemoveAll(tempDir)

		// Create a .continue directory in the temp dir
		continueDir := filepath.Join(tempDir, ".continue")
		err = os.Mkdir(continueDir, 0750)
		require.NoError(t, err)

		// Create a sample Continue YAML config
		configPath := filepath.Join(continueDir, "config.yaml")
		initialConfig := map[string]interface{}{
			"name":    "my-configuration",
			"version": "0.0.1",
			"schema":  "v1",
			"models": []interface{}{
				map[string]interface{}{
					"name":     "GPT-4",
					"provider": "openai",
					"model":    "gpt-4",
					"roles":    []string{"chat"},
				},
			},
			"mcpServers": []interface{}{
				map[string]interface{}{
					"name":    "existing-server",
					"command": "node",
					"args":    []string{"server.js"},
					"env": map[string]interface{}{
						"API_KEY": "test-key",
					},
				},
			},
		}

		// Marshal the initial config to YAML
		yamlData, err := yaml.Marshal(initialConfig)
		require.NoError(t, err)

		// Write the YAML to the config file
		err = os.WriteFile(configPath, yamlData, 0600)
		require.NoError(t, err)

		// Read the config file
		config, err := readConfigFile(configPath)
		require.NoError(t, err)

		err = config.UpdateMCPServerConfig("existing-server", "http://localhost:8080/sse#test")
		require.NoError(t, err)

		// Save the config
		err = config.Save()
		require.NoError(t, err)

		// Read the config again
		updatedConfig, err := readConfigFile(configPath)
		require.NoError(t, err)

		// Check if the server was updated correctly
		mcpServers, ok := updatedConfig.Contents["mcpServers"].([]interface{})
		require.True(t, ok, "mcpServers should be an array")

		serverFound := false
		for _, server := range mcpServers {
			serverMap, ok := server.(map[string]interface{})
			require.True(t, ok, "server should be a map")

			name, ok := serverMap["name"].(string)
			if !ok || name != "existing-server" {
				continue
			}

			serverFound = true
			url, ok := serverMap["url"].(string)
			assert.True(t, ok, "url should be a string")
			assert.Equal(t, "http://localhost:8080/sse#test", url, "url should be updated")

			// Check that other fields are preserved
			command, ok := serverMap["command"].(string)
			assert.True(t, ok, "command should be a string")
			assert.Equal(t, "node", command, "command should be preserved")

			args, ok := serverMap["args"].([]interface{})
			assert.True(t, ok, "args should be an array")
			assert.Equal(t, 1, len(args), "args should have 1 element")
			assert.Equal(t, "server.js", args[0], "args should be preserved")

			env, ok := serverMap["env"].(map[string]interface{})
			assert.True(t, ok, "env should be a map")
			apiKey, ok := env["API_KEY"].(string)
			assert.True(t, ok, "API_KEY should be a string")
			assert.Equal(t, "test-key", apiKey, "API_KEY should be preserved")
		}

		assert.True(t, serverFound, "existing-server should be found")
	})

	// Test adding a new server
	t.Run("AddNewServer", func(t *testing.T) {
		t.Parallel()

		// Create a temporary directory for the test
		tempDir, err := os.MkdirTemp("", "continue-test-add")
		require.NoError(t, err)
		defer os.RemoveAll(tempDir)

		// Create a .continue directory in the temp dir
		continueDir := filepath.Join(tempDir, ".continue")
		err = os.Mkdir(continueDir, 0750)
		require.NoError(t, err)

		// Create a sample Continue YAML config
		configPath := filepath.Join(continueDir, "config.yaml")
		initialConfig := map[string]interface{}{
			"name":    "my-configuration",
			"version": "0.0.1",
			"schema":  "v1",
			"models": []interface{}{
				map[string]interface{}{
					"name":     "GPT-4",
					"provider": "openai",
					"model":    "gpt-4",
					"roles":    []string{"chat"},
				},
			},
			"mcpServers": []interface{}{
				map[string]interface{}{
					"name":    "existing-server",
					"command": "node",
					"args":    []string{"server.js"},
					"env": map[string]interface{}{
						"API_KEY": "test-key",
					},
				},
			},
		}

		// Marshal the initial config to YAML
		yamlData, err := yaml.Marshal(initialConfig)
		require.NoError(t, err)

		// Write the YAML to the config file
		err = os.WriteFile(configPath, yamlData, 0600)
		require.NoError(t, err)

		// Read the config file
		config, err := readConfigFile(configPath)
		require.NoError(t, err)

		err = config.UpdateMCPServerConfig("new-server", "http://localhost:9090/sse#new")
		require.NoError(t, err)

		// Save the config
		err = config.Save()
		require.NoError(t, err)

		// Read the config again
		updatedConfig, err := readConfigFile(configPath)
		require.NoError(t, err)

		// Check if the server was added correctly
		mcpServers, ok := updatedConfig.Contents["mcpServers"].([]interface{})
		require.True(t, ok, "mcpServers should be an array")

		serverFound := false
		for _, server := range mcpServers {
			serverMap, ok := server.(map[string]interface{})
			require.True(t, ok, "server should be a map")

			name, ok := serverMap["name"].(string)
			if !ok || name != "new-server" {
				continue
			}

			serverFound = true
			url, ok := serverMap["url"].(string)
			assert.True(t, ok, "url should be a string")
			assert.Equal(t, "http://localhost:9090/sse#new", url, "url should be set correctly")
		}

		assert.True(t, serverFound, "new-server should be found")
		assert.Equal(t, 2, len(mcpServers), "mcpServers should have 2 elements")
	})
}

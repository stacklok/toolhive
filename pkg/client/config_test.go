// Package client provides utilities for managing client configurations
// and interacting with MCP servers.
package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const testValidJSON = `{"mcpServers": {}, "mcp": {"servers": {}}}`
const testValidYAML = `extensions: {}`

// createMockClientConfigs creates a set of mock client configurations for testing
func createMockClientConfigs() []mcpClientConfig {
	return []mcpClientConfig{
		{
			ClientType:           VSCode,
			Description:          "Visual Studio Code (Mock)",
			RelPath:              []string{"mock_vscode"},
			SettingsFile:         "settings.json",
			MCPServersPathPrefix: "/mcp/servers",
			Extension:            JSON,
		},
		{
			ClientType:           VSCodeInsider,
			Description:          "Visual Studio Code Insiders (Mock)",
			RelPath:              []string{"mock_vscode_insider"},
			SettingsFile:         "settings.json",
			MCPServersPathPrefix: "/mcp/servers",
			Extension:            JSON,
		},
		{
			ClientType:           Cursor,
			Description:          "Cursor editor (Mock)",
			RelPath:              []string{"mock_cursor"},
			SettingsFile:         "mcp.json",
			MCPServersPathPrefix: "/mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           RooCode,
			Description:          "VS Code Roo Code extension (Mock)",
			RelPath:              []string{"mock_roo"},
			SettingsFile:         "mcp_settings.json",
			MCPServersPathPrefix: "/mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           ClaudeCode,
			Description:          "Claude Code CLI (Mock)",
			RelPath:              []string{"mock_claude"},
			SettingsFile:         ".claude.json",
			MCPServersPathPrefix: "/mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           Cline,
			Description:          "VS Code Cline extension (Mock)",
			RelPath:              []string{"mock_cline"},
			SettingsFile:         "cline_mcp_settings.json",
			MCPServersPathPrefix: "/mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           Windsurf,
			Description:          "Windsurf IDE (Mock)",
			RelPath:              []string{"mock_windsurf"},
			SettingsFile:         "mcp_config.json",
			MCPServersPathPrefix: "/mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           WindsurfJetBrains,
			Description:          "Windsurf plugin for JetBrains IDEs (Mock)",
			RelPath:              []string{"mock_windsurf_jetbrains"},
			SettingsFile:         "mcp_config.json",
			MCPServersPathPrefix: "/mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           AmpCli,
			Description:          "Sourcegraph Amp CLI (Mock)",
			RelPath:              []string{"mock_amp_cli"},
			SettingsFile:         "settings.json",
			MCPServersPathPrefix: "/amp.mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           AmpVSCode,
			Description:          "VS Code Sourcegraph Amp extension (Mock)",
			RelPath:              []string{"mock_amp_vscode"},
			SettingsFile:         "settings.json",
			MCPServersPathPrefix: "/amp.mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           AmpCursor,
			Description:          "Cursor Sourcegraph Amp extension (Mock)",
			RelPath:              []string{"mock_amp_cursor"},
			SettingsFile:         "settings.json",
			MCPServersPathPrefix: "/amp.mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           AmpVSCodeInsider,
			Description:          "VS Code Insiders Sourcegraph Amp extension (Mock)",
			RelPath:              []string{"mock_amp_vscode_insider"},
			SettingsFile:         "settings.json",
			MCPServersPathPrefix: "/amp.mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           AmpWindsurf,
			Description:          "Windsurf Sourcegraph Amp extension (Mock)",
			RelPath:              []string{"mock_amp_windsurf"},
			SettingsFile:         "settings.json",
			MCPServersPathPrefix: "/amp.mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           LMStudio,
			Description:          "LM Studio application (Mock)",
			RelPath:              []string{"mock_lm_studio"},
			SettingsFile:         "mcp_config.json",
			MCPServersPathPrefix: "/mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           Goose,
			Description:          "Goose AI agent (Mock)",
			RelPath:              []string{"mock_goose"},
			SettingsFile:         "config.yaml",
			MCPServersPathPrefix: "/extensions",
			Extension:            YAML,
		},
	}
}

// CreateTestConfigProvider creates a config provider for testing with the provided configuration.
// It returns a config provider and a cleanup function that should be deferred.
func CreateTestConfigProvider(t *testing.T, cfg *config.Config) (config.Provider, func()) {
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

func TestFindClientConfigs(t *testing.T) {
	t.Parallel()
	// Setup a temporary home directory for testing
	tempHome := t.TempDir()

	t.Run("InvalidConfigFileFormat", func(t *testing.T) {
		t.Parallel()

		// Initialize in-memory test logger
		observerLogs := logger.InitializeTest()

		// Create an invalid JSON file
		invalidPath := filepath.Join(tempHome, ".cursor", "invalid.json")
		err := os.MkdirAll(filepath.Dir(invalidPath), 0755)
		require.NoError(t, err)

		err = os.WriteFile(invalidPath, []byte("{invalid json}"), 0644)
		require.NoError(t, err)

		// Create fake test client integrations with Cursor pointing to invalid JSON
		// This tests the JSON validation error path
		testClientIntegrations := []mcpClientConfig{
			{
				ClientType:   VSCode,
				Description:  "VS Code (Test)",
				SettingsFile: "settings.json",
				RelPath:      []string{}, // File directly in temp home
				Extension:    JSON,
			},
			{
				ClientType:           Cursor,
				Description:          "Cursor editor (Test)",
				RelPath:              []string{".cursor"}, // Points to the .cursor directory where invalid.json is
				SettingsFile:         "invalid.json",      // This file contains invalid JSON
				MCPServersPathPrefix: "/mcpServers",
				Extension:            JSON,
			},
		}

		// Create a valid VSCode config file
		vscodeConfigPath := filepath.Join(tempHome, "settings.json")
		err = os.WriteFile(vscodeConfigPath, []byte(testValidJSON), 0644)
		require.NoError(t, err)

		testConfig := &config.Config{
			Secrets: config.Secrets{
				ProviderType: "encrypted",
			},
			Clients: config.Clients{
				RegisteredClients: []string{
					string(Cursor), // Register cursor which will have invalid JSON
					string(VSCode), // Also register a valid client for comparison
				},
			},
		}

		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Find client configs using ClientManager - this should NOT fail due to the invalid JSON
		// Instead, it should log a warning and continue
		manager := NewTestClientManager(tempHome, nil, testClientIntegrations, configProvider)
		configs, err := manager.FindRegisteredClientConfigs(context.Background())
		assert.NoError(t, err, "FindRegisteredClientConfigs should not return an error for invalid config files")

		// The cursor client with invalid JSON should be skipped, so we should get configs for valid clients only
		// We expect 1 config (VSCode) since cursor with invalid JSON should be skipped
		assert.Len(t, configs, 1, "Should find configs for valid clients only, skipping invalid ones")

		// Read all log entries
		var sb strings.Builder
		for _, entry := range observerLogs.All() {
			sb.WriteString(entry.Message)
		}

		logOutput := sb.String()

		// Verify that the error was logged
		assert.Contains(t, logOutput, "Unable to process client config for cursor", "Should log warning about cursor client config")
		assert.Contains(t, logOutput, "failed to validate config file format", "Should log the specific validation error")
		assert.Contains(t, logOutput, "cursor", "Should mention cursor in the error message")
	})
}

func TestSuccessfulClientConfigOperations(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	// Setup a temporary home directory for testing
	tempHome := t.TempDir()

	// Create mock client configs explicitly (don't modify global variable)
	mockClientConfigs := createMockClientConfigs()

	// Create test config files using mock configs
	createTestConfigFilesWithConfigs(t, tempHome, mockClientConfigs)

	// Set up config for all sub-tests
	testConfig := &config.Config{
		Secrets: config.Secrets{
			ProviderType: "encrypted",
		},
		Clients: config.Clients{
			RegisteredClients: []string{
				string(VSCode),
				string(VSCodeInsider),
				string(Cursor),
				string(RooCode),
				string(ClaudeCode),
				string(Cline),
				string(Windsurf),
				string(WindsurfJetBrains),
				string(AmpCli),
				string(AmpVSCode),
				string(AmpCursor),
				string(AmpVSCodeInsider),
				string(AmpWindsurf),
				string(LMStudio),
				string(Goose),
			},
		},
	}

	configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
	t.Cleanup(cleanup)

	t.Run("FindAllConfiguredClients", func(t *testing.T) {
		t.Parallel()
		// Create mock client configs explicitly (don't rely on global variable due to parallel tests)
		mockClientConfigs := createMockClientConfigs()

		// Create ClientManager with test dependencies using the mock client integrations
		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)

		configs, err := manager.FindRegisteredClientConfigs(context.Background())
		require.NoError(t, err)
		assert.Len(t, configs, len(mockClientConfigs), "Should find all mock client configs")

		// Verify each client type is found
		foundTypes := make(map[MCPClient]bool)
		for _, cf := range configs {
			foundTypes[cf.ClientType] = true
		}

		for _, expectedClient := range mockClientConfigs {
			assert.True(t, foundTypes[expectedClient.ClientType],
				"Should find config for client type %s", expectedClient.ClientType)
		}
	})

	t.Run("VerifyConfigFileContents", func(t *testing.T) {
		t.Parallel()
		// Create mock client configs explicitly (don't rely on global variable due to parallel tests)
		mockClientConfigs := createMockClientConfigs()

		// Create ClientManager with test dependencies using the mock client integrations
		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)
		configs, err := manager.FindRegisteredClientConfigs(context.Background())
		require.NoError(t, err)
		require.NotEmpty(t, configs)

		for _, cf := range configs {
			// Read and parse the config file
			content, err := os.ReadFile(cf.Path)
			require.NoError(t, err, "Should be able to read config file for %s", cf.ClientType)

			// Verify JSON structure based on client type
			switch cf.ClientType {
			case VSCode, VSCodeInsider:
				assert.Contains(t, string(content), `"mcp":`,
					"Cconfig should contain mcp key")
				assert.Contains(t, string(content), `"servers":`,
					"VSCode config should contain servers key")
			case Cursor:
				assert.Contains(t, string(content), `"mcpServers":`,
					"Cursor config should contain mcpServers key")
			case RooCode:
				assert.Contains(t, string(content), `"mcpServers":`,
					"RooCode config should contain mcpServers key")
			case ClaudeCode:
				assert.Contains(t, string(content), `"mcpServers":`,
					"ClaudeCode config should contain mcpServers key")
			case Cline:
				assert.Contains(t, string(content), `"mcpServers":`,
					"Cline config should contain mcpServers key")
			case Windsurf:
				assert.Contains(t, string(content), `"mcpServers":`,
					"Windsurf config should contain mcpServers key")
			case WindsurfJetBrains:
				assert.Contains(t, string(content), `"mcpServers":`,
					"WindsurfJetBrains config should contain mcpServers key")
			case AmpCli:
				assert.Contains(t, string(content), `"mcpServers":`,
					"AmpCli config should contain mcpServers key")
			case AmpVSCode:
				assert.Contains(t, string(content), `"mcpServers":`,
					"AmpVSCode config should contain mcpServers key")
			case AmpVSCodeInsider:
				assert.Contains(t, string(content), `"mcpServers":`,
					"AmpVSCodeInsider config should contain mcpServers key")
			case AmpCursor:
				assert.Contains(t, string(content), `"mcpServers":`,
					"AmpCursor config should contain mcpServers key")
			case AmpWindsurf:
				assert.Contains(t, string(content), `"mcpServers":`,
					"AmpWindsurf config should contain mcpServers key")
			case LMStudio:
				assert.Contains(t, string(content), `"mcpServers":`,
					"LMStudio config should contain mcpServers key")
			case Goose:
				assert.Contains(t, string(content), `extensions:`,
					"Goose config should contain extensions key")
			}
		}
	})

	t.Run("AddAndVerifyMCPServer", func(t *testing.T) {
		t.Parallel()
		// Create mock client configs explicitly (don't rely on global variable due to parallel tests)
		mockClientConfigs := createMockClientConfigs()

		// Create ClientManager with test dependencies using the mock client integrations
		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)
		configs, err := manager.FindRegisteredClientConfigs(context.Background())
		require.NoError(t, err)
		require.NotEmpty(t, configs)

		testServer := "test-server"
		testURL := "http://localhost:9999/sse#test-server"

		for _, cf := range configs {
			// Use the manager's Upsert method instead of the global function to avoid using the singleton config
			err := manager.Upsert(cf, testServer, testURL, types.TransportTypeSSE.String())
			require.NoError(t, err, "Should be able to add MCP server to %s config", cf.ClientType)

			// Read the file and verify the server was added
			content, err := os.ReadFile(cf.Path)
			require.NoError(t, err)

			// Check based on client type
			switch cf.ClientType {
			case VSCode, VSCodeInsider:
				assert.Contains(t, string(content), testURL,
					"VSCode config should contain the server URL")
			case Cursor, RooCode, ClaudeCode, Cline, Windsurf, WindsurfJetBrains, AmpCli,
				AmpVSCode, AmpCursor, AmpVSCodeInsider, AmpWindsurf, LMStudio, Goose:
				assert.Contains(t, string(content), testURL,
					"Config should contain the server URL")
			}
		}
	})
}

// Helper function to create test config files for specific client configurations
func createTestConfigFilesWithConfigs(t *testing.T, homeDir string, clientConfigs []mcpClientConfig) {
	t.Helper()
	// Create test config files for each provided client configuration
	for _, cfg := range clientConfigs {
		// Build the full path for the config file
		configDir := filepath.Join(homeDir, filepath.Join(cfg.RelPath...))
		err := os.MkdirAll(configDir, 0755)
		if err == nil {
			configPath := filepath.Join(configDir, cfg.SettingsFile)

			// Choose the appropriate content based on the file extension
			var content []byte
			if cfg.Extension == YAML {
				content = []byte(testValidYAML)
			} else {
				content = []byte(testValidJSON)
			}

			err = os.WriteFile(configPath, content, 0644)
			require.NoError(t, err)
		}
	}
}

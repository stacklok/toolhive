// Package client provides utilities for managing client configurations
// and interacting with MCP servers.
package client

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

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
			ClientType:           AmpCli,
			Description:          "Sourcegraph Amp CLI (Mock)",
			RelPath:              []string{"mock_amp_cli"},
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
	}
}

// MockConfig creates a temporary config file with the provided configuration.
// It returns a cleanup function that should be deferred.
func MockConfig(t *testing.T, cfg *config.Config) func() {
	t.Helper()

	// Create a temporary directory for the test
	tempDir := t.TempDir()

	// TODO: see if there's a way to avoid changing env vars during tests.
	// Save original XDG_CONFIG_HOME
	originalXDGConfigHome := os.Getenv("XDG_CONFIG_HOME")
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	xdg.Reload()

	// Create the config directory structure
	configDir := filepath.Join(tempDir, "toolhive")
	err := os.MkdirAll(configDir, 0755)
	require.NoError(t, err)

	// Write the config file if one is provided
	if cfg != nil {
		err = config.UpdateConfig(func(c *config.Config) { *c = *cfg })
		require.NoError(t, err)
	}

	return func() {
		t.Setenv("XDG_CONFIG_HOME", originalXDGConfigHome)
		xdg.Reload()
	}
}

func TestFindClientConfigs(t *testing.T) { //nolint:paralleltest // Uses environment variables
	logger.Initialize()

	// Setup a temporary home directory for testing
	originalHome := os.Getenv("HOME")
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	defer func() {
		t.Setenv("HOME", originalHome)
	}()

	// Save original supported clients and restore after test
	originalClients := supportedClientIntegrations
	defer func() {
		supportedClientIntegrations = originalClients
	}()

	// Set up mock client configurations
	supportedClientIntegrations = createMockClientConfigs()

	// Create test config files for different clients
	createTestConfigFiles(t, tempHome)

	t.Run("InvalidConfigFileFormat", func(t *testing.T) { //nolint:paralleltest // Modifies global state
		// Set up environment for unstructured logs and capture stderr before initializing logger
		originalUnstructuredLogs := os.Getenv("UNSTRUCTURED_LOGS")
		os.Setenv("UNSTRUCTURED_LOGS", "true")
		defer os.Setenv("UNSTRUCTURED_LOGS", originalUnstructuredLogs)

		// Capture log output to verify error logging
		originalStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		// Re-initialize logger to use the captured stderr
		logger.Initialize()

		// Create an invalid JSON file
		invalidPath := filepath.Join(tempHome, ".cursor", "invalid.json")
		err := os.MkdirAll(filepath.Dir(invalidPath), 0755)
		require.NoError(t, err)

		err = os.WriteFile(invalidPath, []byte("{invalid json}"), 0644)
		require.NoError(t, err)

		// Create a custom client config that points to the invalid file
		invalidClient := mcpClientConfig{
			ClientType:           "invalid",
			Description:          "Invalid client",
			RelPath:              []string{".cursor"},
			SettingsFile:         "invalid.json",
			MCPServersPathPrefix: "/mcpServers",
			Extension:            JSON,
		}

		// Save the original supported clients
		originalClients := supportedClientIntegrations
		defer func() {
			supportedClientIntegrations = originalClients
		}()

		// Add our invalid client to the supported clients
		supportedClientIntegrations = append(supportedClientIntegrations, invalidClient)

		testConfig := &config.Config{
			Secrets: config.Secrets{
				ProviderType: "encrypted",
			},
			Clients: config.Clients{
				RegisteredClients: []string{
					"invalid",      // Register the invalid client
					string(VSCode), // Also register a valid client for comparison
				},
			},
		}

		cleanup := MockConfig(t, testConfig)
		defer cleanup()

		// Find client configs - this should NOT fail due to the invalid JSON
		// Instead, it should log a warning and continue
		configs, err := FindRegisteredClientConfigs(context.Background())
		assert.NoError(t, err, "FindRegisteredClientConfigs should not return an error for invalid config files")

		// The invalid client should be skipped, so we should get configs for valid clients only
		// We expect 1 config (VSCode) since invalid should be skipped
		assert.Len(t, configs, 1, "Should find configs for valid clients only, skipping invalid ones")

		// Restore stderr and capture log output
		w.Close()
		os.Stderr = originalStderr

		var capturedOutput bytes.Buffer
		io.Copy(&capturedOutput, r)
		logOutput := capturedOutput.String()

		// Verify that the error was logged
		assert.Contains(t, logOutput, "Unable to process client config for invalid", "Should log warning about invalid client config")
		assert.Contains(t, logOutput, "failed to validate config file format", "Should log the specific validation error")
		assert.Contains(t, logOutput, "cursor", "Should mention cursor in the error message")
	})
}

func TestSuccessfulClientConfigOperations(t *testing.T) {
	logger.Initialize()

	// Setup a temporary home directory for testing
	originalHome := os.Getenv("HOME")
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	defer func() {
		t.Setenv("HOME", originalHome)
	}()

	// Save original supported clients and restore after test
	originalClients := supportedClientIntegrations
	defer func() {
		supportedClientIntegrations = originalClients
	}()

	// Set up mock client configurations
	supportedClientIntegrations = createMockClientConfigs()

	// Create test config files
	createTestConfigFiles(t, tempHome)

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
				string(AmpCli),
				string(AmpVSCode),
				string(AmpCursor),
				string(AmpVSCodeInsider),
				string(AmpWindsurf),
			},
		},
	}

	cleanup := MockConfig(t, testConfig)
	defer cleanup()

	t.Run("FindAllConfiguredClients", func(t *testing.T) { //nolint:paralleltest // Uses environment variables
		configs, err := FindRegisteredClientConfigs(context.Background())
		require.NoError(t, err)
		assert.Len(t, configs, len(supportedClientIntegrations), "Should find all mock client configs")

		// Verify each client type is found
		foundTypes := make(map[MCPClient]bool)
		for _, cf := range configs {
			foundTypes[cf.ClientType] = true
		}

		for _, expectedClient := range supportedClientIntegrations {
			assert.True(t, foundTypes[expectedClient.ClientType],
				"Should find config for client type %s", expectedClient.ClientType)
		}
	})

	t.Run("VerifyConfigFileContents", func(t *testing.T) { //nolint:paralleltest // Uses environment variables
		configs, err := FindRegisteredClientConfigs(context.Background())
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
			}
		}
	})

	t.Run("AddAndVerifyMCPServer", func(t *testing.T) { //nolint:paralleltest // Uses environment variables
		configs, err := FindRegisteredClientConfigs(context.Background())
		require.NoError(t, err)
		require.NotEmpty(t, configs)

		testServer := "test-server"
		testURL := "http://localhost:9999/sse#test-server"

		for _, cf := range configs {
			err := Upsert(cf, testServer, testURL, types.TransportTypeSSE.String())
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
				AmpVSCode, AmpCursor, AmpVSCodeInsider, AmpWindsurf:
				assert.Contains(t, string(content), testURL,
					"Config should contain the server URL")
			}
		}
	})
}

// Helper function to create test config files for different clients
func createTestConfigFiles(t *testing.T, homeDir string) {
	t.Helper()
	// Create test config files for each mock client configuration
	for _, cfg := range supportedClientIntegrations {
		// Build the full path for the config file
		configDir := filepath.Join(homeDir, filepath.Join(cfg.RelPath...))
		err := os.MkdirAll(configDir, 0755)
		if err == nil {
			configPath := filepath.Join(configDir, cfg.SettingsFile)
			validJSON := `{"mcpServers": {}, "mcp": {"servers": {}}}`
			err = os.WriteFile(configPath, []byte(validJSON), 0644)
			require.NoError(t, err)
		}
	}
}

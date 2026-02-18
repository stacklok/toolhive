// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package client provides utilities for managing client configurations
// and interacting with MCP servers.
package client

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/logging"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const testValidJSON = `{"mcpServers": {}, "mcp": {"servers": {}}}`
const testValidYAML = `extensions: {}`
const testValidTOML = ``

// createMockClientConfigs creates a set of mock client configurations for testing
func createMockClientConfigs() []clientAppConfig {
	return []clientAppConfig{
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
			ClientType:           OpenCode,
			Description:          "OpenCode application (Mock)",
			RelPath:              []string{"mock_opencode"},
			SettingsFile:         "opencode.json",
			MCPServersPathPrefix: "/mcp",
			Extension:            JSON,
		},
		{
			ClientType:           Kiro,
			Description:          "Kiro application (Mock)",
			RelPath:              []string{"mock_kiro"},
			SettingsFile:         "mcp.json",
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
		{
			ClientType:           Continue,
			Description:          "Continue.dev extension (Mock)",
			RelPath:              []string{"mock_continue"},
			SettingsFile:         "config.yaml",
			MCPServersPathPrefix: "/mcpServers",
			Extension:            YAML,
		},
		{
			ClientType:           GeminiCli,
			Description:          "Google Gemini CLI (Mock)",
			RelPath:              []string{"mock_gemini"},
			SettingsFile:         "settings.json",
			MCPServersPathPrefix: "/mcpServers",
			Extension:            JSON,
		},
		{
			ClientType:           VSCodeServer,
			Description:          "Microsoft's VS Code Server (Mock)",
			RelPath:              []string{"mock_vscode_server"},
			SettingsFile:         "mcp.json",
			MCPServersPathPrefix: "/servers",
			Extension:            JSON,
		},
		{
			ClientType:           MistralVibe,
			Description:          "Mistral Vibe IDE (Mock)",
			RelPath:              []string{"mock_mistral_vibe"},
			SettingsFile:         "config.toml",
			MCPServersPathPrefix: "/mcp_servers",
			Extension:            TOML,
			TOMLStorageType:      TOMLStorageTypeArray,
		},
		{
			ClientType:           Codex,
			Description:          "OpenAI Codex CLI (Mock)",
			RelPath:              []string{"mock_codex"},
			SettingsFile:         "config.toml",
			MCPServersPathPrefix: "/mcp_servers",
			Extension:            TOML,
			TOMLStorageType:      TOMLStorageTypeMap,
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

//nolint:paralleltest // This test modifies global logger
func TestFindClientConfigs(t *testing.T) { // Can't run in parallel because it uses global logger
	// Setup a temporary home directory for testing
	tempHome := t.TempDir()

	t.Run("InvalidConfigFileFormat", func(t *testing.T) {
		// Initialize in-memory test logger that captures output to a buffer
		logBuf := initializeTest(t)

		// Create an invalid JSON file
		invalidPath := filepath.Join(tempHome, ".cursor", "invalid.json")
		err := os.MkdirAll(filepath.Dir(invalidPath), 0755)
		require.NoError(t, err)

		err = os.WriteFile(invalidPath, []byte("{invalid json}"), 0644)
		require.NoError(t, err)

		// Create fake test client integrations with Cursor pointing to invalid JSON
		// This tests the JSON validation error path
		testClientIntegrations := []clientAppConfig{
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

		logOutput := logBuf.String()

		// Verify that the error was logged (slog uses structured key-value pairs)
		assert.Contains(t, logOutput, "Unable to process client config", "Should log warning about client config")
		assert.Contains(t, logOutput, "client=cursor", "Should log cursor as the client attribute")
		assert.Contains(t, logOutput, "failed to validate config file format", "Should log the specific validation error")
	})
}

// initializeTest sets up a buffer-backed slog logger as the global singleton
// so that test assertions can inspect log output. It returns the buffer.
func initializeTest(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer

	level := slog.LevelInfo
	if viper.GetBool("debug") {
		level = slog.LevelDebug
	}

	testLogger := logging.New(
		logging.WithOutput(&buf),
		logging.WithLevel(level),
		logging.WithFormat(logging.FormatText),
	)

	prev := logger.Get()
	logger.Set(testLogger)

	t.Cleanup(func() {
		logger.Set(prev)
	})

	return &buf
}

func TestSuccessfulClientConfigOperations(t *testing.T) {
	t.Parallel()

	// Helper function to create isolated test setup for each subtest
	setupSubtest := func(t *testing.T) (string, []clientAppConfig, config.Provider) {
		t.Helper()

		// Create isolated temporary home directory for this subtest
		tempHome := t.TempDir()

		// Create mock client configs
		mockClientConfigs := createMockClientConfigs()

		// Create test config files using mock configs
		createTestConfigFilesWithConfigs(t, tempHome, mockClientConfigs)

		// Set up config
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
					string(Trae),
					string(Continue),
					string(OpenCode),
					string(Kiro),
					string(Antigravity),
					string(Zed),
					string(GeminiCli),
					string(VSCodeServer),
					string(MistralVibe),
					string(Codex),
				},
			},
		}

		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		t.Cleanup(cleanup)

		return tempHome, mockClientConfigs, configProvider
	}

	t.Run("FindAllConfiguredClients", func(t *testing.T) {
		t.Parallel()

		// Create isolated resources for this subtest
		tempHome, mockClientConfigs, configProvider := setupSubtest(t)

		// Create ClientManager with test dependencies using the mock client integrations
		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)

		configs, err := manager.FindRegisteredClientConfigs(context.Background())
		require.NoError(t, err)
		assert.Len(t, configs, len(mockClientConfigs), "Should find all mock client configs")

		// Verify each client type is found
		foundTypes := make(map[ClientApp]bool)
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

		// Create isolated resources for this subtest
		tempHome, mockClientConfigs, configProvider := setupSubtest(t)

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
					"Config should contain mcp key")
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
			case LMStudio, Trae, Kiro, Antigravity, GeminiCli:
				assert.Contains(t, string(content), `"mcpServers":`,
					"Config should contain mcpServers key")
			case VSCodeServer:
				assert.Contains(t, string(content), `"servers":`,
					"VSCodeServer config should contain servers key")
			case OpenCode:
				assert.Contains(t, string(content), `"mcp":`,
					"OpenCode config should contain mcp key")
			case Zed:
				assert.Contains(t, string(content), `"context_servers":`,
					"Zed config should contain context_servers key")
			case Goose:
				// YAML files are created empty and initialized on first use
				// Just verify the file exists and is readable
				assert.NotNil(t, content, "Goose config should be readable")
			case Continue:
				// YAML files are created empty and initialized on first use
				// Just verify the file exists and is readable
				assert.NotNil(t, content, "Continue config should be readable")
			case MistralVibe, Codex:
				// TOML files are created empty and initialized on first use
				// Just verify the file exists and is readable
				assert.NotNil(t, content, "TOML config should be readable")
			}
		}
	})

	t.Run("AddAndVerifyMCPServer", func(t *testing.T) {
		t.Parallel()

		// Create isolated resources for this subtest
		tempHome, mockClientConfigs, configProvider := setupSubtest(t)

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
				AmpVSCode, AmpCursor, AmpVSCodeInsider, AmpWindsurf, LMStudio, Goose, Trae, Continue, OpenCode, Kiro, Antigravity, Zed, GeminiCli, VSCodeServer,
				MistralVibe, Codex:
				assert.Contains(t, string(content), testURL,
					"Config should contain the server URL")
			}
		}
	})
}

// Helper function to create test config files for specific client configurations
func createTestConfigFilesWithConfigs(t *testing.T, homeDir string, clientConfigs []clientAppConfig) {
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
			switch cfg.Extension {
			case YAML:
				content = []byte(testValidYAML)
			case TOML:
				content = []byte(testValidTOML)
			case JSON:
				content = []byte(testValidJSON)
			}

			err = os.WriteFile(configPath, content, 0644)
			require.NoError(t, err)
		}
	}
}

func TestCreateClientConfig(t *testing.T) {
	t.Parallel()

	testConfig := &config.Config{
		Secrets: config.Secrets{
			ProviderType: "encrypted",
		},
		Clients: config.Clients{
			RegisteredClients: []string{
				string(VSCode),
				string(Goose),
			},
		},
	}

	t.Run("CreateJSONClientConfig", func(t *testing.T) {
		t.Parallel()
		// Setup a temporary home directory for testing
		tempHome := t.TempDir()

		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create mock client config for JSON client (VSCode)
		mockClientConfigs := []clientAppConfig{
			{
				ClientType:           VSCode,
				Description:          "Visual Studio Code (Mock)",
				RelPath:              []string{"mock_vscode"},
				SettingsFile:         "settings.json",
				MCPServersPathPrefix: "/mcp/servers",
				Extension:            JSON,
			},
		}

		// Create the parent directory structure that would normally exist
		configDir := filepath.Join(tempHome, "mock_vscode")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)

		// Call CreateClientConfig - this should create a new JSON file
		cf, err := manager.CreateClientConfig(VSCode)
		require.NoError(t, err, "Should successfully create new JSON client config")
		require.NotNil(t, cf, "Should return a config file")

		// Verify the file was created
		_, statErr := os.Stat(cf.Path)
		require.NoError(t, statErr, "Config file should exist after creation")

		// Verify the file contains an empty JSON object
		content, err := os.ReadFile(cf.Path)
		require.NoError(t, err, "Should be able to read created file")
		assert.Equal(t, "{}", string(content), "JSON config should contain empty object")

		// Verify file permissions
		fileInfo, err := os.Stat(cf.Path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), fileInfo.Mode().Perm(), "File should have 0600 permissions")
	})

	t.Run("CreateYAMLClientConfig", func(t *testing.T) {
		t.Parallel()
		// Setup a temporary home directory for testing
		tempHome := t.TempDir()

		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create mock client config for YAML client (Goose)
		mockClientConfigs := []clientAppConfig{
			{
				ClientType:           Goose,
				Description:          "Goose AI agent (Mock)",
				RelPath:              []string{"mock_goose"},
				SettingsFile:         "config.yaml",
				MCPServersPathPrefix: "/extensions",
				Extension:            YAML,
			},
		}

		// Create the parent directory structure that would normally exist
		configDir := filepath.Join(tempHome, "mock_goose")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)

		// Call CreateClientConfig - this should create a new YAML file
		cf, err := manager.CreateClientConfig(Goose)
		require.NoError(t, err, "Should successfully create new YAML client config")
		require.NotNil(t, cf, "Should return a config file")

		// Verify the file was created
		_, statErr := os.Stat(cf.Path)
		require.NoError(t, statErr, "Config file should exist after creation")

		// Verify the file is empty (YAML files start empty)
		content, err := os.ReadFile(cf.Path)
		require.NoError(t, err, "Should be able to read created file")
		assert.Equal(t, "", string(content), "YAML config should be empty initially")

		// Verify file permissions
		fileInfo, err := os.Stat(cf.Path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), fileInfo.Mode().Perm(), "File should have 0600 permissions")
	})

	t.Run("CreateClientConfigFileAlreadyExists", func(t *testing.T) {
		t.Parallel()
		// Setup a temporary home directory for testing
		tempHome := t.TempDir()

		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create mock client config
		mockClientConfigs := []clientAppConfig{
			{
				ClientType:           VSCode,
				Description:          "Visual Studio Code (Mock)",
				RelPath:              []string{"mock_vscode"},
				SettingsFile:         "settings.json",
				MCPServersPathPrefix: "/mcp/servers",
				Extension:            JSON,
			},
		}

		// Pre-create the config file
		configDir := filepath.Join(tempHome, "mock_vscode")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)
		configPath := filepath.Join(configDir, "settings.json")
		err = os.WriteFile(configPath, []byte(testValidJSON), 0644)
		require.NoError(t, err)

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)

		// Call CreateClientConfig - this should fail because file already exists
		cf, err := manager.CreateClientConfig(VSCode)
		assert.Error(t, err, "Should return error when config file already exists")
		assert.Nil(t, cf, "Should not return a config file on error")
		assert.Contains(t, err.Error(), "already exists", "Error should mention file already exists")
	})

	t.Run("CreateClientConfigUnsupportedClientType", func(t *testing.T) {
		t.Parallel()
		// Setup a temporary home directory for testing
		tempHome := t.TempDir()

		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create empty mock client configs (no supported clients)
		mockClientConfigs := []clientAppConfig{}

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)

		// Call CreateClientConfig with unsupported client type
		cf, err := manager.CreateClientConfig(VSCode)
		assert.Error(t, err, "Should return error for unsupported client type")
		assert.Nil(t, cf, "Should not return a config file on error")
		assert.Contains(t, err.Error(), "unsupported client type", "Error should mention unsupported client type")
	})

	t.Run("CreateClientConfigUnsupportedClientTypeIsSentinelError", func(t *testing.T) {
		t.Parallel()
		// Setup a temporary home directory for testing
		tempHome := t.TempDir()

		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create empty mock client configs (no supported clients)
		mockClientConfigs := []clientAppConfig{}

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)

		// Call CreateClientConfig with unsupported client type
		_, err := manager.CreateClientConfig(VSCode)
		require.Error(t, err)

		// Verify the error can be matched using errors.Is with the sentinel error
		// This is important for API handlers to return appropriate HTTP status codes
		assert.True(t, errors.Is(err, ErrUnsupportedClientType),
			"Error should be matchable with ErrUnsupportedClientType sentinel error")
	})

	t.Run("CreateClientConfigWriteError", func(t *testing.T) {
		t.Parallel()
		// Setup a temporary home directory for testing
		tempHome := t.TempDir()

		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create mock client config with a path that will cause write error
		mockClientConfigs := []clientAppConfig{
			{
				ClientType:           VSCode,
				Description:          "Visual Studio Code (Mock)",
				RelPath:              []string{"readonly_dir", "nested"},
				SettingsFile:         "settings.json",
				MCPServersPathPrefix: "/mcp/servers",
				Extension:            JSON,
			},
		}

		// Create the nested directory first and make it readonly
		nestedDir := filepath.Join(tempHome, "readonly_dir", "nested")
		err := os.MkdirAll(nestedDir, 0755)
		require.NoError(t, err)

		// Now make the nested directory read-only so we can't create files in it
		err = os.Chmod(nestedDir, 0444)
		require.NoError(t, err)
		defer os.Chmod(nestedDir, 0755) // Cleanup

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)

		// Call CreateClientConfig - this should fail due to permission error
		// Note: The exact error depends on how os.Stat behaves with readonly dirs
		cf, err := manager.CreateClientConfig(VSCode)
		assert.Error(t, err, "Should return error when unable to write file")
		assert.Nil(t, cf, "Should not return a config file on error")
		// Accept either error message since readonly directory can trigger different error paths
		hasExpectedError := strings.Contains(err.Error(), "failed to create client config file") ||
			strings.Contains(err.Error(), "already exists")
		assert.True(t, hasExpectedError, "Error should mention creation failure or file exists, got: %v", err.Error())
	})
}

func TestCreateTOMLClientConfig(t *testing.T) {
	t.Parallel()

	testConfig := &config.Config{
		Secrets: config.Secrets{
			ProviderType: "encrypted",
		},
		Clients: config.Clients{
			RegisteredClients: []string{
				string(MistralVibe),
				string(Codex),
			},
		},
	}

	t.Run("CreateTOMLArrayClientConfig", func(t *testing.T) {
		t.Parallel()
		// Setup a temporary home directory for testing
		tempHome := t.TempDir()

		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create mock client config for TOML client with array storage (MistralVibe)
		mockClientConfigs := []clientAppConfig{
			{
				ClientType:           MistralVibe,
				Description:          "Mistral Vibe IDE (Mock)",
				RelPath:              []string{"mock_mistral_vibe"},
				SettingsFile:         "config.toml",
				MCPServersPathPrefix: "/mcp_servers",
				Extension:            TOML,
				TOMLStorageType:      TOMLStorageTypeArray,
			},
		}

		// Create the parent directory structure that would normally exist
		configDir := filepath.Join(tempHome, "mock_mistral_vibe")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)

		// Call CreateClientConfig - this should create a new TOML file
		cf, err := manager.CreateClientConfig(MistralVibe)
		require.NoError(t, err, "Should successfully create new TOML client config")
		require.NotNil(t, cf, "Should return a config file")

		// Verify the file was created
		_, statErr := os.Stat(cf.Path)
		require.NoError(t, statErr, "Config file should exist after creation")

		// Verify the file is empty (TOML files start empty like YAML)
		content, err := os.ReadFile(cf.Path)
		require.NoError(t, err, "Should be able to read created file")
		assert.Equal(t, "", string(content), "TOML config should be empty initially")

		// Verify file permissions
		fileInfo, err := os.Stat(cf.Path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), fileInfo.Mode().Perm(), "File should have 0600 permissions")
	})

	t.Run("CreateTOMLMapClientConfig", func(t *testing.T) {
		t.Parallel()
		// Setup a temporary home directory for testing
		tempHome := t.TempDir()

		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create mock client config for TOML client with map storage (Codex)
		mockClientConfigs := []clientAppConfig{
			{
				ClientType:           Codex,
				Description:          "OpenAI Codex CLI (Mock)",
				RelPath:              []string{"mock_codex"},
				SettingsFile:         "config.toml",
				MCPServersPathPrefix: "/mcp_servers",
				Extension:            TOML,
				TOMLStorageType:      TOMLStorageTypeMap,
			},
		}

		// Create the parent directory structure that would normally exist
		configDir := filepath.Join(tempHome, "mock_codex")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)

		// Call CreateClientConfig - this should create a new TOML file
		cf, err := manager.CreateClientConfig(Codex)
		require.NoError(t, err, "Should successfully create new TOML client config")
		require.NotNil(t, cf, "Should return a config file")

		// Verify the file was created
		_, statErr := os.Stat(cf.Path)
		require.NoError(t, statErr, "Config file should exist after creation")

		// Verify the file is empty (TOML files start empty like YAML)
		content, err := os.ReadFile(cf.Path)
		require.NoError(t, err, "Should be able to read created file")
		assert.Equal(t, "", string(content), "TOML config should be empty initially")

		// Verify file permissions
		fileInfo, err := os.Stat(cf.Path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), fileInfo.Mode().Perm(), "File should have 0600 permissions")
	})
}

func TestUpsertWithDynamicUrlFieldMapping(t *testing.T) {
	t.Parallel()

	// Test that Gemini CLI uses different URL fields based on transport type
	t.Run("GeminiCli_SSE_UsesUrlField", func(t *testing.T) {
		t.Parallel()
		tempHome := t.TempDir()

		// Create mock client config for Gemini CLI with MCPServersUrlLabelMap
		mockClientConfigs := []clientAppConfig{
			{
				ClientType:                    GeminiCli,
				Description:                   "Google Gemini CLI (Mock)",
				RelPath:                       []string{"mock_gemini"},
				SettingsFile:                  "settings.json",
				MCPServersPathPrefix:          "/mcpServers",
				Extension:                     JSON,
				IsTransportTypeFieldSupported: false,
				MCPServersUrlLabelMap: map[types.TransportType]string{
					types.TransportTypeStdio:          "httpUrl",
					types.TransportTypeSSE:            "url",
					types.TransportTypeStreamableHTTP: "httpUrl",
				},
			},
		}

		testConfig := &config.Config{
			Secrets: config.Secrets{ProviderType: "encrypted"},
			Clients: config.Clients{RegisteredClients: []string{string(GeminiCli)}},
		}
		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create config file
		configDir := filepath.Join(tempHome, "mock_gemini")
		require.NoError(t, os.MkdirAll(configDir, 0755))
		configPath := filepath.Join(configDir, "settings.json")
		require.NoError(t, os.WriteFile(configPath, []byte(`{"mcpServers": {}}`), 0644))

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)
		cf, err := manager.FindClientConfig(GeminiCli)
		require.NoError(t, err)

		// Upsert with SSE transport - should use "url" field
		err = manager.Upsert(*cf, "test-server", "http://localhost:8080/sse", types.TransportTypeSSE.String())
		require.NoError(t, err)

		// Verify the config file uses "url" field (not "httpUrl")
		content, err := os.ReadFile(cf.Path)
		require.NoError(t, err)
		assert.Contains(t, string(content), `"url":`, "SSE transport should use 'url' field")
		assert.NotContains(t, string(content), `"httpUrl":`, "SSE transport should NOT use 'httpUrl' field")
	})

	t.Run("GeminiCli_StreamableHTTP_UsesHttpUrlField", func(t *testing.T) {
		t.Parallel()
		tempHome := t.TempDir()

		// Create mock client config for Gemini CLI with MCPServersUrlLabelMap
		mockClientConfigs := []clientAppConfig{
			{
				ClientType:                    GeminiCli,
				Description:                   "Google Gemini CLI (Mock)",
				RelPath:                       []string{"mock_gemini"},
				SettingsFile:                  "settings.json",
				MCPServersPathPrefix:          "/mcpServers",
				Extension:                     JSON,
				IsTransportTypeFieldSupported: false,
				MCPServersUrlLabelMap: map[types.TransportType]string{
					types.TransportTypeStdio:          "httpUrl",
					types.TransportTypeSSE:            "url",
					types.TransportTypeStreamableHTTP: "httpUrl",
				},
			},
		}

		testConfig := &config.Config{
			Secrets: config.Secrets{ProviderType: "encrypted"},
			Clients: config.Clients{RegisteredClients: []string{string(GeminiCli)}},
		}
		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create config file
		configDir := filepath.Join(tempHome, "mock_gemini")
		require.NoError(t, os.MkdirAll(configDir, 0755))
		configPath := filepath.Join(configDir, "settings.json")
		require.NoError(t, os.WriteFile(configPath, []byte(`{"mcpServers": {}}`), 0644))

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)
		cf, err := manager.FindClientConfig(GeminiCli)
		require.NoError(t, err)

		// Upsert with Streamable HTTP transport - should use "httpUrl" field
		err = manager.Upsert(*cf, "test-server", "http://localhost:8080/mcp", types.TransportTypeStreamableHTTP.String())
		require.NoError(t, err)

		// Verify the config file uses "httpUrl" field (not "url")
		content, err := os.ReadFile(cf.Path)
		require.NoError(t, err)
		assert.Contains(t, string(content), `"httpUrl":`, "Streamable HTTP transport should use 'httpUrl' field")
		assert.NotContains(t, string(content), `"url":`, "Streamable HTTP transport should NOT use 'url' field")
	})

	t.Run("GeminiCli_UnknownTransport_FallsBackToDefaultUrlField", func(t *testing.T) {
		t.Parallel()
		tempHome := t.TempDir()

		// Create mock client config with limited URL label map (no entry for "unknown")
		mockClientConfigs := []clientAppConfig{
			{
				ClientType:                    GeminiCli,
				Description:                   "Google Gemini CLI (Mock)",
				RelPath:                       []string{"mock_gemini"},
				SettingsFile:                  "settings.json",
				MCPServersPathPrefix:          "/mcpServers",
				Extension:                     JSON,
				IsTransportTypeFieldSupported: false,
				MCPServersUrlLabelMap: map[types.TransportType]string{
					types.TransportTypeSSE: "url",
				},
			},
		}

		testConfig := &config.Config{
			Secrets: config.Secrets{ProviderType: "encrypted"},
			Clients: config.Clients{RegisteredClients: []string{string(GeminiCli)}},
		}
		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create config file
		configDir := filepath.Join(tempHome, "mock_gemini")
		require.NoError(t, os.MkdirAll(configDir, 0755))
		configPath := filepath.Join(configDir, "settings.json")
		require.NoError(t, os.WriteFile(configPath, []byte(`{"mcpServers": {}}`), 0644))

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)
		cf, err := manager.FindClientConfig(GeminiCli)
		require.NoError(t, err)

		// Upsert with unknown transport - should fall back to default "url" field
		err = manager.Upsert(*cf, "test-server", "http://localhost:8080/mcp", "unknown-transport")
		require.NoError(t, err)

		// Verify the config file uses "url" field (default fallback)
		content, err := os.ReadFile(cf.Path)
		require.NoError(t, err)
		assert.Contains(t, string(content), `"url":`, "Unknown transport should fall back to default url field")
	})

	t.Run("RegularClient_UsesConsistentUrlField", func(t *testing.T) {
		t.Parallel()
		tempHome := t.TempDir()

		// Create mock client config for Windsurf (uses serverUrl for all transport types)
		mockClientConfigs := []clientAppConfig{
			{
				ClientType:           Windsurf,
				Description:          "Windsurf IDE (Mock)",
				RelPath:              []string{"mock_windsurf"},
				SettingsFile:         "mcp_config.json",
				MCPServersPathPrefix: "/mcpServers",
				Extension:            JSON,
				SupportedTransportTypesMap: map[types.TransportType]string{
					types.TransportTypeStdio:          "http",
					types.TransportTypeSSE:            "sse",
					types.TransportTypeStreamableHTTP: "http",
				},
				IsTransportTypeFieldSupported: true,
				MCPServersUrlLabelMap: map[types.TransportType]string{
					types.TransportTypeStdio:          "serverUrl",
					types.TransportTypeSSE:            "serverUrl",
					types.TransportTypeStreamableHTTP: "serverUrl",
				},
			},
		}

		testConfig := &config.Config{
			Secrets: config.Secrets{ProviderType: "encrypted"},
			Clients: config.Clients{RegisteredClients: []string{string(Windsurf)}},
		}
		configProvider, cleanup := CreateTestConfigProvider(t, testConfig)
		defer cleanup()

		// Create config file
		configDir := filepath.Join(tempHome, "mock_windsurf")
		require.NoError(t, os.MkdirAll(configDir, 0755))
		configPath := filepath.Join(configDir, "mcp_config.json")
		require.NoError(t, os.WriteFile(configPath, []byte(`{"mcpServers": {}}`), 0644))

		manager := NewTestClientManager(tempHome, nil, mockClientConfigs, configProvider)
		cf, err := manager.FindClientConfig(Windsurf)
		require.NoError(t, err)

		// Upsert with SSE transport - should still use "serverUrl" field (fixed, not derived)
		err = manager.Upsert(*cf, "test-server", "http://localhost:8080/sse", types.TransportTypeSSE.String())
		require.NoError(t, err)

		// Verify the config file uses "serverUrl" field regardless of transport type
		content, err := os.ReadFile(cf.Path)
		require.NoError(t, err)
		assert.Contains(t, string(content), `"serverUrl":`, "Regular client should use fixed serverUrl field")
		assert.Contains(t, string(content), `"type":`, "Regular client with IsTransportTypeFieldSupported should have type field")
	})
}

func TestBuildMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		url           string
		transportType string
		clientCfg     *clientAppConfig
		expectUrl     string
		expectSrvUrl  string
		expectHttpUrl string
		expectType    string
	}{
		{
			name:          "url field without type",
			url:           "http://localhost:8080",
			transportType: types.TransportTypeSSE.String(),
			clientCfg: &clientAppConfig{
				IsTransportTypeFieldSupported: false,
				MCPServersUrlLabelMap: map[types.TransportType]string{
					types.TransportTypeSSE: "url",
				},
			},
			expectUrl:     "http://localhost:8080",
			expectSrvUrl:  "",
			expectHttpUrl: "",
			expectType:    "",
		},
		{
			name:          "serverUrl field without type",
			url:           "http://localhost:8080",
			transportType: types.TransportTypeSSE.String(),
			clientCfg: &clientAppConfig{
				IsTransportTypeFieldSupported: false,
				MCPServersUrlLabelMap: map[types.TransportType]string{
					types.TransportTypeSSE: "serverUrl",
				},
			},
			expectUrl:     "",
			expectSrvUrl:  "http://localhost:8080",
			expectHttpUrl: "",
			expectType:    "",
		},
		{
			name:          "httpUrl field without type",
			url:           "http://localhost:8080",
			transportType: types.TransportTypeStreamableHTTP.String(),
			clientCfg: &clientAppConfig{
				IsTransportTypeFieldSupported: false,
				MCPServersUrlLabelMap: map[types.TransportType]string{
					types.TransportTypeStreamableHTTP: "httpUrl",
				},
			},
			expectUrl:     "",
			expectSrvUrl:  "",
			expectHttpUrl: "http://localhost:8080",
			expectType:    "",
		},
		{
			name:          "url field with type support",
			url:           "http://localhost:8080",
			transportType: types.TransportTypeSSE.String(),
			clientCfg: &clientAppConfig{
				IsTransportTypeFieldSupported: true,
				MCPServersUrlLabelMap: map[types.TransportType]string{
					types.TransportTypeSSE: "url",
				},
				SupportedTransportTypesMap: map[types.TransportType]string{
					types.TransportTypeSSE: "sse",
				},
			},
			expectUrl:     "http://localhost:8080",
			expectSrvUrl:  "",
			expectHttpUrl: "",
			expectType:    "sse",
		},
		{
			name:          "MCPServersUrlLabelMap uses transport map for URL field",
			url:           "http://localhost:8080",
			transportType: types.TransportTypeStreamableHTTP.String(),
			clientCfg: &clientAppConfig{
				IsTransportTypeFieldSupported: false,
				MCPServersUrlLabelMap: map[types.TransportType]string{
					types.TransportTypeStreamableHTTP: "httpUrl",
				},
			},
			expectUrl:     "",
			expectSrvUrl:  "",
			expectHttpUrl: "http://localhost:8080",
			expectType:    "",
		},
		{
			name:          "Unknown transport falls back to default url field",
			url:           "http://localhost:8080",
			transportType: "unknown-transport",
			clientCfg: &clientAppConfig{
				IsTransportTypeFieldSupported: false,
				MCPServersUrlLabelMap: map[types.TransportType]string{
					types.TransportTypeSSE: "httpUrl",
				},
			},
			expectUrl:     "http://localhost:8080", // uses default "url" fallback
			expectSrvUrl:  "",
			expectHttpUrl: "",
			expectType:    "",
		},
		{
			name:          "MCPServersUrlLabelMap with SSE uses url field",
			url:           "http://localhost:8080",
			transportType: types.TransportTypeSSE.String(),
			clientCfg: &clientAppConfig{
				IsTransportTypeFieldSupported: false,
				MCPServersUrlLabelMap: map[types.TransportType]string{
					types.TransportTypeSSE:            "url",
					types.TransportTypeStreamableHTTP: "httpUrl",
				},
			},
			expectUrl:     "http://localhost:8080",
			expectSrvUrl:  "",
			expectHttpUrl: "",
			expectType:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := buildMCPServer(tt.url, tt.transportType, tt.clientCfg)
			assert.Equal(t, tt.expectUrl, server.Url, "Url field mismatch")
			assert.Equal(t, tt.expectSrvUrl, server.ServerUrl, "ServerUrl field mismatch")
			assert.Equal(t, tt.expectHttpUrl, server.HttpUrl, "HttpUrl field mismatch")
			assert.Equal(t, tt.expectType, server.Type, "Type field mismatch")
		})
	}
}

func TestGetAllClients(t *testing.T) {
	t.Parallel()

	clients := GetAllClients()

	// Should return all 25 supported clients
	assert.Len(t, clients, 25, "Expected 25 supported clients")

	// Verify the list is sorted alphabetically
	for i := 1; i < len(clients); i++ {
		assert.True(t, clients[i-1] < clients[i],
			"Clients should be sorted alphabetically, but %s comes after %s",
			clients[i-1], clients[i])
	}

	// Verify some known clients are in the list
	expectedClients := []ClientApp{
		RooCode, Cline, Cursor, VSCode, VSCodeInsider, ClaudeCode,
		Windsurf, WindsurfJetBrains, AmpCli, LMStudio, Goose,
		Continue, Zed, Codex, MistralVibe,
	}

	clientMap := make(map[ClientApp]bool)
	for _, client := range clients {
		clientMap[client] = true
	}

	for _, expected := range expectedClients {
		assert.True(t, clientMap[expected], "Expected client %s to be in the list", expected)
	}
}

func TestIsValidClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		client   string
		expected bool
	}{
		{
			name:     "Valid client - vscode",
			client:   "vscode",
			expected: true,
		},
		{
			name:     "Valid client - claude-code",
			client:   "claude-code",
			expected: true,
		},
		{
			name:     "Valid client - cursor",
			client:   "cursor",
			expected: true,
		},
		{
			name:     "Valid client - codex",
			client:   "codex",
			expected: true,
		},
		{
			name:     "Invalid client - unknown",
			client:   "unknown",
			expected: false,
		},
		{
			name:     "Invalid client - empty",
			client:   "",
			expected: false,
		},
		{
			name:     "Invalid client - invalid-name",
			client:   "invalid-client",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := IsValidClient(tt.client)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetClientDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		client      ClientApp
		expectFound bool
	}{
		{
			name:        "VSCode description",
			client:      VSCode,
			expectFound: true,
		},
		{
			name:        "ClaudeCode description",
			client:      ClaudeCode,
			expectFound: true,
		},
		{
			name:        "Cursor description",
			client:      Cursor,
			expectFound: true,
		},
		{
			name:        "Invalid client",
			client:      ClientApp("invalid"),
			expectFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			description := GetClientDescription(tt.client)
			if tt.expectFound {
				assert.NotEmpty(t, description, "Expected non-empty description for %s", tt.client)
			} else {
				assert.Empty(t, description, "Expected empty description for invalid client")
			}
		})
	}
}

func TestGetClientListFormatted(t *testing.T) {
	t.Parallel()

	formatted := GetClientListFormatted()

	// Should not be empty
	assert.NotEmpty(t, formatted)

	// Should contain all expected clients with descriptions
	assert.Contains(t, formatted, "vscode:")
	assert.Contains(t, formatted, "claude-code:")
	assert.Contains(t, formatted, "cursor:")
	assert.Contains(t, formatted, "codex:")

	// Should be formatted with bullet points and newlines
	assert.Contains(t, formatted, "  -")
	lines := strings.Split(formatted, "\n")
	assert.Greater(t, len(lines), 20, "Expected more than 20 lines in formatted list")

	// Verify the list is sorted alphabetically
	// Extract client names from each line (format: "  - clientname: description")
	var clientNames []string
	for _, line := range lines {
		if strings.HasPrefix(line, "  -") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				clientName := strings.TrimPrefix(strings.TrimSpace(parts[0]), "- ")
				clientNames = append(clientNames, clientName)
			}
		}
	}
	for i := 1; i < len(clientNames); i++ {
		assert.True(t, clientNames[i-1] < clientNames[i],
			"Clients should be sorted alphabetically, but %s comes after %s",
			clientNames[i-1], clientNames[i])
	}
}

func TestGetClientListCSV(t *testing.T) {
	t.Parallel()

	csv := GetClientListCSV()

	// Should not be empty
	assert.NotEmpty(t, csv)

	// Should contain all expected clients
	assert.Contains(t, csv, "vscode")
	assert.Contains(t, csv, "claude-code")
	assert.Contains(t, csv, "cursor")
	assert.Contains(t, csv, "codex")

	// Should be comma-separated
	assert.Contains(t, csv, ", ")

	// Verify the list is sorted alphabetically
	clientNames := strings.Split(csv, ", ")
	for i := 1; i < len(clientNames); i++ {
		assert.True(t, clientNames[i-1] < clientNames[i],
			"Clients should be sorted alphabetically, but %s comes after %s",
			clientNames[i-1], clientNames[i])
	}

	// Count the number of clients (should be 25)
	clients := strings.Split(csv, ", ")
	assert.Len(t, clients, 25, "Expected 25 clients in CSV list")
}

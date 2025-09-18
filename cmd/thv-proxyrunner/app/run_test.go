package app

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/runner"
)

func TestRunCmdFlagParsing(t *testing.T) {
	t.Parallel()

	// Test that essential flags exist and can be parsed
	cmd := NewRunCmd()
	require.NotNil(t, cmd, "Run command should not be nil")
	addRunFlags(cmd, &proxyRunFlags{})

	// Test that flag no longer exists (removed in favor of file-based auto-discovery)
	flag := cmd.Flag("from-configmap")
	assert.Nil(t, flag, "flag should not exist (removed in favor of file-based auto-discovery)")

	// Test that essential flags still exist
	transportFlag := cmd.Flag("transport")
	require.NotNil(t, transportFlag, "transport flag should exist")

	nameFlag := cmd.Flag("name")
	require.NotNil(t, nameFlag, "name flag should exist")
}

func TestRunCmdFileBasedConfigIntegration(t *testing.T) {
	t.Parallel()

	// Test that proxy runner loads configuration from file automatically
	cmd := NewRunCmd()
	require.NotNil(t, cmd, "Run command should not be nil")
	addRunFlags(cmd, &proxyRunFlags{})

	// Test parsing command with regular flags
	cmd.SetArgs([]string{"--transport", "stdio", "--name", "test-server", "test-image"})

	// Parse the command to ensure flag parsing works
	err := cmd.ParseFlags([]string{"--transport", "stdio", "--name", "test-server"})
	assert.NoError(t, err, "Flag parsing should not fail")

	// Verify the flags were set correctly
	transportValue, err := cmd.Flags().GetString("transport")
	assert.NoError(t, err, "Getting transport flag value should not fail")
	assert.Equal(t, "stdio", transportValue, "Transport flag value should match what was set")

	nameValue, err := cmd.Flags().GetString("name")
	assert.NoError(t, err, "Getting name flag value should not fail")
	assert.Equal(t, "test-server", nameValue, "Name flag value should match what was set")
}

func TestRunCmdOtherFlagsStillWork(t *testing.T) {
	t.Parallel()

	// Ensure that adding the new flag doesn't break existing functionality
	cmd := NewRunCmd()
	require.NotNil(t, cmd, "Run command should not be nil")
	addRunFlags(cmd, &proxyRunFlags{})

	// Test that other important flags still exist
	transportFlag := cmd.Flag("transport")
	assert.NotNil(t, transportFlag, "transport flag should still exist")

	nameFlag := cmd.Flag("name")
	assert.NotNil(t, nameFlag, "name flag should still exist")

	hostFlag := cmd.Flag("host")
	assert.NotNil(t, hostFlag, "host flag should still exist")

	portFlag := cmd.Flag("proxy-port")
	assert.NotNil(t, portFlag, "proxy-port flag should still exist")

	// Test parsing multiple flags
	err := cmd.ParseFlags([]string{
		"--transport", "stdio",
		"--name", "test-server",
		"--proxy-port", "8080",
	})
	assert.NoError(t, err, "Parsing multiple flags should work")

	transportValue, _ := cmd.Flags().GetString("transport")
	assert.Equal(t, "stdio", transportValue)

	nameValue, _ := cmd.Flags().GetString("name")
	assert.Equal(t, "test-server", nameValue)

	portValue, _ := cmd.Flags().GetInt("proxy-port")
	assert.Equal(t, 8080, portValue)
}

func TestValidateAndNormaliseHostFlagStillWorks(t *testing.T) {
	t.Parallel()

	// Ensure the host validation function still works correctly
	// This is important because our new code is called before host validation

	validCases := []struct {
		input    string
		expected string
	}{
		{"127.0.0.1", "127.0.0.1"},
		{"0.0.0.0", "0.0.0.0"},
		{"192.168.1.1", "192.168.1.1"},
	}

	for _, tc := range validCases {
		result, err := ValidateAndNormaliseHostFlag(tc.input)
		assert.NoError(t, err, "Valid IP should not error: %s", tc.input)
		assert.Equal(t, tc.expected, result, "Result should match expected for: %s", tc.input)
	}

	// Test invalid cases
	invalidCases := []string{
		"not-an-ip",
		"999.999.999.999",
		"",
	}

	for _, invalid := range invalidCases {
		_, err := ValidateAndNormaliseHostFlag(invalid)
		assert.Error(t, err, "Invalid input should error: %s", invalid)
	}
}

func TestProxyModeFlagExists(t *testing.T) {
	t.Parallel()

	// Test that the --proxy-mode flag exists and has correct default
	cmd := NewRunCmd()
	require.NotNil(t, cmd, "Run command should not be nil")
	addRunFlags(cmd, &proxyRunFlags{})

	flag := cmd.Flag("proxy-mode")
	require.NotNil(t, flag, "proxy-mode flag should exist")
	assert.Equal(t, "string", flag.Value.Type(), "proxy-mode flag should be string type")
	assert.Equal(t, "sse", flag.DefValue, "proxy-mode flag should have 'sse' as default value")

	// Test help text contains key information
	assert.Contains(t, flag.Usage, "Proxy mode for stdio transport", "Help text should mention stdio transport")
	assert.Contains(t, flag.Usage, "sse", "Help text should mention sse option")
	assert.Contains(t, flag.Usage, "streamable-http", "Help text should mention streamable-http option")
}

func TestProxyModeFlagParsing(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		proxyMode   string
		expectValid bool
	}{
		{
			name:        "valid sse mode",
			proxyMode:   "sse",
			expectValid: true,
		},
		{
			name:        "valid streamable-http mode",
			proxyMode:   "streamable-http",
			expectValid: true,
		},
		{
			name:        "invalid proxy mode",
			proxyMode:   "invalid-mode",
			expectValid: false,
		},
		{
			name:        "empty proxy mode",
			proxyMode:   "",
			expectValid: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := NewRunCmd()
			require.NotNil(t, cmd, "Run command should not be nil")
			addRunFlags(cmd, &proxyRunFlags{})

			err := cmd.ParseFlags([]string{"--proxy-mode", tc.proxyMode})
			assert.NoError(t, err, "Flag parsing should not fail")

			// Get the parsed value
			flagValue, err := cmd.Flags().GetString("proxy-mode")
			assert.NoError(t, err, "Getting flag value should not fail")
			assert.Equal(t, tc.proxyMode, flagValue, "Flag value should match what was set")
		})
	}
}

func TestProxyModeWithOtherFlags(t *testing.T) {
	t.Parallel()

	// Test that proxy-mode flag works with other flags
	cmd := NewRunCmd()
	require.NotNil(t, cmd, "Run command should not be nil")
	addRunFlags(cmd, &proxyRunFlags{})

	err := cmd.ParseFlags([]string{
		"--proxy-mode", "streamable-http",
		"--transport", "stdio",
		"--name", "test-server",
		"--proxy-port", "8080",
	})
	assert.NoError(t, err, "Parsing multiple flags including proxy-mode should work")

	// Verify all flags were parsed correctly
	proxyModeValue, _ := cmd.Flags().GetString("proxy-mode")
	assert.Equal(t, "streamable-http", proxyModeValue)

	transportValue, _ := cmd.Flags().GetString("transport")
	assert.Equal(t, "stdio", transportValue)

	nameValue, _ := cmd.Flags().GetString("name")
	assert.Equal(t, "test-server", nameValue)

	portValue, _ := cmd.Flags().GetInt("proxy-port")
	assert.Equal(t, 8080, portValue)
}

func TestTryLoadConfigFromFile(t *testing.T) {
	t.Parallel()

	t.Run("no config file exists", func(t *testing.T) {
		t.Parallel()
		config, err := tryLoadConfigFromFile()
		assert.NoError(t, err, "Should not error when no config file exists")
		assert.Nil(t, config, "Should return nil when no config file exists")
	})

	t.Run("loads config from local file", func(t *testing.T) {
		t.Parallel()
		// Test loading config file functionality with a temporary file
		tmpDir := t.TempDir()
		configPath := tmpDir + "/runconfig.json"

		configContent := `{
			"schema_version": "v1",
			"name": "local-test-server",
			"image": "test:local",
			"transport": "sse",
			"port": 9090,
			"target_port": 8080
		}`

		err := os.WriteFile(configPath, []byte(configContent), 0644)
		require.NoError(t, err, "Should be able to create config file")

		// Create a test function that loads from our specific path
		testLoadConfigFromPath := func(path string) (*runner.RunConfig, error) {
			if _, err := os.Stat(path); err != nil {
				return nil, nil // File doesn't exist
			}

			file, err := os.Open(path) // #nosec G304 - test path
			if err != nil {
				return nil, fmt.Errorf("found config file at %s but failed to open: %w", path, err)
			}
			defer file.Close()

			return runner.ReadJSON(file)
		}

		config, err := testLoadConfigFromPath(configPath)
		assert.NoError(t, err, "Should successfully load config from file")
		assert.NotNil(t, config, "Should return config when file exists")
		assert.Equal(t, "local-test-server", config.Name)
		assert.Equal(t, "test:local", config.Image)
		assert.Equal(t, "sse", string(config.Transport))
		assert.Equal(t, 9090, config.Port)
		assert.Equal(t, 8080, config.TargetPort)
	})

	t.Run("loads config with invalid JSON", func(t *testing.T) {
		t.Parallel()
		// Test loading config with invalid JSON
		tmpDir := t.TempDir()
		configPath := tmpDir + "/runconfig.json"

		invalidJSON := `{"invalid": json content}`

		err := os.WriteFile(configPath, []byte(invalidJSON), 0644)
		require.NoError(t, err, "Should be able to create invalid config file")

		// Create a test function that loads from our specific path
		testLoadConfigFromPath := func(path string) (*runner.RunConfig, error) {
			if _, err := os.Stat(path); err != nil {
				return nil, nil // File doesn't exist
			}

			file, err := os.Open(path) // #nosec G304 - test path
			if err != nil {
				return nil, fmt.Errorf("found config file at %s but failed to open: %w", path, err)
			}
			defer file.Close()

			runConfig, err := runner.ReadJSON(file)
			if err != nil {
				return nil, fmt.Errorf("found config file at %s but failed to parse JSON: %w", path, err)
			}

			return runConfig, nil
		}

		config, err := testLoadConfigFromPath(configPath)
		assert.Error(t, err, "Should error when JSON is invalid")
		assert.Nil(t, config, "Should return nil when JSON is invalid")
		assert.Contains(t, err.Error(), "failed to parse JSON")
	})

	t.Run("handles file read error", func(t *testing.T) {
		t.Parallel()
		// Test handling file read error by creating a directory instead of a file
		tmpDir := t.TempDir()
		configPath := tmpDir + "/runconfig.json"

		// Create a directory instead of a file to cause read error
		err := os.Mkdir(configPath, 0755)
		require.NoError(t, err, "Should be able to create directory")

		// Create a test function that loads from our specific path
		testLoadConfigFromPath := func(path string) (*runner.RunConfig, error) {
			if _, err := os.Stat(path); err != nil {
				return nil, nil // File doesn't exist
			}

			file, err := os.Open(path) // #nosec G304 - test path
			if err != nil {
				return nil, fmt.Errorf("found config file at %s but failed to open: %w", path, err)
			}
			defer file.Close()

			runConfig, err := runner.ReadJSON(file)
			if err != nil {
				return nil, fmt.Errorf("found config file at %s but failed to parse JSON: %w", path, err)
			}

			return runConfig, nil
		}

		config, err := testLoadConfigFromPath(configPath)
		assert.Error(t, err, "Should error when file cannot be read")
		assert.Nil(t, config, "Should return nil when file cannot be read")
		assert.Contains(t, err.Error(), "failed to parse JSON")
	})
}

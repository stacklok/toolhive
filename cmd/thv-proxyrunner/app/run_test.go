package app

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestCommand creates a minimal test command for flag parsing tests
func createTestCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "run [flags] IMAGE",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			return nil // Test stub
		},
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	}

	// Add just the essential flags for testing - our dynamic validation works with any flags
	cmd.Flags().String("from-configmap", "", "ConfigMap reference")
	cmd.Flags().String("transport", "", "Transport mode") // Test conflicting flag
	cmd.Flags().String("name", "", "Server name")         // Test conflicting flag
	cmd.Flags().String("proxy-mode", "sse", "Proxy mode") // Test proxy mode flag
	cmd.Flags().Bool("debug", false, "Debug mode")        // Test safe flag

	return cmd
}

func TestRunCmdFlagParsing(t *testing.T) {
	t.Parallel()

	// Test that the --from-configmap flag exists and can be parsed
	cmd := NewRunCmd()
	require.NotNil(t, cmd, "Run command should not be nil")
	addRunFlags(cmd, &proxyRunFlags{})

	// Test flag existence
	flag := cmd.Flag("from-configmap")
	require.NotNil(t, flag, "from-configmap flag should exist")
	assert.Equal(t, "string", flag.Value.Type(), "from-configmap flag should be string type")
	assert.Equal(t, "", flag.DefValue, "from-configmap flag should have empty default value")

	// Test help text contains key information
	assert.Contains(t, flag.Usage, "Experimental", "Help text should mention experimental")
	assert.Contains(t, flag.Usage, "ConfigMap", "Help text should mention ConfigMap")
	assert.Contains(t, flag.Usage, "namespace/configmap-name", "Help text should mention format")
	assert.Contains(t, flag.Usage, "mutually exclusive", "Help text should mention mutual exclusivity")
}

func TestRunCmdConfigMapFlagIntegration(t *testing.T) {
	t.Parallel()

	// Test that setting the flag doesn't break command creation
	cmd := NewRunCmd()
	require.NotNil(t, cmd, "Run command should not be nil")
	addRunFlags(cmd, &proxyRunFlags{})

	// Test parsing command with flag set
	cmd.SetArgs([]string{"--from-configmap", "test-ns/test-cm", "test-image"})

	// Parse the command to ensure flag parsing works
	err := cmd.ParseFlags([]string{"--from-configmap", "test-ns/test-cm"})
	assert.NoError(t, err, "Flag parsing should not fail")

	// Verify the flag was set
	flagValue, err := cmd.Flags().GetString("from-configmap")
	assert.NoError(t, err, "Getting flag value should not fail")
	assert.Equal(t, "test-ns/test-cm", flagValue, "Flag value should match what was set")
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

	// Test parsing multiple flags including the new one
	err := cmd.ParseFlags([]string{
		"--from-configmap", "test-ns/test-cm",
		"--transport", "stdio",
		"--name", "test-server",
		"--proxy-port", "8080",
	})
	assert.NoError(t, err, "Parsing multiple flags should work")

	// Verify all flags were parsed correctly
	configMapValue, _ := cmd.Flags().GetString("from-configmap")
	assert.Equal(t, "test-ns/test-cm", configMapValue)

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

func TestValidateConfigMapOnlyMode(t *testing.T) {
	t.Parallel()

	// Test cases for flag conflict validation
	testCases := []struct {
		name              string
		args              []string
		expectError       bool
		expectedErrorText string
	}{
		{
			name:        "no conflicting flags - should pass",
			args:        []string{"--from-configmap", "test-ns/test-cm", "test-image"},
			expectError: false,
		},
		{
			name:              "transport flag conflicts",
			args:              []string{"--from-configmap", "test-ns/test-cm", "--transport", "stdio", "test-image"},
			expectError:       true,
			expectedErrorText: "cannot use --from-configmap with the following flags",
		},
		{
			name:              "name flag conflicts",
			args:              []string{"--from-configmap", "test-ns/test-cm", "--name", "my-server", "test-image"},
			expectError:       true,
			expectedErrorText: "cannot use --from-configmap with the following flags",
		},
		{
			name:              "multiple conflicting flags",
			args:              []string{"--from-configmap", "test-ns/test-cm", "--transport", "stdio", "--name", "my-server", "test-image"},
			expectError:       true,
			expectedErrorText: "cannot use --from-configmap with the following flags",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a test command instance to avoid interference from global state
			cmd := createTestCommand()
			cmd.SetArgs(tc.args)

			// Parse the command to set flag.Changed properly
			err := cmd.ParseFlags(tc.args[:len(tc.args)-1]) // exclude the image arg
			require.NoError(t, err, "Flag parsing should not fail")

			// Test the validation function
			err = validateConfigMapOnlyMode(cmd)

			if tc.expectError {
				assert.Error(t, err, "Expected error for case: %s", tc.name)
				assert.Contains(t, err.Error(), tc.expectedErrorText, "Error should contain expected text")
			} else {
				assert.NoError(t, err, "Expected no error for case: %s", tc.name)
			}
		})
	}
}

func TestValidateConfigMapOnlyModeWithSafeFlags(t *testing.T) {
	t.Parallel()

	// Test that safe flags work with --from-configmap
	cmd := createTestCommand()
	cmd.SetArgs([]string{"--from-configmap", "test-ns/test-cm", "--debug", "test-image"})

	err := cmd.ParseFlags([]string{"--from-configmap", "test-ns/test-cm", "--debug"})
	require.NoError(t, err, "Flag parsing should not fail")

	// Test the validation function - should not error with safe flags
	err = validateConfigMapOnlyMode(cmd)
	assert.NoError(t, err, "Expected no error for safe flags like --debug")
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

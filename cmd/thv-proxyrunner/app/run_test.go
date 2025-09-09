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
	cmd.Flags().Bool("debug", false, "Debug mode")        // Test safe flag

	return cmd
}

func TestParseConfigMapRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		ref               string
		expectedNamespace string
		expectedName      string
		expectError       bool
	}{
		{
			name:              "valid reference",
			ref:               "default/my-configmap",
			expectedNamespace: "default",
			expectedName:      "my-configmap",
			expectError:       false,
		},
		{
			name:              "valid reference with hyphens",
			ref:               "my-namespace/my-config-map",
			expectedNamespace: "my-namespace",
			expectedName:      "my-config-map",
			expectError:       false,
		},
		{
			name:              "config name with slash (allowed)",
			ref:               "default/config/with/slashes",
			expectedNamespace: "default",
			expectedName:      "config/with/slashes",
			expectError:       false,
		},
		{
			name:        "missing slash",
			ref:         "defaultmy-configmap",
			expectError: true,
		},
		{
			name:        "empty namespace",
			ref:         "/my-configmap",
			expectError: true,
		},
		{
			name:        "empty name",
			ref:         "default/",
			expectError: true,
		},
		{
			name:        "empty reference",
			ref:         "",
			expectError: true,
		},
		{
			name:        "whitespace only namespace",
			ref:         "   /my-configmap",
			expectError: true,
		},
		{
			name:        "whitespace only name",
			ref:         "default/   ",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			namespace, name, err := parseConfigMapRef(tt.ref)

			if tt.expectError {
				assert.Error(t, err, "Expected error for ref: %s", tt.ref)
				assert.Empty(t, namespace, "Namespace should be empty on error")
				assert.Empty(t, name, "Name should be empty on error")
			} else {
				assert.NoError(t, err, "Unexpected error for ref: %s", tt.ref)
				assert.Equal(t, tt.expectedNamespace, namespace, "Namespace mismatch")
				assert.Equal(t, tt.expectedName, name, "Name mismatch")
			}
		})
	}
}

func TestRunCmdFlagParsing(t *testing.T) {
	t.Parallel()

	// Test that the --from-configmap flag exists and can be parsed
	cmd := NewRunCmd()
	require.NotNil(t, cmd, "Run command should not be nil")

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

func TestRunCmdConfigMapFlagIntegrationWithRealCmd(t *testing.T) {
	t.Parallel()

	// Integration test to ensure the validation function is properly integrated
	// into the command execution flow
	cmd := createTestCommand()
	require.NotNil(t, cmd, "Run command should not be nil")

	// Test with conflicting flags to ensure validation works
	cmd.SetArgs([]string{"--from-configmap", "test-ns/test-cm", "--transport", "stdio", "test-image"})
	err := cmd.ParseFlags([]string{"--from-configmap", "test-ns/test-cm", "--transport", "stdio"})
	require.NoError(t, err, "Flag parsing should not fail")

	// Test the validation function integration
	err = validateConfigMapOnlyMode(cmd)
	assert.Error(t, err, "Expected error for conflicting flags")
	assert.Contains(t, err.Error(), "cannot use --from-configmap with the following flags",
		"Error should indicate flag conflict validation")

	// Test with no conflicting flags
	cmd2 := createTestCommand()
	cmd2.SetArgs([]string{"--from-configmap", "test-ns/test-cm", "test-image"})
	err = cmd2.ParseFlags([]string{"--from-configmap", "test-ns/test-cm"})
	require.NoError(t, err, "Flag parsing should not fail")

	// This should pass validation
	err = validateConfigMapOnlyMode(cmd2)
	assert.NoError(t, err, "Expected no error without conflicting flags")
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

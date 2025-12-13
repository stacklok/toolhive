package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authz"
	runtimemocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	regtypes "github.com/stacklok/toolhive/pkg/registry/registry"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	localhostStr = "localhost"
)

func TestNewRunConfig(t *testing.T) {
	t.Parallel()
	config := NewRunConfig()
	assert.NotNil(t, config, "NewRunConfig should return a non-nil config")
	assert.NotNil(t, config.ContainerLabels, "ContainerLabels should be initialized")
	assert.NotNil(t, config.EnvVars, "EnvVars should be initialized")
}

func TestRunConfig_WithTransport(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		transport   string
		expectError bool
		expected    types.TransportType
	}{
		{
			name:        "Valid SSE transport",
			transport:   "sse",
			expectError: false,
			expected:    types.TransportTypeSSE,
		},
		{
			name:        "Valid stdio transport",
			transport:   "stdio",
			expectError: false,
			expected:    types.TransportTypeStdio,
		},
		{
			name:        "Valid streamable-http transport",
			transport:   "streamable-http",
			expectError: false,
			expected:    types.TransportTypeStreamableHTTP,
		},
		{
			name:        "Invalid transport",
			transport:   "invalid",
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := NewRunConfig()

			result, err := config.WithTransport(tc.transport)

			if tc.expectError {
				assert.Error(t, err, "WithTransport should return an error for invalid transport")
				assert.Contains(t, err.Error(), "invalid transport mode: invalid. Valid modes are: sse, streamable-http, stdio", "Error message should match expected format")
			} else {
				assert.NoError(t, err, "WithTransport should not return an error for valid transport")
				assert.Equal(t, config, result, "WithTransport should return the same config instance")
				assert.Equal(t, tc.expected, config.Transport, "Transport should be set correctly")
			}
		})
	}
}

// TestRunConfig_WithPorts tests the WithPorts method
// Note: This test uses actual port finding logic, so it may fail if ports are in use
func TestRunConfig_WithPorts(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		config      *RunConfig
		port        int
		targetPort  int
		expectError bool
	}{
		{
			name:        "SSE transport with specific ports",
			config:      &RunConfig{Transport: types.TransportTypeSSE},
			port:        8001,
			targetPort:  9001,
			expectError: false,
		},
		{
			name:        "SSE transport with auto-selected ports",
			config:      &RunConfig{Transport: types.TransportTypeSSE},
			port:        0,
			targetPort:  0,
			expectError: false,
		},
		{
			name:        "Streamable HTTP transport with specific ports",
			config:      &RunConfig{Transport: types.TransportTypeStreamableHTTP},
			port:        8002,
			targetPort:  9002,
			expectError: false,
		},
		{
			name:        "Stdio transport with specific port",
			config:      &RunConfig{Transport: types.TransportTypeStdio},
			port:        8003,
			targetPort:  9003, // This should be ignored for stdio
			expectError: false,
		},
	}

	logger.Initialize()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := tc.config.WithPorts(tc.port, tc.targetPort)

			if tc.expectError {
				assert.Error(t, err, "WithPorts should return an error")
			} else {
				assert.NoError(t, err, "WithPorts should not return an error")
				assert.Equal(t, tc.config, result, "WithPorts should return the same config instance")

				if tc.port == 0 {
					assert.Greater(t, tc.config.Port, 0, "Proxy Port should be auto-selected")
				} else {
					assert.Equal(t, tc.port, tc.config.Port, "Proxy Port should be set correctly")
				}

				if tc.config.Transport == types.TransportTypeSSE || tc.config.Transport == types.TransportTypeStreamableHTTP {
					if tc.targetPort == 0 {
						assert.Greater(t, tc.config.TargetPort, 0, "TargetPort should be auto-selected")
					} else {
						assert.Equal(t, tc.targetPort, tc.config.TargetPort, "TargetPort should be set correctly")
					}
				} else {
					assert.Zero(t, tc.config.TargetPort, "TargetPort should not be set for stdio")
				}
			}
		})
	}
}

func TestRunConfig_WithEnvironmentVariables(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		config      *RunConfig
		envVars     map[string]string
		expectError bool
		expected    map[string]string
	}{
		{
			name:        "Empty environment variables",
			config:      &RunConfig{Transport: types.TransportTypeSSE, TargetPort: 9000},
			envVars:     map[string]string{},
			expectError: false,
			expected: map[string]string{
				"MCP_TRANSPORT": "sse",
				"MCP_PORT":      "9000",
			},
		},
		{
			name:        "Valid environment variables",
			config:      &RunConfig{Transport: types.TransportTypeSSE, TargetPort: 9000},
			envVars:     map[string]string{"KEY1": "value1", "KEY2": "value2"},
			expectError: false,
			expected: map[string]string{
				"KEY1":          "value1",
				"KEY2":          "value2",
				"MCP_TRANSPORT": "sse",
				"MCP_PORT":      "9000",
			},
		},
		{
			name: "Preserve existing environment variables",
			config: &RunConfig{
				Transport:  types.TransportTypeSSE,
				TargetPort: 9000,
				EnvVars: map[string]string{
					"EXISTING_VAR": "existing_value",
				},
			},
			envVars:     map[string]string{"KEY1": "value1"},
			expectError: false,
			expected: map[string]string{
				"EXISTING_VAR":  "existing_value",
				"KEY1":          "value1",
				"MCP_TRANSPORT": "sse",
				"MCP_PORT":      "9000",
			},
		},
		{
			name: "Override existing environment variables",
			config: &RunConfig{
				Transport:  types.TransportTypeSSE,
				TargetPort: 9000,
				EnvVars: map[string]string{
					"KEY1": "original_value",
				},
			},
			envVars:     map[string]string{"KEY1": "new_value"},
			expectError: false,
			expected: map[string]string{
				"KEY1":          "new_value",
				"MCP_TRANSPORT": "sse",
				"MCP_PORT":      "9000",
			},
		},
		{
			name:        "Stdio transport",
			config:      &RunConfig{Transport: types.TransportTypeStdio},
			envVars:     map[string]string{},
			expectError: false,
			expected: map[string]string{
				"MCP_TRANSPORT": "stdio",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := tc.config.WithEnvironmentVariables(tc.envVars)

			if tc.expectError {
				assert.Error(t, err, "WithEnvironmentVariables should return an error")
			} else {
				assert.NoError(t, err, "WithEnvironmentVariables should not return an error")
				assert.Equal(t, tc.config, result, "WithEnvironmentVariables should return the same config instance")

				// Check that all expected environment variables are set
				for key, value := range tc.expected {
					assert.Equal(t, value, tc.config.EnvVars[key], "Environment variable %s should be set correctly", key)
				}
			}
		})
	}
}

func TestRunConfig_WithSecrets(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		config      *RunConfig
		secrets     []string
		mockSecrets map[string]string
		expectError bool
		expected    map[string]string
	}{
		{
			name:        "No secrets",
			config:      &RunConfig{EnvVars: map[string]string{}},
			secrets:     []string{},
			mockSecrets: map[string]string{},
			expectError: false,
			expected:    map[string]string{},
		},
		{
			name:   "Valid secrets",
			config: &RunConfig{EnvVars: map[string]string{}},
			secrets: []string{
				"secret1,target=ENV_VAR1",
				"secret2,target=ENV_VAR2",
			},
			mockSecrets: map[string]string{
				"secret1": "value1",
				"secret2": "value2",
			},
			expectError: false,
			expected: map[string]string{
				"ENV_VAR1": "value1",
				"ENV_VAR2": "value2",
			},
		},
		{
			name: "Preserve existing environment variables",
			config: &RunConfig{EnvVars: map[string]string{
				"EXISTING_VAR": "existing_value",
			}},
			secrets: []string{
				"secret1,target=ENV_VAR1",
			},
			mockSecrets: map[string]string{
				"secret1": "value1",
			},
			expectError: false,
			expected: map[string]string{
				"EXISTING_VAR": "existing_value",
				"ENV_VAR1":     "value1",
			},
		},
		{
			name: "Secret overrides existing environment variable",
			config: &RunConfig{EnvVars: map[string]string{
				"ENV_VAR1": "original_value",
			}},
			secrets: []string{
				"secret1,target=ENV_VAR1",
			},
			mockSecrets: map[string]string{
				"secret1": "new_value",
			},
			expectError: false,
			expected: map[string]string{
				"ENV_VAR1": "new_value",
			},
		},
		{
			name:   "Invalid secret format",
			config: &RunConfig{EnvVars: map[string]string{}},
			secrets: []string{
				"invalid-format",
			},
			mockSecrets: map[string]string{},
			expectError: true,
		},
		{
			name:   "Secret not found",
			config: &RunConfig{EnvVars: map[string]string{}},
			secrets: []string{
				"nonexistent,target=ENV_VAR",
			},
			mockSecrets: map[string]string{},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Create a mock secret manager
			secretManager := secretsmocks.NewMockProvider(ctrl)

			// Set up mock expectations
			if len(tc.mockSecrets) == 0 {
				secretManager.EXPECT().GetSecret(gomock.Any(), "nonexistent").Return("", fmt.Errorf("secret nonexistent not found")).AnyTimes()
			} else {
				for secretName, secretValue := range tc.mockSecrets {
					secretManager.EXPECT().GetSecret(gomock.Any(), secretName).Return(secretValue, nil).AnyTimes()
				}
			}

			// Set the secrets in the config
			tc.config.Secrets = tc.secrets

			// Call the function
			result, err := tc.config.WithSecrets(context.Background(), secretManager)

			if tc.expectError {
				assert.Error(t, err, "WithSecrets should return an error")
			} else {
				assert.NoError(t, err, "WithSecrets should not return an error")
				assert.Equal(t, tc.config, result, "WithSecrets should return the same config instance")

				// Check that all expected environment variables are set
				for key, value := range tc.expected {
					assert.Equal(t, value, tc.config.EnvVars[key], "Environment variable %s should be set correctly", key)
				}
			}
		})
	}
}

func TestRunConfig_WithContainerName(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name           string
		config         *RunConfig
		expectedChange bool
	}{
		{
			name: "Container name already set",
			config: &RunConfig{
				ContainerName: "existing-container",
				Image:         "test-image",
			},
			expectedChange: false,
		},
		{
			name: "Container name not set, image set",
			config: &RunConfig{
				ContainerName: "",
				Image:         "test-image",
				Name:          "test-server",
			},
			expectedChange: true,
		},
		{
			name: "Container name and image not set",
			config: &RunConfig{
				ContainerName: "",
				Image:         "",
			},
			expectedChange: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			originalContainerName := tc.config.ContainerName

			result, _ := tc.config.WithContainerName()

			assert.Equal(t, tc.config, result, "WithContainerName should return the same config instance")

			if tc.expectedChange {
				assert.NotEqual(t, originalContainerName, tc.config.ContainerName, "ContainerName should be changed")
				assert.NotEmpty(t, tc.config.ContainerName, "ContainerName should not be empty")
				assert.Contains(t, tc.config.ContainerName, tc.config.Name, "ContainerName should contain the server name")
			} else {
				assert.Equal(t, originalContainerName, tc.config.ContainerName, "ContainerName should not be changed")
			}
		})
	}
}

func TestRunConfig_WithStandardLabels(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		config   *RunConfig
		expected map[string]string
	}{
		{
			name: "Basic configuration",
			config: &RunConfig{
				Name:            "test-server",
				Image:           "test-image",
				Transport:       types.TransportTypeSSE,
				Port:            60000,
				ContainerLabels: map[string]string{},
			},
			expected: map[string]string{
				"toolhive":           "true",
				"toolhive-name":      "test-server",
				"toolhive-transport": "sse",
				"toolhive-port":      "60000",
			},
		},
		{
			name: "With existing labels",
			config: &RunConfig{
				Name:      "test-server",
				Image:     "test-image",
				Transport: types.TransportTypeStdio,
				ContainerLabels: map[string]string{
					"existing-label": "existing-value",
				},
			},
			expected: map[string]string{
				"toolhive":           "true",
				"toolhive-name":      "test-server",
				"toolhive-transport": "stdio",
				"existing-label":     "existing-value",
			},
		},
		{
			name: "Stdio transport with SSE proxy mode",
			config: &RunConfig{
				Name:            "test-server",
				Image:           "test-image",
				Transport:       types.TransportTypeStdio,
				ProxyMode:       types.ProxyModeSSE,
				Port:            60000,
				ContainerLabels: map[string]string{},
			},
			expected: map[string]string{
				"toolhive":           "true",
				"toolhive-name":      "test-server",
				"toolhive-transport": "sse", // Should be "sse" not "stdio"
				"toolhive-port":      "60000",
			},
		},
		{
			name: "Stdio transport with streamable-http proxy mode",
			config: &RunConfig{
				Name:            "test-server",
				Image:           "test-image",
				Transport:       types.TransportTypeStdio,
				ProxyMode:       types.ProxyModeStreamableHTTP,
				Port:            60000,
				ContainerLabels: map[string]string{},
			},
			expected: map[string]string{
				"toolhive":           "true",
				"toolhive-name":      "test-server",
				"toolhive-transport": "streamable-http", // Should be "streamable-http" not "stdio"
				"toolhive-port":      "60000",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := tc.config.WithStandardLabels()

			assert.Equal(t, tc.config, result, "WithStandardLabels should return the same config instance")

			// Check that all expected labels are set
			for key, value := range tc.expected {
				assert.Equal(t, value, tc.config.ContainerLabels[key], "Label %s should be set correctly", key)
			}
		})
	}
}

func TestRunConfig_WithAuthz(t *testing.T) {
	t.Parallel()
	config := NewRunConfig()
	authzConfig := &authz.Config{
		Version: "1.0",
		Type:    "cedar_v1",
	}

	result := config.WithAuthz(authzConfig)

	assert.Equal(t, config, result, "WithAuthz should return the same config instance")
	assert.Equal(t, authzConfig, config.AuthzConfig, "AuthzConfig should be set correctly")
}

// mockEnvVarValidator implements the EnvVarValidator interface for testing
type mockEnvVarValidator struct{}

func (*mockEnvVarValidator) Validate(_ context.Context, _ *regtypes.ImageMetadata, _ *RunConfig, suppliedEnvVars map[string]string) (map[string]string, error) {
	// For testing, just return the supplied environment variables as-is
	return suppliedEnvVars, nil
}

func TestRunConfigBuilder(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()
	runtime := &runtimemocks.MockRuntime{}
	cmdArgs := []string{"arg1", "arg2"}
	name := "test-server"
	imageURL := "test-image:latest"
	imageMetadata := &regtypes.ImageMetadata{
		BaseServerMetadata: regtypes.BaseServerMetadata{
			Name:      "test-metadata-name",
			Transport: "sse",
		},
		TargetPort: 9090,
		Args:       []string{"--metadata-arg"},
	}
	host := localhostStr
	debug := true
	volumes := []string{"/host:/container"}
	secretsList := []string{"secret1,target=ENV_VAR1"}
	authzConfigPath := "" // Empty to skip loading the authorization configuration
	permissionProfile := permissions.ProfileNone
	targetHost := localhostStr
	mcpTransport := "sse"
	proxyPort := 60000
	targetPort := 60001
	envVars := map[string]string{"TEST_ENV": "test_value"}

	oidcIssuer := "https://issuer.example.com"
	oidcAudience := "test-audience"
	oidcJwksURL := "https://issuer.example.com/.well-known/jwks.json"
	oidcClientID := "test-client"
	k8sPodPatch := `{"spec":{"containers":[{"name":"test","resources":{"limits":{"memory":"512Mi"}}}]}}`
	envVarValidator := &mockEnvVarValidator{}

	config, err := NewRunConfigBuilder(context.Background(), imageMetadata, envVars, envVarValidator,
		WithRuntime(runtime),
		WithCmdArgs(cmdArgs),
		WithName(name),
		WithImage(imageURL),
		WithHost(host),
		WithTargetHost(targetHost),
		WithDebug(debug),
		WithVolumes(volumes),
		WithSecrets(secretsList),
		WithAuthzConfigPath(authzConfigPath),
		WithAuditConfigPath(""),
		WithPermissionProfileNameOrPath(permissionProfile),
		WithNetworkIsolation(false),
		WithK8sPodPatch(k8sPodPatch),
		WithProxyMode(types.ProxyModeSSE),
		WithTransportAndPorts(mcpTransport, proxyPort, targetPort),
		WithAuditEnabled(false, ""),
		WithLabels(nil),
		WithGroup(""),
		WithOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, "", oidcClientID, "", "", "", "", false, false),
		WithTelemetryConfig("", false, false, false, "", 0.1, nil, false, nil),
		WithToolsFilter(nil),
		WithIgnoreConfig(&ignore.Config{
			LoadGlobal:    false,
			PrintOverlays: false,
		}),
	)
	require.NoError(t, err, "Builder should not return an error")

	assert.NotNil(t, config, "Builder should return a non-nil config")
	assert.Equal(t, runtime, config.Deployer, "Deployer should match")
	assert.Equal(t, targetHost, config.TargetHost, "TargetHost should match")
	// User args override registry defaults
	expectedCmdArgs := cmdArgs // User args take precedence over registry defaults
	assert.Equal(t, expectedCmdArgs, config.CmdArgs, "CmdArgs should use user args, overriding registry defaults")
	assert.Equal(t, name, config.Name, "Name should match")
	assert.Equal(t, imageURL, config.Image, "Image should match")
	assert.Equal(t, debug, config.Debug, "Debug should match")
	assert.Equal(t, volumes, config.Volumes, "Volumes should match")
	assert.Equal(t, secretsList, config.Secrets, "Secrets should match")
	assert.Equal(t, authzConfigPath, config.AuthzConfigPath, "AuthzConfigPath should match")
	assert.Equal(t, permissionProfile, config.PermissionProfileNameOrPath, "PermissionProfileNameOrPath should match")
	assert.NotNil(t, config.ContainerLabels, "ContainerLabels should be initialized")
	assert.NotNil(t, config.EnvVars, "EnvVars should be initialized")
	assert.Equal(t, k8sPodPatch, config.K8sPodTemplatePatch, "K8sPodTemplatePatch should match")

	// Check that environment variables were set
	assert.Equal(t, "test_value", config.EnvVars["TEST_ENV"], "Environment variable should be set")

	// Check transport settings
	assert.Equal(t, types.TransportTypeSSE, config.Transport, "Transport should be set to SSE")
	assert.Equal(t, proxyPort, config.Port, "ProxyPort should match")
	assert.Equal(t, targetPort, config.TargetPort, "TargetPort should match")

	// Check OIDC config
	assert.NotNil(t, config.OIDCConfig, "OIDCConfig should be initialized")
	assert.Equal(t, oidcIssuer, config.OIDCConfig.Issuer, "OIDCConfig.Issuer should match")
	assert.Equal(t, oidcAudience, config.OIDCConfig.Audience, "OIDCConfig.Audience should match")
	assert.Equal(t, oidcJwksURL, config.OIDCConfig.JWKSURL, "OIDCConfig.JWKSURL should match")
	assert.Equal(t, oidcClientID, config.OIDCConfig.ClientID, "OIDCConfig.ClientID should match")

	// Check that user args override registry defaults (metadata args should not be present)
	assert.NotContains(t, config.CmdArgs, "--metadata-arg", "User args should override registry defaults")
}

func TestRunConfig_WriteJSON_ReadJSON(t *testing.T) {
	t.Parallel()
	// Create a config with some values
	originalConfig := &RunConfig{
		Image:         "test-image",
		CmdArgs:       []string{"arg1", "arg2"},
		Name:          "test-server",
		ContainerName: "test-container",
		BaseName:      "test-base",
		Transport:     types.TransportTypeSSE,
		Port:          60000,
		TargetPort:    60001,
		Debug:         true,
		ContainerLabels: map[string]string{
			"label1": "value1",
			"label2": "value2",
		},
		EnvVars: map[string]string{
			"env1": "value1",
			"env2": "value2",
		},
	}

	// Write the config to a buffer
	var buf bytes.Buffer
	err := originalConfig.WriteJSON(&buf)
	require.NoError(t, err, "WriteJSON should not return an error")

	// Read the config from the buffer
	readConfig, err := ReadJSON(&buf)
	require.NoError(t, err, "ReadJSON should not return an error")

	// Check that the read config matches the original config
	assert.Equal(t, originalConfig.Image, readConfig.Image, "Image should match")
	assert.Equal(t, originalConfig.CmdArgs, readConfig.CmdArgs, "CmdArgs should match")
	assert.Equal(t, originalConfig.Name, readConfig.Name, "Name should match")
	assert.Equal(t, originalConfig.ContainerName, readConfig.ContainerName, "ContainerName should match")
	assert.Equal(t, originalConfig.BaseName, readConfig.BaseName, "BaseName should match")
	assert.Equal(t, originalConfig.Transport, readConfig.Transport, "Transport should match")
	assert.Equal(t, originalConfig.Port, readConfig.Port, "Port should match")
	assert.Equal(t, originalConfig.TargetPort, readConfig.TargetPort, "TargetPort should match")
	assert.Equal(t, originalConfig.Debug, readConfig.Debug, "Debug should match")
	assert.Equal(t, originalConfig.ContainerLabels, readConfig.ContainerLabels, "ContainerLabels should match")
	assert.Equal(t, originalConfig.EnvVars, readConfig.EnvVars, "EnvVars should match")
}

func TestCommaSeparatedEnvVars(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "single comma-separated string",
			input:    []string{"ENV1,ENV2,ENV3"},
			expected: []string{"ENV1", "ENV2", "ENV3"},
		},
		{
			name:     "multiple flags with comma-separated values",
			input:    []string{"ENV1,ENV2", "ENV3,ENV4"},
			expected: []string{"ENV1", "ENV2", "ENV3", "ENV4"},
		},
		{
			name:     "mixed single and comma-separated",
			input:    []string{"ENV1", "ENV2,ENV3", "ENV4"},
			expected: []string{"ENV1", "ENV2", "ENV3", "ENV4"},
		},
		{
			name:     "with whitespace",
			input:    []string{"ENV1, ENV2 , ENV3"},
			expected: []string{"ENV1", "ENV2", "ENV3"},
		},
		{
			name:     "empty values filtered out",
			input:    []string{"ENV1,,ENV2, ,ENV3"},
			expected: []string{"ENV1", "ENV2", "ENV3"},
		},
		{
			name:     "single environment variable",
			input:    []string{"ENV1"},
			expected: []string{"ENV1"},
		},
		{
			name:     "empty input",
			input:    []string{},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Test the environment variable processing logic
			var processedEnvVars []string
			for _, envVarEntry := range tt.input {
				// Split by comma and trim whitespace (same logic as in config.go)
				envVars := strings.Split(envVarEntry, ",")
				for _, envVar := range envVars {
					trimmed := strings.TrimSpace(envVar)
					if trimmed != "" {
						processedEnvVars = append(processedEnvVars, trimmed)
					}
				}
			}

			assert.Equal(t, tt.expected, processedEnvVars)
		})
	}
}

// TestRunConfigBuilder_MetadataOverrides ensures metadata is applied correctly
func TestRunConfigBuilder_MetadataOverrides(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	tests := []struct {
		name               string
		userTransport      string
		userTargetPort     int
		metadata           *regtypes.ImageMetadata
		expectedTransport  types.TransportType
		expectedTargetPort int
	}{
		{
			name:           "Metadata transport used when user doesn't specify",
			userTransport:  "",
			userTargetPort: 0,
			metadata: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{
					Transport: "streamable-http",
				},
				TargetPort: 3000,
			},
			expectedTransport:  types.TransportTypeStreamableHTTP,
			expectedTargetPort: 3000,
		},
		{
			name:           "User transport overrides metadata",
			userTransport:  "stdio",
			userTargetPort: 0,
			metadata: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{
					Transport: "sse",
				},
				TargetPort: 3000,
			},
			expectedTransport:  types.TransportTypeStdio,
			expectedTargetPort: 0, // stdio doesn't use target port
		},
		{
			name:           "User target port overrides metadata",
			userTransport:  "sse",
			userTargetPort: 4000,
			metadata: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{
					Transport: "sse",
				},
				TargetPort: 3000,
			},
			expectedTransport:  types.TransportTypeSSE,
			expectedTargetPort: 4000,
		},
		{
			name:               "Default to stdio when no metadata and no user input",
			userTransport:      "",
			userTargetPort:     0,
			metadata:           nil,
			expectedTransport:  types.TransportTypeStdio,
			expectedTargetPort: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runtime := &runtimemocks.MockRuntime{}
			validator := &mockEnvVarValidator{}

			config, err := NewRunConfigBuilder(context.Background(), tt.metadata, nil, validator,
				WithRuntime(runtime),
				WithCmdArgs(nil),
				WithName("test-server"),
				WithImage("test-image"),
				WithHost(localhostStr),
				WithTargetHost(localhostStr),
				WithDebug(false),
				WithVolumes(nil),
				WithSecrets(nil),
				WithAuthzConfigPath(""),
				WithAuditConfigPath(""),
				WithPermissionProfileNameOrPath(permissions.ProfileNone),
				WithNetworkIsolation(false),
				WithK8sPodPatch(""),
				WithProxyMode(types.ProxyModeSSE),
				WithTransportAndPorts(tt.userTransport, 0, tt.userTargetPort),
				WithAuditEnabled(false, ""),
				WithLabels(nil),
				WithGroup(""),
				WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false),
				WithTelemetryConfig("", false, false, false, "", 0, nil, false, nil),
				WithToolsFilter(nil),
				WithIgnoreConfig(&ignore.Config{
					LoadGlobal:    false,
					PrintOverlays: false,
				}),
			)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedTransport, config.Transport)
			assert.Equal(t, tt.expectedTargetPort, config.TargetPort)
		})
	}
}

// TestRunConfigBuilder_EnvironmentVariableTransportDependency ensures that
// environment variables set by WithEnvironmentVariables have access to the
// correct transport and port values
func TestRunConfigBuilder_EnvironmentVariableTransportDependency(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()
	runtime := &runtimemocks.MockRuntime{}
	validator := &mockEnvVarValidator{}

	config, err := NewRunConfigBuilder(context.Background(), nil, map[string]string{"USER_VAR": "value"}, validator,
		WithRuntime(runtime),
		WithCmdArgs(nil),
		WithName("test-server"),
		WithImage("test-image"),
		WithHost(localhostStr),
		WithTargetHost(localhostStr),
		WithDebug(false),
		WithVolumes(nil),
		WithSecrets(nil),
		WithAuthzConfigPath(""),
		WithAuditConfigPath(""),
		WithPermissionProfileNameOrPath(permissions.ProfileNone),
		WithNetworkIsolation(false),
		WithK8sPodPatch(""),
		WithProxyMode(types.ProxyModeSSE),
		WithTransportAndPorts("sse", 0, 9000), // This should result in MCP_TRANSPORT=sse and MCP_PORT=9000 in env vars
		WithAuditEnabled(false, ""),
		WithLabels(nil),
		WithGroup(""),
		WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false),
		WithTelemetryConfig("", false, false, false, "", 0, nil, false, nil),
		WithToolsFilter(nil),
		WithIgnoreConfig(&ignore.Config{
			LoadGlobal:    false,
			PrintOverlays: false,
		}),
	)

	require.NoError(t, err)

	// Verify that transport-specific environment variables were set correctly
	assert.Equal(t, "sse", config.EnvVars["MCP_TRANSPORT"])
	// Verify that MCP_PORT was set to the actual target port (may differ from requested if port was busy)
	assert.Equal(t, fmt.Sprintf("%d", config.TargetPort), config.EnvVars["MCP_PORT"])
	assert.Equal(t, "value", config.EnvVars["USER_VAR"])
}

// TestRunConfigBuilder_CmdArgsMetadataOverride tests that user args override registry defaults
func TestRunConfigBuilder_CmdArgsMetadataOverride(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	runtime := &runtimemocks.MockRuntime{}
	validator := &mockEnvVarValidator{}

	userArgs := []string{"--user-arg1", "--user-arg2"}
	metadata := &regtypes.ImageMetadata{
		Args: []string{"--metadata-arg1", "--metadata-arg2"},
	}

	config, err := NewRunConfigBuilder(context.Background(), metadata, nil, validator,
		WithRuntime(runtime),
		WithCmdArgs(userArgs),
		WithName("test-server"),
		WithImage("test-image"),
		WithHost(localhostStr),
		WithTargetHost(localhostStr),
		WithDebug(false),
		WithVolumes(nil),
		WithSecrets(nil),
		WithAuthzConfigPath(""),
		WithAuditConfigPath(""),
		WithPermissionProfileNameOrPath(permissions.ProfileNone),
		WithNetworkIsolation(false),
		WithK8sPodPatch(""),
		WithProxyMode(types.ProxyModeSSE),
		WithTransportAndPorts("", 0, 0),
		WithAuditEnabled(false, ""),
		WithLabels(nil),
		WithGroup(""),
		WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false),
		WithTelemetryConfig("", false, false, false, "", 0, nil, false, nil),
		WithToolsFilter(nil),
		WithIgnoreConfig(&ignore.Config{
			LoadGlobal:    false,
			PrintOverlays: false,
		}),
	)

	require.NoError(t, err)

	// User args should override registry defaults
	expectedArgs := []string{"--user-arg1", "--user-arg2"}
	assert.Equal(t, expectedArgs, config.CmdArgs)

	// Check that user args override registry defaults (metadata args should not be present)
	assert.NotContains(t, config.CmdArgs, "--metadata-arg", "User args should override registry defaults")
}

// TestRunConfigBuilder_CmdArgsMetadataDefaults tests that registry defaults are used when no user args provided
func TestRunConfigBuilder_CmdArgsMetadataDefaults(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	runtime := &runtimemocks.MockRuntime{}
	validator := &mockEnvVarValidator{}

	// No user args provided
	userArgs := []string{}
	metadata := &regtypes.ImageMetadata{
		Args: []string{"--metadata-arg1", "--metadata-arg2"},
	}

	config, err := NewRunConfigBuilder(context.Background(), metadata, nil, validator,
		WithRuntime(runtime),
		WithCmdArgs(userArgs),
		WithName("test-server"),
		WithImage("test-image"),
		WithHost(localhostStr),
		WithTargetHost(localhostStr),
		WithDebug(false),
		WithVolumes(nil),
		WithSecrets(nil),
		WithAuthzConfigPath(""),
		WithAuditConfigPath(""),
		WithPermissionProfileNameOrPath(permissions.ProfileNone),
		WithNetworkIsolation(false),
		WithK8sPodPatch(""),
		WithProxyMode(types.ProxyModeSSE),
		WithTransportAndPorts("", 0, 0),
		WithAuditEnabled(false, ""),
		WithLabels(nil),
		WithGroup(""),
		WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false),
		WithTelemetryConfig("", false, false, false, "", 0, nil, false, nil),
		WithToolsFilter(nil),
		WithIgnoreConfig(&ignore.Config{
			LoadGlobal:    false,
			PrintOverlays: false,
		}),
	)

	require.NoError(t, err)

	// Registry defaults should be used when no user args provided
	expectedArgs := []string{"--metadata-arg1", "--metadata-arg2"}
	assert.Equal(t, expectedArgs, config.CmdArgs)
}

// TestRunConfigBuilder_VolumeProcessing ensures volumes are processed
// correctly and added to the permission profile
func TestRunConfigBuilder_VolumeProcessing(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()
	runtime := &runtimemocks.MockRuntime{}
	validator := &mockEnvVarValidator{}

	volumes := []string{
		"/host/read:/container/read:ro",
		"/host/write:/container/write",
	}

	config, err := NewRunConfigBuilder(context.Background(), nil, nil, validator,
		WithRuntime(runtime),
		WithCmdArgs(nil),
		WithName("test-server"),
		WithImage("test-image"),
		WithHost(localhostStr),
		WithTargetHost(localhostStr),
		WithDebug(false),
		WithVolumes(volumes),
		WithSecrets(nil),
		WithAuthzConfigPath(""),
		WithAuditConfigPath(""),
		WithPermissionProfileNameOrPath(permissions.ProfileNone), // Start with none profile
		WithNetworkIsolation(false),
		WithK8sPodPatch(""),
		WithProxyMode(types.ProxyModeSSE),
		WithTransportAndPorts("", 0, 0),
		WithAuditEnabled(false, ""),
		WithLabels(nil),
		WithGroup(""),
		WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false),
		WithTelemetryConfig("", false, false, false, "", 0, nil, false, nil),
		WithToolsFilter(nil),
		WithIgnoreConfig(&ignore.Config{
			LoadGlobal:    false,
			PrintOverlays: false,
		}),
	)

	require.NoError(t, err)

	// Verify volumes were processed and added to permission profile
	assert.NotNil(t, config.PermissionProfile)

	// Should have 1 read mount
	readMountFound := false
	for _, mount := range config.PermissionProfile.Read {
		if strings.Contains(string(mount), "/container/read") {
			readMountFound = true
			break
		}
	}
	assert.True(t, readMountFound, "Read-only volume should be in permission profile")

	// Should have 1 write mount
	writeMountFound := false
	for _, mount := range config.PermissionProfile.Write {
		if strings.Contains(string(mount), "/container/write") {
			writeMountFound = true
			break
		}
	}
	assert.True(t, writeMountFound, "Read-write volume should be in permission profile")
}

// TestRunConfigBuilder_FilesystemMCPScenario tests the specific scenario from the bug report
func TestRunConfigBuilder_FilesystemMCPScenario(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	runtime := &runtimemocks.MockRuntime{}
	validator := &mockEnvVarValidator{}

	// Simulate the filesystem MCP registry configuration
	metadata := &regtypes.ImageMetadata{
		Args: []string{"/projects"}, // Default args from registry
	}

	// Simulate user providing their own arguments
	userArgs := []string{"/Users/testuser/repos/github.com/stacklok/toolhive"}

	config, err := NewRunConfigBuilder(context.Background(), metadata, nil, validator,
		WithRuntime(runtime),
		WithCmdArgs(userArgs),
		WithName("filesystem"),
		WithImage("mcp/filesystem:latest"),
		WithHost(localhostStr),
		WithTargetHost(localhostStr),
		WithDebug(false),
		WithVolumes(nil),
		WithSecrets(nil),
		WithAuthzConfigPath(""),
		WithAuditConfigPath(""),
		WithPermissionProfileNameOrPath(permissions.ProfileNone),
		WithNetworkIsolation(false),
		WithK8sPodPatch(""),
		WithProxyMode(types.ProxyModeSSE),
		WithTransportAndPorts("", 0, 0),
		WithAuditEnabled(false, ""),
		WithLabels(nil),
		WithGroup(""),
		WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false),
		WithTelemetryConfig("", false, false, false, "", 0, nil, false, nil),
		WithToolsFilter(nil),
		WithIgnoreConfig(&ignore.Config{
			LoadGlobal:    false,
			PrintOverlays: false,
		}),
	)

	require.NoError(t, err)

	// User args should override registry defaults (not be appended)
	expectedArgs := []string{"/Users/testuser/repos/github.com/stacklok/toolhive"}
	assert.Equal(t, expectedArgs, config.CmdArgs)

	// Registry default args should not be present
	assert.NotContains(t, config.CmdArgs, "/projects", "Registry default args should not be appended")
}

func TestRunConfig_EnvironmentVariableOverrideBehavior(t *testing.T) {
	t.Parallel()

	t.Run("empty CLI env vars preserve existing env vars", func(t *testing.T) {
		t.Parallel()

		// Create a config with existing env vars (simulating file-based config)
		config := &RunConfig{
			Transport:  types.TransportTypeStdio,
			TargetPort: 8080,
			EnvVars: map[string]string{
				"FILE_VAR":   "file_value",
				"COMMON_VAR": "from_file",
			},
		}

		// Apply empty environment variables (simulating no CLI --env flags)
		resultConfig, err := config.WithEnvironmentVariables(map[string]string{})
		require.NoError(t, err, "WithEnvironmentVariables should handle empty map")

		// Verify that original env vars are preserved
		assert.Equal(t, "file_value", resultConfig.EnvVars["FILE_VAR"], "File-based env var should be preserved")
		assert.Equal(t, "from_file", resultConfig.EnvVars["COMMON_VAR"], "File-based env var should be preserved")

		// Verify transport-specific env vars are also set (these are always added)
		assert.Equal(t, "stdio", resultConfig.EnvVars["MCP_TRANSPORT"], "Transport env var should be set")
	})

	t.Run("CLI env vars override existing env vars", func(t *testing.T) {
		t.Parallel()

		// Create a config with existing env vars
		config := &RunConfig{
			Transport:  types.TransportTypeStdio,
			TargetPort: 8080,
			EnvVars: map[string]string{
				"FILE_VAR":   "file_value",
				"COMMON_VAR": "from_file",
			},
		}

		// Apply CLI environment variables that should override existing ones
		cliEnvVars := map[string]string{
			"CLI_VAR":    "cli_value",
			"COMMON_VAR": "from_cli", // This should override the file-based value
		}

		resultConfig, err := config.WithEnvironmentVariables(cliEnvVars)
		require.NoError(t, err, "WithEnvironmentVariables should handle override map")

		// Verify CLI env vars are applied
		assert.Equal(t, "cli_value", resultConfig.EnvVars["CLI_VAR"], "CLI env var should be set")
		assert.Equal(t, "from_cli", resultConfig.EnvVars["COMMON_VAR"], "CLI env var should override file-based value")

		// Verify file-based env vars that were not overridden are preserved
		assert.Equal(t, "file_value", resultConfig.EnvVars["FILE_VAR"], "Non-overridden file-based env var should be preserved")

		// Verify transport env var is still set
		assert.Equal(t, "stdio", resultConfig.EnvVars["MCP_TRANSPORT"], "Transport env var should be set")
	})

	t.Run("nil env vars map preserves existing env vars", func(t *testing.T) {
		t.Parallel()

		// Create a config with existing env vars
		config := &RunConfig{
			Transport:  types.TransportTypeSSE,
			TargetPort: 3000,
			EnvVars: map[string]string{
				"PRESERVED_VAR": "preserved_value",
			},
		}

		// Apply nil environment variables
		resultConfig, err := config.WithEnvironmentVariables(nil)
		require.NoError(t, err, "WithEnvironmentVariables should handle nil map")

		// Verify existing env vars are preserved
		assert.Equal(t, "preserved_value", resultConfig.EnvVars["PRESERVED_VAR"], "Existing env var should be preserved with nil input")

		// Verify transport env var is set
		assert.Equal(t, "sse", resultConfig.EnvVars["MCP_TRANSPORT"], "Transport env var should be set")
	})
}

func TestRunConfig_TelemetryEnvironmentVariablesPreservation(t *testing.T) {
	t.Parallel()

	t.Run("telemetry config with environment variables is preserved", func(t *testing.T) {
		t.Parallel()

		// Create a config with telemetry configuration including environment variables
		config := &RunConfig{
			Transport:  types.TransportTypeStdio,
			TargetPort: 8080,
			TelemetryConfig: &telemetry.Config{
				Endpoint:             "http://file-based-endpoint:4318",
				ServiceName:          "file-based-service",
				TracingEnabled:       true,
				MetricsEnabled:       false,
				SamplingRate:         0.5,
				EnvironmentVariables: []string{"NODE_ENV", "DEPLOYMENT_ENV", "SERVICE_VERSION"},
			},
		}

		// Verify telemetry config exists with environment variables
		require.NotNil(t, config.TelemetryConfig, "TelemetryConfig should exist")
		assert.Equal(t, "http://file-based-endpoint:4318", config.TelemetryConfig.Endpoint)
		assert.Equal(t, "file-based-service", config.TelemetryConfig.ServiceName)
		assert.True(t, config.TelemetryConfig.TracingEnabled)
		assert.False(t, config.TelemetryConfig.MetricsEnabled)
		assert.Equal(t, 0.5, config.TelemetryConfig.SamplingRate)
		require.Len(t, config.TelemetryConfig.EnvironmentVariables, 3)
		assert.Contains(t, config.TelemetryConfig.EnvironmentVariables, "NODE_ENV")
		assert.Contains(t, config.TelemetryConfig.EnvironmentVariables, "DEPLOYMENT_ENV")
		assert.Contains(t, config.TelemetryConfig.EnvironmentVariables, "SERVICE_VERSION")
	})

	t.Run("telemetry environment variables can be extracted correctly", func(t *testing.T) {
		t.Parallel()

		// Create mock config with telemetry env vars
		config := &RunConfig{
			TelemetryConfig: &telemetry.Config{
				Endpoint:             "http://test:4318",
				ServiceName:          "test-service",
				EnvironmentVariables: []string{"TEST_VAR", "ANOTHER_VAR"},
			},
		}

		// Extract environment variables (simulating proxy runner extraction)
		var extractedEnvVars []string
		if config != nil && config.TelemetryConfig != nil {
			extractedEnvVars = config.TelemetryConfig.EnvironmentVariables
		}

		// Verify extraction worked
		require.NotNil(t, extractedEnvVars)
		assert.Len(t, extractedEnvVars, 2)
		assert.Contains(t, extractedEnvVars, "TEST_VAR")
		assert.Contains(t, extractedEnvVars, "ANOTHER_VAR")
	})

	t.Run("nil telemetry config handling", func(t *testing.T) {
		t.Parallel()

		// Test with nil config (should not panic)
		var nilConfig *RunConfig
		var extractedEnvVars []string
		if nilConfig != nil && nilConfig.TelemetryConfig != nil {
			extractedEnvVars = nilConfig.TelemetryConfig.EnvironmentVariables
		}
		assert.Nil(t, extractedEnvVars, "Should handle nil config gracefully")

		// Test with config that has nil telemetry config
		configWithNilTelemetry := &RunConfig{
			Transport: types.TransportTypeStdio,
		}
		var extractedFromNilTelemetry []string
		if configWithNilTelemetry != nil && configWithNilTelemetry.TelemetryConfig != nil {
			extractedFromNilTelemetry = configWithNilTelemetry.TelemetryConfig.EnvironmentVariables
		}
		assert.Nil(t, extractedFromNilTelemetry, "Should handle nil telemetry config gracefully")
	})
}

func TestConfigFileLoading(t *testing.T) {
	t.Parallel()

	t.Run("loads valid JSON config from file", func(t *testing.T) {
		t.Parallel()

		// Test loading config file functionality with a temporary file
		tmpDir := t.TempDir()
		configPath := tmpDir + "/runconfig.json"

		configContent := `{
			"schema_version": "v1",
			"name": "test-server",
			"image": "test:latest",
			"transport": "sse",
			"port": 9090,
			"target_port": 8080,
			"env_vars": {
				"TEST_VAR": "test_value",
				"ANOTHER_VAR": "another_value"
			}
		}`

		err := os.WriteFile(configPath, []byte(configContent), 0644)
		require.NoError(t, err, "Should be able to create config file")

		// Test loading the config file using runner.ReadJSON
		file, err := os.Open(configPath) // #nosec G304 - test path
		require.NoError(t, err, "Should be able to open config file")
		defer file.Close()

		config, err := ReadJSON(file)
		require.NoError(t, err, "Should successfully load config from file")
		require.NotNil(t, config, "Should return config when file exists")

		// Verify config was loaded correctly
		assert.Equal(t, "test-server", config.Name)
		assert.Equal(t, "test:latest", config.Image)
		assert.Equal(t, "sse", string(config.Transport))
		assert.Equal(t, 9090, config.Port)
		assert.Equal(t, 8080, config.TargetPort)
		assert.Equal(t, "test_value", config.EnvVars["TEST_VAR"])
		assert.Equal(t, "another_value", config.EnvVars["ANOTHER_VAR"])
	})

	t.Run("handles invalid JSON gracefully", func(t *testing.T) {
		t.Parallel()

		// Test loading config with invalid JSON
		tmpDir := t.TempDir()
		configPath := tmpDir + "/runconfig.json"

		invalidJSON := `{"invalid": json content}`

		err := os.WriteFile(configPath, []byte(invalidJSON), 0644)
		require.NoError(t, err, "Should be able to create invalid config file")

		// Test loading the invalid config file
		file, err := os.Open(configPath) // #nosec G304 - test path
		require.NoError(t, err, "Should be able to open config file")
		defer file.Close()

		config, err := ReadJSON(file)
		assert.Error(t, err, "Should error when JSON is invalid")
		assert.Nil(t, config, "Should return nil when JSON is invalid")
	})

	t.Run("handles file read errors", func(t *testing.T) {
		t.Parallel()

		// Test handling file read error by creating a directory instead of a file
		tmpDir := t.TempDir()
		configPath := tmpDir + "/runconfig.json"

		// Create a directory instead of a file to cause read error
		err := os.Mkdir(configPath, 0755)
		require.NoError(t, err, "Should be able to create directory")

		// Attempt to open directory as file (should fail)
		file, err := os.Open(configPath) // #nosec G304 - test path
		if err == nil {
			defer file.Close()
			// If opening succeeds (shouldn't normally), reading should fail
			config, err := ReadJSON(file)
			assert.Error(t, err, "Should error when trying to read directory as JSON")
			assert.Nil(t, config, "Should return nil when file cannot be read as JSON")
		} else {
			// Opening directory as file failed as expected
			assert.Error(t, err, "Should error when trying to open directory as file")
		}
	})

	t.Run("handles missing file", func(t *testing.T) {
		t.Parallel()

		// Test handling missing file
		nonexistentPath := "/nonexistent/path/runconfig.json"

		file, err := os.Open(nonexistentPath) // #nosec G304 - test path
		assert.Error(t, err, "Should error when file does not exist")
		assert.Nil(t, file, "File handle should be nil when file does not exist")
	})
}

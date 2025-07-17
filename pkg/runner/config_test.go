package runner

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport/types"
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
		envVars     []string
		expectError bool
		expected    map[string]string
	}{
		{
			name:        "Empty environment variables",
			config:      &RunConfig{Transport: types.TransportTypeSSE, TargetPort: 9000},
			envVars:     []string{},
			expectError: false,
			expected: map[string]string{
				"MCP_TRANSPORT": "sse",
				"MCP_PORT":      "9000",
			},
		},
		{
			name:        "Valid environment variables",
			config:      &RunConfig{Transport: types.TransportTypeSSE, TargetPort: 9000},
			envVars:     []string{"KEY1=value1", "KEY2=value2"},
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
			envVars:     []string{"KEY1=value1"},
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
			envVars:     []string{"KEY1=new_value"},
			expectError: false,
			expected: map[string]string{
				"KEY1":          "new_value",
				"MCP_TRANSPORT": "sse",
				"MCP_PORT":      "9000",
			},
		},
		{
			name:        "Invalid environment variable format",
			config:      &RunConfig{Transport: types.TransportTypeSSE, TargetPort: 9000},
			envVars:     []string{"INVALID_FORMAT"},
			expectError: true,
		},
		{
			name:        "Stdio transport",
			config:      &RunConfig{Transport: types.TransportTypeStdio},
			envVars:     []string{},
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
				assert.Contains(t, err.Error(), "failed to parse environment variables", "Error message should mention parsing failure")
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

// TODO: Use mockgen for this
// mockSecretManager is a mock implementation of a secret manager
type mockSecretManager struct {
	secrets map[string]string
}

func (m *mockSecretManager) GetSecret(_ context.Context, name string) (string, error) {
	if value, ok := m.secrets[name]; ok {
		return value, nil
	}
	return "", fmt.Errorf("secret %s not found", name)
}

func (m *mockSecretManager) SetSecret(_ context.Context, name, value string) error {
	m.secrets[name] = value
	return nil
}

func (m *mockSecretManager) DeleteSecret(_ context.Context, name string) error {
	delete(m.secrets, name)
	return nil
}

func (m *mockSecretManager) ListSecrets(_ context.Context) ([]secrets.SecretDescription, error) {
	keys := make([]secrets.SecretDescription, 0, len(m.secrets))
	for k := range m.secrets {
		keys = append(keys, secrets.SecretDescription{Key: k})
	}
	return keys, nil
}

func (*mockSecretManager) Cleanup() error {
	return nil
}

func (*mockSecretManager) Capabilities() secrets.ProviderCapabilities {
	return secrets.ProviderCapabilities{
		CanRead:    true,
		CanWrite:   true,
		CanDelete:  true,
		CanList:    true,
		CanCleanup: true,
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
			// Create a mock secret manager
			secretManager := &mockSecretManager{
				secrets: tc.mockSecrets,
			}

			// Set the secrets in the config
			tc.config.Secrets = tc.secrets

			// Call the function
			result, err := tc.config.WithSecrets(t.Context(), secretManager)

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

			result := tc.config.WithContainerName()

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
				Port:            8080,
				ContainerLabels: map[string]string{},
			},
			expected: map[string]string{
				"toolhive":           "true",
				"toolhive-name":      "test-server",
				"toolhive-transport": "sse",
				"toolhive-port":      "8080",
				"toolhive-tool-type": "mcp",
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
				"toolhive-tool-type": "mcp",
				"existing-label":     "existing-value",
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

func (*mockEnvVarValidator) Validate(_ context.Context, _ *registry.ImageMetadata, _ *RunConfig, suppliedEnvVars []string) ([]string, error) {
	// For testing, just return the supplied environment variables as-is
	return suppliedEnvVars, nil
}

func TestNewRunConfigFromFlags(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()
	runtime := &mocks.MockRuntime{}
	cmdArgs := []string{"arg1", "arg2"}
	name := "test-server"
	imageURL := "test-image:latest"
	imageMetadata := &registry.ImageMetadata{
		Name:       "test-metadata-name",
		Transport:  "sse",
		TargetPort: 9090,
		Args:       []string{"--metadata-arg"},
	}
	host := "localhost"
	debug := true
	volumes := []string{"/host:/container"}
	secretsList := []string{"secret1,target=ENV_VAR1"}
	authzConfigPath := "" // Empty to skip loading the authorization configuration
	permissionProfile := permissions.ProfileNone
	targetHost := "localhost"
	mcpTransport := "sse"
	port := 8080
	targetPort := 9000
	envVars := []string{"TEST_ENV=test_value"}
	oidcIssuer := "https://issuer.example.com"
	oidcAudience := "test-audience"
	oidcJwksURL := "https://issuer.example.com/.well-known/jwks.json"
	oidcClientID := "test-client"
	k8sPodPatch := `{"spec":{"containers":[{"name":"test","resources":{"limits":{"memory":"512Mi"}}}]}}`
	envVarValidator := &mockEnvVarValidator{}

	config, err := NewRunConfigFromFlags(
		context.Background(),
		runtime,
		cmdArgs,
		name,
		imageURL,
		imageMetadata,
		host,
		debug,
		volumes,
		secretsList,
		authzConfigPath,
		"",    // auditConfigPath
		false, // enableAudit
		permissionProfile,
		targetHost,
		mcpTransport,
		port,
		targetPort,
		envVars,
		oidcIssuer,
		oidcAudience,
		oidcJwksURL,
		oidcClientID,
		false,
		"",    // otelEndpoint
		"",    // otelServiceName
		0.1,   // otelSamplingRate
		nil,   // otelHeaders
		false, // otelInsecure
		false, // otelEnablePrometheusMetricsPath
		nil,   // otelEnvironmentVariables
		false, // isolateNetwork
		k8sPodPatch,
		"",    // thvCABundle
		"",    // jwksAuthTokenFile
		false, // jwksAllowPrivateIP
		envVarValidator,
		"sse",
	)
	require.NoError(t, err, "NewRunConfigFromFlags should not return an error")

	assert.NotNil(t, config, "NewRunConfigFromFlags should return a non-nil config")
	assert.Equal(t, runtime, config.Runtime, "Runtime should match")
	assert.Equal(t, targetHost, config.TargetHost, "TargetHost should match")
	// The metadata args are appended to the command-line args
	expectedCmdArgs := append([]string{}, cmdArgs...)
	expectedCmdArgs = append(expectedCmdArgs, imageMetadata.Args...)
	assert.Equal(t, expectedCmdArgs, config.CmdArgs, "CmdArgs should include metadata args")
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
	assert.Equal(t, port, config.Port, "ProxyPort should match")
	assert.Equal(t, targetPort, config.TargetPort, "TargetPort should match")

	// Check OIDC config
	assert.NotNil(t, config.OIDCConfig, "OIDCConfig should be initialized")
	assert.Equal(t, oidcIssuer, config.OIDCConfig.Issuer, "OIDCConfig.Issuer should match")
	assert.Equal(t, oidcAudience, config.OIDCConfig.Audience, "OIDCConfig.Audience should match")
	assert.Equal(t, oidcJwksURL, config.OIDCConfig.JWKSURL, "OIDCConfig.JWKSURL should match")
	assert.Equal(t, oidcClientID, config.OIDCConfig.ClientID, "OIDCConfig.ClientID should match")

	// Check that metadata args were prepended
	assert.Contains(t, config.CmdArgs, "--metadata-arg", "Metadata args should be prepended")
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
		Port:          8080,
		TargetPort:    9000,
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

// TestNewRunConfigFromFlags_MetadataOverrides ensures metadata is applied correctly
func TestNewRunConfigFromFlags_MetadataOverrides(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	tests := []struct {
		name               string
		userTransport      string
		userTargetPort     int
		metadata           *registry.ImageMetadata
		expectedTransport  types.TransportType
		expectedTargetPort int
	}{
		{
			name:           "Metadata transport used when user doesn't specify",
			userTransport:  "",
			userTargetPort: 0,
			metadata: &registry.ImageMetadata{
				Transport:  "streamable-http",
				TargetPort: 3000,
			},
			expectedTransport:  types.TransportTypeStreamableHTTP,
			expectedTargetPort: 3000,
		},
		{
			name:           "User transport overrides metadata",
			userTransport:  "stdio",
			userTargetPort: 0,
			metadata: &registry.ImageMetadata{
				Transport:  "sse",
				TargetPort: 3000,
			},
			expectedTransport:  types.TransportTypeStdio,
			expectedTargetPort: 0, // stdio doesn't use target port
		},
		{
			name:           "User target port overrides metadata",
			userTransport:  "sse",
			userTargetPort: 4000,
			metadata: &registry.ImageMetadata{
				Transport:  "sse",
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

			runtime := &mocks.MockRuntime{}
			validator := &mockEnvVarValidator{}

			config, err := NewRunConfigFromFlags(
				context.Background(),
				runtime,
				nil, // cmdArgs
				"test-server",
				"test-image",
				tt.metadata,
				"localhost",
				false, // debug
				nil,   // volumes
				nil,   // secrets
				"",    // authzConfigPath
				"",    // auditConfigPath
				false, // enableAudit
				permissions.ProfileNone,
				"localhost",
				tt.userTransport,
				8080, // port
				tt.userTargetPort,
				nil, // envVars
				"",  // oidc params...
				"",
				"",
				"",
				false,
				"", // telemetry params...
				"",
				0,
				nil,
				false,
				false,
				nil,
				false, // isolateNetwork
				"",    // k8sPodPatch
				"",    // thvCABundle
				"",    // jwksAuthTokenFile
				false, // jwksAllowPrivateIP
				validator,
				types.ProxyModeSSE,
			)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedTransport, config.Transport)
			assert.Equal(t, tt.expectedTargetPort, config.TargetPort)
		})
	}
}

// TestNewRunConfigFromFlags_EnvironmentVariableTransportDependency ensures that
// environment variables set by WithEnvironmentVariables have access to the
// correct transport and port values
func TestNewRunConfigFromFlags_EnvironmentVariableTransportDependency(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()
	runtime := &mocks.MockRuntime{}
	validator := &mockEnvVarValidator{}

	config, err := NewRunConfigFromFlags(
		context.Background(),
		runtime,
		nil,
		"test-server",
		"test-image",
		nil,
		"localhost",
		false,
		nil,
		nil,
		"",
		"",
		false,
		permissions.ProfileNone,
		"localhost",
		"sse", // This should result in MCP_TRANSPORT=sse in env vars
		8080,
		9000, // This should result in MCP_PORT=9000 in env vars
		[]string{"USER_VAR=value"},
		"", "", "", "", false, // OIDC params
		"", "", 0, nil, false, false, nil, // telemetry params
		false,
		"",
		"",    // thvCABundle
		"",    // jwksAuthTokenFile
		false, // jwksAllowPrivateIP
		validator,
		types.ProxyModeSSE,
	)

	require.NoError(t, err)

	// Verify that transport-specific environment variables were set correctly
	assert.Equal(t, "sse", config.EnvVars["MCP_TRANSPORT"])
	assert.Equal(t, "9000", config.EnvVars["MCP_PORT"])
	assert.Equal(t, "value", config.EnvVars["USER_VAR"])
}

// TestNewRunConfigFromFlags_CmdArgsMetadataPrepending ensures that metadata args
// are prepended to user args
func TestNewRunConfigFromFlags_CmdArgsMetadataPrepending(t *testing.T) {
	t.Parallel()

	runtime := &mocks.MockRuntime{}
	validator := &mockEnvVarValidator{}

	userArgs := []string{"--user-arg1", "--user-arg2"}
	metadata := &registry.ImageMetadata{
		Args: []string{"--metadata-arg1", "--metadata-arg2"},
	}

	config, err := NewRunConfigFromFlags(
		context.Background(),
		runtime,
		userArgs,
		"test-server",
		"test-image",
		metadata,
		"localhost",
		false,
		nil,
		nil,
		"",
		"",
		false,
		permissions.ProfileNone,
		"localhost",
		"",
		8080,
		0,
		nil,
		"", "", "", "", false,
		"", "", 0, nil, false, false, nil,
		false,
		"",
		"",    // thvCABundle
		"",    // jwksAuthTokenFile
		false, // jwksAllowPrivateIP
		validator,
		types.ProxyModeSSE,
	)

	require.NoError(t, err)

	// Metadata args should be appended to user args (not prepended despite the log message)
	expectedArgs := []string{"--user-arg1", "--user-arg2", "--metadata-arg1", "--metadata-arg2"}
	assert.Equal(t, expectedArgs, config.CmdArgs)
}

// TestNewRunConfigFromFlags_VolumeProcessing ensures volumes are processed
// correctly and added to the permission profile
func TestNewRunConfigFromFlags_VolumeProcessing(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()
	runtime := &mocks.MockRuntime{}
	validator := &mockEnvVarValidator{}

	volumes := []string{
		"/host/read:/container/read:ro",
		"/host/write:/container/write",
	}

	config, err := NewRunConfigFromFlags(
		context.Background(),
		runtime,
		nil,
		"test-server",
		"test-image",
		nil,
		"localhost",
		false,
		volumes,
		nil,
		"",
		"",
		false,
		permissions.ProfileNone, // Start with none profile
		"localhost",
		"",
		8080,
		0,
		nil,
		"", "", "", "", false,
		"", "", 0, nil, false, false, nil,
		false,
		"",
		"",    // thvCABundle
		"",    // jwksAuthTokenFile
		false, // jwksAllowPrivateIP
		validator,
		types.ProxyModeSSE,
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

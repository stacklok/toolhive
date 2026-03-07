// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/permissions"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authz"
	runtimemocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/secrets"
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
		{
			name: "Bearer token in CLI format resolved successfully",
			config: &RunConfig{
				EnvVars: map[string]string{},
				RemoteAuthConfig: &remote.Config{
					BearerToken: "BEARER_TOKEN_SECRET,target=bearer_token",
				},
			},
			secrets: []string{},
			mockSecrets: map[string]string{
				"BEARER_TOKEN_SECRET": "my-bearer-token-value",
			},
			expectError: false,
			expected:    map[string]string{},
		},
		{
			name: "Bearer token in plain text remains unchanged",
			config: &RunConfig{
				EnvVars: map[string]string{},
				RemoteAuthConfig: &remote.Config{
					BearerToken: "plain-text-bearer-token",
				},
			},
			secrets:     []string{},
			mockSecrets: map[string]string{},
			expectError: false,
			expected:    map[string]string{},
		},
		{
			name: "Bearer token secret not found returns error",
			config: &RunConfig{
				EnvVars: map[string]string{},
				RemoteAuthConfig: &remote.Config{
					BearerToken: "NONEXISTENT_BEARER_TOKEN,target=bearer_token",
				},
			},
			secrets:     []string{},
			mockSecrets: map[string]string{},
			expectError: true,
		},
		{
			name: "Bearer token and OAuth client secret both resolved",
			config: &RunConfig{
				EnvVars: map[string]string{},
				RemoteAuthConfig: &remote.Config{
					BearerToken:  "BEARER_TOKEN_SECRET,target=bearer_token",
					ClientSecret: "OAUTH_SECRET,target=oauth_secret",
				},
			},
			secrets: []string{},
			mockSecrets: map[string]string{
				"BEARER_TOKEN_SECRET": "my-bearer-token",
				"OAUTH_SECRET":        "my-oauth-secret",
			},
			expectError: false,
			expected:    map[string]string{},
		},
		{
			name: "Bearer token resolved alongside regular secrets",
			config: &RunConfig{
				EnvVars: map[string]string{},
				RemoteAuthConfig: &remote.Config{
					BearerToken: "BEARER_TOKEN_SECRET,target=bearer_token",
				},
			},
			secrets: []string{
				"secret1,target=ENV_VAR1",
			},
			mockSecrets: map[string]string{
				"BEARER_TOKEN_SECRET": "my-bearer-token",
				"secret1":             "value1",
			},
			expectError: false,
			expected: map[string]string{
				"ENV_VAR1": "value1",
			},
		},
		{
			name: "Bearer token with empty RemoteAuthConfig",
			config: &RunConfig{
				EnvVars:          map[string]string{},
				RemoteAuthConfig: nil,
			},
			secrets: []string{
				"secret1,target=ENV_VAR1",
			},
			mockSecrets: map[string]string{
				"secret1": "value1",
			},
			expectError: false,
			expected: map[string]string{
				"ENV_VAR1": "value1",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Create a mock secret manager
			secretManager := secretsmocks.NewMockProvider(ctrl)

			// Collect all secret names that need to be mocked
			secretNamesToMock := make(map[string]string)

			// Add regular secrets
			for secretName, secretValue := range tc.mockSecrets {
				secretNamesToMock[secretName] = secretValue
			}

			// Set up mock expectations for RemoteAuthConfig secrets (BearerToken and ClientSecret)
			if tc.config.RemoteAuthConfig != nil {
				// Handle BearerToken if present
				if tc.config.RemoteAuthConfig.BearerToken != "" {
					if secretParam, err := secrets.ParseSecretParameter(tc.config.RemoteAuthConfig.BearerToken); err == nil {
						// It's in CLI format, need to mock GetSecret for it
						if expectedToken, exists := tc.mockSecrets[secretParam.Name]; exists {
							secretNamesToMock[secretParam.Name] = expectedToken
						}
					}
				}
				// Handle ClientSecret if present
				if tc.config.RemoteAuthConfig.ClientSecret != "" {
					if secretParam, err := secrets.ParseSecretParameter(tc.config.RemoteAuthConfig.ClientSecret); err == nil {
						// It's in CLI format, need to mock GetSecret for it
						if expectedSecret, exists := tc.mockSecrets[secretParam.Name]; exists {
							secretNamesToMock[secretParam.Name] = expectedSecret
						}
					}
				}
			}

			// Set up mock expectations for all secrets that should succeed
			for secretName, secretValue := range secretNamesToMock {
				secretManager.EXPECT().GetSecret(gomock.Any(), secretName).Return(secretValue, nil).AnyTimes()
			}

			// Set up mock expectations for secrets that should fail (error cases)
			if tc.expectError {
				// Handle regular secret not found
				if len(tc.secrets) > 0 {
					for _, secretStr := range tc.secrets {
						if secretParam, err := secrets.ParseSecretParameter(secretStr); err == nil {
							if _, exists := secretNamesToMock[secretParam.Name]; !exists {
								secretManager.EXPECT().GetSecret(gomock.Any(), secretParam.Name).Return("", fmt.Errorf("secret %s not found", secretParam.Name)).AnyTimes()
							}
						}
					}
				}
				// Handle bearer token secret not found
				if tc.config.RemoteAuthConfig != nil && tc.config.RemoteAuthConfig.BearerToken != "" {
					if secretParam, err := secrets.ParseSecretParameter(tc.config.RemoteAuthConfig.BearerToken); err == nil {
						if _, exists := secretNamesToMock[secretParam.Name]; !exists {
							secretManager.EXPECT().GetSecret(gomock.Any(), secretParam.Name).Return("", fmt.Errorf("secret %s not found", secretParam.Name)).AnyTimes()
						}
					}
				}
				// Handle OAuth client secret not found
				if tc.config.RemoteAuthConfig != nil && tc.config.RemoteAuthConfig.ClientSecret != "" {
					if secretParam, err := secrets.ParseSecretParameter(tc.config.RemoteAuthConfig.ClientSecret); err == nil {
						if _, exists := secretNamesToMock[secretParam.Name]; !exists {
							secretManager.EXPECT().GetSecret(gomock.Any(), secretParam.Name).Return("", fmt.Errorf("secret %s not found", secretParam.Name)).AnyTimes()
						}
					}
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

				// Check bearer token resolution if RemoteAuthConfig is present
				if tc.config.RemoteAuthConfig != nil && tc.config.RemoteAuthConfig.BearerToken != "" {
					// Check if bearer token was in CLI format
					if secretParam, err := secrets.ParseSecretParameter(tc.config.RemoteAuthConfig.BearerToken); err == nil {
						// It was in CLI format, should be resolved to the actual value
						if expectedToken, exists := tc.mockSecrets[secretParam.Name]; exists {
							assert.Equal(t, expectedToken, tc.config.RemoteAuthConfig.BearerToken, "Bearer token should be resolved from CLI format")
						}
					} else {
						// It was plain text, should remain unchanged
						// We need to check against the original value from the test case
						originalToken := "plain-text-bearer-token" // Default for the plain text test case
						if tc.name == "Bearer token in plain text remains unchanged" {
							assert.Equal(t, originalToken, tc.config.RemoteAuthConfig.BearerToken, "Plain text bearer token should remain unchanged")
						}
					}
				}

				// Check OAuth client secret resolution if RemoteAuthConfig is present
				if tc.config.RemoteAuthConfig != nil && tc.config.RemoteAuthConfig.ClientSecret != "" {
					// Check if client secret was in CLI format
					if secretParam, err := secrets.ParseSecretParameter(tc.config.RemoteAuthConfig.ClientSecret); err == nil {
						// It was in CLI format, should be resolved to the actual value
						if expectedSecret, exists := tc.mockSecrets[secretParam.Name]; exists {
							assert.Equal(t, expectedSecret, tc.config.RemoteAuthConfig.ClientSecret, "OAuth client secret should be resolved from CLI format")
						}
					}
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
				"toolhive-transport": "stdio", // Should be "stdio" even when proxied
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
				"toolhive-transport": "stdio", // Should be "stdio" even when proxied
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
	tmpVolumeDir := t.TempDir()
	volumes := []string{tmpVolumeDir + ":/container"}
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
		WithOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, "", oidcClientID, "", "", "", "", false, false, nil),
		WithTelemetryConfigFromFlags("", false, false, false, "", 0.1, nil, false, nil, false),
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

// TestRunConfigBuilder_OIDCScopes tests that OIDC scopes are correctly stored in OIDCConfig
func TestRunConfigBuilder_OIDCScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		scopes         []string
		expectedScopes []string
	}{
		{
			name:           "standard OIDC scopes",
			scopes:         []string{"openid", "profile", "email"},
			expectedScopes: []string{"openid", "profile", "email"},
		},
		{
			name:           "custom scopes",
			scopes:         []string{"openid", "api:read", "api:write"},
			expectedScopes: []string{"openid", "api:read", "api:write"},
		},
		{
			name:           "single scope",
			scopes:         []string{"openid"},
			expectedScopes: []string{"openid"},
		},
		{
			name:           "nil scopes",
			scopes:         nil,
			expectedScopes: nil,
		},
		{
			name:           "empty scopes",
			scopes:         []string{},
			expectedScopes: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runtime := &runtimemocks.MockRuntime{}
			validator := &mockEnvVarValidator{}

			config, err := NewRunConfigBuilder(context.Background(), nil, nil, validator,
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
				WithTransportAndPorts("sse", 0, 9000),
				WithAuditEnabled(false, ""),
				WithLabels(nil),
				WithGroup(""),
				WithOIDCConfig(
					"https://issuer.example.com",
					"test-audience",
					"https://issuer.example.com/.well-known/jwks.json",
					"",
					"test-client-id",
					"",
					"",
					"",
					"",
					false,
					false,
					tt.scopes,
				),
				WithTelemetryConfigFromFlags("", false, false, false, "", 0, nil, false, nil, false),
				WithToolsFilter(nil),
				WithIgnoreConfig(&ignore.Config{
					LoadGlobal:    false,
					PrintOverlays: false,
				}),
			)

			require.NoError(t, err)
			require.NotNil(t, config.OIDCConfig, "OIDCConfig should be initialized")
			assert.Equal(t, tt.expectedScopes, config.OIDCConfig.Scopes, "OIDCConfig.Scopes should match expected values")
		})
	}
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
		HeaderForward: &HeaderForwardConfig{
			AddPlaintextHeaders:  map[string]string{"X-Static": "static-val"},
			AddHeadersFromSecret: map[string]string{"X-Secret": "my-secret-name"},
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
	require.NotNil(t, readConfig.HeaderForward, "HeaderForward should not be nil")
	assert.Equal(t, originalConfig.HeaderForward.AddPlaintextHeaders, readConfig.HeaderForward.AddPlaintextHeaders, "AddPlaintextHeaders should match")
	assert.Equal(t, originalConfig.HeaderForward.AddHeadersFromSecret, readConfig.HeaderForward.AddHeadersFromSecret, "AddHeadersFromSecret should match")
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
				WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false, nil),
				WithTelemetryConfigFromFlags("", false, false, false, "", 0, nil, false, nil, false),
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
		WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false, nil),
		WithTelemetryConfigFromFlags("", false, false, false, "", 0, nil, false, nil, false),
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
		WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false, nil),
		WithTelemetryConfigFromFlags("", false, false, false, "", 0, nil, false, nil, false),
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
		WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false, nil),
		WithTelemetryConfigFromFlags("", false, false, false, "", 0, nil, false, nil, false),
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

	runtime := &runtimemocks.MockRuntime{}
	validator := &mockEnvVarValidator{}

	tmpReadDir := t.TempDir()
	tmpWriteDir := t.TempDir()
	volumes := []string{
		tmpReadDir + ":/container/read:ro",
		tmpWriteDir + ":/container/write",
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
		WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false, nil),
		WithTelemetryConfigFromFlags("", false, false, false, "", 0, nil, false, nil, false),
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
		WithOIDCConfig("", "", "", "", "", "", "", "", "", false, false, nil),
		WithTelemetryConfigFromFlags("", false, false, false, "", 0, nil, false, nil, false),
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
				SamplingRate:         "0.5",
				EnvironmentVariables: []string{"NODE_ENV", "DEPLOYMENT_ENV", "SERVICE_VERSION"},
			},
		}

		// Verify telemetry config exists with environment variables
		require.NotNil(t, config.TelemetryConfig, "TelemetryConfig should exist")
		assert.Equal(t, "http://file-based-endpoint:4318", config.TelemetryConfig.Endpoint)
		assert.Equal(t, "file-based-service", config.TelemetryConfig.ServiceName)
		assert.True(t, config.TelemetryConfig.TracingEnabled)
		assert.False(t, config.TelemetryConfig.MetricsEnabled)
		assert.Equal(t, "0.5", config.TelemetryConfig.SamplingRate)
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
		if config.TelemetryConfig != nil {
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
		if nilConfig != nil {
			extractedEnvVars = nilConfig.TelemetryConfig.EnvironmentVariables
		}
		assert.Nil(t, extractedEnvVars, "Should handle nil config gracefully")

		// Test with config that has nil telemetry config
		configWithNilTelemetry := &RunConfig{
			Transport: types.TransportTypeStdio,
		}
		var extractedFromNilTelemetry []string
		if configWithNilTelemetry.TelemetryConfig != nil {
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

// TestRunConfig_WithPorts_PortReuse tests the port reuse logic when updating workloads
//
//nolint:tparallel,paralleltest // Subtests intentionally run sequentially to share the same listener
func TestRunConfig_WithPorts_PortReuse(t *testing.T) {
	t.Parallel()

	// Create a listener to occupy a port for the entire test
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "Should be able to create listener")
	defer listener.Close()

	usedPort := listener.Addr().(*net.TCPAddr).Port

	t.Run("Reuse same port during update - should skip validation", func(t *testing.T) {
		config := &RunConfig{
			Transport:    types.TransportTypeStdio,
			existingPort: usedPort,
		}
		result, err := config.WithPorts(usedPort, 0)

		assert.NoError(t, err, "When updating a workload and reusing the same port, validation should be skipped")
		assert.Equal(t, config, result, "WithPorts should return the same config instance")
		assert.Equal(t, usedPort, config.Port, "Port should be set to requested port")
	})

	t.Run("Different port during update - should validate", func(t *testing.T) {
		config := &RunConfig{
			Transport:    types.TransportTypeStdio,
			existingPort: 8888, // Different from the port we're requesting
		}
		result, err := config.WithPorts(usedPort, 0)

		assert.Error(t, err, "When updating with a different port, validation should still occur and fail if port is in use")
		assert.Contains(t, err.Error(), "not available", "Error should indicate port is not available")
		assert.Equal(t, config, result, "WithPorts returns config even on error")
	})

	t.Run("No existing port - should validate normally", func(t *testing.T) {
		config := &RunConfig{
			Transport:    types.TransportTypeStdio,
			existingPort: 0,
		}
		result, err := config.WithPorts(usedPort, 0)

		assert.Error(t, err, "When creating new workload (no existing port), validation should occur normally")
		assert.Contains(t, err.Error(), "not available", "Error should indicate port is not available")
		assert.Equal(t, config, result, "WithPorts returns config even on error")
	})

	t.Run("Reuse existing port with value 0 should still work", func(t *testing.T) {
		config := &RunConfig{
			Transport:    types.TransportTypeStdio,
			existingPort: 0,
		}
		result, err := config.WithPorts(0, 0)

		assert.NoError(t, err, "Port 0 should still auto-select a port")
		assert.Equal(t, config, result, "WithPorts should return the same config instance")
		assert.Greater(t, config.Port, 0, "Port should be auto-selected")
	})
}

func TestHeaderForwardConfig_HasHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config *HeaderForwardConfig
		want   bool
	}{
		{
			name:   "nil receiver",
			config: nil,
			want:   false,
		},
		{
			name:   "empty struct",
			config: &HeaderForwardConfig{},
			want:   false,
		},
		{
			name:   "empty maps",
			config: &HeaderForwardConfig{AddPlaintextHeaders: map[string]string{}, AddHeadersFromSecret: map[string]string{}},
			want:   false,
		},
		{
			name:   "plaintext only",
			config: &HeaderForwardConfig{AddPlaintextHeaders: map[string]string{"X-Key": "val"}},
			want:   true,
		},
		{
			name:   "secret only",
			config: &HeaderForwardConfig{AddHeadersFromSecret: map[string]string{"X-Key": "secret-name"}},
			want:   true,
		},
		{
			name: "both set",
			config: &HeaderForwardConfig{
				AddPlaintextHeaders:  map[string]string{"X-A": "a"},
				AddHeadersFromSecret: map[string]string{"X-B": "secret-b"},
			},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.config.HasHeaders())
		})
	}
}

func TestRunConfig_resolveHeaderForwardSecrets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		headerForward *HeaderForwardConfig
		mockSecrets   map[string]string
		mockErrors    map[string]error
		wantErr       bool
		wantResolved  map[string]string
		wantPlaintext map[string]string // verifies AddPlaintextHeaders is NOT mutated
	}{
		{
			name:          "nil HeaderForward",
			headerForward: nil,
			wantErr:       false,
		},
		{
			name:          "empty AddHeadersFromSecret",
			headerForward: &HeaderForwardConfig{AddHeadersFromSecret: map[string]string{}},
			wantErr:       false,
		},
		{
			name: "single secret resolved",
			headerForward: &HeaderForwardConfig{
				AddHeadersFromSecret: map[string]string{"Authorization": "my-api-key"},
			},
			mockSecrets:  map[string]string{"my-api-key": "Bearer token123"},
			wantErr:      false,
			wantResolved: map[string]string{"Authorization": "Bearer token123"},
		},
		{
			name: "multiple secrets",
			headerForward: &HeaderForwardConfig{
				AddHeadersFromSecret: map[string]string{
					"X-Api-Key": "api-secret",
					"X-Token":   "token-secret",
				},
			},
			mockSecrets: map[string]string{
				"api-secret":   "key-value",
				"token-secret": "token-value",
			},
			wantErr:      false,
			wantResolved: map[string]string{"X-Api-Key": "key-value", "X-Token": "token-value"},
		},
		{
			name: "secret resolution error",
			headerForward: &HeaderForwardConfig{
				AddHeadersFromSecret: map[string]string{"X-Key": "missing-secret"},
			},
			mockErrors: map[string]error{"missing-secret": fmt.Errorf("secret not found")},
			wantErr:    true,
		},
		{
			name: "merges into existing plaintext headers without mutating them",
			headerForward: &HeaderForwardConfig{
				AddPlaintextHeaders:  map[string]string{"X-Existing": "existing-value"},
				AddHeadersFromSecret: map[string]string{"X-New": "new-secret"},
			},
			mockSecrets:   map[string]string{"new-secret": "resolved-value"},
			wantErr:       false,
			wantResolved:  map[string]string{"X-Existing": "existing-value", "X-New": "resolved-value"},
			wantPlaintext: map[string]string{"X-Existing": "existing-value"},
		},
		{
			name: "secret overrides plaintext for same header name",
			headerForward: &HeaderForwardConfig{
				AddPlaintextHeaders:  map[string]string{"X-Auth": "plaintext"},
				AddHeadersFromSecret: map[string]string{"X-Auth": "auth-secret"},
			},
			mockSecrets:  map[string]string{"auth-secret": "secret-value"},
			wantResolved: map[string]string{"X-Auth": "secret-value"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)

			secretManager := secretsmocks.NewMockProvider(ctrl)
			for name, val := range tc.mockSecrets {
				secretManager.EXPECT().GetSecret(gomock.Any(), name).Return(val, nil)
			}
			for name, retErr := range tc.mockErrors {
				secretManager.EXPECT().GetSecret(gomock.Any(), name).Return("", retErr)
			}

			cfg := &RunConfig{HeaderForward: tc.headerForward}
			err := cfg.resolveHeaderForwardSecrets(context.Background(), secretManager)

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.wantResolved != nil {
				require.NotNil(t, cfg.HeaderForward)
				assert.Equal(t, tc.wantResolved, cfg.HeaderForward.ResolvedHeaders())
			}
			if tc.wantPlaintext != nil {
				assert.Equal(t, tc.wantPlaintext, cfg.HeaderForward.AddPlaintextHeaders,
					"AddPlaintextHeaders should not be mutated by secret resolution")
			}
		})
	}
}

// TestWithExistingPort tests the WithExistingPort builder option
func TestWithExistingPort(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		existingPort int
		expected     int
	}{
		{
			name:         "Set existing port to valid value",
			existingPort: 8080,
			expected:     8080,
		},
		{
			name:         "Set existing port to 0",
			existingPort: 0,
			expected:     0,
		},
		{
			name:         "Set existing port to high port",
			existingPort: 65535,
			expected:     65535,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			builder := &runConfigBuilder{
				config: &RunConfig{},
			}

			option := WithExistingPort(tc.existingPort)
			err := option(builder)

			assert.NoError(t, err, "WithExistingPort should not return an error")
			assert.Equal(t, tc.expected, builder.config.existingPort, "existingPort should be set correctly")
		})
	}
}

// TestWithEmbeddedAuthServerConfig tests the WithEmbeddedAuthServerConfig builder option
func TestWithEmbeddedAuthServerConfig(t *testing.T) {
	t.Parallel()

	t.Run("sets embedded auth server config", func(t *testing.T) {
		t.Parallel()

		authConfig := &authserver.RunConfig{
			SchemaVersion: authserver.CurrentSchemaVersion,
			Issuer:        "https://auth.example.com",
			SigningKeyConfig: &authserver.SigningKeyRunConfig{
				KeyDir:         "/etc/keys",
				SigningKeyFile: "key-0.pem",
			},
			HMACSecretFiles: []string{"/etc/hmac/hmac-0"},
			TokenLifespans: &authserver.TokenLifespanRunConfig{
				AccessTokenLifespan:  "1h",
				RefreshTokenLifespan: "168h",
				AuthCodeLifespan:     "10m",
			},
			Upstreams: []authserver.UpstreamRunConfig{
				{
					Name: "okta",
					Type: authserver.UpstreamProviderTypeOIDC,
					OIDCConfig: &authserver.OIDCUpstreamRunConfig{
						IssuerURL:          "https://okta.example.com",
						ClientID:           "client-id",
						ClientSecretEnvVar: "UPSTREAM_CLIENT_SECRET",
						Scopes:             []string{"openid", "profile"},
					},
				},
			},
			AllowedAudiences: []string{"https://api.example.com"},
		}

		builder := &runConfigBuilder{
			config: &RunConfig{},
		}

		option := WithEmbeddedAuthServerConfig(authConfig)
		err := option(builder)

		assert.NoError(t, err, "WithEmbeddedAuthServerConfig should not return an error")
		assert.Equal(t, authConfig, builder.config.EmbeddedAuthServerConfig, "EmbeddedAuthServerConfig should be set correctly")
	})

	t.Run("sets nil embedded auth server config", func(t *testing.T) {
		t.Parallel()

		builder := &runConfigBuilder{
			config: &RunConfig{},
		}

		option := WithEmbeddedAuthServerConfig(nil)
		err := option(builder)

		assert.NoError(t, err, "WithEmbeddedAuthServerConfig should not return an error for nil config")
		assert.Nil(t, builder.config.EmbeddedAuthServerConfig, "EmbeddedAuthServerConfig should be nil")
	})
}

// TestRunConfig_WriteJSON_ReadJSON_EmbeddedAuthServer tests serialization of EmbeddedAuthServerConfig
func TestRunConfig_WriteJSON_ReadJSON_EmbeddedAuthServer(t *testing.T) {
	t.Parallel()

	t.Run("serializes and deserializes with embedded auth server config", func(t *testing.T) {
		t.Parallel()

		originalConfig := &RunConfig{
			SchemaVersion: CurrentSchemaVersion,
			Name:          "test-server",
			Image:         "test-image:latest",
			Transport:     types.TransportTypeSSE,
			Port:          60000,
			TargetPort:    60001,
			EmbeddedAuthServerConfig: &authserver.RunConfig{
				SchemaVersion: authserver.CurrentSchemaVersion,
				Issuer:        "https://auth.example.com",
				SigningKeyConfig: &authserver.SigningKeyRunConfig{
					KeyDir:           "/etc/toolhive/authserver/keys",
					SigningKeyFile:   "key-0.pem",
					FallbackKeyFiles: []string{"key-1.pem", "key-2.pem"},
				},
				HMACSecretFiles: []string{
					"/etc/toolhive/authserver/hmac/hmac-0",
					"/etc/toolhive/authserver/hmac/hmac-1",
				},
				TokenLifespans: &authserver.TokenLifespanRunConfig{
					AccessTokenLifespan:  "30m",
					RefreshTokenLifespan: "168h",
					AuthCodeLifespan:     "5m",
				},
				Upstreams: []authserver.UpstreamRunConfig{
					{
						Name: "github",
						Type: authserver.UpstreamProviderTypeOAuth2,
						OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "github-client-id",
							ClientSecretEnvVar:    "GITHUB_CLIENT_SECRET",
							RedirectURI:           "https://auth.example.com/oauth/callback",
							Scopes:                []string{"read:user", "user:email"},
							UserInfo: &authserver.UserInfoRunConfig{
								EndpointURL: "https://api.github.com/user",
								HTTPMethod:  "GET",
								AdditionalHeaders: map[string]string{
									"Accept": "application/vnd.github.v3+json",
								},
								FieldMapping: &authserver.UserInfoFieldMappingRunConfig{
									SubjectFields: []string{"id", "login"},
									NameFields:    []string{"name", "login"},
									EmailFields:   []string{"email"},
								},
							},
						},
					},
				},
				ScopesSupported:  []string{"openid", "profile", "email"},
				AllowedAudiences: []string{"https://api.example.com", "https://mcp.example.com"},
			},
		}

		// Write the config to a buffer
		var buf bytes.Buffer
		err := originalConfig.WriteJSON(&buf)
		require.NoError(t, err, "WriteJSON should not return an error")

		// Read the config from the buffer
		readConfig, err := ReadJSON(&buf)
		require.NoError(t, err, "ReadJSON should not return an error")

		// Verify top-level fields
		assert.Equal(t, originalConfig.Name, readConfig.Name, "Name should match")
		assert.Equal(t, originalConfig.Image, readConfig.Image, "Image should match")
		assert.Equal(t, originalConfig.Transport, readConfig.Transport, "Transport should match")

		// Verify embedded auth server config
		require.NotNil(t, readConfig.EmbeddedAuthServerConfig, "EmbeddedAuthServerConfig should not be nil")
		authConfig := readConfig.EmbeddedAuthServerConfig

		assert.Equal(t, authserver.CurrentSchemaVersion, authConfig.SchemaVersion, "Schema version should match")
		assert.Equal(t, "https://auth.example.com", authConfig.Issuer, "Issuer should match")

		// Verify signing key config
		require.NotNil(t, authConfig.SigningKeyConfig, "SigningKeyConfig should not be nil")
		assert.Equal(t, "/etc/toolhive/authserver/keys", authConfig.SigningKeyConfig.KeyDir, "KeyDir should match")
		assert.Equal(t, "key-0.pem", authConfig.SigningKeyConfig.SigningKeyFile, "SigningKeyFile should match")
		assert.Equal(t, []string{"key-1.pem", "key-2.pem"}, authConfig.SigningKeyConfig.FallbackKeyFiles, "FallbackKeyFiles should match")

		// Verify HMAC secret files
		assert.Equal(t, []string{
			"/etc/toolhive/authserver/hmac/hmac-0",
			"/etc/toolhive/authserver/hmac/hmac-1",
		}, authConfig.HMACSecretFiles, "HMACSecretFiles should match")

		// Verify token lifespans
		require.NotNil(t, authConfig.TokenLifespans, "TokenLifespans should not be nil")
		assert.Equal(t, "30m", authConfig.TokenLifespans.AccessTokenLifespan, "AccessTokenLifespan should match")
		assert.Equal(t, "168h", authConfig.TokenLifespans.RefreshTokenLifespan, "RefreshTokenLifespan should match")
		assert.Equal(t, "5m", authConfig.TokenLifespans.AuthCodeLifespan, "AuthCodeLifespan should match")

		// Verify upstreams
		require.Len(t, authConfig.Upstreams, 1, "Should have one upstream")
		upstream := authConfig.Upstreams[0]
		assert.Equal(t, "github", upstream.Name, "Upstream name should match")
		assert.Equal(t, authserver.UpstreamProviderTypeOAuth2, upstream.Type, "Upstream type should match")

		require.NotNil(t, upstream.OAuth2Config, "OAuth2Config should not be nil")
		assert.Equal(t, "https://github.com/login/oauth/authorize", upstream.OAuth2Config.AuthorizationEndpoint)
		assert.Equal(t, "github-client-id", upstream.OAuth2Config.ClientID)
		assert.Equal(t, "GITHUB_CLIENT_SECRET", upstream.OAuth2Config.ClientSecretEnvVar)

		require.NotNil(t, upstream.OAuth2Config.UserInfo, "UserInfo should not be nil")
		assert.Equal(t, "https://api.github.com/user", upstream.OAuth2Config.UserInfo.EndpointURL)
		assert.Equal(t, "GET", upstream.OAuth2Config.UserInfo.HTTPMethod)
		assert.Equal(t, map[string]string{"Accept": "application/vnd.github.v3+json"}, upstream.OAuth2Config.UserInfo.AdditionalHeaders)

		require.NotNil(t, upstream.OAuth2Config.UserInfo.FieldMapping, "FieldMapping should not be nil")
		assert.Equal(t, []string{"id", "login"}, upstream.OAuth2Config.UserInfo.FieldMapping.SubjectFields)

		// Verify scopes and audiences
		assert.Equal(t, []string{"openid", "profile", "email"}, authConfig.ScopesSupported, "ScopesSupported should match")
		assert.Equal(t, []string{"https://api.example.com", "https://mcp.example.com"}, authConfig.AllowedAudiences, "AllowedAudiences should match")
	})

	t.Run("serializes and deserializes with OIDC upstream", func(t *testing.T) {
		t.Parallel()

		originalConfig := &RunConfig{
			SchemaVersion: CurrentSchemaVersion,
			Name:          "oidc-server",
			EmbeddedAuthServerConfig: &authserver.RunConfig{
				SchemaVersion: authserver.CurrentSchemaVersion,
				Issuer:        "https://auth.example.com",
				Upstreams: []authserver.UpstreamRunConfig{
					{
						Name: "okta",
						Type: authserver.UpstreamProviderTypeOIDC,
						OIDCConfig: &authserver.OIDCUpstreamRunConfig{
							IssuerURL:        "https://okta.example.com",
							ClientID:         "okta-client-id",
							ClientSecretFile: "/etc/secrets/client-secret",
							RedirectURI:      "https://auth.example.com/oauth/callback",
							Scopes:           []string{"openid", "profile", "email"},
							UserInfoOverride: &authserver.UserInfoRunConfig{
								EndpointURL: "https://okta.example.com/oauth2/v1/userinfo",
								HTTPMethod:  "GET",
							},
						},
					},
				},
				AllowedAudiences: []string{"https://api.example.com"},
			},
		}

		var buf bytes.Buffer
		err := originalConfig.WriteJSON(&buf)
		require.NoError(t, err, "WriteJSON should not return an error")

		readConfig, err := ReadJSON(&buf)
		require.NoError(t, err, "ReadJSON should not return an error")

		require.NotNil(t, readConfig.EmbeddedAuthServerConfig, "EmbeddedAuthServerConfig should not be nil")
		require.Len(t, readConfig.EmbeddedAuthServerConfig.Upstreams, 1, "Should have one upstream")

		upstream := readConfig.EmbeddedAuthServerConfig.Upstreams[0]
		assert.Equal(t, "okta", upstream.Name)
		assert.Equal(t, authserver.UpstreamProviderTypeOIDC, upstream.Type)
		require.NotNil(t, upstream.OIDCConfig, "OIDCConfig should not be nil")
		assert.Equal(t, "https://okta.example.com", upstream.OIDCConfig.IssuerURL)
		assert.Equal(t, "/etc/secrets/client-secret", upstream.OIDCConfig.ClientSecretFile)
		require.NotNil(t, upstream.OIDCConfig.UserInfoOverride, "UserInfoOverride should not be nil")
	})

	t.Run("serializes and deserializes with nil embedded auth server config", func(t *testing.T) {
		t.Parallel()

		originalConfig := &RunConfig{
			SchemaVersion:            CurrentSchemaVersion,
			Name:                     "no-auth-server",
			EmbeddedAuthServerConfig: nil,
		}

		var buf bytes.Buffer
		err := originalConfig.WriteJSON(&buf)
		require.NoError(t, err, "WriteJSON should not return an error")

		readConfig, err := ReadJSON(&buf)
		require.NoError(t, err, "ReadJSON should not return an error")

		assert.Nil(t, readConfig.EmbeddedAuthServerConfig, "EmbeddedAuthServerConfig should be nil")
	})

	t.Run("serializes and deserializes minimal embedded auth server config", func(t *testing.T) {
		t.Parallel()

		// Minimal config with just required fields
		originalConfig := &RunConfig{
			SchemaVersion: CurrentSchemaVersion,
			Name:          "minimal-auth-server",
			EmbeddedAuthServerConfig: &authserver.RunConfig{
				SchemaVersion:    authserver.CurrentSchemaVersion,
				Issuer:           "https://auth.example.com",
				AllowedAudiences: []string{"https://api.example.com"},
			},
		}

		var buf bytes.Buffer
		err := originalConfig.WriteJSON(&buf)
		require.NoError(t, err, "WriteJSON should not return an error")

		readConfig, err := ReadJSON(&buf)
		require.NoError(t, err, "ReadJSON should not return an error")

		require.NotNil(t, readConfig.EmbeddedAuthServerConfig, "EmbeddedAuthServerConfig should not be nil")
		assert.Equal(t, "https://auth.example.com", readConfig.EmbeddedAuthServerConfig.Issuer)
		assert.Equal(t, []string{"https://api.example.com"}, readConfig.EmbeddedAuthServerConfig.AllowedAudiences)

		// Optional fields should be nil/empty
		assert.Nil(t, readConfig.EmbeddedAuthServerConfig.SigningKeyConfig)
		assert.Nil(t, readConfig.EmbeddedAuthServerConfig.HMACSecretFiles)
		assert.Nil(t, readConfig.EmbeddedAuthServerConfig.TokenLifespans)
		assert.Nil(t, readConfig.EmbeddedAuthServerConfig.Upstreams)
	})
}

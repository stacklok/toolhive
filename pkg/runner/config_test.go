package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authz"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// mockRuntime implements the Runtime interface for testing
type mockRuntime struct{}

func (*mockRuntime) DeployWorkload(
	_ context.Context,
	_, _ string,
	_ []string,
	_, _ map[string]string,
	_ *permissions.Profile,
	_ string,
	_ *rt.DeployWorkloadOptions,
) (string, error) {
	return "container-id", nil
}

func (*mockRuntime) StartContainer(_ context.Context, _ string) error {
	return nil
}

func (*mockRuntime) ListWorkloads(_ context.Context) ([]rt.ContainerInfo, error) {
	return []rt.ContainerInfo{}, nil
}

func (*mockRuntime) StopWorkload(_ context.Context, _ string) error {
	return nil
}

func (*mockRuntime) RemoveWorkload(_ context.Context, _ string) error {
	return nil
}

func (*mockRuntime) GetWorkloadLogs(_ context.Context, _ string, _ bool) (string, error) {
	return "", nil
}

func (*mockRuntime) IsWorkloadRunning(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func (*mockRuntime) GetWorkloadInfo(_ context.Context, _ string) (rt.ContainerInfo, error) {
	return rt.ContainerInfo{}, nil
}

func (*mockRuntime) AttachToWorkload(_ context.Context, _ string) (io.WriteCloser, io.ReadCloser, error) {
	return nil, nil, nil
}

func (*mockRuntime) ImageExists(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func (*mockRuntime) PullImage(_ context.Context, _ string) error {
	return nil
}

func (*mockRuntime) VerifyImage(_ context.Context, _ *registry.Server, _ string) (bool, error) {
	return true, nil
}

func (*mockRuntime) BuildImage(_ context.Context, _, _ string) error {
	return nil
}

func (*mockRuntime) Name() string {
	return "mock"
}

func (*mockRuntime) String() string {
	return "mock"
}

func TestNewRunConfig(t *testing.T) {
	config := NewRunConfig()
	assert.NotNil(t, config, "NewRunConfig should return a non-nil config")
	assert.NotNil(t, config.ContainerLabels, "ContainerLabels should be initialized")
	assert.NotNil(t, config.EnvVars, "EnvVars should be initialized")
}

func TestRunConfig_WithTransport(t *testing.T) {
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
			name:        "Invalid transport",
			transport:   "invalid",
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := NewRunConfig()

			result, err := config.WithTransport(tc.transport)

			if tc.expectError {
				assert.Error(t, err, "WithTransport should return an error for invalid transport")
				assert.Contains(t, err.Error(), "invalid transport mode", "Error message should mention invalid transport")
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
			port:        8000,
			targetPort:  9000,
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
			name:        "Stdio transport with specific port",
			config:      &RunConfig{Transport: types.TransportTypeStdio},
			port:        8000,
			targetPort:  9000, // This should be ignored for stdio
			expectError: false,
		},
	}

	logger.Initialize()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.config.WithPorts(tc.port, tc.targetPort)

			if tc.expectError {
				assert.Error(t, err, "WithPorts should return an error")
			} else {
				assert.NoError(t, err, "WithPorts should not return an error")
				assert.Equal(t, tc.config, result, "WithPorts should return the same config instance")

				if tc.port == 0 {
					assert.Greater(t, tc.config.Port, 0, "Port should be auto-selected")
				} else {
					assert.Equal(t, tc.port, tc.config.Port, "Port should be set correctly")
				}

				if tc.config.Transport == types.TransportTypeSSE {
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

func TestRunConfig_ParsePermissionProfile(t *testing.T) {
	testCases := []struct {
		name                   string
		profileNameOrPath      string
		expectError            bool
		expectedProfileType    string
		createTempProfileFile  bool
		tempProfileFileContent string
	}{
		{
			name:              "Empty profile name",
			profileNameOrPath: "",
			expectError:       true,
		},
		{
			name:                "None built-in profile",
			profileNameOrPath:   permissions.ProfileNone,
			expectError:         false,
			expectedProfileType: "none",
		},
		{
			name:                "Network built-in profile",
			profileNameOrPath:   permissions.ProfileNetwork,
			expectError:         false,
			expectedProfileType: "network",
		},
		{
			name:                  "Custom profile from file",
			profileNameOrPath:     "custom-profile.json", // Will be replaced with temp file path
			expectError:           false,
			expectedProfileType:   "custom",
			createTempProfileFile: true,
			tempProfileFileContent: `{
				"read": ["file:///tmp/test-read"],
				"write": ["file:///tmp/test-write"],
				"network": {
					"outbound": {
						"insecure_allow_all": false,
						"allow_host": ["localhost"],
						"allow_port": [8080]
					}
				}
			}`,
		},
		{
			name:              "Non-existent profile file",
			profileNameOrPath: "/non/existent/file.json",
			expectError:       true,
		},
		{
			name:                  "Invalid JSON in profile file",
			profileNameOrPath:     "invalid-json.json", // Will be replaced with temp file path
			expectError:           true,
			createTempProfileFile: true,
			tempProfileFileContent: `{
				"read": ["file:///tmp/test-read"],
				"write": ["file:///tmp/test-write"],
				"network": "invalid-network-format"
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := NewRunConfig()

			// Create a temporary profile file if needed
			if tc.createTempProfileFile {
				tempFile, err := os.CreateTemp("", "profile-*.json")
				require.NoError(t, err, "Failed to create temporary file")
				defer os.Remove(tempFile.Name())

				_, err = tempFile.WriteString(tc.tempProfileFileContent)
				require.NoError(t, err, "Failed to write to temporary file")
				tempFile.Close()

				// Replace the profile path with the actual temp file path
				config.PermissionProfileNameOrPath = tempFile.Name()
			} else {
				config.PermissionProfileNameOrPath = tc.profileNameOrPath
			}

			result, err := config.ParsePermissionProfile()

			if tc.expectError {
				assert.Error(t, err, "ParsePermissionProfile should return an error")
			} else {
				assert.NoError(t, err, "ParsePermissionProfile should not return an error")
				assert.Equal(t, config, result, "ParsePermissionProfile should return the same config instance")
				assert.NotNil(t, config.PermissionProfile, "PermissionProfile should be set")

				switch tc.expectedProfileType {
				case "none":
					// For none profile, check that network outbound is not allowed
					assert.False(t, config.PermissionProfile.Network.Outbound.InsecureAllowAll,
						"None profile should not allow all outbound network connections")
				case "network":
					// For network profile, check that network outbound is allowed
					assert.True(t, config.PermissionProfile.Network.Outbound.InsecureAllowAll,
						"Network profile should allow all outbound network connections")
				case "custom":
					// For custom profile, check that it has the expected read/write mounts
					assert.Len(t, config.PermissionProfile.Read, 1, "Custom profile should have 1 read mount")
					assert.Len(t, config.PermissionProfile.Write, 1, "Custom profile should have 1 write mount")
					assert.False(t, config.PermissionProfile.Network.Outbound.InsecureAllowAll,
						"Custom profile should not allow all outbound network connections")
					assert.Contains(t, config.PermissionProfile.Network.Outbound.AllowHost, "localhost",
						"Custom profile should allow localhost")
					assert.Contains(t, config.PermissionProfile.Network.Outbound.AllowPort, 8080,
						"Custom profile should allow port 8080")
				}
			}
		})
	}
}

func TestRunConfig_ProcessVolumeMounts(t *testing.T) {
	testCases := []struct {
		name                string
		config              *RunConfig
		expectError         bool
		expectedReadMounts  int
		expectedWriteMounts int
	}{
		{
			name: "No volumes",
			config: &RunConfig{
				Volumes: []string{},
			},
			expectError:         false,
			expectedReadMounts:  0,
			expectedWriteMounts: 0,
		},
		{
			name: "Volumes without permission profile and without profile name",
			config: &RunConfig{
				Volumes: []string{"/host:/container"},
			},
			expectError: true,
		},
		{
			name: "Volumes without permission profile but with profile name",
			config: &RunConfig{
				Volumes:                     []string{"/host:/container"},
				PermissionProfileNameOrPath: permissions.ProfileNone,
			},
			expectError:         false,
			expectedReadMounts:  0,
			expectedWriteMounts: 1,
		},
		{
			name: "Read-only volume with existing profile",
			config: &RunConfig{
				Volumes:           []string{"/host:/container:ro"},
				PermissionProfile: permissions.BuiltinNoneProfile(),
			},
			expectError:         false,
			expectedReadMounts:  1,
			expectedWriteMounts: 0,
		},
		{
			name: "Read-write volume with existing profile",
			config: &RunConfig{
				Volumes:           []string{"/host:/container"},
				PermissionProfile: permissions.BuiltinNoneProfile(),
			},
			expectError:         false,
			expectedReadMounts:  0,
			expectedWriteMounts: 1,
		},
		{
			name: "Multiple volumes with existing profile",
			config: &RunConfig{
				Volumes: []string{
					"/host1:/container1:ro",
					"/host2:/container2",
					"/host3:/container3:ro",
				},
				PermissionProfile: permissions.BuiltinNoneProfile(),
			},
			expectError:         false,
			expectedReadMounts:  2,
			expectedWriteMounts: 1,
		},
		{
			name: "Invalid volume format",
			config: &RunConfig{
				Volumes:           []string{"invalid:format:with:too:many:colons"},
				PermissionProfile: permissions.BuiltinNoneProfile(),
			},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Save original read/write mounts count
			var originalReadMounts, originalWriteMounts int
			if tc.config.PermissionProfile != nil {
				originalReadMounts = len(tc.config.PermissionProfile.Read)
				originalWriteMounts = len(tc.config.PermissionProfile.Write)
			}

			err := tc.config.ProcessVolumeMounts()

			if tc.expectError {
				assert.Error(t, err, "ProcessVolumeMounts should return an error")
			} else {
				assert.NoError(t, err, "ProcessVolumeMounts should not return an error")

				// For the "No volumes" case, we don't need to check the PermissionProfile
				if len(tc.config.Volumes) > 0 {
					assert.NotNil(t, tc.config.PermissionProfile, "PermissionProfile should be set")
					// Check read mounts
					assert.Equal(t, tc.expectedReadMounts, len(tc.config.PermissionProfile.Read)-originalReadMounts,
						"Number of read mounts should match expected")

					// Check write mounts
					assert.Equal(t, tc.expectedWriteMounts, len(tc.config.PermissionProfile.Write)-originalWriteMounts,
						"Number of write mounts should match expected")
				}
			}
		})
	}
}

func TestRunConfig_WithEnvironmentVariables(t *testing.T) {
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

// mockSecretManager is a mock implementation of a secret manager
type mockSecretManager struct {
	secrets map[string]string
}

func (m *mockSecretManager) GetSecret(name string) (string, error) {
	if value, ok := m.secrets[name]; ok {
		return value, nil
	}
	return "", fmt.Errorf("secret %s not found", name)
}

func (m *mockSecretManager) SetSecret(name, value string) error {
	m.secrets[name] = value
	return nil
}

func (m *mockSecretManager) DeleteSecret(name string) error {
	delete(m.secrets, name)
	return nil
}

func (m *mockSecretManager) ListSecrets() ([]string, error) {
	keys := make([]string, 0, len(m.secrets))
	for k := range m.secrets {
		keys = append(keys, k)
	}
	return keys, nil
}

func (*mockSecretManager) Cleanup() error {
	return nil
}

func TestRunConfig_WithSecrets(t *testing.T) {
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
			// Create a mock secret manager
			secretManager := &mockSecretManager{
				secrets: tc.mockSecrets,
			}

			// Set the secrets in the config
			tc.config.Secrets = tc.secrets

			// Call the function
			result, err := tc.config.WithSecrets(secretManager)

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
	config := NewRunConfig()
	authzConfig := &authz.Config{
		Version: "1.0",
		Type:    "cedar_v1",
	}

	result := config.WithAuthz(authzConfig)

	assert.Equal(t, config, result, "WithAuthz should return the same config instance")
	assert.Equal(t, authzConfig, config.AuthzConfig, "AuthzConfig should be set correctly")
}

func TestNewRunConfigFromFlags(t *testing.T) {
	runtime := &mockRuntime{}
	cmdArgs := []string{"arg1", "arg2"}
	name := "test-server"
	host := "localhost"
	debug := true
	volumes := []string{"/host:/container"}
	secretsList := []string{"secret1,target=ENV_VAR1"}
	authzConfigPath := "/path/to/authz.json"
	permissionProfile := permissions.ProfileNone
	targetHost := "localhost"
	oidcIssuer := "https://issuer.example.com"
	oidcAudience := "test-audience"
	oidcJwksURL := "https://issuer.example.com/.well-known/jwks.json"
	oidcClientID := "test-client"

	config := NewRunConfigFromFlags(
		runtime,
		cmdArgs,
		name,
		host,
		debug,
		volumes,
		secretsList,
		authzConfigPath,
		permissionProfile,
		targetHost,
		oidcIssuer,
		oidcAudience,
		oidcJwksURL,
		oidcClientID,
	)

	assert.NotNil(t, config, "NewRunConfigFromFlags should return a non-nil config")
	assert.Equal(t, runtime, config.Runtime, "Runtime should match")
	assert.Equal(t, targetHost, config.TargetHost, "TargetHost should match")
	assert.Equal(t, cmdArgs, config.CmdArgs, "CmdArgs should match")
	assert.Equal(t, name, config.Name, "Name should match")
	assert.Equal(t, debug, config.Debug, "Debug should match")
	assert.Equal(t, volumes, config.Volumes, "Volumes should match")
	assert.Equal(t, secretsList, config.Secrets, "Secrets should match")
	assert.Equal(t, authzConfigPath, config.AuthzConfigPath, "AuthzConfigPath should match")
	assert.Equal(t, permissionProfile, config.PermissionProfileNameOrPath, "PermissionProfileNameOrPath should match")
	assert.NotNil(t, config.ContainerLabels, "ContainerLabels should be initialized")
	assert.NotNil(t, config.EnvVars, "EnvVars should be initialized")

	// Check OIDC config
	assert.NotNil(t, config.OIDCConfig, "OIDCConfig should be initialized")
	assert.Equal(t, oidcIssuer, config.OIDCConfig.Issuer, "OIDCConfig.Issuer should match")
	assert.Equal(t, oidcAudience, config.OIDCConfig.Audience, "OIDCConfig.Audience should match")
	assert.Equal(t, oidcJwksURL, config.OIDCConfig.JWKSURL, "OIDCConfig.JWKSURL should match")
	assert.Equal(t, oidcClientID, config.OIDCConfig.ClientID, "OIDCConfig.ClientID should match")
}

func TestRunConfig_WriteJSON_ReadJSON(t *testing.T) {
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

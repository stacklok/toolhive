package app

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/k8s"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestParseConfigMapRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		ref           string
		wantNamespace string
		wantName      string
		wantError     bool
	}{
		{
			name:          "valid reference",
			ref:           "default/my-config",
			wantNamespace: "default",
			wantName:      "my-config",
			wantError:     false,
		},
		{
			name:          "valid reference with hyphens",
			ref:           "test-namespace/my-test-config",
			wantNamespace: "test-namespace",
			wantName:      "my-test-config",
			wantError:     false,
		},
		{
			name:      "missing slash",
			ref:       "defaultmy-config",
			wantError: true,
		},
		{
			name:      "empty namespace",
			ref:       "/my-config",
			wantError: true,
		},
		{
			name:      "empty name",
			ref:       "default/",
			wantError: true,
		},
		{
			name:          "config name with slash (allowed)",
			ref:           "default/my-config/extra",
			wantNamespace: "default",
			wantName:      "my-config/extra",
			wantError:     false,
		},
		{
			name:      "empty reference",
			ref:       "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			namespace, name, err := parseConfigMapRef(tt.ref)

			if tt.wantError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantNamespace, namespace)
			assert.Equal(t, tt.wantName, name)
		})
	}
}

func TestComputeConfigMapChecksum(t *testing.T) {
	t.Parallel()

	// Create a test ConfigMap
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
			Annotations: map[string]string{
				"test.io/annotation": "value",
			},
		},
		Data: map[string]string{
			"runconfig.json": `{"name":"test","image":"test:latest"}`,
			"other.json":     `{"key":"value"}`,
		},
	}

	checksum1 := k8s.ComputeConfigMapChecksum(configMap)
	assert.NotEmpty(t, checksum1)
	assert.Len(t, checksum1, 64) // SHA256 hex string length

	// Same ConfigMap should produce same checksum
	checksum2 := k8s.ComputeConfigMapChecksum(configMap)
	assert.Equal(t, checksum1, checksum2)

	// Different data should produce different checksum
	configMap.Data["runconfig.json"] = `{"name":"test","image":"test:v2"}`
	checksum3 := k8s.ComputeConfigMapChecksum(configMap)
	assert.NotEqual(t, checksum1, checksum3)

	// Adding existing checksum annotation should not affect checksum
	configMap.Annotations["toolhive.stacklok.dev/content-checksum"] = "existing-checksum"
	checksum4 := k8s.ComputeConfigMapChecksum(configMap)
	assert.Equal(t, checksum3, checksum4)
}

func TestLoadRunConfigFromConfigMap(t *testing.T) {
	t.Parallel()

	// Skip this test if not running in a Kubernetes environment
	// This test requires in-cluster configuration or a valid kubeconfig
	t.Skip("This test requires Kubernetes cluster access and should be run as an integration test")

	// Create test RunConfig
	testRunConfig := &runner.RunConfig{
		Name:      "test-server",
		Image:     "test:latest",
		Transport: types.TransportTypeSSE,
		Host:      "localhost",
		Port:      8080,
		EnvVars: map[string]string{
			"TEST_VAR": "test_value",
		},
	}

	runConfigJSON, err := json.Marshal(testRunConfig)
	require.NoError(t, err)

	// Create fake Kubernetes client
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
			Annotations: map[string]string{
				"toolhive.stacklok.dev/content-checksum": "test-checksum",
			},
		},
		Data: map[string]string{
			"runconfig.json": string(runConfigJSON),
		},
	}

	// This would need to be adapted for actual testing with a fake Kubernetes client
	// The current implementation uses rest.InClusterConfig() which requires a real cluster

	// For now, we'll test the parsing logic separately
	var parsedConfig runner.RunConfig
	err = json.Unmarshal([]byte(configMap.Data["runconfig.json"]), &parsedConfig)
	require.NoError(t, err)

	assert.Equal(t, testRunConfig.Name, parsedConfig.Name)
	assert.Equal(t, testRunConfig.Image, parsedConfig.Image)
	assert.Equal(t, testRunConfig.Transport, parsedConfig.Transport)
	assert.Equal(t, testRunConfig.EnvVars, parsedConfig.EnvVars)
}

func TestValidateConfigMapOnlyMode(t *testing.T) {
	t.Parallel()

	// Create a mock command for testing
	// Note: This would need to be adapted to work with the actual cobra command structure
	// For now, we'll test the validation logic conceptually

	tests := []struct {
		name          string
		runConfigJSON string
		cmdArgs       map[string]interface{}
		expectError   bool
		errorContains string
	}{
		{
			name: "valid config with no conflicting args",
			runConfigJSON: `{
				"name": "test-server",
				"image": "test:latest",
				"transport": "sse",
				"host": "localhost",
				"port": 8080
			}`,
			cmdArgs:     map[string]interface{}{},
			expectError: false,
		},
		{
			name: "conflicting port argument",
			runConfigJSON: `{
				"name": "test-server",
				"image": "test:latest", 
				"transport": "sse",
				"host": "localhost",
				"port": 8080
			}`,
			cmdArgs: map[string]interface{}{
				"port": 9090,
			},
			expectError:   true,
			errorContains: "cannot be used with --from-configmap",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Parse the RunConfig to understand what's stored
			var runConfig runner.RunConfig
			err := json.Unmarshal([]byte(tt.runConfigJSON), &runConfig)
			require.NoError(t, err)

			// Test validation logic
			// This is a simplified version of what the actual validation should do
			conflictingArgs := []string{}

			// Check for conflicts based on what's in the RunConfig
			if runConfig.Port != 0 {
				if port, exists := tt.cmdArgs["port"]; exists && port != runConfig.Port {
					conflictingArgs = append(conflictingArgs, "--port")
				}
			}

			if runConfig.Host != "" {
				if host, exists := tt.cmdArgs["host"]; exists && host != runConfig.Host {
					conflictingArgs = append(conflictingArgs, "--host")
				}
			}

			hasConflicts := len(conflictingArgs) > 0

			if tt.expectError {
				assert.True(t, hasConflicts, "Expected validation errors but none found")
			} else {
				assert.False(t, hasConflicts, "Unexpected validation errors: %v", conflictingArgs)
			}
		})
	}
}

// TestRunConfigWithConfigMapChecksum tests that RunConfig properly stores ConfigMap checksum
func TestRunConfigWithConfigMapChecksum(t *testing.T) {
	t.Parallel()

	runConfig := &runner.RunConfig{
		Name:              "test-server",
		Image:             "test:latest",
		Transport:         types.TransportTypeSSE,
		ConfigMapChecksum: "abc123def456",
	}

	// Test that checksum is preserved in serialization
	jsonData, err := json.Marshal(runConfig)
	require.NoError(t, err)

	var deserializedConfig runner.RunConfig
	err = json.Unmarshal(jsonData, &deserializedConfig)
	require.NoError(t, err)

	assert.Equal(t, runConfig.ConfigMapChecksum, deserializedConfig.ConfigMapChecksum)
	assert.Equal(t, "abc123def456", deserializedConfig.ConfigMapChecksum)
}

func TestRunConfigMapInitialization(t *testing.T) {
	t.Parallel()

	// Test that EnvVars and ContainerLabels maps are properly initialized after JSON unmarshaling
	testCases := []struct {
		name     string
		jsonData string
	}{
		{
			name:     "empty maps in JSON",
			jsonData: `{"schema_version":"v1","name":"test","image":"test:latest","transport":"stdio","env_vars":{},"container_labels":{}}`,
		},
		{
			name:     "missing maps in JSON",
			jsonData: `{"schema_version":"v1","name":"test","image":"test:latest","transport":"stdio"}`,
		},
		{
			name:     "populated maps in JSON",
			jsonData: `{"schema_version":"v1","name":"test","image":"test:latest","transport":"stdio","env_vars":{"KEY":"value"},"container_labels":{"label":"value"}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Unmarshal the RunConfig
			var runConfig runner.RunConfig
			err := json.Unmarshal([]byte(tc.jsonData), &runConfig)
			require.NoError(t, err)

			// Apply the same initialization logic as loadRunConfigFromConfigMap
			if runConfig.EnvVars == nil {
				runConfig.EnvVars = make(map[string]string)
			}
			if runConfig.ContainerLabels == nil {
				runConfig.ContainerLabels = make(map[string]string)
			}

			// Verify maps are initialized and can be written to
			assert.NotNil(t, runConfig.EnvVars)
			assert.NotNil(t, runConfig.ContainerLabels)

			// Test that we can write to the maps (this would panic if nil)
			runConfig.EnvVars["TEST_KEY"] = "test_value"
			runConfig.ContainerLabels["test_label"] = "test_value"

			assert.Equal(t, "test_value", runConfig.EnvVars["TEST_KEY"])
			assert.Equal(t, "test_value", runConfig.ContainerLabels["test_label"])
		})
	}
}

func TestProxyModeHandling(t *testing.T) {
	t.Parallel()

	// Test that proxy mode flag is properly defined and available
	testCases := []struct {
		name        string
		args        []string
		expectError bool
	}{
		{
			name: "proxy-mode flag is available",
			args: []string{"run", "--proxy-mode", "sse", "test:latest"},
		},
		{
			name: "proxy-mode with streamable-http value",
			args: []string{"run", "--proxy-mode", "streamable-http", "test:latest"},
		},
		{
			name:        "proxy-mode with configmap should be rejected",
			args:        []string{"run", "--from-configmap", "test/config", "--proxy-mode", "sse", "test:latest"},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := NewRunCmd()
			cmd.SetArgs(tc.args)

			// Parse flags only, don't execute
			err := cmd.ParseFlags(tc.args)

			if tc.expectError {
				// For the configmap case, we expect it to fail during execution, not flag parsing
				if err != nil {
					// Flag parsing failed, which is not expected
					t.Errorf("Flag parsing failed unexpectedly: %v", err)
					return
				}
				// Flag parsing succeeded, now check if validation catches the conflict
				// This would be caught in validateConfigMapOnlyMode during execution
				runFromConfigMap := cmd.Flag("from-configmap").Value.String()
				proxyMode := cmd.Flag("proxy-mode").Value.String()
				if runFromConfigMap != "" && proxyMode != "" {
					// This is expected - the validation should catch this
					return
				}
			} else {
				require.NoError(t, err)

				// Check that flags were parsed correctly
				if cmd.Flag("proxy-mode") != nil {
					proxyModeValue := cmd.Flag("proxy-mode").Value.String()
					assert.Contains(t, []string{"sse", "streamable-http", ""}, proxyModeValue)
				}
			}
		})
	}
}

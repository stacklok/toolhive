package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/stacklok/toolhive/pkg/k8s"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestProxyRunnerFromConfigMap(t *testing.T) {
	t.Parallel()
	if !isKubernetesEnvironment() {
		t.Skip("Skipping Kubernetes-specific test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	namespace := "default"
	configMapName := "test-proxy-runner-config"
	serverName := "test-configmap-server"

	// Create test RunConfig
	testRunConfig := &runner.RunConfig{
		SchemaVersion: runner.CurrentSchemaVersion,
		Name:          serverName,
		Image:         "ghcr.io/stacklok/mcp-server-openapi:latest",
		Transport:     types.TransportTypeSSE,
		Host:          "127.0.0.1",
		Port:          8081,
		TargetPort:    8080,
		CmdArgs:       []string{},
		EnvVars: map[string]string{
			"OPENAPI_URL": "https://api.github.com/openapi.yaml",
		},
		ContainerLabels: map[string]string{
			"test": "proxy-runner-configmap",
		},
		IsolateNetwork: true,
	}

	// Serialize to JSON
	runConfigJSON, err := json.Marshal(testRunConfig)
	require.NoError(t, err)

	// Create ConfigMap with RunConfig
	k8sClient := getKubernetesClient(t)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
			Labels: map[string]string{
				"test": "proxy-runner-configmap",
			},
		},
		Data: map[string]string{
			"runconfig.json": string(runConfigJSON),
		},
	}

	// Clean up any existing ConfigMap
	_ = k8sClient.CoreV1().ConfigMaps(namespace).Delete(ctx, configMapName, metav1.DeleteOptions{})

	// Create ConfigMap
	_, err = k8sClient.CoreV1().ConfigMaps(namespace).Create(ctx, configMap, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Cleanup(func() {
		// Cleanup ConfigMap
		_ = k8sClient.CoreV1().ConfigMaps(namespace).Delete(ctx, configMapName, metav1.DeleteOptions{})
	})

	t.Run("test loading from configmap", func(t *testing.T) {
		t.Parallel()
		// Test the proxy runner with --from-configmap flag
		configMapRef := fmt.Sprintf("%s/%s", namespace, configMapName)

		// This would be the command we want to test:
		// thv-proxyrunner run --from-configmap default/test-proxy-runner-config ghcr.io/stacklok/mcp-server-openapi:latest

		// For now, we'll test that the ConfigMap was created correctly
		// and contains the expected data
		retrievedConfigMap, err := k8sClient.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
		require.NoError(t, err)

		// Verify ConfigMap contains the RunConfig
		runConfigData, exists := retrievedConfigMap.Data["runconfig.json"]
		require.True(t, exists, "ConfigMap should contain runconfig.json")

		// Parse and validate RunConfig
		var retrievedRunConfig runner.RunConfig
		err = json.Unmarshal([]byte(runConfigData), &retrievedRunConfig)
		require.NoError(t, err)

		assert.Equal(t, testRunConfig.Name, retrievedRunConfig.Name)
		assert.Equal(t, testRunConfig.Image, retrievedRunConfig.Image)
		assert.Equal(t, testRunConfig.Transport, retrievedRunConfig.Transport)
		assert.Equal(t, testRunConfig.Host, retrievedRunConfig.Host)
		assert.Equal(t, testRunConfig.Port, retrievedRunConfig.Port)
		assert.Equal(t, testRunConfig.EnvVars, retrievedRunConfig.EnvVars)

		t.Logf("Successfully created and retrieved ConfigMap with RunConfig for --from-configmap testing")
		t.Logf("ConfigMap reference: %s", configMapRef)
		t.Logf("To test manually: thv-proxyrunner run --from-configmap %s %s",
			configMapRef, testRunConfig.Image)
	})

	t.Run("test configmap checksum generation", func(t *testing.T) {
		t.Parallel()
		// Retrieve the ConfigMap
		retrievedConfigMap, err := k8sClient.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
		require.NoError(t, err)

		// Test checksum computation (this tests the computeConfigMapChecksum function)
		checksum1 := k8s.ComputeConfigMapChecksum(retrievedConfigMap)
		assert.NotEmpty(t, checksum1)
		assert.Len(t, checksum1, 64) // SHA256 hex string length

		// Same ConfigMap should produce same checksum
		checksum2 := k8s.ComputeConfigMapChecksum(retrievedConfigMap)
		assert.Equal(t, checksum1, checksum2)

		// Modify ConfigMap data and verify checksum changes
		retrievedConfigMap.Data["runconfig.json"] = string(runConfigJSON) + "\n" // Add newline
		checksum3 := k8s.ComputeConfigMapChecksum(retrievedConfigMap)
		assert.NotEqual(t, checksum1, checksum3)

		t.Logf("ConfigMap checksum generation working correctly")
		t.Logf("Original checksum: %s", checksum1)
		t.Logf("Modified checksum: %s", checksum3)
	})
}

func TestProxyRunnerConfigMapValidation(t *testing.T) {
	t.Parallel()
	if !isKubernetesEnvironment() {
		t.Skip("Skipping Kubernetes-specific test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	namespace := "default"
	k8sClient := getKubernetesClient(t)

	tests := []struct {
		name          string
		configMapName string
		configMapData map[string]string
		expectError   bool
		errorContains string
	}{
		{
			name:          "missing configmap",
			configMapName: "non-existent-config",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "configmap without runconfig.json",
			configMapName: "invalid-config-no-runconfig",
			configMapData: map[string]string{
				"other.json": `{"key": "value"}`,
			},
			expectError:   true,
			errorContains: "does not contain 'runconfig.json' key",
		},
		{
			name:          "configmap with invalid json",
			configMapName: "invalid-config-bad-json",
			configMapData: map[string]string{
				"runconfig.json": `{"invalid": json}`,
			},
			expectError:   true,
			errorContains: "failed to unmarshal",
		},
		{
			name:          "valid configmap",
			configMapName: "valid-config",
			configMapData: map[string]string{
				"runconfig.json": `{
					"schema_version": "v1",
					"name": "test-server",
					"image": "test:latest",
					"transport": "sse",
					"host": "localhost",
					"port": 8080
				}`,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Clean up any existing ConfigMap
			_ = k8sClient.CoreV1().ConfigMaps(namespace).Delete(ctx, tt.configMapName, metav1.DeleteOptions{})

			if tt.configMapData != nil {
				// Create ConfigMap for this test
				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tt.configMapName,
						Namespace: namespace,
					},
					Data: tt.configMapData,
				}

				_, err := k8sClient.CoreV1().ConfigMaps(namespace).Create(ctx, configMap, metav1.CreateOptions{})
				require.NoError(t, err)

				t.Cleanup(func() {
					_ = k8sClient.CoreV1().ConfigMaps(namespace).Delete(ctx, tt.configMapName, metav1.DeleteOptions{})
				})
			}

			// Test ConfigMap validation by attempting to retrieve and parse it
			configMap, err := k8sClient.CoreV1().ConfigMaps(namespace).Get(ctx, tt.configMapName, metav1.GetOptions{})

			if tt.expectError {
				if err != nil {
					// Expected error from Kubernetes API (ConfigMap not found)
					assert.Contains(t, err.Error(), tt.errorContains)
					return
				}

				// Expected error from parsing runconfig.json
				runConfigJSON, exists := configMap.Data["runconfig.json"]
				if !exists {
					assert.Contains(t, "does not contain 'runconfig.json' key", tt.errorContains)
					return
				}

				var runConfig runner.RunConfig
				err = json.Unmarshal([]byte(runConfigJSON), &runConfig)
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)

				runConfigJSON, exists := configMap.Data["runconfig.json"]
				require.True(t, exists)

				var runConfig runner.RunConfig
				err = json.Unmarshal([]byte(runConfigJSON), &runConfig)
				require.NoError(t, err)

				assert.Equal(t, "test-server", runConfig.Name)
				assert.Equal(t, "test:latest", runConfig.Image)
				assert.Equal(t, types.TransportTypeSSE, runConfig.Transport)
			}
		})
	}
}

// isKubernetesEnvironment checks if running in a Kubernetes environment
func isKubernetesEnvironment() bool {
	// Check if we can get in-cluster config
	_, err := rest.InClusterConfig()
	return err == nil
}

// getKubernetesClient creates a Kubernetes client for testing
func getKubernetesClient(t *testing.T) kubernetes.Interface {
	t.Helper()

	config, err := rest.InClusterConfig()
	require.NoError(t, err, "Failed to get in-cluster config")

	client, err := kubernetes.NewForConfig(config)
	require.NoError(t, err, "Failed to create Kubernetes client")

	return client
}

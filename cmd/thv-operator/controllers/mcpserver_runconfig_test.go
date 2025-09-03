package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
)

// Test helper functions
func createTestMCPServerWithConfig(name, namespace, image string, env []mcpv1alpha1.EnvVar) *mcpv1alpha1.MCPServer {
	return &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     image,
			Transport: "stdio",
			Port:      8080,
			Env:       env,
		},
	}
}

func createRunConfigTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = mcpv1alpha1.AddToScheme(s)
	return s
}

func TestDeploymentForMCPServer_ConfigMapTimestampAnnotation(t *testing.T) {
	t.Parallel()
	mcpServer := createTestMCPServer("test-server", "default")
	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).Build()
	reconciler := &MCPServerReconciler{
		Client: fakeClient,
		Scheme: testScheme,
	}

	ctx := context.Background()
	timestamp := time.Now().Format(time.RFC3339)

	// Create ConfigMap with timestamp annotation
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server-runconfig",
			Namespace: "default",
			Annotations: map[string]string{
				"toolhive.stacklok.io/last-modified": timestamp,
			},
		},
		Data: map[string]string{
			"config.json": `{"image": "test-image", "transport": "stdio", "port": 8080}`,
		},
	}
	err := fakeClient.Create(ctx, configMap)
	require.NoError(t, err)

	// Generate deployment
	deployment := reconciler.deploymentForMCPServer(ctx, mcpServer)
	require.NotNil(t, deployment)

	// Verify timestamp annotation was propagated to pod template
	podAnnotations := deployment.Spec.Template.Annotations
	require.NotNil(t, podAnnotations)
	assert.Equal(t, timestamp, podAnnotations["toolhive.stacklok.io/configmap-timestamp"])
}

// TestRunConfigContentEquals tests the content comparison logic
func TestRunConfigContentEquals(t *testing.T) {
	t.Parallel()
	reconciler := &MCPServerReconciler{}

	baseConfigMap := &corev1.ConfigMap{
		Data: map[string]string{
			"runconfig.json": `{"name":"test","image":"test:v1"}`,
		},
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"toolhive.stacklok.io/component": "run-config",
			},
			Annotations: map[string]string{
				"toolhive.stacklok.io/last-modified": "2025-01-01T12:00:00Z",
				"other-annotation":                   "value",
			},
		},
	}

	tests := []struct {
		name     string
		current  *corev1.ConfigMap
		desired  *corev1.ConfigMap
		expected bool
	}{
		{
			name:     "identical content should be equal",
			current:  baseConfigMap.DeepCopy(),
			desired:  baseConfigMap.DeepCopy(),
			expected: true,
		},
		{
			name:    "different data should not be equal",
			current: baseConfigMap.DeepCopy(),
			desired: func() *corev1.ConfigMap {
				cm := baseConfigMap.DeepCopy()
				cm.Data["runconfig.json"] = `{"name":"test","image":"test:v2"}`
				return cm
			}(),
			expected: false,
		},
		{
			name:    "different labels should not be equal",
			current: baseConfigMap.DeepCopy(),
			desired: func() *corev1.ConfigMap {
				cm := baseConfigMap.DeepCopy()
				cm.Labels["new-label"] = "new-value"
				return cm
			}(),
			expected: false,
		},
		{
			name:    "different non-timestamp annotations should not be equal",
			current: baseConfigMap.DeepCopy(),
			desired: func() *corev1.ConfigMap {
				cm := baseConfigMap.DeepCopy()
				cm.Annotations["other-annotation"] = "different-value"
				return cm
			}(),
			expected: false,
		},
		{
			name:    "different timestamp annotation should be equal",
			current: baseConfigMap.DeepCopy(),
			desired: func() *corev1.ConfigMap {
				cm := baseConfigMap.DeepCopy()
				cm.Annotations["toolhive.stacklok.io/last-modified"] = "2025-01-01T13:00:00Z"
				return cm
			}(),
			expected: true, // Should ignore timestamp differences
		},
		{
			name: "missing timestamp annotation in current should be equal",
			current: func() *corev1.ConfigMap {
				cm := baseConfigMap.DeepCopy()
				delete(cm.Annotations, "toolhive.stacklok.io/last-modified")
				return cm
			}(),
			desired:  baseConfigMap.DeepCopy(),
			expected: true,
		},
		{
			name:    "missing timestamp annotation in desired should be equal",
			current: baseConfigMap.DeepCopy(),
			desired: func() *corev1.ConfigMap {
				cm := baseConfigMap.DeepCopy()
				delete(cm.Annotations, "toolhive.stacklok.io/last-modified")
				return cm
			}(),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := reconciler.runConfigContentEquals(tt.current, tt.desired)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestEnsureRunConfigConfigMap tests the ConfigMap creation and update logic
func TestEnsureRunConfigConfigMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		mcpServer        *mcpv1alpha1.MCPServer
		existingCM       *corev1.ConfigMap
		expectCreate     bool
		expectUpdate     bool
		expectAnnotation bool
	}{
		{
			name:             "create new ConfigMap with annotation",
			mcpServer:        createTestMCPServerWithConfig("create-server", "create-ns", "test:v1", nil),
			existingCM:       nil,
			expectCreate:     true,
			expectUpdate:     false,
			expectAnnotation: true,
		},
		{
			name:      "update existing ConfigMap when content changes",
			mcpServer: createTestMCPServerWithConfig("update-server", "update-ns", "test:v2", nil),
			existingCM: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "update-server-runconfig",
					Namespace: "update-ns",
					Labels:    labelsForRunConfig("update-server"),
					Annotations: map[string]string{
						"toolhive.stacklok.io/last-modified": "2025-01-01T12:00:00Z",
					},
				},
				Data: map[string]string{
					"runconfig.json": `{"name":"old-config"}`,
				},
			},
			expectCreate:     false,
			expectUpdate:     true,
			expectAnnotation: true,
		},
		{
			name:      "no update when content is the same",
			mcpServer: createTestMCPServerWithConfig("same-server", "same-ns", "test:v1", nil),
			existingCM: func() *corev1.ConfigMap {
				// Create a ConfigMap with the same content that would be generated
				mcpServer := createTestMCPServerWithConfig("same-server", "same-ns", "test:v1", nil)
				reconciler := &MCPServerReconciler{}
				runConfig := reconciler.createRunConfigFromMCPServer(mcpServer)
				runConfigJSON, _ := json.MarshalIndent(runConfig, "", "  ")

				return &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "same-server-runconfig",
						Namespace: "same-ns",
						Labels:    labelsForRunConfig("same-server"),
						Annotations: map[string]string{
							"toolhive.stacklok.io/last-modified": "2025-01-01T12:00:00Z",
						},
					},
					Data: map[string]string{
						"runconfig.json": string(runConfigJSON),
					},
				}
			}(),
			expectCreate:     false,
			expectUpdate:     false,
			expectAnnotation: false, // Shouldn't update annotation if no content change
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Setup test environment
			testScheme := createRunConfigTestScheme()
			clientBuilder := fake.NewClientBuilder().WithScheme(testScheme)

			// Add existing ConfigMap if provided
			if tt.existingCM != nil {
				clientBuilder = clientBuilder.WithObjects(tt.existingCM)
			}

			fakeClient := clientBuilder.Build()
			reconciler := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: testScheme,
			}

			// Execute the method under test
			err := reconciler.ensureRunConfigConfigMap(context.TODO(), tt.mcpServer)
			require.NoError(t, err)

			// Verify the ConfigMap exists
			configMapName := fmt.Sprintf("%s-runconfig", tt.mcpServer.Name)
			configMap := &corev1.ConfigMap{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      configMapName,
				Namespace: tt.mcpServer.Namespace,
			}, configMap)
			require.NoError(t, err)

			// Verify basic structure
			assert.Equal(t, configMapName, configMap.Name)
			assert.Equal(t, tt.mcpServer.Namespace, configMap.Namespace)
			assert.Equal(t, labelsForRunConfig(tt.mcpServer.Name), configMap.Labels)
			assert.Contains(t, configMap.Data, "runconfig.json")

			// Verify the RunConfig content is correct
			var runConfig runner.RunConfig
			err = json.Unmarshal([]byte(configMap.Data["runconfig.json"]), &runConfig)
			require.NoError(t, err)
			assert.Equal(t, tt.mcpServer.Name, runConfig.Name)
			assert.Equal(t, tt.mcpServer.Spec.Image, runConfig.Image)

			// Verify annotation behavior
			if tt.expectAnnotation {
				lastModified, exists := configMap.Annotations["toolhive.stacklok.io/last-modified"]
				assert.True(t, exists, "last-modified annotation should exist")

				// Parse the timestamp and verify it's valid RFC3339 format
				parsedTime, err := time.Parse(time.RFC3339, lastModified)
				require.NoError(t, err)

				// Verify it's a reasonable recent timestamp (within last minute)
				now := time.Now().UTC()
				assert.True(t, now.Sub(parsedTime) < time.Minute,
					"timestamp should be within the last minute, got %v", parsedTime)
			}
		})
	}
}

// TestConfigMapNewerThanDeployment tests the deployment comparison logic
func TestConfigMapNewerThanDeployment(t *testing.T) {
	t.Parallel()
	reconciler := &MCPServerReconciler{
		Client: fake.NewClientBuilder().WithScheme(createRunConfigTestScheme()).Build(),
	}

	baseTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	deploymentTime := baseTime.Add(1 * time.Hour) // 13:00
	configMapTime := baseTime.Add(2 * time.Hour)  // 14:00

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			CreationTimestamp: metav1.NewTime(baseTime),
		},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:           appsv1.DeploymentProgressing,
					LastUpdateTime: metav1.NewTime(deploymentTime),
				},
			},
		},
	}

	tests := []struct {
		name        string
		configMap   *corev1.ConfigMap
		expected    bool
		description string
	}{
		{
			name: "ConfigMap with newer annotation timestamp",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.NewTime(baseTime),
					Annotations: map[string]string{
						"toolhive.stacklok.io/last-modified": configMapTime.Format(time.RFC3339),
					},
				},
			},
			expected:    true,
			description: "ConfigMap with annotation newer than deployment should return true",
		},
		{
			name: "ConfigMap with older annotation timestamp",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.NewTime(configMapTime), // newer creation time
					Annotations: map[string]string{
						"toolhive.stacklok.io/last-modified": baseTime.Format(time.RFC3339), // but older annotation
					},
				},
			},
			expected:    false,
			description: "ConfigMap with annotation older than deployment should return false",
		},
		{
			name: "ConfigMap without annotation uses creation time - newer",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.NewTime(configMapTime), // newer than deployment
				},
			},
			expected:    true,
			description: "ConfigMap without annotation but newer creation time should return true",
		},
		{
			name: "ConfigMap without annotation uses creation time - older",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.NewTime(baseTime), // older than deployment
				},
			},
			expected:    false,
			description: "ConfigMap without annotation and older creation time should return false",
		},
		{
			name: "ConfigMap with invalid annotation format falls back to creation time",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.NewTime(configMapTime), // newer than deployment
					Annotations: map[string]string{
						"toolhive.stacklok.io/last-modified": "invalid-timestamp",
					},
				},
			},
			expected:    true,
			description: "ConfigMap with invalid annotation should fall back to creation time",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := reconciler.configMapNewerThanDeployment(
				context.TODO(),
				deployment,
				"test-ns",
				"test-configmap",
				tt.configMap,
			)
			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

// TestConfigMapNewerThanDeploymentWithNilClient tests the behavior when client is nil (testing scenario)
func TestConfigMapNewerThanDeploymentWithNilClient(t *testing.T) {
	t.Parallel()
	reconciler := &MCPServerReconciler{
		Client: nil, // Simulate test environment
	}

	deployment := &appsv1.Deployment{}
	result := reconciler.configMapNewerThanDeployment(
		context.TODO(),
		deployment,
		"test-ns",
		"test-configmap",
		nil,
	)

	assert.False(t, result, "Should return false when client is nil")
}

// TestEnsureRunConfigConfigMapCompleteFlow tests the complete flow from MCPServer changes to ConfigMap updates
func TestEnsureRunConfigConfigMapCompleteFlow(t *testing.T) {
	t.Parallel()
	testScheme := createRunConfigTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).Build()
	reconciler := &MCPServerReconciler{
		Client: fakeClient,
		Scheme: testScheme,
	}

	// Step 1: Create initial MCPServer and ConfigMap
	mcpServer := createTestMCPServerWithConfig("flow-server", "flow-ns", "test:v1", []mcpv1alpha1.EnvVar{
		{Name: "ENV1", Value: "value1"},
	})

	err := reconciler.ensureRunConfigConfigMap(context.TODO(), mcpServer)
	require.NoError(t, err)

	// Verify initial ConfigMap
	configMapName := fmt.Sprintf("%s-runconfig", mcpServer.Name)
	configMap1 := &corev1.ConfigMap{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      configMapName,
		Namespace: mcpServer.Namespace,
	}, configMap1)
	require.NoError(t, err)

	initialTimestamp := configMap1.Annotations["toolhive.stacklok.io/last-modified"]
	assert.NotEmpty(t, initialTimestamp)

	// Verify initial content
	var initialRunConfig runner.RunConfig
	err = json.Unmarshal([]byte(configMap1.Data["runconfig.json"]), &initialRunConfig)
	require.NoError(t, err)
	assert.Equal(t, "test:v1", initialRunConfig.Image)
	assert.Len(t, initialRunConfig.EnvVars, 1)
	assert.Equal(t, "value1", initialRunConfig.EnvVars["ENV1"])

	// Step 2: Update MCPServer spec to trigger content change
	// Wait a bit to ensure timestamp difference (RFC3339 has second precision)
	time.Sleep(1 * time.Second)

	mcpServer.Spec.Image = "test:v2"
	mcpServer.Spec.Env = []mcpv1alpha1.EnvVar{
		{Name: "ENV1", Value: "value1"},
		{Name: "ENV2", Value: "value2"},
	}

	err = reconciler.ensureRunConfigConfigMap(context.TODO(), mcpServer)
	require.NoError(t, err)

	// Verify ConfigMap was updated
	configMap2 := &corev1.ConfigMap{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      configMapName,
		Namespace: mcpServer.Namespace,
	}, configMap2)
	require.NoError(t, err)

	updatedTimestamp := configMap2.Annotations["toolhive.stacklok.io/last-modified"]
	assert.NotEmpty(t, updatedTimestamp)
	assert.NotEqual(t, initialTimestamp, updatedTimestamp, "Timestamp should be updated")

	// Verify updated content
	var updatedRunConfig runner.RunConfig
	err = json.Unmarshal([]byte(configMap2.Data["runconfig.json"]), &updatedRunConfig)
	require.NoError(t, err)
	assert.Equal(t, "test:v2", updatedRunConfig.Image)
	assert.Len(t, updatedRunConfig.EnvVars, 2)
	assert.Equal(t, "value1", updatedRunConfig.EnvVars["ENV1"])
	assert.Equal(t, "value2", updatedRunConfig.EnvVars["ENV2"])

	// Step 3: Call again with same spec - should not update
	err = reconciler.ensureRunConfigConfigMap(context.TODO(), mcpServer)
	require.NoError(t, err)

	// Verify timestamp didn't change (no content change)
	configMap3 := &corev1.ConfigMap{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      configMapName,
		Namespace: mcpServer.Namespace,
	}, configMap3)
	require.NoError(t, err)

	finalTimestamp := configMap3.Annotations["toolhive.stacklok.io/last-modified"]
	assert.Equal(t, updatedTimestamp, finalTimestamp, "Timestamp should not change when content is the same")
}

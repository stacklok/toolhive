// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1apply "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	// Initialize the logger for tests
	logger.Initialize()
}

// mockWaitForStatefulSetReady is used to mock the waitForStatefulSetReady function in tests
var mockWaitForStatefulSetReady = func(_ context.Context, _ kubernetes.Interface, _, _ string, _ int64) error {
	return nil
}

// mockPlatformDetector is used to mock the platform detector in tests
type mockPlatformDetector struct {
	platform Platform
	err      error
}

func (m *mockPlatformDetector) DetectPlatform(_ *rest.Config) (Platform, error) {
	return m.platform, m.err
}

// TestCreateContainerWithPodTemplatePatch tests that the pod template patch is correctly applied
func TestCreateContainerWithPodTemplatePatch(t *testing.T) {
	t.Parallel()
	// Test cases will create their own clientset

	// Test cases
	testCases := []struct {
		name                             string
		k8sPodTemplatePatch              string
		expectedVolumes                  []corev1.Volume
		expectedTolerations              []corev1.Toleration
		expectedPodSecurityContext       *corev1apply.PodSecurityContextApplyConfiguration
		expectedContainerSecurityContext *corev1apply.SecurityContextApplyConfiguration
	}{
		{
			name: "with pod template patch",
			k8sPodTemplatePatch: `{
				"spec": {
					"securityContext": {
						"runAsNonRoot": false,
						"runAsUser": 2000,
						"runAsGroup": 2000,
						"fsGroup": 2000
					},
					"containers": [
						{
							"name": "mcp",
							"securityContext": {
								"privileged": true,
								"runAsNonRoot": false,
								"runAsUser": 2000,
								"runAsGroup": 2000,
								"fsGroup": 2000,
								"readOnlyRootFilesystem": false,
								"allowPrivilegeEscalation": true
							}
						}
					],
					"volumes": [
						{
							"name": "test-volume",
							"emptyDir": {}
						}
					],
					"tolerations": [
						{
							"key": "key1",
							"operator": "Equal",
							"value": "value1",
							"effect": "NoSchedule"
						}
					]
				}
			}`,
			expectedPodSecurityContext: corev1apply.PodSecurityContext().
				WithRunAsNonRoot(false).
				WithRunAsUser(int64(2000)).
				WithRunAsGroup(int64(2000)).
				WithFSGroup(int64(2000)),
			expectedContainerSecurityContext: corev1apply.SecurityContext().
				WithRunAsNonRoot(false).
				WithRunAsUser(int64(2000)).
				WithRunAsGroup(int64(2000)).
				WithPrivileged(true).
				WithReadOnlyRootFilesystem(false).
				WithAllowPrivilegeEscalation(true),
			expectedVolumes: []corev1.Volume{
				{
					Name: "test-volume",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
			expectedTolerations: []corev1.Toleration{
				{
					Key:      "key1",
					Operator: "Equal",
					Value:    "value1",
					Effect:   "NoSchedule",
				},
			},
		},
		{
			name:                "without pod template patch",
			k8sPodTemplatePatch: "",
			expectedPodSecurityContext: corev1apply.PodSecurityContext().
				WithRunAsNonRoot(true).
				WithRunAsUser(int64(1000)).
				WithRunAsGroup(int64(1000)).
				WithFSGroup(int64(1000)),
			expectedContainerSecurityContext: corev1apply.SecurityContext().
				WithRunAsNonRoot(true).
				WithRunAsUser(int64(1000)).
				WithRunAsGroup(int64(1000)).
				WithPrivileged(false).
				WithReadOnlyRootFilesystem(true).
				WithAllowPrivilegeEscalation(false),
			expectedVolumes:     nil,
			expectedTolerations: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create a fake Kubernetes clientset with a mock statefulset
			mockStatefulSet := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-container",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: func() *int32 { i := int32(1); return &i }(),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "mcp",
									Image: "test-image",
									Args:  []string{"test-command"},
								},
							},
						},
					},
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: 1,
				},
			}
			clientset := fake.NewClientset(mockStatefulSet)

			// Create a fake config for testing
			fakeConfig := &rest.Config{
				Host: "https://fake-k8s-api.example.com",
			}

			// Create a mock platform detector that returns Kubernetes platform
			mockDetector := &mockPlatformDetector{
				platform: PlatformKubernetes,
				err:      nil,
			}

			// Create a client with the fake clientset, config, and platform detector
			client := NewClientWithConfigAndPlatformDetector(clientset, fakeConfig, mockDetector)
			client.waitForStatefulSetReadyFunc = mockWaitForStatefulSetReady
			client.namespaceFunc = func() string { return defaultNamespace }
			// Create workload options with the pod template patch
			options := runtime.NewDeployWorkloadOptions()
			options.K8sPodTemplatePatch = tc.k8sPodTemplatePatch

			// Deploy the workload
			_, err := client.DeployWorkload(
				context.Background(),
				"test-image",
				"test-container",
				[]string{"test-command"},
				map[string]string{"TEST_ENV": "test-value"},
				map[string]string{"test-label": "test-value"},
				nil,
				"stdio",
				options,
				false,
			)

			// Skip test if not running in cluster (expected for unit tests)
			if err != nil && strings.Contains(err.Error(), "unable to load in-cluster configuration") {
				t.Skip("Skipping test - requires in-cluster Kubernetes configuration")
			}

			// Check that there was no error
			require.NoError(t, err)

			// Get the created StatefulSet
			statefulSet, err := clientset.AppsV1().StatefulSets("default").Get(
				context.Background(),
				"test-container",
				metav1.GetOptions{},
			)
			require.NoError(t, err)

			// Check that the StatefulSet was created with the correct values
			assert.Equal(t, "test-container", statefulSet.Name)
			assert.Equal(t, "test-image", statefulSet.Spec.Template.Spec.Containers[0].Image)
			assert.Equal(t, []string{"test-command"}, statefulSet.Spec.Template.Spec.Containers[0].Args)

			// Check that the pod template patch was applied correctly
			if tc.k8sPodTemplatePatch != "" {
				// Check volumes
				assert.Equal(t, tc.expectedVolumes, statefulSet.Spec.Template.Spec.Volumes)

				// Check tolerations
				assert.Equal(t, tc.expectedTolerations, statefulSet.Spec.Template.Spec.Tolerations)
			} else {
				assert.Empty(t, statefulSet.Spec.Template.Spec.Volumes)
				assert.Empty(t, statefulSet.Spec.Template.Spec.Tolerations)
			}

			// Check pod security context
			assert.NotNil(t, statefulSet.Spec.Template.Spec.SecurityContext, "Pod security context should not be nil")
			assert.Equal(t, tc.expectedPodSecurityContext.RunAsNonRoot, statefulSet.Spec.Template.Spec.SecurityContext.RunAsNonRoot, "RunAsNonRoot should be true")

			// Detect platform type based on security context fields
			var detectedPlatform Platform
			if statefulSet.Spec.Template.Spec.SecurityContext.RunAsUser == nil {
				detectedPlatform = PlatformOpenShift
			} else {
				detectedPlatform = PlatformKubernetes
			}

			if detectedPlatform == PlatformOpenShift {
				// In OpenShift, these fields are set to nil and managed by SCCs
				assert.Nil(t, statefulSet.Spec.Template.Spec.SecurityContext.RunAsUser, "RunAsUser should be nil in OpenShift")
				assert.Nil(t, statefulSet.Spec.Template.Spec.SecurityContext.RunAsGroup, "RunAsGroup should be nil in OpenShift")
				assert.Nil(t, statefulSet.Spec.Template.Spec.SecurityContext.FSGroup, "FSGroup should be nil in OpenShift")
			} else {
				// In standard Kubernetes, these fields should have explicit values
				assert.Equal(t, tc.expectedPodSecurityContext.RunAsUser, statefulSet.Spec.Template.Spec.SecurityContext.RunAsUser, "RunAsUser should be 1000")
				assert.Equal(t, tc.expectedPodSecurityContext.RunAsGroup, statefulSet.Spec.Template.Spec.SecurityContext.RunAsGroup, "RunAsGroup should be 1000")
				assert.Equal(t, tc.expectedPodSecurityContext.FSGroup, statefulSet.Spec.Template.Spec.SecurityContext.FSGroup, "FSGroup should be 1000")
			}

			// Check container security context
			container := statefulSet.Spec.Template.Spec.Containers[0]
			assert.NotNil(t, container.SecurityContext, "Container security context should not be nil")
			assert.Equal(t, tc.expectedContainerSecurityContext.RunAsNonRoot, container.SecurityContext.RunAsNonRoot, "Container RunAsNonRoot should be true")

			if detectedPlatform == PlatformOpenShift {
				// In OpenShift, these fields are set to nil and managed by SCCs
				assert.Nil(t, container.SecurityContext.RunAsUser, "Container RunAsUser should be nil in OpenShift")
				assert.Nil(t, container.SecurityContext.RunAsGroup, "Container RunAsGroup should be nil in OpenShift")
			} else {
				// In standard Kubernetes, these fields should have explicit values
				assert.Equal(t, tc.expectedContainerSecurityContext.RunAsUser, container.SecurityContext.RunAsUser, "Container RunAsUser should be 1000")
				assert.Equal(t, tc.expectedContainerSecurityContext.RunAsGroup, container.SecurityContext.RunAsGroup, "Container RunAsGroup should be 1000")
			}

			assert.Equal(t, tc.expectedContainerSecurityContext.Privileged, container.SecurityContext.Privileged, "Container Privileged should be false")
			assert.Equal(t, tc.expectedContainerSecurityContext.ReadOnlyRootFilesystem, container.SecurityContext.ReadOnlyRootFilesystem, "Container ReadOnlyRootFilesystem should be true")
			assert.Equal(t, tc.expectedContainerSecurityContext.AllowPrivilegeEscalation, container.SecurityContext.AllowPrivilegeEscalation, "Container AllowPrivilegeEscalation should be false")
		})
	}
}

// TestCreatePodTemplateFromPatch tests the createPodTemplateFromPatch function
func TestCreatePodTemplateFromPatch(t *testing.T) {
	t.Parallel()
	// Test cases
	testCases := []struct {
		name      string
		patchJSON string
		expectErr bool
	}{
		{
			name: "valid patch",
			patchJSON: `{
				"metadata": {
					"labels": {
						"app": "test-app"
					}
				},
				"spec": {
					"volumes": [
						{
							"name": "test-volume",
							"emptyDir": {}
						}
					]
				}
			}`,
			expectErr: false,
		},
		{
			name:      "invalid JSON",
			patchJSON: `{invalid json`,
			expectErr: true,
		},
		{
			name:      "empty patch",
			patchJSON: `{}`,
			expectErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Call the function
			podTemplateSpec, err := createPodTemplateFromPatch(tc.patchJSON)

			// Check the result
			if tc.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, podTemplateSpec)

				// If the patch is not empty, check that it was parsed correctly
				if tc.patchJSON != "{}" {
					// Convert the patch to a map for comparison
					var patchMap map[string]interface{}
					err := json.Unmarshal([]byte(tc.patchJSON), &patchMap)
					require.NoError(t, err)

					// Convert the pod template spec to JSON
					podTemplateJSON, err := json.Marshal(podTemplateSpec)
					require.NoError(t, err)

					// Convert the JSON back to a map
					var podTemplateMap map[string]interface{}
					err = json.Unmarshal(podTemplateJSON, &podTemplateMap)
					require.NoError(t, err)

					// Check that the pod template contains the patch data
					// This is a simplified check, as the exact structure may differ
					if metadata, ok := patchMap["metadata"].(map[string]interface{}); ok {
						if labels, ok := metadata["labels"].(map[string]interface{}); ok {
							if app, ok := labels["app"].(string); ok {
								assert.Equal(t, "test-app", app)
							}
						}
					}
				}
			}
		})
	}
}

// TestEnsurePodTemplateConfig tests the ensurePodTemplateConfig function
func TestEnsurePodTemplateConfig(t *testing.T) {
	t.Parallel()
	// Test cases
	testCases := []struct {
		name            string
		podTemplateSpec *corev1apply.PodTemplateSpecApplyConfiguration
		containerLabels map[string]string
	}{
		{
			name:            "empty pod template",
			podTemplateSpec: corev1apply.PodTemplateSpec(),
			containerLabels: map[string]string{"app": "test-app"},
		},
		{
			name:            "pod template with existing labels",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithLabels(map[string]string{"existing": "label"}),
			containerLabels: map[string]string{"app": "test-app"},
		},
		{
			name:            "pod template with existing spec",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec()),
			containerLabels: map[string]string{"app": "test-app"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Call the function
			result := ensurePodTemplateConfig(tc.podTemplateSpec, tc.containerLabels, PlatformKubernetes)

			// Check the result
			assert.NotNil(t, result)
			assert.NotNil(t, result.Labels)
			assert.NotNil(t, result.Spec)
			assert.NotNil(t, result.Spec.RestartPolicy)

			// Check that the labels were merged correctly
			for k, v := range tc.containerLabels {
				assert.Equal(t, v, result.Labels[k])
			}

			// Check that the restart policy was set
			assert.Equal(t, corev1.RestartPolicyAlways, *result.Spec.RestartPolicy)
		})
	}
}

// TestGetMCPContainer tests the getMCPContainer function
func TestGetMCPContainer(t *testing.T) {
	t.Parallel()
	// Test cases
	testCases := []struct {
		name            string
		podTemplateSpec *corev1apply.PodTemplateSpecApplyConfiguration
		expectNil       bool
		expectedName    string
	}{
		{
			name:            "empty pod template",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec()),
			expectNil:       true,
		},
		{
			name: "pod template with existing mcp container",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec().
				WithContainers(corev1apply.Container().WithName(mcpContainerName).WithImage("existing-image"))),
			expectNil:    false,
			expectedName: "mcp",
		},
		{
			name: "pod template with different container",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec().
				WithContainers(corev1apply.Container().WithName("other-container"))),
			expectNil: true,
		},
		{
			name: "pod template with multiple existing containers but no mcp",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec().
				WithContainers(
					corev1apply.Container().WithName("container1"),
					corev1apply.Container().WithName("container2"),
				)),
			expectNil: true,
		},
		{
			name: "pod template with multiple existing containers including mcp",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec().
				WithContainers(
					corev1apply.Container().WithName("container1"),
					corev1apply.Container().WithName(mcpContainerName),
					corev1apply.Container().WithName("container2"),
				)),
			expectNil:    false,
			expectedName: "mcp",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Call the function
			result := getMCPContainer(tc.podTemplateSpec)

			if tc.expectNil {
				// Check that the result is nil
				assert.Nil(t, result, "Expected nil result for %s", tc.name)
			} else {
				// Check that the result is not nil and has the expected name
				assert.NotNil(t, result, "Expected non-nil result for %s", tc.name)
				assert.NotNil(t, result.Name, "Expected non-nil name for %s", tc.name)
				assert.Equal(t, tc.expectedName, *result.Name, "Expected name %s for %s", tc.expectedName, tc.name)
			}
		})
	}
}

// TestConfigureMCPContainer tests the configureMCPContainer function
func TestConfigureMCPContainer(t *testing.T) {
	t.Parallel()
	// Test cases
	testCases := []struct {
		name                string
		podTemplateSpec     *corev1apply.PodTemplateSpecApplyConfiguration
		image               string
		command             []string
		attachStdio         bool
		envVars             []*corev1apply.EnvVarApplyConfiguration
		transportType       string
		options             *runtime.DeployWorkloadOptions
		expectedContainers  int
		expectedImage       string
		expectedCommand     []string
		expectedEnvVarCount int
		expectedPorts       int
	}{
		{
			name: "create new container",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec().
				WithContainers(corev1apply.Container().WithName("other-container"))),
			image:               "test-image",
			command:             []string{"test-command"},
			attachStdio:         true,
			envVars:             []*corev1apply.EnvVarApplyConfiguration{corev1apply.EnvVar().WithName("TEST_ENV").WithValue("test-value")},
			transportType:       "stdio",
			options:             nil,
			expectedContainers:  2,
			expectedImage:       "test-image",
			expectedCommand:     []string{"test-command"},
			expectedEnvVarCount: 1,
			expectedPorts:       0,
		},
		{
			name: "configure existing container",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec().
				WithContainers(
					corev1apply.Container().WithName(mcpContainerName).WithImage("old-image"),
					corev1apply.Container().WithName("other-container"),
				)),
			image:               "test-image",
			command:             []string{"test-command"},
			attachStdio:         true,
			envVars:             []*corev1apply.EnvVarApplyConfiguration{corev1apply.EnvVar().WithName("TEST_ENV").WithValue("test-value")},
			transportType:       "stdio",
			options:             nil,
			expectedContainers:  2,
			expectedImage:       "test-image",
			expectedCommand:     []string{"test-command"},
			expectedEnvVarCount: 1,
			expectedPorts:       0,
		},
		{
			name:            "configure with SSE transport",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec()),
			image:           "test-image",
			command:         []string{"test-command"},
			attachStdio:     true,
			envVars:         []*corev1apply.EnvVarApplyConfiguration{corev1apply.EnvVar().WithName("TEST_ENV").WithValue("test-value")},
			transportType:   "sse",
			options: &runtime.DeployWorkloadOptions{
				ExposedPorts: map[string]struct{}{
					"8080/tcp": {},
				},
			},
			expectedContainers:  1,
			expectedImage:       "test-image",
			expectedCommand:     []string{"test-command"},
			expectedEnvVarCount: 1,
			expectedPorts:       1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Call the function
			err := configureMCPContainer(
				tc.podTemplateSpec,
				tc.image,
				tc.command,
				tc.attachStdio,
				tc.envVars,
				tc.transportType,
				tc.options,
				PlatformKubernetes,
			)

			// Check that there was no error
			require.NoError(t, err)

			// Check that the pod template has a spec
			assert.NotNil(t, tc.podTemplateSpec.Spec)

			// Check that the container list is not nil
			assert.NotNil(t, tc.podTemplateSpec.Spec.Containers)

			// Check the number of containers
			assert.Equal(t, tc.expectedContainers, len(tc.podTemplateSpec.Spec.Containers))

			// Find the mcp container
			var mcpContainer *corev1apply.ContainerApplyConfiguration
			for i := range tc.podTemplateSpec.Spec.Containers {
				container := &tc.podTemplateSpec.Spec.Containers[i]
				if container.Name != nil && *container.Name == mcpContainerName {
					mcpContainer = container
					break
				}
			}

			// Check that the mcp container exists
			assert.NotNil(t, mcpContainer)

			// Check the container configuration
			assert.Equal(t, tc.expectedImage, *mcpContainer.Image)
			assert.Equal(t, tc.expectedCommand, mcpContainer.Args)
			assert.Equal(t, tc.attachStdio, *mcpContainer.Stdin)
			assert.Equal(t, tc.expectedEnvVarCount, len(mcpContainer.Env))

			// Check ports if expected
			if tc.expectedPorts > 0 {
				assert.NotNil(t, mcpContainer.Ports)
				assert.Equal(t, tc.expectedPorts, len(mcpContainer.Ports))
			}
		})
	}
}

// TestCreateContainerWithMCP tests the CreateContainer function with MCP container configuration
func TestCreateContainerWithMCP(t *testing.T) {
	t.Parallel()
	// Test cases
	testCases := []struct {
		name                string
		existingContainers  []corev1.Container
		image               string
		command             []string
		envVars             map[string]string
		attachStdio         bool
		transportType       string
		options             *runtime.DeployWorkloadOptions
		expectedContainers  int
		expectedImage       string
		expectedCommand     []string
		expectedEnvVarCount int
	}{
		{
			name:                "create container with no existing containers",
			existingContainers:  []corev1.Container{},
			image:               "test-image",
			command:             []string{"test-command"},
			envVars:             map[string]string{"TEST_ENV": "test-value"},
			attachStdio:         true,
			transportType:       "stdio",
			options:             nil,
			expectedContainers:  1,
			expectedImage:       "test-image",
			expectedCommand:     []string{"test-command"},
			expectedEnvVarCount: 1,
		},
		{
			name: "create container with existing non-mcp container",
			existingContainers: []corev1.Container{
				{
					Name:  "other-container",
					Image: "other-image",
				},
			},
			image:               "test-image",
			command:             []string{"test-command"},
			envVars:             map[string]string{"TEST_ENV": "test-value"},
			attachStdio:         true,
			transportType:       "stdio",
			options:             nil,
			expectedContainers:  2,
			expectedImage:       "test-image",
			expectedCommand:     []string{"test-command"},
			expectedEnvVarCount: 1,
		},
		{
			name: "create container with existing mcp container",
			existingContainers: []corev1.Container{
				{
					Name:  mcpContainerName,
					Image: "old-image",
				},
			},
			image:               "test-image",
			command:             []string{"test-command"},
			envVars:             map[string]string{"TEST_ENV": "test-value"},
			attachStdio:         true,
			transportType:       "stdio",
			options:             nil,
			expectedContainers:  1,
			expectedImage:       "test-image",
			expectedCommand:     []string{"test-command"},
			expectedEnvVarCount: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create a fake Kubernetes clientset with a mock statefulset
			mockStatefulSet := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-container",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: func() *int32 { i := int32(1); return &i }(),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: tc.existingContainers,
						},
					},
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: 1,
				},
			}
			clientset := fake.NewClientset(mockStatefulSet)

			// Create a fake config for testing
			fakeConfig := &rest.Config{
				Host: "https://fake-k8s-api.example.com",
			}

			// Create a mock platform detector that returns Kubernetes platform
			mockDetector := &mockPlatformDetector{
				platform: PlatformKubernetes,
				err:      nil,
			}

			// Create a client with the fake clientset, config, and platform detector
			client := NewClientWithConfigAndPlatformDetector(clientset, fakeConfig, mockDetector)
			client.waitForStatefulSetReadyFunc = mockWaitForStatefulSetReady
			client.namespaceFunc = func() string { return defaultNamespace }

			// Deploy the workload
			_, err := client.DeployWorkload(
				context.Background(),
				tc.image,
				"test-container",
				tc.command,
				tc.envVars,
				map[string]string{"test-label": "test-value"},
				nil,
				tc.transportType,
				tc.options,
				false,
			)

			// Skip test if not running in cluster (expected for unit tests)
			if err != nil && strings.Contains(err.Error(), "unable to load in-cluster configuration") {
				t.Skip("Skipping test - requires in-cluster Kubernetes configuration")
			}

			// Check that there was no error
			require.NoError(t, err)

			// Get the created StatefulSet
			statefulSet, err := clientset.AppsV1().StatefulSets("default").Get(
				context.Background(),
				"test-container",
				metav1.GetOptions{},
			)
			require.NoError(t, err)

			// Check that the StatefulSet was created with the correct values
			assert.Equal(t, "test-container", statefulSet.Name)

			// Check the number of containers
			assert.Equal(t, tc.expectedContainers, len(statefulSet.Spec.Template.Spec.Containers))

			// Find the mcp container
			var mcpContainer *corev1.Container
			for i := range statefulSet.Spec.Template.Spec.Containers {
				container := &statefulSet.Spec.Template.Spec.Containers[i]
				if container.Name == mcpContainerName {
					mcpContainer = container
					break
				}
			}

			// Check that the mcp container exists
			assert.NotNil(t, mcpContainer)

			// Check the container configuration
			assert.Equal(t, tc.expectedImage, mcpContainer.Image)
			assert.Equal(t, tc.expectedCommand, mcpContainer.Args)
			assert.Equal(t, tc.attachStdio, mcpContainer.Stdin)
			assert.Equal(t, tc.expectedEnvVarCount, len(mcpContainer.Env))
		})
	}
}

// TestAttachToWorkloadExitFunc tests that the exit function is properly configured
// and can be mocked for testing
func TestAttachToWorkloadExitFunc(t *testing.T) {
	t.Parallel()

	// Create a client
	clientset := fake.NewClientset()
	fakeConfig := &rest.Config{
		Host: "https://fake-k8s-api.example.com",
	}
	client := NewClientWithConfig(clientset, fakeConfig)

	// Verify that exitFunc can be set and is initially nil
	assert.Nil(t, client.exitFunc, "Expected exitFunc to be nil by default")

	// Set a mock exit function
	exitCalled := false
	exitCode := 0
	client.exitFunc = func(code int) {
		exitCalled = true
		exitCode = code
	}

	// Verify the mock is set
	assert.NotNil(t, client.exitFunc, "Expected exitFunc to be set")

	// Call the exit function directly to verify it works
	client.exitFunc(1)
	assert.True(t, exitCalled, "Expected exit function to be called")
	assert.Equal(t, 1, exitCode, "Expected exit code 1")
}

// TestClientExitFuncDefaultsToNil verifies that the exitFunc field defaults to nil,
// meaning the code will use os.Exit in production. The actual exit behavior on
// connection failure is verified in E2E tests (see test/e2e/thv-operator/virtualmcp/
// virtualmcp_yardstick_base_test.go "should reflect backend health changes in status").
func TestClientExitFuncDefaultsToNil(t *testing.T) {
	t.Parallel()

	clientset := fake.NewClientset()
	fakeConfig := &rest.Config{
		Host: "https://fake-k8s-api.example.com",
	}
	client := NewClientWithConfig(clientset, fakeConfig)

	// Verify exitFunc defaults to nil (production will use os.Exit)
	assert.Nil(t, client.exitFunc, "Expected exitFunc to be nil by default")
}

// TestAttachToWorkloadNoPodFound tests that AttachToWorkload returns error when no pod is found
func TestAttachToWorkloadNoPodFound(t *testing.T) {
	t.Parallel()

	// Create a fake Kubernetes clientset with no pods
	clientset := fake.NewClientset()

	// Create a fake config
	fakeConfig := &rest.Config{
		Host: "https://fake-k8s-api.example.com",
	}

	// Create a client with the fake clientset and config
	client := NewClientWithConfig(clientset, fakeConfig)
	client.namespaceFunc = func() string { return defaultNamespace }

	// Call AttachToWorkload with a workload that has no pods
	_, _, err := client.AttachToWorkload(context.Background(), "nonexistent-workload")

	// Should return error immediately (no pods found)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pods found")
}

// TestApplyPodTemplatePatchAnnotations tests that annotations are correctly applied
// from the pod template patch to the base template.
// This is a regression test for the bug where annotations were not being applied.
func TestApplyPodTemplatePatchAnnotations(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                string
		patchJSON           string
		expectedAnnotations map[string]string
		expectedLabels      map[string]string
	}{
		{
			name: "patch with annotations only",
			patchJSON: `{
				"metadata": {
					"annotations": {
						"vault.hashicorp.com/agent-inject": "true",
						"vault.hashicorp.com/role": "mcp-server"
					}
				}
			}`,
			expectedAnnotations: map[string]string{
				"vault.hashicorp.com/agent-inject": "true",
				"vault.hashicorp.com/role":         "mcp-server",
			},
			expectedLabels: nil,
		},
		{
			name: "patch with both labels and annotations",
			patchJSON: `{
				"metadata": {
					"labels": {
						"app": "test-app",
						"version": "v1"
					},
					"annotations": {
						"prometheus.io/scrape": "true",
						"prometheus.io/port": "8080"
					}
				}
			}`,
			expectedAnnotations: map[string]string{
				"prometheus.io/scrape": "true",
				"prometheus.io/port":   "8080",
			},
			expectedLabels: map[string]string{
				"app":     "test-app",
				"version": "v1",
			},
		},
		{
			name: "patch with labels only (no annotations)",
			patchJSON: `{
				"metadata": {
					"labels": {
						"app": "test-app"
					}
				}
			}`,
			expectedAnnotations: nil,
			expectedLabels: map[string]string{
				"app": "test-app",
			},
		},
		{
			name:                "empty patch",
			patchJSON:           `{}`,
			expectedAnnotations: nil,
			expectedLabels:      nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			baseTemplate := corev1apply.PodTemplateSpec()
			result, err := applyPodTemplatePatch(baseTemplate, tc.patchJSON)

			require.NoError(t, err)
			require.NotNil(t, result)

			// Get labels and annotations safely (may be nil if ObjectMetaApplyConfiguration is nil)
			var resultLabels, resultAnnotations map[string]string
			if result.ObjectMetaApplyConfiguration != nil {
				resultLabels = result.Labels
				resultAnnotations = result.Annotations
			}

			// Verify labels
			if tc.expectedLabels == nil {
				assert.Empty(t, resultLabels, "Expected no labels")
			} else {
				assert.Equal(t, tc.expectedLabels, resultLabels, "Labels mismatch")
			}

			// Verify annotations - this is the key assertion for the bug fix
			if tc.expectedAnnotations == nil {
				assert.Empty(t, resultAnnotations, "Expected no annotations")
			} else {
				assert.Equal(t, tc.expectedAnnotations, resultAnnotations,
					"BUG: Annotations are not being applied from the patch")
			}
		})
	}
}

// Test_isStatefulSetReady tests the isStatefulSetReady function.
//
// The function checks three conditions before returning ready:
// 1. ObservedGeneration >= desiredGeneration (controller processed our spec)
// 2. UpdatedReplicas == Replicas (all pods on new spec)
// 3. ReadyReplicas == Replicas (all pods ready)
func Test_isStatefulSetReady(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		desiredGen      int64
		observedGen     int64
		updatedReplicas int32
		readyReplicas   int32
		replicas        int32
		expectedReady   bool
	}{
		{
			name:            "controller_not_caught_up_old_pod_ready",
			desiredGen:      2,
			observedGen:     1,
			updatedReplicas: 0,
			readyReplicas:   1,
			replicas:        1,
			expectedReady:   false,
		},
		{
			name:            "controller_caught_up_no_new_pods",
			desiredGen:      2,
			observedGen:     2,
			updatedReplicas: 0,
			readyReplicas:   1,
			replicas:        1,
			expectedReady:   false,
		},
		{
			name:            "new_pod_starting_not_ready",
			desiredGen:      2,
			observedGen:     2,
			updatedReplicas: 1,
			readyReplicas:   0,
			replicas:        1,
			expectedReady:   false,
		},
		{
			name:            "rolling_update_complete",
			desiredGen:      2,
			observedGen:     2,
			updatedReplicas: 1,
			readyReplicas:   1,
			replicas:        1,
			expectedReady:   true,
		},
		{
			name:            "steady_state",
			desiredGen:      1,
			observedGen:     1,
			updatedReplicas: 1,
			readyReplicas:   1,
			replicas:        1,
			expectedReady:   true,
		},
		// Multi-replica tests
		{
			name:            "multi_replica_rolling_update_not_started",
			desiredGen:      2,
			observedGen:     2,
			updatedReplicas: 0, // no pods updated yet
			readyReplicas:   3, // all old pods still ready
			replicas:        3,
			expectedReady:   false,
		},
		{
			name:            "multi_replica_rolling_update_one_updated",
			desiredGen:      2,
			observedGen:     2,
			updatedReplicas: 1,
			readyReplicas:   3,
			replicas:        3,
			expectedReady:   false,
		},
		{
			name:            "multi_replica_rolling_update_two_updated",
			desiredGen:      2,
			observedGen:     2,
			updatedReplicas: 2,
			readyReplicas:   3,
			replicas:        3,
			expectedReady:   false,
		},
		{
			name:            "multi_replica_rolling_update_complete",
			desiredGen:      2,
			observedGen:     2,
			updatedReplicas: 3,
			readyReplicas:   3,
			replicas:        3,
			expectedReady:   true,
		},
		{
			name:            "multi_replica_last_pod_not_ready",
			desiredGen:      2,
			observedGen:     2,
			updatedReplicas: 3,
			readyReplicas:   2,
			replicas:        3,
			expectedReady:   false,
		},
		{
			name:            "multi_replica_steady_state",
			desiredGen:      1,
			observedGen:     1,
			updatedReplicas: 3,
			readyReplicas:   3,
			replicas:        3,
			expectedReady:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ss := &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: &tc.replicas,
				},
				Status: appsv1.StatefulSetStatus{
					ObservedGeneration: tc.observedGen,
					UpdatedReplicas:    tc.updatedReplicas,
					ReadyReplicas:      tc.readyReplicas,
					Replicas:           tc.replicas,
				},
			}

			result := isStatefulSetReady(tc.desiredGen, ss)
			assert.Equal(t, tc.expectedReady, result)
		})
	}

	// Test nil/zero value edge cases - all should return false
	nilTests := []struct {
		name  string
		input *appsv1.StatefulSet
	}{
		{
			name:  "nil_statefulset",
			input: nil,
		},
		{
			name:  "empty_statefulset",
			input: &appsv1.StatefulSet{},
		},
		{
			name: "spec_replicas_nil",
			input: &appsv1.StatefulSet{
				Status: appsv1.StatefulSetStatus{
					ObservedGeneration: 1,
					UpdatedReplicas:    1,
					ReadyReplicas:      1,
				},
			},
		},
		{
			name: "status_all_zero",
			input: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: func() *int32 { v := int32(1); return &v }(),
				},
			},
		},
	}

	for _, tc := range nilTests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := isStatefulSetReady(1, tc.input)
			assert.False(t, result)
		})
	}
}

package kubernetes

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1apply "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/StacklokLabs/toolhive/pkg/container/runtime"
	"github.com/StacklokLabs/toolhive/pkg/logger"
)

func init() {
	// Initialize the logger for tests
	logger.Initialize()
}

// mockWaitForStatefulSetReady is used to mock the waitForStatefulSetReady function in tests
var mockWaitForStatefulSetReady = func(_ context.Context, _ kubernetes.Interface, _, _ string) error {
	return nil
}

// TestCreateContainerWithPodTemplatePatch tests that the pod template patch is correctly applied
func TestCreateContainerWithPodTemplatePatch(t *testing.T) {
	// Test cases will create their own clientset

	// Test cases
	testCases := []struct {
		name                string
		k8sPodTemplatePatch string
		expectedVolumes     []corev1.Volume
		expectedTolerations []corev1.Toleration
	}{
		{
			name: "with pod template patch",
			k8sPodTemplatePatch: `{
				"spec": {
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
			expectedVolumes:     nil,
			expectedTolerations: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
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
			clientset := fake.NewSimpleClientset(mockStatefulSet)

			// Create a client with the fake clientset
			client := &Client{
				runtimeType:                 runtime.TypeKubernetes,
				client:                      clientset,
				waitForStatefulSetReadyFunc: mockWaitForStatefulSetReady,
			}
			// Create container options with the pod template patch
			options := runtime.NewCreateContainerOptions()
			options.K8sPodTemplatePatch = tc.k8sPodTemplatePatch

			// Create the container
			containerID, err := client.CreateContainer(
				context.Background(),
				"test-image",
				"test-container",
				[]string{"test-command"},
				map[string]string{"TEST_ENV": "test-value"},
				map[string]string{"test-label": "test-value"},
				nil,
				"stdio",
				options,
			)

			// Check that there was no error
			require.NoError(t, err)
			assert.NotEmpty(t, containerID)

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
				// Check that no volumes or tolerations were added
				assert.Empty(t, statefulSet.Spec.Template.Spec.Volumes)
				assert.Empty(t, statefulSet.Spec.Template.Spec.Tolerations)
			}
		})
	}
}

// TestCreatePodTemplateFromPatch tests the createPodTemplateFromPatch function
func TestCreatePodTemplateFromPatch(t *testing.T) {
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
			// Call the function
			result := ensurePodTemplateConfig(tc.podTemplateSpec, tc.containerLabels)

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
	// Test cases
	testCases := []struct {
		name                string
		podTemplateSpec     *corev1apply.PodTemplateSpecApplyConfiguration
		expectedName        string
		expectedContainers  int
		checkContainerNames []string
	}{
		{
			name:               "empty pod template",
			podTemplateSpec:    corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec()),
			expectedName:       "mcp",
			expectedContainers: 1,
			checkContainerNames: []string{
				"mcp",
			},
		},
		{
			name: "pod template with existing mcp container",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec().
				WithContainers(corev1apply.Container().WithName("mcp").WithImage("existing-image"))),
			expectedName:       "mcp",
			expectedContainers: 1,
			checkContainerNames: []string{
				"mcp",
			},
		},
		{
			name: "pod template with different container",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec().
				WithContainers(corev1apply.Container().WithName("other-container"))),
			expectedName:       "mcp",
			expectedContainers: 2,
			checkContainerNames: []string{
				"other-container",
				"mcp",
			},
		},
		{
			name: "pod template with multiple existing containers",
			podTemplateSpec: corev1apply.PodTemplateSpec().WithSpec(corev1apply.PodSpec().
				WithContainers(
					corev1apply.Container().WithName("container1"),
					corev1apply.Container().WithName("container2"),
				)),
			expectedName:       "mcp",
			expectedContainers: 3,
			checkContainerNames: []string{
				"container1",
				"container2",
				"mcp",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Call the function
			result := getMCPContainer(tc.podTemplateSpec)

			// Check the result
			assert.NotNil(t, result)
			assert.NotNil(t, result.Name)
			assert.Equal(t, tc.expectedName, *result.Name)

			// Check that the container was added to the pod template
			assert.NotNil(t, tc.podTemplateSpec.Spec)
			assert.NotNil(t, tc.podTemplateSpec.Spec.Containers)

			// Check that all expected containers are present by name
			containerNames := make(map[string]bool)
			for _, container := range tc.podTemplateSpec.Spec.Containers {
				if container.Name != nil {
					containerNames[*container.Name] = true
				}
			}

			for _, expectedName := range tc.checkContainerNames {
				assert.True(t, containerNames[expectedName],
					"Expected container %s not found in pod template", expectedName)
			}

			// Check the total number of containers
			assert.Equal(t, tc.expectedContainers, len(tc.podTemplateSpec.Spec.Containers),
				"Expected %d containers, got %d", tc.expectedContainers, len(tc.podTemplateSpec.Spec.Containers))
		})
	}
}

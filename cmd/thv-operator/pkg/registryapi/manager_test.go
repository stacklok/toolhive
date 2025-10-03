package registryapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
	sourcesmocks "github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources/mocks"
)

func TestNewManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		description string
	}{
		{
			name:        "successful manager creation",
			description: "Should create a new manager with all dependencies",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Create mock dependencies
			mockStorageManager := sourcesmocks.NewMockStorageManager(ctrl)
			mockSourceHandlerFactory := sourcesmocks.NewMockSourceHandlerFactory(ctrl)

			scheme := runtime.NewScheme()

			// Create manager
			manager := NewManager(nil, scheme, mockStorageManager, mockSourceHandlerFactory)

			// Verify manager is created
			assert.NotNil(t, manager)

			// Verify manager implements the interface
			var _ = manager
		})
	}
}

func TestManagerConfigureDeploymentStorage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupMocks     func(*sourcesmocks.MockStorageManager)
		expectedError  string
		validateResult func(*testing.T, *appsv1.Deployment, error)
	}{
		{
			name: "configmap storage manager success",
			setupMocks: func(m *sourcesmocks.MockStorageManager) {
				m.EXPECT().GetType().Return(sources.StorageTypeConfigMap).AnyTimes()
			},
			validateResult: func(t *testing.T, deployment *appsv1.Deployment, err error) {
				t.Helper()
				assert.NoError(t, err)

				// Verify ConfigMap volume was added
				foundVolume := false
				for _, volume := range deployment.Spec.Template.Spec.Volumes {
					if volume.Name == RegistryDataVolumeName {
						foundVolume = true
						assert.NotNil(t, volume.ConfigMap)
						assert.Equal(t, "test-registry-registry-storage", volume.ConfigMap.Name)
						break
					}
				}
				assert.True(t, foundVolume, "ConfigMap volume should be added")

				// Verify volume mount was added to container
				container := findContainerByName(deployment.Spec.Template.Spec.Containers, registryAPIContainerName)
				require.NotNil(t, container)

				foundMount := false
				for _, mount := range container.VolumeMounts {
					if mount.Name == RegistryDataVolumeName {
						foundMount = true
						assert.Equal(t, RegistryDataMountPath, mount.MountPath)
						assert.True(t, mount.ReadOnly)
						break
					}
				}
				assert.True(t, foundMount, "Volume mount should be added to container")

				// Verify container args are set correctly
				expectedArgs := []string{
					ServeCommand,
					"--from-file=/data/registry/registry.json",
					"--registry-name=test-registry",
				}
				assert.Equal(t, expectedArgs, container.Args)
			},
		},
		{
			name: "unsupported storage manager type",
			setupMocks: func(m *sourcesmocks.MockStorageManager) {
				m.EXPECT().GetType().Return("unsupported-type").AnyTimes()
			},
			expectedError: "unsupported storage manager type: unsupported-type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStorageManager := sourcesmocks.NewMockStorageManager(ctrl)
			mockSourceHandlerFactory := sourcesmocks.NewMockSourceHandlerFactory(ctrl)
			tt.setupMocks(mockStorageManager)

			scheme := runtime.NewScheme()
			manager := NewManager(nil, scheme, mockStorageManager, mockSourceHandlerFactory).(*manager)

			// Create a test deployment with the registry-api container
			mcpRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
			}

			deployment := &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  registryAPIContainerName,
									Image: "test-image",
									Args:  []string{"old-args"},
								},
							},
						},
					},
				},
			}

			err := manager.configureDeploymentStorage(deployment, mcpRegistry, registryAPIContainerName)

			if tt.expectedError != "" {
				assert.EqualError(t, err, tt.expectedError)
			} else if tt.validateResult != nil {
				tt.validateResult(t, deployment, err)
			}
		})
	}
}

func TestManagerConfigureConfigMapStorage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		containerName   string
		existingVolumes []corev1.Volume
		existingMounts  []corev1.VolumeMount
		expectedError   string
		validateResult  func(*testing.T, *appsv1.Deployment)
	}{
		{
			name:          "successful configuration",
			containerName: registryAPIContainerName,
			validateResult: func(t *testing.T, deployment *appsv1.Deployment) {
				t.Helper()

				// Check volume was added
				assert.Len(t, deployment.Spec.Template.Spec.Volumes, 1)
				volume := deployment.Spec.Template.Spec.Volumes[0]
				assert.Equal(t, RegistryDataVolumeName, volume.Name)
				assert.NotNil(t, volume.ConfigMap)
				assert.Equal(t, "test-registry-registry-storage", volume.ConfigMap.Name)

				// Check volume mount was added
				container := findContainerByName(deployment.Spec.Template.Spec.Containers, registryAPIContainerName)
				require.NotNil(t, container)
				assert.Len(t, container.VolumeMounts, 1)
				mount := container.VolumeMounts[0]
				assert.Equal(t, RegistryDataVolumeName, mount.Name)
				assert.Equal(t, RegistryDataMountPath, mount.MountPath)
				assert.True(t, mount.ReadOnly)
			},
		},
		{
			name:          "container not found",
			containerName: "nonexistent-container",
			expectedError: "container 'nonexistent-container' not found in deployment",
		},
		{
			name:          "idempotent - volume already exists",
			containerName: registryAPIContainerName,
			existingVolumes: []corev1.Volume{
				{
					Name: RegistryDataVolumeName,
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "existing-configmap",
							},
						},
					},
				},
			},
			validateResult: func(t *testing.T, deployment *appsv1.Deployment) {
				t.Helper()
				// Should still have only one volume
				assert.Len(t, deployment.Spec.Template.Spec.Volumes, 1)
			},
		},
		{
			name:          "idempotent - volume mount already exists",
			containerName: registryAPIContainerName,
			existingMounts: []corev1.VolumeMount{
				{
					Name:      RegistryDataVolumeName,
					MountPath: "/existing/path",
					ReadOnly:  false,
				},
			},
			validateResult: func(t *testing.T, deployment *appsv1.Deployment) {
				t.Helper()
				container := findContainerByName(deployment.Spec.Template.Spec.Containers, registryAPIContainerName)
				require.NotNil(t, container)
				// Should still have only one volume mount
				assert.Len(t, container.VolumeMounts, 1)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			manager := &manager{scheme: scheme}

			// Create test deployment
			mcpRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
			}

			deployment := &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Volumes: tt.existingVolumes,
							Containers: []corev1.Container{
								{
									Name:         registryAPIContainerName,
									Image:        "test-image",
									VolumeMounts: tt.existingMounts,
								},
							},
						},
					},
				},
			}

			err := manager.configureConfigMapStorage(deployment, mcpRegistry, tt.containerName)

			if tt.expectedError != "" {
				assert.EqualError(t, err, tt.expectedError)
			} else {
				assert.NoError(t, err)
				if tt.validateResult != nil {
					tt.validateResult(t, deployment)
				}
			}
		})
	}
}

func TestManagerCheckAPIReadiness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		deployment  *appsv1.Deployment
		expected    bool
		description string
	}{
		{
			name:        "nil deployment",
			deployment:  nil,
			expected:    false,
			description: "Should return false for nil deployment",
		},
		{
			name: "deployment with ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 1,
				},
			},
			expected:    true,
			description: "Should return true when deployment has ready replicas",
		},
		{
			name: "deployment with no ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 0,
				},
			},
			expected:    false,
			description: "Should return false when deployment has no ready replicas",
		},
		{
			name: "deployment with partial ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      3,
					ReadyReplicas: 1,
				},
			},
			expected:    true,
			description: "Should return true when deployment has at least one ready replica",
		},
		{
			name: "deployment with failed condition",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 0,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:    appsv1.DeploymentProgressing,
							Status:  corev1.ConditionFalse,
							Reason:  "ProgressDeadlineExceeded",
							Message: "ReplicaSet has timed out progressing",
						},
					},
				},
			},
			expected:    false,
			description: "Should return false when deployment is not progressing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := &manager{}
			ctx := context.Background()

			result := manager.CheckAPIReadiness(ctx, tt.deployment)

			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

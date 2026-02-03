// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registryapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
)

func TestManagerBuildRegistryAPIDeployment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mcpRegistry    *mcpv1alpha1.MCPRegistry
		setupMocks     func()
		expectedError  string
		validateResult func(*testing.T, *appsv1.Deployment)
	}{
		{
			name: "successful deployment creation",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Registries: []mcpv1alpha1.MCPRegistryConfig{
						{
							Name:   "default",
							Format: mcpv1alpha1.RegistryFormatToolHive,
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "test-configmap",
								},
								Key: "registry.json",
							},
						},
					},
				},
			},
			setupMocks: func() {
			},
			validateResult: func(t *testing.T, deployment *appsv1.Deployment) {
				t.Helper()
				require.NotNil(t, deployment)

				// Verify basic metadata
				assert.Equal(t, "test-registry-api", deployment.Name)
				assert.Equal(t, "test-namespace", deployment.Namespace)

				// Verify labels
				expectedLabels := map[string]string{
					"app.kubernetes.io/name":             "test-registry-api",
					"app.kubernetes.io/component":        "registry-api",
					"app.kubernetes.io/managed-by":       "toolhive-operator",
					"toolhive.stacklok.io/registry-name": "test-registry",
				}
				assert.Equal(t, expectedLabels, deployment.Labels)

				// Verify replica count
				assert.Equal(t, int32(1), *deployment.Spec.Replicas)

				// Verify selector
				expectedSelector := map[string]string{
					"app.kubernetes.io/name":      "test-registry-api",
					"app.kubernetes.io/component": "registry-api",
				}
				assert.Equal(t, expectedSelector, deployment.Spec.Selector.MatchLabels)

				// Verify pod template labels
				assert.Equal(t, expectedLabels, deployment.Spec.Template.Labels)

				// Verify pod template annotations
				assert.Equal(t, "hash-dummy-value", deployment.Spec.Template.Annotations["toolhive.stacklok.dev/config-hash"])

				// Verify service account uses the dynamically generated name (registry-name + "-registry-api")
				assert.Equal(t, "test-registry-registry-api", deployment.Spec.Template.Spec.ServiceAccountName)

				// Verify containers
				require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
				container := deployment.Spec.Template.Spec.Containers[0]
				assert.Equal(t, registryAPIContainerName, container.Name)
				assert.Equal(t, getRegistryAPIImage(), container.Image)

				// Verify container ports
				require.Len(t, container.Ports, 1)
				port := container.Ports[0]
				assert.Equal(t, int32(RegistryAPIPort), port.ContainerPort)
				assert.Equal(t, RegistryAPIPortName, port.Name)
				assert.Equal(t, corev1.ProtocolTCP, port.Protocol)

				// Verify resource requirements
				assert.Equal(t, resource.MustParse(DefaultCPURequest), container.Resources.Requests[corev1.ResourceCPU])
				assert.Equal(t, resource.MustParse(DefaultMemoryRequest), container.Resources.Requests[corev1.ResourceMemory])
				assert.Equal(t, resource.MustParse(DefaultCPULimit), container.Resources.Limits[corev1.ResourceCPU])
				assert.Equal(t, resource.MustParse(DefaultMemoryLimit), container.Resources.Limits[corev1.ResourceMemory])

				// Verify liveness probe
				require.NotNil(t, container.LivenessProbe)
				assert.Equal(t, HealthCheckPath, container.LivenessProbe.HTTPGet.Path)
				assert.Equal(t, intstr.FromInt32(RegistryAPIPort), container.LivenessProbe.HTTPGet.Port)
				assert.Equal(t, int32(LivenessInitialDelay), container.LivenessProbe.InitialDelaySeconds)
				assert.Equal(t, int32(LivenessPeriod), container.LivenessProbe.PeriodSeconds)

				// Verify readiness probe
				require.NotNil(t, container.ReadinessProbe)
				assert.Equal(t, ReadinessCheckPath, container.ReadinessProbe.HTTPGet.Path)
				assert.Equal(t, intstr.FromInt32(RegistryAPIPort), container.ReadinessProbe.HTTPGet.Port)
				assert.Equal(t, int32(ReadinessInitialDelay), container.ReadinessProbe.InitialDelaySeconds)
				assert.Equal(t, int32(ReadinessPeriod), container.ReadinessProbe.PeriodSeconds)

			},
		},
		{
			name: "user PodTemplateSpec merged correctly",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Registries: []mcpv1alpha1.MCPRegistryConfig{
						{
							Name:   "default",
							Format: mcpv1alpha1.RegistryFormatToolHive,
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "test-configmap",
								},
								Key: "registry.json",
							},
						},
					},
					PodTemplateSpec: &runtime.RawExtension{
						Raw: []byte(`{"spec":{"serviceAccountName":"custom-sa"}}`),
					},
				},
			},
			setupMocks: func() {
			},
			validateResult: func(t *testing.T, deployment *appsv1.Deployment) {
				t.Helper()
				require.NotNil(t, deployment)

				// User-provided service account name should take precedence
				assert.Equal(t, "custom-sa", deployment.Spec.Template.Spec.ServiceAccountName)

				// Default volumes and mounts should still be present
				volumes := deployment.Spec.Template.Spec.Volumes
				assert.True(t, hasVolume(volumes, RegistryServerConfigVolumeName))
				assert.True(t, hasVolume(volumes, "storage-data"))
				assert.True(t, hasVolume(volumes, "registry-data-source-default"))
			},
		},
		{
			name: "git auth secrets mounted correctly",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Registries: []mcpv1alpha1.MCPRegistryConfig{
						{
							Name:   "private-git",
							Format: mcpv1alpha1.RegistryFormatToolHive,
							Git: &mcpv1alpha1.GitSource{
								Repository: "https://github.com/example/private-repo.git",
								Branch:     "main",
								Path:       "registry.json",
								Auth: &mcpv1alpha1.GitAuthConfig{
									Username: "git",
									PasswordSecretRef: corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "git-credentials",
										},
										Key: "token",
									},
								},
							},
						},
					},
				},
			},
			setupMocks: func() {
			},
			validateResult: func(t *testing.T, deployment *appsv1.Deployment) {
				t.Helper()
				require.NotNil(t, deployment)

				// Verify git auth volume exists
				volumes := deployment.Spec.Template.Spec.Volumes
				assert.True(t, hasVolume(volumes, "git-auth-git-credentials"), "git auth volume should exist")

				// Find the git auth volume and verify its configuration
				var gitAuthVolume *corev1.Volume
				for i := range volumes {
					if volumes[i].Name == "git-auth-git-credentials" {
						gitAuthVolume = &volumes[i]
						break
					}
				}
				require.NotNil(t, gitAuthVolume)
				require.NotNil(t, gitAuthVolume.Secret)
				assert.Equal(t, "git-credentials", gitAuthVolume.Secret.SecretName)
				require.Len(t, gitAuthVolume.Secret.Items, 1)
				assert.Equal(t, "token", gitAuthVolume.Secret.Items[0].Key)

				// Verify container has git auth volume mount
				require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
				container := deployment.Spec.Template.Spec.Containers[0]

				var gitAuthMount *corev1.VolumeMount
				for i := range container.VolumeMounts {
					if container.VolumeMounts[i].Name == "git-auth-git-credentials" {
						gitAuthMount = &container.VolumeMounts[i]
						break
					}
				}
				require.NotNil(t, gitAuthMount, "git auth volume mount should exist")
				assert.Equal(t, "/secrets/git-credentials", gitAuthMount.MountPath)
				assert.True(t, gitAuthMount.ReadOnly)
			},
		},
		{
			name: "multiple git auth secrets mounted for multiple registries",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Registries: []mcpv1alpha1.MCPRegistryConfig{
						{
							Name:   "private-git-1",
							Format: mcpv1alpha1.RegistryFormatToolHive,
							Git: &mcpv1alpha1.GitSource{
								Repository: "https://github.com/example/private-repo-1.git",
								Branch:     "main",
								Path:       "registry.json",
								Auth: &mcpv1alpha1.GitAuthConfig{
									Username: "git",
									PasswordSecretRef: corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "git-credentials-1",
										},
										Key: "token",
									},
								},
							},
						},
						{
							Name:   "private-git-2",
							Format: mcpv1alpha1.RegistryFormatToolHive,
							Git: &mcpv1alpha1.GitSource{
								Repository: "https://github.com/example/private-repo-2.git",
								Branch:     "main",
								Path:       "registry.json",
								Auth: &mcpv1alpha1.GitAuthConfig{
									Username: "git",
									PasswordSecretRef: corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "git-credentials-2",
										},
										Key: "password",
									},
								},
							},
						},
					},
				},
			},
			setupMocks: func() {
			},
			validateResult: func(t *testing.T, deployment *appsv1.Deployment) {
				t.Helper()
				require.NotNil(t, deployment)

				// Verify both git auth volumes exist
				volumes := deployment.Spec.Template.Spec.Volumes
				assert.True(t, hasVolume(volumes, "git-auth-git-credentials-1"), "git auth volume 1 should exist")
				assert.True(t, hasVolume(volumes, "git-auth-git-credentials-2"), "git auth volume 2 should exist")

				// Verify container has both git auth volume mounts
				require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
				container := deployment.Spec.Template.Spec.Containers[0]

				mountPaths := make(map[string]bool)
				for _, mount := range container.VolumeMounts {
					mountPaths[mount.MountPath] = true
				}
				assert.True(t, mountPaths["/secrets/git-credentials-1"], "git auth mount 1 should exist")
				assert.True(t, mountPaths["/secrets/git-credentials-2"], "git auth mount 2 should exist")
			},
		},
		{
			name: "git registry without auth has no git auth volume",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Registries: []mcpv1alpha1.MCPRegistryConfig{
						{
							Name:   "public-git",
							Format: mcpv1alpha1.RegistryFormatToolHive,
							Git: &mcpv1alpha1.GitSource{
								Repository: "https://github.com/example/public-repo.git",
								Branch:     "main",
								Path:       "registry.json",
								// No Auth specified
							},
						},
					},
				},
			},
			setupMocks: func() {
			},
			validateResult: func(t *testing.T, deployment *appsv1.Deployment) {
				t.Helper()
				require.NotNil(t, deployment)

				// Verify no git auth volumes exist
				volumes := deployment.Spec.Template.Spec.Volumes
				for _, vol := range volumes {
					assert.NotContains(t, vol.Name, "git-auth", "no git auth volume should exist for public repo")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			tt.setupMocks()

			manager := &manager{}

			configManager := config.NewConfigManager(tt.mcpRegistry)
			deployment := manager.buildRegistryAPIDeployment(context.Background(), tt.mcpRegistry, configManager)
			tt.validateResult(t, deployment)
		})
	}
}

func TestGetRegistryAPIImage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		envValue    string
		setEnv      bool
		expected    string
		description string
	}{
		{
			name:        "default image when env not set",
			setEnv:      false,
			expected:    "ghcr.io/stacklok/thv-registry-api:latest",
			description: "Should return default image when environment variable is not set",
		},
		{
			name:        "default image when env empty",
			envValue:    "",
			setEnv:      true,
			expected:    "ghcr.io/stacklok/thv-registry-api:latest",
			description: "Should return default image when environment variable is empty",
		},
		{
			name:        "custom image from env",
			envValue:    "custom-registry/thv-registry-api:v1.0.0",
			setEnv:      true,
			expected:    "custom-registry/thv-registry-api:v1.0.0",
			description: "Should return custom image when environment variable is set",
		},
		{
			name:        "local image from env",
			envValue:    "localhost:5000/thv-registry-api:dev",
			setEnv:      true,
			expected:    "localhost:5000/thv-registry-api:dev",
			description: "Should handle local registry images",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a mock environment getter function for this test case
			envGetter := func(key string) string {
				if key == "TOOLHIVE_REGISTRY_API_IMAGE" && tt.setEnv {
					return tt.envValue
				}
				return ""
			}

			result := getRegistryAPIImageWithEnvGetter(envGetter)
			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

func TestFindContainerByName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		containers  []corev1.Container
		searchName  string
		expected    *corev1.Container
		description string
	}{
		{
			name: "container found",
			containers: []corev1.Container{
				{Name: "container1", Image: "image1"},
				{Name: "container2", Image: "image2"},
			},
			searchName:  "container2",
			expected:    &corev1.Container{Name: "container2", Image: "image2"},
			description: "Should return pointer to found container",
		},
		{
			name: "container not found",
			containers: []corev1.Container{
				{Name: "container1", Image: "image1"},
				{Name: "container2", Image: "image2"},
			},
			searchName:  "nonexistent",
			expected:    nil,
			description: "Should return nil when container is not found",
		},
		{
			name:        "empty containers slice",
			containers:  []corev1.Container{},
			searchName:  "any",
			expected:    nil,
			description: "Should return nil when containers slice is empty",
		},
		{
			name: "multiple containers with same name",
			containers: []corev1.Container{
				{Name: "duplicate", Image: "image1"},
				{Name: "duplicate", Image: "image2"},
			},
			searchName:  "duplicate",
			expected:    &corev1.Container{Name: "duplicate", Image: "image1"},
			description: "Should return first container when multiple have same name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := findContainerByName(tt.containers, tt.searchName)

			if tt.expected == nil {
				assert.Nil(t, result, tt.description)
			} else {
				assert.NotNil(t, result, tt.description)
				assert.Equal(t, tt.expected.Name, result.Name)
				assert.Equal(t, tt.expected.Image, result.Image)
			}
		})
	}
}

func TestHasVolume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		volumes     []corev1.Volume
		searchName  string
		expected    bool
		description string
	}{
		{
			name: "volume found",
			volumes: []corev1.Volume{
				{Name: "volume1"},
				{Name: "volume2"},
			},
			searchName:  "volume2",
			expected:    true,
			description: "Should return true when volume is found",
		},
		{
			name: "volume not found",
			volumes: []corev1.Volume{
				{Name: "volume1"},
				{Name: "volume2"},
			},
			searchName:  "nonexistent",
			expected:    false,
			description: "Should return false when volume is not found",
		},
		{
			name:        "empty volumes slice",
			volumes:     []corev1.Volume{},
			searchName:  "any",
			expected:    false,
			description: "Should return false when volumes slice is empty",
		},
		{
			name: "multiple volumes with same name",
			volumes: []corev1.Volume{
				{Name: "duplicate"},
				{Name: "duplicate"},
			},
			searchName:  "duplicate",
			expected:    true,
			description: "Should return true when any volume has the name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := hasVolume(tt.volumes, tt.searchName)

			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

func TestHasVolumeMount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		volumeMounts []corev1.VolumeMount
		searchName   string
		expected     bool
		description  string
	}{
		{
			name: "volume mount found",
			volumeMounts: []corev1.VolumeMount{
				{Name: "mount1", MountPath: "/path1"},
				{Name: "mount2", MountPath: "/path2"},
			},
			searchName:  "mount2",
			expected:    true,
			description: "Should return true when volume mount is found",
		},
		{
			name: "volume mount not found",
			volumeMounts: []corev1.VolumeMount{
				{Name: "mount1", MountPath: "/path1"},
				{Name: "mount2", MountPath: "/path2"},
			},
			searchName:  "nonexistent",
			expected:    false,
			description: "Should return false when volume mount is not found",
		},
		{
			name:         "empty volume mounts slice",
			volumeMounts: []corev1.VolumeMount{},
			searchName:   "any",
			expected:     false,
			description:  "Should return false when volume mounts slice is empty",
		},
		{
			name: "multiple volume mounts with same name",
			volumeMounts: []corev1.VolumeMount{
				{Name: "duplicate", MountPath: "/path1"},
				{Name: "duplicate", MountPath: "/path2"},
			},
			searchName:  "duplicate",
			expected:    true,
			description: "Should return true when any volume mount has the name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := hasVolumeMount(tt.volumeMounts, tt.searchName)

			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

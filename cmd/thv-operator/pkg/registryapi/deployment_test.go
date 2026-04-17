// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registryapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

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

func TestDeploymentNeedsUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		existing *appsv1.Deployment
		desired  *appsv1.Deployment
		expected bool
	}{
		{
			name:     "nil existing returns true",
			existing: nil,
			desired:  &appsv1.Deployment{},
			expected: true,
		},
		{
			name:     "nil desired returns true",
			existing: &appsv1.Deployment{},
			desired:  nil,
			expected: true,
		},
		{
			name: "identical deployments return false",
			existing: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"toolhive.stacklok.io/podtemplatespec-hash": "abc123",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "hash1",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "registry-api",
								Image: "ghcr.io/stacklok/thv-registry-api:latest",
							}},
						},
					},
				},
			},
			desired: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"toolhive.stacklok.io/podtemplatespec-hash": "abc123",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "hash1",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "registry-api",
								Image: "ghcr.io/stacklok/thv-registry-api:latest",
							}},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "different config hash returns true",
			existing: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "old-hash",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Image: "img:v1"}},
						},
					},
				},
			},
			desired: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "new-hash",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Image: "img:v1"}},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "different podtemplatespec hash returns true",
			existing: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"toolhive.stacklok.io/podtemplatespec-hash": "old-pts-hash",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "same",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Image: "img:v1"}},
						},
					},
				},
			},
			desired: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"toolhive.stacklok.io/podtemplatespec-hash": "new-pts-hash",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "same",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Image: "img:v1"}},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "podtemplatespec hash added returns true",
			existing: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "same",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Image: "img:v1"}},
						},
					},
				},
			},
			desired: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"toolhive.stacklok.io/podtemplatespec-hash": "new-hash",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "same",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Image: "img:v1"}},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "podtemplatespec hash removed returns true",
			existing: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"toolhive.stacklok.io/podtemplatespec-hash": "old-hash",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "same",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Image: "img:v1"}},
						},
					},
				},
			},
			desired: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "same",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Image: "img:v1"}},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "different container image returns true",
			existing: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "same",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "registry-api",
								Image: "ghcr.io/stacklok/thv-registry-api:v1.0.0",
							}},
						},
					},
				},
			},
			desired: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"toolhive.stacklok.dev/config-hash": "same",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "registry-api",
								Image: "ghcr.io/stacklok/thv-registry-api:v2.0.0",
							}},
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := deploymentNeedsUpdate(tt.existing, tt.desired)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildRegistryAPIDeployment_PodTemplateSpecHash(t *testing.T) {
	t.Parallel()

	const baseConfigYAML = "sources:\n  - name: k8s\n    kubernetes: {}\n"

	t.Run("no podtemplatespec has no hash annotation", func(t *testing.T) {
		t.Parallel()
		mgr := &manager{}
		mcpRegistry := &mcpv1beta1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-registry",
				Namespace: "test-namespace",
			},
			Spec: mcpv1beta1.MCPRegistrySpec{
				ConfigYAML: baseConfigYAML,
			},
		}
		deployment, err := mgr.buildRegistryAPIDeployment(context.Background(), mcpRegistry, "test-registry-registry-server-config")
		require.NoError(t, err)

		require.NotNil(t, deployment)
		_, hasPTSHash := deployment.Annotations[podTemplateSpecHashAnnotation]
		assert.False(t, hasPTSHash, "should not have podtemplatespec hash when no PodTemplateSpec is set")
	})

	t.Run("with podtemplatespec has hash annotation", func(t *testing.T) {
		t.Parallel()
		mgr := &manager{}
		mcpRegistry := &mcpv1beta1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-registry",
				Namespace: "test-namespace",
			},
			Spec: mcpv1beta1.MCPRegistrySpec{
				ConfigYAML: baseConfigYAML,
				PodTemplateSpec: &runtime.RawExtension{
					Raw: []byte(`{"spec":{"imagePullSecrets":[{"name":"registry-creds"}]}}`),
				},
			},
		}
		deployment, err := mgr.buildRegistryAPIDeployment(context.Background(), mcpRegistry, "test-registry-registry-server-config")
		require.NoError(t, err)

		require.NotNil(t, deployment)
		ptsHash, hasPTSHash := deployment.Annotations[podTemplateSpecHashAnnotation]
		assert.True(t, hasPTSHash, "should have podtemplatespec hash annotation")
		assert.NotEmpty(t, ptsHash)
	})

	t.Run("different podtemplatespec produces different hash", func(t *testing.T) {
		t.Parallel()
		mgr := &manager{}

		registry1 := &mcpv1beta1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
			Spec: mcpv1beta1.MCPRegistrySpec{
				ConfigYAML:      baseConfigYAML,
				PodTemplateSpec: &runtime.RawExtension{Raw: []byte(`{"spec":{"imagePullSecrets":[{"name":"creds-a"}]}}`)},
			},
		}
		registry2 := &mcpv1beta1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
			Spec: mcpv1beta1.MCPRegistrySpec{
				ConfigYAML:      baseConfigYAML,
				PodTemplateSpec: &runtime.RawExtension{Raw: []byte(`{"spec":{"imagePullSecrets":[{"name":"creds-b"}]}}`)},
			},
		}

		d1, err1 := mgr.buildRegistryAPIDeployment(context.Background(), registry1, "test-registry-server-config")
		d2, err2 := mgr.buildRegistryAPIDeployment(context.Background(), registry2, "test-registry-server-config")
		require.NoError(t, err1)
		require.NoError(t, err2)

		require.NotNil(t, d1)
		require.NotNil(t, d2)
		assert.NotEqual(t, d1.Annotations[podTemplateSpecHashAnnotation], d2.Annotations[podTemplateSpecHashAnnotation])
	})
}

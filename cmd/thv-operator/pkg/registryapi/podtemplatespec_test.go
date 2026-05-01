// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registryapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
)

func TestPodTemplateSpecOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		options    func() []PodTemplateSpecOption
		assertions func(t *testing.T, pts corev1.PodTemplateSpec)
	}{
		// Simple options
		{
			name: "WithLabels sets labels",
			options: func() []PodTemplateSpecOption {
				return []PodTemplateSpecOption{WithLabels(map[string]string{"key1": "value1", "key2": "value2"})}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				assert.Equal(t, "value1", pts.Labels["key1"])
				assert.Equal(t, "value2", pts.Labels["key2"])
			},
		},
		{
			name: "WithAnnotations sets annotations",
			options: func() []PodTemplateSpecOption {
				return []PodTemplateSpecOption{WithAnnotations(map[string]string{"anno1": "val1"})}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				assert.Equal(t, "val1", pts.Annotations["anno1"])
			},
		},
		{
			name: "WithServiceAccountName sets service account",
			options: func() []PodTemplateSpecOption {
				return []PodTemplateSpecOption{WithServiceAccountName("my-service-account")}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				assert.Equal(t, "my-service-account", pts.Spec.ServiceAccountName)
			},
		},
		{
			name: "WithContainer adds container",
			options: func() []PodTemplateSpecOption {
				return []PodTemplateSpecOption{WithContainer(corev1.Container{Name: "test-container", Image: "test-image:latest"})}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				require.Len(t, pts.Spec.Containers, 1)
				assert.Equal(t, "test-container", pts.Spec.Containers[0].Name)
				assert.Equal(t, "test-image:latest", pts.Spec.Containers[0].Image)
			},
		},
		// WithVolume tests
		{
			name: "WithVolume adds volume",
			options: func() []PodTemplateSpecOption {
				return []PodTemplateSpecOption{
					WithVolume(corev1.Volume{
						Name:         "test-volume",
						VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					}),
				}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				require.Len(t, pts.Spec.Volumes, 1)
				assert.Equal(t, "test-volume", pts.Spec.Volumes[0].Name)
			},
		},
		{
			name: "WithVolume is idempotent",
			options: func() []PodTemplateSpecOption {
				volume := corev1.Volume{
					Name:         "test-volume",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				}
				return []PodTemplateSpecOption{WithVolume(volume), WithVolume(volume)}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				assert.Len(t, pts.Spec.Volumes, 1)
			},
		},
		// WithVolumeMount tests
		{
			name: "WithVolumeMount adds mount to existing container",
			options: func() []PodTemplateSpecOption {
				return []PodTemplateSpecOption{
					WithContainer(corev1.Container{Name: "my-container"}),
					WithVolumeMount("my-container", corev1.VolumeMount{Name: "my-mount", MountPath: "/data"}),
				}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				require.Len(t, pts.Spec.Containers, 1)
				require.Len(t, pts.Spec.Containers[0].VolumeMounts, 1)
				assert.Equal(t, "my-mount", pts.Spec.Containers[0].VolumeMounts[0].Name)
			},
		},
		{
			name: "WithVolumeMount does nothing if container not found",
			options: func() []PodTemplateSpecOption {
				return []PodTemplateSpecOption{
					WithVolumeMount("nonexistent", corev1.VolumeMount{Name: "my-mount", MountPath: "/data"}),
				}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				assert.Empty(t, pts.Spec.Containers)
			},
		},
		{
			name: "WithVolumeMount is idempotent",
			options: func() []PodTemplateSpecOption {
				mount := corev1.VolumeMount{Name: "my-mount", MountPath: "/data"}
				return []PodTemplateSpecOption{
					WithContainer(corev1.Container{Name: "my-container"}),
					WithVolumeMount("my-container", mount),
					WithVolumeMount("my-container", mount),
				}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				require.Len(t, pts.Spec.Containers, 1)
				assert.Len(t, pts.Spec.Containers[0].VolumeMounts, 1)
			},
		},
		// WithContainerArgs tests
		{
			name: "WithContainerArgs sets args on existing container",
			options: func() []PodTemplateSpecOption {
				return []PodTemplateSpecOption{
					WithContainer(corev1.Container{Name: "my-container"}),
					WithContainerArgs("my-container", []string{"--flag", "value"}),
				}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				require.Len(t, pts.Spec.Containers, 1)
				assert.Equal(t, []string{"--flag", "value"}, pts.Spec.Containers[0].Args)
			},
		},
		{
			name: "WithContainerArgs does nothing if container not found",
			options: func() []PodTemplateSpecOption {
				return []PodTemplateSpecOption{
					WithContainerArgs("nonexistent", []string{"--flag"}),
				}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				assert.Empty(t, pts.Spec.Containers)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := NewPodTemplateSpecBuilderFrom(nil)
			pts := builder.Apply(tt.options()...).Build()

			tt.assertions(t, pts)
		})
	}
}

func TestRegistryMountOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		options    func() []PodTemplateSpecOption
		assertions func(t *testing.T, pts corev1.PodTemplateSpec)
	}{

		{
			name: "WithRegistryServerConfigMount sets container args with serve command, adds ConfigMap volume and volume mount",
			options: func() []PodTemplateSpecOption {
				return []PodTemplateSpecOption{
					WithContainer(corev1.Container{Name: "registry-api"}),
					WithRegistryServerConfigMount("registry-api", "my-configmap"),
				}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				require.Len(t, pts.Spec.Containers, 1)
				require.Len(t, pts.Spec.Containers[0].Args, 2)
				assert.Contains(t, pts.Spec.Containers[0].Args[0], ServeCommand)
				assert.Contains(t, pts.Spec.Containers[0].Args[1], "--config=")

				require.Len(t, pts.Spec.Volumes, 1)
				assert.Equal(t, RegistryServerConfigVolumeName, pts.Spec.Volumes[0].Name)
				assert.Equal(t, "my-configmap", pts.Spec.Volumes[0].ConfigMap.Name)

				require.Len(t, pts.Spec.Containers[0].VolumeMounts, 1)
				assert.Equal(t, RegistryServerConfigVolumeName, pts.Spec.Containers[0].VolumeMounts[0].Name)
				assert.Equal(t, config.RegistryServerConfigFilePath, pts.Spec.Containers[0].VolumeMounts[0].MountPath)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := NewPodTemplateSpecBuilderFrom(nil)
			pts := builder.Apply(tt.options()...).Build()

			tt.assertions(t, pts)
		})
	}

}

func TestBuildRegistryAPIContainer(t *testing.T) {
	t.Parallel()

	container := BuildRegistryAPIContainer("my-image:v1.0")

	assert.Equal(t, RegistryAPIContainerName, container.Name)
	assert.Equal(t, "my-image:v1.0", container.Image)
	assert.Equal(t, []string{ServeCommand}, container.Args)

	// Check ports
	require.Len(t, container.Ports, 1)
	assert.Equal(t, int32(RegistryAPIPort), container.Ports[0].ContainerPort)
	assert.Equal(t, RegistryAPIPortName, container.Ports[0].Name)

	// Check resources
	assert.NotNil(t, container.Resources.Requests)
	assert.NotNil(t, container.Resources.Limits)

	// Check probes
	assert.NotNil(t, container.LivenessProbe)
	assert.NotNil(t, container.ReadinessProbe)
	assert.Equal(t, HealthCheckPath, container.LivenessProbe.HTTPGet.Path)
	assert.Equal(t, ReadinessCheckPath, container.ReadinessProbe.HTTPGet.Path)
	// Probes hit the internal health listener on RegistryAPIHealthPort,
	// not the public API port. See toolhive-registry-server v1.1.0+.
	assert.Equal(t, intstr.FromInt32(RegistryAPIHealthPort), container.LivenessProbe.HTTPGet.Port)
	assert.Equal(t, intstr.FromInt32(RegistryAPIHealthPort), container.ReadinessProbe.HTTPGet.Port)
}

func TestMergePodTemplateSpecs(t *testing.T) {
	t.Parallel()

	t.Run("nil user returns default", func(t *testing.T) {
		t.Parallel()

		defaultPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				ServiceAccountName: "default-sa",
			},
		}

		result := MergePodTemplateSpecs(defaultPTS, nil)

		assert.Equal(t, "default-sa", result.Spec.ServiceAccountName)
	})

	t.Run("nil default returns user", func(t *testing.T) {
		t.Parallel()

		userPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				ServiceAccountName: "user-sa",
			},
		}

		result := MergePodTemplateSpecs(nil, userPTS)

		assert.Equal(t, "user-sa", result.Spec.ServiceAccountName)
	})

	t.Run("both nil returns empty", func(t *testing.T) {
		t.Parallel()

		result := MergePodTemplateSpecs(nil, nil)

		assert.Equal(t, corev1.PodTemplateSpec{}, result)
	})

	t.Run("user labels override defaults", func(t *testing.T) {
		t.Parallel()

		defaultPTS := &corev1.PodTemplateSpec{}
		defaultPTS.Labels = map[string]string{
			"app":     "default-app",
			"version": "v1",
		}

		userPTS := &corev1.PodTemplateSpec{}
		userPTS.Labels = map[string]string{
			"app": "user-app",
			"env": "prod",
		}

		result := MergePodTemplateSpecs(defaultPTS, userPTS)

		assert.Equal(t, "user-app", result.Labels["app"])
		assert.Equal(t, "v1", result.Labels["version"])
		assert.Equal(t, "prod", result.Labels["env"])
	})

	t.Run("user service account overrides default", func(t *testing.T) {
		t.Parallel()

		defaultPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				ServiceAccountName: "default-sa",
			},
		}

		userPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				ServiceAccountName: "user-sa",
			},
		}

		result := MergePodTemplateSpecs(defaultPTS, userPTS)

		assert.Equal(t, "user-sa", result.Spec.ServiceAccountName)
	})

	t.Run("user container image overrides default", func(t *testing.T) {
		t.Parallel()

		defaultPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "app",
						Image: "default-image:v1",
					},
				},
			},
		}

		userPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "app",
						Image: "user-image:v2",
					},
				},
			},
		}

		result := MergePodTemplateSpecs(defaultPTS, userPTS)

		require.Len(t, result.Spec.Containers, 1)
		assert.Equal(t, "user-image:v2", result.Spec.Containers[0].Image)
	})

	t.Run("user adds new container", func(t *testing.T) {
		t.Parallel()

		defaultPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "app", Image: "app-image:v1"},
				},
			},
		}

		userPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sidecar", Image: "sidecar-image:v1"},
				},
			},
		}

		result := MergePodTemplateSpecs(defaultPTS, userPTS)

		require.Len(t, result.Spec.Containers, 2)
	})

	t.Run("user volume overrides default with same name", func(t *testing.T) {
		t.Parallel()

		defaultPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "default-cm"},
							},
						},
					},
				},
			},
		}

		userPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "user-cm"},
							},
						},
					},
				},
			},
		}

		result := MergePodTemplateSpecs(defaultPTS, userPTS)

		require.Len(t, result.Spec.Volumes, 1)
		assert.Equal(t, "user-cm", result.Spec.Volumes[0].ConfigMap.Name)
	})

	t.Run("user tolerations override defaults", func(t *testing.T) {
		t.Parallel()

		defaultPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Tolerations: []corev1.Toleration{
					{Key: "default-key", Operator: corev1.TolerationOpExists},
				},
			},
		}

		userPTS := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Tolerations: []corev1.Toleration{
					{Key: "user-key", Operator: corev1.TolerationOpEqual, Value: "value"},
				},
			},
		}

		result := MergePodTemplateSpecs(defaultPTS, userPTS)

		require.Len(t, result.Spec.Tolerations, 1)
		assert.Equal(t, "user-key", result.Spec.Tolerations[0].Key)
	})
}

func TestMergeContainer(t *testing.T) {
	t.Parallel()

	t.Run("user image overrides default", func(t *testing.T) {
		t.Parallel()

		defaultContainer := corev1.Container{
			Name:  "app",
			Image: "default:v1",
		}
		userContainer := corev1.Container{
			Name:  "app",
			Image: "user:v2",
		}

		result := mergeContainer(defaultContainer, userContainer)

		assert.Equal(t, "user:v2", result.Image)
	})

	t.Run("default image used when user image empty", func(t *testing.T) {
		t.Parallel()

		defaultContainer := corev1.Container{
			Name:  "app",
			Image: "default:v1",
		}
		userContainer := corev1.Container{
			Name: "app",
		}

		result := mergeContainer(defaultContainer, userContainer)

		assert.Equal(t, "default:v1", result.Image)
	})

	t.Run("env vars merged with user precedence", func(t *testing.T) {
		t.Parallel()

		defaultContainer := corev1.Container{
			Name: "app",
			Env: []corev1.EnvVar{
				{Name: "VAR1", Value: "default1"},
				{Name: "VAR2", Value: "default2"},
			},
		}
		userContainer := corev1.Container{
			Name: "app",
			Env: []corev1.EnvVar{
				{Name: "VAR1", Value: "user1"},
				{Name: "VAR3", Value: "user3"},
			},
		}

		result := mergeContainer(defaultContainer, userContainer)

		require.Len(t, result.Env, 3)
		// Find each env var
		envMap := make(map[string]string)
		for _, e := range result.Env {
			envMap[e.Name] = e.Value
		}
		assert.Equal(t, "user1", envMap["VAR1"])
		assert.Equal(t, "default2", envMap["VAR2"])
		assert.Equal(t, "user3", envMap["VAR3"])
	})

	t.Run("user probe overrides default", func(t *testing.T) {
		t.Parallel()

		defaultContainer := corev1.Container{
			Name: "app",
			LivenessProbe: &corev1.Probe{
				InitialDelaySeconds: 10,
			},
		}
		userContainer := corev1.Container{
			Name: "app",
			LivenessProbe: &corev1.Probe{
				InitialDelaySeconds: 30,
			},
		}

		result := mergeContainer(defaultContainer, userContainer)

		assert.Equal(t, int32(30), result.LivenessProbe.InitialDelaySeconds)
	})

	t.Run("default probe kept when user has none", func(t *testing.T) {
		t.Parallel()

		defaultContainer := corev1.Container{
			Name: "app",
			LivenessProbe: &corev1.Probe{
				InitialDelaySeconds: 10,
			},
		}
		userContainer := corev1.Container{
			Name: "app",
		}

		result := mergeContainer(defaultContainer, userContainer)

		require.NotNil(t, result.LivenessProbe)
		assert.Equal(t, int32(10), result.LivenessProbe.InitialDelaySeconds)
	})
}

func TestParsePodTemplateSpec(t *testing.T) {
	t.Parallel()

	t.Run("nil raw extension returns nil", func(t *testing.T) {
		t.Parallel()

		result, err := ParsePodTemplateSpec(nil)

		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("empty raw extension returns nil", func(t *testing.T) {
		t.Parallel()

		raw := &runtime.RawExtension{}

		result, err := ParsePodTemplateSpec(raw)

		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("valid PodTemplateSpec JSON parses successfully", func(t *testing.T) {
		t.Parallel()

		raw := &runtime.RawExtension{
			Raw: []byte(`{"spec":{"serviceAccountName":"test-sa","containers":[{"name":"test","image":"test:v1"}]}}`),
		}

		result, err := ParsePodTemplateSpec(raw)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "test-sa", result.Spec.ServiceAccountName)
		require.Len(t, result.Spec.Containers, 1)
		assert.Equal(t, "test", result.Spec.Containers[0].Name)
		assert.Equal(t, "test:v1", result.Spec.Containers[0].Image)
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		t.Parallel()

		raw := &runtime.RawExtension{
			Raw: []byte(`{invalid json}`),
		}

		result, err := ParsePodTemplateSpec(raw)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to unmarshal PodTemplateSpec")
	})
}

func TestValidatePodTemplateSpec(t *testing.T) {
	t.Parallel()

	t.Run("nil raw extension is valid", func(t *testing.T) {
		t.Parallel()

		err := ValidatePodTemplateSpec(nil)

		assert.NoError(t, err)
	})

	t.Run("valid PodTemplateSpec is valid", func(t *testing.T) {
		t.Parallel()

		raw := &runtime.RawExtension{
			Raw: []byte(`{"spec":{"serviceAccountName":"test-sa"}}`),
		}

		err := ValidatePodTemplateSpec(raw)

		assert.NoError(t, err)
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		t.Parallel()

		raw := &runtime.RawExtension{
			Raw: []byte(`not valid json`),
		}

		err := ValidatePodTemplateSpec(raw)

		assert.Error(t, err)
	})
}

func TestNewPodTemplateSpecBuilderFrom_NilHandling(t *testing.T) {
	t.Parallel()

	t.Run("nil template returns empty builder", func(t *testing.T) {
		t.Parallel()

		builder := NewPodTemplateSpecBuilderFrom(nil)

		assert.NotNil(t, builder)
		assert.NotNil(t, builder.defaultSpec)
		assert.Nil(t, builder.userTemplate)
	})

	t.Run("valid template is deep copied", func(t *testing.T) {
		t.Parallel()

		original := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				ServiceAccountName: "original-sa",
			},
		}

		builder := NewPodTemplateSpecBuilderFrom(original)

		// Modify the builder's user template
		builder.userTemplate.Spec.ServiceAccountName = "modified-sa"

		// Original should be unchanged
		assert.Equal(t, "original-sa", original.Spec.ServiceAccountName)
		assert.Equal(t, "modified-sa", builder.userTemplate.Spec.ServiceAccountName)
	})
}

func TestNewPodTemplateSpecBuilderFrom_MergeOnBuild(t *testing.T) {
	t.Parallel()

	t.Run("user values take precedence over defaults", func(t *testing.T) {
		t.Parallel()

		userTemplate := &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				ServiceAccountName: "user-sa",
			},
		}

		builder := NewPodTemplateSpecBuilderFrom(userTemplate)
		result := builder.Apply(
			WithServiceAccountName("default-sa"),
			WithLabels(map[string]string{"default-label": "default-value"}),
		).Build()

		// User-specified service account takes precedence
		assert.Equal(t, "user-sa", result.Spec.ServiceAccountName)
		// Default labels are merged in
		assert.Equal(t, "default-value", result.Labels["default-label"])
	})

	t.Run("nil user template behaves like NewPodTemplateSpecBuilder", func(t *testing.T) {
		t.Parallel()

		builder := NewPodTemplateSpecBuilderFrom(nil)
		result := builder.Apply(
			WithServiceAccountName("default-sa"),
			WithLabels(map[string]string{"app": "test"}),
		).Build()

		// Should just have the defaults
		assert.Equal(t, "default-sa", result.Spec.ServiceAccountName)
		assert.Equal(t, "test", result.Labels["app"])
	})
}

func TestWithPGPassSecretRefMount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		secretRef  corev1.SecretKeySelector
		assertions func(t *testing.T, pts corev1.PodTemplateSpec)
	}{
		{
			name: "creates pgpass-secret volume from the referenced secret",
			secretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "my-pgpass"},
				Key:                  ".pgpass",
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				var secretVolume *corev1.Volume
				for i := range pts.Spec.Volumes {
					if pts.Spec.Volumes[i].Name == PGPassSecretVolumeName {
						secretVolume = &pts.Spec.Volumes[i]
						break
					}
				}
				require.NotNil(t, secretVolume, "pgpass-secret volume must exist")
				require.NotNil(t, secretVolume.Secret)
				assert.Equal(t, "my-pgpass", secretVolume.Secret.SecretName)
			},
		},
		{
			name: "creates pgpass emptyDir volume",
			secretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "my-pgpass"},
				Key:                  ".pgpass",
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				var emptyDirVolume *corev1.Volume
				for i := range pts.Spec.Volumes {
					if pts.Spec.Volumes[i].Name == PGPassVolumeName {
						emptyDirVolume = &pts.Spec.Volumes[i]
						break
					}
				}
				require.NotNil(t, emptyDirVolume, "pgpass emptyDir volume must exist")
				require.NotNil(t, emptyDirVolume.EmptyDir)
			},
		},
		{
			name: "creates setup-pgpass init container with correct command image and security context",
			secretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "my-pgpass"},
				Key:                  ".pgpass",
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				require.Len(t, pts.Spec.InitContainers, 1)
				ic := pts.Spec.InitContainers[0]

				assert.Equal(t, PGPassInitContainerName, ic.Name)
				assert.Equal(t, "cgr.dev/chainguard/busybox:latest", ic.Image)
				require.Len(t, ic.Command, 3)
				assert.Equal(t, "sh", ic.Command[0])
				assert.Equal(t, "-c", ic.Command[1])
				assert.Contains(t, ic.Command[2], "cp /secret/.pgpass /pgpass/.pgpass")
				assert.Contains(t, ic.Command[2], "chmod 0600 /pgpass/.pgpass")

				// Security context
				require.NotNil(t, ic.SecurityContext)
				assert.True(t, *ic.SecurityContext.RunAsNonRoot)
				assert.False(t, *ic.SecurityContext.AllowPrivilegeEscalation)
				assert.True(t, *ic.SecurityContext.ReadOnlyRootFilesystem)
				require.NotNil(t, ic.SecurityContext.Capabilities)
				assert.Contains(t, ic.SecurityContext.Capabilities.Drop, corev1.Capability("ALL"))
			},
		},
		{
			name: "creates volume mount on app container at pgpass path with subPath",
			secretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "my-pgpass"},
				Key:                  ".pgpass",
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				require.Len(t, pts.Spec.Containers, 1)
				container := pts.Spec.Containers[0]
				require.Len(t, container.VolumeMounts, 1)

				mount := container.VolumeMounts[0]
				assert.Equal(t, PGPassVolumeName, mount.Name)
				assert.Equal(t, PGPassAppUserMountPath, mount.MountPath)
				assert.Equal(t, ".pgpass", mount.SubPath)
				assert.True(t, mount.ReadOnly)
			},
		},
		{
			name: "creates PGPASSFILE env var",
			secretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "my-pgpass"},
				Key:                  ".pgpass",
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				require.Len(t, pts.Spec.Containers, 1)
				container := pts.Spec.Containers[0]

				var pgpassEnv *corev1.EnvVar
				for i := range container.Env {
					if container.Env[i].Name == pgpassEnvVar {
						pgpassEnv = &container.Env[i]
						break
					}
				}
				require.NotNil(t, pgpassEnv, "PGPASSFILE env var must exist")
				assert.Equal(t, PGPassAppUserMountPath, pgpassEnv.Value)
			},
		},
		{
			name: "no-op when secretRef name is empty",
			secretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ""},
				Key:                  ".pgpass",
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				assert.Empty(t, pts.Spec.Volumes, "no volumes should be added when secret name is empty")
				assert.Empty(t, pts.Spec.InitContainers, "no init containers should be added when secret name is empty")
				require.Len(t, pts.Spec.Containers, 1)
				assert.Empty(t, pts.Spec.Containers[0].VolumeMounts, "no volume mounts should be added when secret name is empty")
				assert.Empty(t, pts.Spec.Containers[0].Env, "no env vars should be added when secret name is empty")
			},
		},
		{
			name: "no-op when secretRef key is empty",
			secretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "my-pgpass"},
				Key:                  "",
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				assert.Empty(t, pts.Spec.Volumes, "no volumes should be added when key is empty")
				assert.Empty(t, pts.Spec.InitContainers, "no init containers should be added when key is empty")
				require.Len(t, pts.Spec.Containers, 1)
				assert.Empty(t, pts.Spec.Containers[0].VolumeMounts, "no volume mounts should be added when key is empty")
				assert.Empty(t, pts.Spec.Containers[0].Env, "no env vars should be added when key is empty")
			},
		},
		{
			name: "uses the correct key from secretRef not hardcoded",
			secretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "custom-secret"},
				Key:                  "custom-key",
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				// Find the pgpass-secret volume and verify it uses the custom key
				var secretVolume *corev1.Volume
				for i := range pts.Spec.Volumes {
					if pts.Spec.Volumes[i].Name == PGPassSecretVolumeName {
						secretVolume = &pts.Spec.Volumes[i]
						break
					}
				}
				require.NotNil(t, secretVolume)
				require.NotNil(t, secretVolume.Secret)
				assert.Equal(t, "custom-secret", secretVolume.Secret.SecretName)
				require.Len(t, secretVolume.Secret.Items, 1)
				// The key should match secretRef.Key, not a hardcoded value
				assert.Equal(t, "custom-key", secretVolume.Secret.Items[0].Key)
				// The path is always .pgpass (the filename is fixed)
				assert.Equal(t, ".pgpass", secretVolume.Secret.Items[0].Path)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := NewPodTemplateSpecBuilderFrom(nil)
			pts := builder.Apply(
				WithContainer(corev1.Container{Name: RegistryAPIContainerName}),
				WithPGPassSecretRefMount(RegistryAPIContainerName, tt.secretRef),
			).Build()

			tt.assertions(t, pts)
		})
	}
}

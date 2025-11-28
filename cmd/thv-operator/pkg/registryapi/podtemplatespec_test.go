package registryapi

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
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
		// WithRegistryStorageMount tests
		{
			name: "WithRegistryStorageMount adds emptyDir volume and volume mount",
			options: func() []PodTemplateSpecOption {
				return []PodTemplateSpecOption{
					WithContainer(corev1.Container{Name: "registry-api"}),
					WithRegistryStorageMount("registry-api"),
				}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				require.Len(t, pts.Spec.Volumes, 1)
				assert.Equal(t, "storage-data", pts.Spec.Volumes[0].Name)
				assert.NotNil(t, pts.Spec.Volumes[0].EmptyDir)

				require.Len(t, pts.Spec.Containers, 1)
				require.Len(t, pts.Spec.Containers[0].VolumeMounts, 1)
				assert.Equal(t, "storage-data", pts.Spec.Containers[0].VolumeMounts[0].Name)
				assert.Equal(t, "/data", pts.Spec.Containers[0].VolumeMounts[0].MountPath)
			},
		},
		// WithRegistrySourceMounts tests
		{
			name: "WithRegistrySourceMounts adds mounts for registries with ConfigMapRef",
			options: func() []PodTemplateSpecOption {
				registries := []mcpv1alpha1.MCPRegistryConfig{
					{
						Name: "reg1",
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "configmap1",
							},
							Key: "data.json",
						},
					},
					{
						Name: "reg2",
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "configmap2",
							},
							Key: "registry.json",
						},
					},
				}
				return []PodTemplateSpecOption{
					WithContainer(corev1.Container{Name: "registry-api"}),
					WithRegistrySourceMounts("registry-api", registries),
				}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				assert.Len(t, pts.Spec.Volumes, 2)
				require.Len(t, pts.Spec.Containers, 1)
				assert.Len(t, pts.Spec.Containers[0].VolumeMounts, 2)
				assert.Equal(t, "registry-data-source-reg1", pts.Spec.Containers[0].VolumeMounts[0].Name)
				assert.Equal(t, "registry-data-source-reg2", pts.Spec.Containers[0].VolumeMounts[1].Name)
				assert.Equal(t, filepath.Join(config.RegistryJSONFilePath, "reg1"), pts.Spec.Containers[0].VolumeMounts[0].MountPath)
				assert.Equal(t, filepath.Join(config.RegistryJSONFilePath, "reg2"), pts.Spec.Containers[0].VolumeMounts[1].MountPath)
			},
		},
		{
			name: "WithRegistrySourceMounts skips registries without ConfigMapRef",
			options: func() []PodTemplateSpecOption {
				registries := []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:         "reg1",
						ConfigMapRef: nil,
					},
				}
				return []PodTemplateSpecOption{
					WithContainer(corev1.Container{Name: "registry-api"}),
					WithRegistrySourceMounts("registry-api", registries),
				}
			},
			assertions: func(t *testing.T, pts corev1.PodTemplateSpec) {
				t.Helper()
				assert.Empty(t, pts.Spec.Volumes)
				require.Len(t, pts.Spec.Containers, 1)
				assert.Empty(t, pts.Spec.Containers[0].VolumeMounts)
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

	assert.Equal(t, registryAPIContainerName, container.Name)
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

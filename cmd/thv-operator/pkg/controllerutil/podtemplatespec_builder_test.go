package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	testContainerName  = "test-container"
	testServiceAccount = "my-sa"
)

func TestNewPodTemplateSpecBuilder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         *runtime.RawExtension
		expectError bool
	}{
		{"nil input", nil, false},
		{"nil Raw field", &runtime.RawExtension{Raw: nil}, false},
		{"empty JSON object", &runtime.RawExtension{Raw: []byte(`{}`)}, false},
		{"valid spec", &runtime.RawExtension{Raw: []byte(`{"spec":{"serviceAccountName":"sa"}}`)}, false},
		{"invalid JSON", &runtime.RawExtension{Raw: []byte(`{invalid}`)}, true},
		{"truncated JSON", &runtime.RawExtension{Raw: []byte(`{"spec":{`)}, true},
		{"empty Raw slice", &runtime.RawExtension{Raw: []byte{}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			builder, err := NewPodTemplateSpecBuilder(tt.raw, testContainerName)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, builder)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, builder)
			}
		})
	}
}

func TestNewPodTemplateSpecBuilder_EmptyContainerName(t *testing.T) {
	t.Parallel()

	builder, err := NewPodTemplateSpecBuilder(nil, "")
	assert.Error(t, err)
	assert.Nil(t, builder)
	assert.Contains(t, err.Error(), "containerName cannot be empty")
}

func TestPodTemplateSpecBuilder_Build(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(*PodTemplateSpecBuilder)
		expectNil bool
	}{
		{
			name:      "empty builder returns nil",
			setup:     func(_ *PodTemplateSpecBuilder) {},
			expectNil: true,
		},
		{
			name: "with service account returns spec",
			setup: func(b *PodTemplateSpecBuilder) {
				sa := testServiceAccount
				b.WithServiceAccount(&sa)
			},
			expectNil: false,
		},
		{
			name: "with secrets returns spec",
			setup: func(b *PodTemplateSpecBuilder) {
				b.WithSecrets([]mcpv1alpha1.SecretRef{{Name: "secret", Key: "key"}})
			},
			expectNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			builder, err := NewPodTemplateSpecBuilder(nil, testContainerName)
			require.NoError(t, err)

			tt.setup(builder)
			result := builder.Build()

			if tt.expectNil {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
			}
		})
	}
}

func TestPodTemplateSpecBuilder_WithServiceAccount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *string
		expected string
	}{
		{"nil pointer", nil, ""},
		{"empty string", ptr(""), ""},
		{"valid name", ptr("my-service-account"), "my-service-account"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			builder, err := NewPodTemplateSpecBuilder(nil, testContainerName)
			require.NoError(t, err)

			builder.WithServiceAccount(tt.input)

			if tt.expected == "" {
				assert.Empty(t, builder.spec.Spec.ServiceAccountName)
			} else {
				assert.Equal(t, tt.expected, builder.spec.Spec.ServiceAccountName)
			}
		})
	}
}

func TestPodTemplateSpecBuilder_WithSecrets(t *testing.T) {
	t.Parallel()

	t.Run("empty secrets does nothing", func(t *testing.T) {
		t.Parallel()
		builder, err := NewPodTemplateSpecBuilder(nil, testContainerName)
		require.NoError(t, err)

		builder.WithSecrets(nil)
		builder.WithSecrets([]mcpv1alpha1.SecretRef{})

		assert.Empty(t, builder.spec.Spec.Containers)
	})

	t.Run("creates container with env vars", func(t *testing.T) {
		t.Parallel()
		builder, err := NewPodTemplateSpecBuilder(nil, testContainerName)
		require.NoError(t, err)

		secrets := []mcpv1alpha1.SecretRef{
			{Name: "my-secret", Key: "API_KEY"},
			{Name: "my-secret", Key: "password", TargetEnvName: "DB_PASSWORD"},
		}
		builder.WithSecrets(secrets)

		require.Len(t, builder.spec.Spec.Containers, 1)
		container := builder.spec.Spec.Containers[0]
		assert.Equal(t, testContainerName, container.Name)
		require.Len(t, container.Env, 2)

		// First secret uses key as env name
		assert.Equal(t, "API_KEY", container.Env[0].Name)
		assert.Equal(t, "my-secret", container.Env[0].ValueFrom.SecretKeyRef.Name)
		assert.Equal(t, "API_KEY", container.Env[0].ValueFrom.SecretKeyRef.Key)

		// Second secret uses targetEnvName
		assert.Equal(t, "DB_PASSWORD", container.Env[1].Name)
		assert.Equal(t, "password", container.Env[1].ValueFrom.SecretKeyRef.Key)
	})

	t.Run("merges into existing container", func(t *testing.T) {
		t.Parallel()
		raw := &runtime.RawExtension{
			Raw: []byte(`{"spec":{"containers":[{"name":"test-container","env":[{"name":"EXISTING","value":"val"}]}]}}`),
		}
		builder, err := NewPodTemplateSpecBuilder(raw, testContainerName)
		require.NoError(t, err)

		builder.WithSecrets([]mcpv1alpha1.SecretRef{{Name: "secret", Key: "NEW_KEY"}})

		require.Len(t, builder.spec.Spec.Containers, 1)
		container := builder.spec.Spec.Containers[0]
		require.Len(t, container.Env, 2)
		assert.Equal(t, "EXISTING", container.Env[0].Name)
		assert.Equal(t, "NEW_KEY", container.Env[1].Name)
	})

	t.Run("adds to different container", func(t *testing.T) {
		t.Parallel()
		raw := &runtime.RawExtension{
			Raw: []byte(`{"spec":{"containers":[{"name":"other-container"}]}}`),
		}
		builder, err := NewPodTemplateSpecBuilder(raw, testContainerName)
		require.NoError(t, err)

		builder.WithSecrets([]mcpv1alpha1.SecretRef{{Name: "secret", Key: "KEY"}})

		require.Len(t, builder.spec.Spec.Containers, 2)
		assert.Equal(t, "other-container", builder.spec.Spec.Containers[0].Name)
		assert.Equal(t, testContainerName, builder.spec.Spec.Containers[1].Name)
	})
}

func TestPodTemplateSpecBuilder_isEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      *runtime.RawExtension
		expected bool
	}{
		{"nil input", nil, true},
		{"empty JSON", &runtime.RawExtension{Raw: []byte(`{}`)}, true},
		{"with serviceAccountName", &runtime.RawExtension{Raw: []byte(`{"spec":{"serviceAccountName":"sa"}}`)}, false},
		{"with containers", &runtime.RawExtension{Raw: []byte(`{"spec":{"containers":[{"name":"app"}]}}`)}, false},
		{"with nodeSelector", &runtime.RawExtension{Raw: []byte(`{"spec":{"nodeSelector":{"zone":"us-west-1"}}}`)}, false},
		{"with tolerations", &runtime.RawExtension{Raw: []byte(`{"spec":{"tolerations":[{"key":"k"}]}}`)}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			builder, err := NewPodTemplateSpecBuilder(tt.raw, testContainerName)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, builder.isEmpty())
		})
	}
}

func TestPodTemplateSpecBuilder_Chaining(t *testing.T) {
	t.Parallel()

	builder, err := NewPodTemplateSpecBuilder(nil, testContainerName)
	require.NoError(t, err)

	sa := testServiceAccount
	result := builder.
		WithServiceAccount(&sa).
		WithSecrets([]mcpv1alpha1.SecretRef{{Name: "secret", Key: "KEY"}}).
		Build()

	require.NotNil(t, result)
	assert.Equal(t, testServiceAccount, result.Spec.ServiceAccountName)
	require.Len(t, result.Spec.Containers, 1)
	assert.Equal(t, testContainerName, result.Spec.Containers[0].Name)
}

func TestPodTemplateSpecBuilder_WithResources(t *testing.T) {
	t.Parallel()

	t.Run("empty resources does nothing", func(t *testing.T) {
		t.Parallel()
		builder, err := NewPodTemplateSpecBuilder(nil, testContainerName)
		require.NoError(t, err)

		// Empty resources should not create a container
		emptyResources := BuildResourceRequirements(mcpv1alpha1.ResourceRequirements{})
		builder.WithResources(emptyResources)

		assert.Empty(t, builder.spec.Spec.Containers)
	})

	t.Run("creates container with resources", func(t *testing.T) {
		t.Parallel()
		builder, err := NewPodTemplateSpecBuilder(nil, testContainerName)
		require.NoError(t, err)

		resources := BuildResourceRequirements(mcpv1alpha1.ResourceRequirements{
			Limits: mcpv1alpha1.ResourceList{
				CPU:    "500m",
				Memory: "512Mi",
			},
			Requests: mcpv1alpha1.ResourceList{
				CPU:    "100m",
				Memory: "128Mi",
			},
		})
		builder.WithResources(resources)

		require.Len(t, builder.spec.Spec.Containers, 1)
		container := builder.spec.Spec.Containers[0]
		assert.Equal(t, testContainerName, container.Name)

		// Verify limits
		assert.Equal(t, "500m", container.Resources.Limits.Cpu().String())
		assert.Equal(t, "512Mi", container.Resources.Limits.Memory().String())

		// Verify requests
		assert.Equal(t, "100m", container.Resources.Requests.Cpu().String())
		assert.Equal(t, "128Mi", container.Resources.Requests.Memory().String())
	})

	t.Run("merges into existing container", func(t *testing.T) {
		t.Parallel()
		raw := &runtime.RawExtension{
			Raw: []byte(`{"spec":{"containers":[{"name":"test-container","image":"test:latest"}]}}`),
		}
		builder, err := NewPodTemplateSpecBuilder(raw, testContainerName)
		require.NoError(t, err)

		resources := BuildResourceRequirements(mcpv1alpha1.ResourceRequirements{
			Limits: mcpv1alpha1.ResourceList{
				CPU: "200m",
			},
		})
		builder.WithResources(resources)

		require.Len(t, builder.spec.Spec.Containers, 1)
		container := builder.spec.Spec.Containers[0]
		assert.Equal(t, "test:latest", container.Image)
		assert.Equal(t, "200m", container.Resources.Limits.Cpu().String())
	})

	t.Run("adds to different container", func(t *testing.T) {
		t.Parallel()
		raw := &runtime.RawExtension{
			Raw: []byte(`{"spec":{"containers":[{"name":"other-container"}]}}`),
		}
		builder, err := NewPodTemplateSpecBuilder(raw, testContainerName)
		require.NoError(t, err)

		resources := BuildResourceRequirements(mcpv1alpha1.ResourceRequirements{
			Requests: mcpv1alpha1.ResourceList{
				Memory: "64Mi",
			},
		})
		builder.WithResources(resources)

		require.Len(t, builder.spec.Spec.Containers, 2)
		assert.Equal(t, "other-container", builder.spec.Spec.Containers[0].Name)
		assert.Equal(t, testContainerName, builder.spec.Spec.Containers[1].Name)
		assert.Equal(t, "64Mi", builder.spec.Spec.Containers[1].Resources.Requests.Memory().String())
	})
}

func TestPodTemplateSpecBuilder_WithResourcesChaining(t *testing.T) {
	t.Parallel()

	builder, err := NewPodTemplateSpecBuilder(nil, testContainerName)
	require.NoError(t, err)

	sa := testServiceAccount
	resources := BuildResourceRequirements(mcpv1alpha1.ResourceRequirements{
		Limits: mcpv1alpha1.ResourceList{
			CPU:    "1000m",
			Memory: "1Gi",
		},
	})

	result := builder.
		WithServiceAccount(&sa).
		WithSecrets([]mcpv1alpha1.SecretRef{{Name: "secret", Key: "KEY"}}).
		WithResources(resources).
		Build()

	require.NotNil(t, result)
	assert.Equal(t, testServiceAccount, result.Spec.ServiceAccountName)
	require.Len(t, result.Spec.Containers, 1)

	container := result.Spec.Containers[0]
	assert.Equal(t, testContainerName, container.Name)
	assert.Len(t, container.Env, 1)
	// 1000m is normalized to 1 CPU core
	assert.Equal(t, "1", container.Resources.Limits.Cpu().String())
	assert.Equal(t, "1Gi", container.Resources.Limits.Memory().String())
}

func TestPodTemplateSpecBuilder_WithResourcesMerging(t *testing.T) {
	t.Parallel()

	t.Run("existing resources take precedence over defaults", func(t *testing.T) {
		t.Parallel()
		// Create pod template with existing resources
		raw := &runtime.RawExtension{
			Raw: []byte(`{
				"spec": {
					"containers": [{
						"name": "test-container",
						"resources": {
							"limits": {"cpu": "2", "memory": "2Gi"},
							"requests": {"cpu": "500m"}
						}
					}]
				}
			}`),
		}
		builder, err := NewPodTemplateSpecBuilder(raw, testContainerName)
		require.NoError(t, err)

		// Try to set different resources (these should be overridden by existing)
		defaultResources := BuildResourceRequirements(mcpv1alpha1.ResourceRequirements{
			Limits: mcpv1alpha1.ResourceList{
				CPU:    "1",
				Memory: "1Gi",
			},
			Requests: mcpv1alpha1.ResourceList{
				CPU:    "100m",
				Memory: "128Mi",
			},
		})

		builder.WithResources(defaultResources)
		result := builder.Build()

		require.NotNil(t, result)
		require.Len(t, result.Spec.Containers, 1)
		container := result.Spec.Containers[0]

		// Existing values should be preserved
		assert.Equal(t, "2", container.Resources.Limits.Cpu().String())
		assert.Equal(t, "2Gi", container.Resources.Limits.Memory().String())
		assert.Equal(t, "500m", container.Resources.Requests.Cpu().String())
		// Memory request was not in existing, so default should be used
		assert.Equal(t, "128Mi", container.Resources.Requests.Memory().String())
	})

	t.Run("defaults fill in missing resources", func(t *testing.T) {
		t.Parallel()
		// Create pod template with partial resources (only CPU limit)
		raw := &runtime.RawExtension{
			Raw: []byte(`{
				"spec": {
					"containers": [{
						"name": "test-container",
						"resources": {
							"limits": {"cpu": "3"}
						}
					}]
				}
			}`),
		}
		builder, err := NewPodTemplateSpecBuilder(raw, testContainerName)
		require.NoError(t, err)

		// Set complete resource defaults
		defaultResources := BuildResourceRequirements(mcpv1alpha1.ResourceRequirements{
			Limits: mcpv1alpha1.ResourceList{
				CPU:    "1",
				Memory: "1Gi",
			},
			Requests: mcpv1alpha1.ResourceList{
				CPU:    "100m",
				Memory: "128Mi",
			},
		})

		builder.WithResources(defaultResources)
		result := builder.Build()

		require.NotNil(t, result)
		require.Len(t, result.Spec.Containers, 1)
		container := result.Spec.Containers[0]

		// Existing CPU limit should be preserved
		assert.Equal(t, "3", container.Resources.Limits.Cpu().String())
		// Other resources should come from defaults
		assert.Equal(t, "1Gi", container.Resources.Limits.Memory().String())
		assert.Equal(t, "100m", container.Resources.Requests.Cpu().String())
		assert.Equal(t, "128Mi", container.Resources.Requests.Memory().String())
	})
}

// ptr is a helper to create a pointer to a string.
func ptr(s string) *string {
	return &s
}

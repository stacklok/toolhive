package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestMCPServerPodTemplateSpecBuilder_InvalidSpec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		rawExtension  *runtime.RawExtension
		expectError   bool
		errorContains string
	}{
		{
			name:         "nil_raw_extension",
			rawExtension: nil,
			expectError:  false,
		},
		{
			name: "empty_raw_bytes",
			rawExtension: &runtime.RawExtension{
				Raw: []byte{},
			},
			expectError:   true,
			errorContains: "unexpected end of JSON input",
		},
		{
			name: "invalid_json",
			rawExtension: &runtime.RawExtension{
				Raw: []byte(`{invalid json`),
			},
			expectError:   true,
			errorContains: "failed to unmarshal PodTemplateSpec",
		},
		{
			name: "invalid_yaml",
			rawExtension: &runtime.RawExtension{
				Raw: []byte(`
spec:
  containers:
    - name: test
      invalid_field: [unclosed
`),
			},
			expectError:   true,
			errorContains: "failed to unmarshal PodTemplateSpec",
		},
		{
			name: "wrong_type",
			rawExtension: &runtime.RawExtension{
				Raw: []byte(`{"kind": "Service", "apiVersion": "v1"}`),
			},
			expectError: false, // This will succeed but create an empty PodTemplateSpec
		},
		{
			name: "valid_podtemplatespec",
			rawExtension: &runtime.RawExtension{
				Raw: []byte(`{"spec": {"containers": [{"name": "test"}]}}`),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			builder, err := NewMCPServerPodTemplateSpecBuilder(tt.rawExtension)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				assert.Nil(t, builder)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, builder)
			}
		})
	}
}

func TestMCPServerPodTemplateSpecBuilder_InvalidSpecWithSecrets(t *testing.T) {
	t.Parallel()
	// Test that even with an invalid spec, we can still create a builder with nil input
	// and add secrets to it
	builder, err := NewMCPServerPodTemplateSpecBuilder(nil)
	require.NoError(t, err)

	secrets := []mcpv1alpha1.SecretRef{
		{Name: "secret1", Key: "key1"},
		{Name: "secret2", Key: "key2", TargetEnvName: "CUSTOM_ENV"},
	}

	result := builder.WithSecrets(secrets).Build()
	require.NotNil(t, result)
	require.Len(t, result.Spec.Containers, 1)
	require.Equal(t, mcpContainerName, result.Spec.Containers[0].Name)
	require.Len(t, result.Spec.Containers[0].Env, 2)
}

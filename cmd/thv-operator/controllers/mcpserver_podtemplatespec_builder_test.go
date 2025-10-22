package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestMCPServerPodTemplateSpecBuilder_AllCombinations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		userTemplate           *runtime.RawExtension
		serviceAccount         *string
		secrets                []mcpv1alpha1.SecretRef
		expectedServiceAccount string
		expectedSecrets        int
		expectedContainers     int
		expectNil              bool
		description            string
	}{
		// Base cases - all nil/empty
		{
			name:        "all_nil_empty",
			expectNil:   true,
			description: "No user template, no service account, no secrets should return nil",
		},
		{
			name:         "empty_user_template_only",
			userTemplate: podTemplateSpecToRawExtension(t, &corev1.PodTemplateSpec{}),
			expectNil:    true,
			description:  "Empty user template with no other customizations should return nil",
		},

		// Service account only cases
		{
			name:                   "service_account_only",
			serviceAccount:         ptr.To("test-sa"),
			expectedServiceAccount: "test-sa",
			expectedContainers:     0,
			description:            "Only service account should create spec with service account",
		},
		{
			name:           "empty_service_account_only",
			serviceAccount: ptr.To(""),
			expectNil:      true,
			description:    "Empty service account string should return nil",
		},

		// Secrets only cases
		{
			name: "single_secret_only",
			secrets: []mcpv1alpha1.SecretRef{
				{Name: "secret1", Key: "key1"},
			},
			expectedSecrets:    1,
			expectedContainers: 1,
			description:        "Single secret should create MCP container with env var",
		},
		{
			name: "multiple_secrets_only",
			secrets: []mcpv1alpha1.SecretRef{
				{Name: "secret1", Key: "key1"},
				{Name: "secret2", Key: "key2", TargetEnvName: "CUSTOM_ENV"},
			},
			expectedSecrets:    2,
			expectedContainers: 1,
			description:        "Multiple secrets should create MCP container with multiple env vars",
		},
		{
			name:        "empty_secrets_only",
			secrets:     []mcpv1alpha1.SecretRef{},
			expectNil:   true,
			description: "Empty secrets slice should return nil",
		},

		// Combined service account and secrets
		{
			name:           "service_account_and_single_secret",
			serviceAccount: ptr.To("test-sa"),
			secrets: []mcpv1alpha1.SecretRef{
				{Name: "secret1", Key: "key1"},
			},
			expectedServiceAccount: "test-sa",
			expectedSecrets:        1,
			expectedContainers:     1,
			description:            "Service account and single secret should combine properly",
		},
		{
			name:           "service_account_and_multiple_secrets",
			serviceAccount: ptr.To("test-sa"),
			secrets: []mcpv1alpha1.SecretRef{
				{Name: "secret1", Key: "key1"},
				{Name: "secret2", Key: "key2", TargetEnvName: "CUSTOM_ENV"},
				{Name: "secret3", Key: "key3"},
			},
			expectedServiceAccount: "test-sa",
			expectedSecrets:        3,
			expectedContainers:     1,
			description:            "Service account and multiple secrets should combine properly",
		},

		// User template with various combinations
		{
			name: "user_template_with_existing_mcp_container_and_service_account",
			userTemplate: podTemplateSpecToRawExtension(t, &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: "user-sa",
					Containers: []corev1.Container{
						{
							Name: "other-container",
							Env:  []corev1.EnvVar{{Name: "OTHER_ENV", Value: "value"}},
						},
						{
							Name: mcpContainerName,
							Env:  []corev1.EnvVar{{Name: "EXISTING_ENV", Value: "existing"}},
						},
					},
				},
			}),
			serviceAccount: ptr.To("override-sa"),
			secrets: []mcpv1alpha1.SecretRef{
				{Name: "secret1", Key: "key1"},
			},
			expectedServiceAccount: "override-sa",
			expectedSecrets:        2, // existing + new secret env
			expectedContainers:     2,
			description:            "User template with existing MCP container should merge env vars and override service account",
		},
		{
			name: "user_template_without_mcp_container_and_secrets",
			userTemplate: podTemplateSpecToRawExtension(t, &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "other-container",
							Env:  []corev1.EnvVar{{Name: "OTHER_ENV", Value: "value"}},
						},
					},
				},
			}),
			secrets: []mcpv1alpha1.SecretRef{
				{Name: "secret1", Key: "key1"},
			},
			expectedSecrets:    1,
			expectedContainers: 2, // other + new mcp container
			description:        "User template without MCP container should add new MCP container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Build the PodTemplateSpec
			builder, err := NewMCPServerPodTemplateSpecBuilder(tt.userTemplate)
			require.NoError(t, err, "Failed to create builder")

			result := builder.
				WithServiceAccount(tt.serviceAccount).
				WithSecrets(tt.secrets).
				Build()

			if tt.expectNil {
				assert.Nil(t, result, "Expected nil result for case: %s", tt.description)
				return
			}

			require.NotNil(t, result, "Expected non-nil result for case: %s", tt.description)

			// Check service account
			assert.Equal(t, tt.expectedServiceAccount, result.Spec.ServiceAccountName,
				"Service account mismatch for case: %s", tt.description)

			// Check number of containers
			assert.Len(t, result.Spec.Containers, tt.expectedContainers,
				"Container count mismatch for case: %s", tt.description)

			// If we expect secrets, check the MCP container env vars
			if tt.expectedSecrets > 0 {
				mcpContainer := findMCPContainer(result.Spec.Containers)
				require.NotNil(t, mcpContainer, "Expected MCP container for case: %s", tt.description)
				assert.Len(t, mcpContainer.Env, tt.expectedSecrets,
					"Secret env var count mismatch for case: %s", tt.description)

				// Validate secret env vars structure
				for _, envVar := range mcpContainer.Env {
					if envVar.ValueFrom != nil && envVar.ValueFrom.SecretKeyRef != nil {
						assert.NotEmpty(t, envVar.Name, "Secret env var should have name")
						assert.NotEmpty(t, envVar.ValueFrom.SecretKeyRef.Name, "Secret ref should have name")
						assert.NotEmpty(t, envVar.ValueFrom.SecretKeyRef.Key, "Secret ref should have key")
					}
				}
			}
		})
	}
}

func TestMCPServerPodTemplateSpecBuilder_SecretEnvVarNaming(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		secret      mcpv1alpha1.SecretRef
		expectedEnv string
	}{
		{
			name:        "use_key_as_env_name",
			secret:      mcpv1alpha1.SecretRef{Name: "secret1", Key: "DATABASE_PASSWORD"},
			expectedEnv: "DATABASE_PASSWORD",
		},
		{
			name:        "use_custom_target_env_name",
			secret:      mcpv1alpha1.SecretRef{Name: "secret1", Key: "key1", TargetEnvName: "DB_PASSWORD"},
			expectedEnv: "DB_PASSWORD",
		},
		{
			name:        "empty_target_env_name_uses_key",
			secret:      mcpv1alpha1.SecretRef{Name: "secret1", Key: "api-token", TargetEnvName: ""},
			expectedEnv: "api-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			builder, err := NewMCPServerPodTemplateSpecBuilder(nil)
			require.NoError(t, err, "Failed to create builder")

			result := builder.
				WithSecrets([]mcpv1alpha1.SecretRef{tt.secret}).
				Build()

			require.NotNil(t, result)
			mcpContainer := findMCPContainer(result.Spec.Containers)
			require.NotNil(t, mcpContainer)
			require.Len(t, mcpContainer.Env, 1)

			envVar := mcpContainer.Env[0]
			assert.Equal(t, tt.expectedEnv, envVar.Name)
			assert.Equal(t, tt.secret.Name, envVar.ValueFrom.SecretKeyRef.Name)
			assert.Equal(t, tt.secret.Key, envVar.ValueFrom.SecretKeyRef.Key)
		})
	}
}

func TestMCPServerPodTemplateSpecBuilder_IsEmpty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		setupBuilder   func() *MCPServerPodTemplateSpecBuilder
		expectedEmpty  bool
		expectedResult bool // true if Build() should return non-nil
	}{
		{
			name: "completely_empty",
			setupBuilder: func() *MCPServerPodTemplateSpecBuilder {
				builder, _ := NewMCPServerPodTemplateSpecBuilder(nil)
				return builder
			},
			expectedEmpty:  true,
			expectedResult: false,
		},
		{
			name: "with_service_account",
			setupBuilder: func() *MCPServerPodTemplateSpecBuilder {
				sa := "test-sa"
				builder, _ := NewMCPServerPodTemplateSpecBuilder(nil)
				return builder.WithServiceAccount(&sa)
			},
			expectedEmpty:  false,
			expectedResult: true,
		},
		{
			name: "with_secrets",
			setupBuilder: func() *MCPServerPodTemplateSpecBuilder {
				builder, _ := NewMCPServerPodTemplateSpecBuilder(nil)
				return builder.WithSecrets([]mcpv1alpha1.SecretRef{
					{Name: "secret1", Key: "key1"},
				})
			},
			expectedEmpty:  false,
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			builder := tt.setupBuilder()

			// Test isEmpty method
			isEmpty := builder.isEmpty()
			assert.Equal(t, tt.expectedEmpty, isEmpty)

			// Test that Build() respects isEmpty
			result := builder.Build()
			if tt.expectedResult {
				assert.NotNil(t, result)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

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

// Helper function to find MCP container in a slice
func findMCPContainer(containers []corev1.Container) *corev1.Container {
	for i, container := range containers {
		if container.Name == mcpContainerName {
			return &containers[i]
		}
	}
	return nil
}

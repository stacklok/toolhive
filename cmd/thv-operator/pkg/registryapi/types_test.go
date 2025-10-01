package registryapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// TestLabelsForRegistryAPI tests the label generation function
func TestLabelsForRegistryAPI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		mcpRegistry  *mcpv1alpha1.MCPRegistry
		resourceName string
		expected     map[string]string
		description  string
	}{
		{
			name: "BasicLabels",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-registry",
				},
			},
			resourceName: "test-registry-api",
			expected: map[string]string{
				"app.kubernetes.io/name":             "test-registry-api",
				"app.kubernetes.io/component":        "registry-api",
				"app.kubernetes.io/managed-by":       "toolhive-operator",
				"toolhive.stacklok.io/registry-name": "test-registry",
			},
			description: "Should generate correct labels for basic MCPRegistry",
		},
		{
			name: "LabelsWithSpecialCharacters",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-special-registry-123",
				},
			},
			resourceName: "my-special-registry-123-api",
			expected: map[string]string{
				"app.kubernetes.io/name":             "my-special-registry-123-api",
				"app.kubernetes.io/component":        "registry-api",
				"app.kubernetes.io/managed-by":       "toolhive-operator",
				"toolhive.stacklok.io/registry-name": "my-special-registry-123",
			},
			description: "Should handle registry names with special characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := labelsForRegistryAPI(tt.mcpRegistry, tt.resourceName)
			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

// TestGetConfigMapName tests ConfigMap name generation
func TestGetConfigMapName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		mcpRegistry *mcpv1alpha1.MCPRegistry
		expected    string
		description string
	}{
		{
			name: "BasicConfigMapName",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-registry",
				},
			},
			expected:    "test-registry-registry-storage",
			description: "Should generate correct ConfigMap name for basic registry",
		},
		{
			name: "ConfigMapNameWithSpecialChars",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-special-registry-123",
				},
			},
			expected:    "my-special-registry-123-registry-storage",
			description: "Should handle special characters in registry name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := getConfigMapName(tt.mcpRegistry)
			assert.Equal(t, tt.expected, result, tt.description)

			// Also verify it matches the MCPRegistry helper method
			assert.Equal(t, tt.mcpRegistry.GetStorageName(), result,
				"getConfigMapName should match MCPRegistry.GetStorageName()")
		})
	}
}

// TestMCPRegistryHelperMethods tests the helper methods on MCPRegistry type
func TestMCPRegistryHelperMethods(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                    string
		registryName            string
		expectedStorageName     string
		expectedAPIResourceName string
		description             string
	}{
		{
			name:                    "BasicNames",
			registryName:            "test-registry",
			expectedStorageName:     "test-registry-registry-storage",
			expectedAPIResourceName: "test-registry-api",
			description:             "Should generate correct resource names for basic registry",
		},
		{
			name:                    "NamesWithSpecialChars",
			registryName:            "my-special-registry-123",
			expectedStorageName:     "my-special-registry-123-registry-storage",
			expectedAPIResourceName: "my-special-registry-123-api",
			description:             "Should handle special characters in registry name",
		},
		{
			name:                    "MinimalNames",
			registryName:            "a",
			expectedStorageName:     "a-registry-storage",
			expectedAPIResourceName: "a-api",
			description:             "Should handle minimal registry name",
		},
		{
			name:                    "LongNames",
			registryName:            "this-is-a-very-long-registry-name-that-should-work-fine",
			expectedStorageName:     "this-is-a-very-long-registry-name-that-should-work-fine-registry-storage",
			expectedAPIResourceName: "this-is-a-very-long-registry-name-that-should-work-fine-api",
			description:             "Should handle long registry names",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mcpRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name: tt.registryName,
				},
			}

			storageName := mcpRegistry.GetStorageName()
			assert.Equal(t, tt.expectedStorageName, storageName,
				"GetStorageName should return expected storage name")

			apiResourceName := mcpRegistry.GetAPIResourceName()
			assert.Equal(t, tt.expectedAPIResourceName, apiResourceName,
				"GetAPIResourceName should return expected API resource name")
		})
	}
}

// TestFindContainerByNameEdgeCases tests edge cases for findContainerByName helper function
func TestFindContainerByNameEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		containers  []corev1.Container
		searchName  string
		expected    *corev1.Container
		description string
	}{
		{
			name:        "EmptySlice",
			containers:  []corev1.Container{},
			searchName:  "any",
			expected:    nil,
			description: "Should return nil for empty containers slice",
		},
		{
			name:        "NilSlice",
			containers:  nil,
			searchName:  "any",
			expected:    nil,
			description: "Should handle nil containers slice gracefully",
		},
		{
			name: "EmptySearchName",
			containers: []corev1.Container{
				{Name: "", Image: "image1"},
				{Name: "container2", Image: "image2"},
			},
			searchName:  "",
			expected:    &corev1.Container{Name: "", Image: "image1"},
			description: "Should find container with empty name",
		},
		{
			name: "CaseSensitive",
			containers: []corev1.Container{
				{Name: "Container", Image: "image1"},
				{Name: "container", Image: "image2"},
			},
			searchName:  "container",
			expected:    &corev1.Container{Name: "container", Image: "image2"},
			description: "Should be case sensitive",
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

// TestHasVolumeEdgeCases tests edge cases for hasVolume helper function
func TestHasVolumeEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		volumes     []corev1.Volume
		searchName  string
		expected    bool
		description string
	}{
		{
			name:        "EmptySlice",
			volumes:     []corev1.Volume{},
			searchName:  "any",
			expected:    false,
			description: "Should return false for empty volumes slice",
		},
		{
			name:        "NilSlice",
			volumes:     nil,
			searchName:  "any",
			expected:    false,
			description: "Should handle nil volumes slice gracefully",
		},
		{
			name: "EmptySearchName",
			volumes: []corev1.Volume{
				{Name: ""},
				{Name: "volume2"},
			},
			searchName:  "",
			expected:    true,
			description: "Should find volume with empty name",
		},
		{
			name: "CaseSensitive",
			volumes: []corev1.Volume{
				{Name: "Volume"},
				{Name: "volume"},
			},
			searchName:  "volume",
			expected:    true,
			description: "Should be case sensitive",
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

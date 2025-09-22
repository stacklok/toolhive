package validation

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

func TestAlwaysAllowValidator(t *testing.T) {
	t.Parallel()

	validator := &AlwaysAllowValidator{}
	ctx := context.Background()

	tests := []struct {
		name  string
		image string
	}{
		{
			name:  "allows any image",
			image: "docker.io/example/image:latest",
		},
		{
			name:  "allows empty image",
			image: "",
		},
		{
			name:  "allows invalid image format",
			image: "not-a-valid-image!!!",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validator.ValidateImage(ctx, tt.image)
			assert.ErrorIs(t, err, ErrImageNotChecked)
		})
	}
}

func TestNewImageValidator(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	tests := []struct {
		name         string
		envValue     string
		expectedType string
		setupEnv     bool
	}{
		{
			name:         "returns AlwaysAllowValidator when env not set",
			envValue:     "",
			expectedType: "*validation.AlwaysAllowValidator",
			setupEnv:     false,
		},
		{
			name:         "returns AlwaysAllowValidator when env is false",
			envValue:     "false",
			expectedType: "*validation.AlwaysAllowValidator",
			setupEnv:     true,
		},
		{
			name:         "returns RegistryEnforcingValidator when env is true",
			envValue:     "true",
			expectedType: "*validation.RegistryEnforcingValidator",
			setupEnv:     true,
		},
		{
			name:         "returns AlwaysAllowValidator for any other value",
			envValue:     "yes",
			expectedType: "*validation.AlwaysAllowValidator",
			setupEnv:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Save original env value
			originalValue := os.Getenv("TOOLHIVE_EXPERIMENTAL_REGISTRY_ENFORCEMENT")
			defer func() {
				os.Setenv("TOOLHIVE_EXPERIMENTAL_REGISTRY_ENFORCEMENT", originalValue)
			}()

			// Set test env value
			if tt.setupEnv {
				os.Setenv("TOOLHIVE_EXPERIMENTAL_REGISTRY_ENFORCEMENT", tt.envValue)
			} else {
				os.Unsetenv("TOOLHIVE_EXPERIMENTAL_REGISTRY_ENFORCEMENT")
			}

			var validationType ImageValidation
			if tt.envValue == "true" {
				validationType = ImageValidationRegistryEnforcing
			} else {
				validationType = ImageValidationAlwaysAllow
			}

			validator := NewImageValidator(fakeClient, "test-namespace", validationType)
			assert.NotNil(t, validator)
			assert.Equal(t, tt.expectedType, fmt.Sprintf("%T", validator))
		})
	}
}

func TestRegistryEnforcingValidator_ValidateImage(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	ctx := context.Background()

	// Test registry data
	registryDataWithImage := `{
		"version": "1.0",
		"servers": {
			"test-server": {
				"name": "test-server",
				"image": "docker.io/toolhive/test:v1.0.0",
				"description": "Test server"
			},
			"another-server": {
				"name": "another-server",
				"image": "docker.io/toolhive/another:latest",
				"description": "Another server"
			}
		},
		"groups": [
			{
				"name": "group1",
				"description": "Test group",
				"servers": {
					"group-server": {
						"name": "group-server",
						"image": "docker.io/toolhive/group:v2.0.0",
						"description": "Group server"
					}
				}
			}
		]
	}`

	emptyRegistryData := `{
		"version": "1.0",
		"servers": {}
	}`

	tests := []struct {
		name             string
		namespace        string
		image            string
		registries       []runtime.Object
		configMaps       []runtime.Object
		expectedValid    bool
		expectedError    bool
		expectedErrorMsg string
	}{
		{
			name:          "no registries - validation passes",
			namespace:     "test-namespace",
			image:         "docker.io/toolhive/test:v1.0.0",
			expectedValid: true,
		},
		{
			name:      "registry without enforce - validation passes",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: false,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			expectedValid: true,
		},
		{
			name:      "enforcing registry with image present - validation passes",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			configMaps: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-registry-registry-storage",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"registry.json": registryDataWithImage,
					},
				},
			},
			expectedValid: true,
		},
		{
			name:      "enforcing registry with image in group - validation passes",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/group:v2.0.0",
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			configMaps: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-registry-registry-storage",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"registry.json": registryDataWithImage,
					},
				},
			},
			expectedValid: true,
		},
		{
			name:      "enforcing registry with image not present - validation fails",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/missing:v1.0.0",
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			configMaps: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-registry-registry-storage",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"registry.json": registryDataWithImage,
					},
				},
			},
			expectedValid:    false,
			expectedError:    true,
			expectedErrorMsg: "not found in enforced registries",
		},
		{
			name:      "enforcing registry with empty registry data - validation fails",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			configMaps: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-registry-registry-storage",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"registry.json": emptyRegistryData,
					},
				},
			},
			expectedValid:    false,
			expectedError:    true,
			expectedErrorMsg: "not found in enforced registries",
		},
		{
			name:      "enforcing registry not ready - skips validation",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhasePending,
					},
				},
			},
			expectedValid:    false,
			expectedError:    true,
			expectedErrorMsg: "not found in enforced registries",
		},
		{
			name:      "multiple registries with mixed enforce - image only in non-enforcing should fail",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "enforcing-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "non-enforcing-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: false,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			configMaps: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "enforcing-registry-registry-storage",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"registry.json": emptyRegistryData,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "non-enforcing-registry-registry-storage",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"registry.json": registryDataWithImage,
					},
				},
			},
			expectedValid:    false,
			expectedError:    true,
			expectedErrorMsg: "not found in enforced registries",
		},
		{
			name:      "missing ConfigMap - enforcing registry without ConfigMap should fail",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "registry-without-configmap",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "registry-with-configmap",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: false,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			configMaps: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "registry-with-configmap-registry-storage",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"registry.json": registryDataWithImage,
					},
				},
			},
			expectedValid:    false,
			expectedError:    true,
			expectedErrorMsg: "not found in enforced registries",
		},
		{
			name:      "invalid JSON in ConfigMap - enforcing registry with invalid JSON should fail",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "registry-invalid-json",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "registry-valid-json",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Enforce: false,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			configMaps: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "registry-invalid-json-registry-storage",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"registry.json": "not-valid-json{",
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "registry-valid-json-registry-storage",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"registry.json": registryDataWithImage,
					},
				},
			},
			expectedValid:    false,
			expectedError:    true,
			expectedErrorMsg: "not found in enforced registries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Build fake client with test objects
			var objs []runtime.Object
			objs = append(objs, tt.registries...)
			objs = append(objs, tt.configMaps...)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				Build()

			validator := &RegistryEnforcingValidator{
				client:    fakeClient,
				namespace: tt.namespace,
			}

			err := validator.ValidateImage(ctx, tt.image)

			if tt.expectedValid {
				// Validation should pass (no error or ErrImageNotChecked)
				if err != nil {
					assert.ErrorIs(t, err, ErrImageNotChecked)
				}
			} else {
				// Validation should fail
				if tt.expectedError {
					assert.Error(t, err)
					if tt.expectedErrorMsg != "" {
						assert.Contains(t, err.Error(), tt.expectedErrorMsg)
					}
				} else {
					assert.NoError(t, err)
				}
			}
		})
	}
}

func TestCheckImageInRegistry(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	ctx := context.Background()

	registryData := `{
		"version": "1.0",
		"servers": {
			"test-server": {
				"name": "test-server",
				"image": "docker.io/toolhive/test:v1.0.0",
				"description": "Test server"
			}
		}
	}`

	tests := []struct {
		name          string
		mcpRegistry   *mcpv1alpha1.MCPRegistry
		configMap     *corev1.ConfigMap
		image         string
		expectedFound bool
	}{
		{
			name: "registry not ready - returns false",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhasePending,
				},
			},
			image:         "docker.io/toolhive/test:v1.0.0",
			expectedFound: false,
		},
		{
			name: "ConfigMap not found - returns false",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseReady,
				},
			},
			image:         "docker.io/toolhive/test:v1.0.0",
			expectedFound: false,
		},
		{
			name: "registry data not in ConfigMap - returns false",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseReady,
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry-registry-storage",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"other-key": "some-data",
				},
			},
			image:         "docker.io/toolhive/test:v1.0.0",
			expectedFound: false,
		},
		{
			name: "image found in registry - returns true",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseReady,
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry-registry-storage",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"registry.json": registryData,
				},
			},
			image:         "docker.io/toolhive/test:v1.0.0",
			expectedFound: true,
		},
		{
			name: "image not found in registry - returns false",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseReady,
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry-registry-storage",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"registry.json": registryData,
				},
			},
			image:         "docker.io/toolhive/missing:v1.0.0",
			expectedFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var objs []runtime.Object
			if tt.configMap != nil {
				objs = append(objs, tt.configMap)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				Build()

			validator := &RegistryEnforcingValidator{
				client:    fakeClient,
				namespace: "test-namespace",
			}

			found, err := validator.checkImageInRegistry(ctx, tt.mcpRegistry, tt.image)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedFound, found)
		})
	}
}

func TestFindImageInRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		registry *registry.Registry
		image    string
		expected bool
	}{
		{
			name: "finds image in top-level servers",
			registry: &registry.Registry{
				Servers: map[string]*registry.ImageMetadata{
					"server1": {
						Image: "docker.io/toolhive/test:v1.0.0",
					},
					"server2": {
						Image: "docker.io/toolhive/other:v2.0.0",
					},
				},
			},
			image:    "docker.io/toolhive/test:v1.0.0",
			expected: true,
		},
		{
			name: "finds image in group servers",
			registry: &registry.Registry{
				Servers: map[string]*registry.ImageMetadata{},
				Groups: []*registry.Group{
					{
						Name: "group1",
						Servers: map[string]*registry.ImageMetadata{
							"group-server": {
								Image: "docker.io/toolhive/group:v1.0.0",
							},
						},
					},
				},
			},
			image:    "docker.io/toolhive/group:v1.0.0",
			expected: true,
		},
		{
			name: "does not find missing image",
			registry: &registry.Registry{
				Servers: map[string]*registry.ImageMetadata{
					"server1": {
						Image: "docker.io/toolhive/test:v1.0.0",
					},
				},
				Groups: []*registry.Group{
					{
						Name: "group1",
						Servers: map[string]*registry.ImageMetadata{
							"group-server": {
								Image: "docker.io/toolhive/group:v1.0.0",
							},
						},
					},
				},
			},
			image:    "docker.io/toolhive/missing:v1.0.0",
			expected: false,
		},
		{
			name: "handles empty registry",
			registry: &registry.Registry{
				Servers: map[string]*registry.ImageMetadata{},
			},
			image:    "docker.io/toolhive/test:v1.0.0",
			expected: false,
		},
		{
			name:     "handles nil maps",
			registry: &registry.Registry{},
			image:    "docker.io/toolhive/test:v1.0.0",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := findImageInRegistry(tt.registry, tt.image)
			assert.Equal(t, tt.expected, result)
		})
	}
}

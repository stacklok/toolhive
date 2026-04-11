// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
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
			// Create empty metadata for test
			metadata := metav1.ObjectMeta{}
			err := validator.ValidateImage(ctx, tt.image, metadata)
			assert.ErrorIs(t, err, ErrImageNotChecked)
		})
	}
}

func TestNewImageValidator(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

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

// newRegistryAPIServer creates an httptest.Server that serves the registry API
// /v0.1/servers endpoint, returning the provided servers in the response.
func newRegistryAPIServer(t *testing.T, servers []v0.ServerResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := v0.ServerListResponse{
			Servers:  servers,
			Metadata: v0.Metadata{Count: len(servers)},
		}
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(resp)
		require.NoError(t, err)
	}))
}

// newErrorRegistryAPIServer creates an httptest.Server that returns the specified status code.
func newErrorRegistryAPIServer(statusCode int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(statusCode)
	}))
}

// makeOCIServerResponse creates a v0.ServerResponse with an OCI package for the given image.
func makeOCIServerResponse(name, image string) v0.ServerResponse {
	return v0.ServerResponse{
		Server: v0.ServerJSON{
			Name: name,
			Packages: []model.Package{
				{
					RegistryType: "oci",
					Identifier:   image,
					Transport: model.Transport{
						Type: "stdio",
					},
				},
			},
		},
	}
}

func TestRegistryEnforcingValidator_ValidateImage(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	ctx := context.Background()

	// Registry API servers with test data
	serversWithImage := []v0.ServerResponse{
		makeOCIServerResponse("io.toolhive/test-server", "docker.io/toolhive/test:v1.0.0"),
		makeOCIServerResponse("io.toolhive/another-server", "docker.io/toolhive/another:latest"),
	}

	emptyServers := []v0.ServerResponse{}

	// registryServerData maps registry names to the server data they should return.
	// Registries not in this map use serversWithImage by default.
	registryServerData := map[string][]v0.ServerResponse{
		"empty-registry":     emptyServers,
		"enforcing-registry": emptyServers,
	}

	tests := []struct {
		name             string
		namespace        string
		image            string
		registries       []runtime.Object
		apiServers       map[string]*httptest.Server // registry name -> test server
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
						EnforceServers: false,
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
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
						// URL will be set from apiServers below
					},
				},
			},
			apiServers:    map[string]*httptest.Server{"test-registry": nil}, // placeholder, set in test
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
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			apiServers:       map[string]*httptest.Server{"test-registry": nil},
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
						Name:      "empty-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			apiServers:       map[string]*httptest.Server{"empty-registry": nil},
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
						EnforceServers: true,
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
						EnforceServers: true,
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
						EnforceServers: false,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			apiServers:       map[string]*httptest.Server{"enforcing-registry": nil},
			expectedValid:    false,
			expectedError:    true,
			expectedErrorMsg: "not found in enforced registries",
		},
		{
			name:      "enforcing registry with no URL - skips that registry",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "registry-no-url",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
						URL:   "", // No URL set
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

			// Create test API servers for registries that have apiServers entries
			var testServers []*httptest.Server
			for name, ts := range tt.apiServers {
				if ts == nil {
					// Look up data for this registry, defaulting to serversWithImage
					data := serversWithImage
					if d, ok := registryServerData[name]; ok {
						data = d
					}
					ts = newRegistryAPIServer(t, data)
					tt.apiServers[name] = ts
					testServers = append(testServers, ts)
				}
			}
			defer func() {
				for _, ts := range testServers {
					ts.Close()
				}
			}()

			// Set Status.URL on registries that have API servers
			for i, obj := range tt.registries {
				reg, ok := obj.(*mcpv1alpha1.MCPRegistry)
				if !ok {
					continue
				}
				if ts, exists := tt.apiServers[reg.Name]; exists {
					reg.Status.URL = ts.URL
					tt.registries[i] = reg
				}
			}

			// Build fake client with test objects
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tt.registries...).
				Build()

			validator := &RegistryEnforcingValidator{
				client:     fakeClient,
				namespace:  tt.namespace,
				httpClient: http.DefaultClient,
			}

			// Create empty metadata for test (original behavior)
			metadata := metav1.ObjectMeta{}
			err := validator.ValidateImage(ctx, tt.image, metadata)

			if tt.expectedValid {
				// Validation should pass (no error or ErrImageNotChecked)
				if err != nil {
					assert.ErrorIs(t, err, ErrImageNotChecked)
				}
			} else {
				// Validation should fail
				if tt.expectedError {
					require.Error(t, err)
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

	ctx := context.Background()

	servers := []v0.ServerResponse{
		makeOCIServerResponse("io.toolhive/test-server", "docker.io/toolhive/test:v1.0.0"),
	}

	tests := []struct {
		name          string
		mcpRegistry   *mcpv1alpha1.MCPRegistry
		apiServer     func(t *testing.T) *httptest.Server
		image         string
		expectedFound bool
		expectedError bool
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
			name: "empty URL - returns false",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					URL:   "",
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
			apiServer: func(t *testing.T) *httptest.Server {
				t.Helper()
				return newRegistryAPIServer(t, servers)
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
			apiServer: func(t *testing.T) *httptest.Server {
				t.Helper()
				return newRegistryAPIServer(t, servers)
			},
			image:         "docker.io/toolhive/missing:v1.0.0",
			expectedFound: false,
		},
		{
			name: "API returns error - returns error",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseReady,
				},
			},
			apiServer: func(_ *testing.T) *httptest.Server {
				return newErrorRegistryAPIServer(http.StatusInternalServerError)
			},
			image:         "docker.io/toolhive/test:v1.0.0",
			expectedFound: false,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Set up API server if provided
			if tt.apiServer != nil {
				ts := tt.apiServer(t)
				defer ts.Close()
				tt.mcpRegistry.Status.URL = ts.URL
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			validator := &RegistryEnforcingValidator{
				client:     fakeClient,
				namespace:  "test-namespace",
				httpClient: http.DefaultClient,
			}

			found, err := validator.checkImageInRegistry(ctx, tt.mcpRegistry, tt.image)
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedFound, found)
		})
	}
}

func TestFindImageInServers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		servers  []v0.ServerResponse
		image    string
		expected bool
	}{
		{
			name: "finds image in OCI package",
			servers: []v0.ServerResponse{
				makeOCIServerResponse("io.toolhive/server1", "docker.io/toolhive/test:v1.0.0"),
				makeOCIServerResponse("io.toolhive/server2", "docker.io/toolhive/other:v2.0.0"),
			},
			image:    "docker.io/toolhive/test:v1.0.0",
			expected: true,
		},
		{
			name: "does not find missing image",
			servers: []v0.ServerResponse{
				makeOCIServerResponse("io.toolhive/server1", "docker.io/toolhive/test:v1.0.0"),
			},
			image:    "docker.io/toolhive/missing:v1.0.0",
			expected: false,
		},
		{
			name:     "handles empty server list",
			servers:  []v0.ServerResponse{},
			image:    "docker.io/toolhive/test:v1.0.0",
			expected: false,
		},
		{
			name:     "handles nil server list",
			servers:  nil,
			image:    "docker.io/toolhive/test:v1.0.0",
			expected: false,
		},
		{
			name: "ignores non-OCI packages",
			servers: []v0.ServerResponse{
				{
					Server: v0.ServerJSON{
						Name: "io.toolhive/npm-server",
						Packages: []model.Package{
							{
								RegistryType: "npm",
								Identifier:   "docker.io/toolhive/test:v1.0.0",
							},
						},
					},
				},
			},
			image:    "docker.io/toolhive/test:v1.0.0",
			expected: false,
		},
		{
			name: "handles server with no packages",
			servers: []v0.ServerResponse{
				{
					Server: v0.ServerJSON{
						Name: "io.toolhive/no-packages",
					},
				},
			},
			image:    "docker.io/toolhive/test:v1.0.0",
			expected: false,
		},
		{
			name: "finds image among multiple packages",
			servers: []v0.ServerResponse{
				{
					Server: v0.ServerJSON{
						Name: "io.toolhive/multi-pkg",
						Packages: []model.Package{
							{
								RegistryType: "npm",
								Identifier:   "@toolhive/server",
							},
							{
								RegistryType: "oci",
								Identifier:   "docker.io/toolhive/test:v1.0.0",
							},
						},
					},
				},
			},
			image:    "docker.io/toolhive/test:v1.0.0",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := findImageInServers(tt.servers, tt.image)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRegistryEnforcingValidator_ValidateImageWithRegistryLabel(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	ctx := context.Background()

	serversWithImage := []v0.ServerResponse{
		makeOCIServerResponse("io.toolhive/test-server", "docker.io/toolhive/test:v1.0.0"),
	}

	emptyServers := []v0.ServerResponse{}

	tests := []struct {
		name             string
		namespace        string
		image            string
		metadata         metav1.ObjectMeta
		registries       []runtime.Object
		apiServers       map[string]*httptest.Server
		expectedValid    bool
		expectedError    bool
		expectedErrorMsg string
	}{
		{
			name:      "registry label points to enforcing registry with image - validation passes",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			metadata: metav1.ObjectMeta{
				Labels: map[string]string{
					RegistryNameLabel: "target-registry",
				},
			},
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "target-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "other-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			apiServers:    map[string]*httptest.Server{"target-registry": nil},
			expectedValid: true,
		},
		{
			name:      "registry label points to non-enforcing registry - validation skipped",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			metadata: metav1.ObjectMeta{
				Labels: map[string]string{
					RegistryNameLabel: "target-registry",
				},
			},
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "target-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						EnforceServers: false,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			expectedValid: true,
		},
		{
			name:      "registry label points to enforcing registry without image - validation fails",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/missing:v1.0.0",
			metadata: metav1.ObjectMeta{
				Labels: map[string]string{
					RegistryNameLabel: "target-registry",
				},
			},
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "target-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			apiServers:       map[string]*httptest.Server{"target-registry": nil},
			expectedValid:    false,
			expectedError:    true,
			expectedErrorMsg: "not found in specified registry",
		},
		{
			name:      "registry label points to non-existent registry - validation fails",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			metadata: metav1.ObjectMeta{
				Labels: map[string]string{
					RegistryNameLabel: "non-existent-registry",
				},
			},
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "different-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			expectedValid:    false,
			expectedError:    true,
			expectedErrorMsg: "specified registry \"non-existent-registry\" not found",
		},
		{
			name:      "registry label with enforcing registry but image in different registry - validation fails",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			metadata: metav1.ObjectMeta{
				Labels: map[string]string{
					RegistryNameLabel: "empty-registry",
				},
			},
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "empty-registry",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "registry-with-image",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			apiServers:       map[string]*httptest.Server{"empty-registry": nil, "registry-with-image": nil},
			expectedValid:    false,
			expectedError:    true,
			expectedErrorMsg: "not found in specified registry \"empty-registry\"",
		},
		{
			name:      "no registry label - falls back to original behavior (all registries)",
			namespace: "test-namespace",
			image:     "docker.io/toolhive/test:v1.0.0",
			metadata:  metav1.ObjectMeta{}, // No registry label
			registries: []runtime.Object{
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "registry1",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
				&mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "registry2",
						Namespace: "test-namespace",
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						EnforceServers: true,
					},
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					},
				},
			},
			apiServers:    map[string]*httptest.Server{"registry1": nil, "registry2": nil},
			expectedValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test API servers
			var testServers []*httptest.Server
			for name, ts := range tt.apiServers {
				if ts == nil {
					data := serversWithImage
					if name == "empty-registry" {
						data = emptyServers
					}
					ts = newRegistryAPIServer(t, data)
					tt.apiServers[name] = ts
					testServers = append(testServers, ts)
				}
			}
			defer func() {
				for _, ts := range testServers {
					ts.Close()
				}
			}()

			// Set Status.URL on registries that have API servers
			for i, obj := range tt.registries {
				reg, ok := obj.(*mcpv1alpha1.MCPRegistry)
				if !ok {
					continue
				}
				if ts, exists := tt.apiServers[reg.Name]; exists {
					reg.Status.URL = ts.URL
					tt.registries[i] = reg
				}
			}

			// Build fake client with test objects
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tt.registries...).
				Build()

			validator := &RegistryEnforcingValidator{
				client:     fakeClient,
				namespace:  tt.namespace,
				httpClient: http.DefaultClient,
			}

			err := validator.ValidateImage(ctx, tt.image, tt.metadata)

			if tt.expectedValid {
				// Validation should pass (no error or ErrImageNotChecked)
				if err != nil {
					assert.ErrorIs(t, err, ErrImageNotChecked)
				}
			} else {
				// Validation should fail
				if tt.expectedError {
					require.Error(t, err)
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

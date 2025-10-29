package controllerutil

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestGetToolConfigForMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mcpServer      *mcpv1alpha1.MCPServer
		existingConfig *mcpv1alpha1.MCPToolConfig
		expectConfig   bool
		expectError    bool
	}{
		{
			name: "mcpserver without toolconfig ref",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
				},
			},
			expectConfig: false,
			expectError:  true, // Changed to expect an error when no ToolConfigRef is present
		},
		{
			name: "mcpserver with existing toolconfig",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "test-config",
					},
				},
			},
			existingConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1"},
				},
			},
			expectConfig: true,
			expectError:  false,
		},
		{
			name: "mcpserver with non-existent toolconfig",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "non-existent",
					},
				},
			},
			expectConfig: false,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

			objs := []client.Object{}
			if tt.existingConfig != nil {
				objs = append(objs, tt.existingConfig)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			config, err := GetToolConfigForMCPServer(ctx, fakeClient, tt.mcpServer)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, config)
			} else {
				assert.NoError(t, err)
				if tt.expectConfig {
					assert.NotNil(t, config)
					assert.Equal(t, tt.existingConfig.Name, config.Name)
				} else {
					assert.Nil(t, config)
				}
			}
		})
	}
}

// errorGetClient is a fake client that simulates Get errors
type errorGetClient struct {
	client.Client
	getError error
}

func (c *errorGetClient) Get(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	if c.getError != nil {
		return c.getError
	}
	// Return not found error
	return apierrors.NewNotFound(schema.GroupResource{
		Group:    "toolhive.stacklok.dev",
		Resource: "toolconfigs",
	}, key.Name)
}

func TestGetToolConfigForMCPServer_ErrorScenarios(t *testing.T) {
	t.Parallel()

	t.Run("toolconfig not found returns formatted error", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-server",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
				ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
					Name: "missing-config",
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		config, err := GetToolConfigForMCPServer(ctx, fakeClient, mcpServer)
		assert.Error(t, err)
		assert.Nil(t, config)
		assert.Contains(t, err.Error(), "MCPToolConfig missing-config not found")
		assert.Contains(t, err.Error(), "namespace default")
	})

	t.Run("generic error is wrapped", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-server",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
				ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
					Name: "test-config",
				},
			},
		}

		// Create a client that returns a generic error
		fakeClient := &errorGetClient{
			Client: fake.NewClientBuilder().
				WithScheme(scheme).
				Build(),
			getError: errors.New("network error"),
		}

		config, err := GetToolConfigForMCPServer(ctx, fakeClient, mcpServer)
		assert.Error(t, err)
		assert.Nil(t, config)
		assert.Contains(t, err.Error(), "failed to get MCPToolConfig")
		assert.Contains(t, err.Error(), "network error")
	})
}

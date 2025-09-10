package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestToolConfigReconciler_EdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("reconcile non-existent toolconfig", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		r := &ToolConfigReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		// Try to reconcile a non-existent MCPToolConfig
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "non-existent",
				Namespace: "default",
			},
		}

		result, err := r.Reconcile(ctx, req)
		assert.NoError(t, err)
		assert.False(t, result.RequeueAfter > 0)
	})

	t.Run("reconcile with status update", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		toolConfig := &mcpv1alpha1.MCPToolConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-config",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPToolConfigSpec{
				ToolsFilter: []string{"tool1", "tool2"},
				ToolsOverride: map[string]mcpv1alpha1.ToolOverride{
					"tool1": {
						Name:        "renamed-tool1",
						Description: "Custom description",
					},
				},
			},
		}

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

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(toolConfig, mcpServer).
			WithStatusSubresource(&mcpv1alpha1.MCPToolConfig{}).
			Build()

		r := &ToolConfigReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      toolConfig.Name,
				Namespace: toolConfig.Namespace,
			},
		}

		// First reconciliation adds finalizer
		result, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Greater(t, result.RequeueAfter, time.Duration(0))

		// Second reconciliation updates status
		result, err = r.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), result.RequeueAfter)

		// Verify status was updated
		var updatedConfig mcpv1alpha1.MCPToolConfig
		err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
		require.NoError(t, err)
		assert.NotEmpty(t, updatedConfig.Status.ConfigHash)
		assert.Contains(t, updatedConfig.Status.ReferencingServers, "test-server")
	})

	t.Run("reconcile with changed spec", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		toolConfig := &mcpv1alpha1.MCPToolConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-config",
				Namespace:  "default",
				Finalizers: []string{ToolConfigFinalizerName},
			},
			Spec: mcpv1alpha1.MCPToolConfigSpec{
				ToolsFilter: []string{"tool1"},
			},
			Status: mcpv1alpha1.MCPToolConfigStatus{
				ConfigHash: "oldhash",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(toolConfig).
			WithStatusSubresource(&mcpv1alpha1.MCPToolConfig{}).
			Build()

		r := &ToolConfigReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		// Update the spec
		err := fakeClient.Get(ctx, client.ObjectKeyFromObject(toolConfig), toolConfig)
		require.NoError(t, err)
		toolConfig.Spec.ToolsFilter = append(toolConfig.Spec.ToolsFilter, "tool2")
		err = fakeClient.Update(ctx, toolConfig)
		require.NoError(t, err)

		// Reconcile
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      toolConfig.Name,
				Namespace: toolConfig.Namespace,
			},
		}
		result, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), result.RequeueAfter)

		// Verify hash was updated
		var updatedConfig mcpv1alpha1.MCPToolConfig
		err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
		require.NoError(t, err)
		assert.NotEqual(t, "oldhash", updatedConfig.Status.ConfigHash)
		assert.NotEmpty(t, updatedConfig.Status.ConfigHash)
	})
}

func TestToolConfigReconciler_ErrorScenarios(t *testing.T) {
	t.Parallel()

	t.Run("error updating status", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		toolConfig := &mcpv1alpha1.MCPToolConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-config",
				Namespace:  "default",
				Finalizers: []string{ToolConfigFinalizerName},
			},
			Spec: mcpv1alpha1.MCPToolConfigSpec{
				ToolsFilter: []string{"tool1"},
			},
		}

		// Create a fake client that returns an error when listing MCPServers
		fakeClient := &errorClient{
			Client: fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(toolConfig).
				WithStatusSubresource(&mcpv1alpha1.MCPToolConfig{}).
				Build(),
			listError: errors.New("list error"),
		}

		r := &ToolConfigReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      toolConfig.Name,
				Namespace: toolConfig.Namespace,
			},
		}

		result, err := r.Reconcile(ctx, req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to find referencing MCPServers")
		assert.Equal(t, time.Duration(0), result.RequeueAfter)
	})
}

// errorClient is a fake client that can simulate errors
type errorClient struct {
	client.Client
	listError error
}

func (c *errorClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if c.listError != nil {
		return c.listError
	}
	return c.Client.List(ctx, list, opts...)
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

func TestToolConfigReconciler_ComplexScenarios(t *testing.T) {
	t.Parallel()

	t.Run("multiple MCPServers referencing same MCPToolConfig", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		toolConfig := &mcpv1alpha1.MCPToolConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-config",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPToolConfigSpec{
				ToolsFilter: []string{"tool1", "tool2", "tool3"},
				ToolsOverride: map[string]mcpv1alpha1.ToolOverride{
					"tool1": {
						Name:        "custom-tool1",
						Description: "Customized tool 1",
					},
				},
			},
		}

		// Create multiple MCPServers referencing the same MCPToolConfig
		mcpServers := []*mcpv1alpha1.MCPServer{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server1",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "shared-config",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server2",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "shared-config",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server3",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "shared-config",
					},
				},
			},
		}

		objs := []client.Object{toolConfig}
		for _, server := range mcpServers {
			objs = append(objs, server)
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(objs...).
			WithStatusSubresource(&mcpv1alpha1.MCPToolConfig{}).
			Build()

		r := &ToolConfigReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      toolConfig.Name,
				Namespace: toolConfig.Namespace,
			},
		}

		// First reconciliation adds finalizer
		result, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Greater(t, result.RequeueAfter, time.Duration(0))

		// Second reconciliation updates status
		result, err = r.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), result.RequeueAfter)

		// Verify all servers are listed in status
		var updatedConfig mcpv1alpha1.MCPToolConfig
		err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
		require.NoError(t, err)
		assert.Len(t, updatedConfig.Status.ReferencingServers, 3)
		assert.Contains(t, updatedConfig.Status.ReferencingServers, "server1")
		assert.Contains(t, updatedConfig.Status.ReferencingServers, "server2")
		assert.Contains(t, updatedConfig.Status.ReferencingServers, "server3")
	})

	t.Run("empty MCPToolConfig spec", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		// MCPToolConfig with completely empty spec
		toolConfig := &mcpv1alpha1.MCPToolConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "empty-config",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPToolConfigSpec{
				// Empty spec - no filters, no overrides
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(toolConfig).
			WithStatusSubresource(&mcpv1alpha1.MCPToolConfig{}).
			Build()

		r := &ToolConfigReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      toolConfig.Name,
				Namespace: toolConfig.Namespace,
			},
		}

		// First reconciliation adds finalizer
		result, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Greater(t, result.RequeueAfter, time.Duration(0))

		// Second reconciliation should succeed even with empty spec
		result, err = r.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), result.RequeueAfter)

		// Verify hash was generated even for empty spec
		var updatedConfig mcpv1alpha1.MCPToolConfig
		err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
		require.NoError(t, err)
		assert.NotEmpty(t, updatedConfig.Status.ConfigHash)
	})
}

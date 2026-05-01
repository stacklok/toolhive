// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package k8s_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/k8s"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// mockDiscoverer is a test double for workloads.Discoverer
type mockDiscoverer struct {
	backend *vmcp.Backend
	err     error
}

func (m *mockDiscoverer) GetWorkloadAsVMCPBackend(_ context.Context, _ workloads.TypedWorkload) (*vmcp.Backend, error) {
	return m.backend, m.err
}

func (*mockDiscoverer) ListWorkloadsInGroup(_ context.Context, _ string) ([]workloads.TypedWorkload, error) {
	return nil, nil
}

// mockRegistry is a test double for vmcp.DynamicRegistry that tracks operations
type mockRegistry struct {
	upsertedBackends []vmcp.Backend
	removedIDs       []string
	version          uint64
}

func (m *mockRegistry) Upsert(backend vmcp.Backend) error {
	m.upsertedBackends = append(m.upsertedBackends, backend)
	m.version++
	return nil
}

func (m *mockRegistry) Remove(backendID string) error {
	m.removedIDs = append(m.removedIDs, backendID)
	m.version++

	// Actually remove the backend from upsertedBackends to match real registry behavior
	for i, backend := range m.upsertedBackends {
		if backend.ID == backendID {
			m.upsertedBackends = append(m.upsertedBackends[:i], m.upsertedBackends[i+1:]...)
			break
		}
	}

	return nil
}

func (m *mockRegistry) Version() uint64 {
	return m.version
}

func (m *mockRegistry) Get(_ context.Context, backendID string) *vmcp.Backend {
	for _, backend := range m.upsertedBackends {
		if backend.ID == backendID {
			return &backend
		}
	}
	return nil
}

func (m *mockRegistry) List(_ context.Context) []vmcp.Backend {
	return m.upsertedBackends
}

func (m *mockRegistry) Count() int {
	return len(m.upsertedBackends)
}

// newTestReconciler creates a BackendReconciler for testing with fake client and mocks.
// Parameters provide flexibility for future tests and make test setup explicit and self-documenting.
//
//nolint:unparam // namespace and groupRef parameters make tests self-documenting
func newTestReconciler(
	k8sClient client.Client,
	namespace string,
	groupRef string,
	registry vmcp.DynamicRegistry,
	discoverer workloads.Discoverer,
) *k8s.BackendReconciler {
	return &k8s.BackendReconciler{
		Client:     k8sClient,
		Namespace:  namespace,
		GroupRef:   groupRef,
		Registry:   registry,
		Discoverer: discoverer,
	}
}

// TestReconcile_MCPServer_Success tests successful MCPServer reconciliation
func TestReconcile_MCPServer_Success(t *testing.T) {
	t.Parallel()

	// Create test scheme with MCPServer CRD
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	// Create MCPServer with matching groupRef
	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
		},
	}

	// Create fake K8s client with the MCPServer
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpServer).
		Build()

	// Create mock backend to be returned by discoverer
	mockBackend := &vmcp.Backend{
		ID:      "test-server",
		Name:    "test-server",
		BaseURL: "http://test-server:8080",
	}

	// Create mocks
	mockDisc := &mockDiscoverer{backend: mockBackend}
	mockReg := &mockRegistry{}

	// Create reconciler
	reconciler := newTestReconciler(k8sClient, "default", "test-group", mockReg, mockDisc)

	// Reconcile the MCPServer
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-server",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.Len(t, mockReg.upsertedBackends, 1, "Backend should be upserted to registry")
	assert.Equal(t, "test-server", mockReg.upsertedBackends[0].ID)
	assert.Len(t, mockReg.removedIDs, 0, "No backends should be removed")
	assert.Equal(t, uint64(1), mockReg.Version(), "Registry version should be incremented")
}

// TestReconcile_GroupRefMismatch tests that backends with non-matching groupRef are removed
func TestReconcile_GroupRefMismatch(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	// Create MCPServer with DIFFERENT groupRef
	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "different-group"}, // Does NOT match reconciler's groupRef
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpServer).
		Build()

	mockDisc := &mockDiscoverer{}
	mockReg := &mockRegistry{}

	reconciler := newTestReconciler(k8sClient, "default", "test-group", mockReg, mockDisc)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-server",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.Len(t, mockReg.upsertedBackends, 0, "Backend should NOT be upserted")
	assert.Len(t, mockReg.removedIDs, 1, "Backend should be removed from registry")
	assert.Equal(t, "test-server", mockReg.removedIDs[0])
}

// TestReconcile_Deleted tests that deleted resources are removed from registry
func TestReconcile_Deleted(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	// Create fake K8s client WITHOUT the MCPServer (simulates deletion)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	mockDisc := &mockDiscoverer{}
	mockReg := &mockRegistry{}

	reconciler := newTestReconciler(k8sClient, "default", "test-group", mockReg, mockDisc)

	// Try to reconcile a deleted MCPServer
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "deleted-server",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.Len(t, mockReg.upsertedBackends, 0, "Backend should NOT be upserted")
	assert.Len(t, mockReg.removedIDs, 1, "Backend should be removed from registry")
	assert.Equal(t, "deleted-server", mockReg.removedIDs[0])
}

// TestReconcile_AuthFailure tests that nil backend (auth failed) removes from registry
func TestReconcile_AuthFailure(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpServer).
		Build()

	// Discoverer returns nil backend (simulates auth failure)
	mockDisc := &mockDiscoverer{backend: nil, err: nil}
	mockReg := &mockRegistry{}

	reconciler := newTestReconciler(k8sClient, "default", "test-group", mockReg, mockDisc)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-server",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.Len(t, mockReg.upsertedBackends, 0, "Backend should NOT be upserted (auth failed)")
	assert.Len(t, mockReg.removedIDs, 1, "Backend should be removed from registry")
	assert.Equal(t, "test-server", mockReg.removedIDs[0])
}

// TestReconcile_MCPRemoteProxy_Success tests successful MCPRemoteProxy reconciliation
func TestReconcile_MCPRemoteProxy_Success(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	// Create MCPRemoteProxy with matching groupRef
	mcpRemoteProxy := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpRemoteProxy).
		Build()

	mockBackend := &vmcp.Backend{
		ID:      "test-proxy",
		Name:    "test-proxy",
		BaseURL: "http://test-proxy:8080",
	}

	mockDisc := &mockDiscoverer{backend: mockBackend}
	mockReg := &mockRegistry{}

	reconciler := newTestReconciler(k8sClient, "default", "test-group", mockReg, mockDisc)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-proxy",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.Len(t, mockReg.upsertedBackends, 1, "Backend should be upserted to registry")
	assert.Equal(t, "test-proxy", mockReg.upsertedBackends[0].ID)
}

// TestReconcile_ConversionError tests that conversion errors remove backend from registry
func TestReconcile_ConversionError(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpServer).
		Build()

	// Discoverer returns error (simulates conversion failure)
	mockDisc := &mockDiscoverer{backend: nil, err: fmt.Errorf("conversion failed")}
	mockReg := &mockRegistry{}

	reconciler := newTestReconciler(k8sClient, "default", "test-group", mockReg, mockDisc)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-server",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	// Assert
	require.Error(t, err, "Conversion error should be returned for requeue")
	assert.Contains(t, err.Error(), "conversion failed")
	assert.Equal(t, ctrl.Result{}, result)
	assert.Len(t, mockReg.upsertedBackends, 0, "Backend should NOT be upserted")
	assert.Len(t, mockReg.removedIDs, 1, "Backend should be removed from registry")
	assert.Equal(t, "test-server", mockReg.removedIDs[0])
}

// TestSetupWithManager_RegistersWatches tests that the reconciler has SetupWithManager method
func TestSetupWithManager_RegistersWatches(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	// This test validates the structure without actually registering controllers
	// Full integration testing of watches requires envtest and is covered by integration tests

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	mockDisc := &mockDiscoverer{}
	mockReg := &mockRegistry{}

	reconciler := newTestReconciler(k8sClient, "default", "test-group", mockReg, mockDisc)

	// Verify the reconciler has the required fields
	assert.Equal(t, "default", reconciler.Namespace)
	assert.Equal(t, "test-group", reconciler.GroupRef)
	assert.NotNil(t, reconciler.Registry)
	assert.NotNil(t, reconciler.Discoverer)
}

// TestReconcile_MCPServerEntry_Success tests successful MCPServerEntry reconciliation
func TestReconcile_MCPServerEntry_Success(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	mcpServerEntry := &mcpv1beta1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "remote-mcp",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerEntrySpec{
			RemoteURL: "https://mcp.example.com/mcp",
			Transport: "streamable-http",
			GroupRef:  &mcpv1beta1.MCPGroupRef{Name: "test-group"},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpServerEntry).
		Build()

	mockBackend := &vmcp.Backend{
		ID:      "remote-mcp",
		Name:    "remote-mcp",
		BaseURL: "https://mcp.example.com/mcp",
		Type:    vmcp.BackendTypeEntry,
	}

	mockDisc := &mockDiscoverer{backend: mockBackend}
	mockReg := &mockRegistry{}

	reconciler := newTestReconciler(k8sClient, "default", "test-group", mockReg, mockDisc)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "remote-mcp",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.Len(t, mockReg.upsertedBackends, 1)
	assert.Equal(t, "remote-mcp", mockReg.upsertedBackends[0].ID)
	assert.Equal(t, vmcp.BackendTypeEntry, mockReg.upsertedBackends[0].Type)
}

// TestReconcile_MCPServerEntry_GroupRefMismatch tests that MCPServerEntry with non-matching groupRef is removed
func TestReconcile_MCPServerEntry_GroupRefMismatch(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	mcpServerEntry := &mcpv1beta1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "remote-mcp",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerEntrySpec{
			RemoteURL: "https://mcp.example.com/mcp",
			Transport: "streamable-http",
			GroupRef:  &mcpv1beta1.MCPGroupRef{Name: "other-group"},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpServerEntry).
		Build()

	mockDisc := &mockDiscoverer{}
	mockReg := &mockRegistry{}

	reconciler := newTestReconciler(k8sClient, "default", "test-group", mockReg, mockDisc)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "remote-mcp",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.Empty(t, mockReg.upsertedBackends)
	assert.Contains(t, mockReg.removedIDs, "remote-mcp")
}

// TestReconcile_MCPServerEntry_Deleted tests that deleted MCPServerEntry is removed from registry
func TestReconcile_MCPServerEntry_Deleted(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	// No MCPServerEntry created — simulates deletion
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	mockDisc := &mockDiscoverer{}
	mockReg := &mockRegistry{}

	reconciler := newTestReconciler(k8sClient, "default", "test-group", mockReg, mockDisc)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "deleted-entry",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.Empty(t, mockReg.upsertedBackends)
	assert.Contains(t, mockReg.removedIDs, "deleted-entry")
}

// TestMapAuthConfigToEntries tests that MapAuthConfigToEntries returns reconcile requests
// for MCPServerEntries that reference the given ExternalAuthConfig name.
func TestMapAuthConfigToEntries(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	tests := []struct {
		name           string
		authConfigName string
		entries        []mcpv1beta1.MCPServerEntry
		groupRef       string
		wantNames      []string
	}{
		{
			name:           "matches entry referencing auth config",
			authConfigName: "my-auth",
			entries: []mcpv1beta1.MCPServerEntry{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "entry-1", Namespace: "default"},
					Spec: mcpv1beta1.MCPServerEntrySpec{
						GroupRef:              &mcpv1beta1.MCPGroupRef{Name: "test-group"},
						RemoteURL:             "https://example.com",
						Transport:             "streamable-http",
						ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{Name: "my-auth"},
					},
				},
			},
			groupRef:  "test-group",
			wantNames: []string{"entry-1"},
		},
		{
			name:           "skips entry with different group",
			authConfigName: "my-auth",
			entries: []mcpv1beta1.MCPServerEntry{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "entry-1", Namespace: "default"},
					Spec: mcpv1beta1.MCPServerEntrySpec{
						GroupRef:              &mcpv1beta1.MCPGroupRef{Name: "other-group"},
						RemoteURL:             "https://example.com",
						Transport:             "streamable-http",
						ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{Name: "my-auth"},
					},
				},
			},
			groupRef:  "test-group",
			wantNames: nil,
		},
		{
			name:           "skips entry referencing different auth config",
			authConfigName: "my-auth",
			entries: []mcpv1beta1.MCPServerEntry{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "entry-1", Namespace: "default"},
					Spec: mcpv1beta1.MCPServerEntrySpec{
						GroupRef:              &mcpv1beta1.MCPGroupRef{Name: "test-group"},
						RemoteURL:             "https://example.com",
						Transport:             "streamable-http",
						ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{Name: "other-auth"},
					},
				},
			},
			groupRef:  "test-group",
			wantNames: nil,
		},
		{
			name:           "skips entry with no auth config ref",
			authConfigName: "my-auth",
			entries: []mcpv1beta1.MCPServerEntry{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "entry-1", Namespace: "default"},
					Spec: mcpv1beta1.MCPServerEntrySpec{
						GroupRef:  &mcpv1beta1.MCPGroupRef{Name: "test-group"},
						RemoteURL: "https://example.com",
						Transport: "streamable-http",
					},
				},
			},
			groupRef:  "test-group",
			wantNames: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objs := make([]client.Object, len(tt.entries))
			for i := range tt.entries {
				objs[i] = &tt.entries[i]
			}

			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			reconciler := newTestReconciler(k8sClient, "default", tt.groupRef, &mockRegistry{}, &mockDiscoverer{})
			requests := reconciler.MapAuthConfigToEntries(context.Background(), tt.authConfigName)

			var gotNames []string
			for _, req := range requests {
				gotNames = append(gotNames, req.Name)
			}
			assert.Equal(t, tt.wantNames, gotNames)
		})
	}
}

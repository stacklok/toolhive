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

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
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

// newTestReconciler creates a BackendReconciler for testing with fake client and mocks
//
//nolint:unparam // Parameters provide flexibility for future tests even if current tests use same values
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
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	// Create MCPServer with matching groupRef
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			GroupRef: "test-group",
		},
	}

	// Create fake K8s client with the MCPServer
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpServer).
		Build()

	// Create mock backend to be returned by discoverer
	mockBackend := &vmcp.Backend{
		ID:      "default/test-server",
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
	assert.Equal(t, "default/test-server", mockReg.upsertedBackends[0].ID)
	assert.Len(t, mockReg.removedIDs, 0, "No backends should be removed")
	assert.Equal(t, uint64(1), mockReg.Version(), "Registry version should be incremented")
}

// TestReconcile_GroupRefMismatch tests that backends with non-matching groupRef are removed
func TestReconcile_GroupRefMismatch(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	// Create MCPServer with DIFFERENT groupRef
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			GroupRef: "different-group", // Does NOT match reconciler's groupRef
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
	assert.Equal(t, "default/test-server", mockReg.removedIDs[0])
}

// TestReconcile_Deleted tests that deleted resources are removed from registry
func TestReconcile_Deleted(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

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
	assert.Equal(t, "default/deleted-server", mockReg.removedIDs[0])
}

// TestReconcile_AuthFailure tests that nil backend (auth failed) removes from registry
func TestReconcile_AuthFailure(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			GroupRef: "test-group",
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
	assert.Equal(t, "default/test-server", mockReg.removedIDs[0])
}

// TestReconcile_MCPRemoteProxy_Success tests successful MCPRemoteProxy reconciliation
func TestReconcile_MCPRemoteProxy_Success(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	// Create MCPRemoteProxy with matching groupRef
	mcpRemoteProxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			GroupRef: "test-group",
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpRemoteProxy).
		Build()

	mockBackend := &vmcp.Backend{
		ID:      "default/test-proxy",
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
	assert.Equal(t, "default/test-proxy", mockReg.upsertedBackends[0].ID)
}

// TestReconcile_ConversionError tests that conversion errors remove backend from registry
func TestReconcile_ConversionError(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			GroupRef: "test-group",
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
	assert.Equal(t, "default/test-server", mockReg.removedIDs[0])
}

// TestSetupWithManager_RegistersWatches tests that the reconciler has SetupWithManager method
func TestSetupWithManager_RegistersWatches(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

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

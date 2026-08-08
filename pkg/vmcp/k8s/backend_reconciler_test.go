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
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/k8s"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// mockDiscoverer is a test double for workloads.Discoverer
type mockDiscoverer struct {
	backend *vmcp.Backend
	err     error
	calls   int
}

func (m *mockDiscoverer) GetWorkloadAsVMCPBackend(_ context.Context, _ workloads.TypedWorkload) (*vmcp.Backend, error) {
	m.calls++
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

func newMockRegistryWithBackend(backendID string) *mockRegistry {
	return &mockRegistry{
		upsertedBackends: []vmcp.Backend{{ID: backendID, Name: backendID}},
	}
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
	mockReg := newMockRegistryWithBackend("test-server")

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

// TestReconcile_ExcludedBackend verifies watch events cannot reintroduce a
// backend that initial discovery excluded because its auth failed to resolve.
func TestReconcile_ExcludedBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource client.Object
	}{
		{
			name: "MCPServer",
			resource: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "blocked-server", Namespace: "default"},
				Spec: mcpv1beta1.MCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
				},
			},
		},
		{
			name: "MCPRemoteProxy",
			resource: v1beta1test.NewMCPRemoteProxy(
				"blocked-proxy",
				"default",
				v1beta1test.WithRemoteProxyGroupRef("test-group"),
			),
		},
		{
			name: "MCPServerEntry",
			resource: &mcpv1beta1.MCPServerEntry{
				ObjectMeta: metav1.ObjectMeta{Name: "blocked-entry", Namespace: "default"},
				Spec: mcpv1beta1.MCPServerEntrySpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))

			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.resource).
				Build()

			backendName := tt.resource.GetName()
			mockDisc := &mockDiscoverer{
				backend: &vmcp.Backend{ID: backendName, Name: backendName},
			}
			mockReg := newMockRegistryWithBackend(backendName)
			reconciler := newTestReconciler(k8sClient, "default", "test-group", mockReg, mockDisc)
			reconciler.ExcludedBackends = map[string]struct{}{backendName: {}}

			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: backendName, Namespace: "default"},
			})

			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)
			assert.Zero(t, mockDisc.calls, "excluded backend must not be converted")
			assert.Empty(t, mockReg.upsertedBackends, "excluded backend must remain absent")
			assert.Equal(t, []string{backendName}, mockReg.removedIDs)
			assert.Equal(t, uint64(1), mockReg.Version())

			result, err = reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: backendName, Namespace: "default"},
			})
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)
			assert.Equal(t, []string{backendName}, mockReg.removedIDs,
				"an already absent excluded backend must not be removed twice")
			assert.Equal(t, uint64(1), mockReg.Version(),
				"an already absent excluded backend must not invalidate registry caches")
		})
	}
}

// TestReconcile_AuthPrecedence verifies watcher-driven updates use the same
// precedence as initial aggregation: explicit, discovered, backend fallback,
// then Default.
func TestReconcile_AuthPrecedence(t *testing.T) {
	t.Parallel()

	const backendName = "auth-backend"
	explicitStrategy := &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeHeaderInjection}
	backendFallback := &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeTokenExchange}
	defaultStrategy := &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUpstreamInject}

	tests := []struct {
		name              string
		hasDiscoveredRef  bool
		discoveredInvalid bool
		backends          map[string]*authtypes.BackendAuthStrategy
		explicitBackends  []string
		wantStrategyType  string
		wantExactStrategy *authtypes.BackendAuthStrategy
	}{
		{
			name:              "explicit override wins without inspecting invalid discovered source",
			hasDiscoveredRef:  true,
			discoveredInvalid: true,
			backends:          map[string]*authtypes.BackendAuthStrategy{backendName: explicitStrategy},
			explicitBackends:  []string{backendName},
			wantStrategyType:  authtypes.StrategyTypeHeaderInjection,
			wantExactStrategy: explicitStrategy,
		},
		{
			name:             "discovered auth wins over backend fallback and default",
			hasDiscoveredRef: true,
			backends:         map[string]*authtypes.BackendAuthStrategy{backendName: backendFallback},
			explicitBackends: []string{},
			wantStrategyType: authtypes.StrategyTypeUnauthenticated,
		},
		{
			name:              "backend fallback wins over default",
			backends:          map[string]*authtypes.BackendAuthStrategy{backendName: backendFallback},
			explicitBackends:  []string{},
			wantStrategyType:  authtypes.StrategyTypeTokenExchange,
			wantExactStrategy: backendFallback,
		},
		{
			name:              "valid default survives watcher update",
			backends:          map[string]*authtypes.BackendAuthStrategy{},
			explicitBackends:  []string{},
			wantStrategyType:  authtypes.StrategyTypeUpstreamInject,
			wantExactStrategy: defaultStrategy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))

			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: "default"},
				Spec: mcpv1beta1.MCPServerSpec{
					GroupRef:  &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Transport: "streamable-http",
				},
				Status: mcpv1beta1.MCPServerStatus{
					Phase: mcpv1beta1.MCPServerPhaseReady,
					URL:   "http://auth-backend.default.svc.cluster.local:8080",
				},
			}
			objects := []client.Object{server}
			if tt.hasDiscoveredRef {
				server.Spec.ExternalAuthConfigRef = &mcpv1beta1.ExternalAuthConfigRef{Name: "discovered-auth"}
				discoveredAuth := &mcpv1beta1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "discovered-auth", Namespace: "default"},
					Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
						Type: mcpv1beta1.ExternalAuthTypeUnauthenticated,
					},
				}
				if tt.discoveredInvalid {
					discoveredAuth.Status.Conditions = []metav1.Condition{
						{
							Type:    mcpv1beta1.ConditionTypeValid,
							Status:  metav1.ConditionFalse,
							Reason:  "InvalidConfig",
							Message: "source must not be inspected when an explicit override exists",
						},
					}
				}
				objects = append(objects, discoveredAuth)
			}

			authConfig := &vmcpconfig.OutgoingAuthConfig{
				Source:           "discovered",
				Default:          defaultStrategy,
				Backends:         tt.backends,
				ExplicitBackends: tt.explicitBackends,
			}
			k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			discoverer := workloads.NewK8SDiscovererWithClientAndAuthConfig(k8sClient, "default", authConfig)
			registry := &mockRegistry{}
			reconciler := newTestReconciler(k8sClient, "default", "test-group", registry, discoverer)
			reconciler.AuthConfig = authConfig

			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: backendName, Namespace: "default"},
			})

			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)
			require.Len(t, registry.upsertedBackends, 1)
			require.NotNil(t, registry.upsertedBackends[0].AuthConfig)
			assert.Equal(t, tt.wantStrategyType, registry.upsertedBackends[0].AuthConfig.Type)
			if tt.wantExactStrategy != nil {
				assert.Same(t, tt.wantExactStrategy, registry.upsertedBackends[0].AuthConfig)
			}
		})
	}
}

// TestReconcile_InvalidDiscoveredAuthRemovesBackend verifies a supported
// converter cannot make a source usable when the source controller marked it
// invalid or its stored spec fails local validation.
func TestReconcile_InvalidDiscoveredAuthRemovesBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		externalAuth *mcpv1beta1.MCPExternalAuthConfig
	}{
		{
			name: "Valid False source",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeUnauthenticated,
				},
				Status: mcpv1beta1.MCPExternalAuthConfigStatus{
					Conditions: []metav1.Condition{
						{
							Type:    mcpv1beta1.ConditionTypeValid,
							Status:  metav1.ConditionFalse,
							Reason:  "InvalidConfig",
							Message: "source validation failed",
						},
					},
				},
			},
		},
		{
			name: "locally invalid source",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeUpstreamInject,
					UpstreamInject: &mcpv1beta1.UpstreamInjectSpec{
						ProviderName: "",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const backendName = "invalid-auth-backend"
			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))

			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: "default"},
				Spec: mcpv1beta1.MCPServerSpec{
					GroupRef:              &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Transport:             "streamable-http",
					ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{Name: "invalid-auth"},
				},
				Status: mcpv1beta1.MCPServerStatus{
					Phase: mcpv1beta1.MCPServerPhaseReady,
					URL:   "http://invalid-auth-backend.default.svc.cluster.local:8080",
				},
			}
			tt.externalAuth.Name = "invalid-auth"
			tt.externalAuth.Namespace = "default"
			authConfig := &vmcpconfig.OutgoingAuthConfig{
				Source:           "discovered",
				Backends:         map[string]*authtypes.BackendAuthStrategy{},
				ExplicitBackends: []string{},
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(server, tt.externalAuth).
				Build()
			discoverer := workloads.NewK8SDiscovererWithClientAndAuthConfig(k8sClient, "default", authConfig)
			registry := newMockRegistryWithBackend(backendName)
			reconciler := newTestReconciler(k8sClient, "default", "test-group", registry, discoverer)
			reconciler.AuthConfig = authConfig

			result, err := reconciler.Reconcile(t.Context(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: backendName, Namespace: "default"},
			})

			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)
			assert.Empty(t, registry.upsertedBackends)
			assert.Equal(t, []string{backendName}, registry.removedIDs)
			assert.Equal(t, uint64(1), registry.Version())
		})
	}
}

// TestReconcile_FailedDefaultForNewBackend verifies the runtime failure marker
// denies a newly joined backend without independent auth while preserving a
// peer with valid discovered auth.
func TestReconcile_FailedDefaultForNewBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		backendName      string
		hasDiscoveredRef bool
		wantAllowed      bool
	}{
		{
			name:        "backend without auth ref is denied",
			backendName: "late-default-dependent",
		},
		{
			name:             "backend with valid discovered auth is allowed",
			backendName:      "late-discovered-peer",
			hasDiscoveredRef: true,
			wantAllowed:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))

			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: tt.backendName, Namespace: "default"},
				Spec: mcpv1beta1.MCPServerSpec{
					GroupRef:  &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Transport: "streamable-http",
				},
				Status: mcpv1beta1.MCPServerStatus{
					Phase: mcpv1beta1.MCPServerPhaseReady,
					URL:   "http://" + tt.backendName + ".default.svc.cluster.local:8080",
				},
			}
			objects := []client.Object{server}
			if tt.hasDiscoveredRef {
				server.Spec.ExternalAuthConfigRef = &mcpv1beta1.ExternalAuthConfigRef{Name: "valid-discovered-auth"}
				objects = append(objects, &mcpv1beta1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "valid-discovered-auth", Namespace: "default"},
					Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
						Type: mcpv1beta1.ExternalAuthTypeUnauthenticated,
					},
				})
			}

			authConfig := &vmcpconfig.OutgoingAuthConfig{
				Source:            "discovered",
				Backends:          map[string]*authtypes.BackendAuthStrategy{},
				ExplicitBackends:  []string{},
				DefaultAuthFailed: true,
			}
			k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			discoverer := workloads.NewK8SDiscovererWithClientAndAuthConfig(k8sClient, "default", authConfig)
			registry := &mockRegistry{}
			reconciler := newTestReconciler(k8sClient, "default", "test-group", registry, discoverer)
			reconciler.AuthConfig = authConfig

			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: tt.backendName, Namespace: "default"},
			})

			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)
			if tt.wantAllowed {
				require.Len(t, registry.upsertedBackends, 1)
				require.NotNil(t, registry.upsertedBackends[0].AuthConfig)
				assert.Equal(t, authtypes.StrategyTypeUnauthenticated, registry.upsertedBackends[0].AuthConfig.Type)
				assert.Equal(t, uint64(1), registry.Version())
				return
			}

			assert.Empty(t, registry.upsertedBackends)
			assert.Empty(t, registry.removedIDs, "new backend was never present, so removal is a no-op")
			assert.Zero(t, registry.Version(), "denying an absent backend must not invalidate registry caches")
		})
	}
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
	mockReg := newMockRegistryWithBackend("deleted-server")

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
	mockReg := newMockRegistryWithBackend("test-server")

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
	mcpRemoteProxy := v1beta1test.NewMCPRemoteProxy("test-proxy", "default",
		v1beta1test.WithRemoteProxyGroupRef("test-group"),
		v1beta1test.WithRemoteProxyURL(""),
		v1beta1test.WithRemoteProxyPort(0),
	)

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
	mockReg := newMockRegistryWithBackend("test-server")

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
	mockReg := newMockRegistryWithBackend("remote-mcp")

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
	mockReg := newMockRegistryWithBackend("deleted-entry")

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

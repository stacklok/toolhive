package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
)

func TestDiscoverBackends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		vmcp            *mcpv1alpha1.VirtualMCPServer
		mcpGroup        *mcpv1alpha1.MCPGroup
		mcpServers      []*mcpv1alpha1.MCPServer
		expectedError   bool
		expectedCount   int
		validateBackend func(t *testing.T, backend mcpv1alpha1.DiscoveredBackend)
	}{
		{
			name: "discovers backends from MCPGroup",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: "test-group",
					},
				},
			},
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPGroupStatus{
					Servers: []string{"server1", "server2"},
				},
			},
			mcpServers: []*mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Transport: "streamable-http",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						URL: "http://server1.default.svc.cluster.local:8080",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Transport: "http",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "auth-config-1",
						},
					},
					Status: mcpv1alpha1.MCPServerStatus{
						URL: "http://server2.default.svc.cluster.local:8080",
					},
				},
			},
			expectedError: false,
			expectedCount: 2,
			validateBackend: func(t *testing.T, backend mcpv1alpha1.DiscoveredBackend) {
				t.Helper()
				if backend.Name == "server1" {
					assert.Equal(t, "streamable-http", backend.TransportType)
					assert.Equal(t, "discovered", backend.AuthType)
					assert.Equal(t, "http://server1.default.svc.cluster.local:8080", backend.URL)
					assert.Empty(t, backend.ExternalAuthConfigRef)
				} else if backend.Name == "server2" {
					assert.Equal(t, "http", backend.TransportType)
					assert.Equal(t, "external_auth_config", backend.AuthType)
					assert.Equal(t, "auth-config-1", backend.ExternalAuthConfigRef)
					assert.Equal(t, "http://server2.default.svc.cluster.local:8080", backend.URL)
				}
			},
		},
		{
			name: "applies outgoing auth overrides from VirtualMCPServer",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: "test-group",
					},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Backends: map[string]mcpv1alpha1.BackendAuthConfig{
							"server1": {
								Type: "pass_through",
							},
						},
					},
				},
			},
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPGroupStatus{
					Servers: []string{"server1"},
				},
			},
			mcpServers: []*mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Transport: "streamable-http",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						URL: "http://server1.default.svc.cluster.local:8080",
					},
				},
			},
			expectedError: false,
			expectedCount: 1,
			validateBackend: func(t *testing.T, backend mcpv1alpha1.DiscoveredBackend) {
				t.Helper()
				assert.Equal(t, "server1", backend.Name)
				assert.Equal(t, "pass_through", backend.AuthType)
			},
		},
		{
			name: "handles missing MCPServer gracefully",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: "test-group",
					},
				},
			},
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPGroupStatus{
					Servers: []string{"server1", "missing-server"},
				},
			},
			mcpServers: []*mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Transport: "streamable-http",
					},
				},
			},
			expectedError: false,
			expectedCount: 1, // Only server1 should be discovered, missing-server skipped
			validateBackend: func(t *testing.T, backend mcpv1alpha1.DiscoveredBackend) {
				t.Helper()
				assert.Equal(t, "server1", backend.Name)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create scheme with all CRD types
			scheme := runtime.NewScheme()
			err := mcpv1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			// Create initial objects for fake client
			initObjs := []client.Object{tt.vmcp, tt.mcpGroup}
			for _, server := range tt.mcpServers {
				initObjs = append(initObjs, server)
			}

			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(initObjs...).
				WithStatusSubresource(&mcpv1alpha1.VirtualMCPServer{}).
				Build()

			// Create reconciler
			reconciler := &VirtualMCPServerReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Recorder: record.NewFakeRecorder(10),
			}

			// Create status manager
			statusManager := virtualmcpserverstatus.NewStatusManager(tt.vmcp)

			// Call discoverBackends
			ctx := context.Background()
			err = reconciler.discoverBackends(ctx, tt.vmcp, statusManager)

			// Validate error expectation
			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Apply status updates to capture discovered backends
			latestVMCP := &mcpv1alpha1.VirtualMCPServer{}
			err = fakeClient.Get(ctx, types.NamespacedName{
				Name:      tt.vmcp.Name,
				Namespace: tt.vmcp.Namespace,
			}, latestVMCP)
			require.NoError(t, err)

			statusManager.UpdateStatus(ctx, &latestVMCP.Status)

			// Validate discovered backends count
			assert.Equal(t, tt.expectedCount, latestVMCP.Status.BackendCount)
			assert.Len(t, latestVMCP.Status.DiscoveredBackends, tt.expectedCount)

			// Validate each backend if validator provided
			if tt.validateBackend != nil {
				for _, backend := range latestVMCP.Status.DiscoveredBackends {
					tt.validateBackend(t, backend)
				}
			}
		})
	}
}

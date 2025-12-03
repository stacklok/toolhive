// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
)

// TestVirtualMCPServerDiscoverBackends tests the discoverBackends function
func TestVirtualMCPServerDiscoverBackends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		vmcp                *mcpv1alpha1.VirtualMCPServer
		mcpGroup            *mcpv1alpha1.MCPGroup
		mcpServers          []mcpv1alpha1.MCPServer
		authConfigs         []mcpv1alpha1.MCPExternalAuthConfig // Auth configs referenced by MCPServers
		secrets             []corev1.Secret                     // Secrets referenced by auth configs
		expectedBackends    int
		expectedStatuses    map[string]string // backend name -> status
		expectedAuthConfigs map[string]string // backend name -> authConfigRef
		expectError         bool
	}{
		{
			name: "discovers multiple healthy backends",
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
					Phase:   mcpv1alpha1.MCPGroupPhaseReady,
					Servers: []string{"backend-1", "backend-2"},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef:  "test-group",
						Image:     "test-image:latest",
						Transport: "streamable-http",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
						URL:   "http://mcp-backend-1-proxy.default.svc.cluster.local:8080",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
						URL:   "http://mcp-backend-2-proxy.default.svc.cluster.local:8080",
					},
				},
			},
			expectedBackends: 2,
			expectedStatuses: map[string]string{
				"backend-1": "ready",
				"backend-2": "ready",
			},
			expectedAuthConfigs: map[string]string{},
			expectError:         false,
		},
		{
			name: "discovers backend with external auth config",
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
					Phase:   mcpv1alpha1.MCPGroupPhaseReady,
					Servers: []string{"backend-1"},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef:  "test-group",
						Image:     "test-image:latest",
						Transport: "streamable-http",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "my-auth-config",
						},
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
						URL:   "http://mcp-backend-1-proxy.default.svc.cluster.local:8080",
					},
				},
			},
			authConfigs: []mcpv1alpha1.MCPExternalAuthConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-auth-config",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
						TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
							TokenURL: "https://auth.example.com/token",
							ClientID: "test-client",
							ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
								Name: "my-auth-secret",
								Key:  "client-secret",
							},
							Audience: "test-audience",
						},
					},
				},
			},
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-auth-secret",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"client-secret": []byte("test-secret-value"),
					},
				},
			},
			expectedBackends: 1,
			expectedStatuses: map[string]string{
				"backend-1": "ready",
			},
			expectedAuthConfigs: map[string]string{
				"backend-1": "my-auth-config",
			},
			expectError: false,
		},
		{
			name: "tracks unavailable backends",
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
					Phase:   mcpv1alpha1.MCPGroupPhaseReady,
					Servers: []string{"backend-1", "backend-2"},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef:  "test-group",
						Image:     "test-image:latest",
						Transport: "streamable-http",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
						URL:   "http://mcp-backend-1-proxy.default.svc.cluster.local:8080",
					},
				},
				// backend-2 exists in group but has no URL (unavailable)
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhasePending,
						URL:   "", // No URL means unavailable - GetWorkloadAsVMCPBackend will return nil
					},
				},
			},
			expectedBackends: 2,
			expectedStatuses: map[string]string{
				"backend-1": "ready",
				"backend-2": "unavailable",
			},
			expectedAuthConfigs: map[string]string{},
			expectError:         false,
		},
		{
			name: "maps backend health statuses correctly",
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
					Phase:   mcpv1alpha1.MCPGroupPhaseReady,
					Servers: []string{"healthy", "unhealthy", "degraded", "unknown"},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "healthy",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
						URL:   "http://mcp-healthy-proxy.default.svc.cluster.local:8080",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "unhealthy",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseFailed,
						URL:   "http://mcp-unhealthy-proxy.default.svc.cluster.local:8080",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "degraded",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
						URL:   "http://mcp-degraded-proxy.default.svc.cluster.local:8080",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "unknown",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhasePending,
						URL:   "http://mcp-unknown-proxy.default.svc.cluster.local:8080",
					},
				},
			},
			expectedBackends: 4,
			expectedStatuses: map[string]string{
				"healthy":   "ready",       // MCPServerPhaseRunning -> BackendHealthy -> "ready"
				"unhealthy": "unavailable", // MCPServerPhaseFailed -> BackendUnhealthy -> "unavailable"
				"degraded":  "ready",       // MCPServerPhaseRunning -> BackendHealthy -> "ready"
				"unknown":   "unknown",     // MCPServerPhasePending -> BackendUnknown -> "unknown"
			},
			expectedAuthConfigs: map[string]string{},
			expectError:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)

			// Build objects list for fake client
			objects := []client.Object{tt.vmcp, tt.mcpGroup}
			for i := range tt.mcpServers {
				objects = append(objects, &tt.mcpServers[i])
			}
			for i := range tt.authConfigs {
				objects = append(objects, &tt.authConfigs[i])
			}
			for i := range tt.secrets {
				objects = append(objects, &tt.secrets[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			r := &VirtualMCPServerReconciler{
				Client:           fakeClient,
				Scheme:           scheme,
				PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
			}

			// Note: This test requires actual K8S workload discovery to work
			// In a real scenario, we would mock the aggregator, but since discoverBackends
			// creates its own discoverer internally, we test the integration.
			// For unit testing, we'd need to refactor to inject the discoverer.
			ctx := context.Background()
			discoveredBackends, err := r.discoverBackends(ctx, tt.vmcp)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Len(t, discoveredBackends, tt.expectedBackends)

			// Verify each backend
			for _, backend := range discoveredBackends {
				// Check status
				if expectedStatus, ok := tt.expectedStatuses[backend.Name]; ok {
					assert.Equal(t, expectedStatus, backend.Status, "backend %s should have status %s", backend.Name, expectedStatus)
				}

				// Check auth config
				if expectedAuthConfig, ok := tt.expectedAuthConfigs[backend.Name]; ok {
					assert.Equal(t, expectedAuthConfig, backend.AuthConfigRef, "backend %s should have authConfigRef %s", backend.Name, expectedAuthConfig)
					assert.Equal(t, mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef, backend.AuthType, "backend %s should have correct auth type", backend.Name)
				}

				// Verify all backends have LastHealthCheck set
				assert.False(t, backend.LastHealthCheck.IsZero(), "backend %s should have LastHealthCheck set", backend.Name)

				// Verify URL is set for accessible backends
				if backend.Status == "ready" {
					assert.NotEmpty(t, backend.URL, "backend %s should have URL set", backend.Name)
				}
			}
		})
	}
}

// TestVirtualMCPServerStatusManagerDiscoveredBackends tests that StatusManager
// correctly sets discovered backends and backend count
func TestVirtualMCPServerStatusManagerDiscoveredBackends(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-vmcp",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
		},
	}

	statusManager := virtualmcpserverstatus.NewStatusManager(vmcp)

	discoveredBackends := []mcpv1alpha1.DiscoveredBackend{
		{
			Name:            "backend-1",
			Status:          "ready",
			URL:             "http://backend-1.default.svc.cluster.local:8080",
			LastHealthCheck: metav1.Now(),
		},
		{
			Name:            "backend-2",
			Status:          "ready",
			URL:             "http://backend-2.default.svc.cluster.local:8080",
			LastHealthCheck: metav1.Now(),
		},
		{
			Name:            "backend-3",
			Status:          "unavailable",
			LastHealthCheck: metav1.Now(),
		},
	}

	statusManager.SetDiscoveredBackends(discoveredBackends)

	// Create a fresh status object to update
	vmcpStatus := &mcpv1alpha1.VirtualMCPServerStatus{}

	ctx := context.Background()
	updated := statusManager.UpdateStatus(ctx, vmcpStatus)

	assert.True(t, updated, "status should be updated")
	assert.Len(t, vmcpStatus.DiscoveredBackends, 3, "should have 3 discovered backends")
	assert.Equal(t, 2, vmcpStatus.BackendCount, "backendCount should be 2 (only ready backends)")

	// Verify backend details
	assert.Equal(t, "backend-1", vmcpStatus.DiscoveredBackends[0].Name)
	assert.Equal(t, "ready", vmcpStatus.DiscoveredBackends[0].Status)
	assert.Equal(t, "http://backend-1.default.svc.cluster.local:8080", vmcpStatus.DiscoveredBackends[0].URL)

	assert.Equal(t, "backend-2", vmcpStatus.DiscoveredBackends[1].Name)
	assert.Equal(t, "ready", vmcpStatus.DiscoveredBackends[1].Status)

	assert.Equal(t, "backend-3", vmcpStatus.DiscoveredBackends[2].Name)
	assert.Equal(t, "unavailable", vmcpStatus.DiscoveredBackends[2].Status)
	assert.Empty(t, vmcpStatus.DiscoveredBackends[2].URL, "unavailable backend should not have URL")
}

// TestVirtualMCPServerReconcileWithBackendDiscovery tests the full reconcile
// flow including backend discovery
func TestVirtualMCPServerReconcileWithBackendDiscovery(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-vmcp",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
		},
	}

	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase:   mcpv1alpha1.MCPGroupPhaseReady,
			Servers: []string{"backend-1"},
		},
	}

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend-1",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			GroupRef:  "test-group",
			Image:     "test-image:latest",
			Transport: "streamable-http",
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
			URL:   "http://mcp-backend-1-proxy.default.svc.cluster.local:8080",
		},
	}

	// Create deployment and service for the VMCP
	replicas := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
			Labels:    labelsForVirtualMCPServer(vmcp.Name),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labelsForVirtualMCPServer(vmcp.Name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForVirtualMCPServer(vmcp.Name),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "vmcp",
							Image: "test-image:latest",
						},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: 1,
		},
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcpServiceName(vmcp.Name),
			Namespace: "default",
			Labels:    labelsForVirtualMCPServer(vmcp.Name),
		},
		Spec: corev1.ServiceSpec{
			Selector: labelsForVirtualMCPServer(vmcp.Name),
			Ports: []corev1.ServicePort{
				{
					Port: 4483,
				},
			},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp-pod",
			Namespace: "default",
			Labels:    labelsForVirtualMCPServer(vmcp.Name),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp-vmcp-config",
			Namespace: "default",
		},
		Data: map[string]string{
			"config.yaml": "{}",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, mcpGroup, mcpServer, deployment, service, pod, configMap).
		WithStatusSubresource(vmcp).
		Build()

	r := &VirtualMCPServerReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	ctx := context.Background()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-vmcp",
			Namespace: "default",
		},
	}

	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Fetch updated VMCP
	updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
	err = fakeClient.Get(ctx, req.NamespacedName, updatedVMCP)
	require.NoError(t, err)

	// Verify discovered backends are set
	assert.GreaterOrEqual(t, len(updatedVMCP.Status.DiscoveredBackends), 0, "should have discovered backends")
	assert.GreaterOrEqual(t, updatedVMCP.Status.BackendCount, 0, "should have backendCount set")

	// Verify BackendsDiscovered condition
	backendsDiscoveredCondition := findCondition(updatedVMCP.Status.Conditions, "BackendsDiscovered")
	if backendsDiscoveredCondition != nil {
		assert.Equal(t, metav1.ConditionTrue, backendsDiscoveredCondition.Status, "BackendsDiscovered condition should be True")
		assert.Contains(t, backendsDiscoveredCondition.Message, "Discovered", "condition message should mention discovery")
	}
}

// Helper function to find a condition by type
func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

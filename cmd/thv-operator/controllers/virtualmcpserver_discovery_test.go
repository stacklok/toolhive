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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// TestDiscoverBackends tests backend discovery from MCPGroup
func TestDiscoverBackends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		vmcp               *mcpv1alpha1.VirtualMCPServer
		mcpGroup           *mcpv1alpha1.MCPGroup
		mcpServers         []mcpv1alpha1.MCPServer
		authConfigs        []mcpv1alpha1.MCPExternalAuthConfig
		expectedBackends   int
		expectedCondStatus metav1.ConditionStatus
		expectedReason     string
	}{
		{
			name: "discover two backends successfully",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
			},
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPGroupStatus{
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
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "auth-1",
						},
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
						URL:   "http://backend-1.default.svc.cluster.local:8080",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-2",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
						URL:   "http://backend-2.default.svc.cluster.local:8080",
					},
				},
			},
			authConfigs: []mcpv1alpha1.MCPExternalAuthConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "auth-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					},
				},
			},
			expectedBackends:   2,
			expectedCondStatus: metav1.ConditionTrue,
			expectedReason:     mcpv1alpha1.ConditionReasonDiscoveryComplete,
		},
		{
			name: "no backends in group",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
			},
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-group",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPGroupStatus{
					Servers: []string{},
				},
			},
			expectedBackends:   0,
			expectedCondStatus: metav1.ConditionFalse,
			expectedReason:     mcpv1alpha1.ConditionReasonNoBackends,
		},
		{
			name: "backend not found",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
			},
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPGroupStatus{
					Servers: []string{"missing-backend"},
				},
			},
			expectedBackends:   1, // Should still create entry for unavailable backend
			expectedCondStatus: metav1.ConditionTrue,
			expectedReason:     mcpv1alpha1.ConditionReasonDiscoveryComplete,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)

			objs := []client.Object{tt.vmcp, tt.mcpGroup}
			for i := range tt.mcpServers {
				objs = append(objs, &tt.mcpServers[i])
			}
			for i := range tt.authConfigs {
				objs = append(objs, &tt.authConfigs[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			r := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			err := r.discoverBackends(context.Background(), tt.vmcp, tt.mcpGroup)
			require.NoError(t, err)

			// Check discovered backends count
			assert.Len(t, tt.vmcp.Status.DiscoveredBackends, tt.expectedBackends)

			// Check condition
			foundCondition := false
			for _, cond := range tt.vmcp.Status.Conditions {
				if cond.Type == mcpv1alpha1.ConditionTypeBackendsDiscovered {
					foundCondition = true
					assert.Equal(t, tt.expectedCondStatus, cond.Status)
					assert.Equal(t, tt.expectedReason, cond.Reason)
				}
			}
			assert.True(t, foundCondition, "BackendsDiscovered condition should be set")

			// Verify backend details for successful discovery
			if tt.expectedBackends > 0 && len(tt.mcpServers) > 0 {
				for _, backend := range tt.vmcp.Status.DiscoveredBackends {
					if backend.Status == "ready" {
						assert.NotEmpty(t, backend.URL)
						assert.NotNil(t, backend.LastHealthCheck)
					}
				}
			}
		})
	}
}

// TestDiscoverBackendServer tests individual backend server discovery
func TestDiscoverBackendServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		serverName     string
		mcpServer      *mcpv1alpha1.MCPServer
		authConfig     *mcpv1alpha1.MCPExternalAuthConfig
		expectError    bool
		expectedStatus string
		expectedAuth   string
	}{
		{
			name:       "running server with auth config",
			serverName: "backend-1",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "backend-1",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "auth-config",
					},
				},
				Status: mcpv1alpha1.MCPServerStatus{
					Phase: mcpv1alpha1.MCPServerPhaseRunning,
					URL:   "http://backend-1.default.svc.cluster.local:8080",
				},
			},
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "auth-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
				},
			},
			expectError:    false,
			expectedStatus: "ready",
			expectedAuth:   "token_exchange",
		},
		{
			name:       "running server without auth config",
			serverName: "backend-2",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "backend-2",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPServerStatus{
					Phase: mcpv1alpha1.MCPServerPhaseRunning,
					URL:   "http://backend-2.default.svc.cluster.local:8080",
				},
			},
			expectError:    false,
			expectedStatus: "ready",
			expectedAuth:   "",
		},
		{
			name:       "failed server",
			serverName: "backend-3",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "backend-3",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPServerStatus{
					Phase: mcpv1alpha1.MCPServerPhaseFailed,
					URL:   "http://backend-3.default.svc.cluster.local:8080",
				},
			},
			expectError:    false,
			expectedStatus: "degraded",
			expectedAuth:   "",
		},
		{
			name:       "pending server",
			serverName: "backend-4",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "backend-4",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPServerStatus{
					Phase: mcpv1alpha1.MCPServerPhasePending,
					URL:   "",
				},
			},
			expectError:    false,
			expectedStatus: "unavailable",
			expectedAuth:   "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)

			objs := []client.Object{tt.mcpServer}
			if tt.authConfig != nil {
				objs = append(objs, tt.authConfig)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			r := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			backend, err := r.discoverBackendServer(context.Background(), "default", tt.serverName)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.serverName, backend.Name)
				assert.Equal(t, tt.expectedStatus, backend.Status)
				assert.NotNil(t, backend.LastHealthCheck)

				if tt.expectedAuth != "" {
					assert.Equal(t, tt.expectedAuth, backend.AuthType)
				}
			}
		})
	}
}

// TestGetAuthTypeFromConfig tests auth type extraction
func TestGetAuthTypeFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		authConfig   *mcpv1alpha1.MCPExternalAuthConfig
		expectedType string
	}{
		{
			name: "token exchange",
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
				},
			},
			expectedType: "token_exchange",
		},
		{
			name: "unknown type",
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: "custom",
				},
			},
			expectedType: "custom",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &VirtualMCPServerReconciler{}
			authType := r.getAuthTypeFromConfig(tt.authConfig)
			assert.Equal(t, tt.expectedType, authType)
		})
	}
}

// TestCalculateCapabilitiesSummary tests capability aggregation
func TestCalculateCapabilitiesSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                       string
		vmcp                       *mcpv1alpha1.VirtualMCPServer
		backends                   []mcpv1alpha1.DiscoveredBackend
		expectedToolCount          int
		expectedCompositeToolCount int
	}{
		{
			name: "multiple ready backends",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					CompositeTools: []mcpv1alpha1.CompositeToolSpec{
						{Name: "composite-1"},
						{Name: "composite-2"},
					},
				},
			},
			backends: []mcpv1alpha1.DiscoveredBackend{
				{Name: "backend-1", Status: "ready"},
				{Name: "backend-2", Status: "ready"},
			},
			expectedToolCount:          10, // 2 backends * 5 tools each (placeholder)
			expectedCompositeToolCount: 2,
		},
		{
			name: "mixed backend status",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					CompositeTools: []mcpv1alpha1.CompositeToolSpec{
						{Name: "composite-1"},
					},
				},
			},
			backends: []mcpv1alpha1.DiscoveredBackend{
				{Name: "backend-1", Status: "ready"},
				{Name: "backend-2", Status: "unavailable"},
			},
			expectedToolCount:          5, // 1 ready backend * 5 tools
			expectedCompositeToolCount: 1,
		},
		{
			name: "no ready backends",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{},
			},
			backends: []mcpv1alpha1.DiscoveredBackend{
				{Name: "backend-1", Status: "unavailable"},
			},
			expectedToolCount:          0,
			expectedCompositeToolCount: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &VirtualMCPServerReconciler{}
			summary := r.calculateCapabilitiesSummary(tt.vmcp, tt.backends)

			assert.NotNil(t, summary)
			assert.Equal(t, tt.expectedToolCount, summary.ToolCount)
			assert.Equal(t, tt.expectedCompositeToolCount, summary.CompositeToolCount)
		})
	}
}

// TestCheckBackendHealth tests health check logic
func TestCheckBackendHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		backends            []mcpv1alpha1.DiscoveredBackend
		expectedAllHealthy  bool
		expectedSomeHealthy bool
		expectedUnavailable int
	}{
		{
			name: "all healthy",
			backends: []mcpv1alpha1.DiscoveredBackend{
				{Name: "backend-1", Status: "ready"},
				{Name: "backend-2", Status: "ready"},
			},
			expectedAllHealthy:  true,
			expectedSomeHealthy: true,
			expectedUnavailable: 0,
		},
		{
			name: "some unhealthy",
			backends: []mcpv1alpha1.DiscoveredBackend{
				{Name: "backend-1", Status: "ready"},
				{Name: "backend-2", Status: "unavailable"},
			},
			expectedAllHealthy:  false,
			expectedSomeHealthy: true,
			expectedUnavailable: 1,
		},
		{
			name: "all unhealthy",
			backends: []mcpv1alpha1.DiscoveredBackend{
				{Name: "backend-1", Status: "unavailable"},
				{Name: "backend-2", Status: "degraded"},
			},
			expectedAllHealthy:  false,
			expectedSomeHealthy: false,
			expectedUnavailable: 2,
		},
		{
			name:                "no backends",
			backends:            []mcpv1alpha1.DiscoveredBackend{},
			expectedAllHealthy:  false,
			expectedSomeHealthy: false,
			expectedUnavailable: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &mcpv1alpha1.VirtualMCPServer{
				Status: mcpv1alpha1.VirtualMCPServerStatus{
					DiscoveredBackends: tt.backends,
				},
			}

			r := &VirtualMCPServerReconciler{}
			result := r.checkBackendHealth(context.Background(), vmcp)

			assert.Equal(t, tt.expectedAllHealthy, result.allHealthy)
			assert.Equal(t, tt.expectedSomeHealthy, result.someHealthy)
			assert.Equal(t, tt.expectedUnavailable, result.unavailableCount)
			assert.Equal(t, len(tt.backends), result.totalCount)
		})
	}
}

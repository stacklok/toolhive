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
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestMapMCPGroupToVirtualMCPServer tests the MCPGroup watch handler
func TestMapMCPGroupToVirtualMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		mcpGroup          *mcpv1alpha1.MCPGroup
		virtualMCPServers []mcpv1alpha1.VirtualMCPServer
		expectedRequests  int
		expectedNames     []string
	}{
		{
			name: "single VirtualMCPServer references MCPGroup",
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
					},
				},
			},
			expectedRequests: 1,
			expectedNames:    []string{"vmcp-1"},
		},
		{
			name: "multiple VirtualMCPServers reference MCPGroup",
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
					},
				},
			},
			expectedRequests: 2,
			expectedNames:    []string{"vmcp-1", "vmcp-2"},
		},
		{
			name: "no VirtualMCPServers reference MCPGroup",
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "other-group"},
					},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
		{
			name: "mixed VirtualMCPServers some reference MCPGroup",
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "other-group"},
					},
				},
			},
			expectedRequests: 1,
			expectedNames:    []string{"vmcp-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create scheme
			scheme := runtime.NewScheme()
			err := mcpv1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			// Create objects slice
			objs := []client.Object{tt.mcpGroup}
			for i := range tt.virtualMCPServers {
				objs = append(objs, &tt.virtualMCPServers[i])
			}

			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			// Create reconciler
			r := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Test the watch handler
			requests := r.mapMCPGroupToVirtualMCPServer(context.Background(), tt.mcpGroup)

			// Verify results
			assert.Equal(t, tt.expectedRequests, len(requests), "Expected %d requests, got %d", tt.expectedRequests, len(requests))

			// Verify request names
			if len(tt.expectedNames) > 0 {
				requestNames := make([]string, len(requests))
				for i, req := range requests {
					requestNames[i] = req.Name
				}
				assert.ElementsMatch(t, tt.expectedNames, requestNames)
			}
		})
	}
}

// TestMapMCPGroupToVirtualMCPServer_InvalidObject tests error handling
func TestMapMCPGroupToVirtualMCPServer_InvalidObject(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	err := mcpv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Pass wrong object type
	wrongObj := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
	}

	requests := r.mapMCPGroupToVirtualMCPServer(context.Background(), wrongObj)
	assert.Nil(t, requests, "Expected nil for invalid object type")
}

// TestMapMCPServerToVirtualMCPServer tests the optimized MCPServer watch handler
func TestMapMCPServerToVirtualMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		mcpServer         *mcpv1alpha1.MCPServer
		mcpGroups         []mcpv1alpha1.MCPGroup
		virtualMCPServers []mcpv1alpha1.VirtualMCPServer
		expectedRequests  int
		expectedNames     []string
	}{
		{
			name: "MCPServer is member of MCPGroup referenced by VirtualMCPServer",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						Servers: []string{"test-server", "other-server"},
					},
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
					},
				},
			},
			expectedRequests: 1,
			expectedNames:    []string{"vmcp-1"},
		},
		{
			name: "MCPServer is not member of any MCPGroup",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						Servers: []string{"other-server"},
					},
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
					},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
		{
			name: "MCPServer is member of MCPGroup but no VirtualMCPServers reference it",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						Servers: []string{"test-server"},
					},
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "other-group"},
					},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
		{
			name: "MCPServer is member of multiple MCPGroups with multiple VirtualMCPServers",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group-1",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						Servers: []string{"test-server"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group-2",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						Servers: []string{"test-server", "other-server"},
					},
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "group-1"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "group-2"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-3",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "group-3"},
					},
				},
			},
			expectedRequests: 2,
			expectedNames:    []string{"vmcp-1", "vmcp-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create scheme
			scheme := runtime.NewScheme()
			err := mcpv1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			// Create objects slice
			objs := []client.Object{tt.mcpServer}
			for i := range tt.mcpGroups {
				objs = append(objs, &tt.mcpGroups[i])
			}
			for i := range tt.virtualMCPServers {
				objs = append(objs, &tt.virtualMCPServers[i])
			}

			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(
					&mcpv1alpha1.MCPGroup{},
				).
				Build()

			// Create reconciler
			r := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Test the watch handler
			requests := r.mapMCPServerToVirtualMCPServer(context.Background(), tt.mcpServer)

			// Verify results
			assert.Equal(t, tt.expectedRequests, len(requests), "Expected %d requests, got %d", tt.expectedRequests, len(requests))

			// Verify request names
			if len(tt.expectedNames) > 0 {
				requestNames := make([]string, len(requests))
				for i, req := range requests {
					requestNames[i] = req.Name
				}
				assert.ElementsMatch(t, tt.expectedNames, requestNames)
			}
		})
	}
}

// TestMapMCPServerToVirtualMCPServer_InvalidObject tests error handling
func TestMapMCPServerToVirtualMCPServer_InvalidObject(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	err := mcpv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Pass wrong object type
	wrongObj := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
	}

	requests := r.mapMCPServerToVirtualMCPServer(context.Background(), wrongObj)
	assert.Nil(t, requests, "Expected nil for invalid object type")
}

// TestMapMCPRemoteProxyToVirtualMCPServer tests the optimized MCPRemoteProxy watch handler
func TestMapMCPRemoteProxyToVirtualMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		mcpRemoteProxy    *mcpv1alpha1.MCPRemoteProxy
		mcpGroups         []mcpv1alpha1.MCPGroup
		virtualMCPServers []mcpv1alpha1.VirtualMCPServer
		expectedRequests  int
		expectedNames     []string
	}{
		{
			name: "MCPRemoteProxy is member of MCPGroup referenced by VirtualMCPServer",
			mcpRemoteProxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-proxy",
					Namespace: "default",
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						RemoteProxies: []string{"test-proxy", "other-proxy"},
					},
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
					},
				},
			},
			expectedRequests: 1,
			expectedNames:    []string{"vmcp-1"},
		},
		{
			name: "MCPRemoteProxy is not member of any MCPGroup",
			mcpRemoteProxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-proxy",
					Namespace: "default",
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						RemoteProxies: []string{"other-proxy"},
					},
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
					},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
		{
			name: "MCPRemoteProxy is member of MCPGroup but no VirtualMCPServers reference it",
			mcpRemoteProxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-proxy",
					Namespace: "default",
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						RemoteProxies: []string{"test-proxy"},
					},
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "other-group"},
					},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
		{
			name: "MCPRemoteProxy is member of multiple MCPGroups with multiple VirtualMCPServers",
			mcpRemoteProxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-proxy",
					Namespace: "default",
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group-1",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						RemoteProxies: []string{"test-proxy"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group-2",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						RemoteProxies: []string{"test-proxy", "other-proxy"},
					},
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "group-1"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "group-2"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-3",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "group-3"},
					},
				},
			},
			expectedRequests: 2,
			expectedNames:    []string{"vmcp-1", "vmcp-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create scheme
			scheme := runtime.NewScheme()
			err := mcpv1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			// Create objects slice
			objs := []client.Object{tt.mcpRemoteProxy}
			for i := range tt.mcpGroups {
				objs = append(objs, &tt.mcpGroups[i])
			}
			for i := range tt.virtualMCPServers {
				objs = append(objs, &tt.virtualMCPServers[i])
			}

			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(
					&mcpv1alpha1.MCPGroup{},
				).
				Build()

			// Create reconciler
			r := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Test the watch handler
			requests := r.mapMCPRemoteProxyToVirtualMCPServer(context.Background(), tt.mcpRemoteProxy)

			// Verify results
			assert.Equal(t, tt.expectedRequests, len(requests), "Expected %d requests, got %d", tt.expectedRequests, len(requests))

			// Verify request names
			if len(tt.expectedNames) > 0 {
				requestNames := make([]string, len(requests))
				for i, req := range requests {
					requestNames[i] = req.Name
				}
				assert.ElementsMatch(t, tt.expectedNames, requestNames)
			}
		})
	}
}

// TestMapMCPRemoteProxyToVirtualMCPServer_InvalidObject tests error handling
func TestMapMCPRemoteProxyToVirtualMCPServer_InvalidObject(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	err := mcpv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Pass wrong object type
	wrongObj := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
	}

	requests := r.mapMCPRemoteProxyToVirtualMCPServer(context.Background(), wrongObj)
	assert.Nil(t, requests, "Expected nil for invalid object type")
}

// TestMapExternalAuthConfigToVirtualMCPServer tests the ExternalAuthConfig watch handler
// This function filters to only reconcile VirtualMCPServers that actually reference the changed ExternalAuthConfig
func TestMapExternalAuthConfigToVirtualMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		authConfig        *mcpv1alpha1.MCPExternalAuthConfig
		virtualMCPServers []mcpv1alpha1.VirtualMCPServer
		mcpGroups         []mcpv1alpha1.MCPGroup
		mcpServers        []mcpv1alpha1.MCPServer
		mcpRemoteProxies  []mcpv1alpha1.MCPRemoteProxy
		expectedRequests  int
		expectedNames     []string
	}{
		{
			name: "VirtualMCPServer references ExternalAuthConfig in default backend auth",
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
							Default: &mcpv1alpha1.BackendAuthConfig{
								Type: "external_auth_config_ref",
								ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
									Name: "test-auth",
								},
							},
						},
					},
				},
			},
			expectedRequests: 1,
			expectedNames:    []string{"vmcp-1"},
		},
		{
			name: "VirtualMCPServer references ExternalAuthConfig in per-backend auth",
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
							Backends: map[string]mcpv1alpha1.BackendAuthConfig{
								"backend1": {
									Type: "external_auth_config_ref",
									ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
										Name: "test-auth",
									},
								},
							},
						},
					},
				},
			},
			expectedRequests: 1,
			expectedNames:    []string{"vmcp-1"},
		},
		{
			name: "VirtualMCPServer does not reference ExternalAuthConfig",
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
		{
			name: "multiple VirtualMCPServers, only one references ExternalAuthConfig",
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
							Default: &mcpv1alpha1.BackendAuthConfig{
								Type: "external_auth_config_ref",
								ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
									Name: "test-auth",
								},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{},
				},
			},
			expectedRequests: 1,
			expectedNames:    []string{"vmcp-1"},
		},
		{
			name: "no VirtualMCPServers in namespace",
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{},
			expectedRequests:  0,
			expectedNames:     []string{},
		},
		{
			name: "VirtualMCPServer with discovered mode - MCPServer references auth config",
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-discovered",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
						OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
							Source: "discovered",
						},
					},
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-server",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "test-auth",
						},
					},
				},
			},
			expectedRequests: 1,
			expectedNames:    []string{"vmcp-discovered"},
		},
		{
			name: "VirtualMCPServer with discovered mode - no MCPServer references auth config",
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-discovered",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
						OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
							Source: "discovered",
						},
					},
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-server",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "other-auth",
						},
					},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
		{
			name: "VirtualMCPServer with discovered mode - MCPRemoteProxy references auth config",
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-discovered",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
						OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
							Source: "discovered",
						},
					},
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
				},
			},
			mcpRemoteProxies: []mcpv1alpha1.MCPRemoteProxy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-proxy",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPRemoteProxySpec{
						GroupRef: "test-group",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "test-auth",
						},
					},
				},
			},
			expectedRequests: 1,
			expectedNames:    []string{"vmcp-discovered"},
		},
		{
			name: "VirtualMCPServer with discovered mode - no MCPRemoteProxy references auth config",
			authConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-discovered",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group"},
						OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
							Source: "discovered",
						},
					},
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
				},
			},
			mcpRemoteProxies: []mcpv1alpha1.MCPRemoteProxy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-proxy",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPRemoteProxySpec{
						GroupRef: "test-group",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "other-auth",
						},
					},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create scheme
			scheme := runtime.NewScheme()
			err := mcpv1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			// Create objects slice
			objs := []client.Object{tt.authConfig}
			for i := range tt.virtualMCPServers {
				objs = append(objs, &tt.virtualMCPServers[i])
			}
			for i := range tt.mcpGroups {
				objs = append(objs, &tt.mcpGroups[i])
			}
			for i := range tt.mcpServers {
				objs = append(objs, &tt.mcpServers[i])
			}
			for i := range tt.mcpRemoteProxies {
				objs = append(objs, &tt.mcpRemoteProxies[i])
			}

			// Create fake client with field indexers for groupRef fields
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithIndex(&mcpv1alpha1.MCPServer{}, "spec.groupRef", func(obj client.Object) []string {
					mcpServer := obj.(*mcpv1alpha1.MCPServer)
					if mcpServer.Spec.GroupRef == "" {
						return nil
					}
					return []string{mcpServer.Spec.GroupRef}
				}).
				WithIndex(&mcpv1alpha1.MCPRemoteProxy{}, "spec.groupRef", func(obj client.Object) []string {
					mcpRemoteProxy := obj.(*mcpv1alpha1.MCPRemoteProxy)
					if mcpRemoteProxy.Spec.GroupRef == "" {
						return nil
					}
					return []string{mcpRemoteProxy.Spec.GroupRef}
				}).
				Build()

			// Create reconciler
			r := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Test the watch handler
			requests := r.mapExternalAuthConfigToVirtualMCPServer(context.Background(), tt.authConfig)

			// Verify results
			assert.Equal(t, tt.expectedRequests, len(requests), "Expected %d requests, got %d", tt.expectedRequests, len(requests))

			// Verify request names
			if len(tt.expectedNames) > 0 {
				requestNames := make([]string, len(requests))
				for i, req := range requests {
					requestNames[i] = req.Name
				}
				assert.ElementsMatch(t, tt.expectedNames, requestNames)
			}
		})
	}
}

// TestMapToolConfigToVirtualMCPServer tests the ToolConfig watch handler
func TestMapToolConfigToVirtualMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		toolConfig        *mcpv1alpha1.MCPToolConfig
		virtualMCPServers []mcpv1alpha1.VirtualMCPServer
		expectedRequests  int
		expectedNames     []string
	}{
		{
			name: "VirtualMCPServer references ToolConfig in Aggregation.Tools",
			toolConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-tool-config",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{
							Aggregation: &vmcpconfig.AggregationConfig{
								Tools: []*vmcpconfig.WorkloadToolConfig{
									{
										ToolConfigRef: &vmcpconfig.ToolConfigRef{
											Name: "test-tool-config",
										},
									},
								},
							},
						},
					},
				},
			},
			expectedRequests: 1,
			expectedNames:    []string{"vmcp-1"},
		},
		{
			name: "no VirtualMCPServers reference ToolConfig",
			toolConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-tool-config",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
		{
			name: "multiple VirtualMCPServers reference same ToolConfig",
			toolConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-tool-config",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{
							Aggregation: &vmcpconfig.AggregationConfig{
								Tools: []*vmcpconfig.WorkloadToolConfig{
									{
										ToolConfigRef: &vmcpconfig.ToolConfigRef{
											Name: "test-tool-config",
										},
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{
							Aggregation: &vmcpconfig.AggregationConfig{
								Tools: []*vmcpconfig.WorkloadToolConfig{
									{
										ToolConfigRef: &vmcpconfig.ToolConfigRef{
											Name: "test-tool-config",
										},
									},
									{
										ToolConfigRef: &vmcpconfig.ToolConfigRef{
											Name: "other-tool-config",
										},
									},
								},
							},
						},
					},
				},
			},
			expectedRequests: 2,
			expectedNames:    []string{"vmcp-1", "vmcp-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create scheme
			scheme := runtime.NewScheme()
			err := mcpv1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			// Create objects slice
			objs := []client.Object{tt.toolConfig}
			for i := range tt.virtualMCPServers {
				objs = append(objs, &tt.virtualMCPServers[i])
			}

			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			// Create reconciler
			r := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Test the watch handler
			requests := r.mapToolConfigToVirtualMCPServer(context.Background(), tt.toolConfig)

			// Verify results
			assert.Equal(t, tt.expectedRequests, len(requests), "Expected %d requests, got %d", tt.expectedRequests, len(requests))

			// Verify request names
			if len(tt.expectedNames) > 0 {
				requestNames := make([]string, len(requests))
				for i, req := range requests {
					requestNames[i] = req.Name
				}
				assert.ElementsMatch(t, tt.expectedNames, requestNames)
			}
		})
	}
}

// TestVmcpReferencesToolConfig tests the helper function for checking ToolConfig references
func TestVmcpReferencesToolConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		vmcp       *mcpv1alpha1.VirtualMCPServer
		configName string
		expected   bool
	}{
		{
			name: "VirtualMCPServer references ToolConfig",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Aggregation: &vmcpconfig.AggregationConfig{
							Tools: []*vmcpconfig.WorkloadToolConfig{
								{
									ToolConfigRef: &vmcpconfig.ToolConfigRef{
										Name: "test-config",
									},
								},
							},
						},
					},
				},
			},
			configName: "test-config",
			expected:   true,
		},
		{
			name: "VirtualMCPServer does not reference ToolConfig",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Aggregation: &vmcpconfig.AggregationConfig{
							Tools: []*vmcpconfig.WorkloadToolConfig{
								{
									ToolConfigRef: &vmcpconfig.ToolConfigRef{
										Name: "other-config",
									},
								},
							},
						},
					},
				},
			},
			configName: "test-config",
			expected:   false,
		},
		{
			name: "VirtualMCPServer has no Aggregation",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{},
			},
			configName: "test-config",
			expected:   false,
		},
		{
			name: "VirtualMCPServer references ToolConfig among multiple tools",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Aggregation: &vmcpconfig.AggregationConfig{
							Tools: []*vmcpconfig.WorkloadToolConfig{
								{
									ToolConfigRef: &vmcpconfig.ToolConfigRef{
										Name: "other-config",
									},
								},
								{
									ToolConfigRef: &vmcpconfig.ToolConfigRef{
										Name: "test-config",
									},
								},
								{
									ToolConfigRef: &vmcpconfig.ToolConfigRef{
										Name: "another-config",
									},
								},
							},
						},
					},
				},
			},
			configName: "test-config",
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &VirtualMCPServerReconciler{}
			result := r.vmcpReferencesToolConfig(tt.vmcp, tt.configName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestVmcpReferencesExternalAuthConfig tests the helper function for checking ExternalAuthConfig references
func TestVmcpReferencesExternalAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		vmcp             *mcpv1alpha1.VirtualMCPServer
		mcpGroups        []mcpv1alpha1.MCPGroup
		mcpServers       []mcpv1alpha1.MCPServer
		mcpRemoteProxies []mcpv1alpha1.MCPRemoteProxy
		authConfigName   string
		expected         bool
	}{
		{
			name: "VirtualMCPServer references ExternalAuthConfig in default backend auth",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Default: &mcpv1alpha1.BackendAuthConfig{
							Type: "external_auth_config_ref",
							ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
								Name: "test-auth",
							},
						},
					},
				},
			},
			authConfigName: "test-auth",
			expected:       true,
		},
		{
			name: "VirtualMCPServer references ExternalAuthConfig in per-backend auth",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Backends: map[string]mcpv1alpha1.BackendAuthConfig{
							"backend1": {
								Type: "external_auth_config_ref",
								ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
									Name: "test-auth",
								},
							},
						},
					},
				},
			},
			authConfigName: "test-auth",
			expected:       true,
		},
		{
			name: "VirtualMCPServer does not reference ExternalAuthConfig",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{},
			},
			authConfigName: "test-auth",
			expected:       false,
		},
		{
			name: "VirtualMCPServer has no OutgoingAuth",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					OutgoingAuth: nil,
				},
			},
			authConfigName: "test-auth",
			expected:       false,
		},
		{
			name: "VirtualMCPServer references different ExternalAuthConfig",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Default: &mcpv1alpha1.BackendAuthConfig{
							Type: "external_auth_config_ref",
							ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
								Name: "other-auth",
							},
						},
					},
				},
			},
			authConfigName: "test-auth",
			expected:       false,
		},
		{
			name: "VirtualMCPServer references ExternalAuthConfig in multiple backends",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Backends: map[string]mcpv1alpha1.BackendAuthConfig{
							"backend1": {
								Type: "external_auth_config_ref",
								ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
									Name: "other-auth",
								},
							},
							"backend2": {
								Type: "external_auth_config_ref",
								ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
									Name: "test-auth",
								},
							},
							"backend3": {
								Type: "service_account",
							},
						},
					},
				},
			},
			authConfigName: "test-auth",
			expected:       true,
		},
		{
			name: "VirtualMCPServer with discovered mode - MCPServer references auth config",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vmcp-discovered",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
					},
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-server",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "test-auth",
						},
					},
				},
			},
			authConfigName: "test-auth",
			expected:       true,
		},
		{
			name: "VirtualMCPServer with discovered mode - no MCPServer references auth config",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vmcp-discovered",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
					},
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-server",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "other-auth",
						},
					},
				},
			},
			authConfigName: "test-auth",
			expected:       false,
		},
		{
			name: "VirtualMCPServer with discovered mode - MCPGroup does not exist",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vmcp-discovered",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: "nonexistent-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
					},
				},
			},
			authConfigName: "test-auth",
			expected:       false,
		},
		{
			name: "VirtualMCPServer with discovered mode - multiple MCPServers, one references auth config",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vmcp-discovered",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
					},
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-server-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "other-auth",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-server-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "test-auth",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-server-3",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						GroupRef: "test-group",
					},
				},
			},
			authConfigName: "test-auth",
			expected:       true,
		},
		{
			name: "VirtualMCPServer with discovered mode - MCPRemoteProxy references auth config",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vmcp-discovered",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
					},
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
				},
			},
			mcpRemoteProxies: []mcpv1alpha1.MCPRemoteProxy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-proxy",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPRemoteProxySpec{
						GroupRef: "test-group",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "test-auth",
						},
					},
				},
			},
			authConfigName: "test-auth",
			expected:       true,
		},
		{
			name: "VirtualMCPServer with discovered mode - MCPRemoteProxy does not reference auth config",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vmcp-discovered",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
					},
				},
			},
			mcpGroups: []mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
				},
			},
			mcpRemoteProxies: []mcpv1alpha1.MCPRemoteProxy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-proxy",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPRemoteProxySpec{
						GroupRef: "test-group",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "other-auth",
						},
					},
				},
			},
			authConfigName: "test-auth",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create scheme
			scheme := runtime.NewScheme()
			err := mcpv1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			// Create objects slice
			objs := []client.Object{}
			if tt.vmcp.Name != "" {
				objs = append(objs, tt.vmcp)
			}
			for i := range tt.mcpGroups {
				objs = append(objs, &tt.mcpGroups[i])
			}
			for i := range tt.mcpServers {
				objs = append(objs, &tt.mcpServers[i])
			}
			for i := range tt.mcpRemoteProxies {
				objs = append(objs, &tt.mcpRemoteProxies[i])
			}

			// Create fake client with field indexers for groupRef fields
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithIndex(&mcpv1alpha1.MCPServer{}, "spec.groupRef", func(obj client.Object) []string {
					mcpServer := obj.(*mcpv1alpha1.MCPServer)
					if mcpServer.Spec.GroupRef == "" {
						return nil
					}
					return []string{mcpServer.Spec.GroupRef}
				}).
				WithIndex(&mcpv1alpha1.MCPRemoteProxy{}, "spec.groupRef", func(obj client.Object) []string {
					mcpRemoteProxy := obj.(*mcpv1alpha1.MCPRemoteProxy)
					if mcpRemoteProxy.Spec.GroupRef == "" {
						return nil
					}
					return []string{mcpRemoteProxy.Spec.GroupRef}
				}).
				Build()

			r := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}
			result := r.vmcpReferencesExternalAuthConfig(context.Background(), tt.vmcp, tt.authConfigName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestMapEmbeddingServerToVirtualMCPServer tests the EmbeddingServer watch handler
func TestMapEmbeddingServerToVirtualMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		embeddingServer   *mcpv1alpha1.EmbeddingServer
		virtualMCPServers []mcpv1alpha1.VirtualMCPServer
		expectedRequests  int
		expectedNames     []string
	}{
		{
			name: "single VirtualMCPServer references EmbeddingServer",
			embeddingServer: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "shared-embedding",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config:             vmcpconfig.Config{Group: "test-group"},
						EmbeddingServerRef: &mcpv1alpha1.EmbeddingServerRef{Name: "shared-embedding"},
					},
				},
			},
			expectedRequests: 1,
			expectedNames:    []string{"vmcp-1"},
		},
		{
			name: "multiple VirtualMCPServers share EmbeddingServer",
			embeddingServer: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "shared-embedding",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config:             vmcpconfig.Config{Group: "test-group"},
						EmbeddingServerRef: &mcpv1alpha1.EmbeddingServerRef{Name: "shared-embedding"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config:             vmcpconfig.Config{Group: "test-group"},
						EmbeddingServerRef: &mcpv1alpha1.EmbeddingServerRef{Name: "shared-embedding"},
					},
				},
			},
			expectedRequests: 2,
			expectedNames:    []string{"vmcp-1", "vmcp-2"},
		},
		{
			name: "no VirtualMCPServers reference EmbeddingServer",
			embeddingServer: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "shared-embedding",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config:             vmcpconfig.Config{Group: "test-group"},
						EmbeddingServerRef: &mcpv1alpha1.EmbeddingServerRef{Name: "other-embedding"},
					},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
		{
			name: "inline embeddingServer does not trigger ref watch",
			embeddingServer: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "shared-embedding",
					Namespace: "default",
				},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config:          vmcpconfig.Config{Group: "test-group"},
						EmbeddingServer: &mcpv1alpha1.EmbeddingServerSpec{Model: "test-model", Image: "test:latest"},
					},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create scheme
			scheme := runtime.NewScheme()
			err := mcpv1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			// Create objects slice
			objs := []client.Object{tt.embeddingServer}
			for i := range tt.virtualMCPServers {
				objs = append(objs, &tt.virtualMCPServers[i])
			}

			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			// Create reconciler
			r := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Test the watch handler
			requests := r.mapEmbeddingServerToVirtualMCPServer(context.Background(), tt.embeddingServer)

			// Verify results
			assert.Equal(t, tt.expectedRequests, len(requests), "Expected %d requests, got %d", tt.expectedRequests, len(requests))

			// Verify request names
			if len(tt.expectedNames) > 0 {
				requestNames := make([]string, len(requests))
				for i, req := range requests {
					requestNames[i] = req.Name
				}
				assert.ElementsMatch(t, tt.expectedNames, requestNames)
			}
		})
	}
}

// TestMapEmbeddingServerToVirtualMCPServer_InvalidObject tests error handling
func TestMapEmbeddingServerToVirtualMCPServer_InvalidObject(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	err := mcpv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Pass wrong object type
	wrongObj := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
	}

	requests := r.mapEmbeddingServerToVirtualMCPServer(context.Background(), wrongObj)
	assert.Nil(t, requests, "Expected nil for invalid object type")
}

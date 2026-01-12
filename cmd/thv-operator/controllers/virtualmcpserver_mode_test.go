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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestIsDynamicMode tests the mode detection helper function
func TestIsDynamicMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		vmcp           *mcpv1alpha1.VirtualMCPServer
		expectedResult bool
	}{
		{
			name: "no OutgoingAuth - defaults to dynamic",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{},
			},
			expectedResult: true,
		},
		{
			name: "OutgoingAuth with empty source - defaults to dynamic",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "",
					},
				},
			},
			expectedResult: true,
		},
		{
			name: "OutgoingAuth source is 'discovered' - dynamic mode",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: OutgoingAuthSourceDiscovered,
					},
				},
			},
			expectedResult: true,
		},
		{
			name: "OutgoingAuth source is 'inline' - static mode",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: OutgoingAuthSourceInline,
					},
				},
			},
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &VirtualMCPServerReconciler{}
			result := r.isDynamicMode(tt.vmcp)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestReconcile_DynamicMode_SkipsDiscovery tests that dynamic mode skips backend discovery
func TestReconcile_DynamicMode_SkipsDiscovery(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-vmcp-dynamic",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{Group: "test-group"},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
			OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
				Source: OutgoingAuthSourceDiscovered, // Dynamic mode
			},
		},
		Status: mcpv1alpha1.VirtualMCPServerStatus{
			// Simulate vMCP having previously reported some backends
			DiscoveredBackends: []mcpv1alpha1.DiscoveredBackend{
				{
					Name:   "backend-from-vmcp",
					Status: mcpv1alpha1.BackendStatusReady,
					URL:    "http://backend-from-vmcp.default.svc.cluster.local:8080",
				},
			},
			BackendCount: 1,
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

	// Create minimal resources to allow reconciliation to proceed
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp-dynamic-vmcp-config",
			Namespace: "default",
		},
		Data: map[string]string{
			"config.yaml": "groupRef: test-group\noutgoingAuth:\n  source: discovered\n",
		},
	}

	replicas := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp-dynamic",
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
			Name:      "test-vmcp-dynamic-pod",
			Namespace: "default",
			Labels:    labelsForVirtualMCPServer(vmcp.Name),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, mcpGroup, mcpServer, configMap, deployment, service, pod).
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
			Name:      "test-vmcp-dynamic",
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

	// In dynamic mode, the operator should preserve the existing vMCP-reported status
	// It should NOT overwrite it with operator-discovered backends
	assert.Len(t, updatedVMCP.Status.DiscoveredBackends, 1, "should preserve existing backends from vMCP")
	assert.Equal(t, "backend-from-vmcp", updatedVMCP.Status.DiscoveredBackends[0].Name,
		"should preserve backend name from vMCP status")
	assert.Equal(t, 1, updatedVMCP.Status.BackendCount, "should preserve backend count")

	// In dynamic mode, the operator should NOT set the BackendsDiscovered condition
	// The vMCP server owns this condition and reports it via StatusReporter
	backendsDiscoveredCondition := findCondition(updatedVMCP.Status.Conditions, "BackendsDiscovered")
	assert.Nil(t, backendsDiscoveredCondition,
		"BackendsDiscovered condition should not be set by operator in dynamic mode")
}

// TestReconcile_StaticMode_PerformsDiscovery tests that static mode discovers backends
func TestReconcile_StaticMode_PerformsDiscovery(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-vmcp-static",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{Group: "test-group"},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
			OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
				Source: OutgoingAuthSourceInline, // Static mode
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
			Servers: []string{"backend-1", "backend-2"},
		},
	}

	mcpServer1 := &mcpv1alpha1.MCPServer{
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

	mcpServer2 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend-2",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			GroupRef:  "test-group",
			Image:     "test-image:latest",
			Transport: "streamable-http",
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
			URL:   "http://mcp-backend-2-proxy.default.svc.cluster.local:8080",
		},
	}

	// Create minimal resources
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp-static-vmcp-config",
			Namespace: "default",
		},
		Data: map[string]string{
			"config.yaml": "groupRef: test-group\noutgoingAuth:\n  source: inline\n",
		},
	}

	replicas := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp-static",
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
			Name:      "test-vmcp-static-pod",
			Namespace: "default",
			Labels:    labelsForVirtualMCPServer(vmcp.Name),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, mcpGroup, mcpServer1, mcpServer2, configMap, deployment, service, pod).
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
			Name:      "test-vmcp-static",
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

	// In static mode, the operator should discover and populate backends
	assert.Len(t, updatedVMCP.Status.DiscoveredBackends, 2,
		"should have discovered 2 backends from MCPGroup in static mode")
	assert.Equal(t, 2, updatedVMCP.Status.BackendCount, "should have backend count set")

	// Verify BackendsDiscovered condition is set
	backendsDiscoveredCondition := findCondition(updatedVMCP.Status.Conditions, "BackendsDiscovered")
	require.NotNil(t, backendsDiscoveredCondition, "BackendsDiscovered condition should be set in static mode")
	assert.Equal(t, metav1.ConditionTrue, backendsDiscoveredCondition.Status,
		"BackendsDiscovered condition should be True")
	assert.Contains(t, backendsDiscoveredCondition.Message, "Discovered 2 backends",
		"condition message should mention discovery count")

	// Verify backend names
	backendNames := make(map[string]bool)
	for _, backend := range updatedVMCP.Status.DiscoveredBackends {
		backendNames[backend.Name] = true
	}
	assert.True(t, backendNames["backend-1"], "should have discovered backend-1")
	assert.True(t, backendNames["backend-2"], "should have discovered backend-2")
}

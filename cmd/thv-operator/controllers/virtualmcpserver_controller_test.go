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
)

// TestVirtualMCPServerValidateAndDiscoverBackends tests the backend discovery and validation
func TestVirtualMCPServerValidateAndDiscoverBackends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		vmcp           *mcpv1alpha1.VirtualMCPServer
		mcpGroup       *mcpv1alpha1.MCPGroup
		mcpServers     []mcpv1alpha1.MCPServer
		expectError    bool
		expectedPhase  mcpv1alpha1.VirtualMCPServerPhase
		expectedReason string
	}{
		{
			name: "valid group ref with ready group",
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
			expectError:    false,
			expectedReason: mcpv1alpha1.ConditionReasonVirtualMCPServerGroupRefValid,
		},
		{
			name: "group ref not found",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: "missing-group",
					},
				},
			},
			expectError:    true,
			expectedPhase:  mcpv1alpha1.VirtualMCPServerPhaseFailed,
			expectedReason: mcpv1alpha1.ConditionReasonVirtualMCPServerGroupRefNotFound,
		},
		{
			name: "group ref not ready",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: "pending-group",
					},
				},
			},
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pending-group",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPGroupStatus{
					Phase: mcpv1alpha1.MCPGroupPhasePending,
				},
			},
			expectError:    true,
			expectedPhase:  mcpv1alpha1.VirtualMCPServerPhasePending,
			expectedReason: mcpv1alpha1.ConditionReasonVirtualMCPServerGroupRefNotReady,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Setup fake client with resources
			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)
			_ = rbacv1.AddToScheme(scheme)

			objs := []client.Object{tt.vmcp}
			if tt.mcpGroup != nil {
				objs = append(objs, tt.mcpGroup)
			}
			for i := range tt.mcpServers {
				objs = append(objs, &tt.mcpServers[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.VirtualMCPServer{}).
				Build()

			r := &VirtualMCPServerReconciler{
				Client:           fakeClient,
				Scheme:           scheme,
				PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
			}

			err := r.validateAndDiscoverBackends(context.Background(), tt.vmcp)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, tt.expectedPhase, tt.vmcp.Status.Phase)

				// Check condition reason
				for _, cond := range tt.vmcp.Status.Conditions {
					if cond.Type == mcpv1alpha1.ConditionTypeVirtualMCPServerGroupRefValidated {
						assert.Equal(t, tt.expectedReason, cond.Reason)
					}
				}
			} else {
				assert.NoError(t, err)

				// Check condition is set to true
				foundCondition := false
				for _, cond := range tt.vmcp.Status.Conditions {
					if cond.Type == mcpv1alpha1.ConditionTypeVirtualMCPServerGroupRefValidated {
						foundCondition = true
						assert.Equal(t, metav1.ConditionTrue, cond.Status)
						assert.Equal(t, tt.expectedReason, cond.Reason)
					}
				}
				assert.True(t, foundCondition, "GroupRefValidated condition should be set")
			}
		})
	}
}

// TestVirtualMCPServerEnsureRBACResources tests RBAC resource creation
func TestVirtualMCPServerEnsureRBACResources(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp).
		Build()

	r := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	err := r.ensureRBACResources(context.Background(), vmcp)
	require.NoError(t, err)

	// Verify ServiceAccount was created
	sa := &corev1.ServiceAccount{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcpServiceAccountName(vmcp.Name),
		Namespace: vmcp.Namespace,
	}, sa)
	require.NoError(t, err)
	assert.Equal(t, vmcpServiceAccountName(vmcp.Name), sa.Name)

	// Verify Role was created
	role := &rbacv1.Role{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcpServiceAccountName(vmcp.Name),
		Namespace: vmcp.Namespace,
	}, role)
	require.NoError(t, err)
	assert.Equal(t, vmcpServiceAccountName(vmcp.Name), role.Name)
	assert.NotEmpty(t, role.Rules)

	// Verify RoleBinding was created
	rb := &rbacv1.RoleBinding{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcpServiceAccountName(vmcp.Name),
		Namespace: vmcp.Namespace,
	}, rb)
	require.NoError(t, err)
	assert.Equal(t, vmcpServiceAccountName(vmcp.Name), rb.Name)
	assert.Equal(t, vmcpServiceAccountName(vmcp.Name), rb.RoleRef.Name)
	assert.Len(t, rb.Subjects, 1)
	assert.Equal(t, vmcpServiceAccountName(vmcp.Name), rb.Subjects[0].Name)
}

// TestVirtualMCPServerEnsureDeployment tests Deployment creation
func TestVirtualMCPServerEnsureDeployment(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
		},
	}

	// Create ConfigMap with checksum
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp-vmcp-config",
			Namespace: "default",
			Annotations: map[string]string{
				"toolhive.stacklok.dev/content-checksum": "test-checksum-123",
			},
		},
		Data: map[string]string{
			"config.json": "{}",
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, configMap).
		Build()

	r := &VirtualMCPServerReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	result, err := r.ensureDeployment(context.Background(), vmcp)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify Deployment was created
	deployment := &appsv1.Deployment{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcp.Name,
		Namespace: vmcp.Namespace,
	}, deployment)
	require.NoError(t, err)
	assert.Equal(t, vmcp.Name, deployment.Name)
	assert.NotNil(t, deployment.Spec.Replicas)
	assert.Equal(t, int32(1), *deployment.Spec.Replicas)

	// Verify container configuration
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
	container := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "vmcp", container.Name)
	assert.NotEmpty(t, container.Image)
	assert.Contains(t, container.Args, "serve")
	assert.Contains(t, container.Args, "--config=/etc/vmcp-config/config.json")

	// Verify checksum annotation is set
	assert.Equal(t, "test-checksum-123",
		deployment.Spec.Template.Annotations["toolhive.stacklok.dev/vmcp-config-checksum"])
}

// TestVirtualMCPServerEnsureService tests Service creation
func TestVirtualMCPServerEnsureService(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp).
		Build()

	r := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	result, err := r.ensureService(context.Background(), vmcp)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify Service was created
	service := &corev1.Service{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcpServiceName(vmcp.Name),
		Namespace: vmcp.Namespace,
	}, service)
	require.NoError(t, err)
	assert.Equal(t, vmcpServiceName(vmcp.Name), service.Name)
	assert.Equal(t, corev1.ServiceTypeClusterIP, service.Spec.Type)

	// Verify port configuration
	require.Len(t, service.Spec.Ports, 1)
	assert.Equal(t, vmcpDefaultPort, service.Spec.Ports[0].Port)
	assert.Equal(t, "http", service.Spec.Ports[0].Name)
}

// TestVirtualMCPServerUpdateStatus tests status update logic
func TestVirtualMCPServerUpdateStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		vmcp          *mcpv1alpha1.VirtualMCPServer
		pods          []corev1.Pod
		expectedPhase mcpv1alpha1.VirtualMCPServerPhase
	}{
		{
			name: "running pods with healthy backends",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Status: mcpv1alpha1.VirtualMCPServerStatus{
					DiscoveredBackends: []mcpv1alpha1.DiscoveredBackend{
						{Name: "backend-1", Status: "ready"},
						{Name: "backend-2", Status: "ready"},
					},
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-vmcp-pod-1",
						Namespace: "default",
						Labels:    labelsForVirtualMCPServer("test-vmcp"),
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
			},
			expectedPhase: mcpv1alpha1.VirtualMCPServerPhaseReady,
		},
		{
			name: "running pods with degraded backends",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Status: mcpv1alpha1.VirtualMCPServerStatus{
					DiscoveredBackends: []mcpv1alpha1.DiscoveredBackend{
						{Name: "backend-1", Status: "ready"},
						{Name: "backend-2", Status: "unavailable"},
					},
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-vmcp-pod-1",
						Namespace: "default",
						Labels:    labelsForVirtualMCPServer("test-vmcp"),
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
			},
			expectedPhase: mcpv1alpha1.VirtualMCPServerPhaseDegraded,
		},
		{
			name: "pending pods",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Status: mcpv1alpha1.VirtualMCPServerStatus{
					DiscoveredBackends: []mcpv1alpha1.DiscoveredBackend{
						{Name: "backend-1", Status: "ready"},
					},
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-vmcp-pod-1",
						Namespace: "default",
						Labels:    labelsForVirtualMCPServer("test-vmcp"),
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodPending,
					},
				},
			},
			expectedPhase: mcpv1alpha1.VirtualMCPServerPhasePending,
		},
		{
			name: "failed pods",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Status: mcpv1alpha1.VirtualMCPServerStatus{
					DiscoveredBackends: []mcpv1alpha1.DiscoveredBackend{
						{Name: "backend-1", Status: "ready"},
					},
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-vmcp-pod-1",
						Namespace: "default",
						Labels:    labelsForVirtualMCPServer("test-vmcp"),
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodFailed,
					},
				},
			},
			expectedPhase: mcpv1alpha1.VirtualMCPServerPhaseFailed,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)

			objs := []client.Object{tt.vmcp}
			for i := range tt.pods {
				objs = append(objs, &tt.pods[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.VirtualMCPServer{}).
				Build()

			r := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			err := r.updateVirtualMCPServerStatus(context.Background(), tt.vmcp)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedPhase, tt.vmcp.Status.Phase)
		})
	}
}

// TestVirtualMCPServerLabels tests label generation
func TestVirtualMCPServerLabels(t *testing.T) {
	t.Parallel()

	name := "test-vmcp"
	labels := labelsForVirtualMCPServer(name)

	assert.Equal(t, "virtualmcpserver", labels["app"])
	assert.Equal(t, "virtualmcpserver", labels["app.kubernetes.io/name"])
	assert.Equal(t, name, labels["app.kubernetes.io/instance"])
	assert.Equal(t, "true", labels["toolhive"])
	assert.Equal(t, name, labels["toolhive-name"])
}

// TestVirtualMCPServerNaming tests naming functions
func TestVirtualMCPServerNaming(t *testing.T) {
	t.Parallel()

	vmcpName := "my-vmcp"

	// Test service account name
	saName := vmcpServiceAccountName(vmcpName)
	assert.Equal(t, "my-vmcp-vmcp", saName)

	// Test service name
	svcName := vmcpServiceName(vmcpName)
	assert.Equal(t, "vmcp-my-vmcp", svcName)

	// Test service URL
	url := createVmcpServiceURL(vmcpName, "default", 8080)
	assert.Equal(t, "http://vmcp-my-vmcp.default.svc.cluster.local:8080", url)
}

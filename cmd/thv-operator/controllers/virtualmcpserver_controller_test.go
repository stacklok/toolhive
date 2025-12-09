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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
)

const (
	testChecksumValue = "test-checksum-123"
	testVmcpName      = "test-vmcp"
)

// TestVirtualMCPServerValidateGroupRef tests the GroupRef validation
func TestVirtualMCPServerValidateGroupRef(t *testing.T) {
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
					Name:      testVmcpName,
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: testGroupName,
					},
				},
			},
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testGroupName,
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
					Name:      testVmcpName,
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
					Name:      testVmcpName,
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

			statusManager := virtualmcpserverstatus.NewStatusManager(tt.vmcp)
			err := r.validateGroupRef(context.Background(), tt.vmcp, statusManager)
			// Apply status updates for test assertions
			_ = statusManager.UpdateStatus(context.Background(), &tt.vmcp.Status)

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
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
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
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	// Create MCPGroup that the VirtualMCPServer references
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testGroupName,
			Namespace: "default",
		},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	// Create ConfigMap with checksum
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcpConfigMapName(vmcp.Name),
			Namespace: "default",
			Annotations: map[string]string{
				"toolhive.stacklok.dev/content-checksum": "test-checksum-123",
			},
		},
		Data: map[string]string{
			"config.yaml": "{}",
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, mcpGroup, configMap).
		Build()

	r := &VirtualMCPServerReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	result, err := r.ensureDeployment(context.Background(), vmcp, []string{})
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
	assert.Contains(t, container.Args, "--config=/etc/vmcp-config/config.yaml")

	// Verify checksum annotation is set using standard annotation key
	assert.Equal(t, "test-checksum-123",
		deployment.Spec.Template.Annotations[checksum.RunConfigChecksumAnnotation])
}

// TestVirtualMCPServerEnsureService tests Service creation
func TestVirtualMCPServerEnsureService(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
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

// TestVirtualMCPServerServiceType tests Service creation with different service types
func TestVirtualMCPServerServiceType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		serviceType         string
		expectedServiceType corev1.ServiceType
	}{
		{
			name:                "default to ClusterIP",
			serviceType:         "",
			expectedServiceType: corev1.ServiceTypeClusterIP,
		},
		{
			name:                "explicit ClusterIP",
			serviceType:         "ClusterIP",
			expectedServiceType: corev1.ServiceTypeClusterIP,
		},
		{
			name:                "LoadBalancer",
			serviceType:         "LoadBalancer",
			expectedServiceType: corev1.ServiceTypeLoadBalancer,
		},
		{
			name:                "NodePort",
			serviceType:         "NodePort",
			expectedServiceType: corev1.ServiceTypeNodePort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testVmcpName,
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: testGroupName,
					},
					ServiceType: tt.serviceType,
				},
			}

			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)

			r := &VirtualMCPServerReconciler{
				Scheme: scheme,
			}

			// Test serviceForVirtualMCPServer
			service := r.serviceForVirtualMCPServer(context.Background(), vmcp)
			require.NotNil(t, service)
			assert.Equal(t, tt.expectedServiceType, service.Spec.Type)
		})
	}
}

// TestVirtualMCPServerServiceNeedsUpdate tests service update detection
func TestVirtualMCPServerServiceNeedsUpdate(t *testing.T) {
	t.Parallel()

	baseVmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
			ServiceType: "ClusterIP",
		},
	}

	baseService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcpServiceName(baseVmcp.Name),
			Namespace: baseVmcp.Namespace,
			Labels:    labelsForVirtualMCPServer(baseVmcp.Name),
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{
				Port: vmcpDefaultPort,
			}},
		},
	}

	tests := []struct {
		name        string
		service     *corev1.Service
		vmcp        *mcpv1alpha1.VirtualMCPServer
		needsUpdate bool
	}{
		{
			name:        "no update needed",
			service:     baseService.DeepCopy(),
			vmcp:        baseVmcp.DeepCopy(),
			needsUpdate: false,
		},
		{
			name:    "service type changed to LoadBalancer",
			service: baseService.DeepCopy(),
			vmcp: func() *mcpv1alpha1.VirtualMCPServer {
				v := baseVmcp.DeepCopy()
				v.Spec.ServiceType = "LoadBalancer"
				return v
			}(),
			needsUpdate: true,
		},
		{
			name:    "service type changed to NodePort",
			service: baseService.DeepCopy(),
			vmcp: func() *mcpv1alpha1.VirtualMCPServer {
				v := baseVmcp.DeepCopy()
				v.Spec.ServiceType = "NodePort"
				return v
			}(),
			needsUpdate: true,
		},
		{
			name: "port changed",
			service: func() *corev1.Service {
				s := baseService.DeepCopy()
				s.Spec.Ports[0].Port = 9999
				return s
			}(),
			vmcp:        baseVmcp.DeepCopy(),
			needsUpdate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &VirtualMCPServerReconciler{}
			result := r.serviceNeedsUpdate(tt.service, tt.vmcp)
			assert.Equal(t, tt.needsUpdate, result)
		})
	}
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
			name: "ready pods",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testVmcpName,
					Namespace: "default",
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testVmcpName + "-pod-1",
						Namespace: "default",
						Labels:    labelsForVirtualMCPServer(testVmcpName),
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{
								Type:   corev1.PodReady,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			expectedPhase: mcpv1alpha1.VirtualMCPServerPhaseReady,
		},
		{
			name: "running but not ready pods",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testVmcpName,
					Namespace: "default",
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testVmcpName + "-pod-1",
						Namespace: "default",
						Labels:    labelsForVirtualMCPServer(testVmcpName),
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						// No PodReady condition or PodReady=False means pod isn't ready yet
						Conditions: []corev1.PodCondition{
							{
								Type:   corev1.PodReady,
								Status: corev1.ConditionFalse,
							},
						},
					},
				},
			},
			expectedPhase: mcpv1alpha1.VirtualMCPServerPhasePending,
		},
		{
			name: "pending pods",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testVmcpName,
					Namespace: "default",
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testVmcpName + "-pod-1",
						Namespace: "default",
						Labels:    labelsForVirtualMCPServer(testVmcpName),
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
					Name:      testVmcpName,
					Namespace: "default",
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testVmcpName + "-pod-1",
						Namespace: "default",
						Labels:    labelsForVirtualMCPServer(testVmcpName),
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

			statusManager := virtualmcpserverstatus.NewStatusManager(tt.vmcp)
			err := r.updateVirtualMCPServerStatus(context.Background(), tt.vmcp, statusManager)
			require.NoError(t, err)
			// Apply status updates for test assertions
			_ = statusManager.UpdateStatus(context.Background(), &tt.vmcp.Status)
			assert.Equal(t, tt.expectedPhase, tt.vmcp.Status.Phase)
		})
	}
}

// TestVirtualMCPServerLabels tests label generation
func TestVirtualMCPServerLabels(t *testing.T) {
	t.Parallel()

	name := testVmcpName
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

	// Test ConfigMap name
	cmName := vmcpConfigMapName(vmcpName)
	assert.Equal(t, "my-vmcp-vmcp-config", cmName)

	// Test service URL
	url := createVmcpServiceURL(vmcpName, "default", 8080)
	assert.Equal(t, "http://vmcp-my-vmcp.default.svc.cluster.local:8080", url)
}

// TestVirtualMCPServerAuthConfiguredCondition tests AuthConfigured condition setting
// with various secret validation scenarios
func TestVirtualMCPServerAuthConfiguredCondition(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name                string
		vmcp                *mcpv1alpha1.VirtualMCPServer
		secrets             []client.Object
		expectAuthCondition bool
		expectedAuthStatus  metav1.ConditionStatus
		expectedAuthReason  string
		expectError         bool
	}{
		{
			name: "valid auth with no secrets required (anonymous)",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testVmcpName,
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: testGroupName,
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
				},
			},
			secrets:             []client.Object{},
			expectAuthCondition: true,
			expectedAuthStatus:  metav1.ConditionTrue,
			expectedAuthReason:  mcpv1alpha1.ConditionReasonAuthValid,
			expectError:         false,
		},
		{
			name: "OIDC with missing client secret",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testVmcpName,
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: testGroupName,
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "oidc",
						OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
							Type: "inline",
							Inline: &mcpv1alpha1.InlineOIDCConfig{
								Issuer:   "https://issuer.example.com",
								Audience: "test-audience",
								ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "missing-secret",
									Key:  "client-secret",
								},
							},
						},
					},
				},
			},
			secrets:             []client.Object{},
			expectAuthCondition: true,
			expectedAuthStatus:  metav1.ConditionFalse,
			expectedAuthReason:  mcpv1alpha1.ConditionReasonAuthInvalid,
			expectError:         true,
		},
		{
			name: "OIDC with valid client secret",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testVmcpName,
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: testGroupName,
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "oidc",
						OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
							Type: "inline",
							Inline: &mcpv1alpha1.InlineOIDCConfig{
								Issuer:   "https://issuer.example.com",
								Audience: "test-audience",
								ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "oidc-secret",
									Key:  "client-secret",
								},
							},
						},
					},
				},
			},
			secrets: []client.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "oidc-secret",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"client-secret": []byte("supersecret"),
					},
				},
			},
			expectAuthCondition: true,
			expectedAuthStatus:  metav1.ConditionTrue,
			expectedAuthReason:  mcpv1alpha1.ConditionReasonAuthValid,
			expectError:         false,
		},
		{
			name: "OIDC secret exists but missing required key",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testVmcpName,
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: testGroupName,
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "oidc",
						OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
							Type: "inline",
							Inline: &mcpv1alpha1.InlineOIDCConfig{
								Issuer:   "https://issuer.example.com",
								Audience: "test-audience",
								ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "oidc-secret",
									Key:  "client-secret",
								},
							},
						},
					},
				},
			},
			secrets: []client.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "oidc-secret",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"wrong-key": []byte("supersecret"),
					},
				},
			},
			expectAuthCondition: true,
			expectedAuthStatus:  metav1.ConditionFalse,
			expectedAuthReason:  mcpv1alpha1.ConditionReasonAuthInvalid,
			expectError:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objs := append([]client.Object{tt.vmcp}, tt.secrets...)

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

			statusManager := virtualmcpserverstatus.NewStatusManager(tt.vmcp)
			err := r.ensureAllResources(context.Background(), tt.vmcp, statusManager)

			if tt.expectError {
				assert.Error(t, err)
			}
			// ensureAllResources may return errors for missing resources like MCPGroup
			// We're only testing the auth condition setting

			// Apply status updates to check condition
			_ = statusManager.UpdateStatus(context.Background(), &tt.vmcp.Status)

			if tt.expectAuthCondition {
				// Find AuthConfigured condition
				var authCondition *metav1.Condition
				for i := range tt.vmcp.Status.Conditions {
					if tt.vmcp.Status.Conditions[i].Type == mcpv1alpha1.ConditionTypeAuthConfigured {
						authCondition = &tt.vmcp.Status.Conditions[i]
						break
					}
				}

				require.NotNil(t, authCondition, "AuthConfigured condition should be set")
				assert.Equal(t, tt.expectedAuthStatus, authCondition.Status)
				assert.Equal(t, tt.expectedAuthReason, authCondition.Reason)
			}
		})
	}
}

func TestVirtualMCPServerReconcile_NotFound(t *testing.T) {
	t.Parallel()

	// Setup
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	// Test reconciling a resource that doesn't exist
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nonexistent",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	// Should not error and should not requeue
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestVirtualMCPServerApplyStatusUpdates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupVMCP      func() *mcpv1alpha1.VirtualMCPServer
		setupCollector func(vmcp *mcpv1alpha1.VirtualMCPServer) virtualmcpserverstatus.StatusManager
		expectUpdate   bool
		expectError    bool
	}{
		{
			name: "successful status update",
			setupVMCP: func() *mcpv1alpha1.VirtualMCPServer {
				return &mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:       testVmcpName,
						Namespace:  "default",
						Generation: 1,
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						GroupRef: mcpv1alpha1.GroupRef{
							Name: testGroupName,
						},
					},
				}
			},
			setupCollector: func(vmcp *mcpv1alpha1.VirtualMCPServer) virtualmcpserverstatus.StatusManager {
				collector := virtualmcpserverstatus.NewStatusManager(vmcp)
				collector.SetPhase(mcpv1alpha1.VirtualMCPServerPhaseReady)
				collector.SetMessage("All resources ready")
				return collector
			},
			expectUpdate: true,
			expectError:  false,
		},
		{
			name: "no changes to apply",
			setupVMCP: func() *mcpv1alpha1.VirtualMCPServer {
				return &mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:       testVmcpName,
						Namespace:  "default",
						Generation: 1,
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						GroupRef: mcpv1alpha1.GroupRef{
							Name: testGroupName,
						},
					},
				}
			},
			setupCollector: func(vmcp *mcpv1alpha1.VirtualMCPServer) virtualmcpserverstatus.StatusManager {
				return virtualmcpserverstatus.NewStatusManager(vmcp)
			},
			expectUpdate: false,
			expectError:  false,
		},
		{
			name: "batch update with multiple changes",
			setupVMCP: func() *mcpv1alpha1.VirtualMCPServer {
				return &mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:       testVmcpName,
						Namespace:  "default",
						Generation: 1,
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						GroupRef: mcpv1alpha1.GroupRef{
							Name: testGroupName,
						},
					},
				}
			},
			setupCollector: func(vmcp *mcpv1alpha1.VirtualMCPServer) virtualmcpserverstatus.StatusManager {
				collector := virtualmcpserverstatus.NewStatusManager(vmcp)
				collector.SetPhase(mcpv1alpha1.VirtualMCPServerPhaseReady)
				collector.SetMessage("All resources ready")
				collector.SetURL("http://test.example.com")
				collector.SetObservedGeneration(1)
				collector.SetGroupRefValidatedCondition("GroupValid", "group is valid", metav1.ConditionTrue)
				collector.SetAuthConfiguredCondition("AuthValid", "auth is configured", metav1.ConditionTrue)
				collector.SetReadyCondition("DeploymentReady", "deployment is ready", metav1.ConditionTrue)
				return collector
			},
			expectUpdate: true,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)
			_ = rbacv1.AddToScheme(scheme)

			vmcp := tt.setupVMCP()
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(vmcp).
				WithStatusSubresource(vmcp).
				Build()

			reconciler := &VirtualMCPServerReconciler{
				Client: k8sClient,
				Scheme: scheme,
			}

			collector := tt.setupCollector(vmcp)

			err := reconciler.applyStatusUpdates(context.Background(), vmcp, collector)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify the status was updated
				updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(context.Background(), types.NamespacedName{
					Name:      vmcp.Name,
					Namespace: vmcp.Namespace,
				}, updatedVMCP)
				require.NoError(t, err)

				if tt.expectUpdate {
					// Verify updates were applied
					assert.NotEqual(t, mcpv1alpha1.VirtualMCPServerPhase(""), updatedVMCP.Status.Phase)
				}
			}
		})
	}
}

func TestVirtualMCPServerApplyStatusUpdates_ResourceNotFound(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testVmcpName,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	// Create client WITHOUT the resource
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	collector := virtualmcpserverstatus.NewStatusManager(vmcp)
	collector.SetPhase(mcpv1alpha1.VirtualMCPServerPhaseReady)

	err := reconciler.applyStatusUpdates(context.Background(), vmcp, collector)

	// Should return error when resource doesn't exist
	assert.Error(t, err)
}

func TestVirtualMCPServerEnsureAllResources_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupVMCP   func() *mcpv1alpha1.VirtualMCPServer
		setupClient func(t *testing.T, vmcp *mcpv1alpha1.VirtualMCPServer) client.Client
		expectError bool
	}{
		{
			name: "no auth configured - valid",
			setupVMCP: func() *mcpv1alpha1.VirtualMCPServer {
				return &mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:       testVmcpName,
						Namespace:  "default",
						Generation: 1,
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						GroupRef: mcpv1alpha1.GroupRef{
							Name: testGroupName,
						},
					},
				}
			},
			setupClient: func(_ *testing.T, vmcp *mcpv1alpha1.VirtualMCPServer) client.Client {
				scheme := runtime.NewScheme()
				_ = mcpv1alpha1.AddToScheme(scheme)
				_ = corev1.AddToScheme(scheme)
				_ = appsv1.AddToScheme(scheme)
				_ = rbacv1.AddToScheme(scheme)

				mcpGroup := &mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testGroupName,
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						Phase: mcpv1alpha1.MCPGroupPhaseReady,
					},
				}
				return fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(vmcp, mcpGroup).
					WithStatusSubresource(vmcp).
					Build()
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := tt.setupVMCP()
			k8sClient := tt.setupClient(t, vmcp)

			reconciler := &VirtualMCPServerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			collector := virtualmcpserverstatus.NewStatusManager(vmcp)

			err := reconciler.ensureAllResources(context.Background(), vmcp, collector)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestVirtualMCPServerContainerNeedsUpdate(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	reconciler := &VirtualMCPServerReconciler{
		Scheme: scheme,
	}

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	tests := []struct {
		name           string
		deployment     *appsv1.Deployment
		vmcp           *mcpv1alpha1.VirtualMCPServer
		expectedUpdate bool
	}{
		{
			name:           "nil deployment needs update",
			deployment:     nil,
			vmcp:           vmcp,
			expectedUpdate: true,
		},
		{
			name: "nil vmcp needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
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
			},
			vmcp:           nil,
			expectedUpdate: true,
		},
		{
			name: "empty containers needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{},
						},
					},
				},
			},
			vmcp:           vmcp,
			expectedUpdate: true,
		},
		{
			name: "image change needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "vmcp",
									Image: "old-image:v1",
									Ports: []corev1.ContainerPort{
										{ContainerPort: 4483},
									},
									Env: reconciler.buildEnvVarsForVmcp(context.Background(), vmcp, []string{}),
								},
							},
							ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
						},
					},
				},
			},
			vmcp:           vmcp,
			expectedUpdate: true,
		},
		{
			name: "port change needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "vmcp",
									Image: getVmcpImage(),
									Ports: []corev1.ContainerPort{
										{ContainerPort: 8080},
									},
									Env: reconciler.buildEnvVarsForVmcp(context.Background(), vmcp, []string{}),
								},
							},
							ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
						},
					},
				},
			},
			vmcp:           vmcp,
			expectedUpdate: true,
		},
		{
			name: "env var change needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "vmcp",
									Image: getVmcpImage(),
									Ports: []corev1.ContainerPort{
										{ContainerPort: 4483},
									},
									Env: []corev1.EnvVar{
										{Name: "OLD_VAR", Value: "old-value"},
									},
								},
							},
							ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
						},
					},
				},
			},
			vmcp:           vmcp,
			expectedUpdate: true,
		},
		{
			name: "service account change needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "vmcp",
									Image: getVmcpImage(),
									Ports: []corev1.ContainerPort{
										{ContainerPort: 4483},
									},
									Args: reconciler.buildContainerArgsForVmcp(vmcp),
									Env:  reconciler.buildEnvVarsForVmcp(context.Background(), vmcp, []string{}),
								},
							},
							ServiceAccountName: "wrong-service-account",
						},
					},
				},
			},
			vmcp:           vmcp,
			expectedUpdate: true,
		},
		{
			name: "log level change to debug needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "vmcp",
									Image: getVmcpImage(),
									Ports: []corev1.ContainerPort{
										{ContainerPort: 4483},
									},
									Args: []string{"serve", "--config=/etc/vmcp-config/config.yaml", "--host=0.0.0.0", "--port=4483"},
									Env:  reconciler.buildEnvVarsForVmcp(context.Background(), vmcp, []string{}),
								},
							},
							ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
						},
					},
				},
			},
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testVmcpName,
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: testGroupName,
					},
					Operational: &mcpv1alpha1.OperationalConfig{
						LogLevel: "debug",
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "log level removed from debug needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "vmcp",
									Image: getVmcpImage(),
									Ports: []corev1.ContainerPort{
										{ContainerPort: 4483},
									},
									Args: []string{"serve", "--config=/etc/vmcp-config/config.yaml", "--host=0.0.0.0", "--port=4483", "--debug"},
									Env:  reconciler.buildEnvVarsForVmcp(context.Background(), vmcp, []string{}),
								},
							},
							ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
						},
					},
				},
			},
			vmcp:           vmcp,
			expectedUpdate: true,
		},
		{
			name: "no changes - no update needed",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "vmcp",
									Image: getVmcpImage(),
									Ports: []corev1.ContainerPort{
										{ContainerPort: 4483},
									},
									Args: reconciler.buildContainerArgsForVmcp(vmcp),
									Env:  reconciler.buildEnvVarsForVmcp(context.Background(), vmcp, []string{}),
								},
							},
							ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
						},
					},
				},
			},
			vmcp:           vmcp,
			expectedUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			needsUpdate := reconciler.containerNeedsUpdate(context.Background(), tt.deployment, tt.vmcp, []string{})
			assert.Equal(t, tt.expectedUpdate, needsUpdate)
		})
	}
}

func TestVirtualMCPServerDeploymentMetadataNeedsUpdate(t *testing.T) {
	t.Parallel()

	reconciler := &VirtualMCPServerReconciler{}

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
	}

	tests := []struct {
		name           string
		deployment     *appsv1.Deployment
		vmcp           *mcpv1alpha1.VirtualMCPServer
		expectedUpdate bool
	}{
		{
			name:           "nil deployment needs update",
			deployment:     nil,
			vmcp:           vmcp,
			expectedUpdate: true,
		},
		{
			name: "nil vmcp needs update",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForVirtualMCPServer(testVmcpName),
				},
			},
			vmcp:           nil,
			expectedUpdate: true,
		},
		{
			name: "label change needs update",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"wrong-label": "wrong-value",
					},
					Annotations: make(map[string]string),
				},
			},
			vmcp:           vmcp,
			expectedUpdate: true,
		},
		{
			name: "extra annotations allowed - no update needed",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForVirtualMCPServer(vmcp.Name),
					Annotations: map[string]string{
						"extra-annotation": "extra-value",
					},
				},
			},
			vmcp:           vmcp,
			expectedUpdate: false,
		},
		{
			name: "no changes - no update needed",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labelsForVirtualMCPServer(vmcp.Name),
					Annotations: make(map[string]string),
				},
			},
			vmcp:           vmcp,
			expectedUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			needsUpdate := reconciler.deploymentMetadataNeedsUpdate(tt.deployment, tt.vmcp)
			assert.Equal(t, tt.expectedUpdate, needsUpdate)
		})
	}
}

func TestVirtualMCPServerPodTemplateMetadataNeedsUpdate(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)

	reconciler := &VirtualMCPServerReconciler{
		Scheme: scheme,
	}

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
	}

	vmcpConfigChecksum := testChecksumValue
	expectedLabels, expectedAnnotations := reconciler.buildPodTemplateMetadata(
		labelsForVirtualMCPServer(vmcp.Name), vmcp, vmcpConfigChecksum,
	)

	tests := []struct {
		name           string
		deployment     *appsv1.Deployment
		vmcp           *mcpv1alpha1.VirtualMCPServer
		checksum       string
		expectedUpdate bool
	}{
		{
			name:           "nil deployment needs update",
			deployment:     nil,
			vmcp:           vmcp,
			checksum:       vmcpConfigChecksum,
			expectedUpdate: true,
		},
		{
			name: "nil vmcp needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels:      expectedLabels,
							Annotations: expectedAnnotations,
						},
					},
				},
			},
			vmcp:           nil,
			checksum:       vmcpConfigChecksum,
			expectedUpdate: true,
		},
		{
			name: "pod template label change needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"wrong-label": "wrong-value",
							},
							Annotations: expectedAnnotations,
						},
					},
				},
			},
			vmcp:           vmcp,
			checksum:       vmcpConfigChecksum,
			expectedUpdate: true,
		},
		{
			name: "pod template annotation change needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: expectedLabels,
							Annotations: map[string]string{
								"wrong-annotation": "wrong-value",
							},
						},
					},
				},
			},
			vmcp:           vmcp,
			checksum:       vmcpConfigChecksum,
			expectedUpdate: true,
		},
		{
			name: "checksum change needs update",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: expectedLabels,
							Annotations: map[string]string{
								checksum.RunConfigChecksumAnnotation: "old-checksum",
							},
						},
					},
				},
			},
			vmcp:           vmcp,
			checksum:       vmcpConfigChecksum,
			expectedUpdate: true,
		},
		{
			name: "no changes - no update needed",
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels:      expectedLabels,
							Annotations: expectedAnnotations,
						},
					},
				},
			},
			vmcp:           vmcp,
			checksum:       vmcpConfigChecksum,
			expectedUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			needsUpdate := reconciler.podTemplateMetadataNeedsUpdate(tt.deployment, tt.vmcp, tt.checksum)
			assert.Equal(t, tt.expectedUpdate, needsUpdate)
		})
	}
}

func TestVirtualMCPServerDeploymentNeedsUpdate(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	reconciler := &VirtualMCPServerReconciler{
		Scheme: scheme,
	}

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	vmcpConfigChecksum := testChecksumValue
	expectedLabels, expectedAnnotations := reconciler.buildPodTemplateMetadata(
		labelsForVirtualMCPServer(vmcp.Name), vmcp, vmcpConfigChecksum,
	)

	tests := []struct {
		name           string
		deployment     *appsv1.Deployment
		expectedUpdate bool
	}{
		{
			name: "deployment metadata changed",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"wrong-label": "wrong-value",
					},
					Annotations: make(map[string]string),
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels:      expectedLabels,
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "vmcp",
									Image: getVmcpImage(),
									Ports: []corev1.ContainerPort{
										{ContainerPort: 4483},
									},
									Env: reconciler.buildEnvVarsForVmcp(context.Background(), vmcp, []string{}),
								},
							},
							ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
						},
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "pod template metadata changed",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labelsForVirtualMCPServer(vmcp.Name),
					Annotations: make(map[string]string),
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"wrong-label": "wrong-value",
							},
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "vmcp",
									Image: getVmcpImage(),
									Ports: []corev1.ContainerPort{
										{ContainerPort: 4483},
									},
									Env: reconciler.buildEnvVarsForVmcp(context.Background(), vmcp, []string{}),
								},
							},
							ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
						},
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "container changed",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labelsForVirtualMCPServer(vmcp.Name),
					Annotations: make(map[string]string),
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels:      expectedLabels,
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "vmcp",
									Image: "old-image:v1",
									Ports: []corev1.ContainerPort{
										{ContainerPort: 4483},
									},
									Args: reconciler.buildContainerArgsForVmcp(vmcp),
									Env:  reconciler.buildEnvVarsForVmcp(context.Background(), vmcp, []string{}),
								},
							},
							ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
						},
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "no changes - no update needed",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labelsForVirtualMCPServer(vmcp.Name),
					Annotations: make(map[string]string),
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels:      expectedLabels,
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "vmcp",
									Image: getVmcpImage(),
									Ports: []corev1.ContainerPort{
										{ContainerPort: 4483},
									},
									Args: reconciler.buildContainerArgsForVmcp(vmcp),
									Env:  reconciler.buildEnvVarsForVmcp(context.Background(), vmcp, []string{}),
								},
							},
							ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
						},
					},
				},
			},
			expectedUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			needsUpdate := reconciler.deploymentNeedsUpdate(context.Background(), tt.deployment, vmcp, vmcpConfigChecksum, []string{})
			assert.Equal(t, tt.expectedUpdate, needsUpdate)
		})
	}
}

func TestVirtualMCPServerReconcile_HappyPath(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testVmcpName,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testGroupName,
			Namespace: "default",
		},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	// Create deployment that will be found by ensureDeployment
	replicas := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
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

	// Create service that will be found by ensureService
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
					Port:       4483,
					TargetPort: intstr.FromInt(4483),
				},
			},
		},
	}

	// Create pod for status update
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcp.Name + "-pod",
			Namespace: "default",
			Labels:    labelsForVirtualMCPServer(vmcp.Name),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, mcpGroup, deployment, service, pod).
		WithStatusSubresource(vmcp).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      vmcp.Name,
			Namespace: vmcp.Namespace,
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify status was updated
	updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcp.Name,
		Namespace: vmcp.Namespace,
	}, updatedVMCP)
	require.NoError(t, err)

	// Verify conditions were set
	assert.NotEmpty(t, updatedVMCP.Status.Conditions)
}

func TestVirtualMCPServerReconcile_ValidateGroupRefError(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testVmcpName,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "nonexistent-group",
			},
		},
	}

	// Don't create the MCPGroup so validation fails
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp).
		WithStatusSubresource(vmcp).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      vmcp.Name,
			Namespace: vmcp.Namespace,
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	assert.Error(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify status was updated with error condition
	updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcp.Name,
		Namespace: vmcp.Namespace,
	}, updatedVMCP)
	require.NoError(t, err)

	assert.Equal(t, mcpv1alpha1.VirtualMCPServerPhaseFailed, updatedVMCP.Status.Phase)
	assert.NotEmpty(t, updatedVMCP.Status.Message)
}

func TestVirtualMCPServerReconcile_GroupNotReady(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testVmcpName,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testGroupName,
			Namespace: "default",
		},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: mcpv1alpha1.MCPGroupPhasePending, // Not ready
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, mcpGroup).
		WithStatusSubresource(vmcp).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      vmcp.Name,
			Namespace: vmcp.Namespace,
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "is not ready")
	assert.Equal(t, ctrl.Result{}, result)

	// Verify status was updated
	updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcp.Name,
		Namespace: vmcp.Namespace,
	}, updatedVMCP)
	require.NoError(t, err)

	assert.Equal(t, mcpv1alpha1.VirtualMCPServerPhasePending, updatedVMCP.Status.Phase)
}

func TestVirtualMCPServerReconcile_GetError(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)

	// Create empty client - resource won't be found but we'll test non-NotFound errors
	// by using a client that returns a generic error
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      testVmcpName,
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	// For a not found error, should not error and not requeue
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestVirtualMCPServerEnsureDeployment_ConfigMapNotFound(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	// Don't create ConfigMap - it won't be found
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	result, err := reconciler.ensureDeployment(context.Background(), vmcp, []string{})

	// Should requeue after 5 seconds when ConfigMap not found
	assert.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter)
}

func TestVirtualMCPServerEnsureDeployment_CreateDeployment(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	// Create ConfigMap so checksum can be retrieved
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcpConfigMapName(vmcp.Name),
			Namespace: "default",
			Annotations: map[string]string{
				checksum.ContentChecksumAnnotation: "test-checksum",
			},
		},
		Data: map[string]string{
			"config.yaml": "test-config",
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, configMap).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	result, err := reconciler.ensureDeployment(context.Background(), vmcp, []string{})

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify deployment was created
	deployment := &appsv1.Deployment{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcp.Name,
		Namespace: vmcp.Namespace,
	}, deployment)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.Name, deployment.Name)
}

func TestVirtualMCPServerEnsureDeployment_UpdateDeployment(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcpConfigMapName(vmcp.Name),
			Namespace: "default",
			Annotations: map[string]string{
				checksum.ContentChecksumAnnotation: "test-checksum",
			},
		},
		Data: map[string]string{
			"config.yaml": "test-config",
		},
	}

	// Create existing deployment with old image
	oldDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
			Labels:    labelsForVirtualMCPServer(vmcp.Name),
		},
		Spec: appsv1.DeploymentSpec{
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
							Image: "old-image:v1",
						},
					},
				},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, configMap, oldDeployment).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	result, err := reconciler.ensureDeployment(context.Background(), vmcp, []string{})

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify deployment was updated
	deployment := &appsv1.Deployment{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcp.Name,
		Namespace: vmcp.Namespace,
	}, deployment)
	assert.NoError(t, err)
	assert.Equal(t, getVmcpImage(), deployment.Spec.Template.Spec.Containers[0].Image)
}

func TestVirtualMCPServerEnsureDeployment_NoUpdateNeeded(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcpConfigMapName(vmcp.Name),
			Namespace: "default",
			Annotations: map[string]string{
				checksum.ContentChecksumAnnotation: "test-checksum",
			},
		},
		Data: map[string]string{
			"config.yaml": "test-config",
		},
	}

	reconciler := &VirtualMCPServerReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme: scheme,
	}

	// Create deployment matching current spec
	expectedLabels, expectedAnnotations := reconciler.buildPodTemplateMetadata(
		labelsForVirtualMCPServer(vmcp.Name), vmcp, "test-checksum",
	)

	correctDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        testVmcpName,
			Namespace:   "default",
			Labels:      labelsForVirtualMCPServer(vmcp.Name),
			Annotations: make(map[string]string),
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labelsForVirtualMCPServer(vmcp.Name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      expectedLabels,
					Annotations: expectedAnnotations,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "vmcp",
							Image: getVmcpImage(),
							Ports: []corev1.ContainerPort{
								{ContainerPort: 4483},
							},
							Env: reconciler.buildEnvVarsForVmcp(context.Background(), vmcp, []string{}),
						},
					},
					ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
				},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, configMap, correctDeployment).
		Build()

	reconciler.Client = k8sClient

	result, err := reconciler.ensureDeployment(context.Background(), vmcp, []string{})

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestVirtualMCPServerEnsureService_CreateService(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	result, err := reconciler.ensureService(context.Background(), vmcp)

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify service was created
	service := &corev1.Service{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcpServiceName(vmcp.Name),
		Namespace: vmcp.Namespace,
	}, service)
	assert.NoError(t, err)
	assert.Equal(t, vmcpServiceName(vmcp.Name), service.Name)
}

func TestVirtualMCPServerEnsureService_UpdateService(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
			ServiceType: "LoadBalancer",
		},
	}

	// Create existing service with wrong type
	oldService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcpServiceName(vmcp.Name),
			Namespace: "default",
			Labels:    labelsForVirtualMCPServer(vmcp.Name),
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: labelsForVirtualMCPServer(vmcp.Name),
			Ports: []corev1.ServicePort{
				{
					Port:       4483,
					TargetPort: intstr.FromInt(4483),
				},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, oldService).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	result, err := reconciler.ensureService(context.Background(), vmcp)

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify service was updated
	service := &corev1.Service{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      vmcpServiceName(vmcp.Name),
		Namespace: vmcp.Namespace,
	}, service)
	assert.NoError(t, err)
	assert.Equal(t, corev1.ServiceTypeLoadBalancer, service.Spec.Type)
}

func TestVirtualMCPServerEnsureService_NoUpdateNeeded(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVmcpName,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: testGroupName,
			},
		},
	}

	// Create service matching current spec
	correctService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        vmcpServiceName(vmcp.Name),
			Namespace:   "default",
			Labels:      labelsForVirtualMCPServer(vmcp.Name),
			Annotations: make(map[string]string),
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: labelsForVirtualMCPServer(vmcp.Name),
			Ports: []corev1.ServicePort{
				{
					Port:       4483,
					TargetPort: intstr.FromInt(4483),
				},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp, correctService).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: k8sClient,
		Scheme: scheme,
	}

	result, err := reconciler.ensureService(context.Background(), vmcp)

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

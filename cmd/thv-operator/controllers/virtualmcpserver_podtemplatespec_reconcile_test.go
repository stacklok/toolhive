package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// TestVirtualMCPServerReconcile_WithPodTemplateSpec tests are complex and require envtest
// The key functionality is tested in:
// - TestVirtualMCPServerPodTemplateSpecBuilder: builder validation
// - TestVirtualMCPServerPodTemplateSpecValidation: parsing validation
// - TestVirtualMCPServerPodTemplateSpecNeedsUpdate: change detection
// - TestVirtualMCPServerPodTemplateSpecDeterministic: deterministic generation
// - Integration tests in test/e2e/

func TestVirtualMCPServerReconcile_WithValidPodTemplateSpec(t *testing.T) {
	t.Parallel()
	t.Skip("Complex reconciliation tests require envtest - see TestVirtualMCPServerPodTemplateSpec* for unit tests")
}

// TestVirtualMCPServerPodTemplateSpecDeterministic verifies that generating a deployment
// twice with the same PodTemplateSpec produces identical results (no spurious updates)
func TestVirtualMCPServerPodTemplateSpecDeterministic(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	namespace := "test-namespace"
	vmcpName := "test-vmcp"
	groupName := "test-group"

	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      groupName,
			Namespace: namespace,
		},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	podTemplate := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{"disktype": "ssd"},
		},
	}

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcpName,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: groupName,
			},
			PodTemplateSpec: podTemplateSpecToRawExtension(t, podTemplate),
		},
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcpConfigMapName(vmcpName),
			Namespace: namespace,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpGroup, vmcp, configMap).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Generate deployment twice with same input
	dep1 := reconciler.deploymentForVirtualMCPServer(context.Background(), vmcp, "test-checksum")
	dep2 := reconciler.deploymentForVirtualMCPServer(context.Background(), vmcp, "test-checksum")

	// Both should be non-nil
	assert.NotNil(t, dep1, "First deployment should not be nil")
	assert.NotNil(t, dep2, "Second deployment should not be nil")

	// Compare their PodTemplateSpecs
	json1, err1 := json.Marshal(dep1.Spec.Template)
	json2, err2 := json.Marshal(dep2.Spec.Template)

	assert.NoError(t, err1, "Should marshal first deployment")
	assert.NoError(t, err2, "Should marshal second deployment")

	assert.Equal(t, string(json1), string(json2),
		"Generating deployment twice with same PodTemplateSpec should produce identical results")
}

func TestVirtualMCPServerDeploymentUpdate_WithPodTemplateSpecChange(t *testing.T) {
	t.Parallel()
	t.Skip("Complex reconciliation tests require envtest - see TestVirtualMCPServerPodTemplateSpecNeedsUpdate for change detection tests")
}

func TestVirtualMCPServerPodTemplateSpecNeedsUpdate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		existingPodTemplate corev1.PodTemplateSpec
		newPodTemplateSpec  *runtime.RawExtension
		expectUpdate        bool
	}{
		{
			name: "node selector changed - update needed",
			existingPodTemplate: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{"disktype": "ssd"},
				},
			},
			newPodTemplateSpec: podTemplateSpecToRawExtension(t, &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{"disktype": "nvme"},
				},
			}),
			expectUpdate: true,
		},
		{
			name: "priority class added - update needed",
			existingPodTemplate: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{"disktype": "ssd"},
				},
			},
			newPodTemplateSpec: podTemplateSpecToRawExtension(t, &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector:      map[string]string{"disktype": "ssd"},
					PriorityClassName: "high-priority",
				},
			}),
			expectUpdate: true,
		},
		{
			name: "no PodTemplateSpec - no update needed",
			existingPodTemplate: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{},
			},
			newPodTemplateSpec: nil,
			expectUpdate:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create scheme
			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)

			namespace := "test-namespace"
			vmcpName := "test-vmcp"
			groupName := "test-group"

			// Create MCPGroup (required for deployment generation)
			mcpGroup := &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      groupName,
					Namespace: namespace,
				},
				Status: mcpv1alpha1.MCPGroupStatus{
					Phase: mcpv1alpha1.MCPGroupPhaseReady,
				},
			}

			// Create VirtualMCPServer with initial PodTemplateSpec
			initialVmcp := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: groupName,
					},
					PodTemplateSpec: podTemplateSpecToRawExtension(t, &tt.existingPodTemplate),
				},
			}

			// Create configmap for checksum
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpConfigMapName(vmcpName),
					Namespace: namespace,
					Annotations: map[string]string{
						"toolhive.stacklok.dev/runconfig-checksum": "test-checksum",
					},
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(mcpGroup, initialVmcp, configMap).
				Build()

			reconciler := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Generate existing deployment using the reconciler (this ensures we have a real deployment structure)
			existingDeployment := reconciler.deploymentForVirtualMCPServer(context.Background(), initialVmcp, "test-checksum")
			assert.NotNil(t, existingDeployment, "Should generate existing deployment")

			// Create VirtualMCPServer with new PodTemplateSpec
			newVmcp := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: groupName,
					},
					PodTemplateSpec: tt.newPodTemplateSpec,
				},
			}

			// Check if update is needed
			needsUpdate := reconciler.podTemplateSpecNeedsUpdate(context.Background(), existingDeployment, newVmcp)
			assert.Equal(t, tt.expectUpdate, needsUpdate,
				"PodTemplateSpec update detection should match expected value")
		})
	}
}

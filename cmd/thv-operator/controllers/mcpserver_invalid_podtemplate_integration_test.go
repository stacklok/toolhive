package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestMCPServerReconciler_InvalidPodTemplateSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		mcpServer             *mcpv1alpha1.MCPServer
		expectConditionStatus metav1.ConditionStatus
		expectConditionReason string
		expectEventReason     string
	}{
		{
			name: "invalid_json_in_podtemplatespec",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid-json",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test-image:latest",
					Transport: "stdio",
					Port:      8080,
					PodTemplateSpec: &runtime.RawExtension{
						// Valid JSON but invalid PodTemplateSpec structure
						// (spec.containers should be an array, not a string)
						Raw: []byte(`{"spec": {"containers": "invalid"}}`),
					},
				},
			},
			expectConditionStatus: metav1.ConditionFalse,
			expectConditionReason: "InvalidPodTemplateSpec",
			expectEventReason:     "InvalidPodTemplateSpec",
		},
		{
			name: "valid_podtemplatespec",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-valid",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test-image:latest",
					Transport: "stdio",
					Port:      8080,
					PodTemplateSpec: &runtime.RawExtension{
						Raw: []byte(`{"spec": {"containers": [{"name": "mcp"}]}}`),
					},
				},
			},
			expectConditionStatus: metav1.ConditionTrue,
			expectConditionReason: "ValidPodTemplateSpec",
			expectEventReason:     "", // No warning event for valid spec
		},
		{
			name: "nil_podtemplatespec",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nil",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:           "test-image:latest",
					Transport:       "stdio",
					Port:            8080,
					PodTemplateSpec: nil,
				},
			},
			expectConditionStatus: "", // No condition set for nil spec
			expectConditionReason: "", // No condition set for nil spec
			expectEventReason:     "", // No warning event for nil spec
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			// Setup the test environment for each test to avoid race conditions
			s := runtime.NewScheme()
			require.NoError(t, scheme.AddToScheme(s))
			require.NoError(t, mcpv1alpha1.AddToScheme(s))

			// Create a fake event recorder for each test
			eventRecorder := record.NewFakeRecorder(10)

			// Create a fake client with the MCPServer
			fakeClient := fake.NewClientBuilder().
				WithScheme(s).
				WithObjects(tt.mcpServer).
				WithStatusSubresource(tt.mcpServer).
				Build()

			// Create the reconciler with the fake event recorder
			r := &MCPServerReconciler{
				Client:   fakeClient,
				Scheme:   s,
				Recorder: eventRecorder,
			}

			// Run reconciliation
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.mcpServer.Name,
					Namespace: tt.mcpServer.Namespace,
				},
			}

			// Set a logger for the context
			ctx = log.IntoContext(ctx, log.Log)

			// Reconcile
			_, err := r.Reconcile(ctx, req)
			// We expect the reconciliation to succeed (no error) even with invalid PodTemplateSpec
			// to avoid infinite retries. The deployment should not be created though.
			require.NoError(t, err)

			// Check the MCPServer status conditions
			var updatedMCPServer mcpv1alpha1.MCPServer
			err = fakeClient.Get(ctx, client.ObjectKeyFromObject(tt.mcpServer), &updatedMCPServer)
			require.NoError(t, err)

			// Find the PodTemplateValid condition
			condition := meta.FindStatusCondition(updatedMCPServer.Status.Conditions, "PodTemplateValid")
			if tt.expectConditionStatus != "" {
				require.NotNil(t, condition, "PodTemplateValid condition should be set")
				assert.Equal(t, tt.expectConditionStatus, condition.Status)
				assert.Equal(t, tt.expectConditionReason, condition.Reason)

				if tt.expectConditionStatus == metav1.ConditionFalse {
					assert.Contains(t, condition.Message, "Failed to parse PodTemplateSpec")
					assert.Contains(t, condition.Message, "Deployment blocked until fixed")
				}
			}

			// Check for events
			if tt.expectEventReason != "" {
				// Give the event recorder a moment to process
				time.Sleep(10 * time.Millisecond)

				select {
				case event := <-eventRecorder.Events:
					assert.Contains(t, event, tt.expectEventReason)
					assert.Contains(t, event, "Warning")
					assert.Contains(t, event, "Failed to parse PodTemplateSpec")
				case <-time.After(100 * time.Millisecond):
					if tt.expectEventReason != "" {
						t.Errorf("Expected event with reason %s but no event was recorded", tt.expectEventReason)
					}
				}
			}
		})
	}
}

func TestDeploymentArgsWithInvalidPodTemplateSpec(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s := runtime.NewScheme()
	require.NoError(t, scheme.AddToScheme(s))
	require.NoError(t, mcpv1alpha1.AddToScheme(s))

	// MCPServer with invalid PodTemplateSpec
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			Port:      8080,
			PodTemplateSpec: &runtime.RawExtension{
				Raw: []byte(`{invalid json`),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(mcpServer).
		Build()

	r := &MCPServerReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(10),
	}

	// Set a logger for the context
	ctx = log.IntoContext(ctx, log.Log)

	// Call deploymentForMCPServer to check that it handles invalid PodTemplateSpec gracefully
	deployment := r.deploymentForMCPServer(ctx, mcpServer)

	// Check that the deployment was created successfully
	require.NotNil(t, deployment)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)

	// Check that the --k8s-pod-patch argument is NOT present due to invalid spec
	container := deployment.Spec.Template.Spec.Containers[0]
	for _, arg := range container.Args {
		assert.NotContains(t, arg, "--k8s-pod-patch", "Pod patch should not be present with invalid PodTemplateSpec")
	}

	// The deployment should still have the basic required arguments
	assert.Contains(t, container.Args, "test-image:latest")
	assert.Contains(t, container.Args, "--proxy-port=8080")
	assert.Contains(t, container.Args, "--transport=stdio")
}

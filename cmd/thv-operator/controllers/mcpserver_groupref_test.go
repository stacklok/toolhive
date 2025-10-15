package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// TestMCPServerReconciler_ValidateGroupRef tests the validateGroupRef function
func TestMCPServerReconciler_ValidateGroupRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		mcpServer               *mcpv1alpha1.MCPServer
		mcpGroups               []*mcpv1alpha1.MCPGroup
		expectedConditionStatus metav1.ConditionStatus
		expectedConditionReason string
		expectedConditionMsg    string
	}{
		{
			name: "GroupRef validated when group exists and is Ready",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "test-image",
					GroupRef: "test-group",
				},
			},
			mcpGroups: []*mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						Phase: mcpv1alpha1.MCPGroupPhaseReady,
					},
				},
			},
			expectedConditionStatus: metav1.ConditionTrue,
			expectedConditionReason: "",
		},
		{
			name: "GroupRef not validated when group does not exist",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "test-image",
					GroupRef: "non-existent-group",
				},
			},
			mcpGroups:               []*mcpv1alpha1.MCPGroup{},
			expectedConditionStatus: metav1.ConditionFalse,
			expectedConditionReason: mcpv1alpha1.ConditionReasonGroupRefNotFound,
		},
		{
			name: "GroupRef not validated when group is Pending",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "test-image",
					GroupRef: "test-group",
				},
			},
			mcpGroups: []*mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						Phase: mcpv1alpha1.MCPGroupPhasePending,
					},
				},
			},
			expectedConditionStatus: metav1.ConditionFalse,
			expectedConditionReason: mcpv1alpha1.ConditionReasonGroupRefNotReady,
			expectedConditionMsg:    "GroupRef is not in Ready state",
		},
		{
			name: "GroupRef not validated when group is Failed",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "test-image",
					GroupRef: "test-group",
				},
			},
			mcpGroups: []*mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						Phase: mcpv1alpha1.MCPGroupPhaseFailed,
					},
				},
			},
			expectedConditionStatus: metav1.ConditionFalse,
			expectedConditionReason: mcpv1alpha1.ConditionReasonGroupRefNotReady,
			expectedConditionMsg:    "GroupRef is not in Ready state",
		},
		{
			name: "No validation when GroupRef is empty",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					// No GroupRef
				},
			},
			mcpGroups: []*mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
					Status: mcpv1alpha1.MCPGroupStatus{
						Phase: mcpv1alpha1.MCPGroupPhaseReady,
					},
				},
			},
			expectedConditionStatus: "", // No condition should be set
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			objs := []client.Object{}
			for _, group := range tt.mcpGroups {
				objs = append(objs, group)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPGroup{}).
				Build()

			r := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			r.validateGroupRef(ctx, tt.mcpServer)

			// Check the condition if we expected one
			if tt.expectedConditionStatus != "" {
				condition := meta.FindStatusCondition(tt.mcpServer.Status.Conditions, mcpv1alpha1.ConditionGroupRefValidated)
				require.NotNil(t, condition, "GroupRefValidated condition should be present")
				assert.Equal(t, tt.expectedConditionStatus, condition.Status)
				if tt.expectedConditionReason != "" {
					assert.Equal(t, tt.expectedConditionReason, condition.Reason)
				}
				if tt.expectedConditionMsg != "" {
					assert.Equal(t, tt.expectedConditionMsg, condition.Message)
				}
			} else {
				// No condition should be set when GroupRef is empty
				condition := meta.FindStatusCondition(tt.mcpServer.Status.Conditions, mcpv1alpha1.ConditionGroupRefValidated)
				assert.Nil(t, condition, "GroupRefValidated condition should not be present when GroupRef is empty")
			}
		})
	}
}

// TestMCPServerReconciler_GroupRefValidation_Integration tests GroupRef validation in the context of reconciliation
func TestMCPServerReconciler_GroupRefValidation_Integration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		mcpServer               *mcpv1alpha1.MCPServer
		mcpGroup                *mcpv1alpha1.MCPGroup
		expectedConditionStatus metav1.ConditionStatus
		expectedConditionReason string
	}{
		{
			name: "Server with valid GroupRef gets validated condition",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "test-image",
					GroupRef: "test-group",
				},
			},
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPGroupStatus{
					Phase: mcpv1alpha1.MCPGroupPhaseReady,
				},
			},
			expectedConditionStatus: metav1.ConditionTrue,
		},
		{
			name: "Server with GroupRef to non-Ready group gets failed condition",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "test-image",
					GroupRef: "test-group",
				},
			},
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
				Status: mcpv1alpha1.MCPGroupStatus{
					Phase: mcpv1alpha1.MCPGroupPhasePending,
				},
			},
			expectedConditionStatus: metav1.ConditionFalse,
			expectedConditionReason: mcpv1alpha1.ConditionReasonGroupRefNotReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			objs := []client.Object{tt.mcpServer}
			if tt.mcpGroup != nil {
				objs = append(objs, tt.mcpGroup)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPServer{}, &mcpv1alpha1.MCPGroup{}).
				Build()

			r := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			r.validateGroupRef(ctx, tt.mcpServer)

			condition := meta.FindStatusCondition(tt.mcpServer.Status.Conditions, mcpv1alpha1.ConditionGroupRefValidated)
			require.NotNil(t, condition, "GroupRefValidated condition should be present")
			assert.Equal(t, tt.expectedConditionStatus, condition.Status)
			if tt.expectedConditionReason != "" {
				assert.Equal(t, tt.expectedConditionReason, condition.Reason)
			}
		})
	}
}

// TestMCPServerReconciler_GroupRefCrossNamespace tests that GroupRef only works within same namespace
func TestMCPServerReconciler_GroupRefCrossNamespace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "namespace-a",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:    "test-image",
			GroupRef: "test-group",
		},
	}

	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "namespace-b", // Different namespace
		},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpServer, mcpGroup).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}, &mcpv1alpha1.MCPGroup{}).
		Build()

	r := &MCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	r.validateGroupRef(ctx, mcpServer)

	// Should fail to find the group because it's in a different namespace
	condition := meta.FindStatusCondition(mcpServer.Status.Conditions, mcpv1alpha1.ConditionGroupRefValidated)
	require.NotNil(t, condition, "GroupRefValidated condition should be present")
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, mcpv1alpha1.ConditionReasonGroupRefNotFound, condition.Reason)
}

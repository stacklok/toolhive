package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// TestMCPGroupReconciler_Reconcile_BasicLogic tests the core reconciliation logic
// using a fake client to avoid needing a real Kubernetes cluster
func TestMCPGroupReconciler_Reconcile_BasicLogic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		mcpGroup            *mcpv1alpha1.MCPGroup
		mcpServers          []*mcpv1alpha1.MCPServer
		expectedServerCount int
		expectedServerNames []string
		expectedPhase       mcpv1alpha1.MCPGroupPhase
	}{
		{
			name: "group with two running servers should be ready",
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
			},
			mcpServers: []*mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:    "test-image",
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:    "test-image",
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
					},
				},
			},
			expectedServerCount: 2,
			expectedServerNames: []string{"server1", "server2"},
			expectedPhase:       mcpv1alpha1.MCPGroupPhaseReady,
		},
		{
			name: "group with servers regardless of status should be ready",
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
			},
			mcpServers: []*mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:    "test-image",
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:    "test-image",
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseFailed,
					},
				},
			},
			expectedServerCount: 2,
			expectedServerNames: []string{"server1", "server2"},
			expectedPhase:       mcpv1alpha1.MCPGroupPhaseReady, // Controller doesn't check individual server phases
		},
		{
			name: "group with mixed server phases should be ready",
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
			},
			mcpServers: []*mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:    "test-image",
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhaseRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:    "test-image",
						GroupRef: "test-group",
					},
					Status: mcpv1alpha1.MCPServerStatus{
						Phase: mcpv1alpha1.MCPServerPhasePending,
					},
				},
			},
			expectedServerCount: 2,
			expectedServerNames: []string{"server1", "server2"},
			expectedPhase:       mcpv1alpha1.MCPGroupPhaseReady, // Controller doesn't check individual server phases
		},
		{
			name: "group with no servers should be ready",
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
			},
			mcpServers:          []*mcpv1alpha1.MCPServer{},
			expectedServerCount: 0,
			expectedServerNames: []string{},
			expectedPhase:       mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			// Create fake client with objects
			objs := []client.Object{tt.mcpGroup}
			for _, server := range tt.mcpServers {
				objs = append(objs, server)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPGroup{}).
				Build()

			r := &MCPGroupReconciler{
				Client: fakeClient,
			}

			// Reconcile
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.mcpGroup.Name,
					Namespace: tt.mcpGroup.Namespace,
				},
			}

			result, err := r.Reconcile(ctx, req)
			require.NoError(t, err)
			assert.False(t, result.Requeue)

			// Check the updated MCPGroup
			var updatedGroup mcpv1alpha1.MCPGroup
			err = fakeClient.Get(ctx, req.NamespacedName, &updatedGroup)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedServerCount, updatedGroup.Status.ServerCount)
			assert.Equal(t, tt.expectedPhase, updatedGroup.Status.Phase)
			assert.ElementsMatch(t, tt.expectedServerNames, updatedGroup.Status.Servers)
		})
	}
}

// TestMCPGroupReconciler_ServerFiltering tests the logic for filtering servers by groupRef
func TestMCPGroupReconciler_ServerFiltering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		groupName           string
		namespace           string
		mcpServers          []*mcpv1alpha1.MCPServer
		expectedServerNames []string
		expectedCount       int
	}{
		{
			name:      "filters servers by exact groupRef match",
			groupName: "test-group",
			namespace: "default",
			mcpServers: []*mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "server1", Namespace: "default"},
					Spec:       mcpv1alpha1.MCPServerSpec{Image: "test", GroupRef: "test-group"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "server2", Namespace: "default"},
					Spec:       mcpv1alpha1.MCPServerSpec{Image: "test", GroupRef: "other-group"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "server3", Namespace: "default"},
					Spec:       mcpv1alpha1.MCPServerSpec{Image: "test", GroupRef: "test-group"},
				},
			},
			expectedServerNames: []string{"server1", "server3"},
			expectedCount:       2,
		},
		{
			name:      "excludes servers without groupRef",
			groupName: "test-group",
			namespace: "default",
			mcpServers: []*mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "server1", Namespace: "default"},
					Spec:       mcpv1alpha1.MCPServerSpec{Image: "test", GroupRef: "test-group"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "server2", Namespace: "default"},
					Spec:       mcpv1alpha1.MCPServerSpec{Image: "test"},
				},
			},
			expectedServerNames: []string{"server1"},
			expectedCount:       1,
		},
		{
			name:      "excludes servers from different namespaces",
			groupName: "test-group",
			namespace: "namespace-a",
			mcpServers: []*mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "server1", Namespace: "namespace-a"},
					Spec:       mcpv1alpha1.MCPServerSpec{Image: "test", GroupRef: "test-group"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "server2", Namespace: "namespace-b"},
					Spec:       mcpv1alpha1.MCPServerSpec{Image: "test", GroupRef: "test-group"},
				},
			},
			expectedServerNames: []string{"server1"},
			expectedCount:       1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			mcpGroup := &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tt.groupName,
					Namespace: tt.namespace,
				},
			}

			objs := []client.Object{mcpGroup}
			for _, server := range tt.mcpServers {
				objs = append(objs, server)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPGroup{}).
				Build()

			r := &MCPGroupReconciler{
				Client: fakeClient,
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.groupName,
					Namespace: tt.namespace,
				},
			}

			result, err := r.Reconcile(ctx, req)
			require.NoError(t, err)
			assert.False(t, result.Requeue)

			var updatedGroup mcpv1alpha1.MCPGroup
			err = fakeClient.Get(ctx, req.NamespacedName, &updatedGroup)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedCount, updatedGroup.Status.ServerCount)
			assert.ElementsMatch(t, tt.expectedServerNames, updatedGroup.Status.Servers)
		})
	}
}

// TestMCPGroupReconciler_findMCPGroupForMCPServer tests the watch mapping function
func TestMCPGroupReconciler_findMCPGroupForMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		mcpServer         *mcpv1alpha1.MCPServer
		mcpGroups         []*mcpv1alpha1.MCPGroup
		expectedRequests  int
		expectedGroupName string
	}{
		{
			name: "server with groupRef finds matching group",
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
				},
			},
			expectedRequests:  1,
			expectedGroupName: "test-group",
		},
		{
			name: "server without groupRef returns empty",
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
				},
			},
			expectedRequests: 0,
		},
		{
			name: "server with non-existent groupRef returns empty",
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
			mcpGroups: []*mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group",
						Namespace: "default",
					},
				},
			},
			expectedRequests: 0,
		},
		{
			name: "server finds correct group among multiple groups",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "test-image",
					GroupRef: "group-b",
				},
			},
			mcpGroups: []*mcpv1alpha1.MCPGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group-a",
						Namespace: "default",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group-b",
						Namespace: "default",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group-c",
						Namespace: "default",
					},
				},
			},
			expectedRequests:  1,
			expectedGroupName: "group-b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			// Create fake client with objects
			objs := []client.Object{}
			for _, group := range tt.mcpGroups {
				objs = append(objs, group)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			r := &MCPGroupReconciler{
				Client: fakeClient,
			}

			requests := r.findMCPGroupForMCPServer(ctx, tt.mcpServer)

			assert.Len(t, requests, tt.expectedRequests)
			if tt.expectedRequests > 0 {
				assert.Equal(t, tt.expectedGroupName, requests[0].Name)
				assert.Equal(t, tt.mcpServer.Namespace, requests[0].Namespace)
			}
		})
	}
}

// TestMCPGroupReconciler_GroupNotFound tests handling of non-existent groups
func TestMCPGroupReconciler_GroupNotFound(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &MCPGroupReconciler{
		Client: fakeClient,
	}

	// Reconcile a non-existent group
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent-group",
			Namespace: "default",
		},
	}

	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.False(t, result.Requeue)
}

// TestMCPGroupReconciler_Conditions tests the MCPServersChecked condition
func TestMCPGroupReconciler_Conditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		mcpGroup                *mcpv1alpha1.MCPGroup
		mcpServers              []*mcpv1alpha1.MCPServer
		expectedConditionStatus metav1.ConditionStatus
		expectedConditionReason string
		expectedPhase           mcpv1alpha1.MCPGroupPhase
	}{
		{
			name: "MCPServersChecked condition is True when listing succeeds",
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
			},
			mcpServers: []*mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:    "test-image",
						GroupRef: "test-group",
					},
				},
			},
			expectedConditionStatus: metav1.ConditionTrue,
			expectedConditionReason: mcpv1alpha1.ConditionReasonListMCPServersSucceeded,
			expectedPhase:           mcpv1alpha1.MCPGroupPhaseReady,
		},
		{
			name: "MCPServersChecked condition is True even with no servers",
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group",
					Namespace: "default",
				},
			},
			mcpServers:              []*mcpv1alpha1.MCPServer{},
			expectedConditionStatus: metav1.ConditionTrue,
			expectedConditionReason: mcpv1alpha1.ConditionReasonListMCPServersSucceeded,
			expectedPhase:           mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			objs := []client.Object{tt.mcpGroup}
			for _, server := range tt.mcpServers {
				objs = append(objs, server)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPGroup{}).
				Build()

			r := &MCPGroupReconciler{
				Client: fakeClient,
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.mcpGroup.Name,
					Namespace: tt.mcpGroup.Namespace,
				},
			}

			result, err := r.Reconcile(ctx, req)
			require.NoError(t, err)
			assert.False(t, result.Requeue)

			var updatedGroup mcpv1alpha1.MCPGroup
			err = fakeClient.Get(ctx, req.NamespacedName, &updatedGroup)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedPhase, updatedGroup.Status.Phase)

			// Check the MCPServersChecked condition
			var condition *metav1.Condition
			for i := range updatedGroup.Status.Conditions {
				if updatedGroup.Status.Conditions[i].Type == mcpv1alpha1.ConditionTypeMCPServersChecked {
					condition = &updatedGroup.Status.Conditions[i]
					break
				}
			}

			require.NotNil(t, condition, "MCPServersChecked condition should be present")
			assert.Equal(t, tt.expectedConditionStatus, condition.Status)
			if tt.expectedConditionReason != "" {
				assert.Equal(t, tt.expectedConditionReason, condition.Reason)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestVirtualMCPCompositeToolDefinitionReconciler_Reconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                         string
		definition                   *mcpv1beta1.VirtualMCPCompositeToolDefinition
		virtualMCPServers            []mcpv1beta1.VirtualMCPServer
		expectedStepCount            int32
		expectedRefCount             int32
		expectedReferencingVMCPNames []string
	}{
		{
			name: "populates step and reference counts",
			definition: &mcpv1beta1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: vmcpconfig.CompositeToolConfig{
						Steps: []vmcpconfig.WorkflowStepConfig{{ID: "first"}, {ID: "second"}},
					},
				},
			},
			virtualMCPServers: []mcpv1beta1.VirtualMCPServer{
				virtualMCPServerWithCompositeToolRef("server-b", "workflow"),
				virtualMCPServerWithCompositeToolRef("server-a", "workflow"),
				virtualMCPServerWithCompositeToolRef("unrelated", "other-workflow"),
			},
			expectedStepCount:            2,
			expectedRefCount:             2,
			expectedReferencingVMCPNames: []string{"server-a", "server-b"},
		},
		{
			name: "clears references no longer present",
			definition: &mcpv1beta1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "default"},
				Status: mcpv1beta1.VirtualMCPCompositeToolDefinitionStatus{
					RefCount:                  1,
					ReferencingVirtualServers: []string{"removed-server"},
				},
			},
			expectedStepCount:            0,
			expectedRefCount:             0,
			expectedReferencingVMCPNames: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))

			objects := make([]runtime.Object, 0, len(tt.virtualMCPServers)+1)
			objects = append(objects, tt.definition)
			for i := range tt.virtualMCPServers {
				objects = append(objects, &tt.virtualMCPServers[i])
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				WithStatusSubresource(&mcpv1beta1.VirtualMCPCompositeToolDefinition{}).
				Build()
			reconciler := &VirtualMCPCompositeToolDefinitionReconciler{Client: fakeClient}
			key := types.NamespacedName{Name: tt.definition.Name, Namespace: tt.definition.Namespace}

			_, err := reconciler.Reconcile(t.Context(), reconcile.Request{
				NamespacedName: key,
			})
			require.NoError(t, err)

			updated := &mcpv1beta1.VirtualMCPCompositeToolDefinition{}
			require.NoError(t, fakeClient.Get(t.Context(), key, updated))
			assert.Equal(t, tt.expectedStepCount, updated.Status.StepCount)
			assert.Equal(t, tt.expectedRefCount, updated.Status.RefCount)
			assert.Equal(t, tt.expectedReferencingVMCPNames, updated.Status.ReferencingVirtualServers)
		})
	}
}

func TestVirtualMCPCompositeToolDefinitionReconciler_ReconcileNotFound(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	reconciler := &VirtualMCPCompositeToolDefinitionReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
	}

	result, err := reconciler.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "missing", Namespace: "default"},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestVirtualMCPCompositeToolDefinitionReconciler_ReconcileErrors(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	definition := &mcpv1beta1.VirtualMCPCompositeToolDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "default"},
	}
	getErr := errors.New("simulated get failure")
	listErr := errors.New("simulated list failure")

	t.Run("returns get failure", func(t *testing.T) {
		t.Parallel()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(
					_ context.Context,
					_ client.WithWatch,
					_ client.ObjectKey,
					_ client.Object,
					_ ...client.GetOption,
				) error {
					return getErr
				},
			}).
			Build()
		reconciler := &VirtualMCPCompositeToolDefinitionReconciler{Client: fakeClient}

		_, err := reconciler.Reconcile(t.Context(), reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "workflow", Namespace: "default"},
		})
		require.ErrorIs(t, err, getErr)
	})

	t.Run("returns reference list failure", func(t *testing.T) {
		t.Parallel()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(definition.DeepCopy()).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(
					_ context.Context,
					_ client.WithWatch,
					list client.ObjectList,
					_ ...client.ListOption,
				) error {
					if _, ok := list.(*mcpv1beta1.VirtualMCPServerList); ok {
						return listErr
					}
					return nil
				},
			}).
			Build()
		reconciler := &VirtualMCPCompositeToolDefinitionReconciler{Client: fakeClient}

		_, err := reconciler.Reconcile(t.Context(), reconcile.Request{
			NamespacedName: types.NamespacedName{Name: definition.Name, Namespace: definition.Namespace},
		})
		require.ErrorIs(t, err, listErr)
	})
}

func TestVirtualMCPCompositeToolDefinitionReconciler_MapVirtualMCPServer(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	server := virtualMCPServerWithCompositeToolRef("server", "current")
	server.Spec.Config.CompositeToolRefs = append(server.Spec.Config.CompositeToolRefs,
		vmcpconfig.CompositeToolRef{Name: "current"})
	current := &mcpv1beta1.VirtualMCPCompositeToolDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "current", Namespace: "default"},
		Status: mcpv1beta1.VirtualMCPCompositeToolDefinitionStatus{
			ReferencingVirtualServers: []string{"server"},
		},
	}
	stale := &mcpv1beta1.VirtualMCPCompositeToolDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "stale", Namespace: "default"},
		Status: mcpv1beta1.VirtualMCPCompositeToolDefinitionStatus{
			ReferencingVirtualServers: []string{"server"},
		},
	}

	t.Run("returns current and stale definitions without duplicates", func(t *testing.T) {
		t.Parallel()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(current.DeepCopy(), stale.DeepCopy()).
			Build()
		reconciler := &VirtualMCPCompositeToolDefinitionReconciler{Client: fakeClient}

		assert.ElementsMatch(t, []reconcile.Request{
			{NamespacedName: types.NamespacedName{Name: "current", Namespace: "default"}},
			{NamespacedName: types.NamespacedName{Name: "stale", Namespace: "default"}},
		}, reconciler.mapVirtualMCPServerToCompositeToolDefinitions(t.Context(), server.DeepCopy()))
	})

	t.Run("returns nil for unrelated object type", func(t *testing.T) {
		t.Parallel()

		reconciler := &VirtualMCPCompositeToolDefinitionReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"}}

		assert.Nil(t, reconciler.mapVirtualMCPServerToCompositeToolDefinitions(t.Context(), pod))
	})

	t.Run("returns current definition if stale lookup fails", func(t *testing.T) {
		t.Parallel()

		listErr := errors.New("simulated definition list failure")
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(
					_ context.Context,
					_ client.WithWatch,
					list client.ObjectList,
					_ ...client.ListOption,
				) error {
					if _, ok := list.(*mcpv1beta1.VirtualMCPCompositeToolDefinitionList); ok {
						return listErr
					}
					return nil
				},
			}).
			Build()
		reconciler := &VirtualMCPCompositeToolDefinitionReconciler{Client: fakeClient}

		assert.Equal(t, []reconcile.Request{{
			NamespacedName: types.NamespacedName{Name: "current", Namespace: "default"},
		}}, reconciler.mapVirtualMCPServerToCompositeToolDefinitions(t.Context(), server.DeepCopy()))
	})
}

func virtualMCPServerWithCompositeToolRef(name, definitionName string) mcpv1beta1.VirtualMCPServer {
	return mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{
				CompositeToolRefs: []vmcpconfig.CompositeToolRef{{Name: definitionName}},
			},
		},
	}
}

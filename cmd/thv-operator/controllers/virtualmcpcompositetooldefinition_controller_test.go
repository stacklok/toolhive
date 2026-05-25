// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

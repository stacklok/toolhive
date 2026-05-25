// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

// VirtualMCPCompositeToolDefinitionReconciler maintains display-oriented status
// for a VirtualMCPCompositeToolDefinition.
type VirtualMCPCompositeToolDefinitionReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpcompositetooldefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpcompositetooldefinitions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpservers,verbs=get;list;watch

// Reconcile updates the scalar counts used by kubectl printer columns and
// maintains the existing referencingVirtualServers status list.
func (r *VirtualMCPCompositeToolDefinitionReconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	compositeToolDefinition := &mcpv1beta1.VirtualMCPCompositeToolDefinition{}
	if err := r.Get(ctx, req.NamespacedName, compositeToolDefinition); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	referencingVirtualServers, err := r.findReferencingVirtualServers(ctx, compositeToolDefinition)
	if err != nil {
		return ctrl.Result{}, err
	}

	stepCount := compositeToolDefinitionItemCount(len(compositeToolDefinition.Spec.Steps))
	refCount := compositeToolDefinitionItemCount(len(referencingVirtualServers))

	if err := ctrlutil.MutateAndPatchStatus(ctx, r.Client, compositeToolDefinition,
		func(definition *mcpv1beta1.VirtualMCPCompositeToolDefinition) {
			definition.Status.StepCount = stepCount
			definition.Status.RefCount = refCount
			definition.Status.ReferencingVirtualServers = referencingVirtualServers
		}); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *VirtualMCPCompositeToolDefinitionReconciler) findReferencingVirtualServers(
	ctx context.Context,
	compositeToolDefinition *mcpv1beta1.VirtualMCPCompositeToolDefinition,
) ([]string, error) {
	virtualMCPServers := &mcpv1beta1.VirtualMCPServerList{}
	if err := r.List(ctx, virtualMCPServers, client.InNamespace(compositeToolDefinition.Namespace)); err != nil {
		return nil, err
	}

	referencingVirtualServers := make([]string, 0)
	for _, virtualMCPServer := range virtualMCPServers.Items {
		for _, ref := range virtualMCPServer.Spec.Config.CompositeToolRefs {
			if ref.Name == compositeToolDefinition.Name {
				referencingVirtualServers = append(referencingVirtualServers, virtualMCPServer.Name)
				break
			}
		}
	}
	sort.Strings(referencingVirtualServers)
	return referencingVirtualServers, nil
}

func compositeToolDefinitionItemCount(length int) int32 {
	return int32(length) //nolint:gosec // Kubernetes object size limits keep CRD list lengths within int32.
}

// SetupWithManager configures reconciliation of definitions and the virtual
// servers whose references determine the Refs column.
func (r *VirtualMCPCompositeToolDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	virtualMCPServerHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			virtualMCPServer, ok := obj.(*mcpv1beta1.VirtualMCPServer)
			if !ok {
				return nil
			}

			requests := make([]reconcile.Request, 0, len(virtualMCPServer.Spec.Config.CompositeToolRefs))
			seen := make(map[types.NamespacedName]struct{})
			for _, ref := range virtualMCPServer.Spec.Config.CompositeToolRefs {
				name := types.NamespacedName{Name: ref.Name, Namespace: virtualMCPServer.Namespace}
				if _, exists := seen[name]; exists {
					continue
				}
				seen[name] = struct{}{}
				requests = append(requests, reconcile.Request{NamespacedName: name})
			}

			definitions := &mcpv1beta1.VirtualMCPCompositeToolDefinitionList{}
			if err := r.List(ctx, definitions, client.InNamespace(virtualMCPServer.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list VirtualMCPCompositeToolDefinitions for VirtualMCPServer watch")
				return requests
			}
			for _, definition := range definitions.Items {
				name := types.NamespacedName{Name: definition.Name, Namespace: definition.Namespace}
				if _, exists := seen[name]; exists {
					continue
				}
				for _, referencingVirtualServer := range definition.Status.ReferencingVirtualServers {
					if referencingVirtualServer == virtualMCPServer.Name {
						requests = append(requests, reconcile.Request{NamespacedName: name})
						break
					}
				}
			}
			return requests
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1beta1.VirtualMCPCompositeToolDefinition{}).
		Watches(&mcpv1beta1.VirtualMCPServer{}, virtualMCPServerHandler).
		Complete(r)
}

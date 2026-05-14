// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// mapAuthzConfigMapToVirtualMCPServer maps ConfigMap changes to VirtualMCPServer reconciliation
// requests. Used by SetupWithManager to trigger reconciliation when a ConfigMap referenced via
// spec.incomingAuth.authzConfig.configMap is updated, so the converter can re-resolve policies
// and roll out a pod with the new config.
//
// The mapper lists VirtualMCPServers in the ConfigMap's namespace and enqueues any that
// reference this ConfigMap. ConfigMaps are cluster-wide objects but authz references are
// namespace-scoped, so the lookup is bounded to a single namespace.
func (r *VirtualMCPServerReconciler) mapAuthzConfigMapToVirtualMCPServer(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return nil
	}

	vmcpList := &mcpv1beta1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(cm.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for authz ConfigMap watch")
		return nil
	}

	var requests []reconcile.Request
	for _, vmcp := range vmcpList.Items {
		if !vmcpReferencesAuthzConfigMap(&vmcp, cm.Name) {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      vmcp.Name,
				Namespace: vmcp.Namespace,
			},
		})
	}

	return requests
}

// vmcpReferencesAuthzConfigMap reports whether the VirtualMCPServer references the named
// ConfigMap via spec.incomingAuth.authzConfig.
func vmcpReferencesAuthzConfigMap(vmcp *mcpv1beta1.VirtualMCPServer, configMapName string) bool {
	if vmcp.Spec.IncomingAuth == nil ||
		vmcp.Spec.IncomingAuth.AuthzConfig == nil ||
		vmcp.Spec.IncomingAuth.AuthzConfig.Type != mcpv1beta1.AuthzConfigTypeConfigMap ||
		vmcp.Spec.IncomingAuth.AuthzConfig.ConfigMap == nil {
		return false
	}
	return vmcp.Spec.IncomingAuth.AuthzConfig.ConfigMap.Name == configMapName
}

// configMapDataChangedPredicate admits ConfigMap events that may affect a VirtualMCPServer's
// resolved authz config. Update events are admitted only when .Data or .BinaryData actually
// change, so metadata-only updates (labels, annotations, resourceVersion bumps) do not trigger
// reconciliation. Create and Delete events are passed through so the controller can pick up a
// newly-created ConfigMap or surface a deletion as a status error.
func configMapDataChangedPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldCM, ok := e.ObjectOld.(*corev1.ConfigMap)
			if !ok {
				return false
			}
			newCM, ok := e.ObjectNew.(*corev1.ConfigMap)
			if !ok {
				return false
			}
			return !reflect.DeepEqual(oldCM.Data, newCM.Data) ||
				!reflect.DeepEqual(oldCM.BinaryData, newCM.BinaryData)
		},
		CreateFunc:  func(_ event.CreateEvent) bool { return true },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return true },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

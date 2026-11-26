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

const virtualMCPServerConfigMapIndex = "toolhive.stacklok.dev/virtualmcpserver-configmap"

// indexVirtualMCPServerConfigMaps returns namespace-local ConfigMap references
// whose data changes require the VirtualMCPServer to reconcile.
func indexVirtualMCPServerConfigMaps(obj client.Object) []string {
	vmcp, ok := obj.(*mcpv1beta1.VirtualMCPServer)
	if !ok {
		return nil
	}

	names := make(map[string]struct{})
	if vmcp.Spec.IncomingAuth != nil && vmcp.Spec.IncomingAuth.AuthzConfig != nil &&
		vmcp.Spec.IncomingAuth.AuthzConfig.Type == mcpv1beta1.AuthzConfigTypeConfigMap &&
		vmcp.Spec.IncomingAuth.AuthzConfig.ConfigMap != nil &&
		vmcp.Spec.IncomingAuth.AuthzConfig.ConfigMap.Name != "" {
		names[vmcp.Spec.IncomingAuth.AuthzConfig.ConfigMap.Name] = struct{}{}
	}
	if vmcp.Spec.AuthServerConfig != nil {
		for _, provider := range vmcp.Spec.AuthServerConfig.UpstreamProviders {
			if name := providerCABundleConfigMapName(provider); name != "" {
				names[name] = struct{}{}
			}
		}
		for i := range vmcp.Spec.AuthServerConfig.TrustedIssuers {
			ref := vmcp.Spec.AuthServerConfig.TrustedIssuers[i].CABundleRef
			if ref != nil && ref.ConfigMapRef != nil && ref.ConfigMapRef.Name != "" {
				names[ref.ConfigMapRef.Name] = struct{}{}
			}
		}
	}

	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	return result
}

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
	if err := r.List(ctx, vmcpList,
		client.InNamespace(cm.Namespace),
		client.MatchingFields{virtualMCPServerConfigMapIndex: cm.Name},
	); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for ConfigMap watch")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(vmcpList.Items))
	for i := range vmcpList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      vmcpList.Items[i].Name,
				Namespace: vmcpList.Items[i].Namespace,
			},
		})
	}

	return requests
}

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

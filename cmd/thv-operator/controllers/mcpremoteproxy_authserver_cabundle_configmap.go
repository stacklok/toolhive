// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// mapAuthServerCABundleConfigMapToMCPRemoteProxy enqueues proxies whose
// referenced embedded auth-server configuration uses the changed ConfigMap.
// References are namespace-local and resolved through field indexes.
func (r *MCPRemoteProxyReconciler) mapAuthServerCABundleConfigMapToMCPRemoteProxy(
	ctx context.Context, obj client.Object) []reconcile.Request {
	var configs mcpv1beta1.MCPExternalAuthConfigList
	if err := r.List(ctx, &configs,
		client.InNamespace(obj.GetNamespace()),
		client.MatchingFields{EmbeddedAuthCABundleConfigMapIndex: obj.GetName()},
	); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list MCPExternalAuthConfigs for CA bundle ConfigMap watch")
		return nil
	}

	requests := make([]reconcile.Request, 0)
	for i := range configs.Items {
		var proxies mcpv1beta1.MCPRemoteProxyList
		if err := r.List(ctx, &proxies,
			client.InNamespace(obj.GetNamespace()),
			client.MatchingFields{EmbeddedAuthConfigIndex: configs.Items[i].Name},
		); err != nil {
			log.FromContext(ctx).Error(err, "Failed to list MCPRemoteProxies for CA bundle ConfigMap watch")
			return nil
		}
		for j := range proxies.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&proxies.Items[j])})
		}
	}
	return requests
}

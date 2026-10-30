// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
)

const oidcConfigCABundleConfigMapIndex = "toolhive.stacklok.dev/oidcconfig-cabundle-configmap"

// validateCABundleRef records missing or invalid incoming OIDC CA references as
// advisory conditions and returns nil so reconciliation can continue. A transient
// CA ConfigMap read error preserves the existing condition and returns an error
// for retry: Reconcile stops before resource reconciliation and the status flush.
func (r *VirtualMCPServerReconciler) validateCABundleRef(
	ctx context.Context, vmcp *mcpv1beta1.VirtualMCPServer, statusManager virtualmcpserverstatus.StatusManager,
) error {
	conditionType := mcpv1beta1.ConditionTypeVirtualMCPServerCABundleRefValidated
	if vmcp.Spec.IncomingAuth == nil || vmcp.Spec.IncomingAuth.OIDCConfigRef == nil {
		statusManager.RemoveConditionsWithPrefix(conditionType, nil)
		return nil
	}
	oidcConfig, err := ctrlutil.GetOIDCConfigForServer(ctx, r.Client, vmcp.Namespace, vmcp.Spec.IncomingAuth.OIDCConfigRef)
	if err != nil || oidcConfig == nil {
		// handleOIDCConfig owns OIDC reference failures and their retry. Preserve the
		// existing CA condition until the configuration can actually be resolved.
		return nil
	}
	if oidcConfig.Spec.Type != mcpv1beta1.MCPOIDCConfigTypeInline ||
		oidcConfig.Spec.Inline == nil || oidcConfig.Spec.Inline.CABundleRef == nil {
		statusManager.RemoveConditionsWithPrefix(conditionType, nil)
		return nil
	}
	ref := oidcConfig.Spec.Inline.CABundleRef
	if err := validation.ValidateCABundleSource(ref); err != nil {
		statusManager.SetCondition(conditionType, mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefInvalid,
			err.Error(), metav1.ConditionFalse)
		return nil
	}
	configMap := &corev1.ConfigMap{}
	cmName := ref.ConfigMapRef.Name
	if err := r.Get(ctx, types.NamespacedName{Namespace: vmcp.Namespace, Name: cmName}, configMap); err != nil {
		if errors.IsNotFound(err) {
			statusManager.SetCondition(conditionType, mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefNotFound,
				fmt.Sprintf("CA bundle ConfigMap '%s' not found in namespace '%s'", cmName, vmcp.Namespace), metav1.ConditionFalse)
		} else {
			// Preserve the last observation on API errors and retry rather than
			// misreporting a transient failure as a missing ConfigMap.
			return fmt.Errorf("failed to get OIDC CA bundle ConfigMap %s/%s: %w", vmcp.Namespace, cmName, err)
		}
		return nil
	}
	key := caBundleKey(ref)
	_, dataExists := configMap.Data[key]
	_, binaryDataExists := configMap.BinaryData[key]
	if !dataExists && !binaryDataExists {
		statusManager.SetCondition(conditionType, mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefInvalid,
			fmt.Sprintf("Key '%s' not found in ConfigMap '%s'", key, cmName), metav1.ConditionFalse)
		return nil
	}
	statusManager.SetCondition(conditionType, mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefValid,
		fmt.Sprintf("CA bundle ConfigMap '%s' is valid (key: %s)", cmName, key), metav1.ConditionTrue)
	return nil
}

// indexOIDCConfigByCABundleConfigMap indexes the CA dependency without resolving
// referenced objects in the informer callback.
func indexOIDCConfigByCABundleConfigMap(obj client.Object) []string {
	config, ok := obj.(*mcpv1beta1.MCPOIDCConfig)
	if !ok || config.Spec.Type != mcpv1beta1.MCPOIDCConfigTypeInline || config.Spec.Inline == nil ||
		config.Spec.Inline.CABundleRef == nil || config.Spec.Inline.CABundleRef.ConfigMapRef == nil ||
		config.Spec.Inline.CABundleRef.ConfigMapRef.Name == "" {
		return nil
	}
	return []string{config.Spec.Inline.CABundleRef.ConfigMapRef.Name}
}

// mapOIDCCABundleConfigMapToVirtualMCPServer follows indexed ConfigMap -> OIDC
// config -> vMCP references so fixing or removing the CA refreshes its condition.
func (r *VirtualMCPServerReconciler) mapOIDCCABundleConfigMapToVirtualMCPServer(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return nil
	}
	configs := &mcpv1beta1.MCPOIDCConfigList{}
	if err := r.List(ctx, configs, client.InNamespace(cm.Namespace),
		client.MatchingFields{oidcConfigCABundleConfigMapIndex: cm.Name}); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list OIDC configs for CA bundle ConfigMap watch")
		return nil
	}
	var requests []reconcile.Request
	for _, config := range configs.Items {
		servers := &mcpv1beta1.VirtualMCPServerList{}
		if err := r.List(ctx, servers, client.InNamespace(cm.Namespace),
			client.MatchingFields{vmcpOIDCConfigRefIndexKey: config.Name}); err != nil {
			log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for CA bundle ConfigMap watch")
			continue
		}
		for _, server := range servers.Items {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: server.Namespace, Name: server.Name,
			}})
		}
	}
	return requests
}

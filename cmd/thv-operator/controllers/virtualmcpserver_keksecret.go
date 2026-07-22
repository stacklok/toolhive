// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"reflect"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

// mapKEKSecretToVirtualMCPServer maps KEK Secret changes to the
// VirtualMCPServers whose spec.authServerConfig.storage.tokenEncryption
// references the Secret by name. The operator renders one pod env var per
// Secret data key, so a rotation that adds or removes a key ID changes the
// desired pod env and must trigger reconciliation (the Deployment drift check
// then rolls the vMCP pods). Value-only updates under an unchanged key set
// are already picked up by kubelet's env refresh; the mapper still enqueues
// (cheap) and the reconcile is a no-op.
//
// Key IDs are compared via the deterministic env list the deployment builder
// renders (ctrlutil.TokenEncryptionEnvVars), so the mapper fires exactly when
// the Secret-driven pod env would change.
func (r *VirtualMCPServerReconciler) mapKEKSecretToVirtualMCPServer(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	vmcpList := &mcpv1beta1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(secret.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for KEK Secret watch")
		return nil
	}

	var requests []reconcile.Request
	for i := range vmcpList.Items {
		vmcp := &vmcpList.Items[i]
		as := vmcp.Spec.AuthServerConfig
		if as == nil || as.Storage == nil {
			continue
		}
		te := as.Storage.GetTokenEncryption()
		if te == nil || te.KeySecretRef.Name != secret.Name {
			continue
		}
		envByID, err := ctrlutil.ResolveKEKKeySet(ctx, r.Client, vmcp.Namespace, as)
		if err != nil {
			// The reconcile surfaces the same error on the Valid condition;
			// enqueue so the diagnostic refreshes.
			log.FromContext(ctx).V(1).Info("KEK Secret watch: key set unresolvable; enqueueing for diagnosis",
				"virtualmcpserver", vmcp.Name, "secret", secret.Name, "error", err)
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
				Name: vmcp.Name, Namespace: vmcp.Namespace,
			}})
			continue
		}
		podEnv := ctrlutil.TokenEncryptionEnvVars(te.KeySecretRef.Name, envByID)
		if r.vmcpKEKEnvChanged(ctx, vmcp, podEnv) {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
				Name: vmcp.Name, Namespace: vmcp.Namespace,
			}})
		}
	}
	return requests
}

// vmcpKEKEnvChanged reports whether the token-encryption env set on the
// vMCP's live Deployment differs from the one derived from the current KEK
// Secret (the signal a rotation changed the key-ID set). A missing Deployment
// means creation will pick up the current set — no reconcile needed from the
// watch.
func (r *VirtualMCPServerReconciler) vmcpKEKEnvChanged(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	want []corev1.EnvVar,
) bool {
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: vmcp.Name, Namespace: vmcp.Namespace}, dep); err != nil {
		return false
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return false
	}
	prefix := ctrlutil.TokenEncryptionKEKEnvVarPrefix + "_"
	live := make(map[string]corev1.EnvVar)
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if strings.HasPrefix(e.Name, prefix) {
			live[e.Name] = e
		}
	}
	if len(live) != len(want) {
		return true
	}
	for _, e := range want {
		if !reflect.DeepEqual(live[e.Name], e) {
			return true
		}
	}
	return false
}

// secretDataChangedPredicate admits Secret events that may affect the KEK
// key-ID set rendered into vMCP pod env. Update events are admitted only when
// .Data actually changes, so metadata-only churn (annotation rewrites,
// resourceVersion bumps) does not fan out reconciles. Create and Delete pass
// through so a rotated-into-existence or removed Secret is observed.
// Mirrors configMapDataChangedPredicate.
func secretDataChangedPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldSecret, okOld := e.ObjectOld.(*corev1.Secret)
			newSecret, okNew := e.ObjectNew.(*corev1.Secret)
			if !okOld || !okNew {
				return false
			}
			return !reflect.DeepEqual(oldSecret.Data, newSecret.Data)
		},
		CreateFunc:  func(_ event.CreateEvent) bool { return true },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return true },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

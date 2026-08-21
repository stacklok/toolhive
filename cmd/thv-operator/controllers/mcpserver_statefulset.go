// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// statefulSetHealCooldown is how long we wait after bouncing the proxy
// Deployment before bouncing again while the workload StatefulSet is still
// missing. The proxy-runner creates the StatefulSet on start; a tight loop
// would just restart it forever.
const statefulSetHealCooldown = 2 * time.Minute

// ensureWorkloadStatefulSet makes sure the proxy-runner's backend StatefulSet
// still exists. The operator does not build that spec (the runner does via
// server-side apply), but it owns the lifecycle: if the StatefulSet is
// deleted, the next reconcile must recreate it by bouncing the proxy so the
// runner re-applies (#6343).
//
// When the StatefulSet exists, we adopt it with a controller owner-reference
// so Kubernetes GC and owner-based watches work, and so "orphan" cleanup
// scripts stop treating it as leftover.
func (r *MCPServerReconciler) ensureWorkloadStatefulSet(
	ctx context.Context,
	mcpServer *mcpv1beta1.MCPServer,
	deployment *appsv1.Deployment,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	sts := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, sts)
	if err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("failed to get workload StatefulSet: %w", err)
	}

	if errors.IsNotFound(err) {
		ctxLogger.Info("Workload StatefulSet missing; waiting for proxy-runner to recreate it",
			"MCPServer", mcpServer.Name, "namespace", mcpServer.Namespace)

		// The runner creates the StatefulSet on start. Bounce the proxy only
		// once it is already Available — otherwise we interrupt the first boot.
		if deployment != nil && deployment.Status.AvailableReplicas > 0 {
			if err := r.bounceProxyForMissingStatefulSet(ctx, deployment); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if err := r.adoptWorkloadStatefulSet(ctx, mcpServer, sts); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// bounceProxyForMissingStatefulSet triggers a rolling restart of the proxy
// Deployment so the runner re-runs DeployWorkload and recreates the STS.
func (r *MCPServerReconciler) bounceProxyForMissingStatefulSet(
	ctx context.Context,
	deployment *appsv1.Deployment,
) error {
	ctxLogger := log.FromContext(ctx)

	if recentlyBounced(deployment) {
		ctxLogger.V(1).Info("Proxy already bounced recently; waiting for StatefulSet to reappear",
			"Deployment", deployment.Name)
		return nil
	}

	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = map[string]string{}
	}
	deployment.Spec.Template.Annotations[RestartedAtAnnotationKey] = time.Now().UTC().Format(time.RFC3339)
	if err := r.Update(ctx, deployment); err != nil {
		return fmt.Errorf("failed to bounce proxy Deployment to recreate StatefulSet: %w", err)
	}
	ctxLogger.Info("Bounced proxy Deployment so the runner can recreate the missing StatefulSet",
		"Deployment", deployment.Name)
	return nil
}

func recentlyBounced(deployment *appsv1.Deployment) bool {
	if deployment.Spec.Template.Annotations == nil {
		return false
	}
	raw := deployment.Spec.Template.Annotations[RestartedAtAnnotationKey]
	if raw == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	return time.Since(ts) < statefulSetHealCooldown
}

// adoptWorkloadStatefulSet sets a controller owner-reference from the
// MCPServer onto the runner-created StatefulSet when missing.
func (r *MCPServerReconciler) adoptWorkloadStatefulSet(
	ctx context.Context,
	mcpServer *mcpv1beta1.MCPServer,
	sts *appsv1.StatefulSet,
) error {
	for _, ref := range sts.OwnerReferences {
		if ref.UID == mcpServer.UID && ref.Controller != nil && *ref.Controller {
			return nil
		}
	}
	if err := controllerutil.SetControllerReference(mcpServer, sts, r.Scheme); err != nil {
		return fmt.Errorf("failed to set MCPServer owner on StatefulSet: %w", err)
	}
	if err := r.Update(ctx, sts); err != nil {
		return fmt.Errorf("failed to adopt workload StatefulSet: %w", err)
	}
	log.FromContext(ctx).Info("Adopted workload StatefulSet under MCPServer",
		"StatefulSet", sts.Name, "MCPServer", mcpServer.Name)
	return nil
}

// mapStatefulSetToMCPServer enqueues the MCPServer that owns a toolhive
// StatefulSet, including ones the runner created without an owner-reference.
func (*MCPServerReconciler) mapStatefulSetToMCPServer(_ context.Context, obj client.Object) []reconcile.Request {
	sts, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		return nil
	}
	name := sts.Labels["toolhive-name"]
	if name == "" {
		name = sts.Name
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: name, Namespace: sts.Namespace},
	}}
}

// statefulSetMissing reports whether the runner-managed backend StatefulSet
// is absent. Used by status so Ready is not claimed on a proxy-only stack.
func (r *MCPServerReconciler) statefulSetMissing(ctx context.Context, m *mcpv1beta1.MCPServer) (bool, error) {
	sts := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: m.Name, Namespace: m.Namespace}, sts)
	if err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// workloadStatefulSetOwnerPredicate keeps the STS watch on ToolHive-labeled
// objects so we do not enqueue on unrelated StatefulSets in the namespace.
func isToolhiveStatefulSet(obj client.Object) bool {
	if obj == nil {
		return false
	}
	labels := obj.GetLabels()
	if labels == nil {
		return false
	}
	return labels["toolhive"] == "true"
}

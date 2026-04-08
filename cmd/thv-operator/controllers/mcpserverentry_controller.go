// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	// mcpServerEntryRequeueDelay is the delay before requeuing after a conflict.
	mcpServerEntryRequeueDelay = 500 * time.Millisecond
)

// MCPServerEntryReconciler reconciles a MCPServerEntry object.
// This is a validation-only controller — it never creates infrastructure
// (no Deployment, Service, or Pod) and never probes remote URLs.
type MCPServerEntryReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpserverentries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpserverentries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpexternalauthconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

// Reconcile validates referenced resources and updates status conditions.
func (r *MCPServerEntryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	entry := &mcpv1alpha1.MCPServerEntry{}
	if err := r.Get(ctx, req.NamespacedName, entry); err != nil {
		if errors.IsNotFound(err) {
			ctxLogger.Info("MCPServerEntry resource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		ctxLogger.Error(err, "Failed to get MCPServerEntry")
		return ctrl.Result{}, err
	}

	// Validate all referenced resources
	allValid := true
	allValid = r.validateGroupRef(ctx, entry) && allValid
	allValid = r.validateExternalAuthConfigRef(ctx, entry) && allValid
	allValid = r.validateCABundleRef(ctx, entry) && allValid

	// Compute overall phase and Valid condition
	r.updateOverallStatus(entry, allValid)

	// Persist status
	entry.Status.ObservedGeneration = entry.Generation
	if err := r.Status().Update(ctx, entry); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: mcpServerEntryRequeueDelay}, nil
		}
		ctxLogger.Error(err, "Failed to update MCPServerEntry status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPServerEntryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPServerEntry{}).
		Watches(
			&mcpv1alpha1.MCPExternalAuthConfig{},
			handler.EnqueueRequestsFromMapFunc(r.findEntriesForAuthConfig),
		).
		Complete(r)
}

// validateGroupRef checks that the referenced MCPGroup exists.
// Returns true if the validation passed.
func (r *MCPServerEntryReconciler) validateGroupRef(
	ctx context.Context,
	entry *mcpv1alpha1.MCPServerEntry,
) bool {
	group := &mcpv1alpha1.MCPGroup{}
	groupKey := types.NamespacedName{Namespace: entry.Namespace, Name: entry.Spec.GroupRef}

	if err := r.Get(ctx, groupKey, group); err != nil {
		reason := mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefNotFound
		message := "Referenced MCPGroup not found"
		if !errors.IsNotFound(err) {
			message = "Failed to get referenced MCPGroup: " + err.Error()
		}
		meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValid,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: entry.Generation,
		})
		return false
	}

	meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValid,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefValid,
		Message:            "Referenced MCPGroup exists",
		ObservedGeneration: entry.Generation,
	})
	return true
}

// validateExternalAuthConfigRef checks that the referenced MCPExternalAuthConfig exists when configured.
// Returns true if the validation passed (or if no ref is configured).
func (r *MCPServerEntryReconciler) validateExternalAuthConfigRef(
	ctx context.Context,
	entry *mcpv1alpha1.MCPServerEntry,
) bool {
	if entry.Spec.ExternalAuthConfigRef == nil {
		meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPServerEntryAuthConfigValid,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryAuthConfigNotConfigured,
			Message:            "No external auth config reference configured",
			ObservedGeneration: entry.Generation,
		})
		return true
	}

	authConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
	authKey := types.NamespacedName{
		Namespace: entry.Namespace,
		Name:      entry.Spec.ExternalAuthConfigRef.Name,
	}

	if err := r.Get(ctx, authKey, authConfig); err != nil {
		reason := mcpv1alpha1.ConditionReasonMCPServerEntryAuthConfigNotFound
		message := "Referenced MCPExternalAuthConfig not found"
		if !errors.IsNotFound(err) {
			message = "Failed to get referenced MCPExternalAuthConfig: " + err.Error()
		}
		meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPServerEntryAuthConfigValid,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: entry.Generation,
		})
		return false
	}

	meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeMCPServerEntryAuthConfigValid,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryAuthConfigValid,
		Message:            "Referenced MCPExternalAuthConfig exists",
		ObservedGeneration: entry.Generation,
	})
	return true
}

// validateCABundleRef checks that the referenced CA bundle ConfigMap exists when configured.
// Returns true if the validation passed (or if no ref is configured).
func (r *MCPServerEntryReconciler) validateCABundleRef(
	ctx context.Context,
	entry *mcpv1alpha1.MCPServerEntry,
) bool {
	if entry.Spec.CABundleRef == nil || entry.Spec.CABundleRef.ConfigMapRef == nil {
		meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPServerEntryCABundleValid,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryCABundleNotConfigured,
			Message:            "No CA bundle reference configured",
			ObservedGeneration: entry.Generation,
		})
		return true
	}

	configMap := &corev1.ConfigMap{}
	cmKey := types.NamespacedName{
		Namespace: entry.Namespace,
		Name:      entry.Spec.CABundleRef.ConfigMapRef.Name,
	}

	if err := r.Get(ctx, cmKey, configMap); err != nil {
		reason := mcpv1alpha1.ConditionReasonMCPServerEntryCABundleNotFound
		message := "Referenced CA bundle ConfigMap not found"
		if !errors.IsNotFound(err) {
			message = "Failed to get referenced CA bundle ConfigMap: " + err.Error()
		}
		meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPServerEntryCABundleValid,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: entry.Generation,
		})
		return false
	}

	meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeMCPServerEntryCABundleValid,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryCABundleValid,
		Message:            "Referenced CA bundle ConfigMap exists",
		ObservedGeneration: entry.Generation,
	})
	return true
}

// updateOverallStatus sets the phase and Valid condition based on validation results.
func (*MCPServerEntryReconciler) updateOverallStatus(
	entry *mcpv1alpha1.MCPServerEntry,
	allValid bool,
) {
	if allValid {
		entry.Status.Phase = mcpv1alpha1.MCPServerEntryPhaseValid
		meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPServerEntryValid,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryValid,
			Message:            "All referenced resources are valid",
			ObservedGeneration: entry.Generation,
		})
		return
	}

	entry.Status.Phase = mcpv1alpha1.MCPServerEntryPhasePending
	meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeMCPServerEntryValid,
		Status:             metav1.ConditionFalse,
		Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryInvalid,
		Message:            "One or more referenced resources are missing or invalid",
		ObservedGeneration: entry.Generation,
	})
}

// findEntriesForAuthConfig maps MCPExternalAuthConfig changes to MCPServerEntry reconcile requests.
func (r *MCPServerEntryReconciler) findEntriesForAuthConfig(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	ctxLogger := log.FromContext(ctx)

	authConfig, ok := obj.(*mcpv1alpha1.MCPExternalAuthConfig)
	if !ok {
		ctxLogger.Error(nil, "Object is not an MCPExternalAuthConfig", "object", obj.GetName())
		return nil
	}

	// List all MCPServerEntries in the same namespace
	entryList := &mcpv1alpha1.MCPServerEntryList{}
	if err := r.List(ctx, entryList, client.InNamespace(authConfig.Namespace)); err != nil {
		ctxLogger.Error(err, "Failed to list MCPServerEntries for auth config change")
		return nil
	}

	// Return reconcile requests for entries that reference this auth config
	var requests []reconcile.Request
	for _, entry := range entryList.Items {
		if entry.Spec.ExternalAuthConfigRef != nil &&
			entry.Spec.ExternalAuthConfigRef.Name == authConfig.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: entry.Namespace,
					Name:      entry.Name,
				},
			})
		}
	}
	return requests
}

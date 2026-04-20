// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
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
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

const (
	// mcpServerEntryRequeueDelay is the delay before requeuing after a conflict.
	mcpServerEntryRequeueDelay = 500 * time.Millisecond

	// mcpServerEntryAuthConfigRefField is the field index key for ExternalAuthConfigRef lookups.
	mcpServerEntryAuthConfigRefField = "spec.externalAuthConfigRef.name"

	// mcpServerEntryCABundleRefField is the field index key for CABundleRef ConfigMap lookups.
	mcpServerEntryCABundleRefField = "spec.caBundleRef.configMapRef.name"
)

// MCPServerEntryReconciler reconciles a MCPServerEntry object.
// This is a validation-only controller — it never creates infrastructure
// (no Deployment, Service, or Pod) and never probes remote URLs.
type MCPServerEntryReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpserverentries,verbs=get;list;watch
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

	// Validate all referenced resources. Transient errors are returned directly
	// to force a requeue rather than persisting a misleading condition.
	allValid := true

	allValid = r.validateRemoteURL(entry) && allValid

	valid, err := r.validateGroupRef(ctx, entry)
	if err != nil {
		return ctrl.Result{}, err
	}
	allValid = valid && allValid

	valid, err = r.validateExternalAuthConfigRef(ctx, entry)
	if err != nil {
		return ctrl.Result{}, err
	}
	allValid = valid && allValid

	valid, err = r.validateCABundleRef(ctx, entry)
	if err != nil {
		return ctrl.Result{}, err
	}
	allValid = valid && allValid

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
	// Set up field index for ExternalAuthConfigRef lookups
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&mcpv1alpha1.MCPServerEntry{},
		mcpServerEntryAuthConfigRefField,
		func(obj client.Object) []string {
			entry := obj.(*mcpv1alpha1.MCPServerEntry)
			if entry.Spec.ExternalAuthConfigRef == nil {
				return nil
			}
			return []string{entry.Spec.ExternalAuthConfigRef.Name}
		},
	); err != nil {
		return fmt.Errorf("unable to create field index for MCPServerEntry %s: %w",
			mcpServerEntryAuthConfigRefField, err)
	}

	// Set up field index for CABundleRef ConfigMap lookups
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&mcpv1alpha1.MCPServerEntry{},
		mcpServerEntryCABundleRefField,
		func(obj client.Object) []string {
			entry := obj.(*mcpv1alpha1.MCPServerEntry)
			if entry.Spec.CABundleRef == nil || entry.Spec.CABundleRef.ConfigMapRef == nil {
				return nil
			}
			return []string{entry.Spec.CABundleRef.ConfigMapRef.Name}
		},
	); err != nil {
		return fmt.Errorf("unable to create field index for MCPServerEntry %s: %w",
			mcpServerEntryCABundleRefField, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPServerEntry{}).
		Watches(
			&mcpv1alpha1.MCPExternalAuthConfig{},
			handler.EnqueueRequestsFromMapFunc(r.findEntriesForAuthConfig),
		).
		Watches(
			&mcpv1alpha1.MCPGroup{},
			handler.EnqueueRequestsFromMapFunc(r.findEntriesForGroup),
		).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.findEntriesForConfigMap),
		).
		Complete(r)
}

// validateGroupRef checks that the referenced MCPGroup exists and is ready.
// Returns (valid, error). A non-nil error means a transient failure that should be requeued.
func (r *MCPServerEntryReconciler) validateGroupRef(
	ctx context.Context,
	entry *mcpv1alpha1.MCPServerEntry,
) (bool, error) {
	ctxLogger := log.FromContext(ctx)
	groupName := entry.Spec.GroupRef.GetName()
	group := &mcpv1alpha1.MCPGroup{}
	groupKey := types.NamespacedName{Namespace: entry.Namespace, Name: groupName}

	if err := r.Get(ctx, groupKey, group); err != nil {
		if errors.IsNotFound(err) {
			meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
				Type:               mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValidated,
				Status:             metav1.ConditionFalse,
				Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefNotFound,
				Message:            fmt.Sprintf("MCPGroup '%s' not found in namespace '%s'", groupName, entry.Namespace),
				ObservedGeneration: entry.Generation,
			})
			return false, nil
		}
		ctxLogger.Error(err, "Failed to get referenced MCPGroup")
		return false, err
	}

	// Check that the group is ready
	if group.Status.Phase != mcpv1alpha1.MCPGroupPhaseReady {
		meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefNotReady,
			Message:            fmt.Sprintf("MCPGroup '%s' is not ready (current phase: %s)", groupName, group.Status.Phase),
			ObservedGeneration: entry.Generation,
		})
		return false, nil
	}

	meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefValidated,
		Message:            "Referenced MCPGroup exists and is ready",
		ObservedGeneration: entry.Generation,
	})
	return true, nil
}

// validateExternalAuthConfigRef checks that the referenced MCPExternalAuthConfig exists when configured.
// Returns (valid, error). A non-nil error means a transient failure that should be requeued.
func (r *MCPServerEntryReconciler) validateExternalAuthConfigRef(
	ctx context.Context,
	entry *mcpv1alpha1.MCPServerEntry,
) (bool, error) {
	ctxLogger := log.FromContext(ctx)
	if entry.Spec.ExternalAuthConfigRef == nil {
		meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPServerEntryAuthConfigValidated,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryAuthConfigNotConfigured,
			Message:            "No external auth config reference configured",
			ObservedGeneration: entry.Generation,
		})
		return true, nil
	}

	authConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
	authKey := types.NamespacedName{
		Namespace: entry.Namespace,
		Name:      entry.Spec.ExternalAuthConfigRef.Name,
	}

	if err := r.Get(ctx, authKey, authConfig); err != nil {
		if errors.IsNotFound(err) {
			meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
				Type:               mcpv1alpha1.ConditionTypeMCPServerEntryAuthConfigValidated,
				Status:             metav1.ConditionFalse,
				Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryAuthConfigNotFound,
				Message:            "Referenced MCPExternalAuthConfig not found",
				ObservedGeneration: entry.Generation,
			})
			return false, nil
		}
		ctxLogger.Error(err, "Failed to get referenced MCPExternalAuthConfig")
		return false, err
	}

	meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeMCPServerEntryAuthConfigValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryAuthConfigValid,
		Message:            "Referenced MCPExternalAuthConfig exists",
		ObservedGeneration: entry.Generation,
	})
	return true, nil
}

// validateCABundleRef checks that the referenced CA bundle ConfigMap exists when configured.
// Returns (valid, error). A non-nil error means a transient failure that should be requeued.
func (r *MCPServerEntryReconciler) validateCABundleRef(
	ctx context.Context,
	entry *mcpv1alpha1.MCPServerEntry,
) (bool, error) {
	ctxLogger := log.FromContext(ctx)
	if entry.Spec.CABundleRef == nil || entry.Spec.CABundleRef.ConfigMapRef == nil {
		meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPServerEntryCABundleRefValidated,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryCABundleRefNotConfigured,
			Message:            "No CA bundle reference configured",
			ObservedGeneration: entry.Generation,
		})
		return true, nil
	}

	configMap := &corev1.ConfigMap{}
	cmKey := types.NamespacedName{
		Namespace: entry.Namespace,
		Name:      entry.Spec.CABundleRef.ConfigMapRef.Name,
	}

	if err := r.Get(ctx, cmKey, configMap); err != nil {
		if errors.IsNotFound(err) {
			meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
				Type:               mcpv1alpha1.ConditionTypeMCPServerEntryCABundleRefValidated,
				Status:             metav1.ConditionFalse,
				Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryCABundleRefNotFound,
				Message:            "Referenced CA bundle ConfigMap not found",
				ObservedGeneration: entry.Generation,
			})
			return false, nil
		}
		ctxLogger.Error(err, "Failed to get referenced CA bundle ConfigMap")
		return false, err
	}

	meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeMCPServerEntryCABundleRefValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryCABundleRefValid,
		Message:            "Referenced CA bundle ConfigMap exists",
		ObservedGeneration: entry.Generation,
	})
	return true, nil
}

// validateRemoteURL checks that the RemoteURL is well-formed and does not target
// a blocked internal or metadata endpoint (SSRF protection).
func (*MCPServerEntryReconciler) validateRemoteURL(
	entry *mcpv1alpha1.MCPServerEntry,
) bool {
	if err := validation.ValidateRemoteURL(entry.Spec.RemoteURL); err != nil {
		meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPServerEntryRemoteURLValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryRemoteURLInvalid,
			Message:            err.Error(),
			ObservedGeneration: entry.Generation,
		})
		return false
	}

	meta.SetStatusCondition(&entry.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeMCPServerEntryRemoteURLValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonMCPServerEntryRemoteURLValid,
		Message:            "Remote URL is valid",
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

	entry.Status.Phase = mcpv1alpha1.MCPServerEntryPhaseFailed
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

	entryList := &mcpv1alpha1.MCPServerEntryList{}
	if err := r.List(ctx, entryList,
		client.InNamespace(authConfig.Namespace),
		client.MatchingFields{mcpServerEntryAuthConfigRefField: authConfig.Name},
	); err != nil {
		ctxLogger.Error(err, "Failed to list MCPServerEntries for auth config change")
		return nil
	}

	requests := make([]reconcile.Request, len(entryList.Items))
	for i, entry := range entryList.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: entry.Namespace,
				Name:      entry.Name,
			},
		}
	}
	return requests
}

// findEntriesForGroup maps MCPGroup changes to MCPServerEntry reconcile requests.
func (r *MCPServerEntryReconciler) findEntriesForGroup(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	ctxLogger := log.FromContext(ctx)

	group, ok := obj.(*mcpv1alpha1.MCPGroup)
	if !ok {
		ctxLogger.Error(nil, "Object is not an MCPGroup", "object", obj.GetName())
		return nil
	}

	entryList := &mcpv1alpha1.MCPServerEntryList{}
	if err := r.List(ctx, entryList,
		client.InNamespace(group.Namespace),
		client.MatchingFields{"spec.groupRef": group.Name},
	); err != nil {
		ctxLogger.Error(err, "Failed to list MCPServerEntries for group change")
		return nil
	}

	requests := make([]reconcile.Request, len(entryList.Items))
	for i, entry := range entryList.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: entry.Namespace,
				Name:      entry.Name,
			},
		}
	}
	return requests
}

// findEntriesForConfigMap maps ConfigMap changes to MCPServerEntry reconcile requests
// for entries that reference the ConfigMap as a CA bundle.
func (r *MCPServerEntryReconciler) findEntriesForConfigMap(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	ctxLogger := log.FromContext(ctx)

	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		ctxLogger.Error(nil, "Object is not a ConfigMap", "object", obj.GetName())
		return nil
	}

	entryList := &mcpv1alpha1.MCPServerEntryList{}
	if err := r.List(ctx, entryList,
		client.InNamespace(cm.Namespace),
		client.MatchingFields{mcpServerEntryCABundleRefField: cm.Name},
	); err != nil {
		ctxLogger.Error(err, "Failed to list MCPServerEntries for ConfigMap change")
		return nil
	}

	requests := make([]reconcile.Request, len(entryList.Items))
	for i, entry := range entryList.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: entry.Namespace,
				Name:      entry.Name,
			},
		}
	}
	return requests
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

const (
	// ToolConfigFinalizerName is the name of the finalizer for MCPToolConfig
	ToolConfigFinalizerName = "toolhive.stacklok.dev/toolconfig-finalizer"

	// finalizerRequeueDelay is the delay before requeuing after adding a finalizer
	finalizerRequeueDelay = 500 * time.Millisecond
)

// ToolConfigReconciler reconciles a MCPToolConfig object
type ToolConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptoolconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptoolconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptoolconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ToolConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPToolConfig instance
	toolConfig := &mcpv1beta1.MCPToolConfig{}
	err := r.Get(ctx, req.NamespacedName, toolConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			logger.Info("MCPToolConfig resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get MCPToolConfig")
		return ctrl.Result{}, err
	}

	// Check if the MCPToolConfig is being deleted
	if !toolConfig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, toolConfig)
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(toolConfig, ToolConfigFinalizerName) {
		controllerutil.AddFinalizer(toolConfig, ToolConfigFinalizerName)
		if err := r.Update(ctx, toolConfig); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		// Requeue to continue processing after finalizer is added
		return ctrl.Result{RequeueAfter: finalizerRequeueDelay}, nil
	}

	// Validation succeeded - set Valid=True condition
	conditionChanged := meta.SetStatusCondition(&toolConfig.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionToolConfigValid,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonToolConfigValidationSucceeded,
		Message:            "Spec validation passed",
		ObservedGeneration: toolConfig.Generation,
	})

	// Calculate the hash of the current configuration
	configHash := r.calculateConfigHash(toolConfig.Spec)

	// Check if the hash has changed
	hashChanged := toolConfig.Status.ConfigHash != configHash
	if hashChanged {
		return r.handleConfigHashChange(ctx, toolConfig, configHash)
	}

	// Refresh ReferencingWorkloads list
	referencingWorkloads, err := r.findReferencingWorkloads(ctx, toolConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing workloads")
	} else if !ctrlutil.WorkloadRefsEqual(toolConfig.Status.ReferencingWorkloads, referencingWorkloads) ||
		toolConfig.Status.ReferenceCount != workloadReferenceCount(referencingWorkloads) {
		toolConfig.Status.ReferencingWorkloads = referencingWorkloads
		toolConfig.Status.ReferenceCount = workloadReferenceCount(referencingWorkloads)
		conditionChanged = true
	}

	// Update condition if it changed (even without hash change)
	if conditionChanged {
		if err := r.Status().Update(ctx, toolConfig); err != nil {
			logger.Error(err, "Failed to update MCPToolConfig status after condition change")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// handleConfigHashChange handles the logic when the config hash changes
func (r *ToolConfigReconciler) handleConfigHashChange(
	ctx context.Context,
	toolConfig *mcpv1beta1.MCPToolConfig,
	configHash string,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("MCPToolConfig configuration changed", "oldHash", toolConfig.Status.ConfigHash, "newHash", configHash)

	// Find all MCPServers that reference this MCPToolConfig
	refs, err := r.findReferencingWorkloads(ctx, toolConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing MCPServers")
		// Don't persist the new hash on error; returning the error requeues so
		// the next reconcile recomputes referencing workloads.
		return ctrl.Result{}, fmt.Errorf("failed to find referencing MCPServers: %w", err)
	}

	// Update the status with the new hash only after successful server lookup
	toolConfig.Status.ConfigHash = configHash
	toolConfig.Status.ObservedGeneration = toolConfig.Generation

	toolConfig.Status.ReferencingWorkloads = refs
	toolConfig.Status.ReferenceCount = workloadReferenceCount(refs)

	// Update the MCPToolConfig status
	if err := r.Status().Update(ctx, toolConfig); err != nil {
		logger.Error(err, "Failed to update MCPToolConfig status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// calculateConfigHash calculates a hash of the MCPToolConfig spec using Kubernetes utilities
func (*ToolConfigReconciler) calculateConfigHash(spec mcpv1beta1.MCPToolConfigSpec) string {
	return ctrlutil.CalculateConfigHash(spec)
}

// handleDeletion handles the deletion of a MCPToolConfig
func (r *ToolConfigReconciler) handleDeletion(ctx context.Context, toolConfig *mcpv1beta1.MCPToolConfig) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(toolConfig, ToolConfigFinalizerName) {
		// Check if any workloads still reference this MCPToolConfig
		referencingWorkloads, err := r.findReferencingWorkloads(ctx, toolConfig)
		if err != nil {
			logger.Error(err, "Failed to check referencing workloads during deletion")
			return ctrl.Result{}, err
		}

		if len(referencingWorkloads) > 0 {
			logger.Info("MCPToolConfig is still referenced by workloads, blocking deletion",
				"toolconfig", toolConfig.Name,
				"referencingWorkloads", referencingWorkloads)

			meta.SetStatusCondition(&toolConfig.Status.Conditions, metav1.Condition{
				Type:               mcpv1beta1.ConditionTypeDeletionBlocked,
				Status:             metav1.ConditionTrue,
				Reason:             "ReferencedByWorkloads",
				Message:            fmt.Sprintf("Cannot delete: referenced by workloads: %v", referencingWorkloads),
				ObservedGeneration: toolConfig.Generation,
			})
			toolConfig.Status.ReferencingWorkloads = referencingWorkloads
			toolConfig.Status.ReferenceCount = workloadReferenceCount(referencingWorkloads)
			if updateErr := r.Status().Update(ctx, toolConfig); updateErr != nil {
				logger.Error(updateErr, "Failed to update status during deletion block")
			}

			// Requeue to check again later
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		// No references, safe to remove finalizer and allow deletion
		controllerutil.RemoveFinalizer(toolConfig, ToolConfigFinalizerName)
		if err := r.Update(ctx, toolConfig); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPToolConfig", "toolconfig", toolConfig.Name)
	}

	return ctrl.Result{}, nil
}

// toolConfigRefIndexKey is the field-index key backing findReferencingWorkloads.
// MCPServer references the config via spec.toolConfigRef; the index is registered
// in SetupWithManager.
const toolConfigRefIndexKey = "spec.toolConfigRef"

// indexMCPServerByToolConfigRef extracts the MCPToolConfig name an MCPServer
// references, for the field index. Returns nil when there is no reference so
// unreferencing servers are not indexed under the empty key.
func indexMCPServerByToolConfigRef(obj client.Object) []string {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok || server.Spec.ToolConfigRef == nil || server.Spec.ToolConfigRef.Name == "" {
		return nil
	}
	return []string{server.Spec.ToolConfigRef.Name}
}

// findReferencingWorkloads returns the workload resources (MCPServer)
// that reference this MCPToolConfig via their ToolConfigRef field.
//
// The lookup is served by a field index (registered in SetupWithManager) so the
// query returns only the referencing workloads instead of listing every workload
// in the namespace and filtering in memory.
func (r *ToolConfigReconciler) findReferencingWorkloads(
	ctx context.Context,
	toolConfig *mcpv1beta1.MCPToolConfig,
) ([]mcpv1beta1.WorkloadReference, error) {
	serverList := &mcpv1beta1.MCPServerList{}
	if err := r.List(ctx, serverList, client.InNamespace(toolConfig.Namespace),
		client.MatchingFields{toolConfigRefIndexKey: toolConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list MCPServers by toolConfigRef: %w", err)
	}
	refs := make([]mcpv1beta1.WorkloadReference, 0, len(serverList.Items))
	for i := range serverList.Items {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPServer, Name: serverList.Items[i].Name})
	}
	ctrlutil.SortWorkloadRefs(refs)
	return refs, nil
}

// SetupWithManager sets up the controller with the Manager.
// Watches MCPServer changes to maintain accurate ReferencingWorkloads status.
func (r *ToolConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Field index backing findReferencingWorkloads: lets the controller query only
	// the MCPServers referencing a given config rather than listing every MCPServer
	// in the namespace and filtering in memory.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.MCPServer{}, toolConfigRefIndexKey, indexMCPServerByToolConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up MCPServer toolConfigRef index: %w", err)
	}

	// Watch MCPServer changes to update ReferencingWorkloads on referenced MCPToolConfigs.
	// The map function only returns the MCPToolConfig the server currently references.
	// EnqueueRequestsFromMapFunc runs it on both the old and new object on update (and
	// on the object for create/delete), so removing or changing the ref enqueues both
	// the previously- and newly-referenced config — the previously-referenced config
	// then reconciles and prunes the stale entry. No manual stale-reference scan needed.
	//
	// GenerationChangedPredicate also suppresses the workload-watch resync; the self-heal
	// backstop for a stale ReferencingWorkloads entry (e.g. a workload deleted while the
	// operator was down) is this config's own For() resync, which re-runs Reconcile and
	// rebuilds ReferencingWorkloads from the index.
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1beta1.MCPToolConfig{}).
		Watches(&mcpv1beta1.MCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPServerToToolConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

// mapMCPServerToToolConfig maps an MCPServer to the MCPToolConfig it currently
// references. EnqueueRequestsFromMapFunc invokes this on both the old and new object on
// update (and on the object for create/delete), so a ref change or deletion automatically
// enqueues both the previously- and newly-referenced config; the previously-referenced
// config then prunes the stale entry on reconcile. No manual stale-reference scan needed.
func (*ToolConfigReconciler) mapMCPServerToToolConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok || server.Spec.ToolConfigRef == nil || server.Spec.ToolConfigRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Name:      server.Spec.ToolConfigRef.Name,
		Namespace: server.Namespace,
	}}}
}

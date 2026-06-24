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
	// TelemetryConfigFinalizerName is the name of the finalizer for MCPTelemetryConfig
	TelemetryConfigFinalizerName = "mcptelemetryconfig.toolhive.stacklok.dev/finalizer"

	// telemetryConfigRequeueDelay is the delay before requeuing after adding a finalizer
	telemetryConfigRequeueDelay = 500 * time.Millisecond
)

// MCPTelemetryConfigReconciler reconciles a MCPTelemetryConfig object.
//
// This controller manages the lifecycle of MCPTelemetryConfig resources: validation,
// config hash computation, finalizer management, reference tracking, and deletion protection.
type MCPTelemetryConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptelemetryconfigs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptelemetryconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptelemetryconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpservers,verbs=list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPTelemetryConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPTelemetryConfig instance
	telemetryConfig := &mcpv1beta1.MCPTelemetryConfig{}
	err := r.Get(ctx, req.NamespacedName, telemetryConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("MCPTelemetryConfig resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get MCPTelemetryConfig")
		return ctrl.Result{}, err
	}

	// Check if the MCPTelemetryConfig is being deleted
	if !telemetryConfig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, telemetryConfig)
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(telemetryConfig, TelemetryConfigFinalizerName) {
		controllerutil.AddFinalizer(telemetryConfig, TelemetryConfigFinalizerName)
		if err := r.Update(ctx, telemetryConfig); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: telemetryConfigRequeueDelay}, nil
	}

	// Validate spec configuration early
	if err := telemetryConfig.Validate(); err != nil {
		logger.Error(err, "MCPTelemetryConfig spec validation failed")
		meta.SetStatusCondition(&telemetryConfig.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeValid,
			Status:             metav1.ConditionFalse,
			Reason:             "ValidationFailed",
			Message:            err.Error(),
			ObservedGeneration: telemetryConfig.Generation,
		})
		if updateErr := r.Status().Update(ctx, telemetryConfig); updateErr != nil {
			logger.Error(updateErr, "Failed to update status after validation error")
		}
		return ctrl.Result{}, nil // Don't requeue on validation errors - user must fix spec
	}

	// Validation succeeded - set Valid=True condition
	conditionChanged := meta.SetStatusCondition(&telemetryConfig.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeValid,
		Status:             metav1.ConditionTrue,
		Reason:             "ValidationSucceeded",
		Message:            "Spec validation passed",
		ObservedGeneration: telemetryConfig.Generation,
	})

	// Calculate the hash of the current configuration
	configHash := r.calculateConfigHash(telemetryConfig.Spec)

	// Track referencing workloads
	referencingWorkloads, err := r.findReferencingWorkloads(ctx, telemetryConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing workloads")
		return ctrl.Result{}, err
	}

	// Check what changed
	hashChanged := telemetryConfig.Status.ConfigHash != configHash
	refsChanged := !ctrlutil.WorkloadRefsEqual(telemetryConfig.Status.ReferencingWorkloads, referencingWorkloads)
	needsUpdate := hashChanged || refsChanged || conditionChanged

	if hashChanged {
		logger.Info("MCPTelemetryConfig configuration changed",
			"oldHash", telemetryConfig.Status.ConfigHash,
			"newHash", configHash)
	}

	if needsUpdate {
		telemetryConfig.Status.ConfigHash = configHash
		telemetryConfig.Status.ObservedGeneration = telemetryConfig.Generation
		telemetryConfig.Status.ReferencingWorkloads = referencingWorkloads

		if err := r.Status().Update(ctx, telemetryConfig); err != nil {
			logger.Error(err, "Failed to update MCPTelemetryConfig status")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// Field-index keys backing findReferencingWorkloads. MCPServer, MCPRemoteProxy,
// and VirtualMCPServer all reference the config via a top-level
// spec.telemetryConfigRef. The indexes are registered in SetupWithManager.
const telemetryConfigRefIndexKey = "spec.telemetryConfigRef"

// indexMCPServerByTelemetryConfigRef extracts the MCPTelemetryConfig name an
// MCPServer references, for the field index. Returns nil when there is no
// reference so unreferencing servers are not indexed under the empty key.
func indexMCPServerByTelemetryConfigRef(obj client.Object) []string {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok || server.Spec.TelemetryConfigRef == nil || server.Spec.TelemetryConfigRef.Name == "" {
		return nil
	}
	return []string{server.Spec.TelemetryConfigRef.Name}
}

// indexMCPRemoteProxyByTelemetryConfigRef extracts the MCPTelemetryConfig name an
// MCPRemoteProxy references, for the field index.
func indexMCPRemoteProxyByTelemetryConfigRef(obj client.Object) []string {
	proxy, ok := obj.(*mcpv1beta1.MCPRemoteProxy)
	if !ok || proxy.Spec.TelemetryConfigRef == nil || proxy.Spec.TelemetryConfigRef.Name == "" {
		return nil
	}
	return []string{proxy.Spec.TelemetryConfigRef.Name}
}

// indexVirtualMCPServerByTelemetryConfigRef extracts the MCPTelemetryConfig name a
// VirtualMCPServer references via its top-level spec.telemetryConfigRef.
func indexVirtualMCPServerByTelemetryConfigRef(obj client.Object) []string {
	vmcp, ok := obj.(*mcpv1beta1.VirtualMCPServer)
	if !ok || vmcp.Spec.TelemetryConfigRef == nil || vmcp.Spec.TelemetryConfigRef.Name == "" {
		return nil
	}
	return []string{vmcp.Spec.TelemetryConfigRef.Name}
}

// SetupWithManager sets up the controller with the Manager.
// Watches MCPServer changes to maintain accurate ReferencingWorkloads status.
func (r *MCPTelemetryConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Field indexes backing findReferencingWorkloads: each lets the controller
	// query only the workloads referencing a given config rather than listing
	// every workload in the namespace and filtering in memory.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.MCPServer{}, telemetryConfigRefIndexKey, indexMCPServerByTelemetryConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up MCPServer telemetryConfigRef index: %w", err)
	}
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.MCPRemoteProxy{}, telemetryConfigRefIndexKey, indexMCPRemoteProxyByTelemetryConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up MCPRemoteProxy telemetryConfigRef index: %w", err)
	}
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.VirtualMCPServer{}, telemetryConfigRefIndexKey, indexVirtualMCPServerByTelemetryConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up VirtualMCPServer telemetryConfigRef index: %w", err)
	}

	// Watch MCPServer changes to update ReferencingWorkloads on referenced MCPTelemetryConfigs.
	// The map function only returns the MCPTelemetryConfig the server currently references.
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
		For(&mcpv1beta1.MCPTelemetryConfig{}).
		Watches(&mcpv1beta1.MCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPServerToTelemetryConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&mcpv1beta1.MCPRemoteProxy{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPRemoteProxyToTelemetryConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&mcpv1beta1.VirtualMCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapVirtualMCPServerToTelemetryConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Complete(r)
}

// mapMCPServerToTelemetryConfig maps an MCPServer to the MCPTelemetryConfig it currently
// references. EnqueueRequestsFromMapFunc invokes this on both the old and new object on
// update (and on the object for create/delete), so a ref change or deletion automatically
// enqueues both the previously- and newly-referenced config; the previously-referenced
// config then prunes the stale entry on reconcile. No manual stale-reference scan needed.
func (*MCPTelemetryConfigReconciler) mapMCPServerToTelemetryConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok || server.Spec.TelemetryConfigRef == nil || server.Spec.TelemetryConfigRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Name:      server.Spec.TelemetryConfigRef.Name,
		Namespace: server.Namespace,
	}}}
}

// mapMCPRemoteProxyToTelemetryConfig maps an MCPRemoteProxy to the MCPTelemetryConfig it
// currently references. EnqueueRequestsFromMapFunc invokes this on both the old and new
// object on update (and on the object for create/delete), so a ref change or deletion
// automatically enqueues both the previously- and newly-referenced config; the
// previously-referenced config then prunes the stale entry on reconcile. No manual
// stale-reference scan needed.
func (*MCPTelemetryConfigReconciler) mapMCPRemoteProxyToTelemetryConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	proxy, ok := obj.(*mcpv1beta1.MCPRemoteProxy)
	if !ok || proxy.Spec.TelemetryConfigRef == nil || proxy.Spec.TelemetryConfigRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Name:      proxy.Spec.TelemetryConfigRef.Name,
		Namespace: proxy.Namespace,
	}}}
}

// mapVirtualMCPServerToTelemetryConfig maps a VirtualMCPServer to the MCPTelemetryConfig
// it currently references. EnqueueRequestsFromMapFunc invokes this on both the old and new
// object on update (and on the object for create/delete), so a ref change or deletion
// automatically enqueues both the previously- and newly-referenced config; the
// previously-referenced config then prunes the stale entry on reconcile. No manual
// stale-reference scan needed.
func (*MCPTelemetryConfigReconciler) mapVirtualMCPServerToTelemetryConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	vmcp, ok := obj.(*mcpv1beta1.VirtualMCPServer)
	if !ok || vmcp.Spec.TelemetryConfigRef == nil || vmcp.Spec.TelemetryConfigRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Name:      vmcp.Spec.TelemetryConfigRef.Name,
		Namespace: vmcp.Namespace,
	}}}
}

// calculateConfigHash calculates a hash of the MCPTelemetryConfig spec using Kubernetes utilities
func (*MCPTelemetryConfigReconciler) calculateConfigHash(spec mcpv1beta1.MCPTelemetryConfigSpec) string {
	return ctrlutil.CalculateConfigHash(spec)
}

// handleDeletion handles the deletion of a MCPTelemetryConfig.
// Blocks deletion while MCPServer resources reference this config (deletion protection).
func (r *MCPTelemetryConfigReconciler) handleDeletion(
	ctx context.Context,
	telemetryConfig *mcpv1beta1.MCPTelemetryConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(telemetryConfig, TelemetryConfigFinalizerName) {
		return ctrl.Result{}, nil
	}

	// Check for referencing workloads before allowing deletion
	referencingWorkloads, err := r.findReferencingWorkloads(ctx, telemetryConfig)
	if err != nil {
		logger.Error(err, "Failed to check referencing workloads during deletion")
		return ctrl.Result{}, err
	}

	if len(referencingWorkloads) > 0 {
		names := make([]string, 0, len(referencingWorkloads))
		for _, ref := range referencingWorkloads {
			names = append(names, fmt.Sprintf("%s/%s", ref.Kind, ref.Name))
		}
		msg := fmt.Sprintf("cannot delete: still referenced by MCPServer(s): %v", names)
		logger.Info(msg, "telemetryConfig", telemetryConfig.Name)
		meta.SetStatusCondition(&telemetryConfig.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeDeletionBlocked,
			Status:             metav1.ConditionTrue,
			Reason:             "ReferencedByWorkloads",
			Message:            msg,
			ObservedGeneration: telemetryConfig.Generation,
		})
		// Ignore status update error — the object is being deleted
		_ = r.Status().Update(ctx, telemetryConfig)
		// Requeue to re-check after references are removed
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	controllerutil.RemoveFinalizer(telemetryConfig, TelemetryConfigFinalizerName)
	if err := r.Update(ctx, telemetryConfig); err != nil {
		logger.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}
	logger.Info("Removed finalizer from MCPTelemetryConfig", "telemetryConfig", telemetryConfig.Name)

	return ctrl.Result{}, nil
}

// findReferencingWorkloads returns a sorted list of workload references
// (MCPServer, MCPRemoteProxy, and VirtualMCPServer) in the same namespace that
// reference this MCPTelemetryConfig via TelemetryConfigRef.
//
// Each lookup is served by a field index (registered in SetupWithManager) so the
// query returns only the referencing workloads instead of listing every workload
// in the namespace and filtering in memory.
func (r *MCPTelemetryConfigReconciler) findReferencingWorkloads(
	ctx context.Context,
	telemetryConfig *mcpv1beta1.MCPTelemetryConfig,
) ([]mcpv1beta1.WorkloadReference, error) {
	var refs []mcpv1beta1.WorkloadReference

	serverList := &mcpv1beta1.MCPServerList{}
	if err := r.List(ctx, serverList, client.InNamespace(telemetryConfig.Namespace),
		client.MatchingFields{telemetryConfigRefIndexKey: telemetryConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list MCPServers by telemetryConfigRef: %w", err)
	}
	for i := range serverList.Items {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPServer, Name: serverList.Items[i].Name})
	}

	proxyList := &mcpv1beta1.MCPRemoteProxyList{}
	if err := r.List(ctx, proxyList, client.InNamespace(telemetryConfig.Namespace),
		client.MatchingFields{telemetryConfigRefIndexKey: telemetryConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies by telemetryConfigRef: %w", err)
	}
	for i := range proxyList.Items {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPRemoteProxy, Name: proxyList.Items[i].Name})
	}

	vmcpList := &mcpv1beta1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(telemetryConfig.Namespace),
		client.MatchingFields{telemetryConfigRefIndexKey: telemetryConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list VirtualMCPServers by telemetryConfigRef: %w", err)
	}
	for i := range vmcpList.Items {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindVirtualMCPServer, Name: vmcpList.Items[i].Name})
	}

	ctrlutil.SortWorkloadRefs(refs)
	return refs, nil
}

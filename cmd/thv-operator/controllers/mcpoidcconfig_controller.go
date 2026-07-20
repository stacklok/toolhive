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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
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
	// OIDCConfigFinalizerName is the name of the finalizer for MCPOIDCConfig
	OIDCConfigFinalizerName = "mcpoidcconfig.toolhive.stacklok.dev/finalizer"

	// oidcConfigRequeueDelay is the delay before requeuing after adding a finalizer
	oidcConfigRequeueDelay = 500 * time.Millisecond
)

// MCPOIDCConfigReconciler reconciles a MCPOIDCConfig object.
//
// This controller manages the lifecycle of MCPOIDCConfig resources: validation,
// config hash computation, finalizer management, reference tracking, and
// deletion protection when MCPServer resources reference this config.
type MCPOIDCConfigReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpremoteproxies,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPOIDCConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPOIDCConfig instance
	oidcConfig := &mcpv1beta1.MCPOIDCConfig{}
	err := r.Get(ctx, req.NamespacedName, oidcConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("MCPOIDCConfig resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get MCPOIDCConfig")
		return ctrl.Result{}, err
	}

	// Check if the MCPOIDCConfig is being deleted
	if !oidcConfig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, oidcConfig)
	}

	// Add finalizer if it doesn't exist.
	// MutateAndPatchSpec wraps an optimistic-lock merge patch: any concurrent
	// finalizer additions land on the live object via the apiserver, and our
	// patch only carries the field we changed. See .claude/rules/operator.md.
	if !controllerutil.ContainsFinalizer(oidcConfig, OIDCConfigFinalizerName) {
		if err := ctrlutil.MutateAndPatchSpec(ctx, r.Client, oidcConfig, func(c *mcpv1beta1.MCPOIDCConfig) {
			controllerutil.AddFinalizer(c, OIDCConfigFinalizerName)
		}); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: oidcConfigRequeueDelay}, nil
	}

	// Validate spec configuration early
	if err := oidcConfig.Validate(); err != nil {
		logger.Error(err, "MCPOIDCConfig spec validation failed")
		return r.handleValidationFailure(ctx, oidcConfig, err)
	}

	// Whether we are recovering from a prior validation failure. Captured before
	// the success patches below set Valid=True so a single Normal event fires on
	// the False->True transition.
	wasInvalid := conditionStatusIs(oidcConfig.Status.Conditions,
		mcpv1beta1.ConditionTypeOIDCConfigValid, metav1.ConditionFalse)

	// Calculate the hash of the current configuration
	configHash := r.calculateConfigHash(oidcConfig.Spec)

	// Check if the hash has changed
	hashChanged := oidcConfig.Status.ConfigHash != configHash
	if hashChanged {
		logger.Info("MCPOIDCConfig configuration changed",
			"oldHash", oidcConfig.Status.ConfigHash,
			"newHash", configHash)

		if err := ctrlutil.MutateAndPatchStatus(ctx, r.Client, oidcConfig, func(c *mcpv1beta1.MCPOIDCConfig) {
			setOIDCConfigValidTrueCondition(c)
			c.Status.ConfigHash = configHash
			c.Status.ObservedGeneration = c.Generation
		}); err != nil {
			logger.Error(err, "Failed to update MCPOIDCConfig status")
			return ctrl.Result{}, err
		}
		emitConfigRecoveryEvent(r.Recorder, oidcConfig, wasInvalid)
		return ctrl.Result{}, nil
	}

	// Refresh ReferencingWorkloads list. On error, fall through with the lookup
	// result skipped: the status patch below is best-effort and still ensures
	// the Valid=True condition is set even when the reference refresh fails.
	referencingWorkloads, findErr := r.findReferencingWorkloads(ctx, oidcConfig)
	if findErr != nil {
		logger.Error(findErr, "Failed to find referencing workloads")
	}

	// Single status patch covering the steady-state success path: ensure the
	// Valid=True condition is set, and refresh the references list when the
	// lookup succeeded and the list changed. MutateAndPatchStatus short-circuits
	// on an empty diff so the no-op case still skips the wire call
	// (SteadyStateNoOp behaviour is preserved).
	if err := ctrlutil.MutateAndPatchStatus(ctx, r.Client, oidcConfig, func(c *mcpv1beta1.MCPOIDCConfig) {
		setOIDCConfigValidTrueCondition(c)
		if findErr == nil &&
			(!ctrlutil.WorkloadRefsEqual(c.Status.ReferencingWorkloads, referencingWorkloads) ||
				c.Status.ReferenceCount != workloadReferenceCount(referencingWorkloads)) {
			c.Status.ReferencingWorkloads = referencingWorkloads
			c.Status.ReferenceCount = workloadReferenceCount(referencingWorkloads)
		}
	}); err != nil {
		logger.Error(err, "Failed to update MCPOIDCConfig status")
		return ctrl.Result{}, err
	}

	emitConfigRecoveryEvent(r.Recorder, oidcConfig, wasInvalid)
	return ctrl.Result{}, nil
}

// handleValidationFailure records the Valid=False condition for a spec that
// failed validation and emits a one-shot Warning event on the transition into
// the invalid state. It never requeues — the user must fix the spec.
func (r *MCPOIDCConfigReconciler) handleValidationFailure(
	ctx context.Context, oidcConfig *mcpv1beta1.MCPOIDCConfig, validationErr error,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	// Capture the transition before MutateAndPatchStatus mutates conditions in
	// place, so the Warning fires only when entering the invalid state rather
	// than on every reconcile that re-observes it.
	wasInvalid := conditionStatusIs(oidcConfig.Status.Conditions,
		mcpv1beta1.ConditionTypeOIDCConfigValid, metav1.ConditionFalse)
	updateErr := ctrlutil.MutateAndPatchStatus(ctx, r.Client, oidcConfig, func(c *mcpv1beta1.MCPOIDCConfig) {
		meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeOIDCConfigValid,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonOIDCConfigInvalid,
			Message:            validationErr.Error(),
			ObservedGeneration: c.Generation,
		})
	})
	if updateErr != nil {
		logger.Error(updateErr, "Failed to update status after validation error")
	}
	// Emit the Warning only on the transition into the invalid state, and only
	// once the condition persisted — a status write that keeps failing would
	// otherwise re-fire the event on every reconcile.
	if !wasInvalid && updateErr == nil {
		emitConfigEvent(r.Recorder, oidcConfig, corev1.EventTypeWarning,
			eventReasonConfigInvalid, eventActionValidate, "spec validation failed: %s", validationErr.Error())
	}
	return ctrl.Result{}, nil // Don't requeue on validation errors - user must fix spec
}

// setOIDCConfigValidTrueCondition stamps ConditionTypeOIDCConfigValid=True onto
// the supplied object. It is callable inside a MutateAndPatchStatus closure: the
// closure receives the freshly-snapshotted object, and SetStatusCondition only
// mutates Conditions when the desired state differs, so a no-op reconcile
// produces an empty patch body that the helper skips.
func setOIDCConfigValidTrueCondition(c *mcpv1beta1.MCPOIDCConfig) {
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeOIDCConfigValid,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonOIDCConfigValid,
		Message:            "Spec validation passed",
		ObservedGeneration: c.Generation,
	})
}

// calculateConfigHash calculates a hash of the MCPOIDCConfig spec using Kubernetes utilities
func (*MCPOIDCConfigReconciler) calculateConfigHash(spec mcpv1beta1.MCPOIDCConfigSpec) string {
	return ctrlutil.CalculateConfigHash(spec)
}

// handleDeletion handles the deletion of a MCPOIDCConfig.
// Blocks deletion while MCPServer resources reference this config by keeping the
// finalizer and requeueing. Once all references are removed, the finalizer is removed
// and the resource can be garbage collected.
func (r *MCPOIDCConfigReconciler) handleDeletion(
	ctx context.Context,
	oidcConfig *mcpv1beta1.MCPOIDCConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(oidcConfig, OIDCConfigFinalizerName) {
		// Check if any workloads still reference this config
		referencingWorkloads, err := r.findReferencingWorkloads(ctx, oidcConfig)
		if err != nil {
			logger.Error(err, "Failed to check referencing workloads during deletion")
			return ctrl.Result{}, err
		}

		if len(referencingWorkloads) > 0 {
			logger.Info("MCPOIDCConfig is still referenced by workloads, blocking deletion",
				"oidcConfig", oidcConfig.Name,
				"referencingWorkloads", referencingWorkloads)

			// Capture the transition before the patch mutates conditions in
			// place: emit the Warning only when entering the blocked state.
			wasBlocked := conditionStatusIs(oidcConfig.Status.Conditions,
				mcpv1beta1.ConditionTypeDeletionBlocked, metav1.ConditionTrue)
			updateErr := ctrlutil.MutateAndPatchStatus(ctx, r.Client, oidcConfig, func(c *mcpv1beta1.MCPOIDCConfig) {
				meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
					Type:               mcpv1beta1.ConditionTypeDeletionBlocked,
					Status:             metav1.ConditionTrue,
					Reason:             "ReferencedByWorkloads",
					Message:            fmt.Sprintf("Cannot delete: referenced by workloads: %v", referencingWorkloads),
					ObservedGeneration: c.Generation,
				})
				c.Status.ReferencingWorkloads = referencingWorkloads
				c.Status.ReferenceCount = workloadReferenceCount(referencingWorkloads)
			})
			if updateErr != nil {
				logger.Error(updateErr, "Failed to update status during deletion block")
			}
			// Emit the Warning only on the transition into the blocked state, and
			// only once the condition persisted, so a failing status write does
			// not re-fire the event every reconcile.
			if !wasBlocked && updateErr == nil {
				emitConfigEvent(r.Recorder, oidcConfig, corev1.EventTypeWarning,
					eventReasonDeletionBlocked, eventActionDelete,
					"deletion blocked while still referenced by workloads: %v", referencingWorkloads)
			}

			// Requeue to check again later
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		if err := ctrlutil.MutateAndPatchSpec(ctx, r.Client, oidcConfig, func(c *mcpv1beta1.MCPOIDCConfig) {
			controllerutil.RemoveFinalizer(c, OIDCConfigFinalizerName)
		}); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPOIDCConfig", "oidcConfig", oidcConfig.Name)
	}

	return ctrl.Result{}, nil
}

// Field-index keys backing findReferencingWorkloads. MCPServer and MCPRemoteProxy
// both reference the config via spec.oidcConfigRef; VirtualMCPServer nests it
// under spec.incomingAuth. The indexes are registered in SetupWithManager.
const (
	oidcConfigRefIndexKey     = "spec.oidcConfigRef"
	vmcpOIDCConfigRefIndexKey = "spec.incomingAuth.oidcConfigRef"
)

// indexMCPServerByOIDCConfigRef extracts the MCPOIDCConfig name an MCPServer
// references, for the field index. Returns nil when there is no reference so
// unreferencing servers are not indexed under the empty key.
func indexMCPServerByOIDCConfigRef(obj client.Object) []string {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok || server.Spec.OIDCConfigRef == nil || server.Spec.OIDCConfigRef.Name == "" {
		return nil
	}
	return []string{server.Spec.OIDCConfigRef.Name}
}

// indexVirtualMCPServerByOIDCConfigRef extracts the MCPOIDCConfig name a
// VirtualMCPServer references via spec.incomingAuth, for the field index.
func indexVirtualMCPServerByOIDCConfigRef(obj client.Object) []string {
	vmcp, ok := obj.(*mcpv1beta1.VirtualMCPServer)
	if !ok || vmcp.Spec.IncomingAuth == nil ||
		vmcp.Spec.IncomingAuth.OIDCConfigRef == nil || vmcp.Spec.IncomingAuth.OIDCConfigRef.Name == "" {
		return nil
	}
	return []string{vmcp.Spec.IncomingAuth.OIDCConfigRef.Name}
}

// indexMCPRemoteProxyByOIDCConfigRef extracts the MCPOIDCConfig name an
// MCPRemoteProxy references, for the field index.
func indexMCPRemoteProxyByOIDCConfigRef(obj client.Object) []string {
	proxy, ok := obj.(*mcpv1beta1.MCPRemoteProxy)
	if !ok || proxy.Spec.OIDCConfigRef == nil || proxy.Spec.OIDCConfigRef.Name == "" {
		return nil
	}
	return []string{proxy.Spec.OIDCConfigRef.Name}
}

// findReferencingWorkloads returns the workload resources (MCPServer, VirtualMCPServer, and MCPRemoteProxy)
// that reference this MCPOIDCConfig via their OIDCConfigRef field.
//
// Each lookup is served by a field index (registered in SetupWithManager) so the
// query returns only the referencing workloads instead of listing every workload
// in the namespace and filtering in memory.
func (r *MCPOIDCConfigReconciler) findReferencingWorkloads(
	ctx context.Context,
	oidcConfig *mcpv1beta1.MCPOIDCConfig,
) ([]mcpv1beta1.WorkloadReference, error) {
	var refs []mcpv1beta1.WorkloadReference

	serverList := &mcpv1beta1.MCPServerList{}
	if err := r.List(ctx, serverList, client.InNamespace(oidcConfig.Namespace),
		client.MatchingFields{oidcConfigRefIndexKey: oidcConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list MCPServers by oidcConfigRef: %w", err)
	}
	for i := range serverList.Items {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPServer, Name: serverList.Items[i].Name})
	}

	vmcpList := &mcpv1beta1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(oidcConfig.Namespace),
		client.MatchingFields{vmcpOIDCConfigRefIndexKey: oidcConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list VirtualMCPServers by oidcConfigRef: %w", err)
	}
	for i := range vmcpList.Items {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindVirtualMCPServer, Name: vmcpList.Items[i].Name})
	}

	proxyList := &mcpv1beta1.MCPRemoteProxyList{}
	if err := r.List(ctx, proxyList, client.InNamespace(oidcConfig.Namespace),
		client.MatchingFields{oidcConfigRefIndexKey: oidcConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies by oidcConfigRef: %w", err)
	}
	for i := range proxyList.Items {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPRemoteProxy, Name: proxyList.Items[i].Name})
	}

	ctrlutil.SortWorkloadRefs(refs)
	return refs, nil
}

// SetupWithManager sets up the controller with the Manager.
// Watches MCPServer, VirtualMCPServer, and MCPRemoteProxy changes to maintain accurate ReferencingWorkloads status.
func (r *MCPOIDCConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Field indexes backing findReferencingWorkloads: each lets the controller
	// query only the workloads referencing a given config rather than listing
	// every workload in the namespace and filtering in memory.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.MCPServer{}, oidcConfigRefIndexKey, indexMCPServerByOIDCConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up MCPServer oidcConfigRef index: %w", err)
	}
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.VirtualMCPServer{}, vmcpOIDCConfigRefIndexKey, indexVirtualMCPServerByOIDCConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up VirtualMCPServer oidcConfigRef index: %w", err)
	}
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.MCPRemoteProxy{}, oidcConfigRefIndexKey, indexMCPRemoteProxyByOIDCConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up MCPRemoteProxy oidcConfigRef index: %w", err)
	}

	// Watch MCPServer changes to update ReferencingWorkloads on referenced MCPOIDCConfigs.
	// The map function only returns the MCPOIDCConfig the server currently references.
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
		For(&mcpv1beta1.MCPOIDCConfig{}).
		Watches(&mcpv1beta1.MCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPServerToOIDCConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&mcpv1beta1.VirtualMCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapVirtualMCPServerToOIDCConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&mcpv1beta1.MCPRemoteProxy{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPRemoteProxyToOIDCConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Complete(r)
}

// mapMCPServerToOIDCConfig maps an MCPServer to the MCPOIDCConfig it currently
// references. EnqueueRequestsFromMapFunc invokes this on both the old and new object on
// update (and on the object for create/delete), so a ref change or deletion automatically
// enqueues both the previously- and newly-referenced config; the previously-referenced
// config then prunes the stale entry on reconcile. No manual stale-reference scan needed.
func (*MCPOIDCConfigReconciler) mapMCPServerToOIDCConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok || server.Spec.OIDCConfigRef == nil || server.Spec.OIDCConfigRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Name:      server.Spec.OIDCConfigRef.Name,
		Namespace: server.Namespace,
	}}}
}

// mapVirtualMCPServerToOIDCConfig maps a VirtualMCPServer to the MCPOIDCConfig it
// currently references via spec.incomingAuth. EnqueueRequestsFromMapFunc invokes this
// on both the old and new object on update (and on the object for create/delete), so a
// ref change or deletion automatically enqueues both the previously- and newly-referenced
// config; the previously-referenced config then prunes the stale entry on reconcile. No
// manual stale-reference scan needed.
func (*MCPOIDCConfigReconciler) mapVirtualMCPServerToOIDCConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	vmcp, ok := obj.(*mcpv1beta1.VirtualMCPServer)
	if !ok || vmcp.Spec.IncomingAuth == nil ||
		vmcp.Spec.IncomingAuth.OIDCConfigRef == nil || vmcp.Spec.IncomingAuth.OIDCConfigRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Name:      vmcp.Spec.IncomingAuth.OIDCConfigRef.Name,
		Namespace: vmcp.Namespace,
	}}}
}

// mapMCPRemoteProxyToOIDCConfig maps an MCPRemoteProxy to the MCPOIDCConfig it currently
// references. EnqueueRequestsFromMapFunc invokes this on both the old and new object on
// update (and on the object for create/delete), so a ref change or deletion automatically
// enqueues both the previously- and newly-referenced config; the previously-referenced
// config then prunes the stale entry on reconcile. No manual stale-reference scan needed.
func (*MCPOIDCConfigReconciler) mapMCPRemoteProxyToOIDCConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	proxy, ok := obj.(*mcpv1beta1.MCPRemoteProxy)
	if !ok || proxy.Spec.OIDCConfigRef == nil || proxy.Spec.OIDCConfigRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Name:      proxy.Spec.OIDCConfigRef.Name,
		Namespace: proxy.Namespace,
	}}}
}

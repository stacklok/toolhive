// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"encoding/json"
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
	// AuthzConfigFinalizerName is the name of the finalizer for MCPAuthzConfig
	AuthzConfigFinalizerName = "mcpauthzconfig.toolhive.stacklok.dev/finalizer"

	// authzConfigRequeueDelay is the delay before requeuing after adding a finalizer
	authzConfigRequeueDelay = 500 * time.Millisecond
)

// MCPAuthzConfigReconciler reconciles a MCPAuthzConfig object.
//
// This controller manages the lifecycle of MCPAuthzConfig resources: validation
// via the authorizer factory registry, config hash computation, finalizer management,
// reference tracking, and deletion protection when workloads reference this config.
type MCPAuthzConfigReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpauthzconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpauthzconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpauthzconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpremoteproxies,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPAuthzConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPAuthzConfig instance
	authzConfig := &mcpv1beta1.MCPAuthzConfig{}
	err := r.Get(ctx, req.NamespacedName, authzConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("MCPAuthzConfig resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get MCPAuthzConfig")
		return ctrl.Result{}, err
	}

	// Check if the MCPAuthzConfig is being deleted
	if !authzConfig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, authzConfig)
	}

	// Add finalizer if it doesn't exist.
	// MutateAndPatchSpec wraps an optimistic-lock merge patch: any concurrent
	// finalizer additions land on the live object via the apiserver, and our
	// patch only carries the field we changed. See .claude/rules/operator.md.
	if !controllerutil.ContainsFinalizer(authzConfig, AuthzConfigFinalizerName) {
		if err := ctrlutil.MutateAndPatchSpec(ctx, r.Client, authzConfig, func(c *mcpv1beta1.MCPAuthzConfig) {
			controllerutil.AddFinalizer(c, AuthzConfigFinalizerName)
		}); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: authzConfigRequeueDelay}, nil
	}

	// Validate the authz configuration: structural checks via the type's Validate()
	// method, then backend-specific validation via the authorizer factory registry.
	if err := r.validateAuthzConfig(authzConfig); err != nil {
		logger.Error(err, "MCPAuthzConfig spec validation failed")
		// Capture the transition before MutateAndPatchStatus mutates conditions
		// in place, so the Warning fires only when entering the invalid state.
		wasInvalid := conditionStatusIs(authzConfig.Status.Conditions,
			mcpv1beta1.ConditionTypeAuthzConfigValid, metav1.ConditionFalse)
		updateErr := ctrlutil.MutateAndPatchStatus(ctx, r.Client, authzConfig, func(c *mcpv1beta1.MCPAuthzConfig) {
			meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
				Type:               mcpv1beta1.ConditionTypeAuthzConfigValid,
				Status:             metav1.ConditionFalse,
				Reason:             mcpv1beta1.ConditionReasonAuthzConfigInvalid,
				Message:            err.Error(),
				ObservedGeneration: c.Generation,
			})
		})
		if updateErr != nil {
			logger.Error(updateErr, "Failed to update status after validation error")
		}
		// Emit the Warning only on the transition into the invalid state, and
		// only once the condition persisted, so a failing status write does not
		// re-fire the event every reconcile.
		if !wasInvalid && updateErr == nil {
			emitConfigEvent(r.Recorder, authzConfig, corev1.EventTypeWarning,
				eventReasonConfigInvalid, eventActionValidate, "spec validation failed: %s", err.Error())
		}
		return ctrl.Result{}, nil // Don't requeue on validation errors - user must fix spec
	}

	// Whether we are recovering from a prior validation failure. Captured before
	// the success patch below sets Valid=True so a single Normal event fires on
	// the False->True transition.
	wasInvalid := conditionStatusIs(authzConfig.Status.Conditions,
		mcpv1beta1.ConditionTypeAuthzConfigValid, metav1.ConditionFalse)

	// Refresh the referencing workloads list. If the lookup fails we must
	// requeue with backoff rather than continuing with a stale slice — a
	// silent swallow would let ReferencingWorkloads / ReferenceCount drift
	// permanently out of sync with the cluster on a transient apiserver
	// hiccup.
	referencingWorkloads, err := r.findReferencingWorkloads(ctx, authzConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing workloads")
		return ctrl.Result{}, err
	}
	newRefCount := workloadReferenceCount(referencingWorkloads)

	// Calculate the hash of the current configuration.
	// The spec is canonicalized first so that two semantically-equal configs
	// that differ only in whitespace or JSON key order produce the same hash —
	// otherwise a noop kubectl-apply round trip can re-emit Spec.Config.Raw
	// with different bytes and flip the hash, causing spurious status writes.
	canonicalSpec := canonicalizeSpecForHash(authzConfig.Spec)
	configHash := ctrlutil.CalculateConfigHash(canonicalSpec)
	if authzConfig.Status.ConfigHash != configHash {
		// Routine spec transitions log at DEBUG per the silent-success rule
		// in .claude/rules/go-style.md.
		logger.V(1).Info("MCPAuthzConfig configuration changed",
			"oldHash", authzConfig.Status.ConfigHash,
			"newHash", configHash)
	}

	// Single status patch covering the steady-state success path: Valid=True,
	// fresh hash + observedGeneration, fresh references, and DeletionBlocked
	// cleared (we only reach this path when DeletionTimestamp is zero).
	// MutateAndPatchStatus short-circuits on an empty diff, so a no-op
	// reconcile still produces no wire call (SteadyStateNoOp behaviour
	// preserved).
	if err := ctrlutil.MutateAndPatchStatus(ctx, r.Client, authzConfig, func(c *mcpv1beta1.MCPAuthzConfig) {
		setValidTrueCondition(c)
		meta.RemoveStatusCondition(&c.Status.Conditions, mcpv1beta1.ConditionTypeDeletionBlocked)
		c.Status.ConfigHash = configHash
		c.Status.ObservedGeneration = c.Generation
		c.Status.ReferencingWorkloads = referencingWorkloads
		c.Status.ReferenceCount = newRefCount
	}); err != nil {
		logger.Error(err, "Failed to update MCPAuthzConfig status")
		return ctrl.Result{}, err
	}

	emitConfigRecoveryEvent(r.Recorder, authzConfig, wasInvalid)
	return ctrl.Result{}, nil
}

// setValidTrueCondition stamps ConditionTypeAuthzConfigValid=True onto the
// supplied object. It is callable inside a MutateAndPatchStatus closure: the
// closure receives the freshly-snapshotted object, and SetStatusCondition
// only mutates Conditions when the desired state differs, so a no-op
// reconcile produces an empty patch body that the helper skips.
func setValidTrueCondition(c *mcpv1beta1.MCPAuthzConfig) {
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeAuthzConfigValid,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonAuthzConfigValid,
		Message:            "Spec validation passed",
		ObservedGeneration: c.Generation,
	})
}

// validateAuthzConfig validates the MCPAuthzConfig. It first runs the structural
// validation on the type (Validate()), then reconstructs the full authorizer config
// and delegates backend-specific validation to the factory's ValidateConfig method.
//
// The type's Validate() handles structural checks and verifies that spec.type is
// a registered backend. Backend-specific schema validation lives here because
// the factory's ValidateConfig consumes the full reconstructed JSON envelope
// (version + type + nested config), which it is not the type's responsibility
// to build.
func (*MCPAuthzConfigReconciler) validateAuthzConfig(authzConfig *mcpv1beta1.MCPAuthzConfig) error {
	if err := authzConfig.Validate(); err != nil {
		return err
	}

	// BuildFullAuthzConfigJSON returns the registered factory alongside the
	// envelope, so we don't have to re-Unmarshal the JSON or re-look-up the
	// factory just to dispatch ValidateConfig. It lives in controllerutil so the
	// workload controllers can reuse it without an import cycle.
	fullConfigJSON, factory, err := ctrlutil.BuildFullAuthzConfigJSON(authzConfig.Spec)
	if err != nil {
		return err
	}
	return factory.ValidateConfig(fullConfigJSON)
}

// canonicalizeSpecForHash returns a copy of spec whose Config.Raw has been
// re-marshalled into canonical JSON form (sorted keys, no extra whitespace).
// The returned value is suitable for ctrlutil.CalculateConfigHash and produces
// the same hash for two specs that are semantically equal even if their raw
// bytes differ (whitespace, key ordering, duplicate keys collapsed by Go's
// encoder).
//
// If Config.Raw cannot be unmarshalled (malformed JSON), the original spec is
// returned unchanged — Validate() / validateAuthzConfig will surface the real
// error on the next reconcile path. The Spec passed in is never mutated.
func canonicalizeSpecForHash(spec mcpv1beta1.MCPAuthzConfigSpec) mcpv1beta1.MCPAuthzConfigSpec {
	if len(spec.Config.Raw) == 0 {
		return spec
	}
	var parsed any
	if err := json.Unmarshal(spec.Config.Raw, &parsed); err != nil {
		return spec
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return spec
	}
	out := spec
	out.Config = runtime.RawExtension{Raw: canonical}
	return out
}

// handleDeletion handles the deletion of a MCPAuthzConfig.
// Blocks deletion while workload resources reference this config by keeping the
// finalizer and requeueing. Once all references are removed, the finalizer is removed
// and the resource can be garbage collected.
func (r *MCPAuthzConfigReconciler) handleDeletion(
	ctx context.Context,
	authzConfig *mcpv1beta1.MCPAuthzConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(authzConfig, AuthzConfigFinalizerName) {
		// Check if any workloads still reference this config
		referencingWorkloads, err := r.findReferencingWorkloads(ctx, authzConfig)
		if err != nil {
			logger.Error(err, "Failed to check referencing workloads during deletion")
			return ctrl.Result{}, err
		}

		if len(referencingWorkloads) > 0 {
			logger.Info("MCPAuthzConfig is still referenced by workloads, blocking deletion",
				"authzConfig", authzConfig.Name,
				"referencingWorkloads", referencingWorkloads)

			// Capture the transition before the patch mutates conditions in
			// place: emit the Warning only when entering the blocked state.
			wasBlocked := conditionStatusIs(authzConfig.Status.Conditions,
				mcpv1beta1.ConditionTypeDeletionBlocked, metav1.ConditionTrue)
			updateErr := ctrlutil.MutateAndPatchStatus(ctx, r.Client, authzConfig, func(c *mcpv1beta1.MCPAuthzConfig) {
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
				emitConfigEvent(r.Recorder, authzConfig, corev1.EventTypeWarning,
					eventReasonDeletionBlocked, eventActionDelete,
					"deletion blocked while still referenced by workloads: %v", referencingWorkloads)
			}

			// Requeue to check again later
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		if err := ctrlutil.MutateAndPatchSpec(ctx, r.Client, authzConfig, func(c *mcpv1beta1.MCPAuthzConfig) {
			controllerutil.RemoveFinalizer(c, AuthzConfigFinalizerName)
		}); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPAuthzConfig", "authzConfig", authzConfig.Name)
	}

	return ctrl.Result{}, nil
}

// Field-index keys backing findReferencingWorkloads. MCPServer and MCPRemoteProxy
// both reference the config via spec.authzConfigRef; VirtualMCPServer nests it
// under spec.incomingAuth. The indexes are registered in SetupWithManager.
const (
	authzConfigRefIndexKey     = "spec.authzConfigRef"
	vmcpAuthzConfigRefIndexKey = "spec.incomingAuth.authzConfigRef"
)

// indexMCPServerByAuthzConfigRef extracts the MCPAuthzConfig name an MCPServer
// references, for the field index. Returns nil when there is no reference so
// unreferencing servers are not indexed under the empty key.
func indexMCPServerByAuthzConfigRef(obj client.Object) []string {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok || server.Spec.AuthzConfigRef == nil || server.Spec.AuthzConfigRef.Name == "" {
		return nil
	}
	return []string{server.Spec.AuthzConfigRef.Name}
}

// indexMCPRemoteProxyByAuthzConfigRef extracts the MCPAuthzConfig name an
// MCPRemoteProxy references, for the field index.
func indexMCPRemoteProxyByAuthzConfigRef(obj client.Object) []string {
	proxy, ok := obj.(*mcpv1beta1.MCPRemoteProxy)
	if !ok || proxy.Spec.AuthzConfigRef == nil || proxy.Spec.AuthzConfigRef.Name == "" {
		return nil
	}
	return []string{proxy.Spec.AuthzConfigRef.Name}
}

// indexVirtualMCPServerByAuthzConfigRef extracts the MCPAuthzConfig name a
// VirtualMCPServer references via spec.incomingAuth, for the field index.
func indexVirtualMCPServerByAuthzConfigRef(obj client.Object) []string {
	vmcp, ok := obj.(*mcpv1beta1.VirtualMCPServer)
	if !ok || vmcp.Spec.IncomingAuth == nil ||
		vmcp.Spec.IncomingAuth.AuthzConfigRef == nil || vmcp.Spec.IncomingAuth.AuthzConfigRef.Name == "" {
		return nil
	}
	return []string{vmcp.Spec.IncomingAuth.AuthzConfigRef.Name}
}

// findReferencingWorkloads returns the workload resources (MCPServer, VirtualMCPServer,
// and MCPRemoteProxy) that reference this MCPAuthzConfig via their AuthzConfigRef field.
//
// Each lookup is served by a field index (registered in SetupWithManager) so the
// query returns only the referencing workloads instead of listing every workload
// in the namespace and filtering in memory.
func (r *MCPAuthzConfigReconciler) findReferencingWorkloads(
	ctx context.Context,
	authzConfig *mcpv1beta1.MCPAuthzConfig,
) ([]mcpv1beta1.WorkloadReference, error) {
	var refs []mcpv1beta1.WorkloadReference

	serverList := &mcpv1beta1.MCPServerList{}
	if err := r.List(ctx, serverList, client.InNamespace(authzConfig.Namespace),
		client.MatchingFields{authzConfigRefIndexKey: authzConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list MCPServers by authzConfigRef: %w", err)
	}
	for i := range serverList.Items {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPServer, Name: serverList.Items[i].Name})
	}

	vmcpList := &mcpv1beta1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(authzConfig.Namespace),
		client.MatchingFields{vmcpAuthzConfigRefIndexKey: authzConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list VirtualMCPServers by authzConfigRef: %w", err)
	}
	for i := range vmcpList.Items {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindVirtualMCPServer, Name: vmcpList.Items[i].Name})
	}

	proxyList := &mcpv1beta1.MCPRemoteProxyList{}
	if err := r.List(ctx, proxyList, client.InNamespace(authzConfig.Namespace),
		client.MatchingFields{authzConfigRefIndexKey: authzConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies by authzConfigRef: %w", err)
	}
	for i := range proxyList.Items {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPRemoteProxy, Name: proxyList.Items[i].Name})
	}

	ctrlutil.SortWorkloadRefs(refs)
	return refs, nil
}

// SetupWithManager sets up the controller with the Manager.
// Watches MCPServer, VirtualMCPServer, and MCPRemoteProxy changes to maintain
// accurate ReferencingWorkloads status.
func (r *MCPAuthzConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Field indexes backing findReferencingWorkloads: each lets the controller
	// query only the workloads referencing a given config rather than listing
	// every workload in the namespace and filtering in memory.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.MCPServer{}, authzConfigRefIndexKey, indexMCPServerByAuthzConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up MCPServer authzConfigRef index: %w", err)
	}
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.MCPRemoteProxy{}, authzConfigRefIndexKey, indexMCPRemoteProxyByAuthzConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up MCPRemoteProxy authzConfigRef index: %w", err)
	}
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.VirtualMCPServer{}, vmcpAuthzConfigRefIndexKey, indexVirtualMCPServerByAuthzConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up VirtualMCPServer authzConfigRef index: %w", err)
	}

	// GenerationChangedPredicate also suppresses the workload-watch resync; the self-heal
	// backstop for a stale ReferencingWorkloads entry (e.g. a workload deleted while the
	// operator was down) is this config's own For() resync, which re-runs Reconcile and
	// rebuilds ReferencingWorkloads from the index.
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1beta1.MCPAuthzConfig{}).
		Watches(&mcpv1beta1.MCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPServerToAuthzConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&mcpv1beta1.VirtualMCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapVirtualMCPServerToAuthzConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&mcpv1beta1.MCPRemoteProxy{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPRemoteProxyToAuthzConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

// mapMCPServerToAuthzConfig maps an MCPServer to the MCPAuthzConfig it currently references.
// EnqueueRequestsFromMapFunc invokes this on both the old and new object on update (and on the
// object for create/delete), so a ref change or deletion automatically enqueues both the
// previously- and newly-referenced config; the previously-referenced config then prunes the
// stale entry on reconcile. No manual stale-reference scan needed.
func (*MCPAuthzConfigReconciler) mapMCPServerToAuthzConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok || server.Spec.AuthzConfigRef == nil || server.Spec.AuthzConfigRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Name:      server.Spec.AuthzConfigRef.Name,
		Namespace: server.Namespace,
	}}}
}

// mapVirtualMCPServerToAuthzConfig maps a VirtualMCPServer to the MCPAuthzConfig it currently
// references via spec.incomingAuth. EnqueueRequestsFromMapFunc invokes this on both the old and
// new object on update (and on the object for create/delete), so a ref change or deletion
// automatically enqueues both the previously- and newly-referenced config; the previously-
// referenced config then prunes the stale entry on reconcile. No manual stale-reference scan needed.
func (*MCPAuthzConfigReconciler) mapVirtualMCPServerToAuthzConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	vmcp, ok := obj.(*mcpv1beta1.VirtualMCPServer)
	if !ok || vmcp.Spec.IncomingAuth == nil ||
		vmcp.Spec.IncomingAuth.AuthzConfigRef == nil || vmcp.Spec.IncomingAuth.AuthzConfigRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Name:      vmcp.Spec.IncomingAuth.AuthzConfigRef.Name,
		Namespace: vmcp.Namespace,
	}}}
}

// mapMCPRemoteProxyToAuthzConfig maps an MCPRemoteProxy to the MCPAuthzConfig it currently
// references. EnqueueRequestsFromMapFunc invokes this on both the old and new object on update
// (and on the object for create/delete), so a ref change or deletion automatically enqueues both
// the previously- and newly-referenced config; the previously-referenced config then prunes the
// stale entry on reconcile. No manual stale-reference scan needed.
func (*MCPAuthzConfigReconciler) mapMCPRemoteProxyToAuthzConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	proxy, ok := obj.(*mcpv1beta1.MCPRemoteProxy)
	if !ok || proxy.Spec.AuthzConfigRef == nil || proxy.Spec.AuthzConfigRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Name:      proxy.Spec.AuthzConfigRef.Name,
		Namespace: proxy.Namespace,
	}}}
}

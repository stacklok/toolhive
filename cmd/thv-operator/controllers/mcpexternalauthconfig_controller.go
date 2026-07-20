// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	stderrors "errors"
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
	"github.com/stacklok/toolhive/pkg/auth/obo"
)

const (
	// ExternalAuthConfigFinalizerName is the name of the finalizer for MCPExternalAuthConfig
	ExternalAuthConfigFinalizerName = "mcpexternalauthconfig.toolhive.stacklok.dev/finalizer"

	// externalAuthConfigRequeueDelay is the delay before requeuing after adding a finalizer
	externalAuthConfigRequeueDelay = 500 * time.Millisecond

	// authServerRefKindMCPExternalAuthConfig is the Kind value on a TypedLocalObjectReference
	// that identifies the ref as pointing to an MCPExternalAuthConfig resource.
	authServerRefKindMCPExternalAuthConfig = "MCPExternalAuthConfig"
)

// MCPExternalAuthConfigReconciler reconciles a MCPExternalAuthConfig object
type MCPExternalAuthConfigReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpexternalauthconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpexternalauthconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpexternalauthconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
//nolint:gocyclo // dispatches across all auth types + status conditions; refactoring tracked separately
func (r *MCPExternalAuthConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPExternalAuthConfig instance
	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{}
	err := r.Get(ctx, req.NamespacedName, externalAuthConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			logger.Info("MCPExternalAuthConfig resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get MCPExternalAuthConfig")
		return ctrl.Result{}, err
	}

	// Check if the MCPExternalAuthConfig is being deleted
	if !externalAuthConfig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, externalAuthConfig)
	}

	// Add finalizer if it doesn't exist.
	// MutateAndPatchSpec wraps an optimistic-lock merge patch: any concurrent
	// finalizer additions land on the live object via the apiserver, and our
	// patch only carries the field we changed. See .claude/rules/operator.md.
	if !controllerutil.ContainsFinalizer(externalAuthConfig, ExternalAuthConfigFinalizerName) {
		if err := ctrlutil.MutateAndPatchSpec(ctx, r.Client, externalAuthConfig, func(c *mcpv1beta1.MCPExternalAuthConfig) {
			controllerutil.AddFinalizer(c, ExternalAuthConfigFinalizerName)
		}); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		// Requeue to continue processing after finalizer is added
		return ctrl.Result{RequeueAfter: externalAuthConfigRequeueDelay}, nil
	}

	// Validate spec configuration early
	if err := externalAuthConfig.Validate(); err != nil {
		logger.Error(err, "MCPExternalAuthConfig spec validation failed")
		// Fold the IdentitySynthesized advisory into the same patch as the
		// Valid=False write so a broken edit cannot leave a stale advisory
		// (True/upstream-name) dangling. Both mutations happen inside the
		// closure: MutateAndPatchStatus snapshots the object on entry, so any
		// pre-mutate change would land in both halves of the diff and be
		// silently dropped. applyIdentitySynthesizedCondition is a pure
		// function of the current spec, so it recomputes the advisory even on
		// the validation-failure path.
		// Capture the transition before MutateAndPatchStatus mutates conditions
		// in place, so the Warning fires only when entering the invalid state.
		wasInvalid := conditionStatusIs(externalAuthConfig.Status.Conditions,
			mcpv1beta1.ConditionTypeValid, metav1.ConditionFalse)
		updateErr := ctrlutil.MutateAndPatchStatus(ctx, r.Client, externalAuthConfig,
			func(c *mcpv1beta1.MCPExternalAuthConfig) {
				r.applyIdentitySynthesizedCondition(c)
				meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
					Type:               mcpv1beta1.ConditionTypeValid,
					Status:             metav1.ConditionFalse,
					Reason:             "ValidationFailed",
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
			emitConfigEvent(r.Recorder, externalAuthConfig, corev1.EventTypeWarning,
				eventReasonConfigInvalid, eventActionValidate, "spec validation failed: %s", err.Error())
		}
		return ctrl.Result{}, nil // Don't requeue on validation errors - user must fix spec
	}

	// Dispatch OBO-typed configs through the registered handler. The default
	// handler returns obo.ErrEnterpriseRequired so upstream-only builds surface
	// Valid=False / Reason=EnterpriseRequired here rather than failing later
	// inside a consumer reconciler with a generic "unsupported" error. The OBO
	// failure path routes through setInvalid, which applies the advisory inside
	// its own patch closure.
	if externalAuthConfig.Spec.Type == mcpv1beta1.ExternalAuthTypeOBO {
		if handled, err := r.triageOBOValidation(ctx, externalAuthConfig); handled {
			return ctrl.Result{}, err
		}
	}

	// Calculate the hash of the current configuration
	configHash := r.calculateConfigHash(externalAuthConfig.Spec)

	// Check if the hash has changed
	hashChanged := externalAuthConfig.Status.ConfigHash != configHash
	if hashChanged {
		return r.handleConfigHashChange(ctx, externalAuthConfig, configHash)
	}

	// Steady-state success path: ensure Valid=True and the IdentitySynthesized
	// advisory are set, and refresh the referencing-workloads list, in a single
	// status patch.
	return r.updateReferencingWorkloads(ctx, externalAuthConfig)
}

// setValidTrueAndSynthesized stamps ConditionTypeValid=True and refreshes the
// IdentitySynthesized advisory on the supplied object. It is callable inside a
// MutateAndPatchStatus closure: applyIdentitySynthesizedCondition is idempotent
// on the same spec and SetStatusCondition only mutates Conditions on a real
// change, so a no-op reconcile produces an empty patch body that the helper
// skips.
func (r *MCPExternalAuthConfigReconciler) setValidTrueAndSynthesized(c *mcpv1beta1.MCPExternalAuthConfig) {
	r.applyIdentitySynthesizedCondition(c)
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeValid,
		Status:             metav1.ConditionTrue,
		Reason:             "ValidationSucceeded",
		Message:            "Spec validation passed",
		ObservedGeneration: c.Generation,
	})
}

// calculateConfigHash calculates a hash of the MCPExternalAuthConfig spec using Kubernetes utilities
func (*MCPExternalAuthConfigReconciler) calculateConfigHash(spec mcpv1beta1.MCPExternalAuthConfigSpec) string {
	return ctrlutil.CalculateConfigHash(spec)
}

// applyIdentitySynthesizedCondition sets ConditionTypeIdentitySynthesized
// True when any OAuth2 upstream has nil userInfo, False when every upstream
// has userInfo configured, and removes it for non-embeddedAuthServer types
// where the question is moot. It is idempotent on the same spec and is called
// inside the status-write closures so the advisory is recomputed on every
// status patch.
func (*MCPExternalAuthConfigReconciler) applyIdentitySynthesizedCondition(
	cfg *mcpv1beta1.MCPExternalAuthConfig,
) {
	if cfg.Spec.Type != mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer || cfg.Spec.EmbeddedAuthServer == nil {
		meta.RemoveStatusCondition(&cfg.Status.Conditions, mcpv1beta1.ConditionTypeIdentitySynthesized)
		return
	}

	syntheticUpstreams := cfg.Spec.EmbeddedAuthServer.SyntheticIdentityUpstreams()
	if len(syntheticUpstreams) == 0 {
		meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeIdentitySynthesized,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonIdentitySynthesizedInactive,
			Message:            "All OAuth2 upstreams have userInfo configured; user identity is resolved from the upstream",
			ObservedGeneration: cfg.Generation,
		})
		return
	}

	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type:   mcpv1beta1.ConditionTypeIdentitySynthesized,
		Status: metav1.ConditionTrue,
		Reason: mcpv1beta1.ConditionReasonIdentitySynthesizedActive,
		Message: fmt.Sprintf(
			"OAuth2 upstream(s) %v have no userInfo configured; the embedded auth server will "+
				"synthesize a non-PII subject from the access token (no Name/Email claims). "+
				"If a userInfo endpoint exists for these upstreams, configure it to resolve real identity.",
			syntheticUpstreams,
		),
		ObservedGeneration: cfg.Generation,
	})
}

// triageOBOValidation runs the registered OBO handler's Validate and routes
// the result through the three-bucket contract documented on OBOHandler:
//
//   - ErrEnterpriseRequired   → permanent, Reason=EnterpriseRequired
//   - *obo.ValidationError    → permanent, Reason=InvalidConfig (the typed
//     error's Message is written verbatim into condition.Message)
//   - anything else           → transient; return the error wrapped so
//     controller-runtime requeues with backoff and self-heals once the
//     upstream dependency (Secret/JWKS/webhook) recovers
//
// Returns handled=true to signal the caller to return immediately (with a
// zero ctrl.Result and the returned error). handled=false means Validate
// succeeded and the reconciler should continue with the Valid=True path.
//
// Extracted from Reconcile so the parent stays under the gocyclo threshold.
func (r *MCPExternalAuthConfigReconciler) triageOBOValidation(
	ctx context.Context,
	cfg *mcpv1beta1.MCPExternalAuthConfig,
) (handled bool, err error) {
	validateErr := ctrlutil.OBOValidate(cfg)
	if validateErr == nil {
		return false, nil
	}
	switch {
	case stderrors.Is(validateErr, obo.ErrEnterpriseRequired):
		return true, r.setInvalid(ctx, cfg, validateErr, mcpv1beta1.ConditionReasonEnterpriseRequired)
	default:
		var valErr *obo.ValidationError
		if stderrors.As(validateErr, &valErr) {
			return true, r.setInvalid(ctx, cfg, valErr, mcpv1beta1.ConditionReasonInvalidConfig)
		}
		// Transient: return the error so controller-runtime requeues with
		// backoff. Locking the resource into a permanent InvalidConfig on a
		// transient I/O blip would require the user to touch the spec for
		// the failure to clear, defeating self-healing.
		return true, fmt.Errorf("OBO handler validation failed: %w", validateErr)
	}
}

// setInvalid writes a Valid=False condition through MutateAndPatchStatus,
// using the supplied reason string and the error's message as the condition
// message. Returns an empty result with no requeue: the spec must change (or
// an out-of-tree handler must be registered) for this branch to clear, so
// requeuing buys nothing.
//
// The IdentitySynthesized advisory is recomputed inside the patch closure so
// both the advisory transition (e.g., when a user switches a config from
// embeddedAuthServer to obo) and the Valid=False condition land in the same
// merge patch. The object is re-fetched first so the closure mutates a clean
// snapshot: MutateAndPatchStatus diffs the post-mutate object against the
// snapshot it takes on entry, so any mutation already present before the
// helper runs would land in both halves of the diff and be dropped.
func (r *MCPExternalAuthConfigReconciler) setInvalid(
	ctx context.Context,
	cfg *mcpv1beta1.MCPExternalAuthConfig,
	err error,
	reason string,
) error {
	fresh := &mcpv1beta1.MCPExternalAuthConfig{}
	if getErr := r.Get(ctx, client.ObjectKeyFromObject(cfg), fresh); getErr != nil {
		if errors.IsNotFound(getErr) {
			// Deleted between the reconciler's initial Get and this re-fetch;
			// nothing to patch, and the reconciler's own NotFound handling
			// already returned cleanly the next time around.
			return nil
		}
		return getErr
	}
	// Capture the transition before the patch mutates conditions in place, so
	// the Warning fires only when entering the invalid state.
	wasInvalid := conditionStatusIs(fresh.Status.Conditions,
		mcpv1beta1.ConditionTypeValid, metav1.ConditionFalse)
	if patchErr := ctrlutil.MutateAndPatchStatus(ctx, r.Client, fresh, func(c *mcpv1beta1.MCPExternalAuthConfig) {
		// applyIdentitySynthesizedCondition is idempotent on the same spec;
		// re-applying it inside the closure folds the advisory transition into
		// the same patch as the Valid=False write below. See
		// TestMCPExternalAuthConfigReconciler_IdentitySynthesizedTransitionsOnValidationFailure
		// for the related validation-path regression guard.
		r.applyIdentitySynthesizedCondition(c)
		meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeValid,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            err.Error(),
			ObservedGeneration: c.Generation,
		})
	}); patchErr != nil {
		return patchErr
	}
	if !wasInvalid {
		emitConfigEvent(r.Recorder, fresh, corev1.EventTypeWarning,
			eventReasonConfigInvalid, eventActionValidate, "spec validation failed: %s", err.Error())
	}
	return nil
}

// handleConfigHashChange handles the logic when the config hash changes
func (r *MCPExternalAuthConfigReconciler) handleConfigHashChange(
	ctx context.Context,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
	configHash string,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("MCPExternalAuthConfig configuration changed",
		"oldHash", externalAuthConfig.Status.ConfigHash,
		"newHash", configHash)

	// Find all MCPServers that reference this MCPExternalAuthConfig
	referencingServers, err := r.findReferencingMCPServers(ctx, externalAuthConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing MCPServers")
		return ctrl.Result{}, fmt.Errorf("failed to find referencing MCPServers: %w", err)
	}

	// Build the list of referencing workloads
	refs := make([]mcpv1beta1.WorkloadReference, 0, len(referencingServers))
	for _, server := range referencingServers {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPServer, Name: server.Name})
	}
	ctrlutil.SortWorkloadRefs(refs)

	// Capture the recovery transition before the patch mutates conditions in
	// place, so a single Normal event fires on the False->True transition.
	wasInvalid := conditionStatusIs(externalAuthConfig.Status.Conditions,
		mcpv1beta1.ConditionTypeValid, metav1.ConditionFalse)

	// Single status patch covering the hash-change success path: the new hash
	// and generation, the refreshed reference list, and the Valid=True /
	// IdentitySynthesized conditions. All mutations happen inside the closure so
	// the pre-mutate snapshot stays clean (a MutateAndPatchStatus prerequisite).
	if err := ctrlutil.MutateAndPatchStatus(ctx, r.Client, externalAuthConfig,
		func(c *mcpv1beta1.MCPExternalAuthConfig) {
			r.setValidTrueAndSynthesized(c)
			c.Status.ConfigHash = configHash
			c.Status.ObservedGeneration = c.Generation
			c.Status.ReferencingWorkloads = refs
			c.Status.ReferenceCount = workloadReferenceCount(refs)
		}); err != nil {
		logger.Error(err, "Failed to update MCPExternalAuthConfig status")
		return ctrl.Result{}, err
	}
	emitConfigRecoveryEvent(r.Recorder, externalAuthConfig, wasInvalid)

	return ctrl.Result{}, nil
}

// handleDeletion handles the deletion of a MCPExternalAuthConfig
func (r *MCPExternalAuthConfigReconciler) handleDeletion(
	ctx context.Context,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(externalAuthConfig, ExternalAuthConfigFinalizerName) {
		// Check if any workloads still reference this MCPExternalAuthConfig
		referencingWorkloads, err := r.findReferencingWorkloads(ctx, externalAuthConfig)
		if err != nil {
			logger.Error(err, "Failed to check referencing workloads during deletion")
			return ctrl.Result{}, err
		}

		if len(referencingWorkloads) > 0 {
			logger.Info("MCPExternalAuthConfig is still referenced by workloads, blocking deletion",
				"externalAuthConfig", externalAuthConfig.Name,
				"referencingWorkloads", referencingWorkloads)

			// Capture the transition before the patch mutates conditions in
			// place: emit the Warning only when entering the blocked state.
			wasBlocked := conditionStatusIs(externalAuthConfig.Status.Conditions,
				mcpv1beta1.ConditionTypeDeletionBlocked, metav1.ConditionTrue)
			updateErr := ctrlutil.MutateAndPatchStatus(ctx, r.Client, externalAuthConfig,
				func(c *mcpv1beta1.MCPExternalAuthConfig) {
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
				emitConfigEvent(r.Recorder, externalAuthConfig, corev1.EventTypeWarning,
					eventReasonDeletionBlocked, eventActionDelete,
					"deletion blocked while still referenced by workloads: %v", referencingWorkloads)
			}

			// Requeue to check again later
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		// No references, safe to remove finalizer and allow deletion
		if err := ctrlutil.MutateAndPatchSpec(ctx, r.Client, externalAuthConfig,
			func(c *mcpv1beta1.MCPExternalAuthConfig) {
				controllerutil.RemoveFinalizer(c, ExternalAuthConfigFinalizerName)
			}); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPExternalAuthConfig", "externalAuthConfig", externalAuthConfig.Name)
	}

	return ctrl.Result{}, nil
}

// externalAuthConfigRefIndexKey is the field-index key backing the reverse
// lookups in findReferencingMCPServers / findReferencingMCPRemoteProxies. A
// workload references an MCPExternalAuthConfig through EITHER spec field, so a
// single combined index covers both: the extractors below return every config
// name the object names via either field, deduplicated. The index is registered
// per workload type in SetupWithManager.
const externalAuthConfigRefIndexKey = "spec.externalAuthConfigRef+authServerRef"

// indexMCPServerByExternalAuthConfigRef extracts the MCPExternalAuthConfig
// name(s) an MCPServer references, for the combined field index. It covers both
// spec.externalAuthConfigRef and spec.authServerRef (the latter only when its
// Kind identifies an MCPExternalAuthConfig), deduplicating so a server naming the
// same config via both fields is indexed once. Returns nil when there is no
// reference so unreferencing servers are not indexed under the empty key.
func indexMCPServerByExternalAuthConfigRef(obj client.Object) []string {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok {
		return nil
	}
	names := map[string]struct{}{}
	if server.Spec.ExternalAuthConfigRef != nil && server.Spec.ExternalAuthConfigRef.Name != "" {
		names[server.Spec.ExternalAuthConfigRef.Name] = struct{}{}
	}
	if server.Spec.AuthServerRef != nil &&
		server.Spec.AuthServerRef.Kind == authServerRefKindMCPExternalAuthConfig &&
		server.Spec.AuthServerRef.Name != "" {
		names[server.Spec.AuthServerRef.Name] = struct{}{}
	}
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	return out
}

// indexMCPRemoteProxyByExternalAuthConfigRef extracts the MCPExternalAuthConfig
// name(s) an MCPRemoteProxy references, for the combined field index. It mirrors
// indexMCPServerByExternalAuthConfigRef: it covers both spec.externalAuthConfigRef
// and spec.authServerRef (the latter only when its Kind identifies an
// MCPExternalAuthConfig), deduplicating so a proxy naming the same config via both
// fields is indexed once. Returns nil when there is no reference.
func indexMCPRemoteProxyByExternalAuthConfigRef(obj client.Object) []string {
	proxy, ok := obj.(*mcpv1beta1.MCPRemoteProxy)
	if !ok {
		return nil
	}
	names := map[string]struct{}{}
	if proxy.Spec.ExternalAuthConfigRef != nil && proxy.Spec.ExternalAuthConfigRef.Name != "" {
		names[proxy.Spec.ExternalAuthConfigRef.Name] = struct{}{}
	}
	if proxy.Spec.AuthServerRef != nil &&
		proxy.Spec.AuthServerRef.Kind == authServerRefKindMCPExternalAuthConfig &&
		proxy.Spec.AuthServerRef.Name != "" {
		names[proxy.Spec.AuthServerRef.Name] = struct{}{}
	}
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	return out
}

// findReferencingMCPServers finds all MCPServers that reference the given MCPExternalAuthConfig
// via either externalAuthConfigRef or authServerRef.
//
// The combined field index (externalAuthConfigRefIndexKey, registered in
// SetupWithManager) returns each server once regardless of which field — or both —
// names the config, so a single indexed query replaces the prior two-query merge
// and dedup.
func (r *MCPExternalAuthConfigReconciler) findReferencingMCPServers(
	ctx context.Context,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) ([]mcpv1beta1.MCPServer, error) {
	serverList := &mcpv1beta1.MCPServerList{}
	if err := r.List(ctx, serverList, client.InNamespace(externalAuthConfig.Namespace),
		client.MatchingFields{externalAuthConfigRefIndexKey: externalAuthConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list MCPServers by externalAuthConfigRef: %w", err)
	}
	return serverList.Items, nil
}

// findReferencingMCPRemoteProxies finds all MCPRemoteProxies that reference the given MCPExternalAuthConfig
// via either externalAuthConfigRef or authServerRef.
//
// The combined field index (externalAuthConfigRefIndexKey, registered in
// SetupWithManager) returns each proxy once regardless of which field — or both —
// names the config, so a single indexed query replaces the prior two-query merge
// and dedup.
func (r *MCPExternalAuthConfigReconciler) findReferencingMCPRemoteProxies(
	ctx context.Context,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) ([]mcpv1beta1.MCPRemoteProxy, error) {
	proxyList := &mcpv1beta1.MCPRemoteProxyList{}
	if err := r.List(ctx, proxyList, client.InNamespace(externalAuthConfig.Namespace),
		client.MatchingFields{externalAuthConfigRefIndexKey: externalAuthConfig.Name}); err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies by externalAuthConfigRef: %w", err)
	}
	return proxyList.Items, nil
}

// findReferencingWorkloads returns the workload resources (MCPServer and MCPRemoteProxy)
// that reference this MCPExternalAuthConfig via their ExternalAuthConfigRef or AuthServerRef field.
// Both fields are covered by a single combined field index per workload type, so each
// workload type is found with one indexed query.
func (r *MCPExternalAuthConfigReconciler) findReferencingWorkloads(
	ctx context.Context,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) ([]mcpv1beta1.WorkloadReference, error) {
	servers, err := r.findReferencingMCPServers(ctx, externalAuthConfig)
	if err != nil {
		return nil, err
	}
	refs := make([]mcpv1beta1.WorkloadReference, 0, len(servers))
	for _, server := range servers {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPServer, Name: server.Name})
	}

	proxies, err := r.findReferencingMCPRemoteProxies(ctx, externalAuthConfig)
	if err != nil {
		return nil, err
	}
	for _, proxy := range proxies {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPRemoteProxy, Name: proxy.Name})
	}

	ctrlutil.SortWorkloadRefs(refs)
	return refs, nil
}

// SetupWithManager sets up the controller with the Manager.
// Watches MCPServer and MCPRemoteProxy changes to maintain accurate ReferencingWorkloads status.
func (r *MCPExternalAuthConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Field indexes backing findReferencingMCPServers / findReferencingMCPRemoteProxies.
	// Each is a combined index covering both spec.externalAuthConfigRef and
	// spec.authServerRef, so a single MatchingFields query returns every workload
	// referencing a given config via either field rather than listing every
	// workload in the namespace and filtering in memory.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.MCPServer{}, externalAuthConfigRefIndexKey, indexMCPServerByExternalAuthConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up MCPServer externalAuthConfigRef index: %w", err)
	}
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &mcpv1beta1.MCPRemoteProxy{}, externalAuthConfigRefIndexKey,
		indexMCPRemoteProxyByExternalAuthConfigRef,
	); err != nil {
		return fmt.Errorf("failed to set up MCPRemoteProxy externalAuthConfigRef index: %w", err)
	}

	// GenerationChangedPredicate also suppresses the workload-watch resync; the self-heal
	// backstop for a stale ReferencingWorkloads entry (e.g. a workload deleted while the
	// operator was down) is this config's own For() resync, which re-runs Reconcile and
	// rebuilds ReferencingWorkloads from the index.
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1beta1.MCPExternalAuthConfig{}).
		Watches(
			&mcpv1beta1.MCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPServerToExternalAuthConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&mcpv1beta1.MCPRemoteProxy{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPRemoteProxyToExternalAuthConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Complete(r)
}

// mapMCPServerToExternalAuthConfig maps an MCPServer to the MCPExternalAuthConfig(s)
// it currently references via spec.externalAuthConfigRef and/or spec.authServerRef.
// EnqueueRequestsFromMapFunc invokes this on both the old and new object on update
// (and on the object for create/delete), so a ref change or deletion automatically
// enqueues both the previously- and newly-referenced config; the previously-referenced
// config then prunes the stale entry on reconcile. No manual stale-reference scan needed.
//
// A server may name the same config via both fields, so the two referenced names are
// deduplicated against a seen set before being turned into requests.
func (*MCPExternalAuthConfigReconciler) mapMCPServerToExternalAuthConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok {
		return nil
	}

	seen := make(map[types.NamespacedName]struct{})
	var requests []reconcile.Request

	// Enqueue the currently-referenced MCPExternalAuthConfig via externalAuthConfigRef (if any)
	if server.Spec.ExternalAuthConfigRef != nil && server.Spec.ExternalAuthConfigRef.Name != "" {
		nn := types.NamespacedName{
			Name:      server.Spec.ExternalAuthConfigRef.Name,
			Namespace: server.Namespace,
		}
		seen[nn] = struct{}{}
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}

	// Enqueue the MCPExternalAuthConfig referenced via authServerRef (if any), deduped
	if server.Spec.AuthServerRef != nil &&
		server.Spec.AuthServerRef.Kind == authServerRefKindMCPExternalAuthConfig &&
		server.Spec.AuthServerRef.Name != "" {
		nn := types.NamespacedName{
			Name:      server.Spec.AuthServerRef.Name,
			Namespace: server.Namespace,
		}
		if _, already := seen[nn]; !already {
			seen[nn] = struct{}{}
			requests = append(requests, reconcile.Request{NamespacedName: nn})
		}
	}

	return requests
}

// mapMCPRemoteProxyToExternalAuthConfig maps an MCPRemoteProxy to the MCPExternalAuthConfig(s)
// it currently references via spec.externalAuthConfigRef and/or spec.authServerRef.
// EnqueueRequestsFromMapFunc invokes this on both the old and new object on update
// (and on the object for create/delete), so a ref change or deletion automatically
// enqueues both the previously- and newly-referenced config; the previously-referenced
// config then prunes the stale entry on reconcile. No manual stale-reference scan needed.
//
// A proxy may name the same config via both fields, so the two referenced names are
// deduplicated against a seen set before being turned into requests.
func (*MCPExternalAuthConfigReconciler) mapMCPRemoteProxyToExternalAuthConfig(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	proxy, ok := obj.(*mcpv1beta1.MCPRemoteProxy)
	if !ok {
		return nil
	}

	seen := make(map[types.NamespacedName]struct{})
	var requests []reconcile.Request

	// Enqueue the currently-referenced MCPExternalAuthConfig via externalAuthConfigRef (if any)
	if proxy.Spec.ExternalAuthConfigRef != nil && proxy.Spec.ExternalAuthConfigRef.Name != "" {
		nn := types.NamespacedName{
			Name:      proxy.Spec.ExternalAuthConfigRef.Name,
			Namespace: proxy.Namespace,
		}
		seen[nn] = struct{}{}
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}

	// Enqueue the MCPExternalAuthConfig referenced via authServerRef (if any), deduped
	if proxy.Spec.AuthServerRef != nil &&
		proxy.Spec.AuthServerRef.Kind == authServerRefKindMCPExternalAuthConfig &&
		proxy.Spec.AuthServerRef.Name != "" {
		nn := types.NamespacedName{
			Name:      proxy.Spec.AuthServerRef.Name,
			Namespace: proxy.Namespace,
		}
		if _, already := seen[nn]; !already {
			seen[nn] = struct{}{}
			requests = append(requests, reconcile.Request{NamespacedName: nn})
		}
	}

	return requests
}

// updateReferencingWorkloads writes the steady-state success status in a single
// patch: it ensures Valid=True and the IdentitySynthesized advisory are set and
// refreshes the referencing-workloads list. MutateAndPatchStatus short-circuits
// on an empty diff, so a no-op reconcile skips the wire call.
func (r *MCPExternalAuthConfigReconciler) updateReferencingWorkloads(
	ctx context.Context,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	refs, err := r.findReferencingWorkloads(ctx, externalAuthConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing workloads")
		return ctrl.Result{}, fmt.Errorf("failed to find referencing workloads: %w", err)
	}

	// Capture the recovery transition before the patch mutates conditions in
	// place, so a single Normal event fires on the False->True transition.
	wasInvalid := conditionStatusIs(externalAuthConfig.Status.Conditions,
		mcpv1beta1.ConditionTypeValid, metav1.ConditionFalse)

	if err := ctrlutil.MutateAndPatchStatus(ctx, r.Client, externalAuthConfig,
		func(c *mcpv1beta1.MCPExternalAuthConfig) {
			r.setValidTrueAndSynthesized(c)
			if !ctrlutil.WorkloadRefsEqual(c.Status.ReferencingWorkloads, refs) ||
				c.Status.ReferenceCount != workloadReferenceCount(refs) {
				c.Status.ReferencingWorkloads = refs
				c.Status.ReferenceCount = workloadReferenceCount(refs)
			}
		}); err != nil {
		logger.Error(err, "Failed to update MCPExternalAuthConfig status")
		return ctrl.Result{}, err
	}
	emitConfigRecoveryEvent(r.Recorder, externalAuthConfig, wasInvalid)

	return ctrl.Result{}, nil
}

// GetExternalAuthConfigForMCPServer retrieves the MCPExternalAuthConfig referenced by an MCPServer.
// This function is exported for use by the MCPServer controller (Phase 5 integration).
func GetExternalAuthConfigForMCPServer(
	ctx context.Context,
	c client.Client,
	mcpServer *mcpv1beta1.MCPServer,
) (*mcpv1beta1.MCPExternalAuthConfig, error) {
	if mcpServer.Spec.ExternalAuthConfigRef == nil {
		// We throw an error because in this case you assume there is a ExternalAuthConfig
		// but there isn't one referenced.
		return nil, fmt.Errorf("MCPServer %s does not reference a MCPExternalAuthConfig", mcpServer.Name)
	}

	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      mcpServer.Spec.ExternalAuthConfigRef.Name,
		Namespace: mcpServer.Namespace, // Same namespace as MCPServer
	}, externalAuthConfig)

	if err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("MCPExternalAuthConfig %s not found in namespace %s",
				mcpServer.Spec.ExternalAuthConfigRef.Name, mcpServer.Namespace)
		}
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	return externalAuthConfig, nil
}

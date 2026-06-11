// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

const (
	// AuthzConfigFinalizerName is the name of the finalizer for MCPAuthzConfig
	AuthzConfigFinalizerName = "mcpauthzconfig.toolhive.stacklok.dev/finalizer"

	// authzConfigRequeueDelay is the delay before requeuing after adding a finalizer
	authzConfigRequeueDelay = 500 * time.Millisecond

	// authzConfigVersion is the default version for reconstructed authz configs
	authzConfigVersion = "1.0"
)

// MCPAuthzConfigReconciler reconciles a MCPAuthzConfig object.
//
// This controller manages the lifecycle of MCPAuthzConfig resources: validation
// via the authorizer factory registry, config hash computation, finalizer management,
// reference tracking, and deletion protection when workloads reference this config.
type MCPAuthzConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpauthzconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpauthzconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpauthzconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpremoteproxies,verbs=get;list;watch

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
		if updateErr := ctrlutil.MutateAndPatchStatus(ctx, r.Client, authzConfig, func(c *mcpv1beta1.MCPAuthzConfig) {
			meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
				Type:               mcpv1beta1.ConditionTypeAuthzConfigValid,
				Status:             metav1.ConditionFalse,
				Reason:             mcpv1beta1.ConditionReasonAuthzConfigInvalid,
				Message:            err.Error(),
				ObservedGeneration: c.Generation,
			})
		}); updateErr != nil {
			logger.Error(updateErr, "Failed to update status after validation error")
		}
		return ctrl.Result{}, nil // Don't requeue on validation errors - user must fix spec
	}

	// Calculate the hash of the current configuration.
	// The spec is canonicalized first so that two semantically-equal configs
	// that differ only in whitespace or JSON key order produce the same hash —
	// otherwise a noop kubectl-apply round trip can re-emit Spec.Config.Raw
	// with different bytes and flip the hash, causing spurious status writes.
	canonicalSpec := canonicalizeSpecForHash(authzConfig.Spec)
	configHash := ctrlutil.CalculateConfigHash(canonicalSpec)
	hashChanged := authzConfig.Status.ConfigHash != configHash
	if hashChanged {
		logger.Info("MCPAuthzConfig configuration changed",
			"oldHash", authzConfig.Status.ConfigHash,
			"newHash", configHash)

		if err := ctrlutil.MutateAndPatchStatus(ctx, r.Client, authzConfig, func(c *mcpv1beta1.MCPAuthzConfig) {
			setValidTrueCondition(c)
			c.Status.ConfigHash = configHash
			c.Status.ObservedGeneration = c.Generation
		}); err != nil {
			logger.Error(err, "Failed to update MCPAuthzConfig status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Refresh ReferencingWorkloads list
	referencingWorkloads, err := r.findReferencingWorkloads(ctx, authzConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing workloads")
		// Fall through: status patch below is best-effort. Stage 4 of the
		// review-feedback PR will make this return the error so
		// controller-runtime requeues with backoff.
	}

	// Single status patch covering the steady-state success path: ensure the
	// Valid=True condition is set, and refresh the references list if it
	// changed. MutateAndPatchStatus short-circuits on an empty diff so the
	// no-op case still skips the wire call (SteadyStateNoOp behaviour is
	// preserved).
	if err := ctrlutil.MutateAndPatchStatus(ctx, r.Client, authzConfig, func(c *mcpv1beta1.MCPAuthzConfig) {
		setValidTrueCondition(c)
		if referencingWorkloads != nil &&
			(!ctrlutil.WorkloadRefsEqual(c.Status.ReferencingWorkloads, referencingWorkloads) ||
				c.Status.ReferenceCount != workloadReferenceCount(referencingWorkloads)) {
			c.Status.ReferencingWorkloads = referencingWorkloads
			c.Status.ReferenceCount = workloadReferenceCount(referencingWorkloads)
		}
	}); err != nil {
		logger.Error(err, "Failed to update MCPAuthzConfig status")
		return ctrl.Result{}, err
	}

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
// Backend validation lives here rather than as a Validate() method on the type because
// it requires the authorizer factory registry — an external dependency that the API
// types package must not import.
func (*MCPAuthzConfigReconciler) validateAuthzConfig(authzConfig *mcpv1beta1.MCPAuthzConfig) error {
	if err := authzConfig.Validate(); err != nil {
		return err
	}

	fullConfigJSON, err := BuildFullAuthzConfigJSON(authzConfig.Spec)
	if err != nil {
		return err
	}

	// Parse and validate via the authorizer factory
	var cfg authzConfigEnvelope
	if err := json.Unmarshal(fullConfigJSON, &cfg); err != nil {
		return fmt.Errorf("failed to parse reconstructed authz config: %w", err)
	}
	if cfg.Version == "" || cfg.Type == "" {
		return fmt.Errorf("reconstructed config missing version or type")
	}

	factory := authorizers.GetFactory(cfg.Type)
	if factory == nil {
		return fmt.Errorf("unsupported authorizer type: %s (registered types: %v)",
			cfg.Type, authorizers.RegisteredTypes())
	}

	return factory.ValidateConfig(fullConfigJSON)
}

// authzConfigEnvelope is a minimal struct for extracting version and type from reconstructed JSON.
type authzConfigEnvelope struct {
	Version string `json:"version"`
	Type    string `json:"type"`
}

// BuildFullAuthzConfigJSON reconstructs the full authorizer config JSON from a
// MCPAuthzConfig spec. The result is the same format accepted by authorizers.Config
// and used in ConfigMaps: {"version": "1.0", "type": "<type>", "<configKey>": {<config>}}.
func BuildFullAuthzConfigJSON(spec mcpv1beta1.MCPAuthzConfigSpec) ([]byte, error) {
	factory := authorizers.GetFactory(spec.Type)
	if factory == nil {
		return nil, fmt.Errorf("unsupported authorizer type: %s (registered types: %v)",
			spec.Type, authorizers.RegisteredTypes())
	}

	configKey := factory.ConfigKey()

	if len(spec.Config.Raw) == 0 {
		return nil, fmt.Errorf("config field is empty")
	}

	versionJSON, err := marshalJSONString(authzConfigVersion)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal version: %w", err)
	}
	typeJSON, err := marshalJSONString(spec.Type)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal type: %w", err)
	}

	fullConfig := map[string]json.RawMessage{
		"version": versionJSON,
		"type":    typeJSON,
		configKey: spec.Config.Raw,
	}

	result, err := json.Marshal(fullConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal full authz config: %w", err)
	}
	return result, nil
}

// marshalJSONString marshals a string value to JSON, returning an error instead of panicking.
func marshalJSONString(v string) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %q: %w", v, err)
	}
	return b, nil
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

			if updateErr := ctrlutil.MutateAndPatchStatus(ctx, r.Client, authzConfig, func(c *mcpv1beta1.MCPAuthzConfig) {
				meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
					Type:               mcpv1beta1.ConditionTypeDeletionBlocked,
					Status:             metav1.ConditionTrue,
					Reason:             "ReferencedByWorkloads",
					Message:            fmt.Sprintf("Cannot delete: referenced by workloads: %v", referencingWorkloads),
					ObservedGeneration: c.Generation,
				})
				c.Status.ReferencingWorkloads = referencingWorkloads
				c.Status.ReferenceCount = workloadReferenceCount(referencingWorkloads)
			}); updateErr != nil {
				logger.Error(updateErr, "Failed to update status during deletion block")
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

// findReferencingWorkloads returns the workload resources (MCPServer, VirtualMCPServer,
// and MCPRemoteProxy) that reference this MCPAuthzConfig via their AuthzConfigRef field.
func (r *MCPAuthzConfigReconciler) findReferencingWorkloads(
	ctx context.Context,
	authzConfig *mcpv1beta1.MCPAuthzConfig,
) ([]mcpv1beta1.WorkloadReference, error) {
	// Find referencing MCPServers
	refs, err := ctrlutil.FindWorkloadRefsFromMCPServers(ctx, r.Client, authzConfig.Namespace, authzConfig.Name,
		func(server *mcpv1beta1.MCPServer) *string {
			if server.Spec.AuthzConfigRef != nil {
				return &server.Spec.AuthzConfigRef.Name
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	// Check VirtualMCPServers
	vmcpList := &mcpv1beta1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(authzConfig.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list VirtualMCPServers: %w", err)
	}
	for _, vmcp := range vmcpList.Items {
		if vmcp.Spec.IncomingAuth != nil &&
			vmcp.Spec.IncomingAuth.AuthzConfigRef != nil &&
			vmcp.Spec.IncomingAuth.AuthzConfigRef.Name == authzConfig.Name {
			refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindVirtualMCPServer, Name: vmcp.Name})
		}
	}

	// Check MCPRemoteProxies
	proxyList := &mcpv1beta1.MCPRemoteProxyList{}
	if err := r.List(ctx, proxyList, client.InNamespace(authzConfig.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies: %w", err)
	}
	for _, proxy := range proxyList.Items {
		if proxy.Spec.AuthzConfigRef != nil && proxy.Spec.AuthzConfigRef.Name == authzConfig.Name {
			refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPRemoteProxy, Name: proxy.Name})
		}
	}

	ctrlutil.SortWorkloadRefs(refs)
	return refs, nil
}

// SetupWithManager sets up the controller with the Manager.
// Watches MCPServer, VirtualMCPServer, and MCPRemoteProxy changes to maintain
// accurate ReferencingWorkloads status.
func (r *MCPAuthzConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1beta1.MCPAuthzConfig{}).
		Watches(&mcpv1beta1.MCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPServerToAuthzConfig)).
		Watches(&mcpv1beta1.VirtualMCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapVirtualMCPServerToAuthzConfig)).
		Watches(&mcpv1beta1.MCPRemoteProxy{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPRemoteProxyToAuthzConfig)).
		Complete(r)
}

// mapMCPServerToAuthzConfig maps MCPServer changes to MCPAuthzConfig reconciliation requests.
func (r *MCPAuthzConfigReconciler) mapMCPServerToAuthzConfig(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok {
		return nil
	}

	seen := make(map[types.NamespacedName]struct{})
	var requests []reconcile.Request

	// Enqueue the currently-referenced MCPAuthzConfig (if any)
	if server.Spec.AuthzConfigRef != nil {
		nn := types.NamespacedName{Name: server.Spec.AuthzConfigRef.Name, Namespace: server.Namespace}
		seen[nn] = struct{}{}
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}

	// Also enqueue any MCPAuthzConfig that still lists this server in
	// ReferencingWorkloads — handles ref-removal and server-deletion cases.
	requests = append(requests, r.findStaleRefs(ctx, server.Namespace, mcpv1beta1.WorkloadKindMCPServer, server.Name, seen)...)

	return requests
}

// mapVirtualMCPServerToAuthzConfig maps VirtualMCPServer changes to MCPAuthzConfig reconciliation requests.
func (r *MCPAuthzConfigReconciler) mapVirtualMCPServerToAuthzConfig(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	vmcp, ok := obj.(*mcpv1beta1.VirtualMCPServer)
	if !ok {
		return nil
	}

	seen := make(map[types.NamespacedName]struct{})
	var requests []reconcile.Request

	if vmcp.Spec.IncomingAuth != nil && vmcp.Spec.IncomingAuth.AuthzConfigRef != nil {
		nn := types.NamespacedName{Name: vmcp.Spec.IncomingAuth.AuthzConfigRef.Name, Namespace: vmcp.Namespace}
		seen[nn] = struct{}{}
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}

	requests = append(requests, r.findStaleRefs(ctx, vmcp.Namespace, mcpv1beta1.WorkloadKindVirtualMCPServer, vmcp.Name, seen)...)

	return requests
}

// mapMCPRemoteProxyToAuthzConfig maps MCPRemoteProxy changes to MCPAuthzConfig reconciliation requests.
func (r *MCPAuthzConfigReconciler) mapMCPRemoteProxyToAuthzConfig(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	proxy, ok := obj.(*mcpv1beta1.MCPRemoteProxy)
	if !ok {
		return nil
	}

	seen := make(map[types.NamespacedName]struct{})
	var requests []reconcile.Request

	if proxy.Spec.AuthzConfigRef != nil {
		nn := types.NamespacedName{Name: proxy.Spec.AuthzConfigRef.Name, Namespace: proxy.Namespace}
		seen[nn] = struct{}{}
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}

	requests = append(requests, r.findStaleRefs(ctx, proxy.Namespace, mcpv1beta1.WorkloadKindMCPRemoteProxy, proxy.Name, seen)...)

	return requests
}

// findStaleRefs finds MCPAuthzConfig resources that still list a workload in their
// ReferencingWorkloads status but are not in the seen set. This handles ref-removal
// and workload-deletion cases.
func (r *MCPAuthzConfigReconciler) findStaleRefs(
	ctx context.Context,
	namespace, workloadKind, workloadName string,
	seen map[types.NamespacedName]struct{},
) []reconcile.Request {
	authzConfigList := &mcpv1beta1.MCPAuthzConfigList{}
	if err := r.List(ctx, authzConfigList, client.InNamespace(namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list MCPAuthzConfigs for workload watch",
			"workloadKind", workloadKind, "workloadName", workloadName)
		return nil
	}

	var requests []reconcile.Request
	for _, cfg := range authzConfigList.Items {
		nn := types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}
		if _, already := seen[nn]; already {
			continue
		}
		for _, ref := range cfg.Status.ReferencingWorkloads {
			if ref.Kind == workloadKind && ref.Name == workloadName {
				requests = append(requests, reconcile.Request{NamespacedName: nn})
				break
			}
		}
	}
	return requests
}

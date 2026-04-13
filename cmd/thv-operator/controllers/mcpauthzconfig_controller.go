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

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	// Import authorizer backends so they register with the factory registry.
	_ "github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	_ "github.com/stacklok/toolhive/pkg/authz/authorizers/http"
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
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpauthzconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpauthzconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpauthzconfigs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPAuthzConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPAuthzConfig instance
	authzConfig := &mcpv1alpha1.MCPAuthzConfig{}
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

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(authzConfig, AuthzConfigFinalizerName) {
		controllerutil.AddFinalizer(authzConfig, AuthzConfigFinalizerName)
		if err := r.Update(ctx, authzConfig); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: authzConfigRequeueDelay}, nil
	}

	// Validate the authz configuration via the authorizer factory
	if err := validateAuthzConfigSpec(authzConfig.Spec); err != nil {
		logger.Error(err, "MCPAuthzConfig spec validation failed")
		meta.SetStatusCondition(&authzConfig.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeAuthzConfigValid,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonAuthzConfigInvalid,
			Message:            err.Error(),
			ObservedGeneration: authzConfig.Generation,
		})
		if updateErr := r.Status().Update(ctx, authzConfig); updateErr != nil {
			logger.Error(updateErr, "Failed to update status after validation error")
		}
		return ctrl.Result{}, nil // Don't requeue on validation errors - user must fix spec
	}

	// Validation succeeded - set Valid=True condition
	conditionChanged := meta.SetStatusCondition(&authzConfig.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeAuthzConfigValid,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonAuthzConfigValid,
		Message:            "Spec validation passed",
		ObservedGeneration: authzConfig.Generation,
	})

	// Calculate the hash of the current configuration
	configHash := ctrlutil.CalculateConfigHash(authzConfig.Spec)

	// Check if the hash has changed
	hashChanged := authzConfig.Status.ConfigHash != configHash
	if hashChanged {
		logger.Info("MCPAuthzConfig configuration changed",
			"oldHash", authzConfig.Status.ConfigHash,
			"newHash", configHash)

		authzConfig.Status.ConfigHash = configHash
		authzConfig.Status.ObservedGeneration = authzConfig.Generation

		if err := r.Status().Update(ctx, authzConfig); err != nil {
			logger.Error(err, "Failed to update MCPAuthzConfig status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Refresh ReferencingWorkloads list
	referencingWorkloads, err := r.findReferencingWorkloads(ctx, authzConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing workloads")
	} else if !ctrlutil.WorkloadRefsEqual(authzConfig.Status.ReferencingWorkloads, referencingWorkloads) {
		authzConfig.Status.ReferencingWorkloads = referencingWorkloads
		conditionChanged = true
	}

	// Update condition if it changed (even without hash change)
	if conditionChanged {
		if err := r.Status().Update(ctx, authzConfig); err != nil {
			logger.Error(err, "Failed to update MCPAuthzConfig status after condition change")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// validateAuthzConfigSpec validates the MCPAuthzConfig spec by reconstructing the
// full authorizer config and delegating to the factory's ValidateConfig method.
func validateAuthzConfigSpec(spec mcpv1alpha1.MCPAuthzConfigSpec) error {
	fullConfigJSON, err := ctrlutil.BuildFullAuthzConfigJSON(spec)
	if err != nil {
		return err
	}

	// Parse and validate via the authorizer factory
	var cfg authzConfig
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

// authzConfig is a minimal struct for extracting version and type from reconstructed JSON.
type authzConfig struct {
	Version string `json:"version"`
	Type    string `json:"type"`
}

// handleDeletion handles the deletion of a MCPAuthzConfig.
// Blocks deletion while workload resources reference this config by keeping the
// finalizer and requeueing. Once all references are removed, the finalizer is removed
// and the resource can be garbage collected.
func (r *MCPAuthzConfigReconciler) handleDeletion(
	ctx context.Context,
	authzConfig *mcpv1alpha1.MCPAuthzConfig,
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

			meta.SetStatusCondition(&authzConfig.Status.Conditions, metav1.Condition{
				Type:               mcpv1alpha1.ConditionTypeDeletionBlocked,
				Status:             metav1.ConditionTrue,
				Reason:             "ReferencedByWorkloads",
				Message:            fmt.Sprintf("Cannot delete: referenced by workloads: %v", referencingWorkloads),
				ObservedGeneration: authzConfig.Generation,
			})
			authzConfig.Status.ReferencingWorkloads = referencingWorkloads
			if updateErr := r.Status().Update(ctx, authzConfig); updateErr != nil {
				logger.Error(updateErr, "Failed to update status during deletion block")
			}

			// Requeue to check again later
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		controllerutil.RemoveFinalizer(authzConfig, AuthzConfigFinalizerName)
		if err := r.Update(ctx, authzConfig); err != nil {
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
	authzConfig *mcpv1alpha1.MCPAuthzConfig,
) ([]mcpv1alpha1.WorkloadReference, error) {
	// Find referencing MCPServers
	refs, err := ctrlutil.FindWorkloadRefsFromMCPServers(ctx, r.Client, authzConfig.Namespace, authzConfig.Name,
		func(server *mcpv1alpha1.MCPServer) *string {
			if server.Spec.AuthzConfigRef != nil {
				return &server.Spec.AuthzConfigRef.Name
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	// Check VirtualMCPServers
	vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(authzConfig.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list VirtualMCPServers: %w", err)
	}
	for _, vmcp := range vmcpList.Items {
		if vmcp.Spec.IncomingAuth != nil &&
			vmcp.Spec.IncomingAuth.AuthzConfigRef != nil &&
			vmcp.Spec.IncomingAuth.AuthzConfigRef.Name == authzConfig.Name {
			refs = append(refs, mcpv1alpha1.WorkloadReference{Kind: mcpv1alpha1.WorkloadKindVirtualMCPServer, Name: vmcp.Name})
		}
	}

	// Check MCPRemoteProxies
	proxyList := &mcpv1alpha1.MCPRemoteProxyList{}
	if err := r.List(ctx, proxyList, client.InNamespace(authzConfig.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies: %w", err)
	}
	for _, proxy := range proxyList.Items {
		if proxy.Spec.AuthzConfigRef != nil && proxy.Spec.AuthzConfigRef.Name == authzConfig.Name {
			refs = append(refs, mcpv1alpha1.WorkloadReference{Kind: mcpv1alpha1.WorkloadKindMCPRemoteProxy, Name: proxy.Name})
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
		For(&mcpv1alpha1.MCPAuthzConfig{}).
		Watches(&mcpv1alpha1.MCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPServerToAuthzConfig)).
		Watches(&mcpv1alpha1.VirtualMCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapVirtualMCPServerToAuthzConfig)).
		Watches(&mcpv1alpha1.MCPRemoteProxy{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPRemoteProxyToAuthzConfig)).
		Complete(r)
}

// mapMCPServerToAuthzConfig maps MCPServer changes to MCPAuthzConfig reconciliation requests.
func (r *MCPAuthzConfigReconciler) mapMCPServerToAuthzConfig(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	server, ok := obj.(*mcpv1alpha1.MCPServer)
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
	requests = append(requests, r.findStaleRefs(ctx, server.Namespace, mcpv1alpha1.WorkloadKindMCPServer, server.Name, seen)...)

	return requests
}

// mapVirtualMCPServerToAuthzConfig maps VirtualMCPServer changes to MCPAuthzConfig reconciliation requests.
func (r *MCPAuthzConfigReconciler) mapVirtualMCPServerToAuthzConfig(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	vmcp, ok := obj.(*mcpv1alpha1.VirtualMCPServer)
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

	requests = append(requests, r.findStaleRefs(ctx, vmcp.Namespace, mcpv1alpha1.WorkloadKindVirtualMCPServer, vmcp.Name, seen)...)

	return requests
}

// mapMCPRemoteProxyToAuthzConfig maps MCPRemoteProxy changes to MCPAuthzConfig reconciliation requests.
func (r *MCPAuthzConfigReconciler) mapMCPRemoteProxyToAuthzConfig(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	proxy, ok := obj.(*mcpv1alpha1.MCPRemoteProxy)
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

	requests = append(requests, r.findStaleRefs(ctx, proxy.Namespace, mcpv1alpha1.WorkloadKindMCPRemoteProxy, proxy.Name, seen)...)

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
	authzConfigList := &mcpv1alpha1.MCPAuthzConfigList{}
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

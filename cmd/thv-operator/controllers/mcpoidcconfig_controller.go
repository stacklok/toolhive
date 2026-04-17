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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
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
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpremoteproxies,verbs=get;list;watch

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

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(oidcConfig, OIDCConfigFinalizerName) {
		controllerutil.AddFinalizer(oidcConfig, OIDCConfigFinalizerName)
		if err := r.Update(ctx, oidcConfig); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: oidcConfigRequeueDelay}, nil
	}

	// Validate spec configuration early
	if err := oidcConfig.Validate(); err != nil {
		logger.Error(err, "MCPOIDCConfig spec validation failed")
		meta.SetStatusCondition(&oidcConfig.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeOIDCConfigValid,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonOIDCConfigInvalid,
			Message:            err.Error(),
			ObservedGeneration: oidcConfig.Generation,
		})
		if updateErr := r.Status().Update(ctx, oidcConfig); updateErr != nil {
			logger.Error(updateErr, "Failed to update status after validation error")
		}
		return ctrl.Result{}, nil // Don't requeue on validation errors - user must fix spec
	}

	// Validation succeeded - set Valid=True condition
	conditionChanged := meta.SetStatusCondition(&oidcConfig.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeOIDCConfigValid,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonOIDCConfigValid,
		Message:            "Spec validation passed",
		ObservedGeneration: oidcConfig.Generation,
	})

	// Calculate the hash of the current configuration
	configHash := r.calculateConfigHash(oidcConfig.Spec)

	// Check if the hash has changed
	hashChanged := oidcConfig.Status.ConfigHash != configHash
	if hashChanged {
		logger.Info("MCPOIDCConfig configuration changed",
			"oldHash", oidcConfig.Status.ConfigHash,
			"newHash", configHash)

		oidcConfig.Status.ConfigHash = configHash
		oidcConfig.Status.ObservedGeneration = oidcConfig.Generation

		if err := r.Status().Update(ctx, oidcConfig); err != nil {
			logger.Error(err, "Failed to update MCPOIDCConfig status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Refresh ReferencingWorkloads list
	referencingWorkloads, err := r.findReferencingWorkloads(ctx, oidcConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing workloads")
	} else if !ctrlutil.WorkloadRefsEqual(oidcConfig.Status.ReferencingWorkloads, referencingWorkloads) {
		oidcConfig.Status.ReferencingWorkloads = referencingWorkloads
		conditionChanged = true
	}

	// Update condition if it changed (even without hash change)
	if conditionChanged {
		if err := r.Status().Update(ctx, oidcConfig); err != nil {
			logger.Error(err, "Failed to update MCPOIDCConfig status after condition change")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
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

			meta.SetStatusCondition(&oidcConfig.Status.Conditions, metav1.Condition{
				Type:               mcpv1beta1.ConditionTypeDeletionBlocked,
				Status:             metav1.ConditionTrue,
				Reason:             "ReferencedByWorkloads",
				Message:            fmt.Sprintf("Cannot delete: referenced by workloads: %v", referencingWorkloads),
				ObservedGeneration: oidcConfig.Generation,
			})
			oidcConfig.Status.ReferencingWorkloads = referencingWorkloads
			if updateErr := r.Status().Update(ctx, oidcConfig); updateErr != nil {
				logger.Error(updateErr, "Failed to update status during deletion block")
			}

			// Requeue to check again later
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		controllerutil.RemoveFinalizer(oidcConfig, OIDCConfigFinalizerName)
		if err := r.Update(ctx, oidcConfig); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPOIDCConfig", "oidcConfig", oidcConfig.Name)
	}

	return ctrl.Result{}, nil
}

// findReferencingWorkloads returns the workload resources (MCPServer, VirtualMCPServer, and MCPRemoteProxy)
// that reference this MCPOIDCConfig via their OIDCConfigRef field.
func (r *MCPOIDCConfigReconciler) findReferencingWorkloads(
	ctx context.Context,
	oidcConfig *mcpv1beta1.MCPOIDCConfig,
) ([]mcpv1beta1.WorkloadReference, error) {
	// Find referencing MCPServers
	refs, err := ctrlutil.FindWorkloadRefsFromMCPServers(ctx, r.Client, oidcConfig.Namespace, oidcConfig.Name,
		func(server *mcpv1beta1.MCPServer) *string {
			if server.Spec.OIDCConfigRef != nil {
				return &server.Spec.OIDCConfigRef.Name
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	// Also check VirtualMCPServers
	vmcpList := &mcpv1beta1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(oidcConfig.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list VirtualMCPServers: %w", err)
	}
	for _, vmcp := range vmcpList.Items {
		if vmcp.Spec.IncomingAuth != nil &&
			vmcp.Spec.IncomingAuth.OIDCConfigRef != nil &&
			vmcp.Spec.IncomingAuth.OIDCConfigRef.Name == oidcConfig.Name {
			refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindVirtualMCPServer, Name: vmcp.Name})
		}
	}

	// Check MCPRemoteProxies
	proxyList := &mcpv1beta1.MCPRemoteProxyList{}
	if err := r.List(ctx, proxyList, client.InNamespace(oidcConfig.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies: %w", err)
	}
	for _, proxy := range proxyList.Items {
		if proxy.Spec.OIDCConfigRef != nil && proxy.Spec.OIDCConfigRef.Name == oidcConfig.Name {
			refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPRemoteProxy, Name: proxy.Name})
		}
	}

	ctrlutil.SortWorkloadRefs(refs)
	return refs, nil
}

// SetupWithManager sets up the controller with the Manager.
// Watches MCPServer, VirtualMCPServer, and MCPRemoteProxy changes to maintain accurate ReferencingWorkloads status.
func (r *MCPOIDCConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Watch MCPServer changes to update ReferencingWorkloads on referenced MCPOIDCConfigs.
	// This handler enqueues both the currently-referenced MCPOIDCConfig AND any
	// MCPOIDCConfig that still lists this server in ReferencingWorkloads (covers the
	// case where a server removes its oidcConfigRef — the previously-referenced
	// config needs to reconcile and clean up the stale entry).
	mcpServerHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			server, ok := obj.(*mcpv1beta1.MCPServer)
			if !ok {
				return nil
			}

			seen := make(map[types.NamespacedName]struct{})
			var requests []reconcile.Request

			// Enqueue the currently-referenced MCPOIDCConfig (if any)
			if server.Spec.OIDCConfigRef != nil {
				nn := types.NamespacedName{
					Name:      server.Spec.OIDCConfigRef.Name,
					Namespace: server.Namespace,
				}
				seen[nn] = struct{}{}
				requests = append(requests, reconcile.Request{NamespacedName: nn})
			}

			// Also enqueue any MCPOIDCConfig that still lists this server in
			// ReferencingWorkloads — handles ref-removal and server-deletion cases.
			oidcConfigList := &mcpv1beta1.MCPOIDCConfigList{}
			if err := r.List(ctx, oidcConfigList, client.InNamespace(server.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list MCPOIDCConfigs for MCPServer watch")
				return requests
			}
			for _, cfg := range oidcConfigList.Items {
				nn := types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}
				if _, already := seen[nn]; already {
					continue
				}
				for _, ref := range cfg.Status.ReferencingWorkloads {
					if ref.Kind == mcpv1beta1.WorkloadKindMCPServer && ref.Name == server.Name {
						requests = append(requests, reconcile.Request{NamespacedName: nn})
						break
					}
				}
			}

			return requests
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1beta1.MCPOIDCConfig{}).
		Watches(&mcpv1beta1.MCPServer{}, mcpServerHandler).
		Watches(
			&mcpv1beta1.VirtualMCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapVirtualMCPServerToOIDCConfig),
		).
		Watches(
			&mcpv1beta1.MCPRemoteProxy{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPRemoteProxyToOIDCConfig),
		).
		Complete(r)
}

// mapVirtualMCPServerToOIDCConfig maps VirtualMCPServer changes to MCPOIDCConfig reconciliation requests.
// Enqueues both the currently-referenced config and any config that still lists this
// VirtualMCPServer in ReferencingWorkloads (handles ref-removal / deletion).
func (r *MCPOIDCConfigReconciler) mapVirtualMCPServerToOIDCConfig(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	vmcp, ok := obj.(*mcpv1beta1.VirtualMCPServer)
	if !ok {
		return nil
	}

	seen := make(map[types.NamespacedName]struct{})
	var requests []reconcile.Request

	// Enqueue the currently-referenced MCPOIDCConfig (if any)
	if vmcp.Spec.IncomingAuth != nil && vmcp.Spec.IncomingAuth.OIDCConfigRef != nil {
		nn := types.NamespacedName{
			Name:      vmcp.Spec.IncomingAuth.OIDCConfigRef.Name,
			Namespace: vmcp.Namespace,
		}
		seen[nn] = struct{}{}
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}

	// Also enqueue any MCPOIDCConfig that still lists this VirtualMCPServer in
	// ReferencingWorkloads — handles ref-removal and deletion cases.
	oidcConfigList := &mcpv1beta1.MCPOIDCConfigList{}
	if err := r.List(ctx, oidcConfigList, client.InNamespace(vmcp.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list MCPOIDCConfigs for VirtualMCPServer watch")
		return requests
	}
	for _, cfg := range oidcConfigList.Items {
		nn := types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}
		if _, already := seen[nn]; already {
			continue
		}
		for _, ref := range cfg.Status.ReferencingWorkloads {
			if ref.Kind == mcpv1beta1.WorkloadKindVirtualMCPServer && ref.Name == vmcp.Name {
				requests = append(requests, reconcile.Request{NamespacedName: nn})
				break
			}
		}
	}

	return requests
}

// mapMCPRemoteProxyToOIDCConfig maps MCPRemoteProxy changes to MCPOIDCConfig reconciliation requests.
// Enqueues both the currently-referenced config and any config that still lists this
// MCPRemoteProxy in ReferencingWorkloads (handles ref-removal / deletion).
func (r *MCPOIDCConfigReconciler) mapMCPRemoteProxyToOIDCConfig(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	proxy, ok := obj.(*mcpv1beta1.MCPRemoteProxy)
	if !ok {
		return nil
	}

	seen := make(map[types.NamespacedName]struct{})
	var requests []reconcile.Request

	// Enqueue the currently-referenced MCPOIDCConfig (if any)
	if proxy.Spec.OIDCConfigRef != nil {
		nn := types.NamespacedName{
			Name:      proxy.Spec.OIDCConfigRef.Name,
			Namespace: proxy.Namespace,
		}
		seen[nn] = struct{}{}
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}

	// Also enqueue any MCPOIDCConfig that still lists this MCPRemoteProxy in
	// ReferencingWorkloads — handles ref-removal and deletion cases.
	oidcConfigList := &mcpv1beta1.MCPOIDCConfigList{}
	if err := r.List(ctx, oidcConfigList, client.InNamespace(proxy.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list MCPOIDCConfigs for MCPRemoteProxy watch")
		return requests
	}
	for _, cfg := range oidcConfigList.Items {
		nn := types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}
		if _, already := seen[nn]; already {
			continue
		}
		for _, ref := range cfg.Status.ReferencingWorkloads {
			if ref.Kind == mcpv1beta1.WorkloadKindMCPRemoteProxy && ref.Name == proxy.Name {
				requests = append(requests, reconcile.Request{NamespacedName: nn})
				break
			}
		}
	}

	return requests
}

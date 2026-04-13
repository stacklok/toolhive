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

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPTelemetryConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPTelemetryConfig instance
	telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{}
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
			Type:               mcpv1alpha1.ConditionTypeValid,
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
		Type:               mcpv1alpha1.ConditionTypeValid,
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

// SetupWithManager sets up the controller with the Manager.
// Watches MCPServer changes to maintain accurate ReferencingWorkloads status.
func (r *MCPTelemetryConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Watch MCPServer changes to update ReferencingWorkloads on referenced MCPTelemetryConfigs.
	// This handler enqueues both the currently-referenced MCPTelemetryConfig AND any
	// MCPTelemetryConfig that still lists this server in ReferencingWorkloads (covers the
	// case where a server removes its telemetryConfigRef — the previously-referenced
	// config needs to reconcile and clean up the stale entry).
	mcpServerHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			server, ok := obj.(*mcpv1alpha1.MCPServer)
			if !ok {
				return nil
			}

			seen := make(map[types.NamespacedName]struct{})
			var requests []reconcile.Request

			// Enqueue the currently-referenced MCPTelemetryConfig (if any)
			if server.Spec.TelemetryConfigRef != nil {
				nn := types.NamespacedName{
					Name:      server.Spec.TelemetryConfigRef.Name,
					Namespace: server.Namespace,
				}
				seen[nn] = struct{}{}
				requests = append(requests, reconcile.Request{NamespacedName: nn})
			}

			// Also enqueue any MCPTelemetryConfig that still lists this server in
			// ReferencingWorkloads — handles ref-removal and server-deletion cases.
			telemetryConfigList := &mcpv1alpha1.MCPTelemetryConfigList{}
			if err := r.List(ctx, telemetryConfigList, client.InNamespace(server.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list MCPTelemetryConfigs for MCPServer watch")
				return requests
			}
			for _, cfg := range telemetryConfigList.Items {
				nn := types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}
				if _, already := seen[nn]; already {
					continue
				}
				for _, ref := range cfg.Status.ReferencingWorkloads {
					if ref.Kind == mcpv1alpha1.WorkloadKindMCPServer && ref.Name == server.Name {
						requests = append(requests, reconcile.Request{NamespacedName: nn})
						break
					}
				}
			}

			return requests
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPTelemetryConfig{}).
		Watches(&mcpv1alpha1.MCPServer{}, mcpServerHandler).
		Watches(
			&mcpv1alpha1.MCPRemoteProxy{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPRemoteProxyToTelemetryConfig),
		).
		Complete(r)
}

// mapMCPRemoteProxyToTelemetryConfig enqueues MCPTelemetryConfig reconcile requests
// when an MCPRemoteProxy changes. Handles both the currently-referenced config and
// any config that still lists this proxy in ReferencingWorkloads (ref-removal case).
func (r *MCPTelemetryConfigReconciler) mapMCPRemoteProxyToTelemetryConfig(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	proxy, ok := obj.(*mcpv1alpha1.MCPRemoteProxy)
	if !ok {
		return nil
	}

	seen := make(map[types.NamespacedName]struct{})
	var requests []reconcile.Request

	if proxy.Spec.TelemetryConfigRef != nil {
		nn := types.NamespacedName{
			Name:      proxy.Spec.TelemetryConfigRef.Name,
			Namespace: proxy.Namespace,
		}
		seen[nn] = struct{}{}
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}

	// Also enqueue any MCPTelemetryConfig that still lists this proxy in
	// ReferencingWorkloads — handles ref-removal and proxy-deletion cases.
	telemetryConfigList := &mcpv1alpha1.MCPTelemetryConfigList{}
	if err := r.List(ctx, telemetryConfigList, client.InNamespace(proxy.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list MCPTelemetryConfigs for MCPRemoteProxy watch")
		return requests
	}
	for _, cfg := range telemetryConfigList.Items {
		nn := types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}
		if _, already := seen[nn]; already {
			continue
		}
		for _, ref := range cfg.Status.ReferencingWorkloads {
			if ref.Kind == mcpv1alpha1.WorkloadKindMCPRemoteProxy && ref.Name == proxy.Name {
				requests = append(requests, reconcile.Request{NamespacedName: nn})
				break
			}
		}
	}

	return requests
}

// calculateConfigHash calculates a hash of the MCPTelemetryConfig spec using Kubernetes utilities
func (*MCPTelemetryConfigReconciler) calculateConfigHash(spec mcpv1alpha1.MCPTelemetryConfigSpec) string {
	return ctrlutil.CalculateConfigHash(spec)
}

// handleDeletion handles the deletion of a MCPTelemetryConfig.
// Blocks deletion while MCPServer resources reference this config (deletion protection).
func (r *MCPTelemetryConfigReconciler) handleDeletion(
	ctx context.Context,
	telemetryConfig *mcpv1alpha1.MCPTelemetryConfig,
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
			Type:               mcpv1alpha1.ConditionTypeDeletionBlocked,
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

// findReferencingWorkloads returns a sorted list of workload references in the same namespace
// that reference this MCPTelemetryConfig via TelemetryConfigRef.
func (r *MCPTelemetryConfigReconciler) findReferencingWorkloads(
	ctx context.Context,
	telemetryConfig *mcpv1alpha1.MCPTelemetryConfig,
) ([]mcpv1alpha1.WorkloadReference, error) {
	serverRefs, err := ctrlutil.FindWorkloadRefsFromMCPServers(ctx, r.Client, telemetryConfig.Namespace, telemetryConfig.Name,
		func(server *mcpv1alpha1.MCPServer) *string {
			if server.Spec.TelemetryConfigRef != nil {
				return &server.Spec.TelemetryConfigRef.Name
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	proxies, err := ctrlutil.FindReferencingMCPRemoteProxies(ctx, r.Client, telemetryConfig.Namespace, telemetryConfig.Name,
		func(proxy *mcpv1alpha1.MCPRemoteProxy) *string {
			if proxy.Spec.TelemetryConfigRef != nil {
				return &proxy.Spec.TelemetryConfigRef.Name
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	refs := make([]mcpv1alpha1.WorkloadReference, 0, len(serverRefs)+len(proxies))
	refs = append(refs, serverRefs...)
	for _, proxy := range proxies {
		refs = append(refs, mcpv1alpha1.WorkloadReference{Kind: mcpv1alpha1.WorkloadKindMCPRemoteProxy, Name: proxy.Name})
	}
	ctrlutil.SortWorkloadRefs(refs)
	return refs, nil
}

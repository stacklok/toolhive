// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
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
	// OIDCConfigFinalizerName is the name of the finalizer for MCPOIDCConfig
	OIDCConfigFinalizerName = "mcpoidcconfig.toolhive.stacklok.dev/finalizer"

	// oidcConfigRequeueDelay is the delay before requeuing after adding a finalizer
	oidcConfigRequeueDelay = 500 * time.Millisecond

	// oidcConfigDeletionRequeueDelay is the delay before requeuing when deletion is blocked
	oidcConfigDeletionRequeueDelay = 30 * time.Second
)

// MCPOIDCConfigReconciler reconciles a MCPOIDCConfig object
type MCPOIDCConfigReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPOIDCConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPOIDCConfig instance
	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{}
	err := r.Get(ctx, req.NamespacedName, oidcConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			logger.Info("MCPOIDCConfig resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
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
		// Requeue to continue processing after finalizer is added
		return ctrl.Result{RequeueAfter: oidcConfigRequeueDelay}, nil
	}

	// Validate spec configuration early
	if err := oidcConfig.Validate(); err != nil {
		logger.Error(err, "MCPOIDCConfig spec validation failed")
		// Update status with validation error
		meta.SetStatusCondition(&oidcConfig.Status.Conditions, metav1.Condition{
			Type:               "Valid",
			Status:             metav1.ConditionFalse,
			Reason:             "ValidationFailed",
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
		Type:               "Valid",
		Status:             metav1.ConditionTrue,
		Reason:             "ValidationSucceeded",
		Message:            "Spec validation passed",
		ObservedGeneration: oidcConfig.Generation,
	})

	// Calculate the hash of the current configuration
	configHash := r.calculateConfigHash(oidcConfig.Spec)

	// Check if the hash has changed
	hashChanged := oidcConfig.Status.ConfigHash != configHash
	if hashChanged {
		return r.handleConfigHashChange(ctx, oidcConfig, configHash)
	}

	// Update condition if it changed (even without hash change)
	if conditionChanged {
		if err := r.Status().Update(ctx, oidcConfig); err != nil {
			logger.Error(err, "Failed to update MCPOIDCConfig status after condition change")
			return ctrl.Result{}, err
		}
	}

	// Even when hash hasn't changed, update referencing servers list.
	// This ensures ReferencingServers is updated when MCPServers are created or deleted.
	return r.updateReferencingServers(ctx, oidcConfig)
}

// calculateConfigHash calculates a hash of the MCPOIDCConfig spec using Kubernetes utilities
func (*MCPOIDCConfigReconciler) calculateConfigHash(spec mcpv1alpha1.MCPOIDCConfigSpec) string {
	return ctrlutil.CalculateConfigHash(spec)
}

// handleConfigHashChange handles the logic when the config hash changes
func (r *MCPOIDCConfigReconciler) handleConfigHashChange(
	ctx context.Context,
	oidcConfig *mcpv1alpha1.MCPOIDCConfig,
	configHash string,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("MCPOIDCConfig configuration changed",
		"oldHash", oidcConfig.Status.ConfigHash,
		"newHash", configHash)

	// Update the status with the new hash
	oidcConfig.Status.ConfigHash = configHash
	oidcConfig.Status.ObservedGeneration = oidcConfig.Generation

	// Find all MCPServers that reference this MCPOIDCConfig
	referencingServers, err := r.findReferencingMCPServers(ctx, oidcConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing MCPServers")
		return ctrl.Result{}, fmt.Errorf("failed to find referencing MCPServers: %w", err)
	}

	// Update the status with the list of referencing servers
	serverNames := make([]string, 0, len(referencingServers))
	for _, server := range referencingServers {
		serverNames = append(serverNames, server.Name)
	}
	slices.Sort(serverNames)
	oidcConfig.Status.ReferencingServers = serverNames

	// Update the MCPOIDCConfig status
	if err := r.Status().Update(ctx, oidcConfig); err != nil {
		logger.Error(err, "Failed to update MCPOIDCConfig status")
		return ctrl.Result{}, err
	}

	// Trigger reconciliation of all referencing MCPServers
	for _, server := range referencingServers {
		logger.Info("Triggering reconciliation of MCPServer due to MCPOIDCConfig change",
			"mcpserver", server.Name, "oidcConfig", oidcConfig.Name)

		// Add an annotation to the MCPServer to trigger reconciliation
		if server.Annotations == nil {
			server.Annotations = make(map[string]string)
		}
		server.Annotations["toolhive.stacklok.dev/oidcconfig-hash"] = configHash

		if err := r.Update(ctx, &server); err != nil {
			logger.Error(err, "Failed to update MCPServer annotation", "mcpserver", server.Name)
			// Continue with other servers even if one fails
		}
	}

	r.checkDuplicateAudiences(oidcConfig, referencingServers)

	return ctrl.Result{}, nil
}

// handleDeletion handles the deletion of a MCPOIDCConfig
func (r *MCPOIDCConfigReconciler) handleDeletion(
	ctx context.Context,
	oidcConfig *mcpv1alpha1.MCPOIDCConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(oidcConfig, OIDCConfigFinalizerName) {
		// Check if any MCPServers are still referencing this MCPOIDCConfig
		referencingServers, err := r.findReferencingMCPServers(ctx, oidcConfig)
		if err != nil {
			logger.Error(err, "Failed to find referencing MCPServers during deletion")
			return ctrl.Result{}, err
		}

		if len(referencingServers) > 0 {
			// Cannot delete - still referenced
			serverNames := make([]string, 0, len(referencingServers))
			for _, server := range referencingServers {
				serverNames = append(serverNames, server.Name)
			}
			logger.Info("Cannot delete MCPOIDCConfig - still referenced by MCPServers",
				"oidcConfig", oidcConfig.Name, "referencingServers", serverNames)

			// Update status to show it's still referenced
			oidcConfig.Status.ReferencingServers = serverNames

			// Set a DeletionBlocked condition for kubectl visibility
			meta.SetStatusCondition(&oidcConfig.Status.Conditions, metav1.Condition{
				Type:               "DeletionBlocked",
				Status:             metav1.ConditionTrue,
				Reason:             "ReferencedByServers",
				Message:            fmt.Sprintf("Cannot delete: referenced by MCPServers: %v", serverNames),
				ObservedGeneration: oidcConfig.Generation,
			})
			if err := r.Status().Update(ctx, oidcConfig); err != nil {
				logger.Error(err, "Failed to update status during deletion")
			}

			// Requeue with a delay instead of returning error (avoids exponential backoff)
			return ctrl.Result{RequeueAfter: oidcConfigDeletionRequeueDelay}, nil
		}

		// No references, safe to remove finalizer and allow deletion
		controllerutil.RemoveFinalizer(oidcConfig, OIDCConfigFinalizerName)
		if err := r.Update(ctx, oidcConfig); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPOIDCConfig", "oidcConfig", oidcConfig.Name)
	}

	return ctrl.Result{}, nil
}

// findReferencingMCPServers finds all MCPServers that reference the given MCPOIDCConfig
func (r *MCPOIDCConfigReconciler) findReferencingMCPServers(
	ctx context.Context,
	oidcConfig *mcpv1alpha1.MCPOIDCConfig,
) ([]mcpv1alpha1.MCPServer, error) {
	return ctrlutil.FindReferencingMCPServers(ctx, r.Client, oidcConfig.Namespace, oidcConfig.Name,
		func(server *mcpv1alpha1.MCPServer) *string {
			if server.Spec.OIDCConfigRef != nil {
				return &server.Spec.OIDCConfigRef.Name
			}
			return nil
		})
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPOIDCConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a handler that maps MCPServer changes to MCPOIDCConfig reconciliation requests.
	// When an MCPServer is created, updated, or deleted, we reconcile the MCPOIDCConfig
	// it references so that the ReferencingServers status field stays up to date.
	mcpServerHandler := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, obj client.Object) []reconcile.Request {
			mcpServer, ok := obj.(*mcpv1alpha1.MCPServer)
			if !ok {
				return nil
			}

			if mcpServer.Spec.OIDCConfigRef == nil {
				return nil
			}

			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Name:      mcpServer.Spec.OIDCConfigRef.Name,
					Namespace: mcpServer.Namespace,
				},
			}}
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPOIDCConfig{}).
		// Watch for MCPServers and reconcile the referenced MCPOIDCConfig when they change
		Watches(&mcpv1alpha1.MCPServer{}, mcpServerHandler).
		Complete(r)
}

// updateReferencingServers finds referencing MCPServers and updates the status if the list changed
func (r *MCPOIDCConfigReconciler) updateReferencingServers(
	ctx context.Context,
	oidcConfig *mcpv1alpha1.MCPOIDCConfig,
) (ctrl.Result, error) {
	referencingServers, err := r.findReferencingMCPServers(ctx, oidcConfig)
	if err != nil {
		logger := log.FromContext(ctx)
		logger.Error(err, "Failed to find referencing MCPServers")
		return ctrl.Result{}, fmt.Errorf("failed to find referencing MCPServers: %w", err)
	}

	serverNames := make([]string, 0, len(referencingServers))
	for _, server := range referencingServers {
		serverNames = append(serverNames, server.Name)
	}
	slices.Sort(serverNames)

	if !slices.Equal(oidcConfig.Status.ReferencingServers, serverNames) {
		oidcConfig.Status.ReferencingServers = serverNames
		if err := r.Status().Update(ctx, oidcConfig); err != nil {
			logger := log.FromContext(ctx)
			logger.Error(err, "Failed to update MCPOIDCConfig status")
			return ctrl.Result{}, err
		}
	}

	// Check for duplicate audiences whenever the referencing servers list changes
	r.checkDuplicateAudiences(oidcConfig, referencingServers)

	return ctrl.Result{}, nil
}

// checkDuplicateAudiences emits a Warning event when multiple MCPServers referencing
// the same MCPOIDCConfig share the same audience value. Duplicate audiences can lead
// to token replay attacks where a token intended for one server is accepted by another.
func (r *MCPOIDCConfigReconciler) checkDuplicateAudiences(
	oidcConfig *mcpv1alpha1.MCPOIDCConfig,
	referencingServers []mcpv1alpha1.MCPServer,
) {
	audienceMap := make(map[string][]string) // audience -> server names
	for _, server := range referencingServers {
		if server.Spec.OIDCConfigRef != nil {
			audienceMap[server.Spec.OIDCConfigRef.Audience] = append(
				audienceMap[server.Spec.OIDCConfigRef.Audience], server.Name)
		}
	}
	for audience, servers := range audienceMap {
		if len(servers) > 1 {
			r.Recorder.Eventf(oidcConfig, nil, corev1.EventTypeWarning, "DuplicateAudience", "ConfigValidation",
				"Multiple MCPServers share audience %q: %v. Unique audiences are recommended to prevent token replay.", audience, servers)
		}
	}
}

// GetOIDCConfigForMCPServer retrieves the MCPOIDCConfig referenced by an MCPServer.
// This function is exported for use by the MCPServer controller.
func GetOIDCConfigForMCPServer(
	ctx context.Context,
	c client.Client,
	mcpServer *mcpv1alpha1.MCPServer,
) (*mcpv1alpha1.MCPOIDCConfig, error) {
	if mcpServer.Spec.OIDCConfigRef == nil {
		// We throw an error because in this case you assume there is a OIDCConfig
		// but there isn't one referenced.
		return nil, fmt.Errorf("MCPServer %s does not reference an MCPOIDCConfig", mcpServer.Name)
	}

	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      mcpServer.Spec.OIDCConfigRef.Name,
		Namespace: mcpServer.Namespace, // Same namespace as MCPServer
	}, oidcConfig)

	if err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("MCPOIDCConfig %s not found in namespace %s",
				mcpServer.Spec.OIDCConfigRef.Name, mcpServer.Namespace)
		}
		return nil, fmt.Errorf("failed to get MCPOIDCConfig: %w", err)
	}

	return oidcConfig, nil
}

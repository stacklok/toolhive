// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

const (
	// WebhookConfigFinalizerName is the name of the finalizer for MCPWebhookConfig
	WebhookConfigFinalizerName = "mcpwebhookconfig.toolhive.stacklok.dev/finalizer"

	// webhookConfigRequeueDelay is the delay before requeuing after adding a finalizer
	webhookConfigRequeueDelay = 500 * time.Millisecond
)

// MCPWebhookConfigReconciler reconciles a MCPWebhookConfig object
type MCPWebhookConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpwebhookconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpwebhookconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpwebhookconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch;update;patch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *MCPWebhookConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPWebhookConfig instance
	webhookConfig := &mcpv1alpha1.MCPWebhookConfig{}
	err := r.Get(ctx, req.NamespacedName, webhookConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("MCPWebhookConfig resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get MCPWebhookConfig")
		return ctrl.Result{}, err
	}

	// Check if the MCPWebhookConfig is being deleted
	if !webhookConfig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, webhookConfig)
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(webhookConfig, WebhookConfigFinalizerName) {
		controllerutil.AddFinalizer(webhookConfig, WebhookConfigFinalizerName)
		if err := r.Update(ctx, webhookConfig); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: webhookConfigRequeueDelay}, nil
	}

	// Since validation is mostly handled by CEL, we assume it's structurally valid if it was saved.
	conditionChanged := meta.SetStatusCondition(&webhookConfig.Status.Conditions, metav1.Condition{
		Type:               "Valid",
		Status:             metav1.ConditionTrue,
		Reason:             "ValidationSucceeded",
		Message:            "Spec validation passed",
		ObservedGeneration: webhookConfig.Generation,
	})

	// Calculate the hash of the current configuration
	configHash := r.calculateConfigHash(webhookConfig.Spec)

	// Check if the hash has changed
	hashChanged := webhookConfig.Status.ConfigHash != configHash
	if hashChanged {
		if err := r.handleConfigHashChange(ctx, webhookConfig, configHash); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Update condition if it changed
	if conditionChanged {
		if err := r.Status().Update(ctx, webhookConfig); err != nil {
			logger.Error(err, "Failed to update MCPWebhookConfig status after condition change")
			return ctrl.Result{}, err
		}
	}

	// Even when hash hasn't changed, update referencing servers list.
	return r.updateReferencingServers(ctx, webhookConfig)
}

// calculateConfigHash calculates a hash of the MCPWebhookConfig spec
func (*MCPWebhookConfigReconciler) calculateConfigHash(spec mcpv1alpha1.MCPWebhookConfigSpec) string {
	return ctrlutil.CalculateConfigHash(spec)
}

// handleConfigHashChange handles the logic when the config hash changes
func (r *MCPWebhookConfigReconciler) handleConfigHashChange(
	ctx context.Context,
	webhookConfig *mcpv1alpha1.MCPWebhookConfig,
	configHash string,
) error {
	logger := log.FromContext(ctx)
	logger.Info("MCPWebhookConfig configuration changed",
		"oldHash", webhookConfig.Status.ConfigHash,
		"newHash", configHash)

	webhookConfig.Status.ConfigHash = configHash
	webhookConfig.Status.ObservedGeneration = webhookConfig.Generation

	referencingServers, err := r.findReferencingMCPServers(ctx, webhookConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing MCPServers")
		return fmt.Errorf("failed to find referencing MCPServers: %w", err)
	}

	serverNames := make([]string, 0, len(referencingServers))
	for _, server := range referencingServers {
		serverNames = append(serverNames, server.Name)
	}
	slices.Sort(serverNames)
	webhookConfig.Status.ReferencingServers = serverNames

	if err := r.Status().Update(ctx, webhookConfig); err != nil {
		logger.Error(err, "Failed to update MCPWebhookConfig status")
		return err
	}

	for _, server := range referencingServers {
		logger.Info("Triggering reconciliation of MCPServer due to MCPWebhookConfig change",
			"mcpserver", server.Name, "webhookConfig", webhookConfig.Name)

		if server.Annotations == nil {
			server.Annotations = make(map[string]string)
		}
		server.Annotations["toolhive.stacklok.dev/webhookconfig-hash"] = configHash

		if err := r.Update(ctx, &server); err != nil {
			logger.Error(err, "Failed to update MCPServer annotation", "mcpserver", server.Name)
		}
	}

	return nil
}

// handleDeletion handles the deletion of a MCPWebhookConfig
func (r *MCPWebhookConfigReconciler) handleDeletion(
	ctx context.Context,
	webhookConfig *mcpv1alpha1.MCPWebhookConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(webhookConfig, WebhookConfigFinalizerName) {
		referencingServers, err := r.findReferencingMCPServers(ctx, webhookConfig)
		if err != nil {
			logger.Error(err, "Failed to find referencing MCPServers during deletion")
			return ctrl.Result{}, err
		}

		if len(referencingServers) > 0 {
			serverNames := make([]string, 0, len(referencingServers))
			for _, server := range referencingServers {
				serverNames = append(serverNames, server.Name)
			}
			logger.Info("Cannot delete MCPWebhookConfig - still referenced by MCPServers",
				"webhookConfig", webhookConfig.Name, "referencingServers", serverNames)

			webhookConfig.Status.ReferencingServers = serverNames
			if err := r.Status().Update(ctx, webhookConfig); err != nil {
				logger.Error(err, "Failed to update MCPWebhookConfig status during deletion")
			}

			return ctrl.Result{}, fmt.Errorf("MCPWebhookConfig %s is still referenced by MCPServers: %v",
				webhookConfig.Name, serverNames)
		}

		controllerutil.RemoveFinalizer(webhookConfig, WebhookConfigFinalizerName)
		if err := r.Update(ctx, webhookConfig); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPWebhookConfig", "webhookConfig", webhookConfig.Name)
	}

	return ctrl.Result{}, nil
}

// findReferencingMCPServers finds all MCPServers that reference the given MCPWebhookConfig
func (r *MCPWebhookConfigReconciler) findReferencingMCPServers(
	ctx context.Context,
	webhookConfig *mcpv1alpha1.MCPWebhookConfig,
) ([]mcpv1alpha1.MCPServer, error) {
	return ctrlutil.FindReferencingMCPServers(ctx, r.Client, webhookConfig.Namespace, webhookConfig.Name,
		func(server *mcpv1alpha1.MCPServer) *string {
			if server.Spec.WebhookConfigRef != nil {
				return &server.Spec.WebhookConfigRef.Name
			}
			return nil
		})
}

// updateReferencingServers updates the list of MCPServers referencing this config
func (r *MCPWebhookConfigReconciler) updateReferencingServers(
	ctx context.Context,
	webhookConfig *mcpv1alpha1.MCPWebhookConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	referencingServers, err := r.findReferencingMCPServers(ctx, webhookConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing MCPServers")
		return ctrl.Result{}, err
	}

	serverNames := make([]string, 0, len(referencingServers))
	for _, server := range referencingServers {
		serverNames = append(serverNames, server.Name)
	}
	slices.Sort(serverNames)

	if !slices.Equal(webhookConfig.Status.ReferencingServers, serverNames) {
		webhookConfig.Status.ReferencingServers = serverNames
		if err := r.Status().Update(ctx, webhookConfig); err != nil {
			logger.Error(err, "Failed to update referencing servers list")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPWebhookConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPWebhookConfig{}).
		Complete(r)
}

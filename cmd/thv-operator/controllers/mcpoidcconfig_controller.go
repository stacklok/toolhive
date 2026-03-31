// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
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
	// OIDCConfigFinalizerName is the name of the finalizer for MCPOIDCConfig
	OIDCConfigFinalizerName = "mcpoidcconfig.toolhive.stacklok.dev/finalizer"

	// oidcConfigRequeueDelay is the delay before requeuing after adding a finalizer
	oidcConfigRequeueDelay = 500 * time.Millisecond
)

// MCPOIDCConfigReconciler reconciles a MCPOIDCConfig object.
//
// This controller manages the lifecycle of MCPOIDCConfig resources: validation,
// config hash computation, and finalizer management. Reference tracking, cascade
// to workloads, and deletion protection will be wired up when workload CRDs add
// OIDCConfigRef fields (#4253).
type MCPOIDCConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPOIDCConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPOIDCConfig instance
	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{}
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
func (*MCPOIDCConfigReconciler) calculateConfigHash(spec mcpv1alpha1.MCPOIDCConfigSpec) string {
	return ctrlutil.CalculateConfigHash(spec)
}

// handleDeletion handles the deletion of a MCPOIDCConfig.
// Currently allows immediate deletion by removing the finalizer.
// Deletion protection (blocking while workloads reference this config)
// will be added when workload CRDs gain OIDCConfigRef fields (#4253).
func (r *MCPOIDCConfigReconciler) handleDeletion(
	ctx context.Context,
	oidcConfig *mcpv1alpha1.MCPOIDCConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(oidcConfig, OIDCConfigFinalizerName) {
		controllerutil.RemoveFinalizer(oidcConfig, OIDCConfigFinalizerName)
		if err := r.Update(ctx, oidcConfig); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPOIDCConfig", "oidcConfig", oidcConfig.Name)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
// When workload CRDs gain OIDCConfigRef fields (#4253), this should also
// watch MCPServer changes to update ReferencingServers status.
func (r *MCPOIDCConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPOIDCConfig{}).
		Complete(r)
}

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
	// TelemetryConfigFinalizerName is the name of the finalizer for MCPTelemetryConfig
	TelemetryConfigFinalizerName = "mcptelemetryconfig.toolhive.stacklok.dev/finalizer"

	// telemetryConfigRequeueDelay is the delay before requeuing after adding a finalizer
	telemetryConfigRequeueDelay = 500 * time.Millisecond
)

// MCPTelemetryConfigReconciler reconciles a MCPTelemetryConfig object.
//
// This controller manages the lifecycle of MCPTelemetryConfig resources: validation,
// config hash computation, and finalizer management. Reference tracking, cascade
// to workloads, and deletion protection will be wired up when MCPServer gains a
// TelemetryConfigRef field.
type MCPTelemetryConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptelemetryconfigs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptelemetryconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptelemetryconfigs/finalizers,verbs=update

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
			Type:               "Valid",
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
		Type:               "Valid",
		Status:             metav1.ConditionTrue,
		Reason:             "ValidationSucceeded",
		Message:            "Spec validation passed",
		ObservedGeneration: telemetryConfig.Generation,
	})

	// Calculate the hash of the current configuration
	configHash := r.calculateConfigHash(telemetryConfig.Spec)

	// Check if the hash has changed
	hashChanged := telemetryConfig.Status.ConfigHash != configHash
	if hashChanged {
		logger.Info("MCPTelemetryConfig configuration changed",
			"oldHash", telemetryConfig.Status.ConfigHash,
			"newHash", configHash)

		telemetryConfig.Status.ConfigHash = configHash
		telemetryConfig.Status.ObservedGeneration = telemetryConfig.Generation

		if err := r.Status().Update(ctx, telemetryConfig); err != nil {
			logger.Error(err, "Failed to update MCPTelemetryConfig status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Update condition if it changed (even without hash change)
	if conditionChanged {
		if err := r.Status().Update(ctx, telemetryConfig); err != nil {
			logger.Error(err, "Failed to update MCPTelemetryConfig status after condition change")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
// When MCPServer gains a TelemetryConfigRef field, this should also
// watch MCPServer changes to update ReferencingServers status.
func (r *MCPTelemetryConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPTelemetryConfig{}).
		Complete(r)
}

// calculateConfigHash calculates a hash of the MCPTelemetryConfig spec using Kubernetes utilities
func (*MCPTelemetryConfigReconciler) calculateConfigHash(spec mcpv1alpha1.MCPTelemetryConfigSpec) string {
	return ctrlutil.CalculateConfigHash(spec)
}

// handleDeletion handles the deletion of a MCPTelemetryConfig.
// Currently allows immediate deletion by removing the finalizer.
// Deletion protection (blocking while workloads reference this config)
// will be added when MCPServer gains a TelemetryConfigRef field.
func (r *MCPTelemetryConfigReconciler) handleDeletion(
	ctx context.Context,
	telemetryConfig *mcpv1alpha1.MCPTelemetryConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(telemetryConfig, TelemetryConfigFinalizerName) {
		controllerutil.RemoveFinalizer(telemetryConfig, TelemetryConfigFinalizerName)
		if err := r.Update(ctx, telemetryConfig); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPTelemetryConfig", "telemetryConfig", telemetryConfig.Name)
	}

	return ctrl.Result{}, nil
}

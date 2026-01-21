// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/mcpregistrystatus"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi"
)

// Default timing constants for the controller
const (
	// DefaultControllerRetryAfterConstant is the constant default retry interval for controller operations that fail
	DefaultControllerRetryAfterConstant = time.Minute * 5
)

// Configurable timing variables for testing
var (
	// DefaultControllerRetryAfter is the configurable default retry interval for controller operations that fail
	// This can be modified in tests to speed up retry behavior
	DefaultControllerRetryAfter = DefaultControllerRetryAfterConstant
)

// MCPRegistryReconciler reconciles a MCPRegistry object
type MCPRegistryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Registry API manager handles API deployment operations
	registryAPIManager registryapi.Manager
}

// NewMCPRegistryReconciler creates a new MCPRegistryReconciler with required dependencies
func NewMCPRegistryReconciler(k8sClient client.Client, scheme *runtime.Scheme) *MCPRegistryReconciler {
	registryAPIManager := registryapi.NewManager(k8sClient, scheme)
	return &MCPRegistryReconciler{
		Client:             k8sClient,
		Scheme:             scheme,
		registryAPIManager: registryAPIManager,
	}
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//
// For creating registry-api deployment and service
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//
// For creating registry-api RBAC resources
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
//
// For granting registry-api permissions (operator must have these to grant them via Role)
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers;mcpremoteproxies;virtualmcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes;gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
//nolint:gocyclo // Complex reconciliation logic requires multiple conditions
func (r *MCPRegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// 1. Fetch MCPRegistry instance
	mcpRegistry := &mcpv1alpha1.MCPRegistry{}
	err := r.Get(ctx, req.NamespacedName, mcpRegistry)
	if err != nil {
		if kerrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			ctxLogger.Info("MCPRegistry resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		ctxLogger.Error(err, "Failed to get MCPRegistry")
		return ctrl.Result{}, err
	}
	// Safe access to nested status fields
	var apiPhase any
	if mcpRegistry.Status.APIStatus != nil {
		apiPhase = mcpRegistry.Status.APIStatus.Phase
	}

	apiEndpoint := ""
	if mcpRegistry.Status.APIStatus != nil {
		apiEndpoint = mcpRegistry.Status.APIStatus.Endpoint
	}
	ctxLogger.Info("Reconciling MCPRegistry", "MCPRegistry.Name", mcpRegistry.Name, "phase", mcpRegistry.Status.Phase,
		"apiPhase", apiPhase, "apiEndpoint", apiEndpoint)

	// Validate PodTemplateSpec early - before other operations
	if mcpRegistry.HasPodTemplateSpec() {
		// Validate PodTemplateSpec early - before other operations
		// This ensures we fail fast if the spec is invalid
		if !r.validateAndUpdatePodTemplateStatus(ctx, mcpRegistry) {
			// Invalid PodTemplateSpec - return without error to avoid infinite retries
			// The user must fix the spec and the next reconciliation will retry
			return ctrl.Result{}, nil
		}
	}

	// 2. Handle deletion if DeletionTimestamp is set
	if mcpRegistry.GetDeletionTimestamp() != nil {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(mcpRegistry, "mcpregistry.toolhive.stacklok.dev/finalizer") {
			// Run finalization logic. If the finalization logic fails,
			// don't remove the finalizer so that we can retry during the next reconciliation.
			if err := r.finalizeMCPRegistry(ctx, mcpRegistry); err != nil {
				ctxLogger.Error(err, "Reconciliation completed with error while finalizing MCPRegistry",
					"MCPRegistry.Name", mcpRegistry.Name)
				return ctrl.Result{}, err
			}

			// Remove the finalizer. Once all finalizers have been removed, the object will be deleted.
			controllerutil.RemoveFinalizer(mcpRegistry, "mcpregistry.toolhive.stacklok.dev/finalizer")
			err := r.Update(ctx, mcpRegistry)
			if err != nil {
				ctxLogger.Error(err, "Reconciliation completed with error while removing finalizer",
					"MCPRegistry.Name", mcpRegistry.Name)
				return ctrl.Result{}, err
			}
		}
		ctxLogger.Info("Reconciliation of deleted MCPRegistry completed successfully",
			"MCPRegistry.Name", mcpRegistry.Name,
			"phase", mcpRegistry.Status.Phase)
		return ctrl.Result{}, nil
	}

	// Add finalizer for this CR
	if !controllerutil.ContainsFinalizer(mcpRegistry, "mcpregistry.toolhive.stacklok.dev/finalizer") {
		controllerutil.AddFinalizer(mcpRegistry, "mcpregistry.toolhive.stacklok.dev/finalizer")
		err = r.Update(ctx, mcpRegistry)
		if err != nil {
			ctxLogger.Error(err, "Reconciliation completed with error while adding finalizer",
				"MCPRegistry.Name", mcpRegistry.Name)
			return ctrl.Result{}, err
		}
		ctxLogger.Info("Reconciliation completed successfully after adding finalizer",
			"MCPRegistry.Name", mcpRegistry.Name)
		return ctrl.Result{}, nil
	}

	// 3. Create status manager for batched updates with separation of concerns
	statusManager := mcpregistrystatus.NewStatusManager(mcpRegistry)

	// Initialize result
	result := ctrl.Result{}
	err = nil

	// 4. Reconcile API service
	if apiErr := r.registryAPIManager.ReconcileAPIService(ctx, mcpRegistry); apiErr != nil {
		ctxLogger.Error(apiErr, "Failed to reconcile API service")
		// Set API status with detailed error message from structured error
		statusManager.API().SetAPIStatus(mcpv1alpha1.APIPhaseError, apiErr.Message, "")
		statusManager.API().SetAPIReadyCondition(apiErr.ConditionReason, apiErr.Message, metav1.ConditionFalse)
		err = apiErr
	} else {
		// API reconciliation successful - check readiness and set appropriate status
		isReady := r.registryAPIManager.IsAPIReady(ctx, mcpRegistry)
		if isReady {
			// In-cluster endpoint (simplified form works for internal access)
			endpoint := fmt.Sprintf("http://%s.%s:8080",
				mcpRegistry.GetAPIResourceName(), mcpRegistry.Namespace)
			statusManager.API().SetAPIStatus(mcpv1alpha1.APIPhaseReady,
				"Registry API is ready and serving requests", endpoint)
			statusManager.API().SetAPIReadyCondition("APIReady",
				"Registry API is ready and serving requests", metav1.ConditionTrue)
		} else {
			statusManager.API().SetAPIStatus(mcpv1alpha1.APIPhaseDeploying,
				"Registry API deployment is not ready yet", "")
			statusManager.API().SetAPIReadyCondition("APINotReady",
				"Registry API deployment is not ready yet", metav1.ConditionFalse)
		}
	}

	// 5. Check if we need to requeue for API readiness
	if err == nil && !r.registryAPIManager.IsAPIReady(ctx, mcpRegistry) {
		ctxLogger.Info("API not ready yet, scheduling requeue to check readiness")
		if result.RequeueAfter == 0 || result.RequeueAfter > time.Second*30 {
			result.RequeueAfter = time.Second * 30
		}
	}

	// 6. Derive overall phase and message from API status
	statusDeriver := mcpregistrystatus.NewDefaultStatusDeriver()
	r.deriveOverallStatus(ctx, mcpRegistry, statusManager, statusDeriver)

	// 7. Apply all status changes in a single batch update
	if statusUpdateErr := r.applyStatusUpdates(ctx, r.Client, mcpRegistry, statusManager); statusUpdateErr != nil {
		ctxLogger.Error(statusUpdateErr, "Failed to apply batched status update")
		// Return the status update error only if there was no main reconciliation error
		if err == nil {
			err = statusUpdateErr
		}
	}
	// Log reconciliation completion
	if err != nil {
		ctxLogger.Error(err, "Reconciliation completed with error",
			"MCPRegistry.Name", mcpRegistry.Name, "requeueAfter", result.RequeueAfter)
	} else {
		var apiPhase string
		if mcpRegistry.Status.APIStatus != nil {
			apiPhase = string(mcpRegistry.Status.APIStatus.Phase)
		}

		ctxLogger.Info("Reconciliation completed successfully",
			"MCPRegistry.Name", mcpRegistry.Name,
			"phase", mcpRegistry.Status.Phase,
			"apiPhase", apiPhase,
			"requeueAfter", result.RequeueAfter)
	}

	return result, err
}

// finalizeMCPRegistry performs the finalizer logic for the MCPRegistry
func (r *MCPRegistryReconciler) finalizeMCPRegistry(ctx context.Context, registry *mcpv1alpha1.MCPRegistry) error {
	ctxLogger := log.FromContext(ctx)

	// Update the MCPRegistry status to indicate termination - immediate update needed since object is being deleted
	registry.Status.Phase = mcpv1alpha1.MCPRegistryPhaseTerminating
	registry.Status.Message = "MCPRegistry is being terminated"
	if err := r.Status().Update(ctx, registry); err != nil {
		ctxLogger.Error(err, "Failed to update MCPRegistry status during finalization")
		return err
	}

	ctxLogger.Info("MCPRegistry finalization completed", "registry", registry.Name)
	return nil
}

// deriveOverallStatus determines the overall MCPRegistry phase and message based on API status
func (*MCPRegistryReconciler) deriveOverallStatus(
	ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry,
	statusManager mcpregistrystatus.StatusManager, statusDeriver mcpregistrystatus.StatusDeriver) {
	ctxLogger := log.FromContext(ctx)

	apiStatus := statusManager.API().Status()
	if apiStatus == nil {
		apiStatus = mcpRegistry.Status.APIStatus
	}
	// Use the StatusDeriver to determine the overall phase and message
	// based on current API status
	derivedPhase, derivedMessage := statusDeriver.DeriveOverallStatus(apiStatus)

	// Only update phase and message if they've changed
	statusManager.SetOverallStatus(derivedPhase, derivedMessage)
	ctxLogger.Info("Updated overall status", "apiStatus", apiStatus,
		"oldPhase", mcpRegistry.Status.Phase, "newPhase", derivedPhase,
		"oldMessage", mcpRegistry.Status.Message, "newMessage", derivedMessage)
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPRegistry{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Complete(r)
}

// Apply applies all collected status changes in a single batch update.
// Only actual changes are applied to the status to avoid unnecessary reconciliations
func (*MCPRegistryReconciler) applyStatusUpdates(
	ctx context.Context, k8sClient client.Client,
	mcpRegistry *mcpv1alpha1.MCPRegistry, statusManager mcpregistrystatus.StatusManager) error {

	ctxLogger := log.FromContext(ctx)

	// Refetch the latest version of the resource to avoid conflicts
	latestRegistry := &mcpv1alpha1.MCPRegistry{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(mcpRegistry), latestRegistry); err != nil {
		ctxLogger.Error(err, "Failed to fetch latest MCPRegistry version for status update")
		return fmt.Errorf("failed to fetch latest MCPRegistry version: %w", err)
	}
	latestRegistryStatus := latestRegistry.Status
	hasUpdates := false

	// Apply status changes from status manager
	hasUpdates = statusManager.UpdateStatus(ctx, &latestRegistryStatus) || hasUpdates

	// Single status update using the latest version
	if hasUpdates {
		latestRegistry.Status = latestRegistryStatus
		if err := k8sClient.Status().Update(ctx, latestRegistry); err != nil {
			ctxLogger.Error(err, "Failed to apply batched status update")
			return fmt.Errorf("failed to apply batched status update: %w", err)
		}
		var apiPhase string
		if latestRegistryStatus.APIStatus != nil {
			apiPhase = string(latestRegistryStatus.APIStatus.Phase)
		}
		ctxLogger.V(1).Info("Applied batched status updates",
			"phase", latestRegistryStatus.Phase,
			"apiPhase", apiPhase,
			"message", latestRegistryStatus.Message,
			"conditionsCount", len(latestRegistryStatus.Conditions))
	} else {
		ctxLogger.V(1).Info("No batched status updates applied")
	}

	return nil
}

// validateAndUpdatePodTemplateStatus validates the PodTemplateSpec and updates the MCPRegistry status
// with appropriate conditions. Returns true if validation passes, false otherwise.
func (r *MCPRegistryReconciler) validateAndUpdatePodTemplateStatus(
	ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry,
) bool {
	ctxLogger := log.FromContext(ctx)

	// Validate the PodTemplateSpec by attempting to parse it
	err := registryapi.ValidatePodTemplateSpec(mcpRegistry.GetPodTemplateSpecRaw())
	if err != nil {
		// Set phase and message
		mcpRegistry.Status.Phase = mcpv1alpha1.MCPRegistryPhaseFailed
		mcpRegistry.Status.Message = fmt.Sprintf("Invalid PodTemplateSpec: %v", err)

		// Set condition for invalid PodTemplateSpec
		meta.SetStatusCondition(&mcpRegistry.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionRegistryPodTemplateValid,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: mcpRegistry.Generation,
			Reason:             mcpv1alpha1.ConditionReasonRegistryPodTemplateInvalid,
			Message:            fmt.Sprintf("Failed to parse PodTemplateSpec: %v. Deployment blocked until fixed.", err),
		})

		// Update status with the condition
		if statusErr := r.Status().Update(ctx, mcpRegistry); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRegistry status with PodTemplateSpec validation")
			return false
		}

		ctxLogger.Error(err, "PodTemplateSpec validation failed")
		return false
	}

	// Set condition for valid PodTemplateSpec
	meta.SetStatusCondition(&mcpRegistry.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionRegistryPodTemplateValid,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: mcpRegistry.Generation,
		Reason:             mcpv1alpha1.ConditionReasonRegistryPodTemplateValid,
		Message:            "PodTemplateSpec is valid",
	})

	// Update status with the condition
	if statusErr := r.Status().Update(ctx, mcpRegistry); statusErr != nil {
		ctxLogger.Error(statusErr, "Failed to update MCPRegistry status with PodTemplateSpec validation")
	}

	return true
}

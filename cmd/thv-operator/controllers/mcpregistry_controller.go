package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/mcpregistrystatus"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sync"
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

	// Sync manager handles all sync operations
	syncManager          sync.Manager
	storageManager       sources.StorageManager
	sourceHandlerFactory sources.SourceHandlerFactory
	// Registry API manager handles API deployment operations
	registryAPIManager registryapi.Manager
}

// getCurrentAttemptCount returns the current attempt count from sync status
func getCurrentAttemptCount(mcpRegistry *mcpv1alpha1.MCPRegistry) int {
	if mcpRegistry.Status.SyncStatus != nil {
		return mcpRegistry.Status.SyncStatus.AttemptCount
	}
	return 0
}

// NewMCPRegistryReconciler creates a new MCPRegistryReconciler with required dependencies
func NewMCPRegistryReconciler(k8sClient client.Client, scheme *runtime.Scheme) *MCPRegistryReconciler {
	sourceHandlerFactory := sources.NewSourceHandlerFactory(k8sClient)
	storageManager := sources.NewConfigMapStorageManager(k8sClient, scheme)
	syncManager := sync.NewDefaultSyncManager(k8sClient, scheme, sourceHandlerFactory, storageManager)
	registryAPIManager := registryapi.NewManager(k8sClient, scheme, storageManager, sourceHandlerFactory)

	return &MCPRegistryReconciler{
		Client:               k8sClient,
		Scheme:               scheme,
		syncManager:          syncManager,
		storageManager:       storageManager,
		sourceHandlerFactory: sourceHandlerFactory,
		registryAPIManager:   registryAPIManager,
	}
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//
// For creating registry-api deployment and service
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

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
	var syncPhase, apiPhase any
	if mcpRegistry.Status.SyncStatus != nil {
		syncPhase = mcpRegistry.Status.SyncStatus.Phase
	}
	if mcpRegistry.Status.APIStatus != nil {
		apiPhase = mcpRegistry.Status.APIStatus.Phase
	}

	apiEndpoint := ""
	if mcpRegistry.Status.APIStatus != nil {
		apiEndpoint = mcpRegistry.Status.APIStatus.Endpoint
	}
	ctxLogger.Info("Reconciling MCPRegistry", "MCPRegistry.Name", mcpRegistry.Name, "phase", mcpRegistry.Status.Phase,
		"syncPhase", syncPhase, "apiPhase", apiPhase, "apiEndpoint", apiEndpoint)

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

	// 4. Reconcile sync operation
	result, syncErr := r.reconcileSync(ctx, mcpRegistry, statusManager)

	// 5. Reconcile API service (deployment and service, independent of sync status)
	if syncErr == nil {
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
	}

	// 6. Check if we need to requeue for API readiness
	if syncErr == nil && !r.registryAPIManager.IsAPIReady(ctx, mcpRegistry) {
		ctxLogger.Info("API not ready yet, scheduling requeue to check readiness")
		if result.RequeueAfter == 0 || result.RequeueAfter > time.Second*30 {
			result.RequeueAfter = time.Second * 30
		}
	}

	// 7. Derive overall phase and message from sync and API status
	statusDeriver := mcpregistrystatus.NewDefaultStatusDeriver()
	r.deriveOverallStatus(ctx, mcpRegistry, statusManager, statusDeriver)

	// 8. Apply all status changes in a single batch update
	if statusUpdateErr := r.applyStatusUpdates(ctx, r.Client, mcpRegistry, statusManager); statusUpdateErr != nil {
		ctxLogger.Error(statusUpdateErr, "Failed to apply batched status update")
		// Return the status update error only if there was no main reconciliation error
		if syncErr == nil {
			err = statusUpdateErr
		}
	}

	if err == nil {
		err = syncErr
	}

	// Log reconciliation completion
	if err != nil {
		ctxLogger.Error(err, "Reconciliation completed with error",
			"MCPRegistry.Name", mcpRegistry.Name, "requeueAfter", result.RequeueAfter)
	} else {
		var syncPhase, apiPhase string
		if mcpRegistry.Status.SyncStatus != nil {
			syncPhase = string(mcpRegistry.Status.SyncStatus.Phase)
		}
		if mcpRegistry.Status.APIStatus != nil {
			apiPhase = string(mcpRegistry.Status.APIStatus.Phase)
		}

		ctxLogger.Info("Reconciliation completed successfully",
			"MCPRegistry.Name", mcpRegistry.Name,
			"phase", mcpRegistry.Status.Phase,
			"syncPhase", syncPhase,
			"apiPhase", apiPhase,
			"requeueAfter", result.RequeueAfter)
	}

	return result, err
}

// preserveExistingSyncData extracts sync data from existing status for preservation
// Returns lastSyncTime, lastSyncHash, and serverCount from the current sync status
func (*MCPRegistryReconciler) preserveExistingSyncData(mcpRegistry *mcpv1alpha1.MCPRegistry) (*metav1.Time, string, int) {
	if mcpRegistry.Status.SyncStatus != nil {
		return mcpRegistry.Status.SyncStatus.LastSyncTime,
			mcpRegistry.Status.SyncStatus.LastSyncHash,
			mcpRegistry.Status.SyncStatus.ServerCount
	}
	// Fallback to zero values for new installation
	return nil, "", 0
}

// reconcileSync checks if sync is needed and performs it if necessary
// This method only handles data synchronization to the target ConfigMap
//
//nolint:gocyclo // Complex reconciliation logic requires multiple conditions
func (r *MCPRegistryReconciler) reconcileSync(
	ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry, statusManager mcpregistrystatus.StatusManager,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Check if sync is needed - no need to refresh object here since we just fetched it
	syncNeeded, syncReason, nextSyncTime := r.syncManager.ShouldSync(ctx, mcpRegistry)

	if !syncNeeded {
		ctxLogger.Info("Sync not needed", "reason", syncReason)
		// Do not update sync status if sync is not needed: this would cause unnecessary status updates

		// Schedule next reconciliation if we have a sync policy
		if nextSyncTime != nil {
			requeueAfter := time.Until(*nextSyncTime)
			ctxLogger.Info("Scheduling next automatic sync", "requeueAfter", requeueAfter)
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
		return ctrl.Result{}, nil
	}

	ctxLogger.Info("Sync needed", "reason", syncReason)

	// Handle manual sync with no data changes - update trigger tracking only
	if syncReason == sync.ReasonManualNoChanges {
		// Preserve existing sync data for manual sync with no changes
		lastSyncTime, lastSyncHash, serverCount := r.preserveExistingSyncData(mcpRegistry)
		statusManager.Sync().SetSyncStatus(
			mcpv1alpha1.SyncPhaseComplete, "Manual sync completed (no data changes)", 0,
			lastSyncTime, lastSyncHash, serverCount)
		return r.syncManager.UpdateManualSyncTriggerOnly(ctx, mcpRegistry)
	}

	// Set sync status to syncing before starting the operation
	// Clear sync data when starting sync operation
	statusManager.Sync().SetSyncStatus(
		mcpv1alpha1.SyncPhaseSyncing, "Synchronizing registry data",
		getCurrentAttemptCount(mcpRegistry)+1, nil, "", 0)

	// Perform the sync - the sync manager will handle core registry field updates
	result, syncResult, syncErr := r.syncManager.PerformSync(ctx, mcpRegistry)

	if syncErr != nil {
		// Sync failed - set sync status to failed
		ctxLogger.Error(syncErr, "Sync failed, scheduling retry")
		// Preserve existing sync data when sync fails
		lastSyncTime, lastSyncHash, serverCount := r.preserveExistingSyncData(mcpRegistry)

		// Set sync status with detailed error message from SyncError
		statusManager.Sync().SetSyncStatus(mcpv1alpha1.SyncPhaseFailed,
			syncErr.Message, getCurrentAttemptCount(mcpRegistry)+1, lastSyncTime, lastSyncHash, serverCount)
		// Set the appropriate condition based on the error type
		statusManager.Sync().SetSyncCondition(metav1.Condition{
			Type:               syncErr.ConditionType,
			Status:             metav1.ConditionFalse,
			Reason:             syncErr.ConditionReason,
			Message:            syncErr.Message,
			LastTransitionTime: metav1.Now(),
		})

		// Use a shorter retry interval instead of the full sync interval
		retryAfter := DefaultControllerRetryAfter // Default retry interval
		if result.RequeueAfter > 0 {
			// If PerformSync already set a retry interval, use it
			retryAfter = result.RequeueAfter
		}
		return ctrl.Result{RequeueAfter: retryAfter}, syncErr
	}

	// Sync successful - set sync status to complete using data from sync result
	now := metav1.Now()
	statusManager.Sync().SetSyncStatus(mcpv1alpha1.SyncPhaseComplete, "Registry data synchronized successfully", 0,
		&now, syncResult.Hash, syncResult.ServerCount)

	ctxLogger.Info("Registry data sync completed successfully")

	// Schedule next automatic sync only if this was an automatic sync (not manual)
	if mcpRegistry.Spec.SyncPolicy != nil && !sync.IsManualSync(syncReason) {
		interval, parseErr := time.ParseDuration(mcpRegistry.Spec.SyncPolicy.Interval)
		if parseErr == nil {
			result.RequeueAfter = interval
			ctxLogger.Info("Automatic sync successful, scheduled next automatic sync", "interval", interval)
		} else {
			ctxLogger.Error(parseErr, "Invalid sync interval in policy", "interval", mcpRegistry.Spec.SyncPolicy.Interval)
		}
	} else if sync.IsManualSync(syncReason) {
		ctxLogger.Info("Manual sync successful, automatic sync schedule unchanged")
	} else {
		ctxLogger.Info("Sync successful, no automatic sync policy configured")
	}

	return result, nil
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

	// Clean up internal storage ConfigMaps
	if err := r.syncManager.Delete(ctx, registry); err != nil {
		ctxLogger.Error(err, "Failed to delete storage during finalization")
		// Continue with finalization even if storage cleanup fails
	}

	// TODO: Add additional cleanup logic when other features are implemented:
	// - Clean up Registry API deployment and service (will be handled by owner references)
	// - Cancel any running sync operations

	ctxLogger.Info("MCPRegistry finalization completed", "registry", registry.Name)
	return nil
}

// deriveOverallStatus determines the overall MCPRegistry phase and message based on sync and API status
func (*MCPRegistryReconciler) deriveOverallStatus(
	ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry,
	statusManager mcpregistrystatus.StatusManager, statusDeriver mcpregistrystatus.StatusDeriver) {
	ctxLogger := log.FromContext(ctx)

	syncStatus := statusManager.Sync().Status()
	if syncStatus == nil {
		syncStatus = mcpRegistry.Status.SyncStatus
	}
	apiStatus := statusManager.API().Status()
	if apiStatus == nil {
		apiStatus = mcpRegistry.Status.APIStatus
	}
	// Use the StatusDeriver to determine the overall phase and message
	// based on current sync and API statuses
	derivedPhase, derivedMessage := statusDeriver.DeriveOverallStatus(syncStatus, apiStatus)

	// Only update phase and message if they've changed
	statusManager.SetOverallStatus(derivedPhase, derivedMessage)
	ctxLogger.Info("Updated overall status", "syncStatus", syncStatus, "apiStatus", apiStatus,
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
		Complete(r)
}

// Apply applies all collected status changes in a single batch update.
// Only actual changes are applied to the status to avoid unnecessary reconciliations
func (r *MCPRegistryReconciler) applyStatusUpdates(
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

	// Apply manual sync trigger change if necessary
	if mcpRegistry.Annotations != nil {
		if triggerValue := mcpRegistry.Annotations[mcpregistrystatus.SyncTriggerAnnotation]; triggerValue != "" {
			if latestRegistryStatus.LastManualSyncTrigger != triggerValue {
				latestRegistryStatus.LastManualSyncTrigger = triggerValue
				hasUpdates = true
				ctxLogger.Info("Updated LastManualSyncTrigger", "trigger", triggerValue)
			}
		}
	}

	// Apply filter change if necessary
	currentFilterJSON, err := json.Marshal(mcpRegistry.Spec.Filter)
	if err != nil {
		ctxLogger.Error(err, "Failed to marshal current filter")
		return fmt.Errorf("failed to marshal current filter: %w", err)
	}
	currentFilterHash := sha256.Sum256(currentFilterJSON)
	currentFilterHashStr := hex.EncodeToString(currentFilterHash[:])
	if latestRegistryStatus.LastAppliedFilterHash != currentFilterHashStr {
		latestRegistryStatus.LastAppliedFilterHash = currentFilterHashStr
		hasUpdates = true
		ctxLogger.Info("Updated LastAppliedFilterHash", "hash", currentFilterHashStr)
	}

	// Update storage reference if necessary
	storageRef := r.storageManager.GetStorageReference(latestRegistry)
	if storageRef != nil {
		if latestRegistryStatus.StorageRef == nil || latestRegistryStatus.StorageRef.ConfigMapRef.Name != storageRef.ConfigMapRef.Name {
			latestRegistryStatus.StorageRef = storageRef
			hasUpdates = true
			ctxLogger.Info("Updated StorageRef", "storageRef", storageRef)
		}
	}

	// Apply status changes from status manager
	hasUpdates = statusManager.UpdateStatus(ctx, &latestRegistryStatus) || hasUpdates

	// Single status update using the latest version
	if hasUpdates {
		latestRegistry.Status = latestRegistryStatus
		if err := k8sClient.Status().Update(ctx, latestRegistry); err != nil {
			ctxLogger.Error(err, "Failed to apply batched status update")
			return fmt.Errorf("failed to apply batched status update: %w", err)
		}
		var syncPhase mcpv1alpha1.SyncPhase
		if latestRegistryStatus.SyncStatus != nil {
			syncPhase = latestRegistryStatus.SyncStatus.Phase
		}
		var apiPhase string
		if latestRegistryStatus.APIStatus != nil {
			apiPhase = string(latestRegistryStatus.APIStatus.Phase)
		}
		ctxLogger.V(1).Info("Applied batched status updates",
			"phase", latestRegistryStatus.Phase,
			"syncPhase", syncPhase,
			"apiPhase", apiPhase,
			"message", latestRegistryStatus.Message,
			"conditionsCount", len(latestRegistryStatus.Conditions))
	} else {
		ctxLogger.V(1).Info("No batched status updates applied")
	}

	return nil
}

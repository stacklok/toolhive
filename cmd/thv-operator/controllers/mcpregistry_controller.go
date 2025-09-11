package controllers

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sync"
)

// Sync reason constants - package visible
const (
	// Registry state related reasons
	syncReasonAlreadyInProgress = "sync-already-in-progress"
	syncReasonRegistryNotReady  = "registry-not-ready"

	// Data change related reasons
	syncReasonSourceDataChanged    = "source-data-changed"
	syncReasonErrorCheckingChanges = "error-checking-data-changes"

	// Manual sync related reasons
	syncReasonManualWithChanges = "manual-sync-with-data-changes"
	syncReasonManualNoChanges   = "manual-sync-no-data-changes"

	// Automatic sync related reasons
	syncReasonErrorParsingInterval  = "error-parsing-sync-interval"
	syncReasonErrorCheckingSyncNeed = "error-checking-sync-need"

	// Up-to-date reasons
	syncReasonUpToDateWithPolicy = "up-to-date-with-policy"
	syncReasonUpToDateNoPolicy   = "up-to-date-no-policy"
)

// Manual sync annotation detection reasons - package visible
const (
	manualSyncReasonNoAnnotations    = "no-annotations"
	manualSyncReasonNoTrigger        = "no-manual-trigger"
	manualSyncReasonAlreadyProcessed = "manual-trigger-already-processed"
	manualSyncReasonRequested        = "manual-sync-requested"
)

// MCPRegistryReconciler reconciles a MCPRegistry object
type MCPRegistryReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Sync manager handles all sync operations
	syncManager sync.Manager
}

// NewMCPRegistryReconciler creates a new MCPRegistryReconciler with required dependencies
func NewMCPRegistryReconciler(k8sClient client.Client, scheme *runtime.Scheme) *MCPRegistryReconciler {
	sourceHandlerFactory := sources.NewSourceHandlerFactory(k8sClient)
	storageManager := sources.NewConfigMapStorageManager(k8sClient, scheme)
	syncManager := sync.NewDefaultSyncManager(k8sClient, scheme, sourceHandlerFactory, storageManager)

	return &MCPRegistryReconciler{
		Client:      k8sClient,
		Scheme:      scheme,
		syncManager: syncManager,
	}
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPRegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// 1. Fetch MCPRegistry instance
	mcpRegistry := &mcpv1alpha1.MCPRegistry{}
	err := r.Get(ctx, req.NamespacedName, mcpRegistry)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			ctxLogger.Info("MCPRegistry resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		ctxLogger.Error(err, "Failed to get MCPRegistry")
		return ctrl.Result{}, err
	}
	ctxLogger.Info("Reconciling MCPRegistry", "MCPRegistry.Name", mcpRegistry.Name)

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

	// 3. Check if sync is needed before performing it
	result, err := r.reconcileSync(ctx, mcpRegistry)

	// Log reconciliation completion
	if err != nil {
		ctxLogger.Error(err, "Reconciliation completed with error",
			"MCPRegistry.Name", mcpRegistry.Name,
			"requeueAfter", result.RequeueAfter)
	} else {
		ctxLogger.Info("Reconciliation completed successfully",
			"MCPRegistry.Name", mcpRegistry.Name,
			"phase", mcpRegistry.Status.Phase,
			"requeueAfter", result.RequeueAfter)
	}

	return result, err
}

// reconcileSync checks if sync is needed and performs it if necessary
func (r *MCPRegistryReconciler) reconcileSync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Refresh the object to get latest status for accurate timing calculations
	if err := r.Get(ctx, client.ObjectKeyFromObject(mcpRegistry), mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to refresh MCPRegistry object for sync check")
		return ctrl.Result{}, err
	}

	// Check if sync is needed
	syncNeeded, syncReason, nextSyncTime, err := r.syncManager.ShouldSync(ctx, mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Failed to determine if sync is needed")
		// Proceed with sync on error to be safe
		syncNeeded = true
		syncReason = sync.ReasonErrorCheckingSyncNeed
	}

	if !syncNeeded {
		ctxLogger.Info("Sync not needed", "reason", syncReason)
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
		return r.syncManager.UpdateManualSyncTriggerOnly(ctx, mcpRegistry)
	}

	result, err := r.syncManager.PerformSync(ctx, mcpRegistry)

	if err != nil {
		// Sync failed - schedule retry with exponential backoff
		ctxLogger.Error(err, "Sync failed, scheduling retry")
		// Use a shorter retry interval instead of the full sync interval
		retryAfter := time.Minute * 5 // Default retry interval
		if result.RequeueAfter > 0 {
			// If PerformSync already set a retry interval, use it
			retryAfter = result.RequeueAfter
		}
		return ctrl.Result{RequeueAfter: retryAfter}, err
	}

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

	return result, err
}

// shouldSync determines if a sync operation is needed and when the next sync should occur
// Returns: syncNeeded (bool), reason (string), nextSyncTime (*time.Time), error
func (r *MCPRegistryReconciler) shouldSync(ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, string, *time.Time, error) {
	// If registry is currently syncing, don't start another sync
	if mcpRegistry.Status.Phase == mcpv1alpha1.MCPRegistryPhaseSyncing {
		return false, syncReasonAlreadyInProgress, nil, nil
	}

	// Check for manual sync trigger first (always update trigger tracking)
	manualSyncRequested, _ := r.isManualSyncRequested(mcpRegistry)

	// If registry is in Failed or Pending state, sync is needed
	if mcpRegistry.Status.Phase != mcpv1alpha1.MCPRegistryPhaseReady {
		return true, syncReasonRegistryNotReady, nil, nil
	}

	// Check if source data has changed by comparing hash
	dataChanged, err := r.isDataChanged(ctx, mcpRegistry)
	if err != nil {
		return true, syncReasonErrorCheckingChanges, nil, err
	}

	// Manual sync was requested - but only sync if data has actually changed
	if manualSyncRequested {
		if dataChanged {
			return true, syncReasonManualWithChanges, nil, nil
		}
		// Manual sync requested but no data changes - update trigger tracking only
		return true, syncReasonManualNoChanges, nil, nil
	}

	if dataChanged {
		return true, syncReasonSourceDataChanged, nil, nil
	}

	// Data hasn't changed - check if we need to schedule future checks
	if mcpRegistry.Spec.SyncPolicy != nil {
		_, nextSyncTime, err := r.isIntervalSyncNeeded(mcpRegistry)
		if err != nil {
			return true, syncReasonErrorParsingInterval, nil, err
		}

		// No sync needed since data hasn't changed, but schedule next check
		return false, syncReasonUpToDateWithPolicy, &nextSyncTime, nil
	}

	// No automatic sync policy, registry is up-to-date
	return false, syncReasonUpToDateNoPolicy, nil, nil
}

// isDataChanged checks if source data has changed by comparing hashes
func (r *MCPRegistryReconciler) isDataChanged(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, error) {
	// If we don't have a last sync hash, consider data changed
	if mcpRegistry.Status.LastSyncHash == "" {
		return true, nil
	}

	// Get source handler
	sourceHandler, err := r.sourceHandlerFactory.CreateHandler(mcpRegistry.Spec.Source.Type)
	if err != nil {
		return true, err
	}

	// Get current hash from source
	currentHash, err := sourceHandler.CurrentHash(ctx, mcpRegistry)
	if err != nil {
		return true, err
	}

	// Compare hashes - data changed if different
	return currentHash != mcpRegistry.Status.LastSyncHash, nil
}

// updateManualSyncTriggerOnly updates the manual sync trigger tracking without performing actual sync
func (r *MCPRegistryReconciler) updateManualSyncTriggerOnly(ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Refresh the object to get latest resourceVersion
	if err := r.Get(ctx, client.ObjectKeyFromObject(mcpRegistry), mcpRegistry); err != nil {
		return ctrl.Result{}, err
	}

	// Update manual sync trigger tracking
	if mcpRegistry.Annotations != nil {
		if triggerValue := mcpRegistry.Annotations["toolhive.stacklok.dev/sync-trigger"]; triggerValue != "" {
			mcpRegistry.Status.LastManualSyncTrigger = triggerValue
			ctxLogger.Info("Manual sync trigger processed (no data changes)", "trigger", triggerValue)
		}
	}

	// Update status
	if err := r.Status().Update(ctx, mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to update manual sync trigger tracking")
		return ctrl.Result{}, err
	}

	ctxLogger.Info("Manual sync completed (no data changes required)")
	return ctrl.Result{}, nil
}

// isManualSync checks if the sync reason indicates a manual sync
func isManualSync(reason string) bool {
	return reason == syncReasonManualWithChanges || reason == syncReasonManualNoChanges
}

// isManualSyncRequested checks if a manual sync was requested via annotation
func (*MCPRegistryReconciler) isManualSyncRequested(mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, string) {
	// Check if sync-trigger annotation exists
	if mcpRegistry.Annotations == nil {
		return false, manualSyncReasonNoAnnotations
	}

	triggerValue := mcpRegistry.Annotations["toolhive.stacklok.dev/sync-trigger"]
	if triggerValue == "" {
		return false, manualSyncReasonNoTrigger
	}

	// Check if this trigger was already processed
	lastProcessed := mcpRegistry.Status.LastManualSyncTrigger
	if triggerValue == lastProcessed {
		return false, manualSyncReasonAlreadyProcessed
	}

	return true, manualSyncReasonRequested
}

// isIntervalSyncNeeded checks if sync is needed based on time interval
func (*MCPRegistryReconciler) isIntervalSyncNeeded(mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, time.Time, error) {
	// Parse the sync interval
	interval, err := time.ParseDuration(mcpRegistry.Spec.SyncPolicy.Interval)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("invalid sync interval '%s': %w", mcpRegistry.Spec.SyncPolicy.Interval, err)
	}

	// If we don't have a last sync time, sync is needed
	if mcpRegistry.Status.LastSyncTime == nil {
		return true, time.Now().Add(interval), nil
	}

	// Calculate when next sync should happen
	nextSyncTime := mcpRegistry.Status.LastSyncTime.Add(interval)

	// Check if it's time for the next sync
	now := time.Now()
	return now.After(nextSyncTime) || now.Equal(nextSyncTime), nextSyncTime, nil
}

// performSync performs the complete sync operation for the MCPRegistry
func (r *MCPRegistryReconciler) performSync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Update phase to syncing
	if err := r.updatePhase(ctx, mcpRegistry, mcpv1alpha1.MCPRegistryPhaseSyncing, "Synchronizing registry data"); err != nil {
		return ctrl.Result{}, err
	}

	// Check if sync is needed
	syncNeeded, syncReason, nextSyncTime, err := r.syncManager.ShouldSync(ctx, mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Failed to determine if sync is needed")
		// Proceed with sync on error to be safe
		syncNeeded = true
		syncReason = sync.ReasonErrorCheckingSyncNeed
	}

	if !syncNeeded {
		ctxLogger.Info("Sync not needed", "reason", syncReason)
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
		return r.syncManager.UpdateManualSyncTriggerOnly(ctx, mcpRegistry)
	}

	result, err := r.syncManager.PerformSync(ctx, mcpRegistry)

	if err != nil {
		// Sync failed - schedule retry with exponential backoff
		ctxLogger.Error(err, "Sync failed, scheduling retry")
		// Use a shorter retry interval instead of the full sync interval
		retryAfter := time.Minute * 5 // Default retry interval
		if result.RequeueAfter > 0 {
			// If PerformSync already set a retry interval, use it
			retryAfter = result.RequeueAfter
		}
		return ctrl.Result{RequeueAfter: time.Minute * 5}, nil
	}

	ctxLogger.Info("Registry data stored successfully",
		"namespace", mcpRegistry.Namespace,
		"registryName", mcpRegistry.Name)

	// Refresh the object to get latest resourceVersion before final update
	if err := r.Get(ctx, client.ObjectKeyFromObject(mcpRegistry), mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to refresh MCPRegistry object")
		return ctrl.Result{}, err
	}

	// Get storage reference
	storageRef := r.storageManager.GetStorageReference(mcpRegistry)

	// Update status with successful sync - batch all updates
	now := metav1.Now()
	mcpRegistry.Status.Phase = mcpv1alpha1.MCPRegistryPhaseReady
	mcpRegistry.Status.Message = "Registry is ready and synchronized"
	mcpRegistry.Status.LastSyncTime = &now
	mcpRegistry.Status.LastSyncHash = fetchResult.Hash
	mcpRegistry.Status.ServerCount = fetchResult.ServerCount
	mcpRegistry.Status.SyncAttempts = 0 // Reset on success
	if storageRef != nil {
		mcpRegistry.Status.StorageRef = storageRef
	}

	// Update manual sync trigger tracking if annotation exists
	if mcpRegistry.Annotations != nil {
		if triggerValue := mcpRegistry.Annotations["toolhive.stacklok.dev/sync-trigger"]; triggerValue != "" {
			mcpRegistry.Status.LastManualSyncTrigger = triggerValue
			ctxLogger.Info("Manual sync trigger processed", "trigger", triggerValue)
		}
	}

	// Set all success conditions in memory
	meta.SetStatusCondition(&mcpRegistry.Status.Conditions, metav1.Condition{
		Type:    mcpv1alpha1.ConditionSourceAvailable,
		Status:  metav1.ConditionTrue,
		Reason:  "SourceReady",
		Message: "Source configuration is valid and accessible",
	})
	meta.SetStatusCondition(&mcpRegistry.Status.Conditions, metav1.Condition{
		Type:    mcpv1alpha1.ConditionDataValid,
		Status:  metav1.ConditionTrue,
		Reason:  "DataValid",
		Message: "Registry data is valid and parsed successfully",
	})
	meta.SetStatusCondition(&mcpRegistry.Status.Conditions, metav1.Condition{
		Type:    mcpv1alpha1.ConditionSyncSuccessful,
		Status:  metav1.ConditionTrue,
		Reason:  "SyncCompleted",
		Message: "Registry sync completed successfully",
	})

	// Single final status update
	if err := r.Status().Update(ctx, mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to update final status")
		return ctrl.Result{}, err
	}

	ctxLogger.Info("MCPRegistry sync completed successfully",
		"serverCount", fetchResult.ServerCount,
		"hash", fetchResult.Hash)

	return ctrl.Result{}, nil
}

// updatePhase updates the MCPRegistry phase and message
func (r *MCPRegistryReconciler) updatePhase(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry,
	phase mcpv1alpha1.MCPRegistryPhase, message string) error {
	mcpRegistry.Status.Phase = phase
	mcpRegistry.Status.Message = message
	return r.Status().Update(ctx, mcpRegistry)
}

// updatePhaseFailedWithCondition updates phase, message and sets a condition
func (r *MCPRegistryReconciler) updatePhaseFailedWithCondition(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry,
	message string, conditionType string, reason, conditionMessage string) error {

	// Refresh object to get latest resourceVersion
	if err := r.Get(ctx, client.ObjectKeyFromObject(mcpRegistry), mcpRegistry); err != nil {
		return err
	}

	mcpRegistry.Status.Phase = mcpv1alpha1.MCPRegistryPhaseFailed
	mcpRegistry.Status.Message = message

	// Set condition
	meta.SetStatusCondition(&mcpRegistry.Status.Conditions, metav1.Condition{
		Type:    conditionType,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: conditionMessage,
	})

	return r.Status().Update(ctx, mcpRegistry)
}

// finalizeMCPRegistry performs the finalizer logic for the MCPRegistry
func (r *MCPRegistryReconciler) finalizeMCPRegistry(ctx context.Context, registry *mcpv1alpha1.MCPRegistry) error {
	ctxLogger := log.FromContext(ctx)

	// Update the MCPRegistry status to indicate termination
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
	// - Clean up Registry API service
	// - Cancel any running sync operations

	ctxLogger.Info("MCPRegistry finalization completed", "registry", registry.Name)
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPRegistry{}).
		Complete(r)
}

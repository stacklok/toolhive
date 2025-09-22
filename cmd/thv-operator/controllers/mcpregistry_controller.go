package controllers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
	// Safe access to nested status fields
	var syncPhase, apiPhase any
	if mcpRegistry.Status.SyncStatus != nil {
		syncPhase = mcpRegistry.Status.SyncStatus.Phase
	}
	if mcpRegistry.Status.APIStatus != nil {
		apiPhase = mcpRegistry.Status.APIStatus.Phase
	}

	ctxLogger.Info("Reconciling MCPRegistry", "MCPRegistry.Name", mcpRegistry.Name, "phase", mcpRegistry.Status.Phase,
		"syncPhase", syncPhase, "apiPhase", apiPhase, "apiEndpoint", mcpRegistry.Status.APIEndpoint)

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

	// 3. Create status collector for batched updates
	statusCollector := mcpregistrystatus.NewCollector(mcpRegistry)

	// 4. Reconcile sync operation
	result, syncErr := r.reconcileSync(ctx, mcpRegistry, statusCollector)

	// 5. Reconcile API service (deployment and service, independent of sync status)
	if syncErr == nil {
		if apiErr := r.registryAPIManager.ReconcileAPIService(ctx, mcpRegistry, statusCollector); apiErr != nil {
			ctxLogger.Error(apiErr, "Failed to reconcile API service")
			err = apiErr
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
	r.deriveOverallStatus(ctx, mcpRegistry, statusCollector)

	// 8. Apply all status changes in a single batch update
	if statusUpdateErr := statusCollector.Apply(ctx, r.Client); statusUpdateErr != nil {
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
			"MCPRegistry.Name", mcpRegistry.Name)
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

// reconcileSync checks if sync is needed and performs it if necessary
// This method only handles data synchronization to the target ConfigMap
//
//nolint:gocyclo // Complex reconciliation logic requires multiple conditions
func (r *MCPRegistryReconciler) reconcileSync(
	ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry, statusCollector mcpregistrystatus.Collector,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Check if sync is needed - no need to refresh object here since we just fetched it
	syncNeeded, syncReason, nextSyncTime, err := r.syncManager.ShouldSync(ctx, mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Failed to determine if sync is needed")
		// Proceed with sync on error to be safe
		syncNeeded = true
		syncReason = sync.ReasonErrorCheckingSyncNeed
	}

	if !syncNeeded {
		ctxLogger.Info("Sync not needed", "reason", syncReason)

		// Only update sync status if it's not already Idle with the right message
		currentSyncPhase := mcpv1alpha1.SyncPhaseIdle // default
		currentMessage := ""
		if mcpRegistry.Status.SyncStatus != nil {
			currentSyncPhase = mcpRegistry.Status.SyncStatus.Phase
			currentMessage = mcpRegistry.Status.SyncStatus.Message
		}

		// Only set sync status if it needs to change
		if currentSyncPhase != mcpv1alpha1.SyncPhaseIdle || currentMessage != "No sync required" {
			// Preserve existing sync data when no sync is needed
			var lastSyncTime *metav1.Time
			var lastSyncHash string
			var serverCount int
			if mcpRegistry.Status.SyncStatus != nil {
				lastSyncTime = mcpRegistry.Status.SyncStatus.LastSyncTime
				lastSyncHash = mcpRegistry.Status.SyncStatus.LastSyncHash
				serverCount = mcpRegistry.Status.SyncStatus.ServerCount
			} else {
				// Fallback to zero values for new installation
				lastSyncTime = nil
				lastSyncHash = ""
				serverCount = 0
			}
			statusCollector.SetSyncStatus(mcpv1alpha1.SyncPhaseIdle, "No sync required", 0, lastSyncTime, lastSyncHash, serverCount)
		}

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
		var lastSyncTime *metav1.Time
		var lastSyncHash string
		var serverCount int
		if mcpRegistry.Status.SyncStatus != nil {
			lastSyncTime = mcpRegistry.Status.SyncStatus.LastSyncTime
			lastSyncHash = mcpRegistry.Status.SyncStatus.LastSyncHash
			serverCount = mcpRegistry.Status.SyncStatus.ServerCount
		} else {
			// Fallback to zero values for new installation
			lastSyncTime = nil
			lastSyncHash = ""
			serverCount = 0
		}
		statusCollector.SetSyncStatus(
			mcpv1alpha1.SyncPhaseComplete, "Manual sync completed (no data changes)", 0,
			lastSyncTime, lastSyncHash, serverCount)
		return r.syncManager.UpdateManualSyncTriggerOnly(ctx, mcpRegistry)
	}

	// Set sync status to syncing before starting the operation
	// Clear sync data when starting sync operation
	statusCollector.SetSyncStatus(
		mcpv1alpha1.SyncPhaseSyncing, "Synchronizing registry data",
		getCurrentAttemptCount(mcpRegistry)+1, nil, "", 0)

	// Perform the sync - the sync manager will handle core registry field updates
	result, syncResult, err := r.syncManager.PerformSync(ctx, mcpRegistry)

	if err != nil {
		// Sync failed - set sync status to failed
		ctxLogger.Error(err, "Sync failed, scheduling retry")
		// Preserve existing sync data when sync fails
		var lastSyncTime *metav1.Time
		var lastSyncHash string
		var serverCount int
		if mcpRegistry.Status.SyncStatus != nil {
			lastSyncTime = mcpRegistry.Status.SyncStatus.LastSyncTime
			lastSyncHash = mcpRegistry.Status.SyncStatus.LastSyncHash
			serverCount = mcpRegistry.Status.SyncStatus.ServerCount
		} else {
			// Fallback to zero values for new installation
			lastSyncTime = nil
			lastSyncHash = ""
			serverCount = 0
		}
		statusCollector.SetSyncStatus(mcpv1alpha1.SyncPhaseFailed,
			fmt.Sprintf("Sync failed: %v", err), getCurrentAttemptCount(mcpRegistry)+1, lastSyncTime, lastSyncHash, serverCount)
		// Use a shorter retry interval instead of the full sync interval
		retryAfter := time.Minute * 5 // Default retry interval
		if result.RequeueAfter > 0 {
			// If PerformSync already set a retry interval, use it
			retryAfter = result.RequeueAfter
		}
		return ctrl.Result{RequeueAfter: retryAfter}, err
	}

	// Sync successful - set sync status to complete using data from sync result
	now := metav1.Now()
	statusCollector.SetSyncStatus(mcpv1alpha1.SyncPhaseComplete, "Registry data synchronized successfully", 0,
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
func (r *MCPRegistryReconciler) deriveOverallStatus(
	ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry, statusCollector mcpregistrystatus.Collector) {
	ctxLogger := log.FromContext(ctx)

	// Create a temporary copy with current collected status to derive phase
	tempRegistry := mcpRegistry.DeepCopy()

	// Apply the collected status changes to temp registry for phase calculation
	// Note: This is a simulation - we can't actually access the collected values directly
	// Instead, we'll use the DeriveOverallPhase method which works with current status
	// The controller will need to be smart about when sync/API status get updated

	// For now, let's derive phase based on current MCPRegistry status since
	// the status collector changes haven't been applied yet
	derivedPhase := tempRegistry.DeriveOverallPhase()
	derivedMessage := r.deriveMessage(derivedPhase, tempRegistry)

	// Only update phase and message if they've changed
	if mcpRegistry.Status.Phase != derivedPhase {
		statusCollector.SetPhase(derivedPhase)
		ctxLogger.Info("Updated overall phase", "oldPhase", mcpRegistry.Status.Phase, "newPhase", derivedPhase)
	}

	if mcpRegistry.Status.Message != derivedMessage {
		statusCollector.SetMessage(derivedMessage)
		ctxLogger.Info("Updated overall message", "message", derivedMessage)
	}
}

// deriveMessage creates an appropriate message based on the overall phase and registry state
func (*MCPRegistryReconciler) deriveMessage(phase mcpv1alpha1.MCPRegistryPhase, mcpRegistry *mcpv1alpha1.MCPRegistry) string {
	switch phase {
	case mcpv1alpha1.MCPRegistryPhasePending:
		if mcpRegistry.Status.SyncStatus != nil && mcpRegistry.Status.SyncStatus.Phase == mcpv1alpha1.SyncPhaseComplete {
			return "Registry data synced, API deployment in progress"
		}
		return "Registry initialization in progress"
	case mcpv1alpha1.MCPRegistryPhaseReady:
		return "Registry is ready and API is serving requests"
	case mcpv1alpha1.MCPRegistryPhaseFailed:
		// Return more specific error message if available
		if mcpRegistry.Status.SyncStatus != nil && mcpRegistry.Status.SyncStatus.Phase == mcpv1alpha1.SyncPhaseFailed {
			return fmt.Sprintf("Sync failed: %s", mcpRegistry.Status.SyncStatus.Message)
		}
		if mcpRegistry.Status.APIStatus != nil && mcpRegistry.Status.APIStatus.Phase == mcpv1alpha1.APIPhaseError {
			return fmt.Sprintf("API deployment failed: %s", mcpRegistry.Status.APIStatus.Message)
		}
		return "Registry operation failed"
	case mcpv1alpha1.MCPRegistryPhaseSyncing:
		return "Registry data synchronization in progress"
	case mcpv1alpha1.MCPRegistryPhaseTerminating:
		return "Registry is being terminated"
	default:
		return "Registry status unknown"
	}
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

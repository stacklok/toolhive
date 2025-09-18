package controllers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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

	// 3. Create status collector for batched updates
	statusCollector := mcpregistrystatus.NewCollector(mcpRegistry)

	// 4. Reconcile sync operation
	result, err := r.reconcileSync(ctx, mcpRegistry, statusCollector)

	// 5. Reconcile API service (deployment and service, independent of sync status)
	if apiErr := r.registryAPIManager.ReconcileAPIService(ctx, mcpRegistry, statusCollector); apiErr != nil {
		ctxLogger.Error(apiErr, "Failed to reconcile API service")
		if err == nil {
			err = apiErr
		}
	}

	// 6. Check if we need to requeue for API readiness
	if err == nil && !r.registryAPIManager.IsAPIReady(ctx, mcpRegistry) {
		ctxLogger.Info("API not ready yet, scheduling requeue to check readiness")
		if result.RequeueAfter == 0 || result.RequeueAfter > time.Second*30 {
			result.RequeueAfter = time.Second * 30
		}
	}

	// 7. Apply all status changes in a single batch update
	if statusUpdateErr := statusCollector.Apply(ctx, r.Status()); statusUpdateErr != nil {
		ctxLogger.Error(statusUpdateErr, "Failed to apply batched status update")
		// Return the status update error only if there was no main reconciliation error
		if err == nil {
			err = statusUpdateErr
		}
	}

	// Log reconciliation completion
	if err != nil {
		ctxLogger.Error(err, "Reconciliation completed with error",
			"MCPRegistry.Name", mcpRegistry.Name)
	} else {
		ctxLogger.Info("Reconciliation completed successfully",
			"MCPRegistry.Name", mcpRegistry.Name,
			"phase", mcpRegistry.Status.Phase,
			"requeueAfter", result.RequeueAfter)
	}

	return result, err
}

// reconcileSync checks if sync is needed and performs it if necessary
// This method only handles data synchronization to the target ConfigMap
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
		// Schedule next reconciliation if we have a sync policy
		if nextSyncTime != nil {
			requeueAfter := time.Until(*nextSyncTime)
			ctxLogger.Info("Scheduling next automatic sync", "requeueAfter", requeueAfter)
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
		return ctrl.Result{}, nil
	}

	ctxLogger.Info("Sync needed", "reason", syncReason)

	// Set phase to syncing before starting the sync process
	statusCollector.SetPhase(mcpv1alpha1.MCPRegistryPhaseSyncing)
	statusCollector.SetMessage("Syncing registry data")

	// Handle manual sync with no data changes - update trigger tracking only
	if syncReason == sync.ReasonManualNoChanges {
		return r.syncManager.UpdateManualSyncTriggerOnly(ctx, mcpRegistry)
	}

	result, err := r.syncManager.PerformSync(ctx, mcpRegistry)

	if err != nil {
		// Sync failed - schedule retry with exponential backoff
		ctxLogger.Error(err, "Sync failed, scheduling retry")
		statusCollector.SetPhase(mcpv1alpha1.MCPRegistryPhaseFailed)
		statusCollector.SetMessage(fmt.Sprintf("Sync failed: %v", err))
		// Use a shorter retry interval instead of the full sync interval
		retryAfter := time.Minute * 5 // Default retry interval
		if result.RequeueAfter > 0 {
			// If PerformSync already set a retry interval, use it
			retryAfter = result.RequeueAfter
		}
		return ctrl.Result{RequeueAfter: retryAfter}, err
	}

	// Sync successful - keep in syncing phase until API is also ready
	statusCollector.SetMessage("Registry data synced successfully")

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

// SetupWithManager sets up the controller with the Manager.
func (r *MCPRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPRegistry{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

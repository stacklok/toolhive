package controllers

import (
	"context"
	"fmt"
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
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
)

// MCPRegistryReconciler reconciles a MCPRegistry object
type MCPRegistryReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Dependencies for sync operations
	sourceHandlerFactory sources.SourceHandlerFactory
	storageManager       sources.StorageManager
}

// NewMCPRegistryReconciler creates a new MCPRegistryReconciler with required dependencies
func NewMCPRegistryReconciler(k8sClient client.Client, scheme *runtime.Scheme) *MCPRegistryReconciler {
	return &MCPRegistryReconciler{
		Client:               k8sClient,
		Scheme:               scheme,
		sourceHandlerFactory: sources.NewSourceHandlerFactory(k8sClient),
		storageManager:       sources.NewConfigMapStorageManager(k8sClient, scheme),
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
				return ctrl.Result{}, err
			}

			// Remove the finalizer. Once all finalizers have been removed, the object will be deleted.
			controllerutil.RemoveFinalizer(mcpRegistry, "mcpregistry.toolhive.stacklok.dev/finalizer")
			err := r.Update(ctx, mcpRegistry)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer for this CR
	if !controllerutil.ContainsFinalizer(mcpRegistry, "mcpregistry.toolhive.stacklok.dev/finalizer") {
		controllerutil.AddFinalizer(mcpRegistry, "mcpregistry.toolhive.stacklok.dev/finalizer")
		err = r.Update(ctx, mcpRegistry)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Check if sync is needed before performing it
	return r.reconcileSync(ctx, mcpRegistry)
}

// reconcileSync checks if sync is needed and performs it if necessary
func (r *MCPRegistryReconciler) reconcileSync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Skip sync if registry is already in Ready state and no changes detected
	if mcpRegistry.Status.Phase == mcpv1alpha1.MCPRegistryPhaseReady {
		// Check if source data has changed by comparing hash
		if syncNeeded, err := r.isSyncNeeded(ctx, mcpRegistry); err != nil {
			ctxLogger.Error(err, "Failed to check if sync is needed")
			// Proceed with sync on error to be safe
		} else if !syncNeeded {
			ctxLogger.V(1).Info("Registry is up-to-date, skipping sync")
			return ctrl.Result{}, nil
		}
		ctxLogger.Info("Source data changed, performing sync")
	}

	// Perform the sync operation
	return r.performSync(ctx, mcpRegistry)
}

// isSyncNeeded checks if a sync operation is needed by comparing current source hash with last sync hash
func (r *MCPRegistryReconciler) isSyncNeeded(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, error) {
	// If we don't have a last sync hash, sync is needed
	if mcpRegistry.Status.LastSyncHash == "" {
		return true, nil
	}

	// Get source handler
	sourceHandler, err := r.sourceHandlerFactory.CreateHandler(mcpRegistry.Spec.Source.Type)
	if err != nil {
		return true, err // Sync on error to be safe
	}

	// Get current hash from source
	currentHash, err := sourceHandler.CurrentHash(ctx, mcpRegistry)
	if err != nil {
		return true, err // Sync on error to be safe
	}

	// Compare hashes - sync needed if different
	return currentHash != mcpRegistry.Status.LastSyncHash, nil
}

// performSync performs the complete sync operation for the MCPRegistry
func (r *MCPRegistryReconciler) performSync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Update phase to syncing
	if err := r.updatePhase(ctx, mcpRegistry, mcpv1alpha1.MCPRegistryPhaseSyncing, "Synchronizing registry data"); err != nil {
		return ctrl.Result{}, err
	}

	// Get source handler
	sourceHandler, err := r.sourceHandlerFactory.CreateHandler(mcpRegistry.Spec.Source.Type)
	if err != nil {
		ctxLogger.Error(err, "Failed to create source handler")
		if updateErr := r.updatePhaseFailedWithCondition(ctx, mcpRegistry,
			fmt.Sprintf("Failed to create source handler: %v", err),
			mcpv1alpha1.ConditionSourceAvailable, "HandlerCreationFailed", err.Error()); updateErr != nil {
			ctxLogger.Error(updateErr, "Failed to update status after handler creation failure")
		}
		return ctrl.Result{RequeueAfter: time.Minute * 5}, nil
	}

	// Validate source configuration
	if err := sourceHandler.Validate(&mcpRegistry.Spec.Source); err != nil {
		ctxLogger.Error(err, "Source validation failed")
		if updateErr := r.updatePhaseFailedWithCondition(ctx, mcpRegistry,
			fmt.Sprintf("Source validation failed: %v", err),
			mcpv1alpha1.ConditionSourceAvailable, "ValidationFailed", err.Error()); updateErr != nil {
			ctxLogger.Error(updateErr, "Failed to update status after validation failure")
		}
		return ctrl.Result{RequeueAfter: time.Minute * 5}, nil
	}

	// Execute fetch operation
	fetchResult, err := sourceHandler.FetchRegistry(ctx, mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Fetch operation failed")
		// Increment sync attempts
		mcpRegistry.Status.SyncAttempts++
		if updateErr := r.updatePhaseFailedWithCondition(ctx, mcpRegistry,
			fmt.Sprintf("Fetch failed: %v", err),
			mcpv1alpha1.ConditionSyncSuccessful, "FetchFailed", err.Error()); updateErr != nil {
			ctxLogger.Error(updateErr, "Failed to update status after fetch failure")
		}
		return ctrl.Result{RequeueAfter: time.Minute * 5}, nil
	}

	ctxLogger.Info("Registry data fetched successfully from source",
		"serverCount", fetchResult.ServerCount,
		"format", fetchResult.Format,
		"hash", fetchResult.Hash)

	// Store registry data
	if err := r.storageManager.Store(ctx, mcpRegistry, fetchResult.Registry); err != nil {
		ctxLogger.Error(err, "Failed to store registry data")
		if updateErr := r.updatePhaseFailedWithCondition(ctx, mcpRegistry,
			fmt.Sprintf("Storage failed: %v", err),
			mcpv1alpha1.ConditionSyncSuccessful, "StorageFailed", err.Error()); updateErr != nil {
			ctxLogger.Error(updateErr, "Failed to update status after storage failure")
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
	if err := r.storageManager.Delete(ctx, registry); err != nil {
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

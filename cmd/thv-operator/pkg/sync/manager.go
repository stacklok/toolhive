package sync

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/filtering"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
)

// Sync reason constants
const (
	// Registry state related reasons
	ReasonAlreadyInProgress = "sync-already-in-progress"
	ReasonRegistryNotReady  = "registry-not-ready"

	// Data change related reasons
	ReasonSourceDataChanged    = "source-data-changed"
	ReasonErrorCheckingChanges = "error-checking-data-changes"

	// Manual sync related reasons
	ReasonManualWithChanges = "manual-sync-with-data-changes"
	ReasonManualNoChanges   = "manual-sync-no-data-changes"

	// Automatic sync related reasons
	ReasonErrorParsingInterval  = "error-parsing-sync-interval"
	ReasonErrorCheckingSyncNeed = "error-checking-sync-need"

	// Up-to-date reasons
	ReasonUpToDateWithPolicy = "up-to-date-with-policy"
	ReasonUpToDateNoPolicy   = "up-to-date-no-policy"
)

// Manual sync annotation detection reasons
const (
	ManualSyncReasonNoAnnotations    = "no-annotations"
	ManualSyncReasonNoTrigger        = "no-manual-trigger"
	ManualSyncReasonAlreadyProcessed = "manual-trigger-already-processed"
	ManualSyncReasonRequested        = "manual-sync-requested"
)

// Condition reasons for status conditions
const (
	// Failure reasons
	conditionReasonHandlerCreationFailed = "HandlerCreationFailed"
	conditionReasonValidationFailed      = "ValidationFailed"
	conditionReasonFetchFailed           = "FetchFailed"
	conditionReasonStorageFailed         = "StorageFailed"

	// Success reasons
	conditionReasonSourceReady   = "SourceReady"
	conditionReasonDataValid     = "DataValid"
	conditionReasonSyncCompleted = "SyncCompleted"
)

// Manager manages synchronization operations for MCPRegistry resources
type Manager interface {
	// ShouldSync determines if a sync operation is needed
	ShouldSync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, string, *time.Time, error)

	// PerformSync executes the complete sync operation
	PerformSync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (ctrl.Result, error)

	// UpdateManualSyncTriggerOnly updates manual sync trigger tracking without performing actual sync
	UpdateManualSyncTriggerOnly(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (ctrl.Result, error)

	// Delete cleans up storage resources for the MCPRegistry
	Delete(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) error
}

// DataChangeDetector detects changes in source data
type DataChangeDetector interface {
	// IsDataChanged checks if source data has changed by comparing hashes
	IsDataChanged(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, error)
}

// ManualSyncChecker handles manual sync detection logic
type ManualSyncChecker interface {
	// IsManualSyncRequested checks if a manual sync was requested via annotation
	IsManualSyncRequested(mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, string)
}

// AutomaticSyncChecker handles automatic sync timing logic
type AutomaticSyncChecker interface {
	// IsIntervalSyncNeeded checks if sync is needed based on time interval
	// Returns (syncNeeded, nextSyncTime, error) where nextSyncTime is always in the future
	IsIntervalSyncNeeded(mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, time.Time, error)
}

// DefaultSyncManager is the default implementation of Manager
type DefaultSyncManager struct {
	client               client.Client
	scheme               *runtime.Scheme
	sourceHandlerFactory sources.SourceHandlerFactory
	storageManager       sources.StorageManager
	filterService        filtering.FilterService
	dataChangeDetector   DataChangeDetector
	manualSyncChecker    ManualSyncChecker
	automaticSyncChecker AutomaticSyncChecker
}

// NewDefaultSyncManager creates a new DefaultSyncManager
func NewDefaultSyncManager(k8sClient client.Client, scheme *runtime.Scheme,
	sourceHandlerFactory sources.SourceHandlerFactory, storageManager sources.StorageManager) *DefaultSyncManager {
	return &DefaultSyncManager{
		client:               k8sClient,
		scheme:               scheme,
		sourceHandlerFactory: sourceHandlerFactory,
		storageManager:       storageManager,
		filterService:        filtering.NewDefaultFilterService(),
		dataChangeDetector:   &DefaultDataChangeDetector{sourceHandlerFactory: sourceHandlerFactory},
		manualSyncChecker:    &DefaultManualSyncChecker{},
		automaticSyncChecker: &DefaultAutomaticSyncChecker{},
	}
}

// ShouldSync determines if a sync operation is needed and when the next sync should occur
func (s *DefaultSyncManager) ShouldSync(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, string, *time.Time, error) {
	// If registry is currently syncing, don't start another sync
	if mcpRegistry.Status.Phase == mcpv1alpha1.MCPRegistryPhaseSyncing {
		return false, ReasonAlreadyInProgress, nil, nil
	}

	// If registry is in Failed or Pending state, sync is needed
	if mcpRegistry.Status.Phase != mcpv1alpha1.MCPRegistryPhaseReady {
		return true, ReasonRegistryNotReady, nil, nil
	}

	// Check if source data has changed by comparing hash
	dataChanged, err := s.dataChangeDetector.IsDataChanged(ctx, mcpRegistry)
	if err != nil {
		return true, ReasonErrorCheckingChanges, nil, err
	}

	// Check for manual sync trigger first (always update trigger tracking)
	manualSyncRequested, _ := s.manualSyncChecker.IsManualSyncRequested(mcpRegistry)
	// Manual sync was requested - but only sync if data has actually changed
	if manualSyncRequested {
		if dataChanged {
			return true, ReasonManualWithChanges, nil, nil
		}
		// Manual sync requested but no data changes - update trigger tracking only
		return true, ReasonManualNoChanges, nil, nil
	}

	if dataChanged {
		return true, ReasonSourceDataChanged, nil, nil
	}

	// Data hasn't changed - check if we need to schedule future checks
	if mcpRegistry.Spec.SyncPolicy != nil {
		_, nextSyncTime, err := s.automaticSyncChecker.IsIntervalSyncNeeded(mcpRegistry)
		if err != nil {
			return true, ReasonErrorParsingInterval, nil, err
		}

		// No sync needed since data hasn't changed, but schedule next check
		return false, ReasonUpToDateWithPolicy, &nextSyncTime, nil
	}

	// No automatic sync policy, registry is up-to-date
	return false, ReasonUpToDateNoPolicy, nil, nil
}

// PerformSync performs the complete sync operation for the MCPRegistry
func (s *DefaultSyncManager) PerformSync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (ctrl.Result, error) {
	// Update phase to syncing
	if err := s.updatePhase(ctx, mcpRegistry, mcpv1alpha1.MCPRegistryPhaseSyncing, "Synchronizing registry data"); err != nil {
		return ctrl.Result{}, err
	}

	// Fetch and process registry data
	fetchResult, err := s.fetchAndProcessRegistryData(ctx, mcpRegistry)
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Minute * 5}, err
	}

	// Store the processed registry data
	if err := s.storeRegistryData(ctx, mcpRegistry, fetchResult); err != nil {
		return ctrl.Result{RequeueAfter: time.Minute * 5}, err
	}

	// Update registry status with successful sync results
	if err := s.updateSuccessfulSyncStatus(ctx, mcpRegistry, fetchResult); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// UpdateManualSyncTriggerOnly updates the manual sync trigger tracking without performing actual sync
func (s *DefaultSyncManager) UpdateManualSyncTriggerOnly(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Refresh the object to get latest resourceVersion
	if err := s.client.Get(ctx, client.ObjectKeyFromObject(mcpRegistry), mcpRegistry); err != nil {
		return ctrl.Result{}, err
	}

	// Update manual sync trigger tracking
	if mcpRegistry.Annotations != nil {
		if triggerValue := mcpRegistry.Annotations[SyncTriggerAnnotation]; triggerValue != "" {
			mcpRegistry.Status.LastManualSyncTrigger = triggerValue
			ctxLogger.Info("Manual sync trigger processed (no data changes)", "trigger", triggerValue)
		}
	}

	// Update status
	if err := s.client.Status().Update(ctx, mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to update manual sync trigger tracking")
		return ctrl.Result{}, err
	}

	ctxLogger.Info("Manual sync completed (no data changes required)")
	return ctrl.Result{}, nil
}

// Delete cleans up storage resources for the MCPRegistry
func (s *DefaultSyncManager) Delete(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) error {
	return s.storageManager.Delete(ctx, mcpRegistry)
}

// updatePhase updates the MCPRegistry phase and message
func (s *DefaultSyncManager) updatePhase(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry,
	phase mcpv1alpha1.MCPRegistryPhase, message string) error {
	mcpRegistry.Status.Phase = phase
	mcpRegistry.Status.Message = message
	return s.client.Status().Update(ctx, mcpRegistry)
}

// updatePhaseFailedWithCondition updates phase, message and sets a condition
func (s *DefaultSyncManager) updatePhaseFailedWithCondition(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry,
	message string, conditionType string, reason, conditionMessage string) error {

	// Refresh object to get latest resourceVersion
	if err := s.client.Get(ctx, client.ObjectKeyFromObject(mcpRegistry), mcpRegistry); err != nil {
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

	return s.client.Status().Update(ctx, mcpRegistry)
}

// fetchAndProcessRegistryData handles source handler creation, validation, fetch, and filtering
func (s *DefaultSyncManager) fetchAndProcessRegistryData(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry) (*sources.FetchResult, error) {
	ctxLogger := log.FromContext(ctx)

	// Get source handler
	sourceHandler, err := s.sourceHandlerFactory.CreateHandler(mcpRegistry.Spec.Source.Type)
	if err != nil {
		ctxLogger.Error(err, "Failed to create source handler")
		if updateErr := s.updatePhaseFailedWithCondition(ctx, mcpRegistry,
			fmt.Sprintf("Failed to create source handler: %v", err),
			mcpv1alpha1.ConditionSourceAvailable, conditionReasonHandlerCreationFailed, err.Error()); updateErr != nil {
			ctxLogger.Error(updateErr, "Failed to update status after handler creation failure")
		}
		return nil, err
	}

	// Validate source configuration
	if err := sourceHandler.Validate(&mcpRegistry.Spec.Source); err != nil {
		ctxLogger.Error(err, "Source validation failed")
		if updateErr := s.updatePhaseFailedWithCondition(ctx, mcpRegistry,
			fmt.Sprintf("Source validation failed: %v", err),
			mcpv1alpha1.ConditionSourceAvailable, conditionReasonValidationFailed, err.Error()); updateErr != nil {
			ctxLogger.Error(updateErr, "Failed to update status after validation failure")
		}
		return nil, err
	}

	// Execute fetch operation
	fetchResult, err := sourceHandler.FetchRegistry(ctx, mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Fetch operation failed")
		// Increment sync attempts
		mcpRegistry.Status.SyncAttempts++
		if updateErr := s.updatePhaseFailedWithCondition(ctx, mcpRegistry,
			fmt.Sprintf("Fetch failed: %v", err),
			mcpv1alpha1.ConditionSyncSuccessful, conditionReasonFetchFailed, err.Error()); updateErr != nil {
			ctxLogger.Error(updateErr, "Failed to update status after fetch failure")
		}
		return nil, err
	}

	ctxLogger.Info("Registry data fetched successfully from source",
		"serverCount", fetchResult.ServerCount,
		"format", fetchResult.Format,
		"hash", fetchResult.Hash)

	// Apply filtering if configured
	if err := s.applyFilteringIfConfigured(ctx, mcpRegistry, fetchResult); err != nil {
		return nil, err
	}

	return fetchResult, nil
}

// applyFilteringIfConfigured applies filtering to fetch result if registry has filter configuration
func (s *DefaultSyncManager) applyFilteringIfConfigured(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	fetchResult *sources.FetchResult) error {
	ctxLogger := log.FromContext(ctx)

	if mcpRegistry.Spec.Filter != nil {
		ctxLogger.Info("Applying registry filters",
			"hasNameFilters", mcpRegistry.Spec.Filter.NameFilters != nil,
			"hasTagFilters", mcpRegistry.Spec.Filter.Tags != nil)

		filteredRegistry, err := s.filterService.ApplyFilters(ctx, fetchResult.Registry, mcpRegistry.Spec.Filter)
		if err != nil {
			ctxLogger.Error(err, "Registry filtering failed")
			if updateErr := s.updatePhaseFailedWithCondition(ctx, mcpRegistry,
				fmt.Sprintf("Filtering failed: %v", err),
				mcpv1alpha1.ConditionSyncSuccessful, conditionReasonFetchFailed, err.Error()); updateErr != nil {
				ctxLogger.Error(updateErr, "Failed to update status after filtering failure")
			}
			return err
		}

		// Update fetch result with filtered data
		originalServerCount := fetchResult.ServerCount
		fetchResult.Registry = filteredRegistry
		fetchResult.ServerCount = len(filteredRegistry.Servers) + len(filteredRegistry.RemoteServers)

		ctxLogger.Info("Registry filtering completed",
			"originalServerCount", originalServerCount,
			"filteredServerCount", fetchResult.ServerCount,
			"serversFiltered", originalServerCount-fetchResult.ServerCount)
	} else {
		ctxLogger.Info("No filtering configured, using original registry data")
	}

	return nil
}

// storeRegistryData stores the registry data using the storage manager
func (s *DefaultSyncManager) storeRegistryData(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	fetchResult *sources.FetchResult) error {
	ctxLogger := log.FromContext(ctx)

	if err := s.storageManager.Store(ctx, mcpRegistry, fetchResult.Registry); err != nil {
		ctxLogger.Error(err, "Failed to store registry data")
		if updateErr := s.updatePhaseFailedWithCondition(ctx, mcpRegistry,
			fmt.Sprintf("Storage failed: %v", err),
			mcpv1alpha1.ConditionSyncSuccessful, conditionReasonStorageFailed, err.Error()); updateErr != nil {
			ctxLogger.Error(updateErr, "Failed to update status after storage failure")
		}
		return err
	}

	ctxLogger.Info("Registry data stored successfully",
		"namespace", mcpRegistry.Namespace,
		"registryName", mcpRegistry.Name)

	return nil
}

// updateSuccessfulSyncStatus updates the MCPRegistry status after a successful sync operation
func (s *DefaultSyncManager) updateSuccessfulSyncStatus(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	fetchResult *sources.FetchResult) error {
	ctxLogger := log.FromContext(ctx)

	// Refresh the object to get latest resourceVersion before final update
	if err := s.client.Get(ctx, client.ObjectKeyFromObject(mcpRegistry), mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to refresh MCPRegistry object")
		return err
	}

	// Get storage reference
	storageRef := s.storageManager.GetStorageReference(mcpRegistry)

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
		if triggerValue := mcpRegistry.Annotations[SyncTriggerAnnotation]; triggerValue != "" {
			mcpRegistry.Status.LastManualSyncTrigger = triggerValue
			ctxLogger.Info("Manual sync trigger processed", "trigger", triggerValue)
		}
	}

	// Set all success conditions in memory
	meta.SetStatusCondition(&mcpRegistry.Status.Conditions, metav1.Condition{
		Type:    mcpv1alpha1.ConditionSourceAvailable,
		Status:  metav1.ConditionTrue,
		Reason:  conditionReasonSourceReady,
		Message: "Source configuration is valid and accessible",
	})
	meta.SetStatusCondition(&mcpRegistry.Status.Conditions, metav1.Condition{
		Type:    mcpv1alpha1.ConditionDataValid,
		Status:  metav1.ConditionTrue,
		Reason:  conditionReasonDataValid,
		Message: "Registry data is valid and parsed successfully",
	})
	meta.SetStatusCondition(&mcpRegistry.Status.Conditions, metav1.Condition{
		Type:    mcpv1alpha1.ConditionSyncSuccessful,
		Status:  metav1.ConditionTrue,
		Reason:  conditionReasonSyncCompleted,
		Message: "Registry sync completed successfully",
	})

	// Single final status update
	if err := s.client.Status().Update(ctx, mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to update final status")
		return err
	}

	ctxLogger.Info("MCPRegistry sync completed successfully",
		"serverCount", fetchResult.ServerCount,
		"hash", fetchResult.Hash)

	return nil
}

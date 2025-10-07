package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/filtering"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/mcpregistrystatus"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
)

// Result contains the result of a successful sync operation
type Result struct {
	Hash        string
	ServerCount int
}

// Sync reason constants
const (
	// Registry state related reasons
	ReasonAlreadyInProgress     = "sync-already-in-progress"
	ReasonRegistryNotReady      = "registry-not-ready"
	ReasonRequeueTimeNotElapsed = "requeue-time-not-elapsed"

	// Filter change related reasons
	ReasonFilterChanged = "filter-changed"

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
)

// Default timing constants for the sync manager
const (
	// DefaultSyncRequeueAfterConstant is the constant default requeue interval for sync operations
	DefaultSyncRequeueAfterConstant = time.Minute * 5
)

// Configurable timing variables for testing
var (
	// DefaultSyncRequeueAfter is the configurable default requeue interval for sync operations
	// This can be modified in tests to speed up requeue behavior
	DefaultSyncRequeueAfter = DefaultSyncRequeueAfterConstant
)

// Manager manages synchronization operations for MCPRegistry resources
type Manager interface {
	// ShouldSync determines if a sync operation is needed
	ShouldSync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, string, *time.Time)

	// PerformSync executes the complete sync operation
	PerformSync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (ctrl.Result, *Result, *mcpregistrystatus.Error)

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
// nolint:gocyclo
func (s *DefaultSyncManager) ShouldSync(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, string, *time.Time) {
	ctxLogger := log.FromContext(ctx)

	// If registry is currently syncing, don't start another sync
	if mcpRegistry.Status.Phase == mcpv1alpha1.MCPRegistryPhaseSyncing {
		return false, ReasonAlreadyInProgress, nil
	}

	// Check if requeue time has elapsed and pre-compute next sync time
	requeueElapsed, nextSyncTime := s.calculateNextSyncTime(ctx, mcpRegistry)

	// Check if sync is needed based on registry state
	syncNeededForState := s.isSyncNeededForState(mcpRegistry)
	// Check for manual sync trigger first (always update trigger tracking)
	manualSyncRequested, _ := s.manualSyncChecker.IsManualSyncRequested(mcpRegistry)
	// Check if filter has changed
	filterChanged := s.isFilterChanged(ctx, mcpRegistry)

	shouldSync := false
	reason := ReasonUpToDateNoPolicy

	if syncNeededForState {
		if !requeueElapsed {
			ctxLogger.Info("Sync not needed because requeue time not elapsed",
				"requeueTime", DefaultSyncRequeueAfter, "lastAttempt", mcpRegistry.Status.SyncStatus.LastAttempt)
			reason = ReasonRequeueTimeNotElapsed
		} else {
			shouldSync = true
		}
	}

	if !shouldSync && manualSyncRequested {
		// Manual sync requested
		shouldSync = true
		nextSyncTime = nil
	}

	if !shouldSync && filterChanged {
		// Filter changed
		shouldSync = true
		reason = ReasonFilterChanged
	} else if shouldSync || requeueElapsed {
		// Check if source data has changed by comparing hash
		dataChanged, err := s.dataChangeDetector.IsDataChanged(ctx, mcpRegistry)
		if err != nil {
			ctxLogger.Error(err, "Failed to determine if data has changed")
			shouldSync = true
			reason = ReasonErrorCheckingChanges
		} else {
			ctxLogger.Info("Checked data changes", "dataChanged", dataChanged)
			if dataChanged {
				shouldSync = true
				if syncNeededForState {
					reason = ReasonRegistryNotReady
				} else if manualSyncRequested {
					reason = ReasonManualWithChanges
				} else {
					reason = ReasonSourceDataChanged
				}
			} else {
				shouldSync = false
				if syncNeededForState {
					reason = ReasonUpToDateWithPolicy
				} else {
					reason = ReasonManualNoChanges
				}
			}
		}
	}

	ctxLogger.Info("ShouldSync", "syncNeededForState", syncNeededForState, "filterChanged", filterChanged,
		"requeueElapsed", requeueElapsed, "manualSyncRequested", manualSyncRequested, "nextSyncTime",
		nextSyncTime)
	ctxLogger.Info("ShouldSync returning", "shouldSync", shouldSync, "reason", reason, "nextSyncTime", nextSyncTime)

	if shouldSync {
		return shouldSync, reason, nil
	}
	return shouldSync, reason, nextSyncTime
}

// isSyncNeededForState checks if sync is needed based on the registry's current state
func (*DefaultSyncManager) isSyncNeededForState(mcpRegistry *mcpv1alpha1.MCPRegistry) bool {
	// If we have sync status, use it to determine sync readiness
	if mcpRegistry.Status.SyncStatus != nil {
		syncPhase := mcpRegistry.Status.SyncStatus.Phase
		// If sync is failed, sync is needed
		if syncPhase == mcpv1alpha1.SyncPhaseFailed {
			return true
		}
		// If sync is not complete, sync is needed
		if syncPhase != mcpv1alpha1.SyncPhaseComplete {
			return true
		}
		// Sync is complete, no sync needed based on state
		return false
	}

	// If we don't have sync status, sync is needed
	return true
}

// isFilterChanged checks if the filter has changed compared to the last applied configuration
func (*DefaultSyncManager) isFilterChanged(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) bool {
	logger := log.FromContext(ctx)

	currentFilter := mcpRegistry.Spec.Filter
	currentFilterJSON, err := json.Marshal(currentFilter)
	if err != nil {
		logger.Error(err, "Failed to marshal current filter")
		return false
	}
	currentFilterHash := sha256.Sum256(currentFilterJSON)
	currentHashStr := hex.EncodeToString(currentFilterHash[:])

	lastHash := mcpRegistry.Status.LastAppliedFilterHash
	if lastHash == "" {
		// First time - no change
		return false
	}

	logger.V(1).Info("Current filter hash", "currentFilterHash", currentHashStr)
	logger.V(1).Info("Last applied filter hash", "lastHash", lastHash)
	return currentHashStr != lastHash
}

// calculateNextSyncTime checks if the requeue or sync policy time has elapsed and calculates the next requeue time
func (s *DefaultSyncManager) calculateNextSyncTime(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, *time.Time) {
	ctxLogger := log.FromContext(ctx)

	// First consider the requeue time
	requeueElapsed := false
	var nextSyncTime time.Time
	if mcpRegistry.Status.SyncStatus != nil {
		if mcpRegistry.Status.SyncStatus.LastAttempt != nil {
			nextSyncTime = mcpRegistry.Status.SyncStatus.LastAttempt.Add(DefaultSyncRequeueAfter)
		}
	}

	// If we have a sync policy, check if the next automatic sync time is sooner than the next requeue time
	if mcpRegistry.Spec.SyncPolicy != nil {
		autoSyncNeeded, nextAutomaticSyncTime, err := s.automaticSyncChecker.IsIntervalSyncNeeded(mcpRegistry)
		if err != nil {
			ctxLogger.Error(err, "Failed to determine if interval sync is needed")
		}

		// Resync at the earlier time between the next sync time and the next automatic sync time
		if autoSyncNeeded && nextSyncTime.After(nextAutomaticSyncTime) {
			nextSyncTime = nextAutomaticSyncTime
		}
	}

	requeueElapsed = time.Now().After(nextSyncTime)
	return requeueElapsed, &nextSyncTime
}

// PerformSync performs the complete sync operation for the MCPRegistry
// The controller is responsible for setting sync status via the status collector
func (s *DefaultSyncManager) PerformSync(
	ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry,
) (ctrl.Result, *Result, *mcpregistrystatus.Error) {
	// Fetch and process registry data
	fetchResult, err := s.fetchAndProcessRegistryData(ctx, mcpRegistry)
	if err != nil {
		return ctrl.Result{RequeueAfter: DefaultSyncRequeueAfter}, nil, err
	}

	// Store the processed registry data
	if err := s.storeRegistryData(ctx, mcpRegistry, fetchResult); err != nil {
		return ctrl.Result{RequeueAfter: DefaultSyncRequeueAfter}, nil, err
	}

	// Return sync result with data for status collector
	syncResult := &Result{
		Hash:        fetchResult.Hash,
		ServerCount: fetchResult.ServerCount,
	}

	return ctrl.Result{}, syncResult, nil
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
		if triggerValue := mcpRegistry.Annotations[mcpregistrystatus.SyncTriggerAnnotation]; triggerValue != "" {
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

// fetchAndProcessRegistryData handles source handler creation, validation, fetch, and filtering
func (s *DefaultSyncManager) fetchAndProcessRegistryData(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry) (*sources.FetchResult, *mcpregistrystatus.Error) {
	ctxLogger := log.FromContext(ctx)

	// Get source handler
	sourceHandler, err := s.sourceHandlerFactory.CreateHandler(mcpRegistry.Spec.Source.Type)
	if err != nil {
		ctxLogger.Error(err, "Failed to create source handler")
		return nil, &mcpregistrystatus.Error{
			Err:             err,
			Message:         fmt.Sprintf("Failed to create source handler: %v", err),
			ConditionType:   mcpv1alpha1.ConditionSourceAvailable,
			ConditionReason: conditionReasonHandlerCreationFailed,
		}
	}

	// Validate source configuration
	if err := sourceHandler.Validate(&mcpRegistry.Spec.Source); err != nil {
		ctxLogger.Error(err, "Source validation failed")
		return nil, &mcpregistrystatus.Error{
			Err:             err,
			Message:         fmt.Sprintf("Source validation failed: %v", err),
			ConditionType:   mcpv1alpha1.ConditionSourceAvailable,
			ConditionReason: conditionReasonValidationFailed,
		}
	}

	// Execute fetch operation
	fetchResult, err := sourceHandler.FetchRegistry(ctx, mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Fetch operation failed")
		// Sync attempt counting is now handled by the controller via status collector
		return nil, &mcpregistrystatus.Error{
			Err:             err,
			Message:         fmt.Sprintf("Fetch failed: %v", err),
			ConditionType:   mcpv1alpha1.ConditionSyncSuccessful,
			ConditionReason: conditionReasonFetchFailed,
		}
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
	fetchResult *sources.FetchResult) *mcpregistrystatus.Error {
	ctxLogger := log.FromContext(ctx)

	if mcpRegistry.Spec.Filter != nil {
		ctxLogger.Info("Applying registry filters",
			"hasNameFilters", mcpRegistry.Spec.Filter.NameFilters != nil,
			"hasTagFilters", mcpRegistry.Spec.Filter.Tags != nil)

		filteredRegistry, err := s.filterService.ApplyFilters(ctx, fetchResult.Registry, mcpRegistry.Spec.Filter)
		if err != nil {
			ctxLogger.Error(err, "Registry filtering failed")
			return &mcpregistrystatus.Error{
				Err:             err,
				Message:         fmt.Sprintf("Filtering failed: %v", err),
				ConditionType:   mcpv1alpha1.ConditionSyncSuccessful,
				ConditionReason: conditionReasonFetchFailed,
			}
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
	fetchResult *sources.FetchResult) *mcpregistrystatus.Error {
	ctxLogger := log.FromContext(ctx)

	if err := s.storageManager.Store(ctx, mcpRegistry, fetchResult.Registry); err != nil {
		ctxLogger.Error(err, "Failed to store registry data")
		return &mcpregistrystatus.Error{
			Err:             err,
			Message:         fmt.Sprintf("Storage failed: %v", err),
			ConditionType:   mcpv1alpha1.ConditionSyncSuccessful,
			ConditionReason: conditionReasonStorageFailed,
		}
	}

	ctxLogger.Info("Registry data stored successfully",
		"namespace", mcpRegistry.Namespace,
		"registryName", mcpRegistry.Name)

	return nil
}

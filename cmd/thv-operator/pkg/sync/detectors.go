package sync

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/mcpregistrystatus"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
)

// DefaultDataChangeDetector implements DataChangeDetector
type DefaultDataChangeDetector struct {
	sourceHandlerFactory sources.SourceHandlerFactory
}

// IsDataChanged checks if source data has changed by comparing hashes
func (d *DefaultDataChangeDetector) IsDataChanged(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, error) {
	// Check for hash in syncStatus first, then fallback
	var lastSyncHash string
	if mcpRegistry.Status.SyncStatus != nil {
		lastSyncHash = mcpRegistry.Status.SyncStatus.LastSyncHash
	}

	// If we don't have a last sync hash, consider data changed
	if lastSyncHash == "" {
		return true, nil
	}

	// Get source handler
	sourceHandler, err := d.sourceHandlerFactory.CreateHandler(mcpRegistry.Spec.Source.Type)
	if err != nil {
		return true, err
	}

	// Get current hash from source
	currentHash, err := sourceHandler.CurrentHash(ctx, mcpRegistry)
	if err != nil {
		return true, err
	}

	// Compare hashes - data changed if different
	return currentHash != lastSyncHash, nil
}

// DefaultManualSyncChecker implements ManualSyncChecker
type DefaultManualSyncChecker struct{}

// IsManualSyncRequested checks if a manual sync was requested via annotation
func (*DefaultManualSyncChecker) IsManualSyncRequested(mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, string) {
	// Check if sync-trigger annotation exists
	if mcpRegistry.Annotations == nil {
		return false, ManualSyncReasonNoAnnotations
	}

	triggerValue := mcpRegistry.Annotations[mcpregistrystatus.SyncTriggerAnnotation]
	if triggerValue == "" {
		return false, ManualSyncReasonNoTrigger
	}

	// Check if this trigger was already processed
	lastProcessed := mcpRegistry.Status.LastManualSyncTrigger
	if triggerValue == lastProcessed {
		return false, ManualSyncReasonAlreadyProcessed
	}

	return true, ManualSyncReasonRequested
}

// DefaultAutomaticSyncChecker implements AutomaticSyncChecker
type DefaultAutomaticSyncChecker struct{}

// IsIntervalSyncNeeded checks if sync is needed based on time interval
// Returns: (syncNeeded, nextSyncTime, error)
// nextSyncTime is always a future time when the next sync should occur
func (*DefaultAutomaticSyncChecker) IsIntervalSyncNeeded(mcpRegistry *mcpv1alpha1.MCPRegistry) (bool, time.Time, error) {
	// Parse the sync interval
	interval, err := time.ParseDuration(mcpRegistry.Spec.SyncPolicy.Interval)
	if err != nil {
		return false, time.Time{}, err
	}

	now := time.Now()

	// Check for last sync time in syncStatus first, then fallback
	var lastSyncTime *metav1.Time
	if mcpRegistry.Status.SyncStatus != nil {
		lastSyncTime = mcpRegistry.Status.SyncStatus.LastAttempt
	}

	// If we don't have a last sync time, sync is needed
	if lastSyncTime == nil {
		return true, now.Add(interval), nil
	}

	// Calculate when next sync should happen based on last sync
	nextSyncTime := lastSyncTime.Add(interval)

	// Check if it's time for the next sync
	syncNeeded := now.After(nextSyncTime) || now.Equal(nextSyncTime)

	if syncNeeded {
		// If sync is needed now, calculate when the next one after this should be
		return true, now.Add(interval), nil
	}

	// Sync not needed yet, return the originally calculated next sync time
	return false, nextSyncTime, nil
}

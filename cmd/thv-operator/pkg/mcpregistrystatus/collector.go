// Package mcpregistrystatus provides status management and batched updates for MCPRegistry resources.
package mcpregistrystatus

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// StatusCollector collects status changes during reconciliation
// and applies them in a single batch update at the end.
// It implements the StatusManager interface.
type StatusCollector struct {
	mcpRegistry *mcpv1alpha1.MCPRegistry
	hasChanges  bool
	phase       *mcpv1alpha1.MCPRegistryPhase
	message     *string
	syncStatus  *mcpv1alpha1.SyncStatus
	apiStatus   *mcpv1alpha1.APIStatus
	conditions  map[string]metav1.Condition

	// Component collectors
	syncCollector *syncStatusCollector
	apiCollector  *apiStatusCollector
}

// syncStatusCollector implements SyncStatusCollector
type syncStatusCollector struct {
	parent *StatusCollector
}

// apiStatusCollector implements APIStatusCollector
type apiStatusCollector struct {
	parent *StatusCollector
}

// NewStatusManager creates a new StatusManager for the given MCPRegistry resource.
func NewStatusManager(mcpRegistry *mcpv1alpha1.MCPRegistry) StatusManager {
	return newStatusCollector(mcpRegistry)
}

// newStatusCollector creates the internal StatusCollector implementation
func newStatusCollector(mcpRegistry *mcpv1alpha1.MCPRegistry) *StatusCollector {
	collector := &StatusCollector{
		mcpRegistry: mcpRegistry,
		conditions:  make(map[string]metav1.Condition),
	}
	collector.syncCollector = &syncStatusCollector{parent: collector}
	collector.apiCollector = &apiStatusCollector{parent: collector}
	return collector
}

// SetPhase sets the phase to be updated.
func (s *StatusCollector) SetPhase(phase mcpv1alpha1.MCPRegistryPhase) {
	s.phase = &phase
	s.hasChanges = true
}

// SetMessage sets the message to be updated.
func (s *StatusCollector) SetMessage(message string) {
	s.message = &message
	s.hasChanges = true
}

// SetCondition sets a general condition with the specified type, reason, message, and status
func (s *StatusCollector) SetCondition(conditionType, reason, message string, status metav1.ConditionStatus) {
	s.conditions[conditionType] = metav1.Condition{
		Type:    conditionType,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
	s.hasChanges = true
}

// SetAPIReadyCondition adds or updates the API ready condition.
func (s *StatusCollector) SetAPIReadyCondition(reason, message string, status metav1.ConditionStatus) {
	s.SetCondition(mcpv1alpha1.ConditionAPIReady, reason, message, status)
}

// SetSyncStatus sets the detailed sync status.
func (s *StatusCollector) SetSyncStatus(
	phase mcpv1alpha1.SyncPhase, message string, attemptCount int,
	lastSyncTime *metav1.Time, lastSyncHash string, serverCount int) {
	now := metav1.Now()
	s.syncStatus = &mcpv1alpha1.SyncStatus{
		Phase:        phase,
		Message:      message,
		LastAttempt:  &now,
		AttemptCount: attemptCount,
		LastSyncTime: lastSyncTime,
		LastSyncHash: lastSyncHash,
		ServerCount:  serverCount,
	}
	s.hasChanges = true
}

// SetAPIStatus sets the detailed API status.
func (s *StatusCollector) SetAPIStatus(phase mcpv1alpha1.APIPhase, message string, endpoint string) {
	s.apiStatus = &mcpv1alpha1.APIStatus{
		Phase:    phase,
		Message:  message,
		Endpoint: endpoint,
	}

	// Set ReadySince timestamp when API becomes ready
	if phase == mcpv1alpha1.APIPhaseReady &&
		(s.mcpRegistry.Status.APIStatus == nil || s.mcpRegistry.Status.APIStatus.Phase != mcpv1alpha1.APIPhaseReady) {
		now := metav1.Now()
		s.apiStatus.ReadySince = &now
	} else if s.mcpRegistry.Status.APIStatus != nil && s.mcpRegistry.Status.APIStatus.ReadySince != nil {
		// Preserve existing ReadySince if already set and still ready
		s.apiStatus.ReadySince = s.mcpRegistry.Status.APIStatus.ReadySince
	}

	s.hasChanges = true
}

// Apply applies all collected status changes in a single batch update.
func (s *StatusCollector) Apply(ctx context.Context, k8sClient client.Client) error {
	if !s.hasChanges {
		return nil
	}

	ctxLogger := log.FromContext(ctx)

	// Refetch the latest version of the resource to avoid conflicts
	latestRegistry := &mcpv1alpha1.MCPRegistry{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(s.mcpRegistry), latestRegistry); err != nil {
		ctxLogger.Error(err, "Failed to fetch latest MCPRegistry version for status update")
		return fmt.Errorf("failed to fetch latest MCPRegistry version: %w", err)
	}

	// Apply phase change
	if s.phase != nil {
		latestRegistry.Status.Phase = *s.phase
	}

	// Apply message change
	if s.message != nil {
		latestRegistry.Status.Message = *s.message
	}

	// Apply sync status change
	if s.syncStatus != nil {
		latestRegistry.Status.SyncStatus = s.syncStatus
	}

	// Apply API status change
	if s.apiStatus != nil {
		latestRegistry.Status.APIStatus = s.apiStatus
	}

	// Apply condition changes
	for _, condition := range s.conditions {
		meta.SetStatusCondition(&latestRegistry.Status.Conditions, condition)
	}

	// Single status update using the latest version
	if err := k8sClient.Status().Update(ctx, latestRegistry); err != nil {
		ctxLogger.Error(err, "Failed to apply batched status update")
		return fmt.Errorf("failed to apply batched status update: %w", err)
	}

	ctxLogger.V(1).Info("Applied batched status update",
		"phase", s.phase,
		"message", s.message,
		"conditionsCount", len(s.conditions))

	return nil
}

// StatusManager interface methods

// Sync returns the sync status collector
func (s *StatusCollector) Sync() SyncStatusCollector {
	return s.syncCollector
}

// API returns the API status collector
func (s *StatusCollector) API() APIStatusCollector {
	return s.apiCollector
}

// SetOverallStatus sets the overall phase and message explicitly (for special cases)
func (s *StatusCollector) SetOverallStatus(phase mcpv1alpha1.MCPRegistryPhase, message string) {
	s.SetPhase(phase)
	s.SetMessage(message)
}

// SyncStatusCollector implementation

// SetSyncCondition sets a sync-related condition
func (sc *syncStatusCollector) SetSyncCondition(condition metav1.Condition) {
	sc.parent.conditions[condition.Type] = condition
	sc.parent.hasChanges = true
}

// SetSyncStatus delegates to the parent's SetSyncStatus method
func (sc *syncStatusCollector) SetSyncStatus(phase mcpv1alpha1.SyncPhase, message string, attemptCount int,
	lastSyncTime *metav1.Time, lastSyncHash string, serverCount int) {
	sc.parent.SetSyncStatus(phase, message, attemptCount, lastSyncTime, lastSyncHash, serverCount)
}

// APIStatusCollector implementation

// SetAPIStatus delegates to the parent's SetAPIStatus method
func (ac *apiStatusCollector) SetAPIStatus(phase mcpv1alpha1.APIPhase, message string, endpoint string) {
	ac.parent.SetAPIStatus(phase, message, endpoint)
}

// SetAPIReadyCondition delegates to the parent's SetAPIReadyCondition method
func (ac *apiStatusCollector) SetAPIReadyCondition(reason, message string, status metav1.ConditionStatus) {
	ac.parent.SetAPIReadyCondition(reason, message, status)
}

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
// It implements the Collector interface.
type StatusCollector struct {
	mcpRegistry *mcpv1alpha1.MCPRegistry
	hasChanges  bool
	phase       *mcpv1alpha1.MCPRegistryPhase
	message     *string
	apiEndpoint *string
	syncStatus  *mcpv1alpha1.SyncStatus
	apiStatus   *mcpv1alpha1.APIStatus
	conditions  map[string]metav1.Condition
}

// NewCollector creates a new status update collector for the given MCPRegistry resource.
func NewCollector(mcpRegistry *mcpv1alpha1.MCPRegistry) Collector {
	return &StatusCollector{
		mcpRegistry: mcpRegistry,
		conditions:  make(map[string]metav1.Condition),
	}
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

// SetAPIEndpoint sets the API endpoint to be updated.
func (s *StatusCollector) SetAPIEndpoint(endpoint string) {
	s.apiEndpoint = &endpoint
	s.hasChanges = true
}

// SetAPIReadyCondition adds or updates the API ready condition.
func (s *StatusCollector) SetAPIReadyCondition(reason, message string, status metav1.ConditionStatus) {
	s.conditions[mcpv1alpha1.ConditionAPIReady] = metav1.Condition{
		Type:    mcpv1alpha1.ConditionAPIReady,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
	s.hasChanges = true
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

	// Apply API endpoint change
	if s.apiEndpoint != nil {
		latestRegistry.Status.APIEndpoint = *s.apiEndpoint
	}

	// Apply sync status change
	if s.syncStatus != nil {
		latestRegistry.Status.SyncStatus = s.syncStatus

		// For backward compatibility, also populate the deprecated fields
		if s.syncStatus.LastSyncTime != nil {
			latestRegistry.Status.LastSyncTime = s.syncStatus.LastSyncTime
		}
		latestRegistry.Status.LastSyncHash = s.syncStatus.LastSyncHash
		latestRegistry.Status.ServerCount = s.syncStatus.ServerCount
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

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

// Apply applies all collected status changes in a single batch update.
func (s *StatusCollector) Apply(ctx context.Context, statusWriter client.StatusWriter) error {
	if !s.hasChanges {
		return nil
	}

	ctxLogger := log.FromContext(ctx)

	// Apply phase change
	if s.phase != nil {
		s.mcpRegistry.Status.Phase = *s.phase
	}

	// Apply message change
	if s.message != nil {
		s.mcpRegistry.Status.Message = *s.message
	}

	// Apply API endpoint change
	if s.apiEndpoint != nil {
		s.mcpRegistry.Status.APIEndpoint = *s.apiEndpoint
	}

	// Apply condition changes
	for _, condition := range s.conditions {
		meta.SetStatusCondition(&s.mcpRegistry.Status.Conditions, condition)
	}

	// Single status update
	if err := statusWriter.Update(ctx, s.mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to apply batched status update")
		return fmt.Errorf("failed to apply batched status update: %w", err)
	}

	ctxLogger.V(1).Info("Applied batched status update",
		"phase", s.phase,
		"message", s.message,
		"conditionsCount", len(s.conditions))

	return nil
}

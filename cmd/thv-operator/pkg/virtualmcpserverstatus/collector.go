// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcpserverstatus provides status management and batched updates for VirtualMCPServer resources.
package virtualmcpserverstatus

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// StatusCollector collects status changes during reconciliation
// and applies them in a single batch update at the end.
// It implements the StatusManager interface.
type StatusCollector struct {
	vmcp               *mcpv1alpha1.VirtualMCPServer
	hasChanges         bool
	phase              *mcpv1alpha1.VirtualMCPServerPhase
	message            *string
	url                *string
	observedGeneration *int64
	conditions         map[string]metav1.Condition
	discoveredBackends []mcpv1alpha1.DiscoveredBackend
}

// NewStatusManager creates a new StatusManager for the given VirtualMCPServer resource.
func NewStatusManager(vmcp *mcpv1alpha1.VirtualMCPServer) StatusManager {
	return &StatusCollector{
		vmcp:       vmcp,
		conditions: make(map[string]metav1.Condition),
	}
}

// SetPhase sets the phase to be updated.
func (s *StatusCollector) SetPhase(phase mcpv1alpha1.VirtualMCPServerPhase) {
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

// SetURL sets the service URL to be updated.
func (s *StatusCollector) SetURL(url string) {
	s.url = &url
	s.hasChanges = true
}

// SetObservedGeneration sets the observed generation to be updated.
func (s *StatusCollector) SetObservedGeneration(generation int64) {
	s.observedGeneration = &generation
	s.hasChanges = true
}

// SetGroupRefValidatedCondition sets the GroupRef validation condition.
func (s *StatusCollector) SetGroupRefValidatedCondition(reason, message string, status metav1.ConditionStatus) {
	s.SetCondition(mcpv1alpha1.ConditionTypeVirtualMCPServerGroupRefValidated, reason, message, status)
}

// SetCompositeToolRefsValidatedCondition sets the CompositeToolRefs validation condition.
func (s *StatusCollector) SetCompositeToolRefsValidatedCondition(reason, message string, status metav1.ConditionStatus) {
	s.SetCondition(mcpv1alpha1.ConditionTypeCompositeToolRefsValidated, reason, message, status)
}

// SetAuthConfiguredCondition sets the AuthConfigured condition.
func (s *StatusCollector) SetAuthConfiguredCondition(reason, message string, status metav1.ConditionStatus) {
	s.SetCondition(mcpv1alpha1.ConditionTypeAuthConfigured, reason, message, status)
}

// SetAuthConfigCondition sets a specific auth config condition with dynamic type.
// This allows setting granular conditions for individual auth config failures.
func (s *StatusCollector) SetAuthConfigCondition(conditionType, reason, message string, status metav1.ConditionStatus) {
	s.SetCondition(conditionType, reason, message, status)
}

// RemoveConditionsWithPrefix removes all conditions whose type starts with the given prefix,
// except for those in the exclude list. This is tracked as a change and will be applied
// during UpdateStatus.
func (s *StatusCollector) RemoveConditionsWithPrefix(prefix string, exclude []string) {
	// Validate prefix to prevent removing all conditions
	if prefix == "" {
		return
	}

	// Build exclude map for quick lookup
	excludeMap := make(map[string]bool)
	for _, condType := range exclude {
		excludeMap[condType] = true
	}

	// Mark conditions for removal by storing a condition with empty status
	// The UpdateStatus method will handle the actual removal
	for _, existingCondition := range s.vmcp.Status.Conditions {
		if strings.HasPrefix(existingCondition.Type, prefix) && !excludeMap[existingCondition.Type] {
			// Store a marker condition with empty status to indicate removal
			s.conditions[existingCondition.Type] = metav1.Condition{
				Type:   existingCondition.Type,
				Status: "", // Empty status indicates removal
			}
			s.hasChanges = true
		}
	}
}

// SetReadyCondition sets the Ready condition.
func (s *StatusCollector) SetReadyCondition(reason, message string, status metav1.ConditionStatus) {
	s.SetCondition(mcpv1alpha1.ConditionTypeVirtualMCPServerReady, reason, message, status)
}

// SetDiscoveredBackends sets the discovered backends list to be updated.
func (s *StatusCollector) SetDiscoveredBackends(backends []mcpv1alpha1.DiscoveredBackend) {
	s.discoveredBackends = backends
	s.hasChanges = true
}

// UpdateStatus applies all collected status changes in a single batch update.
// Expects vmcpStatus to be freshly fetched from the cluster to ensure the update operates on the latest resource version.
func (s *StatusCollector) UpdateStatus(ctx context.Context, vmcpStatus *mcpv1alpha1.VirtualMCPServerStatus) bool {
	ctxLogger := log.FromContext(ctx)

	if s.hasChanges {
		// Apply phase change
		if s.phase != nil {
			vmcpStatus.Phase = *s.phase
		}

		// Apply message change
		if s.message != nil {
			vmcpStatus.Message = *s.message
		}

		// Apply URL change
		if s.url != nil {
			vmcpStatus.URL = *s.url
		}

		// Apply observed generation change
		if s.observedGeneration != nil {
			vmcpStatus.ObservedGeneration = *s.observedGeneration
		}

		// Apply condition changes
		for _, condition := range s.conditions {
			if condition.Status == "" {
				// Empty status indicates removal
				meta.RemoveStatusCondition(&vmcpStatus.Conditions, condition.Type)
			} else {
				meta.SetStatusCondition(&vmcpStatus.Conditions, condition)
			}
		}

		// Apply discovered backends change
		if s.discoveredBackends != nil {
			vmcpStatus.DiscoveredBackends = s.discoveredBackends
			// BackendCount represents the number of ready backends
			readyCount := 0
			for _, backend := range s.discoveredBackends {
				if backend.Status == mcpv1alpha1.BackendStatusReady {
					readyCount++
				}
			}
			vmcpStatus.BackendCount = readyCount
		}

		ctxLogger.V(1).Info("Batched status update applied",
			"phase", s.phase,
			"message", s.message,
			"conditionsCount", len(s.conditions),
			"discoveredBackendsCount", len(s.discoveredBackends))
		return true
	}
	ctxLogger.V(1).Info("No batched status update needed")
	return false
}

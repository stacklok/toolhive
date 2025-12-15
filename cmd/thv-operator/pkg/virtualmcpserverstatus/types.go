// Package virtualmcpserverstatus provides status management for VirtualMCPServer resources.
package virtualmcpserverstatus

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

//go:generate mockgen -destination=mocks/mock_collector.go -package=mocks -source=types.go StatusManager

// StatusManager orchestrates all status updates for VirtualMCPServer resources.
// It collects status changes during reconciliation and applies them in a single batch update.
type StatusManager interface {
	// SetPhase sets the VirtualMCPServer phase
	SetPhase(phase mcpv1alpha1.VirtualMCPServerPhase)

	// SetMessage sets the status message
	SetMessage(message string)

	// SetCondition sets a condition with the specified type, reason, message, and status
	SetCondition(conditionType, reason, message string, status metav1.ConditionStatus)

	// SetURL sets the service URL
	SetURL(url string)

	// SetObservedGeneration sets the observed generation
	SetObservedGeneration(generation int64)

	// SetGroupRefValidatedCondition sets the GroupRef validation condition
	SetGroupRefValidatedCondition(reason, message string, status metav1.ConditionStatus)

	// SetReadyCondition sets the Ready condition
	SetReadyCondition(reason, message string, status metav1.ConditionStatus)

	// SetAuthConfiguredCondition sets the AuthConfigured condition
	SetAuthConfiguredCondition(reason, message string, status metav1.ConditionStatus)

	// SetDiscoveredBackends sets the discovered backends list
	SetDiscoveredBackends(backends []mcpv1alpha1.DiscoveredBackend)

	// UpdateStatus applies all collected status changes in a single batch update.
	// Returns true if updates were applied, false if no changes were collected.
	UpdateStatus(ctx context.Context, vmcpStatus *mcpv1alpha1.VirtualMCPServerStatus) bool
}

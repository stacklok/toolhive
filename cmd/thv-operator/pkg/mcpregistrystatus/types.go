// Package mcpregistrystatus provides status management for MCPRegistry resources.
package mcpregistrystatus

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

//go:generate mockgen -destination=mocks/mock_collector.go -package=mocks -source=types.go Collector

// Collector defines the interface for collecting MCPRegistry status updates.
// It provides methods to collect status changes during reconciliation
// and apply them in a single batch update at the end.
type Collector interface {
	// SetAPIReadyCondition sets the API ready condition with the specified reason, message, and status
	SetAPIReadyCondition(reason, message string, status metav1.ConditionStatus)

	// SetAPIEndpoint sets the API endpoint in the status
	SetAPIEndpoint(endpoint string)

	// SetPhase sets the MCPRegistry phase in the status (overall phase)
	SetPhase(phase mcpv1alpha1.MCPRegistryPhase)

	// SetMessage sets the status message (overall message)
	SetMessage(message string)

	// SetSyncStatus sets the detailed sync status
	SetSyncStatus(
		phase mcpv1alpha1.SyncPhase, message string, attemptCount int,
		lastSyncTime *metav1.Time, lastSyncHash string, serverCount int)

	// SetAPIStatus sets the detailed API status
	SetAPIStatus(phase mcpv1alpha1.APIPhase, message string, endpoint string)

	// Apply applies all collected status changes in a single batch update
	Apply(ctx context.Context, k8sClient client.Client) error
}

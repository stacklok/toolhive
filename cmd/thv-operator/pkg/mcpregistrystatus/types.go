// Package mcpregistrystatus provides status management for MCPRegistry resources.
package mcpregistrystatus

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// Collector defines the interface for collecting MCPRegistry status updates.
// It provides methods to collect status changes during reconciliation
// and apply them in a single batch update at the end.
type Collector interface {
	// SetAPIReadyCondition sets the API ready condition with the specified reason, message, and status
	SetAPIReadyCondition(reason, message string, status metav1.ConditionStatus)

	// SetAPIEndpoint sets the API endpoint in the status
	SetAPIEndpoint(endpoint string)

	// SetPhase sets the MCPRegistry phase in the status
	SetPhase(phase mcpv1alpha1.MCPRegistryPhase)

	// SetMessage sets the status message
	SetMessage(message string)

	// Apply applies all collected status changes in a single batch update
	Apply(ctx context.Context, statusWriter client.StatusWriter) error
}

// Package mcpregistrystatus provides status management for MCPRegistry resources.
package mcpregistrystatus

import (
	"fmt"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// DefaultStatusDeriver implements the StatusDeriver interface
type DefaultStatusDeriver struct{}

// NewDefaultStatusDeriver creates a new DefaultStatusDeriver
func NewDefaultStatusDeriver() StatusDeriver {
	return &DefaultStatusDeriver{}
}

// DeriveOverallStatus derives the overall MCPRegistry phase and message from component statuses
func (*DefaultStatusDeriver) DeriveOverallStatus(
	syncStatus *mcpv1alpha1.SyncStatus, apiStatus *mcpv1alpha1.APIStatus) (mcpv1alpha1.MCPRegistryPhase, string) {
	// Handle sync failures first (highest priority)
	if syncStatus != nil && syncStatus.Phase == mcpv1alpha1.SyncPhaseFailed {
		return mcpv1alpha1.MCPRegistryPhaseFailed, fmt.Sprintf("Sync failed: %s", syncStatus.Message)
	}

	// Handle API failures
	if apiStatus != nil && apiStatus.Phase == mcpv1alpha1.APIPhaseError {
		return mcpv1alpha1.MCPRegistryPhaseFailed, fmt.Sprintf("API deployment failed: %s", apiStatus.Message)
	}

	// Handle sync in progress
	if syncStatus != nil && syncStatus.Phase == mcpv1alpha1.SyncPhaseSyncing {
		return mcpv1alpha1.MCPRegistryPhaseSyncing, "Registry data synchronization in progress"
	}

	// Check if both sync and API are ready
	syncReady := syncStatus != nil &&
		(syncStatus.Phase == mcpv1alpha1.SyncPhaseComplete)
	apiReady := apiStatus != nil && apiStatus.Phase == mcpv1alpha1.APIPhaseReady

	if syncReady && apiReady {
		return mcpv1alpha1.MCPRegistryPhaseReady, "Registry is ready and API is serving requests"
	}

	// If sync is complete but API is not ready yet
	if syncReady {
		if apiStatus != nil && apiStatus.Phase == mcpv1alpha1.APIPhaseDeploying {
			return mcpv1alpha1.MCPRegistryPhasePending, "Registry data synced, API deployment in progress"
		}
		return mcpv1alpha1.MCPRegistryPhasePending, "Registry data synced, API deployment pending"
	}

	// Default to pending for initial state or unknown combinations
	return mcpv1alpha1.MCPRegistryPhasePending, "Registry initialization in progress"
}

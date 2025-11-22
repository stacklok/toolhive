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
	apiStatus *mcpv1alpha1.APIStatus) (mcpv1alpha1.MCPRegistryPhase, string) {
	// Handle API failures
	if apiStatus != nil && apiStatus.Phase == mcpv1alpha1.APIPhaseError {
		return mcpv1alpha1.MCPRegistryPhaseFailed, fmt.Sprintf("API deployment failed: %s", apiStatus.Message)
	}

	// If API is not ready, return pending
	if apiStatus != nil && apiStatus.Phase != mcpv1alpha1.APIPhaseReady {
		return mcpv1alpha1.MCPRegistryPhasePending, "API is not ready"
	}

	// Check if API is ready
	apiReady := apiStatus != nil && apiStatus.Phase == mcpv1alpha1.APIPhaseReady

	if apiReady {
		return mcpv1alpha1.MCPRegistryPhaseReady, "Registry is ready and API is serving requests"
	}

	// Default to pending for initial state or unknown combinations
	return mcpv1alpha1.MCPRegistryPhasePending, "Registry initialization in progress"
}

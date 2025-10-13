package sources

// ToolHive Registry API response types
// Re-exported from cmd/thv-registry-api/api/v1 for convenience
// This avoids code duplication while keeping the API contract clear

import (
	registryapiv1 "github.com/stacklok/toolhive/cmd/thv-registry-api/api/v1"
)

// Type aliases for ToolHive Registry API response types
type (
	// ListServersResponse represents the servers list response from ToolHive API
	ListServersResponse = registryapiv1.ListServersResponse

	// ServerSummaryResponse represents a server summary in list responses
	ServerSummaryResponse = registryapiv1.ServerSummaryResponse

	// ServerDetailResponse represents detailed server information
	ServerDetailResponse = registryapiv1.ServerDetailResponse

	// RegistryInfoResponse represents the registry information response
	RegistryInfoResponse = registryapiv1.RegistryInfoResponse
)

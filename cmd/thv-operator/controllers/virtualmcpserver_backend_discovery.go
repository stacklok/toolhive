package controllers

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
)

// discoverBackends discovers backend MCPServers from the referenced MCPGroup
// and extracts their authentication configuration for status tracking.
//
// This implements the "Discovered backends tracking in status" requirement from
// https://github.com/stacklok/stacklok-epics/issues/158
//
// The function:
// 1. Fetches the MCPGroup referenced by VirtualMCPServer.spec.groupRef
// 2. For each server in MCPGroup.status.servers, fetches the MCPServer resource
// 3. Extracts relevant information: name, auth type, external auth config ref, URL, transport
// 4. Updates VirtualMCPServer.status.discoveredBackends with the collected information
func (r *VirtualMCPServerReconciler) discoverBackends(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	statusManager virtualmcpserverstatus.StatusManager,
) error {
	ctxLogger := log.FromContext(ctx)

	// Fetch the referenced MCPGroup
	mcpGroup := &mcpv1alpha1.MCPGroup{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      vmcp.Spec.GroupRef.Name,
		Namespace: vmcp.Namespace,
	}, mcpGroup)
	if err != nil {
		return fmt.Errorf("failed to get MCPGroup %s: %w", vmcp.Spec.GroupRef.Name, err)
	}

	// Extract backend information from each server in the group
	var discoveredBackends []mcpv1alpha1.DiscoveredBackend

	for _, serverName := range mcpGroup.Status.Servers {
		// Fetch the MCPServer resource
		mcpServer := &mcpv1alpha1.MCPServer{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      serverName,
			Namespace: vmcp.Namespace,
		}, mcpServer)
		if err != nil {
			ctxLogger.Error(err, "Failed to get MCPServer, skipping",
				"serverName", serverName,
				"mcpGroup", mcpGroup.Name)
			// Continue with other servers instead of failing completely
			continue
		}

		// Extract backend information
		backend := mcpv1alpha1.DiscoveredBackend{
			Name:          serverName,
			TransportType: string(mcpServer.Spec.Transport),
		}

		// Extract URL from status if available
		if mcpServer.Status.URL != "" {
			backend.URL = mcpServer.Status.URL
		}

		// Extract authentication configuration
		// Check for externalAuthConfigRef in the MCPServer spec
		if mcpServer.Spec.ExternalAuthConfigRef != nil {
			backend.AuthType = "external_auth_config"
			backend.ExternalAuthConfigRef = mcpServer.Spec.ExternalAuthConfigRef.Name
		} else {
			// If no external auth config, check if it's using other auth types
			// Default to "discovered" mode if not explicitly configured
			backend.AuthType = "discovered"
		}

		// Check for outgoing auth overrides in VirtualMCPServer spec
		if vmcp.Spec.OutgoingAuth != nil && vmcp.Spec.OutgoingAuth.Backends != nil {
			if backendAuth, ok := vmcp.Spec.OutgoingAuth.Backends[serverName]; ok {
				// VirtualMCPServer has an explicit override for this backend
				backend.AuthType = backendAuth.Type
				if backendAuth.ExternalAuthConfigRef != nil {
					backend.ExternalAuthConfigRef = backendAuth.ExternalAuthConfigRef.Name
				}
			}
		}

		discoveredBackends = append(discoveredBackends, backend)
	}

	// Update status with discovered backends
	statusManager.SetDiscoveredBackends(discoveredBackends)

	ctxLogger.V(1).Info("Discovered backends from MCPGroup",
		"mcpGroup", mcpGroup.Name,
		"backendCount", len(discoveredBackends))

	return nil
}

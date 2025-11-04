package controllers

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

const (
	backendStatusReady       = "ready"
	backendStatusDegraded    = "degraded"
	backendStatusUnavailable = "unavailable"
)

// backendHealthResult represents the health status of all backends
type backendHealthResult struct {
	allHealthy       bool // All backends are healthy
	someHealthy      bool // At least one backend is healthy
	totalCount       int  // Total number of backends
	unavailableCount int  // Number of unavailable backends
}

// discoverBackends discovers backend MCPServers from the MCPGroup and updates status
func (r *VirtualMCPServerReconciler) discoverBackends(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	mcpGroup *mcpv1alpha1.MCPGroup,
) error {
	ctxLogger := log.FromContext(ctx)

	// List all MCPServers in the same namespace
	// In a real implementation, we'd use the MCPGroup's selector to filter servers
	// For now, we'll look at the MCPGroup's status which lists server names
	discoveredBackends := []mcpv1alpha1.DiscoveredBackend{}

	// Get backend names from MCPGroup status
	if len(mcpGroup.Status.Servers) == 0 {
		ctxLogger.Info("No backends found in MCPGroup", "group", mcpGroup.Name)
		vmcp.Status.DiscoveredBackends = discoveredBackends
		meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeBackendsDiscovered,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonNoBackends,
			Message: fmt.Sprintf("No backends found in MCPGroup %s", mcpGroup.Name),
		})
		return nil
	}

	// Discover each backend server
	for _, serverName := range mcpGroup.Status.Servers {
		backend, err := r.discoverBackendServer(ctx, vmcp.Namespace, serverName)
		if err != nil {
			if errors.IsNotFound(err) {
				ctxLogger.Info("Backend MCPServer not found", "server", serverName)
				// Add as unavailable backend
				discoveredBackends = append(discoveredBackends, mcpv1alpha1.DiscoveredBackend{
					Name:   serverName,
					Status: backendStatusUnavailable,
				})
				continue
			}
			return fmt.Errorf("failed to discover backend %s: %w", serverName, err)
		}
		discoveredBackends = append(discoveredBackends, backend)
	}

	// Update status with discovered backends
	vmcp.Status.DiscoveredBackends = discoveredBackends

	// Calculate capabilities summary
	vmcp.Status.Capabilities = r.calculateCapabilitiesSummary(vmcp, discoveredBackends)

	// Set discovery condition
	if len(discoveredBackends) > 0 {
		meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeBackendsDiscovered,
			Status:  metav1.ConditionTrue,
			Reason:  mcpv1alpha1.ConditionReasonDiscoveryComplete,
			Message: fmt.Sprintf("Discovered %d backends from MCPGroup %s", len(discoveredBackends), mcpGroup.Name),
		})
	} else {
		meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeBackendsDiscovered,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonNoBackends,
			Message: fmt.Sprintf("No backends available in MCPGroup %s", mcpGroup.Name),
		})
	}

	return nil
}

// discoverBackendServer discovers a single backend MCPServer and returns its configuration
func (r *VirtualMCPServerReconciler) discoverBackendServer(
	ctx context.Context,
	namespace string,
	serverName string,
) (mcpv1alpha1.DiscoveredBackend, error) {
	// Fetch the MCPServer
	mcpServer := &mcpv1alpha1.MCPServer{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      serverName,
		Namespace: namespace,
	}, mcpServer)

	if err != nil {
		return mcpv1alpha1.DiscoveredBackend{}, err
	}

	// Build discovered backend
	backend := mcpv1alpha1.DiscoveredBackend{
		Name: serverName,
		URL:  mcpServer.Status.URL,
	}

	// Discover auth configuration from externalAuthConfigRef
	if mcpServer.Spec.ExternalAuthConfigRef != nil {
		backend.AuthConfigRef = mcpServer.Spec.ExternalAuthConfigRef.Name

		// Fetch the auth config to get the type using controllerutil
		authConfig, err := ctrlutil.GetExternalAuthConfigByName(ctx, r.Client, namespace, mcpServer.Spec.ExternalAuthConfigRef.Name)
		if err == nil {
			// Determine auth type from the auth config spec
			backend.AuthType = r.getAuthTypeFromConfig(authConfig)
		}
	}

	// Determine backend status based on MCPServer phase
	switch mcpServer.Status.Phase {
	case mcpv1alpha1.MCPServerPhaseRunning:
		backend.Status = backendStatusReady
	case mcpv1alpha1.MCPServerPhaseFailed:
		backend.Status = backendStatusDegraded
	case mcpv1alpha1.MCPServerPhasePending, mcpv1alpha1.MCPServerPhaseTerminating:
		backend.Status = backendStatusUnavailable
	default:
		backend.Status = backendStatusUnavailable
	}

	// Set last health check time to now
	now := metav1.Now()
	backend.LastHealthCheck = &now

	return backend, nil
}

// getAuthTypeFromConfig determines the auth type from MCPExternalAuthConfig
func (*VirtualMCPServerReconciler) getAuthTypeFromConfig(
	authConfig *mcpv1alpha1.MCPExternalAuthConfig,
) string {
	// Currently only tokenExchange is supported
	if authConfig.Spec.Type == mcpv1alpha1.ExternalAuthTypeTokenExchange {
		return "token_exchange"
	}
	return authConfig.Spec.Type
}

// calculateCapabilitiesSummary calculates aggregated capabilities from discovered backends.
//
// Current Implementation Status:
// This function returns nil until proper capability discovery is implemented.
// Returning nil is preferable to placeholder/estimated data which can confuse operators.
//
// Future Implementation Requirements:
// 1. Query each backend's capabilities via MCP protocol
// 2. Apply tool filtering from Aggregation.Tools
// 3. Apply conflict resolution strategy (prefix, priority, deduplicate)
// 4. Count the final aggregated capabilities after conflict resolution
// 5. Handle backend unavailability gracefully
//
// For now, we return nil to indicate capabilities are unknown rather than showing
// misleading placeholder data. Users can check backend status and count instead.
func (*VirtualMCPServerReconciler) calculateCapabilitiesSummary(
	vmcp *mcpv1alpha1.VirtualMCPServer,
	_ []mcpv1alpha1.DiscoveredBackend,
) *mcpv1alpha1.CapabilitiesSummary {
	// CompositeToolCount can be calculated immediately from the spec
	// as it doesn't require querying backends
	if len(vmcp.Spec.CompositeTools) > 0 || len(vmcp.Spec.CompositeToolRefs) > 0 {
		return &mcpv1alpha1.CapabilitiesSummary{
			ToolCount:          0, // Unknown until capability discovery implemented
			ResourceCount:      0, // Unknown until capability discovery implemented
			PromptCount:        0, // Unknown until capability discovery implemented
			CompositeToolCount: len(vmcp.Spec.CompositeTools) + len(vmcp.Spec.CompositeToolRefs),
		}
	}

	// Return nil to indicate capabilities are unknown
	// This is clearer than returning zeros which could be confused with "no capabilities"
	return nil
}

// checkBackendHealth checks the health of all discovered backends
func (*VirtualMCPServerReconciler) checkBackendHealth(
	_ context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) backendHealthResult {
	result := backendHealthResult{
		totalCount: len(vmcp.Status.DiscoveredBackends),
	}

	if result.totalCount == 0 {
		return result
	}

	healthyCount := 0
	for _, backend := range vmcp.Status.DiscoveredBackends {
		if backend.Status == backendStatusReady {
			healthyCount++
		} else {
			result.unavailableCount++
		}
	}

	result.allHealthy = healthyCount == result.totalCount
	result.someHealthy = healthyCount > 0

	return result
}

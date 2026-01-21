// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package workloads

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/k8s"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/converters"
)

// k8sDiscoverer is a direct implementation of Discoverer for Kubernetes workloads.
// It uses the Kubernetes client directly to query MCPServer CRDs instead of going through k8s.BackendWatcher.
type k8sDiscoverer struct {
	k8sClient client.Client
	namespace string
}

// NewK8SDiscoverer creates a new Kubernetes workload discoverer that directly uses
// the Kubernetes client to discover MCPServer CRDs.
// If namespace is empty, it will detect the namespace using k8s.GetCurrentNamespace().
func NewK8SDiscoverer(namespace ...string) (Discoverer, error) {
	// Create a scheme for controller-runtime client
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add client-go scheme: %w", err)
	}
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add MCP v1alpha1 scheme: %w", err)
	}

	// Create controller-runtime client
	k8sClient, err := k8s.NewControllerRuntimeClient(scheme)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Use provided namespace or detect it
	var ns string
	if len(namespace) > 0 && namespace[0] != "" {
		ns = namespace[0]
	} else {
		ns = k8s.GetCurrentNamespace()
	}

	return NewK8SDiscovererWithClient(k8sClient, ns), nil
}

// NewK8SDiscovererWithClient creates a new Kubernetes workload discoverer with a provided client.
// This is useful for testing with fake clients.
func NewK8SDiscovererWithClient(k8sClient client.Client, namespace string) Discoverer {
	return &k8sDiscoverer{
		k8sClient: k8sClient,
		namespace: namespace,
	}
}

// ListWorkloadsInGroup returns all workloads that belong to the specified group.
// This includes both MCPServers and MCPRemoteProxies.
func (d *k8sDiscoverer) ListWorkloadsInGroup(ctx context.Context, groupName string) ([]TypedWorkload, error) {
	var groupWorkloads []TypedWorkload

	// List MCPServers in the group
	mcpServerList := &mcpv1alpha1.MCPServerList{}
	listOpts := []client.ListOption{
		client.InNamespace(d.namespace),
	}

	if err := d.k8sClient.List(ctx, mcpServerList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list MCPServers: %w", err)
	}

	for i := range mcpServerList.Items {
		mcpServer := &mcpServerList.Items[i]
		if mcpServer.Spec.GroupRef == groupName {
			groupWorkloads = append(groupWorkloads, TypedWorkload{
				Name: mcpServer.Name,
				Type: WorkloadTypeMCPServer,
			})
		}
	}

	// List MCPRemoteProxies in the group
	mcpRemoteProxyList := &mcpv1alpha1.MCPRemoteProxyList{}
	if err := d.k8sClient.List(ctx, mcpRemoteProxyList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies: %w", err)
	}

	for i := range mcpRemoteProxyList.Items {
		mcpRemoteProxy := &mcpRemoteProxyList.Items[i]
		if mcpRemoteProxy.Spec.GroupRef == groupName {
			groupWorkloads = append(groupWorkloads, TypedWorkload{
				Name: mcpRemoteProxy.Name,
				Type: WorkloadTypeMCPRemoteProxy,
			})
		}
	}

	return groupWorkloads, nil
}

// GetWorkloadAsVMCPBackend retrieves workload details and converts it to a vmcp.Backend.
// The workload type determines whether to fetch an MCPServer or MCPRemoteProxy.
func (d *k8sDiscoverer) GetWorkloadAsVMCPBackend(ctx context.Context, workload TypedWorkload) (*vmcp.Backend, error) {
	switch workload.Type {
	case WorkloadTypeMCPRemoteProxy:
		return d.getMCPRemoteProxyAsBackend(ctx, workload.Name)
	case WorkloadTypeMCPServer:
		return d.getMCPServerAsBackend(ctx, workload.Name)
	default:
		// Default: treat as MCPServer for backwards compatibility
		return d.getMCPServerAsBackend(ctx, workload.Name)
	}
}

// getMCPServerAsBackend retrieves an MCPServer and converts it to a vmcp.Backend.
func (d *k8sDiscoverer) getMCPServerAsBackend(ctx context.Context, workloadName string) (*vmcp.Backend, error) {
	mcpServer := &mcpv1alpha1.MCPServer{}
	key := client.ObjectKey{Name: workloadName, Namespace: d.namespace}
	if err := d.k8sClient.Get(ctx, key, mcpServer); err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("MCPServer %s not found", workloadName)
		}
		return nil, fmt.Errorf("failed to get MCPServer: %w", err)
	}

	// Convert MCPServer to Backend
	backend := d.mcpServerToBackend(ctx, mcpServer)

	// If auth discovery failed, mcpServerToBackend returns nil
	if backend == nil {
		logger.Warnf("Skipping workload %s due to auth discovery failure", workloadName)
		return nil, nil
	}

	// Skip workloads without a URL (not accessible)
	if backend.BaseURL == "" {
		logger.Debugf("Skipping workload %s without URL", workloadName)
		return nil, nil
	}

	return backend, nil
}

// getMCPRemoteProxyAsBackend retrieves an MCPRemoteProxy and converts it to a vmcp.Backend.
func (d *k8sDiscoverer) getMCPRemoteProxyAsBackend(ctx context.Context, proxyName string) (*vmcp.Backend, error) {
	mcpRemoteProxy := &mcpv1alpha1.MCPRemoteProxy{}
	key := client.ObjectKey{Name: proxyName, Namespace: d.namespace}
	if err := d.k8sClient.Get(ctx, key, mcpRemoteProxy); err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("MCPRemoteProxy %s not found", proxyName)
		}
		return nil, fmt.Errorf("failed to get MCPRemoteProxy: %w", err)
	}

	// Convert MCPRemoteProxy to Backend
	backend := d.mcpRemoteProxyToBackend(ctx, mcpRemoteProxy)

	// If conversion failed, return nil
	if backend == nil {
		logger.Warnf("Skipping remote proxy %s due to conversion failure", proxyName)
		return nil, nil
	}

	// Skip workloads without a URL (not accessible)
	if backend.BaseURL == "" {
		logger.Debugf("Skipping remote proxy %s without URL", proxyName)
		return nil, nil
	}

	return backend, nil
}

// mcpServerToBackend converts an MCPServer CRD to a vmcp.Backend.
// If the MCPServer has an ExternalAuthConfigRef, it will be fetched and converted to auth strategy metadata.
// Auth discovery errors are logged but do not fail backend creation.
func (d *k8sDiscoverer) mcpServerToBackend(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) *vmcp.Backend {
	// Parse transport type
	transportType, err := transporttypes.ParseTransportType(mcpServer.Spec.Transport)
	if err != nil {
		logger.Warnf("Failed to parse transport type %s for MCPServer %s: %v", mcpServer.Spec.Transport, mcpServer.Name, err)
		transportType = transporttypes.TransportTypeStreamableHTTP
	}

	// Calculate effective proxy mode
	effectiveProxyMode := getEffectiveProxyMode(transportType, mcpServer.Spec.ProxyMode)

	// Generate URL from status or reconstruct from spec
	url := mcpServer.Status.URL
	if url == "" {
		// Use ProxyPort (not McpPort) because it's the externally accessible port
		// that the egress proxy listens on. This is what vMCP connects to.
		// The McpPort is only for internal container-to-container communication.
		port := int(mcpServer.Spec.ProxyPort)
		if port == 0 {
			port = int(mcpServer.Spec.Port) // Fallback to deprecated Port field
		}
		if port > 0 {
			url = transport.GenerateMCPServerURL(
				mcpServer.Spec.Transport,
				mcpServer.Spec.ProxyMode,
				transport.LocalhostIPv4,
				port,
				mcpServer.Name,
				"",
			)
		}
	}

	// Map workload phase to backend health status
	healthStatus := mapK8SWorkloadPhaseToHealth(mcpServer.Status.Phase)

	// Use ProxyMode instead of TransportType to reflect how ToolHive is exposing the workload.
	// For stdio MCP servers, ToolHive proxies them via SSE or streamable-http.
	// ProxyMode tells us which transport the vmcp client should use.
	transportTypeStr := effectiveProxyMode
	if transportTypeStr == "" {
		// Fallback to TransportType if ProxyMode is not set (for direct transports)
		transportTypeStr = transportType.String()
		if transportTypeStr == "" {
			transportTypeStr = "unknown"
		}
	}

	// Extract user labels from annotations (Kubernetes doesn't have container labels like Docker)
	userLabels := make(map[string]string)
	if mcpServer.Annotations != nil {
		// Filter out standard Kubernetes annotations
		for key, value := range mcpServer.Annotations {
			if !isStandardK8sAnnotation(key) {
				userLabels[key] = value
			}
		}
	}

	backend := &vmcp.Backend{
		ID:            mcpServer.Name,
		Name:          mcpServer.Name,
		BaseURL:       url,
		TransportType: transportTypeStr,
		HealthStatus:  healthStatus,
		Metadata:      make(map[string]string),
	}

	// Copy user labels to metadata first
	maps.Copy(backend.Metadata, userLabels)

	// Set system metadata (these override user labels to prevent conflicts)
	backend.Metadata["tool_type"] = "mcp"
	backend.Metadata["workload_type"] = "mcp_server"
	backend.Metadata["workload_status"] = string(mcpServer.Status.Phase)
	if mcpServer.Namespace != "" {
		backend.Metadata["namespace"] = mcpServer.Namespace
	}

	// Discover and populate authentication configuration from MCPServer
	if err := d.discoverAuthConfig(ctx, mcpServer, backend); err != nil {
		// If auth discovery fails, we must fail - don't silently allow unauthorized access
		// This is a security-critical operation: if auth is configured but fails to load,
		// we should not proceed without it
		logger.Errorf("Failed to discover auth config for MCPServer %s: %v", mcpServer.Name, err)
		return nil
	}

	return backend
}

// discoverAuthConfig discovers and populates authentication configuration from the MCPServer's ExternalAuthConfigRef.
// This enables runtime discovery of backend authentication requirements.
//
// Return behavior:
//   - Returns nil error if ExternalAuthConfigRef is nil (no auth config) - this is expected behavior
//   - Returns nil error if auth config is discovered and successfully populated into backend
//   - Returns error if auth config exists but discovery/resolution fails (e.g., missing secret, invalid config)
func (d *k8sDiscoverer) discoverAuthConfig(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, backend *vmcp.Backend) error {
	return d.discoverAuthConfigFromRef(
		ctx,
		mcpServer.Spec.ExternalAuthConfigRef,
		mcpServer.Namespace,
		mcpServer.Name,
		"MCPServer",
		backend,
	)
}

// discoverAuthConfigFromRef is a helper that discovers and populates authentication configuration
// from an ExternalAuthConfigRef. This consolidates auth discovery logic for both MCPServer and MCPRemoteProxy.
//
// Return behavior:
//   - Returns nil error if authConfigRef is nil (no auth config) - this is expected behavior
//   - Returns nil error if auth config is discovered and successfully populated into backend
//   - Returns error if auth config exists but discovery/resolution fails (e.g., missing secret, invalid config)
func (d *k8sDiscoverer) discoverAuthConfigFromRef(
	ctx context.Context,
	authConfigRef *mcpv1alpha1.ExternalAuthConfigRef,
	namespace string,
	resourceName string,
	resourceKind string,
	backend *vmcp.Backend,
) error {
	// Discover and resolve auth using the converters package
	strategy, err := converters.DiscoverAndResolveAuth(
		ctx,
		authConfigRef,
		namespace,
		d.k8sClient,
	)
	if err != nil {
		return err
	}

	// If no auth was discovered, nothing to populate
	if strategy == nil {
		logger.Debugf("%s %s has no ExternalAuthConfigRef, no auth config to discover", resourceKind, resourceName)
		return nil
	}

	// Populate backend auth fields with typed strategy
	backend.AuthConfig = strategy

	logger.Debugf("Discovered auth config for %s %s: strategy=%s", resourceKind, resourceName, strategy.Type)
	return nil
}

// mapK8SWorkloadPhaseToHealth converts a MCPServerPhase to a backend health status.
func mapK8SWorkloadPhaseToHealth(phase mcpv1alpha1.MCPServerPhase) vmcp.BackendHealthStatus {
	switch phase {
	case mcpv1alpha1.MCPServerPhaseRunning:
		return vmcp.BackendHealthy
	case mcpv1alpha1.MCPServerPhaseFailed:
		return vmcp.BackendUnhealthy
	case mcpv1alpha1.MCPServerPhaseTerminating:
		return vmcp.BackendUnhealthy
	case mcpv1alpha1.MCPServerPhasePending:
		return vmcp.BackendUnknown
	default:
		return vmcp.BackendUnknown
	}
}

// mapMCPRemoteProxyPhaseToHealth converts a MCPRemoteProxyPhase to a backend health status.
func mapMCPRemoteProxyPhaseToHealth(phase mcpv1alpha1.MCPRemoteProxyPhase) vmcp.BackendHealthStatus {
	switch phase {
	case mcpv1alpha1.MCPRemoteProxyPhaseReady:
		return vmcp.BackendHealthy
	case mcpv1alpha1.MCPRemoteProxyPhaseFailed:
		return vmcp.BackendUnhealthy
	case mcpv1alpha1.MCPRemoteProxyPhaseTerminating:
		return vmcp.BackendUnhealthy
	case mcpv1alpha1.MCPRemoteProxyPhasePending:
		return vmcp.BackendUnknown
	default:
		return vmcp.BackendUnknown
	}
}

// mcpRemoteProxyToBackend converts an MCPRemoteProxy CRD to a vmcp.Backend.
// If the MCPRemoteProxy has an ExternalAuthConfigRef, it will be fetched and converted to auth strategy metadata.
func (d *k8sDiscoverer) mcpRemoteProxyToBackend(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) *vmcp.Backend {
	// Parse transport type from proxy spec
	transportType, err := transporttypes.ParseTransportType(proxy.Spec.Transport)
	if err != nil {
		logger.Warnf("Failed to parse transport type %s for MCPRemoteProxy %s: %v", proxy.Spec.Transport, proxy.Name, err)
		transportType = transporttypes.TransportTypeStreamableHTTP
	}

	// Use the status URL if available, otherwise reconstruct from service name
	url := proxy.Status.URL
	if url == "" {
		port := int(proxy.GetProxyPort())
		if port > 0 {
			url = transport.GenerateMCPServerURL(proxy.Spec.Transport, "", transport.LocalhostIPv4, port, proxy.Name, "")
		}
	}

	// Map proxy phase to backend health status
	healthStatus := mapMCPRemoteProxyPhaseToHealth(proxy.Status.Phase)

	// Transport type string
	transportTypeStr := transportType.String()
	if transportTypeStr == "" {
		transportTypeStr = "unknown"
	}

	// Extract user labels from annotations
	userLabels := make(map[string]string)
	if proxy.Annotations != nil {
		for key, value := range proxy.Annotations {
			if !isStandardK8sAnnotation(key) {
				userLabels[key] = value
			}
		}
	}

	backend := &vmcp.Backend{
		ID:            proxy.Name,
		Name:          proxy.Name,
		BaseURL:       url,
		TransportType: transportTypeStr,
		HealthStatus:  healthStatus,
		Metadata:      make(map[string]string),
	}

	// Copy user labels to metadata first
	for k, v := range userLabels {
		backend.Metadata[k] = v
	}

	// Set system metadata (these override user labels to prevent conflicts)
	backend.Metadata["tool_type"] = "mcp"
	backend.Metadata["workload_type"] = "remote_proxy"
	backend.Metadata["workload_status"] = string(proxy.Status.Phase)
	backend.Metadata["remote_url"] = proxy.Spec.RemoteURL
	if proxy.Namespace != "" {
		backend.Metadata["namespace"] = proxy.Namespace
	}

	// Discover and populate authentication configuration from MCPRemoteProxy
	if err := d.discoverRemoteProxyAuthConfig(ctx, proxy, backend); err != nil {
		// If auth discovery fails, we must fail - don't silently allow unauthorized access
		logger.Errorf("Failed to discover auth config for MCPRemoteProxy %s: %v", proxy.Name, err)
		return nil
	}

	return backend
}

// discoverRemoteProxyAuthConfig discovers and populates authentication configuration
// from the MCPRemoteProxy's ExternalAuthConfigRef.
func (d *k8sDiscoverer) discoverRemoteProxyAuthConfig(
	ctx context.Context,
	proxy *mcpv1alpha1.MCPRemoteProxy,
	backend *vmcp.Backend,
) error {
	return d.discoverAuthConfigFromRef(
		ctx,
		proxy.Spec.ExternalAuthConfigRef,
		proxy.Namespace,
		proxy.Name,
		"MCPRemoteProxy",
		backend,
	)
}

// getEffectiveProxyMode calculates the effective proxy mode based on transport type and configured proxy mode.
// This replicates the logic from pkg/workloads/types/proxy_mode.go
func getEffectiveProxyMode(transportType transporttypes.TransportType, configuredProxyMode string) string {
	// If proxy mode is explicitly configured, use it
	if configuredProxyMode != "" {
		return configuredProxyMode
	}

	// For stdio transports, default to streamable-http proxy mode
	if transportType == transporttypes.TransportTypeStdio {
		return transporttypes.ProxyModeStreamableHTTP.String()
	}

	// For direct transports (SSE, streamable-http), use the transport type as proxy mode
	return transportType.String()
}

// isStandardK8sAnnotation checks if an annotation key is a standard Kubernetes annotation.
func isStandardK8sAnnotation(key string) bool {
	// Common Kubernetes annotation prefixes
	standardPrefixes := []string{
		"kubectl.kubernetes.io/",
		"kubernetes.io/",
		"deployment.kubernetes.io/",
		"k8s.io/",
	}

	for _, prefix := range standardPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

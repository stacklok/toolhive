// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package workloads

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/k8s"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/converters"
	"github.com/stacklok/toolhive/pkg/workloads/types"
)

const (
	metadataToolTypeMCP       = "mcp"
	transportTypeUnknown      = "unknown"
	metadataKeyToolType       = "tool_type"
	metadataKeyWorkloadType   = "workload_type"
	metadataKeyWorkloadStatus = "workload_status"
	metadataKeyNamespace      = "namespace"
	metadataKeyRemoteURL      = "remote_url"
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
	if err := mcpv1beta1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add MCP v1beta1 scheme: %w", err)
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
	mcpServerList := &mcpv1beta1.MCPServerList{}
	listOpts := []client.ListOption{
		client.InNamespace(d.namespace),
	}

	if err := d.k8sClient.List(ctx, mcpServerList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list MCPServers: %w", err)
	}

	for i := range mcpServerList.Items {
		mcpServer := &mcpServerList.Items[i]
		if mcpServer.Spec.GroupRef.GetName() == groupName {
			groupWorkloads = append(groupWorkloads, TypedWorkload{
				Name: mcpServer.Name,
				Type: WorkloadTypeMCPServer,
			})
		}
	}

	// List MCPRemoteProxies in the group
	mcpRemoteProxyList := &mcpv1beta1.MCPRemoteProxyList{}
	if err := d.k8sClient.List(ctx, mcpRemoteProxyList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies: %w", err)
	}

	for i := range mcpRemoteProxyList.Items {
		mcpRemoteProxy := &mcpRemoteProxyList.Items[i]
		if mcpRemoteProxy.Spec.GroupRef.GetName() == groupName {
			groupWorkloads = append(groupWorkloads, TypedWorkload{
				Name: mcpRemoteProxy.Name,
				Type: WorkloadTypeMCPRemoteProxy,
			})
		}
	}

	// List MCPServerEntries in the group
	mcpServerEntryList := &mcpv1beta1.MCPServerEntryList{}
	if err := d.k8sClient.List(ctx, mcpServerEntryList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list MCPServerEntries: %w", err)
	}

	for i := range mcpServerEntryList.Items {
		mcpServerEntry := &mcpServerEntryList.Items[i]
		if mcpServerEntry.Spec.GroupRef.GetName() == groupName {
			groupWorkloads = append(groupWorkloads, TypedWorkload{
				Name: mcpServerEntry.Name,
				Type: WorkloadTypeMCPServerEntry,
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
	case WorkloadTypeMCPServerEntry:
		return d.getMCPServerEntryAsBackend(ctx, workload.Name)
	case WorkloadTypeMCPServer:
		return d.getMCPServerAsBackend(ctx, workload.Name)
	default:
		// Default: treat as MCPServer for backwards compatibility
		return d.getMCPServerAsBackend(ctx, workload.Name)
	}
}

// getMCPServerAsBackend retrieves an MCPServer and converts it to a vmcp.Backend.
func (d *k8sDiscoverer) getMCPServerAsBackend(ctx context.Context, workloadName string) (*vmcp.Backend, error) {
	mcpServer := &mcpv1beta1.MCPServer{}
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
		slog.Warn("skipping workload due to auth discovery failure", "workload", workloadName)
		return nil, nil
	}

	// Skip workloads without a URL (not accessible)
	if backend.BaseURL == "" {
		slog.Debug("skipping workload without URL", "workload", workloadName)
		return nil, nil
	}

	return backend, nil
}

// getMCPRemoteProxyAsBackend retrieves an MCPRemoteProxy and converts it to a vmcp.Backend.
func (d *k8sDiscoverer) getMCPRemoteProxyAsBackend(ctx context.Context, proxyName string) (*vmcp.Backend, error) {
	mcpRemoteProxy := &mcpv1beta1.MCPRemoteProxy{}
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
		slog.Warn("skipping remote proxy due to conversion failure", "proxy", proxyName)
		return nil, nil
	}

	// Skip workloads without a URL (not accessible)
	if backend.BaseURL == "" {
		slog.Debug("skipping remote proxy without URL", "proxy", proxyName)
		return nil, nil
	}

	return backend, nil
}

// mcpServerToBackend converts an MCPServer CRD to a vmcp.Backend.
// If the MCPServer has an ExternalAuthConfigRef, it will be fetched and converted to auth strategy metadata.
// Auth discovery errors are logged but do not fail backend creation.
func (d *k8sDiscoverer) mcpServerToBackend(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) *vmcp.Backend {
	// Parse transport type
	transportType, err := transporttypes.ParseTransportType(mcpServer.Spec.Transport)
	if err != nil {
		slog.Warn("failed to parse transport type for MCPServer",
			"transport", mcpServer.Spec.Transport,
			"server", mcpServer.Name,
			"error", err)
		transportType = transporttypes.TransportTypeStreamableHTTP
	}

	// Calculate effective proxy mode
	effectiveProxyMode := types.GetEffectiveProxyMode(transportType, mcpServer.Spec.ProxyMode)

	// Use the URL from status, which is set by the MCPServer controller after
	// creating the K8s Service. Do NOT fall back to localhost — in K8s mode,
	// 127.0.0.1 inside the vMCP pod points to the vMCP itself (e.g. its metrics
	// server on port 8080), not the backend. If Status.URL is empty, the backend
	// will be skipped and added later by the reconciler once the URL is set.
	serverURL := mcpServer.Status.URL

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
			transportTypeStr = transportTypeUnknown
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
		BaseURL:       serverURL,
		TransportType: transportTypeStr,
		HealthStatus:  healthStatus,
		Metadata:      make(map[string]string),
	}

	// Copy user labels to metadata first
	maps.Copy(backend.Metadata, userLabels)

	// Set system metadata (these override user labels to prevent conflicts)
	backend.Metadata[metadataKeyToolType] = metadataToolTypeMCP
	backend.Metadata[metadataKeyWorkloadType] = "mcp_server"
	backend.Metadata[metadataKeyWorkloadStatus] = string(mcpServer.Status.Phase)
	if mcpServer.Namespace != "" {
		backend.Metadata[metadataKeyNamespace] = mcpServer.Namespace
	}

	// Discover and populate authentication configuration from MCPServer
	if err := d.discoverAuthConfig(ctx, mcpServer, backend); err != nil {
		// If auth discovery fails, we must fail - don't silently allow unauthorized access
		// This is a security-critical operation: if auth is configured but fails to load,
		// we should not proceed without it
		slog.Error("failed to discover auth config for MCPServer", "server", mcpServer.Name, "error", err)
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
func (d *k8sDiscoverer) discoverAuthConfig(ctx context.Context, mcpServer *mcpv1beta1.MCPServer, backend *vmcp.Backend) error {
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
	authConfigRef *mcpv1beta1.ExternalAuthConfigRef,
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
		slog.Debug("no ExternalAuthConfigRef, no auth config to discover", "kind", resourceKind, "name", resourceName)
		return nil
	}

	// Populate backend auth fields with typed strategy
	backend.AuthConfig = strategy
	// Also store the reference to the MCPExternalAuthConfig resource name
	// This is used for status reporting and debugging
	backend.AuthConfigRef = authConfigRef.Name

	slog.Debug("discovered auth config",
		"kind", resourceKind,
		"name", resourceName,
		"strategy", strategy.Type,
		"config_ref", authConfigRef.Name)
	return nil
}

// mapK8SWorkloadPhaseToHealth converts a MCPServerPhase to a backend health status.
func mapK8SWorkloadPhaseToHealth(phase mcpv1beta1.MCPServerPhase) vmcp.BackendHealthStatus {
	switch phase {
	case mcpv1beta1.MCPServerPhaseReady:
		return vmcp.BackendHealthy
	case mcpv1beta1.MCPServerPhaseFailed:
		return vmcp.BackendUnhealthy
	case mcpv1beta1.MCPServerPhaseTerminating:
		return vmcp.BackendUnhealthy
	case mcpv1beta1.MCPServerPhaseStopped:
		return vmcp.BackendUnhealthy
	case mcpv1beta1.MCPServerPhasePending:
		return vmcp.BackendUnknown
	default:
		return vmcp.BackendUnknown
	}
}

// mapMCPRemoteProxyPhaseToHealth converts a MCPRemoteProxyPhase to a backend health status.
func mapMCPRemoteProxyPhaseToHealth(phase mcpv1beta1.MCPRemoteProxyPhase) vmcp.BackendHealthStatus {
	switch phase {
	case mcpv1beta1.MCPRemoteProxyPhaseReady:
		return vmcp.BackendHealthy
	case mcpv1beta1.MCPRemoteProxyPhaseFailed:
		return vmcp.BackendUnhealthy
	case mcpv1beta1.MCPRemoteProxyPhaseTerminating:
		return vmcp.BackendUnhealthy
	case mcpv1beta1.MCPRemoteProxyPhasePending:
		return vmcp.BackendUnknown
	default:
		return vmcp.BackendUnknown
	}
}

// mcpRemoteProxyToBackend converts an MCPRemoteProxy CRD to a vmcp.Backend.
// If the MCPRemoteProxy has an ExternalAuthConfigRef, it will be fetched and converted to auth strategy metadata.
func (d *k8sDiscoverer) mcpRemoteProxyToBackend(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) *vmcp.Backend {
	// Parse transport type from proxy spec
	transportType, err := transporttypes.ParseTransportType(proxy.Spec.Transport)
	if err != nil {
		slog.Warn("failed to parse transport type for MCPRemoteProxy",
			"transport", proxy.Spec.Transport,
			"proxy", proxy.Name,
			"error", err)
		transportType = transporttypes.TransportTypeStreamableHTTP
	}

	// Use the URL from status, which is set by the controller after creating the
	// K8s Service. Do NOT fall back to localhost — see mcpServerToBackend comment.
	proxyURL := proxy.Status.URL

	// Map proxy phase to backend health status
	healthStatus := mapMCPRemoteProxyPhaseToHealth(proxy.Status.Phase)

	// Transport type string
	transportTypeStr := transportType.String()
	if transportTypeStr == "" {
		transportTypeStr = transportTypeUnknown
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
		BaseURL:       proxyURL,
		TransportType: transportTypeStr,
		HealthStatus:  healthStatus,
		Metadata:      make(map[string]string),
	}

	// Copy user labels to metadata first
	maps.Copy(backend.Metadata, userLabels)

	// Set system metadata (these override user labels to prevent conflicts)
	backend.Metadata[metadataKeyToolType] = metadataToolTypeMCP
	backend.Metadata[metadataKeyWorkloadType] = "remote_proxy"
	backend.Metadata[metadataKeyWorkloadStatus] = string(proxy.Status.Phase)
	backend.Metadata[metadataKeyRemoteURL] = proxy.Spec.RemoteURL
	if proxy.Namespace != "" {
		backend.Metadata[metadataKeyNamespace] = proxy.Namespace
	}

	// Discover and populate authentication configuration from MCPRemoteProxy
	if err := d.discoverRemoteProxyAuthConfig(ctx, proxy, backend); err != nil {
		// If auth discovery fails, we must fail - don't silently allow unauthorized access
		slog.Error("failed to discover auth config for MCPRemoteProxy", "proxy", proxy.Name, "error", err)
		return nil
	}

	return backend
}

// getMCPServerEntryAsBackend retrieves an MCPServerEntry and converts it to a vmcp.Backend.
// MCPServerEntry is a zero-infrastructure catalog entry that directly points to a remote URL.
func (d *k8sDiscoverer) getMCPServerEntryAsBackend(ctx context.Context, entryName string) (*vmcp.Backend, error) {
	mcpServerEntry := &mcpv1beta1.MCPServerEntry{}
	key := client.ObjectKey{Name: entryName, Namespace: d.namespace}
	if err := d.k8sClient.Get(ctx, key, mcpServerEntry); err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("MCPServerEntry %s not found", entryName)
		}
		return nil, fmt.Errorf("failed to get MCPServerEntry: %w", err)
	}

	// Unlike MCPServer/MCPRemoteProxy (which use status.URL, empty until ready),
	// MCPServerEntry always has spec.remoteUrl set. Explicitly check phase to
	// avoid routing to entries that failed validation.
	if mcpServerEntry.Status.Phase != mcpv1beta1.MCPServerEntryPhaseValid {
		slog.Debug("skipping server entry with non-valid phase",
			"entry", entryName, "phase", mcpServerEntry.Status.Phase)
		return nil, nil
	}

	backend := d.mcpServerEntryToBackend(ctx, mcpServerEntry)
	if backend == nil {
		slog.Warn("skipping server entry due to conversion failure", "entry", entryName)
		return nil, nil
	}

	if backend.BaseURL == "" {
		slog.Debug("skipping server entry without URL", "entry", entryName)
		return nil, nil
	}

	return backend, nil
}

// mcpServerEntryToBackend converts an MCPServerEntry CRD to a vmcp.Backend.
// Unlike MCPServer and MCPRemoteProxy, MCPServerEntry uses the remote URL directly
// from the spec (no K8s Service needed since it's a zero-infrastructure entry).
func (d *k8sDiscoverer) mcpServerEntryToBackend(ctx context.Context, entry *mcpv1beta1.MCPServerEntry) *vmcp.Backend {
	transportType, err := transporttypes.ParseTransportType(entry.Spec.Transport)
	if err != nil {
		slog.Warn("failed to parse transport type for MCPServerEntry",
			"transport", entry.Spec.Transport,
			"entry", entry.Name,
			"error", err)
		transportType = transporttypes.TransportTypeStreamableHTTP
	}

	// MCPServerEntry uses the remote URL directly from the spec, not from status.
	// This is the key difference from MCPServer/MCPRemoteProxy which use status.URL
	// (set after K8s Service creation).
	// Defense-in-depth: validate the URL at runtime even though the CRD has pattern validation.
	if _, err := url.Parse(entry.Spec.RemoteURL); err != nil {
		slog.Warn("invalid RemoteURL for MCPServerEntry",
			"entry", entry.Name,
			"url", entry.Spec.RemoteURL,
			"error", err)
		return nil
	}
	remoteURL := entry.Spec.RemoteURL

	// Map entry phase to backend health status
	healthStatus := mapMCPServerEntryPhaseToHealth(entry.Status.Phase)

	transportTypeStr := transportType.String()
	if transportTypeStr == "" {
		transportTypeStr = transportTypeUnknown
	}

	// Extract user labels from annotations
	userLabels := make(map[string]string)
	if entry.Annotations != nil {
		for key, value := range entry.Annotations {
			if !isStandardK8sAnnotation(key) {
				userLabels[key] = value
			}
		}
	}

	backend := &vmcp.Backend{
		ID:            entry.Name,
		Name:          entry.Name,
		BaseURL:       remoteURL,
		TransportType: transportTypeStr,
		Type:          vmcp.BackendTypeEntry,
		HealthStatus:  healthStatus,
		Metadata:      make(map[string]string),
	}

	// Copy user labels to metadata first
	maps.Copy(backend.Metadata, userLabels)

	// Set system metadata (these override user labels to prevent conflicts)
	backend.Metadata[metadataKeyToolType] = metadataToolTypeMCP
	backend.Metadata[metadataKeyWorkloadType] = "server_entry"
	backend.Metadata[metadataKeyWorkloadStatus] = string(entry.Status.Phase)
	backend.Metadata[metadataKeyRemoteURL] = entry.Spec.RemoteURL
	if entry.Namespace != "" {
		backend.Metadata[metadataKeyNamespace] = entry.Namespace
	}

	// Fetch CA bundle data from ConfigMap for dynamic mode TLS verification.
	// Failure is fatal: if the user explicitly configured caBundleRef, proceeding
	// without custom CA would silently degrade TLS trust. The reconciler will retry.
	if entry.Spec.CABundleRef != nil && entry.Spec.CABundleRef.ConfigMapRef != nil {
		caData, err := d.fetchCABundleData(ctx, entry.Spec.CABundleRef)
		if err != nil {
			slog.Error("failed to fetch CA bundle for MCPServerEntry",
				"entry", entry.Name, "error", err)
			return nil
		}
		backend.CABundleData = caData
	}

	// Discover and populate authentication configuration from MCPServerEntry
	if err := d.discoverServerEntryAuthConfig(ctx, entry, backend); err != nil {
		slog.Error("failed to discover auth config for MCPServerEntry", "entry", entry.Name, "error", err)
		return nil
	}

	// Extract header forward configuration. Plaintext headers are copied verbatim.
	// Secret-backed headers are mapped to secret identifiers resolved at runtime via
	// secrets.EnvironmentProvider (env var TOOLHIVE_SECRET_<identifier>). The operator
	// injects the actual Secret value into the vMCP pod via valueFrom.secretKeyRef.
	backend.HeaderForward = buildHeaderForwardFromEntry(entry)

	return backend
}

// buildHeaderForwardFromEntry converts MCPServerEntry.spec.headerForward into the
// runtime vmcp.HeaderForwardConfig. Returns nil if the entry declares no header
// forwarding. Secret values are never read or stored here — only identifiers.
func buildHeaderForwardFromEntry(entry *mcpv1beta1.MCPServerEntry) *vmcp.HeaderForwardConfig {
	if entry.Spec.HeaderForward == nil {
		return nil
	}
	src := entry.Spec.HeaderForward

	var plaintext map[string]string
	if len(src.AddPlaintextHeaders) > 0 {
		plaintext = make(map[string]string, len(src.AddPlaintextHeaders))
		maps.Copy(plaintext, src.AddPlaintextHeaders)
	}

	var secretIdents map[string]string
	if len(src.AddHeadersFromSecret) > 0 {
		secretIdents = make(map[string]string, len(src.AddHeadersFromSecret))
		for _, ref := range src.AddHeadersFromSecret {
			if ref.ValueSecretRef == nil {
				continue
			}
			_, identifier := ctrlutil.GenerateHeaderForwardSecretEnvVarName(entry.Name, ref.HeaderName)
			secretIdents[ref.HeaderName] = identifier
		}
	}

	if plaintext == nil && secretIdents == nil {
		return nil
	}
	return &vmcp.HeaderForwardConfig{
		AddPlaintextHeaders:  plaintext,
		AddHeadersFromSecret: secretIdents,
	}
}

// mapMCPServerEntryPhaseToHealth converts a MCPServerEntryPhase to a backend health status.
func mapMCPServerEntryPhaseToHealth(phase mcpv1beta1.MCPServerEntryPhase) vmcp.BackendHealthStatus {
	switch phase {
	case mcpv1beta1.MCPServerEntryPhaseValid:
		return vmcp.BackendHealthy
	case mcpv1beta1.MCPServerEntryPhaseFailed:
		return vmcp.BackendUnhealthy
	case mcpv1beta1.MCPServerEntryPhasePending:
		return vmcp.BackendUnknown
	default:
		return vmcp.BackendUnknown
	}
}

// discoverServerEntryAuthConfig discovers and populates authentication configuration
// from the MCPServerEntry's ExternalAuthConfigRef.
func (d *k8sDiscoverer) discoverServerEntryAuthConfig(
	ctx context.Context,
	entry *mcpv1beta1.MCPServerEntry,
	backend *vmcp.Backend,
) error {
	return d.discoverAuthConfigFromRef(
		ctx,
		entry.Spec.ExternalAuthConfigRef,
		entry.Namespace,
		entry.Name,
		"MCPServerEntry",
		backend,
	)
}

// fetchCABundleData reads CA certificate PEM data from a ConfigMap referenced by CABundleRef.
// Returns the raw PEM bytes for use in dynamic mode where volumes aren't mounted.
func (d *k8sDiscoverer) fetchCABundleData(ctx context.Context, ref *mcpv1beta1.CABundleSource) ([]byte, error) {
	if ref.ConfigMapRef == nil {
		return nil, fmt.Errorf("CABundleRef.configMapRef is nil")
	}

	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: ref.ConfigMapRef.Name, Namespace: d.namespace}
	if err := d.k8sClient.Get(ctx, key, cm); err != nil {
		return nil, fmt.Errorf("failed to get CA bundle ConfigMap %s: %w", ref.ConfigMapRef.Name, err)
	}

	// Default key is "ca.crt" if not specified
	dataKey := ref.ConfigMapRef.Key
	if dataKey == "" {
		dataKey = "ca.crt"
	}

	data, ok := cm.Data[dataKey]
	if !ok {
		return nil, fmt.Errorf("ConfigMap %s does not contain key %q", ref.ConfigMapRef.Name, dataKey)
	}

	return []byte(data), nil
}

// discoverRemoteProxyAuthConfig discovers and populates authentication configuration
// from the MCPRemoteProxy's ExternalAuthConfigRef.
func (d *k8sDiscoverer) discoverRemoteProxyAuthConfig(
	ctx context.Context,
	proxy *mcpv1beta1.MCPRemoteProxy,
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

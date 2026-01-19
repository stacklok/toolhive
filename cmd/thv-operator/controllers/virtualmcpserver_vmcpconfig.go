package controllers

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/configmaps"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	operatorvmcpconfig "github.com/stacklok/toolhive/cmd/thv-operator/pkg/vmcpconfig"
	"github.com/stacklok/toolhive/pkg/groups"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// ensureVmcpConfigConfigMap ensures the vmcp Config ConfigMap exists and is up to date
// workloadInfos is the list of workloads in the group, passed in to ensure consistency
// across multiple calls that need the same workload list.
func (r *VirtualMCPServerReconciler) ensureVmcpConfigConfigMap(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	typedWorkloads []workloads.TypedWorkload,
) error {
	// Create OIDC resolver and converter for CRD-to-config transformation
	oidcResolver := oidc.NewResolver(r.Client)
	converter, err := operatorvmcpconfig.NewConverter(oidcResolver, r.Client)
	if err != nil {
		return fmt.Errorf("failed to create vmcp converter: %w", err)
	}
	config, err := converter.Convert(ctx, vmcp)
	if err != nil {
		return fmt.Errorf("failed to create vmcp Config from VirtualMCPServer: %w", err)
	}

	// Static mode (inline): Embed full backend details in ConfigMap.
	// Dynamic mode (discovered): vMCP discovers backends at runtime via K8s API.
	if config.OutgoingAuth != nil && config.OutgoingAuth.Source == "inline" {
		// Build auth config with backend details
		discoveredAuthConfig, err := r.buildOutgoingAuthConfig(ctx, vmcp, typedWorkloads)
		if err != nil {
			return fmt.Errorf("failed to build auth config for static mode: %w", err)
		}
		if discoveredAuthConfig != nil {
			config.OutgoingAuth = discoveredAuthConfig
		}

		// Discover backends with metadata
		backends, err := r.discoverBackendsWithMetadata(ctx, vmcp)
		if err != nil {
			return fmt.Errorf("failed to discover backends for static mode: %w", err)
		}

		// Get transport types from workload specs
		transportMap, err := r.buildTransportMap(ctx, vmcp.Namespace, typedWorkloads)
		if err != nil {
			return fmt.Errorf("failed to build transport map for static mode: %w", err)
		}

		config.Backends = convertBackendsToStaticBackends(ctx, backends, transportMap)

		// Validate at least one backend exists
		if len(config.Backends) == 0 {
			return fmt.Errorf(
				"static mode requires at least one backend with valid transport (%v), "+
					"but none were discovered in group %s",
				vmcpconfig.StaticModeAllowedTransports,
				config.Group,
			)
		}
	}

	// Validate the vmcp Config before creating the ConfigMap
	validator := operatorvmcpconfig.NewValidator()
	if err := validator.Validate(ctx, config); err != nil {
		return fmt.Errorf("invalid vmcp Config: %w", err)
	}

	// Marshal to YAML for storage in ConfigMap
	// Note: gopkg.in/yaml.v3 produces deterministic output by sorting map keys alphabetically.
	// This ensures stable checksums for triggering pod rollouts only when content actually changes.
	vmcpConfigYAML, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal vmcp config: %w", err)
	}

	configMapName := vmcpConfigMapName(vmcp.Name)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: vmcp.Namespace,
			Labels:    labelsForVmcpConfig(vmcp.Name),
		},
		Data: map[string]string{
			"config.yaml": string(vmcpConfigYAML),
		},
	}

	// Compute and add content checksum annotation using robust SHA256-based checksum
	checksumCalculator := checksum.NewRunConfigConfigMapChecksum()
	checksumValue := checksumCalculator.ComputeConfigMapChecksum(configMap)
	configMap.Annotations = map[string]string{
		checksum.ContentChecksumAnnotation: checksumValue,
	}

	// Use the kubernetes configmaps client for upsert operations
	configMapsClient := configmaps.NewClient(r.Client, r.Scheme)
	if _, err := configMapsClient.UpsertWithOwnerReference(ctx, configMap, vmcp); err != nil {
		return fmt.Errorf("failed to upsert vmcp Config ConfigMap: %w", err)
	}

	return nil
}

// labelsForVmcpConfig returns labels for vmcp config ConfigMap
func labelsForVmcpConfig(vmcpName string) map[string]string {
	return map[string]string{
		"toolhive.stacklok.io/component":          "vmcp-config",
		"toolhive.stacklok.io/virtual-mcp-server": vmcpName,
		"toolhive.stacklok.io/managed-by":         "toolhive-operator",
	}
}

// discoverBackendsWithMetadata discovers backends and returns full Backend objects with metadata.
// Used in static mode for ConfigMap generation to preserve backend metadata.
func (r *VirtualMCPServerReconciler) discoverBackendsWithMetadata(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) ([]vmcptypes.Backend, error) {
	groupsManager := groups.NewCRDManager(r.Client, vmcp.Namespace)
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(r.Client, vmcp.Namespace)

	// Build auth config if OutgoingAuth is configured
	var authConfig *vmcpconfig.OutgoingAuthConfig
	if vmcp.Spec.OutgoingAuth != nil {
		typedWorkloads, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, vmcp.Spec.Config.Group)
		if err != nil {
			return nil, fmt.Errorf("failed to list workloads in group: %w", err)
		}

		authConfig, err = r.buildOutgoingAuthConfig(ctx, vmcp, typedWorkloads)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.V(1).Info("Failed to build outgoing auth config, continuing without authentication",
				"error", err,
				"virtualmcpserver", vmcp.Name,
				"namespace", vmcp.Namespace)
			authConfig = nil // Continue without auth config on error
		}
	}

	backendDiscoverer := aggregator.NewUnifiedBackendDiscoverer(workloadDiscoverer, groupsManager, authConfig)
	backends, err := backendDiscoverer.Discover(ctx, vmcp.Spec.Config.Group)
	if err != nil {
		return nil, fmt.Errorf("failed to discover backends: %w", err)
	}

	return backends, nil
}

// buildTransportMap builds a map of backend names to transport types from workload Specs.
// Used in static mode to populate transport field in ConfigMap.
func (r *VirtualMCPServerReconciler) buildTransportMap(
	ctx context.Context,
	namespace string,
	typedWorkloads []workloads.TypedWorkload,
) (map[string]string, error) {
	transportMap := make(map[string]string, len(typedWorkloads))

	mcpServerMap, err := r.listMCPServersAsMap(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPServers: %w", err)
	}

	mcpRemoteProxyMap, err := r.listMCPRemoteProxiesAsMap(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies: %w", err)
	}

	for _, workload := range typedWorkloads {
		var transport string

		switch workload.Type {
		case workloads.WorkloadTypeMCPServer:
			if mcpServer, found := mcpServerMap[workload.Name]; found {
				// Read effective transport (ProxyMode takes precedence over Transport)
				// For stdio servers, ProxyMode indicates how they're proxied (sse or streamable-http)
				if mcpServer.Spec.ProxyMode != "" {
					transport = string(mcpServer.Spec.ProxyMode)
				} else {
					transport = string(mcpServer.Spec.Transport)
				}
			}

		case workloads.WorkloadTypeMCPRemoteProxy:
			if mcpRemoteProxy, found := mcpRemoteProxyMap[workload.Name]; found {
				transport = string(mcpRemoteProxy.Spec.Transport)
			}
		}

		if transport != "" {
			transportMap[workload.Name] = transport
		}
	}

	return transportMap, nil
}

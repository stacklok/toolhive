// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/configmaps"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
	operatorvmcpconfig "github.com/stacklok/toolhive/cmd/thv-operator/pkg/vmcpconfig"
	"github.com/stacklok/toolhive/pkg/groups"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// ensureVmcpConfigConfigMap ensures the vmcp Config ConfigMap exists and is up to date.
// workloadInfos is the list of workloads in the group, passed in to ensure consistency
// across multiple calls that need the same workload list.
// statusManager is used to set auth config conditions for any conversion failures.
func (r *VirtualMCPServerReconciler) ensureVmcpConfigConfigMap(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	typedWorkloads []workloads.TypedWorkload,
	statusManager virtualmcpserverstatus.StatusManager,
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

	// Process outgoing auth configuration for both inline and discovered modes
	if err := r.processOutgoingAuth(ctx, vmcp, config, typedWorkloads, statusManager); err != nil {
		return err
	}

	// Auto-populate embedding service name for embedding servers (inline or referenced).
	// When the VirtualMCPServer has an embeddingServer spec (inline), the operator creates
	// an EmbeddingServer CR and wires its service name into the optimizer config.
	// When embeddingServerRef is used, the referenced EmbeddingServer's name is used directly.
	if config.Optimizer != nil {
		if esName := embeddingServerNameForVMCP(vmcp); esName != "" {
			config.Optimizer.EmbeddingService = esName
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

		// Build auth config and collect any errors (but don't fail the operation)
		// Note: Auth errors are collected and reported via status conditions by processOutgoingAuth.
		// In static mode, we still attempt to build the auth config for ConfigMap embedding.
		authConfig, _, _ = r.buildOutgoingAuthConfig(ctx, vmcp, typedWorkloads)
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

// extractInlineBackendNames extracts the list of inline backend names from the VirtualMCPServer spec.
func extractInlineBackendNames(vmcp *mcpv1alpha1.VirtualMCPServer) []string {
	if vmcp.Spec.OutgoingAuth == nil || vmcp.Spec.OutgoingAuth.Backends == nil {
		return nil
	}
	names := make([]string, 0, len(vmcp.Spec.OutgoingAuth.Backends))
	for backendName := range vmcp.Spec.OutgoingAuth.Backends {
		names = append(names, backendName)
	}
	return names
}

// determineValidInlineBackends determines which inline backends have valid auth configs.
func determineValidInlineBackends(authConfig *vmcpconfig.OutgoingAuthConfig, inlineBackendNames []string) []string {
	if authConfig == nil || authConfig.Backends == nil {
		return nil
	}
	valid := make([]string, 0)
	for backendName := range authConfig.Backends {
		// Only count inline backends (not discovered backends)
		for _, inlineBackend := range inlineBackendNames {
			if backendName == inlineBackend {
				valid = append(valid, backendName)
				break
			}
		}
	}
	return valid
}

// processOutgoingAuth processes outgoing auth configuration for both inline and discovered modes.
// It builds auth configs, sets status conditions for all auth config types, and configures static backends for inline mode.
func (r *VirtualMCPServerReconciler) processOutgoingAuth(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	config *vmcpconfig.Config,
	typedWorkloads []workloads.TypedWorkload,
	statusManager virtualmcpserverstatus.StatusManager,
) error {
	// Clean up stale conditions if outgoing auth is not configured
	if config.OutgoingAuth == nil {
		setAuthConfigConditions(statusManager, nil, nil, false, nil, nil)
		return nil
	}

	isInlineMode := config.OutgoingAuth.Source == OutgoingAuthSourceInline
	isDiscoveredMode := config.OutgoingAuth.Source == OutgoingAuthSourceDiscovered

	// Clean up stale conditions if not using inline or discovered mode
	if !isInlineMode && !isDiscoveredMode {
		setAuthConfigConditions(statusManager, nil, nil, false, nil, nil)
		return nil
	}

	// Build auth config and collect all errors (default, backend-specific, discovered)
	// All errors are non-fatal - the system continues in degraded mode with partial auth config
	authConfig, backendsWithAuthConfig, allAuthErrors := r.buildOutgoingAuthConfig(ctx, vmcp, typedWorkloads)

	// Extract inline backend names and determine valid auth configs
	inlineBackendNames := extractInlineBackendNames(vmcp)
	hasValidDefaultAuth := authConfig != nil && authConfig.Default != nil
	validInlineBackends := determineValidInlineBackends(authConfig, inlineBackendNames)

	// Set conditions for all auth config types (default, backend-specific, discovered)
	// True for success, False for errors
	setAuthConfigConditions(
		statusManager,
		backendsWithAuthConfig,
		inlineBackendNames,
		hasValidDefaultAuth,
		validInlineBackends,
		allAuthErrors,
	)

	// Static mode (inline): Embed full backend details in ConfigMap
	if isInlineMode {
		if authConfig != nil {
			config.OutgoingAuth = authConfig
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
	// Dynamic mode (discovered): vMCP discovers backends at runtime via K8s API
	// Conditions are already set above, no additional ConfigMap config needed

	return nil
}

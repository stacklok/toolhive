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
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/vmcpconfig"
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
	ctxLogger := log.FromContext(ctx)

	// Create OIDC resolver to handle all OIDC types (kubernetes, configMap, inline)
	oidcResolver := oidc.NewResolver(r.Client)

	// Convert CRD to vmcp config using converter with OIDC resolver and Kubernetes client
	// The client is needed to fetch referenced VirtualMCPCompositeToolDefinition resources
	converter, err := vmcpconfig.NewConverter(oidcResolver, r.Client)
	if err != nil {
		return fmt.Errorf("failed to create vmcp converter: %w", err)
	}
	config, err := converter.Convert(ctx, vmcp)
	if err != nil {
		return fmt.Errorf("failed to create vmcp Config from VirtualMCPServer: %w", err)
	}

	// Only include backends in ConfigMap for static mode (source: inline)
	// In dynamic mode (source: discovered), vMCP discovers backends at runtime via K8s API
	if config.OutgoingAuth != nil && config.OutgoingAuth.Source == "inline" {
		// Build OutgoingAuthConfig with full backend details for static mode
		discoveredAuthConfig, err := r.buildOutgoingAuthConfig(ctx, vmcp, typedWorkloads)
		if err != nil {
			ctxLogger.V(1).Info("Failed to build auth config for inline mode, using spec-only config",
				"error", err)
		} else if discoveredAuthConfig != nil {
			// Merge discovered config into the config
			// The discovered config already includes inline overrides, so we can replace it
			config.OutgoingAuth = discoveredAuthConfig
		}

		// Build static backend configurations with URLs and transport types
		// This allows vMCP to operate without K8s API access in static mode
		staticBackends, err := r.buildStaticBackends(ctx, vmcp, typedWorkloads)
		if err != nil {
			ctxLogger.V(1).Info("Failed to build static backends, using empty list",
				"error", err)
		} else {
			config.Backends = staticBackends
		}
	}
	// For discovered mode, keep the minimal OutgoingAuthConfig (source, defaults, overrides only)
	// vMCP will discover backends and their auth configs at runtime using K8s API

	// Validate the vmcp Config before creating the ConfigMap
	validator := vmcpconfig.NewValidator()
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

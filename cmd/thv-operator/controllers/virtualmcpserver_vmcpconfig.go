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

	// For dynamic mode (source: "discovered"), preserve the Source field so the vMCP pod
	// can start BackendWatcher for runtime backend discovery.
	// For inline mode, discover backends at reconcile time and include in ConfigMap.
	if config.OutgoingAuth != nil && config.OutgoingAuth.Source != "discovered" {
		discoveredAuthConfig, err := r.buildOutgoingAuthConfig(ctx, vmcp, typedWorkloads)
		if err != nil {
			ctxLogger.V(1).Info("Failed to build discovered auth config, using spec-only config",
				"error", err)
		} else if discoveredAuthConfig != nil {
			config.OutgoingAuth = discoveredAuthConfig
		}
	}

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

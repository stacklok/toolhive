package controllers

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
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
	workloadInfos []workloads.TypedWorkload,
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

	// If OutgoingAuth source is "discovered", we need to discover and include
	// ExternalAuthConfig from MCPServers in the ConfigMap
	if config.OutgoingAuth != nil && config.OutgoingAuth.Source == "discovered" {
		// Build discovered OutgoingAuthConfig using the provided workload infos
		discoveredAuthConfig, err := r.buildOutgoingAuthConfig(ctx, vmcp, workloadInfos)
		if err != nil {
			ctxLogger.V(1).Info("Failed to build discovered auth config, using spec-only config",
				"error", err)
		} else if discoveredAuthConfig != nil {
			// Merge discovered config into the config
			// The discovered config already includes inline overrides, so we can replace it
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

	return r.ensureVmcpConfigConfigMapResource(ctx, vmcp, configMap)
}

// ensureVmcpConfigConfigMapResource ensures the vmcp Config ConfigMap exists and is up to date
func (r *VirtualMCPServerReconciler) ensureVmcpConfigConfigMapResource(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	desired *corev1.ConfigMap,
) error {
	ctxLogger := log.FromContext(ctx)
	current := &corev1.ConfigMap{}
	objectKey := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	err := r.Get(ctx, objectKey, current)

	if errors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(vmcp, desired, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference for vmcp Config ConfigMap: %w", err)
		}

		ctxLogger.Info("vmcp Config ConfigMap does not exist, creating", "ConfigMap.Name", desired.Name)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create vmcp Config ConfigMap: %w", err)
		}
		ctxLogger.Info("vmcp Config ConfigMap created", "ConfigMap.Name", desired.Name)
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get vmcp Config ConfigMap: %w", err)
	}

	// ConfigMap exists, check if content has changed by comparing checksums
	currentChecksum := current.Annotations[checksum.ContentChecksumAnnotation]
	desiredChecksum := desired.Annotations[checksum.ContentChecksumAnnotation]

	if currentChecksum != desiredChecksum {
		desired.ResourceVersion = current.ResourceVersion
		desired.UID = current.UID

		if err := controllerutil.SetControllerReference(vmcp, desired, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference for vmcp Config ConfigMap: %w", err)
		}

		ctxLogger.Info("vmcp Config ConfigMap content changed, updating",
			"ConfigMap.Name", desired.Name,
			"oldChecksum", currentChecksum,
			"newChecksum", desiredChecksum)
		if err := r.Update(ctx, desired); err != nil {
			return fmt.Errorf("failed to update vmcp Config ConfigMap: %w", err)
		}
		ctxLogger.Info("vmcp Config ConfigMap updated", "ConfigMap.Name", desired.Name)
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

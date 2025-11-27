package controllers

import (
	"context"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/vmcpconfig"
	vmcpconfigtypes "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// ensureVmcpConfigConfigMap ensures the vmcp Config ConfigMap exists and is up to date
func (r *VirtualMCPServerReconciler) ensureVmcpConfigConfigMap(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) error {
	// Convert CRD to vmcp config using converter
	converter := vmcpconfig.NewConverter()
	config, err := converter.Convert(ctx, vmcp)
	if err != nil {
		return fmt.Errorf("failed to create vmcp Config from VirtualMCPServer: %w", err)
	}

	// Validate the vmcp Config before creating the ConfigMap
	validator := vmcpconfig.NewValidator()
	if err := validator.Validate(ctx, config); err != nil {
		return fmt.Errorf("invalid vmcp Config: %w", err)
	}

	// Convert to CLI-compatible format before marshaling
	// The vmcp binary's YAML loader expects a different structure than the Go config type
	cliConfig := convertToCLIFormat(config)

	// Marshal to YAML for storage in ConfigMap
	// Note: gopkg.in/yaml.v3 produces deterministic output by sorting map keys alphabetically.
	// This ensures stable checksums for triggering pod rollouts only when content actually changes.
	vmcpConfigYAML, err := yaml.Marshal(cliConfig)
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

// convertToCLIFormat converts the config to a format compatible with vmcp's CLI YAML loader.
// The vmcp YAML loader expects token_cache to have a single "config" section with all fields,
// rather than separate "memory" and "redis" sections.
func convertToCLIFormat(cfg *vmcpconfigtypes.Config) map[string]interface{} {
	result := map[string]interface{}{
		"name":  cfg.Name,
		"group": cfg.Group,
	}

	convertIncomingAuth(cfg, result)
	convertOutgoingAuth(cfg, result)
	convertAggregation(cfg, result)
	convertTokenCache(cfg, result)
	convertCompositeTools(cfg, result)
	convertOperational(cfg, result)

	return result
}

func convertIncomingAuth(cfg *vmcpconfigtypes.Config, result map[string]interface{}) {
	if cfg.IncomingAuth == nil {
		return
	}
	incomingAuth := map[string]interface{}{
		"type": cfg.IncomingAuth.Type,
	}
	if cfg.IncomingAuth.OIDC != nil {
		incomingAuth["oidc"] = cfg.IncomingAuth.OIDC
	}
	if cfg.IncomingAuth.Authz != nil {
		incomingAuth["authz"] = cfg.IncomingAuth.Authz
	}
	result["incoming_auth"] = incomingAuth
}

func convertOutgoingAuth(cfg *vmcpconfigtypes.Config, result map[string]interface{}) {
	if cfg.OutgoingAuth == nil {
		return
	}
	outgoingAuth := map[string]interface{}{
		"source": cfg.OutgoingAuth.Source,
	}
	if cfg.OutgoingAuth.Default != nil {
		outgoingAuth["default"] = cfg.OutgoingAuth.Default
	}
	if cfg.OutgoingAuth.Backends != nil {
		outgoingAuth["backends"] = cfg.OutgoingAuth.Backends
	}
	result["outgoing_auth"] = outgoingAuth
}

func convertAggregation(cfg *vmcpconfigtypes.Config, result map[string]interface{}) {
	if cfg.Aggregation == nil {
		return
	}
	aggregation := map[string]interface{}{
		"conflict_resolution": cfg.Aggregation.ConflictResolution,
	}
	if cfg.Aggregation.ConflictResolutionConfig != nil {
		aggregation["conflict_resolution_config"] = cfg.Aggregation.ConflictResolutionConfig
	}
	if cfg.Aggregation.Tools != nil {
		aggregation["tools"] = cfg.Aggregation.Tools
	}
	result["aggregation"] = aggregation
}

func convertTokenCache(cfg *vmcpconfigtypes.Config, result map[string]interface{}) {
	if cfg.TokenCache == nil {
		return
	}
	tokenCache := map[string]interface{}{
		"provider": cfg.TokenCache.Provider,
	}

	cacheConfig := buildCacheConfig(cfg.TokenCache)
	if len(cacheConfig) > 0 {
		tokenCache["config"] = cacheConfig
	}

	result["token_cache"] = tokenCache
}

func buildCacheConfig(tc *vmcpconfigtypes.TokenCacheConfig) map[string]interface{} {
	cacheConfig := make(map[string]interface{})

	if tc.Memory != nil {
		cacheConfig["max_entries"] = tc.Memory.MaxEntries
		if tc.Memory.TTLOffset > 0 {
			cacheConfig["ttl_offset"] = time.Duration(tc.Memory.TTLOffset).String()
		}
	}

	if tc.Redis != nil {
		cacheConfig["address"] = tc.Redis.Address
		cacheConfig["db"] = tc.Redis.DB
		cacheConfig["key_prefix"] = tc.Redis.KeyPrefix
		if tc.Redis.Password != "" {
			cacheConfig["password"] = tc.Redis.Password
		}
		if tc.Redis.TTLOffset > 0 {
			cacheConfig["ttl_offset"] = time.Duration(tc.Redis.TTLOffset).String()
		}
	}

	return cacheConfig
}

func convertCompositeTools(cfg *vmcpconfigtypes.Config, result map[string]interface{}) {
	if cfg.CompositeTools != nil {
		result["composite_tools"] = cfg.CompositeTools
	}
}

func convertOperational(cfg *vmcpconfigtypes.Config, result map[string]interface{}) {
	if cfg.Operational != nil {
		result["operational"] = cfg.Operational
	}
}

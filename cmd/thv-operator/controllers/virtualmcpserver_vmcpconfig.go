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
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

const (
	// conflictResolutionPrefix is the string value for prefix conflict resolution strategy
	conflictResolutionPrefix = "prefix"
)

// ensureVmcpConfigConfigMap ensures the vmcp Config ConfigMap exists and is up to date
func (r *VirtualMCPServerReconciler) ensureVmcpConfigConfigMap(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) error {
	vmcpConfig, err := r.createVmcpConfigFromVirtualMCPServer(ctx, vmcp)
	if err != nil {
		return fmt.Errorf("failed to create vmcp Config from VirtualMCPServer: %w", err)
	}

	// Validate the vmcp Config before creating the ConfigMap
	if err := r.validateVmcpConfig(ctx, vmcpConfig); err != nil {
		return fmt.Errorf("invalid vmcp Config: %w", err)
	}

	vmcpConfigYAML, err := yaml.Marshal(vmcpConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal vmcp config: %w", err)
	}

	configMapName := fmt.Sprintf("%s-vmcp-config", vmcp.Name)
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

// createVmcpConfigFromVirtualMCPServer converts VirtualMCPServer CRD spec to vmcp Config
//
//nolint:unparam // error return reserved for future reference resolution
func (r *VirtualMCPServerReconciler) createVmcpConfigFromVirtualMCPServer(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*vmcpconfig.Config, error) {
	config := &vmcpconfig.Config{
		Name:     vmcp.Name,
		GroupRef: vmcp.Spec.GroupRef.Name,
	}

	// Convert IncomingAuth
	if vmcp.Spec.IncomingAuth != nil {
		config.IncomingAuth = r.convertIncomingAuth(ctx, vmcp)
	}

	// Convert OutgoingAuth
	if vmcp.Spec.OutgoingAuth != nil {
		config.OutgoingAuth = r.convertOutgoingAuth(ctx, vmcp)
	}

	// Convert Aggregation
	if vmcp.Spec.Aggregation != nil {
		config.Aggregation = r.convertAggregation(ctx, vmcp)
	}

	// Convert CompositeTools
	if len(vmcp.Spec.CompositeTools) > 0 {
		config.CompositeTools = r.convertCompositeTools(ctx, vmcp)
	}

	// Convert TokenCache
	if vmcp.Spec.TokenCache != nil {
		config.TokenCache = r.convertTokenCache(ctx, vmcp)
	}

	// Convert Operational
	if vmcp.Spec.Operational != nil {
		config.Operational = r.convertOperational(ctx, vmcp)
	}

	return config, nil
}

// convertIncomingAuth converts IncomingAuthConfig from CRD to vmcp config
func (*VirtualMCPServerReconciler) convertIncomingAuth(
	_ context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) *vmcpconfig.IncomingAuthConfig {
	incoming := &vmcpconfig.IncomingAuthConfig{}

	// Convert OIDC configuration
	if vmcp.Spec.IncomingAuth.OIDCConfig != nil {
		incoming.Type = "oidc"

		// Handle inline OIDC configuration
		if vmcp.Spec.IncomingAuth.OIDCConfig.Type == authzLabelValueInline && vmcp.Spec.IncomingAuth.OIDCConfig.Inline != nil {
			inline := vmcp.Spec.IncomingAuth.OIDCConfig.Inline
			incoming.OIDC = &vmcpconfig.OIDCConfig{
				Issuer:       inline.Issuer,
				ClientID:     inline.ClientID, // Note: API uses clientId (camelCase) but config uses ClientID
				ClientSecret: inline.ClientSecret,
				Audience:     inline.Audience,
				Resource:     vmcp.Spec.IncomingAuth.OIDCConfig.ResourceURL,
				Scopes:       nil, // TODO: Add scopes if needed
			}
		} else {
			// TODO: Handle configMap and kubernetes types
			// For now, create empty config to avoid nil pointer
			incoming.OIDC = &vmcpconfig.OIDCConfig{}
		}
	}

	// Convert authorization configuration
	if vmcp.Spec.IncomingAuth.AuthzConfig != nil {
		// Map Kubernetes API types to vmcp config types
		// API "inline" maps to vmcp "cedar"
		authzType := vmcp.Spec.IncomingAuth.AuthzConfig.Type
		if authzType == authzLabelValueInline {
			authzType = "cedar"
		}

		incoming.Authz = &vmcpconfig.AuthzConfig{
			Type: authzType,
		}

		// Handle inline policies
		if vmcp.Spec.IncomingAuth.AuthzConfig.Type == authzLabelValueInline && vmcp.Spec.IncomingAuth.AuthzConfig.Inline != nil {
			incoming.Authz.Policies = vmcp.Spec.IncomingAuth.AuthzConfig.Inline.Policies
		}
		// TODO: Load policies from ConfigMap if Type is "configMap"
	}

	return incoming
}

// convertOutgoingAuth converts OutgoingAuthConfig from CRD to vmcp config
func (r *VirtualMCPServerReconciler) convertOutgoingAuth(
	_ context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) *vmcpconfig.OutgoingAuthConfig {
	outgoing := &vmcpconfig.OutgoingAuthConfig{
		Source:   vmcp.Spec.OutgoingAuth.Source,
		Backends: make(map[string]*vmcpconfig.BackendAuthStrategy),
	}

	// Convert Default
	if vmcp.Spec.OutgoingAuth.Default != nil {
		outgoing.Default = r.convertBackendAuthConfig(vmcp.Spec.OutgoingAuth.Default)
	}

	// Convert per-backend overrides
	for backendName, backendAuth := range vmcp.Spec.OutgoingAuth.Backends {
		outgoing.Backends[backendName] = r.convertBackendAuthConfig(&backendAuth)
	}

	return outgoing
}

// convertBackendAuthConfig converts BackendAuthConfig from CRD to vmcp config
func (*VirtualMCPServerReconciler) convertBackendAuthConfig(
	crdConfig *mcpv1alpha1.BackendAuthConfig,
) *vmcpconfig.BackendAuthStrategy {
	strategy := &vmcpconfig.BackendAuthStrategy{
		Type:     crdConfig.Type,
		Metadata: make(map[string]any),
	}

	// Convert type-specific configuration to metadata
	if crdConfig.ServiceAccount != nil {
		strategy.Metadata["credentialsRef"] = map[string]string{
			"name": crdConfig.ServiceAccount.CredentialsRef.Name,
			"key":  crdConfig.ServiceAccount.CredentialsRef.Key,
		}
		if crdConfig.ServiceAccount.HeaderName != "" {
			strategy.Metadata["headerName"] = crdConfig.ServiceAccount.HeaderName
		}
		if crdConfig.ServiceAccount.HeaderFormat != "" {
			strategy.Metadata["headerFormat"] = crdConfig.ServiceAccount.HeaderFormat
		}
	}

	if crdConfig.ExternalAuthConfigRef != nil {
		strategy.Metadata["externalAuthConfigRef"] = crdConfig.ExternalAuthConfigRef.Name
	}

	return strategy
}

// convertAggregation converts AggregationConfig from CRD to vmcp config
func (*VirtualMCPServerReconciler) convertAggregation(
	_ context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) *vmcpconfig.AggregationConfig {
	agg := &vmcpconfig.AggregationConfig{}

	// Convert conflict resolution strategy
	switch vmcp.Spec.Aggregation.ConflictResolution {
	case mcpv1alpha1.ConflictResolutionPrefix:
		agg.ConflictResolution = conflictResolutionPrefix
	case mcpv1alpha1.ConflictResolutionPriority:
		agg.ConflictResolution = "priority"
	case mcpv1alpha1.ConflictResolutionManual:
		agg.ConflictResolution = "manual"
	default:
		agg.ConflictResolution = conflictResolutionPrefix // default
	}

	// Convert conflict resolution config
	if vmcp.Spec.Aggregation.ConflictResolutionConfig != nil {
		agg.ConflictResolutionConfig = &vmcpconfig.ConflictResolutionConfig{
			PrefixFormat:  vmcp.Spec.Aggregation.ConflictResolutionConfig.PrefixFormat,
			PriorityOrder: vmcp.Spec.Aggregation.ConflictResolutionConfig.PriorityOrder,
		}
	} else if agg.ConflictResolution == conflictResolutionPrefix {
		// Provide default prefix format if using prefix strategy without explicit config
		agg.ConflictResolutionConfig = &vmcpconfig.ConflictResolutionConfig{
			PrefixFormat: "{workload}_",
		}
	}

	// Convert per-workload tool configs
	if len(vmcp.Spec.Aggregation.Tools) > 0 {
		agg.Tools = make([]*vmcpconfig.WorkloadToolConfig, 0, len(vmcp.Spec.Aggregation.Tools))
		for _, toolConfig := range vmcp.Spec.Aggregation.Tools {
			wtc := &vmcpconfig.WorkloadToolConfig{
				Workload: toolConfig.Workload,
				Filter:   toolConfig.Filter,
			}

			// Convert overrides
			if len(toolConfig.Overrides) > 0 {
				wtc.Overrides = make(map[string]*vmcpconfig.ToolOverride)
				for toolName, override := range toolConfig.Overrides {
					wtc.Overrides[toolName] = &vmcpconfig.ToolOverride{
						Name:        override.Name,
						Description: override.Description,
					}
				}
			}

			agg.Tools = append(agg.Tools, wtc)
		}
	}

	return agg
}

// convertCompositeTools converts CompositeToolSpec from CRD to vmcp config
func (*VirtualMCPServerReconciler) convertCompositeTools(
	_ context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) []*vmcpconfig.CompositeToolConfig {
	compositeTools := make([]*vmcpconfig.CompositeToolConfig, 0, len(vmcp.Spec.CompositeTools))

	for _, crdTool := range vmcp.Spec.CompositeTools {
		tool := &vmcpconfig.CompositeToolConfig{
			Name:        crdTool.Name,
			Description: crdTool.Description,
			Parameters:  make(map[string]vmcpconfig.ParameterSchema),
			Steps:       make([]*vmcpconfig.WorkflowStepConfig, 0, len(crdTool.Steps)),
		}

		// Parse timeout
		if crdTool.Timeout != "" {
			if duration, err := time.ParseDuration(crdTool.Timeout); err == nil {
				tool.Timeout = vmcpconfig.Duration(duration)
			}
		}

		// Convert parameters
		for paramName, paramSpec := range crdTool.Parameters {
			tool.Parameters[paramName] = vmcpconfig.ParameterSchema{
				Type:    paramSpec.Type,
				Default: paramSpec.Default,
			}
		}

		// Convert steps
		for _, crdStep := range crdTool.Steps {
			step := &vmcpconfig.WorkflowStepConfig{
				ID:        crdStep.ID,
				Type:      crdStep.Type,
				Tool:      crdStep.Tool,
				Arguments: convertArguments(crdStep.Arguments),
				Message:   crdStep.Message,
				Condition: crdStep.Condition,
				DependsOn: crdStep.DependsOn,
			}

			// Parse timeout
			if crdStep.Timeout != "" {
				if duration, err := time.ParseDuration(crdStep.Timeout); err == nil {
					step.Timeout = vmcpconfig.Duration(duration)
				}
			}

			// Convert error handling
			if crdStep.OnError != nil {
				step.OnError = &vmcpconfig.StepErrorHandling{
					Action:     crdStep.OnError.Action,
					RetryCount: crdStep.OnError.MaxRetries,
				}
			}

			tool.Steps = append(tool.Steps, step)
		}

		compositeTools = append(compositeTools, tool)
	}

	return compositeTools
}

// convertArguments converts string arguments to any type for template expansion
func convertArguments(args map[string]string) map[string]any {
	result := make(map[string]any, len(args))
	for k, v := range args {
		result[k] = v
	}
	return result
}

// convertTokenCache converts TokenCacheConfig from CRD to vmcp config
func (*VirtualMCPServerReconciler) convertTokenCache(
	_ context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) *vmcpconfig.TokenCacheConfig {
	cache := &vmcpconfig.TokenCacheConfig{
		Provider: vmcp.Spec.TokenCache.Provider,
	}

	if vmcp.Spec.TokenCache.Memory != nil {
		cache.Memory = &vmcpconfig.MemoryCacheConfig{
			MaxEntries: vmcp.Spec.TokenCache.Memory.MaxEntries,
		}
		if vmcp.Spec.TokenCache.Memory.TTLOffset != "" {
			if duration, err := time.ParseDuration(vmcp.Spec.TokenCache.Memory.TTLOffset); err == nil {
				cache.Memory.TTLOffset = vmcpconfig.Duration(duration)
			}
		}
	}

	if vmcp.Spec.TokenCache.Redis != nil {
		cache.Redis = &vmcpconfig.RedisCacheConfig{
			Address:   vmcp.Spec.TokenCache.Redis.Address,
			DB:        vmcp.Spec.TokenCache.Redis.DB,
			KeyPrefix: vmcp.Spec.TokenCache.Redis.KeyPrefix,
			// TODO: Resolve password from secret reference when PasswordRef is set
		}
		//nolint:staticcheck // Empty branch reserved for future password reference resolution
		if vmcp.Spec.TokenCache.Redis.PasswordRef != nil {
			// Password will be resolved at runtime by vmcp binary via secret reference
		}
	}

	return cache
}

// convertOperational converts OperationalConfig from CRD to vmcp config
func (*VirtualMCPServerReconciler) convertOperational(
	_ context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) *vmcpconfig.OperationalConfig {
	operational := &vmcpconfig.OperationalConfig{}

	if vmcp.Spec.Operational.Timeouts != nil {
		operational.Timeouts = &vmcpconfig.TimeoutConfig{
			PerWorkload: make(map[string]vmcpconfig.Duration),
		}

		// Parse default timeout
		if vmcp.Spec.Operational.Timeouts.Default != "" {
			if duration, err := time.ParseDuration(vmcp.Spec.Operational.Timeouts.Default); err == nil {
				operational.Timeouts.Default = vmcpconfig.Duration(duration)
			}
		}

		// Parse per-workload timeouts
		for workload, timeoutStr := range vmcp.Spec.Operational.Timeouts.PerWorkload {
			if duration, err := time.ParseDuration(timeoutStr); err == nil {
				operational.Timeouts.PerWorkload[workload] = vmcpconfig.Duration(duration)
			}
		}
	}

	if vmcp.Spec.Operational.FailureHandling != nil {
		operational.FailureHandling = &vmcpconfig.FailureHandlingConfig{
			UnhealthyThreshold: vmcp.Spec.Operational.FailureHandling.UnhealthyThreshold,
			PartialFailureMode: vmcp.Spec.Operational.FailureHandling.PartialFailureMode,
		}

		// Parse health check interval
		if vmcp.Spec.Operational.FailureHandling.HealthCheckInterval != "" {
			if duration, err := time.ParseDuration(vmcp.Spec.Operational.FailureHandling.HealthCheckInterval); err == nil {
				operational.FailureHandling.HealthCheckInterval = vmcpconfig.Duration(duration)
			}
		}

		// Convert circuit breaker config
		if vmcp.Spec.Operational.FailureHandling.CircuitBreaker != nil {
			operational.FailureHandling.CircuitBreaker = &vmcpconfig.CircuitBreakerConfig{
				Enabled:          vmcp.Spec.Operational.FailureHandling.CircuitBreaker.Enabled,
				FailureThreshold: vmcp.Spec.Operational.FailureHandling.CircuitBreaker.FailureThreshold,
			}

			// Parse circuit breaker timeout
			if vmcp.Spec.Operational.FailureHandling.CircuitBreaker.Timeout != "" {
				if duration, err := time.ParseDuration(vmcp.Spec.Operational.FailureHandling.CircuitBreaker.Timeout); err == nil {
					operational.FailureHandling.CircuitBreaker.Timeout = vmcpconfig.Duration(duration)
				}
			}
		}
	}

	return operational
}

// validateVmcpConfig validates a vmcp Config
func (*VirtualMCPServerReconciler) validateVmcpConfig(
	ctx context.Context,
	config *vmcpconfig.Config,
) error {
	if config == nil {
		return fmt.Errorf("vmcp Config cannot be nil")
	}

	if config.Name == "" {
		return fmt.Errorf("name is required")
	}

	if config.GroupRef == "" {
		return fmt.Errorf("groupRef is required")
	}

	ctxLogger := log.FromContext(ctx)
	ctxLogger.V(1).Info("vmcp Config validation passed", "name", config.Name)
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

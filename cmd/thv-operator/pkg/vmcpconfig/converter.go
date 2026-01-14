// Package vmcpconfig provides conversion logic from VirtualMCPServer CRD to vmcp Config
package vmcpconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/spectoconfig"
	thvjson "github.com/stacklok/toolhive/pkg/json"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/converters"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

const (
	// authzLabelValueInline is the string value for inline authz configuration
	authzLabelValueInline = "inline"
	// conflictResolutionPrefix is the string value for prefix conflict resolution strategy
	conflictResolutionPrefix = "prefix"
	// vmcpOIDCClientSecretEnvVar is the environment variable name for the OIDC client secret.
	// The deployment controller mounts secrets as environment variables with this name.
	//nolint:gosec // This is an environment variable name, not a credential
	vmcpOIDCClientSecretEnvVar = "VMCP_OIDC_CLIENT_SECRET"
)

// Converter converts VirtualMCPServer CRD specs to vmcp Config
type Converter struct {
	oidcResolver oidc.Resolver
	k8sClient    client.Client
}

// NewConverter creates a new Converter instance.
// oidcResolver is required and used to resolve OIDC configuration from various sources
// (kubernetes, configMap, inline). Use a mock resolver in tests.
// k8sClient is required for resolving MCPToolConfig references and fetching referenced
// VirtualMCPCompositeToolDefinition resources.
// Returns an error if oidcResolver or k8sClient is nil.
func NewConverter(oidcResolver oidc.Resolver, k8sClient client.Client) (*Converter, error) {
	if oidcResolver == nil {
		return nil, fmt.Errorf("oidcResolver is required")
	}
	if k8sClient == nil {
		return nil, fmt.Errorf("k8sClient is required")
	}
	return &Converter{
		oidcResolver: oidcResolver,
		k8sClient:    k8sClient,
	}, nil
}

// Convert converts VirtualMCPServer CRD spec to vmcp Config.
//
// The conversion starts with a DeepCopy of the embedded config.Config from the CRD spec.
// This ensures that simple fields (like Optimizer, Metadata, etc.) are automatically
// passed through without explicit mapping. Only fields that require special handling
// (auth, aggregation, composite tools, telemetry) are explicitly converted below.
func (c *Converter) Convert(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*vmcpconfig.Config, error) {
	// Start with a deep copy of the embedded config for automatic field passthrough.
	// This ensures new fields added to config.Config are automatically included
	// without requiring explicit mapping in this converter.
	config := vmcp.Spec.Config.DeepCopy()

	// Override name with the CR name (authoritative source)
	config.Name = vmcp.Name

	// Convert IncomingAuth - required field, no defaults
	if vmcp.Spec.IncomingAuth != nil {
		incomingAuth, err := c.convertIncomingAuth(ctx, vmcp)
		if err != nil {
			return nil, fmt.Errorf("failed to convert incoming auth: %w", err)
		}
		config.IncomingAuth = incomingAuth
	}

	// Convert OutgoingAuth - always set with defaults if not specified
	if vmcp.Spec.OutgoingAuth != nil {
		outgoingAuth, err := c.convertOutgoingAuth(ctx, vmcp)
		if err != nil {
			return nil, fmt.Errorf("failed to convert outgoing auth: %w", err)
		}
		config.OutgoingAuth = outgoingAuth
	} else {
		// Provide default outgoing auth config
		config.OutgoingAuth = &vmcpconfig.OutgoingAuthConfig{
			Source: "discovered", // Default to discovered mode
		}
	}

	// Convert Aggregation - always set with defaults if not specified
	if vmcp.Spec.Aggregation != nil {
		agg, err := c.convertAggregation(ctx, vmcp)
		if err != nil {
			return nil, fmt.Errorf("failed to convert aggregation config: %w", err)
		}
		config.Aggregation = agg
	} else {
		// Provide default aggregation config with prefix conflict resolution
		config.Aggregation = &vmcpconfig.AggregationConfig{
			ConflictResolution: conflictResolutionPrefix, // Default to prefix strategy
			ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
				PrefixFormat: "{workload}_", // Default prefix format
			},
		}
	}

	// Convert CompositeTools (inline and referenced)
	compositeTools, err := c.convertAllCompositeTools(ctx, vmcp)
	if err != nil {
		return nil, fmt.Errorf("failed to convert composite tools: %w", err)
	}
	if len(compositeTools) > 0 {
		config.CompositeTools = compositeTools
	}

	// Convert Operational
	if vmcp.Spec.Operational != nil {
		config.Operational = c.convertOperational(ctx, vmcp)
	}

	config.Telemetry = spectoconfig.ConvertTelemetryConfig(
		ctx,
		telemetryConfigFromEmbedded(vmcp.Spec.Config.Telemetry),
		vmcp.Name,
	)

	if vmcp.Spec.Config.Audit != nil && vmcp.Spec.Config.Audit.Enabled {
		config.Audit = vmcp.Spec.Config.Audit
	}

	if config.Audit != nil && config.Audit.Component == "" {
		config.Audit.Component = vmcp.Name
	}

	// Apply operational defaults (fills missing values)
	config.EnsureOperationalDefaults()

	return config, nil
}

// convertIncomingAuth converts IncomingAuthConfig from CRD to vmcp config.
func (c *Converter) convertIncomingAuth(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*vmcpconfig.IncomingAuthConfig, error) {
	ctxLogger := log.FromContext(ctx)

	incoming := &vmcpconfig.IncomingAuthConfig{
		Type: vmcp.Spec.IncomingAuth.Type,
	}

	// Convert OIDC configuration if present
	if vmcp.Spec.IncomingAuth.OIDCConfig != nil {
		// Use the OIDC resolver to handle all OIDC types (kubernetes, configMap, inline)
		// VirtualMCPServer implements OIDCConfigurable, so the resolver can work with it directly
		resolvedConfig, err := c.oidcResolver.Resolve(ctx, vmcp)
		if err != nil {
			ctxLogger.Error(err, "failed to resolve OIDC config",
				"vmcp", vmcp.Name,
				"namespace", vmcp.Namespace,
				"oidcType", vmcp.Spec.IncomingAuth.OIDCConfig.Type)
			// Fail closed: return error when OIDC is configured but resolution fails
			// This prevents deploying without authentication when OIDC is explicitly requested
			return nil, fmt.Errorf("OIDC resolution failed for type %q: %w",
				vmcp.Spec.IncomingAuth.OIDCConfig.Type, err)
		}
		if resolvedConfig != nil {
			incoming.OIDC = mapResolvedOIDCToVmcpConfig(resolvedConfig, vmcp.Spec.IncomingAuth.OIDCConfig)
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

	return incoming, nil
}

// mapResolvedOIDCToVmcpConfig maps from oidc.OIDCConfig (resolved by the OIDC resolver)
// to vmcpconfig.OIDCConfig (used by the vmcp runtime).
// This keeps the vmcp config types separate from the operator's OIDC resolver types,
// maintaining clean architectural boundaries while enabling unified OIDC resolution.
func mapResolvedOIDCToVmcpConfig(
	resolved *oidc.OIDCConfig,
	oidcConfigRef *mcpv1alpha1.OIDCConfigRef,
) *vmcpconfig.OIDCConfig {
	if resolved == nil {
		return nil
	}

	config := &vmcpconfig.OIDCConfig{
		Issuer:                          resolved.Issuer,
		ClientID:                        resolved.ClientID,
		Audience:                        resolved.Audience,
		Resource:                        resolved.ResourceURL,
		ProtectedResourceAllowPrivateIP: resolved.JWKSAllowPrivateIP,
		InsecureAllowHTTP:               resolved.InsecureAllowHTTP,
		Scopes:                          resolved.Scopes,
	}

	// Handle client secret - the deployment controller mounts secrets as environment variables
	// We need to set ClientSecretEnv for all OIDC config types that may have a client secret
	if oidcConfigRef != nil {
		switch oidcConfigRef.Type {
		case mcpv1alpha1.OIDCConfigTypeInline:
			// Inline config: check if ClientSecretRef or ClientSecret is set
			if oidcConfigRef.Inline != nil {
				if oidcConfigRef.Inline.ClientSecretRef != nil || oidcConfigRef.Inline.ClientSecret != "" {
					config.ClientSecretEnv = vmcpOIDCClientSecretEnvVar
				}
			}
		case mcpv1alpha1.OIDCConfigTypeConfigMap:
			// ConfigMap config: check if the resolved config has a client secret
			// Note: Storing secrets in ConfigMaps is not recommended; use inline with SecretRef instead
			if resolved.ClientSecret != "" {
				config.ClientSecretEnv = vmcpOIDCClientSecretEnvVar
			}
			// OIDCConfigTypeKubernetes does not use client secrets (uses service account tokens)
		}
	}

	return config
}

// convertOutgoingAuth converts OutgoingAuthConfig from CRD to vmcp config
func (c *Converter) convertOutgoingAuth(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*vmcpconfig.OutgoingAuthConfig, error) {
	outgoing := &vmcpconfig.OutgoingAuthConfig{
		Source:   vmcp.Spec.OutgoingAuth.Source,
		Backends: make(map[string]*authtypes.BackendAuthStrategy),
	}

	// Convert Default
	if vmcp.Spec.OutgoingAuth.Default != nil {
		defaultStrategy, err := c.convertBackendAuthConfig(ctx, vmcp, "default", vmcp.Spec.OutgoingAuth.Default)
		if err != nil {
			return nil, fmt.Errorf("failed to convert default backend auth: %w", err)
		}
		outgoing.Default = defaultStrategy
	}

	// Convert per-backend overrides
	for backendName, backendAuth := range vmcp.Spec.OutgoingAuth.Backends {
		strategy, err := c.convertBackendAuthConfig(ctx, vmcp, backendName, &backendAuth)
		if err != nil {
			return nil, fmt.Errorf("failed to convert backend auth for %s: %w", backendName, err)
		}
		outgoing.Backends[backendName] = strategy
	}

	return outgoing, nil
}

// convertBackendAuthConfig converts BackendAuthConfig from CRD to vmcp config
func (c *Converter) convertBackendAuthConfig(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	backendName string,
	crdConfig *mcpv1alpha1.BackendAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	// If type is "discovered", return unauthenticated strategy
	if crdConfig.Type == mcpv1alpha1.BackendAuthTypeDiscovered {
		return &authtypes.BackendAuthStrategy{
			Type: authtypes.StrategyTypeUnauthenticated,
		}, nil
	}

	// If type is "external_auth_config_ref", resolve the MCPExternalAuthConfig
	if crdConfig.Type == mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef {
		if crdConfig.ExternalAuthConfigRef == nil {
			return nil, fmt.Errorf("backend %s: external_auth_config_ref type requires externalAuthConfigRef field", backendName)
		}

		// Fetch the MCPExternalAuthConfig resource
		externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
		err := c.k8sClient.Get(ctx, types.NamespacedName{
			Name:      crdConfig.ExternalAuthConfigRef.Name,
			Namespace: vmcp.Namespace,
		}, externalAuthConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to get MCPExternalAuthConfig %s/%s: %w",
				vmcp.Namespace, crdConfig.ExternalAuthConfigRef.Name, err)
		}

		// Convert the external auth config to backend auth strategy
		return c.convertExternalAuthConfigToStrategy(ctx, externalAuthConfig)
	}

	// Unknown type
	return nil, fmt.Errorf("backend %s: unknown auth type %q", backendName, crdConfig.Type)
}

// convertExternalAuthConfigToStrategy converts MCPExternalAuthConfig to BackendAuthStrategy.
// This uses the converter registry to consolidate conversion logic and apply token type normalization consistently.
// The registry pattern makes adding new auth types easier and ensures conversion happens in one place.
func (*Converter) convertExternalAuthConfigToStrategy(
	_ context.Context,
	externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	// Use the converter registry to convert to typed strategy
	registry := converters.DefaultRegistry()
	converter, err := registry.GetConverter(externalAuthConfig.Spec.Type)
	if err != nil {
		return nil, err
	}

	// Convert to typed BackendAuthStrategy (applies token type normalization)
	strategy, err := converter.ConvertToStrategy(externalAuthConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to convert external auth config to strategy: %w", err)
	}

	// Enrich with unique env var names per ExternalAuthConfig to avoid conflicts
	// when multiple configs of the same type reference different secrets
	if strategy.TokenExchange != nil &&
		externalAuthConfig.Spec.TokenExchange != nil &&
		externalAuthConfig.Spec.TokenExchange.ClientSecretRef != nil {
		strategy.TokenExchange.ClientSecretEnv = controllerutil.GenerateUniqueTokenExchangeEnvVarName(externalAuthConfig.Name)
	}
	if strategy.HeaderInjection != nil &&
		externalAuthConfig.Spec.HeaderInjection != nil &&
		externalAuthConfig.Spec.HeaderInjection.ValueSecretRef != nil {
		strategy.HeaderInjection.HeaderValueEnv = controllerutil.GenerateUniqueHeaderInjectionEnvVarName(externalAuthConfig.Name)
	}

	return strategy, nil
}

// convertAggregation converts AggregationConfig from CRD to vmcp config
func (c *Converter) convertAggregation(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*vmcpconfig.AggregationConfig, error) {
	agg := &vmcpconfig.AggregationConfig{}

	c.convertConflictResolution(vmcp, agg)
	if err := c.convertToolConfigs(ctx, vmcp, agg); err != nil {
		return nil, err
	}

	return agg, nil
}

// convertConflictResolution converts conflict resolution strategy and config
func (*Converter) convertConflictResolution(
	vmcp *mcpv1alpha1.VirtualMCPServer,
	agg *vmcpconfig.AggregationConfig,
) {
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
	} else {
		// For other strategies (manual, priority), provide an empty config
		// The validator requires a non-nil config for all strategies
		agg.ConflictResolutionConfig = &vmcpconfig.ConflictResolutionConfig{}
	}
}

// convertToolConfigs converts per-workload tool configurations
func (c *Converter) convertToolConfigs(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	agg *vmcpconfig.AggregationConfig,
) error {
	if len(vmcp.Spec.Aggregation.Tools) == 0 {
		return nil
	}

	ctxLogger := log.FromContext(ctx)
	agg.Tools = make([]*vmcpconfig.WorkloadToolConfig, 0, len(vmcp.Spec.Aggregation.Tools))

	for _, toolConfig := range vmcp.Spec.Aggregation.Tools {
		wtc := &vmcpconfig.WorkloadToolConfig{
			Workload: toolConfig.Workload,
			Filter:   toolConfig.Filter,
		}

		if err := c.applyToolConfigRef(ctx, ctxLogger, vmcp, toolConfig, wtc); err != nil {
			return err
		}
		c.applyInlineOverrides(toolConfig, wtc)

		agg.Tools = append(agg.Tools, wtc)
	}
	return nil
}

// applyToolConfigRef resolves and applies MCPToolConfig reference
func (c *Converter) applyToolConfigRef(
	ctx context.Context,
	ctxLogger logr.Logger,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	toolConfig mcpv1alpha1.WorkloadToolConfig,
	wtc *vmcpconfig.WorkloadToolConfig,
) error {
	if toolConfig.ToolConfigRef == nil {
		return nil
	}

	resolvedConfig, err := c.resolveMCPToolConfig(ctx, vmcp.Namespace, toolConfig.ToolConfigRef.Name)
	if err != nil {
		ctxLogger.Error(err, "failed to resolve MCPToolConfig reference",
			"workload", toolConfig.Workload,
			"toolConfigRef", toolConfig.ToolConfigRef.Name)
		// Fail closed: return error when MCPToolConfig is configured but resolution fails
		// This prevents deploying without tool filtering when explicit configuration is requested
		return fmt.Errorf("MCPToolConfig resolution failed for %q: %w",
			toolConfig.ToolConfigRef.Name, err)
	}

	// Note: resolveMCPToolConfig never returns (nil, nil) - it either succeeds with
	// (toolConfig, nil) or fails with (nil, error), so no nil check needed here

	c.mergeToolConfigFilter(wtc, resolvedConfig)
	c.mergeToolConfigOverrides(wtc, resolvedConfig)
	return nil
}

// mergeToolConfigFilter merges filter from MCPToolConfig
func (*Converter) mergeToolConfigFilter(
	wtc *vmcpconfig.WorkloadToolConfig,
	resolvedConfig *mcpv1alpha1.MCPToolConfig,
) {
	if len(wtc.Filter) == 0 && len(resolvedConfig.Spec.ToolsFilter) > 0 {
		wtc.Filter = resolvedConfig.Spec.ToolsFilter
	}
}

// mergeToolConfigOverrides merges overrides from MCPToolConfig
func (*Converter) mergeToolConfigOverrides(
	wtc *vmcpconfig.WorkloadToolConfig,
	resolvedConfig *mcpv1alpha1.MCPToolConfig,
) {
	if len(resolvedConfig.Spec.ToolsOverride) == 0 {
		return
	}

	if wtc.Overrides == nil {
		wtc.Overrides = make(map[string]*vmcpconfig.ToolOverride)
	}

	for toolName, override := range resolvedConfig.Spec.ToolsOverride {
		if _, exists := wtc.Overrides[toolName]; !exists {
			wtc.Overrides[toolName] = &vmcpconfig.ToolOverride{
				Name:        override.Name,
				Description: override.Description,
			}
		}
	}
}

// applyInlineOverrides applies inline tool overrides
func (*Converter) applyInlineOverrides(
	toolConfig mcpv1alpha1.WorkloadToolConfig,
	wtc *vmcpconfig.WorkloadToolConfig,
) {
	if len(toolConfig.Overrides) == 0 {
		return
	}

	if wtc.Overrides == nil {
		wtc.Overrides = make(map[string]*vmcpconfig.ToolOverride)
	}

	for toolName, override := range toolConfig.Overrides {
		wtc.Overrides[toolName] = &vmcpconfig.ToolOverride{
			Name:        override.Name,
			Description: override.Description,
		}
	}
}

// resolveMCPToolConfig fetches an MCPToolConfig resource by name and namespace
func (c *Converter) resolveMCPToolConfig(
	ctx context.Context,
	namespace string,
	name string,
) (*mcpv1alpha1.MCPToolConfig, error) {
	toolConfig := &mcpv1alpha1.MCPToolConfig{}
	err := c.k8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, toolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPToolConfig %s/%s: %w", namespace, name, err)
	}
	return toolConfig, nil
}

// convertCompositeTools converts CompositeToolSpec from CRD to vmcp config
func (c *Converter) convertCompositeTools(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) ([]*vmcpconfig.CompositeToolConfig, error) {
	compositeTools := make([]*vmcpconfig.CompositeToolConfig, 0, len(vmcp.Spec.CompositeTools))

	for _, crdTool := range vmcp.Spec.CompositeTools {
		tool, err := c.convertCompositeToolSpec(
			ctx, crdTool.Name, crdTool.Description, crdTool.Timeout,
			crdTool.Parameters, crdTool.Steps, crdTool.Output, crdTool.Name)
		if err != nil {
			return nil, err
		}
		compositeTools = append(compositeTools, tool)
	}

	return compositeTools, nil
}

// convertAllCompositeTools converts both inline CompositeTools and referenced CompositeToolRefs,
// merging them together and validating for duplicate names.
func (c *Converter) convertAllCompositeTools(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) ([]*vmcpconfig.CompositeToolConfig, error) {
	// Convert inline composite tools
	inlineTools, err := c.convertCompositeTools(ctx, vmcp)
	if err != nil {
		return nil, fmt.Errorf("failed to convert inline composite tools: %w", err)
	}

	// Convert referenced composite tools
	var referencedTools []*vmcpconfig.CompositeToolConfig
	if len(vmcp.Spec.CompositeToolRefs) > 0 {
		referencedTools, err = c.convertReferencedCompositeTools(ctx, vmcp)
		if err != nil {
			return nil, fmt.Errorf("failed to convert referenced composite tools: %w", err)
		}
	}

	// Merge inline and referenced tools
	allTools := make([]*vmcpconfig.CompositeToolConfig, 0, len(inlineTools)+len(referencedTools))
	allTools = append(allTools, inlineTools...)
	allTools = append(allTools, referencedTools...)

	// Validate for duplicate names
	if err := validateCompositeToolNames(allTools); err != nil {
		return nil, fmt.Errorf("invalid composite tools: %w", err)
	}

	return allTools, nil
}

// convertReferencedCompositeTools fetches and converts referenced VirtualMCPCompositeToolDefinition resources.
func (c *Converter) convertReferencedCompositeTools(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) ([]*vmcpconfig.CompositeToolConfig, error) {
	referencedTools := make([]*vmcpconfig.CompositeToolConfig, 0, len(vmcp.Spec.CompositeToolRefs))

	for _, ref := range vmcp.Spec.CompositeToolRefs {
		// Fetch the referenced VirtualMCPCompositeToolDefinition
		compositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{}
		key := types.NamespacedName{
			Name:      ref.Name,
			Namespace: vmcp.Namespace,
		}

		if err := c.k8sClient.Get(ctx, key, compositeToolDef); err != nil {
			if errors.IsNotFound(err) {
				return nil, fmt.Errorf("referenced VirtualMCPCompositeToolDefinition %q not found in namespace %q: %w",
					ref.Name, vmcp.Namespace, err)
			}
			return nil, fmt.Errorf("failed to get VirtualMCPCompositeToolDefinition %q: %w", ref.Name, err)
		}

		// Convert the referenced definition to CompositeToolConfig
		tool, err := c.convertCompositeToolDefinition(ctx, compositeToolDef)
		if err != nil {
			return nil, fmt.Errorf("failed to convert referenced tool %q: %w", ref.Name, err)
		}
		referencedTools = append(referencedTools, tool)
	}

	return referencedTools, nil
}

// convertCompositeToolDefinition converts a VirtualMCPCompositeToolDefinition to CompositeToolConfig.
func (c *Converter) convertCompositeToolDefinition(
	ctx context.Context,
	def *mcpv1alpha1.VirtualMCPCompositeToolDefinition,
) (*vmcpconfig.CompositeToolConfig, error) {
	return c.convertCompositeToolSpec(
		ctx, def.Spec.Name, def.Spec.Description, def.Spec.Timeout,
		def.Spec.Parameters, def.Spec.Steps, def.Spec.Output, def.Name)
}

// convertCompositeToolSpec is a shared helper that converts common composite tool fields to CompositeToolConfig.
// This eliminates code duplication between convertCompositeTools and convertCompositeToolDefinition.
func (c *Converter) convertCompositeToolSpec(
	ctx context.Context,
	name, description, timeout string,
	parameters *runtime.RawExtension,
	steps []mcpv1alpha1.WorkflowStep,
	output *mcpv1alpha1.OutputSpec,
	toolNameForLogging string,
) (*vmcpconfig.CompositeToolConfig, error) {
	tool := &vmcpconfig.CompositeToolConfig{
		Name:        name,
		Description: description,
		Steps:       make([]*vmcpconfig.WorkflowStepConfig, 0, len(steps)),
	}

	// Parse timeout
	if timeout != "" {
		if duration, err := time.ParseDuration(timeout); err != nil {
			// Log error but continue with default - validation should have caught this at admission time
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "failed to parse composite tool timeout, using default",
				"tool", toolNameForLogging, "timeout", timeout)
			// Use default timeout of 30m (matches CRD default)
			if defaultDuration, defaultErr := time.ParseDuration("30m"); defaultErr == nil {
				tool.Timeout = vmcpconfig.Duration(defaultDuration)
			}
		} else {
			tool.Timeout = vmcpconfig.Duration(duration)
		}
	}

	// Convert parameters from runtime.RawExtension to json.Map
	if parameters != nil && len(parameters.Raw) > 0 {
		params, err := thvjson.MapFromRawExtension(*parameters)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "failed to convert parameters", "tool", toolNameForLogging)
		} else {
			tool.Parameters = params
		}
	}

	// Convert steps
	workflowSteps, err := c.convertWorkflowSteps(ctx, steps, toolNameForLogging)
	if err != nil {
		return nil, fmt.Errorf("failed to convert steps for tool %q: %w", toolNameForLogging, err)
	}
	tool.Steps = workflowSteps

	// Convert output configuration
	if output != nil {
		tool.Output = convertOutputSpec(ctx, output)
	}

	return tool, nil
}

// convertWorkflowSteps converts a slice of WorkflowStep CRD objects to WorkflowStepConfig.
// nolint:gocyclo // the workflow steps contain a lot of information that needs to be converted to the vmcp config.
func (*Converter) convertWorkflowSteps(
	ctx context.Context,
	steps []mcpv1alpha1.WorkflowStep,
	toolNameForLogging string,
) ([]*vmcpconfig.WorkflowStepConfig, error) {
	workflowSteps := make([]*vmcpconfig.WorkflowStepConfig, 0, len(steps))

	for _, crdStep := range steps {
		args, err := convertArguments(crdStep.Arguments)
		if err != nil {
			return nil, fmt.Errorf("step %q: %w", crdStep.ID, err)
		}

		step := &vmcpconfig.WorkflowStepConfig{
			ID:        crdStep.ID,
			Type:      crdStep.Type,
			Tool:      crdStep.Tool,
			Arguments: args,
			Message:   crdStep.Message,
			Condition: crdStep.Condition,
			DependsOn: crdStep.DependsOn,
		}

		// Convert Schema from runtime.RawExtension to json.Map (for elicitation steps)
		if crdStep.Schema != nil && len(crdStep.Schema.Raw) > 0 {
			schema, err := thvjson.MapFromRawExtension(*crdStep.Schema)
			if err != nil {
				ctxLogger := log.FromContext(ctx)
				ctxLogger.Error(err, "failed to convert schema", "tool", toolNameForLogging, "step", crdStep.ID)
			} else {
				step.Schema = schema
			}
		}

		// Parse timeout
		if crdStep.Timeout != "" {
			if duration, err := time.ParseDuration(crdStep.Timeout); err != nil {
				// Log error but continue without step timeout - step will use tool-level timeout or no timeout
				// Validation should have caught this at admission time
				ctxLogger := log.FromContext(ctx)
				ctxLogger.Error(err, "failed to parse step timeout, step will use tool-level timeout",
					"tool", toolNameForLogging, "step", crdStep.ID, "timeout", crdStep.Timeout)
			} else {
				step.Timeout = vmcpconfig.Duration(duration)
			}
		}

		// Convert error handling
		if crdStep.OnError != nil {
			stepError := &vmcpconfig.StepErrorHandling{
				Action:     crdStep.OnError.Action,
				RetryCount: crdStep.OnError.MaxRetries,
			}
			if crdStep.OnError.RetryDelay != "" {
				if duration, err := time.ParseDuration(crdStep.OnError.RetryDelay); err != nil {
					ctxLogger := log.FromContext(ctx)
					ctxLogger.Error(err, "failed to parse retry delay",
						"step", crdStep.ID, "retryDelay", crdStep.OnError.RetryDelay)
				} else {
					stepError.RetryDelay = vmcpconfig.Duration(duration)
				}
			}
			step.OnError = stepError
		}

		// Convert elicitation response handlers
		if crdStep.OnDecline != nil {
			step.OnDecline = &vmcpconfig.ElicitationResponseConfig{
				Action: crdStep.OnDecline.Action,
			}
		}

		if crdStep.OnCancel != nil {
			step.OnCancel = &vmcpconfig.ElicitationResponseConfig{
				Action: crdStep.OnCancel.Action,
			}
		}

		// Convert default results from map[string]runtime.RawExtension to thvjson.Map
		if len(crdStep.DefaultResults) > 0 {
			defaultResults := make(map[string]any, len(crdStep.DefaultResults))
			for key, rawExt := range crdStep.DefaultResults {
				if len(rawExt.Raw) > 0 {
					var value any
					if err := json.Unmarshal(rawExt.Raw, &value); err != nil {
						return nil, fmt.Errorf("failed to unmarshal default result %q: %w", key, err)
					}
					defaultResults[key] = value
				}
			}
			step.DefaultResults = thvjson.NewMap(defaultResults)
		}

		workflowSteps = append(workflowSteps, step)
	}

	return workflowSteps, nil
}

// validateCompositeToolNames checks for duplicate tool names across all composite tools.
func validateCompositeToolNames(tools []*vmcpconfig.CompositeToolConfig) error {
	seen := make(map[string]bool)
	for _, tool := range tools {
		if seen[tool.Name] {
			return fmt.Errorf("duplicate composite tool name: %q", tool.Name)
		}
		seen[tool.Name] = true
	}
	return nil
}

// convertArguments converts arguments from runtime.RawExtension to json.Map.
// This preserves the original types (integers, booleans, arrays, objects) from the CRD.
// Returns an empty json.Map if no arguments are specified.
func convertArguments(args *runtime.RawExtension) (thvjson.Map, error) {
	if args == nil || len(args.Raw) == 0 {
		return thvjson.Map{}, nil
	}
	return thvjson.MapFromRawExtension(*args)
}

// convertOutputSpec converts OutputSpec from CRD to vmcp config OutputConfig
func convertOutputSpec(ctx context.Context, crdOutput *mcpv1alpha1.OutputSpec) *vmcpconfig.OutputConfig {
	if crdOutput == nil {
		return nil
	}

	output := &vmcpconfig.OutputConfig{
		Properties: make(map[string]vmcpconfig.OutputProperty, len(crdOutput.Properties)),
		Required:   crdOutput.Required,
	}

	// Convert properties
	for propName, propSpec := range crdOutput.Properties {
		output.Properties[propName] = convertOutputProperty(ctx, propName, propSpec)
	}

	return output
}

// convertOutputProperty converts OutputPropertySpec from CRD to vmcp config OutputProperty
func convertOutputProperty(
	ctx context.Context, propName string, crdProp mcpv1alpha1.OutputPropertySpec,
) vmcpconfig.OutputProperty {
	prop := vmcpconfig.OutputProperty{
		Type:        crdProp.Type,
		Description: crdProp.Description,
		Value:       crdProp.Value,
	}

	// Convert nested properties for object types
	if len(crdProp.Properties) > 0 {
		prop.Properties = make(map[string]vmcpconfig.OutputProperty, len(crdProp.Properties))
		for nestedName, nestedSpec := range crdProp.Properties {
			prop.Properties[nestedName] = convertOutputProperty(ctx, propName+"."+nestedName, nestedSpec)
		}
	}

	// Convert default value from runtime.RawExtension to json.Any
	if crdProp.Default != nil && len(crdProp.Default.Raw) > 0 {
		defaultVal, err := thvjson.FromRawExtension(*crdProp.Default)
		if err != nil {
			// Log warning but continue - invalid defaults will be caught at runtime
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "failed to unmarshal output property default value",
				"property", propName, "raw", string(crdProp.Default.Raw))
		} else {
			prop.Default = defaultVal
		}
	}

	return prop
}

// convertOperational converts OperationalConfig from CRD to vmcp config
func (*Converter) convertOperational(
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

// telemetryConfigFromEmbedded constructs a v1alpha1.TelemetryConfig from the embedded telemetry.Config.
// This allows reusing ConvertTelemetryConfig which applies all the normalization and defaults.
func telemetryConfigFromEmbedded(cfg *telemetry.Config) *mcpv1alpha1.TelemetryConfig {
	if cfg == nil {
		return nil
	}

	// Check if telemetry is actually configured
	if cfg.Endpoint == "" && !cfg.EnablePrometheusMetricsPath {
		return nil
	}

	telemetryCfg := &mcpv1alpha1.TelemetryConfig{}

	// Build OpenTelemetry config if endpoint is configured
	if cfg.Endpoint != "" || cfg.TracingEnabled || cfg.MetricsEnabled {
		telemetryCfg.OpenTelemetry = &mcpv1alpha1.OpenTelemetryConfig{
			Enabled:     cfg.Endpoint != "" || cfg.TracingEnabled || cfg.MetricsEnabled,
			Endpoint:    cfg.Endpoint,
			ServiceName: cfg.ServiceName,
			Insecure:    cfg.Insecure,
		}

		// Build tracing config
		if cfg.TracingEnabled || cfg.SamplingRate != "" {
			telemetryCfg.OpenTelemetry.Tracing = &mcpv1alpha1.OpenTelemetryTracingConfig{
				Enabled:      cfg.TracingEnabled,
				SamplingRate: cfg.SamplingRate,
			}
		}

		// Build metrics config
		if cfg.MetricsEnabled {
			telemetryCfg.OpenTelemetry.Metrics = &mcpv1alpha1.OpenTelemetryMetricsConfig{
				Enabled: cfg.MetricsEnabled,
			}
		}

		// Convert headers from map to slice
		if len(cfg.Headers) > 0 {
			headers := make([]string, 0, len(cfg.Headers))
			for k, v := range cfg.Headers {
				headers = append(headers, k+"="+v)
			}
			telemetryCfg.OpenTelemetry.Headers = headers
		}
	}

	// Build Prometheus config
	if cfg.EnablePrometheusMetricsPath {
		telemetryCfg.Prometheus = &mcpv1alpha1.PrometheusConfig{
			Enabled: cfg.EnablePrometheusMetricsPath,
		}
	}

	return telemetryCfg
}

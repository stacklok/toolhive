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

// Convert converts VirtualMCPServer CRD spec to vmcp Config
func (c *Converter) Convert(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*vmcpconfig.Config, error) {
	config := &vmcpconfig.Config{
		Name:  vmcp.Name,
		Group: vmcp.Spec.GroupRef.Name,
	}

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

	config.Telemetry = spectoconfig.ConvertTelemetryConfig(ctx, vmcp.Spec.Telemetry, vmcp.Name)

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
		// Scopes are not currently in oidc.OIDCConfig - should be added later
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

// convertExternalAuthConfigToStrategy converts MCPExternalAuthConfig to BackendAuthStrategy
func (*Converter) convertExternalAuthConfigToStrategy(
	_ context.Context,
	externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	strategy := &authtypes.BackendAuthStrategy{}

	switch externalAuthConfig.Spec.Type {
	case mcpv1alpha1.ExternalAuthTypeUnauthenticated:
		strategy.Type = authtypes.StrategyTypeUnauthenticated

	case mcpv1alpha1.ExternalAuthTypeHeaderInjection:
		if externalAuthConfig.Spec.HeaderInjection == nil {
			return nil, fmt.Errorf("headerInjection config is required when type is headerInjection")
		}

		strategy.Type = authtypes.StrategyTypeHeaderInjection
		strategy.HeaderInjection = &authtypes.HeaderInjectionConfig{
			HeaderName: externalAuthConfig.Spec.HeaderInjection.HeaderName,
			// The secret value will be mounted as an environment variable by the deployment controller
			// Use the same env var naming convention as the deployment controller
			HeaderValueEnv: controllerutil.GenerateUniqueHeaderInjectionEnvVarName(externalAuthConfig.Name),
		}

	case mcpv1alpha1.ExternalAuthTypeTokenExchange:
		if externalAuthConfig.Spec.TokenExchange == nil {
			return nil, fmt.Errorf("tokenExchange config is required when type is tokenExchange")
		}

		strategy.Type = authtypes.StrategyTypeTokenExchange
		strategy.TokenExchange = &authtypes.TokenExchangeConfig{
			TokenURL:         externalAuthConfig.Spec.TokenExchange.TokenURL,
			ClientID:         externalAuthConfig.Spec.TokenExchange.ClientID,
			Audience:         externalAuthConfig.Spec.TokenExchange.Audience,
			Scopes:           externalAuthConfig.Spec.TokenExchange.Scopes,
			SubjectTokenType: externalAuthConfig.Spec.TokenExchange.SubjectTokenType,
		}

		// If client secret ref is set, use an environment variable
		if externalAuthConfig.Spec.TokenExchange.ClientSecretRef != nil {
			// The secret value will be mounted as an environment variable by the deployment controller
			// Use the same env var naming convention as the deployment controller
			strategy.TokenExchange.ClientSecretEnv = controllerutil.GenerateUniqueTokenExchangeEnvVarName(externalAuthConfig.Name)
		}

	default:
		return nil, fmt.Errorf("unknown external auth type: %s", externalAuthConfig.Spec.Type)
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
) []*vmcpconfig.CompositeToolConfig {
	compositeTools := make([]*vmcpconfig.CompositeToolConfig, 0, len(vmcp.Spec.CompositeTools))

	for _, crdTool := range vmcp.Spec.CompositeTools {
		tool := c.convertCompositeToolSpec(
			ctx, crdTool.Name, crdTool.Description, crdTool.Timeout,
			crdTool.Parameters, crdTool.Steps, crdTool.Output, crdTool.Name)
		compositeTools = append(compositeTools, tool)
	}

	return compositeTools
}

// convertAllCompositeTools converts both inline CompositeTools and referenced CompositeToolRefs,
// merging them together and validating for duplicate names.
func (c *Converter) convertAllCompositeTools(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) ([]*vmcpconfig.CompositeToolConfig, error) {
	// Convert inline composite tools
	inlineTools := c.convertCompositeTools(ctx, vmcp)

	// Convert referenced composite tools
	var referencedTools []*vmcpconfig.CompositeToolConfig
	if len(vmcp.Spec.CompositeToolRefs) > 0 {
		var err error
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
		tool := c.convertCompositeToolDefinition(ctx, compositeToolDef)
		referencedTools = append(referencedTools, tool)
	}

	return referencedTools, nil
}

// convertCompositeToolDefinition converts a VirtualMCPCompositeToolDefinition to CompositeToolConfig.
func (c *Converter) convertCompositeToolDefinition(
	ctx context.Context,
	def *mcpv1alpha1.VirtualMCPCompositeToolDefinition,
) *vmcpconfig.CompositeToolConfig {
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
) *vmcpconfig.CompositeToolConfig {
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

	// Convert parameters from runtime.RawExtension to map[string]any
	if parameters != nil && len(parameters.Raw) > 0 {
		var params map[string]any
		if err := json.Unmarshal(parameters.Raw, &params); err != nil {
			// Log warning but continue - validation should have caught this at admission time
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "failed to unmarshal composite tool parameters",
				"tool", toolNameForLogging, "raw", string(parameters.Raw))
		} else {
			tool.Parameters = params
		}
	}

	// Convert steps
	tool.Steps = c.convertWorkflowSteps(ctx, steps, toolNameForLogging)

	// Convert output configuration
	if output != nil {
		tool.Output = convertOutputSpec(ctx, output)
	}

	return tool
}

// convertWorkflowSteps converts a slice of WorkflowStep CRD objects to WorkflowStepConfig.
func (*Converter) convertWorkflowSteps(
	ctx context.Context,
	steps []mcpv1alpha1.WorkflowStep,
	toolNameForLogging string,
) []*vmcpconfig.WorkflowStepConfig {
	workflowSteps := make([]*vmcpconfig.WorkflowStepConfig, 0, len(steps))

	for _, crdStep := range steps {
		step := &vmcpconfig.WorkflowStepConfig{
			ID:        crdStep.ID,
			Type:      crdStep.Type,
			Tool:      crdStep.Tool,
			Arguments: convertArguments(crdStep.Arguments),
			Message:   crdStep.Message,
			Condition: crdStep.Condition,
			DependsOn: crdStep.DependsOn,
		}

		// Convert Schema from runtime.RawExtension to map[string]any (for elicitation steps)
		if crdStep.Schema != nil && len(crdStep.Schema.Raw) > 0 {
			var schema map[string]any
			if err := json.Unmarshal(crdStep.Schema.Raw, &schema); err != nil {
				// Log warning but continue - validation should have caught this at admission time
				ctxLogger := log.FromContext(ctx)
				ctxLogger.Error(err, "failed to unmarshal step schema",
					"tool", toolNameForLogging, "step", crdStep.ID, "raw", string(crdStep.Schema.Raw))
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

		workflowSteps = append(workflowSteps, step)
	}

	return workflowSteps
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

// convertArguments converts string arguments to any type for template expansion
func convertArguments(args map[string]string) map[string]any {
	result := make(map[string]any, len(args))
	for k, v := range args {
		result[k] = v
	}
	return result
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

	// Convert default value from runtime.RawExtension to any
	// RawExtension.Raw contains JSON bytes. json.Unmarshal correctly handles:
	// - JSON strings: "hello" -> Go string "hello"
	// - JSON numbers: 42 -> Go float64(42)
	// - JSON booleans: true -> Go bool true
	// - JSON objects: {"key":"value"} -> Go map[string]any
	// - JSON arrays: [1,2,3] -> Go []any
	if crdProp.Default != nil && len(crdProp.Default.Raw) > 0 {
		var defaultVal any
		if err := json.Unmarshal(crdProp.Default.Raw, &defaultVal); err != nil {
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

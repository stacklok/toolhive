// Package vmcpconfig provides conversion logic from VirtualMCPServer CRD to vmcp Config
package vmcpconfig

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/spectoconfig"
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

// Convert converts VirtualMCPServer CRD spec to vmcp Config
func (c *Converter) Convert(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*vmcpconfig.Config, error) {
	config := &vmcpconfig.Config{
		Name:  vmcp.Name,
		Group: vmcp.Spec.Config.Group,
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
	if vmcp.Spec.Config.Aggregation != nil {
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

	// Use Operational from spec.config directly
	config.Operational = vmcp.Spec.Config.Operational

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

// convertAggregation converts AggregationConfig from config.Config, resolving ToolConfigRef references
func (c *Converter) convertAggregation(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*vmcpconfig.AggregationConfig, error) {
	// Start with a deep copy of the source config
	srcAgg := vmcp.Spec.Config.Aggregation
	agg := &vmcpconfig.AggregationConfig{
		ConflictResolution: srcAgg.ConflictResolution,
		ExcludeAllTools:    srcAgg.ExcludeAllTools,
	}

	// Apply defaults for conflict resolution
	c.applyConflictResolutionDefaults(srcAgg, agg)

	// Resolve ToolConfigRef references for each tool
	if err := c.resolveToolConfigRefs(ctx, vmcp, srcAgg, agg); err != nil {
		return nil, err
	}

	return agg, nil
}

// applyConflictResolutionDefaults applies defaults for conflict resolution
func (*Converter) applyConflictResolutionDefaults(
	srcAgg *vmcpconfig.AggregationConfig,
	agg *vmcpconfig.AggregationConfig,
) {
	// Apply default strategy if not set
	if agg.ConflictResolution == "" {
		agg.ConflictResolution = conflictResolutionPrefix
	}

	// Copy or create conflict resolution config
	if srcAgg.ConflictResolutionConfig != nil {
		agg.ConflictResolutionConfig = &vmcpconfig.ConflictResolutionConfig{
			PrefixFormat:  srcAgg.ConflictResolutionConfig.PrefixFormat,
			PriorityOrder: srcAgg.ConflictResolutionConfig.PriorityOrder,
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

// resolveToolConfigRefs resolves ToolConfigRef references in tool configurations
func (c *Converter) resolveToolConfigRefs(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	srcAgg *vmcpconfig.AggregationConfig,
	agg *vmcpconfig.AggregationConfig,
) error {
	if len(srcAgg.Tools) == 0 {
		return nil
	}

	ctxLogger := log.FromContext(ctx)
	agg.Tools = make([]*vmcpconfig.WorkloadToolConfig, 0, len(srcAgg.Tools))

	for _, toolConfig := range srcAgg.Tools {
		// Deep copy the tool config
		wtc := &vmcpconfig.WorkloadToolConfig{
			Workload:   toolConfig.Workload,
			Filter:     toolConfig.Filter,
			ExcludeAll: toolConfig.ExcludeAll,
		}

		// Copy inline overrides first
		if len(toolConfig.Overrides) > 0 {
			wtc.Overrides = make(map[string]*vmcpconfig.ToolOverride)
			for name, override := range toolConfig.Overrides {
				if override != nil {
					wtc.Overrides[name] = &vmcpconfig.ToolOverride{
						Name:        override.Name,
						Description: override.Description,
					}
				}
			}
		}

		// Resolve ToolConfigRef if present (this may merge with inline config)
		if err := c.resolveToolConfigRef(ctx, ctxLogger, vmcp.Namespace, toolConfig, wtc); err != nil {
			return err
		}

		agg.Tools = append(agg.Tools, wtc)
	}
	return nil
}

// resolveToolConfigRef resolves and applies MCPToolConfig reference
func (c *Converter) resolveToolConfigRef(
	ctx context.Context,
	ctxLogger logr.Logger,
	namespace string,
	toolConfig *vmcpconfig.WorkloadToolConfig,
	wtc *vmcpconfig.WorkloadToolConfig,
) error {
	if toolConfig.ToolConfigRef == nil {
		return nil
	}

	resolvedConfig, err := c.resolveMCPToolConfig(ctx, namespace, toolConfig.ToolConfigRef.Name)
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

// convertAllCompositeTools resolves CompositeToolRefs and merges them with inline CompositeTools.
func (c *Converter) convertAllCompositeTools(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) ([]vmcpconfig.CompositeToolConfig, error) {
	// Resolve referenced composite tools
	referencedTools, err := c.resolveCompositeToolRefs(ctx, vmcp)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve composite tool references: %w", err)
	}

	// Merge inline and referenced tools
	allTools := append(vmcp.Spec.Config.CompositeTools, referencedTools...)

	// Validate for duplicate names
	if err := validateCompositeToolNames(allTools); err != nil {
		return nil, fmt.Errorf("invalid composite tools: %w", err)
	}

	return allTools, nil
}

// resolveCompositeToolRefs fetches and converts referenced VirtualMCPCompositeToolDefinition resources.
func (c *Converter) resolveCompositeToolRefs(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) ([]vmcpconfig.CompositeToolConfig, error) {
	referencedTools := make([]vmcpconfig.CompositeToolConfig, 0, len(vmcp.Spec.Config.CompositeToolRefs))

	for i := range vmcp.Spec.Config.CompositeToolRefs {
		ref := &vmcp.Spec.Config.CompositeToolRefs[i]
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
		tool := c.convertCompositeToolDefinition(compositeToolDef)
		referencedTools = append(referencedTools, tool)
	}

	return referencedTools, nil
}

// convertCompositeToolDefinition converts a VirtualMCPCompositeToolDefinition to CompositeToolConfig.
// Since VirtualMCPCompositeToolDefinitionSpec embeds config.CompositeToolConfig directly,
// this is a simple copy operation.
func (*Converter) convertCompositeToolDefinition(
	def *mcpv1alpha1.VirtualMCPCompositeToolDefinition,
) vmcpconfig.CompositeToolConfig {
	// The spec directly embeds CompositeToolConfig, so we can return it directly
	return def.Spec.CompositeToolConfig
}

// validateCompositeToolNames checks for duplicate tool names across all composite tools.
func validateCompositeToolNames(tools []vmcpconfig.CompositeToolConfig) error {
	seen := make(map[string]bool)
	for i := range tools {
		if seen[tools[i].Name] {
			return fmt.Errorf("duplicate composite tool name: %q", tools[i].Name)
		}
		seen[tools[i].Name] = true
	}
	return nil
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

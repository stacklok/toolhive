// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package vmcpconfig provides conversion logic from VirtualMCPServer CRD to vmcp Config
package vmcpconfig

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/spectoconfig"
	"github.com/stacklok/toolhive/pkg/authserver"
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

// Convert converts VirtualMCPServer CRD spec to a vmcp Config and an optional
// auth server RunConfig.
//
// The conversion starts with a DeepCopy of the embedded config.Config from the CRD spec.
// This ensures that simple fields (like Optimizer, Metadata, etc.) are automatically
// passed through without explicit mapping. Only fields that require special handling
// (auth, aggregation, composite tools, telemetry) are explicitly converted below.
//
// telemetryCfg is the already-fetched MCPTelemetryConfig (nil when not referenced).
// It is passed in by the controller to avoid redundant API calls; normalizeTelemetry
// uses it directly instead of re-fetching.
//
// The returned Config is the serializable vMCP config. The RunConfig is non-nil only
// when AuthServerConfig is set on the VirtualMCPServer spec.
func (c *Converter) Convert(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	telemetryCfg *mcpv1beta1.MCPTelemetryConfig,
) (*vmcpconfig.Config, *authserver.RunConfig, error) {
	// Start with a deep copy of the embedded config for automatic field passthrough.
	// This ensures new fields added to config.Config are automatically included
	// without requiring explicit mapping in this converter.
	config := vmcp.Spec.Config.DeepCopy()

	// Override name with the CR name (authoritative source)
	config.Name = vmcp.Name

	// Set group from spec.groupRef (authoritative source for operator)
	config.Group = vmcp.ResolveGroupName()

	// Convert IncomingAuth - required field, no defaults
	if vmcp.Spec.IncomingAuth != nil {
		incomingAuth, err := c.convertIncomingAuth(ctx, vmcp)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to convert incoming auth: %w", err)
		}
		config.IncomingAuth = incomingAuth
	}

	// Convert OutgoingAuth - always set with defaults if not specified
	outgoingAuth, err := c.convertOutgoingAuthWithDefaults(ctx, vmcp)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to convert outgoing auth: %w", err)
	}
	config.OutgoingAuth = outgoingAuth

	// Convert Aggregation - always set with defaults if not specified
	agg, err := c.convertAggregationWithDefaults(ctx, vmcp)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to convert aggregation config: %w", err)
	}
	config.Aggregation = agg

	// Convert CompositeTools (inline and referenced)
	compositeTools, err := c.convertAllCompositeTools(ctx, vmcp)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to convert composite tools: %w", err)
	}
	if len(compositeTools) > 0 {
		config.CompositeTools = compositeTools
	}

	// Use Operational from spec.config directly
	config.Operational = vmcp.Spec.Config.Operational

	// Normalize telemetry config: prefer TelemetryConfigRef (shared MCPTelemetryConfig resource),
	// The inline config.telemetry field is no longer read by the operator.
	normalizedTelemetry := c.normalizeTelemetry(ctx, vmcp, telemetryCfg)
	config.Telemetry = normalizedTelemetry

	if vmcp.Spec.Config.Audit != nil && vmcp.Spec.Config.Audit.Enabled {
		config.Audit = vmcp.Spec.Config.Audit
	}

	if config.Audit != nil && config.Audit.Component == "" {
		config.Audit.Component = vmcp.Name
	}

	config.SessionStorage = convertSessionStorage(vmcp)
	if vmcp.Spec.RateLimiting != nil {
		rateLimiting, err := convertRateLimiting(vmcp.Spec.RateLimiting)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to convert rate limiting: %w", err)
		}
		config.RateLimiting = rateLimiting
	}

	// Apply operational defaults (fills missing values)
	config.EnsureOperationalDefaults()

	var authServerRC *authserver.RunConfig
	// Convert inline AuthServerConfig if specified.
	if vmcp.Spec.AuthServerConfig != nil {
		rc, err := c.convertAuthServerConfig(vmcp, config)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to convert auth server config: %w", err)
		}
		authServerRC = rc
	}

	return config, authServerRC, nil
}

func convertRateLimiting(rateLimiting *mcpv1beta1.RateLimitConfig) (*thvjson.Map, error) {
	if rateLimiting == nil {
		return nil, nil
	}
	raw, err := json.Marshal(rateLimiting)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	converted := thvjson.NewMap(value)
	return &converted, nil
}

// convertIncomingAuth converts IncomingAuthConfig from CRD to vmcp config.
func (c *Converter) convertIncomingAuth(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
) (*vmcpconfig.IncomingAuthConfig, error) {
	oidcConfig, err := c.resolveOIDCConfig(ctx, vmcp)
	if err != nil {
		return nil, err
	}

	incoming := &vmcpconfig.IncomingAuthConfig{
		Type: vmcp.Spec.IncomingAuth.Type,
		OIDC: oidcConfig,
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

		// When an embedded auth server with upstream providers is configured, Cedar
		// policies must evaluate claims from the upstream IDP token rather than the
		// ToolHive-issued AS token. Mirrors injectSubjectProviderIfNeeded in
		// virtualmcpserver_controller.go (outgoing auth) and
		// injectUpstreamProviderIfNeeded in pkg/runner/middleware.go (thv run path).
		// Leaving PrimaryUpstreamProvider empty (no embedded AS or no upstreams) lets
		// Cedar fall back to claims from the ToolHive-issued token.
		if vmcp.Spec.AuthServerConfig != nil && len(vmcp.Spec.AuthServerConfig.UpstreamProviders) > 0 {
			incoming.Authz.PrimaryUpstreamProvider = authserver.ResolveUpstreamName(
				vmcp.Spec.AuthServerConfig.UpstreamProviders[0].Name,
			)
		}
	}

	return incoming, nil
}

// resolveOIDCConfig resolves OIDC configuration from an MCPOIDCConfig reference.
// Returns nil when no OIDC config is present.
// Fails closed: returns an error when OIDC is configured but resolution fails,
// preventing deployment without authentication when OIDC is explicitly requested.
func (c *Converter) resolveOIDCConfig(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
) (*vmcpconfig.OIDCConfig, error) {
	if vmcp.Spec.IncomingAuth == nil {
		return nil, nil
	}

	ctxLogger := log.FromContext(ctx)

	// Resolve from MCPOIDCConfig reference
	if vmcp.Spec.IncomingAuth.OIDCConfigRef != nil {
		oidcCfg, err := controllerutil.GetOIDCConfigForServer(
			ctx, c.k8sClient, vmcp.Namespace, vmcp.Spec.IncomingAuth.OIDCConfigRef)
		if err != nil {
			return nil, fmt.Errorf("failed to get MCPOIDCConfig %s: %w",
				vmcp.Spec.IncomingAuth.OIDCConfigRef.Name, err)
		}
		resolved, err := c.oidcResolver.ResolveFromConfigRef(
			ctx, vmcp.Spec.IncomingAuth.OIDCConfigRef, oidcCfg,
			vmcp.Name, vmcp.Namespace, vmcp.GetProxyPort())
		if err != nil {
			ctxLogger.Error(err, "failed to resolve OIDC config from MCPOIDCConfig",
				"vmcp", vmcp.Name,
				"namespace", vmcp.Namespace,
				"oidcConfigRef", vmcp.Spec.IncomingAuth.OIDCConfigRef.Name)
			return nil, fmt.Errorf("OIDC resolution failed from MCPOIDCConfig %q: %w",
				vmcp.Spec.IncomingAuth.OIDCConfigRef.Name, err)
		}
		return mapResolvedOIDCToVmcpConfigFromRef(resolved, oidcCfg), nil
	}

	return nil, nil
}

// mapResolvedOIDCToVmcpConfigFromRef maps from oidc.OIDCConfig (resolved by the OIDC resolver)
// to vmcpconfig.OIDCConfig when using an MCPOIDCConfig reference.
// Client secret detection uses the MCPOIDCConfig's inline config rather than OIDCConfigRef.
func mapResolvedOIDCToVmcpConfigFromRef(
	resolved *oidc.OIDCConfig,
	oidcCfg *mcpv1beta1.MCPOIDCConfig,
) *vmcpconfig.OIDCConfig {
	if resolved == nil {
		return nil
	}

	config := &vmcpconfig.OIDCConfig{
		Issuer:                          resolved.Issuer,
		ClientID:                        resolved.ClientID,
		Audience:                        resolved.Audience,
		Resource:                        resolved.ResourceURL,
		JWKSURL:                         resolved.JWKSURL,
		IntrospectionURL:                resolved.IntrospectionURL,
		ProtectedResourceAllowPrivateIP: resolved.ProtectedResourceAllowPrivateIP,
		JwksAllowPrivateIP:              resolved.JWKSAllowPrivateIP,
		InsecureAllowHTTP:               resolved.InsecureAllowHTTP,
		Scopes:                          resolved.Scopes,
	}

	// MCPOIDCConfig inline type may have a client secret
	if oidcCfg != nil &&
		oidcCfg.Spec.Type == mcpv1beta1.MCPOIDCConfigTypeInline &&
		oidcCfg.Spec.Inline != nil &&
		oidcCfg.Spec.Inline.ClientSecretRef != nil {
		config.ClientSecretEnv = vmcpOIDCClientSecretEnvVar
	}

	return config
}

// normalizeTelemetry resolves and normalizes the telemetry config from a
// pre-fetched MCPTelemetryConfig. Returns nil when TelemetryConfigRef is not set.
// The Config.Telemetry field is still valid for standalone CLI deployments but is
// no longer read by the operator — use TelemetryConfigRef instead.
func (*Converter) normalizeTelemetry(
	_ context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	telemetryCfg *mcpv1beta1.MCPTelemetryConfig,
) *telemetry.Config {
	if vmcp.Spec.TelemetryConfigRef != nil && telemetryCfg != nil {
		return spectoconfig.NormalizeMCPTelemetryConfig(
			&telemetryCfg.Spec, vmcp.Spec.TelemetryConfigRef.ServiceName, vmcp.Name)
	}
	return nil
}

// convertSessionStorage populates SessionStorage from the VirtualMCPServer spec.
// spec.sessionStorage is the authoritative source; always overwrite whatever
// the DeepCopy brought in from spec.config.sessionStorage.
// PasswordRef is K8s-specific and is resolved separately; the password is injected
// as the THV_SESSION_REDIS_PASSWORD environment variable by the deployment builder.
func convertSessionStorage(vmcp *mcpv1beta1.VirtualMCPServer) *vmcpconfig.SessionStorageConfig {
	if vmcp.Spec.SessionStorage != nil &&
		vmcp.Spec.SessionStorage.Provider == mcpv1beta1.SessionStorageProviderRedis {
		return &vmcpconfig.SessionStorageConfig{
			Provider:  vmcp.Spec.SessionStorage.Provider,
			Address:   vmcp.Spec.SessionStorage.Address,
			DB:        vmcp.Spec.SessionStorage.DB,
			KeyPrefix: vmcp.Spec.SessionStorage.KeyPrefix,
		}
	}
	return nil
}

// convertAuthServerConfig converts the inline EmbeddedAuthServerConfig from the
// VirtualMCPServer spec into an authserver.RunConfig using the shared builder in
// controllerutil. AllowedAudiences is derived from the resolved incoming OIDC config.
func (*Converter) convertAuthServerConfig(
	vmcp *mcpv1beta1.VirtualMCPServer,
	config *vmcpconfig.Config,
) (*authserver.RunConfig, error) {
	if vmcp.Spec.AuthServerConfig == nil {
		return nil, nil
	}
	return controllerutil.BuildAuthServerRunConfig(
		vmcp.Namespace, vmcp.Name,
		vmcp.Spec.AuthServerConfig,
		deriveAllowedAudiences(config),
		deriveScopesSupported(config),
		deriveResourceURL(config),
	)
}

// deriveAllowedAudiences derives the AllowedAudiences list from the already-resolved
// vmcp Config. The CRD intentionally omits AllowedAudiences on EmbeddedAuthServerConfig
// — the converter derives it here so the auth server can validate the "resource"
// parameter (RFC 8707) on every token request.
//
// Per RFC 8707, the resource indicator is the authoritative value for token audience.
// Only Resource is used (consistent with controllerutil/authserver.go which requires
// ResourceURL). When Resource is not set, returns nil — ValidateAuthServerIntegration
// catches this as an error when AuthServerConfig is present.
//
// Using the resolved config (rather than the raw CRD spec) ensures the value is
// populated correctly for all OIDC config types (inline, configMap, kubernetes).
func deriveAllowedAudiences(config *vmcpconfig.Config) []string {
	if config.IncomingAuth == nil || config.IncomingAuth.OIDC == nil {
		return nil
	}
	resource := config.IncomingAuth.OIDC.Resource
	if resource == "" {
		return nil
	}
	return []string{resource}
}

// deriveResourceURL returns the resource URL from the resolved incoming OIDC config.
// Returns empty string when OIDC is not configured or Resource is empty.
// Used to default upstream provider RedirectURIs to {resourceURL}/oauth/callback.
func deriveResourceURL(config *vmcpconfig.Config) string {
	if config.IncomingAuth == nil || config.IncomingAuth.OIDC == nil {
		return ""
	}
	return config.IncomingAuth.OIDC.Resource
}

// deriveScopesSupported returns the scopes from the resolved incoming OIDC config.
// Returns nil when OIDC is not configured or scopes are empty, which causes the
// auth server to use its default scopes (["openid", "profile", "email", "offline_access"]).
func deriveScopesSupported(config *vmcpconfig.Config) []string {
	if config.IncomingAuth == nil || config.IncomingAuth.OIDC == nil {
		return nil
	}
	if len(config.IncomingAuth.OIDC.Scopes) == 0 {
		return nil
	}
	return config.IncomingAuth.OIDC.Scopes
}

// convertOutgoingAuthWithDefaults converts OutgoingAuthConfig or returns defaults.
func (c *Converter) convertOutgoingAuthWithDefaults(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
) (*vmcpconfig.OutgoingAuthConfig, error) {
	if vmcp.Spec.OutgoingAuth != nil {
		return c.convertOutgoingAuth(ctx, vmcp)
	}
	return &vmcpconfig.OutgoingAuthConfig{
		Source: "discovered", // Default to discovered mode
	}, nil
}

// convertAggregationWithDefaults converts AggregationConfig or returns defaults.
func (c *Converter) convertAggregationWithDefaults(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
) (*vmcpconfig.AggregationConfig, error) {
	if vmcp.Spec.Config.Aggregation != nil {
		return c.convertAggregation(ctx, vmcp)
	}
	return &vmcpconfig.AggregationConfig{
		ConflictResolution: conflictResolutionPrefix,
		ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
			PrefixFormat: "{workload}_",
		},
	}, nil
}

// convertOutgoingAuth converts OutgoingAuthConfig from CRD to vmcp config
func (c *Converter) convertOutgoingAuth(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
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
	vmcp *mcpv1beta1.VirtualMCPServer,
	backendName string,
	crdConfig *mcpv1beta1.BackendAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	// If type is "discovered", return unauthenticated strategy
	if crdConfig.Type == mcpv1beta1.BackendAuthTypeDiscovered {
		return &authtypes.BackendAuthStrategy{
			Type: authtypes.StrategyTypeUnauthenticated,
		}, nil
	}

	// If type is "externalAuthConfigRef", resolve the MCPExternalAuthConfig
	if crdConfig.Type == mcpv1beta1.BackendAuthTypeExternalAuthConfigRef {
		if crdConfig.ExternalAuthConfigRef == nil {
			return nil, fmt.Errorf("backend %s: externalAuthConfigRef type requires externalAuthConfigRef field", backendName)
		}

		// Fetch the MCPExternalAuthConfig resource
		externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{}
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
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
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
	vmcp *mcpv1beta1.VirtualMCPServer,
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
	vmcp *mcpv1beta1.VirtualMCPServer,
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
					wtc.Overrides[name] = override.DeepCopy()
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
	resolvedConfig *mcpv1beta1.MCPToolConfig,
) {
	if len(wtc.Filter) == 0 && len(resolvedConfig.Spec.ToolsFilter) > 0 {
		wtc.Filter = resolvedConfig.Spec.ToolsFilter
	}
}

// mergeToolConfigOverrides merges overrides from MCPToolConfig
func (*Converter) mergeToolConfigOverrides(
	wtc *vmcpconfig.WorkloadToolConfig,
	resolvedConfig *mcpv1beta1.MCPToolConfig,
) {
	if len(resolvedConfig.Spec.ToolsOverride) == 0 {
		return
	}

	if wtc.Overrides == nil {
		wtc.Overrides = make(map[string]*vmcpconfig.ToolOverride)
	}

	for toolName, override := range resolvedConfig.Spec.ToolsOverride {
		if _, exists := wtc.Overrides[toolName]; !exists {
			wtc.Overrides[toolName] = convertCRDToolOverride(&override)
		}
	}
}

// convertCRDToolOverride converts a CRD ToolOverride to a config ToolOverride.
func convertCRDToolOverride(src *mcpv1beta1.ToolOverride) *vmcpconfig.ToolOverride {
	o := &vmcpconfig.ToolOverride{
		Name:        src.Name,
		Description: src.Description,
	}
	if src.Annotations != nil {
		o.Annotations = &vmcpconfig.ToolAnnotationsOverride{
			Title:           src.Annotations.Title,
			ReadOnlyHint:    src.Annotations.ReadOnlyHint,
			DestructiveHint: src.Annotations.DestructiveHint,
			IdempotentHint:  src.Annotations.IdempotentHint,
			OpenWorldHint:   src.Annotations.OpenWorldHint,
		}
	}
	return o
}

// resolveMCPToolConfig fetches an MCPToolConfig resource by name and namespace
func (c *Converter) resolveMCPToolConfig(
	ctx context.Context,
	namespace string,
	name string,
) (*mcpv1beta1.MCPToolConfig, error) {
	toolConfig := &mcpv1beta1.MCPToolConfig{}
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
	vmcp *mcpv1beta1.VirtualMCPServer,
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
	vmcp *mcpv1beta1.VirtualMCPServer,
) ([]vmcpconfig.CompositeToolConfig, error) {
	referencedTools := make([]vmcpconfig.CompositeToolConfig, 0, len(vmcp.Spec.Config.CompositeToolRefs))

	for i := range vmcp.Spec.Config.CompositeToolRefs {
		ref := &vmcp.Spec.Config.CompositeToolRefs[i]
		// Fetch the referenced VirtualMCPCompositeToolDefinition
		compositeToolDef := &mcpv1beta1.VirtualMCPCompositeToolDefinition{}
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
	def *mcpv1beta1.VirtualMCPCompositeToolDefinition,
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

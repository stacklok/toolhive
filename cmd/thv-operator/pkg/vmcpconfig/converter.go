// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	"github.com/stacklok/toolhive/pkg/authserver"
	authstorage "github.com/stacklok/toolhive/pkg/authserver/storage"
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

	// secretMountBasePath is the base directory where Kubernetes secrets are mounted as volumes.
	// The deployment controller mounts each referenced secret under this path.
	secretMountBasePath = "/secrets"
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

// Convert converts VirtualMCPServer CRD spec to vmcp RuntimeConfig.
//
// The conversion starts with a DeepCopy of the embedded config.Config from the CRD spec.
// This ensures that simple fields (like Optimizer, Metadata, etc.) are automatically
// passed through without explicit mapping. Only fields that require special handling
// (auth, aggregation, composite tools, telemetry) are explicitly converted below.
//
// The returned RuntimeConfig embeds Config (the serializable config) and may additionally
// carry an AuthServer config when AuthServerConfig is set on the VirtualMCPServer spec.
func (c *Converter) Convert(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*vmcpconfig.RuntimeConfig, error) {
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

	// Normalize telemetry config using the shared spectoconfig normalization logic.
	// This applies runtime defaults and normalization (endpoint prefix stripping, service name defaults).
	// Note: Most defaults (e.g., SamplingRate="0.05", TracingEnabled=false, MetricsEnabled=false)
	// are handled by kubebuilder annotations in pkg/telemetry/config.go and applied by the API server.
	config.Telemetry = spectoconfig.NormalizeTelemetryConfig(vmcp.Spec.Config.Telemetry, vmcp.Name)

	if vmcp.Spec.Config.Audit != nil && vmcp.Spec.Config.Audit.Enabled {
		config.Audit = vmcp.Spec.Config.Audit
	}

	if config.Audit != nil && config.Audit.Component == "" {
		config.Audit.Component = vmcp.Name
	}

	// Apply operational defaults (fills missing values)
	config.EnsureOperationalDefaults()

	rtCfg := &vmcpconfig.RuntimeConfig{Config: *config}

	// Convert inline AuthServerConfig if specified.
	if vmcp.Spec.AuthServerConfig != nil {
		authServerCfg, err := c.convertAuthServerConfig(vmcp, config)
		if err != nil {
			return nil, fmt.Errorf("failed to convert auth server config: %w", err)
		}
		rtCfg.AuthServer = authServerCfg
	}

	return rtCfg, nil
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

// convertAuthServerConfig converts the inline EmbeddedAuthServerConfig from the
// VirtualMCPServer spec to an authserver.RunConfig wrapped in AuthServerConfig.
//
// Secret references are converted to file paths following the convention
// /secrets/{secret-name}/{key}, which is the path used by the deployment controller
// when mounting Kubernetes secrets as volumes into the vMCP pod.
//
// AllowedAudiences is derived from the incoming OIDC Resource (RFC 8707) or Audience.
func (*Converter) convertAuthServerConfig(
	vmcp *mcpv1alpha1.VirtualMCPServer,
	config *vmcpconfig.Config,
) (*vmcpconfig.AuthServerConfig, error) {
	embCfg := vmcp.Spec.AuthServerConfig
	if embCfg == nil {
		return nil, nil
	}

	rc, err := buildAuthServerRunConfig(embCfg, vmcp)
	if err != nil {
		return nil, err
	}
	rc.AllowedAudiences = deriveAllowedAudiences(config)

	return vmcpconfig.NewAuthServerConfig(rc), nil
}

// buildAuthServerRunConfig maps an EmbeddedAuthServerConfig to an authserver.RunConfig.
// Secret refs are converted to mounted file paths; all sub-conversions are delegated to
// focused helpers to keep cyclomatic complexity manageable.
func buildAuthServerRunConfig(
	embCfg *mcpv1alpha1.EmbeddedAuthServerConfig,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*authserver.RunConfig, error) {
	rc := &authserver.RunConfig{
		SchemaVersion: authserver.CurrentSchemaVersion,
		Issuer:        embCfg.Issuer,
	}

	// Map signing key secret refs to file paths.
	// The first ref becomes the primary signing key; subsequent refs are fallback keys.
	if len(embCfg.SigningKeySecretRefs) > 0 {
		rc.SigningKeyConfig = convertSigningKeyRefs(embCfg.SigningKeySecretRefs)
	}

	// Map HMAC secret refs to file paths using the same mount paths as GenerateAuthServerVolumes.
	for i := range embCfg.HMACSecretRefs {
		filePath := fmt.Sprintf("%s/"+controllerutil.AuthServerHMACFilePattern,
			controllerutil.AuthServerHMACMountPath, i)
		rc.HMACSecretFiles = append(rc.HMACSecretFiles, filePath)
	}

	// Map token lifespans if configured.
	if embCfg.TokenLifespans != nil {
		rc.TokenLifespans = &authserver.TokenLifespanRunConfig{
			AccessTokenLifespan:  embCfg.TokenLifespans.AccessTokenLifespan,
			RefreshTokenLifespan: embCfg.TokenLifespans.RefreshTokenLifespan,
			AuthCodeLifespan:     embCfg.TokenLifespans.AuthCodeLifespan,
		}
	}

	// Convert upstream providers.
	upstreams, err := convertUpstreamProviders(embCfg.UpstreamProviders)
	if err != nil {
		return nil, fmt.Errorf("failed to convert upstream providers: %w", err)
	}
	rc.Upstreams = upstreams

	// Convert storage config.
	if embCfg.Storage != nil {
		storageCfg, err := convertAuthServerStorage(embCfg.Storage, vmcp)
		if err != nil {
			return nil, fmt.Errorf("failed to convert auth server storage: %w", err)
		}
		rc.Storage = storageCfg
	}

	return rc, nil
}

// convertSigningKeyRefs maps a slice of SecretKeyRefs to a SigningKeyRunConfig.
// The first ref is the primary signing key; subsequent refs become fallback keys for rotation.
func convertSigningKeyRefs(refs []mcpv1alpha1.SecretKeyRef) *authserver.SigningKeyRunConfig {
	if len(refs) == 0 {
		return nil
	}
	// Use the same mount paths as GenerateAuthServerVolumes (controllerutil).
	cfg := &authserver.SigningKeyRunConfig{
		KeyDir:         controllerutil.AuthServerKeysMountPath,
		SigningKeyFile: fmt.Sprintf(controllerutil.AuthServerKeyFilePattern, 0),
	}
	for i := range refs[1:] {
		fallbackPath := fmt.Sprintf("%s/"+controllerutil.AuthServerKeyFilePattern,
			controllerutil.AuthServerKeysMountPath, i+1)
		cfg.FallbackKeyFiles = append(cfg.FallbackKeyFiles, fallbackPath)
	}
	return cfg
}

// deriveAllowedAudiences derives the AllowedAudiences list from the already-resolved
// vmcp Config. The CRD intentionally omits AllowedAudiences on EmbeddedAuthServerConfig
// — the converter derives it here so the auth server can validate the "resource"
// parameter (RFC 8707) on every token request.
//
// Per RFC 8707, the resource indicator is the authoritative value for token audience.
// When Resource is set, it takes precedence over Audience (consistent with
// controllerutil/authserver.go which uses ResourceURL for AllowedAudiences).
// Falls back to Audience when Resource is not specified.
//
// Using the resolved config (rather than the raw CRD spec) ensures the value is
// populated correctly for all OIDC config types (inline, configMap, kubernetes).
func deriveAllowedAudiences(config *vmcpconfig.Config) []string {
	if config.IncomingAuth == nil || config.IncomingAuth.OIDC == nil {
		return nil
	}
	oidcCfg := config.IncomingAuth.OIDC
	// Resource (RFC 8707) takes precedence, falling back to Audience.
	value := oidcCfg.Resource
	if value == "" {
		value = oidcCfg.Audience
	}
	if value == "" {
		return nil
	}
	return []string{value}
}

// convertUpstreamProviders converts a slice of CRD UpstreamProviderConfig to
// authserver UpstreamRunConfig values.
func convertUpstreamProviders(
	providers []mcpv1alpha1.UpstreamProviderConfig,
) ([]authserver.UpstreamRunConfig, error) {
	upstreams := make([]authserver.UpstreamRunConfig, 0, len(providers))
	for i, provider := range providers {
		up, err := convertUpstreamProvider(&providers[i])
		if err != nil {
			return nil, fmt.Errorf("upstream provider %q (index %d): %w", provider.Name, i, err)
		}
		// Override client secret reference to use env var (matching GenerateAuthServerEnvVars)
		// instead of file path. The deployment controller mounts secrets as indexed env vars.
		envVarName := fmt.Sprintf("%s_%d", controllerutil.UpstreamClientSecretEnvVar, i)
		if up.OIDCConfig != nil && up.OIDCConfig.ClientSecretFile != "" {
			up.OIDCConfig.ClientSecretFile = ""
			up.OIDCConfig.ClientSecretEnvVar = envVarName
		}
		if up.OAuth2Config != nil && up.OAuth2Config.ClientSecretFile != "" {
			up.OAuth2Config.ClientSecretFile = ""
			up.OAuth2Config.ClientSecretEnvVar = envVarName
		}
		upstreams = append(upstreams, up)
	}
	return upstreams, nil
}

// convertUpstreamProvider converts a single CRD UpstreamProviderConfig to an
// authserver.UpstreamRunConfig.
func convertUpstreamProvider(p *mcpv1alpha1.UpstreamProviderConfig) (authserver.UpstreamRunConfig, error) {
	up := authserver.UpstreamRunConfig{
		Name: p.Name,
		// Convert between the CRD UpstreamProviderType and authserver.UpstreamProviderType.
		// Both use the same string values ("oidc" / "oauth2") but are distinct Go types.
		Type: authserver.UpstreamProviderType(p.Type),
	}

	switch p.Type {
	case mcpv1alpha1.UpstreamProviderTypeOIDC:
		if p.OIDCConfig == nil {
			return authserver.UpstreamRunConfig{}, fmt.Errorf("oidcConfig is required for type 'oidc'")
		}
		up.OIDCConfig = convertOIDCUpstreamConfig(p.OIDCConfig)

	case mcpv1alpha1.UpstreamProviderTypeOAuth2:
		if p.OAuth2Config == nil {
			return authserver.UpstreamRunConfig{}, fmt.Errorf("oauth2Config is required for type 'oauth2'")
		}
		oauth2Cfg, err := convertOAuth2UpstreamConfig(p.OAuth2Config)
		if err != nil {
			return authserver.UpstreamRunConfig{}, err
		}
		up.OAuth2Config = oauth2Cfg

	default:
		return authserver.UpstreamRunConfig{}, fmt.Errorf("unsupported upstream provider type: %q", p.Type)
	}

	return up, nil
}

// convertOIDCUpstreamConfig maps a CRD OIDCUpstreamConfig to an authserver OIDCUpstreamRunConfig.
func convertOIDCUpstreamConfig(cfg *mcpv1alpha1.OIDCUpstreamConfig) *authserver.OIDCUpstreamRunConfig {
	if cfg == nil {
		return nil
	}
	rc := &authserver.OIDCUpstreamRunConfig{
		IssuerURL:   cfg.IssuerURL,
		ClientID:    cfg.ClientID,
		RedirectURI: cfg.RedirectURI,
		Scopes:      cfg.Scopes,
	}
	// Map client secret ref to a mounted file path.
	if cfg.ClientSecretRef != nil {
		rc.ClientSecretFile = secretRefToFilePath(cfg.ClientSecretRef)
	}
	// Map optional UserInfo override.
	if cfg.UserInfoOverride != nil {
		rc.UserInfoOverride = convertUserInfoConfig(cfg.UserInfoOverride)
	}
	return rc
}

// convertOAuth2UpstreamConfig maps a CRD OAuth2UpstreamConfig to an authserver OAuth2UpstreamRunConfig.
func convertOAuth2UpstreamConfig(cfg *mcpv1alpha1.OAuth2UpstreamConfig) (*authserver.OAuth2UpstreamRunConfig, error) {
	if cfg == nil {
		return nil, nil
	}
	if cfg.UserInfo == nil {
		return nil, fmt.Errorf("userInfo is required for OAuth2 upstream provider")
	}
	rc := &authserver.OAuth2UpstreamRunConfig{
		AuthorizationEndpoint: cfg.AuthorizationEndpoint,
		TokenEndpoint:         cfg.TokenEndpoint,
		ClientID:              cfg.ClientID,
		RedirectURI:           cfg.RedirectURI,
		Scopes:                cfg.Scopes,
		UserInfo:              convertUserInfoConfig(cfg.UserInfo),
	}
	// Map client secret ref to a mounted file path.
	if cfg.ClientSecretRef != nil {
		rc.ClientSecretFile = secretRefToFilePath(cfg.ClientSecretRef)
	}
	// Map optional token response field mapping.
	if cfg.TokenResponseMapping != nil {
		rc.TokenResponseMapping = &authserver.TokenResponseMappingRunConfig{
			AccessTokenPath:  cfg.TokenResponseMapping.AccessTokenPath,
			ScopePath:        cfg.TokenResponseMapping.ScopePath,
			RefreshTokenPath: cfg.TokenResponseMapping.RefreshTokenPath,
			ExpiresInPath:    cfg.TokenResponseMapping.ExpiresInPath,
		}
	}
	return rc, nil
}

// convertUserInfoConfig maps a CRD UserInfoConfig to an authserver UserInfoRunConfig.
func convertUserInfoConfig(cfg *mcpv1alpha1.UserInfoConfig) *authserver.UserInfoRunConfig {
	if cfg == nil {
		return nil
	}
	rc := &authserver.UserInfoRunConfig{
		EndpointURL:       cfg.EndpointURL,
		HTTPMethod:        cfg.HTTPMethod,
		AdditionalHeaders: cfg.AdditionalHeaders,
	}
	if cfg.FieldMapping != nil {
		rc.FieldMapping = &authserver.UserInfoFieldMappingRunConfig{
			SubjectFields: cfg.FieldMapping.SubjectFields,
			NameFields:    cfg.FieldMapping.NameFields,
			EmailFields:   cfg.FieldMapping.EmailFields,
		}
	}
	return rc
}

// convertAuthServerStorage maps a CRD AuthServerStorageConfig to a storage.RunConfig.
func convertAuthServerStorage(
	cfg *mcpv1alpha1.AuthServerStorageConfig,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*authstorage.RunConfig, error) {
	if cfg == nil {
		return nil, nil
	}
	rc := &authstorage.RunConfig{
		Type: string(cfg.Type),
	}
	if cfg.Type == mcpv1alpha1.AuthServerStorageTypeRedis {
		if cfg.Redis == nil {
			return nil, fmt.Errorf("redis storage config is required when type is 'redis'")
		}
		redisRC, err := convertRedisStorageConfig(cfg.Redis, vmcp)
		if err != nil {
			return nil, fmt.Errorf("redis storage config: %w", err)
		}
		rc.RedisConfig = redisRC
	}
	return rc, nil
}

// convertRedisStorageConfig maps a CRD RedisStorageConfig to a storage.RedisRunConfig.
func convertRedisStorageConfig(
	cfg *mcpv1alpha1.RedisStorageConfig,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*authstorage.RedisRunConfig, error) {
	if cfg == nil {
		return nil, nil
	}
	if cfg.SentinelConfig == nil {
		return nil, fmt.Errorf("sentinelConfig is required")
	}
	if cfg.ACLUserConfig == nil {
		return nil, fmt.Errorf("aclUserConfig is required")
	}

	sentinel, err := convertRedisSentinelConfig(cfg.SentinelConfig, vmcp.Namespace)
	if err != nil {
		return nil, fmt.Errorf("sentinelConfig: %w", err)
	}

	rc := &authstorage.RedisRunConfig{
		SentinelConfig: sentinel,
		AuthType:       authstorage.AuthTypeACLUser,
		ACLUserConfig: &authstorage.ACLUserRunConfig{
			UsernameEnvVar: secretRefToEnvVarName(cfg.ACLUserConfig.UsernameSecretRef),
			PasswordEnvVar: secretRefToEnvVarName(cfg.ACLUserConfig.PasswordSecretRef),
		},
		// KeyPrefix scopes all Redis keys to this CR's namespace and name,
		// preventing cross-tenant key collisions in shared Redis clusters.
		KeyPrefix:    fmt.Sprintf("thv:auth:%s:%s:", vmcp.Namespace, vmcp.Name),
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	// Map optional TLS configs.
	if cfg.TLS != nil {
		rc.TLS = convertRedisTLSConfig(cfg.TLS)
	}
	if cfg.SentinelTLS != nil {
		rc.SentinelTLS = convertRedisTLSConfig(cfg.SentinelTLS)
	}

	return rc, nil
}

// convertRedisSentinelConfig maps a CRD RedisSentinelConfig to a storage.SentinelRunConfig.
// SentinelService references are resolved to addresses using Kubernetes DNS convention
// ({service}.{namespace}.svc.cluster.local:{port}).
//
// crNamespace is the namespace of the VirtualMCPServer CR; it is used as the default
// when SentinelService.Namespace is empty, co-locating the sentinel service in the same
// namespace as the CR rather than falling back to "default".
func convertRedisSentinelConfig(
	cfg *mcpv1alpha1.RedisSentinelConfig,
	crNamespace string,
) (*authstorage.SentinelRunConfig, error) {
	if cfg == nil {
		return nil, nil
	}

	rc := &authstorage.SentinelRunConfig{
		MasterName: cfg.MasterName,
		DB:         int(cfg.DB),
	}

	switch {
	case len(cfg.SentinelAddrs) > 0:
		rc.SentinelAddrs = cfg.SentinelAddrs
	case cfg.SentinelService != nil:
		svc := cfg.SentinelService
		port := svc.Port
		if port == 0 {
			port = 26379
		}
		ns := svc.Namespace
		if ns == "" {
			// Default to the CR's own namespace so the sentinel service is looked up
			// in the same namespace as the VirtualMCPServer, not the "default" namespace.
			ns = crNamespace
		}
		addr := fmt.Sprintf("%s.%s.svc.cluster.local:%d", svc.Name, ns, port)
		rc.SentinelAddrs = []string{addr}
	default:
		return nil, fmt.Errorf("either sentinelAddrs or sentinelService must be set")
	}

	return rc, nil
}

// convertRedisTLSConfig maps a CRD RedisTLSConfig to a storage.RedisTLSRunConfig.
func convertRedisTLSConfig(cfg *mcpv1alpha1.RedisTLSConfig) *authstorage.RedisTLSRunConfig {
	if cfg == nil {
		return nil
	}
	rc := &authstorage.RedisTLSRunConfig{
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}
	if cfg.CACertSecretRef != nil {
		rc.CACertFile = secretRefToFilePath(cfg.CACertSecretRef)
	}
	return rc
}

// secretRefToFilePath converts a SecretKeyRef to a mounted file path.
// The deployment controller mounts each referenced Kubernetes Secret as a volume
// under /secrets/{secret-name}/, so the key becomes a file in that directory.
func secretRefToFilePath(ref *mcpv1alpha1.SecretKeyRef) string {
	if ref == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s/%s", secretMountBasePath, ref.Name, ref.Key)
}

// secretRefToEnvVarName converts a SecretKeyRef to a deterministic environment variable name.
// This is used for Redis ACL credentials which are passed via environment variables.
// The generated name is upper-cased and uses underscores in place of hyphens.
func secretRefToEnvVarName(ref *mcpv1alpha1.SecretKeyRef) string {
	if ref == nil {
		return ""
	}
	// Produce a stable, shell-safe environment variable name from the secret name and key.
	// Example: secret "my-redis-secret", key "password" → "MY_REDIS_SECRET_PASSWORD"
	import_safe := func(s string) string {
		result := make([]byte, len(s))
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == '-' || c == '.' || c == '/' {
				result[i] = '_'
			} else if c >= 'a' && c <= 'z' {
				result[i] = c - 32
			} else {
				result[i] = c
			}
		}
		return string(result)
	}
	return import_safe(ref.Name) + "_" + import_safe(ref.Key)
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
			wtc.Overrides[toolName] = convertCRDToolOverride(&override)
		}
	}
}

// convertCRDToolOverride converts a CRD ToolOverride to a config ToolOverride.
func convertCRDToolOverride(src *mcpv1alpha1.ToolOverride) *vmcpconfig.ToolOverride {
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

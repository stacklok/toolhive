// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stacklok/toolhive-core/permissions"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/awssts"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authz"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/recovery"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/usagemetrics"
)

// BuildContext defines the context in which the RunConfigBuilder is being used
type BuildContext int

const (
	// BuildContextCLI indicates the builder is being used in CLI context with full validation
	BuildContextCLI BuildContext = iota
	// BuildContextOperator indicates the builder is being used in Kubernetes operator context
	BuildContextOperator
)

// runConfigBuilder provides a fluent interface for building RunConfig instances
type runConfigBuilder struct {
	config *RunConfig
	// Store transport string separately to avoid type confusion
	transportString string
	// Store ports separately for proper validation
	port       int
	targetPort int
	// Store network mode to apply to permission profile after it's loaded
	networkMode string
	// Build context determines which validation and features are enabled
	buildContext BuildContext
}

// RunConfigBuilderOption is a function that modifies the RunConfigBuilder
type RunConfigBuilderOption func(*runConfigBuilder) error

// WithRuntime sets the container runtime
func WithRuntime(deployer rt.Deployer) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if b.buildContext == BuildContextCLI {
			b.config.Deployer = deployer
		}
		return nil
	}
}

// WithImage sets the Docker image
func WithImage(image string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.Image = image
		return nil
	}
}

// WithRuntimeConfig sets the runtime configuration (base images and packages)
func WithRuntimeConfig(runtimeConfig *templates.RuntimeConfig) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.RuntimeConfig = runtimeConfig
		return nil
	}
}

// WithRemoteURL sets the remote URL for the MCP server
func WithRemoteURL(remoteURL string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.RemoteURL = remoteURL
		return nil
	}
}

// WithRemoteAuth sets the remote authentication configuration
func WithRemoteAuth(config *remote.Config) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if config == nil {
			config = &remote.Config{
				CallbackPort: remote.DefaultCallbackPort,
			}
		}
		b.config.RemoteAuthConfig = config
		return nil
	}
}

// WithName sets the MCP server name
func WithName(name string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.Name = name
		return nil
	}
}

// WithMiddlewareConfig sets the middleware configuration
func WithMiddlewareConfig(middlewareConfig []types.MiddlewareConfig) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.MiddlewareConfigs = middlewareConfig
		return nil
	}
}

// WithCmdArgs sets the command arguments
func WithCmdArgs(args []string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.CmdArgs = args
		return nil
	}
}

// WithHost sets the host (applies default if empty)
func WithHost(host string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if host == "" {
			host = transport.LocalhostIPv4
		}
		b.config.Host = host
		return nil
	}
}

// WithTargetHost sets the target host (applies default if empty)
func WithTargetHost(targetHost string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if b.config.RemoteURL != "" {
			remoteURL, err := url.Parse(b.config.RemoteURL)
			if err == nil {
				targetHost = remoteURL.Host
			} else {
				slog.Warn("Failed to parse remote URL", "error", err)
				targetHost = transport.LocalhostIPv4
			}
		} else if targetHost == "" {
			targetHost = transport.LocalhostIPv4
		}
		b.config.TargetHost = targetHost
		return nil
	}
}

// WithDebug sets debug mode
func WithDebug(debug bool) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.Debug = debug
		return nil
	}
}

// WithVolumes sets the volume mounts
func WithVolumes(volumes []string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.Volumes = volumes
		return nil
	}
}

// WithSecrets sets the secrets list
func WithSecrets(secrets []string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.Secrets = secrets
		return nil
	}
}

// WithAuthzConfigPath sets the authorization config path
func WithAuthzConfigPath(path string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.AuthzConfigPath = path
		return nil
	}
}

// WithAuthzConfig sets the authorization config data
func WithAuthzConfig(config *authz.Config) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.AuthzConfig = config
		return nil
	}
}

// WithAuditConfigPath sets the audit config path
func WithAuditConfigPath(path string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.AuditConfigPath = path
		return nil
	}
}

// WithPermissionProfileNameOrPath sets the permission profile name or path.
// If called multiple times or mixed with WithPermissionProfile,
// the last call takes precedence.
func WithPermissionProfileNameOrPath(profile string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.PermissionProfileNameOrPath = profile
		b.config.PermissionProfile = nil // Clear any existing profile
		return nil
	}
}

// WithPermissionProfile sets the permission profile directly.
// If called multiple times or mixed with WithPermissionProfile,
// the last call takes precedence.
func WithPermissionProfile(profile *permissions.Profile) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.PermissionProfile = profile
		b.config.PermissionProfileNameOrPath = "" // Clear any existing name or path
		return nil
	}
}

// WithNetworkIsolation sets network isolation
func WithNetworkIsolation(isolate bool) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.IsolateNetwork = isolate
		return nil
	}
}

// WithTrustProxyHeaders sets whether to trust X-Forwarded-* headers from reverse proxies
func WithTrustProxyHeaders(trust bool) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.TrustProxyHeaders = trust
		return nil
	}
}

// WithEndpointPrefix sets the path prefix for SSE endpoint URLs
func WithEndpointPrefix(prefix string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.EndpointPrefix = prefix
		return nil
	}
}

// WithNetworkMode sets the network mode for the container.
// The network mode will be applied to the permission profile after it is loaded.
func WithNetworkMode(networkMode string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.networkMode = networkMode
		return nil
	}
}

// WithK8sPodPatch sets the Kubernetes pod template patch
func WithK8sPodPatch(patch string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.K8sPodTemplatePatch = patch
		return nil
	}
}

// WithProxyMode sets the proxy mode
func WithProxyMode(mode types.ProxyMode) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.ProxyMode = mode
		return nil
	}
}

// WithGroup sets the group name for the workload
func WithGroup(groupName string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.Group = groupName
		return nil
	}
}

// WithLabels sets custom labels from command-line flags
func WithLabels(labelStrings []string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if len(labelStrings) == 0 {
			return nil
		}

		// Initialize ContainerLabels if it's nil
		if b.config.ContainerLabels == nil {
			b.config.ContainerLabels = make(map[string]string)
		}

		// Parse and add each label
		for _, labelString := range labelStrings {
			key, value, err := labels.ParseLabel(labelString)
			if err != nil {
				slog.Warn("Skipping invalid label", "label", labelString, "error", err)
				continue
			}
			b.config.ContainerLabels[key] = value
		}

		return nil
	}
}

// WithTransportAndPorts sets transport and port configuration
func WithTransportAndPorts(mcpTransport string, port, targetPort int) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.transportString = mcpTransport
		b.port = port
		b.targetPort = targetPort
		return nil
	}
}

// WithExistingPort sets the existing port for update operations
// This allows port reuse during workload updates by skipping validation for the same port
func WithExistingPort(port int) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.existingPort = port
		return nil
	}
}

// WithAuditEnabled configures audit settings
func WithAuditEnabled(enableAudit bool, auditConfigPath string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if enableAudit && auditConfigPath == "" {
			b.config.AuditConfig = audit.DefaultConfig()
		}
		return nil
	}
}

// WithOIDCConfig configures OIDC settings
func WithOIDCConfig(
	oidcIssuer string,
	oidcAudience string,
	oidcJwksURL string,
	oidcIntrospectionURL string,
	oidcClientID string,
	oidcClientSecret string,
	thvCABundle string,
	jwksAuthTokenFile string,
	resourceURL string,
	jwksAllowPrivateIP bool,
	insecureAllowHTTP bool,
	scopes []string,
) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if oidcIssuer != "" || oidcAudience != "" || oidcJwksURL != "" || oidcIntrospectionURL != "" ||
			oidcClientID != "" || oidcClientSecret != "" {
			b.config.OIDCConfig = &auth.TokenValidatorConfig{
				Issuer:            oidcIssuer,
				Audience:          oidcAudience,
				JWKSURL:           oidcJwksURL,
				IntrospectionURL:  oidcIntrospectionURL,
				ClientID:          oidcClientID,
				ClientSecret:      oidcClientSecret,
				CACertPath:        thvCABundle,
				AuthTokenFile:     jwksAuthTokenFile,
				AllowPrivateIP:    jwksAllowPrivateIP,
				InsecureAllowHTTP: insecureAllowHTTP,
				Scopes:            scopes,
			}
		}

		// Set JWKS-related configuration
		b.config.ThvCABundle = thvCABundle
		b.config.JWKSAuthTokenFile = jwksAuthTokenFile

		// Set ResourceURL if OIDCConfig exists or if resourceURL is not empty
		if b.config.OIDCConfig != nil {
			b.config.OIDCConfig.ResourceURL = resourceURL
		} else if resourceURL != "" {
			// Create OIDCConfig just for ResourceURL if it doesn't exist but resourceURL is provided
			b.config.OIDCConfig = &auth.TokenValidatorConfig{
				ResourceURL: resourceURL,
			}
		}

		return nil
	}
}

// WithTokenExchangeConfig sets the token exchange configuration
func WithTokenExchangeConfig(config *tokenexchange.Config) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.TokenExchangeConfig = config
		return nil
	}
}

// WithAWSStsConfig sets the AWS STS token exchange configuration
func WithAWSStsConfig(config *awssts.Config) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.AWSStsConfig = config
		return nil
	}
}

// WithTelemetryConfigFromFlags configures telemetry settings (legacy - custom attributes handled via middleware)
func WithTelemetryConfigFromFlags(
	otelEndpoint string,
	otelEnablePrometheusMetricsPath bool,
	otelTracingEnabled bool,
	otelMetricsEnabled bool,
	otelServiceName string,
	otelSamplingRate float64,
	otelHeaders []string,
	otelInsecure bool,
	otelEnvironmentVariables []string,
	otelUseLegacyAttributes bool,
) RunConfigBuilderOption {
	config := telemetry.MaybeMakeConfig(
		otelEndpoint,
		otelEnablePrometheusMetricsPath,
		otelTracingEnabled,
		otelMetricsEnabled,
		otelServiceName,
		otelSamplingRate,
		otelHeaders,
		otelInsecure,
		otelEnvironmentVariables,
		otelUseLegacyAttributes,
	)
	return WithTelemetryConfig(config)
}

// WithTelemetryConfig sets the telemetry configuration
func WithTelemetryConfig(config *telemetry.Config) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.TelemetryConfig = config
		return nil
	}
}

// WithToolsFilter sets the tools filter
func WithToolsFilter(toolsFilter []string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.ToolsFilter = toolsFilter
		return nil
	}
}

// WithToolsOverride sets the tool override map for the RunConfig
// This method is mutually exclusive with WithToolOverrideFile
func WithToolsOverride(toolOverride map[string]ToolOverride) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.ToolsOverride = toolOverride
		return nil
	}
}

// WithIgnoreConfig sets the ignore configuration
func WithIgnoreConfig(ignoreConfig *ignore.Config) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.IgnoreConfig = ignoreConfig
		return nil
	}
}

// WithMiddlewareFromFlags creates middleware configurations directly from flag values
func WithMiddlewareFromFlags(
	oidcConfig *auth.TokenValidatorConfig,
	tokenExchangeConfig *tokenexchange.Config,
	toolsFilter []string,
	toolsOverride map[string]ToolOverride,
	telemetryConfig *telemetry.Config,
	authzConfigPath string,
	enableAudit bool,
	auditConfigPath string,
	serverName string,
	transportType string,
	disableUsageMetrics bool,
) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		var middlewareConfigs []types.MiddlewareConfig

		// NOTE: order matters here. Specifically, these routines use append
		// to add new middleware configs, but once these routines are called,
		// inside the proxy, they are applied in reverse order, so the first
		// being added here is effectively the last being called at HTTP
		// request time.
		//
		// We should avoid doing this and a better pattern would be to let the
		// actual proxy determine the order of application of middlewares, since
		// the types of middleware are known at compile time.

		// Add tool filter middlewares
		middlewareConfigs = addToolFilterMiddlewares(middlewareConfigs, toolsFilter, toolsOverride)

		// Add core middlewares (always present)
		middlewareConfigs = addCoreMiddlewares(middlewareConfigs, oidcConfig, tokenExchangeConfig, disableUsageMetrics)

		// NOTE: Header forward middleware is NOT added here because secret-backed
		// headers are not yet resolved at builder time. It is added in Runner.Run()
		// after WithSecrets() resolves all secret references.

		// NOTE: AWS STS middleware is NOT added here because it is only configured
		// through the operator path via PopulateMiddlewareConfigs(), not via CLI flags.

		// Add optional middlewares
		middlewareConfigs = addTelemetryMiddleware(middlewareConfigs, telemetryConfig, serverName, transportType)
		middlewareConfigs = addAuthzMiddleware(middlewareConfigs, authzConfigPath)
		middlewareConfigs = addAuditMiddleware(middlewareConfigs, enableAudit, auditConfigPath, serverName, transportType)

		// Add recovery middleware (always present, added last to be outermost wrapper)
		middlewareConfigs = addRecoveryMiddleware(middlewareConfigs)

		// Set the populated middleware configs
		b.config.MiddlewareConfigs = middlewareConfigs
		return nil
	}
}

// WithEnvVars sets environment variables from a map
func WithEnvVars(envVars map[string]string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if b.config.EnvVars == nil {
			b.config.EnvVars = make(map[string]string)
		}
		for key, value := range envVars {
			b.config.EnvVars[key] = value
		}
		return nil
	}
}

// addToolFilterMiddlewares adds tool filter middlewares if tools filter is provided
func addToolFilterMiddlewares(
	middlewareConfigs []types.MiddlewareConfig,
	toolsFilter []string,
	toolsOverride map[string]ToolOverride,
) []types.MiddlewareConfig {
	if len(toolsFilter) == 0 && len(toolsOverride) == 0 {
		return middlewareConfigs
	}

	overrides := make(map[string]mcp.ToolOverride)
	for actualName, tool := range toolsOverride {
		overrides[actualName] = mcp.ToolOverride{
			Name:        tool.Name,
			Description: tool.Description,
		}
	}

	toolFilterParams := mcp.ToolFilterMiddlewareParams{
		FilterTools:   toolsFilter,
		ToolsOverride: overrides,
	}

	// Add tool filter middleware
	if toolFilterConfig, err := types.NewMiddlewareConfig(mcp.ToolFilterMiddlewareType, toolFilterParams); err == nil {
		middlewareConfigs = append(middlewareConfigs, *toolFilterConfig)
	}

	// Add tool call filter middleware
	if toolCallFilterConfig, err := types.NewMiddlewareConfig(mcp.ToolCallFilterMiddlewareType, toolFilterParams); err == nil {
		middlewareConfigs = append(middlewareConfigs, *toolCallFilterConfig)
	}

	return middlewareConfigs
}

// addCoreMiddlewares adds core middlewares that are always present
func addCoreMiddlewares(
	middlewareConfigs []types.MiddlewareConfig,
	oidcConfig *auth.TokenValidatorConfig,
	tokenExchangeConfig *tokenexchange.Config,
	disableUsageMetrics bool,
) []types.MiddlewareConfig {
	// Authentication middleware (always present)
	authParams := auth.MiddlewareParams{
		OIDCConfig: oidcConfig,
	}
	if authConfig, err := types.NewMiddlewareConfig(auth.MiddlewareType, authParams); err == nil {
		middlewareConfigs = append(middlewareConfigs, *authConfig)
	}

	// Token Exchange middleware (conditionally present)
	if tokenExchangeConfig != nil {
		tokenExchangeParams := tokenexchange.MiddlewareParams{
			TokenExchangeConfig: tokenExchangeConfig,
		}
		if tokenExchangeMwConfig, err := types.NewMiddlewareConfig(tokenexchange.MiddlewareType, tokenExchangeParams); err == nil {
			middlewareConfigs = append(middlewareConfigs, *tokenExchangeMwConfig)
		} else {
			slog.Warn("Failed to create token exchange middleware config", "error", err)
		}
	}

	// MCP Parser middleware (always present)
	mcpParserParams := mcp.ParserMiddlewareParams{}
	if mcpParserConfig, err := types.NewMiddlewareConfig(mcp.ParserMiddlewareType, mcpParserParams); err == nil {
		middlewareConfigs = append(middlewareConfigs, *mcpParserConfig)
	}

	// Usage metrics middleware (if enabled)
	if usagemetrics.ShouldEnableMetrics(disableUsageMetrics) {
		usageMetricsParams := usagemetrics.MiddlewareParams{}
		if usageMetricsConfig, err := types.NewMiddlewareConfig(usagemetrics.MiddlewareType, usageMetricsParams); err == nil {
			middlewareConfigs = append(middlewareConfigs, *usageMetricsConfig)
		}
	}

	return middlewareConfigs
}

// addTelemetryMiddleware adds telemetry middleware if enabled
func addTelemetryMiddleware(
	middlewareConfigs []types.MiddlewareConfig,
	telemetryConfig *telemetry.Config,
	serverName, transportType string,
) []types.MiddlewareConfig {
	if telemetryConfig == nil {
		return middlewareConfigs
	}

	telemetryParams := telemetry.FactoryMiddlewareParams{
		Config:     telemetryConfig,
		ServerName: serverName,
		Transport:  transportType,
	}
	if telemetryMwConfig, err := types.NewMiddlewareConfig(telemetry.MiddlewareType, telemetryParams); err == nil {
		middlewareConfigs = append(middlewareConfigs, *telemetryMwConfig)
	}

	return middlewareConfigs
}

// addAuthzMiddleware adds authorization middleware if config path is provided
func addAuthzMiddleware(
	middlewareConfigs []types.MiddlewareConfig, authzConfigPath string,
) []types.MiddlewareConfig {
	if authzConfigPath == "" {
		return middlewareConfigs
	}

	authzParams := authz.FactoryMiddlewareParams{
		ConfigPath: authzConfigPath, // Keep for backwards compatibility
	}

	// Read authz config contents if path is provided
	if authzConfigData, err := authz.LoadConfig(authzConfigPath); err == nil {
		authzParams.ConfigData = authzConfigData
	}
	// Note: We keep ConfigPath set for backwards compatibility

	if authzConfig, err := types.NewMiddlewareConfig(authz.MiddlewareType, authzParams); err == nil {
		middlewareConfigs = append(middlewareConfigs, *authzConfig)
	}

	return middlewareConfigs
}

// addAuditMiddleware adds audit middleware if enabled or config path is provided
func addAuditMiddleware(
	middlewareConfigs []types.MiddlewareConfig,
	enableAudit bool,
	auditConfigPath, serverName, transportType string,
) []types.MiddlewareConfig {
	if !enableAudit && auditConfigPath == "" {
		return middlewareConfigs
	}

	auditParams := audit.MiddlewareParams{
		ConfigPath:    auditConfigPath, // Keep for backwards compatibility
		Component:     serverName,      // Use server name as component
		TransportType: transportType,   // Pass the actual transport type
	}

	// Read audit config contents if path is provided
	if auditConfigPath != "" {
		if auditConfigData, err := audit.LoadFromFile(auditConfigPath); err == nil {
			auditParams.ConfigData = auditConfigData
		}
		// Note: We keep ConfigPath set for backwards compatibility
	}

	if auditConfig, err := types.NewMiddlewareConfig(audit.MiddlewareType, auditParams); err == nil {
		middlewareConfigs = append(middlewareConfigs, *auditConfig)
	}

	return middlewareConfigs
}

// addRecoveryMiddleware adds recovery middleware (always present, added last to be outermost wrapper)
// Middleware is applied in reverse order, so adding last means it executes first
// and catches panics from all other middleware and handlers.
func addRecoveryMiddleware(middlewareConfigs []types.MiddlewareConfig) []types.MiddlewareConfig {
	recoveryConfig, err := types.NewMiddlewareConfig(recovery.MiddlewareType, nil)
	if err != nil {
		slog.Warn("failed to create recovery middleware", "error", err)
		return middlewareConfigs
	}
	middlewareConfigs = append(middlewareConfigs, *recoveryConfig)
	return middlewareConfigs
}

// NewOperatorRunConfigBuilder creates a new RunConfigBuilder configured for operator use
func NewOperatorRunConfigBuilder(
	ctx context.Context,
	imageMetadata *regtypes.ImageMetadata,
	envVars map[string]string,
	envVarValidator EnvVarValidator,
	runConfigOptions ...RunConfigBuilderOption,
) (*RunConfig, error) {
	return internalRunConfigBuilder(ctx,
		&runConfigBuilder{
			config: &RunConfig{
				ContainerLabels: make(map[string]string),
				EnvVars:         make(map[string]string),
			},
			buildContext: BuildContextOperator,
		}, imageMetadata, envVars, envVarValidator, runConfigOptions...)
}

// NewRunConfigBuilder creates the final RunConfig instance with validation and processing
func NewRunConfigBuilder(
	ctx context.Context,
	imageMetadata *regtypes.ImageMetadata,
	envVars map[string]string,
	envVarValidator EnvVarValidator,
	runConfigOptions ...RunConfigBuilderOption,
) (*RunConfig, error) {
	return internalRunConfigBuilder(ctx,
		&runConfigBuilder{
			config: &RunConfig{
				ContainerLabels: make(map[string]string),
				EnvVars:         make(map[string]string),
			},
			buildContext: BuildContextCLI,
		}, imageMetadata, envVars, envVarValidator, runConfigOptions...)
}

func internalRunConfigBuilder(
	ctx context.Context,
	b *runConfigBuilder,
	imageMetadata *regtypes.ImageMetadata,
	envVars map[string]string,
	envVarValidator EnvVarValidator,
	runConfigOptions ...RunConfigBuilderOption,
) (*RunConfig, error) {
	// Set the build context on the config to control validation behavior
	b.config.buildContext = b.buildContext

	// Apply all the options
	for _, option := range runConfigOptions {
		if err := option(b); err != nil {
			return nil, fmt.Errorf("failed to apply run config option: %w", err)
		}
	}

	// Resolve the OTel service name from the workload name when not explicitly set.
	// This ensures the service name is always populated before persisting the config.
	telemetry.ResolveServiceName(b.config.TelemetryConfig, b.config.Name)

	// When the embedded auth server is configured and the proxy has no
	// explicit PRM scopes, derive them from the AS's ScopesSupported.
	// This ensures the PRM advertises the same scopes the AS supports
	// (including offline_access for refresh tokens).
	// If the AS also has no explicit scopes, use the auth server's
	// default scopes (registration.DefaultScopes).
	if b.config.EmbeddedAuthServerConfig != nil &&
		b.config.OIDCConfig != nil &&
		len(b.config.OIDCConfig.Scopes) == 0 {
		scopes := b.config.EmbeddedAuthServerConfig.ScopesSupported
		if len(scopes) == 0 {
			scopes = registration.DefaultScopes
		}
		b.config.OIDCConfig.Scopes = scopes
	}

	// When using the CLI validation strategy, this is where the prompting for
	// missing environment variables will happen.
	processedEnvVars := envVars
	if envVarValidator != nil {
		validatedEnvVars, err := envVarValidator.Validate(ctx, imageMetadata, b.config, envVars)
		if err != nil {
			return nil, fmt.Errorf("failed to validate environment variables: %w", err)
		}
		processedEnvVars = validatedEnvVars
	}

	// Do some final validation which can only be done after everything else is set.
	// Apply image metadata overrides if needed.
	if err := b.validateConfig(imageMetadata); err != nil {
		return nil, fmt.Errorf("failed to validate run config: %w", err)
	}

	// Now set environment variables with the correct transport and ports resolved
	if _, err := b.config.WithEnvironmentVariables(processedEnvVars); err != nil {
		return nil, fmt.Errorf("failed to set environment variables: %w", err)
	}

	// Set schema version.
	b.config.SchemaVersion = CurrentSchemaVersion

	return b.config, nil
}

// validateConfig ensures the RunConfig is valid and sets up some of the final
// configuration details which can only be applied after all other flags are added.
// This function also handles setting missing values based on the image metadata (if present).
//
//nolint:gocyclo // This function needs to be refactored to reduce cyclomatic complexity.
func (b *runConfigBuilder) validateConfig(imageMetadata *regtypes.ImageMetadata) error {
	c := b.config
	var err error

	// The old logic claimed to override the name with the name from the registry
	// but didn't. Instead, it used the name passed in from the CLI.
	// See: https://github.com/stacklok/toolhive/blob/2873152b62bf61698cbcdd0aba1707a046151e67/cmd/thv/app/run.go#L425
	// The following code implements what I believe was the intended behavior:
	if c.Name == "" && imageMetadata != nil {
		c.Name = imageMetadata.Name
	}

	// Check to see if the mcpTransport is defined in the metadata.
	// Use this value if it was not set by the user.
	// Else, default to stdio.
	mcpTransport := b.transportString
	if mcpTransport == "" {
		if imageMetadata != nil && imageMetadata.Transport != "" {
			slog.Debug("Using registry transport", "transport", imageMetadata.Transport)
			mcpTransport = imageMetadata.Transport
		} else {
			slog.Debug("Defaulting mcpTransport to stdio")
			mcpTransport = types.TransportTypeStdio.String()
		}
	}
	// Set mcpTransport
	if _, err = c.WithTransport(mcpTransport); err != nil {
		return err
	}

	// Use registry ports if not overridden and if the mcpTransport is HTTP-based.
	proxyPort := b.port
	targetPort := b.targetPort
	if imageMetadata != nil {
		isHTTPServer := mcpTransport == types.TransportTypeSSE.String() ||
			mcpTransport == types.TransportTypeStreamableHTTP.String()
		if isHTTPServer {
			// Use registry proxy port if not set by CLI
			if proxyPort == 0 && imageMetadata.ProxyPort > 0 {
				slog.Debug("Using registry proxy port", "port", imageMetadata.ProxyPort)
				proxyPort = imageMetadata.ProxyPort
			}
			// Use registry target port if not set by CLI
			if targetPort == 0 && imageMetadata.TargetPort > 0 {
				slog.Debug("Using registry target port", "port", imageMetadata.TargetPort)
				targetPort = imageMetadata.TargetPort
			}
		}
	}
	// Configure ports and target host
	if _, err = c.WithPorts(proxyPort, targetPort); err != nil {
		return err
	}

	// Load or default the permission profile
	// NOTE: This must be done before processing volume mounts
	c.PermissionProfile, err = b.loadPermissionProfile(imageMetadata)
	if err != nil {
		return err
	}

	// Apply network mode to permission profile if specified
	if b.networkMode != "" {
		// Ensure Network permissions struct exists
		if c.PermissionProfile.Network == nil {
			c.PermissionProfile.Network = &permissions.NetworkPermissions{}
		}
		c.PermissionProfile.Network.Mode = b.networkMode
		slog.Debug("Setting network mode on permission profile", "network_mode", b.networkMode)
	}

	// Process volume mounts
	if err = b.processVolumeMounts(); err != nil {
		return err
	}

	// Generate container name if not already set
	_, wasModified := c.WithContainerName()
	if wasModified && c.Name != "" {
		slog.Warn("The provided name contained invalid characters and was sanitized", "name", c.Name)
	}

	// Add standard labels
	c.WithStandardLabels()

	// Add authorization configuration if provided
	if c.AuthzConfigPath != "" {
		authzConfig, err := authz.LoadConfig(c.AuthzConfigPath)
		if err != nil {
			return fmt.Errorf("failed to load authorization configuration: %w", err)
		}
		c.WithAuthz(authzConfig)
	}

	// Add audit configuration if provided
	if c.AuditConfigPath != "" {
		auditConfig, err := audit.LoadFromFile(c.AuditConfigPath)
		if err != nil {
			return fmt.Errorf("failed to load audit configuration: %w", err)
		}
		c.WithAudit(auditConfig)
	}
	// Note: AuditConfig is already set from --enable-audit flag if provided

	if imageMetadata != nil && len(imageMetadata.Args) > 0 {
		if len(c.CmdArgs) == 0 {
			// No user args provided, use registry defaults
			slog.Debug("Using registry default args", "args", imageMetadata.Args)
			c.CmdArgs = append(c.CmdArgs, imageMetadata.Args...)
		}
	}

	for toolName, tool := range c.ToolsOverride {
		if tool.Name == "" && tool.Description == "" {
			return fmt.Errorf("tool override for %s must have either Name or Description set", toolName)
		}
	}

	if c.ToolsOverride != nil && imageMetadata != nil && imageMetadata.Tools != nil {
		slog.Debug("Using tools override", "tools", c.ToolsOverride)
		for toolName := range c.ToolsOverride {
			if !slices.Contains(imageMetadata.Tools, toolName) {
				return fmt.Errorf("tool %s not found in registry", toolName)
			}
		}
	}

	if c.ToolsFilter != nil && imageMetadata != nil && imageMetadata.Tools != nil {
		slog.Debug("Using tools filter", "filter", c.ToolsFilter)
		for _, tool := range c.ToolsFilter {
			name := tool

			if c.ToolsOverride != nil {
				for actualName, toolOverride := range c.ToolsOverride {
					if toolOverride.Name == tool {
						name = actualName
						break
					}
				}
			}

			if !slices.Contains(imageMetadata.Tools, name) {
				return fmt.Errorf("tool %s not found in registry", name)
			}
		}
	}

	return nil
}

func (b *runConfigBuilder) loadPermissionProfile(imageMetadata *regtypes.ImageMetadata) (*permissions.Profile, error) {
	// The permission profile object takes precedence over the name or path.
	if b.config.PermissionProfile != nil {
		return b.config.PermissionProfile, nil
	}

	// Try to load the permission profile by name or path.
	if b.config.PermissionProfileNameOrPath != "" {
		switch b.config.PermissionProfileNameOrPath {
		case permissions.ProfileNone, "stdio":
			return permissions.BuiltinNoneProfile(), nil
		case permissions.ProfileNetwork:
			return permissions.BuiltinNetworkProfile(), nil
		default:
			// Try to load from file
			return permissions.FromFile(b.config.PermissionProfileNameOrPath)
		}
	}

	// If a profile was not set by name or path, check the image metadata.
	if imageMetadata != nil && imageMetadata.Permissions != nil {

		slog.Debug("Using registry permission profile", "permissions", imageMetadata.Permissions)
		return imageMetadata.Permissions, nil
	}

	// If no metadata is available, use the network permission profile as default.
	slog.Debug("Using default permission profile", "profile", permissions.ProfileNetwork)
	return permissions.BuiltinNetworkProfile(), nil
}

// processVolumeMounts processes volume mounts and adds them to the permission profile
func (b *runConfigBuilder) processVolumeMounts() error {

	// Skip if no volumes to process
	if len(b.config.Volumes) == 0 {
		return nil
	}

	// Ensure permission profile is loaded
	if b.config.PermissionProfile == nil {
		return fmt.Errorf("permission profile is required when using volume mounts")
	}

	// Create a map of existing mount targets for quick lookup
	existingMounts := make(map[string]string)

	// Add existing read mounts to the map
	for _, m := range b.config.PermissionProfile.Read {
		source, target, _ := m.Parse()
		existingMounts[target] = source
	}

	// Add existing write mounts to the map
	for _, m := range b.config.PermissionProfile.Write {
		source, target, _ := m.Parse()
		existingMounts[target] = source
	}

	// Process each volume mount
	for _, volume := range b.config.Volumes {
		// Parse read-only flag
		readOnly := strings.HasSuffix(volume, ":ro")
		volumeSpec := volume
		if readOnly {
			volumeSpec = strings.TrimSuffix(volume, ":ro")
		}

		// Create and parse mount declaration
		mount := permissions.MountDeclaration(volumeSpec)
		source, target, err := mount.Parse()
		if err != nil {
			return fmt.Errorf("invalid volume format: %s (%w)", volume, err)
		}

		// Validate source path exists on the host filesystem (CLI context only).
		// Skip for Kubernetes operator context where paths are container-relative,
		// and for resource URIs which are not filesystem paths.
		if b.buildContext != BuildContextOperator && !strings.HasPrefix(source, "resource://") {
			absSource := source
			if !filepath.IsAbs(absSource) {
				absSource, err = filepath.Abs(absSource)
				if err != nil {
					return fmt.Errorf("failed to resolve volume mount source path %q: %w", source, err)
				}
			}
			if _, err := os.Stat(absSource); err != nil {
				return fmt.Errorf("volume mount source path does not exist: %s", source)
			}
		}

		// Check for duplicate mount target
		if existingSource, isDuplicate := existingMounts[target]; isDuplicate {
			slog.Warn("Skipping duplicate mount target", "target", target, "existing_source", existingSource)
			continue
		}

		// Add the mount to the appropriate permission list
		if readOnly {
			b.config.PermissionProfile.Read = append(b.config.PermissionProfile.Read, mount)
		} else {
			b.config.PermissionProfile.Write = append(b.config.PermissionProfile.Write, mount)
		}

		// Add to the map of existing mounts
		existingMounts[target] = source

		slog.Debug("Adding volume mount", "source", source, "target", target,
			"mode", map[bool]string{true: "read-only", false: "read-write"}[readOnly])
	}

	return nil
}

// BuildForOperator creates a RunConfig for operator use, using the same validation as CLI
func (b *runConfigBuilder) BuildForOperator() (*RunConfig, error) {
	if b.buildContext != BuildContextOperator {
		return nil, fmt.Errorf("BuildForOperator can only be used with BuildContextOperator")
	}

	// Set build context on the config to control validation behavior
	b.config.buildContext = BuildContextOperator

	// Use the same validation logic as CLI, but without image metadata (pass nil)
	if err := b.validateConfig(nil); err != nil {
		return nil, fmt.Errorf("failed to validate run config: %w", err)
	}

	// Set schema version
	b.config.SchemaVersion = CurrentSchemaVersion

	return b.config, nil
}

// WithEnvVars sets environment variables from a map
func (b *runConfigBuilder) WithEnvVars(envVars map[string]string) *runConfigBuilder {
	if b.config.EnvVars == nil {
		b.config.EnvVars = make(map[string]string)
	}
	for key, value := range envVars {
		b.config.EnvVars[key] = value
	}
	return b
}

// WithEnvFile adds environment variables from a single file
func WithEnvFile(filePath string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if _, err := b.config.WithEnvFile(filePath); err != nil {
			return err
		}
		return nil
	}
}

// WithEnvFilesFromDirectory adds environment variables from all files in a directory
func WithEnvFilesFromDirectory(dirPath string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if _, err := b.config.WithEnvFilesFromDirectory(dirPath); err != nil {
			return err
		}
		return nil
	}
}

// WithHeaderForward adds plaintext header forward entries for remote MCP servers.
// The headers parameter contains literal header values (non-sensitive, stored as-is in RunConfig).
// Multiple calls are additive; later values for the same header name overwrite earlier ones.
func WithHeaderForward(headers map[string]string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if len(headers) == 0 {
			return nil
		}
		hf := b.ensureHeaderForward()
		if hf.AddPlaintextHeaders == nil {
			hf.AddPlaintextHeaders = make(map[string]string, len(headers))
		}
		maps.Copy(hf.AddPlaintextHeaders, headers)
		return nil
	}
}

// WithHeaderForwardSecrets adds secret-backed header forward entries for remote MCP servers.
// The headers parameter maps header names to secret names in the secrets manager.
// Secret values are resolved at runtime via WithSecrets() and never persisted to disk.
// Multiple calls are additive; later values for the same header name overwrite earlier ones.
func WithHeaderForwardSecrets(headers map[string]string) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		if len(headers) == 0 {
			return nil
		}
		hf := b.ensureHeaderForward()
		if hf.AddHeadersFromSecret == nil {
			hf.AddHeadersFromSecret = make(map[string]string, len(headers))
		}
		maps.Copy(hf.AddHeadersFromSecret, headers)
		return nil
	}
}

func (b *runConfigBuilder) ensureHeaderForward() *HeaderForwardConfig {
	if b.config.HeaderForward == nil {
		b.config.HeaderForward = &HeaderForwardConfig{}
	}
	return b.config.HeaderForward
}

// WithEmbeddedAuthServerConfig sets the embedded auth server configuration.
// The config is a RunConfig with file paths and env var names for secrets.
func WithEmbeddedAuthServerConfig(config *authserver.RunConfig) RunConfigBuilderOption {
	return func(b *runConfigBuilder) error {
		b.config.EmbeddedAuthServerConfig = config
		return nil
	}
}

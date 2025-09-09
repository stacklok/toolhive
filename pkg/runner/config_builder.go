package runner

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// BuildContext defines the context in which the RunConfigBuilder is being used
type BuildContext int

const (
	// BuildContextCLI indicates the builder is being used in CLI context with full validation
	BuildContextCLI BuildContext = iota
	// BuildContextOperator indicates the builder is being used in Kubernetes operator context
	BuildContextOperator
)

// RunConfigBuilder provides a fluent interface for building RunConfig instances
type RunConfigBuilder struct {
	config *RunConfig
	// Store transport string separately to avoid type confusion
	transportString string
	// Store ports separately for proper validation
	port       int
	targetPort int
	// Build context determines which validation and features are enabled
	buildContext BuildContext
}

// NewRunConfigBuilder creates a new RunConfigBuilder with default values for CLI use
func NewRunConfigBuilder() *RunConfigBuilder {
	return &RunConfigBuilder{
		config: &RunConfig{
			ContainerLabels: make(map[string]string),
			EnvVars:         make(map[string]string),
		},
		buildContext: BuildContextCLI,
	}
}

// NewOperatorRunConfigBuilder creates a new RunConfigBuilder configured for operator use
func NewOperatorRunConfigBuilder() *RunConfigBuilder {
	return &RunConfigBuilder{
		config: &RunConfig{
			ContainerLabels: make(map[string]string),
			EnvVars:         make(map[string]string),
		},
		buildContext: BuildContextOperator,
	}
}

// WithRuntime sets the container runtime (only used in CLI context)
func (b *RunConfigBuilder) WithRuntime(deployer rt.Deployer) *RunConfigBuilder {
	// Only set deployer in CLI context
	if b.buildContext == BuildContextCLI {
		b.config.Deployer = deployer
	}
	return b
}

// WithImage sets the Docker image
func (b *RunConfigBuilder) WithImage(image string) *RunConfigBuilder {
	b.config.Image = image
	return b
}

// WithRemoteURL sets the remote URL for the MCP server
func (b *RunConfigBuilder) WithRemoteURL(remoteURL string) *RunConfigBuilder {
	b.config.RemoteURL = remoteURL
	return b
}

// WithRemoteAuth sets the remote authentication configuration
func (b *RunConfigBuilder) WithRemoteAuth(config *RemoteAuthConfig) *RunConfigBuilder {
	b.config.RemoteAuthConfig = config
	return b
}

// WithName sets the MCP server name
func (b *RunConfigBuilder) WithName(name string) *RunConfigBuilder {
	b.config.Name = name
	return b
}

// WithMiddlewareConfig sets the middleware configuration
func (b *RunConfigBuilder) WithMiddlewareConfig(middlewareConfig []types.MiddlewareConfig) *RunConfigBuilder {
	b.config.MiddlewareConfigs = middlewareConfig
	return b
}

// WithCmdArgs sets the command arguments
func (b *RunConfigBuilder) WithCmdArgs(args []string) *RunConfigBuilder {
	b.config.CmdArgs = args
	return b
}

// WithHost sets the host (applies default if empty)
func (b *RunConfigBuilder) WithHost(host string) *RunConfigBuilder {
	if host == "" {
		host = transport.LocalhostIPv4
	}
	b.config.Host = host
	return b
}

// WithTargetHost sets the target host (applies default if empty)
func (b *RunConfigBuilder) WithTargetHost(targetHost string) *RunConfigBuilder {
	if b.config.RemoteURL != "" {
		remoteURL, err := url.Parse(b.config.RemoteURL)
		if err == nil {
			targetHost = remoteURL.Host
		} else {
			logger.Warnf("Failed to parse remote URL: %v", err)
			targetHost = transport.LocalhostIPv4
		}
	} else if targetHost == "" {
		targetHost = transport.LocalhostIPv4
	}
	b.config.TargetHost = targetHost
	return b
}

// WithDebug sets debug mode
func (b *RunConfigBuilder) WithDebug(debug bool) *RunConfigBuilder {
	b.config.Debug = debug
	return b
}

// WithVolumes sets the volume mounts
func (b *RunConfigBuilder) WithVolumes(volumes []string) *RunConfigBuilder {
	b.config.Volumes = volumes
	return b
}

// WithSecrets sets the secrets list
func (b *RunConfigBuilder) WithSecrets(secrets []string) *RunConfigBuilder {
	b.config.Secrets = secrets
	return b
}

// WithAuthzConfigPath sets the authorization config path
func (b *RunConfigBuilder) WithAuthzConfigPath(path string) *RunConfigBuilder {
	b.config.AuthzConfigPath = path
	return b
}

// WithAuthzConfig sets the authorization config data
func (b *RunConfigBuilder) WithAuthzConfig(config *authz.Config) *RunConfigBuilder {
	b.config.AuthzConfig = config
	return b
}

// WithAuditConfigPath sets the audit config path
func (b *RunConfigBuilder) WithAuditConfigPath(path string) *RunConfigBuilder {
	b.config.AuditConfigPath = path
	return b
}

// WithPermissionProfileNameOrPath sets the permission profile name or path.
// If called multiple times or mixed with WithPermissionProfile,
// the last call takes precedence.
func (b *RunConfigBuilder) WithPermissionProfileNameOrPath(profile string) *RunConfigBuilder {
	b.config.PermissionProfileNameOrPath = profile
	b.config.PermissionProfile = nil // Clear any existing profile
	return b
}

// WithPermissionProfile sets the permission profile directly.
// If called multiple times or mixed with WithPermissionProfile,
// the last call takes precedence.
func (b *RunConfigBuilder) WithPermissionProfile(profile *permissions.Profile) *RunConfigBuilder {
	b.config.PermissionProfile = profile
	b.config.PermissionProfileNameOrPath = "" // Clear any existing name or path
	return b
}

// WithNetworkIsolation sets network isolation
func (b *RunConfigBuilder) WithNetworkIsolation(isolate bool) *RunConfigBuilder {
	b.config.IsolateNetwork = isolate
	return b
}

// WithK8sPodPatch sets the Kubernetes pod template patch
func (b *RunConfigBuilder) WithK8sPodPatch(patch string) *RunConfigBuilder {
	b.config.K8sPodTemplatePatch = patch
	return b
}

// WithProxyMode sets the proxy mode
func (b *RunConfigBuilder) WithProxyMode(mode types.ProxyMode) *RunConfigBuilder {
	b.config.ProxyMode = mode
	return b
}

// WithGroup sets the group name for the workload
func (b *RunConfigBuilder) WithGroup(groupName string) *RunConfigBuilder {
	b.config.Group = groupName
	return b
}

// WithLabels sets custom labels from command-line flags
func (b *RunConfigBuilder) WithLabels(labelStrings []string) *RunConfigBuilder {
	if len(labelStrings) == 0 {
		return b
	}

	// Initialize ContainerLabels if it's nil
	if b.config.ContainerLabels == nil {
		b.config.ContainerLabels = make(map[string]string)
	}

	// Parse and add each label
	for _, labelString := range labelStrings {
		key, value, err := labels.ParseLabel(labelString)
		if err != nil {
			logger.Warnf("Skipping invalid label: %s (%v)", labelString, err)
			continue
		}
		b.config.ContainerLabels[key] = value
	}

	return b
}

// WithTransportAndPorts sets transport and port configuration
func (b *RunConfigBuilder) WithTransportAndPorts(mcpTransport string, port, targetPort int) *RunConfigBuilder {
	b.transportString = mcpTransport
	b.port = port
	b.targetPort = targetPort
	return b
}

// WithAuditEnabled configures audit settings
func (b *RunConfigBuilder) WithAuditEnabled(enableAudit bool, auditConfigPath string) *RunConfigBuilder {
	if enableAudit && auditConfigPath == "" {
		b.config.AuditConfig = audit.DefaultConfig()
	}
	return b
}

// WithOIDCConfig configures OIDC settings
func (b *RunConfigBuilder) WithOIDCConfig(
	oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID string, oidcClientSecret string,
	thvCABundle, jwksAuthTokenFile, resourceURL string,
	jwksAllowPrivateIP bool,
) *RunConfigBuilder {
	if oidcIssuer != "" || oidcAudience != "" || oidcJwksURL != "" || oidcIntrospectionURL != "" ||
		oidcClientID != "" || oidcClientSecret != "" {
		b.config.OIDCConfig = &auth.TokenValidatorConfig{
			Issuer:           oidcIssuer,
			Audience:         oidcAudience,
			JWKSURL:          oidcJwksURL,
			IntrospectionURL: oidcIntrospectionURL,
			ClientID:         oidcClientID,
			ClientSecret:     oidcClientSecret,
			AllowPrivateIP:   jwksAllowPrivateIP,
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
	return b
}

// WithTelemetryConfig configures telemetry settings
func (b *RunConfigBuilder) WithTelemetryConfig(otelEndpoint string, otelEnablePrometheusMetricsPath bool,
	otelTracingEnabled bool, otelMetricsEnabled bool, otelServiceName string, otelSamplingRate float64,
	otelHeaders []string, otelInsecure bool,
	otelEnvironmentVariables []string) *RunConfigBuilder {

	if otelEndpoint == "" && !otelEnablePrometheusMetricsPath {
		return b
	}

	// Parse headers from key=value format
	headers := make(map[string]string)
	for _, header := range otelHeaders {
		parts := strings.SplitN(header, "=", 2)
		if len(parts) == 2 {
			headers[parts[0]] = parts[1]
		}
	}

	// Use provided service name or default
	serviceName := otelServiceName
	if serviceName == "" {
		serviceName = telemetry.DefaultConfig().ServiceName
	}

	// Process environment variables - split comma-separated values
	var processedEnvVars []string
	for _, envVarEntry := range otelEnvironmentVariables {
		// Split by comma and trim whitespace
		envVars := strings.Split(envVarEntry, ",")
		for _, envVar := range envVars {
			trimmed := strings.TrimSpace(envVar)
			if trimmed != "" {
				processedEnvVars = append(processedEnvVars, trimmed)
			}
		}
	}

	b.config.TelemetryConfig = &telemetry.Config{
		Endpoint:                    otelEndpoint,
		ServiceName:                 serviceName,
		ServiceVersion:              telemetry.DefaultConfig().ServiceVersion,
		TracingEnabled:              otelTracingEnabled,
		MetricsEnabled:              otelMetricsEnabled,
		SamplingRate:                otelSamplingRate,
		Headers:                     headers,
		Insecure:                    otelInsecure,
		EnablePrometheusMetricsPath: otelEnablePrometheusMetricsPath,
		EnvironmentVariables:        processedEnvVars,
	}
	return b
}

// WithToolsFilter sets the tools filter
func (b *RunConfigBuilder) WithToolsFilter(toolsFilter []string) *RunConfigBuilder {
	b.config.ToolsFilter = toolsFilter
	return b
}

// WithToolOverride sets the tool override map for the RunConfig
// This method is mutually exclusive with WithToolOverrideFile
func (b *RunConfigBuilder) WithToolOverride(toolOverride map[string]ToolOverride) *RunConfigBuilder {
	b.config.ToolOverride = toolOverride
	return b
}

// WithToolOverrideFile sets the path to the tool override file for the RunConfig
// This method is mutually exclusive with WithToolOverride
func (b *RunConfigBuilder) WithToolOverrideFile(toolOverrideFile string) *RunConfigBuilder {
	b.config.ToolOverrideFile = toolOverrideFile
	return b
}

// WithIgnoreConfig sets the ignore configuration
func (b *RunConfigBuilder) WithIgnoreConfig(ignoreConfig *ignore.Config) *RunConfigBuilder {
	b.config.IgnoreConfig = ignoreConfig
	return b
}

// WithMiddlewareFromFlags creates middleware configurations directly from flag values
func (b *RunConfigBuilder) WithMiddlewareFromFlags(
	oidcConfig *auth.TokenValidatorConfig,
	toolsFilter []string,
	telemetryConfig *telemetry.Config,
	authzConfigPath string,
	enableAudit bool,
	auditConfigPath string,
	serverName string,
	transportType string,
) *RunConfigBuilder {
	var middlewareConfigs []types.MiddlewareConfig

	// Add tool filter middlewares
	middlewareConfigs = b.addToolFilterMiddlewares(middlewareConfigs, toolsFilter)

	// Add core middlewares (always present)
	middlewareConfigs = b.addCoreMiddlewares(middlewareConfigs, oidcConfig)

	// Add optional middlewares
	middlewareConfigs = b.addTelemetryMiddleware(middlewareConfigs, telemetryConfig, serverName, transportType)
	middlewareConfigs = b.addAuthzMiddleware(middlewareConfigs, authzConfigPath)
	middlewareConfigs = b.addAuditMiddleware(middlewareConfigs, enableAudit, auditConfigPath, serverName)

	// Set the populated middleware configs
	b.config.MiddlewareConfigs = middlewareConfigs
	return b
}

// addToolFilterMiddlewares adds tool filter middlewares if tools filter is provided
func (*RunConfigBuilder) addToolFilterMiddlewares(
	middlewareConfigs []types.MiddlewareConfig, toolsFilter []string,
) []types.MiddlewareConfig {
	if len(toolsFilter) == 0 {
		return middlewareConfigs
	}

	toolFilterParams := mcp.ToolFilterMiddlewareParams{
		FilterTools: toolsFilter,
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
func (*RunConfigBuilder) addCoreMiddlewares(
	middlewareConfigs []types.MiddlewareConfig, oidcConfig *auth.TokenValidatorConfig,
) []types.MiddlewareConfig {
	// Authentication middleware (always present)
	authParams := auth.MiddlewareParams{
		OIDCConfig: oidcConfig,
	}
	if authConfig, err := types.NewMiddlewareConfig(auth.MiddlewareType, authParams); err == nil {
		middlewareConfigs = append(middlewareConfigs, *authConfig)
	}

	// MCP Parser middleware (always present)
	mcpParserParams := mcp.ParserMiddlewareParams{}
	if mcpParserConfig, err := types.NewMiddlewareConfig(mcp.ParserMiddlewareType, mcpParserParams); err == nil {
		middlewareConfigs = append(middlewareConfigs, *mcpParserConfig)
	}

	return middlewareConfigs
}

// addTelemetryMiddleware adds telemetry middleware if enabled
func (*RunConfigBuilder) addTelemetryMiddleware(
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
func (*RunConfigBuilder) addAuthzMiddleware(
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
func (*RunConfigBuilder) addAuditMiddleware(
	middlewareConfigs []types.MiddlewareConfig,
	enableAudit bool,
	auditConfigPath, serverName string,
) []types.MiddlewareConfig {
	if !enableAudit && auditConfigPath == "" {
		return middlewareConfigs
	}

	auditParams := audit.MiddlewareParams{
		ConfigPath: auditConfigPath, // Keep for backwards compatibility
		Component:  serverName,      // Use server name as component
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

// Build creates the final RunConfig instance with validation and processing
func (b *RunConfigBuilder) Build(
	ctx context.Context,
	imageMetadata *registry.ImageMetadata,
	envVars map[string]string,
	envVarValidator EnvVarValidator,
) (*RunConfig, error) {
	// When using the CLI validation strategy, this is where the prompting for
	// missing environment variables will happen.
	processedEnvVars, err := envVarValidator.Validate(ctx, imageMetadata, b.config, envVars)
	if err != nil {
		return nil, fmt.Errorf("failed to validate environment variables: %v", err)
	}

	// Do some final validation which can only be done after everything else is set.
	// Apply image metadata overrides if needed.
	if err := b.validateConfig(imageMetadata); err != nil {
		return nil, fmt.Errorf("failed to validate run config: %v", err)
	}

	// Now set environment variables with the correct transport and ports resolved
	if _, err := b.config.WithEnvironmentVariables(processedEnvVars); err != nil {
		return nil, fmt.Errorf("failed to set environment variables: %v", err)
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
func (b *RunConfigBuilder) validateConfig(imageMetadata *registry.ImageMetadata) error {
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
			logger.Debugf("Using registry mcpTransport: %s", imageMetadata.Transport)
			mcpTransport = imageMetadata.Transport
		} else {
			logger.Debugf("Defaulting mcpTransport to stdio")
			mcpTransport = types.TransportTypeStdio.String()
		}
	}
	// Set mcpTransport
	if _, err = c.WithTransport(mcpTransport); err != nil {
		return err
	}

	// Use registry target port if not overridden and if the mcpTransport is HTTP-based.
	targetPort := b.targetPort
	if imageMetadata != nil {
		isHTTPServer := mcpTransport == types.TransportTypeSSE.String() ||
			mcpTransport == types.TransportTypeStreamableHTTP.String()
		if targetPort == 0 && isHTTPServer && imageMetadata.TargetPort > 0 {
			logger.Debugf("Using registry target port: %d", imageMetadata.TargetPort)
			targetPort = imageMetadata.TargetPort
		}
	}
	// Configure ports and target host
	if _, err = c.WithPorts(b.port, targetPort); err != nil {
		return err
	}

	// Load or default the permission profile
	// NOTE: This must be done before processing volume mounts
	c.PermissionProfile, err = b.loadPermissionProfile(imageMetadata)
	if err != nil {
		return err
	}

	// Process volume mounts
	if err = b.processVolumeMounts(); err != nil {
		return err
	}

	// Generate container name if not already set
	_, wasModified := c.WithContainerName()
	if wasModified && c.Name != "" {
		logger.Warnf("The provided name '%s' contained invalid characters and was sanitized", c.Name)
	}

	// Add standard labels
	c.WithStandardLabels()

	// Add authorization configuration if provided
	if c.AuthzConfigPath != "" {
		authzConfig, err := authz.LoadConfig(c.AuthzConfigPath)
		if err != nil {
			return fmt.Errorf("failed to load authorization configuration: %v", err)
		}
		c.WithAuthz(authzConfig)
	}

	// Add audit configuration if provided
	if c.AuditConfigPath != "" {
		auditConfig, err := audit.LoadFromFile(c.AuditConfigPath)
		if err != nil {
			return fmt.Errorf("failed to load audit configuration: %v", err)
		}
		c.WithAudit(auditConfig)
	}
	// Note: AuditConfig is already set from --enable-audit flag if provided

	if imageMetadata != nil && len(imageMetadata.Args) > 0 {
		if len(c.CmdArgs) == 0 {
			// No user args provided, use registry defaults
			logger.Debugf("Using registry default args: %v", imageMetadata.Args)
			c.CmdArgs = append(c.CmdArgs, imageMetadata.Args...)
		}
	}

	if c.ToolsFilter != nil && imageMetadata != nil && imageMetadata.Tools != nil {
		logger.Debugf("Using tools filter: %v", c.ToolsFilter)
		for _, tool := range c.ToolsFilter {
			if !slices.Contains(imageMetadata.Tools, tool) {
				return fmt.Errorf("tool %s not found in registry", tool)
			}
		}
	}

	// Validate tool overrides - ensure they are mutually exclusive
	if len(c.ToolOverride) > 0 && c.ToolOverrideFile != "" {
		return fmt.Errorf("both tool override map and tool override file are set, they are mutually exclusive")
	}

	// Validate tool override map entries if present
	if len(c.ToolOverride) > 0 {
		for toolName, override := range c.ToolOverride {
			if override.Name == "" && override.Description == "" {
				return fmt.Errorf("tool override for %s must have either Name or Description set", toolName)
			}
		}
	}

	return nil
}

func (b *RunConfigBuilder) loadPermissionProfile(imageMetadata *registry.ImageMetadata) (*permissions.Profile, error) {
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

		logger.Debugf("Using registry permission profile: %v", imageMetadata.Permissions)
		return imageMetadata.Permissions, nil
	}

	// If no metadata is available, use the network permission profile as default.
	logger.Debugf("Using default permission profile: %s", permissions.ProfileNetwork)
	return permissions.BuiltinNetworkProfile(), nil
}

// processVolumeMounts processes volume mounts and adds them to the permission profile
func (b *RunConfigBuilder) processVolumeMounts() error {

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
			return fmt.Errorf("invalid volume format: %s (%v)", volume, err)
		}

		// Check for duplicate mount target
		if existingSource, isDuplicate := existingMounts[target]; isDuplicate {
			logger.Warnf("Skipping duplicate mount target: %s (already mounted from %s)",
				target, existingSource)
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

		logger.Infof("Adding volume mount: %s -> %s (%s)",
			source, target,
			map[bool]string{true: "read-only", false: "read-write"}[readOnly])
	}

	return nil
}

// BuildForOperator creates a RunConfig for operator use, using the same validation as CLI
func (b *RunConfigBuilder) BuildForOperator() (*RunConfig, error) {
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
func (b *RunConfigBuilder) WithEnvVars(envVars map[string]string) *RunConfigBuilder {
	if b.config.EnvVars == nil {
		b.config.EnvVars = make(map[string]string)
	}
	for key, value := range envVars {
		b.config.EnvVars[key] = value
	}
	return b
}

// WithEnvFile adds environment variables from a single file
func (b *RunConfigBuilder) WithEnvFile(filePath string) (*RunConfigBuilder, error) {
	if _, err := b.config.WithEnvFile(filePath); err != nil {
		return nil, err
	}
	return b, nil
}

// WithEnvFilesFromDirectory adds environment variables from all files in a directory
func (b *RunConfigBuilder) WithEnvFilesFromDirectory(dirPath string) (*RunConfigBuilder, error) {
	if _, err := b.config.WithEnvFilesFromDirectory(dirPath); err != nil {
		return nil, err
	}
	return b, nil
}

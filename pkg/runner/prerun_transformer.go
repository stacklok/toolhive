package runner

import (
	"context"
	"fmt"
	"os"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	defaultTransportType = "streamable-http"
)

// PreRunTransformer handles the transformation from PreRunConfig to RunConfig
type PreRunTransformer struct {
	ctx             context.Context
	rt              runtime.Deployer
	envVarValidator EnvVarValidator
	imageManager    images.ImageManager
}

// NewPreRunTransformer creates a new PreRunTransformer
func NewPreRunTransformer(ctx context.Context) (*PreRunTransformer, error) {
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create container runtime: %v", err)
	}

	var envVarValidator EnvVarValidator
	if process.IsDetached() || runtime.IsKubernetesRuntime() {
		envVarValidator = &DetachedEnvVarValidator{}
	} else {
		envVarValidator = &CLIEnvVarValidator{}
	}

	imageManager := images.NewImageManager(ctx)

	return &PreRunTransformer{
		ctx:             ctx,
		rt:              rt,
		envVarValidator: envVarValidator,
		imageManager:    imageManager,
	}, nil
}

// TransformToRunConfig converts a PreRunConfig to a RunConfig
func (t *PreRunTransformer) TransformToRunConfig(
	preConfig *PreRunConfig,
	runFlags RunFlagsInterface,
	cmdArgs []string,
	debugMode bool,
	validatedHost string,
	oidcConfig *auth.TokenValidatorConfig,
	telemetryConfig *telemetry.Config,
) (*RunConfig, error) {
	switch preConfig.Type {
	case PreRunConfigTypeConfigFile:
		return t.transformFromConfigFile(preConfig)
	case PreRunConfigTypeRemoteURL:
		return t.transformFromRemoteURL(preConfig, runFlags, cmdArgs, debugMode, validatedHost, oidcConfig, telemetryConfig)
	case PreRunConfigTypeProtocolScheme:
		return t.transformFromProtocolScheme(preConfig, runFlags, cmdArgs, debugMode, validatedHost, oidcConfig, telemetryConfig)
	case PreRunConfigTypeRegistry:
		return t.transformFromRegistry(preConfig, runFlags, cmdArgs, debugMode, validatedHost, oidcConfig, telemetryConfig)
	case PreRunConfigTypeContainerImage:
		return t.transformFromContainerImage(preConfig, runFlags, cmdArgs, debugMode, validatedHost, oidcConfig, telemetryConfig)
	default:
		return nil, fmt.Errorf("unsupported PreRunConfig type: %s", preConfig.Type)
	}
}

// transformFromConfigFile handles loading from a configuration file
func (t *PreRunTransformer) transformFromConfigFile(preConfig *PreRunConfig) (*RunConfig, error) {
	src := preConfig.ParsedSource.(*ConfigFileSource)

	configFile, err := os.Open(src.FilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open configuration file '%s': %w", src.FilePath, err)
	}
	defer configFile.Close()

	runConfig, err := ReadJSON(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to parse configuration file: %w", err)
	}

	// Set the runtime in the config
	runConfig.Deployer = t.rt

	return runConfig, nil
}

// transformFromRemoteURL handles remote MCP servers
func (t *PreRunTransformer) transformFromRemoteURL(
	preConfig *PreRunConfig,
	runFlags RunFlagsInterface,
	cmdArgs []string,
	debugMode bool,
	validatedHost string,
	oidcConfig *auth.TokenValidatorConfig,
	telemetryConfig *telemetry.Config,
) (*RunConfig, error) {
	src := preConfig.ParsedSource.(*RemoteURLSource)

	logger.Debugf("Creating RunConfig for remote URL: %s", src.URL)

	return t.buildRunConfigFromSource(
		src.URL, // imageURL (will be empty for remote)
		nil,     // serverMetadata
		runFlags,
		cmdArgs,
		debugMode,
		validatedHost,
		oidcConfig,
		telemetryConfig,
		src.URL, // remoteURL
	)
}

// transformFromProtocolScheme handles protocol scheme builds
func (t *PreRunTransformer) transformFromProtocolScheme(
	preConfig *PreRunConfig,
	runFlags RunFlagsInterface,
	cmdArgs []string,
	debugMode bool,
	validatedHost string,
	oidcConfig *auth.TokenValidatorConfig,
	telemetryConfig *telemetry.Config,
) (*RunConfig, error) {
	src := preConfig.ParsedSource.(*ProtocolSchemeSource)

	logger.Debugf("Building container from protocol scheme: %s://%s", src.ProtocolTransportType, src.Package)

	// Build the container image from the protocol scheme
	generatedImage, err := HandleProtocolScheme(t.ctx, t.imageManager, preConfig.Source, runFlags.GetCACertPath())
	if err != nil {
		return nil, fmt.Errorf("failed to build image from protocol scheme: %w", err)
	}

	logger.Debugf("Built image: %s", generatedImage)

	return t.buildRunConfigFromSource(
		generatedImage, // imageURL
		nil,            // serverMetadata
		runFlags,
		cmdArgs,
		debugMode,
		validatedHost,
		oidcConfig,
		telemetryConfig,
		"", // remoteURL
	)
}

// transformFromRegistry handles registry server lookups
func (t *PreRunTransformer) transformFromRegistry(
	preConfig *PreRunConfig,
	runFlags RunFlagsInterface,
	cmdArgs []string,
	debugMode bool,
	validatedHost string,
	oidcConfig *auth.TokenValidatorConfig,
	telemetryConfig *telemetry.Config,
) (*RunConfig, error) {
	src := preConfig.ParsedSource.(*RegistrySource)

	provider, err := registry.GetDefaultProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to get registry provider: %v", err)
	}

	server, err := provider.GetServer(src.ServerName)
	if err != nil {
		return nil, fmt.Errorf("failed to get server from registry: %v", err)
	}

	if server.IsRemote() {
		// Remote server from registry
		logger.Debugf("Found remote server in registry: %s", src.ServerName)
		return t.buildRunConfigFromSource(
			src.ServerName, // imageURL (server name)
			server,         // serverMetadata
			runFlags,
			cmdArgs,
			debugMode,
			validatedHost,
			oidcConfig,
			telemetryConfig,
			"", // remoteURL (will be set from metadata)
		)
	}

	// Container server from registry
	imageMetadata, err := provider.GetImageServer(src.ServerName)
	if err != nil {
		return nil, fmt.Errorf("failed to get image metadata from registry: %v", err)
	}

	logger.Debugf("Found container server in registry: %s -> %s", src.ServerName, imageMetadata.Image)

	// Pull the image if necessary
	if err := t.pullImageIfNeeded(imageMetadata.Image); err != nil {
		return nil, fmt.Errorf("failed to pull image: %v", err)
	}

	return t.buildRunConfigFromSource(
		imageMetadata.Image, // imageURL
		imageMetadata,       // serverMetadata
		runFlags,
		cmdArgs,
		debugMode,
		validatedHost,
		oidcConfig,
		telemetryConfig,
		"", // remoteURL
	)
}

// transformFromContainerImage handles direct container image references
func (t *PreRunTransformer) transformFromContainerImage(
	preConfig *PreRunConfig,
	runFlags RunFlagsInterface,
	cmdArgs []string,
	debugMode bool,
	validatedHost string,
	oidcConfig *auth.TokenValidatorConfig,
	telemetryConfig *telemetry.Config,
) (*RunConfig, error) {
	src := preConfig.ParsedSource.(*ContainerImageSource)

	logger.Debugf("Using direct container image: %s", src.ImageRef)

	// Pull the image if necessary
	if err := t.pullImageIfNeeded(src.ImageRef); err != nil {
		return nil, fmt.Errorf("failed to pull image: %v", err)
	}

	return t.buildRunConfigFromSource(
		src.ImageRef, // imageURL
		nil,          // serverMetadata
		runFlags,
		cmdArgs,
		debugMode,
		validatedHost,
		oidcConfig,
		telemetryConfig,
		"", // remoteURL
	)
}

// buildRunConfigFromSource builds the final RunConfig using the existing builder pattern
func (t *PreRunTransformer) buildRunConfigFromSource(
	imageURL string,
	serverMetadata registry.ServerMetadata,
	runFlags RunFlagsInterface,
	cmdArgs []string,
	debugMode bool,
	validatedHost string,
	oidcConfig *auth.TokenValidatorConfig,
	telemetryConfig *telemetry.Config,
	remoteURL string,
) (*RunConfig, error) {
	// Parse environment variables
	envVars, err := environment.ParseEnvironmentVariables(runFlags.GetEnv())
	if err != nil {
		return nil, fmt.Errorf("failed to parse environment variables: %v", err)
	}

	// Determine transport type and proxy mode
	transportType, proxyMode, err := t.determineTransportAndProxyMode(runFlags, serverMetadata)
	if err != nil {
		return nil, err
	}

	// Create base builder
	builder := t.createBaseBuilder(imageURL, runFlags, cmdArgs, debugMode, validatedHost, remoteURL, transportType, proxyMode)

	// Configure middleware and authentication
	builder = t.configureMiddlewareAndAuth(builder, serverMetadata, runFlags, oidcConfig, telemetryConfig, remoteURL)

	// Configure legacy OIDC and telemetry
	builder = t.configureLegacyOIDCAndTelemetry(builder, runFlags, oidcConfig, telemetryConfig)

	// Process environment files
	builder, err = t.processEnvironmentFiles(builder, runFlags)
	if err != nil {
		return nil, err
	}

	imageMetadata, _ := serverMetadata.(*registry.ImageMetadata)
	return builder.Build(t.ctx, imageMetadata, envVars, t.envVarValidator)
}

// determineTransportAndProxyMode determines the transport type and validates proxy mode
func (*PreRunTransformer) determineTransportAndProxyMode(
	runFlags RunFlagsInterface,
	serverMetadata registry.ServerMetadata,
) (string, string, error) {
	// Determine transport type
	transportType := defaultTransportType
	if runFlags.GetTransport() != "" {
		transportType = runFlags.GetTransport()
	} else if serverMetadata != nil {
		transportType = serverMetadata.GetTransport()
	}

	// Validate and setup proxy mode
	proxyMode := runFlags.GetProxyMode()
	if !types.IsValidProxyMode(proxyMode) {
		if proxyMode == "" {
			proxyMode = types.ProxyModeSSE.String() // default to SSE for backward compatibility
		} else {
			return "", "", fmt.Errorf("invalid value for --proxy-mode: %s", proxyMode)
		}
	}

	return transportType, proxyMode, nil
}

// createBaseBuilder creates the base RunConfig builder with core settings
func (t *PreRunTransformer) createBaseBuilder(
	imageURL string,
	runFlags RunFlagsInterface,
	cmdArgs []string,
	debugMode bool,
	validatedHost, remoteURL, transportType, proxyMode string,
) *RunConfigBuilder {
	return NewRunConfigBuilder().
		WithRuntime(t.rt).
		WithCmdArgs(cmdArgs).
		WithName(runFlags.GetName()).
		WithImage(imageURL).
		WithRemoteURL(remoteURL).
		WithHost(validatedHost).
		WithTargetHost(runFlags.GetTargetHost()).
		WithDebug(debugMode).
		WithVolumes(runFlags.GetVolumes()).
		WithSecrets(runFlags.GetSecrets()).
		WithAuthzConfigPath(runFlags.GetAuthzConfig()).
		WithAuditConfigPath(runFlags.GetAuditConfig()).
		WithPermissionProfileNameOrPath(runFlags.GetPermissionProfile()).
		WithNetworkIsolation(runFlags.GetIsolateNetwork()).
		WithK8sPodPatch(runFlags.GetK8sPodPatch()).
		WithProxyMode(types.ProxyMode(proxyMode)).
		WithTransportAndPorts(transportType, runFlags.GetProxyPort(), runFlags.GetTargetPort()).
		WithAuditEnabled(runFlags.GetEnableAudit(), runFlags.GetAuditConfig()).
		WithLabels(runFlags.GetLabels()).
		WithGroup(runFlags.GetGroup()).
		WithIgnoreConfig(&ignore.Config{
			LoadGlobal:    runFlags.GetIgnoreGlobally(),
			PrintOverlays: runFlags.GetPrintOverlays(),
		})
}

// configureMiddlewareAndAuth configures middleware and authentication settings
func (*PreRunTransformer) configureMiddlewareAndAuth(
	builder *RunConfigBuilder,
	serverMetadata registry.ServerMetadata,
	runFlags RunFlagsInterface,
	oidcConfig *auth.TokenValidatorConfig,
	telemetryConfig *telemetry.Config,
	remoteURL string,
) *RunConfigBuilder {
	// Configure middleware from flags
	builder = builder.WithMiddlewareFromFlags(
		oidcConfig,
		runFlags.GetToolsFilter(),
		telemetryConfig,
		runFlags.GetAuthzConfig(),
		runFlags.GetEnableAudit(),
		runFlags.GetAuditConfig(),
		runFlags.GetName(),
		runFlags.GetTransport(),
	)

	// Handle remote authentication if applicable
	if remoteServerMetadata, ok := serverMetadata.(*registry.RemoteServerMetadata); ok {
		if remoteAuthConfig := getRemoteAuthFromRemoteServerMetadata(remoteServerMetadata, runFlags); remoteAuthConfig != nil {
			builder = builder.WithRemoteAuth(remoteAuthConfig)
		}
	}
	if remoteURL != "" {
		if remoteAuthConfig := getRemoteAuthFromRunFlags(runFlags); remoteAuthConfig != nil {
			builder = builder.WithRemoteAuth(remoteAuthConfig)
		}
	}

	// Load authz config if path is provided
	if runFlags.GetAuthzConfig() != "" {
		if authzConfigData, err := authz.LoadConfig(runFlags.GetAuthzConfig()); err == nil {
			builder = builder.WithAuthzConfig(authzConfigData)
		}
		// Note: Path is already set via WithAuthzConfigPath above
	}

	return builder
}

// configureLegacyOIDCAndTelemetry configures legacy OIDC and telemetry settings
func (*PreRunTransformer) configureLegacyOIDCAndTelemetry(
	builder *RunConfigBuilder,
	runFlags RunFlagsInterface,
	oidcConfig *auth.TokenValidatorConfig,
	telemetryConfig *telemetry.Config,
) *RunConfigBuilder {
	// Get OIDC and telemetry values for legacy configuration
	oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID, oidcClientSecret := extractOIDCValues(oidcConfig)
	finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables := extractTelemetryValues(telemetryConfig)

	// Set additional configurations that are still needed in old format for other parts of the system
	return builder.WithOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID, oidcClientSecret,
		runFlags.GetThvCABundle(), runFlags.GetJWKSAuthTokenFile(), runFlags.GetResourceURL(), runFlags.GetJWKSAllowPrivateIP()).
		WithTelemetryConfig(finalOtelEndpoint, runFlags.GetOtelEnablePrometheusMetricsPath(), runFlags.GetOtelServiceName(),
			finalOtelSamplingRate, runFlags.GetOtelHeaders(), runFlags.GetOtelInsecure(), finalOtelEnvironmentVariables).
		WithToolsFilter(runFlags.GetToolsFilter())
}

// processEnvironmentFiles processes environment files and directories
func (*PreRunTransformer) processEnvironmentFiles(
	builder *RunConfigBuilder,
	runFlags RunFlagsInterface,
) (*RunConfigBuilder, error) {
	var err error

	// Process environment files
	if runFlags.GetEnvFile() != "" {
		builder, err = builder.WithEnvFile(runFlags.GetEnvFile())
		if err != nil {
			return nil, fmt.Errorf("failed to process env file %s: %v", runFlags.GetEnvFile(), err)
		}
	}
	if runFlags.GetEnvFileDir() != "" {
		builder, err = builder.WithEnvFilesFromDirectory(runFlags.GetEnvFileDir())
		if err != nil {
			return nil, fmt.Errorf("failed to process env files from directory %s: %v", runFlags.GetEnvFileDir(), err)
		}
	}

	return builder, nil
}

// pullImageIfNeeded pulls an image if it doesn't exist locally or has the latest tag
func (t *PreRunTransformer) pullImageIfNeeded(imageRef string) error {
	if !runtime.IsKubernetesRuntime() {
		return t.pullImage(imageRef)
	}
	return nil
}

// pullImage is a copy of the pullImage function from retriever package
// since it's not exported. This pulls an image from a remote registry if it has the "latest" tag
// or if it doesn't exist locally.
func (t *PreRunTransformer) pullImage(image string) error {
	// Check if the image has the "latest" tag
	isLatestTag := t.hasLatestTag(image)

	if isLatestTag {
		// For "latest" tag, try to pull first
		logger.Infof("Image %s has 'latest' tag, pulling to ensure we have the most recent version...", image)
		err := t.imageManager.PullImage(t.ctx, image)
		if err != nil {
			// Pull failed, check if it exists locally
			logger.Infof("Pull failed, checking if image exists locally: %s", image)
			imageExists, checkErr := t.imageManager.ImageExists(t.ctx, image)
			if checkErr != nil {
				return fmt.Errorf("failed to check if image exists: %v", checkErr)
			}

			if imageExists {
				logger.Debugf("Using existing local image: %s", image)
			} else {
				return fmt.Errorf("image not found: %s", image)
			}
		} else {
			logger.Infof("Successfully pulled image: %s", image)
		}
	} else {
		// For non-latest tags, check locally first
		logger.Debugf("Checking if image exists locally: %s", image)
		imageExists, err := t.imageManager.ImageExists(t.ctx, image)
		logger.Debugf("ImageExists locally: %t", imageExists)
		if err != nil {
			return fmt.Errorf("failed to check if image exists locally: %v", err)
		}

		if imageExists {
			logger.Debugf("Using existing local image: %s", image)
		} else {
			// Image doesn't exist locally, try to pull
			logger.Infof("Image %s not found locally, pulling...", image)
			if err := t.imageManager.PullImage(t.ctx, image); err != nil {
				return fmt.Errorf("image not found: %s", image)
			}
			logger.Infof("Successfully pulled image: %s", image)
		}
	}

	return nil
}

// hasLatestTag checks if the given image reference has the "latest" tag or no tag (which defaults to "latest")
func (*PreRunTransformer) hasLatestTag(imageRef string) bool {
	// We can reuse the existing logic from parseContainerImageRef
	_, _, tag, err := parseContainerImageRef(imageRef)
	if err != nil {
		logger.Warnf("Warning: Failed to parse image reference: %v", err)
		return false
	}
	return tag == "latest"
}

// Helper functions that need to be implemented or imported from existing code

// extractOIDCValues extracts OIDC values from the OIDC config for legacy configuration
func extractOIDCValues(config *auth.TokenValidatorConfig) (string, string, string, string, string, string) {
	if config == nil {
		return "", "", "", "", "", ""
	}
	return config.Issuer, config.Audience, config.JWKSURL, config.IntrospectionURL, config.ClientID, config.ClientSecret
}

// extractTelemetryValues extracts telemetry values from the telemetry config for legacy configuration
func extractTelemetryValues(config *telemetry.Config) (string, float64, []string) {
	if config == nil {
		return "", 0.0, nil
	}
	return config.Endpoint, config.SamplingRate, config.EnvironmentVariables
}

// getRemoteAuthFromRemoteServerMetadata creates RemoteAuthConfig from RemoteServerMetadata
func getRemoteAuthFromRemoteServerMetadata(
	remoteServerMetadata *registry.RemoteServerMetadata,
	runFlags RunFlagsInterface,
) *RemoteAuthConfig {
	if remoteServerMetadata == nil {
		return nil
	}

	if remoteServerMetadata.OAuthConfig != nil {
		remoteAuthFlags := runFlags.GetRemoteAuthFlags()
		return &RemoteAuthConfig{
			ClientID:     remoteAuthFlags.GetRemoteAuthClientID(),
			ClientSecret: remoteAuthFlags.GetRemoteAuthClientSecret(),
			Scopes:       remoteServerMetadata.OAuthConfig.Scopes,
			SkipBrowser:  remoteAuthFlags.GetRemoteAuthSkipBrowser(),
			Timeout:      remoteAuthFlags.GetRemoteAuthTimeout(),
			CallbackPort: remoteServerMetadata.OAuthConfig.CallbackPort,
			Issuer:       remoteServerMetadata.OAuthConfig.Issuer,
			AuthorizeURL: remoteServerMetadata.OAuthConfig.AuthorizeURL,
			TokenURL:     remoteServerMetadata.OAuthConfig.TokenURL,
			OAuthParams:  remoteServerMetadata.OAuthConfig.OAuthParams,
			Headers:      remoteServerMetadata.Headers,
			EnvVars:      remoteServerMetadata.EnvVars,
		}
	}
	return nil
}

// getRemoteAuthFromRunFlags creates RemoteAuthConfig from RunFlags
func getRemoteAuthFromRunFlags(runFlags RunFlagsInterface) *RemoteAuthConfig {
	remoteAuthFlags := runFlags.GetRemoteAuthFlags()
	if remoteAuthFlags.GetEnableRemoteAuth() || remoteAuthFlags.GetRemoteAuthClientID() != "" {
		return &RemoteAuthConfig{
			ClientID:     remoteAuthFlags.GetRemoteAuthClientID(),
			ClientSecret: remoteAuthFlags.GetRemoteAuthClientSecret(),
			Scopes:       remoteAuthFlags.GetRemoteAuthScopes(),
			SkipBrowser:  remoteAuthFlags.GetRemoteAuthSkipBrowser(),
			Timeout:      remoteAuthFlags.GetRemoteAuthTimeout(),
			CallbackPort: remoteAuthFlags.GetRemoteAuthCallbackPort(),
			Issuer:       remoteAuthFlags.GetRemoteAuthIssuer(),
			AuthorizeURL: remoteAuthFlags.GetRemoteAuthAuthorizeURL(),
			TokenURL:     remoteAuthFlags.GetRemoteAuthTokenURL(),
			OAuthParams:  runFlags.GetOAuthParams(),
		}
	}
	return nil
}

package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/cli"
	cfg "github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	defaultTransportType = "streamable-http"
)

// RunFlags holds the configuration for running MCP servers
type RunFlags struct {
	// Transport and proxy settings
	Transport  string
	ProxyMode  string
	Host       string
	ProxyPort  int
	TargetPort int
	TargetHost string

	// Server configuration
	Name              string
	Group             string
	PermissionProfile string
	Env               []string
	Volumes           []string
	Secrets           []string

	// Remote MCP server support
	RemoteURL string

	// Security and audit
	AuthzConfig string
	AuditConfig string
	EnableAudit bool
	K8sPodPatch string

	// Image verification
	CACertPath  string
	VerifyImage string

	// OIDC configuration
	ThvCABundle        string
	JWKSAuthTokenFile  string
	JWKSAllowPrivateIP bool

	// OAuth discovery configuration
	ResourceURL string

	// Telemetry configuration
	OtelEndpoint                    string
	OtelServiceName                 string
	OtelTracingEnabled              bool
	OtelMetricsEnabled              bool
	OtelSamplingRate                float64
	OtelHeaders                     []string
	OtelInsecure                    bool
	OtelEnablePrometheusMetricsPath bool
	OtelEnvironmentVariables        []string // renamed binding to otel-env-vars

	// Network isolation
	IsolateNetwork bool

	// Labels
	Labels []string

	// Execution mode
	Foreground bool

	// Tools filter
	ToolsFilter []string
	// Tools override file
	ToolsOverride string

	// Configuration import
	FromConfig string

	// Environment file processing
	EnvFile    string
	EnvFileDir string

	// Ignore functionality
	IgnoreGlobally bool
	PrintOverlays  bool

	// Remote authentication
	RemoteAuthFlags RemoteAuthFlags
	OAuthParams     map[string]string
}

// AddRunFlags adds all the run flags to a command
func AddRunFlags(cmd *cobra.Command, config *RunFlags) {
	cmd.Flags().StringVar(&config.Transport, "transport", "", "Transport mode (sse, streamable-http or stdio)")
	cmd.Flags().StringVar(&config.ProxyMode, "proxy-mode", "sse", "Proxy mode for stdio transport (sse or streamable-http)")
	cmd.Flags().StringVar(&config.Name, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	cmd.Flags().StringVar(&config.Group, "group", "default",
		"Name of the group this workload belongs to (defaults to 'default' if not specified)")
	cmd.Flags().StringVar(&config.Host, "host", transport.LocalhostIPv4, "Host for the HTTP proxy to listen on (IP or hostname)")
	cmd.Flags().IntVar(&config.ProxyPort, "proxy-port", 0, "Port for the HTTP proxy to listen on (host port)")
	cmd.Flags().IntVar(&config.TargetPort, "target-port", 0,
		"Port for the container to expose (only applicable to SSE or Streamable HTTP transport)")
	cmd.Flags().StringVar(
		&config.TargetHost,
		"target-host",
		transport.LocalhostIPv4,
		"Host to forward traffic to (only applicable to SSE or Streamable HTTP transport)")
	cmd.Flags().StringVar(
		&config.PermissionProfile,
		"permission-profile",
		"",
		"Permission profile to use (none, network, or path to JSON file)",
	)
	cmd.Flags().StringArrayVarP(
		&config.Env,
		"env",
		"e",
		[]string{},
		"Environment variables to pass to the MCP server (format: KEY=VALUE)",
	)
	cmd.Flags().StringArrayVarP(
		&config.Volumes,
		"volume",
		"v",
		[]string{},
		"Mount a volume into the container (format: host-path:container-path[:ro])",
	)
	cmd.Flags().StringArrayVar(
		&config.Secrets,
		"secret",
		[]string{},
		"Specify a secret to be fetched from the secrets manager and set as an environment variable (format: NAME,target=TARGET)",
	)
	cmd.Flags().StringVar(&config.AuthzConfig, "authz-config", "", "Path to the authorization configuration file")
	cmd.Flags().StringVar(&config.AuditConfig, "audit-config", "", "Path to the audit configuration file")
	cmd.Flags().BoolVar(&config.EnableAudit, "enable-audit", false, "Enable audit logging with default configuration")
	cmd.Flags().StringVar(&config.K8sPodPatch, "k8s-pod-patch", "",
		"JSON string to patch the Kubernetes pod template (only applicable when using Kubernetes runtime)")
	cmd.Flags().StringVar(&config.CACertPath, "ca-cert", "", "Path to a custom CA certificate file to use for container builds")
	cmd.Flags().StringVar(&config.VerifyImage, "image-verification", retriever.VerifyImageWarn,
		fmt.Sprintf("Set image verification mode (%s, %s, %s)",
			retriever.VerifyImageWarn, retriever.VerifyImageEnabled, retriever.VerifyImageDisabled))
	cmd.Flags().StringVar(&config.ThvCABundle, "thv-ca-bundle", "",
		"Path to CA certificate bundle for ToolHive HTTP operations (JWKS, OIDC discovery, etc.)")
	cmd.Flags().StringVar(&config.JWKSAuthTokenFile, "jwks-auth-token-file", "",
		"Path to file containing bearer token for authenticating JWKS/OIDC requests")
	cmd.Flags().BoolVar(&config.JWKSAllowPrivateIP, "jwks-allow-private-ip", false,
		"Allow JWKS/OIDC endpoints on private IP addresses (use with caution)")

	// Remote authentication flags
	AddRemoteAuthFlags(cmd, &config.RemoteAuthFlags)

	// OAuth discovery configuration
	cmd.Flags().StringVar(&config.ResourceURL, "resource-url", "",
		"Explicit resource URL for OAuth discovery endpoint (RFC 9728)")

	// OpenTelemetry flags updated per origin/main
	cmd.Flags().StringVar(&config.OtelEndpoint, "otel-endpoint", "",
		"OpenTelemetry OTLP endpoint URL (e.g., https://api.honeycomb.io)")
	cmd.Flags().StringVar(&config.OtelServiceName, "otel-service-name", "",
		"OpenTelemetry service name (defaults to toolhive-mcp-proxy)")
	cmd.Flags().BoolVar(&config.OtelTracingEnabled, "otel-tracing-enabled", true,
		"Enable distributed tracing (when OTLP endpoint is configured)")
	cmd.Flags().BoolVar(&config.OtelMetricsEnabled, "otel-metrics-enabled", true,
		"Enable OTLP metrics export (when OTLP endpoint is configured)")
	cmd.Flags().Float64Var(&config.OtelSamplingRate, "otel-sampling-rate", 0.1, "OpenTelemetry trace sampling rate (0.0-1.0)")
	cmd.Flags().StringArrayVar(&config.OtelHeaders, "otel-headers", nil,
		"OpenTelemetry OTLP headers in key=value format (e.g., x-honeycomb-team=your-api-key)")
	cmd.Flags().BoolVar(&config.OtelInsecure, "otel-insecure", false,
		"Connect to the OpenTelemetry endpoint using HTTP instead of HTTPS")
	cmd.Flags().BoolVar(&config.OtelEnablePrometheusMetricsPath, "otel-enable-prometheus-metrics-path", false,
		"Enable Prometheus-style /metrics endpoint on the main transport port")
	cmd.Flags().StringArrayVar(&config.OtelEnvironmentVariables, "otel-env-vars", nil,
		"Environment variable names to include in OpenTelemetry spans (comma-separated: ENV1,ENV2)")

	cmd.Flags().BoolVar(&config.IsolateNetwork, "isolate-network", false,
		"Isolate the container network from the host (default: false)")
	cmd.Flags().StringArrayVarP(&config.Labels, "label", "l", []string{}, "Set labels on the container (format: key=value)")
	cmd.Flags().BoolVarP(&config.Foreground, "foreground", "f", false, "Run in foreground mode (block until container exits)")
	cmd.Flags().StringArrayVar(
		&config.ToolsFilter,
		"tools",
		nil,
		"Filter MCP server tools (comma-separated list of tool names)",
	)
	cmd.Flags().StringVar(
		&config.ToolsOverride,
		"tools-override",
		"",
		"Path to a JSON file containing overrides for MCP server tools names and descriptions",
	)
	cmd.Flags().StringVar(&config.FromConfig, "from-config", "", "Load configuration from exported file")

	// Environment file processing flags
	cmd.Flags().StringVar(&config.EnvFile, "env-file", "", "Load environment variables from a single file")
	cmd.Flags().StringVar(&config.EnvFileDir, "env-file-dir", "", "Load environment variables from all files in a directory")

	// Ignore functionality flags
	cmd.Flags().BoolVar(&config.IgnoreGlobally, "ignore-globally", true,
		"Load global ignore patterns from ~/.config/toolhive/thvignore")
	cmd.Flags().BoolVar(&config.PrintOverlays, "print-resolved-overlays", false,
		"Debug: show resolved container paths for tmpfs overlays")
}

// BuildRunnerConfig creates a runner.RunConfig from the configuration
func BuildRunnerConfig(
	ctx context.Context,
	runFlags *RunFlags,
	serverOrImage string,
	cmdArgs []string,
	debugMode bool,
	cmd *cobra.Command,
) (*runner.RunConfig, error) {
	// Validate and setup basic configuration
	validatedHost, err := ValidateAndNormaliseHostFlag(runFlags.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid host: %s", runFlags.Host)
	}

	// Setup OIDC configuration
	oidcConfig, err := setupOIDCConfiguration(cmd, runFlags)
	if err != nil {
		return nil, err
	}

	// Setup telemetry configuration
	telemetryConfig := setupTelemetryConfiguration(cmd, runFlags)

	// Setup runtime and validation
	rt, envVarValidator, err := setupRuntimeAndValidation(ctx)
	if err != nil {
		return nil, err
	}

	if runFlags.RemoteURL != "" {
		return buildRunnerConfig(ctx, runFlags, cmdArgs, debugMode, validatedHost, rt, runFlags.RemoteURL, nil,
			nil, envVarValidator, oidcConfig, telemetryConfig)
	}

	// Handle image retrieval
	imageURL, serverMetadata, err := handleImageRetrieval(ctx, serverOrImage, runFlags)
	if err != nil {
		return nil, err
	}

	// Validate and setup proxy mode
	if err := validateAndSetupProxyMode(runFlags); err != nil {
		return nil, err
	}

	// Parse environment variables
	envVars, err := environment.ParseEnvironmentVariables(runFlags.Env)
	if err != nil {
		return nil, fmt.Errorf("failed to parse environment variables: %v", err)
	}

	// Build the runner config
	return buildRunnerConfig(ctx, runFlags, cmdArgs, debugMode, validatedHost, rt, imageURL, serverMetadata,
		envVars, envVarValidator, oidcConfig, telemetryConfig)
}

// setupOIDCConfiguration sets up OIDC configuration and validates URLs
func setupOIDCConfiguration(cmd *cobra.Command, runFlags *RunFlags) (*auth.TokenValidatorConfig, error) {
	oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID, oidcClientSecret := getOidcFromFlags(cmd)

	if oidcJwksURL != "" {
		if err := networking.ValidateEndpointURL(oidcJwksURL); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", oidcJwksURL, err)
		}
	}
	if oidcIntrospectionURL != "" {
		if err := networking.ValidateEndpointURL(oidcIntrospectionURL); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", oidcIntrospectionURL, err)
		}
	}

	return createOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL,
		oidcClientID, oidcClientSecret, runFlags.ResourceURL, runFlags.JWKSAllowPrivateIP), nil
}

// setupTelemetryConfiguration sets up telemetry configuration with config fallbacks
func setupTelemetryConfiguration(cmd *cobra.Command, runFlags *RunFlags) *telemetry.Config {
	configProvider := cfg.NewDefaultProvider()
	config := configProvider.GetConfig()
	finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables := getTelemetryFromFlags(cmd, config,
		runFlags.OtelEndpoint, runFlags.OtelSamplingRate, runFlags.OtelEnvironmentVariables)

	return createTelemetryConfig(finalOtelEndpoint, runFlags.OtelEnablePrometheusMetricsPath,
		runFlags.OtelServiceName, runFlags.OtelTracingEnabled, runFlags.OtelMetricsEnabled, finalOtelSamplingRate,
		runFlags.OtelHeaders, runFlags.OtelInsecure, finalOtelEnvironmentVariables)
}

// setupRuntimeAndValidation creates container runtime and selects environment variable validator
func setupRuntimeAndValidation(ctx context.Context) (runtime.Deployer, runner.EnvVarValidator, error) {
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create container runtime: %v", err)
	}

	var envVarValidator runner.EnvVarValidator
	if process.IsDetached() || runtime.IsKubernetesRuntime() {
		envVarValidator = &runner.DetachedEnvVarValidator{}
	} else {
		cfgProvider := cfg.NewDefaultProvider()
		envVarValidator = runner.NewCLIEnvVarValidator(cfgProvider)
	}

	return rt, envVarValidator, nil
}

// handleImageRetrieval handles image retrieval and metadata fetching
func handleImageRetrieval(
	ctx context.Context,
	serverOrImage string,
	runFlags *RunFlags,
) (
	string,
	registry.ServerMetadata,
	error,
) {

	// Try to get server from registry (container or remote) or direct URL
	imageURL, serverMetadata, err := retriever.GetMCPServer(
		ctx, serverOrImage, runFlags.CACertPath, runFlags.VerifyImage)
	if err != nil {
		return "", nil, fmt.Errorf("failed to find or create the MCP server %s: %v", serverOrImage, err)
	}

	// Check if we have a remote server
	if serverMetadata != nil && serverMetadata.IsRemote() {
		return imageURL, serverMetadata, nil
	}

	// Only pull image if we are not running in Kubernetes mode.
	// This split will go away if we implement a separate command or binary
	// for running MCP servers in Kubernetes.
	if !runtime.IsKubernetesRuntime() {
		// Take the MCP server we were supplied and either fetch the image, or
		// build it from a protocol scheme. If the server URI refers to an image
		// in our trusted registry, we will also fetch the image metadata.
		if serverMetadata != nil {
			return imageURL, serverMetadata, nil
		}
	}
	return serverOrImage, nil, nil
}

// validateAndSetupProxyMode validates and sets default proxy mode if needed
func validateAndSetupProxyMode(runFlags *RunFlags) error {
	if !types.IsValidProxyMode(runFlags.ProxyMode) {
		if runFlags.ProxyMode == "" {
			runFlags.ProxyMode = types.ProxyModeSSE.String() // default to SSE for backward compatibility
		} else {
			return fmt.Errorf("invalid value for --proxy-mode: %s", runFlags.ProxyMode)
		}
	}
	return nil
}

// buildRunnerConfig creates the final RunnerConfig using the builder pattern
func buildRunnerConfig(
	ctx context.Context,
	runFlags *RunFlags,
	cmdArgs []string,
	debugMode bool,
	validatedHost string,
	rt runtime.Deployer,
	imageURL string,
	serverMetadata registry.ServerMetadata,
	envVars map[string]string,
	envVarValidator runner.EnvVarValidator,
	oidcConfig *auth.TokenValidatorConfig,
	telemetryConfig *telemetry.Config,
) (*runner.RunConfig, error) {
	// Determine transport type
	transportType := defaultTransportType
	if runFlags.Transport != "" {
		transportType = runFlags.Transport
	} else if serverMetadata != nil {
		transportType = serverMetadata.GetTransport()
	}

	// set default options
	opts := []runner.RunConfigBuilderOption{
		runner.WithRuntime(rt),
		runner.WithCmdArgs(cmdArgs),
		runner.WithName(runFlags.Name),
		runner.WithImage(imageURL),
		runner.WithRemoteURL(runFlags.RemoteURL),
		runner.WithHost(validatedHost),
		runner.WithTargetHost(runFlags.TargetHost),
		runner.WithDebug(debugMode),
		runner.WithVolumes(runFlags.Volumes),
		runner.WithSecrets(runFlags.Secrets),
		runner.WithAuthzConfigPath(runFlags.AuthzConfig),
		runner.WithAuditConfigPath(runFlags.AuditConfig),
		runner.WithPermissionProfileNameOrPath(runFlags.PermissionProfile),
		runner.WithNetworkIsolation(runFlags.IsolateNetwork),
		runner.WithK8sPodPatch(runFlags.K8sPodPatch),
		runner.WithProxyMode(types.ProxyMode(runFlags.ProxyMode)),
		runner.WithTransportAndPorts(transportType, runFlags.ProxyPort, runFlags.TargetPort),
		runner.WithAuditEnabled(runFlags.EnableAudit, runFlags.AuditConfig),
		runner.WithLabels(runFlags.Labels),
		runner.WithGroup(runFlags.Group),
		runner.WithIgnoreConfig(&ignore.Config{
			LoadGlobal:    runFlags.IgnoreGlobally,
			PrintOverlays: runFlags.PrintOverlays,
		}),
	}

	var toolsOverride map[string]runner.ToolOverride
	if runFlags.ToolsOverride != "" {
		loadedToolsOverride, err := cli.LoadToolsOverride(runFlags.ToolsOverride)
		if err != nil {
			return nil, fmt.Errorf("failed to load tools override: %v", err)
		}
		toolsOverride = *loadedToolsOverride
	}

	opts = append(opts, runner.WithToolsOverride(toolsOverride))
	// Configure middleware from flags
	opts = append(
		opts,
		runner.WithMiddlewareFromFlags(
			oidcConfig,
			runFlags.ToolsFilter,
			toolsOverride,
			telemetryConfig,
			runFlags.AuthzConfig,
			runFlags.EnableAudit,
			runFlags.AuditConfig,
			runFlags.Name,
			runFlags.Transport,
		),
	)

	if remoteServerMetadata, ok := serverMetadata.(*registry.RemoteServerMetadata); ok {
		if remoteAuthConfig := getRemoteAuthFromRemoteServerMetadata(remoteServerMetadata); remoteAuthConfig != nil {
			opts = append(opts, runner.WithRemoteAuth(remoteAuthConfig), runner.WithRemoteURL(remoteServerMetadata.URL))
		}
	}
	if runFlags.RemoteURL != "" {
		if remoteAuthConfig := getRemoteAuthFromRunFlags(runFlags); remoteAuthConfig != nil {
			opts = append(opts, runner.WithRemoteAuth(remoteAuthConfig))
		}
	}

	// Load authz config if path is provided
	if runFlags.AuthzConfig != "" {
		if authzConfigData, err := authz.LoadConfig(runFlags.AuthzConfig); err == nil {
			opts = append(opts, runner.WithAuthzConfig(authzConfigData))
		}
		// Note: Path is already set via WithAuthzConfigPath above
	}

	// Get OIDC and telemetry values for legacy configuration
	oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID, oidcClientSecret := extractOIDCValues(oidcConfig)
	finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables := extractTelemetryValues(telemetryConfig)

	// Set additional configurations that are still needed in old format for other parts of the system
	opts = append(opts,
		runner.WithOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID, oidcClientSecret,
			runFlags.ThvCABundle, runFlags.JWKSAuthTokenFile, runFlags.ResourceURL, runFlags.JWKSAllowPrivateIP,
		),
		runner.WithTelemetryConfig(finalOtelEndpoint, runFlags.OtelEnablePrometheusMetricsPath,
			runFlags.OtelTracingEnabled, runFlags.OtelMetricsEnabled, runFlags.OtelServiceName,
			finalOtelSamplingRate, runFlags.OtelHeaders, runFlags.OtelInsecure, finalOtelEnvironmentVariables,
		),
		runner.WithToolsFilter(runFlags.ToolsFilter))

	imageMetadata, _ := serverMetadata.(*registry.ImageMetadata)
	// Process environment files
	var err error
	if runFlags.EnvFile != "" {
		opts = append(opts, runner.WithEnvFile(runFlags.EnvFile))
		if err != nil {
			return nil, fmt.Errorf("failed to process env file %s: %v", runFlags.EnvFile, err)
		}
	}
	if runFlags.EnvFileDir != "" {
		opts = append(opts, runner.WithEnvFilesFromDirectory(runFlags.EnvFileDir))
	}

	return runner.NewRunConfigBuilder(ctx, imageMetadata, envVars, envVarValidator, opts...)
}

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
func getRemoteAuthFromRemoteServerMetadata(remoteServerMetadata *registry.RemoteServerMetadata) *runner.RemoteAuthConfig {
	if remoteServerMetadata == nil {
		return nil
	}

	if remoteServerMetadata.OAuthConfig != nil {
		return &runner.RemoteAuthConfig{
			ClientID:     runFlags.RemoteAuthFlags.RemoteAuthClientID,
			ClientSecret: runFlags.RemoteAuthFlags.RemoteAuthClientSecret,
			Scopes:       remoteServerMetadata.OAuthConfig.Scopes,
			SkipBrowser:  runFlags.RemoteAuthFlags.RemoteAuthSkipBrowser,
			Timeout:      runFlags.RemoteAuthFlags.RemoteAuthTimeout,
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
func getRemoteAuthFromRunFlags(runFlags *RunFlags) *runner.RemoteAuthConfig {
	return &runner.RemoteAuthConfig{
		ClientID:     runFlags.RemoteAuthFlags.RemoteAuthClientID,
		ClientSecret: runFlags.RemoteAuthFlags.RemoteAuthClientSecret,
		Scopes:       runFlags.RemoteAuthFlags.RemoteAuthScopes,
		SkipBrowser:  runFlags.RemoteAuthFlags.RemoteAuthSkipBrowser,
		Timeout:      runFlags.RemoteAuthFlags.RemoteAuthTimeout,
		CallbackPort: runFlags.RemoteAuthFlags.RemoteAuthCallbackPort,
		Issuer:       runFlags.RemoteAuthFlags.RemoteAuthIssuer,
		AuthorizeURL: runFlags.RemoteAuthFlags.RemoteAuthAuthorizeURL,
		TokenURL:     runFlags.RemoteAuthFlags.RemoteAuthTokenURL,
		OAuthParams:  runFlags.OAuthParams,
	}
}

// getOidcFromFlags extracts OIDC configuration from command flags
func getOidcFromFlags(cmd *cobra.Command) (string, string, string, string, string, string) {
	oidcIssuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
	oidcAudience := GetStringFlagOrEmpty(cmd, "oidc-audience")
	oidcJwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
	introspectionURL := GetStringFlagOrEmpty(cmd, "oidc-introspection-url")
	oidcClientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")
	oidcClientSecret := GetStringFlagOrEmpty(cmd, "oidc-client-secret")

	return oidcIssuer, oidcAudience, oidcJwksURL, introspectionURL, oidcClientID, oidcClientSecret
}

// getTelemetryFromFlags extracts telemetry configuration from command flags
func getTelemetryFromFlags(cmd *cobra.Command, config *cfg.Config, otelEndpoint string, otelSamplingRate float64,
	otelEnvironmentVariables []string) (string, float64, []string) {
	// Use config values as fallbacks for OTEL flags if not explicitly set
	finalOtelEndpoint := otelEndpoint
	if !cmd.Flags().Changed("otel-endpoint") && config.OTEL.Endpoint != "" {
		finalOtelEndpoint = config.OTEL.Endpoint
	}

	finalOtelSamplingRate := otelSamplingRate
	if !cmd.Flags().Changed("otel-sampling-rate") && config.OTEL.SamplingRate != 0.0 {
		finalOtelSamplingRate = config.OTEL.SamplingRate
	}

	finalOtelEnvironmentVariables := otelEnvironmentVariables
	if !cmd.Flags().Changed("otel-env-vars") && len(config.OTEL.EnvVars) > 0 {
		finalOtelEnvironmentVariables = config.OTEL.EnvVars
	}

	return finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables
}

// createOIDCConfig creates an OIDC configuration if any OIDC parameters are provided
func createOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL,
	oidcClientID, oidcClientSecret, resourceURL string, allowPrivateIP bool) *auth.TokenValidatorConfig {
	if oidcIssuer != "" || oidcAudience != "" || oidcJwksURL != "" || oidcIntrospectionURL != "" ||
		oidcClientID != "" || oidcClientSecret != "" || resourceURL != "" {
		return &auth.TokenValidatorConfig{
			Issuer:           oidcIssuer,
			Audience:         oidcAudience,
			JWKSURL:          oidcJwksURL,
			IntrospectionURL: oidcIntrospectionURL,
			ClientID:         oidcClientID,
			ClientSecret:     oidcClientSecret,
			ResourceURL:      resourceURL,
			AllowPrivateIP:   allowPrivateIP,
		}
	}
	return nil
}

// createTelemetryConfig creates a telemetry configuration if any telemetry parameters are provided
func createTelemetryConfig(otelEndpoint string, otelEnablePrometheusMetricsPath bool,
	otelServiceName string, otelTracingEnabled bool, otelMetricsEnabled bool, otelSamplingRate float64, otelHeaders []string,
	otelInsecure bool, otelEnvironmentVariables []string) *telemetry.Config {
	if otelEndpoint == "" && !otelEnablePrometheusMetricsPath {
		return nil
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

	return &telemetry.Config{
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
}

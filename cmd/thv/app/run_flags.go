// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	authsecrets "github.com/stacklok/toolhive/pkg/auth/secrets"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/cli"
	cfg "github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/process"
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
	InsecureAllowHTTP  bool

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
	OtelCustomAttributes            string   // Custom attributes in key=value format
	OtelUseLegacyAttributes         bool     // Emit legacy attribute names alongside new ones

	// Network isolation
	IsolateNetwork bool

	// Proxy headers
	TrustProxyHeaders bool

	// Endpoint prefix for SSE endpoint URLs
	EndpointPrefix string

	// Network mode
	Network string

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

	// Remote header forwarding
	RemoteForwardHeaders       []string
	RemoteForwardHeadersSecret []string

	// Runtime configuration
	RuntimeImage       string
	RuntimeAddPackages []string
}

// AddRunFlags adds all the run flags to a command
func AddRunFlags(cmd *cobra.Command, config *RunFlags) {
	cmd.Flags().StringVar(&config.Transport, "transport", "", "Transport mode (sse, streamable-http or stdio)")
	cmd.Flags().StringVar(&config.ProxyMode,
		"proxy-mode",
		"streamable-http",
		"Proxy mode for stdio (streamable-http or sse (deprecated, will be removed))")
	cmd.Flags().StringVar(&config.Name, "name", "", "Name of the MCP server (default to auto-generated from image)")
	cmd.Flags().StringVar(&config.Group, "group", "default", "Name of the group this workload should belong to")
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
		"Permission profile to use (none, network, or path to JSON file) (default is to use the permission profile from "+
			"the registry or \"network\" if not part of the registry)",
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
	cmd.Flags().BoolVar(&config.EnableAudit, "enable-audit", false, "Enable audit logging with default configuration "+
		"(default false)")
	cmd.Flags().StringVar(&config.K8sPodPatch, "k8s-pod-patch", "",
		"JSON string to patch the Kubernetes pod template (only applicable when using Kubernetes runtime)")
	cmd.Flags().StringVar(&config.CACertPath, "ca-cert", "", "Path to a custom CA certificate file to use for container builds")
	cmd.Flags().StringVar(&config.RuntimeImage, "runtime-image", "",
		"Override the default base image for protocol schemes (e.g., golang:1.24-alpine, node:20-alpine, python:3.11-slim)")
	cmd.Flags().StringArrayVar(&config.RuntimeAddPackages, "runtime-add-package", []string{},
		"Add additional packages to install in the builder stage (can be repeated)")
	cmd.Flags().StringVar(&config.VerifyImage, "image-verification", retriever.VerifyImageWarn,
		fmt.Sprintf("Set image verification mode (%s, %s, %s)",
			retriever.VerifyImageWarn, retriever.VerifyImageEnabled, retriever.VerifyImageDisabled))
	cmd.Flags().StringVar(&config.ThvCABundle, "thv-ca-bundle", "",
		"Path to CA certificate bundle for ToolHive HTTP operations (JWKS, OIDC discovery, etc.)")
	cmd.Flags().StringVar(&config.JWKSAuthTokenFile, "jwks-auth-token-file", "",
		"Path to file containing bearer token for authenticating JWKS/OIDC requests")
	cmd.Flags().BoolVar(&config.JWKSAllowPrivateIP, "jwks-allow-private-ip", false,
		"Allow JWKS/OIDC endpoints on private IP addresses (use with caution) (default false)")
	cmd.Flags().BoolVar(&config.InsecureAllowHTTP, "oidc-insecure-allow-http", false,
		"Allow HTTP (non-HTTPS) OIDC issuers for local development/testing (WARNING: Insecure!) (default false)")

	// Remote authentication flags
	AddRemoteAuthFlags(cmd, &config.RemoteAuthFlags)

	// Remote header forwarding flags
	// Using StringArrayVar (not StringSliceVar) to avoid comma-splitting in header values
	cmd.Flags().StringArrayVar(&config.RemoteForwardHeaders, "remote-forward-headers", []string{},
		"Headers to inject into requests to remote MCP server (format: Name=Value, can be repeated)")
	cmd.Flags().StringArrayVar(&config.RemoteForwardHeadersSecret, "remote-forward-headers-secret", []string{},
		"Headers with secret values from ToolHive secrets manager (format: Name=secret-name, can be repeated)")

	// OAuth discovery configuration
	cmd.Flags().StringVar(&config.ResourceURL, "resource-url", "",
		"Explicit resource URL for OAuth discovery endpoint (RFC 9728)")

	// OpenTelemetry flags updated per origin/main
	cmd.Flags().StringVar(&config.OtelEndpoint, "otel-endpoint", "",
		"OpenTelemetry OTLP endpoint URL (e.g., https://api.honeycomb.io)")
	cmd.Flags().StringVar(&config.OtelServiceName, "otel-service-name", "",
		"OpenTelemetry service name (defaults to thv-<workload-name>)")
	cmd.Flags().BoolVar(&config.OtelTracingEnabled, "otel-tracing-enabled", true,
		"Enable distributed tracing (when OTLP endpoint is configured)")
	cmd.Flags().BoolVar(&config.OtelMetricsEnabled, "otel-metrics-enabled", true,
		"Enable OTLP metrics export (when OTLP endpoint is configured)")
	cmd.Flags().Float64Var(&config.OtelSamplingRate, "otel-sampling-rate", 0.1, "OpenTelemetry trace sampling rate (0.0-1.0)")
	cmd.Flags().StringArrayVar(&config.OtelHeaders, "otel-headers", nil,
		"OpenTelemetry OTLP headers in key=value format (e.g., x-honeycomb-team=your-api-key)")
	cmd.Flags().BoolVar(&config.OtelInsecure, "otel-insecure", false,
		"Connect to the OpenTelemetry endpoint using HTTP instead of HTTPS (default false)")
	cmd.Flags().BoolVar(&config.OtelEnablePrometheusMetricsPath, "otel-enable-prometheus-metrics-path", false,
		"Enable Prometheus-style /metrics endpoint on the main transport port (default false)")
	cmd.Flags().StringArrayVar(&config.OtelEnvironmentVariables, "otel-env-vars", nil,
		"Environment variable names to include in OpenTelemetry spans (comma-separated: ENV1,ENV2)")
	cmd.Flags().StringVar(&config.OtelCustomAttributes, "otel-custom-attributes", "",
		"Custom resource attributes for OpenTelemetry in key=value format (e.g., server_type=prod,region=us-east-1,team=platform)")
	cmd.Flags().BoolVar(&config.OtelUseLegacyAttributes, "otel-use-legacy-attributes", true,
		"Emit legacy attribute names alongside new OTEL semantic convention names (default true)")

	cmd.Flags().BoolVar(&config.IsolateNetwork, "isolate-network", false,
		"Isolate the container network from the host (default false)")
	cmd.Flags().BoolVar(&config.TrustProxyHeaders, "trust-proxy-headers", false,
		"Trust X-Forwarded-* headers from reverse proxies (X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-Port, X-Forwarded-Prefix) "+
			"(default false)")
	cmd.Flags().StringVar(&config.EndpointPrefix, "endpoint-prefix", "",
		"Path prefix to prepend to SSE endpoint URLs (e.g., /playwright)")
	cmd.Flags().StringVar(&config.Network, "network", "",
		"Connect the container to a network (e.g., 'host' for host networking)")
	cmd.Flags().StringArrayVarP(&config.Labels, "label", "l", []string{}, "Set labels on the container (format: key=value)")
	cmd.Flags().BoolVarP(&config.Foreground, "foreground", "f", false, "Run in foreground mode (block until container exits) "+
		"(default false)")
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
		"Debug: show resolved container paths for tmpfs overlays (default false)")
}

// BuildRunnerConfig creates a runner.RunConfig from the configuration
func BuildRunnerConfig(
	ctx context.Context,
	runFlags *RunFlags,
	serverOrImage string,
	cmdArgs []string,
	debugMode bool,
	cmd *cobra.Command,
	groupName string,
) (*runner.RunConfig, error) {
	// Validate and setup basic configuration
	validatedHost, err := ValidateAndNormaliseHostFlag(runFlags.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid host: %s", runFlags.Host)
	}

	// Validate endpoint prefix
	if runFlags.EndpointPrefix != "" && !strings.HasPrefix(runFlags.EndpointPrefix, "/") {
		return nil, fmt.Errorf("endpoint-prefix must start with '/' when provided, got: %s", runFlags.EndpointPrefix)
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
		slog.Debug(fmt.Sprintf("Attempting to run remote MCP server: %s", runFlags.RemoteURL))
		return buildRunnerConfig(ctx, runFlags, cmdArgs, debugMode, validatedHost, rt, runFlags.RemoteURL, nil,
			nil, envVarValidator, oidcConfig, telemetryConfig)
	}

	// Handle image retrieval
	imageURL, serverMetadata, err := handleImageRetrieval(ctx, serverOrImage, runFlags, groupName)
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
		return nil, fmt.Errorf("failed to parse environment variables: %w", err)
	}

	// Build the runner config
	return buildRunnerConfig(ctx, runFlags, cmdArgs, debugMode, validatedHost, rt, imageURL, serverMetadata,
		envVars, envVarValidator, oidcConfig, telemetryConfig)
}

// setupOIDCConfiguration sets up OIDC configuration and validates URLs
func setupOIDCConfiguration(cmd *cobra.Command, runFlags *RunFlags) (*auth.TokenValidatorConfig, error) {
	oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID, oidcClientSecret, oidcScopes := getOidcFromFlags(cmd)

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
		oidcClientID, oidcClientSecret, runFlags.ResourceURL, runFlags.JWKSAllowPrivateIP, oidcScopes), nil
}

// setupTelemetryConfiguration sets up telemetry configuration with config fallbacks
func setupTelemetryConfiguration(cmd *cobra.Command, runFlags *RunFlags) *telemetry.Config {
	configProvider := cfg.NewDefaultProvider()
	config := configProvider.GetConfig()
	finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables, finalOtelInsecure,
		finalOtelEnablePrometheusMetricsPath, finalOtelUseLegacyAttributes := getTelemetryFromFlags(
		cmd, config, runFlags.OtelEndpoint,
		runFlags.OtelSamplingRate, runFlags.OtelEnvironmentVariables, runFlags.OtelInsecure,
		runFlags.OtelEnablePrometheusMetricsPath, runFlags.OtelUseLegacyAttributes)

	return createTelemetryConfig(finalOtelEndpoint, finalOtelEnablePrometheusMetricsPath,
		runFlags.OtelServiceName, runFlags.OtelTracingEnabled, runFlags.OtelMetricsEnabled, finalOtelSamplingRate,
		runFlags.OtelHeaders, finalOtelInsecure, finalOtelEnvironmentVariables, runFlags.OtelCustomAttributes,
		finalOtelUseLegacyAttributes)
}

// setupRuntimeAndValidation creates container runtime and selects environment variable validator
func setupRuntimeAndValidation(ctx context.Context) (runtime.Deployer, runner.EnvVarValidator, error) {
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create container runtime: %w", err)
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
	groupName string,
) (
	string,
	regtypes.ServerMetadata,
	error,
) {

	// Build runtime config override from flags (if any)
	var runtimeOverride *templates.RuntimeConfig
	if runFlags.RuntimeImage != "" || len(runFlags.RuntimeAddPackages) > 0 {
		runtimeOverride = &templates.RuntimeConfig{
			BuilderImage:       runFlags.RuntimeImage,
			AdditionalPackages: runFlags.RuntimeAddPackages,
		}
	}

	// Try to get server from registry (container or remote) or direct URL
	imageURL, serverMetadata, err := retriever.GetMCPServer(
		ctx, serverOrImage, runFlags.CACertPath, runFlags.VerifyImage, groupName, runtimeOverride)
	if err != nil {
		return "", nil, fmt.Errorf("failed to find or create the MCP server %s: %w", serverOrImage, err)
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
			runFlags.ProxyMode = types.ProxyModeStreamableHTTP.String() // default to streamable-http (SSE is deprecated)
		} else {
			return fmt.Errorf("invalid value for --proxy-mode: %s", runFlags.ProxyMode)
		}
	}
	return nil
}

// resolveTransportType selects the appropriate transport type based on flags and metadata.
// Uses a type assertion with nil check to guard against typed nil pointers wrapped
// in a non-nil interface (e.g., nil *ImageMetadata returned as ServerMetadata).
func resolveTransportType(runFlags *RunFlags, serverMetadata regtypes.ServerMetadata) string {
	if runFlags.Transport != "" {
		return runFlags.Transport
	}
	if imageMetadata, ok := serverMetadata.(*regtypes.ImageMetadata); ok && imageMetadata != nil {
		if t := imageMetadata.GetTransport(); t != "" {
			return t
		}
	}
	return defaultTransportType
}

// resolveServerName resolves the server name for telemetry from flags or metadata
func resolveServerName(runFlags *RunFlags, serverMetadata regtypes.ServerMetadata) string {
	if runFlags.Name != "" {
		return runFlags.Name
	}
	if imageMetadata, ok := serverMetadata.(*regtypes.ImageMetadata); ok && imageMetadata != nil {
		return imageMetadata.Name
	}
	return ""
}

// loadToolsOverrideConfig loads and parses the tools override configuration file
func loadToolsOverrideConfig(toolsOverridePath string) (map[string]runner.ToolOverride, error) {
	if toolsOverridePath == "" {
		return nil, nil
	}
	loadedToolsOverride, err := cli.LoadToolsOverride(toolsOverridePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load tools override: %w", err)
	}
	return *loadedToolsOverride, nil
}

// configureRemoteHeaderOptions configures header forwarding options for remote servers
func configureRemoteHeaderOptions(runFlags *RunFlags) ([]runner.RunConfigBuilderOption, error) {
	var opts []runner.RunConfigBuilderOption

	if runFlags.RemoteURL == "" {
		return opts, nil
	}

	addHeaders, err := parseHeaderForwardFlags(runFlags.RemoteForwardHeaders)
	if err != nil {
		return nil, fmt.Errorf("failed to parse header forward flags: %w", err)
	}
	if len(addHeaders) > 0 {
		opts = append(opts, runner.WithHeaderForward(addHeaders))
	}

	if len(runFlags.RemoteForwardHeadersSecret) > 0 {
		secretHeaders, err := parseHeaderSecretFlags(runFlags.RemoteForwardHeadersSecret)
		if err != nil {
			return nil, fmt.Errorf("failed to parse header secret flags: %w", err)
		}
		if len(secretHeaders) > 0 {
			opts = append(opts, runner.WithHeaderForwardSecrets(secretHeaders))
		}
	}

	return opts, nil
}

// configureRuntimeOptions configures runtime image and package options
func configureRuntimeOptions(runFlags *RunFlags) []runner.RunConfigBuilderOption {
	if runFlags.RuntimeImage == "" && len(runFlags.RuntimeAddPackages) == 0 {
		return nil
	}

	runtimeConfig := &templates.RuntimeConfig{
		BuilderImage:       runFlags.RuntimeImage,
		AdditionalPackages: runFlags.RuntimeAddPackages,
	}
	return []runner.RunConfigBuilderOption{runner.WithRuntimeConfig(runtimeConfig)}
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
	serverMetadata regtypes.ServerMetadata,
	envVars map[string]string,
	envVarValidator runner.EnvVarValidator,
	oidcConfig *auth.TokenValidatorConfig,
	telemetryConfig *telemetry.Config,
) (*runner.RunConfig, error) {
	transportType := resolveTransportType(runFlags, serverMetadata)
	serverName := resolveServerName(runFlags, serverMetadata)

	// Use type assertion with nil check to guard against typed nil pointers
	// wrapped in a non-nil interface (e.g., protocol scheme images).
	var imageMetadata *regtypes.ImageMetadata
	if md, ok := serverMetadata.(*regtypes.ImageMetadata); ok && md != nil {
		imageMetadata = md
	}

	// Build default options
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
		runner.WithTrustProxyHeaders(runFlags.TrustProxyHeaders),
		runner.WithEndpointPrefix(runFlags.EndpointPrefix),
		runner.WithNetworkMode(runFlags.Network),
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

	// Load tools override configuration
	toolsOverride, err := loadToolsOverrideConfig(runFlags.ToolsOverride)
	if err != nil {
		return nil, err
	}

	// Configure remote header forwarding options
	remoteHeaderOpts, err := configureRemoteHeaderOptions(runFlags)
	if err != nil {
		return nil, err
	}
	opts = append(opts, remoteHeaderOpts...)

	// Configure runtime options
	runtimeOpts := configureRuntimeOptions(runFlags)
	opts = append(opts, runtimeOpts...)

	// Configure middleware and additional options
	additionalOpts, err := configureMiddlewareAndOptions(runFlags, serverMetadata, toolsOverride, oidcConfig,
		telemetryConfig, serverName, transportType)
	if err != nil {
		return nil, err
	}
	opts = append(opts, additionalOpts...)

	return runner.NewRunConfigBuilder(ctx, imageMetadata, envVars, envVarValidator, opts...)
}

// configureMiddlewareAndOptions configures middleware and additional runner options
func configureMiddlewareAndOptions(
	runFlags *RunFlags,
	serverMetadata regtypes.ServerMetadata,
	toolsOverride map[string]runner.ToolOverride,
	oidcConfig *auth.TokenValidatorConfig,
	telemetryConfig *telemetry.Config,
	serverName string,
	transportType string,
) ([]runner.RunConfigBuilderOption, error) {
	var opts []runner.RunConfigBuilderOption

	// Load application config for global settings
	configProvider := cfg.NewDefaultProvider()
	appConfig := configProvider.GetConfig()

	// Resolve the OTel service name from the workload name when not explicitly set
	telemetry.ResolveServiceName(telemetryConfig, serverName)

	// Configure middleware from flags
	tokenExchangeConfig, err := runFlags.RemoteAuthFlags.BuildTokenExchangeConfig()
	if err != nil {
		return nil, fmt.Errorf("invalid token exchange configuration: %w", err)
	}

	// Use computed serverName and transportType for correct telemetry labels
	opts = append(opts, runner.WithToolsOverride(toolsOverride))
	opts = append(
		opts,
		runner.WithMiddlewareFromFlags(
			oidcConfig,
			tokenExchangeConfig,
			runFlags.ToolsFilter,
			toolsOverride,
			telemetryConfig,
			runFlags.AuthzConfig,
			runFlags.EnableAudit,
			runFlags.AuditConfig,
			serverName,
			transportType,
			appConfig.DisableUsageMetrics,
		),
	)

	// Configure remote authentication if applicable
	remoteAuthOpts, err := configureRemoteAuth(runFlags, serverMetadata)
	if err != nil {
		return nil, err
	}
	opts = append(opts, remoteAuthOpts...)

	// Load authz config if path is provided
	if runFlags.AuthzConfig != "" {
		if authzConfigData, err := authz.LoadConfig(runFlags.AuthzConfig); err == nil {
			opts = append(opts, runner.WithAuthzConfig(authzConfigData))
		}
		// Note: Path is already set via WithAuthzConfigPath above
	}

	// Get OIDC and telemetry values for legacy configuration
	oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID, oidcClientSecret,
		oidcScopes := extractOIDCValues(oidcConfig)
	finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables := extractTelemetryValues(telemetryConfig)

	// Set additional configurations that are still needed in old format for other parts of the system
	opts = append(opts,
		runner.WithOIDCConfig(
			oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID, oidcClientSecret,
			runFlags.ThvCABundle, runFlags.JWKSAuthTokenFile, runFlags.ResourceURL,
			runFlags.JWKSAllowPrivateIP, runFlags.InsecureAllowHTTP, oidcScopes,
		),
		runner.WithTelemetryConfigFromFlags(finalOtelEndpoint, runFlags.OtelEnablePrometheusMetricsPath,
			runFlags.OtelTracingEnabled, runFlags.OtelMetricsEnabled, runFlags.OtelServiceName,
			finalOtelSamplingRate, runFlags.OtelHeaders, runFlags.OtelInsecure, finalOtelEnvironmentVariables,
			runFlags.OtelUseLegacyAttributes,
		),
		runner.WithToolsFilter(runFlags.ToolsFilter))

	// Process environment files
	if runFlags.EnvFile != "" {
		opts = append(opts, runner.WithEnvFile(runFlags.EnvFile))
	}
	if runFlags.EnvFileDir != "" {
		opts = append(opts, runner.WithEnvFilesFromDirectory(runFlags.EnvFileDir))
	}

	return opts, nil
}

// configureRemoteAuth configures remote authentication options if applicable
func configureRemoteAuth(runFlags *RunFlags, serverMetadata regtypes.ServerMetadata) ([]runner.RunConfigBuilderOption, error) {
	var opts []runner.RunConfigBuilderOption

	if remoteServerMetadata, ok := serverMetadata.(*regtypes.RemoteServerMetadata); ok && remoteServerMetadata != nil {
		remoteAuthConfig, err := getRemoteAuthFromRemoteServerMetadata(remoteServerMetadata, runFlags)
		if err != nil {
			return nil, err
		}

		// Validate OAuth callback port availability upfront for better user experience
		if err := networking.ValidateCallbackPort(remoteAuthConfig.CallbackPort, remoteAuthConfig.ClientID); err != nil {
			return nil, err
		}

		opts = append(opts, runner.WithRemoteAuth(remoteAuthConfig), runner.WithRemoteURL(remoteServerMetadata.URL))
	}

	if runFlags.RemoteURL != "" {
		remoteAuthConfig, err := getRemoteAuthFromRunFlags(runFlags)
		if err != nil {
			return nil, err
		}

		// Validate OAuth callback port availability upfront for better user experience
		if err := networking.ValidateCallbackPort(remoteAuthConfig.CallbackPort, remoteAuthConfig.ClientID); err != nil {
			return nil, err
		}

		opts = append(opts, runner.WithRemoteAuth(remoteAuthConfig))
	}

	return opts, nil
}

// extractOIDCValues extracts OIDC values from the OIDC config for legacy configuration
func extractOIDCValues(
	config *auth.TokenValidatorConfig,
) (string, string, string, string, string, string, []string) {
	if config == nil {
		return "", "", "", "", "", "", nil
	}
	return config.Issuer, config.Audience, config.JWKSURL, config.IntrospectionURL,
		config.ClientID, config.ClientSecret, config.Scopes
}

// extractTelemetryValues extracts telemetry values from the telemetry config for legacy configuration
func extractTelemetryValues(config *telemetry.Config) (string, float64, []string) {
	if config == nil {
		return "", 0.0, nil
	}
	return config.Endpoint, config.GetSamplingRateFloat(), config.EnvironmentVariables
}

// getRemoteAuthFromRemoteServerMetadata creates RemoteAuthConfig from RemoteServerMetadata,
// giving CLI flags priority. For OAuthParams: if CLI provides any, they REPLACE metadata entirely.
func getRemoteAuthFromRemoteServerMetadata(
	remoteServerMetadata *regtypes.RemoteServerMetadata,
	runFlags *RunFlags,
) (*remote.Config, error) {
	if remoteServerMetadata == nil || remoteServerMetadata.OAuthConfig == nil {
		return getRemoteAuthFromRunFlags(runFlags)
	}

	oc := remoteServerMetadata.OAuthConfig
	f := runFlags.RemoteAuthFlags

	firstNonEmpty := func(a, b string) string {
		if a != "" {
			return a
		}
		return b
	}

	// Resolve OAuth client secret from multiple sources (flag, file, environment variable)
	// This follows the same priority as resolveSecret: flag → file → environment variable
	resolvedClientSecret, err := resolveSecret(
		f.RemoteAuthClientSecret,
		f.RemoteAuthClientSecretFile,
		"", // No specific environment variable for OAuth client secret
	)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve OAuth client secret: %w", err)
	}

	// Process the resolved client secret (convert plain text to secret reference if needed)
	clientSecret, err := authsecrets.ProcessSecret(runFlags.Name, resolvedClientSecret, authsecrets.TokenTypeOAuthClientSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to process OAuth client secret: %w", err)
	}

	authCfg := &remote.Config{
		ClientID:     f.RemoteAuthClientID,
		ClientSecret: clientSecret,
		SkipBrowser:  f.RemoteAuthSkipBrowser,
		Timeout:      f.RemoteAuthTimeout,
		Headers:      remoteServerMetadata.Headers,
		EnvVars:      remoteServerMetadata.EnvVars,
	}

	// Scopes: CLI overrides if provided
	if len(f.RemoteAuthScopes) > 0 {
		authCfg.Scopes = f.RemoteAuthScopes
	} else {
		authCfg.Scopes = oc.Scopes
	}

	// Heuristic: treat default runner.DefaultCallbackPort as "unset"
	if f.RemoteAuthCallbackPort > 0 && f.RemoteAuthCallbackPort != runner.DefaultCallbackPort {
		authCfg.CallbackPort = f.RemoteAuthCallbackPort
	} else if oc.CallbackPort > 0 {
		authCfg.CallbackPort = oc.CallbackPort
	} else {
		authCfg.CallbackPort = runner.DefaultCallbackPort
	}

	// Issuer / URLs / Resource: CLI non-empty wins
	authCfg.Issuer = firstNonEmpty(f.RemoteAuthIssuer, oc.Issuer)
	authCfg.AuthorizeURL = firstNonEmpty(f.RemoteAuthAuthorizeURL, oc.AuthorizeURL)
	authCfg.TokenURL = firstNonEmpty(f.RemoteAuthTokenURL, oc.TokenURL)

	resourceIndicator := firstNonEmpty(f.RemoteAuthResource, oc.Resource)
	if resourceIndicator != "" {
		authCfg.Resource = resourceIndicator
	} else {
		authCfg.Resource = remote.DefaultResourceIndicator(remoteServerMetadata.URL)
	}

	// OAuthParams: REPLACE metadata when CLI provides any key/value.
	if len(runFlags.OAuthParams) > 0 {
		authCfg.OAuthParams = runFlags.OAuthParams
	} else {
		authCfg.OAuthParams = oc.OAuthParams
	}

	// Resolve bearer token from multiple sources (flag, file, environment variable)
	resolvedBearerToken, err := resolveSecret(
		f.RemoteAuthBearerToken,
		f.RemoteAuthBearerTokenFile,
		remote.BearerTokenEnvVarName, // Hardcoded environment variable
	)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve bearer token: %w", err)
	}
	authCfg.BearerToken = resolvedBearerToken
	authCfg.BearerTokenFile = f.RemoteAuthBearerTokenFile

	return authCfg, nil
}

// getRemoteAuthFromRunFlags creates RemoteAuthConfig from RunFlags
func getRemoteAuthFromRunFlags(runFlags *RunFlags) (*remote.Config, error) {
	// Resolve OAuth client secret from multiple sources (flag, file, environment variable)
	// This follows the same priority as resolveSecret: flag → file → environment variable
	resolvedClientSecret, err := resolveSecret(
		runFlags.RemoteAuthFlags.RemoteAuthClientSecret,
		runFlags.RemoteAuthFlags.RemoteAuthClientSecretFile,
		"", // No specific environment variable for OAuth client secret
	)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve OAuth client secret: %w", err)
	}

	// Process the resolved client secret (convert plain text to secret reference if needed)
	clientSecret, err := authsecrets.ProcessSecret(runFlags.Name, resolvedClientSecret, authsecrets.TokenTypeOAuthClientSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to process OAuth client secret: %w", err)
	}

	// Resolve bearer token from multiple sources (flag, file, environment variable)
	// This follows the same priority as resolveSecret: flag → file → environment variable
	resolvedBearerToken, err := resolveSecret(
		runFlags.RemoteAuthFlags.RemoteAuthBearerToken,
		runFlags.RemoteAuthFlags.RemoteAuthBearerTokenFile,
		remote.BearerTokenEnvVarName, // Hardcoded environment variable
	)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve bearer token: %w", err)
	}

	// Process the resolved bearer token (convert plain text to secret reference if needed)
	bearerToken, err := authsecrets.ProcessSecret(runFlags.Name, resolvedBearerToken, authsecrets.TokenTypeBearerToken)
	if err != nil {
		return nil, fmt.Errorf("failed to process bearer token: %w", err)
	}

	// Derive the resource parameter (RFC 8707)
	resource := runFlags.RemoteAuthFlags.RemoteAuthResource
	if resource == "" && runFlags.ResourceURL != "" {
		resource = remote.DefaultResourceIndicator(runFlags.RemoteURL)
	}

	return &remote.Config{
		ClientID:        runFlags.RemoteAuthFlags.RemoteAuthClientID,
		ClientSecret:    clientSecret,
		Scopes:          runFlags.RemoteAuthFlags.RemoteAuthScopes,
		SkipBrowser:     runFlags.RemoteAuthFlags.RemoteAuthSkipBrowser,
		Timeout:         runFlags.RemoteAuthFlags.RemoteAuthTimeout,
		CallbackPort:    runFlags.RemoteAuthFlags.RemoteAuthCallbackPort,
		Issuer:          runFlags.RemoteAuthFlags.RemoteAuthIssuer,
		AuthorizeURL:    runFlags.RemoteAuthFlags.RemoteAuthAuthorizeURL,
		TokenURL:        runFlags.RemoteAuthFlags.RemoteAuthTokenURL,
		Resource:        resource,
		OAuthParams:     runFlags.OAuthParams,
		BearerToken:     bearerToken,
		BearerTokenFile: runFlags.RemoteAuthFlags.RemoteAuthBearerTokenFile,
	}, nil
}

// getOidcFromFlags extracts OIDC configuration from command flags
func getOidcFromFlags(cmd *cobra.Command) (string, string, string, string, string, string, []string) {
	oidcIssuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
	oidcAudience := GetStringFlagOrEmpty(cmd, "oidc-audience")
	oidcJwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
	introspectionURL := GetStringFlagOrEmpty(cmd, "oidc-introspection-url")
	oidcClientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")
	oidcClientSecret := GetStringFlagOrEmpty(cmd, "oidc-client-secret")
	oidcScopes, _ := cmd.Flags().GetStringSlice("oidc-scopes")

	return oidcIssuer, oidcAudience, oidcJwksURL, introspectionURL, oidcClientID, oidcClientSecret, oidcScopes
}

// getTelemetryFromFlags extracts telemetry configuration from command flags
func getTelemetryFromFlags(cmd *cobra.Command, config *cfg.Config, otelEndpoint string, otelSamplingRate float64,
	otelEnvironmentVariables []string, otelInsecure bool, otelEnablePrometheusMetricsPath bool,
	otelUseLegacyAttributes bool) (
	string, float64, []string, bool, bool, bool) {
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

	finalOtelInsecure := otelInsecure
	if !cmd.Flags().Changed("otel-insecure") {
		finalOtelInsecure = config.OTEL.Insecure
	}

	finalOtelEnablePrometheusMetricsPath := otelEnablePrometheusMetricsPath
	if !cmd.Flags().Changed("otel-enable-prometheus-metrics-path") {
		finalOtelEnablePrometheusMetricsPath = config.OTEL.EnablePrometheusMetricsPath
	}

	// UseLegacyAttributes defaults to true for this release to avoid breaking existing
	// dashboards and alerts. When the config file explicitly sets this field (non-nil),
	// use the config value. Otherwise, use the CLI flag value (which defaults to true).
	// This default will change to false in a future release.
	finalOtelUseLegacyAttributes := otelUseLegacyAttributes
	if !cmd.Flags().Changed("otel-use-legacy-attributes") && config.OTEL.UseLegacyAttributes != nil {
		finalOtelUseLegacyAttributes = *config.OTEL.UseLegacyAttributes
	}

	return finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables,
		finalOtelInsecure, finalOtelEnablePrometheusMetricsPath, finalOtelUseLegacyAttributes
}

// createOIDCConfig creates an OIDC configuration if any OIDC parameters are provided
func createOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL,
	oidcClientID, oidcClientSecret, resourceURL string, allowPrivateIP bool, scopes []string) *auth.TokenValidatorConfig {
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
			Scopes:           scopes,
		}
	}
	return nil
}

// createTelemetryConfig creates a telemetry configuration if any telemetry parameters are provided
func createTelemetryConfig(otelEndpoint string, otelEnablePrometheusMetricsPath bool,
	otelServiceName string, otelTracingEnabled bool, otelMetricsEnabled bool, otelSamplingRate float64, otelHeaders []string,
	otelInsecure bool, otelEnvironmentVariables []string, otelCustomAttributes string,
	otelUseLegacyAttributes bool) *telemetry.Config {
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

	// Parse custom attributes
	customAttrs, err := telemetry.ParseCustomAttributes(otelCustomAttributes)
	if err != nil {
		// Log the error but don't fail - telemetry is optional
		slog.Warn(fmt.Sprintf("Failed to parse custom attributes: %v", err))
		customAttrs = nil
	}

	telemetryCfg := &telemetry.Config{
		Endpoint:                    otelEndpoint,
		ServiceName:                 otelServiceName,
		ServiceVersion:              "", // resolved at runtime in NewProvider()
		TracingEnabled:              otelTracingEnabled,
		MetricsEnabled:              otelMetricsEnabled,
		Headers:                     headers,
		Insecure:                    otelInsecure,
		EnablePrometheusMetricsPath: otelEnablePrometheusMetricsPath,
		EnvironmentVariables:        processedEnvVars,
		CustomAttributes:            customAttrs,
		UseLegacyAttributes:         otelUseLegacyAttributes,
	}
	telemetryCfg.SetSamplingRateFromFloat(otelSamplingRate)
	return telemetryCfg
}

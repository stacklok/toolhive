package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	cfg "github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
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

	// Configuration import
	FromConfig string

	// Ignore functionality
	IgnoreGlobally bool
	PrintOverlays  bool
}

// AddRunFlags adds all the run flags to a command
func AddRunFlags(cmd *cobra.Command, config *RunFlags) {
	cmd.Flags().StringVar(&config.Transport, "transport", "", "Transport mode (sse, streamable-http or stdio)")
	cmd.Flags().StringVar(&config.ProxyMode, "proxy-mode", "sse", "Proxy mode for stdio transport (sse or streamable-http)")
	cmd.Flags().StringVar(&config.Name, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	// TODO: Re-enable when group functionality is complete
	// cmd.Flags().StringVar(&config.Group, "group", "default",
	//	"Name of the group this workload belongs to (defaults to 'default' if not specified)")
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
	cmd.Flags().StringVar(&config.RemoteURL, "remote", "", "URL of remote MCP server to run as a workload")
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

	// OAuth discovery configuration
	cmd.Flags().StringVar(&config.ResourceURL, "resource-url", "",
		"Explicit resource URL for OAuth discovery endpoint (RFC 9728)")

	// OpenTelemetry flags updated per origin/main
	cmd.Flags().StringVar(&config.OtelEndpoint, "otel-endpoint", "",
		"OpenTelemetry OTLP endpoint URL (e.g., https://api.honeycomb.io)")
	cmd.Flags().StringVar(&config.OtelServiceName, "otel-service-name", "",
		"OpenTelemetry service name (defaults to toolhive-mcp-proxy)")
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
	cmd.Flags().StringVar(&config.FromConfig, "from-config", "", "Load configuration from exported file")

	// Ignore functionality flags
	cmd.Flags().BoolVar(&config.IgnoreGlobally, "ignore-globally", true,
		"Load global ignore patterns from ~/.config/toolhive/thvignore")
	cmd.Flags().BoolVar(&config.PrintOverlays, "print-resolved-overlays", false,
		"Debug: show resolved container paths for tmpfs overlays")
}

// BuildRunnerConfig creates a runner.RunConfig from the configuration
func BuildRunnerConfig(
	ctx context.Context,
	runConfig *RunFlags,
	serverOrImage string,
	cmdArgs []string,
	debugMode bool,
	cmd *cobra.Command,
) (*runner.RunConfig, error) {
	// Validate the host flag
	validatedHost, err := ValidateAndNormaliseHostFlag(runConfig.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid host: %s", runConfig.Host)
	}

	// Get OIDC flags
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

	// Get OTEL flag values with config fallbacks
	config := cfg.GetConfig()
	finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables := getTelemetryFromFlags(cmd, config,
		runConfig.OtelEndpoint, runConfig.OtelSamplingRate, runConfig.OtelEnvironmentVariables)

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Select an env var validation strategy depending on how the CLI is run:
	// If we have called the CLI directly, we use the CLIEnvVarValidator.
	// If we are running in detached mode, or the CLI is wrapped by the K8s operator,
	// we use the DetachedEnvVarValidator.
	var envVarValidator runner.EnvVarValidator
	if process.IsDetached() || runtime.IsKubernetesRuntime() {
		envVarValidator = &runner.DetachedEnvVarValidator{}
	} else {
		envVarValidator = &runner.CLIEnvVarValidator{}
	}

	// Handle remote MCP server
	var imageMetadata *registry.ImageMetadata
	imageURL := serverOrImage
	transportType := runConfig.Transport

	// If --remote flag is provided, use SSE transport and set the remote URL as the image
	if runConfig.RemoteURL != "" {
		transportType = "sse" // Remote servers use SSE transport
		imageURL = runConfig.RemoteURL
		// For remote servers, we don't need to pull image metadata
		imageMetadata = nil
	} else {
		// Only pull image if we are not running in Kubernetes mode.
		// In Kubernetes mode, the image is pulled by the container runtime.
		if !runtime.IsKubernetesRuntime() {
			// Take the MCP server we were supplied and either fetch the image, or
			// build it from a protocol scheme. If the server URI refers to an image
			// in our trusted registry, we will also fetch the image metadata.
			imageURL, imageMetadata, err = retriever.GetMCPServer(ctx, serverOrImage, runConfig.CACertPath, runConfig.VerifyImage)
			if err != nil {
				return nil, fmt.Errorf("failed to find or create the MCP server %s: %v", serverOrImage, err)
			}
		}
	}

	// Validate proxy mode early
	if !types.IsValidProxyMode(runConfig.ProxyMode) {
		if runConfig.ProxyMode == "" {
			runConfig.ProxyMode = types.ProxyModeSSE.String() // default to SSE for backward compatibility
		} else {
			return nil, fmt.Errorf("invalid value for --proxy-mode: %s", runConfig.ProxyMode)
		}
	}

	// Initialize a new RunConfig with values from command-line flags
	return runner.NewRunConfigBuilder().
		WithRuntime(rt).
		WithCmdArgs(cmdArgs).
		WithName(runConfig.Name).
		WithImage(imageURL).
		WithRemoteURL(runConfig.RemoteURL).
		WithHost(validatedHost).
		WithTargetHost(runConfig.TargetHost).
		WithDebug(debugMode).
		WithVolumes(runConfig.Volumes).
		WithSecrets(runConfig.Secrets).
		WithAuthzConfigPath(runConfig.AuthzConfig).
		WithAuditConfigPath(runConfig.AuditConfig).
		WithPermissionProfileNameOrPath(runConfig.PermissionProfile).
		WithNetworkIsolation(runConfig.IsolateNetwork).
		WithK8sPodPatch(runConfig.K8sPodPatch).
		WithProxyMode(types.ProxyMode(runConfig.ProxyMode)).
		WithTransportAndPorts(transportType, runConfig.ProxyPort, runConfig.TargetPort).
		WithAuditEnabled(runConfig.EnableAudit, runConfig.AuditConfig).
		WithLabels(runConfig.Labels).
		WithGroup(runConfig.Group).
		WithOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID, oidcClientSecret,
			runConfig.ThvCABundle, runConfig.JWKSAuthTokenFile, runConfig.ResourceURL, runConfig.JWKSAllowPrivateIP).
		WithTelemetryConfig(finalOtelEndpoint, runConfig.OtelEnablePrometheusMetricsPath, runConfig.OtelServiceName,
			finalOtelSamplingRate, runConfig.OtelHeaders, runConfig.OtelInsecure, finalOtelEnvironmentVariables).
		WithToolsFilter(runConfig.ToolsFilter).
		WithIgnoreConfig(&ignore.Config{
			LoadGlobal:    runConfig.IgnoreGlobally,
			PrintOverlays: runConfig.PrintOverlays,
		}).
		Build(ctx, imageMetadata, runConfig.Env, envVarValidator)
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

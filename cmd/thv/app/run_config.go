package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	cfg "github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// RunConfig holds the configuration for running MCP servers
type RunConfig struct {
	// Transport and proxy settings
	Transport  string
	ProxyMode  string
	Host       string
	ProxyPort  int
	Port       int
	TargetPort int
	TargetHost string

	// Server configuration
	Name              string
	Group             string
	PermissionProfile string
	Env               []string
	Volumes           []string
	Secrets           []string

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

	// Telemetry configuration
	OtelEndpoint                    string
	OtelServiceName                 string
	OtelSamplingRate                float64
	OtelHeaders                     []string
	OtelInsecure                    bool
	OtelEnablePrometheusMetricsPath bool
	OtelEnvironmentVariables        []string

	// Network isolation
	IsolateNetwork bool

	// Labels
	Labels []string
}

// AddRunFlags adds all the run flags to a command
func AddRunFlags(cmd *cobra.Command, config *RunConfig) {
	cmd.Flags().StringVar(&config.Transport, "transport", "", "Transport mode (sse, streamable-http or stdio)")
	cmd.Flags().StringVar(&config.ProxyMode, "proxy-mode", "sse", "Proxy mode for stdio transport (sse or streamable-http)")
	cmd.Flags().StringVar(&config.Name, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	cmd.Flags().StringVar(&config.Group, "group", "", "Name of the group this workload belongs to")
	cmd.Flags().StringVar(&config.Host, "host", transport.LocalhostIPv4, "Host for the HTTP proxy to listen on (IP or hostname)")
	cmd.Flags().IntVar(&config.ProxyPort, "proxy-port", 0, "Port for the HTTP proxy to listen on (host port)")
	cmd.Flags().IntVar(&config.Port, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	if err := cmd.Flags().MarkDeprecated("port", "use --proxy-port instead"); err != nil {
		logger.Warnf("Error marking port flag as deprecated: %v", err)
	}
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
	cmd.Flags().StringVar(
		&config.AuthzConfig,
		"authz-config",
		"",
		"Path to the authorization configuration file",
	)
	cmd.Flags().StringVar(
		&config.AuditConfig,
		"audit-config",
		"",
		"Path to the audit configuration file",
	)
	cmd.Flags().BoolVar(
		&config.EnableAudit,
		"enable-audit",
		false,
		"Enable audit logging with default configuration",
	)
	cmd.Flags().StringVar(
		&config.K8sPodPatch,
		"k8s-pod-patch",
		"",
		"JSON string to patch the Kubernetes pod template (only applicable when using Kubernetes runtime)",
	)
	cmd.Flags().StringVar(
		&config.CACertPath,
		"ca-cert",
		"",
		"Path to a custom CA certificate file to use for container builds",
	)
	cmd.Flags().StringVar(
		&config.VerifyImage,
		"image-verification",
		retriever.VerifyImageWarn,
		fmt.Sprintf(
			"Set image verification mode (%s, %s, %s)",
			retriever.VerifyImageWarn,
			retriever.VerifyImageEnabled,
			retriever.VerifyImageDisabled,
		),
	)
	cmd.Flags().StringVar(
		&config.ThvCABundle,
		"thv-ca-bundle",
		"",
		"Path to CA certificate bundle for ToolHive HTTP operations (JWKS, OIDC discovery, etc.)",
	)
	cmd.Flags().StringVar(
		&config.JWKSAuthTokenFile,
		"jwks-auth-token-file",
		"",
		"Path to file containing bearer token for authenticating JWKS/OIDC requests",
	)
	cmd.Flags().BoolVar(
		&config.JWKSAllowPrivateIP,
		"jwks-allow-private-ip",
		false,
		"Allow JWKS/OIDC endpoints on private IP addresses",
	)
	cmd.Flags().StringVar(
		&config.OtelEndpoint,
		"otel-endpoint",
		"",
		"OpenTelemetry endpoint for metrics and traces",
	)
	cmd.Flags().StringVar(
		&config.OtelServiceName,
		"otel-service-name",
		"",
		"OpenTelemetry service name",
	)
	cmd.Flags().Float64Var(
		&config.OtelSamplingRate,
		"otel-sampling-rate",
		0.1,
		"OpenTelemetry sampling rate (0.0 to 1.0)",
	)
	cmd.Flags().StringArrayVar(
		&config.OtelHeaders,
		"otel-headers",
		[]string{},
		"OpenTelemetry headers (format: KEY=VALUE)",
	)
	cmd.Flags().BoolVar(
		&config.OtelInsecure,
		"otel-insecure",
		false,
		"Use insecure connection for OpenTelemetry",
	)
	cmd.Flags().BoolVar(
		&config.OtelEnablePrometheusMetricsPath,
		"otel-enable-prometheus-metrics-path",
		false,
		"Enable Prometheus metrics endpoint",
	)
	cmd.Flags().StringArrayVar(
		&config.OtelEnvironmentVariables,
		"otel-env",
		[]string{},
		"OpenTelemetry environment variables (format: KEY=VALUE)",
	)
	cmd.Flags().BoolVar(
		&config.IsolateNetwork,
		"isolate-network",
		false,
		"Isolate the network for the container",
	)
	cmd.Flags().StringArrayVar(
		&config.Labels,
		"label",
		[]string{},
		"Add labels to the container (format: KEY=VALUE)",
	)
}

// BuildRunnerConfig creates a runner.RunConfig from the configuration
func BuildRunnerConfig(
	ctx context.Context,
	runConfig *RunConfig,
	serverOrImage string,
	cmdArgs []string,
	debugMode bool,
) (*runner.RunConfig, error) {

	// Validate the host flag and default resolving to IP in case hostname is provided
	validatedHost, err := ValidateAndNormaliseHostFlag(runConfig.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid host: %s", runConfig.Host)
	}

	// Get OIDC flag values
	// We'll get these from config later
	oidcIssuer, oidcAudience, oidcJwksURL, oidcClientID, oidcAllowOpaqueTokens, err := getOidcFromFlags(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get OIDC flags: %v", err)
	}

	// Get OTEL flag values with config fallbacks
	config := cfg.GetConfig()
	finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables := getTelemetryFromFlags(nil, config,
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

	var imageMetadata *registry.ImageMetadata
	imageURL := serverOrImage

	// Only pull image if we are not running in Kubernetes mode.
	// This split will go away if we implement a separate command or binary
	// for running MCP servers in Kubernetes.
	if !runtime.IsKubernetesRuntime() {
		// Take the MCP server we were supplied and either fetch the image, or
		// build it from a protocol scheme. If the server URI refers to an image
		// in our trusted registry, we will also fetch the image metadata.
		imageURL, imageMetadata, err = retriever.GetMCPServer(ctx, serverOrImage, runConfig.CACertPath, runConfig.VerifyImage)
		if err != nil {
			return nil, fmt.Errorf("failed to find or create the MCP server %s: %v", serverOrImage, err)
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

	// Determine effective port value - prefer --proxy-port over --port for backwards compatibility
	effectivePort := runConfig.ProxyPort
	if effectivePort == 0 && runConfig.Port != 0 {
		effectivePort = runConfig.Port
	}

	// Initialize a new RunConfig with values from command-line flags
	runnerConfig, err := runner.NewRunConfigFromFlags(
		ctx,
		rt,
		cmdArgs,
		runConfig.Name,
		imageURL,
		imageMetadata,
		validatedHost,
		debugMode,
		runConfig.Volumes,
		runConfig.Secrets,
		runConfig.AuthzConfig,
		runConfig.AuditConfig,
		runConfig.EnableAudit,
		runConfig.PermissionProfile,
		runConfig.TargetHost,
		runConfig.Transport,
		effectivePort,
		runConfig.TargetPort,
		runConfig.Env,
		runConfig.Labels,
		oidcIssuer,
		oidcAudience,
		oidcJwksURL,
		oidcClientID,
		oidcAllowOpaqueTokens,
		finalOtelEndpoint,
		runConfig.OtelServiceName,
		finalOtelSamplingRate,
		runConfig.OtelHeaders,
		runConfig.OtelInsecure,
		runConfig.OtelEnablePrometheusMetricsPath,
		finalOtelEnvironmentVariables,
		runConfig.IsolateNetwork,
		runConfig.K8sPodPatch,
		runConfig.ThvCABundle,
		runConfig.JWKSAuthTokenFile,
		runConfig.JWKSAllowPrivateIP,
		envVarValidator,
		types.ProxyMode(runConfig.ProxyMode),
		runConfig.Group,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create RunConfig: %v", err)
	}

	return runnerConfig, nil
}

// getOidcFromFlags extracts OIDC configuration from command flags
func getOidcFromFlags(_ *cobra.Command) (string, string, string, string, bool, error) {
	// For now, return empty values since we're not using OIDC in the shared config
	// This can be enhanced later if needed
	return "", "", "", "", false, nil
}

// getTelemetryFromFlags extracts telemetry configuration from command flags
func getTelemetryFromFlags(cmd *cobra.Command, config *cfg.Config, otelEndpoint string, otelSamplingRate float64,
	otelEnvironmentVariables []string) (string, float64, []string) {
	// Use config values as fallbacks for OTEL flags if not explicitly set
	finalOtelEndpoint := otelEndpoint
	if cmd != nil && !cmd.Flags().Changed("otel-endpoint") && config.OTEL.Endpoint != "" {
		finalOtelEndpoint = config.OTEL.Endpoint
	}

	finalOtelSamplingRate := otelSamplingRate
	if cmd != nil && !cmd.Flags().Changed("otel-sampling-rate") && config.OTEL.SamplingRate != 0.0 {
		finalOtelSamplingRate = config.OTEL.SamplingRate
	}

	finalOtelEnvironmentVariables := otelEnvironmentVariables
	if cmd != nil && !cmd.Flags().Changed("otel-env-vars") && len(config.OTEL.EnvVars) > 0 {
		finalOtelEnvironmentVariables = config.OTEL.EnvVars
	}

	return finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables
}

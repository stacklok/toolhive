package app

import (
	"fmt"
	"net"
	"os"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var runCmd *cobra.Command
var runFlags proxyRunFlags

// NewRunCmd creates a new run command for testing
func NewRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run [flags] SERVER_OR_IMAGE_OR_PROTOCOL [-- ARGS...]",
		Short: "Run an MCP server",
		Long: `Run an MCP server with the specified name, image, or protocol scheme.

	ToolHive supports three ways to run an MCP server:

	1. From the registry:
	   $ thv run server-name [-- args...]
	   Looks up the server in the registry and uses its predefined settings
	   (transport, permissions, environment variables, etc.)

	2. From a container image:
	   $ thv run ghcr.io/example/mcp-server:latest [-- args...]
	   Runs the specified container image directly with the provided arguments

	The container will be started with the specified transport mode and
	permission profile. Additional configuration can be provided via flags.`,
		Args: cobra.MinimumNArgs(1),
		RunE: runCmdFunc,
		// Ignore unknown flags to allow passing flags to the MCP server
		FParseErrWhitelist: cobra.FParseErrWhitelist{
			UnknownFlags: true,
		},
	}
}

type proxyRunFlags struct {
	runTransport          string
	runName               string
	runHost               string
	runProxyPort          int
	runTargetPort         int
	runPermissionProfile  string
	runProxyMode          string
	runEnv                []string
	runVolumes            []string
	runSecrets            []string
	runAuthzConfig        string
	runAuditConfig        string
	runEnableAudit        bool
	runK8sPodPatch        string
	runThvCABundle        string
	runJWKSAuthTokenFile  string
	runJWKSAllowPrivateIP bool

	oidcIssuer           string
	oidcAudience         string
	oidcJwksURL          string
	oidcIntrospectionURL string
	oidcClientID         string
	oidcClientSecret     string

	// OpenTelemetry flags
	runOtelEnabled              bool
	runOtelEndpoint             string
	runOtelServiceName          string
	runOtelHeaders              []string
	runOtelTracingEnabled       bool
	runOtelMetricsEnabled       bool
	runOtelInsecure             bool
	runOtelTracingSamplingRate  float64
	enablePrometheusMetricsPath bool

	// Network isolation flag
	runIsolateNetwork bool

	// Tools filter
	runToolsFilter []string

	// OAuth discovery resource URL
	runResourceURL string

	// Environment file processing
	runEnvFileDir string
}

func addRunFlags(runCmd *cobra.Command, runFlags *proxyRunFlags) {
	runCmd.Flags().StringVar(&runFlags.runTransport, "transport", "", "Transport mode (sse, streamable-http or stdio)")
	runCmd.Flags().StringVar(&runFlags.runProxyMode, "proxy-mode", "sse", "Proxy mode for stdio transport (sse or streamable-http)")
	runCmd.Flags().StringVar(&runFlags.runName, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	runCmd.Flags().IntVar(&runFlags.runProxyPort, "proxy-port", 0, "Port for the HTTP proxy to listen on (host port)")
	runCmd.Flags().StringVar(
		&runFlags.runHost,
		"host",
		transport.LocalhostIPv4,
		"Host for the HTTP proxy to listen on (IP or hostname)",
	)
	runCmd.Flags().IntVar(&runFlags.runTargetPort, "target-port", 0,
		"Port for the container to expose (only applicable to SSE or Streamable HTTP transport)")
	runCmd.Flags().StringVar(
		&runFlags.runPermissionProfile,
		"permission-profile",
		"",
		"Permission profile to use (none, network, or path to JSON file)",
	)
	runCmd.Flags().StringArrayVarP(
		&runFlags.runEnv,
		"env",
		"e",
		[]string{},
		"Environment variables to pass to the MCP server (format: KEY=VALUE)",
	)
	runCmd.Flags().StringVar(
		&runFlags.runK8sPodPatch,
		"k8s-pod-patch",
		"",
		"JSON string to patch the Kubernetes pod template (only applicable when using Kubernetes runtime)",
	)
	// This is used for the K8s operator which wraps the run command, but shouldn't be visible to users.
	if err := runCmd.Flags().MarkHidden("k8s-pod-patch"); err != nil {
		logger.Warnf("Error hiding flag: %v", err)
	}
	runCmd.Flags().StringVar(
		&runFlags.runThvCABundle,
		"thv-ca-bundle",
		"",
		"Path to CA certificate bundle for ToolHive HTTP operations (JWKS, OIDC discovery, etc.)",
	)
	runCmd.Flags().StringVar(
		&runFlags.runJWKSAuthTokenFile,
		"jwks-auth-token-file",
		"",
		"Path to file containing bearer token for authenticating JWKS/OIDC requests",
	)
	runCmd.Flags().BoolVar(
		&runFlags.runJWKSAllowPrivateIP,
		"jwks-allow-private-ip",
		false,
		"Allow JWKS/OIDC endpoints on private IP addresses (use with caution)",
	)
	runCmd.Flags().StringVar(&runFlags.oidcIssuer, "oidc-issuer", "", "OIDC issuer URL (e.g., https://accounts.google.com)")
	runCmd.Flags().StringVar(&runFlags.oidcAudience, "oidc-audience", "", "Expected audience for the token")
	runCmd.Flags().StringVar(&runFlags.oidcJwksURL, "oidc-jwks-url", "", "URL to fetch the JWKS from")
	runCmd.Flags().StringVar(&runFlags.oidcClientID, "oidc-client-id", "", "OIDC client ID")
	runCmd.Flags().StringVar(
		&runFlags.oidcClientSecret,
		"oidc-client-secret",
		"",
		"OIDC client secret (optional, for introspection)",
	)
	runCmd.Flags().StringVar(&runFlags.oidcIntrospectionURL, "oidc-introspection-url", "", "OIDC token introspection URL")

	// the below aren't used or set via the operator, so we need to see if lower level packages use their defaults
	runCmd.Flags().StringArrayVarP(
		&runFlags.runVolumes,
		"volume",
		"v",
		[]string{},
		"Mount a volume into the container (format: host-path:container-path[:ro])",
	)
	runCmd.Flags().StringArrayVar(
		&runFlags.runSecrets,
		"secret",
		[]string{},
		"Specify a secret to be fetched from the secrets manager and set as an environment variable (format: NAME,target=TARGET)",
	)
	runCmd.Flags().StringVar(
		&runFlags.runAuthzConfig,
		"authz-config",
		"",
		"Path to the authorization configuration file",
	)
	runCmd.Flags().StringVar(
		&runFlags.runAuditConfig,
		"audit-config",
		"",
		"Path to the audit configuration file",
	)
	runCmd.Flags().BoolVar(
		&runFlags.runEnableAudit,
		"enable-audit",
		false,
		"Enable audit logging with default configuration",
	)

	// Add OpenTelemetry flags
	runCmd.Flags().BoolVar(&runFlags.runOtelEnabled, "otel-enabled", false,
		"Enable OpenTelemetry")
	runCmd.Flags().StringVar(&runFlags.runOtelEndpoint, "otel-endpoint", "",
		"OpenTelemetry endpoint URL (defaults to http://localhost:4318)")
	runCmd.Flags().StringVar(&runFlags.runOtelServiceName, "otel-service-name", "",
		"OpenTelemetry service name (defaults to toolhive-mcp-proxy)")
	runCmd.Flags().StringArrayVar(&runFlags.runOtelHeaders, "otel-headers", nil,
		"OpenTelemetry OTLP headers in key=value format (e.g., x-honeycomb-team=your-api-key)")
	runCmd.Flags().BoolVar(&runFlags.runOtelInsecure, "otel-insecure", false,
		"Connect to the OpenTelemetry endpoint using HTTP instead of HTTPS")
	runCmd.Flags().BoolVar(&runFlags.runOtelTracingEnabled, "otel-tracing-enabled", false,
		"Enable distributed tracing (when OTLP endpoint is configured)")
	runCmd.Flags().BoolVar(&runFlags.runOtelMetricsEnabled, "otel-metrics-enabled", false,
		"Enable OTLP metrics export (when OTLP endpoint is configured)")
	runCmd.Flags().Float64Var(&runFlags.runOtelTracingSamplingRate, "otel-tracing-sampling-rate", 0.0,
		"OpenTelemetry trace sampling rate (0.0-1.0)")
	runCmd.Flags().BoolVar(&runFlags.enablePrometheusMetricsPath, "enable-prometheus-metrics-path", false,
		"Enable Prometheus-style /metrics endpoint on the main transport port")
	runCmd.Flags().BoolVar(&runFlags.runIsolateNetwork, "isolate-network", false,
		"Isolate the container network from the host (default: false)")
	runCmd.Flags().StringArrayVar(
		&runFlags.runToolsFilter,
		"tools",
		nil,
		"Filter MCP server tools (comma-separated list of tool names)",
	)
	runCmd.Flags().StringVar(
		&runFlags.runResourceURL,
		"resource-url",
		"",
		"Explicit resource URL for OAuth discovery endpoint (RFC 9728)",
	)
	runCmd.Flags().StringVar(
		&runFlags.runEnvFileDir,
		"env-file-dir",
		"",
		"Load environment variables from all files in a directory",
	)
}

func init() {
	runCmd = NewRunCmd()
	addRunFlags(runCmd, &runFlags)
}

func runCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Always try to load runconfig.json from filesystem first
	fileBasedConfig, err := tryLoadConfigFromFile()
	if err != nil {
		logger.Debugf("No configuration file found or failed to load: %v", err)
		// Continue without configuration file - will use flags instead
	} else if fileBasedConfig != nil {
		logger.Infof("Auto-discovered and loaded configuration from runconfig.json file")
	}

	// Validate the host flag and default resolving to IP in case hostname is provided
	validatedHost, err := ValidateAndNormaliseHostFlag(runFlags.runHost)
	if err != nil {
		return fmt.Errorf("invalid host: %s", runFlags.runHost)
	}
	runFlags.runHost = validatedHost

	// Validate and setup proxy mode
	if !types.IsValidProxyMode(runFlags.runProxyMode) {
		return fmt.Errorf("invalid value for --proxy-mode: %s (valid values: sse, streamable-http)", runFlags.runProxyMode)
	}

	// Get the name of the MCP server to run.
	// This may be a server name from the registry, a container image, or a protocol scheme.
	mcpServerImage := args[0]

	// Process command arguments using os.Args to find everything after --
	cmdArgs := parseCommandArguments(os.Args)

	// Print the processed command arguments for debugging
	logger.Debugf("Processed cmdArgs: %v", cmdArgs)

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Select an env var validation strategy depending on how the CLI is run:
	// If we have called the CLI directly, we use the CLIEnvVarValidator.
	// If we are running in detached mode, or the CLI is wrapped by the K8s operator,
	// we use the DetachedEnvVarValidator.
	envVarValidator := &runner.DetachedEnvVarValidator{}

	var imageMetadata *registry.ImageMetadata

	// Parse environment variables from slice to map only if env flag was set
	var envVarsMap map[string]string
	if cmd.Flags().Changed("env") {
		var err error
		envVarsMap, err = environment.ParseEnvironmentVariables(runFlags.runEnv)
		if err != nil {
			return fmt.Errorf("failed to parse environment variables: %v", err)
		}
	} else {
		// Use empty map if no env flags were set
		envVarsMap = make(map[string]string)
	}

	// Start with basic options
	opts := []runner.RunConfigBuilderOption{
		runner.WithRuntime(rt),
		runner.WithDebug(debugMode),
	}

	// Apply file-based configuration first (if available)
	if fileBasedConfig != nil {
		opts = applyRunConfigToBuilder(opts, fileBasedConfig)
	}

	// Apply CLI flag overrides
	opts = applyBasicFlagOverrides(cmd, &runFlags, validatedHost, opts)

	// Always apply essential runtime options (these are not configuration file fields)
	opts = append(opts,
		runner.WithCmdArgs(cmdArgs),
		runner.WithImage(mcpServerImage),
		runner.WithTargetHost(transport.LocalhostIPv4),
	)

	// Apply advanced flag overrides
	opts = applyAdvancedFlagOverrides(cmd, &runFlags, runFlags.runOtelHeaders, opts)

	runConfig, err := runner.NewRunConfigBuilder(ctx, imageMetadata, envVarsMap, envVarValidator, opts...)
	if err != nil {
		return fmt.Errorf("failed to create RunConfig: %v", err)
	}

	workloadManager, err := workloads.NewManagerFromRuntime(rt)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}
	return workloadManager.RunWorkload(ctx, runConfig)
}

// parseCommandArguments processes command-line arguments to find everything after the -- separator
// which are the arguments to be passed to the MCP server
func parseCommandArguments(args []string) []string {
	var cmdArgs []string
	for i, arg := range args {
		if arg == "--" && i < len(args)-1 {
			// Found the separator, take everything after it
			cmdArgs = args[i+1:]
			break
		}
	}
	return cmdArgs
}

// ValidateAndNormaliseHostFlag validates and normalizes the host flag resolving it to an IP address if hostname is provided
func ValidateAndNormaliseHostFlag(host string) (string, error) {
	// Check if the host is a valid IP address
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.To4() == nil {
			return "", fmt.Errorf("IPv6 addresses are not supported: %s", host)
		}
		return host, nil
	}

	// If not an IP address, resolve the hostname to an IP address
	addrs, err := net.LookupHost(host)
	if err != nil {
		return "", fmt.Errorf("invalid host: %s", host)
	}

	// Use the first IPv4 address found
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && ip.To4() != nil {
			return ip.String(), nil
		}
	}

	return "", fmt.Errorf("could not resolve host: %s", host)
}

// Standard configuration file paths for runconfig.json
// These paths match the volume mount paths used by the Kubernetes operator
const (
	kubernetesRunConfigPath = "/etc/runconfig/runconfig.json" // Primary path for K8s ConfigMap volume mounts
	systemRunConfigPath     = "/etc/toolhive/runconfig.json"  // System-wide configuration path
	localRunConfigPath      = "./runconfig.json"              // Local directory fallback
)

// tryLoadConfigFromFile attempts to load runconfig.json from standard file locations
func tryLoadConfigFromFile() (*runner.RunConfig, error) {
	// Standard locations where runconfig.json might be mounted or placed
	configPaths := []string{
		kubernetesRunConfigPath,
		systemRunConfigPath,
		localRunConfigPath,
	}

	for _, path := range configPaths {
		if _, err := os.Stat(path); err != nil {
			continue // File doesn't exist, try next location
		}

		logger.Debugf("Found configuration file at %s", path)

		// Security: Only read from predefined safe paths to avoid path traversal
		file, err := os.Open(path) // #nosec G304 - path is from predefined safe list
		if err != nil {
			return nil, fmt.Errorf("found config file at %s but failed to open: %w", path, err)
		}
		defer file.Close()

		// Use existing runner.ReadJSON function for consistency
		runConfig, err := runner.ReadJSON(file)
		if err != nil {
			return nil, fmt.Errorf("found config file at %s but failed to parse JSON: %w", path, err)
		}

		logger.Infof("Successfully loaded configuration from %s", path)
		return runConfig, nil
	}

	// No configuration file found
	return nil, nil
}

// applyRunConfigToBuilder applies a RunConfig to the builder (works for both ConfigMap and flags)
func applyRunConfigToBuilder(
	opts []runner.RunConfigBuilderOption,
	config *runner.RunConfig,
) []runner.RunConfigBuilderOption {
	opts = append(
		opts,
		runner.WithImage(config.Image),
		runner.WithName(config.Name),
		runner.WithCmdArgs(config.CmdArgs),
		runner.WithHost(config.Host),
		runner.WithTargetHost(config.TargetHost),
		runner.WithVolumes(config.Volumes),
		runner.WithSecrets(config.Secrets),
		runner.WithAuthzConfigPath(config.AuthzConfigPath),
		runner.WithAuditConfigPath(config.AuditConfigPath),
		runner.WithAuditEnabled(config.AuditConfig != nil, config.AuditConfigPath),
		runner.WithPermissionProfileNameOrPath(config.PermissionProfileNameOrPath),
		runner.WithNetworkIsolation(config.IsolateNetwork),
		runner.WithK8sPodPatch(config.K8sPodTemplatePatch),
		runner.WithProxyMode(config.ProxyMode),
		runner.WithGroup(config.Group),
		runner.WithToolsFilter(config.ToolsFilter),
		runner.WithEnvVars(config.EnvVars),
		runner.WithTransportAndPorts(string(config.Transport), config.Port, config.TargetPort),
	)

	// Process environment files if EnvFileDir is specified
	if config.EnvFileDir != "" {
		opts = append(opts, runner.WithEnvFilesFromDirectory(config.EnvFileDir))
	}

	// Apply complex configs if they exist
	if config.AuthzConfig != nil {
		opts = append(opts, runner.WithAuthzConfig(config.AuthzConfig))
	}
	// Note: AuditConfig is handled via WithAuditEnabled in builder based on the flag and path
	if config.PermissionProfile != nil {
		opts = append(opts, runner.WithPermissionProfile(config.PermissionProfile))
	}
	if config.ToolsOverride != nil {
		opts = append(opts, runner.WithToolsOverride(config.ToolsOverride))
	}
	if config.OIDCConfig != nil {
		opts = append(opts,
			runner.WithOIDCConfig(
				config.OIDCConfig.Issuer,
				config.OIDCConfig.Audience,
				config.OIDCConfig.JWKSURL,
				"", // IntrospectionURL not available in TokenValidatorConfig
				config.OIDCConfig.ClientID,
				config.OIDCConfig.ClientSecret,
				config.OIDCConfig.CACertPath,
				config.OIDCConfig.AuthTokenFile,
				"",
				config.OIDCConfig.AllowPrivateIP,
			),
		) // ResourceURL not available
	}
	if config.TelemetryConfig != nil {
		// Convert headers from map[string]string to []string
		var headersSlice []string
		for k, v := range config.TelemetryConfig.Headers {
			headersSlice = append(headersSlice, k+"="+v)
		}

		opts = append(opts,
			runner.WithTelemetryConfig(
				config.TelemetryConfig.Endpoint,
				config.TelemetryConfig.EnablePrometheusMetricsPath,
				config.TelemetryConfig.TracingEnabled,
				config.TelemetryConfig.MetricsEnabled,
				config.TelemetryConfig.ServiceName,
				config.TelemetryConfig.SamplingRate,
				headersSlice,
				config.TelemetryConfig.Insecure,
				config.TelemetryConfig.EnvironmentVariables,
			),
		)
	}

	return opts
}

// applyBasicFlagOverrides applies basic CLI flag overrides when explicitly set by user
func applyBasicFlagOverrides(
	cmd *cobra.Command,
	runFlags *proxyRunFlags,
	validatedHost string,
	opts []runner.RunConfigBuilderOption,
) []runner.RunConfigBuilderOption {
	if cmd.Flags().Changed("transport") {
		opts = append(opts, runner.WithTransport(runFlags.runTransport))
	}
	if cmd.Flags().Changed("proxy-port") {
		opts = append(opts, runner.WithPort(runFlags.runProxyPort))
	}
	if cmd.Flags().Changed("target-port") {
		opts = append(opts, runner.WithTargetPort(runFlags.runTargetPort))
	}
	if cmd.Flags().Changed("name") {
		opts = append(opts, runner.WithName(runFlags.runName))
	}
	if cmd.Flags().Changed("proxy-mode") {
		opts = append(opts, runner.WithProxyMode(types.ProxyMode(runFlags.runProxyMode)))
	}
	if cmd.Flags().Changed("host") {
		opts = append(opts, runner.WithHost(validatedHost))
	}
	return opts
}

// applyAdvancedFlagOverrides applies advanced CLI flag overrides when explicitly set by user
func applyAdvancedFlagOverrides(
	cmd *cobra.Command,
	runFlags *proxyRunFlags,
	otelHeaders []string,
	opts []runner.RunConfigBuilderOption,
) []runner.RunConfigBuilderOption {
	// Apply simple flags
	if cmd.Flags().Changed("volume") {
		opts = append(opts, runner.WithVolumes(runFlags.runVolumes))
	}
	if cmd.Flags().Changed("secret") {
		opts = append(opts, runner.WithSecrets(runFlags.runSecrets))
	}
	if cmd.Flags().Changed("authz-config") {
		opts = append(opts, runner.WithAuthzConfigPath(runFlags.runAuthzConfig))
	}
	if cmd.Flags().Changed("audit-config") {
		opts = append(opts, runner.WithAuditConfigPath(runFlags.runAuditConfig))
	}
	if cmd.Flags().Changed("permission-profile") {
		opts = append(opts, runner.WithPermissionProfileNameOrPath(runFlags.runPermissionProfile))
	}
	if cmd.Flags().Changed("isolate-network") {
		opts = append(opts, runner.WithNetworkIsolation(runFlags.runIsolateNetwork))
	}
	if cmd.Flags().Changed("enable-audit") || cmd.Flags().Changed("audit-config") {
		opts = append(opts, runner.WithAuditEnabled(runFlags.runEnableAudit, runFlags.runAuditConfig))
	}
	if cmd.Flags().Changed("tools") {
		opts = append(opts, runner.WithToolsFilter(runFlags.runToolsFilter))
	}
	if cmd.Flags().Changed("env-file-dir") {
		opts = append(opts, runner.WithEnvFilesFromDirectory(runFlags.runEnvFileDir))
	}

	// Apply complex config groups
	opts = applyOIDCConfigIfChanged(cmd, runFlags, opts)
	opts = applyTelemetryConfigIfChanged(cmd, runFlags, otelHeaders, opts)

	// Add optional flags that are always checked (these are runtime-specific)
	if runFlags.runK8sPodPatch != "" {
		opts = append(opts, runner.WithK8sPodPatch(runFlags.runK8sPodPatch))
	}

	return opts
}

// applyOIDCConfigIfChanged applies OIDC configuration only if any OIDC flag was set
func applyOIDCConfigIfChanged(
	cmd *cobra.Command,
	runFlags *proxyRunFlags,
	opts []runner.RunConfigBuilderOption,
) []runner.RunConfigBuilderOption {
	if cmd.Flags().Changed("oidc-issuer") || cmd.Flags().Changed("oidc-audience") ||
		cmd.Flags().Changed("oidc-jwks-url") || cmd.Flags().Changed("oidc-introspection-url") ||
		cmd.Flags().Changed("oidc-client-id") || cmd.Flags().Changed("oidc-client-secret") ||
		cmd.Flags().Changed("thv-ca-bundle") || cmd.Flags().Changed("jwks-auth-token-file") ||
		cmd.Flags().Changed("resource-url") || cmd.Flags().Changed("jwks-allow-private-ip") {
		opts = append(opts, runner.WithOIDCConfig(
			runFlags.oidcIssuer,
			runFlags.oidcAudience,
			runFlags.oidcJwksURL,
			runFlags.oidcIntrospectionURL,
			runFlags.oidcClientID,
			runFlags.oidcClientSecret,
			runFlags.runThvCABundle,
			runFlags.runJWKSAuthTokenFile,
			runFlags.runResourceURL,
			runFlags.runJWKSAllowPrivateIP,
		))
	}
	return opts
}

// applyTelemetryConfigIfChanged applies telemetry configuration only if any telemetry flag was set
func applyTelemetryConfigIfChanged(
	cmd *cobra.Command,
	runFlags *proxyRunFlags,
	otelHeaders []string,
	opts []runner.RunConfigBuilderOption,
) []runner.RunConfigBuilderOption {
	if cmd.Flags().Changed("otel-endpoint") || cmd.Flags().Changed("enable-prometheus-metrics-path") ||
		cmd.Flags().Changed("otel-tracing-enabled") || cmd.Flags().Changed("otel-metrics-enabled") ||
		cmd.Flags().Changed("otel-service-name") || cmd.Flags().Changed("otel-tracing-sampling-rate") {

		// Use empty environment variables for now (this was originally finalOtelEnvironmentVariables)
		finalOtelEnvironmentVariables := []string{}

		opts = append(opts, runner.WithTelemetryConfig(
			runFlags.runOtelEndpoint,
			runFlags.enablePrometheusMetricsPath,
			runFlags.runOtelTracingEnabled,
			runFlags.runOtelMetricsEnabled,
			runFlags.runOtelServiceName,
			runFlags.runOtelTracingSamplingRate,
			otelHeaders,
			runFlags.runOtelInsecure,
			finalOtelEnvironmentVariables,
		))
	}
	return opts
}

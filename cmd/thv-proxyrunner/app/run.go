package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/stacklok/toolhive/pkg/container"
	kubernetes "github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// NewRunCmd creates a new run command for testing
func NewRunCmd() *cobra.Command {
	return runCmd
}

var runCmd = &cobra.Command{
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

var (
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

	// ConfigMap reference flag (for identification only)
	runFromConfigMap string

	// configMapReader is used for dependency injection in tests
	// If nil, the default implementation will be used
	configMapReader kubernetes.RunConfigMapReader
)

func init() {
	runCmd.Flags().StringVar(&runTransport, "transport", "", "Transport mode (sse, streamable-http or stdio)")
	runCmd.Flags().StringVar(&runProxyMode, "proxy-mode", "sse", "Proxy mode for stdio transport (sse or streamable-http)")
	runCmd.Flags().StringVar(&runName, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	runCmd.Flags().IntVar(&runProxyPort, "proxy-port", 0, "Port for the HTTP proxy to listen on (host port)")
	runCmd.Flags().StringVar(&runHost, "host", transport.LocalhostIPv4, "Host for the HTTP proxy to listen on (IP or hostname)")
	runCmd.Flags().IntVar(&runTargetPort, "target-port", 0,
		"Port for the container to expose (only applicable to SSE or Streamable HTTP transport)")
	runCmd.Flags().StringVar(
		&runPermissionProfile,
		"permission-profile",
		"",
		"Permission profile to use (none, network, or path to JSON file)",
	)
	runCmd.Flags().StringArrayVarP(
		&runEnv,
		"env",
		"e",
		[]string{},
		"Environment variables to pass to the MCP server (format: KEY=VALUE)",
	)
	runCmd.Flags().StringVar(
		&runK8sPodPatch,
		"k8s-pod-patch",
		"",
		"JSON string to patch the Kubernetes pod template (only applicable when using Kubernetes runtime)",
	)
	// This is used for the K8s operator which wraps the run command, but shouldn't be visible to users.
	if err := runCmd.Flags().MarkHidden("k8s-pod-patch"); err != nil {
		logger.Warnf("Error hiding flag: %v", err)
	}
	runCmd.Flags().StringVar(
		&runThvCABundle,
		"thv-ca-bundle",
		"",
		"Path to CA certificate bundle for ToolHive HTTP operations (JWKS, OIDC discovery, etc.)",
	)
	runCmd.Flags().StringVar(
		&runJWKSAuthTokenFile,
		"jwks-auth-token-file",
		"",
		"Path to file containing bearer token for authenticating JWKS/OIDC requests",
	)
	runCmd.Flags().BoolVar(
		&runJWKSAllowPrivateIP,
		"jwks-allow-private-ip",
		false,
		"Allow JWKS/OIDC endpoints on private IP addresses (use with caution)",
	)
	runCmd.Flags().StringVar(&oidcIssuer, "oidc-issuer", "", "OIDC issuer URL (e.g., https://accounts.google.com)")
	runCmd.Flags().StringVar(&oidcAudience, "oidc-audience", "", "Expected audience for the token")
	runCmd.Flags().StringVar(&oidcJwksURL, "oidc-jwks-url", "", "URL to fetch the JWKS from")
	runCmd.Flags().StringVar(&oidcClientID, "oidc-client-id", "", "OIDC client ID")
	runCmd.Flags().StringVar(&oidcClientSecret, "oidc-client-secret", "", "OIDC client secret (optional, for introspection)")
	runCmd.Flags().StringVar(&oidcIntrospectionURL, "oidc-introspection-url", "", "OIDC token introspection URL")

	// the below aren't used or set via the operator, so we need to see if lower level packages use their defaults
	runCmd.Flags().StringArrayVarP(
		&runVolumes,
		"volume",
		"v",
		[]string{},
		"Mount a volume into the container (format: host-path:container-path[:ro])",
	)
	runCmd.Flags().StringArrayVar(
		&runSecrets,
		"secret",
		[]string{},
		"Specify a secret to be fetched from the secrets manager and set as an environment variable (format: NAME,target=TARGET)",
	)
	runCmd.Flags().StringVar(
		&runAuthzConfig,
		"authz-config",
		"",
		"Path to the authorization configuration file",
	)
	runCmd.Flags().StringVar(
		&runAuditConfig,
		"audit-config",
		"",
		"Path to the audit configuration file",
	)
	runCmd.Flags().BoolVar(
		&runEnableAudit,
		"enable-audit",
		false,
		"Enable audit logging with default configuration",
	)

	// Add OpenTelemetry flags
	runCmd.Flags().BoolVar(&runOtelEnabled, "otel-enabled", false,
		"Enable OpenTelemetry")
	runCmd.Flags().StringVar(&runOtelEndpoint, "otel-endpoint", "",
		"OpenTelemetry endpoint URL (defaults to http://localhost:4318)")
	runCmd.Flags().StringVar(&runOtelServiceName, "otel-service-name", "",
		"OpenTelemetry service name (defaults to toolhive-mcp-proxy)")
	runCmd.Flags().StringArrayVar(&runOtelHeaders, "otel-headers", nil,
		"OpenTelemetry OTLP headers in key=value format (e.g., x-honeycomb-team=your-api-key)")
	runCmd.Flags().BoolVar(&runOtelInsecure, "otel-insecure", false,
		"Connect to the OpenTelemetry endpoint using HTTP instead of HTTPS")
	runCmd.Flags().BoolVar(&runOtelTracingEnabled, "otel-tracing-enabled", false,
		"Enable distributed tracing (when OTLP endpoint is configured)")
	runCmd.Flags().BoolVar(&runOtelMetricsEnabled, "otel-metrics-enabled", false,
		"Enable OTLP metrics export (when OTLP endpoint is configured)")
	runCmd.Flags().Float64Var(&runOtelTracingSamplingRate, "otel-tracing-sampling-rate", 0.0,
		"OpenTelemetry trace sampling rate (0.0-1.0)")
	runCmd.Flags().BoolVar(&enablePrometheusMetricsPath, "enable-prometheus-metrics-path", false,
		"Enable Prometheus-style /metrics endpoint on the main transport port")
	runCmd.Flags().BoolVar(&runIsolateNetwork, "isolate-network", false,
		"Isolate the container network from the host (default: false)")
	runCmd.Flags().StringArrayVar(
		&runToolsFilter,
		"tools",
		nil,
		"Filter MCP server tools (comma-separated list of tool names)",
	)
	runCmd.Flags().StringVar(
		&runResourceURL,
		"resource-url",
		"",
		"Explicit resource URL for OAuth discovery endpoint (RFC 9728)",
	)
	runCmd.Flags().StringVar(
		&runEnvFileDir,
		"env-file-dir",
		"",
		"Load environment variables from all files in a directory",
	)
	runCmd.Flags().StringVar(
		&runFromConfigMap,
		"from-configmap",
		"",
		"[Experimental] Load configuration from a Kubernetes ConfigMap (format: namespace/configmap-name). "+
			"This flag is mutually exclusive with other configuration flags.",
	)
}

// validateConfigMapOnlyMode validates that no conflicting flags are used with --from-configmap
// It dynamically finds all flags that could conflict by checking which ones are changed
func validateConfigMapOnlyMode(cmd *cobra.Command) error {
	// If --from-configmap is not set, no validation needed
	fromConfigMapFlag := cmd.Flag("from-configmap")
	if fromConfigMapFlag == nil || !fromConfigMapFlag.Changed {
		return nil
	}

	var conflictingFlags []string

	// Check all flags that are explicitly set by the user
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		// Skip the from-configmap flag itself and flags that weren't changed
		if flag.Name == "from-configmap" || !flag.Changed {
			return
		}

		// Skip flags that are safe to use with ConfigMap (like help, debug, etc.)
		if isSafeFlagWithConfigMap(flag.Name) {
			return
		}

		// All other changed flags are considered conflicting
		conflictingFlags = append(conflictingFlags, "--"+flag.Name)
	})

	if len(conflictingFlags) > 0 {
		return fmt.Errorf("cannot use --from-configmap with the following flags (configuration should be provided by ConfigMap): %s",
			strings.Join(conflictingFlags, ", "))
	}

	return nil
}

// isSafeFlagWithConfigMap returns true for flags that are safe to use with --from-configmap
// These are typically operational flags that don't affect the RunConfig
func isSafeFlagWithConfigMap(flagName string) bool {
	safeFlagsWithConfigMap := map[string]bool{
		"help":  true,
		"debug": true,
		// Add other safe flags here if needed
	}
	return safeFlagsWithConfigMap[flagName]
}

func runCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Handle ConfigMap configuration if specified
	var configMapRunConfig *runner.RunConfig
	if runFromConfigMap != "" {
		// Validate that conflicting flags are not used with --from-configmap first
		if err := validateConfigMapOnlyMode(cmd); err != nil {
			return err
		}

		var err error
		configMapRunConfig, err = identifyAndReadConfigMap(ctx, runFromConfigMap)
		if err != nil {
			return fmt.Errorf("failed to load ConfigMap configuration: %w", err)
		}
	}

	// Validate the host flag and default resolving to IP in case hostname is provided
	validatedHost, err := ValidateAndNormaliseHostFlag(runHost)
	if err != nil {
		return fmt.Errorf("invalid host: %s", runHost)
	}
	runHost = validatedHost

	// Validate and setup proxy mode
	if !types.IsValidProxyMode(runProxyMode) {
		return fmt.Errorf("invalid value for --proxy-mode: %s (valid values: sse, streamable-http)", runProxyMode)
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

	finalOtelEnvironmentVariables := []string{}

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

	// Parse environment variables from slice to map
	envVarsMap, err := environment.ParseEnvironmentVariables(runEnv)
	if err != nil {
		return fmt.Errorf("failed to parse environment variables: %v", err)
	}

	// Initialize a new RunConfig builder with values from ConfigMap or command-line flags
	var builder *runner.RunConfigBuilder
	if configMapRunConfig != nil {
		// Use ConfigMap configuration
		builder = runner.NewRunConfigBuilder().WithRuntime(rt).WithDebug(debugMode)
		applyRunConfigToBuilder(builder, configMapRunConfig)
	} else {
		// Initialize with values from command-line flags (original behavior)
		builder = runner.NewRunConfigBuilder().
			WithRuntime(rt).
			WithCmdArgs(cmdArgs).
			WithName(runName).
			WithImage(mcpServerImage).
			WithHost(runHost).
			WithTargetHost(transport.LocalhostIPv4).
			WithDebug(debugMode).
			WithVolumes(runVolumes).
			WithSecrets(runSecrets).
			WithAuthzConfigPath(runAuthzConfig).
			WithAuditConfigPath(runAuditConfig).
			WithPermissionProfileNameOrPath(runPermissionProfile).
			WithNetworkIsolation(runIsolateNetwork).
			WithK8sPodPatch(runK8sPodPatch).
			WithProxyMode(types.ProxyMode(runProxyMode)).
			WithTransportAndPorts(runTransport, runProxyPort, runTargetPort).
			WithAuditEnabled(runEnableAudit, runAuditConfig).
			WithOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID, oidcClientSecret,
				runThvCABundle, runJWKSAuthTokenFile, runResourceURL, runJWKSAllowPrivateIP).
			WithTelemetryConfig(runOtelEndpoint, enablePrometheusMetricsPath, runOtelTracingEnabled,
				runOtelMetricsEnabled, runOtelServiceName, runOtelTracingSamplingRate,
				runOtelHeaders, runOtelInsecure, finalOtelEnvironmentVariables).
			WithToolsFilter(runToolsFilter)
	}

	// Process environment files
	if runEnvFileDir != "" {
		builder, err = builder.WithEnvFilesFromDirectory(runEnvFileDir)
		if err != nil {
			return fmt.Errorf("failed to process env files from directory %s: %v", runEnvFileDir, err)
		}
	}

	runConfig, err := builder.Build(ctx, imageMetadata, envVarsMap, envVarValidator)
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

// identifyAndReadConfigMap identifies and reads a ConfigMap, returning its RunConfig contents
func identifyAndReadConfigMap(ctx context.Context, configMapRef string) (*runner.RunConfig, error) {
	reader, err := kubernetes.NewConfigMapReader()
	if err != nil {
		return nil, err
	}
	// Get the runconfig.json data from the ConfigMap
	runConfigJSON, err := reader.GetRunConfigMap(ctx, configMapRef)
	if err != nil {
		return nil, err
	}

	// Parse and return the RunConfig
	var runConfig runner.RunConfig
	if err := json.Unmarshal([]byte(runConfigJSON), &runConfig); err != nil {
		return nil, fmt.Errorf("ConfigMap '%s' contains invalid runconfig.json: %w", configMapRef, err)
	}

	return &runConfig, nil
}

// applyRunConfigToBuilder applies a RunConfig to the builder (works for both ConfigMap and flags)
func applyRunConfigToBuilder(builder *runner.RunConfigBuilder, config *runner.RunConfig) {
	builder.WithImage(config.Image).
		WithName(config.Name).
		WithCmdArgs(config.CmdArgs).
		WithHost(config.Host).
		WithTargetHost(config.TargetHost).
		WithVolumes(config.Volumes).
		WithSecrets(config.Secrets).
		WithAuthzConfigPath(config.AuthzConfigPath).
		WithAuditConfigPath(config.AuditConfigPath).
		WithAuditEnabled(config.AuditConfig != nil, config.AuditConfigPath).
		WithPermissionProfileNameOrPath(config.PermissionProfileNameOrPath).
		WithNetworkIsolation(config.IsolateNetwork).
		WithK8sPodPatch(config.K8sPodTemplatePatch).
		WithProxyMode(config.ProxyMode).
		WithGroup(config.Group).
		WithToolsFilter(config.ToolsFilter).
		WithEnvVars(config.EnvVars).
		WithTransportAndPorts(string(config.Transport), config.Port, config.TargetPort)

	// Apply complex configs if they exist
	if config.AuthzConfig != nil {
		builder.WithAuthzConfig(config.AuthzConfig)
	}
	// Note: AuditConfig is handled via WithAuditEnabled in builder based on the flag and path
	if config.PermissionProfile != nil {
		builder.WithPermissionProfile(config.PermissionProfile)
	}
	if config.ToolsOverride != nil {
		builder.WithToolsOverride(config.ToolsOverride)
	}
	if config.OIDCConfig != nil {
		builder.WithOIDCConfig(config.OIDCConfig.Issuer, config.OIDCConfig.Audience,
			config.OIDCConfig.JWKSURL, "", // IntrospectionURL not available in TokenValidatorConfig
			config.OIDCConfig.ClientID, config.OIDCConfig.ClientSecret,
			config.OIDCConfig.CACertPath, config.OIDCConfig.AuthTokenFile,
			"", config.OIDCConfig.AllowPrivateIP) // ResourceURL not available
	}
	if config.TelemetryConfig != nil {
		// Convert headers from map[string]string to []string
		var headersSlice []string
		for k, v := range config.TelemetryConfig.Headers {
			headersSlice = append(headersSlice, k+"="+v)
		}

		builder.WithTelemetryConfig(config.TelemetryConfig.Endpoint, config.TelemetryConfig.EnablePrometheusMetricsPath,
			config.TelemetryConfig.TracingEnabled, config.TelemetryConfig.MetricsEnabled,
			config.TelemetryConfig.ServiceName, config.TelemetryConfig.SamplingRate,
			headersSlice, config.TelemetryConfig.Insecure,
			config.TelemetryConfig.EnvironmentVariables)
	}
}

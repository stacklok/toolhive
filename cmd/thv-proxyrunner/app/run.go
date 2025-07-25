package app

import (
	"fmt"
	"net"
	"os"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"
)

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

	oidcIssuer            string
	oidcAudience          string
	oidcJwksURL           string
	oidcClientID          string
	oidcAllowOpaqueTokens bool

	// OpenTelemetry flags
	runOtelServiceName                 string
	runOtelHeaders                     []string
	runOtelInsecure                    bool
	runOtelEnablePrometheusMetricsPath bool

	// Network isolation flag
	runIsolateNetwork bool

	// Tools filter
	runToolsFilter []string
)

func init() {
	runCmd.Flags().StringVar(&runTransport, "transport", "", "Transport mode (sse, streamable-http or stdio)")
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
	runCmd.Flags().BoolVar(&oidcAllowOpaqueTokens, "oidc-skip-opaque-token-validation",
		false, "Allow skipping validation of opaque tokens (default: false)",
	)

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
	runCmd.Flags().StringVar(&runOtelServiceName, "otel-service-name", "",
		"OpenTelemetry service name (defaults to toolhive-mcp-proxy)")
	runCmd.Flags().StringArrayVar(&runOtelHeaders, "otel-headers", nil,
		"OpenTelemetry OTLP headers in key=value format (e.g., x-honeycomb-team=your-api-key)")
	runCmd.Flags().BoolVar(&runOtelInsecure, "otel-insecure", false,
		"Connect to the OpenTelemetry endpoint using HTTP instead of HTTPS")
	runCmd.Flags().BoolVar(&runOtelEnablePrometheusMetricsPath, "otel-enable-prometheus-metrics-path", false,
		"Enable Prometheus-style /metrics endpoint on the main transport port")
	runCmd.Flags().BoolVar(&runIsolateNetwork, "isolate-network", false,
		"Isolate the container network from the host (default: false)")
	runCmd.Flags().StringArrayVar(
		&runToolsFilter,
		"tools",
		nil,
		"Filter MCP server tools (comma-separated list of tool names)",
	)
}

func runCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Validate the host flag and default resolving to IP in case hostname is provided
	validatedHost, err := ValidateAndNormaliseHostFlag(runHost)
	if err != nil {
		return fmt.Errorf("invalid host: %s", runHost)
	}
	runHost = validatedHost

	// Get the name of the MCP server to run.
	// This may be a server name from the registry, a container image, or a protocol scheme.
	mcpServerImage := args[0]

	// Process command arguments using os.Args to find everything after --
	cmdArgs := parseCommandArguments(os.Args)

	// Print the processed command arguments for debugging
	logger.Debugf("Processed cmdArgs: %v", cmdArgs)

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// we set the below to empty values because they currently aren't used in kubernetes.
	// we will remove the below completely when we have a kubernetes runner that doesn't require these values.
	finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables := "", 0.0, []string{}

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

	// Initialize a new RunConfig with values from command-line flags
	runConfig, err := runner.NewRunConfigBuilder().
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
		WithProxyMode(types.ProxyMode("sse")).
		WithTransportAndPorts(runTransport, runProxyPort, runTargetPort).
		WithAuditEnabled(runEnableAudit, runAuditConfig).
		WithOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcClientID, oidcAllowOpaqueTokens,
			runThvCABundle, runJWKSAuthTokenFile, runJWKSAllowPrivateIP).
		WithTelemetryConfig(finalOtelEndpoint, runOtelEnablePrometheusMetricsPath, runOtelServiceName,
			finalOtelSamplingRate, runOtelHeaders, runOtelInsecure, finalOtelEnvironmentVariables).
		WithToolsFilter(runToolsFilter).
		Build(ctx, imageMetadata, runEnv, envVarValidator)
	if err != nil {
		return fmt.Errorf("failed to create RunConfig: %v", err)
	}

	workloadManager := workloads.NewManagerFromRuntime(rt)
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

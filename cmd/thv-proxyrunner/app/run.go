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

3. Using a protocol scheme:
   $ thv run uvx://package-name [-- args...]
   $ thv run npx://package-name [-- args...]
   $ thv run go://package-name [-- args...]
   $ thv run go://./local-path [-- args...]
   Automatically generates a container that runs the specified package
   using either uvx (Python with uv package manager), npx (Node.js),
   or go (Golang). For Go, you can also specify local paths starting
   with './' or '../' to build and run local Go projects.

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
	runPort               int
	runProxyPort          int
	runTargetPort         int
	runTargetHost         string
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

	// OpenTelemetry flags
	runOtelEndpoint                    string
	runOtelServiceName                 string
	runOtelSamplingRate                float64
	runOtelHeaders                     []string
	runOtelInsecure                    bool
	runOtelEnablePrometheusMetricsPath bool
	runOtelEnvironmentVariables        []string

	// Network isolation flag
	runIsolateNetwork bool
)

func init() {
	// runCmd.Flags().StringVar(&runTransport, "transport", "", "Transport mode (sse, streamable-http or stdio)")
	// runCmd.Flags().StringVar(&runName, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	runCmd.Flags().IntVar(&runProxyPort, "proxy-port", 0, "Port for the HTTP proxy to listen on (host port)")
	// runCmd.Flags().IntVar(&runPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	// if err := runCmd.Flags().MarkDeprecated("port", "use --proxy-port instead"); err != nil {
	// 	logger.Warnf("Error marking port flag as deprecated: %v", err)
	// }
	// runCmd.Flags().IntVar(&runTargetPort, "target-port", 0,
	// 	"Port for the container to expose (only applicable to SSE or Streamable HTTP transport)")
	runCmd.Flags().StringVar(
		&runTargetHost,
		"target-host",
		transport.LocalhostIPv4,
		"Host to forward traffic to (only applicable to SSE or Streamable HTTP transport)")
	// runCmd.Flags().StringVar(
	// 	&runPermissionProfile,
	// 	"permission-profile",
	// 	"",
	// 	"Permission profile to use (none, network, or path to JSON file)",
	// )
	// runCmd.Flags().StringArrayVarP(
	// 	&runEnv,
	// 	"env",
	// 	"e",
	// 	[]string{},
	// 	"Environment variables to pass to the MCP server (format: KEY=VALUE)",
	// )
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
	// runCmd.Flags().StringVar(
	// 	&runK8sPodPatch,
	// 	"k8s-pod-patch",
	// 	"",
	// 	"JSON string to patch the Kubernetes pod template (only applicable when using Kubernetes runtime)",
	// )
	// runCmd.Flags().StringVar(
	// 	&runThvCABundle,
	// 	"thv-ca-bundle",
	// 	"",
	// 	"Path to CA certificate bundle for ToolHive HTTP operations (JWKS, OIDC discovery, etc.)",
	// )
	// runCmd.Flags().StringVar(
	// 	&runJWKSAuthTokenFile,
	// 	"jwks-auth-token-file",
	// 	"",
	// 	"Path to file containing bearer token for authenticating JWKS/OIDC requests",
	// )
	// runCmd.Flags().BoolVar(
	// 	&runJWKSAllowPrivateIP,
	// 	"jwks-allow-private-ip",
	// 	false,
	// 	"Allow JWKS/OIDC endpoints on private IP addresses (use with caution)",
	// )

	// This is used for the K8s operator which wraps the run command, but shouldn't be visible to users.
	if err := runCmd.Flags().MarkHidden("k8s-pod-patch"); err != nil {
		logger.Warnf("Error hiding flag: %v", err)
	}

	// Add OIDC validation flags
	AddOIDCFlags(runCmd)

	// Add OpenTelemetry flags
	runCmd.Flags().StringVar(&runOtelEndpoint, "otel-endpoint", "",
		"OpenTelemetry OTLP endpoint URL (e.g., https://api.honeycomb.io)")
	runCmd.Flags().StringVar(&runOtelServiceName, "otel-service-name", "",
		"OpenTelemetry service name (defaults to toolhive-mcp-proxy)")
	runCmd.Flags().Float64Var(&runOtelSamplingRate, "otel-sampling-rate", 0.1,
		"OpenTelemetry trace sampling rate (0.0-1.0)")
	runCmd.Flags().StringArrayVar(&runOtelHeaders, "otel-headers", nil,
		"OpenTelemetry OTLP headers in key=value format (e.g., x-honeycomb-team=your-api-key)")
	runCmd.Flags().BoolVar(&runOtelInsecure, "otel-insecure", false,
		"Disable TLS verification for OpenTelemetry endpoint")
	runCmd.Flags().BoolVar(&runOtelEnablePrometheusMetricsPath, "otel-enable-prometheus-metrics-path", false,
		"Enable Prometheus-style /metrics endpoint on the main transport port")
	runCmd.Flags().StringArrayVar(&runOtelEnvironmentVariables, "otel-env-vars", nil,
		"Environment variable names to include in OpenTelemetry spans "+
			"(comma-separated: ENV1,ENV2)")
	runCmd.Flags().BoolVar(&runIsolateNetwork, "isolate-network", false,
		"Isolate the container network from the host (default: false)")

}

// AddOIDCFlags adds OIDC validation flags to the provided command.
func AddOIDCFlags(cmd *cobra.Command) {
	// cmd.Flags().String("oidc-issuer", "", "OIDC issuer URL (e.g., https://accounts.google.com)")
	// cmd.Flags().String("oidc-audience", "", "Expected audience for the token")
	// cmd.Flags().String("oidc-jwks-url", "", "URL to fetch the JWKS from")
	// cmd.Flags().String("oidc-client-id", "", "OIDC client ID")
	cmd.Flags().Bool("oidc-skip-opaque-token-validation", false, "Allow skipping validation of opaque tokens")
}

// GetStringFlagOrEmpty tries to get the string value of the given flag.
// If the flag doesn't exist or there's an error, it returns an empty string.
func GetStringFlagOrEmpty(cmd *cobra.Command, flagName string) string {
	value, err := cmd.Flags().GetString(flagName)
	if err != nil {
		return ""
	}
	return value
}

func getOidcFromFlags(cmd *cobra.Command) (string, string, string, string, bool, error) {
	oidcIssuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
	oidcAudience := GetStringFlagOrEmpty(cmd, "oidc-audience")
	oidcJwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
	oidcClientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")
	oidcAllowOpaqueTokens, err := cmd.Flags().GetBool("oidc-skip-opaque-token-validation")
	if err != nil {
		return "", "", "", "", false, fmt.Errorf("failed to get oidc-skip-opaque-token-validation flag: %v", err)
	}

	return oidcIssuer, oidcAudience, oidcJwksURL, oidcClientID, oidcAllowOpaqueTokens, nil
}

func runCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Validate the host flag and default resolving to IP in case hostname is provided
	validatedHost, err := ValidateAndNormaliseHostFlag(transport.LocalhostIPv4)
	if err != nil {
		return fmt.Errorf("invalid host: %s", runHost)
	}
	runHost = validatedHost

	// Get the name of the MCP server to run.
	// This may be a server name from the registry, a container image, or a protocol scheme.
	serverOrImage := args[0]

	// Process command arguments using os.Args to find everything after --
	cmdArgs := parseCommandArguments(os.Args)

	// Print the processed command arguments for debugging
	logger.Debugf("Processed cmdArgs: %v", cmdArgs)

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Get OIDC flag values
	oidcIssuer, oidcAudience, oidcJwksURL, oidcClientID, oidcAllowOpaqueTokens, err := getOidcFromFlags(cmd)
	if err != nil {
		return fmt.Errorf("failed to get OIDC flags: %v", err)
	}

	// we set the below to empty values because they currently aren't used in kubernetes.
	// we will remove the below completely when we have a kubernetes runner that doens't require these values.
	finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables := "", 0.0, []string{}

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}
	workloadManager := workloads.NewManagerFromRuntime(rt)

	// Select an env var validation strategy depending on how the CLI is run:
	// If we have called the CLI directly, we use the CLIEnvVarValidator.
	// If we are running in detached mode, or the CLI is wrapped by the K8s operator,
	// we use the DetachedEnvVarValidator.
	envVarValidator := &runner.DetachedEnvVarValidator{}

	var imageMetadata *registry.ImageMetadata
	imageURL := serverOrImage

	// Determine effective port value
	effectivePort := 0
	if runPort != 0 {
		effectivePort = runPort
	}

	// Initialize a new RunConfig with values from command-line flags
	// TODO: As noted elsewhere, we should use the builder pattern here to make it more readable.
	runConfig, err := runner.NewRunConfigFromFlags(
		ctx,
		rt,
		cmdArgs,
		runName,
		imageURL,
		imageMetadata,
		runHost,
		debugMode,
		runVolumes,
		runSecrets,
		runAuthzConfig,
		runAuditConfig,
		runEnableAudit,
		runPermissionProfile,
		runTargetHost,
		runTransport,
		effectivePort,
		runTargetPort,
		runEnv,
		oidcIssuer,
		oidcAudience,
		oidcJwksURL,
		oidcClientID,
		oidcAllowOpaqueTokens,
		finalOtelEndpoint,
		runOtelServiceName,
		finalOtelSamplingRate,
		runOtelHeaders,
		runOtelInsecure,
		runOtelEnablePrometheusMetricsPath,
		finalOtelEnvironmentVariables,
		runIsolateNetwork,
		runK8sPodPatch,
		runThvCABundle,
		runJWKSAuthTokenFile,
		runJWKSAllowPrivateIP,
		envVarValidator,
		types.ProxyMode("sse"),
	)
	if err != nil {
		return fmt.Errorf("failed to create RunConfig: %v", err)
	}

	// Once we have built the RunConfig, start the MCP workload.
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

package app

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
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
	runTransport         string
	runName              string
	runHost              string
	runPort              int
	runTargetPort        int
	runTargetHost        string
	runPermissionProfile string
	runEnv               []string
	runForeground        bool
	runVolumes           []string
	runSecrets           []string
	runAuthzConfig       string
	runAuditConfig       string
	runEnableAudit       bool
	runK8sPodPatch       string
	runCACertPath        string
	runVerifyImage       string

	// OpenTelemetry flags
	runOtelEndpoint                    string
	runOtelServiceName                 string
	runOtelSamplingRate                float64
	runOtelHeaders                     []string
	runOtelInsecure                    bool
	runOtelEnablePrometheusMetricsPath bool

	// Network isolation flag
	runIsolateNetwork bool
)

func init() {
	runCmd.Flags().StringVar(&runTransport, "transport", "stdio", "Transport mode (sse, streamable-http or stdio)")
	runCmd.Flags().StringVar(&runName, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	runCmd.Flags().StringVar(&runHost, "host", transport.LocalhostIPv4, "Host for the HTTP proxy to listen on (IP or hostname)")
	runCmd.Flags().IntVar(&runPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	runCmd.Flags().IntVar(&runTargetPort, "target-port", 0,
		"Port for the container to expose (only applicable to SSE or Streamable HTTP transport)")
	runCmd.Flags().StringVar(
		&runTargetHost,
		"target-host",
		transport.LocalhostIPv4,
		"Host to forward traffic to (only applicable to SSE or Streamable HTTP transport)")
	runCmd.Flags().StringVar(
		&runPermissionProfile,
		"permission-profile",
		permissions.ProfileNetwork,
		"Permission profile to use (none, network, or path to JSON file)",
	)
	runCmd.Flags().StringArrayVarP(
		&runEnv,
		"env",
		"e",
		[]string{},
		"Environment variables to pass to the MCP server (format: KEY=VALUE)",
	)
	runCmd.Flags().BoolVarP(&runForeground, "foreground", "f", false, "Run in foreground mode (block until container exits)")
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
	runCmd.Flags().StringVar(
		&runK8sPodPatch,
		"k8s-pod-patch",
		"",
		"JSON string to patch the Kubernetes pod template (only applicable when using Kubernetes runtime)",
	)
	runCmd.Flags().StringVar(
		&runCACertPath,
		"ca-cert",
		"",
		"Path to a custom CA certificate file to use for container builds",
	)
	runCmd.Flags().StringVar(
		&runVerifyImage,
		"image-verification",
		retriever.VerifyImageWarn,
		fmt.Sprintf(
			"Set image verification mode (%s, %s, %s)",
			retriever.VerifyImageWarn,
			retriever.VerifyImageEnabled,
			retriever.VerifyImageDisabled,
		),
	)

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
	runCmd.Flags().BoolVar(&runIsolateNetwork, "isolate-network", false,
		"Isolate the container network from the host (default: false)")

}

func runCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Validate the host flag and default resolving to IP in case hostname is provided
	validatedHost, err := ValidateAndNormaliseHostFlag(runHost)
	if err != nil {
		return fmt.Errorf("invalid host: %s", runHost)
	}
	runHost = validatedHost

	// Get the server name or image
	serverOrImage := args[0]

	// Process command arguments using os.Args to find everything after --
	cmdArgs := parseCommandArguments(os.Args)

	// Print the processed command arguments for debugging
	logger.Debugf("Processed cmdArgs: %v", cmdArgs)

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Get OIDC flag values
	oidcIssuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
	oidcAudience := GetStringFlagOrEmpty(cmd, "oidc-audience")
	oidcJwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
	oidcClientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}
	workloadManager := workloads.NewManagerFromRuntime(rt)

	// Initialize a new RunConfig with values from command-line flags
	runConfig := runner.NewRunConfigFromFlags(
		rt,
		cmdArgs,
		runName,
		runHost,
		debugMode,
		runVolumes,
		runSecrets,
		runAuthzConfig,
		runAuditConfig,
		runEnableAudit,
		runPermissionProfile,
		runTargetHost,
		oidcIssuer,
		oidcAudience,
		oidcJwksURL,
		oidcClientID,
		runOtelEndpoint,
		runOtelServiceName,
		runOtelSamplingRate,
		runOtelHeaders,
		runOtelInsecure,
		runOtelEnablePrometheusMetricsPath,
		runIsolateNetwork,
	)

	// Set the Kubernetes pod template patch if provided
	if runK8sPodPatch != "" {
		runConfig.K8sPodTemplatePatch = runK8sPodPatch
	}

	imageURL, imageMetadata, err := retriever.GetMCPServer(ctx, serverOrImage, runCACertPath, runVerifyImage)
	if err != nil {
		return fmt.Errorf("failed to retrieve MCP server: %v", err)
	}
	runConfig.Image = imageURL

	if imageMetadata != nil {
		// If the image came from our registry, apply settings from the registry metadata.
		err = applyRegistrySettings(ctx, cmd, serverOrImage, imageMetadata, runConfig, debugMode)
		if err != nil {
			return fmt.Errorf("failed to apply registry settings: %v", err)
		}
	}

	// Configure the RunConfig with transport, ports, permissions, etc.
	if err := configureRunConfig(runConfig, runTransport, runPort, runTargetPort, runEnv); err != nil {
		return err
	}

	// Once we have built the RunConfig, start the MCP workload.
	// If we are running the container in the foreground - call the RunWorkload method directly.
	if runForeground {
		return workloadManager.RunWorkload(ctx, runConfig)
	}
	return workloadManager.RunWorkloadDetached(runConfig)
}

// applyRegistrySettings applies settings from a registry server to the run config
func applyRegistrySettings(
	ctx context.Context,
	cmd *cobra.Command,
	serverName string,
	metadata *registry.ImageMetadata,
	runConfig *runner.RunConfig,
	debugMode bool,
) error {
	// Use the image from the registry
	runConfig.Image = metadata.Image

	// If name is not provided, use the metadata name from registry
	if runConfig.Name == "" {
		runConfig.Name = serverName
	}

	// Use registry transport if not overridden
	if !cmd.Flags().Changed("transport") {
		logDebug(debugMode, "Using registry transport: %s", metadata.Transport)
		runTransport = metadata.Transport
	} else {
		logDebug(debugMode, "Using provided transport: %s (overriding registry default: %s)",
			runTransport, metadata.Transport)
	}

	// Use registry target port if not overridden and transport is SSE or Streamable HTTP
	if !cmd.Flags().Changed("target-port") && (metadata.Transport == types.TransportTypeSSE.String() ||
		metadata.Transport == types.TransportTypeStreamableHTTP.String()) && metadata.TargetPort > 0 {
		logDebug(debugMode, "Using registry target port: %d", metadata.TargetPort)
		runTargetPort = metadata.TargetPort
	}

	// Prepend registry args to command-line args if available
	if len(metadata.Args) > 0 {
		logDebug(debugMode, "Prepending registry args: %v", metadata.Args)
		runConfig.CmdArgs = append(metadata.Args, runConfig.CmdArgs...)
	}

	// Note this logic will be moved elsewhere in a future PR.
	// Select an env var validation strategy depending on how the CLI is run:
	// If we have called the CLI directly, we use the CLIEnvVarValidator.
	// If we are running in detached mode, or the CLI is wrapped by the K8s operator,
	// we use the DetachedEnvVarValidator.
	var envVarValidator runner.EnvVarValidator
	if process.IsDetached() || container.IsKubernetesRuntime() {
		envVarValidator = &runner.DetachedEnvVarValidator{}
	} else {
		envVarValidator = &runner.CLIEnvVarValidator{}
	}

	// Process environment variables from registry.
	// This will be merged with command-line env vars in configureRunConfig
	if err := envVarValidator.Validate(ctx, metadata, runConfig, runEnv); err != nil {
		return fmt.Errorf("failed to validate required configuration values: %v", err)
	}

	// Create a temporary file for the permission profile if not explicitly provided
	if !cmd.Flags().Changed("permission-profile") {
		permProfilePath, err := runner.CreatePermissionProfileFile(serverName, metadata.Permissions)
		if err != nil {
			// Just log the error and continue with the default permission profile
			logger.Warnf("Warning: Failed to create permission profile file: %v", err)
		} else {
			// Update the permission profile path
			runConfig.PermissionProfileNameOrPath = permProfilePath
		}
	}

	return nil
}

// logDebug logs a message if debug mode is enabled
func logDebug(debugMode bool, format string, args ...interface{}) {
	if debugMode {
		logger.Infof(format+"", args...)
	}
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

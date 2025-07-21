package app

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var groupCmd = &cobra.Command{
	Use:   "group",
	Short: "Manage logical groupings of MCP servers",
	Long:  `The group command provides subcommands to manage logical groupings of MCP servers.`,
}

var groupCreateCmd = &cobra.Command{
	Use:   "create [group-name]",
	Short: "Create a new group of MCP servers",
	Long:  `Create a new logical group of MCP servers. The group can be used to organize and manage multiple MCP servers together.`,
	Args:  cobra.ExactArgs(1),
	RunE:  groupCreateCmdFunc,
}

// Shared flags for group add command (same as run command except --foreground)
var (
	groupAddTransport          string
	groupAddProxyMode          string
	groupAddName               string
	groupAddHost               string
	groupAddPort               int
	groupAddProxyPort          int
	groupAddTargetPort         int
	groupAddTargetHost         string
	groupAddPermissionProfile  string
	groupAddEnv                []string
	groupAddVolumes            []string
	groupAddSecrets            []string
	groupAddAuthzConfig        string
	groupAddAuditConfig        string
	groupAddEnableAudit        bool
	groupAddK8sPodPatch        string
	groupAddCACertPath         string
	groupAddVerifyImage        string
	groupAddThvCABundle        string
	groupAddJWKSAuthTokenFile  string
	groupAddJWKSAllowPrivateIP bool

	// OpenTelemetry flags
	groupAddOtelEndpoint                    string
	groupAddOtelServiceName                 string
	groupAddOtelSamplingRate                float64
	groupAddOtelHeaders                     []string
	groupAddOtelInsecure                    bool
	groupAddOtelEnablePrometheusMetricsPath bool
	groupAddOtelEnvironmentVariables        []string

	// Network isolation flag
	groupAddIsolateNetwork bool

	// Labels flag
	groupAddLabels []string
)

var groupAddCmd = &cobra.Command{
	Use:   "add [group-name] [flags] SERVER_OR_IMAGE_OR_PROTOCOL [-- ARGS...]",
	Short: "Add an MCP server to a group",
	Long: `Add an MCP server to a logical group. The server will be created in "Stopped" state and can be started later.

This command shares the same flags as 'thv run' except for --foreground, as the server is always created in stopped state.

Examples:
  # Add a server from registry to a group
  thv group add mygroup fetch

  # Add a custom image to a group
  thv group add mygroup ghcr.io/example/mcp-server:latest

  # Add a server with specific transport and port
  thv group add mygroup fetch --transport sse --proxy-port 8080`,
	Args: cobra.MinimumNArgs(2),
	RunE: groupAddCmdFunc,
	// Ignore unknown flags to allow passing flags to the MCP server
	FParseErrWhitelist: cobra.FParseErrWhitelist{
		UnknownFlags: true,
	},
}

func groupCreateCmdFunc(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	ctx := cmd.Context()

	manager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	if err := manager.Create(ctx, groupName); err != nil {
		return err
	}

	fmt.Printf("Group '%s' created successfully.\n", groupName)
	return nil
}

func groupAddCmdFunc(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	serverOrImage := args[1]
	ctx := cmd.Context()

	// Validate that the group exists
	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	exists, err := groupManager.Exists(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %w", err)
	}
	if !exists {
		return fmt.Errorf("group '%s' does not exist", groupName)
	}

	// Validate the host flag and default resolving to IP in case hostname is provided
	validatedHost, err := ValidateAndNormaliseHostFlag(groupAddHost)
	if err != nil {
		return fmt.Errorf("invalid host: %s", groupAddHost)
	}
	groupAddHost = validatedHost

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

	// Get OTEL flag values with config fallbacks
	cfg := config.GetConfig()
	finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables := getTelemetryFromFlags(cmd, cfg,
		groupAddOtelEndpoint, groupAddOtelSamplingRate, groupAddOtelEnvironmentVariables)

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
		imageURL, imageMetadata, err = retriever.GetMCPServer(ctx, serverOrImage, groupAddCACertPath, groupAddVerifyImage)
		if err != nil {
			return fmt.Errorf("failed to find or create the MCP server %s: %v", serverOrImage, err)
		}
	}

	// Validate proxy mode early
	if !types.IsValidProxyMode(groupAddProxyMode) {
		if groupAddProxyMode == "" {
			groupAddProxyMode = types.ProxyModeSSE.String() // default to SSE for backward compatibility
		} else {
			return fmt.Errorf("invalid value for --proxy-mode: %s", groupAddProxyMode)
		}
	}

	// Determine effective port value - prefer --proxy-port over --port for backwards compatibility
	effectivePort := groupAddProxyPort
	if effectivePort == 0 && groupAddPort != 0 {
		effectivePort = groupAddPort
	}

	// Add group label to the labels
	groupLabels := make([]string, len(groupAddLabels))
	copy(groupLabels, groupAddLabels)
	groupLabels = append(groupLabels, fmt.Sprintf("%s=%s", labels.LabelGroup, groupName))

	// Initialize a new RunConfig with values from command-line flags
	runConfig, err := runner.NewRunConfigFromFlags(
		ctx,
		rt,
		cmdArgs,
		groupAddName,
		imageURL,
		imageMetadata,
		groupAddHost,
		debugMode,
		groupAddVolumes,
		groupAddSecrets,
		groupAddAuthzConfig,
		groupAddAuditConfig,
		groupAddEnableAudit,
		groupAddPermissionProfile,
		groupAddTargetHost,
		groupAddTransport,
		effectivePort,
		groupAddTargetPort,
		groupAddEnv,
		groupLabels,
		oidcIssuer,
		oidcAudience,
		oidcJwksURL,
		oidcClientID,
		oidcAllowOpaqueTokens,
		finalOtelEndpoint,
		groupAddOtelServiceName,
		finalOtelSamplingRate,
		groupAddOtelHeaders,
		groupAddOtelInsecure,
		groupAddOtelEnablePrometheusMetricsPath,
		finalOtelEnvironmentVariables,
		groupAddIsolateNetwork,
		groupAddK8sPodPatch,
		groupAddThvCABundle,
		groupAddJWKSAuthTokenFile,
		groupAddJWKSAllowPrivateIP,
		envVarValidator,
		types.ProxyMode(groupAddProxyMode),
		groupName,
	)
	if err != nil {
		return fmt.Errorf("failed to create RunConfig: %v", err)
	}

	// Create the workload in stopped state
	if err := workloadManager.CreateWorkload(ctx, runConfig); err != nil {
		return fmt.Errorf("failed to create workload: %v", err)
	}

	fmt.Printf("MCP server '%s' added to group '%s' in 'Stopped' state.\n", runConfig.BaseName, groupName)
	fmt.Printf("Use 'thv start %s' to start the server.\n", runConfig.BaseName)

	return nil
}

func init() {
	groupCmd.AddCommand(groupCreateCmd)
	groupCmd.AddCommand(groupAddCmd)

	// Add shared flags from run command (excluding --foreground)
	addSharedRunFlags(groupAddCmd)
}

// addSharedRunFlags adds the same flags as the run command (excluding --foreground)
func addSharedRunFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&groupAddTransport, "transport", "", "Transport mode (sse, streamable-http or stdio)")
	cmd.Flags().StringVar(&groupAddProxyMode, "proxy-mode", "sse", "Proxy mode for stdio transport (sse or streamable-http)")
	cmd.Flags().StringVar(&groupAddName, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	cmd.Flags().StringVar(&groupAddHost, "host", transport.LocalhostIPv4, "Host for the HTTP proxy to listen on (IP or hostname)")
	cmd.Flags().IntVar(&groupAddProxyPort, "proxy-port", 0, "Port for the HTTP proxy to listen on (host port)")
	cmd.Flags().IntVar(&groupAddPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	if err := cmd.Flags().MarkDeprecated("port", "use --proxy-port instead"); err != nil {
		// Log warning but don't fail
	}
	cmd.Flags().IntVar(&groupAddTargetPort, "target-port", 0,
		"Port for the container to expose (only applicable to SSE or Streamable HTTP transport)")
	cmd.Flags().StringVar(
		&groupAddTargetHost,
		"target-host",
		transport.LocalhostIPv4,
		"Host to forward traffic to (only applicable to SSE or Streamable HTTP transport)")
	cmd.Flags().StringVar(
		&groupAddPermissionProfile,
		"permission-profile",
		"",
		"Permission profile to use (none, network, or path to JSON file)",
	)
	cmd.Flags().StringArrayVarP(
		&groupAddEnv,
		"env",
		"e",
		[]string{},
		"Environment variables to pass to the MCP server (format: KEY=VALUE)",
	)
	cmd.Flags().StringArrayVarP(
		&groupAddVolumes,
		"volume",
		"v",
		[]string{},
		"Mount a volume into the container (format: host-path:container-path[:ro])",
	)
	cmd.Flags().StringArrayVar(
		&groupAddSecrets,
		"secret",
		[]string{},
		"Specify a secret to be fetched from the secrets manager and set as an environment variable (format: NAME,target=TARGET)",
	)
	cmd.Flags().StringVar(
		&groupAddAuthzConfig,
		"authz-config",
		"",
		"Path to the authorization configuration file",
	)
	cmd.Flags().StringVar(
		&groupAddAuditConfig,
		"audit-config",
		"",
		"Path to the audit configuration file",
	)
	cmd.Flags().BoolVar(
		&groupAddEnableAudit,
		"enable-audit",
		false,
		"Enable audit logging with default configuration",
	)
	cmd.Flags().StringVar(
		&groupAddK8sPodPatch,
		"k8s-pod-patch",
		"",
		"JSON string to patch the Kubernetes pod template (only applicable when using Kubernetes runtime)",
	)
	cmd.Flags().StringVar(
		&groupAddCACertPath,
		"ca-cert",
		"",
		"Path to a custom CA certificate file to use for container builds",
	)
	cmd.Flags().StringVar(
		&groupAddVerifyImage,
		"image-verification",
		"warn",
		"Set image verification mode (warn, enabled, disabled)",
	)
	cmd.Flags().StringVar(
		&groupAddThvCABundle,
		"thv-ca-bundle",
		"",
		"Path to CA certificate bundle for ToolHive HTTP operations (JWKS, OIDC discovery, etc.)",
	)
	cmd.Flags().StringVar(
		&groupAddJWKSAuthTokenFile,
		"jwks-auth-token-file",
		"",
		"Path to file containing bearer token for authenticating JWKS/OIDC requests",
	)
	cmd.Flags().BoolVar(
		&groupAddJWKSAllowPrivateIP,
		"jwks-allow-private-ip",
		false,
		"Allow JWKS/OIDC endpoints on private IP addresses (use with caution)",
	)

	// Add OIDC validation flags
	AddOIDCFlags(cmd)

	// Add OpenTelemetry flags
	cmd.Flags().StringVar(&groupAddOtelEndpoint, "otel-endpoint", "",
		"OpenTelemetry OTLP endpoint URL (e.g., https://api.honeycomb.io)")
	cmd.Flags().StringVar(&groupAddOtelServiceName, "otel-service-name", "",
		"OpenTelemetry service name (defaults to toolhive-mcp-proxy)")
	cmd.Flags().Float64Var(&groupAddOtelSamplingRate, "otel-sampling-rate", 0.1,
		"OpenTelemetry trace sampling rate (0.0-1.0)")
	cmd.Flags().StringArrayVar(&groupAddOtelHeaders, "otel-headers", nil,
		"OpenTelemetry OTLP headers in key=value format (e.g., x-honeycomb-team=your-api-key)")
	cmd.Flags().BoolVar(&groupAddOtelInsecure, "otel-insecure", false,
		"Disable TLS verification for OpenTelemetry endpoint")
	cmd.Flags().BoolVar(&groupAddOtelEnablePrometheusMetricsPath, "otel-enable-prometheus-metrics-path", false,
		"Enable Prometheus-style /metrics endpoint on the main transport port")
	cmd.Flags().StringArrayVar(&groupAddOtelEnvironmentVariables, "otel-env-vars", nil,
		"Environment variable names to include in OpenTelemetry spans "+
			"(comma-separated: ENV1,ENV2)")
	cmd.Flags().BoolVar(&groupAddIsolateNetwork, "isolate-network", false,
		"Isolate the container network from the host (default: false)")
	cmd.Flags().StringArrayVarP(
		&groupAddLabels,
		"label",
		"l",
		[]string{},
		"Set labels on the container (format: key=value)",
	)
}

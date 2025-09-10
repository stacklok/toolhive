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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/stacklok/toolhive/pkg/container"
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

	// Handle ConfigMap identification if specified
	if runFromConfigMap != "" {
		// Validate that conflicting flags are not used with --from-configmap first
		if err := validateConfigMapOnlyMode(cmd); err != nil {
			return err
		}

		if err := identifyAndReadConfigMap(ctx, runFromConfigMap); err != nil {
			return fmt.Errorf("failed to identify ConfigMap: %w", err)
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

	// Initialize a new RunConfig with values from command-line flags
	builder := runner.NewRunConfigBuilder().
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

// parseConfigMapRef parses a ConfigMap reference in the format "namespace/configmap-name"
func parseConfigMapRef(ref string) (namespace, name string, err error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("expected format 'namespace/configmap-name', got '%s'", ref)
	}

	namespace = strings.TrimSpace(parts[0])
	name = strings.TrimSpace(parts[1])

	if namespace == "" {
		return "", "", fmt.Errorf("namespace cannot be empty")
	}
	if name == "" {
		return "", "", fmt.Errorf("configmap name cannot be empty")
	}

	return namespace, name, nil
}

// identifyAndReadConfigMap identifies and reads a ConfigMap to validate its existence and contents
// This function only validates the ConfigMap but does not use it for configuration
func identifyAndReadConfigMap(ctx context.Context, configMapRef string) error {
	// Parse namespace and ConfigMap name
	namespace, configMapName, err := parseConfigMapRef(configMapRef)
	if err != nil {
		return fmt.Errorf("invalid --from-configmap format: %w", err)
	}

	logger.Infof("Identifying ConfigMap '%s/%s'", namespace, configMapName)

	// Create in-cluster Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to create in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Get the ConfigMap
	configMap, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get ConfigMap '%s/%s': %w", namespace, configMapName, err)
	}

	// Validate that runconfig.json exists in the ConfigMap
	runConfigJSON, ok := configMap.Data["runconfig.json"]
	if !ok {
		return fmt.Errorf("ConfigMap '%s/%s' does not contain 'runconfig.json' key", namespace, configMapName)
	}

	// Validate that the JSON is parseable (but don't use it)
	var runConfig runner.RunConfig
	if err := json.Unmarshal([]byte(runConfigJSON), &runConfig); err != nil {
		return fmt.Errorf("ConfigMap '%s/%s' contains invalid runconfig.json: %w", namespace, configMapName, err)
	}

	logger.Infof("Successfully identified and validated ConfigMap '%s/%s' (contains %d bytes of runconfig.json)",
		namespace, configMapName, len(runConfigJSON))

	return nil
}

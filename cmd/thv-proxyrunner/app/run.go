package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/k8s"
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

3. From a ConfigMap (proxy runner only):
   $ thv-proxyrunner run --from-configmap namespace/configmap-name image-name
   Loads configuration from a Kubernetes ConfigMap containing runconfig.json
   The image-name argument overrides the image specified in the ConfigMap

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
	runProxyMode          string
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

	// ConfigMap source flag
	runFromConfigMap string
)

func init() {
	runCmd.Flags().StringVar(&runTransport, "transport", "", "Transport mode (sse, streamable-http or stdio)")
	runCmd.Flags().StringVar(&runProxyMode, "proxy-mode", "", "Proxy mode for stdio transport (sse or streamable-http)")
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
		"Load RunConfig from Kubernetes ConfigMap (format: namespace/configmap-name). When set, other configuration flags are ignored.",
	)
}

func runCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Validate arguments first - both ConfigMap and direct paths require the image argument
	if len(args) == 0 {
		return fmt.Errorf("server name, image, or protocol scheme is required")
	}

	// Handle ConfigMap loading path
	if runFromConfigMap != "" {
		return runFromConfigMapPath(ctx, cmd, args)
	}

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

// runFromConfigMapPath handles the --from-configmap execution path
func runFromConfigMapPath(ctx context.Context, cmd *cobra.Command, args []string) error {
	// Validate that other conflicting flags are not set when using --from-configmap
	if err := validateConfigMapOnlyMode(cmd); err != nil {
		return err
	}

	// Get the server/image from command line argument
	mcpServerImage := args[0]

	// Parse namespace and ConfigMap name
	namespace, configMapName, err := parseConfigMapRef(runFromConfigMap)
	if err != nil {
		return fmt.Errorf("invalid --from-configmap format: %w", err)
	}

	// Load RunConfig from ConfigMap
	runConfig, configMapChecksum, err := loadRunConfigFromConfigMap(ctx, namespace, configMapName)
	if err != nil {
		return fmt.Errorf("failed to load RunConfig from ConfigMap: %w", err)
	}

	// Set image from command line argument
	runConfig.Image = mcpServerImage

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Set runtime in the config (required for execution)
	runConfig.Deployer = rt

	// Configure ConfigMap checksum for deployment restart tracking
	// This must be done after setting the deployer to properly configure it
	runConfig.ConfigMapChecksum = configMapChecksum
	if configMapChecksum != "" {
		if k8sDeployer, ok := rt.(interface{ SetConfigMapChecksum(string) }); ok {
			k8sDeployer.SetConfigMapChecksum(configMapChecksum)
		}
	}

	// Create workload manager and run
	workloadManager, err := workloads.NewManagerFromRuntime(rt)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}

	return workloadManager.RunWorkload(ctx, runConfig)
}

// validateConfigMapOnlyMode ensures that when --from-configmap is used,
// only runtime-specific flags that are not stored in ConfigMap are allowed
func validateConfigMapOnlyMode(cmd *cobra.Command) error {
	// These flags are prohibited because they're stored in the ConfigMap
	conflictingFlags := []string{
		"transport", "proxy-mode", "name", "proxy-port", "host", "target-port",
		"permission-profile", "env", "volume", "secret", "authz-config",
		"audit-config", "enable-audit", "otel-enabled", "otel-endpoint",
		"otel-service-name", "otel-headers", "otel-insecure",
		"enable-prometheus-metrics-path", "isolate-network", "tools",
		"resource-url", "env-file-dir",
		"oidc-issuer", "oidc-audience", "oidc-jwks-url", "oidc-client-id",
		"oidc-client-secret", "oidc-introspection-url",
	}

	// These flags are allowed as they provide runtime-specific overrides
	// not stored in the ConfigMap or are required for container execution:
	// - k8s-pod-patch: runtime container configuration
	// - thv-ca-bundle: runtime certificate path
	// - jwks-auth-token-file: runtime token file path
	// - jwks-allow-private-ip: runtime security setting
	// Note: debug flag is also implicitly allowed as it's a common runtime override

	var setFlags []string
	for _, flagName := range conflictingFlags {
		flag := cmd.Flag(flagName)
		if flag != nil && flag.Changed {
			setFlags = append(setFlags, flagName)
		}
	}

	if len(setFlags) > 0 {
		return fmt.Errorf("when using --from-configmap, the following flags cannot be set "+
			"(they should be configured in the ConfigMap): %s", strings.Join(setFlags, ", "))
	}

	return nil
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

// loadRunConfigFromConfigMap loads a RunConfig from a Kubernetes ConfigMap
func loadRunConfigFromConfigMap(ctx context.Context, namespace, configMapName string) (*runner.RunConfig, string, error) {
	// Create in-cluster Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, "", fmt.Errorf("failed to create in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Get the ConfigMap
	configMap, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("failed to get ConfigMap '%s/%s': %w", namespace, configMapName, err)
	}

	// Extract runconfig.json from the ConfigMap
	runConfigJSON, ok := configMap.Data["runconfig.json"]
	if !ok {
		return nil, "", fmt.Errorf("ConfigMap '%s/%s' does not contain 'runconfig.json' key", namespace, configMapName)
	}

	// Unmarshal the RunConfig
	var runConfig runner.RunConfig
	if err := json.Unmarshal([]byte(runConfigJSON), &runConfig); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal RunConfig from ConfigMap: %w", err)
	}

	// Initialize maps if they are nil (JSON unmarshaling leaves them nil when empty)
	if runConfig.EnvVars == nil {
		runConfig.EnvVars = make(map[string]string)
	}
	if runConfig.ContainerLabels == nil {
		runConfig.ContainerLabels = make(map[string]string)
	}

	// Get or compute the ConfigMap checksum
	checksum := configMap.Annotations["toolhive.stacklok.dev/content-checksum"]
	if checksum == "" {
		// If no checksum annotation exists, compute it from the ConfigMap content
		checksum = k8s.ComputeConfigMapChecksum(configMap)
	}

	logger.Infof("Successfully loaded RunConfig from ConfigMap '%s/%s' with checksum %s", namespace, configMapName, checksum)
	return &runConfig, checksum, nil
}

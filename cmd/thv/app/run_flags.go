package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	cfg "github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/environment"
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

	// Remote authentication
	EnableRemoteAuth           bool
	RemoteAuthClientID         string
	RemoteAuthClientSecret     string
	RemoteAuthClientSecretFile string
	RemoteAuthScopes           []string
	RemoteAuthSkipBrowser      bool
	RemoteAuthTimeout          time.Duration
	RemoteAuthCallbackPort     int
	RemoteAuthBearerToken      string

	// Additional remote auth fields for registry-based servers
	RemoteAuthIssuer       string
	RemoteAuthAuthorizeURL string
	RemoteAuthTokenURL     string

	// Environment variables for remote servers
	EnvVars map[string]string

	// OAuth parameters for remote servers
	OAuthParams map[string]string
}

// AddRunFlags adds all the run flags to a command
func AddRunFlags(cmd *cobra.Command, config *RunFlags) {
	cmd.Flags().StringVar(&config.Transport, "transport", "", "Transport type to use (stdio, sse, streamable-http)")
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

	// Remote authentication flags
	cmd.Flags().BoolVar(&config.EnableRemoteAuth, "remote-auth", false, "Enable automatic OAuth authentication for remote MCP servers")
	cmd.Flags().StringVar(&config.RemoteAuthClientID, "remote-auth-client-id", "", "OAuth client ID for remote server authentication")
	cmd.Flags().StringVar(&config.RemoteAuthClientSecret, "remote-auth-client-secret", "", "OAuth client secret for remote server authentication")
	cmd.Flags().StringVar(&config.RemoteAuthClientSecretFile, "remote-auth-client-secret-file", "", "Path to file containing OAuth client secret")
	cmd.Flags().StringSliceVar(&config.RemoteAuthScopes, "remote-auth-scopes", []string{}, "OAuth scopes to request for remote server authentication")
	cmd.Flags().BoolVar(&config.RemoteAuthSkipBrowser, "remote-auth-skip-browser", false, "Skip opening browser for remote server OAuth flow")
	cmd.Flags().DurationVar(&config.RemoteAuthTimeout, "remote-auth-timeout", 5*time.Minute, "Timeout for OAuth authentication flow")
	cmd.Flags().IntVar(&config.RemoteAuthCallbackPort, "remote-auth-callback-port", 8666, "Port for OAuth callback server during remote authentication")
	cmd.Flags().StringVar(&config.RemoteAuthBearerToken, "remote-auth-bearer-token", "", "Bearer token for remote server authentication (alternative to OAuth)")

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
	runFlags *RunFlags,
	serverOrImage string,
	cmdArgs []string,
	debugMode bool,
	cmd *cobra.Command,
) (*runner.RunConfig, error) {
	// Validate the host flag
	validatedHost, err := ValidateAndNormaliseHostFlag(runFlags.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid host: %s", runFlags.Host)
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
		runFlags.OtelEndpoint, runFlags.OtelSamplingRate, runFlags.OtelEnvironmentVariables)

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Select an envVars var validation strategy depending on how the CLI is run:
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
	var remoteServerMetadata *registry.RemoteServerMetadata
	imageURL := serverOrImage
	transportType := runFlags.Transport

	// If --remote flag is provided, use it as the serverOrImage
	if runFlags.RemoteURL != "" {
		serverOrImage = runFlags.RemoteURL
		imageURL = runFlags.RemoteURL
	}

	// Try to get server from registry (container or remote) or direct URL
	imageURL, imageMetadata, remoteServerMetadata, err = retriever.GetMCPServerOrRemote(
		ctx, serverOrImage, runFlags.CACertPath, runFlags.VerifyImage)
	if err != nil {
		return nil, fmt.Errorf("failed to find or create the MCP server %s: %v", serverOrImage, err)
	}

	if remoteServerMetadata != nil {
		// Handle registry-based remote server
		runFlags.RemoteURL = remoteServerMetadata.URL
		if transportType == "" {
			transportType = remoteServerMetadata.Transport
		}

		// Set up OAuth config if provided
		if remoteServerMetadata.OAuthConfig != nil {
			runFlags.EnableRemoteAuth = true
			// Only set ClientID from registry if not provided via command line
			if runFlags.RemoteAuthClientID == "" {
				runFlags.RemoteAuthClientID = remoteServerMetadata.OAuthConfig.ClientID
			}
			runFlags.RemoteAuthIssuer = remoteServerMetadata.OAuthConfig.Issuer
			runFlags.RemoteAuthAuthorizeURL = remoteServerMetadata.OAuthConfig.AuthorizeURL
			runFlags.RemoteAuthTokenURL = remoteServerMetadata.OAuthConfig.TokenURL
			runFlags.RemoteAuthScopes = remoteServerMetadata.OAuthConfig.Scopes

			// Set OAuth parameters and callback port from registry
			if remoteServerMetadata.OAuthConfig.OAuthParams != nil {
				runFlags.OAuthParams = remoteServerMetadata.OAuthConfig.OAuthParams
			}
			if remoteServerMetadata.OAuthConfig.CallbackPort != 0 {
				runFlags.RemoteAuthCallbackPort = remoteServerMetadata.OAuthConfig.CallbackPort
			}
		}

		// Set up headers if provided
		for _, header := range remoteServerMetadata.Headers {
			if header.Secret {
				runFlags.Secrets = append(runFlags.Secrets, fmt.Sprintf("%s,target=%s", header.Name, header.Name))
			} else {
				if runFlags.EnvVars == nil {
					runFlags.EnvVars = make(map[string]string)
				}
				runFlags.EnvVars[header.Name] = header.Default
			}
		}

		// Set up environment variables if provided
		for _, envVar := range remoteServerMetadata.EnvVars {
			if envVar.Secret {
				// Only add secrets if no authentication method is provided
				hasAuth := runFlags.RemoteAuthBearerToken != "" ||
					runFlags.RemoteAuthClientID != "" ||
					remoteServerMetadata.OAuthConfig != nil
				if !hasAuth {
					runFlags.Secrets = append(runFlags.Secrets, fmt.Sprintf("%s,target=%s", envVar.Name, envVar.Name))
				}
			} else {
				if runFlags.EnvVars == nil {
					runFlags.EnvVars = make(map[string]string)
				}
				runFlags.EnvVars[envVar.Name] = envVar.Default
			}
		}
	} else if isURL(imageURL) {
		// Handle direct URL approach
		runFlags.RemoteURL = imageURL
		if transportType == "" {
			transportType = "streamable-http" // Default for direct URLs
		}
	} else {
		// Handle container server (existing logic)
		if transportType == "" {
			transportType = "streamable-http" // Default for remote servers
		}
		// Only pull image if we are not running in Kubernetes mode.
		// This split will go away if we implement a separate command or binary
		// for running MCP servers in Kubernetes.
		if !runtime.IsKubernetesRuntime() {
			// Take the MCP server we were supplied and either fetch the image, or
			// build it from a protocol scheme. If the server URI refers to an image
			// in our trusted registry, we will also fetch the image metadata.
			imageURL, imageMetadata, err = retriever.GetMCPServer(ctx, serverOrImage, runFlags.CACertPath, runFlags.VerifyImage)
			if err != nil {
				return nil, fmt.Errorf("failed to find or create the MCP server %s: %v", serverOrImage, err)
			}
		}
	}

	// Build remote auth config if enabled
	var remoteAuthConfig *runner.RemoteAuthConfig
	if runFlags.EnableRemoteAuth || runFlags.RemoteAuthClientID != "" || runFlags.RemoteAuthBearerToken != "" {
		remoteAuthConfig = &runner.RemoteAuthConfig{
			EnableRemoteAuth: runFlags.EnableRemoteAuth,
			ClientID:         runFlags.RemoteAuthClientID,
			ClientSecret:     runFlags.RemoteAuthClientSecret,
			ClientSecretFile: runFlags.RemoteAuthClientSecretFile,
			Scopes:           runFlags.RemoteAuthScopes,
			SkipBrowser:      runFlags.RemoteAuthSkipBrowser,
			Timeout:          runFlags.RemoteAuthTimeout,
			CallbackPort:     runFlags.RemoteAuthCallbackPort,
			BearerToken:      runFlags.RemoteAuthBearerToken,
			Issuer:           runFlags.RemoteAuthIssuer,
			AuthorizeURL:     runFlags.RemoteAuthAuthorizeURL,
			TokenURL:         runFlags.RemoteAuthTokenURL,
			OAuthParams:      runFlags.OAuthParams,
		}
	}

	// Validate proxy mode early
	if !types.IsValidProxyMode(runFlags.ProxyMode) {
		if runFlags.ProxyMode == "" {
			runFlags.ProxyMode = types.ProxyModeSSE.String() // default to SSE for backward compatibility
		} else {
			return nil, fmt.Errorf("invalid value for --proxy-mode: %s", runFlags.ProxyMode)
		}
	}

	// Parse the environment variables from a list of strings to a map.
	envVars, err := environment.ParseEnvironmentVariables(runFlags.Env)
	if err != nil {
		return nil, fmt.Errorf("failed to parse environment variables: %v", err)
	}

	// Initialize a new RunConfig with values from command-line flags
	return runner.NewRunConfigBuilder().
		WithRuntime(rt).
		WithCmdArgs(cmdArgs).
		WithName(runFlags.Name).
		WithImage(imageURL).
		WithRemoteURL(runFlags.RemoteURL).
		WithHost(validatedHost).
		WithTargetHost(runFlags.TargetHost).
		WithDebug(debugMode).
		WithVolumes(runFlags.Volumes).
		WithSecrets(runFlags.Secrets).
		WithAuthzConfigPath(runFlags.AuthzConfig).
		WithAuditConfigPath(runFlags.AuditConfig).
		WithPermissionProfileNameOrPath(runFlags.PermissionProfile).
		WithNetworkIsolation(runFlags.IsolateNetwork).
		WithK8sPodPatch(runFlags.K8sPodPatch).
		WithProxyMode(types.ProxyMode(runFlags.ProxyMode)).
		WithTransportAndPorts(transportType, runFlags.ProxyPort, runFlags.TargetPort).
		WithAuditEnabled(runFlags.EnableAudit, runFlags.AuditConfig).
		WithLabels(runFlags.Labels).
		WithGroup(runFlags.Group).
		WithOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL, oidcClientID, oidcClientSecret,
			runFlags.ThvCABundle, runFlags.JWKSAuthTokenFile, runFlags.ResourceURL, runFlags.JWKSAllowPrivateIP).
		WithTelemetryConfig(finalOtelEndpoint, runFlags.OtelEnablePrometheusMetricsPath, runFlags.OtelServiceName,
			finalOtelSamplingRate, runFlags.OtelHeaders, runFlags.OtelInsecure, finalOtelEnvironmentVariables).
		WithToolsFilter(runFlags.ToolsFilter).
		WithIgnoreConfig(&ignore.Config{
			LoadGlobal:    runFlags.IgnoreGlobally,
			PrintOverlays: runFlags.PrintOverlays,
		}).
		WithRemoteAuth(remoteAuthConfig).
		Build(ctx, imageMetadata, envVars, envVarValidator)
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

// isURL checks if the input is a URL
func isURL(input string) bool {
	return strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://")
}

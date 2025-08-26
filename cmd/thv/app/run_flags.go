package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/auth"
	cfg "github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport"
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

	// Environment file processing
	EnvFile    string
	EnvFileDir string

	// Ignore functionality
	IgnoreGlobally bool
	PrintOverlays  bool

	// Remote authentication
	RemoteAuthFlags RemoteAuthFlags
	OAuthParams     map[string]string
}

// GetEnableRemoteAuth returns whether remote authentication is enabled
func (r *RemoteAuthFlags) GetEnableRemoteAuth() bool { return r.EnableRemoteAuth }

// GetRemoteAuthClientID returns the remote authentication client ID
func (r *RemoteAuthFlags) GetRemoteAuthClientID() string { return r.RemoteAuthClientID }

// GetRemoteAuthClientSecret returns the remote authentication client secret
func (r *RemoteAuthFlags) GetRemoteAuthClientSecret() string { return r.RemoteAuthClientSecret }

// GetRemoteAuthClientSecretFile returns the remote authentication client secret file path
func (r *RemoteAuthFlags) GetRemoteAuthClientSecretFile() string { return r.RemoteAuthClientSecretFile }

// GetRemoteAuthScopes returns the remote authentication scopes
func (r *RemoteAuthFlags) GetRemoteAuthScopes() []string { return r.RemoteAuthScopes }

// GetRemoteAuthSkipBrowser returns whether to skip browser for remote authentication
func (r *RemoteAuthFlags) GetRemoteAuthSkipBrowser() bool { return r.RemoteAuthSkipBrowser }

// GetRemoteAuthTimeout returns the remote authentication timeout
func (r *RemoteAuthFlags) GetRemoteAuthTimeout() time.Duration { return r.RemoteAuthTimeout }

// GetRemoteAuthCallbackPort returns the remote authentication callback port
func (r *RemoteAuthFlags) GetRemoteAuthCallbackPort() int { return r.RemoteAuthCallbackPort }

// GetRemoteAuthIssuer returns the remote authentication issuer
func (r *RemoteAuthFlags) GetRemoteAuthIssuer() string { return r.RemoteAuthIssuer }

// GetRemoteAuthAuthorizeURL returns the remote authentication authorize URL
func (r *RemoteAuthFlags) GetRemoteAuthAuthorizeURL() string { return r.RemoteAuthAuthorizeURL }

// GetRemoteAuthTokenURL returns the remote authentication token URL
func (r *RemoteAuthFlags) GetRemoteAuthTokenURL() string { return r.RemoteAuthTokenURL }

// GetName returns the server name
func (r *RunFlags) GetName() string { return r.Name }

// GetGroup returns the server group
func (r *RunFlags) GetGroup() string { return r.Group }

// GetTransport returns the transport type
func (r *RunFlags) GetTransport() string { return r.Transport }

// GetProxyMode returns the proxy mode
func (r *RunFlags) GetProxyMode() string { return r.ProxyMode }

// GetHost returns the host
func (r *RunFlags) GetHost() string { return r.Host }

// GetProxyPort returns the proxy port
func (r *RunFlags) GetProxyPort() int { return r.ProxyPort }

// GetTargetPort returns the target port
func (r *RunFlags) GetTargetPort() int { return r.TargetPort }

// GetTargetHost returns the target host
func (r *RunFlags) GetTargetHost() string { return r.TargetHost }

// GetEnv returns the environment variables
func (r *RunFlags) GetEnv() []string { return r.Env }

// GetVolumes returns the volumes
func (r *RunFlags) GetVolumes() []string { return r.Volumes }

// GetSecrets returns the secrets
func (r *RunFlags) GetSecrets() []string { return r.Secrets }

// GetEnvFile returns the environment file path
func (r *RunFlags) GetEnvFile() string { return r.EnvFile }

// GetEnvFileDir returns the environment file directory
func (r *RunFlags) GetEnvFileDir() string { return r.EnvFileDir }

// GetPermissionProfile returns the permission profile
func (r *RunFlags) GetPermissionProfile() string { return r.PermissionProfile }

// GetAuthzConfig returns the authorization config
func (r *RunFlags) GetAuthzConfig() string { return r.AuthzConfig }

// GetAuditConfig returns the audit config
func (r *RunFlags) GetAuditConfig() string { return r.AuditConfig }

// GetEnableAudit returns whether audit is enabled
func (r *RunFlags) GetEnableAudit() bool { return r.EnableAudit }

// GetCACertPath returns the CA certificate path
func (r *RunFlags) GetCACertPath() string { return r.CACertPath }

// GetVerifyImage returns the image verification setting
func (r *RunFlags) GetVerifyImage() string { return r.VerifyImage }

// GetRemoteURL returns the remote URL
func (r *RunFlags) GetRemoteURL() string { return r.RemoteURL }

// GetRemoteAuthFlags returns the remote authentication flags
func (r *RunFlags) GetRemoteAuthFlags() runner.RemoteAuthFlagsInterface { return &r.RemoteAuthFlags }

// GetOAuthParams returns the OAuth parameters
func (r *RunFlags) GetOAuthParams() map[string]string { return r.OAuthParams }

// GetIsolateNetwork returns whether network isolation is enabled
func (r *RunFlags) GetIsolateNetwork() bool { return r.IsolateNetwork }

// GetLabels returns the labels
func (r *RunFlags) GetLabels() []string { return r.Labels }

// GetToolsFilter returns the tools filter
func (r *RunFlags) GetToolsFilter() []string { return r.ToolsFilter }

// GetForeground returns whether to run in foreground
func (r *RunFlags) GetForeground() bool { return r.Foreground }

// GetThvCABundle returns the THV CA bundle
func (r *RunFlags) GetThvCABundle() string { return r.ThvCABundle }

// GetJWKSAuthTokenFile returns the JWKS auth token file
func (r *RunFlags) GetJWKSAuthTokenFile() string { return r.JWKSAuthTokenFile }

// GetJWKSAllowPrivateIP returns whether to allow private IP for JWKS
func (r *RunFlags) GetJWKSAllowPrivateIP() bool { return r.JWKSAllowPrivateIP }

// GetResourceURL returns the resource URL
func (r *RunFlags) GetResourceURL() string { return r.ResourceURL }

// GetOtelEndpoint returns the OpenTelemetry endpoint
func (r *RunFlags) GetOtelEndpoint() string { return r.OtelEndpoint }

// GetOtelServiceName returns the OpenTelemetry service name
func (r *RunFlags) GetOtelServiceName() string { return r.OtelServiceName }

// GetOtelSamplingRate returns the OpenTelemetry sampling rate
func (r *RunFlags) GetOtelSamplingRate() float64 { return r.OtelSamplingRate }

// GetOtelHeaders returns the OpenTelemetry headers
func (r *RunFlags) GetOtelHeaders() []string { return r.OtelHeaders }

// GetOtelInsecure returns whether OpenTelemetry is insecure
func (r *RunFlags) GetOtelInsecure() bool { return r.OtelInsecure }

// GetOtelEnablePrometheusMetricsPath returns whether Prometheus metrics path is enabled
func (r *RunFlags) GetOtelEnablePrometheusMetricsPath() bool {
	return r.OtelEnablePrometheusMetricsPath
}

// GetOtelEnvironmentVariables returns the OpenTelemetry environment variables
func (r *RunFlags) GetOtelEnvironmentVariables() []string { return r.OtelEnvironmentVariables }

// GetK8sPodPatch returns the Kubernetes pod patch
func (r *RunFlags) GetK8sPodPatch() string { return r.K8sPodPatch }

// GetIgnoreGlobally returns whether to ignore globally
func (r *RunFlags) GetIgnoreGlobally() bool { return r.IgnoreGlobally }

// GetPrintOverlays returns whether to print overlays
func (r *RunFlags) GetPrintOverlays() bool { return r.PrintOverlays }

// AddRunFlags adds all the run flags to a command
func AddRunFlags(cmd *cobra.Command, config *RunFlags) {
	cmd.Flags().StringVar(&config.Transport, "transport", "", "Transport mode (sse, streamable-http or stdio)")
	cmd.Flags().StringVar(&config.ProxyMode, "proxy-mode", "sse", "Proxy mode for stdio transport (sse or streamable-http)")
	cmd.Flags().StringVar(&config.Name, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	cmd.Flags().StringVar(&config.Group, "group", "default",
		"Name of the group this workload belongs to (defaults to 'default' if not specified)")
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
	AddRemoteAuthFlags(cmd, &config.RemoteAuthFlags)

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

	// Environment file processing flags
	cmd.Flags().StringVar(&config.EnvFile, "env-file", "", "Load environment variables from a single file")
	cmd.Flags().StringVar(&config.EnvFileDir, "env-file-dir", "", "Load environment variables from all files in a directory")

	// Ignore functionality flags
	cmd.Flags().BoolVar(&config.IgnoreGlobally, "ignore-globally", true,
		"Load global ignore patterns from ~/.config/toolhive/thvignore")
	cmd.Flags().BoolVar(&config.PrintOverlays, "print-resolved-overlays", false,
		"Debug: show resolved container paths for tmpfs overlays")
}

// BuildRunnerConfig creates a runner.RunConfig from the configuration using the new PreRunConfig approach
func BuildRunnerConfig(
	ctx context.Context,
	runFlags *RunFlags,
	serverOrImage string,
	cmdArgs []string,
	debugMode bool,
	cmd *cobra.Command,
) (*runner.RunConfig, error) {
	// Validate and setup basic configuration
	validatedHost, err := ValidateAndNormaliseHostFlag(runFlags.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid host: %s", runFlags.Host)
	}

	// Setup OIDC configuration
	oidcConfig, err := setupOIDCConfiguration(cmd, runFlags)
	if err != nil {
		return nil, err
	}

	// Setup telemetry configuration
	telemetryConfig := setupTelemetryConfiguration(cmd, runFlags)

	// Parse and classify the input using PreRunConfig
	preConfig, err := runner.ParsePreRunConfig(serverOrImage, runFlags.FromConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse run configuration: %w", err)
	}

	logger.Debugf("Parsed PreRunConfig: %s", preConfig.String())

	// Create the transformer
	transformer, err := runner.NewPreRunTransformer(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create transformer: %w", err)
	}

	// Transform PreRunConfig to RunConfig
	runConfig, err := transformer.TransformToRunConfig(
		preConfig,
		runFlags, // runFlags implements RunFlagsInterface
		cmdArgs,
		debugMode,
		validatedHost,
		oidcConfig,
		telemetryConfig,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to transform to run config: %w", err)
	}

	return runConfig, nil
}

// setupOIDCConfiguration sets up OIDC configuration and validates URLs
func setupOIDCConfiguration(cmd *cobra.Command, runFlags *RunFlags) (*auth.TokenValidatorConfig, error) {
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

	return createOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL,
		oidcClientID, oidcClientSecret, runFlags.ResourceURL, runFlags.JWKSAllowPrivateIP), nil
}

// setupTelemetryConfiguration sets up telemetry configuration with config fallbacks
func setupTelemetryConfiguration(cmd *cobra.Command, runFlags *RunFlags) *telemetry.Config {
	config := cfg.GetConfig()
	finalOtelEndpoint, finalOtelSamplingRate, finalOtelEnvironmentVariables := getTelemetryFromFlags(cmd, config,
		runFlags.OtelEndpoint, runFlags.OtelSamplingRate, runFlags.OtelEnvironmentVariables)

	return createTelemetryConfig(finalOtelEndpoint, runFlags.OtelEnablePrometheusMetricsPath,
		runFlags.OtelServiceName, finalOtelSamplingRate, runFlags.OtelHeaders, runFlags.OtelInsecure,
		finalOtelEnvironmentVariables)
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

// createOIDCConfig creates an OIDC configuration if any OIDC parameters are provided
func createOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcIntrospectionURL,
	oidcClientID, oidcClientSecret, resourceURL string, allowPrivateIP bool) *auth.TokenValidatorConfig {
	if oidcIssuer != "" || oidcAudience != "" || oidcJwksURL != "" || oidcIntrospectionURL != "" ||
		oidcClientID != "" || oidcClientSecret != "" || resourceURL != "" {
		return &auth.TokenValidatorConfig{
			Issuer:           oidcIssuer,
			Audience:         oidcAudience,
			JWKSURL:          oidcJwksURL,
			IntrospectionURL: oidcIntrospectionURL,
			ClientID:         oidcClientID,
			ClientSecret:     oidcClientSecret,
			ResourceURL:      resourceURL,
			AllowPrivateIP:   allowPrivateIP,
		}
	}
	return nil
}

// createTelemetryConfig creates a telemetry configuration if any telemetry parameters are provided
func createTelemetryConfig(otelEndpoint string, otelEnablePrometheusMetricsPath bool,
	otelServiceName string, otelSamplingRate float64, otelHeaders []string,
	otelInsecure bool, otelEnvironmentVariables []string) *telemetry.Config {
	if otelEndpoint == "" && !otelEnablePrometheusMetricsPath {
		return nil
	}

	// Parse headers from key=value format
	headers := make(map[string]string)
	for _, header := range otelHeaders {
		parts := strings.SplitN(header, "=", 2)
		if len(parts) == 2 {
			headers[parts[0]] = parts[1]
		}
	}

	// Use provided service name or default
	serviceName := otelServiceName
	if serviceName == "" {
		serviceName = telemetry.DefaultConfig().ServiceName
	}

	// Process environment variables - split comma-separated values
	var processedEnvVars []string
	for _, envVarEntry := range otelEnvironmentVariables {
		// Split by comma and trim whitespace
		envVars := strings.Split(envVarEntry, ",")
		for _, envVar := range envVars {
			trimmed := strings.TrimSpace(envVar)
			if trimmed != "" {
				processedEnvVars = append(processedEnvVars, trimmed)
			}
		}
	}

	return &telemetry.Config{
		Endpoint:                    otelEndpoint,
		ServiceName:                 serviceName,
		ServiceVersion:              telemetry.DefaultConfig().ServiceVersion,
		SamplingRate:                otelSamplingRate,
		Headers:                     headers,
		Insecure:                    otelInsecure,
		EnablePrometheusMetricsPath: otelEnablePrometheusMetricsPath,
		EnvironmentVariables:        processedEnvVars,
	}
}

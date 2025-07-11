package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// RunConfigBuilder provides a fluent interface for building RunConfig instances
type RunConfigBuilder struct {
	config *RunConfig
	// Store transport string separately to avoid type confusion
	transportString string
	// Store ports separately for proper validation
	port       int
	targetPort int
}

// NewRunConfigBuilder creates a new RunConfigBuilder with default values
func NewRunConfigBuilder() *RunConfigBuilder {
	return &RunConfigBuilder{
		config: &RunConfig{
			ContainerLabels: make(map[string]string),
			EnvVars:         make(map[string]string),
		},
	}
}

// WithRuntime sets the container runtime
func (b *RunConfigBuilder) WithRuntime(runtime rt.Runtime) *RunConfigBuilder {
	b.config.Runtime = runtime
	return b
}

// WithImage sets the Docker image
func (b *RunConfigBuilder) WithImage(image string) *RunConfigBuilder {
	b.config.Image = image
	return b
}

// WithName sets the MCP server name
func (b *RunConfigBuilder) WithName(name string) *RunConfigBuilder {
	b.config.Name = name
	return b
}

// WithCmdArgs sets the command arguments
func (b *RunConfigBuilder) WithCmdArgs(args []string) *RunConfigBuilder {
	b.config.CmdArgs = args
	return b
}

// WithHost sets the host (applies default if empty)
func (b *RunConfigBuilder) WithHost(host string) *RunConfigBuilder {
	if host == "" {
		host = transport.LocalhostIPv4
	}
	b.config.Host = host
	return b
}

// WithTargetHost sets the target host (applies default if empty)
func (b *RunConfigBuilder) WithTargetHost(targetHost string) *RunConfigBuilder {
	if targetHost == "" {
		targetHost = transport.LocalhostIPv4
	}
	b.config.TargetHost = targetHost
	return b
}

// WithDebug sets debug mode
func (b *RunConfigBuilder) WithDebug(debug bool) *RunConfigBuilder {
	b.config.Debug = debug
	return b
}

// WithVolumes sets the volume mounts
func (b *RunConfigBuilder) WithVolumes(volumes []string) *RunConfigBuilder {
	b.config.Volumes = volumes
	return b
}

// WithSecrets sets the secrets list
func (b *RunConfigBuilder) WithSecrets(secrets []string) *RunConfigBuilder {
	b.config.Secrets = secrets
	return b
}

// WithAuthzConfigPath sets the authorization config path
func (b *RunConfigBuilder) WithAuthzConfigPath(path string) *RunConfigBuilder {
	b.config.AuthzConfigPath = path
	return b
}

// WithAuditConfigPath sets the audit config path
func (b *RunConfigBuilder) WithAuditConfigPath(path string) *RunConfigBuilder {
	b.config.AuditConfigPath = path
	return b
}

// WithPermissionProfileNameOrPath sets the permission profile name or path.
// If called multiple times or mixed with WithPermissionProfile,
// the last call takes precedence.
func (b *RunConfigBuilder) WithPermissionProfileNameOrPath(profile string) *RunConfigBuilder {
	b.config.PermissionProfileNameOrPath = profile
	b.config.PermissionProfile = nil // Clear any existing profile
	return b
}

// WithPermissionProfile sets the permission profile directly.
// If called multiple times or mixed with WithPermissionProfile,
// the last call takes precedence.
func (b *RunConfigBuilder) WithPermissionProfile(profile *permissions.Profile) *RunConfigBuilder {
	b.config.PermissionProfile = profile
	b.config.PermissionProfileNameOrPath = "" // Clear any existing name or path
	return b
}

// WithNetworkIsolation sets network isolation
func (b *RunConfigBuilder) WithNetworkIsolation(isolate bool) *RunConfigBuilder {
	b.config.IsolateNetwork = isolate
	return b
}

// WithK8sPodPatch sets the Kubernetes pod template patch
func (b *RunConfigBuilder) WithK8sPodPatch(patch string) *RunConfigBuilder {
	b.config.K8sPodTemplatePatch = patch
	return b
}

// WithProxyMode sets the proxy mode
func (b *RunConfigBuilder) WithProxyMode(mode types.ProxyMode) *RunConfigBuilder {
	b.config.ProxyMode = mode
	return b
}

// WithTransportAndPorts sets transport and port configuration
func (b *RunConfigBuilder) WithTransportAndPorts(mcpTransport string, port, targetPort int) *RunConfigBuilder {
	b.transportString = mcpTransport
	b.port = port
	b.targetPort = targetPort
	return b
}

// WithAuditEnabled configures audit settings
func (b *RunConfigBuilder) WithAuditEnabled(enableAudit bool, auditConfigPath string) *RunConfigBuilder {
	if enableAudit && auditConfigPath == "" {
		b.config.AuditConfig = audit.DefaultConfig()
	}
	return b
}

// WithOIDCConfig configures OIDC settings
func (b *RunConfigBuilder) WithOIDCConfig(
	oidcIssuer, oidcAudience, oidcJwksURL, oidcClientID string,
	oidcAllowOpaqueTokens bool,
	thvCABundle, jwksAuthTokenFile string,
	jwksAllowPrivateIP bool,
) *RunConfigBuilder {
	if oidcIssuer != "" || oidcAudience != "" || oidcJwksURL != "" || oidcClientID != "" {
		b.config.OIDCConfig = &auth.TokenValidatorConfig{
			Issuer:            oidcIssuer,
			Audience:          oidcAudience,
			JWKSURL:           oidcJwksURL,
			ClientID:          oidcClientID,
			AllowOpaqueTokens: oidcAllowOpaqueTokens,
		}
	}
	// Set JWKS-related configuration
	b.config.ThvCABundle = thvCABundle
	b.config.JWKSAuthTokenFile = jwksAuthTokenFile
	b.config.JWKSAllowPrivateIP = jwksAllowPrivateIP
	return b
}

// WithTelemetryConfig configures telemetry settings
func (b *RunConfigBuilder) WithTelemetryConfig(otelEndpoint string, otelEnablePrometheusMetricsPath bool,
	otelServiceName string, otelSamplingRate float64, otelHeaders []string, otelInsecure bool,
	otelEnvironmentVariables []string) *RunConfigBuilder {

	if otelEndpoint == "" && !otelEnablePrometheusMetricsPath {
		return b
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

	b.config.TelemetryConfig = &telemetry.Config{
		Endpoint:                    otelEndpoint,
		ServiceName:                 serviceName,
		ServiceVersion:              telemetry.DefaultConfig().ServiceVersion,
		SamplingRate:                otelSamplingRate,
		Headers:                     headers,
		Insecure:                    otelInsecure,
		EnablePrometheusMetricsPath: otelEnablePrometheusMetricsPath,
		EnvironmentVariables:        processedEnvVars,
	}
	return b
}

// WithTools sets the list of tools to enable for this MCP server
func (b *RunConfigBuilder) WithTools(tools []string) *RunConfigBuilder {
	b.config.Tools = tools
	return b
}

// Build creates the final RunConfig instance with validation and processing
func (b *RunConfigBuilder) Build(ctx context.Context, imageMetadata *registry.ImageMetadata,
	envVars []string, envVarValidator EnvVarValidator) (*RunConfig, error) {
	// When using the CLI validation strategy, this is where the prompting for
	// missing environment variables will happen.
	processedEnvVars, err := envVarValidator.Validate(ctx, imageMetadata, b.config, envVars)
	if err != nil {
		return nil, fmt.Errorf("failed to validate environment variables: %v", err)
	}

	// Do some final validation which can only be done after everything else is set.
	// Apply image metadata overrides if needed.
	if err := b.validateConfig(imageMetadata); err != nil {
		return nil, fmt.Errorf("failed to validate run config: %v", err)
	}

	// Now set environment variables with the correct transport and ports resolved
	if _, err := b.config.WithEnvironmentVariables(processedEnvVars); err != nil {
		return nil, fmt.Errorf("failed to set environment variables: %v", err)
	}

	return b.config, nil
}

// validateConfig ensures the RunConfig is valid and sets up some of the final
// configuration details which can only be applied after all other flags are added.
// This function also handles setting missing values based on the image metadata (if present).
//
//nolint:gocyclo // This function needs to be refactored to reduce cyclomatic complexity.
func (b *RunConfigBuilder) validateConfig(imageMetadata *registry.ImageMetadata) error {
	c := b.config
	var err error

	// The old logic claimed to override the name with the name from the registry
	// but didn't. Instead, it used the name passed in from the CLI.
	// See: https://github.com/stacklok/toolhive/blob/2873152b62bf61698cbcdd0aba1707a046151e67/cmd/thv/app/run.go#L425
	// The following code implements what I believe was the intended behavior:
	if c.Name == "" && imageMetadata != nil {
		c.Name = imageMetadata.Name
	}

	// Check to see if the mcpTransport is defined in the metadata.
	// Use this value if it was not set by the user.
	// Else, default to stdio.
	mcpTransport := b.transportString
	if mcpTransport == "" {
		if imageMetadata != nil && imageMetadata.Transport != "" {
			logger.Debugf("Using registry mcpTransport: %s", imageMetadata.Transport)
			mcpTransport = imageMetadata.Transport
		} else {
			logger.Debugf("Defaulting mcpTransport to stdio")
			mcpTransport = types.TransportTypeStdio.String()
		}
	}
	// Set mcpTransport
	if _, err = c.WithTransport(mcpTransport); err != nil {
		return err
	}

	// Use registry target port if not overridden and if the mcpTransport is HTTP-based.
	targetPort := b.targetPort
	if imageMetadata != nil {
		isHTTPServer := mcpTransport == types.TransportTypeSSE.String() ||
			mcpTransport == types.TransportTypeStreamableHTTP.String()
		if targetPort == 0 && isHTTPServer && imageMetadata.TargetPort > 0 {
			logger.Debugf("Using registry target port: %d", imageMetadata.TargetPort)
			targetPort = imageMetadata.TargetPort
		}
	}
	// Configure ports and target host
	if _, err = c.WithPorts(b.port, targetPort); err != nil {
		return err
	}

	// If we are missing the permission profile, attempt to load one from the image metadata.
	if c.PermissionProfileNameOrPath == "" && c.PermissionProfile == nil && imageMetadata != nil {
		permProfilePath, err := CreatePermissionProfileFile(c.Name, imageMetadata.Permissions)
		if err != nil {
			// Just log the error and continue with the default permission profile
			logger.Warnf("Warning: failed to create permission profile file: %v", err)
		} else {
			// Update the permission profile path
			c.PermissionProfileNameOrPath = permProfilePath
		}
	}
	// Set permission profile (mandatory)
	if _, err = c.ParsePermissionProfile(); err != nil {
		return err
	}

	// Process volume mounts
	if err = c.ProcessVolumeMounts(); err != nil {
		return err
	}

	// Generate container name if not already set
	c.WithContainerName()

	// Add standard labels
	c.WithStandardLabels()

	// Add authorization configuration if provided
	if c.AuthzConfigPath != "" {
		authzConfig, err := authz.LoadConfig(c.AuthzConfigPath)
		if err != nil {
			return fmt.Errorf("failed to load authorization configuration: %v", err)
		}
		c.WithAuthz(authzConfig)
	}

	// Add audit configuration if provided
	if c.AuditConfigPath != "" {
		auditConfig, err := audit.LoadFromFile(c.AuditConfigPath)
		if err != nil {
			return fmt.Errorf("failed to load audit configuration: %v", err)
		}
		c.WithAudit(auditConfig)
	}
	// Note: AuditConfig is already set from --enable-audit flag if provided

	// Prepend registry args to command-line args if available
	if imageMetadata != nil && len(imageMetadata.Args) > 0 {
		logger.Debugf("Prepending registry args: %v", imageMetadata.Args)
		c.CmdArgs = append(c.CmdArgs, imageMetadata.Args...)
	}

	return nil
}

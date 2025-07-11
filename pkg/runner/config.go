// Package runner provides functionality for running MCP servers
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// RunConfig contains all the configuration needed to run an MCP server
// It is serializable to JSON and YAML
type RunConfig struct {
	// Image is the Docker image to run
	Image string `json:"image" yaml:"image"`

	// CmdArgs are the arguments to pass to the container
	CmdArgs []string `json:"cmd_args,omitempty" yaml:"cmd_args,omitempty"`

	// Name is the name of the MCP server
	Name string `json:"name" yaml:"name"`

	// ContainerName is the name of the container
	ContainerName string `json:"container_name" yaml:"container_name"`

	// BaseName is the base name used for the container (without prefixes)
	BaseName string `json:"base_name" yaml:"base_name"`

	// Transport is the transport mode (stdio, sse, or streamable-http)
	Transport types.TransportType `json:"transport" yaml:"transport"`

	// Host is the host for the HTTP proxy
	Host string `json:"host" yaml:"host"`

	// Port is the port for the HTTP proxy to listen on (host port)
	Port int `json:"port" yaml:"port"`

	// TargetPort is the port for the container to expose (only applicable to SSE transport)
	TargetPort int `json:"target_port,omitempty" yaml:"target_port,omitempty"`

	// TargetHost is the host to forward traffic to (only applicable to SSE transport)
	TargetHost string `json:"target_host,omitempty" yaml:"target_host,omitempty"`

	// PermissionProfileNameOrPath is the name or path of the permission profile
	PermissionProfileNameOrPath string `json:"permission_profile_name_or_path,omitempty" yaml:"permission_profile_name_or_path,omitempty"` //nolint:lll

	// PermissionProfile is the permission profile to use
	PermissionProfile *permissions.Profile `json:"permission_profile" yaml:"permission_profile"`

	// EnvVars are the parsed environment variables as key-value pairs
	EnvVars map[string]string `json:"env_vars,omitempty" yaml:"env_vars,omitempty"`

	// Debug indicates whether debug mode is enabled
	Debug bool `json:"debug,omitempty" yaml:"debug,omitempty"`

	// Volumes are the directory mounts to pass to the container
	// Format: "host-path:container-path[:ro]"
	Volumes []string `json:"volumes,omitempty" yaml:"volumes,omitempty"`

	// ContainerLabels are the labels to apply to the container
	ContainerLabels map[string]string `json:"container_labels,omitempty" yaml:"container_labels,omitempty"`

	// OIDCConfig contains OIDC configuration
	OIDCConfig *auth.TokenValidatorConfig `json:"oidc_config,omitempty" yaml:"oidc_config,omitempty"`

	// AuthzConfig contains the authorization configuration
	AuthzConfig *authz.Config `json:"authz_config,omitempty" yaml:"authz_config,omitempty"`

	// AuthzConfigPath is the path to the authorization configuration file
	AuthzConfigPath string `json:"authz_config_path,omitempty" yaml:"authz_config_path,omitempty"`

	// AuditConfig contains the audit logging configuration
	AuditConfig *audit.Config `json:"audit_config,omitempty" yaml:"audit_config,omitempty"`

	// AuditConfigPath is the path to the audit configuration file
	AuditConfigPath string `json:"audit_config_path,omitempty" yaml:"audit_config_path,omitempty"`

	// TelemetryConfig contains the OpenTelemetry configuration
	TelemetryConfig *telemetry.Config `json:"telemetry_config,omitempty" yaml:"telemetry_config,omitempty"`

	// Secrets are the secret parameters to pass to the container
	// Format: "<secret name>,target=<target environment variable>"
	Secrets []string `json:"secrets,omitempty" yaml:"secrets,omitempty"`

	// K8sPodTemplatePatch is a JSON string to patch the Kubernetes pod template
	// Only applicable when using Kubernetes runtime
	K8sPodTemplatePatch string `json:"k8s_pod_template_patch,omitempty" yaml:"k8s_pod_template_patch,omitempty"`

	// Runtime is the container runtime to use (not serialized)
	Runtime rt.Runtime `json:"-" yaml:"-"`

	// IsolateNetwork indicates whether to isolate the network for the container
	IsolateNetwork bool `json:"isolate_network,omitempty" yaml:"isolate_network,omitempty"`

	// ProxyMode is the proxy mode for stdio transport ("sse" or "streamable-http")
	ProxyMode types.ProxyMode `json:"proxy_mode,omitempty" yaml:"proxy_mode,omitempty"`

	// ThvCABundle is the path to the CA certificate bundle for ToolHive HTTP operations
	ThvCABundle string `json:"thv_ca_bundle,omitempty" yaml:"thv_ca_bundle,omitempty"`

	// JWKSAuthTokenFile is the path to file containing auth token for JWKS/OIDC requests
	JWKSAuthTokenFile string `json:"jwks_auth_token_file,omitempty" yaml:"jwks_auth_token_file,omitempty"`

	// JWKSAllowPrivateIP allows JWKS/OIDC endpoints on private IP addresses
	JWKSAllowPrivateIP bool `json:"jwks_allow_private_ip,omitempty" yaml:"jwks_allow_private_ip,omitempty"`
}

// WriteJSON serializes the RunConfig to JSON and writes it to the provided writer
func (c *RunConfig) WriteJSON(w io.Writer) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(c)
}

// ReadJSON deserializes the RunConfig from JSON read from the provided reader
func ReadJSON(r io.Reader) (*RunConfig, error) {
	var config RunConfig
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

// NewRunConfig creates a new RunConfig with default values
func NewRunConfig() *RunConfig {
	return &RunConfig{
		ContainerLabels: make(map[string]string),
		EnvVars:         make(map[string]string),
	}
}

// NewRunConfigFromFlags creates a new RunConfig with values from command-line flags
func NewRunConfigFromFlags(
	ctx context.Context,
	runtime rt.Runtime,
	cmdArgs []string,
	name string,
	imageURL string,
	imageMetadata *registry.ImageMetadata,
	host string,
	debug bool,
	volumes []string,
	secretsList []string,
	authzConfigPath string,
	auditConfigPath string,
	enableAudit bool,
	permissionProfile string,
	targetHost string,
	mcpTransport string,
	port int,
	targetPort int,
	envVars []string,
	oidcIssuer string,
	oidcAudience string,
	oidcJwksURL string,
	oidcClientID string,
	oidcAllowOpaqueTokens bool,
	otelEndpoint string,
	otelServiceName string,
	otelSamplingRate float64,
	otelHeaders []string,
	otelInsecure bool,
	otelEnablePrometheusMetricsPath bool,
	otelEnvironmentVariables []string,
	isolateNetwork bool,
	k8sPodPatch string,
	thvCABundle string,
	jwksAuthTokenFile string,
	jwksAllowPrivateIP bool,
	envVarValidator EnvVarValidator,
	proxyMode types.ProxyMode,
) (*RunConfig, error) {
	return NewRunConfigBuilder().
		WithRuntime(runtime).
		WithCmdArgs(cmdArgs).
		WithName(name).
		WithImage(imageURL).
		WithHost(host).
		WithTargetHost(targetHost).
		WithDebug(debug).
		WithVolumes(volumes).
		WithSecrets(secretsList).
		WithAuthzConfigPath(authzConfigPath).
		WithAuditConfigPath(auditConfigPath).
		WithPermissionProfileNameOrPath(permissionProfile).
		WithNetworkIsolation(isolateNetwork).
		WithK8sPodPatch(k8sPodPatch).
		WithProxyMode(proxyMode).
		WithTransportAndPorts(mcpTransport, port, targetPort).
		WithAuditEnabled(enableAudit, auditConfigPath).
		WithOIDCConfig(oidcIssuer, oidcAudience, oidcJwksURL, oidcClientID, oidcAllowOpaqueTokens,
			thvCABundle, jwksAuthTokenFile, jwksAllowPrivateIP).
		WithTelemetryConfig(otelEndpoint, otelEnablePrometheusMetricsPath, otelServiceName,
			otelSamplingRate, otelHeaders, otelInsecure, otelEnvironmentVariables).
		Build(ctx, imageMetadata, envVars, envVarValidator)
}

// WithAuthz adds authorization configuration to the RunConfig
func (c *RunConfig) WithAuthz(config *authz.Config) *RunConfig {
	c.AuthzConfig = config
	return c
}

// WithAudit adds audit configuration to the RunConfig
func (c *RunConfig) WithAudit(config *audit.Config) *RunConfig {
	c.AuditConfig = config
	return c
}

// WithTransport parses and sets the transport type
func (c *RunConfig) WithTransport(t string) (*RunConfig, error) {
	transportType, err := types.ParseTransportType(t)
	if err != nil {
		return c, fmt.Errorf("invalid transport mode: %s. Valid modes are: sse, streamable-http, stdio", t)
	}
	c.Transport = transportType
	return c, nil
}

// WithPorts configures the host and target ports
func (c *RunConfig) WithPorts(port, targetPort int) (*RunConfig, error) {
	// Select a port for the HTTP proxy (host port)
	selectedPort, err := networking.FindOrUsePort(port)
	if err != nil {
		return c, err
	}
	c.Port = selectedPort

	// Select a target port for the container if using SSE or Streamable HTTP transport
	if c.Transport == types.TransportTypeSSE || c.Transport == types.TransportTypeStreamableHTTP {
		selectedTargetPort, err := networking.FindOrUsePort(targetPort)
		if err != nil {
			return c, fmt.Errorf("target port error: %w", err)
		}
		logger.Infof("Using target port: %d", selectedTargetPort)
		c.TargetPort = selectedTargetPort
	}

	return c, nil
}

// WithEnvironmentVariables parses and sets environment variables
func (c *RunConfig) WithEnvironmentVariables(envVarStrings []string) (*RunConfig, error) {
	envVars, err := environment.ParseEnvironmentVariables(envVarStrings)
	if err != nil {
		return c, fmt.Errorf("failed to parse environment variables: %v", err)
	}

	// Initialize EnvVars if it's nil
	if c.EnvVars == nil {
		c.EnvVars = make(map[string]string)
	}

	// Merge the parsed environment variables with existing ones
	for key, value := range envVars {
		c.EnvVars[key] = value
	}

	// Set transport-specific environment variables
	environment.SetTransportEnvironmentVariables(c.EnvVars, string(c.Transport), c.TargetPort)
	return c, nil
}

// WithSecrets processes secrets and adds them to environment variables
func (c *RunConfig) WithSecrets(ctx context.Context, secretManager secrets.Provider) (*RunConfig, error) {
	if len(c.Secrets) == 0 {
		return c, nil // No secrets to process
	}

	secretVariables, err := environment.ParseSecretParameters(ctx, c.Secrets, secretManager)
	if err != nil {
		return c, fmt.Errorf("failed to get secrets: %v", err)
	}

	// Initialize EnvVars if it's nil
	if c.EnvVars == nil {
		c.EnvVars = make(map[string]string)
	}

	// Add secret variables to environment variables
	for key, value := range secretVariables {
		c.EnvVars[key] = value
	}

	return c, nil
}

// WithContainerName generates container name if not already set
func (c *RunConfig) WithContainerName() *RunConfig {
	if c.ContainerName == "" && c.Image != "" {
		containerName, baseName := container.GetOrGenerateContainerName(c.Name, c.Image)
		c.ContainerName = containerName
		c.BaseName = baseName
	}
	return c
}

// WithStandardLabels adds standard labels to the container
func (c *RunConfig) WithStandardLabels() *RunConfig {
	if c.ContainerLabels == nil {
		c.ContainerLabels = make(map[string]string)
	}
	// Use Name if ContainerName is not set
	containerName := c.ContainerName
	if containerName == "" {
		containerName = c.Name
	}

	transportLabel := c.Transport.String()
	if c.Transport == types.TransportTypeStdio && c.ProxyMode == types.ProxyModeStreamableHTTP {
		transportLabel = types.TransportTypeStreamableHTTP.String()
	}
	labels.AddStandardLabels(c.ContainerLabels, containerName, c.BaseName, transportLabel, c.Port)
	return c
}

// Package runner provides functionality for running MCP servers
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/state"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// CurrentSchemaVersion is the current version of the RunConfig schema
// TODO: Set to "v1.0.0" when we clean up the middleware configuration.
const CurrentSchemaVersion = "v0.1.0"

// RunConfig contains all the configuration needed to run an MCP server
// It is serializable to JSON and YAML
// NOTE: This format is importable and exportable, and as a result should be
// considered part of ToolHive's API contract.
type RunConfig struct {
	// SchemaVersion is the version of the RunConfig schema
	SchemaVersion string `json:"schema_version" yaml:"schema_version"`

	// Image is the Docker image to run
	Image string `json:"image" yaml:"image"`

	// RemoteURL is the URL of the remote MCP server (if running remotely)
	RemoteURL string `json:"remote_url,omitempty" yaml:"remote_url,omitempty"`

	// RemoteAuthConfig contains OAuth configuration for remote MCP servers
	RemoteAuthConfig *RemoteAuthConfig `json:"remote_auth_config,omitempty" yaml:"remote_auth_config,omitempty"`

	// CmdArgs are the arguments to pass to the container
	CmdArgs []string `json:"cmd_args,omitempty" yaml:"cmd_args,omitempty"`

	// Name is the name of the MCP server
	Name string `json:"name" yaml:"name"`

	// ContainerName is the name of the container
	ContainerName string `json:"container_name,omitempty" yaml:"container_name,omitempty"`

	// BaseName is the base name used for the container (without prefixes)
	BaseName string `json:"base_name,omitempty" yaml:"base_name,omitempty"`

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

	// Deployer is the container runtime to use (not serialized)
	Deployer rt.Deployer `json:"-" yaml:"-"`

	// IsolateNetwork indicates whether to isolate the network for the container
	IsolateNetwork bool `json:"isolate_network,omitempty" yaml:"isolate_network,omitempty"`

	// ProxyMode is the proxy mode for stdio transport ("sse" or "streamable-http")
	ProxyMode types.ProxyMode `json:"proxy_mode,omitempty" yaml:"proxy_mode,omitempty"`

	// ThvCABundle is the path to the CA certificate bundle for ToolHive HTTP operations
	ThvCABundle string `json:"thv_ca_bundle,omitempty" yaml:"thv_ca_bundle,omitempty"`

	// JWKSAuthTokenFile is the path to file containing auth token for JWKS/OIDC requests
	JWKSAuthTokenFile string `json:"jwks_auth_token_file,omitempty" yaml:"jwks_auth_token_file,omitempty"`

	// Group is the name of the group this workload belongs to, if any
	Group string `json:"group,omitempty" yaml:"group,omitempty"`

	// ToolsFilter is the list of tools to filter
	ToolsFilter []string `json:"tools_filter,omitempty" yaml:"tools_filter,omitempty"`

	// IgnoreConfig contains configuration for ignore processing
	IgnoreConfig *ignore.Config `json:"ignore_config,omitempty" yaml:"ignore_config,omitempty"`

	// MiddlewareConfigs contains the list of middleware to apply to the transport
	// and the configuration for each middleware.
	MiddlewareConfigs []types.MiddlewareConfig `json:"middleware_configs,omitempty" yaml:"middleware_configs,omitempty"`
}

// WriteJSON serializes the RunConfig to JSON and writes it to the provided writer
func (c *RunConfig) WriteJSON(w io.Writer) error {
	// Ensure the schema version is set
	if c.SchemaVersion == "" {
		c.SchemaVersion = CurrentSchemaVersion
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(c)
}

// ReadJSON deserializes the RunConfig from JSON read from the provided reader
func ReadJSON(r io.Reader) (*RunConfig, error) {
	var config RunConfig
	if err := state.ReadJSON(r, &config); err != nil {
		return nil, err
	}

	// Initialize maps if they're nil after deserialization
	if config.EnvVars == nil {
		config.EnvVars = make(map[string]string)
	}
	if config.ContainerLabels == nil {
		config.ContainerLabels = make(map[string]string)
	}

	// Initialize slices if they're nil after deserialization
	if config.CmdArgs == nil {
		config.CmdArgs = []string{}
	}
	if config.Volumes == nil {
		config.Volumes = []string{}
	}
	if config.Secrets == nil {
		config.Secrets = []string{}
	}

	// Set the default schema version if not set
	if config.SchemaVersion == "" {
		config.SchemaVersion = CurrentSchemaVersion
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

// WithMiddlewareConfig adds middleware configuration to the RunConfig
func (c *RunConfig) WithMiddlewareConfig(middlewareConfig []types.MiddlewareConfig) *RunConfig {
	c.MiddlewareConfigs = middlewareConfig
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
func (c *RunConfig) WithPorts(proxyPort, targetPort int) (*RunConfig, error) {
	var selectedPort int
	var err error

	// If the user requested an explicit proxy port, check if it's available.
	// If not available - treat as an error, since picking a random port here
	// is going to lead to confusion.
	if proxyPort != 0 {
		if !networking.IsAvailable(proxyPort) {
			return c, fmt.Errorf("requested proxy port %d is not available", proxyPort)
		}
		logger.Debugf("Using requested port: %d", proxyPort)
		selectedPort = proxyPort
	} else {
		// Otherwise - pick a random available port.
		selectedPort, err = networking.FindOrUsePort(proxyPort)
		if err != nil {
			return c, err
		}
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

// WithEnvironmentVariables sets environment variables
func (c *RunConfig) WithEnvironmentVariables(envVars map[string]string) (*RunConfig, error) {
	// Initialize EnvVars if it's nil
	if c.EnvVars == nil {
		c.EnvVars = make(map[string]string)
	}

	// Merge the provided environment variables with existing ones
	for key, value := range envVars {
		c.EnvVars[key] = value
	}

	// Set transport-specific environment variables
	environment.SetTransportEnvironmentVariables(c.EnvVars, string(c.Transport), c.TargetPort)
	return c, nil
}

// ValidateSecrets checks if the secrets can be parsed and are valid
func (c *RunConfig) ValidateSecrets(ctx context.Context, secretManager secrets.Provider) error {
	if len(c.Secrets) == 0 {
		return nil // No secrets to validate
	}

	_, err := environment.ParseSecretParameters(ctx, c.Secrets, secretManager)
	if err != nil {
		return fmt.Errorf("failed to get secrets: %w", err)
	}

	return nil
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

// mergeEnvVars is a helper method to merge environment variables into RunConfig
func (c *RunConfig) mergeEnvVars(envVars map[string]string) *RunConfig {
	// Initialize EnvVars if it's nil
	if c.EnvVars == nil {
		c.EnvVars = make(map[string]string)
	}

	// Add env vars to environment variables
	for key, value := range envVars {
		c.EnvVars[key] = value
	}

	return c
}

// WithEnvFilesFromDirectory processes environment files from a directory and adds them to environment variables
func (c *RunConfig) WithEnvFilesFromDirectory(dirPath string) (*RunConfig, error) {
	envVars, err := processEnvFilesDirectory(dirPath)
	if err != nil {
		return c, fmt.Errorf("failed to process env files from %s: %w", dirPath, err)
	}

	return c.mergeEnvVars(envVars), nil
}

// WithEnvFile processes a single environment file and adds it to environment variables
func (c *RunConfig) WithEnvFile(filePath string) (*RunConfig, error) {
	envVars, err := processEnvFile(filePath)
	if err != nil {
		return c, fmt.Errorf("failed to process env file %s: %w", filePath, err)
	}

	return c.mergeEnvVars(envVars), nil
}

// WithContainerName generates container name if not already set
func (c *RunConfig) WithContainerName() *RunConfig {
	if c.ContainerName == "" {
		if c.Image != "" {
			// For container-based servers
			containerName, baseName := container.GetOrGenerateContainerName(c.Name, c.Image)
			c.ContainerName = containerName
			c.BaseName = baseName
		} else if c.RemoteURL != "" && c.Name != "" {
			// For remote servers, use the provided name as base name
			c.BaseName = c.Name
		}
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
	// Use the Group field from the RunConfig
	labels.AddStandardLabels(c.ContainerLabels, containerName, c.BaseName, transportLabel, c.Port)
	return c
}

// GetBaseName returns the base name for the run configuration
func (c *RunConfig) GetBaseName() string {
	return c.BaseName
}

// SaveState saves the run configuration to the state store
func (c *RunConfig) SaveState(ctx context.Context) error {
	return state.SaveRunConfig(ctx, c)
}

// LoadState loads a run configuration from the state store
func LoadState(ctx context.Context, name string) (*RunConfig, error) {
	return state.LoadRunConfig(ctx, name, ReadJSON)
}

// RemoteAuthConfig holds configuration for remote authentication
type RemoteAuthConfig struct {
	EnableRemoteAuth bool
	ClientID         string
	ClientSecret     string
	ClientSecretFile string
	Scopes           []string
	SkipBrowser      bool
	Timeout          time.Duration `swaggertype:"string" example:"5m"`
	CallbackPort     int

	// OAuth endpoint configuration (from registry)
	Issuer       string
	AuthorizeURL string
	TokenURL     string

	// Headers for HTTP requests
	Headers []*registry.Header

	// Environment variables for the client
	EnvVars []*registry.EnvVar

	// OAuth parameters for server-specific customization
	OAuthParams map[string]string
}

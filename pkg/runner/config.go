// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package runner provides functionality for running MCP servers
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/awssts"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	authsecrets "github.com/stacklok/toolhive/pkg/auth/secrets"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/auth/upstreamswap"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/state"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport/types"
	workloadtypes "github.com/stacklok/toolhive/pkg/workloads/types"
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
	RemoteAuthConfig *remote.Config `json:"remote_auth_config,omitempty" yaml:"remote_auth_config,omitempty"`

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
	PermissionProfile *permissions.Profile `json:"permission_profile" yaml:"permission_profile" swaggerignore:"true"`

	// EnvVars are the parsed environment variables as key-value pairs
	EnvVars map[string]string `json:"env_vars,omitempty" yaml:"env_vars,omitempty"`

	// DEPRECATED: No longer appears to be used.
	// EnvFileDir is the directory path to load environment files from
	EnvFileDir string `json:"env_file_dir,omitempty" yaml:"env_file_dir,omitempty"`

	// Debug indicates whether debug mode is enabled
	Debug bool `json:"debug,omitempty" yaml:"debug,omitempty"`

	// Volumes are the directory mounts to pass to the container
	// Format: "host-path:container-path[:ro]"
	Volumes []string `json:"volumes,omitempty" yaml:"volumes,omitempty"`

	// ContainerLabels are the labels to apply to the container
	ContainerLabels map[string]string `json:"container_labels,omitempty" yaml:"container_labels,omitempty"`

	// DEPRECATED: Middleware configuration.
	// OIDCConfig contains OIDC configuration
	OIDCConfig *auth.TokenValidatorConfig `json:"oidc_config,omitempty" yaml:"oidc_config,omitempty"`

	// TokenExchangeConfig contains token exchange configuration for external authentication
	TokenExchangeConfig *tokenexchange.Config `json:"token_exchange_config,omitempty" yaml:"token_exchange_config,omitempty"`

	// UpstreamSwapConfig contains configuration for upstream token swap middleware.
	// When set along with EmbeddedAuthServerConfig, this middleware exchanges ToolHive JWTs
	// for upstream IdP tokens before forwarding requests to the MCP server.
	UpstreamSwapConfig *upstreamswap.Config `json:"upstream_swap_config,omitempty" yaml:"upstream_swap_config,omitempty"`

	// AWSStsConfig contains AWS STS token exchange configuration for accessing AWS services
	AWSStsConfig *awssts.Config `json:"aws_sts_config,omitempty" yaml:"aws_sts_config,omitempty"`

	// DEPRECATED: Middleware configuration.
	// AuthzConfig contains the authorization configuration
	AuthzConfig *authz.Config `json:"authz_config,omitempty" yaml:"authz_config,omitempty"`

	// DEPRECATED: Middleware configuration.
	// AuthzConfigPath is the path to the authorization configuration file
	AuthzConfigPath string `json:"authz_config_path,omitempty" yaml:"authz_config_path,omitempty"`

	// DEPRECATED: Middleware configuration.
	// AuditConfig contains the audit logging configuration
	AuditConfig *audit.Config `json:"audit_config,omitempty" yaml:"audit_config,omitempty"`

	// DEPRECATED: Middleware configuration.
	// AuditConfigPath is the path to the audit configuration file
	AuditConfigPath string `json:"audit_config_path,omitempty" yaml:"audit_config_path,omitempty"`

	// DEPRECATED: Middleware configuration.
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

	// buildContext indicates whether this config is being built for CLI or operator use (not serialized)
	buildContext BuildContext

	// IsolateNetwork indicates whether to isolate the network for the container
	IsolateNetwork bool `json:"isolate_network,omitempty" yaml:"isolate_network,omitempty"`

	// TrustProxyHeaders indicates whether to trust X-Forwarded-* headers from reverse proxies
	TrustProxyHeaders bool `json:"trust_proxy_headers,omitempty" yaml:"trust_proxy_headers,omitempty"`

	// ProxyMode is the proxy mode for stdio transport ("sse" or "streamable-http")
	// Note: "sse" is deprecated; use "streamable-http" instead.
	ProxyMode types.ProxyMode `json:"proxy_mode,omitempty" yaml:"proxy_mode,omitempty"`

	// DEPRECATED: No longer appears to be used.
	// ThvCABundle is the path to the CA certificate bundle for ToolHive HTTP operations
	ThvCABundle string `json:"thv_ca_bundle,omitempty" yaml:"thv_ca_bundle,omitempty"`

	// DEPRECATED: No longer appears to be used.
	// JWKSAuthTokenFile is the path to file containing auth token for JWKS/OIDC requests
	JWKSAuthTokenFile string `json:"jwks_auth_token_file,omitempty" yaml:"jwks_auth_token_file,omitempty"`

	// Group is the name of the group this workload belongs to, if any
	Group string `json:"group,omitempty" yaml:"group,omitempty"`

	// DEPRECATED: Middleware configuration.
	// ToolsFilter is the list of tools to filter
	ToolsFilter []string `json:"tools_filter,omitempty" yaml:"tools_filter,omitempty"`

	// DEPRECATED: Middleware configuration.
	// ToolsOverride is a map from an actual tool to its overridden name and/or description
	ToolsOverride map[string]ToolOverride `json:"tools_override,omitempty" yaml:"tools_override,omitempty"`

	// IgnoreConfig contains configuration for ignore processing
	IgnoreConfig *ignore.Config `json:"ignore_config,omitempty" yaml:"ignore_config,omitempty"`

	// MiddlewareConfigs contains the list of middleware to apply to the transport
	// and the configuration for each middleware.
	MiddlewareConfigs []types.MiddlewareConfig `json:"middleware_configs,omitempty" yaml:"middleware_configs,omitempty"`

	// existingPort is the port from an existing workload being updated (not serialized)
	// Used during port validation to allow reusing the same port
	existingPort int

	// EndpointPrefix is an explicit prefix to prepend to SSE endpoint URLs.
	// This is used to handle path-based ingress routing scenarios.
	EndpointPrefix string `json:"endpoint_prefix,omitempty" yaml:"endpoint_prefix,omitempty"`

	// RuntimeConfig allows overriding the default runtime configuration
	// for this specific workload (base images and packages)
	RuntimeConfig *templates.RuntimeConfig `json:"runtime_config,omitempty" yaml:"runtime_config,omitempty"`

	// HeaderForward contains configuration for injecting headers into requests to remote servers.
	HeaderForward *HeaderForwardConfig `json:"header_forward,omitempty" yaml:"header_forward,omitempty"`

	// EmbeddedAuthServerConfig contains configuration for the embedded OAuth2/OIDC authorization server.
	// When set, the proxy runner will start an embedded auth server that delegates to upstream IDPs.
	// This is the serializable RunConfig; secrets are referenced by file paths or env var names.
	EmbeddedAuthServerConfig *authserver.RunConfig `json:"embedded_auth_server_config,omitempty" yaml:"embedded_auth_server_config,omitempty"` //nolint:lll
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

	// Migrate plain text OAuth client secrets to CLI format
	if err := migrateOAuthClientSecret(&config); err != nil {
		return nil, fmt.Errorf("failed to migrate OAuth client secret: %w", err)
	}

	// Migrate plain text bearer tokens to CLI format
	if err := migrateBearerToken(&config); err != nil {
		return nil, fmt.Errorf("failed to migrate bearer token: %w", err)
	}

	return &config, nil
}

// migrateOAuthClientSecret migrates plain text OAuth client secrets to CLI format
// This handles the transition from storing plain text secrets to CLI format references
func migrateOAuthClientSecret(config *RunConfig) error {
	if config.RemoteAuthConfig == nil {
		return nil // No OAuth config to migrate
	}

	if config.RemoteAuthConfig.ClientSecret == "" {
		return nil
	}

	// Check if the client secret is already in CLI format
	if _, err := secrets.ParseSecretParameter(config.RemoteAuthConfig.ClientSecret); err == nil {
		return nil // Already in CLI format, no migration needed
	}

	// The client secret is in plain text format - migrate it
	cliFormatSecret, err := authsecrets.ProcessSecret(
		config.Name,
		config.RemoteAuthConfig.ClientSecret,
		authsecrets.TokenTypeOAuthClientSecret,
	)
	if err != nil {
		return fmt.Errorf("failed to process OAuth client secret: %w", err)
	}

	// Update the RunConfig to use the CLI format reference
	config.RemoteAuthConfig.ClientSecret = cliFormatSecret

	// Save the migrated RunConfig back to disk so migration only happens once
	if err := config.SaveState(context.Background()); err != nil {
		// Log error without potentially sensitive details - only log error type and message
		slog.Warn("failed to save migrated RunConfig for workload", "name", config.Name, "error", err)
		// Don't fail the migration - the secret is already stored and the config is updated in memory
	}

	return nil
}

// migrateBearerToken migrates plain text bearer tokens to CLI format
// This handles the transition from storing plain text tokens to CLI format references
func migrateBearerToken(config *RunConfig) error {
	if config.RemoteAuthConfig == nil {
		return nil // No remote auth config to migrate
	}

	if config.RemoteAuthConfig.BearerToken == "" {
		return nil
	}

	// Check if the bearer token is already in CLI format
	if _, err := secrets.ParseSecretParameter(config.RemoteAuthConfig.BearerToken); err == nil {
		return nil // Already in CLI format, no migration needed
	}

	// The bearer token is in plain text format - migrate it
	cliFormatToken, err := authsecrets.ProcessSecret(
		config.Name,
		config.RemoteAuthConfig.BearerToken,
		authsecrets.TokenTypeBearerToken,
	)
	if err != nil {
		return fmt.Errorf("failed to process bearer token: %w", err)
	}

	// Update the RunConfig to use the CLI format reference
	config.RemoteAuthConfig.BearerToken = cliFormatToken

	// Save the migrated RunConfig back to disk so migration only happens once
	if err := config.SaveState(context.Background()); err != nil {
		// Log error without potentially sensitive details - only log error type and message
		slog.Warn("failed to save migrated RunConfig for workload", "name", config.Name, "error", err)
		// Don't fail the migration - the secret is already stored and the config is updated in memory
	}

	return nil
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
	// Skip port validation for operator context - ports will be used in containers, not on operator host
	if c.buildContext == BuildContextOperator {
		c.Port = proxyPort
		c.TargetPort = targetPort
		return c, nil
	}

	// CLI context: perform port validation as before
	var selectedPort int
	var err error

	// If the user requested an explicit proxy port, check if it's available.
	// If not available - treat as an error, since picking a random port here
	// is going to lead to confusion.
	if proxyPort != 0 {
		// Skip validation if reusing the same port from existing workload (during update)
		if proxyPort == c.existingPort && c.existingPort > 0 {
			slog.Debug("reusing existing port", "port", proxyPort)
			selectedPort = proxyPort
		} else if !networking.IsAvailable(proxyPort) {
			return c, fmt.Errorf("requested proxy port %d is not available", proxyPort)
		} else {
			slog.Debug("using requested port", "port", proxyPort)
			selectedPort = proxyPort
		}
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
		slog.Debug("using target port", "port", selectedTargetPort)
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
	if len(c.Secrets) > 0 {
		_, err := environment.ParseSecretParameters(ctx, c.Secrets, secretManager)
		if err != nil {
			return fmt.Errorf("failed to get secrets: %w", err)
		}
	}
	if c.RemoteAuthConfig != nil && c.RemoteAuthConfig.ClientSecret != "" {
		_, err := secrets.ParseSecretParameter(c.RemoteAuthConfig.ClientSecret)
		if err != nil {
			return fmt.Errorf("failed to get secrets: %w", err)
		}
	}

	return nil
}

// WithSecrets processes secrets and adds them to environment variables
func (c *RunConfig) WithSecrets(ctx context.Context, secretManager secrets.Provider) (*RunConfig, error) {
	// Process regular secrets if provided
	if len(c.Secrets) > 0 {
		secretVariables, err := environment.ParseSecretParameters(ctx, c.Secrets, secretManager)
		if err != nil {
			return c, fmt.Errorf("failed to get secrets: %w", err)
		}

		// Initialize EnvVars if it's nil
		if c.EnvVars == nil {
			c.EnvVars = make(map[string]string)
		}

		// Add secret variables to environment variables
		for key, value := range secretVariables {
			c.EnvVars[key] = value
		}
	}

	// Process RemoteAuthConfig.ClientSecret if it's in CLI format
	if c.RemoteAuthConfig != nil && c.RemoteAuthConfig.ClientSecret != "" {
		// Check if it's in CLI format (contains ",target=")
		if secretParam, err := secrets.ParseSecretParameter(c.RemoteAuthConfig.ClientSecret); err == nil {
			// It's in CLI format, resolve the actual secret value
			actualSecret, err := secretManager.GetSecret(ctx, secretParam.Name)
			if err != nil {
				return c, fmt.Errorf("failed to resolve OAuth client secret '%s': %w", secretParam.Name, err)
			}
			// Replace the CLI format string with the actual secret value
			c.RemoteAuthConfig.ClientSecret = actualSecret
		}
		// If it's not in CLI format (plain text), leave it as is
	}

	// Process RemoteAuthConfig.BearerToken if it's in CLI format
	if c.RemoteAuthConfig != nil && c.RemoteAuthConfig.BearerToken != "" {
		// Check if it's in CLI format (contains ",target=")
		if secretParam, err := secrets.ParseSecretParameter(c.RemoteAuthConfig.BearerToken); err == nil {
			// It's in CLI format, resolve the actual token value
			actualToken, err := secretManager.GetSecret(ctx, secretParam.Name)
			if err != nil {
				return c, fmt.Errorf("failed to resolve bearer token '%s': %w", secretParam.Name, err)
			}
			// Replace the CLI format string with the actual token value
			c.RemoteAuthConfig.BearerToken = actualToken
		}
		// If it's not in CLI format (plain text), leave it as is
	}

	// Process HeaderForward.AddHeadersFromSecret
	if err := c.resolveHeaderForwardSecrets(ctx, secretManager); err != nil {
		return c, err
	}

	return c, nil
}

// resolveHeaderForwardSecrets resolves secret references in HeaderForward.AddHeadersFromSecret
// and builds the merged resolvedHeaders map for middleware consumption.
// Only the secret references are persisted to disk; actual values exist only in memory
// via the non-serialized resolvedHeaders field.
func (c *RunConfig) resolveHeaderForwardSecrets(ctx context.Context, secretManager secrets.Provider) error {
	if c.HeaderForward == nil || len(c.HeaderForward.AddHeadersFromSecret) == 0 {
		return nil
	}
	// Build merged map: start with plaintext headers, then overlay resolved secrets.
	merged := make(map[string]string, len(c.HeaderForward.AddPlaintextHeaders)+len(c.HeaderForward.AddHeadersFromSecret))
	for k, v := range c.HeaderForward.AddPlaintextHeaders {
		merged[k] = v
	}
	for headerName, secretName := range c.HeaderForward.AddHeadersFromSecret {
		actualValue, err := secretManager.GetSecret(ctx, secretName)
		if err != nil {
			return fmt.Errorf("failed to resolve header secret %q: %w", secretName, err)
		}
		merged[headerName] = actualValue
	}
	c.HeaderForward.resolvedHeaders = merged
	return nil
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
// Returns the config and a boolean indicating if the name was sanitized
func (c *RunConfig) WithContainerName() (*RunConfig, bool) {
	var wasModified bool

	if c.ContainerName == "" {
		if c.Image != "" {
			// For container-based servers
			// Sanitize the name if provided to ensure it's safe for file paths
			safeName := ""
			if c.Name != "" {
				safeName, wasModified = workloadtypes.SanitizeWorkloadName(c.Name)
			}
			containerName, baseName := container.GetOrGenerateContainerName(safeName, c.Image)
			c.ContainerName = containerName
			c.BaseName = baseName
		} else if c.RemoteURL != "" && c.Name != "" {
			// For remote servers, sanitize the provided name to ensure it's safe for file paths
			c.BaseName, wasModified = workloadtypes.SanitizeWorkloadName(c.Name)
			c.ContainerName = c.Name
		}
	}
	return c, wasModified
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

// ToolOverride represents a tool override.
// Both Name and Description can be overridden independently, but
// they can't be both empty.
type ToolOverride struct {
	// Name is the redefined name of the tool
	Name string `json:"name,omitempty"`
	// Description is the redefined description of the tool
	Description string `json:"description,omitempty"`
}

// HeaderForwardConfig defines configuration for injecting headers into requests to remote servers.
// Headers are added server-side, so clients don't need to configure them individually.
type HeaderForwardConfig struct {
	// AddPlaintextHeaders is a map of header names to literal values to inject into requests.
	// WARNING: These values are stored in plaintext in the configuration.
	// For sensitive values (API keys, tokens), use AddHeadersFromSecret instead.
	AddPlaintextHeaders map[string]string `json:"add_plaintext_headers,omitempty" yaml:"add_plaintext_headers,omitempty"`

	// AddHeadersFromSecret is a map of header names to secret names.
	// The key is the header name, the value is the secret name in ToolHive's secrets manager.
	// Resolved at runtime via WithSecrets() into resolvedHeaders.
	// The actual secret value is only held in memory, never persisted.
	AddHeadersFromSecret map[string]string `json:"add_headers_from_secret,omitempty" yaml:"add_headers_from_secret,omitempty"`

	// resolvedHeaders holds the merged set of headers (plaintext + resolved secrets)
	// for middleware consumption. Never serialized to disk.
	resolvedHeaders map[string]string
}

// ResolvedHeaders returns the merged set of headers for middleware use.
// After WithSecrets() has run, this includes both plaintext and secret-backed headers.
// If secrets have not been resolved yet, returns only AddPlaintextHeaders.
// Safe to call on a nil receiver.
func (h *HeaderForwardConfig) ResolvedHeaders() map[string]string {
	if h == nil {
		return nil
	}
	if h.resolvedHeaders != nil {
		return h.resolvedHeaders
	}
	return h.AddPlaintextHeaders
}

// HasHeaders returns true if any headers are configured (plaintext or secret-backed).
// Safe to call on a nil receiver.
func (h *HeaderForwardConfig) HasHeaders() bool {
	if h == nil {
		return false
	}
	return len(h.AddPlaintextHeaders) > 0 || len(h.AddHeadersFromSecret) > 0
}

// DefaultCallbackPort is the default port for the OAuth callback server
const DefaultCallbackPort = 8666

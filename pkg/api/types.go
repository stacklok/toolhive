// Package api provides a programmatic interface to ToolHive functionality.
package api

import (
	"io"
	"time"

	"github.com/StacklokLabs/toolhive/pkg/auth"
	"github.com/StacklokLabs/toolhive/pkg/permissions"
	"github.com/StacklokLabs/toolhive/pkg/registry"
	"github.com/StacklokLabs/toolhive/pkg/runner"
	"github.com/StacklokLabs/toolhive/pkg/transport/types"
)

// ServerStatus represents the status of an MCP server.
type ServerStatus string

const (
	// ServerStatusRunning indicates the server is running.
	ServerStatusRunning ServerStatus = "running"
	// ServerStatusStopped indicates the server is stopped.
	ServerStatusStopped ServerStatus = "stopped"
	// ServerStatusError indicates the server is in an error state.
	ServerStatusError ServerStatus = "error"
)

// Server represents an MCP server in the API.
type Server struct {
	// Name is the identifier for the MCP server
	Name string `json:"name"`
	// Image is the Docker image reference for the MCP server
	Image string `json:"image"`
	// Description is a human-readable description of the server's purpose and functionality
	Description string `json:"description"`
	// Transport defines the communication protocol for the server (stdio or sse)
	Transport string `json:"transport"`
	// TargetPort is the port for the container to expose (only applicable to SSE transport)
	TargetPort int `json:"target_port,omitempty"`
	// Permissions defines the security profile and access permissions for the server
	Permissions *permissions.Profile `json:"permissions"`
	// Tools is a list of tool names provided by this MCP server
	Tools []string `json:"tools"`
	// EnvVars defines environment variables that can be passed to the server
	EnvVars []*registry.EnvVar `json:"env_vars"`
	// Args are the default command-line arguments to pass to the MCP server container
	Args []string `json:"args"`
	// RepositoryURL is the URL to the source code repository for the server
	RepositoryURL string `json:"repository_url,omitempty"`
	// Tags are categorization labels for the server to aid in discovery and filtering
	Tags []string `json:"tags,omitempty"`
	// DockerTags lists the available Docker tags for this server image
	DockerTags []string `json:"docker_tags,omitempty"`
	// Status is the current status of the server (running, stopped, error)
	Status ServerStatus `json:"status"`
	// ContainerID is the ID of the container running the server (if running)
	ContainerID string `json:"container_id,omitempty"`
	// HostPort is the port on the host that the server is listening on (if running)
	HostPort int `json:"host_port,omitempty"`
	// StartedAt is the time when the server was started (if running)
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// RunOptions represents options for running an MCP server.
type RunOptions struct {
	// Name is the name of the MCP server
	Name string `json:"name,omitempty"`
	// Transport is the transport mode (sse or stdio)
	Transport types.TransportType `json:"transport,omitempty"`
	// Port is the port for the HTTP proxy to listen on (host port)
	Port int `json:"port,omitempty"`
	// TargetPort is the port for the container to expose (only applicable to SSE transport)
	TargetPort int `json:"target_port,omitempty"`
	// TargetHost is the host to forward traffic to (only applicable to SSE transport)
	TargetHost string `json:"target_host,omitempty"`
	// PermissionProfile is the name or path of the permission profile
	PermissionProfile string `json:"permission_profile,omitempty"`
	// EnvVars are environment variables to pass to the MCP server
	EnvVars map[string]string `json:"env_vars,omitempty"`
	// Debug indicates whether debug mode is enabled
	Debug bool `json:"debug,omitempty"`
	// Volumes are directory mounts to pass to the container
	Volumes []string `json:"volumes,omitempty"`
	// Secrets are secret parameters to pass to the container
	Secrets []string `json:"secrets,omitempty"`
	// AuthzConfig is the path to the authorization configuration file
	AuthzConfig string `json:"authz_config,omitempty"`
	// CmdArgs are additional command-line arguments to pass to the MCP server
	CmdArgs []string `json:"cmd_args,omitempty"`
	// Foreground indicates whether to run in foreground mode
	Foreground bool `json:"foreground,omitempty"`
	// OIDCConfig contains OIDC configuration
	OIDCConfig *auth.JWTValidatorConfig `json:"oidc_config,omitempty"`
	// K8sPodTemplatePatch is a JSON string to patch the Kubernetes pod template
	K8sPodTemplatePatch string `json:"k8s_pod_template_patch,omitempty"`
}

// ListOptions represents options for listing MCP servers.
type ListOptions struct {
	// Status filters servers by status (running, stopped, all)
	Status string `json:"status,omitempty"`
	// Tag filters servers by tag
	Tag string `json:"tag,omitempty"`
	// Search filters servers by search term (searches in name, description, and tags)
	Search string `json:"search,omitempty"`
}

// StopOptions represents options for stopping an MCP server.
type StopOptions struct {
	// Force indicates whether to force stop the server
	Force bool `json:"force,omitempty"`
	// Timeout is the timeout for stopping the server
	Timeout time.Duration `json:"timeout,omitempty"`
}

// RemoveOptions represents options for removing an MCP server.
type RemoveOptions struct {
	// Force indicates whether to force remove the server
	Force bool `json:"force,omitempty"`
	// RemoveVolumes indicates whether to remove volumes associated with the server
	RemoveVolumes bool `json:"remove_volumes,omitempty"`
}

// RestartOptions represents options for restarting an MCP server.
type RestartOptions struct {
	// Timeout is the timeout for stopping the server before restarting
	Timeout time.Duration `json:"timeout,omitempty"`
}

// LogsOptions represents options for getting logs from an MCP server.
type LogsOptions struct {
	// Follow indicates whether to follow the logs
	Follow bool `json:"follow,omitempty"`
	// Tail indicates the number of lines to show from the end of the logs
	Tail int `json:"tail,omitempty"`
	// Since shows logs since a specific time
	Since time.Duration `json:"since,omitempty"`
	// Output is the writer to write logs to
	Output io.Writer `json:"-"`
}

// ProxyOptions represents options for proxying to an MCP server.
type ProxyOptions struct {
	// Port is the port to listen on
	Port int `json:"port,omitempty"`
	// TargetPort is the port to forward to
	TargetPort int `json:"target_port,omitempty"`
	// TargetHost is the host to forward to
	TargetHost string `json:"target_host,omitempty"`
	// OIDCConfig contains OIDC configuration for JWT validation
	OIDCConfig *auth.JWTValidatorConfig `json:"oidc_config,omitempty"`
}

// RegistryListOptions represents options for listing MCP servers in the registry.
type RegistryListOptions struct {
	// Format is the output format (json or text)
	Format string `json:"format,omitempty"`
}

// RegistryInfoOptions represents options for getting information about an MCP server in the registry.
type RegistryInfoOptions struct {
	// Format is the output format (json or text)
	Format string `json:"format,omitempty"`
}

// SecretSetOptions represents options for setting a secret.
type SecretSetOptions struct {
	// Value is the value of the secret
	Value string `json:"value,omitempty"`
	// Provider is the secrets provider to use
	Provider string `json:"provider,omitempty"`
}

// SecretGetOptions represents options for getting a secret.
type SecretGetOptions struct {
	// Provider is the secrets provider to use
	Provider string `json:"provider,omitempty"`
}

// SecretDeleteOptions represents options for deleting a secret.
type SecretDeleteOptions struct {
	// Provider is the secrets provider to use
	Provider string `json:"provider,omitempty"`
}

// SecretListOptions represents options for listing secrets.
type SecretListOptions struct {
	// Provider is the secrets provider to use
	Provider string `json:"provider,omitempty"`
}

// SecretResetKeyringOptions represents options for resetting the keyring.
type SecretResetKeyringOptions struct {
	// Force indicates whether to force reset the keyring
	Force bool `json:"force,omitempty"`
}

// ConfigRegisterClientOptions represents options for registering a client.
type ConfigRegisterClientOptions struct {
	// Name is the name of the client
	Name string `json:"name,omitempty"`
	// URL is the URL of the client
	URL string `json:"url,omitempty"`
}

// ConfigRemoveClientOptions represents options for removing a client.
type ConfigRemoveClientOptions struct {
	// Name is the name of the client
	Name string `json:"name,omitempty"`
}

// ConfigListRegisteredClientsOptions represents options for listing registered clients.
type ConfigListRegisteredClientsOptions struct {
	// Format is the output format (json or text)
	Format string `json:"format,omitempty"`
}

// ConfigAutoDiscoveryOptions represents options for auto-discovering clients.
type ConfigAutoDiscoveryOptions struct {
	// Enable indicates whether to enable auto-discovery
	Enable bool `json:"enable,omitempty"`
}

// ConfigSecretsProviderOptions represents options for setting the secrets provider.
type ConfigSecretsProviderOptions struct {
	// Provider is the secrets provider to use
	Provider string `json:"provider,omitempty"`
}

// SearchOptions represents options for searching for MCP servers.
type SearchOptions struct {
	// Query is the search query
	Query string `json:"query,omitempty"`
	// Format is the output format (json or text)
	Format string `json:"format,omitempty"`
}

// VersionOptions represents options for showing version information.
type VersionOptions struct {
	// Format is the output format (json or text)
	Format string `json:"format,omitempty"`
}

// FromRegistryServer converts a registry.Server to an API Server.
func FromRegistryServer(regServer *registry.Server) *Server {
	return &Server{
		Name:          regServer.Name,
		Image:         regServer.Image,
		Description:   regServer.Description,
		Transport:     regServer.Transport,
		TargetPort:    regServer.TargetPort,
		Permissions:   regServer.Permissions,
		Tools:         regServer.Tools,
		EnvVars:       regServer.EnvVars,
		Args:          regServer.Args,
		RepositoryURL: regServer.RepositoryURL,
		Tags:          regServer.Tags,
		DockerTags:    regServer.DockerTags,
		Status:        ServerStatusStopped,
	}
}

// ToRegistryServer converts an API Server to a registry.Server.
func (s *Server) ToRegistryServer() *registry.Server {
	return &registry.Server{
		Name:          s.Name,
		Image:         s.Image,
		Description:   s.Description,
		Transport:     s.Transport,
		TargetPort:    s.TargetPort,
		Permissions:   s.Permissions,
		Tools:         s.Tools,
		EnvVars:       s.EnvVars,
		Args:          s.Args,
		RepositoryURL: s.RepositoryURL,
		Tags:          s.Tags,
		DockerTags:    s.DockerTags,
		Metadata:      &registry.Metadata{},
	}
}

// ToRunnerConfig converts RunOptions to a runner.RunConfig.
func (o *RunOptions) ToRunnerConfig() *runner.RunConfig {
	config := runner.NewRunConfig()

	// Set basic fields
	config.Name = o.Name
	config.Debug = o.Debug
	config.TargetHost = o.TargetHost
	config.K8sPodTemplatePatch = o.K8sPodTemplatePatch
	config.PermissionProfileNameOrPath = o.PermissionProfile
	config.Volumes = o.Volumes
	config.Secrets = o.Secrets
	config.AuthzConfigPath = o.AuthzConfig
	config.CmdArgs = o.CmdArgs

	// Set environment variables
	if o.EnvVars != nil {
		config.EnvVars = o.EnvVars
	}

	// Set OIDC config
	if o.OIDCConfig != nil {
		config.OIDCConfig = o.OIDCConfig
	}

	return config
}

// FromRunnerConfig converts a runner.RunConfig to RunOptions.
func FromRunnerConfig(config *runner.RunConfig) *RunOptions {
	return &RunOptions{
		Name:                config.Name,
		Transport:           config.Transport,
		Port:                config.Port,
		TargetPort:          config.TargetPort,
		TargetHost:          config.TargetHost,
		PermissionProfile:   config.PermissionProfileNameOrPath,
		EnvVars:             config.EnvVars,
		Debug:               config.Debug,
		Volumes:             config.Volumes,
		Secrets:             config.Secrets,
		AuthzConfig:         config.AuthzConfigPath,
		CmdArgs:             config.CmdArgs,
		OIDCConfig:          config.OIDCConfig,
		K8sPodTemplatePatch: config.K8sPodTemplatePatch,
	}
}

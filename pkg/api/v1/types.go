// Package v1 provides version 1 of the ToolHive API.
package v1

import (
	"fmt"
	"io"
	"time"
)

// Error represents an API error.
type Error struct {
	// Code is a machine-readable error code.
	Code string `json:"code"`
	// Message is a human-readable error message.
	Message string `json:"message"`
	// Details contains additional error details.
	Details any `json:"details,omitempty"`
}

// Predefined error codes
const (
	ErrNotFound      = "NOT_FOUND"
	ErrAlreadyExists = "ALREADY_EXISTS"
	ErrInvalidInput  = "INVALID_INPUT"
	ErrUnavailable   = "UNAVAILABLE"
	ErrInternal      = "INTERNAL"
	ErrUnauthorized  = "UNAUTHORIZED"
	ErrForbidden     = "FORBIDDEN"
)

// ServerStatus represents the status of an MCP server.
type ServerStatus string

const (
	// ServerStatusPending indicates the server is being created.
	ServerStatusPending ServerStatus = "pending"
	// ServerStatusRunning indicates the server is running.
	ServerStatusRunning ServerStatus = "running"
	// ServerStatusFailed indicates the server failed to start or run.
	ServerStatusFailed ServerStatus = "failed"
	// ServerStatusStopped indicates the server is stopped.
	ServerStatusStopped ServerStatus = "stopped"
)

// TransportType represents the type of transport to use.
type TransportType string

const (
	// TransportTypeStdio represents the stdio transport.
	TransportTypeStdio TransportType = "stdio"
	// TransportTypeSSE represents the SSE transport.
	TransportTypeSSE TransportType = "sse"
)

// NetworkPermissions defines network permissions for a server.
type NetworkPermissions struct {
	// Outbound defines outbound network permissions.
	Outbound *OutboundNetworkPermissions `json:"outbound,omitempty"`
}

// OutboundNetworkPermissions defines outbound network permissions.
type OutboundNetworkPermissions struct {
	// InsecureAllowAll allows all outbound network connections.
	InsecureAllowAll bool `json:"insecureAllowAll,omitempty"`
	// AllowTransport is a list of allowed transport protocols (tcp, udp).
	AllowTransport []string `json:"allowTransport,omitempty"`
	// AllowHost is a list of allowed hosts.
	AllowHost []string `json:"allowHost,omitempty"`
	// AllowPort is a list of allowed ports.
	AllowPort []int `json:"allowPort,omitempty"`
}

// PermissionProfile defines the security profile for a server.
type PermissionProfile struct {
	// Read is a list of mount declarations that the container can read from.
	Read []string `json:"read,omitempty"`
	// Write is a list of mount declarations that the container can write to.
	Write []string `json:"write,omitempty"`
	// Network defines network permissions.
	Network *NetworkPermissions `json:"network,omitempty"`
}

// EnvVar represents an environment variable for an MCP server.
type EnvVar struct {
	// Name is the environment variable name (e.g., API_KEY).
	Name string `json:"name"`
	// Description is a human-readable explanation of the variable's purpose.
	Description string `json:"description"`
	// Required indicates whether this environment variable must be provided.
	Required bool `json:"required"`
	// Default is the value to use if the environment variable is not explicitly provided.
	Default string `json:"default,omitempty"`
}

// Server represents an MCP server.
type Server struct {
	// Name is the unique identifier for the server.
	Name string `json:"name"`
	// Image is the Docker image reference for the MCP server.
	Image string `json:"image"`
	// Description is a human-readable description of the server's purpose and functionality.
	Description string `json:"description"`
	// Transport defines the communication protocol for the server (stdio or sse).
	Transport string `json:"transport"`
	// TargetPort is the port for the container to expose (only applicable to SSE transport).
	TargetPort int `json:"targetPort,omitempty"`
	// Permissions defines the security profile and access permissions for the server.
	Permissions *PermissionProfile `json:"permissions,omitempty"`
	// Tools is a list of tool names provided by this MCP server.
	Tools []string `json:"tools,omitempty"`
	// EnvVars defines environment variables that can be passed to the server.
	EnvVars []*EnvVar `json:"envVars,omitempty"`
	// Args are the default command-line arguments to pass to the MCP server container.
	Args []string `json:"args,omitempty"`
	// RepositoryURL is the URL to the source code repository for the server.
	RepositoryURL string `json:"repositoryUrl,omitempty"`
	// Tags are categorization labels for the server to aid in discovery and filtering.
	Tags []string `json:"tags,omitempty"`
	// DockerTags lists the available Docker tags for this server image.
	DockerTags []string `json:"dockerTags,omitempty"`
	// Status is the current status of the server.
	Status ServerStatus `json:"status,omitempty"`
	// ContainerID is the ID of the container running the server (if running).
	ContainerID string `json:"containerId,omitempty"`
	// Port is the port on the host that the server is listening on (if running).
	Port int `json:"port,omitempty"`
	// StartedAt is the time when the server was started (if running).
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// CreatedAt is when the server was created.
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	// Labels are key-value pairs attached to the server.
	Labels map[string]string `json:"labels,omitempty"`
	// Message provides additional status information.
	Message string `json:"message,omitempty"`
}

// ServerList represents a list of MCP servers.
type ServerList struct {
	// Items is the list of servers.
	Items []Server `json:"items"`
	// Total is the total number of servers matching the query.
	Total int `json:"total,omitempty"`
}

// OIDCConfig contains OIDC configuration.
type OIDCConfig struct {
	// Issuer is the OIDC issuer URL.
	Issuer string `json:"issuer,omitempty"`
	// Audience is the OIDC audience.
	Audience string `json:"audience,omitempty"`
	// JWKSURL is the OIDC JWKS URL.
	JWKSURL string `json:"jwksUrl,omitempty"`
	// ClientID is the OIDC client ID.
	ClientID string `json:"clientId,omitempty"`
}

// RunOptions represents options for running an MCP server.
type RunOptions struct {
	// Name is the name of the MCP server.
	Name string `json:"name,omitempty"`
	// Transport is the transport mode (sse or stdio).
	Transport TransportType `json:"transport,omitempty"`
	// Port is the port for the HTTP proxy to listen on (host port).
	Port int `json:"port,omitempty"`
	// TargetPort is the port for the container to expose (only applicable to SSE transport).
	TargetPort int `json:"targetPort,omitempty"`
	// TargetHost is the host to forward traffic to (only applicable to SSE transport).
	TargetHost string `json:"targetHost,omitempty"`
	// PermissionProfile is the name or path of the permission profile.
	PermissionProfile string `json:"permissionProfile,omitempty"`
	// EnvVars are environment variables to pass to the MCP server.
	EnvVars map[string]string `json:"envVars,omitempty"`
	// Debug indicates whether debug mode is enabled.
	Debug bool `json:"debug,omitempty"`
	// Volumes are directory mounts to pass to the container.
	Volumes []string `json:"volumes,omitempty"`
	// Secrets are secret parameters to pass to the container.
	Secrets []string `json:"secrets,omitempty"`
	// AuthzConfig is the path to the authorization configuration file.
	AuthzConfig string `json:"authzConfig,omitempty"`
	// CmdArgs are additional command-line arguments to pass to the MCP server.
	CmdArgs []string `json:"cmdArgs,omitempty"`
	// Foreground indicates whether to run in foreground mode.
	Foreground bool `json:"foreground,omitempty"`
	// OIDCConfig contains OIDC configuration.
	OIDCConfig *OIDCConfig `json:"oidcConfig,omitempty"`
	// K8sPodTemplatePatch is a JSON string to patch the Kubernetes pod template.
	K8sPodTemplatePatch string `json:"k8sPodTemplatePatch,omitempty"`
}

// Validate validates the RunOptions.
func (o *RunOptions) Validate() error {
	if o.Transport != "" && o.Transport != TransportTypeStdio && o.Transport != TransportTypeSSE {
		return fmt.Errorf("invalid transport: %s", o.Transport)
	}

	if o.TargetPort < 0 || o.TargetPort > 65535 {
		return fmt.Errorf("invalid target port: %d", o.TargetPort)
	}

	if o.Port < 0 || o.Port > 65535 {
		return fmt.Errorf("invalid port: %d", o.Port)
	}

	return nil
}

// ListOptions represents options for listing MCP servers.
type ListOptions struct {
	// Status filters servers by status (running, stopped, all).
	Status string `json:"status,omitempty"`
	// Tag filters servers by tag.
	Tag string `json:"tag,omitempty"`
	// Search filters servers by search term (searches in name, description, and tags).
	Search string `json:"search,omitempty"`
	// Limit limits the number of results.
	Limit int `json:"limit,omitempty"`
	// Offset is the offset for pagination.
	Offset int `json:"offset,omitempty"`
}

// StopOptions represents options for stopping an MCP server.
type StopOptions struct {
	// Force indicates whether to force stop the server.
	Force bool `json:"force,omitempty"`
	// Timeout is the timeout for stopping the server.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// RemoveOptions represents options for removing an MCP server.
type RemoveOptions struct {
	// Force indicates whether to force remove the server.
	Force bool `json:"force,omitempty"`
	// RemoveVolumes indicates whether to remove volumes associated with the server.
	RemoveVolumes bool `json:"removeVolumes,omitempty"`
}

// RestartOptions represents options for restarting an MCP server.
type RestartOptions struct {
	// Timeout is the timeout for stopping the server before restarting.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// LogsOptions represents options for getting logs from an MCP server.
type LogsOptions struct {
	// Follow indicates whether to follow the logs.
	Follow bool `json:"follow,omitempty"`
	// Tail indicates the number of lines to show from the end of the logs.
	Tail int `json:"tail,omitempty"`
	// Since shows logs since a specific time.
	Since time.Duration `json:"since,omitempty"`
	// Output is the writer to write logs to.
	Output io.Writer `json:"-"`
}

// ProxyOptions represents options for proxying to an MCP server.
type ProxyOptions struct {
	// Port is the port to listen on.
	Port int `json:"port,omitempty"`
	// TargetPort is the port to forward to.
	TargetPort int `json:"targetPort,omitempty"`
	// TargetHost is the host to forward to.
	TargetHost string `json:"targetHost,omitempty"`
}

// Secret represents a secret.
type Secret struct {
	// Name is the name of the secret.
	Name string `json:"name"`
	// Provider is the secrets provider used.
	Provider string `json:"provider,omitempty"`
	// CreatedAt is when the secret was created.
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	// UpdatedAt is when the secret was last updated.
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// SecretList represents a list of secrets.
type SecretList struct {
	// Items is the list of secrets.
	Items []Secret `json:"items"`
}

// SecretSetOptions represents options for setting a secret.
type SecretSetOptions struct {
	// Value is the value of the secret.
	Value string `json:"value,omitempty"`
	// Provider is the secrets provider to use.
	Provider string `json:"provider,omitempty"`
}

// SecretGetOptions represents options for getting a secret.
type SecretGetOptions struct {
	// Provider is the secrets provider to use.
	Provider string `json:"provider,omitempty"`
}

// SecretDeleteOptions represents options for deleting a secret.
type SecretDeleteOptions struct {
	// Provider is the secrets provider to use.
	Provider string `json:"provider,omitempty"`
}

// SecretListOptions represents options for listing secrets.
type SecretListOptions struct {
	// Provider is the secrets provider to use.
	Provider string `json:"provider,omitempty"`
}

// SecretResetKeyringOptions represents options for resetting the keyring.
type SecretResetKeyringOptions struct {
	// Force indicates whether to force reset the keyring.
	Force bool `json:"force,omitempty"`
}

// RegisteredClient represents a registered client.
type RegisteredClient struct {
	// Name is the name of the client.
	Name string `json:"name"`
	// URL is the URL of the client.
	URL string `json:"url"`
	// RegisteredAt is when the client was registered.
	RegisteredAt *time.Time `json:"registeredAt,omitempty"`
}

// ClientList represents a list of registered clients.
type ClientList struct {
	// Items is the list of clients.
	Items []RegisteredClient `json:"items"`
}

// ConfigRegisterClientOptions represents options for registering a client.
type ConfigRegisterClientOptions struct {
	// Name is the name of the client.
	Name string `json:"name,omitempty"`
	// URL is the URL of the client.
	URL string `json:"url,omitempty"`
}

// ConfigRemoveClientOptions represents options for removing a client.
type ConfigRemoveClientOptions struct {
	// Name is the name of the client.
	Name string `json:"name,omitempty"`
}

// ConfigListRegisteredClientsOptions represents options for listing registered clients.
type ConfigListRegisteredClientsOptions struct {
	// Format is the output format (json or text).
	Format string `json:"format,omitempty"`
}

// ConfigAutoDiscoveryOptions represents options for auto-discovering clients.
type ConfigAutoDiscoveryOptions struct {
	// Enable indicates whether to enable auto-discovery.
	Enable bool `json:"enable,omitempty"`
}

// ConfigSecretsProviderOptions represents options for setting the secrets provider.
type ConfigSecretsProviderOptions struct {
	// Provider is the secrets provider to use.
	Provider string `json:"provider,omitempty"`
}

// SearchOptions represents options for searching for MCP servers.
type SearchOptions struct {
	// Query is the search query.
	Query string `json:"query,omitempty"`
	// Format is the output format (json or text).
	Format string `json:"format,omitempty"`
}

// VersionInfo represents version information.
type VersionInfo struct {
	// Version is the version number.
	Version string `json:"version"`
	// GitCommit is the git commit hash.
	GitCommit string `json:"gitCommit,omitempty"`
	// BuildDate is when the binary was built.
	BuildDate string `json:"buildDate,omitempty"`
	// GoVersion is the version of Go used to build the binary.
	GoVersion string `json:"goVersion,omitempty"`
	// Platform is the platform the binary was built for.
	Platform string `json:"platform,omitempty"`
}

// VersionOptions represents options for showing version information.
type VersionOptions struct {
	// Format is the output format (json or text).
	Format string `json:"format,omitempty"`
}

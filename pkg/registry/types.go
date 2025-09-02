// Package registry provides access to the MCP server registry
package registry

import (
	"sort"
	"time"

	"github.com/stacklok/toolhive/pkg/permissions"
)

// Updates to the registry schema should be reflected in the JSON schema file located at pkg/registry/data/schema.json.
// The schema is used for validation and documentation purposes.
//
// The embedded registry.json is automatically validated against the schema during tests.
// See pkg/registry/schema_validation_test.go for the validation implementation.

// Registry represents the top-level structure of the MCP registry
type Registry struct {
	// Version is the schema version of the registry
	Version string `json:"version" yaml:"version"`
	// LastUpdated is the timestamp when the registry was last updated, in RFC3339 format
	LastUpdated string `json:"last_updated" yaml:"last_updated"`
	// Servers is a map of server names to their corresponding server definitions
	Servers map[string]*ImageMetadata `json:"servers" yaml:"servers"`
	// RemoteServers is a map of server names to their corresponding remote server definitions
	// These are MCP servers accessed via HTTP/HTTPS using the thv proxy command
	RemoteServers map[string]*RemoteServerMetadata `json:"remote_servers,omitempty" yaml:"remote_servers,omitempty"`
}

// BaseServerMetadata contains common fields shared between container and remote MCP servers
type BaseServerMetadata struct {
	// Name is the identifier for the MCP server, used when referencing the server in commands
	// If not provided, it will be auto-generated from the registry key
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// Description is a human-readable description of the server's purpose and functionality
	Description string `json:"description" yaml:"description"`
	// Tier represents the tier classification level of the server, e.g., "Official" or "Community"
	Tier string `json:"tier" yaml:"tier"`
	// Status indicates whether the server is currently active or deprecated
	Status string `json:"status" yaml:"status"`
	// Transport defines the communication protocol for the server
	// For containers: stdio, sse, or streamable-http
	// For remote servers: sse or streamable-http (stdio not supported)
	Transport string `json:"transport" yaml:"transport"`
	// Tools is a list of tool names provided by this MCP server
	Tools []string `json:"tools" yaml:"tools"`
	// Metadata contains additional information about the server such as popularity metrics
	Metadata *Metadata `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	// RepositoryURL is the URL to the source code repository for the server
	RepositoryURL string `json:"repository_url,omitempty" yaml:"repository_url,omitempty"`
	// Tags are categorization labels for the server to aid in discovery and filtering
	Tags []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	// CustomMetadata allows for additional user-defined metadata
	CustomMetadata map[string]any `json:"custom_metadata,omitempty" yaml:"custom_metadata,omitempty"`
}

// ImageMetadata represents the metadata for an MCP server image stored in our registry.
type ImageMetadata struct {
	BaseServerMetadata `yaml:",inline"`
	// Image is the Docker image reference for the MCP server
	Image string `json:"image" yaml:"image"`
	// TargetPort is the port for the container to expose (only applicable to SSE and Streamable HTTP transports)
	TargetPort int `json:"target_port,omitempty" yaml:"target_port,omitempty"`
	// Permissions defines the security profile and access permissions for the server
	Permissions *permissions.Profile `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	// EnvVars defines environment variables that can be passed to the server
	EnvVars []*EnvVar `json:"env_vars,omitempty" yaml:"env_vars,omitempty"`
	// Args are the default command-line arguments to pass to the MCP server container.
	// These arguments will be used only if no command-line arguments are provided by the user.
	// If the user provides arguments, they will override these defaults.
	Args []string `json:"args,omitempty" yaml:"args,omitempty"`
	// DockerTags lists the available Docker tags for this server image
	DockerTags []string `json:"docker_tags,omitempty" yaml:"docker_tags,omitempty"`
	// Provenance contains verification and signing metadata
	Provenance *Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

// Provenance contains metadata about the image's provenance and signing status
type Provenance struct {
	SigstoreURL       string               `json:"sigstore_url" yaml:"sigstore_url"`
	RepositoryURI     string               `json:"repository_uri" yaml:"repository_uri"`
	RepositoryRef     string               `json:"repository_ref,omitempty" yaml:"repository_ref,omitempty"`
	SignerIdentity    string               `json:"signer_identity" yaml:"signer_identity"`
	RunnerEnvironment string               `json:"runner_environment" yaml:"runner_environment"`
	CertIssuer        string               `json:"cert_issuer" yaml:"cert_issuer"`
	Attestation       *VerifiedAttestation `json:"attestation,omitempty" yaml:"attestation,omitempty"`
}

// VerifiedAttestation represents the verified attestation information
type VerifiedAttestation struct {
	PredicateType string `json:"predicate_type,omitempty" yaml:"predicate_type,omitempty"`
	Predicate     any    `json:"predicate,omitempty" yaml:"predicate,omitempty"`
}

// EnvVar represents an environment variable for an MCP server
type EnvVar struct {
	// Name is the environment variable name (e.g., API_KEY)
	Name string `json:"name" yaml:"name"`
	// Description is a human-readable explanation of the variable's purpose
	Description string `json:"description" yaml:"description"`
	// Required indicates whether this environment variable must be provided
	// If true and not provided via command line or secrets, the user will be prompted for a value
	Required bool `json:"required" yaml:"required"`
	// Default is the value to use if the environment variable is not explicitly provided
	// Only used for non-required variables
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
	// Secret indicates whether this environment variable contains sensitive information
	// If true, the value will be stored as a secret rather than as a plain environment variable
	Secret bool `json:"secret,omitempty" yaml:"secret,omitempty"`
}

// Header represents an HTTP header for remote MCP server authentication
type Header struct {
	// Name is the header name (e.g., X-API-Key, Authorization)
	Name string `json:"name" yaml:"name"`
	// Description is a human-readable explanation of the header's purpose
	Description string `json:"description" yaml:"description"`
	// Required indicates whether this header must be provided
	// If true and not provided via command line or secrets, the user will be prompted for a value
	Required bool `json:"required" yaml:"required"`
	// Default is the value to use if the header is not explicitly provided
	// Only used for non-required headers
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
	// Secret indicates whether this header contains sensitive information
	// If true, the value will be stored as a secret rather than as plain text
	Secret bool `json:"secret,omitempty" yaml:"secret,omitempty"`
	// Choices provides a list of valid values for the header (optional)
	Choices []string `json:"choices,omitempty" yaml:"choices,omitempty"`
}

// OAuthConfig represents OAuth/OIDC configuration for remote server authentication
type OAuthConfig struct {
	// Issuer is the OAuth/OIDC issuer URL (e.g., https://accounts.google.com)
	// Used for OIDC discovery to find authorization and token endpoints
	Issuer string `json:"issuer,omitempty" yaml:"issuer,omitempty"`
	// AuthorizeURL is the OAuth authorization endpoint URL
	// Used for non-OIDC OAuth flows when issuer is not provided
	AuthorizeURL string `json:"authorize_url,omitempty" yaml:"authorize_url,omitempty"`
	// TokenURL is the OAuth token endpoint URL
	// Used for non-OIDC OAuth flows when issuer is not provided
	TokenURL string `json:"token_url,omitempty" yaml:"token_url,omitempty"`
	// ClientID is the OAuth client ID for authentication
	ClientID string `json:"client_id,omitempty" yaml:"client_id,omitempty"`
	// Scopes are the OAuth scopes to request
	// If not specified, defaults to ["openid", "profile", "email"] for OIDC
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
	// UsePKCE indicates whether to use PKCE for the OAuth flow
	// Defaults to true for enhanced security
	UsePKCE bool `json:"use_pkce,omitempty" yaml:"use_pkce,omitempty"`
	// OAuthParams contains additional OAuth parameters to include in the authorization request
	// These are server-specific parameters like "prompt", "response_mode", etc.
	OAuthParams map[string]string `json:"oauth_params,omitempty" yaml:"oauth_params,omitempty"`
	// CallbackPort is the specific port to use for the OAuth callback server
	// If not specified, a random available port will be used
	CallbackPort int `json:"callback_port,omitempty" yaml:"callback_port,omitempty"`
}

// RemoteServerMetadata represents the metadata for a remote MCP server accessed via HTTP/HTTPS.
// Remote servers are accessed through the thv proxy command which handles authentication and tunneling.
type RemoteServerMetadata struct {
	BaseServerMetadata `yaml:",inline"`
	// URL is the endpoint URL for the remote MCP server (e.g., https://api.example.com/mcp)
	URL string `json:"url" yaml:"url"`
	// Headers defines HTTP headers that can be passed to the remote server for authentication
	// These are used with the thv proxy command's authentication features
	Headers []*Header `json:"headers,omitempty" yaml:"headers,omitempty"`
	// OAuthConfig provides OAuth/OIDC configuration for authentication to the remote server
	// Used with the thv proxy command's --remote-auth flags
	OAuthConfig *OAuthConfig `json:"oauth_config,omitempty" yaml:"oauth_config,omitempty"`
	// EnvVars defines environment variables that can be passed to configure the client
	// These might be needed for client-side configuration when connecting to the remote server
	EnvVars []*EnvVar `json:"env_vars,omitempty" yaml:"env_vars,omitempty"`
}

// Metadata represents metadata about an MCP server
type Metadata struct {
	// Stars represents the popularity rating or number of stars for the server
	Stars int `json:"stars" yaml:"stars"`
	// Pulls indicates how many times the server image has been downloaded
	Pulls int `json:"pulls" yaml:"pulls"`
	// LastUpdated is the timestamp when the server was last updated, in RFC3339 format
	LastUpdated string `json:"last_updated" yaml:"last_updated"`
}

// ParsedTime returns the LastUpdated field as a time.Time
func (m *Metadata) ParsedTime() (time.Time, error) {
	return time.Parse(time.RFC3339, m.LastUpdated)
}

// ServerMetadata is an interface that both ImageMetadata and RemoteServerMetadata implement
type ServerMetadata interface {
	// GetName returns the server name
	GetName() string
	// GetDescription returns the server description
	GetDescription() string
	// GetTier returns the server tier
	GetTier() string
	// GetStatus returns the server status
	GetStatus() string
	// GetTransport returns the server transport
	GetTransport() string
	// GetTools returns the list of tools provided by the server
	GetTools() []string
	// GetMetadata returns the server metadata
	GetMetadata() *Metadata
	// GetRepositoryURL returns the repository URL
	GetRepositoryURL() string
	// GetTags returns the server tags
	GetTags() []string
	// GetCustomMetadata returns custom metadata
	GetCustomMetadata() map[string]any
	// IsRemote returns true if this is a remote server
	IsRemote() bool
	// GetEnvVars returns environment variables
	GetEnvVars() []*EnvVar
}

// Implement ServerMetadata interface for ImageMetadata

// GetName returns the server name
func (i *ImageMetadata) GetName() string {
	if i == nil {
		return ""
	}
	return i.Name
}

// GetDescription returns the server description
func (i *ImageMetadata) GetDescription() string {
	if i == nil {
		return ""
	}
	return i.Description
}

// GetTier returns the server tier
func (i *ImageMetadata) GetTier() string {
	if i == nil {
		return ""
	}
	return i.Tier
}

// GetStatus returns the server status
func (i *ImageMetadata) GetStatus() string {
	if i == nil {
		return ""
	}
	return i.Status
}

// GetTransport returns the server transport
func (i *ImageMetadata) GetTransport() string {
	if i == nil {
		return ""
	}
	return i.Transport
}

// GetTools returns the list of tools provided by the server
func (i *ImageMetadata) GetTools() []string {
	if i == nil {
		return nil
	}
	return i.Tools
}

// GetMetadata returns the server metadata
func (i *ImageMetadata) GetMetadata() *Metadata {
	if i == nil {
		return nil
	}
	return i.Metadata
}

// GetRepositoryURL returns the repository URL
func (i *ImageMetadata) GetRepositoryURL() string {
	if i == nil {
		return ""
	}
	return i.RepositoryURL
}

// GetTags returns the server tags
func (i *ImageMetadata) GetTags() []string {
	if i == nil {
		return nil
	}
	return i.Tags
}

// GetCustomMetadata returns custom metadata
func (i *ImageMetadata) GetCustomMetadata() map[string]any {
	if i == nil {
		return nil
	}
	return i.CustomMetadata
}

// IsRemote returns false for container servers
func (*ImageMetadata) IsRemote() bool {
	return false
}

// GetEnvVars returns environment variables
func (i *ImageMetadata) GetEnvVars() []*EnvVar {
	if i == nil {
		return nil
	}
	return i.EnvVars
}

// Implement ServerMetadata interface for RemoteServerMetadata

// GetName returns the server name
func (r *RemoteServerMetadata) GetName() string {
	if r == nil {
		return ""
	}
	return r.Name
}

// GetDescription returns the server description
func (r *RemoteServerMetadata) GetDescription() string {
	if r == nil {
		return ""
	}
	return r.Description
}

// GetTier returns the server tier
func (r *RemoteServerMetadata) GetTier() string {
	if r == nil {
		return ""
	}
	return r.Tier
}

// GetStatus returns the server status
func (r *RemoteServerMetadata) GetStatus() string {
	if r == nil {
		return ""
	}
	return r.Status
}

// GetTransport returns the server transport
func (r *RemoteServerMetadata) GetTransport() string {
	if r == nil {
		return ""
	}
	return r.Transport
}

// GetTools returns the list of tools provided by the server
func (r *RemoteServerMetadata) GetTools() []string {
	if r == nil {
		return nil
	}
	return r.Tools
}

// GetMetadata returns the server metadata
func (r *RemoteServerMetadata) GetMetadata() *Metadata {
	if r == nil {
		return nil
	}
	return r.Metadata
}

// GetRepositoryURL returns the repository URL
func (r *RemoteServerMetadata) GetRepositoryURL() string {
	if r == nil {
		return ""
	}
	return r.RepositoryURL
}

// GetTags returns the server tags
func (r *RemoteServerMetadata) GetTags() []string {
	if r == nil {
		return nil
	}
	return r.Tags
}

// GetCustomMetadata returns custom metadata
func (r *RemoteServerMetadata) GetCustomMetadata() map[string]any {
	if r == nil {
		return nil
	}
	return r.CustomMetadata
}

// IsRemote returns true for remote servers
func (*RemoteServerMetadata) IsRemote() bool {
	return true
}

// GetEnvVars returns environment variables
func (r *RemoteServerMetadata) GetEnvVars() []*EnvVar {
	if r == nil {
		return nil
	}
	return r.EnvVars
}

// GetRawImplementation returns the underlying RemoteServerMetadata pointer
func (r *RemoteServerMetadata) GetRawImplementation() any {
	if r == nil {
		return nil
	}
	return r
}

// GetAllServers returns all servers (both container and remote) as a unified list
func (reg *Registry) GetAllServers() []ServerMetadata {
	servers := make([]ServerMetadata, 0, len(reg.Servers)+len(reg.RemoteServers))

	// Add container servers
	for _, server := range reg.Servers {
		servers = append(servers, server)
	}

	// Add remote servers
	for _, server := range reg.RemoteServers {
		servers = append(servers, server)
	}

	return servers
}

// GetServerByName returns a server by name (either container or remote)
func (reg *Registry) GetServerByName(name string) (ServerMetadata, bool) {
	// Check container servers first
	if server, ok := reg.Servers[name]; ok {
		return server, true
	}

	// Check remote servers
	if server, ok := reg.RemoteServers[name]; ok {
		return server, true
	}

	return nil, false
}

// SortServersByName sorts a slice of ServerMetadata by name
func SortServersByName(servers []ServerMetadata) {
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].GetName() < servers[j].GetName()
	})
}

// Package registry provides access to the MCP server registry
package registry

// UpstreamServerDetail represents the top-level server.json format as defined in the MCP community registry
// This follows the schema at https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json
type UpstreamServerDetail struct {
	// Schema is the JSON Schema URI for the server.json format
	Schema string `json:"$schema,omitempty" yaml:"$schema,omitempty"`
	// Name is the server name in reverse-DNS format (e.g., "io.github.user/weather")
	Name string `json:"name" yaml:"name"`
	// Description is a human-readable explanation of server functionality
	Description string `json:"description" yaml:"description"`
	// Version is the server version (should follow semantic versioning)
	Version string `json:"version" yaml:"version"`
	// Title is an optional human-readable display name
	Title string `json:"title,omitempty" yaml:"title,omitempty"`
	// WebsiteURL is an optional URL to the server's homepage or documentation
	WebsiteURL string `json:"websiteUrl,omitempty" yaml:"websiteUrl,omitempty"`
	// Repository contains optional repository metadata
	Repository *UpstreamRepository `json:"repository,omitempty" yaml:"repository,omitempty"`
	// Icons are optional sized icons for display in user interfaces
	Icons []UpstreamIcon `json:"icons,omitempty" yaml:"icons,omitempty"`
	// Packages contains package installation options
	Packages []UpstreamPackage `json:"packages,omitempty" yaml:"packages,omitempty"`
	// Remotes contains remote server connection options
	Remotes []UpstreamRemote `json:"remotes,omitempty" yaml:"remotes,omitempty"`
	// Meta contains extension metadata using reverse DNS namespacing
	Meta *UpstreamMeta `json:"_meta,omitempty" yaml:"_meta,omitempty"`
}

// UpstreamRepository contains repository information
type UpstreamRepository struct {
	// URL is the repository URL
	URL string `json:"url" yaml:"url"`
	// Source is the repository hosting service (e.g., "github")
	Source string `json:"source" yaml:"source"`
	// ID is an optional repository identifier from the hosting service
	ID string `json:"id,omitempty" yaml:"id,omitempty"`
	// Subfolder is an optional relative path to the server within a monorepo
	Subfolder string `json:"subfolder,omitempty" yaml:"subfolder,omitempty"`
}

// UpstreamIcon represents an optionally-sized icon for display in user interfaces
type UpstreamIcon struct {
	// Src is the HTTPS URL pointing to the icon resource
	Src string `json:"src" yaml:"src"`
	// MimeType is an optional MIME type override
	MimeType string `json:"mimeType,omitempty" yaml:"mimeType,omitempty"`
	// Sizes specifies available icon sizes (e.g., "48x48", "96x96", "any")
	Sizes []string `json:"sizes,omitempty" yaml:"sizes,omitempty"`
	// Theme indicates the intended background (light/dark)
	Theme string `json:"theme,omitempty" yaml:"theme,omitempty"`
}

// UpstreamPackage represents a package installation option
type UpstreamPackage struct {
	// RegistryType indicates how to download packages (e.g., "npm", "pypi", "oci", "nuget", "mcpb")
	RegistryType string `json:"registryType" yaml:"registryType"`
	// RegistryBaseURL is the base URL of the package registry
	RegistryBaseURL string `json:"registryBaseUrl,omitempty" yaml:"registryBaseUrl,omitempty"`
	// Identifier is the package name or URL
	Identifier string `json:"identifier" yaml:"identifier"`
	// Version is the package version (must be specific, not a range)
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	// FileSha256 is the SHA-256 hash for integrity verification (required for MCPB)
	FileSha256 string `json:"fileSha256,omitempty" yaml:"fileSha256,omitempty"`
	// RuntimeHint provides a hint for the appropriate runtime (e.g., "npx", "uvx", "docker", "dnx")
	RuntimeHint string `json:"runtimeHint,omitempty" yaml:"runtimeHint,omitempty"`
	// RuntimeArguments are arguments passed to the package's runtime command
	RuntimeArguments []UpstreamArgument `json:"runtimeArguments,omitempty" yaml:"runtimeArguments,omitempty"`
	// PackageArguments are arguments passed to the package's binary
	PackageArguments []UpstreamArgument `json:"packageArguments,omitempty" yaml:"packageArguments,omitempty"`
	// EnvironmentVariables are environment variables for the package
	EnvironmentVariables []UpstreamKeyValueInput `json:"environmentVariables,omitempty" yaml:"environmentVariables,omitempty"`
	// Transport is the transport protocol configuration
	Transport UpstreamTransport `json:"transport" yaml:"transport"`
}

// UpstreamRemote represents a remote server connection option
// Remotes are discriminated by the "type" field and can be SSE or Streamable HTTP
type UpstreamRemote struct {
	// Type is the transport protocol type ("sse" or "streamable-http")
	Type UpstreamTransportType `json:"type" yaml:"type"`
	// URL is the remote server URL
	URL string `json:"url" yaml:"url"`
	// Headers are HTTP headers to include (for sse and streamable-http)
	Headers []UpstreamKeyValueInput `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// UpstreamTransport represents the transport configuration for a package
// This can be stdio, sse, or streamable-http
type UpstreamTransport struct {
	// Type is the transport protocol type
	Type UpstreamTransportType `json:"type" yaml:"type"`
	// URL is the server URL (for sse and streamable-http transports)
	URL string `json:"url,omitempty" yaml:"url,omitempty"`
	// Headers are HTTP headers to include (for sse and streamable-http transports)
	Headers []UpstreamKeyValueInput `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// UpstreamTransportType represents the transport protocol type
type UpstreamTransportType string

const (
	// UpstreamTransportTypeStdio represents standard input/output transport
	UpstreamTransportTypeStdio UpstreamTransportType = "stdio"
	// UpstreamTransportTypeSSE represents Server-Sent Events transport
	UpstreamTransportTypeSSE UpstreamTransportType = "sse"
	// UpstreamTransportTypeStreamable represents streamable HTTP transport
	UpstreamTransportTypeStreamable UpstreamTransportType = "streamable-http"
)

// UpstreamArgument represents a command-line argument (positional or named)
type UpstreamArgument struct {
	// Type is the argument type ("positional" or "named")
	Type UpstreamArgumentType `json:"type" yaml:"type"`
	// Name is the flag name for named arguments (including leading dashes)
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// ValueHint is an identifier for positional arguments (not part of command line)
	ValueHint string `json:"valueHint,omitempty" yaml:"valueHint,omitempty"`
	// Description describes the argument's purpose
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// IsRequired indicates if the argument is required
	IsRequired bool `json:"isRequired,omitempty" yaml:"isRequired,omitempty"`
	// IsRepeated indicates if the argument can be repeated
	IsRepeated bool `json:"isRepeated,omitempty" yaml:"isRepeated,omitempty"`
	// Format specifies the input format
	Format UpstreamInputFormat `json:"format,omitempty" yaml:"format,omitempty"`
	// Value is the default or fixed value
	Value string `json:"value,omitempty" yaml:"value,omitempty"`
	// IsSecret indicates if the value is sensitive
	IsSecret bool `json:"isSecret,omitempty" yaml:"isSecret,omitempty"`
	// Default is the default value
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
	// Placeholder provides examples or guidance about expected input
	Placeholder string `json:"placeholder,omitempty" yaml:"placeholder,omitempty"`
	// Choices are valid values for the argument
	Choices []string `json:"choices,omitempty" yaml:"choices,omitempty"`
	// Variables are variable substitutions for the value
	Variables map[string]UpstreamInput `json:"variables,omitempty" yaml:"variables,omitempty"`
}

// UpstreamArgumentType represents the type of command-line argument
type UpstreamArgumentType string

const (
	// UpstreamArgumentTypePositional represents a positional argument
	UpstreamArgumentTypePositional UpstreamArgumentType = "positional"
	// UpstreamArgumentTypeNamed represents a named argument (flag)
	UpstreamArgumentTypeNamed UpstreamArgumentType = "named"
)

// UpstreamKeyValueInput represents a key-value input (environment variable or header)
type UpstreamKeyValueInput struct {
	// Name is the variable or header name
	Name string `json:"name" yaml:"name"`
	// Description describes the input's purpose
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// IsRequired indicates if the input is required
	IsRequired bool `json:"isRequired,omitempty" yaml:"isRequired,omitempty"`
	// Format specifies the input format
	Format UpstreamInputFormat `json:"format,omitempty" yaml:"format,omitempty"`
	// Value is the default or fixed value (supports variable substitution)
	Value string `json:"value,omitempty" yaml:"value,omitempty"`
	// IsSecret indicates if the value is sensitive
	IsSecret bool `json:"isSecret,omitempty" yaml:"isSecret,omitempty"`
	// Default is the default value
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
	// Placeholder provides examples or guidance about expected input
	Placeholder string `json:"placeholder,omitempty" yaml:"placeholder,omitempty"`
	// Choices are valid values for the input
	Choices []string `json:"choices,omitempty" yaml:"choices,omitempty"`
	// Variables are variable substitutions for the value
	Variables map[string]UpstreamInput `json:"variables,omitempty" yaml:"variables,omitempty"`
}

// UpstreamInput represents a generic input with validation and formatting
type UpstreamInput struct {
	// Description describes the input's purpose
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// IsRequired indicates if the input is required
	IsRequired bool `json:"isRequired,omitempty" yaml:"isRequired,omitempty"`
	// Format specifies the input format
	Format UpstreamInputFormat `json:"format,omitempty" yaml:"format,omitempty"`
	// Value is the default or fixed value
	Value string `json:"value,omitempty" yaml:"value,omitempty"`
	// IsSecret indicates if the value is sensitive
	IsSecret bool `json:"isSecret,omitempty" yaml:"isSecret,omitempty"`
	// Default is the default value
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
	// Placeholder provides examples or guidance about expected input
	Placeholder string `json:"placeholder,omitempty" yaml:"placeholder,omitempty"`
	// Choices are valid values for the input
	Choices []string `json:"choices,omitempty" yaml:"choices,omitempty"`
}

// UpstreamInputFormat represents the format of an input value
type UpstreamInputFormat string

const (
	// UpstreamInputFormatString represents a string input
	UpstreamInputFormatString UpstreamInputFormat = "string"
	// UpstreamInputFormatNumber represents a numeric input
	UpstreamInputFormatNumber UpstreamInputFormat = "number"
	// UpstreamInputFormatBoolean represents a boolean input
	UpstreamInputFormatBoolean UpstreamInputFormat = "boolean"
	// UpstreamInputFormatFilepath represents a file path input
	UpstreamInputFormatFilepath UpstreamInputFormat = "filepath"
)

// UpstreamMeta contains extension metadata using reverse DNS namespacing
type UpstreamMeta struct {
	// PublisherProvided contains publisher-provided metadata for downstream registries
	PublisherProvided map[string]any `json:"io.modelcontextprotocol.registry/publisher-provided,omitempty" yaml:"io.modelcontextprotocol.registry/publisher-provided,omitempty"` //nolint:lll
	// ToolhiveExtension contains ToolHive-specific metadata
	ToolhiveExtension *ToolhiveMetadataExtension `json:"dev.toolhive,omitempty" yaml:"dev.toolhive,omitempty"`
}

// ToolhiveMetadataExtension contains ToolHive-specific metadata in the _meta field
type ToolhiveMetadataExtension struct {
	// Tier represents the tier classification level of the server
	Tier string `json:"tier,omitempty" yaml:"tier,omitempty"`
	// Tools is a list of tool names provided by this MCP server
	Tools []string `json:"tools,omitempty" yaml:"tools,omitempty"`
	// Metadata contains additional information about the server
	Metadata *Metadata `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	// Tags are categorization labels for the server
	Tags []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	// CustomMetadata allows for additional user-defined metadata
	CustomMetadata map[string]any `json:"custom_metadata,omitempty" yaml:"custom_metadata,omitempty"`
	// TargetPort is the port for the container to expose (only applicable to SSE and Streamable HTTP transports)
	TargetPort int `json:"target_port,omitempty" yaml:"target_port,omitempty"`
	// DockerTags lists the available Docker tags for this server image
	DockerTags []string `json:"docker_tags,omitempty" yaml:"docker_tags,omitempty"`
	// Provenance contains verification and signing metadata
	Provenance *Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

// Package registry provides access to the MCP server registry
package registry

// UpstreamServerDetail represents the upstream MCP server format as defined in the community registry
// This follows the schema at https://modelcontextprotocol.io/schemas/draft/2025-07-09/server.json
type UpstreamServerDetail struct {
	// Server contains the core server information
	Server UpstreamServer `json:"server" yaml:"server"`
	// XPublisher contains optional publisher metadata (extension field)
	XPublisher *UpstreamPublisher `json:"x-publisher,omitempty" yaml:"x-publisher,omitempty"`
}

// UpstreamServer represents the core server information in the upstream format
type UpstreamServer struct {
	// Name is the server name/identifier (e.g., "io.modelcontextprotocol/filesystem")
	Name string `json:"name" yaml:"name"`
	// Description is a human-readable description of the server's functionality
	Description string `json:"description" yaml:"description"`
	// Status indicates the server lifecycle status
	Status UpstreamServerStatus `json:"status,omitempty" yaml:"status,omitempty"`
	// Repository contains repository information
	Repository *UpstreamRepository `json:"repository,omitempty" yaml:"repository,omitempty"`
	// VersionDetail contains version information
	VersionDetail UpstreamVersionDetail `json:"version_detail" yaml:"version_detail"`
	// Packages contains package installation options
	Packages []UpstreamPackage `json:"packages,omitempty" yaml:"packages,omitempty"`
	// Remotes contains remote server connection options
	Remotes []UpstreamRemote `json:"remotes,omitempty" yaml:"remotes,omitempty"`
}

// UpstreamServerStatus represents the server lifecycle status
type UpstreamServerStatus string

const (
	// UpstreamServerStatusActive indicates the server is actively maintained
	UpstreamServerStatusActive UpstreamServerStatus = "active"
	// UpstreamServerStatusDeprecated indicates the server is deprecated
	UpstreamServerStatusDeprecated UpstreamServerStatus = "deprecated"
)

// UpstreamRepository contains repository information
type UpstreamRepository struct {
	// URL is the repository URL
	URL string `json:"url" yaml:"url"`
	// Source is the repository hosting service (e.g., "github")
	Source string `json:"source" yaml:"source"`
	// ID is an optional repository identifier
	ID string `json:"id,omitempty" yaml:"id,omitempty"`
}

// UpstreamVersionDetail contains version information
type UpstreamVersionDetail struct {
	// Version is the server version (equivalent to Implementation.version in MCP spec)
	Version string `json:"version" yaml:"version"`
}

// UpstreamPackage represents a package installation option
type UpstreamPackage struct {
	// RegistryName is the package registry type (e.g., "npm", "pypi", "docker", "nuget")
	RegistryName string `json:"registry_name" yaml:"registry_name"`
	// Name is the package name in the registry
	Name string `json:"name" yaml:"name"`
	// Version is the package version
	Version string `json:"version" yaml:"version"`
	// RuntimeHint provides a hint for the appropriate runtime (e.g., "npx", "uvx", "dnx")
	RuntimeHint string `json:"runtime_hint,omitempty" yaml:"runtime_hint,omitempty"`
	// RuntimeArguments are arguments passed to the package's runtime command
	RuntimeArguments []UpstreamArgument `json:"runtime_arguments,omitempty" yaml:"runtime_arguments,omitempty"`
	// PackageArguments are arguments passed to the package's binary
	PackageArguments []UpstreamArgument `json:"package_arguments,omitempty" yaml:"package_arguments,omitempty"`
	// EnvironmentVariables are environment variables for the package
	EnvironmentVariables []UpstreamKeyValueInput `json:"environment_variables,omitempty" yaml:"environment_variables,omitempty"`
}

// UpstreamRemote represents a remote server connection option
type UpstreamRemote struct {
	// TransportType is the transport protocol type
	TransportType UpstreamTransportType `json:"transport_type" yaml:"transport_type"`
	// URL is the remote server URL
	URL string `json:"url" yaml:"url"`
	// Headers are HTTP headers to include
	Headers []UpstreamKeyValueInput `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// UpstreamTransportType represents the transport protocol type
type UpstreamTransportType string

const (
	// UpstreamTransportTypeStreamable represents streamable HTTP transport
	UpstreamTransportTypeStreamable UpstreamTransportType = "streamable"
	// UpstreamTransportTypeSSE represents Server-Sent Events transport
	UpstreamTransportTypeSSE UpstreamTransportType = "sse"
)

// UpstreamArgument represents a command-line argument
type UpstreamArgument struct {
	// Type is the argument type
	Type UpstreamArgumentType `json:"type" yaml:"type"`
	// Name is the flag name for named arguments (including leading dashes)
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// ValueHint is an identifier-like hint for positional arguments
	ValueHint string `json:"value_hint,omitempty" yaml:"value_hint,omitempty"`
	// Description describes the argument's purpose
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// IsRequired indicates if the argument is required
	IsRequired bool `json:"is_required,omitempty" yaml:"is_required,omitempty"`
	// IsRepeated indicates if the argument can be repeated
	IsRepeated bool `json:"is_repeated,omitempty" yaml:"is_repeated,omitempty"`
	// Format specifies the input format
	Format UpstreamInputFormat `json:"format,omitempty" yaml:"format,omitempty"`
	// Value is the default or fixed value
	Value string `json:"value,omitempty" yaml:"value,omitempty"`
	// IsSecret indicates if the value is sensitive
	IsSecret bool `json:"is_secret,omitempty" yaml:"is_secret,omitempty"`
	// Default is the default value
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
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
	IsRequired bool `json:"is_required,omitempty" yaml:"is_required,omitempty"`
	// Format specifies the input format
	Format UpstreamInputFormat `json:"format,omitempty" yaml:"format,omitempty"`
	// Value is the default or fixed value
	Value string `json:"value,omitempty" yaml:"value,omitempty"`
	// IsSecret indicates if the value is sensitive
	IsSecret bool `json:"is_secret,omitempty" yaml:"is_secret,omitempty"`
	// Default is the default value
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
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
	IsRequired bool `json:"is_required,omitempty" yaml:"is_required,omitempty"`
	// Format specifies the input format
	Format UpstreamInputFormat `json:"format,omitempty" yaml:"format,omitempty"`
	// Value is the default or fixed value
	Value string `json:"value,omitempty" yaml:"value,omitempty"`
	// IsSecret indicates if the value is sensitive
	IsSecret bool `json:"is_secret,omitempty" yaml:"is_secret,omitempty"`
	// Default is the default value
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
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

// UpstreamPublisher contains optional publisher metadata (extension field)
type UpstreamPublisher struct {
	// Tool is the name of the publishing tool
	Tool string `json:"tool,omitempty" yaml:"tool,omitempty"`
	// Version is the version of the publishing tool
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	// BuildInfo contains additional build metadata
	BuildInfo map[string]any `json:"build_info,omitempty" yaml:"build_info,omitempty"`
}

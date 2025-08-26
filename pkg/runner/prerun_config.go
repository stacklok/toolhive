package runner

import (
	"fmt"
	"net/url"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/registry"
)

// PreRunConfigType represents the different types of MCP server sources
type PreRunConfigType string

const (
	// PreRunConfigTypeRegistry indicates the source is a server name from the registry
	PreRunConfigTypeRegistry PreRunConfigType = "registry"
	// PreRunConfigTypeContainerImage indicates the source is a direct container image reference
	PreRunConfigTypeContainerImage PreRunConfigType = "container_image"
	// PreRunConfigTypeProtocolScheme indicates the source is a protocol scheme (uvx://, npx://, go://)
	PreRunConfigTypeProtocolScheme PreRunConfigType = "protocol_scheme"
	// PreRunConfigTypeRemoteURL indicates the source is a remote HTTP/HTTPS URL
	PreRunConfigTypeRemoteURL PreRunConfigType = "remote_url"
	// PreRunConfigTypeConfigFile indicates the source is a configuration file
	PreRunConfigTypeConfigFile PreRunConfigType = "config_file"
)

// PreRunConfig represents the parsed and classified input before building a RunConfig
type PreRunConfig struct {
	// Type indicates what kind of source this is
	Type PreRunConfigType

	// Source is the original input string
	Source string

	// ParsedSource contains type-specific parsed information
	ParsedSource interface{}

	// Metadata contains any additional metadata discovered during parsing
	Metadata map[string]interface{}
}

// RegistrySource represents a server from the registry
type RegistrySource struct {
	ServerName string
	IsRemote   bool
}

// ContainerImageSource represents a direct container image reference
type ContainerImageSource struct {
	ImageRef string
	Registry string
	Name     string
	Tag      string
}

// ProtocolSchemeSource represents a protocol scheme build
type ProtocolSchemeSource struct {
	ProtocolTransportType templates.TransportType // TransportTypeUVX, TransportTypeNPX, TransportTypeGO
	Package               string                  // everything after the ://
	IsLocalPath           bool                    // true for go://./local-path
}

// RemoteURLSource represents a remote MCP server URL
type RemoteURLSource struct {
	URL       string
	ParsedURL *url.URL
}

// ConfigFileSource represents a configuration file
type ConfigFileSource struct {
	FilePath string
}

// ParsePreRunConfig analyzes the input and creates a PreRunConfig
func ParsePreRunConfig(source string, fromConfigPath string) (*PreRunConfig, error) {
	preConfig := &PreRunConfig{
		Source:   source,
		Metadata: make(map[string]interface{}),
	}

	// 1. Check if loading from config file
	if fromConfigPath != "" {
		preConfig.Type = PreRunConfigTypeConfigFile
		preConfig.ParsedSource = &ConfigFileSource{
			FilePath: fromConfigPath,
		}
		return preConfig, nil
	}

	// 2. Check if it's a remote URL
	if networking.IsURL(source) {
		parsedURL, err := url.Parse(source)
		if err != nil {
			return nil, fmt.Errorf("invalid URL: %w", err)
		}

		preConfig.Type = PreRunConfigTypeRemoteURL
		preConfig.ParsedSource = &RemoteURLSource{
			URL:       source,
			ParsedURL: parsedURL,
		}
		return preConfig, nil
	}

	// 3. Check if it's a protocol scheme
	if IsImageProtocolScheme(source) {
		transportType, packageName, err := parseProtocolScheme(source)
		if err != nil {
			return nil, fmt.Errorf("failed to parse protocol scheme: %w", err)
		}

		isLocal := transportType == templates.TransportTypeGO && isLocalGoPath(packageName)

		preConfig.Type = PreRunConfigTypeProtocolScheme
		preConfig.ParsedSource = &ProtocolSchemeSource{
			ProtocolTransportType: transportType,
			Package:               packageName,
			IsLocalPath:           isLocal,
		}
		return preConfig, nil
	}

	// 4. Try to find in registry
	provider, err := registry.GetDefaultProvider()
	if err == nil {
		server, err := provider.GetServer(source)
		if err == nil {
			preConfig.Type = PreRunConfigTypeRegistry
			preConfig.ParsedSource = &RegistrySource{
				ServerName: source,
				IsRemote:   server.IsRemote(),
			}
			return preConfig, nil
		}
	}

	// 5. Default to container image reference
	registryName, name, tag, err := parseContainerImageRef(source)
	if err != nil {
		return nil, fmt.Errorf("failed to parse container image reference: %w", err)
	}

	preConfig.Type = PreRunConfigTypeContainerImage
	preConfig.ParsedSource = &ContainerImageSource{
		ImageRef: source,
		Registry: registryName,
		Name:     name,
		Tag:      tag,
	}

	return preConfig, nil
}

// parseContainerImageRef parses a container image reference using go-containerregistry
func parseContainerImageRef(imageRef string) (registryName, name, tag string, err error) {
	ref, err := nameref.ParseReference(imageRef)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid image reference: %w", err)
	}

	registryName = ref.Context().RegistryStr()
	name = ref.Context().RepositoryStr()

	// Check if it's a tagged reference or digest
	if taggedRef, ok := ref.(nameref.Tag); ok {
		tag = taggedRef.TagStr()
	} else if digestRef, ok := ref.(nameref.Digest); ok {
		tag = digestRef.DigestStr()
	} else {
		tag = "latest"
	}

	return registryName, name, tag, nil
}

// String returns a human-readable description of the PreRunConfig
func (p *PreRunConfig) String() string {
	switch p.Type {
	case PreRunConfigTypeRegistry:
		src := p.ParsedSource.(*RegistrySource)
		if src.IsRemote {
			return fmt.Sprintf("Registry remote server: %s", src.ServerName)
		}
		return fmt.Sprintf("Registry container server: %s", src.ServerName)
	case PreRunConfigTypeContainerImage:
		src := p.ParsedSource.(*ContainerImageSource)
		return fmt.Sprintf("Container image: %s:%s", src.Name, src.Tag)
	case PreRunConfigTypeProtocolScheme:
		src := p.ParsedSource.(*ProtocolSchemeSource)
		if src.IsLocalPath {
			return fmt.Sprintf("Protocol scheme %s (local): %s", src.ProtocolTransportType, src.Package)
		}
		return fmt.Sprintf("Protocol scheme %s: %s", src.ProtocolTransportType, src.Package)
	case PreRunConfigTypeRemoteURL:
		src := p.ParsedSource.(*RemoteURLSource)
		return fmt.Sprintf("Remote URL: %s", src.URL)
	case PreRunConfigTypeConfigFile:
		src := p.ParsedSource.(*ConfigFileSource)
		return fmt.Sprintf("Config file: %s", src.FilePath)
	default:
		return fmt.Sprintf("Unknown type: %s", p.Source)
	}
}

// IsRemote returns true if this PreRunConfig represents a remote MCP server
func (p *PreRunConfig) IsRemote() bool {
	switch p.Type {
	case PreRunConfigTypeRemoteURL:
		return true
	case PreRunConfigTypeRegistry:
		src := p.ParsedSource.(*RegistrySource)
		return src.IsRemote
	case PreRunConfigTypeContainerImage, PreRunConfigTypeProtocolScheme, PreRunConfigTypeConfigFile:
		return false
	default:
		return false
	}
}

// RequiresBuild returns true if this PreRunConfig requires building a container image
func (p *PreRunConfig) RequiresBuild() bool {
	switch p.Type {
	case PreRunConfigTypeProtocolScheme:
		return true
	case PreRunConfigTypeRegistry, PreRunConfigTypeContainerImage, PreRunConfigTypeRemoteURL, PreRunConfigTypeConfigFile:
		return false
	default:
		return false
	}
}

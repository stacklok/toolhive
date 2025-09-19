package sources

import (
	"context"
	"encoding/json"
	"fmt"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

// SourceDataValidator is an interface for validating registry source configurations
type SourceDataValidator interface {
	// ValidateData validates raw data and returns a parsed Registry
	ValidateData(data []byte, format string) (*registry.Registry, error)
}

//go:generate mockgen -destination=mocks/mock_source_handler.go -package=mocks -source=types.go SourceHandler,SourceHandlerFactory

// SourceHandler is an interface with methods to fetch data from external data sources
type SourceHandler interface {
	// FetchRegistry retrieves data from the source and returns the result
	FetchRegistry(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (*FetchResult, error)

	// Validate validates the source configuration
	Validate(source *mcpv1alpha1.MCPRegistrySource) error

	// CurrentHash returns the current hash of the source data without performing a full fetch
	CurrentHash(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (string, error)
}

// FetchResult contains the result of a fetch operation
type FetchResult struct {
	// Registry is the parsed registry data (replaces raw Data field)
	Registry *registry.Registry

	// Hash is the SHA256 hash of the serialized data for change detection
	Hash string

	// ServerCount is the number of servers found in the registry data
	ServerCount int

	// Format indicates the original format of the source data
	Format string
}

// NewFetchResult creates a new FetchResult from a Registry instance and pre-calculated hash
// The hash should be calculated by the source handler to ensure consistency with CurrentHash
func NewFetchResult(reg *registry.Registry, hash string, format string) *FetchResult {
	serverCount := len(reg.Servers) + len(reg.RemoteServers)

	return &FetchResult{
		Registry:    reg,
		Hash:        hash,
		ServerCount: serverCount,
		Format:      format,
	}
}

// SourceHandlerFactory creates source handlers based on source type
type SourceHandlerFactory interface {
	// CreateHandler creates a source handler for the given source type
	CreateHandler(sourceType string) (SourceHandler, error)
}

// DefaultSourceDataValidator is the default implementation of SourceValidator
type DefaultSourceDataValidator struct{}

// NewSourceDataValidator creates a new default source validator
func NewSourceDataValidator() SourceDataValidator {
	return &DefaultSourceDataValidator{}
}

// ValidateData validates raw data and returns a parsed Registry
func (*DefaultSourceDataValidator) ValidateData(data []byte, format string) (*registry.Registry, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("data cannot be empty")
	}

	switch format {
	case mcpv1alpha1.RegistryFormatToolHive:
		return validateToolhiveFormatAndParse(data)
	case mcpv1alpha1.RegistryFormatUpstream:
		return validateUpstreamFormatAndParse(data)
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}
}

// validateToolhiveFormatAndParse validates data against ToolHive registry format and returns parsed Registry
func validateToolhiveFormatAndParse(data []byte) (*registry.Registry, error) {
	// Use the existing schema validation from pkg/registry
	if err := registry.ValidateRegistrySchema(data); err != nil {
		return nil, err
	}

	// Parse the validated data
	var reg registry.Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("failed to parse ToolHive registry format: %w", err)
	}

	return &reg, nil
}

// validateUpstreamFormatAndParse validates data against upstream registry format and returns converted Registry
func validateUpstreamFormatAndParse(data []byte) (*registry.Registry, error) {
	// Parse as upstream format to validate structure
	var upstreamServers []registry.UpstreamServerDetail
	if err := json.Unmarshal(data, &upstreamServers); err != nil {
		return nil, fmt.Errorf("invalid upstream format: %w", err)
	}

	// Basic validation - ensure we have at least one server and required fields
	if len(upstreamServers) == 0 {
		return nil, fmt.Errorf("upstream registry must contain at least one server")
	}

	for i, server := range upstreamServers {
		if server.Server.Name == "" {
			return nil, fmt.Errorf("server at index %d: name is required", i)
		}
		if server.Server.Description == "" {
			return nil, fmt.Errorf("server at index %d (%s): description is required", i, server.Server.Name)
		}
	}

	// Convert upstream format to ToolHive Registry format
	toolhiveRegistry := &registry.Registry{
		Version:       "1.0",
		LastUpdated:   "", // Will be set during sync
		Servers:       make(map[string]*registry.ImageMetadata),
		RemoteServers: make(map[string]*registry.RemoteServerMetadata),
	}

	for _, upstreamServer := range upstreamServers {
		serverMetadata, err := registry.ConvertUpstreamToToolhive(&upstreamServer)
		if err != nil {
			return nil, fmt.Errorf("failed to convert server %s: %w", upstreamServer.Server.Name, err)
		}

		// Add to appropriate map based on server type
		switch server := serverMetadata.(type) {
		case *registry.ImageMetadata:
			toolhiveRegistry.Servers[upstreamServer.Server.Name] = server
		case *registry.RemoteServerMetadata:
			toolhiveRegistry.RemoteServers[upstreamServer.Server.Name] = server
		default:
			return nil, fmt.Errorf("unknown server type for %s", upstreamServer.Server.Name)
		}
	}

	return toolhiveRegistry, nil
}

package sources

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

// SourceDataValidator is an interface for validating registry source configurations
type SourceDataValidator interface {
	// ValidateData ensures the provided data is valid for the specified format
	ValidateData(data []byte, format string) error
}

// SourceHandler is an interface with methods to sync from external data sources
type SourceHandler interface {
	// Sync retrieves data from the source and returns the result
	Sync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (*SyncResult, error)

	// Validate validates the source configuration
	Validate(source *mcpv1alpha1.MCPRegistrySource) error
}

// SyncResult contains the result of a sync operation
type SyncResult struct {
	// Data is the raw registry data retrieved from the source
	Data []byte

	// Hash is the SHA256 hash of the data for change detection
	Hash string

	// ServerCount is the number of servers found in the registry data
	ServerCount int
}

// NewSyncResult creates a new SyncResult with computed hash
func NewSyncResult(data []byte, serverCount int) *SyncResult {
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	return &SyncResult{
		Data:        data,
		Hash:        hash,
		ServerCount: serverCount,
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

// ValidateData ensures the provided data is valid for the specified format
func (*DefaultSourceDataValidator) ValidateData(data []byte, format string) error {
	if len(data) == 0 {
		return fmt.Errorf("data cannot be empty")
	}

	switch format {
	case mcpv1alpha1.RegistryFormatToolHive:
		return validateToolhiveFormat(data)
	case mcpv1alpha1.RegistryFormatUpstream:
		return validateUpstreamFormat(data)
	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

// validateToolhiveFormat validates data against ToolHive registry format
func validateToolhiveFormat(data []byte) error {
	// Use the existing schema validation from pkg/registry
	return registry.ValidateRegistrySchema(data)
}

// validateUpstreamFormat validates data against upstream registry format
func validateUpstreamFormat(data []byte) error {
	// Parse as upstream format to validate structure
	var upstreamServers []registry.UpstreamServerDetail
	if err := json.Unmarshal(data, &upstreamServers); err != nil {
		return fmt.Errorf("invalid upstream format: %w", err)
	}

	// Basic validation - ensure we have at least one server and required fields
	if len(upstreamServers) == 0 {
		return fmt.Errorf("upstream registry must contain at least one server")
	}

	for i, server := range upstreamServers {
		if server.Server.Name == "" {
			return fmt.Errorf("server at index %d: name is required", i)
		}
		if server.Server.Description == "" {
			return fmt.Errorf("server at index %d (%s): description is required", i, server.Server.Name)
		}
		// Additional validation could be added here
	}

	return nil
}

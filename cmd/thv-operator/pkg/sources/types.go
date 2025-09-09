package sources

import (
	"context"
	"crypto/sha256"
	"fmt"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// SourceHandler is an interface with methods to validate and sync from external data sources
type SourceHandler interface {
	// Validate validates the source configuration
	Validate(source *mcpv1alpha1.MCPRegistrySource) error

	// Sync retrieves data from the source and returns the result
	Sync(ctx context.Context, registry *mcpv1alpha1.MCPRegistry) (*SyncResult, error)
}

// SyncResult contains the result of a sync operation
type SyncResult struct {
	// Data is the raw registry data retrieved from the source
	Data []byte

	// Hash is the SHA256 hash of the data for change detection
	Hash string

	// ServerCount is the number of servers found in the registry data
	ServerCount int32
}

// NewSyncResult creates a new SyncResult with computed hash
func NewSyncResult(data []byte, serverCount int32) *SyncResult {
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
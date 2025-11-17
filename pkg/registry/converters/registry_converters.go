package converters

import (
	"fmt"
	"time"

	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	"github.com/stacklok/toolhive/pkg/registry/types"
)

// NewServerRegistryFromUpstream creates a ServerRegistry from upstream ServerJSON array.
// This is used when ingesting data from upstream MCP Registry API endpoints.
func NewServerRegistryFromUpstream(servers []upstreamv0.ServerJSON) *types.ServerRegistry {
	return &types.ServerRegistry{
		Version:     "1.0.0",
		LastUpdated: time.Now().Format(time.RFC3339),
		Servers:     servers,
	}
}

// NewServerRegistryFromToolhive creates a ServerRegistry from ToolHive Registry.
// This converts ToolHive format to upstream ServerJSON using the converters package.
// Used when ingesting data from ToolHive-format sources (Git, File, API).
func NewServerRegistryFromToolhive(toolhiveReg *types.Registry) (*types.ServerRegistry, error) {
	if toolhiveReg == nil {
		return nil, fmt.Errorf("toolhive registry cannot be nil")
	}

	servers := make([]upstreamv0.ServerJSON, 0, len(toolhiveReg.Servers)+len(toolhiveReg.RemoteServers))

	// Convert container servers using converters package
	for name, imgMeta := range toolhiveReg.Servers {
		serverJSON, err := ImageMetadataToServerJSON(name, imgMeta)
		if err != nil {
			return nil, fmt.Errorf("failed to convert server %s: %w", name, err)
		}
		servers = append(servers, *serverJSON)
	}

	// Convert remote servers using converters package
	for name, remoteMeta := range toolhiveReg.RemoteServers {
		serverJSON, err := RemoteServerMetadataToServerJSON(name, remoteMeta)
		if err != nil {
			return nil, fmt.Errorf("failed to convert remote server %s: %w", name, err)
		}
		servers = append(servers, *serverJSON)
	}

	return &types.ServerRegistry{
		Version:     toolhiveReg.Version,
		LastUpdated: toolhiveReg.LastUpdated,
		Servers:     servers,
	}, nil
}

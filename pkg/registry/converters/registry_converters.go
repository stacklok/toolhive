package converters

import (
	"fmt"
	"time"

	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	types "github.com/stacklok/toolhive/pkg/registry/registry"
)

// NewUpstreamRegistryFromUpstreamServers creates a UpstreamRegistry from upstream ServerJSON array.
// This is used when ingesting data from upstream MCP Registry API endpoints.
func NewUpstreamRegistryFromUpstreamServers(servers []upstreamv0.ServerJSON) *types.UpstreamRegistry {
	return &types.UpstreamRegistry{
		Schema:  "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/registry.schema.json",
		Version: "1.0.0",
		Meta: types.RegistryMeta{
			LastUpdated: time.Now().Format(time.RFC3339),
		},
		Data: types.RegistryData{
			Servers: servers,
			Groups:  []types.RegistryGroup{},
		},
	}
}

// NewUpstreamRegistryFromToolhiveRegistry creates a UpstreamRegistry from ToolHive Registry.
// This converts ToolHive format to upstream ServerJSON using the converters package.
// Used when ingesting data from ToolHive-format sources (Git, File, API).
func NewUpstreamRegistryFromToolhiveRegistry(toolhiveReg *types.Registry) (*types.UpstreamRegistry, error) {
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

	return &types.UpstreamRegistry{
		Schema:  "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/registry.schema.json",
		Version: toolhiveReg.Version,
		Meta: types.RegistryMeta{
			LastUpdated: toolhiveReg.LastUpdated,
		},
		Data: types.RegistryData{
			Servers: servers,
			Groups:  []types.RegistryGroup{},
		},
	}, nil
}

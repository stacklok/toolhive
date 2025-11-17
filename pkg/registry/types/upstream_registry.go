package types

import (
	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
)

// UpstreamRegistry is the unified internal registry format.
// It stores servers in upstream ServerJSON format while maintaining
// ToolHive-compatible metadata fields for backward compatibility.
type UpstreamRegistry struct {
	// Version is the schema version (ToolHive compatibility)
	Version string `json:"version"`

	// LastUpdated is the timestamp when registry was last updated (ToolHive compatibility)
	LastUpdated string `json:"last_updated"`

	// Servers contains the server definitions in upstream MCP format
	Servers []upstreamv0.ServerJSON `json:"servers"`
}

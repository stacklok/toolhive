package registry

import (
	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
)

// UpstreamRegistry is the unified registry format that stores servers in upstream
// ServerJSON format with proper meta/data separation and groups support.
type UpstreamRegistry struct {
	// Schema is the JSON schema URL for validation
	Schema string `json:"$schema" yaml:"$schema"`

	// Version is the schema version (e.g., "1.0.0")
	Version string `json:"version" yaml:"version"`

	// Meta contains registry metadata
	Meta UpstreamMeta `json:"meta" yaml:"meta"`

	// Data contains the actual registry content
	Data UpstreamData `json:"data" yaml:"data"`
}

// UpstreamMeta contains metadata about the registry
type UpstreamMeta struct {
	// LastUpdated is the timestamp when registry was last updated in RFC3339 format
	LastUpdated string `json:"last_updated" yaml:"last_updated"`
}

// UpstreamData contains the actual registry content (servers and groups)
type UpstreamData struct {
	// Servers contains the server definitions in upstream MCP format
	Servers []upstreamv0.ServerJSON `json:"servers" yaml:"servers"`

	// Groups contains grouped collections of servers (optional, for future use)
	Groups []UpstreamGroup `json:"groups,omitempty" yaml:"groups,omitempty"`
}

// UpstreamGroup represents a named collection of related MCP servers
type UpstreamGroup struct {
	// Name is the unique identifier for the group
	Name string `json:"name" yaml:"name"`

	// Description explains the purpose of this group
	Description string `json:"description" yaml:"description"`

	// Servers contains the server definitions in this group
	Servers []upstreamv0.ServerJSON `json:"servers" yaml:"servers"`
}

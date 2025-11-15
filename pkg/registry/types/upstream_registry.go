package types

import (
	"strings"

	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
)

// ServerRegistry is the unified internal registry format.
// It stores servers in upstream ServerJSON format while maintaining
// ToolHive-compatible metadata fields for backward compatibility.
type ServerRegistry struct {
	// Version is the schema version (ToolHive compatibility)
	Version string `json:"version"`

	// LastUpdated is the timestamp when registry was last updated (ToolHive compatibility)
	LastUpdated string `json:"last_updated"`

	// Servers contains the server definitions in upstream MCP format
	Servers []upstreamv0.ServerJSON `json:"servers"`
}

// GetServerByName retrieves a server by its name.
// Supports both reverse-DNS format (e.g., "io.github.user/server") and simple names (e.g., "server").
func (sr *ServerRegistry) GetServerByName(name string) (*upstreamv0.ServerJSON, bool) {
	if sr == nil {
		return nil, false
	}

	for i := range sr.Servers {
		serverName := sr.Servers[i].Name
		if serverName == name || ExtractSimpleName(serverName) == name {
			return &sr.Servers[i], true
		}
	}
	return nil, false
}

// ExtractSimpleName extracts the simple server name from reverse-DNS format.
// Examples:
//   - "io.github.user/server" -> "server"
//   - "com.example/my-server" -> "my-server"
//   - "simple-name" -> "simple-name" (no change if not reverse-DNS)
func ExtractSimpleName(reverseDNS string) string {
	idx := strings.LastIndex(reverseDNS, "/")
	if idx >= 0 && idx < len(reverseDNS)-1 {
		return reverseDNS[idx+1:]
	}
	return reverseDNS
}

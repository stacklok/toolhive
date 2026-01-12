package models

import (
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// BaseMCPServer represents the common fields for MCP servers.
type BaseMCPServer struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Remote          bool          `json:"remote"`
	Transport       TransportType `json:"transport"`
	Description     *string       `json:"description,omitempty"`
	ServerEmbedding []float32     `json:"-"` // Excluded from JSON, stored as BLOB
	Group           string        `json:"group"`
	LastUpdated     time.Time     `json:"last_updated"`
	CreatedAt       time.Time     `json:"created_at"`
}

// RegistryServer represents an MCP server from the registry catalog.
type RegistryServer struct {
	BaseMCPServer
	URL     *string `json:"url,omitempty"`     // For remote servers
	Package *string `json:"package,omitempty"` // For container servers
}

// Validate checks if the registry server has valid data.
// Remote servers must have URL, container servers must have package.
func (r *RegistryServer) Validate() error {
	if r.Remote && r.URL == nil {
		return ErrRemoteServerMissingURL
	}
	if !r.Remote && r.Package == nil {
		return ErrContainerServerMissingPackage
	}
	return nil
}

// WorkloadServer represents a running MCP server workload.
type WorkloadServer struct {
	BaseMCPServer
	URL                string    `json:"url"`
	WorkloadIdentifier string    `json:"workload_identifier"`
	Status             MCPStatus `json:"status"`
	RegistryServerID   *string   `json:"registry_server_id,omitempty"`   // NULL if autonomous
	RegistryServerName *string   `json:"registry_server_name,omitempty"` // Cached for tool embedding
}

// BaseTool represents the common fields for tools.
type BaseTool struct {
	ID               string    `json:"id"`
	MCPServerID      string    `json:"mcpserver_id"`
	Details          mcp.Tool  `json:"details"`
	DetailsEmbedding []float32 `json:"-"` // Excluded from JSON, stored as BLOB
	LastUpdated      time.Time `json:"last_updated"`
	CreatedAt        time.Time `json:"created_at"`
}

// RegistryTool represents a tool from a registry MCP server.
type RegistryTool struct {
	BaseTool
}

// WorkloadTool represents a tool from a workload MCP server.
type WorkloadTool struct {
	BaseTool
	TokenCount int `json:"token_count"` // Token count for LLM consumption
}

// ToolDetailsToJSON converts mcp.Tool to JSON for storage in the database.
func ToolDetailsToJSON(tool mcp.Tool) (string, error) {
	data, err := json.Marshal(tool)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ToolDetailsFromJSON converts JSON to mcp.Tool
func ToolDetailsFromJSON(data string) (*mcp.Tool, error) {
	var tool mcp.Tool
	err := json.Unmarshal([]byte(data), &tool)
	if err != nil {
		return nil, err
	}
	return &tool, nil
}

// WorkloadToolWithMetadata represents a workload tool with server information and similarity distance.
type WorkloadToolWithMetadata struct {
	ServerName        string       `json:"server_name"`
	ServerDescription *string      `json:"server_description,omitempty"`
	Distance          float64      `json:"distance"` // Cosine distance from query embedding
	Tool              WorkloadTool `json:"tool"`
}

// RegistryToolWithMetadata represents a registry tool with server information and similarity distance.
type RegistryToolWithMetadata struct {
	ServerName        string       `json:"server_name"`
	ServerDescription *string      `json:"server_description,omitempty"`
	Distance          float64      `json:"distance"` // Cosine distance from query embedding
	Tool              RegistryTool `json:"tool"`
}

// WorkloadWithRegistry represents a workload server with its resolved registry relationship.
type WorkloadWithRegistry struct {
	Workload WorkloadServer  `json:"workload"`
	Registry *RegistryServer `json:"registry,omitempty"` // NULL if autonomous
}

// EffectiveDescription returns the description (inherited from registry or own).
func (w *WorkloadWithRegistry) EffectiveDescription() *string {
	if w.Registry != nil {
		return w.Registry.Description
	}
	return w.Workload.Description
}

// EffectiveEmbedding returns the embedding (inherited from registry or own).
func (w *WorkloadWithRegistry) EffectiveEmbedding() []float32 {
	if w.Registry != nil {
		return w.Registry.ServerEmbedding
	}
	return w.Workload.ServerEmbedding
}

// ServerNameForTools returns the server name to use as context for tool embeddings.
func (w *WorkloadWithRegistry) ServerNameForTools() string {
	if w.Registry != nil {
		return w.Registry.Name
	}
	return w.Workload.Name
}

// TokenMetrics represents token efficiency metrics for tool filtering.
type TokenMetrics struct {
	BaselineTokens    int     `json:"baseline_tokens"`    // Total tokens for all running server tools
	ReturnedTokens    int     `json:"returned_tokens"`    // Total tokens for returned/filtered tools
	TokensSaved       int     `json:"tokens_saved"`       // Number of tokens saved by filtering
	SavingsPercentage float64 `json:"savings_percentage"` // Percentage of tokens saved (0-100)
}

// Validate checks if the token metrics are consistent.
func (t *TokenMetrics) Validate() error {
	if t.TokensSaved != t.BaselineTokens-t.ReturnedTokens {
		return ErrInvalidTokenMetrics
	}

	var expectedPct float64
	if t.BaselineTokens > 0 {
		expectedPct = (float64(t.TokensSaved) / float64(t.BaselineTokens)) * 100
		// Allow small floating point differences (0.01%)
		if expectedPct-t.SavingsPercentage > 0.01 || t.SavingsPercentage-expectedPct > 0.01 {
			return ErrInvalidTokenMetrics
		}
	} else if t.SavingsPercentage != 0.0 {
		return ErrInvalidTokenMetrics
	}

	return nil
}

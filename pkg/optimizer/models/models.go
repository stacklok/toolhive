// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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

// BackendServer represents a running MCP server backend.
// Simplified: Only stores metadata needed for tool organization and search results.
// vMCP manages backend lifecycle (URL, status, transport, etc.)
type BackendServer struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Description     *string   `json:"description,omitempty"`
	Group           string    `json:"group"`
	ServerEmbedding []float32 `json:"-"` // Excluded from JSON, stored as BLOB
	LastUpdated     time.Time `json:"last_updated"`
	CreatedAt       time.Time `json:"created_at"`
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

// BackendTool represents a tool from a backend MCP server.
// With chromem-go, embeddings are managed by the database.
type BackendTool struct {
	ID            string          `json:"id"`
	MCPServerID   string          `json:"mcpserver_id"`
	ToolName      string          `json:"tool_name"`
	Description   *string         `json:"description,omitempty"`
	InputSchema   json.RawMessage `json:"input_schema,omitempty"`
	ToolEmbedding []float32       `json:"-"` // Managed by chromem-go
	TokenCount    int             `json:"token_count"`
	LastUpdated   time.Time       `json:"last_updated"`
	CreatedAt     time.Time       `json:"created_at"`
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

// BackendToolWithMetadata represents a backend tool with similarity score.
type BackendToolWithMetadata struct {
	BackendTool
	Similarity float32 `json:"similarity"` // Cosine similarity from chromem-go (0-1, higher is better)
}

// RegistryToolWithMetadata represents a registry tool with server information and similarity distance.
type RegistryToolWithMetadata struct {
	ServerName        string       `json:"server_name"`
	ServerDescription *string      `json:"server_description,omitempty"`
	Distance          float64      `json:"distance"` // Cosine distance from query embedding
	Tool              RegistryTool `json:"tool"`
}

// BackendWithRegistry represents a backend server with its resolved registry relationship.
type BackendWithRegistry struct {
	Backend  BackendServer   `json:"backend"`
	Registry *RegistryServer `json:"registry,omitempty"` // NULL if autonomous
}

// EffectiveDescription returns the description (inherited from registry or own).
func (b *BackendWithRegistry) EffectiveDescription() *string {
	if b.Registry != nil {
		return b.Registry.Description
	}
	return b.Backend.Description
}

// EffectiveEmbedding returns the embedding (inherited from registry or own).
func (b *BackendWithRegistry) EffectiveEmbedding() []float32 {
	if b.Registry != nil {
		return b.Registry.ServerEmbedding
	}
	return b.Backend.ServerEmbedding
}

// ServerNameForTools returns the server name to use as context for tool embeddings.
func (b *BackendWithRegistry) ServerNameForTools() string {
	if b.Registry != nil {
		return b.Registry.Name
	}
	return b.Backend.Name
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

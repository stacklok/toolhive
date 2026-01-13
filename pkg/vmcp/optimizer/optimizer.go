// Package optimizer provides the Optimizer interface for intelligent tool discovery
// and invocation in the Virtual MCP Server.
//
// When the optimizer is enabled, vMCP exposes only two tools to clients:
//   - find_tool: Semantic search over available tools
//   - call_tool: Dynamic invocation of any backend tool
//
// This reduces token usage by avoiding the need to send all tool definitions
// to the LLM, instead allowing it to discover relevant tools on demand.
package optimizer

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
)

// Optimizer defines the interface for intelligent tool discovery and invocation.
//
// Implementations may use various strategies for tool matching:
//   - DummyOptimizer: Exact string matching (for testing)
//   - EmbeddingOptimizer: Semantic similarity via embeddings (production)
type Optimizer interface {
	// FindTool searches for tools matching the given description and keywords.
	// Returns matching tools ranked by relevance score.
	FindTool(ctx context.Context, input FindToolInput) (*FindToolOutput, error)

	// CallTool invokes a tool by name with the given parameters.
	// Returns the tool's result or an error if the tool is not found or execution fails.
	// Returns the MCP CallToolResult directly from the underlying tool handler.
	CallTool(ctx context.Context, input CallToolInput) (*mcp.CallToolResult, error)
}

// FindToolInput contains the parameters for finding tools.
type FindToolInput struct {
	// ToolDescription is a natural language description of the tool to find.
	ToolDescription string `json:"tool_description" description:"Natural language description of the tool to find"`

	// ToolKeywords is an optional list of keywords to narrow the search.
	ToolKeywords []string `json:"tool_keywords,omitempty" description:"Optional keywords to narrow search"`
}

// FindToolOutput contains the results of a tool search.
type FindToolOutput struct {
	// Tools contains the matching tools, ranked by relevance.
	Tools []ToolMatch `json:"tools"`

	// TokenMetrics provides information about token savings from using the optimizer.
	TokenMetrics TokenMetrics `json:"token_metrics"`
}

// ToolMatch represents a tool that matched the search criteria.
type ToolMatch struct {
	// Name is the unique identifier of the tool.
	Name string `json:"name"`

	// Description is the human-readable description of the tool.
	Description string `json:"description"`

	// Parameters is the JSON schema for the tool's input parameters.
	// Uses json.RawMessage to preserve the original schema format.
	Parameters json.RawMessage `json:"parameters"`

	// Score indicates how well this tool matches the search criteria (0.0-1.0).
	Score float64 `json:"score"`
}

// TokenMetrics provides information about token usage optimization.
type TokenMetrics struct {
	// BaselineTokens is the estimated tokens if all tools were sent.
	BaselineTokens int `json:"baseline_tokens"`

	// ReturnedTokens is the actual tokens for the returned tools.
	ReturnedTokens int `json:"returned_tokens"`

	// SavingsPercent is the percentage of tokens saved.
	SavingsPercent float64 `json:"savings_percent"`
}

// CallToolInput contains the parameters for calling a tool.
type CallToolInput struct {
	// ToolName is the name of the tool to invoke.
	ToolName string `json:"tool_name" description:"Name of the tool to call"`

	// Parameters are the arguments to pass to the tool.
	Parameters map[string]any `json:"parameters" description:"Parameters to pass to the tool"`
}

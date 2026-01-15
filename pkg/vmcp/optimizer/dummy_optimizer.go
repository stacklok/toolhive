package optimizer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// DummyOptimizer implements the Optimizer interface using exact string matching.
//
// This implementation is intended for testing and development. It performs
// case-insensitive substring matching on tool names and descriptions.
//
// For production use, see the EmbeddingOptimizer which uses semantic similarity.
type DummyOptimizer struct {
	// tools contains all available tools indexed by name.
	tools map[string]server.ServerTool
}

// NewDummyOptimizer creates a new DummyOptimizer with the given tools.
//
// The tools slice should contain all backend tools (as ServerTool with handlers).
func NewDummyOptimizer(tools []server.ServerTool) Optimizer {
	toolMap := make(map[string]server.ServerTool, len(tools))
	for _, tool := range tools {
		toolMap[tool.Tool.Name] = tool
	}
	fmt.Printf("[DummyOptimizer.NewDummyOptimizer] Created with %d tools:\n", len(toolMap))
	for name, tool := range toolMap {
		fmt.Printf("  - %q: %q\n", name, tool.Tool.Description)
	}

	return DummyOptimizer{
		tools: toolMap,
	}
}

// FindTool searches for tools using exact substring matching.
//
// The search is case-insensitive and matches against:
//   - Tool name (substring match)
//   - Tool description (substring match)
//
// Returns all matching tools with a score of 1.0 (exact match semantics).
// TokenMetrics are returned as zero values (not implemented in dummy).
func (d DummyOptimizer) FindTool(_ context.Context, input FindToolInput) (*FindToolOutput, error) {
	if input.ToolDescription == "" {
		return nil, fmt.Errorf("tool_description is required")
	}

	// Log all tools in the optimizer for debugging
	fmt.Printf("[DummyOptimizer.FindTool] Searching for %q in %d tools:\n", input.ToolDescription, len(d.tools))
	for name, tool := range d.tools {
		fmt.Printf("  - %q: %q\n", name, tool.Tool.Description)
	}

	searchTerm := strings.ToLower(input.ToolDescription)

	var matches []ToolMatch
	for _, tool := range d.tools {
		nameLower := strings.ToLower(tool.Tool.Name)
		descLower := strings.ToLower(tool.Tool.Description)

		// Check if search term matches name or description
		if strings.Contains(nameLower, searchTerm) || strings.Contains(descLower, searchTerm) {
			schema, err := getToolSchema(tool.Tool)
			if err != nil {
				return nil, err
			}
			matches = append(matches, ToolMatch{
				Name:        tool.Tool.Name,
				Description: tool.Tool.Description,
				Parameters:  schema,
				Score:       1.0, // Exact match semantics
			})
		}
	}

	return &FindToolOutput{
		Tools:        matches,
		TokenMetrics: TokenMetrics{}, // Zero values for dummy
	}, nil
}

// CallTool invokes a tool by name using its registered handler.
//
// The tool is looked up by exact name match. If found, the handler
// is invoked directly with the given parameters.
func (d DummyOptimizer) CallTool(ctx context.Context, input CallToolInput) (*mcp.CallToolResult, error) {
	if input.ToolName == "" {
		return nil, fmt.Errorf("tool_name is required")
	}

	// Verify the tool exists
	tool, exists := d.tools[input.ToolName]
	if !exists {
		return mcp.NewToolResultError(fmt.Sprintf("tool not found: %s", input.ToolName)), nil
	}

	// Build the MCP request
	request := mcp.CallToolRequest{}
	request.Params.Name = input.ToolName
	request.Params.Arguments = input.Parameters

	// Call the tool handler directly
	return tool.Handler(ctx, request)
}

// getToolSchema returns the input schema for a tool.
// Prefers RawInputSchema if set, otherwise marshals InputSchema.
func getToolSchema(tool mcp.Tool) (json.RawMessage, error) {
	if len(tool.RawInputSchema) > 0 {
		return tool.RawInputSchema, nil
	}

	// Fall back to InputSchema
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input schema for tool %s: %w", tool.Name, err)
	}
	return data, nil
}

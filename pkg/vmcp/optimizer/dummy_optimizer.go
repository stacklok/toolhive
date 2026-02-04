// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter"
)

// DummyOptimizer implements the Optimizer interface using exact string matching.
//
// This implementation is intended for testing and development. It performs
// case-insensitive substring matching on tool names and descriptions.
//
// For production use, see the EmbeddingOptimizer which uses semantic similarity.
type DummyOptimizer struct {
	// tools contains all available tools indexed by name.
	tools map[string]mcpserver.ServerTool
}

// NewDummyOptimizer creates a new DummyOptimizer with the given tools.
//
// The tools slice should contain all backend tools (as ServerTool with handlers).
func NewDummyOptimizer(tools []mcpserver.ServerTool) Optimizer {
	toolMap := make(map[string]mcpserver.ServerTool, len(tools))
	for _, tool := range tools {
		toolMap[tool.Tool.Name] = tool
	}

	return &DummyOptimizer{
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
func (d *DummyOptimizer) FindTool(_ context.Context, input FindToolInput) (*FindToolOutput, error) {
	if input.ToolDescription == "" {
		return nil, fmt.Errorf("tool_description is required")
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
				Name:            tool.Tool.Name,
				Description:     tool.Tool.Description,
				InputSchema:     schema,
				BackendID:       "dummy",
				SimilarityScore: 1.0, // Exact match semantics
				TokenCount:      0,   // Not implemented in dummy
			})
		}
	}

	// Apply limit if specified
	limit := input.Limit
	if limit <= 0 {
		limit = 10
	}
	if len(matches) > limit {
		matches = matches[:limit]
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
func (d *DummyOptimizer) CallTool(ctx context.Context, input CallToolInput) (*mcp.CallToolResult, error) {
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

// Close is a no-op for DummyOptimizer.
func (*DummyOptimizer) Close() error {
	return nil
}

// HandleSessionRegistration is a no-op for DummyOptimizer.
// Returns false to indicate the dummy optimizer doesn't handle session registration.
func (*DummyOptimizer) HandleSessionRegistration(
	_ context.Context,
	_ string,
	_ *aggregator.AggregatedCapabilities,
	_ *mcpserver.MCPServer,
	_ func([]vmcp.Resource) []mcpserver.ServerResource,
) (bool, error) {
	return false, nil
}

// CreateFindToolHandler implements adapter.OptimizerHandlerProvider.
func (d *DummyOptimizer) CreateFindToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Parse input from request arguments
		args, ok := request.Params.Arguments.(map[string]any)
		if !ok {
			return mcp.NewToolResultError("invalid arguments: expected object"), nil
		}

		input := FindToolInput{}
		if desc, ok := args["tool_description"].(string); ok {
			input.ToolDescription = desc
		}
		if kw, ok := args["tool_keywords"].(string); ok {
			input.ToolKeywords = kw
		}
		if limit, ok := args["limit"].(float64); ok {
			input.Limit = int(limit)
		}

		output, err := d.FindTool(ctx, input)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("find_tool failed: %v", err)), nil
		}
		return mcp.NewToolResultStructuredOnly(output), nil
	}
}

// CreateCallToolHandler implements adapter.OptimizerHandlerProvider.
func (d *DummyOptimizer) CreateCallToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Parse input from request arguments
		args, ok := request.Params.Arguments.(map[string]any)
		if !ok {
			return mcp.NewToolResultError("invalid arguments: expected object"), nil
		}

		input := CallToolInput{}
		if name, ok := args["tool_name"].(string); ok {
			input.ToolName = name
		}
		if params, ok := args["parameters"].(map[string]any); ok {
			input.Parameters = params
		}

		result, err := d.CallTool(ctx, input)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("call_tool failed: %v", err)), nil
		}
		return result, nil
	}
}

// getToolSchema returns the input schema for a tool.
// Prefers RawInputSchema if set, otherwise marshals InputSchema.
func getToolSchema(tool mcp.Tool) (map[string]any, error) {
	var result map[string]any

	if len(tool.RawInputSchema) > 0 {
		if err := json.Unmarshal(tool.RawInputSchema, &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal raw input schema for tool %s: %w", tool.Name, err)
		}
		return result, nil
	}

	// Fall back to InputSchema - convert to map via JSON round-trip
	// Check if InputSchema has any content by marshaling it
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input schema for tool %s: %w", tool.Name, err)
	}

	// Empty struct marshals to "{}", only process if not empty
	if string(data) != "{}" && string(data) != "null" {
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal input schema for tool %s: %w", tool.Name, err)
		}
		return result, nil
	}

	return nil, nil
}

// Verify DummyOptimizer implements Optimizer interface.
var _ Optimizer = (*DummyOptimizer)(nil)

// Verify DummyOptimizer implements OptimizerHandlerProvider interface.
var _ adapter.OptimizerHandlerProvider = (*DummyOptimizer)(nil)

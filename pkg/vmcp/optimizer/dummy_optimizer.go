// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// DummyOptimizer implements the Optimizer interface using a shared ToolStore
// for search and a local handler map for tool invocation.
//
// This implementation is intended for testing and development. It delegates
// search to the ToolStore (which performs case-insensitive substring matching
// for InMemoryToolStore) and scopes results to only the tools this instance
// was created with.
//
// For production use, see the EmbeddingOptimizer which uses semantic similarity.
type DummyOptimizer struct {
	// store is the shared tool store used for search.
	store ToolStore

	// scope contains the names of tools this optimizer instance can access.
	scope []string

	// handlers contains tool handlers indexed by name for CallTool.
	handlers map[string]server.ServerTool
}

// NewDummyOptimizer creates a new DummyOptimizer backed by the given ToolStore.
//
// The tools slice should contain all backend tools (as ServerTool with handlers).
// Tools are upserted into the shared store and scoped for this optimizer instance.
func NewDummyOptimizer(store ToolStore, tools []server.ServerTool) Optimizer {
	scope := make([]string, 0, len(tools))
	handlers := make(map[string]server.ServerTool, len(tools))
	for _, tool := range tools {
		scope = append(scope, tool.Tool.Name)
		handlers[tool.Tool.Name] = tool
	}

	// Upsert tools into the shared store (best-effort; errors logged at call site if needed)
	//nolint:errcheck // UpsertTools for InMemoryToolStore never returns an error
	_ = store.UpsertTools(context.Background(), tools)

	return &DummyOptimizer{
		store:    store,
		scope:    scope,
		handlers: handlers,
	}
}

// FindTool searches for tools using the shared ToolStore, scoped to this instance's tools.
//
// Returns all matching tools with a score of 1.0 (exact match semantics).
// TokenMetrics are returned as zero values (not implemented in dummy).
func (d *DummyOptimizer) FindTool(ctx context.Context, input FindToolInput) (*FindToolOutput, error) {
	if input.ToolDescription == "" {
		return nil, fmt.Errorf("tool_description is required")
	}

	matches, err := d.store.Search(ctx, input.ToolDescription, d.scope)
	if err != nil {
		return nil, fmt.Errorf("tool search failed: %w", err)
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
	tool, exists := d.handlers[input.ToolName]
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

// Close is a no-op for DummyOptimizer. The shared store is closed separately.
func (*DummyOptimizer) Close() error {
	return nil
}

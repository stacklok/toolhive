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

	// tools contains all available tools indexed by name.
	tools map[string]server.ServerTool

	// tokenCounts holds precomputed per-tool token estimates, indexed by tool name.
	// Immutable after construction: token counts are computed once in NewDummyOptimizer
	// and never modified. The tools are fixed per session (one optimizer per session),
	// and the TokenCounter is set at configuration time, so counts cannot change at runtime.
	tokenCounts map[string]int

	// baselineTokens is the precomputed sum of all per-tool token counts.
	// Immutable after construction; used as the denominator for savings metrics.
	baselineTokens int
}

// NewDummyOptimizer creates a new DummyOptimizer backed by the given ToolStore.
//
// The tools slice should contain all backend tools (as ServerTool with handlers).
// Tools are upserted into the shared store and scoped for this optimizer instance.
// Token counts are precomputed using the provided counter for metrics calculation.
func NewDummyOptimizer(ctx context.Context, store ToolStore, counter TokenCounter, tools []server.ServerTool) (Optimizer, error) {
	toolMap := make(map[string]server.ServerTool, len(tools))
	tokenCounts := make(map[string]int, len(tools))
	var baselineTokens int
	for _, tool := range tools {
		toolMap[tool.Tool.Name] = tool
		tc := counter.CountTokens(tool.Tool)
		tokenCounts[tool.Tool.Name] = tc
		baselineTokens += tc
	}

	if err := store.UpsertTools(ctx, tools); err != nil {
		return nil, fmt.Errorf("failed to upsert tools into store: %w", err)
	}

	return &DummyOptimizer{
		store:          store,
		tools:          toolMap,
		tokenCounts:    tokenCounts,
		baselineTokens: baselineTokens,
	}, nil
}

// FindTool searches for tools using the shared ToolStore, scoped to this instance's tools.
//
// Returns all matching tools with a score of 1.0 (exact match semantics).
// TokenMetrics quantify the token savings from returning only matching tools
// instead of the full set of available tools.
func (d *DummyOptimizer) FindTool(ctx context.Context, input FindToolInput) (*FindToolOutput, error) {
	if input.ToolDescription == "" {
		return nil, fmt.Errorf("tool_description is required")
	}

	matches, err := d.store.Search(ctx, input.ToolDescription, d.toolNames())
	if err != nil {
		return nil, fmt.Errorf("tool search failed: %w", err)
	}

	metrics := computeTokenMetrics(d.baselineTokens, d.tokenCounts, matches)

	return &FindToolOutput{
		Tools:        matches,
		TokenMetrics: metrics,
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

// toolNames returns the names of all tools in the handlers map.
func (d *DummyOptimizer) toolNames() []string {
	names := make([]string, 0, len(d.tools))
	for name := range d.tools {
		names = append(names, name)
	}
	return names
}

// NewDummyOptimizerFactory returns an OptimizerFactory that creates DummyOptimizer
// instances backed by a shared InMemoryToolStore. All optimizers created by the
// returned factory share the same underlying storage, enabling cross-session search.
func NewDummyOptimizerFactory() func(context.Context, []server.ServerTool) (Optimizer, error) {
	counter := DefaultTokenCounter()
	store := NewInMemoryToolStore()
	return NewDummyOptimizerFactoryWithStore(store, counter)
}

// computeTokenMetrics calculates token savings by comparing the precomputed
// baseline (all tools) against only the matched tools.
func computeTokenMetrics(baselineTokens int, tokenCounts map[string]int, matches []ToolMatch) TokenMetrics {
	if baselineTokens == 0 {
		return TokenMetrics{}
	}

	var returnedTokens int
	for _, m := range matches {
		returnedTokens += tokenCounts[m.Name]
	}

	savingsPercent := float64(baselineTokens-returnedTokens) / float64(baselineTokens) * 100

	return TokenMetrics{
		BaselineTokens: baselineTokens,
		ReturnedTokens: returnedTokens,
		SavingsPercent: savingsPercent,
	}
}

// NewDummyOptimizerFactoryWithStore returns an OptimizerFactory that creates
// DummyOptimizer instances backed by the given ToolStore. All optimizers created
// by the returned factory share the same store, enabling cross-session search.
//
// Use this when you need to provide a specific store implementation (e.g.,
// SQLiteToolStore for FTS5-based search) instead of the default InMemoryToolStore.
func NewDummyOptimizerFactoryWithStore(
	store ToolStore, counter TokenCounter,
) func(context.Context, []server.ServerTool) (Optimizer, error) {
	return func(ctx context.Context, tools []server.ServerTool) (Optimizer, error) {
		return NewDummyOptimizer(ctx, store, counter, tools)
	}
}

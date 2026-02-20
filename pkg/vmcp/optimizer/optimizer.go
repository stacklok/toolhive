// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	"fmt"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/similarity"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/tokencounter"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/toolstore"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

// Config defines configuration options for the Optimizer.
// It is defined in the internal/types package and aliased here so that
// external consumers continue to use optimizer.Config.
type Config = types.OptimizerConfig

// GetAndValidateConfig validates the CRD-compatible OptimizerConfig and converts it
// to the internal optimizer.Config with parsed, typed values.
// Returns (nil, nil) if cfg is nil.
func GetAndValidateConfig(cfg *vmcpconfig.OptimizerConfig) (*Config, error) {
	if cfg == nil {
		return nil, nil
	}

	optCfg := &Config{
		EmbeddingService:        cfg.EmbeddingService,
		EmbeddingServiceTimeout: time.Duration(cfg.EmbeddingServiceTimeout),
	}

	if cfg.MaxToolsToReturn != 0 {
		if cfg.MaxToolsToReturn < 1 || cfg.MaxToolsToReturn > 50 {
			return nil, fmt.Errorf("optimizer.maxToolsToReturn must be between 1 and 50, got %d", cfg.MaxToolsToReturn)
		}
		optCfg.MaxToolsToReturn = &cfg.MaxToolsToReturn
	}

	if cfg.HybridSearchSemanticRatio != "" {
		ratio, err := strconv.ParseFloat(cfg.HybridSearchSemanticRatio, 64)
		if err != nil {
			return nil, fmt.Errorf("optimizer.hybridSearchSemanticRatio must be a valid number: %w", err)
		}
		if ratio < 0 || ratio > 1 {
			return nil, fmt.Errorf(
				"optimizer.hybridSearchSemanticRatio must be between 0.0 and 1.0, got %s",
				cfg.HybridSearchSemanticRatio,
			)
		}
		optCfg.HybridSemanticRatio = &ratio
	}

	if cfg.SemanticDistanceThreshold != "" {
		threshold, err := strconv.ParseFloat(cfg.SemanticDistanceThreshold, 64)
		if err != nil {
			return nil, fmt.Errorf("optimizer.semanticDistanceThreshold must be a valid number: %w", err)
		}
		if threshold < 0 || threshold > 2 {
			return nil, fmt.Errorf(
				"optimizer.semanticDistanceThreshold must be between 0.0 and 2.0, got %s",
				cfg.SemanticDistanceThreshold,
			)
		}
		optCfg.SemanticDistanceThreshold = &threshold
	}

	return optCfg, nil
}

// Optimizer defines the interface for intelligent tool discovery and invocation.
//
// The default implementation delegates search to a ToolStore (SQLite FTS5 with
// optional embedding-based semantic search) and scopes results to the tools
// registered for each session.
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
// It is defined in the internal/types package and aliased here so that
// external consumers continue to use optimizer.ToolMatch.
type ToolMatch = types.ToolMatch

// TokenMetrics provides information about token usage optimization.
// It is defined in the internal/tokencounter package and aliased here so that
// external consumers continue to use optimizer.TokenMetrics.
type TokenMetrics = tokencounter.TokenMetrics

// CallToolInput contains the parameters for calling a tool.
type CallToolInput struct {
	// ToolName is the name of the tool to invoke.
	ToolName string `json:"tool_name" description:"Name of the tool to call"`

	// Parameters are the arguments to pass to the tool.
	Parameters map[string]any `json:"parameters" description:"Parameters to pass to the tool"`
}

// NewOptimizerFactory creates the embedding client and SQLite tool store from
// the given OptimizerConfig, then returns an OptimizerFactory and a cleanup
// function that closes the store. The caller must invoke the cleanup function
// during shutdown to release resources.
func NewOptimizerFactory(cfg *Config) (
	func(context.Context, []server.ServerTool) (Optimizer, error),
	func(context.Context) error,
	error,
) {
	embClient, err := similarity.NewEmbeddingClient(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create embedding client: %w", err)
	}

	store, err := toolstore.NewSQLiteToolStore(embClient, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create optimizer store: %w", err)
	}

	factory := newOptimizerFactoryWithStore(store, tokencounter.NewJSONByteCounter())
	cleanup := func(_ context.Context) error {
		return store.Close()
	}

	return factory, cleanup, nil
}

// toolOptimizer implements the Optimizer interface using a shared ToolStore
// for search and a local handler map for tool invocation.
//
// It delegates search to the ToolStore (which uses SQLite FTS5 with optional
// embedding-based semantic search) and scopes results to only the tools this
// instance was created with.
type toolOptimizer struct {
	// store is the shared tool store used for search.
	store types.ToolStore

	// tools contains all available tools indexed by name.
	tools map[string]server.ServerTool

	// toolNames is the precomputed list of tool names from the tools map.
	// Immutable after construction; avoids re-allocation on every FindTool call.
	toolNames []string

	// tokenCounts holds precomputed per-tool token estimates, indexed by tool name.
	// Immutable after construction: token counts are computed once in newToolOptimizer
	// and never modified. The tools are fixed per session (one optimizer per session),
	// and the tokencounter.Counter is set at configuration time, so counts cannot change at runtime.
	tokenCounts map[string]int

	// baselineTokens is the precomputed sum of all per-tool token counts.
	// Immutable after construction; used as the denominator for savings metrics.
	baselineTokens int
}

// newToolOptimizer creates a new toolOptimizer backed by the given ToolStore.
//
// The tools slice should contain all backend tools (as ServerTool with handlers).
// Tools are upserted into the shared store and scoped for this optimizer instance.
// Token counts are precomputed using the provided counter for metrics calculation.
func newToolOptimizer(
	ctx context.Context, store types.ToolStore, counter tokencounter.Counter, tools []server.ServerTool,
) (Optimizer, error) {
	toolMap := make(map[string]server.ServerTool, len(tools))
	names := make([]string, 0, len(tools))
	tokenCounts := make(map[string]int, len(tools))
	var baselineTokens int
	for _, tool := range tools {
		toolMap[tool.Tool.Name] = tool
		names = append(names, tool.Tool.Name)
		tc := counter.CountTokens(tool.Tool)
		tokenCounts[tool.Tool.Name] = tc
		baselineTokens += tc
	}

	if err := store.UpsertTools(ctx, tools); err != nil {
		return nil, fmt.Errorf("failed to upsert tools into store: %w", err)
	}

	return &toolOptimizer{
		store:          store,
		tools:          toolMap,
		toolNames:      names,
		tokenCounts:    tokenCounts,
		baselineTokens: baselineTokens,
	}, nil
}

// FindTool searches for tools using the shared ToolStore, scoped to this instance's tools.
//
// TokenMetrics quantify the token savings from returning only matching tools
// instead of the full set of available tools.
func (d *toolOptimizer) FindTool(ctx context.Context, input FindToolInput) (*FindToolOutput, error) {
	if input.ToolDescription == "" {
		return nil, fmt.Errorf("tool_description is required")
	}

	matches, err := d.store.Search(ctx, input.ToolDescription, d.toolNames)
	if err != nil {
		return nil, fmt.Errorf("tool search failed: %w", err)
	}

	matchedNames := make([]string, len(matches))
	for i, m := range matches {
		matchedNames[i] = m.Name
	}
	metrics := tokencounter.ComputeTokenMetrics(d.baselineTokens, d.tokenCounts, matchedNames)

	return &FindToolOutput{
		Tools:        matches,
		TokenMetrics: metrics,
	}, nil
}

// CallTool invokes a tool by name using its registered handler.
//
// The tool is looked up by exact name match. If found, the handler
// is invoked directly with the given parameters.
func (d *toolOptimizer) CallTool(ctx context.Context, input CallToolInput) (*mcp.CallToolResult, error) {
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

// newOptimizerFactoryWithStore returns an OptimizerFactory that creates
// toolOptimizer instances backed by the given ToolStore. All optimizers created
// by the returned factory share the same store, enabling cross-session search.
func newOptimizerFactoryWithStore(
	store types.ToolStore, counter tokencounter.Counter,
) func(context.Context, []server.ServerTool) (Optimizer, error) {
	return func(ctx context.Context, tools []server.ServerTool) (Optimizer, error) {
		return newToolOptimizer(ctx, store, counter, tools)
	}
}

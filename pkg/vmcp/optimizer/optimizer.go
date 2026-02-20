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
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

// Config defines configuration options for the Optimizer.
// It is defined in the internal/types package and aliased here so that
// external consumers continue to use optimizer.Config.
type Config = types.OptimizerConfig

// EmbeddingClient generates vector embeddings from text.
// It is defined in the internal/types package and aliased here so that
// external consumers can reference the type.
type EmbeddingClient = types.EmbeddingClient

// NewEmbeddingClient creates an EmbeddingClient from the given optimizer
// configuration. Returns (nil, nil) if cfg is nil or no embedding service
// is configured, meaning only keyword full-text search will be used.
func NewEmbeddingClient(cfg *Config) (EmbeddingClient, error) {
	return similarity.NewEmbeddingClient(cfg)
}

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
// It is defined in the internal/types package and aliased here so that
// external consumers continue to use optimizer.ToolMatch.
type ToolMatch = types.ToolMatch

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

// NewOptimizerFactory creates the embedding client and SQLite tool store from
// the given OptimizerConfig, then returns an OptimizerFactory and a cleanup
// function that closes the store. The caller must invoke the cleanup function
// during shutdown to release resources.
func NewOptimizerFactory(cfg *Config) (
	func(context.Context, []server.ServerTool) (Optimizer, error),
	func(context.Context) error,
	error,
) {
	embClient, err := NewEmbeddingClient(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create embedding client: %w", err)
	}

	store, err := NewSQLiteToolStore(embClient, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create optimizer store: %w", err)
	}

	factory := NewDummyOptimizerFactoryWithStore(store, DefaultTokenCounter())
	cleanup := func(_ context.Context) error {
		return store.Close()
	}

	return factory, cleanup, nil
}

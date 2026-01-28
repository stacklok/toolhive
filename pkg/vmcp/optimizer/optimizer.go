// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package optimizer provides semantic tool discovery for Virtual MCP Server.
//
// The optimizer reduces token usage by exposing only two tools to clients:
//   - optim_find_tool: Semantic search over available tools
//   - optim_call_tool: Dynamic invocation of backend tools
//
// This allows LLMs to discover relevant tools on-demand instead of receiving
// all tool definitions upfront.
//
// Architecture:
//   - Public API defined by Optimizer interface
//   - Implementation details in internal/ subpackages
//   - Embeddings generated once at startup for efficiency
package optimizer

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/logger"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/db"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/ingestion"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/models"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter"
)

// Config is a type alias for config.OptimizerConfig, provided for test compatibility.
// Deprecated: Use config.OptimizerConfig directly.
type Config = config.OptimizerConfig

// OptimizerIntegration is a type alias for EmbeddingOptimizer, provided for test compatibility.
// Deprecated: Use *EmbeddingOptimizer directly.
type OptimizerIntegration = EmbeddingOptimizer

// Optimizer defines the interface for intelligent tool discovery and invocation.
//
// Implementations manage their own lifecycle, including:
//   - Embedding generation and database management
//   - Backend tool ingestion at startup
//   - Resource cleanup on shutdown
//
// The optimizer is called via MCP tool handlers (optim_find_tool, optim_call_tool)
// which delegate to these methods.
type Optimizer interface {
	// FindTool searches for tools matching the given description and keywords.
	// Returns matching tools ranked by relevance with token savings metrics.
	FindTool(ctx context.Context, input FindToolInput) (*FindToolOutput, error)

	// CallTool invokes a tool by name with the given parameters.
	// Handles tool name resolution and routing to the correct backend.
	CallTool(ctx context.Context, input CallToolInput) (*mcp.CallToolResult, error)

	// Close cleans up optimizer resources (databases, caches, connections).
	Close() error

	// HandleSessionRegistration handles session-specific setup for optimizer mode.
	// Returns true if optimizer handled the registration, false otherwise.
	HandleSessionRegistration(
		ctx context.Context,
		sessionID string,
		caps *aggregator.AggregatedCapabilities,
		mcpServer *server.MCPServer,
		resourceConverter func([]vmcp.Resource) []server.ServerResource,
	) (bool, error)

	// OptimizerHandlerProvider provides tool handlers for adapter integration
	adapter.OptimizerHandlerProvider
}

// FindToolInput contains the parameters for finding tools.
type FindToolInput struct {
	// ToolDescription is a natural language description of the tool to find.
	ToolDescription string `json:"tool_description"`

	// ToolKeywords is an optional space-separated list of keywords to narrow search.
	ToolKeywords string `json:"tool_keywords,omitempty"`

	// Limit is the maximum number of tools to return (default: 10).
	Limit int `json:"limit,omitempty"`
}

// FindToolOutput contains the results of a tool search.
type FindToolOutput struct {
	// Tools contains the matching tools, ranked by relevance.
	Tools []ToolMatch `json:"tools"`

	// TokenMetrics provides information about token savings.
	TokenMetrics TokenMetrics `json:"token_metrics"`
}

// ToolMatch represents a tool that matched the search criteria.
type ToolMatch struct {
	// Name is the resolved name of the tool (after conflict resolution).
	Name string `json:"name"`

	// Description is the human-readable description of the tool.
	Description string `json:"description"`

	// InputSchema is the JSON schema for the tool's input parameters.
	InputSchema map[string]any `json:"input_schema"`

	// BackendID is the ID of the backend that provides this tool.
	BackendID string `json:"backend_id"`

	// SimilarityScore indicates relevance (0.0-1.0, higher is better).
	SimilarityScore float64 `json:"similarity_score"`

	// TokenCount is the estimated tokens for this tool's definition.
	TokenCount int `json:"token_count"`
}

// TokenMetrics provides information about token usage optimization.
type TokenMetrics struct {
	// BaselineTokens is the total tokens if all tools were sent.
	BaselineTokens int `json:"baseline_tokens"`

	// ReturnedTokens is the tokens for the returned tools.
	ReturnedTokens int `json:"returned_tokens"`

	// TokensSaved is the number of tokens saved by filtering.
	TokensSaved int `json:"tokens_saved"`

	// SavingsPercentage is the percentage of tokens saved (0-100).
	SavingsPercentage float64 `json:"savings_percentage"`
}

// CallToolInput contains the parameters for calling a tool.
type CallToolInput struct {
	// BackendID is the ID of the backend that provides the tool.
	BackendID string `json:"backend_id"`

	// ToolName is the name of the tool to invoke.
	ToolName string `json:"tool_name"`

	// Parameters are the arguments to pass to the tool.
	Parameters map[string]any `json:"parameters"`
}

// Factory creates an Optimizer instance with direct backend access.
// Called once at startup to enable efficient ingestion and embedding generation.
type Factory func(
	ctx context.Context,
	cfg *config.OptimizerConfig,
	mcpServer *server.MCPServer,
	backendClient vmcp.BackendClient,
	sessionManager *transportsession.Manager,
) (Optimizer, error)

// EmbeddingOptimizer implements Optimizer using semantic embeddings and hybrid search.
//
// Architecture:
//   - Uses chromem-go for vector embeddings (in-memory or persisted)
//   - Uses SQLite FTS5 for BM25 keyword search
//   - Combines both for hybrid semantic + keyword matching
//   - Ingests backends once at startup, not per-session
type EmbeddingOptimizer struct {
	config            *config.OptimizerConfig
	ingestionService  *ingestion.Service
	mcpServer         *server.MCPServer
	backendClient     vmcp.BackendClient
	sessionManager    *transportsession.Manager
	processedSessions sync.Map
	tracer            trace.Tracer
}

// NewIntegration is an alias for NewEmbeddingOptimizer, provided for test compatibility.
// Returns the concrete type to allow access to test helper methods.
// Deprecated: Use NewEmbeddingOptimizer directly.
func NewIntegration(
	ctx context.Context,
	cfg *config.OptimizerConfig,
	mcpServer *server.MCPServer,
	backendClient vmcp.BackendClient,
	sessionManager *transportsession.Manager,
) (*EmbeddingOptimizer, error) {
	opt, err := NewEmbeddingOptimizer(ctx, cfg, mcpServer, backendClient, sessionManager)
	if err != nil {
		return nil, err
	}
	if opt == nil {
		return nil, nil
	}
	return opt.(*EmbeddingOptimizer), nil
}

// NewEmbeddingOptimizer is a Factory that creates an embedding-based optimizer.
// This is the production implementation using semantic embeddings.
func NewEmbeddingOptimizer(
	ctx context.Context,
	cfg *config.OptimizerConfig,
	mcpServer *server.MCPServer,
	backendClient vmcp.BackendClient,
	sessionManager *transportsession.Manager,
) (Optimizer, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil // Optimizer disabled
	}

	// Initialize ingestion service with embedding backend
	ingestionCfg := &ingestion.Config{
		DBConfig: &db.Config{
			PersistPath: cfg.PersistPath,
			FTSDBPath:   cfg.FTSDBPath,
		},
		// Pass individual embedding fields
		EmbeddingBackend:   cfg.EmbeddingBackend,
		EmbeddingURL:       cfg.EmbeddingURL,
		EmbeddingModel:     cfg.EmbeddingModel,
		EmbeddingDimension: cfg.EmbeddingDimension,
	}

	svc, err := ingestion.NewService(ingestionCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize ingestion service: %w", err)
	}

	opt := &EmbeddingOptimizer{
		config:           cfg,
		ingestionService: svc,
		mcpServer:        mcpServer,
		backendClient:    backendClient,
		sessionManager:   sessionManager,
		tracer:           otel.Tracer("github.com/stacklok/toolhive/pkg/vmcp/optimizer"),
	}

	return opt, nil
}

// Ensure EmbeddingOptimizer implements Optimizer interface at compile time.
var _ Optimizer = (*EmbeddingOptimizer)(nil)

// FindTool implements Optimizer.FindTool using hybrid semantic + keyword search.
func (o *EmbeddingOptimizer) FindTool(ctx context.Context, input FindToolInput) (*FindToolOutput, error) {
	// Get database for search
	if o.ingestionService == nil {
		return nil, fmt.Errorf("ingestion service not initialized")
	}
	database := o.ingestionService.GetDatabase()
	if database == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// Configure hybrid search
	limit := input.Limit
	if limit <= 0 {
		limit = 10 // Default
	}

	// Handle HybridSearchRatio (pointer in config, with default)
	hybridRatio := 70 // Default
	if o.config.HybridSearchRatio != nil {
		hybridRatio = *o.config.HybridSearchRatio
	}

	hybridConfig := &db.HybridSearchConfig{
		SemanticRatio: hybridRatio,
		Limit:         limit,
		ServerID:      nil, // Search across all servers
	}

	// Build query text
	queryText := input.ToolDescription
	if input.ToolKeywords != "" {
		queryText = queryText + " " + input.ToolKeywords
	}

	// Execute hybrid search
	results, err := database.SearchToolsHybrid(ctx, queryText, hybridConfig)
	if err != nil {
		logger.Errorw("Hybrid search failed",
			"error", err,
			"tool_description", input.ToolDescription,
			"tool_keywords", input.ToolKeywords)
		return nil, fmt.Errorf("search failed: %w", err)
	}

	// Get routing table from context to resolve tool names
	var routingTable *vmcp.RoutingTable
	if capabilities, ok := discovery.DiscoveredCapabilitiesFromContext(ctx); ok && capabilities != nil {
		routingTable = capabilities.RoutingTable
	}

	// Convert results to output format
	tools, totalReturnedTokens := o.convertSearchResults(results, routingTable)

	// Calculate token metrics
	baselineTokens := o.ingestionService.GetTotalToolTokens(ctx)
	tokensSaved := baselineTokens - totalReturnedTokens
	savingsPercentage := 0.0
	if baselineTokens > 0 {
		savingsPercentage = (float64(tokensSaved) / float64(baselineTokens)) * 100.0
	}

	// Record OpenTelemetry metrics
	o.recordTokenMetrics(ctx, baselineTokens, totalReturnedTokens, tokensSaved, savingsPercentage)

	logger.Infow("optim_find_tool completed",
		"query", input.ToolDescription,
		"results_count", len(tools),
		"tokens_saved", tokensSaved,
		"savings_percentage", fmt.Sprintf("%.2f%%", savingsPercentage))

	return &FindToolOutput{
		Tools: tools,
		TokenMetrics: TokenMetrics{
			BaselineTokens:    baselineTokens,
			ReturnedTokens:    totalReturnedTokens,
			TokensSaved:       tokensSaved,
			SavingsPercentage: savingsPercentage,
		},
	}, nil
}

// CallTool implements Optimizer.CallTool by routing to the correct backend.
func (o *EmbeddingOptimizer) CallTool(ctx context.Context, input CallToolInput) (*mcp.CallToolResult, error) {
	// Resolve target backend
	target, backendToolName, err := o.resolveToolTarget(ctx, input.BackendID, input.ToolName)
	if err != nil {
		return nil, err
	}

	logger.Infow("Calling tool via optimizer",
		"backend_id", input.BackendID,
		"tool_name", input.ToolName,
		"backend_tool_name", backendToolName,
		"workload_name", target.WorkloadName)

	// Call the tool on the backend
	result, err := o.backendClient.CallTool(ctx, target, backendToolName, input.Parameters, nil)
	if err != nil {
		logger.Errorw("Tool call failed",
			"error", err,
			"backend_id", input.BackendID,
			"tool_name", input.ToolName,
			"backend_tool_name", backendToolName)
		return nil, fmt.Errorf("tool call failed: %w", err)
	}

	// Convert result to MCP format
	mcpResult := convertToolResult(result)

	logger.Infow("optim_call_tool completed successfully",
		"backend_id", input.BackendID,
		"tool_name", input.ToolName)

	return mcpResult, nil
}

// Close implements Optimizer.Close by cleaning up resources.
func (o *EmbeddingOptimizer) Close() error {
	if o == nil || o.ingestionService == nil {
		return nil
	}
	return o.ingestionService.Close()
}

// HandleSessionRegistration implements Optimizer.HandleSessionRegistration.
func (o *EmbeddingOptimizer) HandleSessionRegistration(
	_ context.Context,
	sessionID string,
	caps *aggregator.AggregatedCapabilities,
	mcpServer *server.MCPServer,
	resourceConverter func([]vmcp.Resource) []server.ServerResource,
) (bool, error) {
	logger.Debugw("HandleSessionRegistration called for optimizer mode", "session_id", sessionID)

	// Register optimizer tools for this session
	optimizerTools, err := adapter.CreateOptimizerTools(o)
	if err != nil {
		return false, fmt.Errorf("failed to create optimizer tools: %w", err)
	}

	// Add optimizer tools to session
	if err := mcpServer.AddSessionTools(sessionID, optimizerTools...); err != nil {
		return false, fmt.Errorf("failed to add optimizer tools to session: %w", err)
	}

	logger.Debugw("Optimizer tools registered for session", "session_id", sessionID)

	// Inject resources (but not backend tools or composite tools)
	if len(caps.Resources) > 0 {
		sdkResources := resourceConverter(caps.Resources)
		if err := mcpServer.AddSessionResources(sessionID, sdkResources...); err != nil {
			return false, fmt.Errorf("failed to add session resources: %w", err)
		}
		logger.Debugw("Added session resources (optimizer mode)",
			"session_id", sessionID,
			"count", len(sdkResources))
	}

	logger.Infow("Optimizer mode: backend tools not exposed directly",
		"session_id", sessionID,
		"backend_tool_count", len(caps.Tools),
		"resource_count", len(caps.Resources))

	return true, nil // Optimizer handled the registration
}

// CreateFindToolHandler implements adapter.OptimizerHandlerProvider.
func (o *EmbeddingOptimizer) CreateFindToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Debugw("optim_find_tool called", "request", request)

		// Extract parameters
		args, ok := request.Params.Arguments.(map[string]any)
		if !ok {
			return mcp.NewToolResultError("invalid arguments: expected object"), nil
		}

		// Extract and validate parameters
		toolDescription, toolKeywords, limit, err := extractFindToolParams(args)
		if err != nil {
			return err, nil
		}

		// Call FindTool
		output, findErr := o.FindTool(ctx, FindToolInput{
			ToolDescription: toolDescription,
			ToolKeywords:    toolKeywords,
			Limit:           limit,
		})
		if findErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", findErr)), nil
		}

		// Marshal response to JSON
		responseJSON, marshalErr := json.Marshal(output)
		if marshalErr != nil {
			logger.Errorw("Failed to marshal response", "error", marshalErr)
			return mcp.NewToolResultError(fmt.Sprintf("failed to marshal response: %v", marshalErr)), nil
		}

		return mcp.NewToolResultText(string(responseJSON)), nil
	}
}

// CreateCallToolHandler implements adapter.OptimizerHandlerProvider.
func (o *EmbeddingOptimizer) CreateCallToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Debugw("optim_call_tool called", "request", request)

		// Parse request
		backendID, toolName, parameters, err := parseCallToolRequest(request)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Call CallTool
		result, err := o.CallTool(ctx, CallToolInput{
			BackendID:  backendID,
			ToolName:   toolName,
			Parameters: parameters,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return result, nil
	}
}

// Initialize performs optimizer initialization (registers tools, ingests backends).
// This should be called once during server startup.
func (o *EmbeddingOptimizer) Initialize(
	ctx context.Context,
	mcpServer *server.MCPServer,
	backendRegistry vmcp.BackendRegistry,
) error {
	// Register optimizer tools globally
	optimizerTools, err := adapter.CreateOptimizerTools(o)
	if err != nil {
		return fmt.Errorf("failed to create optimizer tools: %w", err)
	}
	for _, tool := range optimizerTools {
		mcpServer.AddTool(tool.Tool, tool.Handler)
	}
	logger.Info("Optimizer tools registered globally")

	// Ingest discovered backends
	initialBackends := backendRegistry.List(ctx)
	if err := o.IngestInitialBackends(ctx, initialBackends); err != nil {
		logger.Warnf("Failed to ingest initial backends: %v", err)
		// Don't fail initialization - optimizer can still work with incremental ingestion
	}

	return nil
}

// IngestInitialBackends ingests all discovered backends and their tools at startup.
func (o *EmbeddingOptimizer) IngestInitialBackends(ctx context.Context, backends []vmcp.Backend) error {
	if o == nil || o.ingestionService == nil {
		logger.Infow("Optimizer disabled, embedding time: 0ms")
		return nil
	}

	// Reset embedding time before starting ingestion
	o.ingestionService.ResetEmbeddingTime()

	// Create a span for the entire ingestion process
	ctx, span := o.tracer.Start(ctx, "optimizer.ingestion.ingest_initial_backends",
		trace.WithAttributes(
			attribute.Int("backends.count", len(backends)),
		))
	defer span.End()

	start := time.Now()
	logger.Infof("Ingesting %d discovered backends into optimizer", len(backends))

	ingestedCount := 0
	totalToolsIngested := 0
	for _, backend := range backends {
		// Create a span for each backend ingestion
		backendCtx, backendSpan := o.tracer.Start(ctx, "optimizer.ingestion.ingest_backend",
			trace.WithAttributes(
				attribute.String("backend.id", backend.ID),
				attribute.String("backend.name", backend.Name),
			))

		// Convert Backend to BackendTarget for client API
		target := vmcp.BackendToTarget(&backend)
		if target == nil {
			logger.Warnf("Failed to convert backend %s to target", backend.Name)
			backendSpan.RecordError(fmt.Errorf("failed to convert backend to target"))
			backendSpan.SetStatus(codes.Error, "conversion failed")
			backendSpan.End()
			continue
		}

		// Query backend capabilities to get its tools
		capabilities, err := o.backendClient.ListCapabilities(backendCtx, target)
		if err != nil {
			logger.Warnf("Failed to query capabilities for backend %s: %v", backend.Name, err)
			backendSpan.RecordError(err)
			backendSpan.SetStatus(codes.Error, err.Error())
			backendSpan.End()
			continue
		}

		// Extract tools from capabilities
		var tools []mcp.Tool
		for _, tool := range capabilities.Tools {
			tools = append(tools, mcp.Tool{
				Name:        tool.Name,
				Description: tool.Description,
			})
		}

		// Get description from metadata
		var description *string
		if backend.Metadata != nil {
			if desc := backend.Metadata["description"]; desc != "" {
				description = &desc
			}
		}

		backendSpan.SetAttributes(
			attribute.Int("tools.count", len(tools)),
		)

		// Ingest this backend's tools
		if err := o.ingestionService.IngestServer(
			backendCtx,
			backend.ID,
			backend.Name,
			description,
			tools,
		); err != nil {
			logger.Warnf("Failed to ingest backend %s: %v", backend.Name, err)
			backendSpan.RecordError(err)
			backendSpan.SetStatus(codes.Error, err.Error())
			backendSpan.End()
			continue
		}
		ingestedCount++
		totalToolsIngested += len(tools)
		backendSpan.SetAttributes(
			attribute.Int("tools.ingested", len(tools)),
		)
		backendSpan.SetStatus(codes.Ok, "backend ingested successfully")
		backendSpan.End()
	}

	// Get total embedding time
	totalEmbeddingTime := o.ingestionService.GetTotalEmbeddingTime()
	totalDuration := time.Since(start)

	span.SetAttributes(
		attribute.Int64("ingestion.duration_ms", totalDuration.Milliseconds()),
		attribute.Int64("embedding.duration_ms", totalEmbeddingTime.Milliseconds()),
		attribute.Int("backends.ingested", ingestedCount),
		attribute.Int("tools.ingested", totalToolsIngested),
	)

	logger.Infow("Initial backend ingestion completed",
		"servers_ingested", ingestedCount,
		"tools_ingested", totalToolsIngested,
		"total_duration_ms", totalDuration.Milliseconds(),
		"total_embedding_time_ms", totalEmbeddingTime.Milliseconds(),
		"embedding_time_percentage", fmt.Sprintf("%.2f%%", float64(totalEmbeddingTime)/float64(totalDuration)*100))

	return nil
}

// Helper methods

// convertSearchResults converts database search results to ToolMatch format.
func (o *EmbeddingOptimizer) convertSearchResults(
	results []*models.BackendToolWithMetadata,
	routingTable *vmcp.RoutingTable,
) ([]ToolMatch, int) {
	tools := make([]ToolMatch, 0, len(results))
	totalReturnedTokens := 0

	for _, result := range results {
		// Unmarshal InputSchema
		var inputSchema map[string]any
		if len(result.InputSchema) > 0 {
			if err := json.Unmarshal(result.InputSchema, &inputSchema); err != nil {
				logger.Warnw("Failed to unmarshal input schema",
					"tool_id", result.ID,
					"tool_name", result.ToolName,
					"error", err)
				inputSchema = map[string]any{}
			}
		}

		// Handle nil description
		description := ""
		if result.Description != nil {
			description = *result.Description
		}

		// Resolve tool name using routing table
		resolvedName := resolveToolName(routingTable, result.MCPServerID, result.ToolName)

		tool := ToolMatch{
			Name:            resolvedName,
			Description:     description,
			InputSchema:     inputSchema,
			BackendID:       result.MCPServerID,
			SimilarityScore: float64(result.Similarity),
			TokenCount:      result.TokenCount,
		}
		tools = append(tools, tool)
		totalReturnedTokens += result.TokenCount
	}

	return tools, totalReturnedTokens
}

// resolveToolTarget finds and validates the target backend for a tool.
func (o *EmbeddingOptimizer) resolveToolTarget(
	ctx context.Context,
	backendID string,
	toolName string,
) (*vmcp.BackendTarget, string, error) {
	capabilities, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
	if !ok || capabilities == nil {
		return nil, "", fmt.Errorf("routing information not available in context")
	}

	if capabilities.RoutingTable == nil || capabilities.RoutingTable.Tools == nil {
		return nil, "", fmt.Errorf("routing table not initialized")
	}

	target, exists := capabilities.RoutingTable.Tools[toolName]
	if !exists {
		return nil, "", fmt.Errorf("tool not found in routing table: %s", toolName)
	}

	if target.WorkloadID != backendID {
		return nil, "", fmt.Errorf("tool %s belongs to backend %s, not %s",
			toolName, target.WorkloadID, backendID)
	}

	backendToolName := target.GetBackendCapabilityName(toolName)
	return target, backendToolName, nil
}

// recordTokenMetrics records OpenTelemetry metrics for token savings.
func (*EmbeddingOptimizer) recordTokenMetrics(
	ctx context.Context,
	baselineTokens int,
	returnedTokens int,
	tokensSaved int,
	savingsPercentage float64,
) {
	meter := otel.Meter("github.com/stacklok/toolhive/pkg/vmcp/optimizer")

	baselineCounter, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_baseline_tokens",
		metric.WithDescription("Total tokens for all tools in the optimizer database (baseline)"),
	)
	if err != nil {
		logger.Debugw("Failed to create baseline_tokens counter", "error", err)
		return
	}

	returnedCounter, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_returned_tokens",
		metric.WithDescription("Total tokens for tools returned by optim_find_tool"),
	)
	if err != nil {
		logger.Debugw("Failed to create returned_tokens counter", "error", err)
		return
	}

	savedCounter, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_tokens_saved",
		metric.WithDescription("Number of tokens saved by filtering tools with optim_find_tool"),
	)
	if err != nil {
		logger.Debugw("Failed to create tokens_saved counter", "error", err)
		return
	}

	savingsGauge, err := meter.Float64Gauge(
		"toolhive_vmcp_optimizer_savings_percentage",
		metric.WithDescription("Percentage of tokens saved by filtering tools (0-100)"),
		metric.WithUnit("%"),
	)
	if err != nil {
		logger.Debugw("Failed to create savings_percentage gauge", "error", err)
		return
	}

	attrs := metric.WithAttributes(
		attribute.String("operation", "find_tool"),
	)

	baselineCounter.Add(ctx, int64(baselineTokens), attrs)
	returnedCounter.Add(ctx, int64(returnedTokens), attrs)
	savedCounter.Add(ctx, int64(tokensSaved), attrs)
	savingsGauge.Record(ctx, savingsPercentage, attrs)
}

// Helper functions

// extractFindToolParams extracts and validates parameters from the find_tool request.
func extractFindToolParams(args map[string]any) (toolDescription, toolKeywords string, limit int, err *mcp.CallToolResult) {
	toolDescription, ok := args["tool_description"].(string)
	if !ok || toolDescription == "" {
		return "", "", 0, mcp.NewToolResultError("tool_description is required and must be a non-empty string")
	}

	toolKeywords, _ = args["tool_keywords"].(string)

	limit = 10 // Default
	if limitVal, ok := args["limit"]; ok {
		if limitFloat, ok := limitVal.(float64); ok {
			limit = int(limitFloat)
		}
	}

	return toolDescription, toolKeywords, limit, nil
}

// parseCallToolRequest extracts and validates parameters from the call_tool request.
func parseCallToolRequest(request mcp.CallToolRequest) (backendID, toolName string, parameters map[string]any, err error) {
	args, ok := request.Params.Arguments.(map[string]any)
	if !ok {
		return "", "", nil, fmt.Errorf("invalid arguments: expected object")
	}

	backendID, ok = args["backend_id"].(string)
	if !ok || backendID == "" {
		return "", "", nil, fmt.Errorf("backend_id is required and must be a non-empty string")
	}

	toolName, ok = args["tool_name"].(string)
	if !ok || toolName == "" {
		return "", "", nil, fmt.Errorf("tool_name is required and must be a non-empty string")
	}

	parameters, ok = args["parameters"].(map[string]any)
	if !ok {
		return "", "", nil, fmt.Errorf("parameters is required and must be an object")
	}

	return backendID, toolName, parameters, nil
}

// resolveToolName looks up the resolved name for a tool in the routing table.
func resolveToolName(routingTable *vmcp.RoutingTable, backendID string, originalName string) string {
	if routingTable == nil || routingTable.Tools == nil {
		return originalName
	}

	for resolvedName, target := range routingTable.Tools {
		// Case 1: Tool was renamed
		if target.WorkloadID == backendID && target.OriginalCapabilityName == originalName {
			return resolvedName
		}

		// Case 2: Tool was not renamed
		if target.WorkloadID == backendID && target.OriginalCapabilityName == "" && resolvedName == originalName {
			return resolvedName
		}
	}

	return originalName // Fallback
}

// convertToolResult converts vmcp.ToolCallResult to mcp.CallToolResult.
func convertToolResult(result *vmcp.ToolCallResult) *mcp.CallToolResult {
	mcpContent := make([]mcp.Content, len(result.Content))
	for i, content := range result.Content {
		mcpContent[i] = convertVMCPContent(content)
	}

	return &mcp.CallToolResult{
		Content: mcpContent,
		IsError: result.IsError,
	}
}

// convertVMCPContent converts a vmcp.Content to mcp.Content.
func convertVMCPContent(content vmcp.Content) mcp.Content {
	switch content.Type {
	case "text":
		return mcp.NewTextContent(content.Text)
	case "image":
		return mcp.NewImageContent(content.Data, content.MimeType)
	case "audio":
		return mcp.NewAudioContent(content.Data, content.MimeType)
	case "resource":
		logger.Warnw("Converting resource content to text - embedded resources not yet supported")
		return mcp.NewTextContent("")
	default:
		logger.Warnw("Converting unknown content type to text", "type", content.Type)
		return mcp.NewTextContent("")
	}
}

// OnRegisterSession is a test helper that registers a session without all the infrastructure setup.
// It's a simplified version for testing purposes.
func (o *EmbeddingOptimizer) OnRegisterSession(
	_ context.Context,
	_ interface{}, // session - not used in simplified test version
	_ *aggregator.AggregatedCapabilities, // capabilities - not used in simplified test version
) error {
	// Test helper - no-op implementation
	if o == nil {
		return nil
	}
	return nil
}

// RegisterTools is a test helper for registering optimizer tools with a session.
// It's a simplified version for testing purposes.
func (o *EmbeddingOptimizer) RegisterTools(
	_ context.Context,
	_ interface{}, // session - not used in simplified test version
) error {
	// Test helper - no-op implementation (or could panic if o is nil)
	if o == nil {
		return nil
	}
	return nil
}

// IngestToolsForTesting manually ingests tools for testing purposes.
// This is a test helper that bypasses the normal ingestion flow.
func (o *EmbeddingOptimizer) IngestToolsForTesting(
	ctx context.Context,
	serverID string,
	serverName string,
	description *string,
	tools []mcp.Tool,
) error {
	if o.ingestionService == nil {
		return fmt.Errorf("optimizer integration not initialized")
	}
	return o.ingestionService.IngestServer(ctx, serverID, serverName, description, tools)
}

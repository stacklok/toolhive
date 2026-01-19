// Package optimizer provides vMCP integration for semantic tool discovery.
//
// This package implements the RFC-0022 optimizer integration, exposing:
// - optim.find_tool: Semantic/keyword-based tool discovery
// - optim.call_tool: Dynamic tool invocation across backends
//
// Architecture:
//   - Embeddings are generated during session initialization (OnRegisterSession hook)
//   - Tools are exposed as standard MCP tools callable via tools/call
//   - Integrates with vMCP's two-boundary authentication model
//   - Uses existing router for backend tool invocation
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
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/optimizer/db"
	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
	"github.com/stacklok/toolhive/pkg/optimizer/ingestion"
	"github.com/stacklok/toolhive/pkg/optimizer/models"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
)

// Config holds optimizer configuration for vMCP integration.
type Config struct {
	// Enabled controls whether optimizer tools are available
	Enabled bool

	// PersistPath is the optional path for chromem-go database persistence (empty = in-memory)
	PersistPath string

	// FTSDBPath is the path to SQLite FTS5 database for BM25 search
	// (empty = auto-default: ":memory:" or "{PersistPath}/fts.db")
	FTSDBPath string

	// HybridSearchRatio controls semantic vs BM25 mix (0.0-1.0, default: 0.7)
	HybridSearchRatio float64

	// EmbeddingConfig configures the embedding backend (vLLM, Ollama, placeholder)
	EmbeddingConfig *embeddings.Config
}

// OptimizerIntegration manages optimizer functionality within vMCP.
//
//nolint:revive // Name is intentional for clarity in external packages
type OptimizerIntegration struct {
	config            *Config
	ingestionService  *ingestion.Service
	mcpServer         *server.MCPServer  // For registering tools
	backendClient     vmcp.BackendClient // For querying backends at startup
	sessionManager    *transportsession.Manager
	processedSessions sync.Map // Track sessions that have already been processed
	tracer            trace.Tracer
}

// NewIntegration creates a new optimizer integration.
func NewIntegration(
	_ context.Context,
	cfg *Config,
	mcpServer *server.MCPServer,
	backendClient vmcp.BackendClient,
	sessionManager *transportsession.Manager,
) (*OptimizerIntegration, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil // Optimizer disabled
	}

	// Initialize ingestion service with embedding backend
	ingestionCfg := &ingestion.Config{
		DBConfig: &db.Config{
			PersistPath: cfg.PersistPath,
			FTSDBPath:   cfg.FTSDBPath,
		},
		EmbeddingConfig: cfg.EmbeddingConfig,
	}

	svc, err := ingestion.NewService(ingestionCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize optimizer service: %w", err)
	}

	return &OptimizerIntegration{
		config:           cfg,
		ingestionService: svc,
		mcpServer:        mcpServer,
		backendClient:    backendClient,
		sessionManager:   sessionManager,
		tracer:           otel.Tracer("github.com/stacklok/toolhive/pkg/vmcp/optimizer"),
	}, nil
}

// OnRegisterSession is called during session initialization to generate embeddings
// and register optimizer tools.
//
// This hook:
// 1. Extracts backend tools from discovered capabilities
// 2. Generates embeddings for all tools (parallel per-backend)
// 3. Registers optim.find_tool and optim.call_tool as session tools
func (o *OptimizerIntegration) OnRegisterSession(
	_ context.Context,
	session server.ClientSession,
	_ *aggregator.AggregatedCapabilities,
) error {
	if o == nil {
		return nil // Optimizer not enabled
	}

	sessionID := session.SessionID()

	logger.Debugw("OnRegisterSession called", "session_id", sessionID)

	// Check if this session has already been processed
	if _, alreadyProcessed := o.processedSessions.LoadOrStore(sessionID, true); alreadyProcessed {
		logger.Debugw("Session already processed, skipping duplicate ingestion",
			"session_id", sessionID)
		return nil
	}

	// Skip ingestion in OnRegisterSession - IngestInitialBackends already handles ingestion at startup
	// This prevents duplicate ingestion when sessions are registered
	// The optimizer database is populated once at startup, not per-session
	logger.Infow("Skipping ingestion in OnRegisterSession (handled by IngestInitialBackends at startup)",
		"session_id", sessionID)

	return nil
}

// RegisterTools adds optimizer tools to the session.
// This should be called after OnRegisterSession completes.
func (o *OptimizerIntegration) RegisterTools(_ context.Context, session server.ClientSession) error {
	if o == nil {
		return nil // Optimizer not enabled
	}

	sessionID := session.SessionID()

	// Define optimizer tools with handlers
	optimizerTools := []server.ServerTool{
		{
			Tool: mcp.Tool{
				Name:        "optim.find_tool",
				Description: "Semantic search across all backend tools using natural language description and optional keywords",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"tool_description": map[string]any{
							"type":        "string",
							"description": "Natural language description of the tool you're looking for",
						},
						"tool_keywords": map[string]any{
							"type":        "string",
							"description": "Optional space-separated keywords for keyword-based search",
						},
						"limit": map[string]any{
							"type":        "integer",
							"description": "Maximum number of tools to return (default: 10)",
							"default":     10,
						},
					},
					Required: []string{"tool_description"},
				},
			},
			Handler: o.createFindToolHandler(),
		},
		{
			Tool: mcp.Tool{
				Name:        "optim.call_tool",
				Description: "Dynamically invoke any tool on any backend using the backend_id from find_tool",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"backend_id": map[string]any{
							"type":        "string",
							"description": "Backend ID from find_tool results",
						},
						"tool_name": map[string]any{
							"type":        "string",
							"description": "Tool name to invoke",
						},
						"parameters": map[string]any{
							"type":        "object",
							"description": "Parameters to pass to the tool",
						},
					},
					Required: []string{"backend_id", "tool_name", "parameters"},
				},
			},
			Handler: o.CreateCallToolHandler(),
		},
	}

	// Add tools to session
	if err := o.mcpServer.AddSessionTools(sessionID, optimizerTools...); err != nil {
		return fmt.Errorf("failed to add optimizer tools to session: %w", err)
	}

	logger.Debugw("Optimizer tools registered", "session_id", sessionID)
	return nil
}

// CreateFindToolHandler creates the handler for optim.find_tool
// Exported for testing purposes
func (o *OptimizerIntegration) CreateFindToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return o.createFindToolHandler()
}

// extractFindToolParams extracts and validates parameters from the find_tool request
func extractFindToolParams(args map[string]any) (toolDescription, toolKeywords string, limit int, err *mcp.CallToolResult) {
	// Extract tool_description (required)
	toolDescription, ok := args["tool_description"].(string)
	if !ok || toolDescription == "" {
		return "", "", 0, mcp.NewToolResultError("tool_description is required and must be a non-empty string")
	}

	// Extract tool_keywords (optional)
	toolKeywords, _ = args["tool_keywords"].(string)

	// Extract limit (optional, default: 10)
	limit = 10
	if limitVal, ok := args["limit"]; ok {
		if limitFloat, ok := limitVal.(float64); ok {
			limit = int(limitFloat)
		}
	}

	return toolDescription, toolKeywords, limit, nil
}

// resolveToolName looks up the resolved name for a tool in the routing table.
// Returns the resolved name if found, otherwise returns the original name.
//
// The routing table maps resolved names (after conflict resolution) to BackendTarget.
// Each BackendTarget contains:
//   - WorkloadID: the backend ID
//   - OriginalCapabilityName: the original tool name (empty if not renamed)
//
// We need to find the resolved name by matching backend ID and original name.
func resolveToolName(routingTable *vmcp.RoutingTable, backendID string, originalName string) string {
	if routingTable == nil || routingTable.Tools == nil {
		return originalName
	}

	// Search through routing table to find the resolved name
	// Match by backend ID and original capability name
	for resolvedName, target := range routingTable.Tools {
		// Case 1: Tool was renamed (OriginalCapabilityName is set)
		// Match by backend ID and original name
		if target.WorkloadID == backendID && target.OriginalCapabilityName == originalName {
			logger.Debugw("Resolved tool name (renamed)",
				"backend_id", backendID,
				"original_name", originalName,
				"resolved_name", resolvedName)
			return resolvedName
		}

		// Case 2: Tool was not renamed (OriginalCapabilityName is empty)
		// Match by backend ID and resolved name equals original name
		if target.WorkloadID == backendID && target.OriginalCapabilityName == "" && resolvedName == originalName {
			logger.Debugw("Resolved tool name (not renamed)",
				"backend_id", backendID,
				"original_name", originalName,
				"resolved_name", resolvedName)
			return resolvedName
		}
	}

	// If not found, return original name (fallback for tools not in routing table)
	// This can happen if:
	// - Tool was just ingested but routing table hasn't been updated yet
	// - Tool belongs to a backend that's not currently registered
	logger.Debugw("Tool name not found in routing table, using original name",
		"backend_id", backendID,
		"original_name", originalName)
	return originalName
}

// convertSearchResultsToResponse converts database search results to the response format.
// It resolves tool names using the routing table to ensure returned names match routing table keys.
func convertSearchResultsToResponse(
	results []*models.BackendToolWithMetadata,
	routingTable *vmcp.RoutingTable,
) ([]map[string]any, int) {
	responseTools := make([]map[string]any, 0, len(results))
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
				inputSchema = map[string]any{} // Use empty schema on error
			}
		}

		// Handle nil description
		description := ""
		if result.Description != nil {
			description = *result.Description
		}

		// Resolve tool name using routing table to ensure it matches routing table keys
		resolvedName := resolveToolName(routingTable, result.MCPServerID, result.ToolName)

		tool := map[string]any{
			"name":             resolvedName,
			"description":      description,
			"input_schema":     inputSchema,
			"backend_id":       result.MCPServerID,
			"similarity_score": result.Similarity,
			"token_count":      result.TokenCount,
		}
		responseTools = append(responseTools, tool)
		totalReturnedTokens += result.TokenCount
	}

	return responseTools, totalReturnedTokens
}

// createFindToolHandler creates the handler for optim.find_tool
func (o *OptimizerIntegration) createFindToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Debugw("optim.find_tool called", "request", request)

		// Extract parameters from request arguments
		args, ok := request.Params.Arguments.(map[string]any)
		if !ok {
			return mcp.NewToolResultError("invalid arguments: expected object"), nil
		}

		// Extract and validate parameters
		toolDescription, toolKeywords, limit, err := extractFindToolParams(args)
		if err != nil {
			return err, nil
		}

		// Perform hybrid search using database operations
		if o.ingestionService == nil {
			return mcp.NewToolResultError("backend tool operations not initialized"), nil
		}
		backendToolOps := o.ingestionService.GetBackendToolOps()
		if backendToolOps == nil {
			return mcp.NewToolResultError("backend tool operations not initialized"), nil
		}

		// Configure hybrid search
		hybridConfig := &db.HybridSearchConfig{
			SemanticRatio: o.config.HybridSearchRatio,
			Limit:         limit,
			ServerID:      nil, // Search across all servers
		}

		// Execute hybrid search
		queryText := toolDescription
		if toolKeywords != "" {
			queryText = toolDescription + " " + toolKeywords
		}
		results, err2 := backendToolOps.SearchHybrid(ctx, queryText, hybridConfig)
		if err2 != nil {
			logger.Errorw("Hybrid search failed",
				"error", err2,
				"tool_description", toolDescription,
				"tool_keywords", toolKeywords,
				"query_text", queryText)
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err2)), nil
		}

		// Get routing table from context to resolve tool names
		var routingTable *vmcp.RoutingTable
		if capabilities, ok := discovery.DiscoveredCapabilitiesFromContext(ctx); ok && capabilities != nil {
			routingTable = capabilities.RoutingTable
		}

		// Convert results to response format, resolving tool names to match routing table
		responseTools, totalReturnedTokens := convertSearchResultsToResponse(results, routingTable)

		// Calculate token metrics
		baselineTokens := o.ingestionService.GetTotalToolTokens(ctx)
		tokensSaved := baselineTokens - totalReturnedTokens
		savingsPercentage := 0.0
		if baselineTokens > 0 {
			savingsPercentage = (float64(tokensSaved) / float64(baselineTokens)) * 100.0
		}

		tokenMetrics := map[string]any{
			"baseline_tokens":    baselineTokens,
			"returned_tokens":    totalReturnedTokens,
			"tokens_saved":       tokensSaved,
			"savings_percentage": savingsPercentage,
		}

		// Record OpenTelemetry metrics for token savings
		o.recordTokenMetrics(ctx, baselineTokens, totalReturnedTokens, tokensSaved, savingsPercentage)

		// Build response
		response := map[string]any{
			"tools":         responseTools,
			"token_metrics": tokenMetrics,
		}

		// Marshal to JSON for the result
		responseJSON, err3 := json.Marshal(response)
		if err3 != nil {
			logger.Errorw("Failed to marshal response", "error", err3)
			return mcp.NewToolResultError(fmt.Sprintf("failed to marshal response: %v", err3)), nil
		}

		logger.Infow("optim.find_tool completed",
			"query", toolDescription,
			"results_count", len(responseTools),
			"tokens_saved", tokensSaved,
			"savings_percentage", fmt.Sprintf("%.2f%%", savingsPercentage))

		return mcp.NewToolResultText(string(responseJSON)), nil
	}
}

// recordTokenMetrics records OpenTelemetry metrics for token savings
func (_ *OptimizerIntegration) recordTokenMetrics(
	ctx context.Context,
	baselineTokens int,
	returnedTokens int,
	tokensSaved int,
	savingsPercentage float64,
) {
	// Get meter from global OpenTelemetry provider
	meter := otel.Meter("github.com/stacklok/toolhive/pkg/vmcp/optimizer")

	// Create metrics if they don't exist (they'll be cached by the meter)
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
		metric.WithDescription("Total tokens for tools returned by optim.find_tool"),
	)
	if err != nil {
		logger.Debugw("Failed to create returned_tokens counter", "error", err)
		return
	}

	savedCounter, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_tokens_saved",
		metric.WithDescription("Number of tokens saved by filtering tools with optim.find_tool"),
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

	// Record metrics with attributes
	attrs := metric.WithAttributes(
		attribute.String("operation", "find_tool"),
	)

	baselineCounter.Add(ctx, int64(baselineTokens), attrs)
	returnedCounter.Add(ctx, int64(returnedTokens), attrs)
	savedCounter.Add(ctx, int64(tokensSaved), attrs)
	savingsGauge.Record(ctx, savingsPercentage, attrs)

	logger.Debugw("Token metrics recorded",
		"baseline_tokens", baselineTokens,
		"returned_tokens", returnedTokens,
		"tokens_saved", tokensSaved,
		"savings_percentage", savingsPercentage)
}

// CreateCallToolHandler creates the handler for optim.call_tool
// Exported for testing purposes
func (o *OptimizerIntegration) CreateCallToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return o.createCallToolHandler()
}

// createCallToolHandler creates the handler for optim.call_tool
func (o *OptimizerIntegration) createCallToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Debugw("optim.call_tool called", "request", request)

		// Extract parameters from request arguments
		args, ok := request.Params.Arguments.(map[string]any)
		if !ok {
			return mcp.NewToolResultError("invalid arguments: expected object"), nil
		}

		// Extract backend_id (required)
		backendID, ok := args["backend_id"].(string)
		if !ok || backendID == "" {
			return mcp.NewToolResultError("backend_id is required and must be a non-empty string"), nil
		}

		// Extract tool_name (required)
		toolName, ok := args["tool_name"].(string)
		if !ok || toolName == "" {
			return mcp.NewToolResultError("tool_name is required and must be a non-empty string"), nil
		}

		// Extract parameters (required)
		parameters, ok := args["parameters"].(map[string]any)
		if !ok {
			return mcp.NewToolResultError("parameters is required and must be an object"), nil
		}

		// Get routing table from context via discovered capabilities
		capabilities, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
		if !ok || capabilities == nil {
			return mcp.NewToolResultError("routing information not available in context"), nil
		}

		if capabilities.RoutingTable == nil || capabilities.RoutingTable.Tools == nil {
			return mcp.NewToolResultError("routing table not initialized"), nil
		}

		// Find the tool in the routing table
		target, exists := capabilities.RoutingTable.Tools[toolName]
		if !exists {
			return mcp.NewToolResultError(fmt.Sprintf("tool not found in routing table: %s", toolName)), nil
		}

		// Verify the tool belongs to the specified backend
		if target.WorkloadID != backendID {
			return mcp.NewToolResultError(fmt.Sprintf(
				"tool %s belongs to backend %s, not %s",
				toolName,
				target.WorkloadID,
				backendID,
			)), nil
		}

		// Get the backend capability name (handles renamed tools)
		backendToolName := target.GetBackendCapabilityName(toolName)

		logger.Infow("Calling tool via optimizer",
			"backend_id", backendID,
			"tool_name", toolName,
			"backend_tool_name", backendToolName,
			"workload_name", target.WorkloadName)

		// Call the tool on the backend using the backend client
		result, err := o.backendClient.CallTool(ctx, target, backendToolName, parameters)
		if err != nil {
			logger.Errorw("Tool call failed",
				"error", err,
				"backend_id", backendID,
				"tool_name", toolName,
				"backend_tool_name", backendToolName)
			return mcp.NewToolResultError(fmt.Sprintf("tool call failed: %v", err)), nil
		}

		// Convert result to JSON
		resultJSON, err := json.Marshal(result)
		if err != nil {
			logger.Errorw("Failed to marshal tool result", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to marshal result: %v", err)), nil
		}

		logger.Infow("optim.call_tool completed successfully",
			"backend_id", backendID,
			"tool_name", toolName)

		return mcp.NewToolResultText(string(resultJSON)), nil
	}
}

// IngestInitialBackends ingests all discovered backends and their tools at startup.
// This should be called after backends are discovered during server initialization.
func (o *OptimizerIntegration) IngestInitialBackends(ctx context.Context, backends []vmcp.Backend) error {
	if o == nil || o.ingestionService == nil {
		// Optimizer disabled - log that embedding time is 0
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
		// Convert Backend to BackendTarget for client API
		target := vmcp.BackendToTarget(&backend)
		if target == nil {
			logger.Warnf("Failed to convert backend %s to target", backend.Name)
			continue
		}

		// Query backend capabilities to get its tools
		capabilities, err := o.backendClient.ListCapabilities(ctx, target)
		if err != nil {
			logger.Warnf("Failed to query capabilities for backend %s: %v", backend.Name, err)
			continue // Skip this backend but continue with others
		}

		// Extract tools from capabilities
		// Note: For ingestion, we only need name and description (for generating embeddings)
		// InputSchema is not used by the ingestion service
		var tools []mcp.Tool
		for _, tool := range capabilities.Tools {
			tools = append(tools, mcp.Tool{
				Name:        tool.Name,
				Description: tool.Description,
				// InputSchema not needed for embedding generation
			})
		}

		// Get description from metadata (may be empty)
		var description *string
		if backend.Metadata != nil {
			if desc := backend.Metadata["description"]; desc != "" {
				description = &desc
			}
		}

		// Ingest this backend's tools
		if err := o.ingestionService.IngestServer(
			ctx,
			backend.ID,
			backend.Name,
			description,
			tools,
		); err != nil {
			logger.Warnf("Failed to ingest backend %s: %v", backend.Name, err)
			continue // Log but don't fail startup
		}
		ingestedCount++
		totalToolsIngested += len(tools)
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

// Close cleans up optimizer resources.
func (o *OptimizerIntegration) Close() error {
	if o == nil || o.ingestionService == nil {
		return nil
	}
	return o.ingestionService.Close()
}

// IngestToolsForTesting manually ingests tools for testing purposes.
// This is a test helper that bypasses the normal ingestion flow.
func (o *OptimizerIntegration) IngestToolsForTesting(
	ctx context.Context,
	serverID string,
	serverName string,
	description *string,
	tools []mcp.Tool,
) error {
	if o == nil || o.ingestionService == nil {
		return fmt.Errorf("optimizer integration not initialized")
	}
	return o.ingestionService.IngestServer(ctx, serverID, serverName, description, tools)
}

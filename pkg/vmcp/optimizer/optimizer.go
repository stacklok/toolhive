// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package optimizer provides vMCP integration for semantic tool discovery.
//
// This package implements the RFC-0022 optimizer integration, exposing:
// - optim_find_tool: Semantic/keyword-based tool discovery
// - optim_call_tool: Dynamic tool invocation across backends
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
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/db"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/embeddings"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/ingestion"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/models"
	"github.com/stacklok/toolhive/pkg/logger"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter"
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

	// HybridSearchRatio controls semantic vs BM25 mix (0-100 percentage, default: 70)
	HybridSearchRatio int

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

// Ensure OptimizerIntegration implements Integration interface at compile time.
var _ Integration = (*OptimizerIntegration)(nil)

// HandleSessionRegistration handles session registration for optimizer mode.
// Returns true if optimizer mode is enabled and handled the registration,
// false if optimizer is disabled and normal registration should proceed.
//
// When optimizer is enabled:
//  1. Registers optimizer tools (find_tool, call_tool) for the session
//  2. Injects resources (but not backend tools or composite tools)
//  3. Backend tools are accessible via find_tool and call_tool
func (o *OptimizerIntegration) HandleSessionRegistration(
	_ context.Context,
	sessionID string,
	caps *aggregator.AggregatedCapabilities,
	mcpServer *server.MCPServer,
	resourceConverter func([]vmcp.Resource) []server.ServerResource,
) (bool, error) {
	if o == nil {
		return false, nil // Optimizer not enabled, use normal registration
	}

	logger.Debugw("HandleSessionRegistration called for optimizer mode", "session_id", sessionID)

	// Register optimizer tools for this session
	// Tools are already registered globally, but we need to add them to the session
	// when using WithToolCapabilities(false)
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
	// Backend tools will be accessible via find_tool and call_tool
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

// OnRegisterSession is a legacy method kept for test compatibility.
// It does nothing since ingestion is now handled by Initialize().
// This method is deprecated and will be removed in a future version.
// Tests should be updated to use HandleSessionRegistration instead.
func (o *OptimizerIntegration) OnRegisterSession(
	_ context.Context,
	session server.ClientSession,
	_ *aggregator.AggregatedCapabilities,
) error {
	if o == nil {
		return nil // Optimizer not enabled
	}

	sessionID := session.SessionID()

	logger.Debugw("OnRegisterSession called (legacy method, no-op)", "session_id", sessionID)

	// Check if this session has already been processed
	if _, alreadyProcessed := o.processedSessions.LoadOrStore(sessionID, true); alreadyProcessed {
		logger.Debugw("Session already processed, skipping duplicate ingestion",
			"session_id", sessionID)
		return nil
	}

	// Skip ingestion in OnRegisterSession - IngestInitialBackends already handles ingestion at startup
	// This prevents duplicate ingestion when sessions are registered
	// The optimizer database is populated once at startup, not per-session
	logger.Infow("Skipping ingestion in OnRegisterSession (handled by Initialize at startup)",
		"session_id", sessionID)

	return nil
}

// Initialize performs all optimizer initialization:
//   - Registers optimizer tools globally with the MCP server
//   - Ingests initial backends from the registry
//
// This should be called once during server startup, after the MCP server is created.
func (o *OptimizerIntegration) Initialize(
	ctx context.Context,
	mcpServer *server.MCPServer,
	backendRegistry vmcp.BackendRegistry,
) error {
	if o == nil {
		return nil // Optimizer not enabled
	}

	// Register optimizer tools globally (available to all sessions immediately)
	optimizerTools, err := adapter.CreateOptimizerTools(o)
	if err != nil {
		return fmt.Errorf("failed to create optimizer tools: %w", err)
	}
	for _, tool := range optimizerTools {
		mcpServer.AddTool(tool.Tool, tool.Handler)
	}
	logger.Info("Optimizer tools registered globally")

	// Ingest discovered backends into optimizer database
	initialBackends := backendRegistry.List(ctx)
	if err := o.IngestInitialBackends(ctx, initialBackends); err != nil {
		logger.Warnf("Failed to ingest initial backends into optimizer: %v", err)
		// Don't fail initialization - optimizer can still work with incremental ingestion
	}

	return nil
}

// RegisterTools adds optimizer tools to the session.
// Even though tools are registered globally via RegisterGlobalTools(),
// with WithToolCapabilities(false), we also need to register them per-session
// to ensure they appear in list_tools responses.
// This should be called after OnRegisterSession completes.
func (o *OptimizerIntegration) RegisterTools(_ context.Context, session server.ClientSession) error {
	if o == nil {
		return nil // Optimizer not enabled
	}

	sessionID := session.SessionID()

	// Define optimizer tools with handlers (same as global registration)
	optimizerTools := []server.ServerTool{
		{
			Tool: mcp.Tool{
				Name:        "optim_find_tool",
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
				Name:        "optim_call_tool",
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

	// Add tools to session (required when WithToolCapabilities(false))
	if err := o.mcpServer.AddSessionTools(sessionID, optimizerTools...); err != nil {
		return fmt.Errorf("failed to add optimizer tools to session: %w", err)
	}

	logger.Debugw("Optimizer tools registered for session", "session_id", sessionID)
	return nil
}

// GetOptimizerToolDefinitions returns the tool definitions for optimizer tools
// without handlers. This is useful for adding tools to capabilities before session registration.
func (o *OptimizerIntegration) GetOptimizerToolDefinitions() []mcp.Tool {
	if o == nil {
		return nil
	}
	return []mcp.Tool{
		{
			Name:        "optim_find_tool",
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
		{
			Name:        "optim_call_tool",
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
	}
}

// CreateFindToolHandler creates the handler for optim_find_tool
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

// createFindToolHandler creates the handler for optim_find_tool
func (o *OptimizerIntegration) createFindToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Debugw("optim_find_tool called", "request", request)

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

		logger.Infow("optim_find_tool completed",
			"query", toolDescription,
			"results_count", len(responseTools),
			"tokens_saved", tokensSaved,
			"savings_percentage", fmt.Sprintf("%.2f%%", savingsPercentage))

		return mcp.NewToolResultText(string(responseJSON)), nil
	}
}

// recordTokenMetrics records OpenTelemetry metrics for token savings
func (*OptimizerIntegration) recordTokenMetrics(
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

// CreateCallToolHandler creates the handler for optim_call_tool
// Exported for testing purposes
func (o *OptimizerIntegration) CreateCallToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return o.createCallToolHandler()
}

// createCallToolHandler creates the handler for optim_call_tool
func (o *OptimizerIntegration) createCallToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Debugw("optim_call_tool called", "request", request)

		// Parse and validate request arguments
		backendID, toolName, parameters, err := parseCallToolRequest(request)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Resolve target backend
		target, backendToolName, err := o.resolveToolTarget(ctx, backendID, toolName)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		logger.Infow("Calling tool via optimizer",
			"backend_id", backendID,
			"tool_name", toolName,
			"backend_tool_name", backendToolName,
			"workload_name", target.WorkloadName)

		// Call the tool on the backend
		result, err := o.backendClient.CallTool(ctx, target, backendToolName, parameters, nil)
		if err != nil {
			logger.Errorw("Tool call failed",
				"error", err,
				"backend_id", backendID,
				"tool_name", toolName,
				"backend_tool_name", backendToolName)
			return mcp.NewToolResultError(fmt.Sprintf("tool call failed: %v", err)), nil
		}

		// Convert result to MCP format
		mcpResult := convertToolResult(result)

		logger.Infow("optim_call_tool completed successfully",
			"backend_id", backendID,
			"tool_name", toolName)

		return mcpResult, nil
	}
}

// parseCallToolRequest extracts and validates parameters from the request.
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

// resolveToolTarget finds and validates the target backend for a tool.
func (*OptimizerIntegration) resolveToolTarget(
	ctx context.Context, backendID, toolName string,
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
		// Create a span for each backend ingestion
		backendCtx, backendSpan := o.tracer.Start(ctx, "optimizer.ingestion.ingest_backend",
			trace.WithAttributes(
				attribute.String("backend.id", backend.ID),
				attribute.String("backend.name", backend.Name),
			))
		defer backendSpan.End()

		// Convert Backend to BackendTarget for client API
		target := vmcp.BackendToTarget(&backend)
		if target == nil {
			logger.Warnf("Failed to convert backend %s to target", backend.Name)
			backendSpan.RecordError(fmt.Errorf("failed to convert backend to target"))
			backendSpan.SetStatus(codes.Error, "conversion failed")
			continue
		}

		// Query backend capabilities to get its tools
		capabilities, err := o.backendClient.ListCapabilities(backendCtx, target)
		if err != nil {
			logger.Warnf("Failed to query capabilities for backend %s: %v", backend.Name, err)
			backendSpan.RecordError(err)
			backendSpan.SetStatus(codes.Error, err.Error())
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

		backendSpan.SetAttributes(
			attribute.Int("tools.count", len(tools)),
		)

		// Ingest this backend's tools (IngestServer will create its own spans)
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
			continue // Log but don't fail startup
		}
		ingestedCount++
		totalToolsIngested += len(tools)
		backendSpan.SetAttributes(
			attribute.Int("tools.ingested", len(tools)),
		)
		backendSpan.SetStatus(codes.Ok, "backend ingested successfully")
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

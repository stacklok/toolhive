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

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/optimizer/db"
	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
	"github.com/stacklok/toolhive/pkg/optimizer/ingestion"
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
	config           *Config
	ingestionService *ingestion.Service
	mcpServer        *server.MCPServer  // For registering tools
	backendClient    vmcp.BackendClient // For querying backends at startup
	sessionManager   *transportsession.Manager
	processedSessions sync.Map // Track sessions that have already been processed
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
			Handler: o.createCallToolHandler(),
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

// createFindToolHandler creates the handler for optim.find_tool
func (o *OptimizerIntegration) createFindToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Debugw("optim.find_tool called", "request", request)

		// Extract parameters from request arguments
		args, ok := request.Params.Arguments.(map[string]any)
		if !ok {
			return mcp.NewToolResultError("invalid arguments: expected object"), nil
		}

		// Extract tool_description (required)
		toolDescription, ok := args["tool_description"].(string)
		if !ok || toolDescription == "" {
			return mcp.NewToolResultError("tool_description is required and must be a non-empty string"), nil
		}

		// Extract tool_keywords (optional)
		toolKeywords, _ := args["tool_keywords"].(string)

		// Extract limit (optional, default: 10)
		limit := 10
		if limitVal, ok := args["limit"]; ok {
			if limitFloat, ok := limitVal.(float64); ok {
				limit = int(limitFloat)
			}
		}

		// Perform hybrid search using database operations
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
		// Use toolDescription for semantic search (chromem-go will generate embedding internally)
		// If toolKeywords is provided, combine with toolDescription for better BM25 matching
		queryText := toolDescription
		if toolKeywords != "" {
			queryText = toolDescription + " " + toolKeywords
		}
		results, err := backendToolOps.SearchHybrid(ctx, queryText, hybridConfig)
		if err != nil {
			logger.Errorw("Hybrid search failed",
				"error", err,
				"tool_description", toolDescription,
				"tool_keywords", toolKeywords,
				"query_text", queryText)
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}

		// Convert results to response format
		responseTools := make([]map[string]any, 0, len(results))
		totalReturnedTokens := 0

		for _, result := range results {
			// Unmarshal InputSchema
			var inputSchema map[string]any
			if len(result.InputSchema) > 0 {
				if err2 := json.Unmarshal(result.InputSchema, &inputSchema); err2 != nil {
					logger.Warnw("Failed to unmarshal input schema",
						"tool_id", result.ID,
						"tool_name", result.ToolName,
						"error", err2)
					inputSchema = map[string]any{} // Use empty schema on error
				}
			}

			// Handle nil description
			description := ""
			if result.Description != nil {
				description = *result.Description
			}

			tool := map[string]any{
				"name":             result.ToolName,
				"description":      description,
				"input_schema":     inputSchema,
				"backend_id":       result.MCPServerID,
				"similarity_score": result.Similarity,
				"token_count":      result.TokenCount,
			}
			responseTools = append(responseTools, tool)
			totalReturnedTokens += result.TokenCount
		}

		// Calculate token metrics
		// Get baseline tokens from all tools in the database
		baselineTokens := o.ingestionService.GetTotalToolTokens(ctx)
		tokensSaved := baselineTokens - totalReturnedTokens
		savingsPercentage := 0.0
		if baselineTokens > 0 {
			savingsPercentage = (float64(tokensSaved) / float64(baselineTokens)) * 100.0
		}

		tokenMetrics := map[string]any{
			"baseline_tokens":     baselineTokens,
			"returned_tokens":     totalReturnedTokens,
			"tokens_saved":        tokensSaved,
			"savings_percentage":  savingsPercentage,
		}

		// Build response
		response := map[string]any{
			"tools":         responseTools,
			"token_metrics": tokenMetrics,
		}

		// Marshal to JSON for the result
		responseJSON, err := json.Marshal(response)
		if err != nil {
			logger.Errorw("Failed to marshal response", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to marshal response: %v", err)), nil
		}

		logger.Infow("optim.find_tool completed",
			"query", toolDescription,
			"results_count", len(responseTools),
			"tokens_saved", tokensSaved,
			"savings_percentage", fmt.Sprintf("%.2f%%", savingsPercentage))

		return mcp.NewToolResultText(string(responseJSON)), nil
	}
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
		return nil // Optimizer disabled
	}

	logger.Infof("Ingesting %d discovered backends into optimizer", len(backends))

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
	}

	logger.Info("Initial backend ingestion completed")
	return nil
}

// Close cleans up optimizer resources.
func (o *OptimizerIntegration) Close() error {
	if o == nil || o.ingestionService == nil {
		return nil
	}
	return o.ingestionService.Close()
}

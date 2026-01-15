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
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/optimizer/db"
	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
	"github.com/stacklok/toolhive/pkg/optimizer/ingestion"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
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
	mcpServer        *server.MCPServer // For registering tools
	backendClient    vmcp.BackendClient // For querying backends at startup
}

// NewIntegration creates a new optimizer integration.
func NewIntegration(
	_ context.Context,
	cfg *Config,
	mcpServer *server.MCPServer,
	backendClient vmcp.BackendClient,
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
	ctx context.Context,
	session server.ClientSession,
	capabilities *aggregator.AggregatedCapabilities,
) error {
	if o == nil {
		return nil // Optimizer not enabled
	}

	sessionID := session.SessionID()
	logger.Infow("Generating embeddings for session", "session_id", sessionID)

	// Group tools by backend for parallel processing
	type backendTools struct {
		backendID   string
		backendName string
		backendURL  string
		transport   string
		tools       []mcp.Tool
	}

	backendMap := make(map[string]*backendTools)

	// Extract tools from routing table
	if capabilities.RoutingTable != nil {
		for toolName, target := range capabilities.RoutingTable.Tools {
			// Find the tool definition from capabilities.Tools
			var toolDef mcp.Tool
			found := false
			for i := range capabilities.Tools {
				if capabilities.Tools[i].Name == toolName {
					// Convert vmcp.Tool to mcp.Tool
					// Note: vmcp.Tool.InputSchema is map[string]any, mcp.Tool.InputSchema is ToolInputSchema struct
					// For ingestion, we just need the tool name and description
					toolDef = mcp.Tool{
						Name:        capabilities.Tools[i].Name,
						Description: capabilities.Tools[i].Description,
						// InputSchema will be empty - we only need name/description for embedding generation
					}
					found = true
					break
				}
			}
			if !found {
				logger.Warnw("Tool in routing table but not in capabilities",
					"tool_name", toolName,
					"backend_id", target.WorkloadID)
				continue
			}

			// Group by backend
			if _, exists := backendMap[target.WorkloadID]; !exists {
				backendMap[target.WorkloadID] = &backendTools{
					backendID:   target.WorkloadID,
					backendName: target.WorkloadName,
					backendURL:  target.BaseURL,
					transport:   target.TransportType,
					tools:       []mcp.Tool{},
				}
			}
			backendMap[target.WorkloadID].tools = append(backendMap[target.WorkloadID].tools, toolDef)
		}
	}

	// Ingest each backend's tools (in parallel - TODO: add goroutines)
	for _, bt := range backendMap {
		logger.Debugw("Ingesting backend for session",
			"session_id", sessionID,
			"backend_id", bt.backendID,
			"backend_name", bt.backendName,
			"tool_count", len(bt.tools))

		// Ingest server with simplified metadata
		// Note: URL and transport are not stored - vMCP manages backend lifecycle
		err := o.ingestionService.IngestServer(
			ctx,
			bt.backendID,
			bt.backendName,
			nil, // description
			bt.tools,
		)
		if err != nil {
			logger.Errorw("Failed to ingest backend",
				"session_id", sessionID,
				"backend_id", bt.backendID,
				"error", err)
			// Continue with other backends
		}
	}

	logger.Infow("Embeddings generated for session",
		"session_id", sessionID,
		"backend_count", len(backendMap))

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

// createFindToolHandler creates the handler for optim.find_tool
func (*OptimizerIntegration) createFindToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// TODO: Implement semantic search
		// 1. Extract tool_description and tool_keywords from request.Params.Arguments
		// 2. Call optimizer search service (hybrid semantic + BM25)
		// 3. Return ranked list of tools with scores and token metrics

		logger.Debugw("optim.find_tool called", "request", request)

		return mcp.NewToolResultError("optim.find_tool not yet implemented"), nil
	}
}

// createCallToolHandler creates the handler for optim.call_tool
func (*OptimizerIntegration) createCallToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// TODO: Implement dynamic tool invocation
		// 1. Extract backend_id, tool_name, parameters from request.Params.Arguments
		// 2. Validate backend and tool exist
		// 3. Route to backend via existing router
		// 4. Return result

		logger.Debugw("optim.call_tool called", "request", request)

		return mcp.NewToolResultError("optim.call_tool not yet implemented"), nil
	}
}

// Close cleans up optimizer resources
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

func (o *OptimizerIntegration) Close() error {
	if o == nil || o.ingestionService == nil {
		return nil
	}
	return o.ingestionService.Close()
}

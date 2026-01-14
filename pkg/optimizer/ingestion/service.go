package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/optimizer/db"
	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
	"github.com/stacklok/toolhive/pkg/optimizer/models"
	"github.com/stacklok/toolhive/pkg/optimizer/tokens"
)

// Config holds configuration for the ingestion service
type Config struct {
	// Database configuration
	DBConfig *db.Config

	// Embedding configuration
	EmbeddingConfig *embeddings.Config

	// MCP timeout in seconds
	MCPTimeout int

	// Workloads to skip during ingestion
	SkippedWorkloads []string

	// Runtime mode: "docker" or "k8s"
	RuntimeMode string

	// Kubernetes configuration (used when RuntimeMode is "k8s")
	K8sAPIServerURL  string
	K8sNamespace     string
	K8sAllNamespaces bool
}

// Service handles ingestion of MCP backends and their tools
type Service struct {
	config           *Config
	database         *db.DB
	embeddingManager *embeddings.Manager
	tokenCounter     *tokens.Counter
	backendServerOps *db.BackendServerOps
	backendToolOps   *db.BackendToolOps
}

// NewService creates a new ingestion service
func NewService(config *Config) (*Service, error) {
	// Set defaults
	if config.MCPTimeout == 0 {
		config.MCPTimeout = 30
	}
	if len(config.SkippedWorkloads) == 0 {
		config.SkippedWorkloads = []string{"inspector", "mcp-optimizer"}
	}

	// Initialize database
	database, err := db.NewDB(config.DBConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	// Initialize embedding manager
	embeddingManager, err := embeddings.NewManager(config.EmbeddingConfig)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("failed to initialize embedding manager: %w", err)
	}

	// Initialize token counter
	tokenCounter := tokens.NewCounter()

	// Create chromem-go embeddingFunc from our embedding manager
	embeddingFunc := func(ctx context.Context, text string) ([]float32, error) {
		// Our manager takes a slice, so wrap the single text
		embeddings, err := embeddingManager.GenerateEmbedding([]string{text})
		if err != nil {
			return nil, err
		}
		if len(embeddings) == 0 {
			return nil, fmt.Errorf("no embeddings generated")
		}
		return embeddings[0], nil
	}

	svc := &Service{
		config:           config,
		database:         database,
		embeddingManager: embeddingManager,
		tokenCounter:     tokenCounter,
		backendServerOps: db.NewBackendServerOps(database, embeddingFunc),
		backendToolOps:   db.NewBackendToolOps(database, embeddingFunc),
	}

	logger.Info("Ingestion service initialized for event-driven ingestion (chromem-go)")
	return svc, nil
}

// IngestServer ingests a single MCP server and its tools into the optimizer database.
// This is called by vMCP during session registration for each backend server.
//
// Parameters:
//   - serverID: Unique identifier for the backend server
//   - serverName: Human-readable server name
//   - description: Optional server description
//   - tools: List of tools available from this server
//
// This method will:
//  1. Create or update the backend server record (simplified metadata only)
//  2. Generate embeddings for server and tools
//  3. Count tokens for each tool
//  4. Store everything in the database for semantic search
//
// Note: URL, transport, status are NOT stored - vMCP manages backend lifecycle
func (s *Service) IngestServer(
	ctx context.Context,
	serverID string,
	serverName string,
	description *string,
	tools []mcp.Tool,
) error {
	logger.Infof("Ingesting server: %s (%d tools)", serverName, len(tools))

	// Create backend server record (simplified - vMCP manages lifecycle)
	// chromem-go will generate embeddings automatically from the content
	backendServer := &models.BackendServer{
		ID:          serverID,
		Name:        serverName,
		Description: description,
		Group:       "default", // TODO: Pass group from vMCP if needed
		CreatedAt:   time.Now(),
		LastUpdated: time.Now(),
	}

	// Create or update server (chromem-go handles embeddings)
	if err := s.backendServerOps.Update(ctx, backendServer); err != nil {
		return fmt.Errorf("failed to create/update server %s: %w", serverName, err)
	}
	logger.Debugf("Created/updated server: %s", serverName)

	// Sync tools for this server
	toolCount, err := s.syncBackendTools(ctx, serverID, serverName, tools)
	if err != nil {
		return fmt.Errorf("failed to sync tools for %s: %w", serverName, err)
	}

	logger.Infof("Successfully ingested server %s with %d tools", serverName, toolCount)
	return nil
}

// shouldSkipWorkload checks if a backend should be skipped
func (s *Service) shouldSkipWorkload(backendName string) bool {
	if backendName == "" {
		return true
	}

	for _, skipped := range s.config.SkippedWorkloads {
		if strings.Contains(backendName, skipped) {
			return true
		}
	}

	return false
}

// createToolTextToEmbed creates the text representation for a tool embedding
func (*Service) createToolTextToEmbed(tool mcp.Tool, serverName string) string {
	parts := []string{fmt.Sprintf("Server: %s", serverName)}

	if tool.Name != "" {
		parts = append(parts, fmt.Sprintf("Tool: %s", tool.Name))
	}

	if tool.Description != "" {
		parts = append(parts, fmt.Sprintf("Description: %s", tool.Description))
	}

	// If we only have server name, add the full tool JSON
	if len(parts) == 1 {
		toolJSON, _ := tool.MarshalJSON()
		parts = append(parts, string(toolJSON))
	}

	return strings.Join(parts, " | ")
}

// syncBackendTools synchronizes tools for a backend server
func (s *Service) syncBackendTools(ctx context.Context, serverID string, serverName string, tools []mcp.Tool) (int, error) {
	// Delete existing tools
	if err := s.backendToolOps.DeleteByServer(ctx, serverID); err != nil {
		return 0, fmt.Errorf("failed to delete existing tools: %w", err)
	}

	if len(tools) == 0 {
		return 0, nil
	}

	// Create tool records (chromem-go will generate embeddings automatically)
	for _, tool := range tools {
		// Extract description for embedding
		description := tool.Description
		
		// Convert InputSchema to JSON
		schemaJSON, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal input schema for tool %s: %w", tool.Name, err)
		}
		
		backendTool := &models.BackendTool{
			ID:           uuid.New().String(),
			MCPServerID:  serverID,
			ToolName:     tool.Name,
			Description:  &description,
			InputSchema:  schemaJSON,
			TokenCount:   s.tokenCounter.CountToolTokens(tool),
			CreatedAt:    time.Now(),
			LastUpdated:  time.Now(),
		}

		if err := s.backendToolOps.Create(ctx, backendTool); err != nil {
			return 0, fmt.Errorf("failed to create tool %s: %w", tool.Name, err)
		}
	}

	logger.Infof("Synced %d tools for server %s", len(tools), serverName)
	return len(tools), nil
}

// Close releases resources
func (s *Service) Close() error {
	var errs []error

	if err := s.embeddingManager.Close(); err != nil {
		errs = append(errs, fmt.Errorf("failed to close embedding manager: %w", err))
	}

	if err := s.database.Close(); err != nil {
		errs = append(errs, fmt.Errorf("failed to close database: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing service: %v", errs)
	}

	return nil
}

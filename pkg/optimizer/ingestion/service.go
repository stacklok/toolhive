package ingestion

import (
	"context"
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

	// Batch sizes for parallel ingestion
	RegistryBatchSize int
	WorkloadBatchSize int

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
	config            *Config
	database          *db.DB
	embeddingManager  *embeddings.Manager
	tokenCounter      *tokens.Counter
	backendServerOps *db.BackendServerOps
	backendToolOps   *db.BackendToolOps
}

// NewService creates a new ingestion service
func NewService(config *Config) (*Service, error) {
	// Validate runtime mode
	runtimeMode := strings.ToLower(config.RuntimeMode)
	if runtimeMode != "docker" && runtimeMode != "k8s" {
		return nil, ErrInvalidRuntimeMode
	}
	config.RuntimeMode = runtimeMode

	// Set defaults
	if config.MCPTimeout == 0 {
		config.MCPTimeout = 30
	}
	if config.RegistryBatchSize == 0 {
		config.RegistryBatchSize = 5
	}
	if config.WorkloadBatchSize == 0 {
		config.WorkloadBatchSize = 5
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

	svc := &Service{
		config:            config,
		database:          database,
		embeddingManager:  embeddingManager,
		tokenCounter:      tokenCounter,
		backendServerOps: db.NewBackendServerOps(database),
		backendToolOps:   db.NewBackendToolOps(database),
	}

	logger.Infof("Ingestion service initialized (runtime: %s)", config.RuntimeMode)
	return svc, nil
}

// IngestBackends discovers and ingests backends from ToolHive
func (*Service) IngestBackends(_ context.Context) error {
	logger.Info("Starting backend ingestion")

	// TODO: Implement actual backend discovery from Docker or Kubernetes
	// For now, this is a placeholder implementation

	// Example workflow:
	// 1. Discover backends from Docker/K8s
	// 2. For each backend:
	//    a. Check if it should be skipped
	//    b. Connect to the MCP server
	//    c. List tools
	//    d. Generate embeddings for tools
	//    e. Count tokens
	//    f. Store in database
	// 3. Clean up backends that no longer exist

	logger.Info("Backend ingestion completed (placeholder)")
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
	deleted, err := s.backendToolOps.DeleteByServerID(ctx, serverID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete existing tools: %w", err)
	}

	if deleted > 0 {
		logger.Debugf("Deleted %d existing tools for server %s", deleted, serverName)
	}

	if len(tools) == 0 {
		return 0, nil
	}

	// Generate embeddings for all tools
	texts := make([]string, len(tools))
	for i, tool := range tools {
		texts[i] = s.createToolTextToEmbed(tool, serverName)
	}

	toolEmbeddings, err := s.embeddingManager.GenerateEmbedding(texts)
	if err != nil {
		return 0, fmt.Errorf("failed to generate embeddings: %w", err)
	}

	// Create tool records
	for i, tool := range tools {
		backendTool := &models.BackendTool{
			BaseTool: models.BaseTool{
				ID:               uuid.New().String(),
				MCPServerID:      serverID,
				Details:          tool,
				DetailsEmbedding: toolEmbeddings[i],
				CreatedAt:        time.Now(),
				LastUpdated:      time.Now(),
			},
			TokenCount: s.tokenCounter.CountToolTokens(tool),
		}

		if err := s.backendToolOps.Create(ctx, backendTool); err != nil {
			return 0, fmt.Errorf("failed to create tool: %w", err)
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

// StartPolling starts periodic polling for backend changes
func (s *Service) StartPolling(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Infof("Starting backend polling (interval: %s)", interval)

	// Initial ingestion
	if err := s.IngestBackends(ctx); err != nil {
		logger.Errorf("Initial backend ingestion failed: %v", err)
	}

	// Periodic polling
	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping backend polling")
			return
		case <-ticker.C:
		if err := s.IngestBackends(ctx); err != nil {
			logger.Errorf("Backend ingestion failed: %v", err)
			}
		}
	}
}

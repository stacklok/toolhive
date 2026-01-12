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

// Service handles ingestion of MCP workloads and their tools
type Service struct {
	config           *Config
	database         *db.DB
	embeddingManager *embeddings.Manager
	tokenCounter     *tokens.Counter
	workloadServerOps *db.WorkloadServerOps
	workloadToolOps   *db.WorkloadToolOps
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
		database.Close()
		return nil, fmt.Errorf("failed to initialize embedding manager: %w", err)
	}

	// Initialize token counter
	tokenCounter := tokens.NewCounter()

	svc := &Service{
		config:           config,
		database:         database,
		embeddingManager: embeddingManager,
		tokenCounter:     tokenCounter,
		workloadServerOps: db.NewWorkloadServerOps(database),
		workloadToolOps:   db.NewWorkloadToolOps(database),
	}

	logger.Infof("Ingestion service initialized (runtime: %s)", config.RuntimeMode)
	return svc, nil
}

// IngestWorkloads discovers and ingests workloads from ToolHive
func (s *Service) IngestWorkloads(ctx context.Context) error {
	logger.Info("Starting workload ingestion")

	// TODO: Implement actual workload discovery from Docker or Kubernetes
	// For now, this is a placeholder implementation
	
	// Example workflow:
	// 1. Discover workloads from Docker/K8s
	// 2. For each workload:
	//    a. Check if it should be skipped
	//    b. Connect to the MCP server
	//    c. List tools
	//    d. Generate embeddings for tools
	//    e. Count tokens
	//    f. Store in database
	// 3. Clean up workloads that no longer exist

	logger.Info("Workload ingestion completed (placeholder)")
	return nil
}

// mapWorkloadStatus maps a workload status string to MCPStatus
func (s *Service) mapWorkloadStatus(status string) (models.MCPStatus, error) {
	if status == "" {
		return "", ErrWorkloadStatusNil
	}

	switch strings.ToLower(status) {
	case "running":
		return models.StatusRunning, nil
	case "stopped", "pending", "failed", "unknown":
		return models.StatusStopped, nil
	default:
		return models.StatusStopped, nil
	}
}

// shouldSkipWorkload checks if a workload should be skipped
func (s *Service) shouldSkipWorkload(workloadName string) bool {
	if workloadName == "" {
		return true
	}

	for _, skipped := range s.config.SkippedWorkloads {
		if strings.Contains(workloadName, skipped) {
			return true
		}
	}

	return false
}

// createToolTextToEmbed creates the text representation for a tool embedding
func (s *Service) createToolTextToEmbed(tool mcp.Tool, serverName string) string {
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

// syncWorkloadTools synchronizes tools for a workload server
func (s *Service) syncWorkloadTools(ctx context.Context, serverID string, serverName string, tools []mcp.Tool) (int, error) {
	// Delete existing tools
	deleted, err := s.workloadToolOps.DeleteByServerID(ctx, serverID)
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

	embeddings, err := s.embeddingManager.GenerateEmbedding(texts)
	if err != nil {
		return 0, fmt.Errorf("failed to generate embeddings: %w", err)
	}

	// Create tool records
	for i, tool := range tools {
		workloadTool := &models.WorkloadTool{
			BaseTool: models.BaseTool{
				ID:               uuid.New().String(),
				MCPServerID:      serverID,
				Details:          tool,
				DetailsEmbedding: embeddings[i],
				CreatedAt:        time.Now(),
				LastUpdated:      time.Now(),
			},
			TokenCount: s.tokenCounter.CountToolTokens(tool),
		}

		if err := s.workloadToolOps.Create(ctx, workloadTool); err != nil {
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

// StartPolling starts periodic polling for workload changes
func (s *Service) StartPolling(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Infof("Starting workload polling (interval: %s)", interval)

	// Initial ingestion
	if err := s.IngestWorkloads(ctx); err != nil {
		logger.Errorf("Initial workload ingestion failed: %v", err)
	}

	// Periodic polling
	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping workload polling")
			return
		case <-ticker.C:
			if err := s.IngestWorkloads(ctx); err != nil {
				logger.Errorf("Workload ingestion failed: %v", err)
			}
		}
	}
}


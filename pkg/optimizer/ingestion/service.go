package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

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
	tracer           trace.Tracer

	// Embedding time tracking
	embeddingTimeMu    sync.Mutex
	totalEmbeddingTime time.Duration
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

	// Clear database on startup to ensure fresh embeddings
	// This is important when the embedding model changes or for consistency
	database.Reset()
	logger.Info("Cleared optimizer database on startup")

	// Initialize embedding manager
	embeddingManager, err := embeddings.NewManager(config.EmbeddingConfig)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("failed to initialize embedding manager: %w", err)
	}

	// Initialize token counter
	tokenCounter := tokens.NewCounter()

	// Initialize tracer
	tracer := otel.Tracer("github.com/stacklok/toolhive/pkg/optimizer/ingestion")

	svc := &Service{
		config:             config,
		database:           database,
		embeddingManager:   embeddingManager,
		tokenCounter:       tokenCounter,
		tracer:             tracer,
		totalEmbeddingTime: 0,
	}

	// Create chromem-go embeddingFunc from our embedding manager with tracing
	embeddingFunc := func(ctx context.Context, text string) ([]float32, error) {
		// Create a span for embedding calculation
		ctx, span := svc.tracer.Start(ctx, "optimizer.ingestion.calculate_embedding",
			trace.WithAttributes(
				attribute.String("operation", "embedding_calculation"),
			))
		defer span.End()

		start := time.Now()

		// Our manager takes a slice, so wrap the single text
		embeddingsResult, err := embeddingManager.GenerateEmbedding([]string{text})
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, err
		}
		if len(embeddingsResult) == 0 {
			err := fmt.Errorf("no embeddings generated")
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, err
		}

		// Track embedding time
		duration := time.Since(start)
		svc.embeddingTimeMu.Lock()
		svc.totalEmbeddingTime += duration
		svc.embeddingTimeMu.Unlock()

		span.SetAttributes(
			attribute.Int64("embedding.duration_ms", duration.Milliseconds()),
		)

		return embeddingsResult[0], nil
	}

	svc.backendServerOps = db.NewBackendServerOps(database, embeddingFunc)
	svc.backendToolOps = db.NewBackendToolOps(database, embeddingFunc)

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
	// Create a span for the entire ingestion operation
	ctx, span := s.tracer.Start(ctx, "optimizer.ingestion.ingest_server",
		trace.WithAttributes(
			attribute.String("server.id", serverID),
			attribute.String("server.name", serverName),
			attribute.Int("tools.count", len(tools)),
		))
	defer span.End()

	start := time.Now()
	logger.Infof("Ingesting server: %s (%d tools) [serverID=%s]", serverName, len(tools), serverID)

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
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("failed to create/update server %s: %w", serverName, err)
	}
	logger.Debugf("Created/updated server: %s", serverName)

	// Sync tools for this server
	toolCount, err := s.syncBackendTools(ctx, serverID, serverName, tools)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("failed to sync tools for %s: %w", serverName, err)
	}

	duration := time.Since(start)
	span.SetAttributes(
		attribute.Int64("ingestion.duration_ms", duration.Milliseconds()),
		attribute.Int("tools.ingested", toolCount),
	)

	logger.Infow("Successfully ingested server",
		"server_name", serverName,
		"server_id", serverID,
		"tools_count", toolCount,
		"duration_ms", duration.Milliseconds())
	return nil
}

// syncBackendTools synchronizes tools for a backend server
func (s *Service) syncBackendTools(ctx context.Context, serverID string, serverName string, tools []mcp.Tool) (int, error) {
	// Create a span for tool synchronization
	ctx, span := s.tracer.Start(ctx, "optimizer.ingestion.sync_backend_tools",
		trace.WithAttributes(
			attribute.String("server.id", serverID),
			attribute.String("server.name", serverName),
			attribute.Int("tools.count", len(tools)),
		))
	defer span.End()

	logger.Debugf("syncBackendTools: server=%s, serverID=%s, tool_count=%d", serverName, serverID, len(tools))

	// Delete existing tools
	if err := s.backendToolOps.DeleteByServer(ctx, serverID); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
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
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return 0, fmt.Errorf("failed to marshal input schema for tool %s: %w", tool.Name, err)
		}

		backendTool := &models.BackendTool{
			ID:          uuid.New().String(),
			MCPServerID: serverID,
			ToolName:    tool.Name,
			Description: &description,
			InputSchema: schemaJSON,
			TokenCount:  s.tokenCounter.CountToolTokens(tool),
			CreatedAt:   time.Now(),
			LastUpdated: time.Now(),
		}

		if err := s.backendToolOps.Create(ctx, backendTool, serverName); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return 0, fmt.Errorf("failed to create tool %s: %w", tool.Name, err)
		}
	}

	logger.Infof("Synced %d tools for server %s", len(tools), serverName)
	return len(tools), nil
}

// GetEmbeddingManager returns the embedding manager for this service
func (s *Service) GetEmbeddingManager() *embeddings.Manager {
	return s.embeddingManager
}

// GetBackendToolOps returns the backend tool operations for search and retrieval
func (s *Service) GetBackendToolOps() *db.BackendToolOps {
	return s.backendToolOps
}

// GetTotalToolTokens returns the total token count across all tools in the database
func (s *Service) GetTotalToolTokens(ctx context.Context) int {
	// Use FTS database to efficiently count all tool tokens
	if s.database.GetFTSDB() != nil {
		totalTokens, err := s.database.GetFTSDB().GetTotalToolTokens(ctx)
		if err != nil {
			logger.Warnw("Failed to get total tool tokens from FTS", "error", err)
			return 0
		}
		return totalTokens
	}

	// Fallback: query all tools (less efficient but works)
	logger.Warn("FTS database not available, using fallback for token counting")
	return 0
}

// GetTotalEmbeddingTime returns the total time spent calculating embeddings
func (s *Service) GetTotalEmbeddingTime() time.Duration {
	s.embeddingTimeMu.Lock()
	defer s.embeddingTimeMu.Unlock()
	return s.totalEmbeddingTime
}

// ResetEmbeddingTime resets the total embedding time counter
func (s *Service) ResetEmbeddingTime() {
	s.embeddingTimeMu.Lock()
	defer s.embeddingTimeMu.Unlock()
	s.totalEmbeddingTime = 0
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

package ingestion

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/optimizer/db"
	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
	"github.com/stacklok/toolhive/pkg/optimizer/models"
)

// TestServiceCreationAndIngestion demonstrates the complete workflow:
// 1. Create database
// 2. Initialize ingestion service
// 3. Ingest tools with embeddings
// 4. Query the database
func TestServiceCreationAndIngestion(t *testing.T) {
	t.Parallel()

	// Skip if sqlite-vec is not available
	if os.Getenv("SQLITE_VEC_PATH") == "" {
		t.Skip("Skipping test: SQLITE_VEC_PATH not set. Run 'task test-optimizer' to set up sqlite-vec.")
	}

	// Create database in persistent location for inspection
	dbPath := "/tmp/optimizer-test.db"

	// Clean up old database if it exists
	os.Remove(dbPath)
	os.Remove(dbPath + "-shm")
	os.Remove(dbPath + "-wal")

	// Initialize service with placeholder embeddings (no dependencies)
	config := &Config{
		DBConfig: &db.Config{
			DBPath: dbPath,
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "placeholder", // Use placeholder for testing
			Dimension:   384,
			EnableCache: true,
		},
		RuntimeMode: "docker",
	}

	svc, err := NewService(config)
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}
	defer svc.Close()

	// Verify database was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("Database file was not created")
	}

	ctx := context.Background()

	// Create a mock backend server
	serverID := uuid.New().String()
	serverName := "test-server"
	description := "Test MCP server"

	server := &models.BackendServer{
		BaseMCPServer: models.BaseMCPServer{
			ID:          serverID,
			Name:        serverName,
			Description: &description,
			CreatedAt:   time.Now(),
			LastUpdated: time.Now(),
			Transport:   models.TransportStreamable,
		},
		URL:                "http://localhost:8080",
		BackendIdentifier: "test-backend",
		Status:             models.StatusRunning,
	}

	// Create the server in database
	if err := svc.backendServerOps.Create(ctx, server); err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Create mock tools
	tools := []mcp.Tool{
		{
			Name:        "get_weather",
			Description: "Get current weather for a location",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"location": map[string]interface{}{
						"type":        "string",
						"description": "City name",
					},
				},
				Required: []string{"location"},
			},
		},
		{
			Name:        "get_time",
			Description: "Get current time in a timezone",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"timezone": map[string]interface{}{
						"type":        "string",
						"description": "Timezone name",
					},
				},
			},
		},
	}

	// Ingest tools (with embeddings and token counting)
	toolCount, err := svc.syncBackendTools(ctx, serverID, serverName, tools)
	if err != nil {
		t.Fatalf("Failed to sync tools: %v", err)
	}

	if toolCount != len(tools) {
		t.Errorf("Expected %d tools synced, got %d", len(tools), toolCount)
	}

	// Verify tools were created
	createdTools, err := svc.backendToolOps.GetByServerID(ctx, serverID)
	if err != nil {
		t.Fatalf("Failed to retrieve tools: %v", err)
	}

	if len(createdTools) != len(tools) {
		t.Errorf("Expected %d tools in database, got %d", len(tools), len(createdTools))
	}

	// Verify each tool has embeddings and token counts
	for i, tool := range createdTools {
		if tool.ID == "" {
			t.Errorf("Tool %d missing ID", i)
		}
		if tool.MCPServerID != serverID {
			t.Errorf("Tool %d has wrong server ID: %s", i, tool.MCPServerID)
		}
		if len(tool.DetailsEmbedding) == 0 {
			t.Errorf("Tool %d missing embedding", i)
		}
		if len(tool.DetailsEmbedding) != 384 {
			t.Errorf("Tool %d has wrong embedding dimension: %d", i, len(tool.DetailsEmbedding))
		}
		if tool.TokenCount == 0 {
			t.Errorf("Tool %d has zero token count", i)
		}
		t.Logf("Tool %d: name=%s, tokens=%d, embedding_dim=%d",
			i, tool.Details.Name, tool.TokenCount, len(tool.DetailsEmbedding))
	}

	// Test semantic search by embedding
	queryText := "what's the weather"
	queryEmbeddings, err := svc.embeddingManager.GenerateEmbedding([]string{queryText})
	if err != nil {
		t.Fatalf("Failed to generate query embedding: %v", err)
	}

	results, err := svc.backendToolOps.SearchByEmbedding(ctx, queryEmbeddings[0], 10)
	if err != nil {
		t.Fatalf("Failed to search by embedding: %v", err)
	}

	if len(results) == 0 {
		t.Error("Search returned no results")
	}

	t.Logf("Search results for '%s':", queryText)
	for i, result := range results {
		t.Logf("  %d. %s - %s (distance: %.4f, tokens: %d)",
			i+1,
			result.ServerName,
			result.Tool.Details.Name,
			result.Distance,
			result.Tool.TokenCount,
		)
	}

	// Test updating tools (delete and re-sync)
	updatedTools := []mcp.Tool{
		{
			Name:        "get_weather",
			Description: "Get current weather for a location (updated)",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"location": map[string]interface{}{
						"type":        "string",
						"description": "City name",
					},
					"units": map[string]interface{}{
						"type":        "string",
						"description": "Temperature units",
					},
				},
			},
		},
	}

	toolCount, err = svc.syncBackendTools(ctx, serverID, serverName, updatedTools)
	if err != nil {
		t.Fatalf("Failed to sync updated tools: %v", err)
	}

	if toolCount != len(updatedTools) {
		t.Errorf("Expected %d tools after update, got %d", len(updatedTools), toolCount)
	}

	// Verify old tools were deleted
	finalTools, err := svc.backendToolOps.GetByServerID(ctx, serverID)
	if err != nil {
		t.Fatalf("Failed to retrieve final tools: %v", err)
	}

	if len(finalTools) != len(updatedTools) {
		t.Errorf("Expected %d tools after update, got %d", len(updatedTools), len(finalTools))
	}

	t.Log("✓ Complete workflow tested:")
	t.Log("  - Database creation")
	t.Log("  - Service initialization")
	t.Log("  - Tool ingestion with embeddings")
	t.Log("  - Token counting")
	t.Log("  - Semantic search")
	t.Log("  - Tool updates")
	t.Logf("")
	t.Logf("📁 Database saved at: %s", dbPath)
	t.Logf("   Inspect with: sqlite3 %s", dbPath)
	t.Logf("   Vector tables: workload_tool_vectors, workload_server_vector")
	t.Logf("   FTS tables: workload_tool_fts")
}

// TestServiceWithOllama demonstrates using real embeddings (requires Ollama running)
// This test is skipped by default and can be enabled with -tags=integration
func TestServiceWithOllama(t *testing.T) {
	t.Parallel()
	// Skip if not in integration mode
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_ollama.db")

	config := &Config{
		DBConfig: &db.Config{
			DBPath: dbPath,
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm", // Use 384-dim model to match database schema
			Dimension:   384,
			EnableCache: true,
		},
		RuntimeMode: "docker",
	}

	svc, err := NewService(config)
	if err != nil {
		t.Skipf("Failed to create service (Ollama may not be running): %v", err)
	}
	defer svc.Close()

	ctx := context.Background()

	// Create server
	serverID := uuid.New().String()
	description := "Test with real embeddings"
	server := &models.BackendServer{
		BaseMCPServer: models.BaseMCPServer{
			ID:          serverID,
			Name:        "ollama-test-server",
			Description: &description,
			CreatedAt:   time.Now(),
			LastUpdated: time.Now(),
			Transport:   models.TransportStreamable,
		},
		URL:                "http://localhost:8080",
		BackendIdentifier: "ollama-test-backend",
		Status:             models.StatusRunning,
	}

	if err := svc.backendServerOps.Create(ctx, server); err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Create tool
	tools := []mcp.Tool{
		{
			Name:        "calculate_sum",
			Description: "Calculate the sum of two numbers",
		},
	}

	_, err = svc.syncBackendTools(ctx, serverID, server.Name, tools)
	if err != nil {
		t.Fatalf("Failed to sync tools: %v", err)
	}

	t.Log("✓ Successfully tested with Ollama embeddings")
}

// TestCreateToolTextToEmbed tests the text generation for embeddings
func TestCreateToolTextToEmbed(t *testing.T) {
	t.Parallel()
	config := &Config{
		RuntimeMode: "docker",
	}

	svc := &Service{
		config: config,
	}

	tests := []struct {
		name       string
		tool       mcp.Tool
		serverName string
		want       string
	}{
		{
			name: "full tool info",
			tool: mcp.Tool{
				Name:        "get_weather",
				Description: "Get current weather",
			},
			serverName: "weather-server",
			want:       "Server: weather-server | Tool: get_weather | Description: Get current weather",
		},
		{
			name: "tool without description",
			tool: mcp.Tool{
				Name: "test_tool",
			},
			serverName: "test-server",
			want:       "Server: test-server | Tool: test_tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := svc.createToolTextToEmbed(tt.tool, tt.serverName)
			if got != tt.want {
				t.Errorf("createToolTextToEmbed() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestShouldSkipBackend tests backend filtering
func TestShouldSkipBackend(t *testing.T) {
	t.Parallel()
	config := &Config{
		RuntimeMode:      "docker",
		SkippedWorkloads: []string{"inspector", "mcp-optimizer"},
	}

	svc := &Service{
		config: config,
	}

	tests := []struct {
		name         string
		workloadName string
		wantSkip     bool
	}{
		{"skip inspector", "inspector", true},
		{"skip optimizer", "mcp-optimizer-service", true},
		{"keep normal", "weather-server", false},
		{"keep empty", "", true}, // Empty names are skipped
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := svc.shouldSkipWorkload(tt.workloadName)
			if got != tt.wantSkip {
				t.Errorf("shouldSkipWorkload(%q) = %v, want %v", tt.workloadName, got, tt.wantSkip)
			}
		})
	}
}

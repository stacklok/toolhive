package ingestion_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/optimizer/db"
	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
	"github.com/stacklok/toolhive/pkg/optimizer/ingestion"
	_ "github.com/stacklok/toolhive/pkg/optimizer/db" // Registers mattn/go-sqlite3 driver with FTS5 support
)

// TestVMCPIntegrationExample demonstrates how vMCP would use the optimizer
func TestVMCPIntegrationExample(t *testing.T) {
	t.Parallel()

	// This is how vMCP would initialize the optimizer service at startup
	fmt.Println("\n🚀 vMCP Starting Up...")
	fmt.Println(strings.Repeat("=", 60))

	// Step 1: Initialize the optimizer service (once at vMCP startup)
	dbPath := "/tmp/vmcp-optimizer-demo.db"
	_ = os.Remove(dbPath) // Clean slate for demo

	optimizerSvc, err := ingestion.NewService(&ingestion.Config{
		DBConfig: &db.Config{
			DBPath: dbPath,
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "placeholder", // Using placeholder for demo
			Dimension:   384,
		},
		RuntimeMode: "docker", // Required field
	})
	require.NoError(t, err)
	defer optimizerSvc.Close()
	defer os.Remove(dbPath)

	fmt.Println("✅ Optimizer service initialized")
	fmt.Println()

	// Step 2: Simulate vMCP loading its startup groups
	fmt.Println("📦 Loading startup groups...")
	fmt.Println()

	// Group 1: Weather services
	weatherGroup := []struct {
		id          string
		name        string
		url         string
		description string
		tools       []mcp.Tool
	}{
		{
			id:          "weather-001",
			name:        "weather-service",
			url:         "http://weather.local:8080",
			description: "Provides current weather and forecast information",
			tools: []mcp.Tool{
				{
					Name:        "get_current_weather",
					Description: "Get the current weather conditions for a location",
					InputSchema: mcp.ToolInputSchema{
						Type: "object",
						Properties: map[string]interface{}{
							"location": map[string]interface{}{
								"type":        "string",
								"description": "City name or coordinates",
							},
							"units": map[string]interface{}{
								"type": "string",
								"enum": []string{"celsius", "fahrenheit"},
							},
						},
						Required: []string{"location"},
					},
				},
				{
					Name:        "get_weather_forecast",
					Description: "Get weather forecast for the next 7 days",
					InputSchema: mcp.ToolInputSchema{
						Type: "object",
						Properties: map[string]interface{}{
							"location": map[string]interface{}{
								"type": "string",
							},
							"days": map[string]interface{}{
								"type":    "integer",
								"minimum": 1,
								"maximum": 7,
							},
						},
						Required: []string{"location"},
					},
				},
			},
		},
	}

	// Group 2: Database tools
	databaseGroup := []struct {
		id          string
		name        string
		url         string
		description string
		tools       []mcp.Tool
	}{
		{
			id:          "postgres-001",
			name:        "postgres-service",
			url:         "http://postgres.local:5432",
			description: "PostgreSQL database query and management tools",
			tools: []mcp.Tool{
				{
					Name:        "execute_query",
					Description: "Execute a SQL query on the database",
					InputSchema: mcp.ToolInputSchema{
						Type: "object",
						Properties: map[string]interface{}{
							"query": map[string]interface{}{
								"type":        "string",
								"description": "SQL query to execute",
							},
							"database": map[string]interface{}{
								"type": "string",
							},
						},
						Required: []string{"query"},
					},
				},
				{
					Name:        "list_tables",
					Description: "List all tables in the database",
					InputSchema: mcp.ToolInputSchema{
						Type: "object",
						Properties: map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "string",
							},
						},
					},
				},
				{
					Name:        "describe_table",
					Description: "Get the schema and structure of a table",
					InputSchema: mcp.ToolInputSchema{
						Type: "object",
						Properties: map[string]interface{}{
							"table_name": map[string]interface{}{
								"type": "string",
							},
						},
						Required: []string{"table_name"},
					},
				},
			},
		},
	}

	// Group 3: GitHub integration
	githubGroup := []struct {
		id          string
		name        string
		url         string
		description string
		tools       []mcp.Tool
	}{
		{
			id:          "github-001",
			name:        "github-service",
			url:         "http://github-mcp.local:8080",
			description: "GitHub repository and issue management",
			tools: []mcp.Tool{
				{
					Name:        "create_issue",
					Description: "Create a new GitHub issue",
					InputSchema: mcp.ToolInputSchema{
						Type: "object",
						Properties: map[string]interface{}{
							"repo": map[string]interface{}{
								"type":        "string",
								"description": "Repository in owner/repo format",
							},
							"title": map[string]interface{}{
								"type": "string",
							},
							"body": map[string]interface{}{
								"type": "string",
							},
							"labels": map[string]interface{}{
								"type": "array",
								"items": map[string]interface{}{
									"type": "string",
								},
							},
						},
						Required: []string{"repo", "title"},
					},
				},
				{
					Name:        "list_pull_requests",
					Description: "List pull requests in a repository",
					InputSchema: mcp.ToolInputSchema{
						Type: "object",
						Properties: map[string]interface{}{
							"repo": map[string]interface{}{
								"type": "string",
							},
							"state": map[string]interface{}{
								"type": "string",
								"enum": []string{"open", "closed", "all"},
							},
						},
						Required: []string{"repo"},
					},
				},
			},
		},
	}

	ctx := context.Background()

	// Step 3: Ingest each server as vMCP starts it
	fmt.Println("🔄 Starting servers and ingesting into optimizer...")
	fmt.Println()

	totalServers := 0
	totalTools := 0

	// Ingest weather services
	for _, server := range weatherGroup {
		fmt.Printf("  📍 Starting: %s (%s)\n", server.name, server.url)
		fmt.Printf("     Tools: %d\n", len(server.tools))

		desc := server.description
		err := optimizerSvc.IngestServer(
			ctx,
			server.id,
			server.name,
			&desc,
			server.tools,
		)
		require.NoError(t, err)

		totalServers++
		totalTools += len(server.tools)
		fmt.Printf("     ✅ Ingested into optimizer\n\n")
	}

	// Ingest database services
	for _, server := range databaseGroup {
		fmt.Printf("  📍 Starting: %s (%s)\n", server.name, server.url)
		fmt.Printf("     Tools: %d\n", len(server.tools))

		desc := server.description
		err := optimizerSvc.IngestServer(
			ctx,
			server.id,
			server.name,
			&desc,
			server.tools,
		)
		require.NoError(t, err)

		totalServers++
		totalTools += len(server.tools)
		fmt.Printf("     ✅ Ingested into optimizer\n\n")
	}

	// Ingest GitHub services
	for _, server := range githubGroup {
		fmt.Printf("  📍 Starting: %s (%s)\n", server.name, server.url)
		fmt.Printf("     Tools: %d\n", len(server.tools))

		desc := server.description
		err := optimizerSvc.IngestServer(
			ctx,
			server.id,
			server.name,
			&desc,
			server.tools,
		)
		require.NoError(t, err)

		totalServers++
		totalTools += len(server.tools)
		fmt.Printf("     ✅ Ingested into optimizer\n\n")
	}

	// Step 4: Show what's in the optimizer database
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("📊 Optimizer Database Status:")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("   Total Servers: %d\n", totalServers)
	fmt.Printf("   Total Tools:   %d\n", totalTools)
	fmt.Println()

	// Step 5: Simulate adding a new server dynamically (via API/CLI)
	fmt.Println("🆕 User adds a new server via CLI...")
	fmt.Println()

	newServerID := uuid.New().String()
	newServerName := "slack-service"
	newServerURL := "http://slack-mcp.local:8080"
	newServerDesc := "Slack messaging and channel management"
	newServerTools := []mcp.Tool{
		{
			Name:        "send_message",
			Description: "Send a message to a Slack channel",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"channel": map[string]interface{}{
						"type": "string",
					},
					"message": map[string]interface{}{
						"type": "string",
					},
				},
				Required: []string{"channel", "message"},
			},
		},
	}

	fmt.Printf("  📍 Adding: %s (%s)\n", newServerName, newServerURL)
	fmt.Printf("     Tools: %d\n", len(newServerTools))

	err = optimizerSvc.IngestServer(
		ctx,
		newServerID,
		newServerName,
		&newServerDesc,
		newServerTools,
	)
	require.NoError(t, err)

	totalServers++
	totalTools += len(newServerTools)
	fmt.Printf("     ✅ Ingested into optimizer\n\n")

	// Final status
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("✅ vMCP Fully Started and Indexed!")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("   Total Servers: %d\n", totalServers)
	fmt.Printf("   Total Tools:   %d\n", totalTools)
	fmt.Printf("   Database:      %s\n", dbPath)
	fmt.Println()
	fmt.Println("🔍 Optimizer is ready for semantic tool search!")
	fmt.Println()
}


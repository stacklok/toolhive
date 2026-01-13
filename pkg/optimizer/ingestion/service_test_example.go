package ingestion

// Example: How to test with multiple servers and different tool sets

/*
func TestMultipleServers(t *testing.T) {
	// Setup database and service (same as before)
	dbPath := "/tmp/optimizer-test-multi.db"
	config := &Config{
		DBConfig: &db.Config{DBPath: dbPath},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "placeholder",
			Dimension:   384,
		},
		RuntimeMode: "docker",
	}
	svc, _ := NewService(config)
	defer svc.Close()
	ctx := context.Background()

	// Server 1: Weather Service
	weatherServer := &models.BackendServer{
		BaseMCPServer: models.BaseMCPServer{
			ID:          uuid.New().String(),
			Name:        "weather-service",
			Description: stringPtr("Provides weather information"),
			Transport:   models.TransportHTTP,
		},
		URL:                "http://weather.example.com",
		WorkloadIdentifier: "prod-weather",
		Status:             models.StatusRunning,
	}

	weatherTools := []mcp.Tool{
		{
			Name:        "get_current_weather",
			Description: "Get current weather conditions",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"location": map[string]interface{}{
						"type": "string",
						"description": "City name or coordinates",
					},
					"units": map[string]interface{}{
						"type": "string",
						"enum": []string{"metric", "imperial"},
					},
				},
				Required: []string{"location"},
			},
		},
		{
			Name:        "get_forecast",
			Description: "Get weather forecast for next N days",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"location": map[string]interface{}{
						"type": "string",
					},
					"days": map[string]interface{}{
						"type": "integer",
						"minimum": 1,
						"maximum": 10,
					},
				},
			},
		},
	}

	// Server 2: Database Service
	dbServer := &models.BackendServer{
		BaseMCPServer: models.BaseMCPServer{
			ID:          uuid.New().String(),
			Name:        "database-service",
			Description: stringPtr("Database query and management tools"),
			Transport:   models.TransportStreamable,
		},
		URL:                "http://db.example.com:5432",
		WorkloadIdentifier: "prod-database",
		Status:             models.StatusRunning,
	}

	dbTools := []mcp.Tool{
		{
			Name:        "execute_query",
			Description: "Execute a SQL query",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"query": map[string]interface{}{
						"type": "string",
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
			Description: "List all tables in a database",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"database": map[string]interface{}{
						"type": "string",
					},
				},
			},
		},
	}

	// Server 3: GitHub Service
	githubServer := &models.BackendServer{
		BaseMCPServer: models.BaseMCPServer{
			ID:          uuid.New().String(),
			Name:        "github-service",
			Description: stringPtr("GitHub API integration"),
			Transport:   models.TransportSSE,
		},
		URL:                "http://github-mcp.example.com",
		WorkloadIdentifier: "prod-github",
		Status:             models.StatusRunning,
	}

	githubTools := []mcp.Tool{
		{
			Name:        "create_issue",
			Description: "Create a GitHub issue",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"repo": map[string]interface{}{
						"type": "string",
						"description": "Repository in owner/repo format",
					},
					"title": map[string]interface{}{
						"type": "string",
					},
					"body": map[string]interface{}{
						"type": "string",
					},
				},
				Required: []string{"repo", "title"},
			},
		},
	}

	// Ingest all servers
	servers := []struct {
		server *models.BackendServer
		tools  []mcp.Tool
	}{
		{weatherServer, weatherTools},
		{dbServer, dbTools},
		{githubServer, githubTools},
	}

	for _, s := range servers {
		// Create server
		if err := svc.workloadServerOps.Create(ctx, s.server); err != nil {
			t.Fatalf("Failed to create server %s: %v", s.server.Name, err)
		}

		// Sync tools
		count, err := svc.syncBackendTools(ctx, s.server.ID, s.server.Name, s.tools)
		if err != nil {
			t.Fatalf("Failed to sync tools for %s: %v", s.server.Name, err)
		}
		t.Logf("Synced %d tools for %s", count, s.server.Name)
	}

	// Now test semantic search across all servers
	results, err := svc.workloadToolOps.SearchSimilar(ctx, "query database tables", 5)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// Should find database tools ranked higher
	for i, result := range results {
		t.Logf("%d. %s - %s (distance: %.4f)",
			i+1, result.ServerName, result.ToolName, result.Distance)
	}
}

func stringPtr(s string) *string {
	return &s
}
*/

package optimizer

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

const (
	testBackendOllama = "ollama"
	testBackendOpenAI = "openai"
)

// verifyEmbeddingBackendWorking verifies that the embedding backend is actually working by attempting to generate an embedding
// This ensures the service is not just reachable but actually functional
func verifyEmbeddingBackendWorking(t *testing.T, manager *embeddings.Manager, backendType string) {
	t.Helper()
	_, err := manager.GenerateEmbedding([]string{"test"})
	if err != nil {
		if backendType == testBackendOllama {
			t.Skipf("Skipping test: Ollama is reachable but embedding generation failed. Error: %v. Ensure 'ollama pull %s' has been executed", err, embeddings.DefaultModelAllMiniLM)
		} else {
			t.Skipf("Skipping test: Embedding backend is reachable but embedding generation failed. Error: %v", err)
		}
	}
}

// TestFindTool_SemanticSearch tests semantic search capabilities
// These tests verify that find_tool can find tools based on semantic meaning,
// not just exact keyword matches
func TestFindTool_SemanticSearch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Try to use Ollama if available, otherwise skip test
	embeddingBackend := testBackendOllama
	embeddingConfig := &embeddings.Config{
		BackendType: embeddingBackend,
		BaseURL:     "http://localhost:11434",
		Model:       embeddings.DefaultModelAllMiniLM,
		Dimension:   384, // all-MiniLM-L6-v2 dimension
	}

	// Test if Ollama is available
	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		// Try OpenAI-compatible (might be vLLM or Ollama v1 API)
		embeddingConfig.BackendType = testBackendOpenAI
		embeddingConfig.BaseURL = "http://localhost:11434"
		embeddingConfig.Model = embeddings.DefaultModelAllMiniLM
		embeddingConfig.Dimension = 768
		embeddingManager, err = embeddings.NewManager(embeddingConfig)
		if err != nil {
			t.Skipf("Skipping semantic search test: No embedding backend available (Ollama or OpenAI-compatible). Error: %v", err)
			return
		}
		embeddingBackend = testBackendOpenAI
	}
	t.Cleanup(func() { _ = embeddingManager.Close() })

	// Verify embedding backend is actually working, not just reachable
	verifyEmbeddingBackendWorking(t, embeddingManager, embeddingBackend)

	// Setup optimizer integration with high semantic ratio to favor semantic search
	mcpServer := server.NewMCPServer("test-server", "1.0")
	mockClient := &mockBackendClient{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: embeddingBackend,
			BaseURL:     embeddingConfig.BaseURL,
			Model:       embeddingConfig.Model,
			Dimension:   embeddingConfig.Dimension,
		},
		HybridSearchRatio: 0.9, // 90% semantic, 10% BM25 to test semantic search
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	require.NotNil(t, integration)
	t.Cleanup(func() { _ = integration.Close() })

	// Create tools with diverse descriptions to test semantic understanding
	tools := []vmcp.Tool{
		{
			Name:        "github_pull_request_read",
			Description: "Get information on a specific pull request in GitHub repository.",
			BackendID:   "github",
		},
		{
			Name:        "github_list_pull_requests",
			Description: "List pull requests in a GitHub repository.",
			BackendID:   "github",
		},
		{
			Name:        "github_create_pull_request",
			Description: "Create a new pull request in a GitHub repository.",
			BackendID:   "github",
		},
		{
			Name:        "github_merge_pull_request",
			Description: "Merge a pull request in a GitHub repository.",
			BackendID:   "github",
		},
		{
			Name:        "github_issue_read",
			Description: "Get information about a specific issue in a GitHub repository.",
			BackendID:   "github",
		},
		{
			Name:        "github_list_issues",
			Description: "List issues in a GitHub repository.",
			BackendID:   "github",
		},
		{
			Name:        "github_create_repository",
			Description: "Create a new GitHub repository in your account or specified organization",
			BackendID:   "github",
		},
		{
			Name:        "github_get_commit",
			Description: "Get details for a commit from a GitHub repository",
			BackendID:   "github",
		},
		{
			Name:        "github_get_branch",
			Description: "Get information about a branch in a GitHub repository",
			BackendID:   "github",
		},
		{
			Name:        "fetch_fetch",
			Description: "Fetches a URL from the internet and optionally extracts its contents as markdown.",
			BackendID:   "fetch",
		},
	}

	capabilities := &aggregator.AggregatedCapabilities{
		Tools: tools,
		RoutingTable: &vmcp.RoutingTable{
			Tools:     make(map[string]*vmcp.BackendTarget),
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	for _, tool := range tools {
		capabilities.RoutingTable.Tools[tool.Name] = &vmcp.BackendTarget{
			WorkloadID:   tool.BackendID,
			WorkloadName: tool.BackendID,
		}
	}

	session := &mockSession{sessionID: "test-session"}
	err = integration.OnRegisterSession(ctx, session, capabilities)
	require.NoError(t, err)

	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, capabilities)

	// Test cases for semantic search - queries that mean the same thing but use different words
	testCases := []struct {
		name          string
		query         string
		keywords      string
		expectedTools []string // Tools that should be found semantically
		description   string
	}{
		{
			name:          "semantic_pr_synonyms",
			query:         "view code review request",
			keywords:      "",
			expectedTools: []string{"github_pull_request_read", "github_list_pull_requests"},
			description:   "Should find PR tools using semantic synonyms (code review = pull request)",
		},
		{
			name:          "semantic_merge_synonyms",
			query:         "combine code changes",
			keywords:      "",
			expectedTools: []string{"github_merge_pull_request"},
			description:   "Should find merge tool using semantic meaning (combine = merge)",
		},
		{
			name:          "semantic_create_synonyms",
			query:         "make a new code review",
			keywords:      "",
			expectedTools: []string{"github_create_pull_request", "github_list_pull_requests", "github_pull_request_read"},
			description:   "Should find PR-related tools using semantic meaning (make = create, code review = PR)",
		},
		{
			name:          "semantic_issue_synonyms",
			query:         "show bug reports",
			keywords:      "",
			expectedTools: []string{"github_issue_read", "github_list_issues"},
			description:   "Should find issue tools using semantic synonyms (bug report = issue)",
		},
		{
			name:          "semantic_repository_synonyms",
			query:         "start a new project",
			keywords:      "",
			expectedTools: []string{"github_create_repository"},
			description:   "Should find repository tool using semantic meaning (project = repository)",
		},
		{
			name:          "semantic_commit_synonyms",
			query:         "get change details",
			keywords:      "",
			expectedTools: []string{"github_get_commit"},
			description:   "Should find commit tool using semantic meaning (change = commit)",
		},
		{
			name:          "semantic_fetch_synonyms",
			query:         "download web page content",
			keywords:      "",
			expectedTools: []string{"fetch_fetch"},
			description:   "Should find fetch tool using semantic synonyms (download = fetch)",
		},
		{
			name:          "semantic_branch_synonyms",
			query:         "get branch information",
			keywords:      "",
			expectedTools: []string{"github_get_branch"},
			description:   "Should find branch tool using semantic meaning",
		},
		{
			name:          "semantic_related_concepts",
			query:         "code collaboration features",
			keywords:      "",
			expectedTools: []string{"github_pull_request_read", "github_create_pull_request", "github_issue_read"},
			description:   "Should find collaboration-related tools (PRs and issues are collaboration features)",
		},
		{
			name:          "semantic_intent_based",
			query:         "I want to see what code changes were made",
			keywords:      "",
			expectedTools: []string{"github_get_commit", "github_pull_request_read"},
			description:   "Should find tools based on user intent (seeing code changes = commits/PRs)",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name: "optim.find_tool",
					Arguments: map[string]any{
						"tool_description": tc.query,
						"tool_keywords":    tc.keywords,
						"limit":            10,
					},
				},
			}

			handler := integration.CreateFindToolHandler()
			result, err := handler(ctxWithCaps, request)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.False(t, result.IsError, "Tool call should not return error for query: %s", tc.query)

			// Parse the result
			require.NotEmpty(t, result.Content, "Result should have content")
			textContent, okText := mcp.AsTextContent(result.Content[0])
			require.True(t, okText, "Result should be text content")

			var response map[string]any
			err = json.Unmarshal([]byte(textContent.Text), &response)
			require.NoError(t, err, "Result should be valid JSON")

			toolsArray, okArray := response["tools"].([]interface{})
			require.True(t, okArray, "Response should have tools array")
			require.NotEmpty(t, toolsArray, "Should return at least one result for semantic query: %s", tc.query)

			// Extract tool names from results
			foundTools := make([]string, 0, len(toolsArray))
			for _, toolInterface := range toolsArray {
				toolMap, okMap := toolInterface.(map[string]interface{})
				require.True(t, okMap, "Tool should be a map")
				toolName, okName := toolMap["name"].(string)
				require.True(t, okName, "Tool should have name")
				foundTools = append(foundTools, toolName)

				// Verify similarity score exists and is reasonable
				similarity, okScore := toolMap["similarity_score"].(float64)
				require.True(t, okScore, "Tool should have similarity_score")
				assert.Greater(t, similarity, 0.0, "Similarity score should be positive")
			}

			// Check that at least one expected tool is found
			foundCount := 0
			for _, expectedTool := range tc.expectedTools {
				for _, foundTool := range foundTools {
					if foundTool == expectedTool {
						foundCount++
						break
					}
				}
			}

			assert.GreaterOrEqual(t, foundCount, 1,
				"Semantic query '%s' should find at least one expected tool from %v. Found tools: %v (found %d/%d)",
				tc.query, tc.expectedTools, foundTools, foundCount, len(tc.expectedTools))

			// Log results for debugging
			if foundCount < len(tc.expectedTools) {
				t.Logf("Semantic query '%s': Found %d/%d expected tools. Found: %v, Expected: %v",
					tc.query, foundCount, len(tc.expectedTools), foundTools, tc.expectedTools)
			}

			// Verify token metrics exist
			tokenMetrics, okMetrics := response["token_metrics"].(map[string]interface{})
			require.True(t, okMetrics, "Response should have token_metrics")
			assert.Contains(t, tokenMetrics, "baseline_tokens")
			assert.Contains(t, tokenMetrics, "returned_tokens")
		})
	}
}

// TestFindTool_SemanticVsKeyword tests that semantic search finds different results than keyword search
func TestFindTool_SemanticVsKeyword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Try to use Ollama if available
	embeddingBackend := "ollama"
	embeddingConfig := &embeddings.Config{
		BackendType: embeddingBackend,
		BaseURL:     "http://localhost:11434",
		Model:       "nomic-embed-text",
		Dimension:   768,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		// Try OpenAI-compatible
		embeddingConfig.BackendType = testBackendOpenAI
		embeddingManager, err = embeddings.NewManager(embeddingConfig)
		if err != nil {
			t.Skipf("Skipping test: No embedding backend available. Error: %v", err)
			return
		}
		embeddingBackend = testBackendOpenAI
	}
	
	// Verify embedding backend is actually working, not just reachable
	verifyEmbeddingBackendWorking(t, embeddingManager, embeddingBackend)
	_ = embeddingManager.Close()

	mcpServer := server.NewMCPServer("test-server", "1.0")
	mockClient := &mockBackendClient{}

	// Test with high semantic ratio
	configSemantic := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db-semantic"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: embeddingBackend,
			BaseURL:     embeddingConfig.BaseURL,
			Model:       embeddingConfig.Model,
			Dimension:   embeddingConfig.Dimension,
		},
		HybridSearchRatio: 0.9, // 90% semantic
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integrationSemantic, err := NewIntegration(ctx, configSemantic, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integrationSemantic.Close() }()

	// Test with low semantic ratio (high BM25)
	configKeyword := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db-keyword"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: embeddingBackend,
			BaseURL:     embeddingConfig.BaseURL,
			Model:       embeddingConfig.Model,
			Dimension:   embeddingConfig.Dimension,
		},
		HybridSearchRatio: 0.1, // 10% semantic, 90% BM25
	}

	integrationKeyword, err := NewIntegration(ctx, configKeyword, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integrationKeyword.Close() }()

	tools := []vmcp.Tool{
		{
			Name:        "github_pull_request_read",
			Description: "Get information on a specific pull request in GitHub repository.",
			BackendID:   "github",
		},
		{
			Name:        "github_create_repository",
			Description: "Create a new GitHub repository in your account or specified organization",
			BackendID:   "github",
		},
	}

	capabilities := &aggregator.AggregatedCapabilities{
		Tools: tools,
		RoutingTable: &vmcp.RoutingTable{
			Tools:     make(map[string]*vmcp.BackendTarget),
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	for _, tool := range tools {
		capabilities.RoutingTable.Tools[tool.Name] = &vmcp.BackendTarget{
			WorkloadID:   tool.BackendID,
			WorkloadName: tool.BackendID,
		}
	}

	session := &mockSession{sessionID: "test-session"}
	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, capabilities)

	// Register both integrations
	err = integrationSemantic.OnRegisterSession(ctx, session, capabilities)
	require.NoError(t, err)

	err = integrationKeyword.OnRegisterSession(ctx, session, capabilities)
	require.NoError(t, err)

	// Query that has semantic meaning but no exact keyword match
	query := "view code review"

	// Test semantic search
	requestSemantic := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": query,
				"tool_keywords":    "",
				"limit":            10,
			},
		},
	}

	handlerSemantic := integrationSemantic.CreateFindToolHandler()
	resultSemantic, err := handlerSemantic(ctxWithCaps, requestSemantic)
	require.NoError(t, err)
	require.False(t, resultSemantic.IsError)

	// Test keyword search
	requestKeyword := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": query,
				"tool_keywords":    "",
				"limit":            10,
			},
		},
	}

	handlerKeyword := integrationKeyword.CreateFindToolHandler()
	resultKeyword, err := handlerKeyword(ctxWithCaps, requestKeyword)
	require.NoError(t, err)
	require.False(t, resultKeyword.IsError)

	// Parse both results
	textSemantic, _ := mcp.AsTextContent(resultSemantic.Content[0])
	var responseSemantic map[string]any
	json.Unmarshal([]byte(textSemantic.Text), &responseSemantic)

	textKeyword, _ := mcp.AsTextContent(resultKeyword.Content[0])
	var responseKeyword map[string]any
	json.Unmarshal([]byte(textKeyword.Text), &responseKeyword)

	toolsSemantic, _ := responseSemantic["tools"].([]interface{})
	toolsKeyword, _ := responseKeyword["tools"].([]interface{})

	// Both should find results (semantic should find PR tools, keyword might not)
	assert.NotEmpty(t, toolsSemantic, "Semantic search should find results")
	assert.NotEmpty(t, toolsKeyword, "Keyword search should find results")

	// Semantic search should find pull request tools even without exact keyword match
	foundPRSemantic := false
	for _, toolInterface := range toolsSemantic {
		toolMap, _ := toolInterface.(map[string]interface{})
		toolName, _ := toolMap["name"].(string)
		if toolName == "github_pull_request_read" {
			foundPRSemantic = true
			break
		}
	}

	t.Logf("Semantic search (90%% semantic): Found %d tools", len(toolsSemantic))
	t.Logf("Keyword search (10%% semantic): Found %d tools", len(toolsKeyword))
	t.Logf("Semantic search found PR tool: %v", foundPRSemantic)

	// Semantic search should be able to find semantically related tools
	// even when keywords don't match exactly
	assert.True(t, foundPRSemantic,
		"Semantic search should find 'github_pull_request_read' for query 'view code review' even without exact keyword match")
}

// TestFindTool_SemanticSimilarityScores tests that similarity scores are meaningful
func TestFindTool_SemanticSimilarityScores(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Try to use Ollama if available
	embeddingBackend := "ollama"
	embeddingConfig := &embeddings.Config{
		BackendType: embeddingBackend,
		BaseURL:     "http://localhost:11434",
		Model:       "nomic-embed-text",
		Dimension:   768,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		// Try OpenAI-compatible
		embeddingConfig.BackendType = testBackendOpenAI
		embeddingManager, err = embeddings.NewManager(embeddingConfig)
		if err != nil {
			t.Skipf("Skipping test: No embedding backend available. Error: %v", err)
			return
		}
		embeddingBackend = testBackendOpenAI
	}
	
	// Verify embedding backend is actually working, not just reachable
	verifyEmbeddingBackendWorking(t, embeddingManager, embeddingBackend)
	_ = embeddingManager.Close()

	mcpServer := server.NewMCPServer("test-server", "1.0")
	mockClient := &mockBackendClient{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: embeddingBackend,
			BaseURL:     embeddingConfig.BaseURL,
			Model:       embeddingConfig.Model,
			Dimension:   embeddingConfig.Dimension,
		},
		HybridSearchRatio: 0.9, // High semantic ratio
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	tools := []vmcp.Tool{
		{
			Name:        "github_pull_request_read",
			Description: "Get information on a specific pull request in GitHub repository.",
			BackendID:   "github",
		},
		{
			Name:        "github_create_repository",
			Description: "Create a new GitHub repository in your account or specified organization",
			BackendID:   "github",
		},
		{
			Name:        "fetch_fetch",
			Description: "Fetches a URL from the internet and optionally extracts its contents as markdown.",
			BackendID:   "fetch",
		},
	}

	capabilities := &aggregator.AggregatedCapabilities{
		Tools: tools,
		RoutingTable: &vmcp.RoutingTable{
			Tools:     make(map[string]*vmcp.BackendTarget),
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	for _, tool := range tools {
		capabilities.RoutingTable.Tools[tool.Name] = &vmcp.BackendTarget{
			WorkloadID:   tool.BackendID,
			WorkloadName: tool.BackendID,
		}
	}

	session := &mockSession{sessionID: "test-session"}
	err = integration.OnRegisterSession(ctx, session, capabilities)
	require.NoError(t, err)

	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, capabilities)

	// Query for pull request
	query := "view pull request"

	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": query,
				"tool_keywords":    "",
				"limit":            10,
			},
		},
	}

	handler := integration.CreateFindToolHandler()
	result, err := handler(ctxWithCaps, request)
	require.NoError(t, err)
	require.False(t, result.IsError)

	textContent, _ := mcp.AsTextContent(result.Content[0])
	var response map[string]any
	json.Unmarshal([]byte(textContent.Text), &response)

	toolsArray, _ := response["tools"].([]interface{})
	require.NotEmpty(t, toolsArray)

	// Check that results are sorted by similarity (highest first)
	var similarities []float64
	for _, toolInterface := range toolsArray {
		toolMap, _ := toolInterface.(map[string]interface{})
		similarity, _ := toolMap["similarity_score"].(float64)
		similarities = append(similarities, similarity)
	}

	// Verify results are sorted by similarity (descending)
	for i := 1; i < len(similarities); i++ {
		assert.GreaterOrEqual(t, similarities[i-1], similarities[i],
			"Results should be sorted by similarity score (descending). Scores: %v", similarities)
	}

	// The most relevant tool (pull request) should have a higher similarity than unrelated tools
	if len(similarities) > 1 {
		// First result should have highest similarity
		assert.Greater(t, similarities[0], 0.0, "Top result should have positive similarity")
	}
}

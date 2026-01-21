package optimizer

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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

// verifyOllamaWorking verifies that Ollama is actually working by attempting to generate an embedding
// This ensures the service is not just reachable but actually functional
func verifyOllamaWorking(t *testing.T, manager *embeddings.Manager) {
	t.Helper()
	_, err := manager.GenerateEmbedding([]string{"test"})
	if err != nil {
		t.Skipf("Skipping test: Ollama is reachable but embedding generation failed. Error: %v. Ensure 'ollama pull %s' has been executed", err, embeddings.DefaultModelAllMiniLM)
	}
}

// getRealToolData returns test data based on actual MCP server tools
// These are real tool descriptions from GitHub and other MCP servers
func getRealToolData() []vmcp.Tool {
	return []vmcp.Tool{
		{
			Name:        "github_pull_request_read",
			Description: "Get information on a specific pull request in GitHub repository.",
			BackendID:   "github",
		},
		{
			Name:        "github_list_pull_requests",
			Description: "List pull requests in a GitHub repository. If the user specifies an author, then DO NOT use this tool and use the search_pull_requests tool instead.",
			BackendID:   "github",
		},
		{
			Name:        "github_search_pull_requests",
			Description: "Search for pull requests in GitHub repositories using issues search syntax already scoped to is:pr",
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
			Name:        "github_pull_request_review_write",
			Description: "Create and/or submit, delete review of a pull request.",
			BackendID:   "github",
		},
		{
			Name:        "github_issue_read",
			Description: "Get information about a specific issue in a GitHub repository.",
			BackendID:   "github",
		},
		{
			Name:        "github_list_issues",
			Description: "List issues in a GitHub repository. For pagination, use the 'endCursor' from the previous response's 'pageInfo' in the 'after' parameter.",
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
			Name:        "fetch_fetch",
			Description: "Fetches a URL from the internet and optionally extracts its contents as markdown.",
			BackendID:   "fetch",
		},
	}
}

// TestFindTool_StringMatching tests that find_tool can match strings correctly
func TestFindTool_StringMatching(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Setup optimizer integration
	mcpServer := server.NewMCPServer("test-server", "1.0")
	mockClient := &mockBackendClient{}

	// Try to use Ollama if available, otherwise skip test
	embeddingConfig := &embeddings.Config{
		BackendType: embeddings.BackendTypeOllama,
		BaseURL:     "http://localhost:11434",
		Model:       embeddings.DefaultModelAllMiniLM,
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v. Run 'ollama serve && ollama pull %s'", err, embeddings.DefaultModelAllMiniLM)
		return
	}
	t.Cleanup(func() { _ = embeddingManager.Close() })

	// Verify Ollama is actually working, not just reachable
	verifyOllamaWorking(t, embeddingManager)

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: embeddings.BackendTypeOllama,
			BaseURL:     "http://localhost:11434",
			Model:       embeddings.DefaultModelAllMiniLM,
			Dimension:   384,
		},
		HybridSearchRatio: 0.5, // 50% semantic, 50% BM25 for better string matching
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	require.NotNil(t, integration)
	t.Cleanup(func() { _ = integration.Close() })

	// Get real tool data
	tools := getRealToolData()

	// Create capabilities with real tools
	capabilities := &aggregator.AggregatedCapabilities{
		Tools: tools,
		RoutingTable: &vmcp.RoutingTable{
			Tools:     make(map[string]*vmcp.BackendTarget),
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	// Build routing table
	for _, tool := range tools {
		capabilities.RoutingTable.Tools[tool.Name] = &vmcp.BackendTarget{
			WorkloadID:   tool.BackendID,
			WorkloadName: tool.BackendID,
		}
	}

	// Register session and generate embeddings
	session := &mockSession{sessionID: "test-session"}
	err = integration.OnRegisterSession(ctx, session, capabilities)
	require.NoError(t, err)

	// Manually ingest tools for testing (OnRegisterSession skips ingestion)
	mcpTools := make([]mcp.Tool, len(tools))
	for i, tool := range tools {
		mcpTools[i] = mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
		}
	}
	err = integration.IngestToolsForTesting(ctx, "github", "GitHub", nil, mcpTools)
	require.NoError(t, err)

	// Create context with capabilities
	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, capabilities)

	// Test cases: query -> expected tool names that should be found
	testCases := []struct {
		name          string
		query         string
		keywords      string
		expectedTools []string // Tools that should definitely be in results
		minResults    int      // Minimum number of results expected
		description   string
	}{
		{
			name:          "exact_pull_request_match",
			query:         "pull request",
			keywords:      "pull request",
			expectedTools: []string{"github_pull_request_read", "github_list_pull_requests", "github_create_pull_request"},
			minResults:    3,
			description:   "Should find tools with exact 'pull request' string match",
		},
		{
			name:          "pull_request_in_name",
			query:         "pull request",
			keywords:      "pull_request",
			expectedTools: []string{"github_pull_request_read", "github_list_pull_requests"},
			minResults:    2,
			description:   "Should match tools with 'pull_request' in name",
		},
		{
			name:          "list_pull_requests",
			query:         "list pull requests",
			keywords:      "list pull requests",
			expectedTools: []string{"github_list_pull_requests"},
			minResults:    1,
			description:   "Should find list pull requests tool",
		},
		{
			name:          "read_pull_request",
			query:         "read pull request",
			keywords:      "read pull request",
			expectedTools: []string{"github_pull_request_read"},
			minResults:    1,
			description:   "Should find read pull request tool",
		},
		{
			name:          "create_pull_request",
			query:         "create pull request",
			keywords:      "create pull request",
			expectedTools: []string{"github_create_pull_request"},
			minResults:    1,
			description:   "Should find create pull request tool",
		},
		{
			name:          "merge_pull_request",
			query:         "merge pull request",
			keywords:      "merge pull request",
			expectedTools: []string{"github_merge_pull_request"},
			minResults:    1,
			description:   "Should find merge pull request tool",
		},
		{
			name:          "search_pull_requests",
			query:         "search pull requests",
			keywords:      "search pull requests",
			expectedTools: []string{"github_search_pull_requests"},
			minResults:    1,
			description:   "Should find search pull requests tool",
		},
		{
			name:          "issue_tools",
			query:         "issue",
			keywords:      "issue",
			expectedTools: []string{"github_issue_read", "github_list_issues"},
			minResults:    2,
			description:   "Should find issue-related tools",
		},
		{
			name:          "repository_tool",
			query:         "create repository",
			keywords:      "create repository",
			expectedTools: []string{"github_create_repository"},
			minResults:    1,
			description:   "Should find create repository tool",
		},
		{
			name:          "commit_tool",
			query:         "get commit",
			keywords:      "commit",
			expectedTools: []string{"github_get_commit"},
			minResults:    1,
			description:   "Should find get commit tool",
		},
		{
			name:          "fetch_tool",
			query:         "fetch URL",
			keywords:      "fetch",
			expectedTools: []string{"fetch_fetch"},
			minResults:    1,
			description:   "Should find fetch tool",
		},
	}

	for _, tc := range testCases {
		tc := tc // capture loop variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create the tool call request
			request := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name: "optim_find_tool",
					Arguments: map[string]any{
						"tool_description": tc.query,
						"tool_keywords":    tc.keywords,
						"limit":            20,
					},
				},
			}

			// Call the handler
			handler := integration.CreateFindToolHandler()
			result, err := handler(ctxWithCaps, request)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.False(t, result.IsError, "Tool call should not return error")

			// Parse the result
			require.NotEmpty(t, result.Content, "Result should have content")
			textContent, ok := mcp.AsTextContent(result.Content[0])
			require.True(t, ok, "Result should be text content")

			// Parse JSON response
			var response map[string]any
			err = json.Unmarshal([]byte(textContent.Text), &response)
			require.NoError(t, err, "Result should be valid JSON")

			// Check tools array exists
			toolsArray, ok := response["tools"].([]interface{})
			require.True(t, ok, "Response should have tools array")
			require.GreaterOrEqual(t, len(toolsArray), tc.minResults,
				"Should return at least %d results for query: %s", tc.minResults, tc.query)

			// Extract tool names from results
			foundTools := make([]string, 0, len(toolsArray))
			for _, toolInterface := range toolsArray {
				toolMap, okMap := toolInterface.(map[string]interface{})
				require.True(t, okMap, "Tool should be a map")
				toolName, okName := toolMap["name"].(string)
				require.True(t, okName, "Tool should have name")
				foundTools = append(foundTools, toolName)
			}

			// Check that at least some expected tools are found
			// String matching may not be perfect, so we check that at least one expected tool is found
			foundCount := 0
			for _, expectedTool := range tc.expectedTools {
				for _, foundTool := range foundTools {
					if foundTool == expectedTool {
						foundCount++
						break
					}
				}
			}

			// We should find at least one expected tool, or at least 50% of expected tools
			minExpected := 1
			if len(tc.expectedTools) > 1 {
				half := len(tc.expectedTools) / 2
				if half > minExpected {
					minExpected = half
				}
			}

			assert.GreaterOrEqual(t, foundCount, minExpected,
				"Query '%s' should find at least %d of expected tools %v. Found tools: %v (found %d/%d)",
				tc.query, minExpected, tc.expectedTools, foundTools, foundCount, len(tc.expectedTools))

			// Log which expected tools were found for debugging
			if foundCount < len(tc.expectedTools) {
				t.Logf("Query '%s': Found %d/%d expected tools. Found: %v, Expected: %v",
					tc.query, foundCount, len(tc.expectedTools), foundTools, tc.expectedTools)
			}

			// Verify token metrics exist
			tokenMetrics, ok := response["token_metrics"].(map[string]interface{})
			require.True(t, ok, "Response should have token_metrics")
			assert.Contains(t, tokenMetrics, "baseline_tokens")
			assert.Contains(t, tokenMetrics, "returned_tokens")
			assert.Contains(t, tokenMetrics, "tokens_saved")
			assert.Contains(t, tokenMetrics, "savings_percentage")
		})
	}
}

// TestFindTool_ExactStringMatch tests that exact string matches work correctly
func TestFindTool_ExactStringMatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Setup optimizer integration with higher BM25 ratio for better string matching
	mcpServer := server.NewMCPServer("test-server", "1.0")
	mockClient := &mockBackendClient{}

	// Try to use Ollama if available, otherwise skip test
	embeddingConfig := &embeddings.Config{
		BackendType: embeddings.BackendTypeOllama,
		BaseURL:     "http://localhost:11434",
		Model:       embeddings.DefaultModelAllMiniLM,
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v. Run 'ollama serve && ollama pull %s'", err, embeddings.DefaultModelAllMiniLM)
		return
	}
	t.Cleanup(func() { _ = embeddingManager.Close() })

	// Verify Ollama is actually working, not just reachable
	verifyOllamaWorking(t, embeddingManager)

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: embeddings.BackendTypeOllama,
			BaseURL:     "http://localhost:11434",
			Model:       embeddings.DefaultModelAllMiniLM,
			Dimension:   384,
		},
		HybridSearchRatio: 0.3, // 30% semantic, 70% BM25 for better exact string matching
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	require.NotNil(t, integration)
	t.Cleanup(func() { _ = integration.Close() })

	// Create tools with specific strings to match
	tools := []vmcp.Tool{
		{
			Name:        "test_pull_request_tool",
			Description: "This tool handles pull requests in GitHub",
			BackendID:   "test",
		},
		{
			Name:        "test_issue_tool",
			Description: "This tool handles issues in GitHub",
			BackendID:   "test",
		},
		{
			Name:        "test_repository_tool",
			Description: "This tool creates repositories",
			BackendID:   "test",
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

	// Manually ingest tools for testing (OnRegisterSession skips ingestion)
	mcpTools := make([]mcp.Tool, len(tools))
	for i, tool := range tools {
		mcpTools[i] = mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
		}
	}
	err = integration.IngestToolsForTesting(ctx, "test", "test", nil, mcpTools)
	require.NoError(t, err)

	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, capabilities)

	// Test exact string matching
	testCases := []struct {
		name         string
		query        string
		keywords     string
		expectedTool string
		description  string
	}{
		{
			name:         "exact_pull_request_string",
			query:        "pull request",
			keywords:     "pull request",
			expectedTool: "test_pull_request_tool",
			description:  "Should match exact 'pull request' string",
		},
		{
			name:         "exact_issue_string",
			query:        "issue",
			keywords:     "issue",
			expectedTool: "test_issue_tool",
			description:  "Should match exact 'issue' string",
		},
		{
			name:         "exact_repository_string",
			query:        "repository",
			keywords:     "repository",
			expectedTool: "test_repository_tool",
			description:  "Should match exact 'repository' string",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name: "optim_find_tool",
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
			require.False(t, result.IsError)

			textContent, okText := mcp.AsTextContent(result.Content[0])
			require.True(t, okText)

			var response map[string]any
			err = json.Unmarshal([]byte(textContent.Text), &response)
			require.NoError(t, err)

			toolsArray, okArray := response["tools"].([]interface{})
			require.True(t, okArray)
			require.NotEmpty(t, toolsArray, "Should find at least one tool for query: %s", tc.query)

			// Check that the expected tool is in the results
			found := false
			for _, toolInterface := range toolsArray {
				toolMap, okMap := toolInterface.(map[string]interface{})
				require.True(t, okMap)
				toolName, okName := toolMap["name"].(string)
				require.True(t, okName)
				if toolName == tc.expectedTool {
					found = true
					break
				}
			}

			assert.True(t, found,
				"Expected tool '%s' not found in results for query '%s'. This indicates string matching is not working correctly.",
				tc.expectedTool, tc.query)
		})
	}
}

// TestFindTool_CaseInsensitive tests case-insensitive string matching
func TestFindTool_CaseInsensitive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	mcpServer := server.NewMCPServer("test-server", "1.0")
	mockClient := &mockBackendClient{}

	// Try to use Ollama if available, otherwise skip test
	embeddingConfig := &embeddings.Config{
		BackendType: embeddings.BackendTypeOllama,
		BaseURL:     "http://localhost:11434",
		Model:       embeddings.DefaultModelAllMiniLM,
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v. Run 'ollama serve && ollama pull %s'", err, embeddings.DefaultModelAllMiniLM)
		return
	}
	t.Cleanup(func() { _ = embeddingManager.Close() })

	// Verify Ollama is actually working, not just reachable
	verifyOllamaWorking(t, embeddingManager)

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: embeddings.BackendTypeOllama,
			BaseURL:     "http://localhost:11434",
			Model:       embeddings.DefaultModelAllMiniLM,
			Dimension:   384,
		},
		HybridSearchRatio: 0.3, // Favor BM25 for string matching
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	require.NotNil(t, integration)
	t.Cleanup(func() { _ = integration.Close() })

	tools := []vmcp.Tool{
		{
			Name:        "github_pull_request_read",
			Description: "Get information on a specific pull request in GitHub repository.",
			BackendID:   "github",
		},
	}

	capabilities := &aggregator.AggregatedCapabilities{
		Tools: tools,
		RoutingTable: &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"github_pull_request_read": {
					WorkloadID:   "github",
					WorkloadName: "github",
				},
			},
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	session := &mockSession{sessionID: "test-session"}
	err = integration.OnRegisterSession(ctx, session, capabilities)
	require.NoError(t, err)

	// Manually ingest tools for testing (OnRegisterSession skips ingestion)
	mcpTools := make([]mcp.Tool, len(tools))
	for i, tool := range tools {
		mcpTools[i] = mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
		}
	}
	err = integration.IngestToolsForTesting(ctx, "github", "GitHub", nil, mcpTools)
	require.NoError(t, err)

	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, capabilities)

	// Test different case variations
	queries := []string{
		"PULL REQUEST",
		"Pull Request",
		"pull request",
		"PuLl ReQuEsT",
	}

	for _, query := range queries {
		query := query
		t.Run("case_"+strings.ToLower(query), func(t *testing.T) {
			t.Parallel()

			request := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name: "optim_find_tool",
					Arguments: map[string]any{
						"tool_description": query,
						"tool_keywords":    strings.ToLower(query),
						"limit":            10,
					},
				},
			}

			handler := integration.CreateFindToolHandler()
			result, err := handler(ctxWithCaps, request)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.False(t, result.IsError)

			textContent, okText := mcp.AsTextContent(result.Content[0])
			require.True(t, okText)

			var response map[string]any
			err = json.Unmarshal([]byte(textContent.Text), &response)
			require.NoError(t, err)

			toolsArray, okArray := response["tools"].([]interface{})
			require.True(t, okArray)

			// Should find the pull request tool regardless of case
			found := false
			for _, toolInterface := range toolsArray {
				toolMap, okMap := toolInterface.(map[string]interface{})
				require.True(t, okMap)
				toolName, okName := toolMap["name"].(string)
				require.True(t, okName)
				if toolName == "github_pull_request_read" {
					found = true
					break
				}
			}

			assert.True(t, found,
				"Should find pull request tool with case-insensitive query: %s", query)
		})
	}
}

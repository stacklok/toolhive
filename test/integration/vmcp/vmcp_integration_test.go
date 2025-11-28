package vmcp_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/test/integration/vmcp/helpers"
)

// TestVMCPServer_ConflictResolution verifies that vMCP correctly resolves
// tool name conflicts between backends using prefix-based conflict resolution.
// Subtests share a common vMCP server and client instance for efficiency.
//
//nolint:paralleltest,tparallel // Subtests intentionally sequential - share expensive test fixtures
func TestVMCPServer_ConflictResolution(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup: Create synthetic MCP backend servers
	githubServer := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendTool("create_issue", "Create a GitHub issue",
			func(_ context.Context, _ map[string]any) string {
				return `{"source": "github", "issue_id": 123, "url": "https://github.com/org/repo/issues/123"}`
			}),
		helpers.NewBackendTool("list_repos", "List GitHub repositories",
			func(_ context.Context, _ map[string]any) string {
				return `{"source": "github", "repos": ["repo1", "repo2", "repo3"]}`
			}),
	}, helpers.WithBackendName("github-mcp"))
	defer githubServer.Close()

	jiraServer := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendTool("create_issue", "Create a Jira issue",
			func(_ context.Context, _ map[string]any) string {
				return `{"source": "jira", "issue_key": "PROJ-456", "url": "https://jira.example.com/browse/PROJ-456"}`
			}),
	}, helpers.WithBackendName("jira-mcp"))
	defer jiraServer.Close()

	// Configure backends pointing to test servers
	backends := []vmcp.Backend{
		helpers.NewBackend("github",
			helpers.WithURL(githubServer.URL+"/mcp"),
			helpers.WithMetadata("group", "test-group"),
		),
		helpers.NewBackend("jira",
			helpers.WithURL(jiraServer.URL+"/mcp"),
			helpers.WithMetadata("group", "test-group"),
		),
	}

	// Create vMCP server with prefix conflict resolution
	vmcpServer := helpers.NewVMCPServer(ctx, t, backends,
		helpers.WithPrefixConflictResolution("{workload}_"),
	)

	// Create and initialize MCP client
	vmcpURL := "http://" + vmcpServer.Address() + "/mcp"
	client := helpers.NewMCPClient(ctx, t, vmcpURL)
	defer client.Close()

	// Run subtests
	t.Run("ListTools", func(t *testing.T) {
		toolsResp := client.ListTools(ctx)
		toolNames := helpers.GetToolNames(toolsResp)

		assert.Len(t, toolNames, 3, "Should have 3 tools after prefix conflict resolution")
		assert.Contains(t, toolNames, "github_create_issue")
		assert.Contains(t, toolNames, "github_list_repos")
		assert.Contains(t, toolNames, "jira_create_issue")
	})

	t.Run("CallGitHubCreateIssue", func(t *testing.T) {
		resp := client.CallTool(ctx, "github_create_issue", map[string]any{"title": "Test Issue"})
		text := helpers.AssertToolCallSuccess(t, resp)
		helpers.AssertTextContains(t, text, "github", "issue_id")
	})

	t.Run("CallJiraCreateIssue", func(t *testing.T) {
		resp := client.CallTool(ctx, "jira_create_issue", map[string]any{"summary": "Test Ticket"})
		text := helpers.AssertToolCallSuccess(t, resp)
		helpers.AssertTextContains(t, text, "jira", "issue_key")
	})

	t.Run("CallGitHubListRepos", func(t *testing.T) {
		resp := client.CallTool(ctx, "github_list_repos", map[string]any{})
		text := helpers.AssertToolCallSuccess(t, resp)
		helpers.AssertTextContains(t, text, "repos")
	})
}

// TestVMCPServer_TwoBoundaryAuth_HeaderInjection verifies the two-boundary authentication
// model where vMCP injects different auth headers to different backends, and ensures no
// credential leakage occurs between backends.
// Subtests share a common vMCP server and client instance for efficiency.
//
//nolint:paralleltest,tparallel // Subtests intentionally sequential - share expensive test fixtures
func TestVMCPServer_TwoBoundaryAuth_HeaderInjection(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup: Create backend servers that verify auth headers
	// GitLab backend expects X-GitLab-Token header
	gitlabServer := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendTool("list_projects", "List GitLab projects",
			func(ctx context.Context, _ map[string]any) string {
				// Verify the correct auth header was injected by vMCP
				headers := helpers.GetHTTPHeadersFromContext(ctx)
				if headers == nil {
					return `{"error": "no headers in context"}`
				}

				gitlabToken := headers.Get("X-Gitlab-Token")
				if gitlabToken == "" {
					return `{"error": "missing X-GitLab-Token header", "auth": "failed"}`
				}

				if gitlabToken != "secret-123" {
					return `{"error": "invalid X-GitLab-Token header", "auth": "failed", "received": "` + gitlabToken + `"}`
				}

				// Auth successful
				return `{"source": "gitlab", "projects": ["project1", "project2"], "auth": "success"}`
			}),
	},
		helpers.WithBackendName("gitlab-mcp"),
		helpers.WithCaptureHeaders(), // Enable header capture
	)
	defer gitlabServer.Close()

	// GitHub backend expects Authorization header
	githubServer := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendTool("list_repos", "List GitHub repositories",
			func(ctx context.Context, _ map[string]any) string {
				// Verify the correct auth header was injected by vMCP
				headers := helpers.GetHTTPHeadersFromContext(ctx)
				if headers == nil {
					return `{"error": "no headers in context"}`
				}

				authHeader := headers.Get("Authorization")
				if authHeader == "" {
					return `{"error": "missing Authorization header", "auth": "failed"}`
				}

				if authHeader != "Bearer token-456" {
					return `{"error": "invalid Authorization header", "auth": "failed", "received": "` + authHeader + `"}`
				}

				// Auth successful - also verify GitLab token was NOT leaked
				gitlabToken := headers.Get("X-Gitlab-Token")
				if gitlabToken != "" {
					return `{"error": "credential leakage detected", "auth": "failed", "leaked": "X-GitLab-Token"}`
				}

				return `{"source": "github", "repos": ["repo1", "repo2", "repo3"], "auth": "success"}`
			}),
	},
		helpers.WithBackendName("github-mcp"),
		helpers.WithCaptureHeaders(), // Enable header capture
	)
	defer githubServer.Close()

	// Configure backends with header_injection auth
	backends := []vmcp.Backend{
		helpers.NewBackend("gitlab",
			helpers.WithURL(gitlabServer.URL+"/mcp"),
			helpers.WithAuth(&authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-GitLab-Token",
					HeaderValue: "secret-123",
				},
			}),
		),
		helpers.NewBackend("github",
			helpers.WithURL(githubServer.URL+"/mcp"),
			helpers.WithAuth(&authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "Authorization",
					HeaderValue: "Bearer token-456",
				},
			}),
		),
	}

	// Create vMCP server (uses anonymous incoming auth by default)
	vmcpServer := helpers.NewVMCPServer(ctx, t, backends,
		helpers.WithPrefixConflictResolution("{workload}_"),
	)

	// Create and initialize MCP client
	vmcpURL := "http://" + vmcpServer.Address() + "/mcp"
	client := helpers.NewMCPClient(ctx, t, vmcpURL)
	defer client.Close()

	// Run subtests
	t.Run("ListTools", func(t *testing.T) {
		toolsResp := client.ListTools(ctx)
		toolNames := helpers.GetToolNames(toolsResp)

		assert.Len(t, toolNames, 2, "Should have 2 tools from both backends")
		assert.Contains(t, toolNames, "gitlab_list_projects")
		assert.Contains(t, toolNames, "github_list_repos")
	})

	t.Run("CallGitLabListProjects", func(t *testing.T) {
		resp := client.CallTool(ctx, "gitlab_list_projects", map[string]any{})
		text := helpers.AssertToolCallSuccess(t, resp)
		helpers.AssertTextContains(t, text, "gitlab", "projects", "auth", "success")
		helpers.AssertTextNotContains(t, text, "error", "failed")
	})

	t.Run("CallGitHubListRepos", func(t *testing.T) {
		resp := client.CallTool(ctx, "github_list_repos", map[string]any{})
		text := helpers.AssertToolCallSuccess(t, resp)
		helpers.AssertTextContains(t, text, "github", "repos", "auth", "success")
		helpers.AssertTextNotContains(t, text, "error", "failed", "leakage")
	})
}

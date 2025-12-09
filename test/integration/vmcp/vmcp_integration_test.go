package vmcp_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
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

// TestVMCPServer_CompositeToolNonStringArguments verifies that composite tools
// correctly pass non-string argument types (integers, booleans, arrays, objects)
// to backend tools. This tests the fix for issue #2921 where Arguments was
// defined as map[string]string instead of map[string]any.
//
//nolint:paralleltest // safe to run in parallel with other tests
func TestVMCPServer_CompositeToolNonStringArguments(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Setup: Create a backend server with a tool that echoes back the received arguments
	// This allows us to verify that non-string types are preserved through the pipeline
	echoArgsServer := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendTool("echo_args", "Echo back received arguments with their types",
			func(_ context.Context, args map[string]any) string {
				// Verify count is a number (JSON unmarshals numbers as float64)
				count, hasCount := args["count"]
				countResult := "missing"
				if hasCount {
					if countFloat, ok := count.(float64); ok && countFloat == 42 {
						countResult = "42"
					} else {
						countResult = "wrong_type_or_value"
					}
				}

				// Verify enabled is a boolean
				enabled, hasEnabled := args["enabled"]
				enabledResult := "missing"
				if hasEnabled {
					if enabledBool, ok := enabled.(bool); ok && enabledBool {
						enabledResult = "true"
					} else {
						enabledResult = "wrong_type_or_value"
					}
				}

				return `{"count": "` + countResult + `", "enabled": "` + enabledResult + `"}`
			}),
	}, helpers.WithBackendName("echo-args-mcp"))
	defer echoArgsServer.Close()

	// Configure backend
	backends := []vmcp.Backend{
		helpers.NewBackend("echoargs",
			helpers.WithURL(echoArgsServer.URL+"/mcp"),
			helpers.WithMetadata("group", "test-group"),
		),
	}

	// Create composite tool with static non-string arguments (no templates, no parameters)
	// The key test is that these non-string values flow through correctly
	workflowDefs := map[string]*composer.WorkflowDefinition{
		"static_args_tool": {
			Name:        "static_args_tool",
			Description: "A composite tool with static non-string arguments",
			Parameters:  map[string]any{}, // No input parameters - all args are static
			Steps: []composer.WorkflowStep{
				{
					ID:   "call_with_static_args",
					Type: "tool",
					Tool: "echoargs_echo_args", // prefixed with backend name
					Arguments: map[string]any{
						"count":   42,
						"enabled": true,
					},
				},
			},
			Timeout: 30 * time.Second,
		},
	}

	// Create vMCP server with composite tool
	vmcpServer := helpers.NewVMCPServer(ctx, t, backends,
		helpers.WithPrefixConflictResolution("{workload}_"),
		helpers.WithWorkflowDefinitions(workflowDefs),
	)

	// Create and initialize MCP client
	vmcpURL := "http://" + vmcpServer.Address() + "/mcp"
	client := helpers.NewMCPClient(ctx, t, vmcpURL)
	defer client.Close()

	// Verify the composite tool is listed

	toolsResp := client.ListTools(ctx)
	toolNames := helpers.GetToolNames(toolsResp)

	assert.Contains(t, toolNames, "static_args_tool", "Should have composite tool")
	assert.Contains(t, toolNames, "echoargs_echo_args", "Should have backend tool")

	// Call composite tool with empty arguments (all args are static in the workflow)
	resp := client.CallTool(ctx, "static_args_tool", map[string]any{})
	text := helpers.AssertToolCallSuccess(t, resp)

	// Verify the backend received the integer and boolean values correctly
	// The handler returns "42" and "true" if the types were correct
	helpers.AssertTextContains(t, text, "count", "42")
	helpers.AssertTextContains(t, text, "enabled", "true")

	// Verify no type conversion errors occurred
	helpers.AssertTextNotContains(t, text, "wrong_type", "missing")
}

// TestVMCPServer_Telemetry_CompositeToolMetrics verifies that vMCP exposes
// Prometheus metrics for composite tool workflow executions and backend requests on /metrics.
// This test creates a composite tool, executes it, and verifies the metrics
// for both the workflow and the backend subtool calls are correctly exposed.
//
//nolint:paralleltest // safe to run in parallel with other tests
func TestVMCPServer_Telemetry_CompositeToolMetrics(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup: Create a synthetic MCP backend server with a simple tool
	echoServer := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendTool("echo", "Echo the input message",
			func(_ context.Context, args map[string]any) string {
				msg, _ := args["message"].(string)
				return `{"echoed": "` + msg + `"}`
			}),
	}, helpers.WithBackendName("echo-mcp"))
	defer echoServer.Close()

	// Configure backend pointing to test server
	backends := []vmcp.Backend{
		helpers.NewBackend("echo",
			helpers.WithURL(echoServer.URL+"/mcp"),
			helpers.WithMetadata("group", "test-group"),
		),
	}

	// Create composite tool workflow definition that calls the echo tool
	workflowDefs := map[string]*composer.WorkflowDefinition{
		"echo_workflow": {
			Name:        "echo_workflow",
			Description: "A composite tool that echoes a message",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{
						"type":        "string",
						"description": "The message to echo",
					},
				},
				"required": []string{"message"},
			},
			Steps: []composer.WorkflowStep{
				{
					ID:   "echo_step",
					Type: "tool",
					Tool: "echo_echo", // prefixed with backend name
					Arguments: map[string]any{
						"message": "{{.params.message}}",
					},
				},
			},
			Timeout: 30 * time.Second,
		},
	}

	// Create telemetry provider with Prometheus enabled
	telemetryConfig := telemetry.Config{
		ServiceName:                 "vmcp-telemetry-test",
		ServiceVersion:              "1.0.0",
		EnablePrometheusMetricsPath: true,
	}
	telemetryProvider, err := telemetry.NewProvider(ctx, telemetryConfig)
	require.NoError(t, err, "failed to create telemetry provider")
	defer telemetryProvider.Shutdown(ctx)

	// Create vMCP server with composite tool and telemetry
	vmcpServer := helpers.NewVMCPServer(ctx, t, backends,
		helpers.WithPrefixConflictResolution("{workload}_"),
		helpers.WithWorkflowDefinitions(workflowDefs),
		helpers.WithTelemetryProvider(telemetryProvider),
	)

	// Create and initialize MCP client
	vmcpURL := "http://" + vmcpServer.Address() + "/mcp"
	client := helpers.NewMCPClient(ctx, t, vmcpURL)
	defer client.Close()

	// Call the composite tool
	resp := client.CallTool(ctx, "echo_workflow", map[string]any{"message": "hello world"})
	text := helpers.AssertToolCallSuccess(t, resp)
	helpers.AssertTextContains(t, text, "echoed", "hello world")

	// Fetch metrics from /metrics endpoint
	metricsURL := "http://" + vmcpServer.Address() + "/metrics"
	httpClient := &http.Client{Timeout: 5 * time.Second}
	metricsResp, err := httpClient.Get(metricsURL)
	require.NoError(t, err, "failed to fetch metrics")
	defer metricsResp.Body.Close()

	require.Equal(t, http.StatusOK, metricsResp.StatusCode, "metrics endpoint should return 200")

	body, err := io.ReadAll(metricsResp.Body)
	require.NoError(t, err, "failed to read metrics body")
	metricsContent := string(body)

	// Log metrics for debugging
	t.Logf("Metrics content:\n%s", metricsContent)

	// Verify workflow execution metrics are present (composite tool).
	assert.True(t, strings.Contains(metricsContent, "toolhive_vmcp_workflow_executions_total"),
		"Should contain workflow executions total metric")
	assert.True(t, strings.Contains(metricsContent, "toolhive_vmcp_workflow_duration_seconds"),
		"Should contain workflow duration metric")
	assert.True(t, strings.Contains(metricsContent, `workflow_name="echo_workflow"`),
		"Should contain workflow name label")

	// Verify backend metrics are present.
	assert.True(t, strings.Contains(metricsContent, "toolhive_vmcp_backend_requests_total"),
		"Should contain backend requests total metric")
	assert.True(t, strings.Contains(metricsContent, "toolhive_vmcp_backend_requests_duration"),
		"Should contain backend requests duration metric")

	// Verify HTTP middleware metrics are present (incoming MCP requests).
	assert.True(t, strings.Contains(metricsContent, "toolhive_mcp_requests_total"),
		"Should contain HTTP middleware requests total metric")
	assert.True(t, strings.Contains(metricsContent, "toolhive_mcp_request_duration_seconds"),
		"Should contain HTTP middleware request duration metric")
}

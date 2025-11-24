package vmcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/test/integration/vmcp/helpers"
)

// composerTestCase defines a declarative test case for composer workflows.
type composerTestCase struct {
	name        string
	tools       []helpers.BackendTool // All tools (will be grouped by backend automatically)
	workflow    *composer.WorkflowDefinition
	params      map[string]any
	wantStrings []string // Strings that must appear in output
	wantError   bool     // Expect workflow to return an error
}

// runComposerTest executes a single composer test case.
func runComposerTest(t *testing.T, tc composerTestCase) {
	t.Helper()
	t.Parallel()

	ctx := context.Background()

	// Create a single backend with all tools
	backend := helpers.CreateBackendServer(t, tc.tools, helpers.WithBackendName("test-backend"))
	defer backend.Close()

	// Create vMCP server
	vmcpServer := helpers.NewVMCPServer(ctx, t, helpers.BackendsFromURL("test", backend.URL+"/mcp"),
		helpers.WithPrefixConflictResolution("{workload}_"),
		helpers.WithWorkflows([]*composer.WorkflowDefinition{tc.workflow}),
	)

	// Create client and execute workflow
	client := helpers.NewMCPClient(ctx, t, "http://"+vmcpServer.Address()+"/mcp")
	defer client.Close()

	result := client.CallTool(ctx, tc.workflow.Name, tc.params)

	// Handle error vs success expectations
	if tc.wantError {
		text := helpers.AssertToolCallError(t, result)
		if len(tc.wantStrings) > 0 {
			helpers.AssertTextContains(t, text, tc.wantStrings...)
		}
		t.Logf("Test %q completed with expected error: %s", tc.name, text)
		return
	}

	text := helpers.AssertToolCallSuccess(t, result)

	// Verify expected strings
	helpers.AssertTextContains(t, text, tc.wantStrings...)

	t.Logf("Test %q completed: %s", tc.name, text)
}

// TestVMCPComposer runs table-driven tests for composer workflow patterns.
// Note: Tests use plain text output instead of structured JSON fields due to
// a known bug where MCP tool results aren't parsed into structured data.
// See: https://github.com/stacklok/toolhive/issues/2669
func TestVMCPComposer(t *testing.T) {
	t.Parallel()

	tests := []composerTestCase{
		{
			name: "SequentialTwoSteps",
			tools: []helpers.BackendTool{
				helpers.NewBackendTool("generate", "Generate message",
					func(_ context.Context, args map[string]any) string {
						prefix, _ := args["prefix"].(string)
						return prefix + " - generated"
					}),
				helpers.NewBackendTool("process", "Process message",
					func(_ context.Context, args map[string]any) string {
						msg, _ := args["message"].(string)
						suffix, _ := args["suffix"].(string)
						return strings.ToUpper(msg) + " " + suffix
					}),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "sequential",
				Description: "Two-step sequential workflow",
				Parameters: map[string]any{
					"prefix": map[string]any{"type": "string"},
					"suffix": map[string]any{"type": "string"},
				},
				FailureMode: "abort",
				Steps: []composer.WorkflowStep{
					{ID: "gen", Type: composer.StepTypeTool, Tool: "test_generate",
						Arguments: map[string]any{"prefix": "{{.params.prefix}}"}},
					{ID: "proc", Type: composer.StepTypeTool, Tool: "test_process",
						Arguments: map[string]any{
							"message": "{{.steps.gen.output.text}}",
							"suffix":  "{{.params.suffix}}",
						},
						DependsOn: []string{"gen"}},
				},
			},
			params:      map[string]any{"prefix": "Hello", "suffix": "[DONE]"},
			wantStrings: []string{"HELLO", "GENERATED", "[DONE]"},
		},
		{
			name: "ParallelFanOut",
			tools: []helpers.BackendTool{
				helpers.NewBackendTool("task_a", "Task A", func(_ context.Context, _ map[string]any) string { return "A" }),
				helpers.NewBackendTool("task_b", "Task B", func(_ context.Context, _ map[string]any) string { return "B" }),
				helpers.NewBackendTool("task_c", "Task C", func(_ context.Context, _ map[string]any) string { return "C" }),
				helpers.NewBackendTool("aggregate", "Aggregate",
					func(_ context.Context, args map[string]any) string {
						a, _ := args["a"].(string)
						b, _ := args["b"].(string)
						c, _ := args["c"].(string)
						return "COMBINED:" + a + "," + b + "," + c
					}),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "fanout",
				Description: "Parallel fan-out workflow",
				Parameters:  map[string]any{},
				FailureMode: "abort",
				Steps: []composer.WorkflowStep{
					{ID: "a", Type: composer.StepTypeTool, Tool: "test_task_a", Arguments: map[string]any{}},
					{ID: "b", Type: composer.StepTypeTool, Tool: "test_task_b", Arguments: map[string]any{}},
					{ID: "c", Type: composer.StepTypeTool, Tool: "test_task_c", Arguments: map[string]any{}},
					{ID: "agg", Type: composer.StepTypeTool, Tool: "test_aggregate",
						Arguments: map[string]any{
							"a": "{{.steps.a.output.text}}",
							"b": "{{.steps.b.output.text}}",
							"c": "{{.steps.c.output.text}}",
						},
						DependsOn: []string{"a", "b", "c"}},
				},
			},
			params:      map[string]any{},
			wantStrings: []string{"COMBINED", "A", "B", "C"},
		},
		{
			name: "DiamondDAG",
			tools: []helpers.BackendTool{
				helpers.NewBackendTool("start", "Start",
					func(_ context.Context, args map[string]any) string {
						input, _ := args["input"].(string)
						return "started:" + input
					}),
				helpers.NewBackendTool("left", "Left branch",
					func(_ context.Context, args map[string]any) string {
						data, _ := args["data"].(string)
						return "L(" + data + ")"
					}),
				helpers.NewBackendTool("right", "Right branch",
					func(_ context.Context, args map[string]any) string {
						data, _ := args["data"].(string)
						return "R(" + data + ")"
					}),
				helpers.NewBackendTool("merge", "Merge",
					func(_ context.Context, args map[string]any) string {
						l, _ := args["left"].(string)
						r, _ := args["right"].(string)
						return "MERGED[" + l + "+" + r + "]"
					}),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "diamond",
				Description: "Diamond-shaped DAG",
				Parameters:  map[string]any{"input": map[string]any{"type": "string"}},
				FailureMode: "abort",
				Steps: []composer.WorkflowStep{
					{ID: "start", Type: composer.StepTypeTool, Tool: "test_start",
						Arguments: map[string]any{"input": "{{.params.input}}"}},
					{ID: "left", Type: composer.StepTypeTool, Tool: "test_left",
						Arguments: map[string]any{"data": "{{.steps.start.output.text}}"},
						DependsOn: []string{"start"}},
					{ID: "right", Type: composer.StepTypeTool, Tool: "test_right",
						Arguments: map[string]any{"data": "{{.steps.start.output.text}}"},
						DependsOn: []string{"start"}},
					{ID: "merge", Type: composer.StepTypeTool, Tool: "test_merge",
						Arguments: map[string]any{
							"left":  "{{.steps.left.output.text}}",
							"right": "{{.steps.right.output.text}}",
						},
						DependsOn: []string{"left", "right"}},
				},
			},
			params:      map[string]any{"input": "test"},
			wantStrings: []string{"MERGED", "L(", "R(", "started:test"},
		},
		{
			name: "ParameterPassing",
			tools: []helpers.BackendTool{
				helpers.NewBackendTool("echo", "Echo params",
					func(_ context.Context, args map[string]any) string {
						name, _ := args["name"].(string)
						count, _ := args["count"].(string)
						return "name=" + name + ",count=" + count
					}),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "params",
				Description: "Parameter passing test",
				Parameters: map[string]any{
					"name":  map[string]any{"type": "string"},
					"count": map[string]any{"type": "string"},
				},
				FailureMode: "abort",
				Steps: []composer.WorkflowStep{
					{ID: "echo", Type: composer.StepTypeTool, Tool: "test_echo",
						Arguments: map[string]any{
							"name":  "{{.params.name}}",
							"count": "{{.params.count}}",
						}},
				},
			},
			params:      map[string]any{"name": "testuser", "count": "42"},
			wantStrings: []string{"name=testuser", "count=42"},
		},
		// Error handling tests
		{
			name: "SingleStepToolError",
			tools: []helpers.BackendTool{
				helpers.NewErrorBackendTool("failing_tool", "Always fails", "TOOL_ERROR: operation failed"),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "single_error",
				Description: "Single step that errors",
				Parameters:  map[string]any{},
				FailureMode: "abort",
				Steps: []composer.WorkflowStep{
					{ID: "fail", Type: composer.StepTypeTool, Tool: "test_failing_tool", Arguments: map[string]any{}},
				},
			},
			params:      map[string]any{},
			wantError:   true,
			wantStrings: []string{"TOOL_ERROR"},
		},
		{
			name: "AbortMode_StopsOnFirstError",
			tools: []helpers.BackendTool{
				helpers.NewErrorBackendTool("error_step", "Fails", "STEP_FAILED"),
				helpers.NewBackendTool("success_step", "Succeeds", func(_ context.Context, _ map[string]any) string { return "SUCCESS" }),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "abort_test",
				Description: "Abort on first error",
				Parameters:  map[string]any{},
				FailureMode: "abort",
				Steps: []composer.WorkflowStep{
					{ID: "error", Type: composer.StepTypeTool, Tool: "test_error_step", Arguments: map[string]any{}},
					{ID: "success", Type: composer.StepTypeTool, Tool: "test_success_step", Arguments: map[string]any{}, DependsOn: []string{"error"}},
				},
			},
			params:      map[string]any{},
			wantError:   true,
			wantStrings: []string{"STEP_FAILED"},
		},
		{
			name: "ContinueMode_PartialSuccess",
			tools: []helpers.BackendTool{
				helpers.NewErrorBackendTool("fail_a", "Fails A", "ERROR_A"),
				helpers.NewBackendTool("ok_b", "OK B", func(_ context.Context, _ map[string]any) string { return "SUCCESS_B" }),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "continue_test",
				Description: "Continue despite errors",
				Parameters:  map[string]any{},
				FailureMode: "continue",
				Steps: []composer.WorkflowStep{
					// Independent parallel steps
					{ID: "a", Type: composer.StepTypeTool, Tool: "test_fail_a", Arguments: map[string]any{}},
					{ID: "b", Type: composer.StepTypeTool, Tool: "test_ok_b", Arguments: map[string]any{}},
				},
			},
			params: map[string]any{},
			// Continue mode returns success with partial results
			wantError:   false,
			wantStrings: []string{"SUCCESS_B"},
		},
		{
			name: "MultipleParallelErrors",
			tools: []helpers.BackendTool{
				helpers.NewErrorBackendTool("fail_1", "Fails 1", "ERROR_1"),
				helpers.NewErrorBackendTool("fail_2", "Fails 2", "ERROR_2"),
				helpers.NewErrorBackendTool("fail_3", "Fails 3", "ERROR_3"),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "multi_error",
				Description: "Multiple parallel failures",
				Parameters:  map[string]any{},
				FailureMode: "abort",
				Steps: []composer.WorkflowStep{
					{ID: "f1", Type: composer.StepTypeTool, Tool: "test_fail_1", Arguments: map[string]any{}},
					{ID: "f2", Type: composer.StepTypeTool, Tool: "test_fail_2", Arguments: map[string]any{}},
					{ID: "f3", Type: composer.StepTypeTool, Tool: "test_fail_3", Arguments: map[string]any{}},
				},
			},
			params:    map[string]any{},
			wantError: true,
			// At least one error message should appear
			wantStrings: []string{"ERROR"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runComposerTest(t, tc)
		})
	}
}

// TestVMCPComposer_WorkflowListed verifies workflow appears in tool list.
func TestVMCPComposer_WorkflowListed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendTool("noop", "No-op", func(_ context.Context, _ map[string]any) string { return "ok" }),
	}, helpers.WithBackendName("test"))
	defer backend.Close()

	workflow := &composer.WorkflowDefinition{
		Name:        "my_workflow",
		Description: "Test workflow",
		Parameters:  map[string]any{},
		FailureMode: "abort",
		Steps: []composer.WorkflowStep{
			{ID: "step1", Type: composer.StepTypeTool, Tool: "test_noop", Arguments: map[string]any{}},
		},
	}

	vmcpServer := helpers.NewVMCPServer(ctx, t, helpers.BackendsFromURL("test", backend.URL+"/mcp"),
		helpers.WithPrefixConflictResolution("{workload}_"),
		helpers.WithWorkflows([]*composer.WorkflowDefinition{workflow}),
	)

	client := helpers.NewMCPClient(ctx, t, "http://"+vmcpServer.Address()+"/mcp")
	defer client.Close()

	toolsResp := client.ListTools(ctx)
	toolNames := helpers.GetToolNames(toolsResp)
	assert.Contains(t, toolNames, "my_workflow", "Workflow should be exposed as a tool")
}

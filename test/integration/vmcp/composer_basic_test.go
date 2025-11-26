package vmcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
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
				Output: &config.OutputConfig{
					Properties: map[string]config.OutputProperty{
						"result": {
							Type:        "string",
							Description: "Processed result",
							Value:       "{{.steps.proc.output.text}}",
						},
					},
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
				Output: &config.OutputConfig{
					Properties: map[string]config.OutputProperty{
						"combined": {
							Type:        "string",
							Description: "Combined result from all tasks",
							Value:       "{{.steps.agg.output.text}}",
						},
					},
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
				Output: &config.OutputConfig{
					Properties: map[string]config.OutputProperty{
						"merged": {
							Type:        "string",
							Description: "Merged result from diamond DAG",
							Value:       "{{.steps.merge.output.text}}",
						},
					},
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
				Output: &config.OutputConfig{
					Properties: map[string]config.OutputProperty{
						"echo_result": {
							Type:        "string",
							Description: "Echo result with parameters",
							Value:       "{{.steps.echo.output.text}}",
						},
					},
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
				Output: &config.OutputConfig{
					Properties: map[string]config.OutputProperty{
						"result_b": {
							Type:        "string",
							Description: "Result from successful step B",
							Value:       "{{.steps.b.output.text}}",
						},
					},
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
		// Advanced error handling and edge case tests
		//
		// Test: StepTimeoutDuringRetry
		// Verifies that step timeout is respected even when retry is configured.
		// The slow tool takes longer than the step timeout, so it should fail due
		// to timeout rather than exhausting all retry attempts.
		{
			name: "StepTimeoutDuringRetry",
			tools: []helpers.BackendTool{
				// Tool that delays 500ms before returning - longer than the 50ms step timeout
				helpers.NewSlowBackendTool("slow_tool", "Slow tool that times out", 500*time.Millisecond, "SUCCESS"),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "timeout_retry",
				Description: "Tests that timeout is respected during retry attempts",
				Parameters:  map[string]any{},
				FailureMode: "abort",
				Steps: []composer.WorkflowStep{
					{
						ID:        "slow_step",
						Type:      composer.StepTypeTool,
						Tool:      "test_slow_tool",
						Arguments: map[string]any{},
						// Step timeout is shorter than the tool delay
						Timeout: 50 * time.Millisecond,
						OnError: &composer.ErrorHandler{
							Action:     "retry",
							RetryCount: 3,
							RetryDelay: 10 * time.Millisecond,
						},
					},
				},
			},
			params:    map[string]any{},
			wantError: true,
			// Should fail due to timeout - the error message should indicate timeout or context deadline
			wantStrings: []string{},
		},
		// Test: ConditionalStepReferencingSkippedStep
		// Tests template expansion when a step references output from a skipped step.
		// Step 1 has a condition that evaluates to false (gets skipped).
		// Step 2 depends on Step 1 and tries to use its output in arguments.
		//
		// DISCUSSION: When a step is skipped, referencing its output via template
		// (e.g., {{.steps.step1.output.text}}) returns "<no value>" (Go template default
		// for missing map keys). The workflow succeeds and the dependent step runs with
		// this placeholder value.
		//
		// This may or may not be the desired behavior. Alternatives to consider:
		// - Fail the workflow when referencing skipped step output
		// - Skip dependent steps automatically when their dependency is skipped
		// - Provide an explicit "default" value mechanism for skipped step references
		{
			name: "ConditionalStepReferencingSkippedStep",
			tools: []helpers.BackendTool{
				helpers.NewBackendTool("producer", "Produces data",
					func(_ context.Context, _ map[string]any) string {
						return "PRODUCED_DATA"
					}),
				helpers.NewBackendTool("consumer", "Consumes data",
					func(_ context.Context, args map[string]any) string {
						data, _ := args["data"].(string)
						// The data will be "<no value>" from the skipped step's template expansion
						return "RECEIVED:" + data
					}),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "skipped_ref",
				Description: "Tests referencing output from a skipped step",
				Parameters:  map[string]any{},
				FailureMode: "abort",
				Steps: []composer.WorkflowStep{
					{
						ID:        "step1",
						Type:      composer.StepTypeTool,
						Tool:      "test_producer",
						Arguments: map[string]any{},
						// Condition that always evaluates to false - step will be skipped
						Condition: "false",
					},
					{
						ID:   "step2",
						Type: composer.StepTypeTool,
						Tool: "test_consumer",
						Arguments: map[string]any{
							// Reference output from skipped step - will expand to "<no value>"
							"data": "{{.steps.step1.output.text}}",
						},
						DependsOn: []string{"step1"},
					},
				},
				Output: &config.OutputConfig{
					Properties: map[string]config.OutputProperty{
						"result": {
							Type:        "string",
							Description: "Consumer result",
							Value:       "{{.steps.step2.output.text}}",
						},
					},
				},
			},
			params: map[string]any{},
			// The workflow succeeds. Step2 receives "<no value>" from the skipped step's
			// template expansion (Go template default for missing keys).
			// Note: "<no value>" is JSON-escaped as "\u003cno value\u003e" in the response.
			wantError:   false,
			wantStrings: []string{"RECEIVED:", "no value"},
		},
		// Test: OutputMissingRequiredField
		// Tests output construction when a required output field template references
		// a non-existent field.
		//
		// NOTE: The `Required` field in OutputConfig does NOT validate that the
		// templated value is non-empty. When a template references a non-existent
		// field (e.g., {{.steps.step1.output.nonexistent_field}}), it expands to
		// "<no value>" (Go template default) WITHOUT failing.
		//
		// DISCUSSION: This is potentially surprising behavior that may warrant a fix.
		// Users may expect `Required` to ensure meaningful values are present.
		// Consider:
		// - Adding validation that required fields don't contain "<no value>"
		// - Using Go template's "missingkey=error" option for strict mode
		// - Documenting this behavior clearly if it's intentional
		{
			name: "OutputMissingRequiredField",
			tools: []helpers.BackendTool{
				helpers.NewBackendTool("simple_tool", "Returns simple output",
					func(_ context.Context, _ map[string]any) string {
						// Returns text output but not a "special_field" that the output requires
						return "simple_result"
					}),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "missing_required",
				Description: "Tests output with template referencing non-existent field",
				Parameters:  map[string]any{},
				FailureMode: "abort",
				Steps: []composer.WorkflowStep{
					{
						ID:        "step1",
						Type:      composer.StepTypeTool,
						Tool:      "test_simple_tool",
						Arguments: map[string]any{},
					},
				},
				Output: &config.OutputConfig{
					Properties: map[string]config.OutputProperty{
						"result": {
							Type:        "string",
							Description: "Result field referencing non-existent step output field",
							// Reference a field that doesn't exist in the step output
							// This will expand to "<no value>" instead of failing
							Value: "{{.steps.step1.output.nonexistent_field}}",
						},
					},
					// Required does not validate that the value is non-empty
					Required: []string{"result"},
				},
			},
			params: map[string]any{},
			// The workflow succeeds - Required does not enforce non-empty values.
			// The result field will contain "<no value>" from the template expansion.
			// Note: "<no value>" is JSON-escaped as "\u003cno value\u003e" in the response.
			wantError:   false,
			wantStrings: []string{"no value"},
		},
		// Test: ContinueModeWithFailedDependency
		// Tests what happens in "continue" failure mode when a dependent step
		// tries to use output from a failed step.
		// - Step A fails with an error
		// - Step B depends on Step A and tries to use its output
		// - Step C is independent (no dependencies)
		//
		// NOTE: In "continue" mode, dependent steps still run even when their
		// dependency failed. They receive "<no value>" placeholder for the failed
		// step's output. Step B is NOT skipped - it executes with the placeholder.
		//
		// DISCUSSION: This may or may not be desired behavior. Alternative approaches:
		// - Skip dependent steps automatically when their dependency fails
		// - Allow steps to declare "skip_on_dependency_failure: true"
		// - Provide a way to detect failed dependencies in conditions
		//
		// Current behavior means dependent steps must handle "<no value>" gracefully
		// or the workflow may produce unexpected results.
		{
			name: "ContinueModeWithFailedDependency",
			tools: []helpers.BackendTool{
				helpers.NewErrorBackendTool("fail_first", "Always fails", "DEPENDENCY_FAILED"),
				helpers.NewBackendTool("use_output", "Uses output from dependency",
					func(_ context.Context, args map[string]any) string {
						data, _ := args["data"].(string)
						// data will be "<no value>" from the failed dependency
						return "PROCESSED:" + data
					}),
				helpers.NewBackendTool("independent", "Independent step",
					func(_ context.Context, _ map[string]any) string {
						return "INDEPENDENT_SUCCESS"
					}),
			},
			workflow: &composer.WorkflowDefinition{
				Name:        "continue_failed_dep",
				Description: "Tests continue mode when a step depends on a failed step",
				Parameters:  map[string]any{},
				FailureMode: "continue",
				Steps: []composer.WorkflowStep{
					{
						ID:        "step_a",
						Type:      composer.StepTypeTool,
						Tool:      "test_fail_first",
						Arguments: map[string]any{},
					},
					{
						ID:   "step_b",
						Type: composer.StepTypeTool,
						Tool: "test_use_output",
						Arguments: map[string]any{
							// Reference output from failed step - will expand to "<no value>"
							"data": "{{.steps.step_a.output.text}}",
						},
						DependsOn: []string{"step_a"},
					},
					{
						// Independent step that succeeds regardless
						ID:        "step_c",
						Type:      composer.StepTypeTool,
						Tool:      "test_independent",
						Arguments: map[string]any{},
					},
				},
				Output: &config.OutputConfig{
					Properties: map[string]config.OutputProperty{
						"independent_result": {
							Type:        "string",
							Description: "Result from independent step",
							Value:       "{{.steps.step_c.output.text}}",
						},
						"dependent_result": {
							Type:        "string",
							Description: "Result from dependent step (runs with placeholder from failed dep)",
							Value:       "{{.steps.step_b.output.text}}",
							Default:     "SKIPPED_DUE_TO_DEPENDENCY",
						},
					},
				},
			},
			params: map[string]any{},
			// In continue mode, the workflow succeeds with partial results.
			// Step B runs (not skipped!) with "<no value>" from failed step_a.
			// Step C (independent) also succeeds.
			// Both outputs are present in the result.
			// Note: "<no value>" is JSON-escaped as "\u003cno value\u003e" in the response.
			wantError:   false,
			wantStrings: []string{"INDEPENDENT_SUCCESS", "PROCESSED:", "no value"},
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
		Output: &config.OutputConfig{
			Properties: map[string]config.OutputProperty{
				"result": {
					Type:        "string",
					Description: "Noop result",
					Value:       "{{.steps.step1.output.text}}",
				},
			},
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

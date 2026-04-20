// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package composer

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestForEachStep_BasicIteration(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	packages := []any{
		map[string]any{"name": "openssl", "version": "1.1.1"},
		map[string]any{"name": "curl", "version": "7.80"},
		map[string]any{"name": "zlib", "version": "1.2.11"},
	}
	packagesJSON, err := json.Marshal(packages)
	require.NoError(t, err)

	// Expect 3 tool calls, one per package
	for _, pkg := range packages {
		p := pkg.(map[string]any)
		te.expectToolCall("osv.query_vulnerability",
			map[string]any{"package_name": p["name"]},
			map[string]any{"vulns": []any{}},
		)
	}

	def := simpleWorkflow("test-foreach",
		toolStep("get_packages", "oci.get_image_config",
			map[string]any{"image": "test:latest"},
		),
		WorkflowStep{
			ID:         "check_vulns",
			Type:       StepTypeForEach,
			Collection: "{{json .steps.get_packages.output.packages}}",
			ItemVar:    "pkg",
			InnerStep: &WorkflowStep{
				ID:   "inner",
				Type: StepTypeTool,
				Tool: "osv.query_vulnerability",
				Arguments: map[string]any{
					"package_name": "{{.forEach.pkg.name}}",
				},
			},
			DependsOn: []string{"get_packages"},
		},
	)

	te.expectToolCall("oci.get_image_config",
		map[string]any{"image": "test:latest"},
		map[string]any{"packages": json.RawMessage(packagesJSON)},
	)

	result, err := execute(t, te.Engine, def, map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify forEach output structure
	forEachResult, exists := result.Steps["check_vulns"]
	require.True(t, exists)
	assert.Equal(t, StepStatusCompleted, forEachResult.Status)

	output := forEachResult.Output
	assert.Equal(t, 3, output["count"])
	assert.Equal(t, 3, output["completed"])
	assert.Equal(t, 0, output["failed"])

	iterations, ok := output["iterations"].([]any)
	require.True(t, ok)
	assert.Len(t, iterations, 3)
}

func TestForEachStep_EmptyCollection(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	te.expectToolCall("oci.get_image_config",
		map[string]any{"image": "empty:latest"},
		map[string]any{"packages": json.RawMessage(`[]`)},
	)

	def := simpleWorkflow("test-foreach-empty",
		toolStep("get_packages", "oci.get_image_config",
			map[string]any{"image": "empty:latest"},
		),
		WorkflowStep{
			ID:         "check_vulns",
			Type:       StepTypeForEach,
			Collection: "{{json .steps.get_packages.output.packages}}",
			InnerStep: &WorkflowStep{
				ID:   "inner",
				Type: StepTypeTool,
				Tool: "osv.query_vulnerability",
				Arguments: map[string]any{
					"package_name": "{{.forEach.item.name}}",
				},
			},
			DependsOn: []string{"get_packages"},
		},
	)

	result, err := execute(t, te.Engine, def, map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	forEachResult := result.Steps["check_vulns"]
	require.NotNil(t, forEachResult)
	assert.Equal(t, 0, forEachResult.Output["count"])
	assert.Equal(t, 0, forEachResult.Output["completed"])

	iterations, ok := forEachResult.Output["iterations"].([]any)
	require.True(t, ok)
	assert.Empty(t, iterations)
}

func TestForEachStep_ErrorAbort(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	// First item will be set up, the tool call for the failing item uses AnyArgs
	// because with abort mode, we can't guarantee ordering with parallelism
	te.expectToolCallWithAnyArgsAndError("osv.query_vulnerability", fmt.Errorf("network error"))

	te.expectToolCall("oci.get_image_config",
		map[string]any{"image": "test:latest"},
		map[string]any{"packages": json.RawMessage(`[{"name":"openssl"}]`)},
	)

	def := simpleWorkflow("test-foreach-abort",
		toolStep("get_packages", "oci.get_image_config",
			map[string]any{"image": "test:latest"},
		),
		WorkflowStep{
			ID:            "check_vulns",
			Type:          StepTypeForEach,
			Collection:    "{{json .steps.get_packages.output.packages}}",
			MaxIterations: 10,
			InnerStep: &WorkflowStep{
				ID:   "inner",
				Type: StepTypeTool,
				Tool: "osv.query_vulnerability",
				Arguments: map[string]any{
					"package_name": "{{.forEach.item.name}}",
				},
			},
			DependsOn: []string{"get_packages"},
			// Default onError is abort
		},
	)

	result, err := execute(t, te.Engine, def, map[string]any{})
	require.Error(t, err)
	assert.Equal(t, WorkflowStatusFailed, result.Status)
}

func TestForEachStep_ErrorContinue(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	// Set up: first call fails, second succeeds
	target := &vmcp.BackendTarget{
		WorkloadID: "test-backend",
		BaseURL:    "http://test:8080",
	}
	te.Router.EXPECT().RouteTool(gomock.Any(), "osv.query_vulnerability").
		Return(target, nil).Times(2)

	callCount := int32(0)
	te.Backend.EXPECT().CallTool(gomock.Any(), target, "osv.query_vulnerability", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ interface{}, _ interface{}, _ interface{}, _ map[string]any, _ interface{}) (*vmcp.ToolCallResult, error) {
			n := atomic.AddInt32(&callCount, 1)
			if n == 1 {
				return nil, fmt.Errorf("network error")
			}
			return &vmcp.ToolCallResult{
				StructuredContent: map[string]any{"vulns": []any{}},
			}, nil
		}).Times(2)

	te.expectToolCall("oci.get_image_config",
		map[string]any{"image": "test:latest"},
		map[string]any{"packages": json.RawMessage(`[{"name":"openssl"},{"name":"curl"}]`)},
	)

	def := simpleWorkflow("test-foreach-continue",
		toolStep("get_packages", "oci.get_image_config",
			map[string]any{"image": "test:latest"},
		),
		WorkflowStep{
			ID:          "check_vulns",
			Type:        StepTypeForEach,
			Collection:  "{{json .steps.get_packages.output.packages}}",
			MaxParallel: 1, // sequential for deterministic ordering
			InnerStep: &WorkflowStep{
				ID:   "inner",
				Type: StepTypeTool,
				Tool: "osv.query_vulnerability",
				Arguments: map[string]any{
					"package_name": "{{.forEach.item.name}}",
				},
			},
			OnError: &ErrorHandler{
				Action:          "continue",
				ContinueOnError: true,
			},
			DependsOn: []string{"get_packages"},
		},
	)

	result, err := execute(t, te.Engine, def, map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	forEachResult := result.Steps["check_vulns"]
	require.NotNil(t, forEachResult)
	assert.Equal(t, StepStatusCompleted, forEachResult.Status)
	assert.Equal(t, 2, forEachResult.Output["count"])
	assert.Equal(t, 1, forEachResult.Output["completed"])
	assert.Equal(t, 1, forEachResult.Output["failed"])
}

func TestForEachStep_MaxIterationsExceeded(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	// Build a collection with 6 items but maxIterations = 5
	te.expectToolCall("oci.get_image_config",
		map[string]any{"image": "test:latest"},
		map[string]any{"packages": json.RawMessage(`[1,2,3,4,5,6]`)},
	)

	def := simpleWorkflow("test-foreach-max",
		toolStep("get_packages", "oci.get_image_config",
			map[string]any{"image": "test:latest"},
		),
		WorkflowStep{
			ID:            "check_vulns",
			Type:          StepTypeForEach,
			Collection:    "{{json .steps.get_packages.output.packages}}",
			MaxIterations: 5,
			InnerStep: &WorkflowStep{
				ID:   "inner",
				Type: StepTypeTool,
				Tool: "osv.query_vulnerability",
				Arguments: map[string]any{
					"package_name": "{{.forEach.item}}",
				},
			},
			DependsOn: []string{"get_packages"},
		},
	)

	result, err := execute(t, te.Engine, def, map[string]any{})
	require.Error(t, err)
	assert.Equal(t, WorkflowStatusFailed, result.Status)
	assert.Contains(t, err.Error(), "exceeds maxIterations")
}

func TestForEachStep_DownstreamAccess(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	te.expectToolCall("oci.get_image_config",
		map[string]any{"image": "test:latest"},
		map[string]any{"packages": json.RawMessage(`[{"name":"openssl"}]`)},
	)
	te.expectToolCall("osv.query_vulnerability",
		map[string]any{"package_name": "openssl"},
		map[string]any{"vulns": []any{"CVE-2021-1234"}},
	)

	// Downstream step references forEach output
	te.expectToolCallWithAnyArgs("reporter.summarize",
		map[string]any{"summary": "done"},
	)

	def := simpleWorkflow("test-foreach-downstream",
		toolStep("get_packages", "oci.get_image_config",
			map[string]any{"image": "test:latest"},
		),
		WorkflowStep{
			ID:         "check_vulns",
			Type:       StepTypeForEach,
			Collection: "{{json .steps.get_packages.output.packages}}",
			ItemVar:    "pkg",
			InnerStep: &WorkflowStep{
				ID:   "inner",
				Type: StepTypeTool,
				Tool: "osv.query_vulnerability",
				Arguments: map[string]any{
					"package_name": "{{.forEach.pkg.name}}",
				},
			},
			DependsOn: []string{"get_packages"},
		},
		toolStepWithDeps("summarize", "reporter.summarize",
			map[string]any{
				"total": "{{.steps.check_vulns.output.count}}",
			},
			[]string{"check_vulns"},
		),
	)

	result, err := execute(t, te.Engine, def, map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify the summarize step executed (downstream of forEach)
	_, exists := result.Steps["summarize"]
	assert.True(t, exists)
}

func TestForEachStep_BoundedParallelism(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	items := make([]any, 5)
	for i := range items {
		items[i] = map[string]any{"name": fmt.Sprintf("pkg-%d", i)}
	}
	itemsJSON, err := json.Marshal(items)
	require.NoError(t, err)

	te.expectToolCall("oci.get_image_config",
		map[string]any{"image": "test:latest"},
		map[string]any{"packages": json.RawMessage(itemsJSON)},
	)

	target := &vmcp.BackendTarget{
		WorkloadID: "test-backend",
		BaseURL:    "http://test:8080",
	}
	te.Router.EXPECT().RouteTool(gomock.Any(), "osv.query_vulnerability").
		Return(target, nil).Times(5)
	te.Backend.EXPECT().CallTool(gomock.Any(), target, "osv.query_vulnerability", gomock.Any(), gomock.Any()).
		Return(&vmcp.ToolCallResult{
			StructuredContent: map[string]any{"vulns": []any{}},
		}, nil).Times(5)

	def := simpleWorkflow("test-foreach-parallel",
		toolStep("get_packages", "oci.get_image_config",
			map[string]any{"image": "test:latest"},
		),
		WorkflowStep{
			ID:          "check_vulns",
			Type:        StepTypeForEach,
			Collection:  "{{json .steps.get_packages.output.packages}}",
			MaxParallel: 3,
			InnerStep: &WorkflowStep{
				ID:   "inner",
				Type: StepTypeTool,
				Tool: "osv.query_vulnerability",
				Arguments: map[string]any{
					"package_name": "{{.forEach.item.name}}",
				},
			},
			DependsOn: []string{"get_packages"},
		},
	)

	result, execErr := execute(t, te.Engine, def, map[string]any{})
	require.NoError(t, execErr)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	forEachResult := result.Steps["check_vulns"]
	require.NotNil(t, forEachResult)
	assert.Equal(t, 5, forEachResult.Output["count"])
	assert.Equal(t, 5, forEachResult.Output["completed"])
}

func TestForEachStep_TemplateContext(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	te.expectToolCall("oci.get_image_config",
		map[string]any{"image": "test:latest"},
		map[string]any{"packages": json.RawMessage(`["alpha","beta"]`)},
	)

	// Verify both forEach.item and forEach.index are accessible
	target := &vmcp.BackendTarget{
		WorkloadID: "test-backend",
		BaseURL:    "http://test:8080",
	}
	te.Router.EXPECT().RouteTool(gomock.Any(), "echo.echo").
		Return(target, nil).Times(2)

	// Use a sync.Map to safely capture args from concurrent goroutines
	var capturedArgs sync.Map
	te.Backend.EXPECT().CallTool(gomock.Any(), target, "echo.echo", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ interface{}, _ interface{}, _ interface{}, args map[string]any, _ interface{}) (*vmcp.ToolCallResult, error) {
			// Key by the index value to avoid ordering issues
			capturedArgs.Store(args["index"], args["value"])
			return &vmcp.ToolCallResult{
				StructuredContent: map[string]any{"echo": args},
			}, nil
		}).Times(2)

	def := simpleWorkflow("test-foreach-context",
		toolStep("get_packages", "oci.get_image_config",
			map[string]any{"image": "test:latest"},
		),
		WorkflowStep{
			ID:         "iterate",
			Type:       StepTypeForEach,
			Collection: "{{json .steps.get_packages.output.packages}}",
			InnerStep: &WorkflowStep{
				ID:   "inner",
				Type: StepTypeTool,
				Tool: "echo.echo",
				Arguments: map[string]any{
					"value": "{{.forEach.item}}",
					"index": "{{.forEach.index}}",
				},
			},
			DependsOn: []string{"get_packages"},
		},
	)

	result, err := execute(t, te.Engine, def, map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify template context by checking captured args keyed by index
	val0, ok0 := capturedArgs.Load("0")
	require.True(t, ok0)
	assert.Equal(t, "alpha", val0)

	val1, ok1 := capturedArgs.Load("1")
	require.True(t, ok1)
	assert.Equal(t, "beta", val1)
}

func TestForEachStep_StringCollection(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	// The upstream step returns a JSON string that encodes an array
	te.expectToolCall("data.get_list",
		map[string]any{},
		map[string]any{"items": `["a","b","c"]`},
	)

	te.expectToolCallWithAnyArgs("echo.echo", map[string]any{"echo": "ok"})
	te.expectToolCallWithAnyArgs("echo.echo", map[string]any{"echo": "ok"})
	te.expectToolCallWithAnyArgs("echo.echo", map[string]any{"echo": "ok"})

	def := simpleWorkflow("test-foreach-string",
		toolStep("get_list", "data.get_list", map[string]any{}),
		WorkflowStep{
			ID:         "iterate",
			Type:       StepTypeForEach,
			Collection: "{{.steps.get_list.output.items}}",
			InnerStep: &WorkflowStep{
				ID:   "inner",
				Type: StepTypeTool,
				Tool: "echo.echo",
				Arguments: map[string]any{
					"value": "{{.forEach.item}}",
				},
			},
			DependsOn: []string{"get_list"},
		},
	)

	result, err := execute(t, te.Engine, def, map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Equal(t, 3, result.Steps["iterate"].Output["count"])
}

func TestForEachStep_InvalidCollection(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	te.expectToolCall("data.get_list",
		map[string]any{},
		map[string]any{"items": "not-json"},
	)

	def := simpleWorkflow("test-foreach-invalid",
		toolStep("get_list", "data.get_list", map[string]any{}),
		WorkflowStep{
			ID:         "iterate",
			Type:       StepTypeForEach,
			Collection: "{{.steps.get_list.output.items}}",
			InnerStep: &WorkflowStep{
				ID:   "inner",
				Type: StepTypeTool,
				Tool: "echo.echo",
				Arguments: map[string]any{
					"value": "{{.forEach.item}}",
				},
			},
			DependsOn: []string{"get_list"},
		},
	)

	result, err := execute(t, te.Engine, def, map[string]any{})
	require.Error(t, err)
	assert.Equal(t, WorkflowStatusFailed, result.Status)
	assert.Contains(t, err.Error(), "must resolve to a JSON array")
}

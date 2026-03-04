// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package composer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestTemplateExpander_DepthLimit tests protection against deeply nested structures.
func TestTemplateExpander_DepthLimit(t *testing.T) {
	t.Parallel()

	// Create deeply nested structure exceeding maxTemplateDepth
	deeplyNested := make(map[string]any)
	current := deeplyNested
	for i := 0; i < 150; i++ {
		nested := make(map[string]any)
		current["nested"] = nested
		current = nested
	}
	current["value"] = "{{.params.test}}"

	expander := NewTemplateExpander()
	_, err := expander.Expand(context.Background(), map[string]any{"deep": deeplyNested}, newWorkflowContext(map[string]any{"test": "value"}))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "depth limit exceeded")
}

// TestTemplateExpander_OutputSizeLimit tests protection against large outputs.
func TestTemplateExpander_OutputSizeLimit(t *testing.T) {
	t.Parallel()

	largeString := strings.Repeat("A", 11*1024*1024) // 11 MB (exceeds 10 MB limit)
	expander := NewTemplateExpander()

	_, err := expander.Expand(context.Background(),
		map[string]any{"output": "{{.params.large}}"},
		newWorkflowContext(map[string]any{"large": largeString}))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "template output too large")
}

// TestWorkflowEngine_MaxStepsValidation tests protection against excessive steps.
func TestWorkflowEngine_MaxStepsValidation(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	steps := make([]WorkflowStep, 150) // Exceeds maxWorkflowSteps (100)
	for i := range steps {
		steps[i] = toolStep(fmt.Sprintf("s%d", i), "test.tool", nil)
	}

	err := te.Engine.ValidateWorkflow(context.Background(), &WorkflowDefinition{Name: "test", Steps: steps})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many steps")
}

// TestWorkflowEngine_RetryCountCapping tests that retries are capped at maximum.
func TestWorkflowEngine_RetryCountCapping(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	def := &WorkflowDefinition{
		Name: "retry-test",
		Steps: []WorkflowStep{{
			ID:   "flaky",
			Type: StepTypeTool,
			Tool: "test.tool",
			OnError: &ErrorHandler{
				Action:     "retry",
				RetryCount: 1000, // Should be capped at maxRetryCount (10)
				RetryDelay: 1 * time.Millisecond,
			},
		}},
		Timeout: 5 * time.Second,
	}

	target := &vmcp.BackendTarget{WorkloadID: "test", BaseURL: "http://test:8080"}
	te.Router.EXPECT().RouteTool(gomock.Any(), "test.tool").Return(target, nil)

	callCount := 0
	te.Backend.EXPECT().CallTool(gomock.Any(), target, "test.tool", gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, *vmcp.BackendTarget, string, map[string]any, map[string]any) (*vmcp.ToolCallResult, error) {
			callCount++
			return nil, fmt.Errorf("fail")
		}).MaxTimes(12) // 1 initial + 10 retries max

	result, err := execute(t, te.Engine, def, nil)

	require.Error(t, err)
	assert.Equal(t, maxRetryCount, callCount-1)
	assert.LessOrEqual(t, result.Steps["flaky"].RetryCount, maxRetryCount)
}

// TestTemplateExpander_NoCodeExecution tests that templates cannot execute code.
func TestTemplateExpander_NoCodeExecution(t *testing.T) {
	t.Parallel()

	malicious := []string{
		"{{exec \"rm -rf /\"}}",
		"{{system \"whoami\"}}",
		"{{eval \"code\"}}",
		"{{import \"os\"}}",
		"{{.Execute \"danger\"}}",
	}

	expander := NewTemplateExpander()
	ctx := newWorkflowContext(map[string]any{"test": "value"})

	for _, tmpl := range malicious {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()
			_, err := expander.Expand(context.Background(), map[string]any{"attempt": tmpl}, ctx)
			require.Error(t, err, "malicious template should fail safely")
		})
	}
}

// TestWorkflowEngine_CircularDependencyDetection verifies cycle detection.
func TestWorkflowEngine_CircularDependencyDetection(t *testing.T) {
	t.Parallel()

	cycles := []struct {
		name  string
		steps []WorkflowStep
	}{
		{"A->B->A", []WorkflowStep{
			toolStepWithDeps("A", "t1", nil, []string{"B"}),
			toolStepWithDeps("B", "t2", nil, []string{"A"})}},
		{"A->B->C->A", []WorkflowStep{
			toolStepWithDeps("A", "t1", nil, []string{"C"}),
			toolStepWithDeps("B", "t2", nil, []string{"A"}),
			toolStepWithDeps("C", "t3", nil, []string{"B"})}},
		{"A->A", []WorkflowStep{toolStepWithDeps("A", "t1", nil, []string{"A"})}},
	}

	te := newTestEngine(t)

	for _, tc := range cycles {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := te.Engine.ValidateWorkflow(context.Background(), simpleWorkflow("test", tc.steps...))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "circular dependency")
		})
	}
}

// TestWorkflowContext_ConcurrentAccess tests thread-safety.
func TestWorkflowContext_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	mgr := newWorkflowContextManager()
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(id int) {
			ctx := mgr.CreateContext(map[string]any{"id": id})
			time.Sleep(time.Millisecond)
			retrieved, err := mgr.GetContext(ctx.WorkflowID)
			assert.NoError(t, err)
			assert.Equal(t, ctx.WorkflowID, retrieved.WorkflowID)
			mgr.DeleteContext(ctx.WorkflowID)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestTemplateExpander_SafeFunctions verifies only safe functions are available.
func TestTemplateExpander_SafeFunctions(t *testing.T) {
	t.Parallel()

	safe := map[string]string{
		"json":  `{{json .params.obj}}`,
		"quote": `{{quote .params.str}}`,
	}

	expander := NewTemplateExpander()
	ctx := newWorkflowContext(map[string]any{"obj": map[string]any{"k": "v"}, "str": "test"})

	for name, tmpl := range safe {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := expander.Expand(context.Background(), map[string]any{"data": tmpl}, ctx)
			require.NoError(t, err)
			assert.NotNil(t, result)
		})
	}
}

// TestWorkflowEngine_NoSensitiveDataInErrors tests error sanitization.
func TestWorkflowEngine_NoSensitiveDataInErrors(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	def := simpleWorkflow("auth", toolStep("login", "auth.login", map[string]any{
		"username": "{{.params.username}}",
		"password": "{{.params.password}}",
	}))

	te.expectToolCallWithAnyArgsAndError("auth.login", fmt.Errorf("auth failed"))

	_, err := execute(t, te.Engine, def, map[string]any{
		"username": "admin",
		"password": "supersecret123",
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "supersecret123")
	assert.NotContains(t, err.Error(), "password")
}

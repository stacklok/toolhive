package composer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTemplateExpander_Expand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     map[string]any
		params   map[string]any
		steps    map[string]*StepResult
		expected map[string]any
		wantErr  bool
	}{
		{
			name:     "basic param substitution",
			data:     map[string]any{"title": "Issue: {{.params.title}}"},
			params:   map[string]any{"title": "Test"},
			expected: map[string]any{"title": "Issue: Test"},
		},
		{
			name:   "step output substitution",
			data:   map[string]any{"msg": "Created: {{.steps.create.output.url}}"},
			params: map[string]any{},
			steps: map[string]*StepResult{
				"create": {Status: StepStatusCompleted, Output: map[string]any{"url": "http://example.com"}},
			},
			expected: map[string]any{"msg": "Created: http://example.com"},
		},
		{
			name:     "nested objects",
			data:     map[string]any{"cfg": map[string]any{"repo": "{{.params.repo}}"}},
			params:   map[string]any{"repo": "myrepo"},
			expected: map[string]any{"cfg": map[string]any{"repo": "myrepo"}},
		},
		{
			name:     "arrays",
			data:     map[string]any{"files": []any{"{{.params.f1}}", "{{.params.f2}}"}},
			params:   map[string]any{"f1": "a.go", "f2": "b.go"},
			expected: map[string]any{"files": []any{"a.go", "b.go"}},
		},
		{
			name:     "mixed types",
			data:     map[string]any{"title": "{{.params.title}}", "num": 42, "flag": true},
			params:   map[string]any{"title": "Test"},
			expected: map[string]any{"title": "Test", "num": 42, "flag": true},
		},
		{
			name:     "json function",
			data:     map[string]any{"payload": `{"data": {{json .params.obj}}}`},
			params:   map[string]any{"obj": map[string]any{"key": "value"}},
			expected: map[string]any{"payload": `{"data": {"key":"value"}}`},
		},
		{
			name:    "invalid template",
			data:    map[string]any{"bad": "{{.params.missing"},
			params:  map[string]any{},
			wantErr: true,
		},
		{
			name:     "missing param uses zero value",
			data:     map[string]any{"val": "{{.params.nonexistent}}"},
			params:   map[string]any{},
			expected: map[string]any{"val": "<no value>"},
		},
	}

	expander := NewTemplateExpander()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := newWorkflowContext(tt.params)
			if tt.steps != nil {
				ctx.Steps = tt.steps
			}

			result, err := expander.Expand(context.Background(), tt.data, ctx)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTemplateExpander_EvaluateCondition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		condition string
		params    map[string]any
		steps     map[string]*StepResult
		expected  bool
		wantErr   bool
	}{
		{"", nil, nil, true, false}, // empty = true
		{"true", nil, nil, true, false},
		{"false", nil, nil, false, false},
		{"True", nil, nil, true, false}, // case insensitive
		{"{{if eq .params.enabled true}}true{{else}}false{{end}}", map[string]any{"enabled": true}, nil, true, false},
		{"{{if eq .params.enabled true}}true{{else}}false{{end}}", map[string]any{"enabled": false}, nil, false, false},
		{"{{if eq .steps.s1.status \"completed\"}}true{{else}}false{{end}}", nil,
			map[string]*StepResult{"s1": {Status: StepStatusCompleted}}, true, false},
		{"not_boolean", nil, nil, false, true},
		{"{{.params.missing", nil, nil, false, true},
	}

	expander := NewTemplateExpander()

	for _, tt := range tests {
		t.Run(tt.condition, func(t *testing.T) {
			t.Parallel()

			ctx := newWorkflowContext(tt.params)
			if tt.steps != nil {
				ctx.Steps = tt.steps
			}

			result, err := expander.EvaluateCondition(context.Background(), tt.condition, ctx)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestWorkflowContext_Lifecycle(t *testing.T) {
	t.Parallel()

	ctx := newWorkflowContext(map[string]any{"key": "value"})

	// Start -> Success
	ctx.RecordStepStart("s1")
	assert.Equal(t, StepStatusRunning, ctx.Steps["s1"].Status)

	time.Sleep(10 * time.Millisecond)
	ctx.RecordStepSuccess("s1", map[string]any{"result": "ok"})
	assert.Equal(t, StepStatusCompleted, ctx.Steps["s1"].Status)
	assert.Greater(t, ctx.Steps["s1"].Duration, time.Duration(0))

	// Start -> Failure
	ctx.RecordStepStart("s2")
	ctx.RecordStepFailure("s2", assert.AnError)
	assert.Equal(t, StepStatusFailed, ctx.Steps["s2"].Status)
	assert.True(t, ctx.HasStepFailed("s2"))

	// Skipped
	ctx.RecordStepSkipped("s3")
	assert.Equal(t, StepStatusSkipped, ctx.Steps["s3"].Status)

	// Check completion status
	assert.True(t, ctx.HasStepCompleted("s1"))
	assert.False(t, ctx.HasStepCompleted("s2"))
	assert.False(t, ctx.HasStepCompleted("s3"))
}

func TestWorkflowContext_GetLastStepOutput(t *testing.T) {
	t.Parallel()

	ctx := newWorkflowContext(nil)

	// No completed steps
	assert.Nil(t, ctx.GetLastStepOutput())

	// Add steps with different completion times
	ctx.RecordStepStart("s1")
	time.Sleep(5 * time.Millisecond)
	ctx.RecordStepSuccess("s1", map[string]any{"order": 1})

	time.Sleep(5 * time.Millisecond)
	ctx.RecordStepStart("s2")
	time.Sleep(5 * time.Millisecond)
	ctx.RecordStepSuccess("s2", map[string]any{"order": 2})

	// Should return latest (s2)
	output := ctx.GetLastStepOutput()
	require.NotNil(t, output)
	assert.Equal(t, 2, output["order"])
}

func TestWorkflowContext_Clone(t *testing.T) {
	t.Parallel()

	original := &WorkflowContext{
		WorkflowID: "test",
		Params:     map[string]any{"key": "value"},
		Steps:      map[string]*StepResult{"s1": {StepID: "s1", Status: StepStatusCompleted}},
		Variables:  map[string]any{"var": "val"},
	}

	clone := original.Clone()

	// Verify deep copy
	assert.Equal(t, original.WorkflowID, clone.WorkflowID)
	assert.Equal(t, original.Params, clone.Params)

	// Modify clone - shouldn't affect original
	clone.Params["new"] = "val"
	clone.Steps["s2"] = &StepResult{StepID: "s2"}

	assert.NotEqual(t, original.Params, clone.Params)
	assert.NotEqual(t, len(original.Steps), len(clone.Steps))
}

func TestTemplateExpander_ExpandOutputFormat(t *testing.T) {
	t.Parallel()

	expander := NewTemplateExpander()
	startTime := time.Now().UnixMilli()
	endTime := startTime + 1500 // 1.5 seconds later

	tests := []struct {
		name           string
		template       string
		params         map[string]any
		steps          map[string]*StepResult
		workflowID     string
		expectedFields map[string]any // Fields to check in output
		wantErr        bool
		errContains    string
	}{
		{
			name:     "simple step output aggregation",
			template: `{"logs": {{json .steps.fetch_logs.output}}, "metrics": {{json .steps.fetch_metrics.output}}}`,
			steps: map[string]*StepResult{
				"fetch_logs":    {Status: StepStatusCompleted, Output: map[string]any{"count": 100}},
				"fetch_metrics": {Status: StepStatusCompleted, Output: map[string]any{"cpu": "50%"}},
			},
			expectedFields: map[string]any{
				"logs":    map[string]any{"count": float64(100)}, // JSON unmarshal converts to float64
				"metrics": map[string]any{"cpu": "50%"},
			},
		},
		{
			name: "with workflow metadata",
			template: `{
				"data": {{json .steps.fetch_data.output}},
				"metadata": {
					"workflow_id": "{{.workflow.id}}",
					"duration_ms": {{.workflow.duration_ms}},
					"step_count": {{.workflow.step_count}}
				}
			}`,
			workflowID: "test-wf-123",
			steps: map[string]*StepResult{
				"fetch_data": {Status: StepStatusCompleted, Output: map[string]any{"result": "ok"}},
			},
			expectedFields: map[string]any{
				"data": map[string]any{"result": "ok"},
				"metadata": map[string]any{
					"workflow_id": "test-wf-123",
					"duration_ms": float64(1500),
					"step_count":  float64(1),
				},
			},
		},
		{
			name: "with parameters",
			template: `{
				"incident_id": "{{.params.incident_id}}",
				"data": {{json .steps.fetch.output}}
			}`,
			params: map[string]any{"incident_id": "INC-12345"},
			steps: map[string]*StepResult{
				"fetch": {Status: StepStatusCompleted, Output: map[string]any{"status": "resolved"}},
			},
			expectedFields: map[string]any{
				"incident_id": "INC-12345",
				"data":        map[string]any{"status": "resolved"},
			},
		},
		{
			name: "multi-step with status",
			template: `{
				"results": {
					"step1": {
						"status": "{{.steps.step1.status}}",
						"output": {{json .steps.step1.output}}
					},
					"step2": {
						"status": "{{.steps.step2.status}}",
						"output": {{json .steps.step2.output}}
					}
				}
			}`,
			steps: map[string]*StepResult{
				"step1": {Status: StepStatusCompleted, Output: map[string]any{"a": 1}},
				"step2": {Status: StepStatusCompleted, Output: map[string]any{"b": 2}},
			},
			expectedFields: map[string]any{
				"results": map[string]any{
					"step1": map[string]any{
						"status": "completed",
						"output": map[string]any{"a": float64(1)},
					},
					"step2": map[string]any{
						"status": "completed",
						"output": map[string]any{"b": float64(2)},
					},
				},
			},
		},
		{
			name: "nested data structures",
			template: `{
				"pages": {
					"overview": {{json .steps.fetch_overview.output}},
					"details": {{json .steps.fetch_details.output}}
				},
				"summary": {
					"total_pages": 2,
					"completed_at": {{.workflow.end_time}}
				}
			}`,
			steps: map[string]*StepResult{
				"fetch_overview": {
					Status: StepStatusCompleted,
					Output: map[string]any{"title": "Overview", "content": "..."},
				},
				"fetch_details": {
					Status: StepStatusCompleted,
					Output: map[string]any{"title": "Details", "sections": []any{"intro", "body"}},
				},
			},
			expectedFields: map[string]any{
				"pages": map[string]any{
					"overview": map[string]any{"title": "Overview", "content": "..."},
					"details":  map[string]any{"title": "Details", "sections": []any{"intro", "body"}},
				},
				"summary": map[string]any{
					"total_pages":  float64(2),
					"completed_at": float64(endTime),
				},
			},
		},
		{
			name:        "invalid template syntax",
			template:    `{"data": {{.steps.fetch.output}`,
			wantErr:     true,
			errContains: "invalid output format template",
		},
		{
			name:        "template references missing field",
			template:    `{"data": {{.nonexistent.field}}}`,
			wantErr:     true,
			errContains: "output format must produce valid JSON",
		},
		{
			name:        "non-JSON output",
			template:    `This is not JSON`,
			wantErr:     true,
			errContains: "output format must produce valid JSON",
		},
		{
			name:        "invalid JSON structure",
			template:    `{"unclosed": "bracket"`,
			wantErr:     true,
			errContains: "output format must produce valid JSON",
		},
		{
			name: "empty steps",
			template: `{
				"workflow_id": "{{.workflow.id}}",
				"step_count": {{.workflow.step_count}}
			}`,
			workflowID: "empty-wf",
			steps:      map[string]*StepResult{},
			expectedFields: map[string]any{
				"workflow_id": "empty-wf",
				"step_count":  float64(0),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := newWorkflowContext(tt.params)
			if tt.workflowID != "" {
				ctx.WorkflowID = tt.workflowID
			} else {
				ctx.WorkflowID = "test-workflow"
			}
			if tt.steps != nil {
				ctx.Steps = tt.steps
			}

			result, err := expander.ExpandOutputFormat(context.Background(), tt.template, ctx, startTime, endTime)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)

			// Verify expected fields
			for key, expectedValue := range tt.expectedFields {
				actualValue, exists := result[key]
				require.True(t, exists, "expected key %q not found in result", key)
				assert.Equal(t, expectedValue, actualValue, "mismatch for key %q", key)
			}
		})
	}
}

func TestTemplateExpander_ExpandOutputFormat_SizeLimits(t *testing.T) {
	t.Parallel()

	expander := NewTemplateExpander()
	ctx := newWorkflowContext(nil)
	ctx.WorkflowID = "test"

	// Create a large step output that will exceed 10MB size limit
	// Each entry is ~100 bytes, so we need >100k entries
	largeString := make([]byte, 100)
	for i := range largeString {
		largeString[i] = 'x'
	}

	largeData := make(map[string]any)
	for i := 0; i < 120000; i++ { // 120k entries * ~100 bytes > 10MB
		largeData[fmt.Sprintf("key_%d", i)] = string(largeString)
	}

	ctx.Steps = map[string]*StepResult{
		"large_step": {Status: StepStatusCompleted, Output: largeData},
	}

	template := `{"data": {{json .steps.large_step.output}}}`

	_, err := expander.ExpandOutputFormat(context.Background(), template, ctx, 0, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too large")
}

func TestTemplateExpander_ExpandOutputFormat_ContextCancellation(t *testing.T) {
	t.Parallel()

	expander := NewTemplateExpander()
	ctx := newWorkflowContext(nil)
	ctx.WorkflowID = "test"
	ctx.Steps = map[string]*StepResult{
		"step1": {Status: StepStatusCompleted, Output: map[string]any{"result": "ok"}},
	}

	// Create cancelled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	template := `{"data": {{json .steps.step1.output}}}`

	_, err := expander.ExpandOutputFormat(cancelledCtx, template, ctx, 0, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled")
}

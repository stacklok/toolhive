package composer

import (
	"context"
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
	ctx.RecordStepSkipped("s3", nil)
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

func TestTemplateExpander_WorkflowMetadata(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		data     map[string]any
		workflow *WorkflowMetadata
		expected map[string]any
		wantErr  bool
	}{
		{
			name: "workflow ID",
			data: map[string]any{"id": "{{.workflow.id}}"},
			workflow: &WorkflowMetadata{
				ID:         "wf-123",
				StartTime:  startTime,
				StepCount:  3,
				Status:     WorkflowStatusCompleted,
				DurationMs: 1500,
			},
			expected: map[string]any{"id": "wf-123"},
		},
		{
			name: "workflow duration_ms",
			data: map[string]any{"duration": "{{.workflow.duration_ms}}"},
			workflow: &WorkflowMetadata{
				ID:         "wf-123",
				StartTime:  startTime,
				StepCount:  3,
				Status:     WorkflowStatusCompleted,
				DurationMs: 2500,
			},
			expected: map[string]any{"duration": "2500"},
		},
		{
			name: "workflow step_count",
			data: map[string]any{"steps": "{{.workflow.step_count}}"},
			workflow: &WorkflowMetadata{
				ID:         "wf-123",
				StartTime:  startTime,
				StepCount:  5,
				Status:     WorkflowStatusCompleted,
				DurationMs: 1000,
			},
			expected: map[string]any{"steps": "5"},
		},
		{
			name: "workflow status",
			data: map[string]any{"status": "{{.workflow.status}}"},
			workflow: &WorkflowMetadata{
				ID:         "wf-123",
				StartTime:  startTime,
				StepCount:  3,
				Status:     WorkflowStatusCompleted,
				DurationMs: 1000,
			},
			expected: map[string]any{"status": "completed"},
		},
		{
			name: "workflow start_time",
			data: map[string]any{"started": "{{.workflow.start_time}}"},
			workflow: &WorkflowMetadata{
				ID:         "wf-123",
				StartTime:  startTime,
				StepCount:  3,
				Status:     WorkflowStatusCompleted,
				DurationMs: 1000,
			},
			expected: map[string]any{"started": "2024-01-01T12:00:00Z"},
		},
		{
			name: "combined workflow metadata",
			data: map[string]any{
				"summary": map[string]any{
					"workflow_id": "{{.workflow.id}}",
					"duration_ms": "{{.workflow.duration_ms}}",
					"step_count":  "{{.workflow.step_count}}",
					"status":      "{{.workflow.status}}",
					"started_at":  "{{.workflow.start_time}}",
				},
			},
			workflow: &WorkflowMetadata{
				ID:         "wf-abc",
				StartTime:  startTime,
				StepCount:  7,
				Status:     WorkflowStatusCompleted,
				DurationMs: 3250,
			},
			expected: map[string]any{
				"summary": map[string]any{
					"workflow_id": "wf-abc",
					"duration_ms": "3250",
					"step_count":  "7",
					"status":      "completed",
					"started_at":  "2024-01-01T12:00:00Z",
				},
			},
		},
		{
			name: "workflow metadata with step outputs",
			data: map[string]any{
				"result":      "{{.steps.fetch.output.data}}",
				"workflow_id": "{{.workflow.id}}",
				"step_count":  "{{.workflow.step_count}}",
			},
			workflow: &WorkflowMetadata{
				ID:         "wf-456",
				StartTime:  startTime,
				StepCount:  2,
				Status:     WorkflowStatusCompleted,
				DurationMs: 800,
			},
			expected: map[string]any{
				"result":      "test-data",
				"workflow_id": "wf-456",
				"step_count":  "2",
			},
		},
	}

	expander := NewTemplateExpander()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := &WorkflowContext{
				WorkflowID: tt.workflow.ID,
				Params:     map[string]any{},
				Steps:      map[string]*StepResult{},
				Variables:  map[string]any{},
				Workflow:   tt.workflow,
			}

			// Add test step data for the combined test
			if tt.name == "workflow metadata with step outputs" {
				ctx.Steps = map[string]*StepResult{
					"fetch": {
						StepID: "fetch",
						Status: StepStatusCompleted,
						Output: map[string]any{"data": "test-data"},
					},
				}
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

func TestTemplateExpander_WorkflowMetadataEmpty(t *testing.T) {
	t.Parallel()

	expander := NewTemplateExpander()

	// Test with nil workflow metadata
	ctx := &WorkflowContext{
		WorkflowID: "test",
		Params:     map[string]any{},
		Steps:      map[string]*StepResult{},
		Variables:  map[string]any{},
		Workflow:   nil,
	}

	data := map[string]any{"id": "{{.workflow.id}}"}

	// Should not panic, should return empty/zero value
	result, err := expander.Expand(context.Background(), data, ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"id": "<no value>"}, result)
}

func TestTemplateExpander_FromJsonFunction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     map[string]any
		steps    map[string]*StepResult
		expected map[string]any
		wantErr  bool
	}{
		{
			name: "parse JSON from step output and access field",
			data: map[string]any{"name": `{{(fromJson .steps.fetch.output.text).name}}`},
			steps: map[string]*StepResult{
				"fetch": {
					Status: StepStatusCompleted,
					Output: map[string]any{"text": `{"name": "Alice", "email": "alice@example.com"}`},
				},
			},
			expected: map[string]any{"name": "Alice"},
		},
		{
			name: "parse JSON and access nested field",
			data: map[string]any{"email": `{{(fromJson .steps.fetch.output.text).user.email}}`},
			steps: map[string]*StepResult{
				"fetch": {
					Status: StepStatusCompleted,
					Output: map[string]any{"text": `{"user": {"email": "bob@example.com"}}`},
				},
			},
			expected: map[string]any{"email": "bob@example.com"},
		},
		{
			name: "parse JSON array and use with index",
			data: map[string]any{"first": `{{index (fromJson .steps.fetch.output.text) 0}}`},
			steps: map[string]*StepResult{
				"fetch": {
					Status: StepStatusCompleted,
					Output: map[string]any{"text": `["apple", "banana", "cherry"]`},
				},
			},
			expected: map[string]any{"first": "apple"},
		},
		{
			name: "combine fromJson with json function",
			data: map[string]any{"data": `{{json (fromJson .steps.fetch.output.text)}}`},
			steps: map[string]*StepResult{
				"fetch": {
					Status: StepStatusCompleted,
					Output: map[string]any{"text": `{"key": "value"}`},
				},
			},
			expected: map[string]any{"data": `{"key":"value"}`},
		},
		{
			name: "fromJson with invalid JSON causes error",
			data: map[string]any{"val": `{{(fromJson .steps.fetch.output.text).key}}`},
			steps: map[string]*StepResult{
				"fetch": {
					Status: StepStatusCompleted,
					Output: map[string]any{"text": `not valid json`},
				},
			},
			wantErr: true,
		},
	}

	expander := NewTemplateExpander()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := &WorkflowContext{
				WorkflowID: "test",
				Params:     map[string]any{},
				Steps:      tt.steps,
				Variables:  map[string]any{},
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

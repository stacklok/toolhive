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

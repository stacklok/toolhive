package composer

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestConstructOutputFromConfig(t *testing.T) {
	t.Parallel()

	// Create a minimal workflow engine for testing
	engine := &workflowEngine{
		templateExpander: NewTemplateExpander(),
	}

	tests := []struct {
		name        string
		outputCfg   *config.OutputConfig
		workflowCtx *WorkflowContext
		want        map[string]any
		wantErr     bool
		errMsg      string
	}{
		{
			name: "simple string output",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result",
						Value:       "{{.steps.step1.output.data}}",
					},
				},
			},
			workflowCtx: &WorkflowContext{
				Steps: map[string]*StepResult{
					"step1": {
						Status: StepStatusCompleted,
						Output: map[string]any{"data": "test_value"},
					},
				},
			},
			want: map[string]any{
				"result": "test_value",
			},
			wantErr: false,
		},
		{
			name: "multiple properties with different types",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"name": {
						Type:        "string",
						Description: "Name",
						Value:       "{{.params.name}}",
					},
					"count": {
						Type:        "integer",
						Description: "Count",
						Value:       "{{.steps.step1.output.count}}",
					},
					"success": {
						Type:        "boolean",
						Description: "Success flag",
						Value:       "{{.steps.step1.output.success}}",
					},
				},
			},
			workflowCtx: &WorkflowContext{
				Params: map[string]any{"name": "test"},
				Steps: map[string]*StepResult{
					"step1": {
						Status: StepStatusCompleted,
						Output: map[string]any{
							"count":   "42",
							"success": "true",
						},
					},
				},
			},
			want: map[string]any{
				"name":    "test",
				"count":   int64(42),
				"success": true,
			},
			wantErr: false,
		},
		{
			name: "nested object properties",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"metadata": {
						Type:        "object",
						Description: "Metadata",
						Properties: map[string]config.OutputProperty{
							"version": {
								Type:        "string",
								Description: "Version",
								Value:       "{{.steps.step1.output.version}}",
							},
							"timestamp": {
								Type:        "integer",
								Description: "Timestamp",
								Value:       "{{.steps.step1.output.timestamp}}",
							},
						},
					},
				},
			},
			workflowCtx: &WorkflowContext{
				Steps: map[string]*StepResult{
					"step1": {
						Status: StepStatusCompleted,
						Output: map[string]any{
							"version":   "1.0.0",
							"timestamp": "1234567890",
						},
					},
				},
			},
			want: map[string]any{
				"metadata": map[string]any{
					"version":   "1.0.0",
					"timestamp": int64(1234567890),
				},
			},
			wantErr: false,
		},
		{
			name: "object type with JSON value",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"data": {
						Type:        "object",
						Description: "Data object",
						Value:       `{"name": "test", "count": 42}`,
					},
				},
			},
			workflowCtx: &WorkflowContext{},
			want: map[string]any{
				"data": map[string]any{
					"name":  "test",
					"count": float64(42), // JSON numbers are float64
				},
			},
			wantErr: false,
		},
		{
			name: "default value fallback on template expansion failure",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result",
						Value:       "{{.steps.missing_step.output.data}}",
						Default:     config.RawJSON{Raw: []byte(`"default_value"`)},
					},
				},
			},
			workflowCtx: &WorkflowContext{
				Steps: map[string]*StepResult{},
			},
			want: map[string]any{
				"result": "default_value",
			},
			wantErr: false,
		},
		{
			name: "default value with type coercion",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"count": {
						Type:        "integer",
						Description: "Count",
						Value:       "{{.steps.missing.output.count}}",
						Default:     config.RawJSON{Raw: []byte(`123`)},
					},
					"enabled": {
						Type:        "boolean",
						Description: "Enabled",
						Value:       "{{.steps.missing.output.enabled}}",
						Default:     config.RawJSON{Raw: []byte(`true`)},
					},
				},
			},
			workflowCtx: &WorkflowContext{},
			want: map[string]any{
				"count":   int64(123),
				"enabled": true,
			},
			wantErr: false,
		},
		{
			name: "required field validation",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"optional": {
						Type:        "string",
						Description: "Optional",
						Value:       "value",
					},
				},
				Required: []string{"required_field"},
			},
			workflowCtx: &WorkflowContext{},
			wantErr:     true,
			errMsg:      "required output field",
		},
		{
			name: "missing step reference returns no value placeholder",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result",
						Value:       "{{.steps.missing_step.output.data}}",
					},
				},
			},
			workflowCtx: &WorkflowContext{
				Steps: map[string]*StepResult{},
			},
			// Template expansion returns "<no value>" for missing fields
			want: map[string]any{
				"result": "<no value>",
			},
			wantErr: false,
		},
		{
			name: "invalid JSON for object type",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"data": {
						Type:        "object",
						Description: "Data",
						Value:       "not valid json",
					},
				},
			},
			workflowCtx: &WorkflowContext{},
			wantErr:     true,
			errMsg:      "failed to deserialize JSON",
		},
		{
			name: "empty string value from template",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"message": {
						Type:        "string",
						Description: "Empty message",
						Value:       "{{.steps.step1.output.empty}}",
					},
				},
			},
			workflowCtx: &WorkflowContext{
				Steps: map[string]*StepResult{
					"step1": {
						Status: StepStatusCompleted,
						Output: map[string]any{
							"empty": "",
						},
					},
				},
			},
			want: map[string]any{
				"message": "",
			},
			wantErr: false,
		},
		{
			name: "missing field with no value placeholder and no default",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "Result",
						Value:       "{{.steps.step1.output.nonexistent}}",
					},
				},
			},
			workflowCtx: &WorkflowContext{
				Steps: map[string]*StepResult{
					"step1": {
						Status: StepStatusCompleted,
						Output: map[string]any{
							"data": "value",
						},
					},
				},
			},
			// Without default, <no value> is returned as-is
			want: map[string]any{
				"result": "<no value>",
			},
			wantErr: false,
		},
		{
			name: "missing field with no value placeholder and default",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "Result",
						Value:       "{{.steps.step1.output.nonexistent}}",
						Default:     config.RawJSON{Raw: []byte(`"default_value"`)},
					},
				},
			},
			workflowCtx: &WorkflowContext{
				Steps: map[string]*StepResult{
					"step1": {
						Status: StepStatusCompleted,
						Output: map[string]any{
							"data": "value",
						},
					},
				},
			},
			// With default, the default value should be used instead of <no value>
			want: map[string]any{
				"result": "default_value",
			},
			wantErr: false,
		},
		{
			name: "integer field with no value placeholder and default",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"count": {
						Type:        "integer",
						Description: "Count",
						Value:       "{{.steps.step1.output.missing_count}}",
						Default:     config.RawJSON{Raw: []byte(`42`)},
					},
				},
			},
			workflowCtx: &WorkflowContext{
				Steps: map[string]*StepResult{
					"step1": {
						Status: StepStatusCompleted,
						Output: map[string]any{
							"other": "value",
						},
					},
				},
			},
			want: map[string]any{
				"count": int64(42),
			},
			wantErr: false,
		},
		{
			name: "empty string is different from no value",
			outputCfg: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"value1": {
						Type:        "string",
						Description: "Empty string from backend",
						Value:       "{{.steps.step1.output.empty}}",
						Default:     config.RawJSON{Raw: []byte(`"should_not_be_used"`)},
					},
					"value2": {
						Type:        "string",
						Description: "Missing field",
						Value:       "{{.steps.step1.output.missing}}",
						Default:     config.RawJSON{Raw: []byte(`"should_be_used"`)},
					},
				},
			},
			workflowCtx: &WorkflowContext{
				Steps: map[string]*StepResult{
					"step1": {
						Status: StepStatusCompleted,
						Output: map[string]any{
							"empty": "", // Explicit empty string
						},
					},
				},
			},
			want: map[string]any{
				"value1": "",               // Empty string preserved
				"value2": "should_be_used", // Default used for missing field
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			got, err := engine.constructOutputFromConfig(ctx, tt.outputCfg, tt.workflowCtx)

			if tt.wantErr {
				if err == nil {
					t.Errorf("constructOutputFromConfig() expected error, got nil")
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("constructOutputFromConfig() error = %v, want error containing %q", err, tt.errMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("constructOutputFromConfig() unexpected error = %v", err)
				return
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("constructOutputFromConfig() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCoerceStringToType(t *testing.T) {
	t.Parallel()

	engine := &workflowEngine{}

	tests := []struct {
		name       string
		value      string
		targetType string
		want       any
		wantErr    bool
	}{
		{
			name:       "string to string",
			value:      "test",
			targetType: "string",
			want:       "test",
			wantErr:    false,
		},
		{
			name:       "string to integer",
			value:      "42",
			targetType: "integer",
			want:       int64(42),
			wantErr:    false,
		},
		{
			name:       "invalid string to integer",
			value:      "not_a_number",
			targetType: "integer",
			wantErr:    true,
		},
		{
			name:       "string to number",
			value:      "3.14",
			targetType: "number",
			want:       3.14,
			wantErr:    false,
		},
		{
			name:       "string to boolean (true)",
			value:      "true",
			targetType: "boolean",
			want:       true,
			wantErr:    false,
		},
		{
			name:       "string to boolean (false)",
			value:      "false",
			targetType: "boolean",
			want:       false,
			wantErr:    false,
		},
		{
			name:       "string to boolean (1)",
			value:      "1",
			targetType: "boolean",
			want:       true,
			wantErr:    false,
		},
		{
			name:       "string to boolean (0)",
			value:      "0",
			targetType: "boolean",
			want:       false,
			wantErr:    false,
		},
		{
			name:       "invalid string to boolean",
			value:      "maybe",
			targetType: "boolean",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := engine.coerceStringToType(tt.value, tt.targetType)

			if tt.wantErr {
				if err == nil {
					t.Errorf("coerceStringToType() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("coerceStringToType() unexpected error = %v", err)
				return
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("coerceStringToType() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCoerceDefaultValue(t *testing.T) {
	t.Parallel()

	engine := &workflowEngine{}

	tests := []struct {
		name       string
		defaultVal any
		targetType string
		want       any
		wantErr    bool
	}{
		{
			name:       "nil default",
			defaultVal: nil,
			targetType: "string",
			want:       nil,
			wantErr:    false,
		},
		{
			name:       "string default to string",
			defaultVal: "test",
			targetType: "string",
			want:       "test",
			wantErr:    false,
		},
		{
			name:       "int default to integer",
			defaultVal: 42,
			targetType: "integer",
			want:       int64(42),
			wantErr:    false,
		},
		{
			name:       "string default to integer",
			defaultVal: "123",
			targetType: "integer",
			want:       int64(123),
			wantErr:    false,
		},
		{
			name:       "float64 default to number",
			defaultVal: 3.14,
			targetType: "number",
			want:       3.14,
			wantErr:    false,
		},
		{
			name:       "int default to number",
			defaultVal: 42,
			targetType: "number",
			want:       float64(42),
			wantErr:    false,
		},
		{
			name:       "bool default to boolean",
			defaultVal: true,
			targetType: "boolean",
			want:       true,
			wantErr:    false,
		},
		{
			name:       "string default to boolean",
			defaultVal: "true",
			targetType: "boolean",
			want:       true,
			wantErr:    false,
		},
		{
			name:       "map default to object",
			defaultVal: map[string]any{"key": "value"},
			targetType: "object",
			want:       map[string]any{"key": "value"},
			wantErr:    false,
		},
		{
			name:       "JSON string default to object",
			defaultVal: `{"key": "value"}`,
			targetType: "object",
			want:       map[string]any{"key": "value"},
			wantErr:    false,
		},
		{
			name:       "slice default to array",
			defaultVal: []any{"a", "b", "c"},
			targetType: "array",
			want:       []any{"a", "b", "c"},
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := engine.coerceDefaultValue(tt.defaultVal, tt.targetType)

			if tt.wantErr {
				if err == nil {
					t.Errorf("coerceDefaultValue() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("coerceDefaultValue() unexpected error = %v", err)
				return
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("coerceDefaultValue() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Helper function to check if error contains substring
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Note: Integration tests for full workflow execution with output config
// are covered by the e2e tests. The unit tests above cover the core
// output construction logic in isolation.

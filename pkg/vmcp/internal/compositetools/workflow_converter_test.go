// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package compositetools

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestBuildOutputSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output *config.OutputConfig
		want   map[string]any
	}{
		{
			name:   "nil output config",
			output: nil,
			want:   nil,
		},
		{
			name: "simple string property",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result",
						Value:       "{{.steps.step1.output.data}}",
					},
				},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result": map[string]any{
						"type":        "string",
						"description": "The result",
					},
				},
			},
		},
		{
			name: "multiple properties with different types",
			output: &config.OutputConfig{
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
					"active": {
						Type:        "boolean",
						Description: "Active flag",
						Value:       "{{.steps.step1.output.active}}",
					},
				},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Name",
					},
					"count": map[string]any{
						"type":        "integer",
						"description": "Count",
					},
					"active": map[string]any{
						"type":        "boolean",
						"description": "Active flag",
					},
				},
			},
		},
		{
			name: "nested object properties",
			output: &config.OutputConfig{
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
								Value:       "{{.steps.step1.output.ts}}",
							},
						},
					},
				},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"metadata": map[string]any{
						"type":        "object",
						"description": "Metadata",
						"properties": map[string]any{
							"version": map[string]any{
								"type":        "string",
								"description": "Version",
							},
							"timestamp": map[string]any{
								"type":        "integer",
								"description": "Timestamp",
							},
						},
					},
				},
			},
		},
		{
			name: "with required fields",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"required_field": {
						Type:        "string",
						Description: "Required",
						Value:       "value",
					},
					"optional_field": {
						Type:        "string",
						Description: "Optional",
						Value:       "value",
					},
				},
				Required: []string{"required_field"},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"required_field": map[string]any{
						"type":        "string",
						"description": "Required",
					},
					"optional_field": map[string]any{
						"type":        "string",
						"description": "Optional",
					},
				},
				"required": []string{"required_field"},
			},
		},
		{
			name: "deeply nested structure",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"level1": {
						Type:        "object",
						Description: "Level 1",
						Properties: map[string]config.OutputProperty{
							"level2": {
								Type:        "object",
								Description: "Level 2",
								Properties: map[string]config.OutputProperty{
									"level3": {
										Type:        "string",
										Description: "Level 3",
										Value:       "deep_value",
									},
								},
							},
						},
					},
				},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"level1": map[string]any{
						"type":        "object",
						"description": "Level 1",
						"properties": map[string]any{
							"level2": map[string]any{
								"type":        "object",
								"description": "Level 2",
								"properties": map[string]any{
									"level3": map[string]any{
										"type":        "string",
										"description": "Level 3",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "object with value (not properties)",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"data": {
						Type:        "object",
						Description: "Data object",
						Value:       "{{.steps.step1.output.json_data}}",
					},
				},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"data": map[string]any{
						"type":        "object",
						"description": "Data object",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildOutputSchema(tt.output)

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("buildOutputSchema() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestConvertWorkflowDefsToToolsWithOutputSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		defs         map[string]*composer.WorkflowDefinition
		want         int // number of tools expected
		validateTool func(*testing.T, map[string]*composer.WorkflowDefinition, []any)
	}{
		{
			name: "empty definitions",
			defs: map[string]*composer.WorkflowDefinition{},
			want: 0,
		},
		{
			name: "workflow without output schema",
			defs: map[string]*composer.WorkflowDefinition{
				"test": {
					Name:        "test_workflow",
					Description: "Test workflow",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"param1": map[string]any{
								"type": "string",
							},
						},
					},
					Output: nil,
				},
			},
			want: 1,
			validateTool: func(t *testing.T, _ map[string]*composer.WorkflowDefinition, tools []any) {
				t.Helper()
				if len(tools) != 1 {
					t.Fatalf("expected 1 tool, got %d", len(tools))
				}
				// Tool should not have OutputSchema field set
			},
		},
		{
			name: "workflow with output schema",
			defs: map[string]*composer.WorkflowDefinition{
				"test": {
					Name:        "test_workflow",
					Description: "Test workflow",
					Parameters: map[string]any{
						"type": "object",
					},
					Output: &config.OutputConfig{
						Properties: map[string]config.OutputProperty{
							"result": {
								Type:        "string",
								Description: "Result",
								Value:       "{{.steps.step1.output}}",
							},
						},
					},
				},
			},
			want: 1,
			validateTool: func(t *testing.T, _ map[string]*composer.WorkflowDefinition, tools []any) {
				t.Helper()
				if len(tools) != 1 {
					t.Fatalf("expected 1 tool, got %d", len(tools))
				}
				// Tool should have OutputSchema field set
			},
		},
		{
			name: "multiple workflows",
			defs: map[string]*composer.WorkflowDefinition{
				"workflow1": {
					Name:        "workflow1",
					Description: "First workflow",
					Output: &config.OutputConfig{
						Properties: map[string]config.OutputProperty{
							"result1": {
								Type:        "string",
								Description: "Result 1",
								Value:       "value",
							},
						},
					},
				},
				"workflow2": {
					Name:        "workflow2",
					Description: "Second workflow",
					Output: &config.OutputConfig{
						Properties: map[string]config.OutputProperty{
							"result2": {
								Type:        "integer",
								Description: "Result 2",
								Value:       "42",
							},
						},
					},
				},
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tools := ConvertWorkflowDefsToTools(tt.defs, nil)

			if len(tools) != tt.want {
				t.Errorf("ConvertWorkflowDefsToTools() returned %d tools, want %d", len(tools), tt.want)
			}

			if tt.validateTool != nil {
				// Convert tools to []any for validation function
				toolsAny := make([]any, len(tools))
				for i, tool := range tools {
					toolsAny[i] = tool
				}
				tt.validateTool(t, tt.defs, toolsAny)
			}

			// Verify all tools have required fields
			for _, tool := range tools {
				if tool.Name == "" {
					t.Error("Tool missing name")
				}
				if tool.Description == "" {
					t.Error("Tool missing description")
				}
			}
		})
	}
}

func TestFilterWorkflowDefsForSession(t *testing.T) {
	t.Parallel()

	makeRT := func(toolNames ...string) *vmcp.RoutingTable {
		rt := &vmcp.RoutingTable{Tools: make(map[string]*vmcp.BackendTarget)}
		for _, name := range toolNames {
			rt.Tools[name] = &vmcp.BackendTarget{WorkloadID: name}
		}
		return rt
	}

	tests := []struct {
		name      string
		defs      map[string]*composer.WorkflowDefinition
		rt        *vmcp.RoutingTable
		wantNames []string // workflow names expected in result
	}{
		{
			name:      "empty defs",
			defs:      map[string]*composer.WorkflowDefinition{},
			rt:        makeRT("tool_a"),
			wantNames: []string{},
		},
		{
			name: "all tools accessible",
			defs: map[string]*composer.WorkflowDefinition{
				"wf1": {
					Name:  "wf1",
					Steps: []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "tool_a"}},
				},
			},
			rt:        makeRT("tool_a", "tool_b"),
			wantNames: []string{"wf1"},
		},
		{
			name: "missing tool excludes workflow",
			defs: map[string]*composer.WorkflowDefinition{
				"wf1": {
					Name:  "wf1",
					Steps: []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "tool_a"}},
				},
			},
			rt:        makeRT("tool_b"),
			wantNames: []string{},
		},
		{
			name: "partially accessible: only accessible workflow included",
			defs: map[string]*composer.WorkflowDefinition{
				"wf_ok": {
					Name: "wf_ok",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "tool_a"},
					},
				},
				"wf_restricted": {
					Name: "wf_restricted",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "tool_a"},
						{ID: "s2", Type: composer.StepTypeTool, Tool: "tool_secret"},
					},
				},
			},
			rt:        makeRT("tool_a"),
			wantNames: []string{"wf_ok"},
		},
		{
			name: "elicitation steps do not require routing table entry",
			defs: map[string]*composer.WorkflowDefinition{
				"wf1": {
					Name: "wf1",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeElicitation},
						{ID: "s2", Type: composer.StepTypeTool, Tool: "tool_a"},
					},
				},
			},
			rt:        makeRT("tool_a"),
			wantNames: []string{"wf1"},
		},
		{
			// Composite tool steps use "{workloadID}.{toolName}" convention.
			// With prefix conflict resolution the routing table key is
			// "{workloadID}_echo", but the step still uses "{workloadID}.echo".
			// The filter must resolve via WorkloadID + OriginalCapabilityName.
			name: "dotted step tool resolved via workload ID and original name",
			defs: map[string]*composer.WorkflowDefinition{
				"wf1": {
					Name: "wf1",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "my-backend.echo"},
					},
				},
			},
			rt: func() *vmcp.RoutingTable {
				rt := &vmcp.RoutingTable{Tools: make(map[string]*vmcp.BackendTarget)}
				// Prefix strategy stores "my-backend_echo" as the resolved key.
				rt.Tools["my-backend_echo"] = &vmcp.BackendTarget{
					WorkloadID:             "my-backend",
					OriginalCapabilityName: "echo",
				}
				return rt
			}(),
			wantNames: []string{"wf1"},
		},
		{
			name: "dotted step tool excluded when workload not in session",
			defs: map[string]*composer.WorkflowDefinition{
				"wf1": {
					Name: "wf1",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "restricted-backend.echo"},
					},
				},
			},
			rt: func() *vmcp.RoutingTable {
				rt := &vmcp.RoutingTable{Tools: make(map[string]*vmcp.BackendTarget)}
				rt.Tools["other-backend_echo"] = &vmcp.BackendTarget{
					WorkloadID:             "other-backend",
					OriginalCapabilityName: "echo",
				}
				return rt
			}(),
			wantNames: []string{},
		},
		{
			name: "nil routing table excludes workflows with tool steps",
			defs: map[string]*composer.WorkflowDefinition{
				"wf_tool": {
					Name:  "wf_tool",
					Steps: []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "tool_a"}},
				},
				"wf_elicit_only": {
					Name:  "wf_elicit_only",
					Steps: []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeElicitation}},
				},
			},
			rt:        nil,
			wantNames: []string{"wf_elicit_only"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := FilterWorkflowDefsForSession(tt.defs, tt.rt)

			if len(got) != len(tt.wantNames) {
				t.Errorf("FilterWorkflowDefsForSession() returned %d defs, want %d (%v)",
					len(got), len(tt.wantNames), tt.wantNames)
			}
			for _, name := range tt.wantNames {
				if _, ok := got[name]; !ok {
					t.Errorf("expected workflow %q in result but it was absent", name)
				}
			}
		})
	}
}

func TestDeriveCompositeAnnotations(t *testing.T) {
	t.Parallel()

	trueVal, falseVal := true, false

	tests := []struct {
		name    string
		stepAnn []*vmcp.ToolAnnotations
		want    *vmcp.ToolAnnotations
	}{
		{
			name:    "empty input (no tool steps) yields nil floor",
			stepAnn: nil,
			want:    nil,
		},
		{
			// Fail-closed (issue #6192): ≥1 tool step exists but none declares
			// annotations → conservative floor, NOT nil. An explicit readOnlyHint:true
			// against this floor is dropped by CheckAnnotationContradiction.
			name:    "all nil annotations (tool steps exist, none declare) yields conservative floor",
			stepAnn: []*vmcp.ToolAnnotations{nil, nil},
			want: &vmcp.ToolAnnotations{
				ReadOnlyHint:    &falseVal,
				DestructiveHint: &trueVal,
				OpenWorldHint:   &trueVal,
			},
		},
		{
			name: "all steps read-only",
			stepAnn: []*vmcp.ToolAnnotations{
				{ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
				{ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &trueVal},
			},
			want: &vmcp.ToolAnnotations{
				ReadOnlyHint:    &trueVal,
				DestructiveHint: &falseVal,
				OpenWorldHint:   &trueVal,
			},
		},
		{
			name: "one non-read-only step makes floor not read-only",
			stepAnn: []*vmcp.ToolAnnotations{
				{ReadOnlyHint: &trueVal},
				{ReadOnlyHint: &falseVal},
			},
			want: &vmcp.ToolAnnotations{
				ReadOnlyHint:    &falseVal,
				DestructiveHint: &trueVal, // nil hints taint conservatively
				OpenWorldHint:   &trueVal,
			},
		},
		{
			name: "nil step annotations taint destructive and open-world",
			stepAnn: []*vmcp.ToolAnnotations{
				{ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
				nil,
			},
			want: &vmcp.ToolAnnotations{
				ReadOnlyHint:    &falseVal,
				DestructiveHint: &trueVal,
				OpenWorldHint:   &trueVal,
			},
		},
		{
			name: "destructive OR across steps",
			stepAnn: []*vmcp.ToolAnnotations{
				{ReadOnlyHint: &falseVal, DestructiveHint: &trueVal, OpenWorldHint: &falseVal},
				{ReadOnlyHint: &falseVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
			},
			want: &vmcp.ToolAnnotations{
				ReadOnlyHint:    &falseVal,
				DestructiveHint: &trueVal,
				OpenWorldHint:   &falseVal,
			},
		},
		{
			name: "idempotent hint is never derived",
			stepAnn: []*vmcp.ToolAnnotations{
				{ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal, IdempotentHint: &trueVal},
			},
			want: &vmcp.ToolAnnotations{
				ReadOnlyHint:    &trueVal,
				DestructiveHint: &falseVal,
				OpenWorldHint:   &falseVal,
				IdempotentHint:  nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := DeriveCompositeAnnotations(tt.stepAnn)

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("DeriveCompositeAnnotations() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCheckAnnotationContradiction(t *testing.T) {
	t.Parallel()

	trueVal, falseVal := true, false

	tests := []struct {
		name     string
		explicit *vmcp.ToolAnnotations
		floor    *vmcp.ToolAnnotations
		wantErr  bool
	}{
		{
			name:     "nil explicit",
			explicit: nil,
			floor:    &vmcp.ToolAnnotations{ReadOnlyHint: &trueVal},
			wantErr:  false,
		},
		{
			name:     "nil floor",
			explicit: &vmcp.ToolAnnotations{ReadOnlyHint: &trueVal},
			floor:    nil,
			wantErr:  false,
		},
		{
			name:     "readOnly true against non-read-only floor",
			explicit: &vmcp.ToolAnnotations{ReadOnlyHint: &trueVal},
			floor:    &vmcp.ToolAnnotations{ReadOnlyHint: &falseVal},
			wantErr:  true,
		},
		{
			name:     "readOnly true against unknown read-only floor",
			explicit: &vmcp.ToolAnnotations{ReadOnlyHint: &trueVal},
			floor:    &vmcp.ToolAnnotations{ReadOnlyHint: nil},
			wantErr:  true,
		},
		{
			name:     "destructive false against destructive floor",
			explicit: &vmcp.ToolAnnotations{DestructiveHint: &falseVal},
			floor:    &vmcp.ToolAnnotations{DestructiveHint: &trueVal},
			wantErr:  true,
		},
		{
			name:     "openWorld false against open-world floor",
			explicit: &vmcp.ToolAnnotations{OpenWorldHint: &falseVal},
			floor:    &vmcp.ToolAnnotations{OpenWorldHint: &trueVal},
			wantErr:  true,
		},
		{
			name:     "equal annotations",
			explicit: &vmcp.ToolAnnotations{ReadOnlyHint: &trueVal, DestructiveHint: &falseVal},
			floor:    &vmcp.ToolAnnotations{ReadOnlyHint: &trueVal, DestructiveHint: &falseVal},
			wantErr:  false,
		},
		{
			name:     "more conservative readOnly false when floor is true",
			explicit: &vmcp.ToolAnnotations{ReadOnlyHint: &falseVal},
			floor:    &vmcp.ToolAnnotations{ReadOnlyHint: &trueVal},
			wantErr:  false,
		},
		{
			name:     "more conservative destructive true when floor is false",
			explicit: &vmcp.ToolAnnotations{DestructiveHint: &trueVal},
			floor:    &vmcp.ToolAnnotations{DestructiveHint: &falseVal},
			wantErr:  false,
		},
		{
			name:     "idempotent never contradicts",
			explicit: &vmcp.ToolAnnotations{IdempotentHint: &trueVal},
			floor:    &vmcp.ToolAnnotations{IdempotentHint: &falseVal},
			wantErr:  false,
		},
		{
			// Q1: explicit destructiveHint:false against a floor whose destructiveHint
			// is nil (unknown) is allowed — the floor does not assert the tool is
			// destructive, so the explicit claim does not contradict it.
			name:     "destructiveHint:false against nil-floor destructiveHint (nil-floor-hint guard)",
			explicit: &vmcp.ToolAnnotations{DestructiveHint: &falseVal},
			floor:    &vmcp.ToolAnnotations{DestructiveHint: nil},
			wantErr:  false,
		},
		{
			// Q2: explicit openWorldHint:false against a floor whose openWorldHint is
			// nil (unknown) is allowed for the same reason.
			name:     "openWorldHint:false against nil-floor openWorldHint (nil-floor-hint guard)",
			explicit: &vmcp.ToolAnnotations{OpenWorldHint: &falseVal},
			floor:    &vmcp.ToolAnnotations{OpenWorldHint: nil},
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckAnnotationContradiction(tt.explicit, tt.floor)

			if (err != nil) != tt.wantErr {
				t.Errorf("CheckAnnotationContradiction() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMergeAnnotations(t *testing.T) {
	t.Parallel()

	trueVal, falseVal := true, false

	tests := []struct {
		name     string
		floor    *vmcp.ToolAnnotations
		explicit *vmcp.ToolAnnotations
		want     *vmcp.ToolAnnotations
	}{
		{
			name:     "both nil",
			floor:    nil,
			explicit: nil,
			want:     nil,
		},
		{
			name:     "floor only",
			floor:    &vmcp.ToolAnnotations{ReadOnlyHint: &trueVal},
			explicit: nil,
			want:     &vmcp.ToolAnnotations{ReadOnlyHint: &trueVal},
		},
		{
			name:     "explicit only",
			floor:    nil,
			explicit: &vmcp.ToolAnnotations{Title: "T", IdempotentHint: &trueVal},
			want:     &vmcp.ToolAnnotations{Title: "T", IdempotentHint: &trueVal},
		},
		{
			name: "explicit non-nil fields win per hint",
			floor: &vmcp.ToolAnnotations{
				Title:           "Floor",
				ReadOnlyHint:    &trueVal,
				DestructiveHint: &trueVal,
				OpenWorldHint:   &trueVal,
			},
			explicit: &vmcp.ToolAnnotations{
				Title:           "Explicit",
				ReadOnlyHint:    &falseVal,
				IdempotentHint:  &trueVal,
				DestructiveHint: nil, // nil keeps floor
			},
			want: &vmcp.ToolAnnotations{
				Title:           "Explicit",
				ReadOnlyHint:    &falseVal,
				DestructiveHint: &trueVal,
				IdempotentHint:  &trueVal,
				OpenWorldHint:   &trueVal,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := MergeAnnotations(tt.floor, tt.explicit)

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("MergeAnnotations() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestConvertWorkflowDefsToToolsAnnotations(t *testing.T) {
	t.Parallel()

	trueVal, falseVal := true, false

	resolver := func(ann map[string]*vmcp.ToolAnnotations) StepAnnotationResolver {
		return func(stepTool string) *vmcp.ToolAnnotations { return ann[stepTool] }
	}

	tests := []struct {
		name         string
		defs         map[string]*composer.WorkflowDefinition
		stepResolver StepAnnotationResolver
		want         map[string]*vmcp.ToolAnnotations // tool name -> expected annotations (absent = dropped)
	}{
		{
			name: "derives annotations from step tools",
			defs: map[string]*composer.WorkflowDefinition{
				"wf": {
					Name:        "wf",
					Description: "workflow",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "backend.read"},
						{ID: "s2", Type: composer.StepTypeTool, Tool: "backend.list"},
					},
				},
			},
			stepResolver: resolver(map[string]*vmcp.ToolAnnotations{
				"backend.read": {ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
				"backend.list": {ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
			}),
			want: map[string]*vmcp.ToolAnnotations{
				"wf": {ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
			},
		},
		{
			name: "forEach inner step contributes to derivation",
			defs: map[string]*composer.WorkflowDefinition{
				"wf": {
					Name:        "wf",
					Description: "workflow",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "backend.read"},
						{
							ID:   "s2",
							Type: composer.StepTypeForEach,
							InnerStep: &composer.WorkflowStep{
								ID: "inner", Type: composer.StepTypeTool, Tool: "backend.write",
							},
						},
					},
				},
			},
			stepResolver: resolver(map[string]*vmcp.ToolAnnotations{
				"backend.read":  {ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
				"backend.write": {ReadOnlyHint: &falseVal, DestructiveHint: &trueVal, OpenWorldHint: &trueVal},
			}),
			want: map[string]*vmcp.ToolAnnotations{
				"wf": {ReadOnlyHint: &falseVal, DestructiveHint: &trueVal, OpenWorldHint: &trueVal},
			},
		},
		{
			name: "explicit annotations merge over the derived floor",
			defs: map[string]*composer.WorkflowDefinition{
				"wf": {
					Name:        "wf",
					Description: "workflow",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "backend.read"},
					},
					Annotations: &config.ToolAnnotationsOverride{
						Title:          ptr("My Tool"),
						IdempotentHint: &trueVal,
					},
				},
			},
			stepResolver: resolver(map[string]*vmcp.ToolAnnotations{
				"backend.read": {ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
			}),
			want: map[string]*vmcp.ToolAnnotations{
				"wf": {
					Title:           "My Tool",
					ReadOnlyHint:    &trueVal,
					DestructiveHint: &falseVal,
					OpenWorldHint:   &falseVal,
					IdempotentHint:  &trueVal,
				},
			},
		},
		{
			name: "contradicting tool is dropped, others kept",
			defs: map[string]*composer.WorkflowDefinition{
				"wf_bad": {
					Name:        "wf_bad",
					Description: "contradicting workflow",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "backend.write"},
					},
					Annotations: &config.ToolAnnotationsOverride{ReadOnlyHint: &trueVal},
				},
				"wf_ok": {
					Name:        "wf_ok",
					Description: "valid workflow",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "backend.read"},
					},
				},
			},
			stepResolver: resolver(map[string]*vmcp.ToolAnnotations{
				"backend.read":  {ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
				"backend.write": {ReadOnlyHint: &falseVal, DestructiveHint: &trueVal, OpenWorldHint: &trueVal},
			}),
			want: map[string]*vmcp.ToolAnnotations{
				"wf_ok": {ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
			},
		},
		{
			name: "unknown step tool taints the floor",
			defs: map[string]*composer.WorkflowDefinition{
				"wf": {
					Name:        "wf",
					Description: "workflow",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "backend.read"},
						{ID: "s2", Type: composer.StepTypeTool, Tool: "backend.unknown"},
					},
				},
			},
			stepResolver: resolver(map[string]*vmcp.ToolAnnotations{
				"backend.read": {ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
			}),
			want: map[string]*vmcp.ToolAnnotations{
				"wf": {ReadOnlyHint: &falseVal, DestructiveHint: &trueVal, OpenWorldHint: &trueVal},
			},
		},
		{
			name: "nil resolver with no explicit annotations yields no annotations",
			defs: map[string]*composer.WorkflowDefinition{
				"wf": {
					Name:        "wf",
					Description: "workflow",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "backend.read"},
					},
				},
			},
			stepResolver: nil,
			want:         map[string]*vmcp.ToolAnnotations{"wf": nil},
		},
		{
			// Q4: with a nil stepResolver the floor cannot be derived (all steps are
			// unknown), but an explicit annotation that does not tighten safety
			// against the conservative floor still passes through.
			name: "nil resolver with explicit annotations passes explicit through",
			defs: map[string]*composer.WorkflowDefinition{
				"wf": {
					Name:        "wf",
					Description: "workflow",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "backend.read"},
					},
					Annotations: &config.ToolAnnotationsOverride{
						Title:          ptr("Explicit Tool"),
						IdempotentHint: &trueVal,
					},
				},
			},
			stepResolver: nil,
			// Conservative floor (readOnly=false, destructive=true, openWorld=true)
			// merged with the explicit Title + IdempotentHint.
			want: map[string]*vmcp.ToolAnnotations{
				"wf": {
					Title:           "Explicit Tool",
					ReadOnlyHint:    &falseVal,
					DestructiveHint: &trueVal,
					OpenWorldHint:   &trueVal,
					IdempotentHint:  &trueVal,
				},
			},
		},
		{
			// Q3: a forEach step with a nil InnerStep is structurally invalid but must
			// be skipped (no panic) during annotation resolution.
			name: "forEach with nil InnerStep is skipped without panic",
			defs: map[string]*composer.WorkflowDefinition{
				"wf": {
					Name:        "wf",
					Description: "workflow",
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: "backend.read"},
						{ID: "s2", Type: composer.StepTypeForEach, InnerStep: nil},
					},
				},
			},
			stepResolver: resolver(map[string]*vmcp.ToolAnnotations{
				"backend.read": {ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
			}),
			want: map[string]*vmcp.ToolAnnotations{
				"wf": {ReadOnlyHint: &trueVal, DestructiveHint: &falseVal, OpenWorldHint: &falseVal},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tools := ConvertWorkflowDefsToTools(tt.defs, tt.stepResolver)

			if len(tools) != len(tt.want) {
				t.Fatalf("ConvertWorkflowDefsToTools() returned %d tools, want %d", len(tools), len(tt.want))
			}
			for _, tool := range tools {
				wantAnn, ok := tt.want[tool.Name]
				if !ok {
					t.Errorf("unexpected tool %q in result", tool.Name)
					continue
				}
				if diff := cmp.Diff(wantAnn, tool.Annotations); diff != "" {
					t.Errorf("tool %q annotations mismatch (-want +got):\n%s", tool.Name, diff)
				}
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestBuildOutputPropertySchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		prop config.OutputProperty
		want map[string]any
	}{
		{
			name: "simple string property",
			prop: config.OutputProperty{
				Type:        "string",
				Description: "A string",
				Value:       "{{.steps.step1.output}}",
			},
			want: map[string]any{
				"type":        "string",
				"description": "A string",
			},
		},
		{
			name: "integer property",
			prop: config.OutputProperty{
				Type:        "integer",
				Description: "An integer",
				Value:       "{{.steps.step1.output.count}}",
			},
			want: map[string]any{
				"type":        "integer",
				"description": "An integer",
			},
		},
		{
			name: "object with nested properties",
			prop: config.OutputProperty{
				Type:        "object",
				Description: "An object",
				Properties: map[string]config.OutputProperty{
					"field1": {
						Type:        "string",
						Description: "Field 1",
						Value:       "value",
					},
					"field2": {
						Type:        "integer",
						Description: "Field 2",
						Value:       "42",
					},
				},
			},
			want: map[string]any{
				"type":        "object",
				"description": "An object",
				"properties": map[string]any{
					"field1": map[string]any{
						"type":        "string",
						"description": "Field 1",
					},
					"field2": map[string]any{
						"type":        "integer",
						"description": "Field 2",
					},
				},
			},
		},
		{
			name: "array property",
			prop: config.OutputProperty{
				Type:        "array",
				Description: "An array",
				Value:       "{{.steps.step1.output.items}}",
			},
			want: map[string]any{
				"type":        "array",
				"description": "An array",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildOutputPropertySchema(tt.prop)

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("buildOutputPropertySchema() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

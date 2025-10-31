package v1alpha1

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestVirtualMCPCompositeToolDefinitionValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ctd     *VirtualMCPCompositeToolDefinition
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid minimal workflow",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application to production",
					Steps: []WorkflowStep{
						{
							ID:   "step1",
							Type: WorkflowStepTypeToolCall,
							Tool: "kubectl.apply",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing workflow name",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "",
					Description: "Deploy application",
					Steps: []WorkflowStep{
						{
							ID:   "step1",
							Tool: "kubectl.apply",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.name is required",
		},
		{
			name: "missing description",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "",
					Steps: []WorkflowStep{
						{
							ID:   "step1",
							Tool: "kubectl.apply",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.description is required",
		},
		{
			name: "no steps",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application",
					Steps:       []WorkflowStep{},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps must have at least one step",
		},
		{
			name: "duplicate step IDs",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application",
					Steps: []WorkflowStep{
						{
							ID:   "step1",
							Tool: "kubectl.apply",
						},
						{
							ID:   "step1",
							Tool: "kubectl.get",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps[1].id \"step1\" is duplicated",
		},
		{
			name: "valid workflow with parameters",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application with parameters",
					Parameters: map[string]ParameterSpec{
						"environment": {
							Type:        "string",
							Description: "Target environment",
							Required:    true,
						},
						"replicas": {
							Type:        "integer",
							Description: "Number of replicas",
							Default:     "3",
						},
					},
					Steps: []WorkflowStep{
						{
							ID:   "deploy",
							Tool: "kubectl.apply",
							Arguments: map[string]string{
								"namespace": "{{.params.environment}}",
								"replicas":  "{{.params.replicas}}",
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid parameter type",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application",
					Parameters: map[string]ParameterSpec{
						"environment": {
							Type: "invalid_type",
						},
					},
					Steps: []WorkflowStep{
						{
							ID:   "step1",
							Tool: "kubectl.apply",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.parameters[environment].type must be one of: string, integer, number, boolean, array, object",
		},
		{
			name: "missing step ID",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application",
					Steps: []WorkflowStep{
						{
							ID:   "",
							Tool: "kubectl.apply",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps[0].id is required",
		},
		{
			name: "missing tool for tool_call step",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application",
					Steps: []WorkflowStep{
						{
							ID:   "step1",
							Type: WorkflowStepTypeToolCall,
							Tool: "",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps[0].tool is required when type is tool_call",
		},
		{
			name: "invalid tool reference format",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application",
					Steps: []WorkflowStep{
						{
							ID:   "step1",
							Type: WorkflowStepTypeToolCall,
							Tool: "invalid-tool-reference",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps[0].tool must be in format 'workload.tool_name'",
		},
		{
			name: "valid elicitation step",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "interactive_deploy",
					Description: "Deploy with user confirmation",
					Steps: []WorkflowStep{
						{
							ID:      "confirm",
							Type:    WorkflowStepTypeElicitation,
							Message: "Are you sure you want to deploy to production?",
							Schema:  &runtime.RawExtension{Raw: []byte(`{"type": "boolean"}`)},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing message for elicitation step",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "interactive_deploy",
					Description: "Deploy with user confirmation",
					Steps: []WorkflowStep{
						{
							ID:      "confirm",
							Type:    WorkflowStepTypeElicitation,
							Message: "",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps[0].message is required when type is elicitation",
		},
		{
			name: "valid workflow with dependencies",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_and_verify",
					Description: "Deploy and verify application",
					Steps: []WorkflowStep{
						{
							ID:   "deploy",
							Tool: "kubectl.apply",
						},
						{
							ID:        "verify",
							Tool:      "kubectl.get",
							DependsOn: []string{"deploy"},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid dependency reference",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_and_verify",
					Description: "Deploy and verify application",
					Steps: []WorkflowStep{
						{
							ID:        "verify",
							Tool:      "kubectl.get",
							DependsOn: []string{"nonexistent"},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps[0].dependsOn references unknown step \"nonexistent\"",
		},
		{
			name: "dependency cycle",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "cyclic_workflow",
					Description: "Workflow with dependency cycle",
					Steps: []WorkflowStep{
						{
							ID:        "step1",
							Tool:      "tool.a",
							DependsOn: []string{"step2"},
						},
						{
							ID:        "step2",
							Tool:      "tool.b",
							DependsOn: []string{"step1"},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "dependency cycle detected",
		},
		{
			name: "valid error handling with retry",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "resilient_deploy",
					Description: "Deploy with retry logic",
					Steps: []WorkflowStep{
						{
							ID:   "deploy",
							Tool: "kubectl.apply",
							OnError: &ErrorHandling{
								Action:     "retry",
								MaxRetries: 3,
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid error handling - retry without maxRetries",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application",
					Steps: []WorkflowStep{
						{
							ID:   "deploy",
							Tool: "kubectl.apply",
							OnError: &ErrorHandling{
								Action:     "retry",
								MaxRetries: 0,
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps[0].onError.maxRetries must be at least 1 when action is retry",
		},
		{
			name: "invalid error handling action",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application",
					Steps: []WorkflowStep{
						{
							ID:   "deploy",
							Tool: "kubectl.apply",
							OnError: &ErrorHandling{
								Action: "invalid",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps[0].onError.action must be one of: abort, continue, retry",
		},
		{
			name: "valid timeout configuration",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "timed_deploy",
					Description: "Deploy with timeout",
					Timeout:     "5m",
					Steps: []WorkflowStep{
						{
							ID:      "deploy",
							Tool:    "kubectl.apply",
							Timeout: "2m",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid timeout format",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application",
					Timeout:     "invalid",
					Steps: []WorkflowStep{
						{
							ID:   "deploy",
							Tool: "kubectl.apply",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.timeout",
		},
		{
			name: "valid failure mode configuration",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "best_effort_deploy",
					Description: "Deploy with best effort",
					FailureMode: "best_effort",
					Steps: []WorkflowStep{
						{
							ID:   "deploy1",
							Tool: "kubectl.apply",
						},
						{
							ID:   "deploy2",
							Tool: "kubectl.apply",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid failure mode",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application",
					FailureMode: "invalid",
					Steps: []WorkflowStep{
						{
							ID:   "deploy",
							Tool: "kubectl.apply",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.failureMode must be one of: abort, continue, best_effort",
		},
		{
			name: "valid conditional step",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "conditional_deploy",
					Description: "Deploy with condition",
					Steps: []WorkflowStep{
						{
							ID:        "check",
							Tool:      "kubectl.get",
							Condition: "{{.params.production}}",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid template syntax in arguments",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					Name:        "deploy_app",
					Description: "Deploy application",
					Steps: []WorkflowStep{
						{
							ID:   "deploy",
							Tool: "kubectl.apply",
							Arguments: map[string]string{
								"namespace": "{{.params.env",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "invalid template",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.ctd.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("VirtualMCPCompositeToolDefinition.Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil {
				if tt.errMsg != "" {
					// Check if error message contains expected substring
					if !strings.Contains(err.Error(), tt.errMsg) {
						t.Errorf("VirtualMCPCompositeToolDefinition.Validate() error = %v, want error containing %q", err, tt.errMsg)
					}
				}
			}
		})
	}
}

func TestValidateDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		duration string
		wantErr  bool
	}{
		{
			name:     "valid seconds",
			duration: "30s",
			wantErr:  false,
		},
		{
			name:     "valid minutes",
			duration: "5m",
			wantErr:  false,
		},
		{
			name:     "valid hours",
			duration: "1h",
			wantErr:  false,
		},
		{
			name:     "valid milliseconds",
			duration: "500ms",
			wantErr:  false,
		},
		{
			name:     "valid compound",
			duration: "1h30m",
			wantErr:  false,
		},
		{
			name:     "valid decimal",
			duration: "1.5s",
			wantErr:  false,
		},
		{
			name:     "invalid format",
			duration: "invalid",
			wantErr:  true,
		},
		{
			name:     "invalid unit",
			duration: "30x",
			wantErr:  true,
		},
		{
			name:     "empty string",
			duration: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateDuration(tt.duration)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDuration() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsValidToolReference(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		tool      string
		wantValid bool
	}{
		{
			name:      "valid tool reference",
			tool:      "kubectl.apply",
			wantValid: true,
		},
		{
			name:      "valid with underscores",
			tool:      "my_workload.my_tool",
			wantValid: true,
		},
		{
			name:      "valid with hyphens",
			tool:      "my-workload.my-tool",
			wantValid: true,
		},
		{
			name:      "invalid - missing dot",
			tool:      "kubectl-apply",
			wantValid: false,
		},
		{
			name:      "invalid - empty workload",
			tool:      ".apply",
			wantValid: false,
		},
		{
			name:      "invalid - empty tool",
			tool:      "kubectl.",
			wantValid: false,
		},
		{
			name:      "invalid - multiple dots",
			tool:      "kubectl.apply.extra",
			wantValid: false,
		},
		{
			name:      "invalid - no dot",
			tool:      "kubectl",
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			valid := isValidToolReference(tt.tool)
			if valid != tt.wantValid {
				t.Errorf("isValidToolReference() = %v, want %v", valid, tt.wantValid)
			}
		})
	}
}

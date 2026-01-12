package v1alpha1

import (
	"strings"
	"testing"
	"time"

	thvjson "github.com/stacklok/toolhive/pkg/json"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
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
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "Deploy application to production",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "step1",
								Type: WorkflowStepTypeToolCall,
								Tool: "kubectl.apply",
							},
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
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "",
						Description: "Deploy application",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "step1",
								Tool: "kubectl.apply",
							},
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
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "step1",
								Tool: "kubectl.apply",
							},
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
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "Deploy application",
						Steps:       []config.WorkflowStepConfig{},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps must have at least one step",
		},
		{
			name: "duplicate step IDs",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "Deploy application",
						Steps: []config.WorkflowStepConfig{
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
			},
			wantErr: true,
			errMsg:  "spec.steps[1].id \"step1\" is duplicated",
		},
		{
			name: "valid workflow with parameters",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "Deploy application with parameters",
						Parameters: thvjson.NewMap(map[string]any{
							"type": "object",
							"properties": map[string]any{
								"environment": map[string]any{
									"type":        "string",
									"description": "Target environment",
								},
								"replicas": map[string]any{
									"type":        "integer",
									"description": "Number of replicas",
									"default":     3,
								},
							},
							"required": []any{"environment"},
						}),
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "deploy",
								Tool: "kubectl.apply",
								Arguments: thvjson.NewMap(map[string]any{
									"namespace": "{{.params.environment}}",
									"replicas":  "{{.params.replicas}}",
								}),
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
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "Deploy application",
						Parameters: thvjson.NewMap(map[string]any{
							"type": "invalid_type_not_object",
							"properties": map[string]any{
								"environment": map[string]any{"type": "string"},
							},
						}),
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "step1",
								Tool: "kubectl.apply",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.parameters: 'type' must be 'object'",
		},
		{
			name: "missing step ID",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "Deploy application",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "",
								Tool: "kubectl.apply",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps[0].id is required",
		},
		{
			name: "missing tool for tool step",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "Deploy application",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "step1",
								Type: WorkflowStepTypeToolCall,
								Tool: "",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps[0].tool is required when type is tool",
		},
		{
			name: "invalid tool reference format",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "Deploy application",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "step1",
								Type: WorkflowStepTypeToolCall,
								Tool: "invalid-tool-reference",
							},
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
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "interactive_deploy",
						Description: "Deploy with user confirmation",
						Steps: []config.WorkflowStepConfig{
							{
								ID:      "confirm",
								Type:    WorkflowStepTypeElicitation,
								Message: "Are you sure you want to deploy to production?",
								Schema:  thvjson.NewMap(map[string]any{"type": "boolean"}),
							},
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
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "interactive_deploy",
						Description: "Deploy with user confirmation",
						Steps: []config.WorkflowStepConfig{
							{
								ID:      "confirm",
								Type:    WorkflowStepTypeElicitation,
								Message: "",
							},
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
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_and_verify",
						Description: "Deploy and verify application",
						Steps: []config.WorkflowStepConfig{
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
			},
			wantErr: false,
		},
		{
			name: "invalid dependency reference",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_and_verify",
						Description: "Deploy and verify application",
						Steps: []config.WorkflowStepConfig{
							{
								ID:        "verify",
								Tool:      "kubectl.get",
								DependsOn: []string{"nonexistent"},
							},
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
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "cyclic_workflow",
						Description: "Workflow with dependency cycle",
						Steps: []config.WorkflowStepConfig{
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
			},
			wantErr: true,
			errMsg:  "dependency cycle detected",
		},
		{
			name: "valid error handling with retry",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "resilient_deploy",
						Description: "Deploy with retry logic",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "deploy",
								Tool: "kubectl.apply",
								OnError: &config.StepErrorHandling{
									Action:     ErrorActionRetry,
									RetryCount: 3,
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid error handling - retry without retryCount",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "Deploy application",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "deploy",
								Tool: "kubectl.apply",
								OnError: &config.StepErrorHandling{
									Action:     ErrorActionRetry,
									RetryCount: 0,
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.steps[0].onError.retryCount must be at least 1 when action is retry",
		},
		{
			name: "invalid error handling action",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "Deploy application",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "deploy",
								Tool: "kubectl.apply",
								OnError: &config.StepErrorHandling{
									Action: "invalid",
								},
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
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "timed_deploy",
						Description: "Deploy with timeout",
						Timeout:     config.Duration(5 * time.Minute),
						Steps: []config.WorkflowStepConfig{
							{
								ID:      "deploy",
								Tool:    "kubectl.apply",
								Timeout: config.Duration(2 * time.Minute),
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid conditional step",
			ctd: &VirtualMCPCompositeToolDefinition{
				Spec: VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "conditional_deploy",
						Description: "Deploy with condition",
						Steps: []config.WorkflowStepConfig{
							{
								ID:        "check",
								Tool:      "kubectl.get",
								Condition: "{{.params.production}}",
							},
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
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "deploy_app",
						Description: "Deploy application",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "deploy",
								Tool: "kubectl.apply",
								Arguments: thvjson.NewMap(map[string]any{
									"namespace": "{{.params.env",
								}),
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
			valid := config.IsValidToolReference(tt.tool)
			if valid != tt.wantValid {
				t.Errorf("config.IsValidToolReference() = %v, want %v", valid, tt.wantValid)
			}
		})
	}
}

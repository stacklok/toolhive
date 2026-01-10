package v1alpha1

import (
	"testing"
	"time"

	vmcp "github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestVirtualMCPServerValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		vmcp    *VirtualMCPServer
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid minimal configuration",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{Group: "test-group"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing groupRef name",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{Group: ""},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.groupRef is required",
		},
		{
			name: "empty IncomingAuth type",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{Group: "test-group"},
					IncomingAuth: &IncomingAuthConfig{
						Type: "",
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.incomingAuth.type is required",
		},
		{
			name: "OIDC auth without OIDCConfig",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{Group: "test-group"},
					IncomingAuth: &IncomingAuthConfig{
						Type:       "oidc",
						OIDCConfig: nil,
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.incomingAuth.oidcConfig is required when type is oidc",
		},
		{
			name: "valid outgoingAuth with discovered source",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{Group: "test-group"},
					OutgoingAuth: &OutgoingAuthConfig{
						Source: "discovered",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid outgoingAuth source",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{Group: "test-group"},
					OutgoingAuth: &OutgoingAuthConfig{
						Source: "invalid",
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.outgoingAuth.source must be one of: discovered, inline",
		},
		{
			name: "invalid backend auth type",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{Group: "test-group"},
					OutgoingAuth: &OutgoingAuthConfig{
						Backends: map[string]BackendAuthConfig{
							"test-backend": {
								Type: "invalid-type",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.outgoingAuth.backends[test-backend].type must be one of: discovered, external_auth_config_ref",
		},
		{
			name: "valid backend external auth config ref",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{Group: "test-group"},
					OutgoingAuth: &OutgoingAuthConfig{
						Backends: map[string]BackendAuthConfig{
							"test-backend": {
								Type: BackendAuthTypeExternalAuthConfigRef,
								ExternalAuthConfigRef: &ExternalAuthConfigRef{
									Name: "test-auth-config",
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid backend external auth config ref - missing name",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{Group: "test-group"},
					OutgoingAuth: &OutgoingAuthConfig{
						Backends: map[string]BackendAuthConfig{
							"test-backend": {
								Type:                  BackendAuthTypeExternalAuthConfigRef,
								ExternalAuthConfigRef: &ExternalAuthConfigRef{},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.outgoingAuth.backends[test-backend].externalAuthConfigRef.name is required",
		},
		{
			name: "valid aggregation with prefix strategy",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						Aggregation: &config.AggregationConfig{
							ConflictResolution: vmcp.ConflictStrategyPrefix,
							ConflictResolutionConfig: &config.ConflictResolutionConfig{
								PrefixFormat: "{workload}_",
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid aggregation with priority strategy",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						Aggregation: &config.AggregationConfig{
							ConflictResolution: vmcp.ConflictStrategyPriority,
							ConflictResolutionConfig: &config.ConflictResolutionConfig{
								PriorityOrder: []string{"backend1", "backend2"},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid aggregation - priority strategy without priority order",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						Aggregation: &config.AggregationConfig{
							ConflictResolution:       vmcp.ConflictStrategyPriority,
							ConflictResolutionConfig: &config.ConflictResolutionConfig{},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "config.aggregation.conflictResolutionConfig.priorityOrder is required when conflictResolution is priority",
		},
		{
			name: "valid aggregation with tool config ref",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						Aggregation: &config.AggregationConfig{
							Tools: []*config.WorkloadToolConfig{
								{
									Workload: "backend1",
									ToolConfigRef: &config.ToolConfigRef{
										Name: "test-tool-config",
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid aggregation - missing workload name",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						Aggregation: &config.AggregationConfig{
							Tools: []*config.WorkloadToolConfig{
								{
									ToolConfigRef: &config.ToolConfigRef{
										Name: "test-tool-config",
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "config.aggregation.tools[0].workload is required",
		},
		{
			name: "valid composite tool",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:   "step1",
										Type: WorkflowStepTypeToolCall,
										Tool: "backend.tool1",
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid composite tool - missing name",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:   "step1",
										Type: "tool",
										Tool: "backend.tool1",
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].name is required",
		},
		{
			name: "invalid composite tool - missing description",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name: "test-tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:   "step1",
										Tool: "backend.tool1",
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].description is required",
		},
		{
			name: "invalid composite tool - no steps",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps:       []config.WorkflowStepConfig{},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].steps must have at least one step",
		},
		{
			name: "invalid composite tool - duplicate tool names",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool 1",
								Steps: []config.WorkflowStepConfig{
									{ID: "step1", Tool: "backend.tool1"},
								},
							},
							{
								Name:        "test-tool",
								Description: "Test composite tool 2",
								Steps: []config.WorkflowStepConfig{
									{ID: "step1", Tool: "backend.tool1"},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[1].name \"test-tool\" is duplicated",
		},
		{
			name: "invalid composite tool - missing step ID",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{Tool: "backend.tool1"},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].steps[0].id is required",
		},
		{
			name: "invalid composite tool - tool step without tool",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:   "step1",
										Type: WorkflowStepTypeToolCall,
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].steps[0].tool is required when type is tool",
		},
		{
			name: "invalid composite tool - elicitation step without message",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:   "step1",
										Type: WorkflowStepTypeElicitation,
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].steps[0].message is required when type is elicitation",
		},
		{
			name: "invalid composite tool - duplicate step IDs",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{ID: "step1", Tool: "backend.tool1"},
									{ID: "step1", Tool: "backend.tool2"},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].steps[1].id \"step1\" is duplicated",
		},
		{
			name: "invalid composite tool - invalid step type",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:   "step1",
										Type: "invalid-type",
										Tool: "backend.tool",
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].steps[0].type must be tool or elicitation",
		},
		{
			name: "invalid aggregation - invalid conflict resolution strategy",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						Aggregation: &config.AggregationConfig{
							ConflictResolution: "invalid-strategy",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "config.aggregation.conflictResolution must be one of: prefix, priority, manual",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.vmcp.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && err.Error() != tt.errMsg {
				t.Errorf("Validate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestValidateCompositeToolsWithDependencies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		vmcp    *VirtualMCPServer
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid step dependencies",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{ID: "step1", Tool: "backend.tool1"},
									{ID: "step2", Tool: "backend.tool2", DependsOn: []string{"step1"}},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid forward dependencies",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{ID: "step1", Tool: "backend.tool1", DependsOn: []string{"step2"}},
									{ID: "step2", Tool: "backend.tool2"},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid error handling with retry",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:   "step1",
										Tool: "backend.tool1",
										OnError: &config.StepErrorHandling{
											Action:     "retry",
											RetryCount: 3,
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid error handling action",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:   "step1",
										Tool: "backend.tool1",
										OnError: &config.StepErrorHandling{
											Action: "invalid-action",
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].steps[0].onError.action must be one of: abort, continue, retry",
		},
		{
			name: "invalid error handling - retry without retryCount",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:   "step1",
										Tool: "backend.tool1",
										OnError: &config.StepErrorHandling{
											Action:     "retry",
											RetryCount: 0,
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].steps[0].onError.retryCount is required for action retry",
		},
		{
			name: "valid error handling with retryDelay",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:   "step1",
										Tool: "backend.tool1",
										OnError: &config.StepErrorHandling{
											Action:     "retry",
											RetryCount: 3,
											RetryDelay: config.Duration(5 * time.Second),
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid error handling with complex retryDelay",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:   "step1",
										Tool: "backend.tool1",
										OnError: &config.StepErrorHandling{
											Action:     "retry",
											RetryCount: 3,
											RetryDelay: config.Duration(90 * time.Second),
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid composite tool - unknown dependency reference",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:        "step1",
										Tool:      "backend.tool1",
										DependsOn: []string{"unknown-step"},
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].steps[0].dependsOn references unknown step ID \"unknown-step\"",
		},
		{
			name: "valid elicitation with OnDecline skip_remaining",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:      "step1",
										Type:    WorkflowStepTypeElicitation,
										Message: "Please provide input",
										OnDecline: &config.ElicitationResponseConfig{
											Action: "skip_remaining",
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid elicitation with OnDecline abort",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:      "step1",
										Type:    WorkflowStepTypeElicitation,
										Message: "Please provide input",
										OnDecline: &config.ElicitationResponseConfig{
											Action: "abort",
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid elicitation with OnDecline continue",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:      "step1",
										Type:    WorkflowStepTypeElicitation,
										Message: "Please provide input",
										OnDecline: &config.ElicitationResponseConfig{
											Action: "continue",
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid elicitation with OnCancel skip_remaining",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:      "step1",
										Type:    WorkflowStepTypeElicitation,
										Message: "Please provide input",
										OnCancel: &config.ElicitationResponseConfig{
											Action: "skip_remaining",
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid elicitation with OnCancel abort",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:      "step1",
										Type:    WorkflowStepTypeElicitation,
										Message: "Please provide input",
										OnCancel: &config.ElicitationResponseConfig{
											Action: "abort",
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid elicitation with OnCancel continue",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:      "step1",
										Type:    WorkflowStepTypeElicitation,
										Message: "Please provide input",
										OnCancel: &config.ElicitationResponseConfig{
											Action: "continue",
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid elicitation with both OnDecline and OnCancel",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:      "step1",
										Type:    WorkflowStepTypeElicitation,
										Message: "Please provide input",
										OnDecline: &config.ElicitationResponseConfig{
											Action: "skip_remaining",
										},
										OnCancel: &config.ElicitationResponseConfig{
											Action: "abort",
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid elicitation - OnDecline with invalid action",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:      "step1",
										Type:    WorkflowStepTypeElicitation,
										Message: "Please provide input",
										OnDecline: &config.ElicitationResponseConfig{
											Action: "invalid-action",
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].steps[0].onDecline.action must be one of: skip_remaining, abort, continue",
		},
		{
			name: "invalid elicitation - OnCancel with invalid action",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-tool",
								Description: "Test composite tool",
								Steps: []config.WorkflowStepConfig{
									{
										ID:      "step1",
										Type:    WorkflowStepTypeElicitation,
										Message: "Please provide input",
										OnCancel: &config.ElicitationResponseConfig{
											Action: "invalid-action",
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.config.compositeTools[0].steps[0].onCancel.action must be one of: skip_remaining, abort, continue",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.vmcp.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && err.Error() != tt.errMsg {
				t.Errorf("Validate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}

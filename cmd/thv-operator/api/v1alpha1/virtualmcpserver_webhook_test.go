package v1alpha1

import (
	"testing"
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
					GroupRef: GroupRef{
						Name: "test-group",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing groupRef name",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{
						Name: "",
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.groupRef.name is required",
		},
		{
			name: "valid outgoingAuth with discovered source",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
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
					GroupRef: GroupRef{Name: "test-group"},
					OutgoingAuth: &OutgoingAuthConfig{
						Source: "invalid",
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.outgoingAuth.source must be one of: discovered, inline, mixed",
		},
		{
			name: "valid backend external auth config ref",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
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
					GroupRef: GroupRef{Name: "test-group"},
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
					GroupRef: GroupRef{Name: "test-group"},
					Aggregation: &AggregationConfig{
						ConflictResolution: ConflictResolutionPrefix,
						ConflictResolutionConfig: &ConflictResolutionConfig{
							PrefixFormat: "{workload}_",
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
					GroupRef: GroupRef{Name: "test-group"},
					Aggregation: &AggregationConfig{
						ConflictResolution: ConflictResolutionPriority,
						ConflictResolutionConfig: &ConflictResolutionConfig{
							PriorityOrder: []string{"backend1", "backend2"},
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
					GroupRef: GroupRef{Name: "test-group"},
					Aggregation: &AggregationConfig{
						ConflictResolution:       ConflictResolutionPriority,
						ConflictResolutionConfig: &ConflictResolutionConfig{},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.aggregation.conflictResolutionConfig.priorityOrder is required when conflictResolution is priority",
		},
		{
			name: "valid aggregation with tool config ref",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					Aggregation: &AggregationConfig{
						Tools: []WorkloadToolConfig{
							{
								Workload: "backend1",
								ToolConfigRef: &ToolConfigRef{
									Name: "test-tool-config",
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
					GroupRef: GroupRef{Name: "test-group"},
					Aggregation: &AggregationConfig{
						Tools: []WorkloadToolConfig{
							{
								ToolConfigRef: &ToolConfigRef{
									Name: "test-tool-config",
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.aggregation.tools[0].workload is required",
		},
		{
			name: "valid composite tool",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
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
			wantErr: false,
		},
		{
			name: "invalid composite tool - missing name",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{
									ID:   "step1",
									Tool: "backend.tool1",
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.compositeTools[0].name is required",
		},
		{
			name: "invalid composite tool - missing description",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name: "test-tool",
							Steps: []WorkflowStep{
								{
									ID:   "step1",
									Tool: "backend.tool1",
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.compositeTools[0].description is required",
		},
		{
			name: "invalid composite tool - no steps",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps:       []WorkflowStep{},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.compositeTools[0].steps must have at least one step",
		},
		{
			name: "invalid composite tool - duplicate tool names",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool 1",
							Steps: []WorkflowStep{
								{ID: "step1", Tool: "backend.tool1"},
							},
						},
						{
							Name:        "test-tool",
							Description: "Test composite tool 2",
							Steps: []WorkflowStep{
								{ID: "step1", Tool: "backend.tool1"},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.compositeTools[1].name \"test-tool\" is duplicated",
		},
		{
			name: "invalid composite tool - missing step ID",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{Tool: "backend.tool1"},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.compositeTools[0].steps[0].id is required",
		},
		{
			name: "invalid composite tool - tool step without tool",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{
									ID:   "step1",
									Type: WorkflowStepTypeToolCall,
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.compositeTools[0].steps[0].tool is required when type is tool",
		},
		{
			name: "invalid composite tool - elicitation step without message",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{
									ID:   "step1",
									Type: WorkflowStepTypeElicitation,
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.compositeTools[0].steps[0].message is required when type is elicitation",
		},
		{
			name: "invalid composite tool - duplicate step IDs",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{ID: "step1", Tool: "backend.tool1"},
								{ID: "step1", Tool: "backend.tool2"},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.compositeTools[0].steps[1].id \"step1\" is duplicated",
		},
		{
			name: "valid token cache - memory",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					TokenCache: &TokenCacheConfig{
						Provider: "memory",
						Memory: &MemoryCacheConfig{
							MaxEntries: 1000,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid token cache - redis with password",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					TokenCache: &TokenCacheConfig{
						Provider: "redis",
						Redis: &RedisCacheConfig{
							Address: "redis:6379",
							PasswordRef: &SecretKeyRef{
								Name: "redis-secret",
								Key:  "password",
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid token cache - redis without address",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					TokenCache: &TokenCacheConfig{
						Provider: "redis",
						Redis:    &RedisCacheConfig{},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.tokenCache.redis.address is required",
		},
		{
			name: "invalid token cache - invalid provider",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					TokenCache: &TokenCacheConfig{
						Provider: "invalid",
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.tokenCache.provider must be memory or redis",
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
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{ID: "step1", Tool: "backend.tool1"},
								{ID: "step2", Tool: "backend.tool2", DependsOn: []string{"step1"}},
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
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{ID: "step1", Tool: "backend.tool1", DependsOn: []string{"step2"}},
								{ID: "step2", Tool: "backend.tool2"},
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
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{
									ID:   "step1",
									Tool: "backend.tool1",
									OnError: &ErrorHandling{
										Action:     "retry",
										MaxRetries: 3,
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
			name: "invalid error handling - retry without maxRetries",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{
									ID:   "step1",
									Tool: "backend.tool1",
									OnError: &ErrorHandling{
										Action:     "retry",
										MaxRetries: 0,
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.compositeTools[0].steps[0].onError.maxRetries must be at least 1 when action is retry",
		},
		{
			name: "valid error handling with retryDelay",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{
									ID:   "step1",
									Tool: "backend.tool1",
									OnError: &ErrorHandling{
										Action:     "retry",
										MaxRetries: 3,
										RetryDelay: "5s",
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
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{
									ID:   "step1",
									Tool: "backend.tool1",
									OnError: &ErrorHandling{
										Action:     "retry",
										MaxRetries: 3,
										RetryDelay: "1m30s",
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
			name: "invalid error handling - invalid retryDelay format",
			vmcp: &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "Test composite tool",
							Steps: []WorkflowStep{
								{
									ID:   "step1",
									Tool: "backend.tool1",
									OnError: &ErrorHandling{
										Action:     "retry",
										MaxRetries: 3,
										RetryDelay: "invalid",
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "spec.compositeTools[0].steps[0].onError.retryDelay: invalid duration format \"invalid\", expected format like '30s', '5m', '1h', '1h30m'",
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

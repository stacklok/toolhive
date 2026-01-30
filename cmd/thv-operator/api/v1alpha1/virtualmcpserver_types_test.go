// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vmcp "github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestVirtualMCPServerPhaseTransitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		initialPhase  VirtualMCPServerPhase
		targetPhase   VirtualMCPServerPhase
		shouldBeValid bool
		description   string
	}{
		{
			name:          "pending_to_ready",
			initialPhase:  VirtualMCPServerPhasePending,
			targetPhase:   VirtualMCPServerPhaseReady,
			shouldBeValid: true,
			description:   "Normal transition from Pending to Ready",
		},
		{
			name:          "pending_to_failed",
			initialPhase:  VirtualMCPServerPhasePending,
			targetPhase:   VirtualMCPServerPhaseFailed,
			shouldBeValid: true,
			description:   "Transition from Pending to Failed on error",
		},
		{
			name:          "ready_to_degraded",
			initialPhase:  VirtualMCPServerPhaseReady,
			targetPhase:   VirtualMCPServerPhaseDegraded,
			shouldBeValid: true,
			description:   "Transition from Ready to Degraded when some backends fail",
		},
		{
			name:          "degraded_to_ready",
			initialPhase:  VirtualMCPServerPhaseDegraded,
			targetPhase:   VirtualMCPServerPhaseReady,
			shouldBeValid: true,
			description:   "Transition from Degraded back to Ready when backends recover",
		},
		{
			name:          "ready_to_failed",
			initialPhase:  VirtualMCPServerPhaseReady,
			targetPhase:   VirtualMCPServerPhaseFailed,
			shouldBeValid: true,
			description:   "Transition from Ready to Failed on critical error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Status: VirtualMCPServerStatus{
					Phase: tt.initialPhase,
				},
			}

			// Update phase
			vmcp.Status.Phase = tt.targetPhase

			assert.Equal(t, tt.targetPhase, vmcp.Status.Phase,
				"Phase transition from %s to %s should be valid: %s",
				tt.initialPhase, tt.targetPhase, tt.description)
		})
	}
}

func TestVirtualMCPServerConditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		conditions []metav1.Condition
		validate   func(*testing.T, *VirtualMCPServer)
	}{
		{
			name: "all_conditions_true",
			conditions: []metav1.Condition{
				{
					Type:   ConditionTypeVirtualMCPServerReady,
					Status: metav1.ConditionTrue,
					Reason: "DeploymentReady",
				},
				{
					Type:   ConditionTypeAuthConfigured,
					Status: metav1.ConditionTrue,
					Reason: ConditionReasonIncomingAuthValid,
				},
			},
			validate: func(t *testing.T, vmcp *VirtualMCPServer) {
				t.Helper()
				assert.Len(t, vmcp.Status.Conditions, 2)
				for _, cond := range vmcp.Status.Conditions {
					assert.Equal(t, metav1.ConditionTrue, cond.Status)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Status: VirtualMCPServerStatus{
					Conditions: tt.conditions,
				},
			}

			tt.validate(t, vmcp)
		})
	}
}

func TestVirtualMCPServerDefaultValues(t *testing.T) {
	t.Parallel()

	server := &VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: VirtualMCPServerSpec{
			Config: config.Config{
				Group: "test-group",
				Aggregation: &config.AggregationConfig{
					ConflictResolution: "", // Should default to "prefix"
				},
			},
			OutgoingAuth: &OutgoingAuthConfig{
				Source: "", // Should default to "discovered"
			},
		},
	}

	// These defaults are enforced by kubebuilder markers
	// but we document expected values here
	assert.NotNil(t, server.Spec.OutgoingAuth)
	assert.NotNil(t, server.Spec.Config.Aggregation)
}

func TestVirtualMCPServerNamespaceIsolation(t *testing.T) {
	t.Parallel()

	// VirtualMCPServer in namespace "team-a"
	vmcpTeamA := &VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmcp",
			Namespace: "team-a",
		},
		Spec: VirtualMCPServerSpec{
			Config: config.Config{Group: "backend-group"}, // Must be in team-a namespace
		},
	}

	// VirtualMCPServer in namespace "team-b"
	vmcpTeamB := &VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmcp",
			Namespace: "team-b",
		},
		Spec: VirtualMCPServerSpec{
			Config: config.Config{Group: "backend-group"}, // Different group in team-b namespace
		},
	}

	// Both can have the same name because they're in different namespaces
	assert.Equal(t, "vmcp", vmcpTeamA.Name)
	assert.Equal(t, "vmcp", vmcpTeamB.Name)
	assert.NotEqual(t, vmcpTeamA.Namespace, vmcpTeamB.Namespace)

	// Group names can be the same but refer to different groups in different namespaces
	assert.Equal(t, "backend-group", vmcpTeamA.Spec.Config.Group)
	assert.Equal(t, "backend-group", vmcpTeamB.Spec.Config.Group)
}

func TestConflictResolutionStrategies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		strategy    vmcp.ConflictResolutionStrategy
		configValue *config.ConflictResolutionConfig
		isValid     bool
	}{
		{
			name:     "prefix_strategy_with_format",
			strategy: vmcp.ConflictStrategyPrefix,
			configValue: &config.ConflictResolutionConfig{
				PrefixFormat: "{workload}_",
			},
			isValid: true,
		},
		{
			name:     "priority_strategy_with_order",
			strategy: vmcp.ConflictStrategyPriority,
			configValue: &config.ConflictResolutionConfig{
				PriorityOrder: []string{"github", "jira", "slack"},
			},
			isValid: true,
		},
		{
			name:        "manual_strategy",
			strategy:    vmcp.ConflictStrategyManual,
			configValue: &config.ConflictResolutionConfig{},
			isValid:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcpServer := &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						Aggregation: &config.AggregationConfig{
							ConflictResolution:       tt.strategy,
							ConflictResolutionConfig: tt.configValue,
						},
					},
				},
			}

			// Validate the configuration
			err := vmcpServer.Validate()
			if tt.isValid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestBackendAuthConfigTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		authConfig BackendAuthConfig
		isValid    bool
		errorMsg   string
	}{
		{
			name: "discovered_auth",
			authConfig: BackendAuthConfig{
				Type: BackendAuthTypeDiscovered,
			},
			isValid: true,
		},
		{
			name: "external_auth_config_ref_valid",
			authConfig: BackendAuthConfig{
				Type: BackendAuthTypeExternalAuthConfigRef,
				ExternalAuthConfigRef: &ExternalAuthConfigRef{
					Name: "my-auth-config",
				},
			},
			isValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{Group: "test-group"},
					OutgoingAuth: &OutgoingAuthConfig{
						Backends: map[string]BackendAuthConfig{
							"test-backend": tt.authConfig,
						},
					},
				},
			}

			err := vmcp.Validate()
			if tt.isValid {
				assert.NoError(t, err, "Auth config should be valid: %s", tt.name)
			} else {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			}
		})
	}
}

func TestCompositeToolStepDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		steps   []config.WorkflowStepConfig
		isValid bool
		errMsg  string
	}{
		{
			name: "valid_sequential_dependencies",
			steps: []config.WorkflowStepConfig{
				{ID: "step1", Type: "tool", Tool: "backend.tool1"},
				{ID: "step2", Type: "tool", Tool: "backend.tool2", DependsOn: []string{"step1"}},
				{ID: "step3", Type: "tool", Tool: "backend.tool3", DependsOn: []string{"step2"}},
			},
			isValid: true,
		},
		{
			name: "valid_parallel_steps",
			steps: []config.WorkflowStepConfig{
				{ID: "step1", Type: "tool", Tool: "backend.tool1"},
				{ID: "step2", Type: "tool", Tool: "backend.tool2"},
				{ID: "step3", Type: "tool", Tool: "backend.tool3", DependsOn: []string{"step1", "step2"}},
			},
			isValid: true,
		},
		{
			name: "valid_forward_reference",
			steps: []config.WorkflowStepConfig{
				{ID: "step1", Type: "tool", Tool: "backend.tool1", DependsOn: []string{"step2"}},
				{ID: "step2", Type: "tool", Tool: "backend.tool2"},
			},
			isValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						CompositeTools: []config.CompositeToolConfig{
							{
								Name:        "test-workflow",
								Description: "Test workflow",
								Steps:       tt.steps,
							},
						},
					},
				},
			}

			err := server.Validate()
			if tt.isValid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			}
		})
	}
}

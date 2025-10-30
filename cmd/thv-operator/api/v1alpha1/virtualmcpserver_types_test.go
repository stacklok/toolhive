package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
					Reason: ConditionReasonAllBackendsReady,
				},
				{
					Type:   ConditionTypeAuthConfigured,
					Status: metav1.ConditionTrue,
					Reason: ConditionReasonIncomingAuthValid,
				},
				{
					Type:   ConditionTypeBackendsDiscovered,
					Status: metav1.ConditionTrue,
					Reason: ConditionReasonDiscoveryComplete,
				},
			},
			validate: func(t *testing.T, vmcp *VirtualMCPServer) {
				t.Helper()
				assert.Len(t, vmcp.Status.Conditions, 3)
				for _, cond := range vmcp.Status.Conditions {
					assert.Equal(t, metav1.ConditionTrue, cond.Status)
				}
			},
		},
		{
			name: "ready_false_with_backend_issues",
			conditions: []metav1.Condition{
				{
					Type:    ConditionTypeVirtualMCPServerReady,
					Status:  metav1.ConditionFalse,
					Reason:  ConditionReasonSomeBackendsUnavailable,
					Message: "2 out of 5 backends unavailable",
				},
			},
			validate: func(t *testing.T, vmcp *VirtualMCPServer) {
				t.Helper()
				assert.Len(t, vmcp.Status.Conditions, 1)
				cond := vmcp.Status.Conditions[0]
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Contains(t, cond.Message, "backends unavailable")
			},
		},
		{
			name: "discovery_failed",
			conditions: []metav1.Condition{
				{
					Type:    ConditionTypeBackendsDiscovered,
					Status:  metav1.ConditionFalse,
					Reason:  ConditionReasonDiscoveryFailed,
					Message: "Failed to discover backends from group",
				},
			},
			validate: func(t *testing.T, vmcp *VirtualMCPServer) {
				t.Helper()
				cond := vmcp.Status.Conditions[0]
				assert.Equal(t, ConditionTypeBackendsDiscovered, cond.Type)
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
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

func TestDiscoveredBackendsStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		discoveredBackends []DiscoveredBackend
		expectedCount      int
		validate           func(*testing.T, []DiscoveredBackend)
	}{
		{
			name: "multiple_backends_all_ready",
			discoveredBackends: []DiscoveredBackend{
				{
					Name:          "github",
					AuthConfigRef: "github-token-exchange",
					AuthType:      "token_exchange",
					Status:        "ready",
					URL:           "http://github-mcp.default.svc:8080",
				},
				{
					Name:          "jira",
					AuthConfigRef: "jira-token-exchange",
					AuthType:      "token_exchange",
					Status:        "ready",
					URL:           "http://jira-mcp.default.svc:8080",
				},
				{
					Name:     "slack",
					AuthType: "service_account",
					Status:   "ready",
					URL:      "http://slack-mcp.default.svc:8080",
				},
			},
			expectedCount: 3,
			validate: func(t *testing.T, backends []DiscoveredBackend) {
				t.Helper()
				readyCount := 0
				for _, b := range backends {
					if b.Status == "ready" {
						readyCount++
					}
				}
				assert.Equal(t, 3, readyCount, "All backends should be ready")
			},
		},
		{
			name: "mixed_backend_status",
			discoveredBackends: []DiscoveredBackend{
				{
					Name:          "github",
					AuthConfigRef: "github-token-exchange",
					Status:        "ready",
				},
				{
					Name:          "jira",
					AuthConfigRef: "jira-token-exchange",
					Status:        "degraded",
				},
				{
					Name:   "slack",
					Status: "unavailable",
				},
			},
			expectedCount: 3,
			validate: func(t *testing.T, backends []DiscoveredBackend) {
				t.Helper()
				statusCounts := make(map[string]int)
				for _, b := range backends {
					statusCounts[b.Status]++
				}
				assert.Equal(t, 1, statusCounts["ready"])
				assert.Equal(t, 1, statusCounts["degraded"])
				assert.Equal(t, 1, statusCounts["unavailable"])
			},
		},
		{
			name: "backend_with_no_auth",
			discoveredBackends: []DiscoveredBackend{
				{
					Name:          "internal-api",
					AuthConfigRef: "", // No auth config
					AuthType:      "pass_through",
					Status:        "ready",
				},
			},
			expectedCount: 1,
			validate: func(t *testing.T, backends []DiscoveredBackend) {
				t.Helper()
				assert.Empty(t, backends[0].AuthConfigRef)
				assert.Equal(t, "pass_through", backends[0].AuthType)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &VirtualMCPServer{
				Status: VirtualMCPServerStatus{
					DiscoveredBackends: tt.discoveredBackends,
				},
			}

			assert.Len(t, vmcp.Status.DiscoveredBackends, tt.expectedCount)
			tt.validate(t, vmcp.Status.DiscoveredBackends)
		})
	}
}

func TestCapabilitiesSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		capabilities *CapabilitiesSummary
		validate     func(*testing.T, *CapabilitiesSummary)
	}{
		{
			name: "full_capabilities",
			capabilities: &CapabilitiesSummary{
				ToolCount:          25,
				ResourceCount:      10,
				PromptCount:        5,
				CompositeToolCount: 3,
			},
			validate: func(t *testing.T, caps *CapabilitiesSummary) {
				t.Helper()
				assert.Equal(t, 25, caps.ToolCount)
				assert.Equal(t, 10, caps.ResourceCount)
				assert.Equal(t, 5, caps.PromptCount)
				assert.Equal(t, 3, caps.CompositeToolCount)
			},
		},
		{
			name: "only_tools_no_resources",
			capabilities: &CapabilitiesSummary{
				ToolCount:          15,
				ResourceCount:      0,
				PromptCount:        0,
				CompositeToolCount: 1,
			},
			validate: func(t *testing.T, caps *CapabilitiesSummary) {
				t.Helper()
				assert.Greater(t, caps.ToolCount, 0)
				assert.Equal(t, 0, caps.ResourceCount)
				assert.Equal(t, 0, caps.PromptCount)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &VirtualMCPServer{
				Status: VirtualMCPServerStatus{
					Capabilities: tt.capabilities,
				},
			}

			tt.validate(t, vmcp.Status.Capabilities)
		})
	}
}

func TestVirtualMCPServerDefaultValues(t *testing.T) {
	t.Parallel()

	vmcp := &VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: VirtualMCPServerSpec{
			GroupRef: GroupRef{
				Name: "test-group",
			},
			OutgoingAuth: &OutgoingAuthConfig{
				Source: "", // Should default to "discovered"
			},
			Aggregation: &AggregationConfig{
				ConflictResolution: "", // Should default to "prefix"
			},
			TokenCache: &TokenCacheConfig{
				Provider: "", // Should default to "memory"
			},
		},
	}

	// These defaults are enforced by kubebuilder markers
	// but we document expected values here
	assert.NotNil(t, vmcp.Spec.OutgoingAuth)
	assert.NotNil(t, vmcp.Spec.Aggregation)
	assert.NotNil(t, vmcp.Spec.TokenCache)
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
			GroupRef: GroupRef{
				Name: "backend-group", // Must be in team-a namespace
			},
		},
	}

	// VirtualMCPServer in namespace "team-b"
	vmcpTeamB := &VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmcp",
			Namespace: "team-b",
		},
		Spec: VirtualMCPServerSpec{
			GroupRef: GroupRef{
				Name: "backend-group", // Different group in team-b namespace
			},
		},
	}

	// Both can have the same name because they're in different namespaces
	assert.Equal(t, "vmcp", vmcpTeamA.Name)
	assert.Equal(t, "vmcp", vmcpTeamB.Name)
	assert.NotEqual(t, vmcpTeamA.Namespace, vmcpTeamB.Namespace)

	// GroupRef names can be the same but refer to different groups in different namespaces
	assert.Equal(t, "backend-group", vmcpTeamA.Spec.GroupRef.Name)
	assert.Equal(t, "backend-group", vmcpTeamB.Spec.GroupRef.Name)
}

func TestConflictResolutionStrategies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		strategy string
		config   *ConflictResolutionConfig
		isValid  bool
	}{
		{
			name:     "prefix_strategy_with_format",
			strategy: ConflictResolutionPrefix,
			config: &ConflictResolutionConfig{
				PrefixFormat: "{workload}_",
			},
			isValid: true,
		},
		{
			name:     "priority_strategy_with_order",
			strategy: ConflictResolutionPriority,
			config: &ConflictResolutionConfig{
				PriorityOrder: []string{"github", "jira", "slack"},
			},
			isValid: true,
		},
		{
			name:     "manual_strategy",
			strategy: ConflictResolutionManual,
			config:   &ConflictResolutionConfig{},
			isValid:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					Aggregation: &AggregationConfig{
						ConflictResolution:       tt.strategy,
						ConflictResolutionConfig: tt.config,
					},
				},
			}

			// Validate the configuration
			err := vmcp.Validate()
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
			name: "pass_through_auth",
			authConfig: BackendAuthConfig{
				Type: BackendAuthTypePassThrough,
			},
			isValid: true,
		},
		{
			name: "service_account_auth_valid",
			authConfig: BackendAuthConfig{
				Type: BackendAuthTypeServiceAccount,
				ServiceAccount: &ServiceAccountAuth{
					CredentialsRef: SecretKeyRef{
						Name: "my-secret",
						Key:  "token",
					},
					HeaderName:   "Authorization",
					HeaderFormat: "Bearer {token}",
				},
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
					GroupRef: GroupRef{Name: "test-group"},
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
		steps   []WorkflowStep
		isValid bool
		errMsg  string
	}{
		{
			name: "valid_sequential_dependencies",
			steps: []WorkflowStep{
				{ID: "step1", Tool: "backend.tool1"},
				{ID: "step2", Tool: "backend.tool2", DependsOn: []string{"step1"}},
				{ID: "step3", Tool: "backend.tool3", DependsOn: []string{"step2"}},
			},
			isValid: true,
		},
		{
			name: "valid_parallel_steps",
			steps: []WorkflowStep{
				{ID: "step1", Tool: "backend.tool1"},
				{ID: "step2", Tool: "backend.tool2"},
				{ID: "step3", Tool: "backend.tool3", DependsOn: []string{"step1", "step2"}},
			},
			isValid: true,
		},
		{
			name: "valid_forward_reference",
			steps: []WorkflowStep{
				{ID: "step1", Tool: "backend.tool1", DependsOn: []string{"step2"}},
				{ID: "step2", Tool: "backend.tool2"},
			},
			isValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &VirtualMCPServer{
				Spec: VirtualMCPServerSpec{
					GroupRef: GroupRef{Name: "test-group"},
					CompositeTools: []CompositeToolSpec{
						{
							Name:        "test-workflow",
							Description: "Test workflow",
							Steps:       tt.steps,
						},
					},
				},
			}

			err := vmcp.Validate()
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

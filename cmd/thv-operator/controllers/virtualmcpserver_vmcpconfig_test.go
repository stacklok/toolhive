// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	oidcmocks "github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc/mocks"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
	statusmocks "github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus/mocks"
	vmcpconfigconv "github.com/stacklok/toolhive/cmd/thv-operator/pkg/vmcpconfig"
	thvjson "github.com/stacklok/toolhive/pkg/json"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// newNoOpMockResolver creates a mock resolver that returns (nil, nil) for all calls.
// Use this in tests that don't care about OIDC configuration.
func newNoOpMockResolver(t *testing.T) *oidcmocks.MockResolver {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockResolver := oidcmocks.NewMockResolver(ctrl)
	mockResolver.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	return mockResolver
}

// newTestConverter creates a Converter with the given resolver, failing the test if creation fails.
func newTestConverter(t *testing.T, resolver *oidcmocks.MockResolver) *vmcpconfigconv.Converter {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	converter, err := vmcpconfigconv.NewConverter(resolver, fakeClient)
	require.NoError(t, err)
	return converter
}

// TestCreateVmcpConfigFromVirtualMCPServer tests vmcp config generation
func TestCreateVmcpConfigFromVirtualMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		vmcp             *mcpv1alpha1.VirtualMCPServer
		expectedName     string
		expectedGroupRef string
	}{
		{
			name: "basic config",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: "test-group"},
				},
			},
			expectedName:     "test-vmcp",
			expectedGroupRef: "test-group",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter := newTestConverter(t, newNoOpMockResolver(t))
			config, err := converter.Convert(context.Background(), tt.vmcp)

			require.NoError(t, err)
			assert.NotNil(t, config)
			assert.Equal(t, tt.expectedName, config.Name)
			assert.Equal(t, tt.expectedGroupRef, config.Group)
		})
	}
}

// TestConvertOutgoingAuth tests outgoing auth configuration conversion
func TestConvertOutgoingAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		outgoingAuth   *mcpv1alpha1.OutgoingAuthConfig
		expectedSource string
		hasDefault     bool
		backendCount   int
	}{
		{
			name: "discovered mode",
			outgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
				Source: mcpv1alpha1.BackendAuthTypeDiscovered,
			},
			expectedSource: mcpv1alpha1.BackendAuthTypeDiscovered,
			hasDefault:     false,
			backendCount:   0,
		},
		{
			name: "with default auth",
			outgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
				Source: "inline",
				Default: &mcpv1alpha1.BackendAuthConfig{
					Type: mcpv1alpha1.BackendAuthTypeDiscovered,
				},
			},
			expectedSource: "inline",
			hasDefault:     true,
			backendCount:   0,
		},
		{
			name: "with per-backend auth",
			outgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
				Source: "discovered",
				Backends: map[string]mcpv1alpha1.BackendAuthConfig{
					"backend-1": {
						Type: mcpv1alpha1.BackendAuthTypeDiscovered,
					},
				},
			},
			expectedSource: "discovered",
			hasDefault:     false,
			backendCount:   1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcpServer := &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config:       vmcpconfig.Config{Group: "test-group"},
					OutgoingAuth: tt.outgoingAuth,
				},
			}

			converter := newTestConverter(t, newNoOpMockResolver(t))
			config, err := converter.Convert(context.Background(), vmcpServer)
			require.NoError(t, err)

			require.NotNil(t, config.OutgoingAuth)
			assert.Equal(t, tt.expectedSource, config.OutgoingAuth.Source)

			if tt.hasDefault {
				assert.NotNil(t, config.OutgoingAuth.Default)
			}

			assert.Len(t, config.OutgoingAuth.Backends, tt.backendCount)
		})
	}
}

// TestConvertBackendAuthConfig tests backend auth config conversion
func TestConvertBackendAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		authConfig   *mcpv1alpha1.BackendAuthConfig
		expectedType string
	}{
		{
			name: "discovered",
			authConfig: &mcpv1alpha1.BackendAuthConfig{
				Type: mcpv1alpha1.BackendAuthTypeDiscovered,
			},
			// "discovered" type is converted to "unauthenticated" by the converter
			expectedType: "unauthenticated",
		},
		{
			name: "external auth config ref",
			authConfig: &mcpv1alpha1.BackendAuthConfig{
				Type: mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef,
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: "auth-config",
				},
			},
			// For external_auth_config_ref, the type comes from the referenced MCPExternalAuthConfig
			expectedType: "unauthenticated",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcpServer := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Default: tt.authConfig,
					},
				},
			}

			// For external_auth_config_ref test, create the referenced MCPExternalAuthConfig
			var converter *vmcpconfigconv.Converter
			if tt.authConfig.Type == mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef {
				// Create a fake MCPExternalAuthConfig
				externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "auth-config",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeUnauthenticated,
					},
				}

				// Create converter with fake client that has the external auth config
				scheme := runtime.NewScheme()
				_ = mcpv1alpha1.AddToScheme(scheme)
				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(externalAuthConfig).
					Build()
				var err error
				converter, err = vmcpconfigconv.NewConverter(newNoOpMockResolver(t), fakeClient)
				require.NoError(t, err)
			} else {
				converter = newTestConverter(t, newNoOpMockResolver(t))
			}

			config, err := converter.Convert(context.Background(), vmcpServer)
			require.NoError(t, err)

			require.NotNil(t, config.OutgoingAuth)
			require.NotNil(t, config.OutgoingAuth.Default)
			strategy := config.OutgoingAuth.Default

			require.NotNil(t, strategy)
			assert.Equal(t, tt.expectedType, strategy.Type)

			// Note: HeaderInjection and TokenExchange are nil because the CRD's
			// BackendAuthConfig only stores type and reference information.
			// For external_auth_config_ref, the actual auth config is resolved
			// at runtime from the referenced MCPExternalAuthConfig resource.
			assert.Nil(t, strategy.HeaderInjection)
			assert.Nil(t, strategy.TokenExchange)
		})
	}
}

// TestConvertAggregation tests aggregation config conversion
func TestConvertAggregation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		aggregation             *vmcpconfig.AggregationConfig
		expectedStrategy        vmcp.ConflictResolutionStrategy
		hasPrefixFormat         bool
		hasPriorityOrder        bool
		expectedToolConfigCount int
	}{
		{
			name: "prefix strategy",
			aggregation: &vmcpconfig.AggregationConfig{
				ConflictResolution: vmcp.ConflictStrategyPrefix,
				ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
					PrefixFormat: "{workload}_",
				},
			},
			expectedStrategy: vmcp.ConflictStrategyPrefix,
			hasPrefixFormat:  true,
		},
		{
			name: "priority strategy",
			aggregation: &vmcpconfig.AggregationConfig{
				ConflictResolution: vmcp.ConflictStrategyPriority,
				ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
					PriorityOrder: []string{"backend-1", "backend-2"},
				},
			},
			expectedStrategy: vmcp.ConflictStrategyPriority,
			hasPriorityOrder: true,
		},
		{
			name: "with tool configs",
			aggregation: &vmcpconfig.AggregationConfig{
				ConflictResolution: vmcp.ConflictStrategyPrefix,
				Tools: []*vmcpconfig.WorkloadToolConfig{
					{
						Workload: "backend-1",
						Filter:   []string{"tool1", "tool2"},
					},
					{
						Workload: "backend-2",
						Overrides: map[string]*vmcpconfig.ToolOverride{
							"tool3": {
								Name:        "renamed_tool3",
								Description: "Updated description",
							},
						},
					},
				},
			},
			expectedStrategy:        vmcp.ConflictStrategyPrefix,
			expectedToolConfigCount: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcpServer := &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group:       "test-group",
						Aggregation: tt.aggregation,
					},
				},
			}

			converter := newTestConverter(t, newNoOpMockResolver(t))
			config, err := converter.Convert(context.Background(), vmcpServer)
			require.NoError(t, err)

			require.NotNil(t, config.Aggregation)
			assert.Equal(t, tt.expectedStrategy, config.Aggregation.ConflictResolution)

			if tt.hasPrefixFormat {
				require.NotNil(t, config.Aggregation.ConflictResolutionConfig)
				assert.NotEmpty(t, config.Aggregation.ConflictResolutionConfig.PrefixFormat)
			}

			if tt.hasPriorityOrder {
				require.NotNil(t, config.Aggregation.ConflictResolutionConfig)
				assert.NotEmpty(t, config.Aggregation.ConflictResolutionConfig.PriorityOrder)
			}

			if tt.expectedToolConfigCount > 0 {
				assert.Len(t, config.Aggregation.Tools, tt.expectedToolConfigCount)
			}
		})
	}
}

// TestConvertCompositeTools tests that composite tools pass through during conversion
func TestConvertCompositeTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		compositeTools []vmcpconfig.CompositeToolConfig
		expectedCount  int
	}{
		{
			name: "single composite tool",
			compositeTools: []vmcpconfig.CompositeToolConfig{
				{
					Name:        "deploy_workflow",
					Description: "Deploy and verify",
					Timeout:     vmcpconfig.Duration(10 * time.Minute),
					Steps: []vmcpconfig.WorkflowStepConfig{
						{
							ID:   "deploy",
							Type: mcpv1alpha1.WorkflowStepTypeToolCall,
							Tool: "kubectl.apply",
						},
					},
				},
			},
			expectedCount: 1,
		},
		{
			name: "multiple composite tools",
			compositeTools: []vmcpconfig.CompositeToolConfig{
				{
					Name:        "workflow1",
					Description: "Workflow 1",
					Steps: []vmcpconfig.WorkflowStepConfig{
						{
							ID:   "step1",
							Type: mcpv1alpha1.WorkflowStepTypeToolCall,
						},
					},
				},
				{
					Name:        "workflow2",
					Description: "Workflow 2",
					Steps: []vmcpconfig.WorkflowStepConfig{
						{
							ID:   "step1",
							Type: mcpv1alpha1.WorkflowStepTypeElicitation,
						},
					},
				},
			},
			expectedCount: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcpServer := &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group:          "test-group",
						CompositeTools: tt.compositeTools,
					},
				},
			}

			converter := newTestConverter(t, newNoOpMockResolver(t))
			config, err := converter.Convert(context.Background(), vmcpServer)
			require.NoError(t, err)

			tools := config.CompositeTools
			assert.Len(t, tools, tt.expectedCount)

			for i, tool := range tools {
				assert.Equal(t, tt.compositeTools[i].Name, tool.Name)
				assert.Equal(t, tt.compositeTools[i].Description, tool.Description)
				assert.Len(t, tool.Steps, len(tt.compositeTools[i].Steps))
			}
		})
	}
}

// TestEnsureVmcpConfigConfigMap tests ConfigMap creation
func TestEnsureVmcpConfigConfigMap(t *testing.T) {
	t.Parallel()

	testVmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{Group: "test-group"},
		},
	}

	// Create MCPGroup for workload discovery
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPGroupSpec{},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(testVmcp, mcpGroup).
		Build()

	r := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Fetch workload names (matching production behavior)
	ctx := context.Background()
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(fakeClient, testVmcp.Namespace)
	workloadNames, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, testVmcp.Spec.Config.Group)
	require.NoError(t, err, "should successfully list workloads in group")

	// Create a status collector (we don't validate status in this test)
	statusCollector := virtualmcpserverstatus.NewStatusManager(testVmcp)

	err = r.ensureVmcpConfigConfigMap(ctx, testVmcp, workloadNames, statusCollector)
	require.NoError(t, err)

	// Verify ConfigMap was created
	cm := &corev1.ConfigMap{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-vmcp-vmcp-config",
		Namespace: "default",
	}, cm)
	require.NoError(t, err)
	assert.Equal(t, "test-vmcp-vmcp-config", cm.Name)
	assert.Contains(t, cm.Data, "config.yaml")
	assert.NotEmpty(t, cm.Annotations["toolhive.stacklok.dev/content-checksum"])
}

// TestSetAuthConfigConditions tests that auth config conditions reflect the current state
// for all three types of auth configs: default, backend-specific (inline), and discovered.
func TestSetAuthConfigConditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		backendsWithAuthConfig []string // Only backends with ExternalAuthConfigRef
		inlineBackendNames     []string // Inline backends from OutgoingAuth.Backends
		hasValidDefaultAuth    bool     // Whether default auth is valid
		validInlineBackends    []string // Inline backends with valid auth
		allAuthErrors          []AuthConfigError
		validate               func(*testing.T, *statusmocks.MockStatusManager)
	}{
		{
			name:                   "discovered: backend with auth error sets False condition",
			backendsWithAuthConfig: []string{"backend-1"},
			inlineBackendNames:     []string{}, // No inline backends
			allAuthErrors: []AuthConfigError{
				{
					Context:     "discovered:backend-1",
					BackendName: "backend-1",
					Error:       fmt.Errorf("failed to get MCPExternalAuthConfig missing-config: not found"),
				},
			},
			validate: func(t *testing.T, mock *statusmocks.MockStatusManager) {
				t.Helper()
				mock.EXPECT().
					RemoveConditionsWithPrefix("DefaultAuthConfig", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("DiscoveredAuthConfig-", []string{"DiscoveredAuthConfig-backend-1"}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("BackendAuthConfig-", []string{}).
					Times(1)
				mock.EXPECT().
					SetAuthConfigCondition(
						"DiscoveredAuthConfig-backend-1",
						"ConversionFailed",
						gomock.Any(),
						metav1.ConditionFalse,
					).
					Times(1).
					Do(func(_, _, message string, _ metav1.ConditionStatus) {
						assert.Contains(t, message, "Failed to convert discovered auth config")
						assert.Contains(t, message, "missing-config")
					})
			},
		},
		{
			name:                   "backend with auth config but no error sets True condition",
			backendsWithAuthConfig: []string{"backend-1"},
			inlineBackendNames:     []string{}, // No inline backends
			allAuthErrors:          []AuthConfigError{},
			validate: func(t *testing.T, mock *statusmocks.MockStatusManager) {
				t.Helper()
				mock.EXPECT().
					RemoveConditionsWithPrefix("DefaultAuthConfig", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("DiscoveredAuthConfig-", []string{"DiscoveredAuthConfig-backend-1"}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("BackendAuthConfig-", []string{}).
					Times(1)
				mock.EXPECT().
					SetAuthConfigCondition(
						"DiscoveredAuthConfig-backend-1",
						"ConversionSucceeded",
						"Discovered auth config is valid",
						metav1.ConditionTrue,
					).
					Times(1)
			},
		},
		{
			name:                   "mixed: some backends with errors, some without",
			backendsWithAuthConfig: []string{"backend-1", "backend-2", "backend-3"},
			inlineBackendNames:     []string{}, // No inline backends
			allAuthErrors: []AuthConfigError{
				{
					Context:     "discovered:backend-1",
					BackendName: "backend-1",
					Error:       fmt.Errorf("auth error 1"),
				},
			},
			validate: func(t *testing.T, mock *statusmocks.MockStatusManager) {
				t.Helper()
				mock.EXPECT().
					RemoveConditionsWithPrefix("DefaultAuthConfig", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("DiscoveredAuthConfig-", []string{
						"DiscoveredAuthConfig-backend-1",
						"DiscoveredAuthConfig-backend-2",
						"DiscoveredAuthConfig-backend-3",
					}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("BackendAuthConfig-", []string{}).
					Times(1)
				// backend-1 has error - False condition
				mock.EXPECT().
					SetAuthConfigCondition(
						"DiscoveredAuthConfig-backend-1",
						"ConversionFailed",
						gomock.Any(),
						metav1.ConditionFalse,
					).
					Times(1)
				// backend-2 has no error - True condition
				mock.EXPECT().
					SetAuthConfigCondition(
						"DiscoveredAuthConfig-backend-2",
						"ConversionSucceeded",
						"Discovered auth config is valid",
						metav1.ConditionTrue,
					).
					Times(1)
				// backend-3 has no error - True condition
				mock.EXPECT().
					SetAuthConfigCondition(
						"DiscoveredAuthConfig-backend-3",
						"ConversionSucceeded",
						"Discovered auth config is valid",
						metav1.ConditionTrue,
					).
					Times(1)
			},
		},
		{
			name:                   "no backends with auth configs means no conditions",
			backendsWithAuthConfig: []string{},
			inlineBackendNames:     []string{}, // No inline backends
			allAuthErrors:          []AuthConfigError{},
			validate: func(t *testing.T, mock *statusmocks.MockStatusManager) {
				t.Helper()
				mock.EXPECT().
					RemoveConditionsWithPrefix("DefaultAuthConfig", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("DiscoveredAuthConfig-", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("BackendAuthConfig-", []string{}).
					Times(1)
				// No backends with auth configs = no conditions set
			},
		},
		{
			name:                   "default auth error sets DefaultAuthConfig condition",
			backendsWithAuthConfig: []string{},
			inlineBackendNames:     []string{}, // No inline backends
			allAuthErrors: []AuthConfigError{
				{
					Context:     "default",
					BackendName: "",
					Error:       fmt.Errorf("invalid OIDC config"),
				},
			},
			validate: func(t *testing.T, mock *statusmocks.MockStatusManager) {
				t.Helper()
				mock.EXPECT().
					RemoveConditionsWithPrefix("DiscoveredAuthConfig-", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("BackendAuthConfig-", []string{}).
					Times(1)
				mock.EXPECT().
					SetAuthConfigCondition(
						"DefaultAuthConfig",
						"ConversionFailed",
						gomock.Any(),
						metav1.ConditionFalse,
					).
					Times(1).
					Do(func(_, _, message string, _ metav1.ConditionStatus) {
						assert.Contains(t, message, "Failed to convert default auth config")
						assert.Contains(t, message, "invalid OIDC config")
					})
			},
		},
		{
			name:                   "backend-specific auth error sets BackendAuthConfig condition",
			backendsWithAuthConfig: []string{},
			inlineBackendNames:     []string{"api-backend"}, // Inline backend exists in spec
			allAuthErrors: []AuthConfigError{
				{
					Context:     "backend:api-backend",
					BackendName: "api-backend",
					Error:       fmt.Errorf("missing secret reference"),
				},
			},
			validate: func(t *testing.T, mock *statusmocks.MockStatusManager) {
				t.Helper()
				mock.EXPECT().
					RemoveConditionsWithPrefix("DefaultAuthConfig", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("DiscoveredAuthConfig-", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("BackendAuthConfig-", []string{"BackendAuthConfig-api-backend"}).
					Times(1)
				mock.EXPECT().
					SetAuthConfigCondition(
						"BackendAuthConfig-api-backend",
						"ConversionFailed",
						gomock.Any(),
						metav1.ConditionFalse,
					).
					Times(1).
					Do(func(_, _, message string, _ metav1.ConditionStatus) {
						assert.Contains(t, message, "Failed to convert backend auth config")
						assert.Contains(t, message, "missing secret reference")
					})
			},
		},
		{
			name:                   "all three auth types: default error, backend error, discovered success and error",
			backendsWithAuthConfig: []string{"discovered-1", "discovered-2"},
			inlineBackendNames:     []string{"inline-backend"}, // Inline backend exists in spec
			allAuthErrors: []AuthConfigError{
				{
					Context:     "default",
					BackendName: "",
					Error:       fmt.Errorf("default auth failed"),
				},
				{
					Context:     "backend:inline-backend",
					BackendName: "inline-backend",
					Error:       fmt.Errorf("inline backend auth failed"),
				},
				{
					Context:     "discovered:discovered-1",
					BackendName: "discovered-1",
					Error:       fmt.Errorf("discovered auth failed"),
				},
				// discovered-2 has no error (will get True condition)
			},
			validate: func(t *testing.T, mock *statusmocks.MockStatusManager) {
				t.Helper()
				mock.EXPECT().
					RemoveConditionsWithPrefix("DiscoveredAuthConfig-", []string{
						"DiscoveredAuthConfig-discovered-1",
						"DiscoveredAuthConfig-discovered-2",
					}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("BackendAuthConfig-", []string{"BackendAuthConfig-inline-backend"}).
					Times(1)
				// Default auth error
				mock.EXPECT().
					SetAuthConfigCondition(
						"DefaultAuthConfig",
						"ConversionFailed",
						gomock.Any(),
						metav1.ConditionFalse,
					).
					Times(1)
				// Backend-specific auth error
				mock.EXPECT().
					SetAuthConfigCondition(
						"BackendAuthConfig-inline-backend",
						"ConversionFailed",
						gomock.Any(),
						metav1.ConditionFalse,
					).
					Times(1)
				// Discovered auth error for discovered-1
				mock.EXPECT().
					SetAuthConfigCondition(
						"DiscoveredAuthConfig-discovered-1",
						"ConversionFailed",
						gomock.Any(),
						metav1.ConditionFalse,
					).
					Times(1)
				// Discovered auth success for discovered-2
				mock.EXPECT().
					SetAuthConfigCondition(
						"DiscoveredAuthConfig-discovered-2",
						"ConversionSucceeded",
						"Discovered auth config is valid",
						metav1.ConditionTrue,
					).
					Times(1)
			},
		},
		{
			name:                   "stale BackendAuthConfig conditions are removed when backend removed from spec",
			backendsWithAuthConfig: []string{},
			inlineBackendNames:     []string{"current-backend"}, // Only current-backend is in spec now
			allAuthErrors:          []AuthConfigError{},         // No errors
			validate: func(t *testing.T, mock *statusmocks.MockStatusManager) {
				t.Helper()
				// RemoveConditionsWithPrefix will remove any BackendAuthConfig-* conditions
				// that are NOT in the current list (e.g., BackendAuthConfig-removed-backend)
				mock.EXPECT().
					RemoveConditionsWithPrefix("DefaultAuthConfig", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("DiscoveredAuthConfig-", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("BackendAuthConfig-", []string{"BackendAuthConfig-current-backend"}).
					Times(1)
				// No new conditions are set because there are no errors
			},
		},
		{
			name:                   "valid default auth sets True condition",
			backendsWithAuthConfig: []string{},
			inlineBackendNames:     []string{},
			hasValidDefaultAuth:    true, // Valid default auth
			validInlineBackends:    []string{},
			allAuthErrors:          []AuthConfigError{}, // No errors
			validate: func(t *testing.T, mock *statusmocks.MockStatusManager) {
				t.Helper()
				mock.EXPECT().
					SetAuthConfigCondition(
						"DefaultAuthConfig",
						"ConversionSucceeded",
						"Default auth config is valid",
						metav1.ConditionTrue,
					).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("DiscoveredAuthConfig-", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("BackendAuthConfig-", []string{}).
					Times(1)
			},
		},
		{
			name:                   "valid inline backend auth sets True condition",
			backendsWithAuthConfig: []string{},
			inlineBackendNames:     []string{"api-backend"}, // Backend exists in spec
			hasValidDefaultAuth:    false,
			validInlineBackends:    []string{"api-backend"}, // Backend has valid auth
			allAuthErrors:          []AuthConfigError{},     // No errors
			validate: func(t *testing.T, mock *statusmocks.MockStatusManager) {
				t.Helper()
				mock.EXPECT().
					RemoveConditionsWithPrefix("DefaultAuthConfig", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("DiscoveredAuthConfig-", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("BackendAuthConfig-", []string{"BackendAuthConfig-api-backend"}).
					Times(1)
				mock.EXPECT().
					SetAuthConfigCondition(
						"BackendAuthConfig-api-backend",
						"ConversionSucceeded",
						"Backend auth config is valid",
						metav1.ConditionTrue,
					).
					Times(1)
			},
		},
		{
			name:                   "mixed valid and error auth configs: default valid, backend error",
			backendsWithAuthConfig: []string{},
			inlineBackendNames:     []string{"backend-1", "backend-2"},
			hasValidDefaultAuth:    true,                  // Valid default auth
			validInlineBackends:    []string{"backend-1"}, // backend-1 valid
			allAuthErrors: []AuthConfigError{
				{
					Context:     "backend:backend-2",
					BackendName: "backend-2",
					Error:       fmt.Errorf("backend-2 auth failed"),
				},
			},
			validate: func(t *testing.T, mock *statusmocks.MockStatusManager) {
				t.Helper()
				// Default auth True condition
				mock.EXPECT().
					SetAuthConfigCondition(
						"DefaultAuthConfig",
						"ConversionSucceeded",
						"Default auth config is valid",
						metav1.ConditionTrue,
					).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("DiscoveredAuthConfig-", []string{}).
					Times(1)
				mock.EXPECT().
					RemoveConditionsWithPrefix("BackendAuthConfig-", []string{
						"BackendAuthConfig-backend-1",
						"BackendAuthConfig-backend-2",
					}).
					Times(1)
				// backend-2 error - False condition
				mock.EXPECT().
					SetAuthConfigCondition(
						"BackendAuthConfig-backend-2",
						"ConversionFailed",
						gomock.Any(),
						metav1.ConditionFalse,
					).
					Times(1)
				// backend-1 valid - True condition
				mock.EXPECT().
					SetAuthConfigCondition(
						"BackendAuthConfig-backend-1",
						"ConversionSucceeded",
						"Backend auth config is valid",
						metav1.ConditionTrue,
					).
					Times(1)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockStatusManager := statusmocks.NewMockStatusManager(ctrl)

			// Set up expectations
			if tt.validate != nil {
				tt.validate(t, mockStatusManager)
			}

			// Call the function being tested
			setAuthConfigConditions(mockStatusManager, tt.backendsWithAuthConfig, tt.inlineBackendNames, tt.hasValidDefaultAuth, tt.validInlineBackends, tt.allAuthErrors)

			// gomock will verify expectations automatically
		})
	}
}

// TestValidateVmcpConfig tests config validation
func TestValidateVmcpConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      interface{}
		expectError bool
		errContains string
	}{
		{
			name:        "nil config",
			config:      nil,
			expectError: true,
			errContains: "cannot be nil",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			validator := vmcpconfigconv.NewValidator()

			// Type assertion will fail for nil, which is expected
			if tt.config == nil {
				err := validator.Validate(context.Background(), nil)
				if tt.expectError {
					require.Error(t, err)
					if tt.errContains != "" {
						assert.Contains(t, err.Error(), tt.errContains)
					}
				}
			}
		})
	}
}

// TestLabelsForVmcpConfig tests label generation for ConfigMap
func TestLabelsForVmcpConfig(t *testing.T) {
	t.Parallel()

	vmcpName := "my-vmcp"
	labels := labelsForVmcpConfig(vmcpName)

	assert.Equal(t, "vmcp-config", labels["toolhive.stacklok.io/component"])
	assert.Equal(t, vmcpName, labels["toolhive.stacklok.io/virtual-mcp-server"])
	assert.Equal(t, "toolhive-operator", labels["toolhive.stacklok.io/managed-by"])
}

// TestYAMLMarshalingDeterminism tests that YAML marshaling produces deterministic output
// for vmcp config containing map fields, ensuring stable checksums for ConfigMap updates.
func TestYAMLMarshalingDeterminism(t *testing.T) {
	t.Parallel()

	// Create a VirtualMCPServer with multiple map fields to test determinism
	testVmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{
				Group: "test-group",
				// Aggregation with tool overrides (map)
				Aggregation: &vmcpconfig.AggregationConfig{
					ConflictResolution: vmcp.ConflictStrategyPrefix,
					Tools: []*vmcpconfig.WorkloadToolConfig{
						{
							Workload: "workload-1",
							Overrides: map[string]*vmcpconfig.ToolOverride{
								"tool-zebra": {
									Name:        "renamed-zebra",
									Description: "Zebra tool",
								},
								"tool-alpha": {
									Name:        "renamed-alpha",
									Description: "Alpha tool",
								},
								"tool-middle": {
									Name:        "renamed-middle",
									Description: "Middle tool",
								},
							},
						},
					},
				},
				// Operational with PerWorkload timeouts (map)
				Operational: &vmcpconfig.OperationalConfig{
					Timeouts: &vmcpconfig.TimeoutConfig{
						Default: vmcpconfig.Duration(30 * time.Second),
						PerWorkload: map[string]vmcpconfig.Duration{
							"workload-zebra":  vmcpconfig.Duration(60 * time.Second),
							"workload-alpha":  vmcpconfig.Duration(45 * time.Second),
							"workload-middle": vmcpconfig.Duration(50 * time.Second),
						},
					},
				},
			},
			// OutgoingAuth with Backends map
			OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
				Source: "discovered",
				Backends: map[string]mcpv1alpha1.BackendAuthConfig{
					"backend-zebra": {
						Type: mcpv1alpha1.BackendAuthTypeDiscovered,
					},
					"backend-alpha": {
						Type: mcpv1alpha1.BackendAuthTypeDiscovered,
					},
					"backend-middle": {
						Type: mcpv1alpha1.BackendAuthTypeDiscovered,
					},
				},
			},
		},
	}

	converter := newTestConverter(t, newNoOpMockResolver(t))

	// Marshal the config 10 times to ensure deterministic output
	const iterations = 10
	results := make([]string, iterations)

	for i := 0; i < iterations; i++ {
		config, err := converter.Convert(context.Background(), testVmcp)
		require.NoError(t, err)

		yamlBytes, err := yaml.Marshal(config)
		require.NoError(t, err)

		results[i] = string(yamlBytes)
	}

	// Verify all results are identical
	for i := 1; i < len(results); i++ {
		assert.Equal(t, results[0], results[i],
			"YAML marshaling produced different output on iteration %d.\n"+
				"This indicates non-deterministic marshaling which will cause incorrect ConfigMap checksums.\n"+
				"Expected yaml.v3 to sort map keys alphabetically for deterministic output.", i)
	}

	// Additional verification: check that output contains sorted keys
	// (yaml.v3 should sort map keys alphabetically)
	firstResult := results[0]
	assert.Contains(t, firstResult, "name: test-vmcp")
	assert.Contains(t, firstResult, "groupRef: test-group")

	// Verify the YAML is valid and non-empty
	assert.NotEmpty(t, firstResult)
	assert.Greater(t, len(firstResult), 100, "YAML output should contain substantial content")

	t.Logf("All %d marshaling iterations produced identical output (%d bytes)",
		iterations, len(results[0]))
}

// TestVirtualMCPServerReconciler_CompositeToolRefs_EndToEnd tests the complete end-to-end flow
// of CompositeToolRefs resolution: creating a VirtualMCPCompositeToolDefinition, referencing it
// from a VirtualMCPServer, and verifying it's included in the generated ConfigMap.
func TestVirtualMCPServerReconciler_CompositeToolRefs_EndToEnd(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	testScheme := createRunConfigTestScheme()

	// Create a VirtualMCPCompositeToolDefinition
	compositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-composite-tool",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
			CompositeToolConfig: vmcpconfig.CompositeToolConfig{
				Name:        "test-composite-tool",
				Description: "A test composite tool definition",
				Parameters: thvjson.NewMap(map[string]any{
					"type": "object",
					"properties": map[string]any{
						"message": map[string]any{"type": "string"},
					},
				}),
				Timeout: vmcpconfig.Duration(30 * time.Second),
				Steps: []vmcpconfig.WorkflowStepConfig{
					{
						ID:        "step1",
						Type:      "tool",
						Tool:      "backend.echo",
						Arguments: thvjson.NewMap(map[string]any{"input": "{{ .params.message }}"}),
					},
				},
			},
		},
	}

	// Create MCPGroup
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPGroupSpec{},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	// Create VirtualMCPServer that references the composite tool
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{
				Group: "test-group",
				CompositeToolRefs: []vmcpconfig.CompositeToolRef{
					{Name: "test-composite-tool"},
				},
			},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
		},
	}

	// Create fake client with all resources
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(vmcpServer, mcpGroup, compositeToolDef).
		Build()

	// Create reconciler
	reconciler := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: testScheme,
	}

	// Fetch workload names (matching production behavior)
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(fakeClient, vmcpServer.Namespace)
	workloadNames, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, vmcpServer.Spec.Config.Group)
	require.NoError(t, err, "should successfully list workloads in group")

	// Test the ensureVmcpConfigConfigMap function
	statusCollector := virtualmcpserverstatus.NewStatusManager(vmcpServer)
	err = reconciler.ensureVmcpConfigConfigMap(ctx, vmcpServer, workloadNames, statusCollector)
	require.NoError(t, err, "should successfully create ConfigMap with referenced composite tool")

	// Verify ConfigMap was created
	configMap := &corev1.ConfigMap{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      vmcpConfigMapName("test-vmcp"),
		Namespace: "default",
	}, configMap)
	require.NoError(t, err, "ConfigMap should exist")

	// Verify ConfigMap contains the config
	require.Contains(t, configMap.Data, "config.yaml", "ConfigMap should contain config.yaml")

	// Parse the YAML config
	var config vmcpconfig.Config
	err = yaml.Unmarshal([]byte(configMap.Data["config.yaml"]), &config)
	require.NoError(t, err, "should parse config YAML")

	// Verify the referenced composite tool is included
	require.Len(t, config.CompositeTools, 1, "should have one composite tool")
	assert.Equal(t, "test-composite-tool", config.CompositeTools[0].Name)
	assert.Equal(t, "A test composite tool definition", config.CompositeTools[0].Description)
	require.Len(t, config.CompositeTools[0].Steps, 1)
	assert.Equal(t, "step1", config.CompositeTools[0].Steps[0].ID)
	assert.Equal(t, "backend.echo", config.CompositeTools[0].Steps[0].Tool)
	assert.Equal(t, vmcpconfig.Duration(30*time.Second), config.CompositeTools[0].Timeout)

	// Verify parameters were converted
	require.NotNil(t, config.CompositeTools[0].Parameters)
	paramsMap, err := config.CompositeTools[0].Parameters.ToMap()
	require.NoError(t, err)
	assert.Equal(t, "object", paramsMap["type"])
}

// TestVirtualMCPServerReconciler_CompositeToolRefs_MergeInlineAndReferenced tests merging
// inline CompositeTools with referenced CompositeToolRefs.
func TestVirtualMCPServerReconciler_CompositeToolRefs_MergeInlineAndReferenced(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	testScheme := createRunConfigTestScheme()

	// Create a referenced VirtualMCPCompositeToolDefinition
	referencedTool := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "referenced-tool",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
			CompositeToolConfig: vmcpconfig.CompositeToolConfig{
				Name:        "referenced-tool",
				Description: "A referenced composite tool",
				Steps: []vmcpconfig.WorkflowStepConfig{
					{
						ID:   "step1",
						Type: "tool",
						Tool: "backend.referenced",
					},
				},
			},
		},
	}

	// Create MCPGroup
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPGroupSpec{},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	// Create VirtualMCPServer with both inline and referenced tools
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{
				Group: "test-group",
				CompositeTools: []vmcpconfig.CompositeToolConfig{
					{
						Name:        "inline-tool",
						Description: "An inline composite tool",
						Steps: []vmcpconfig.WorkflowStepConfig{
							{
								ID:   "step1",
								Type: "tool",
								Tool: "backend.inline",
							},
						},
					},
				},
				CompositeToolRefs: []vmcpconfig.CompositeToolRef{
					{Name: "referenced-tool"},
				},
			},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
		},
	}

	// Create fake client
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(vmcpServer, mcpGroup, referencedTool).
		Build()

	// Create reconciler
	reconciler := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: testScheme,
	}

	// Fetch workload names (matching production behavior)
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(fakeClient, vmcpServer.Namespace)
	workloadNames, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, vmcpServer.Spec.Config.Group)
	require.NoError(t, err, "should successfully list workloads in group")

	// Test the ensureVmcpConfigConfigMap function
	statusCollector := virtualmcpserverstatus.NewStatusManager(vmcpServer)
	err = reconciler.ensureVmcpConfigConfigMap(ctx, vmcpServer, workloadNames, statusCollector)
	require.NoError(t, err, "should successfully merge inline and referenced tools")

	// Verify ConfigMap was created
	configMap := &corev1.ConfigMap{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      vmcpConfigMapName("test-vmcp"),
		Namespace: "default",
	}, configMap)
	require.NoError(t, err, "ConfigMap should exist")

	// Parse the YAML config
	var config vmcpconfig.Config
	err = yaml.Unmarshal([]byte(configMap.Data["config.yaml"]), &config)
	require.NoError(t, err, "should parse config YAML")

	// Verify both tools are present
	require.Len(t, config.CompositeTools, 2, "should have both inline and referenced tools")
	toolNames := make(map[string]bool)
	for _, tool := range config.CompositeTools {
		toolNames[tool.Name] = true
	}
	assert.True(t, toolNames["inline-tool"], "inline-tool should be present")
	assert.True(t, toolNames["referenced-tool"], "referenced-tool should be present")
}

// TestVirtualMCPServerReconciler_CompositeToolRefs_NotFound tests error handling
// when a referenced VirtualMCPCompositeToolDefinition doesn't exist.
func TestVirtualMCPServerReconciler_CompositeToolRefs_NotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	testScheme := createRunConfigTestScheme()

	// Create MCPGroup
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPGroupSpec{},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	// Create VirtualMCPServer that references a non-existent composite tool
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{
				Group: "test-group",
				CompositeToolRefs: []vmcpconfig.CompositeToolRef{
					{Name: "non-existent-tool"},
				},
			},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
		},
	}

	// Create fake client WITHOUT the referenced tool
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(vmcpServer, mcpGroup).
		Build()

	// Create reconciler
	reconciler := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: testScheme,
	}

	// Fetch workload names (matching production behavior)
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(fakeClient, vmcpServer.Namespace)
	workloadNames, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, vmcpServer.Spec.Config.Group)
	require.NoError(t, err, "should successfully list workloads in group")

	// Test should fail with not found error
	statusCollector := virtualmcpserverstatus.NewStatusManager(vmcpServer)
	err = reconciler.ensureVmcpConfigConfigMap(ctx, vmcpServer, workloadNames, statusCollector)
	require.Error(t, err, "should fail when referenced tool doesn't exist")
	assert.Contains(t, err.Error(), "not found", "error should mention not found")
}

// TestConfigMapContent_DynamicMode tests that in dynamic mode (discovered),
// the ConfigMap contains minimal content without backends
func TestConfigMapContent_DynamicMode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	testScheme := createRunConfigTestScheme()

	// Create MCPGroup for workload discovery
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPGroupSpec{},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	// Create VirtualMCPServer in dynamic mode (source: discovered)
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{Group: "test-group"},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
			OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
				Source: "discovered", // Dynamic mode
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(vmcpServer, mcpGroup).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: testScheme,
	}

	// Discover workloads
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(fakeClient, vmcpServer.Namespace)
	workloadNames, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, vmcpServer.Spec.Config.Group)
	require.NoError(t, err)

	// Create ConfigMap
	statusCollector := virtualmcpserverstatus.NewStatusManager(vmcpServer)
	err = reconciler.ensureVmcpConfigConfigMap(ctx, vmcpServer, workloadNames, statusCollector)
	require.NoError(t, err)

	// Verify ConfigMap was created
	configMap := &corev1.ConfigMap{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      vmcpConfigMapName("test-vmcp"),
		Namespace: "default",
	}, configMap)
	require.NoError(t, err)

	// Parse the YAML config
	var config vmcpconfig.Config
	err = yaml.Unmarshal([]byte(configMap.Data["config.yaml"]), &config)
	require.NoError(t, err)

	// In dynamic mode, ConfigMap should have minimal content:
	// - OutgoingAuth with source: discovered
	// - No auth backends in OutgoingAuth (vMCP discovers at runtime)
	// - No static backends in Backends (vMCP discovers at runtime)
	require.NotNil(t, config.OutgoingAuth)
	assert.Equal(t, "discovered", config.OutgoingAuth.Source, "source should be discovered")
	assert.Empty(t, config.OutgoingAuth.Backends, "auth backends should be empty in dynamic mode")
	assert.Empty(t, config.Backends, "static backends should be empty in dynamic mode")

	t.Log("Dynamic mode ConfigMap contains minimal content without backends")
}

// TestConfigMapContent_StaticMode_InlineOverrides tests that in static mode (inline),
// explicitly specified backends in the spec are preserved in the ConfigMap.
// This tests inline overrides, not discovery. See TestConfigMapContent_StaticModeWithDiscovery
// for testing actual backend discovery from MCPServers in the group.
func TestConfigMapContent_StaticMode_InlineOverrides(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	testScheme := createRunConfigTestScheme()

	// Create MCPGroup for workload discovery
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPGroupSpec{},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	// Create MCPServer in the group so static mode has something to discover
	// This is needed because static mode validates that at least one backend exists
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			GroupRef:  "test-group",
			Transport: "sse", // Required for backend discovery
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
			URL:   "http://test-backend.default.svc.cluster.local:8080",
		},
	}

	// Create VirtualMCPServer in static mode (source: inline)
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{Group: "test-group"},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
			OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
				Source: "inline", // Static mode
				Backends: map[string]mcpv1alpha1.BackendAuthConfig{
					"test-backend": {
						Type: mcpv1alpha1.BackendAuthTypeDiscovered,
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(vmcpServer, mcpGroup, mcpServer).
		WithStatusSubresource(mcpServer).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: testScheme,
	}

	// Discover workloads
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(fakeClient, vmcpServer.Namespace)
	workloadNames, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, vmcpServer.Spec.Config.Group)
	require.NoError(t, err)

	// Create ConfigMap
	statusCollector := virtualmcpserverstatus.NewStatusManager(vmcpServer)
	err = reconciler.ensureVmcpConfigConfigMap(ctx, vmcpServer, workloadNames, statusCollector)
	require.NoError(t, err)

	// Verify ConfigMap was created
	configMap := &corev1.ConfigMap{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      vmcpConfigMapName("test-vmcp"),
		Namespace: "default",
	}, configMap)
	require.NoError(t, err)

	// Parse the YAML config
	var config vmcpconfig.Config
	err = yaml.Unmarshal([]byte(configMap.Data["config.yaml"]), &config)
	require.NoError(t, err)

	// In static mode with inline backends, ConfigMap should preserve them:
	// - OutgoingAuth with source: inline
	// - Backends from spec.outgoingAuth.backends are included
	require.NotNil(t, config.OutgoingAuth)
	assert.Equal(t, "inline", config.OutgoingAuth.Source, "source should be inline")
	require.NotEmpty(t, config.OutgoingAuth.Backends, "backends should be present in static mode")

	// Verify the inline backend from spec is present
	_, exists := config.OutgoingAuth.Backends["test-backend"]
	assert.True(t, exists, "inline backend from spec should be present in ConfigMap")

	t.Log("Static mode ConfigMap preserves inline backend overrides from spec")
}

// TestConfigMapContent_StaticModeWithDiscovery tests that in static mode (inline),
// the ConfigMap contains discovered backend auth configs from MCPServer ExternalAuthConfigRefs
func TestConfigMapContent_StaticModeWithDiscovery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	testScheme := createRunConfigTestScheme()

	// Create MCPGroup for workload discovery
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPGroupSpec{},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: mcpv1alpha1.MCPGroupPhaseReady,
		},
	}

	// Create MCPExternalAuthConfig that will be referenced by MCPServer
	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-auth-config",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeUnauthenticated,
		},
	}

	// Create MCPServer with ExternalAuthConfigRef and Status
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "discovered-backend",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			GroupRef:  "test-group",
			Transport: "sse", // Required for static mode backend discovery
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-auth-config",
			},
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
			URL:   "http://discovered-backend.default.svc.cluster.local:8080",
		},
	}

	// Create VirtualMCPServer in static mode (source: inline) WITHOUT inline backends
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{Group: "test-group"},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
			OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
				Source: "inline", // Static mode - should discover backends
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(vmcpServer, mcpGroup, mcpServer, externalAuthConfig).
		WithStatusSubresource(mcpServer).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: testScheme,
	}

	// Discover workloads
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(fakeClient, vmcpServer.Namespace)
	workloadNames, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, vmcpServer.Spec.Config.Group)
	require.NoError(t, err)
	require.NotEmpty(t, workloadNames, "should have discovered the MCPServer")

	// Create ConfigMap
	statusCollector := virtualmcpserverstatus.NewStatusManager(vmcpServer)
	err = reconciler.ensureVmcpConfigConfigMap(ctx, vmcpServer, workloadNames, statusCollector)
	require.NoError(t, err)

	// Verify ConfigMap was created
	configMap := &corev1.ConfigMap{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      vmcpConfigMapName("test-vmcp"),
		Namespace: "default",
	}, configMap)
	require.NoError(t, err)

	// Parse the YAML config
	var config vmcpconfig.Config
	err = yaml.Unmarshal([]byte(configMap.Data["config.yaml"]), &config)
	require.NoError(t, err)

	// In static mode with discovery, ConfigMap should have:
	// - OutgoingAuth with source: inline and auth configs
	// - Backends populated with URLs and transport types for zero-K8s-access mode
	require.NotNil(t, config.OutgoingAuth)
	assert.Equal(t, "inline", config.OutgoingAuth.Source, "source should be inline")
	require.NotEmpty(t, config.OutgoingAuth.Backends, "backends should be discovered in static mode")

	// Verify the discovered backend auth config is present
	discoveredBackend, exists := config.OutgoingAuth.Backends["discovered-backend"]
	require.True(t, exists, "discovered backend should be present in ConfigMap")
	require.NotNil(t, discoveredBackend, "discovered backend should have auth strategy")
	assert.Equal(t, "unauthenticated", discoveredBackend.Type, "backend should have correct auth type")

	// Verify static backend configurations (URLs + transport) are populated
	require.NotEmpty(t, config.Backends, "static backends with URLs should be populated in static mode")

	// Find the discovered backend in the static backend list
	var foundBackend *vmcpconfig.StaticBackendConfig
	for i := range config.Backends {
		if config.Backends[i].Name == "discovered-backend" {
			foundBackend = &config.Backends[i]
			break
		}
	}
	require.NotNil(t, foundBackend, "discovered backend should be in static backends list")
	assert.NotEmpty(t, foundBackend.URL, "backend should have URL populated")
	assert.NotEmpty(t, foundBackend.Transport, "backend should have transport type populated")

	// Verify metadata is preserved (group, tool_type, workload_type, namespace)
	require.NotNil(t, foundBackend.Metadata, "backend should have metadata")
	assert.Equal(t, "test-group", foundBackend.Metadata["group"], "backend should have group metadata")
	assert.Equal(t, "mcp", foundBackend.Metadata["tool_type"], "backend should have tool_type metadata")
	assert.Equal(t, "mcp_server", foundBackend.Metadata["workload_type"], "backend should have workload_type metadata")
	assert.Equal(t, "default", foundBackend.Metadata["namespace"], "backend should have namespace metadata")

	t.Log("Static mode ConfigMap contains both auth configs, backend URLs/transports, and metadata")
}

// TestConvertBackendsToStaticBackends_SkipsInvalidBackends tests that backends
// without URL or transport are skipped with appropriate logging
func TestConvertBackendsToStaticBackends_SkipsInvalidBackends(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	backends := []vmcp.Backend{
		{
			Name:          "valid-backend",
			BaseURL:       "http://backend1:8080",
			TransportType: "sse",
			Metadata:      map[string]string{"key": "value"},
		},
		{
			Name:          "no-url-backend",
			BaseURL:       "", // Missing URL
			TransportType: "sse",
		},
		{
			Name:    "no-transport-backend",
			BaseURL: "http://backend2:8080",
			// Transport will be missing from map
		},
	}

	transportMap := map[string]string{
		"valid-backend":  "sse",
		"no-url-backend": "streamable-http",
		// "no-transport-backend" intentionally missing
	}

	result := convertBackendsToStaticBackends(ctx, backends, transportMap)

	// Should only include the valid backend
	assert.Len(t, result, 1, "should only include backends with URL and transport")
	assert.Equal(t, "valid-backend", result[0].Name)
	assert.Equal(t, "http://backend1:8080", result[0].URL)
	assert.Equal(t, "sse", result[0].Transport)
	assert.Equal(t, "value", result[0].Metadata["key"])
}

// TestStaticModeTransportConstants verifies that the transport constants match the CRD enum.
// This test ensures consistency between runtime validation and CRD schema validation.
func TestStaticModeTransportConstants(t *testing.T) {
	t.Parallel()

	// Define the expected transports that should be in the CRD enum.
	// If this test fails, it means the CRD enum in StaticBackendConfig.Transport
	// is out of sync with vmcpconfig.StaticModeAllowedTransports.
	expectedTransports := []string{vmcpconfig.TransportSSE, vmcpconfig.TransportStreamableHTTP}

	// Verify the slice matches exactly
	assert.ElementsMatch(t, expectedTransports, vmcpconfig.StaticModeAllowedTransports,
		"StaticModeAllowedTransports must match the transport constants")

	// Verify individual constants have expected values
	assert.Equal(t, "sse", vmcpconfig.TransportSSE, "TransportSSE constant value")
	assert.Equal(t, "streamable-http", vmcpconfig.TransportStreamableHTTP, "TransportStreamableHTTP constant value")

	// NOTE: When updating allowed transports:
	// 1. Update the constants in pkg/vmcp/config/config.go
	// 2. Update the CRD enum in StaticBackendConfig.Transport: +kubebuilder:validation:Enum=...
	// 3. Run: task operator-generate && task operator-manifests
	// 4. This test will verify the constants match the expected values
}

// TestOptimizerEmbeddingServiceURL tests that the optimizer's EmbeddingService
// field is populated with the full base URL (scheme + host + port) from the EmbeddingServer
// Status.URL. This ensures the optimizer can use it directly as an HTTP client endpoint.
func TestOptimizerEmbeddingServiceURL(t *testing.T) {
	t.Parallel()

	const (
		testNamespace       = "default"
		testGroup           = "test-group"
		customPort    int32 = 9090
	)

	tests := []struct {
		name        string
		vmcp        *mcpv1alpha1.VirtualMCPServer
		esName      string
		esPort      int32
		expectedURL string
	}{
		{
			name: "referenced embedding server populates full URL",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-vmcp",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group:     testGroup,
						Optimizer: &vmcpconfig.OptimizerConfig{},
					},
					EmbeddingServerRef: &mcpv1alpha1.EmbeddingServerRef{
						Name: "shared-embedding",
					},
				},
			},
			esName:      "shared-embedding",
			esPort:      customPort,
			expectedURL: "http://shared-embedding.default.svc.cluster.local:9090",
		},
		{
			name: "no embedding server leaves EmbeddingService empty",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-vmcp",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group:     testGroup,
						Optimizer: &vmcpconfig.OptimizerConfig{},
					},
					// No EmbeddingServer or EmbeddingServerRef
				},
			},
			expectedURL: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			testScheme := createRunConfigTestScheme()

			mcpGroup := &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testGroup,
					Namespace: testNamespace,
				},
				Spec:   mcpv1alpha1.MCPGroupSpec{},
				Status: mcpv1alpha1.MCPGroupStatus{Phase: mcpv1alpha1.MCPGroupPhaseReady},
			}

			objects := []runtime.Object{tt.vmcp, mcpGroup}

			// Create the EmbeddingServer with Status.URL if one is expected
			if tt.esName != "" {
				es := &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tt.esName,
						Namespace: testNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Image: "ghcr.io/huggingface/text-embeddings-inference:cpu-1.5",
						Model: "BAAI/bge-small-en-v1.5",
						Port:  tt.esPort,
					},
					Status: mcpv1alpha1.EmbeddingServerStatus{
						Phase:         mcpv1alpha1.EmbeddingServerPhaseRunning,
						ReadyReplicas: 1,
						URL: fmt.Sprintf("http://%s.%s.svc.cluster.local:%d",
							tt.esName, testNamespace, tt.esPort),
					},
				}
				objects = append(objects, es)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(objects...).
				Build()

			reconciler := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: testScheme,
			}

			workloadDiscoverer := workloads.NewK8SDiscovererWithClient(fakeClient, testNamespace)
			workloadNames, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, testGroup)
			require.NoError(t, err)

			err = reconciler.ensureVmcpConfigConfigMap(ctx, tt.vmcp, workloadNames)
			require.NoError(t, err)

			// Read back the ConfigMap and parse the config
			configMap := &corev1.ConfigMap{}
			err = fakeClient.Get(ctx, types.NamespacedName{
				Name:      vmcpConfigMapName(tt.vmcp.Name),
				Namespace: testNamespace,
			}, configMap)
			require.NoError(t, err)

			var config vmcpconfig.Config
			err = yaml.Unmarshal([]byte(configMap.Data["config.yaml"]), &config)
			require.NoError(t, err)

			require.NotNil(t, config.Optimizer, "Optimizer config should be present")
			assert.Equal(t, tt.expectedURL, config.Optimizer.EmbeddingService,
				"EmbeddingService should contain the full base URL from EmbeddingServer Status.URL")
		})
	}
}

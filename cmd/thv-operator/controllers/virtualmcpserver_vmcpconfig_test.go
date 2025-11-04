// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

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
					GroupRef: mcpv1alpha1.GroupRef{
						Name: "test-group",
					},
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

			r := &VirtualMCPServerReconciler{}
			config, err := r.createVmcpConfigFromVirtualMCPServer(context.Background(), tt.vmcp)

			require.NoError(t, err)
			assert.NotNil(t, config)
			assert.Equal(t, tt.expectedName, config.Name)
			assert.Equal(t, tt.expectedGroupRef, config.GroupRef)
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
					Type: mcpv1alpha1.BackendAuthTypePassThrough,
				},
			},
			expectedSource: "inline",
			hasDefault:     true,
			backendCount:   0,
		},
		{
			name: "with per-backend auth",
			outgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
				Source: "mixed",
				Backends: map[string]mcpv1alpha1.BackendAuthConfig{
					"backend-1": {
						Type: mcpv1alpha1.BackendAuthTypeServiceAccount,
						ServiceAccount: &mcpv1alpha1.ServiceAccountAuth{
							CredentialsRef: mcpv1alpha1.SecretKeyRef{
								Name: "sa-secret",
								Key:  "token",
							},
						},
					},
				},
			},
			expectedSource: "mixed",
			hasDefault:     false,
			backendCount:   1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					OutgoingAuth: tt.outgoingAuth,
				},
			}

			r := &VirtualMCPServerReconciler{}
			config := r.convertOutgoingAuth(context.Background(), vmcp)

			require.NotNil(t, config)
			assert.Equal(t, tt.expectedSource, config.Source)

			if tt.hasDefault {
				assert.NotNil(t, config.Default)
			}

			assert.Len(t, config.Backends, tt.backendCount)
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
		hasMetadata  bool
	}{
		{
			name: "pass through",
			authConfig: &mcpv1alpha1.BackendAuthConfig{
				Type: mcpv1alpha1.BackendAuthTypePassThrough,
			},
			expectedType: mcpv1alpha1.BackendAuthTypePassThrough,
			hasMetadata:  false,
		},
		{
			name: "service account",
			authConfig: &mcpv1alpha1.BackendAuthConfig{
				Type: mcpv1alpha1.BackendAuthTypeServiceAccount,
				ServiceAccount: &mcpv1alpha1.ServiceAccountAuth{
					CredentialsRef: mcpv1alpha1.SecretKeyRef{
						Name: "secret-name",
						Key:  "token",
					},
					HeaderName:   "X-Auth-Token",
					HeaderFormat: "Bearer {token}",
				},
			},
			expectedType: mcpv1alpha1.BackendAuthTypeServiceAccount,
			hasMetadata:  true,
		},
		{
			name: "external auth config ref",
			authConfig: &mcpv1alpha1.BackendAuthConfig{
				Type: mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef,
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: "auth-config",
				},
			},
			expectedType: mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef,
			hasMetadata:  true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &VirtualMCPServerReconciler{}
			strategy := r.convertBackendAuthConfig(tt.authConfig)

			require.NotNil(t, strategy)
			assert.Equal(t, tt.expectedType, strategy.Type)

			if tt.hasMetadata {
				assert.NotEmpty(t, strategy.Metadata)
			}
		})
	}
}

// TestConvertAggregation tests aggregation config conversion
func TestConvertAggregation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		aggregation            *mcpv1alpha1.AggregationConfig
		expectedStrategy       string
		hasPrefixFormat        bool
		hasPriorityOrder       bool
		expectedToolConfigCount int
	}{
		{
			name: "prefix strategy",
			aggregation: &mcpv1alpha1.AggregationConfig{
				ConflictResolution: mcpv1alpha1.ConflictResolutionPrefix,
				ConflictResolutionConfig: &mcpv1alpha1.ConflictResolutionConfig{
					PrefixFormat: "{workload}_",
				},
			},
			expectedStrategy: "prefix",
			hasPrefixFormat:  true,
		},
		{
			name: "priority strategy",
			aggregation: &mcpv1alpha1.AggregationConfig{
				ConflictResolution: mcpv1alpha1.ConflictResolutionPriority,
				ConflictResolutionConfig: &mcpv1alpha1.ConflictResolutionConfig{
					PriorityOrder: []string{"backend-1", "backend-2"},
				},
			},
			expectedStrategy: "priority",
			hasPriorityOrder: true,
		},
		{
			name: "with tool configs",
			aggregation: &mcpv1alpha1.AggregationConfig{
				ConflictResolution: mcpv1alpha1.ConflictResolutionPrefix,
				Tools: []mcpv1alpha1.WorkloadToolConfig{
					{
						Workload: "backend-1",
						Filter:   []string{"tool1", "tool2"},
					},
					{
						Workload: "backend-2",
						Overrides: map[string]mcpv1alpha1.ToolOverride{
							"tool3": {
								Name:        "renamed_tool3",
								Description: "Updated description",
							},
						},
					},
				},
			},
			expectedStrategy:        "prefix",
			expectedToolConfigCount: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Aggregation: tt.aggregation,
				},
			}

			r := &VirtualMCPServerReconciler{}
			config := r.convertAggregation(context.Background(), vmcp)

			require.NotNil(t, config)
			assert.Equal(t, tt.expectedStrategy, config.ConflictResolution)

			if tt.hasPrefixFormat {
				require.NotNil(t, config.ConflictResolutionConfig)
				assert.NotEmpty(t, config.ConflictResolutionConfig.PrefixFormat)
			}

			if tt.hasPriorityOrder {
				require.NotNil(t, config.ConflictResolutionConfig)
				assert.NotEmpty(t, config.ConflictResolutionConfig.PriorityOrder)
			}

			if tt.expectedToolConfigCount > 0 {
				assert.Len(t, config.Tools, tt.expectedToolConfigCount)
			}
		})
	}
}

// TestConvertCompositeTools tests composite tool conversion
func TestConvertCompositeTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		compositeTools []mcpv1alpha1.CompositeToolSpec
		expectedCount int
	}{
		{
			name: "single composite tool",
			compositeTools: []mcpv1alpha1.CompositeToolSpec{
				{
					Name:        "deploy_workflow",
					Description: "Deploy and verify",
					Timeout:     "10m",
					Steps: []mcpv1alpha1.WorkflowStep{
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
			compositeTools: []mcpv1alpha1.CompositeToolSpec{
				{
					Name:        "workflow1",
					Description: "Workflow 1",
					Steps: []mcpv1alpha1.WorkflowStep{
						{
							ID:   "step1",
							Type: mcpv1alpha1.WorkflowStepTypeToolCall,
						},
					},
				},
				{
					Name:        "workflow2",
					Description: "Workflow 2",
					Steps: []mcpv1alpha1.WorkflowStep{
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

			vmcp := &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					CompositeTools: tt.compositeTools,
				},
			}

			r := &VirtualMCPServerReconciler{}
			tools := r.convertCompositeTools(context.Background(), vmcp)

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

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp).
		Build()

	r := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	err := r.ensureVmcpConfigConfigMap(context.Background(), vmcp)
	require.NoError(t, err)

	// Verify ConfigMap was created
	cm := &corev1.ConfigMap{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-vmcp-vmcp-config",
		Namespace: "default",
	}, cm)
	require.NoError(t, err)
	assert.Equal(t, "test-vmcp-vmcp-config", cm.Name)
	assert.Contains(t, cm.Data, "config.json")
	assert.NotEmpty(t, cm.Annotations["toolhive.stacklok.dev/content-checksum"])
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

			r := &VirtualMCPServerReconciler{}

			// Type assertion will fail for nil, which is expected
			if tt.config == nil {
				err := r.validateVmcpConfig(context.Background(), nil)
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

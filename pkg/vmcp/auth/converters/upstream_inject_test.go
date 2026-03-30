// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestUpstreamInjectConverter_StrategyType(t *testing.T) {
	t.Parallel()

	converter := &UpstreamInjectConverter{}
	assert.Equal(t, authtypes.StrategyTypeUpstreamInject, converter.StrategyType())
}

func TestUpstreamInjectConverter_ConvertToStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		wantStrategy *authtypes.BackendAuthStrategy
		wantErr      bool
		errContains  string
	}{
		{
			name: "valid config with ProviderName=github",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-upstream-inject",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeUpstreamInject,
					UpstreamInject: &mcpv1alpha1.UpstreamInjectSpec{
						ProviderName: "github",
					},
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: &authtypes.UpstreamInjectConfig{
					ProviderName: "github",
				},
			},
			wantErr: false,
		},
		{
			name: "nil upstream inject spec",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:           mcpv1alpha1.ExternalAuthTypeUpstreamInject,
					UpstreamInject: nil,
				},
			},
			wantErr:     true,
			errContains: "upstream inject config is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter := &UpstreamInjectConverter{}
			strategy, err := converter.ConvertToStrategy(tt.externalAuth)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantStrategy, strategy)
		})
	}
}

func TestUpstreamInjectConverter_ResolveSecrets(t *testing.T) {
	t.Parallel()

	converter := &UpstreamInjectConverter{}
	externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-upstream-inject",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeUpstreamInject,
			UpstreamInject: &mcpv1alpha1.UpstreamInjectSpec{
				ProviderName: "github",
			},
		},
	}

	strategy := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeUpstreamInject,
		UpstreamInject: &authtypes.UpstreamInjectConfig{
			ProviderName: "github",
		},
	}

	// ResolveSecrets should be a no-op for upstream inject
	resolvedStrategy, err := converter.ResolveSecrets(context.Background(), externalAuth, nil, "default", strategy)

	require.NoError(t, err)
	require.NotNil(t, resolvedStrategy)
	assert.Equal(t, strategy, resolvedStrategy, "Strategy should be unchanged")
	assert.Equal(t, authtypes.StrategyTypeUpstreamInject, resolvedStrategy.Type)
}

func TestUpstreamInjectConverter_Integration(t *testing.T) {
	t.Parallel()

	// Test that upstream inject converter is registered in default registry
	registry := DefaultRegistry()
	converter, err := registry.GetConverter(mcpv1alpha1.ExternalAuthTypeUpstreamInject)
	require.NoError(t, err)
	require.NotNil(t, converter)
	assert.IsType(t, &UpstreamInjectConverter{}, converter)

	// Test end-to-end conversion using ConvertToStrategy convenience function
	externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-upstream-inject",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeUpstreamInject,
			UpstreamInject: &mcpv1alpha1.UpstreamInjectSpec{
				ProviderName: "github",
			},
		},
	}

	strategy, err := ConvertToStrategy(externalAuth)
	require.NoError(t, err)
	require.NotNil(t, strategy)
	assert.Equal(t, authtypes.StrategyTypeUpstreamInject, strategy.Type)
	require.NotNil(t, strategy.UpstreamInject)
	assert.Equal(t, "github", strategy.UpstreamInject.ProviderName)
	assert.Nil(t, strategy.TokenExchange)
	assert.Nil(t, strategy.HeaderInjection)
}

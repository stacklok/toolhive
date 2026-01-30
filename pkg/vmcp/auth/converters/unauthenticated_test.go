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

func TestUnauthenticatedConverter_StrategyType(t *testing.T) {
	t.Parallel()

	converter := &UnauthenticatedConverter{}
	assert.Equal(t, authtypes.StrategyTypeUnauthenticated, converter.StrategyType())
}

func TestUnauthenticatedConverter_ConvertToStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		externalAuth  *mcpv1alpha1.MCPExternalAuthConfig
		expectedType  string
		expectedError bool
	}{
		{
			name: "valid unauthenticated config",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-unauthenticated",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeUnauthenticated,
				},
			},
			expectedType:  authtypes.StrategyTypeUnauthenticated,
			expectedError: false,
		},
		{
			name: "unauthenticated with no extra fields",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-unauthenticated-minimal",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeUnauthenticated,
					// No TokenExchange or HeaderInjection
				},
			},
			expectedType:  authtypes.StrategyTypeUnauthenticated,
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter := &UnauthenticatedConverter{}
			strategy, err := converter.ConvertToStrategy(tt.externalAuth)

			if tt.expectedError {
				require.Error(t, err)
				assert.Nil(t, strategy)
			} else {
				require.NoError(t, err)
				require.NotNil(t, strategy)
				assert.Equal(t, tt.expectedType, strategy.Type)
				// Verify no auth-specific fields are set
				assert.Nil(t, strategy.TokenExchange)
				assert.Nil(t, strategy.HeaderInjection)
			}
		})
	}
}

func TestUnauthenticatedConverter_ResolveSecrets(t *testing.T) {
	t.Parallel()

	converter := &UnauthenticatedConverter{}
	externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-unauthenticated",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeUnauthenticated,
		},
	}

	strategy := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeUnauthenticated,
	}

	// ResolveSecrets should be a no-op for unauthenticated
	resolvedStrategy, err := converter.ResolveSecrets(context.Background(), externalAuth, nil, "default", strategy)

	require.NoError(t, err)
	require.NotNil(t, resolvedStrategy)
	assert.Equal(t, strategy, resolvedStrategy, "Strategy should be unchanged")
	assert.Equal(t, authtypes.StrategyTypeUnauthenticated, resolvedStrategy.Type)
}

func TestUnauthenticatedConverter_Integration(t *testing.T) {
	t.Parallel()

	// Test that unauthenticated converter is registered in default registry
	registry := DefaultRegistry()
	converter, err := registry.GetConverter(mcpv1alpha1.ExternalAuthTypeUnauthenticated)
	require.NoError(t, err)
	require.NotNil(t, converter)
	assert.IsType(t, &UnauthenticatedConverter{}, converter)

	// Test end-to-end conversion using ConvertToStrategy convenience function
	externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-unauthenticated",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeUnauthenticated,
		},
	}

	strategy, err := ConvertToStrategy(externalAuth)
	require.NoError(t, err)
	require.NotNil(t, strategy)
	assert.Equal(t, authtypes.StrategyTypeUnauthenticated, strategy.Type)
	assert.Nil(t, strategy.TokenExchange)
	assert.Nil(t, strategy.HeaderInjection)
}

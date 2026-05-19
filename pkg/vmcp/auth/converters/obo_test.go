// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestOBOConverterStub_StrategyType(t *testing.T) {
	t.Parallel()

	assert.Equal(t, authtypes.StrategyTypeOBO, (&OBOConverterStub{}).StrategyType())
}

func TestOBOConverterStub_ConvertToStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		externalCfg *mcpv1beta1.MCPExternalAuthConfig
	}{
		{name: "nil config", externalCfg: nil},
		{
			name: "obo-typed config",
			externalCfg: &mcpv1beta1.MCPExternalAuthConfig{
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeOBO,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy, err := (&OBOConverterStub{}).ConvertToStrategy(tt.externalCfg)
			require.Error(t, err)
			assert.Nil(t, strategy, "stub must not return a strategy on the error path")
			assert.ErrorIs(t, err, controllerutil.ErrEnterpriseRequired)
		})
	}
}

func TestOBOConverterStub_ResolveSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		externalCfg *mcpv1beta1.MCPExternalAuthConfig
		strategy    *authtypes.BackendAuthStrategy
	}{
		{name: "nil inputs", externalCfg: nil, strategy: nil},
		{
			name: "non-nil strategy",
			externalCfg: &mcpv1beta1.MCPExternalAuthConfig{
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeOBO,
				},
			},
			strategy: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeOBO},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolved, err := (&OBOConverterStub{}).ResolveSecrets(
				context.Background(), tt.externalCfg, nil, "default", tt.strategy,
			)
			require.Error(t, err)
			assert.Nil(t, resolved, "stub must not return a strategy on the error path")
			assert.ErrorIs(t, err, controllerutil.ErrEnterpriseRequired)
		})
	}
}

func TestOBOConverterStub_RegisteredInDefaultRegistry(t *testing.T) {
	t.Parallel()

	converter, err := NewRegistry().GetConverter(mcpv1beta1.ExternalAuthTypeOBO)
	require.NoError(t, err)
	require.NotNil(t, converter)
	assert.IsType(t, &OBOConverterStub{}, converter)

	// DefaultRegistry should also have it.
	converter, err = DefaultRegistry().GetConverter(mcpv1beta1.ExternalAuthTypeOBO)
	require.NoError(t, err)
	assert.IsType(t, &OBOConverterStub{}, converter)
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestOBOConverter_StrategyType(t *testing.T) {
	t.Parallel()

	assert.Equal(t, authtypes.StrategyTypeOBO, (&OBOConverter{}).StrategyType())
}

func TestOBOConverter_ConvertToStrategy(t *testing.T) {
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
					OBO:  &mcpv1beta1.OBOConfig{},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy, err := (&OBOConverter{}).ConvertToStrategy(tt.externalCfg)
			require.Error(t, err)
			assert.Nil(t, strategy, "stub must not return a strategy on the error path")
			assert.ErrorIs(t, err, obo.ErrEnterpriseRequired)
		})
	}
}

func TestOBOConverter_ResolveSecrets(t *testing.T) {
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
					OBO:  &mcpv1beta1.OBOConfig{},
				},
			},
			strategy: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeOBO},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolved, err := (&OBOConverter{}).ResolveSecrets(
				context.Background(), tt.externalCfg, nil, "default", tt.strategy,
			)
			require.Error(t, err)
			assert.Nil(t, resolved, "stub must not return a strategy on the error path")
			assert.ErrorIs(t, err, obo.ErrEnterpriseRequired)
		})
	}
}

func TestOBOConverter_RegisteredInDefaultRegistry(t *testing.T) {
	t.Parallel()

	converter, err := NewRegistry().GetConverter(mcpv1beta1.ExternalAuthTypeOBO)
	require.NoError(t, err)
	require.NotNil(t, converter)
	assert.IsType(t, &OBOConverter{}, converter)

	// DefaultRegistry should also have it.
	converter, err = DefaultRegistry().GetConverter(mcpv1beta1.ExternalAuthTypeOBO)
	require.NoError(t, err)
	assert.IsType(t, &OBOConverter{}, converter)
}

// TestDiscoverAndResolveAuth_OBO_SentinelSurvivesWrap proves that the
// OBO sentinel propagates through DiscoverAndResolveAuth's
// `"failed to convert to strategy: %w"` wrap at interface.go:191 and
// remains recognizable via errors.Is. The vMCP integration path is
// structurally fragile: matching by the wrap-prefix string would silently
// degrade if the message ever changed. This test is the regression guard.
func TestDiscoverAndResolveAuth_OBO_SentinelSurvivesWrap(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	cfg := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "obo-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO:  &mcpv1beta1.OBOConfig{},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg).
		Build()

	strategy, err := DiscoverAndResolveAuth(
		t.Context(),
		&mcpv1beta1.ExternalAuthConfigRef{Name: cfg.Name},
		cfg.Namespace,
		fakeClient,
	)

	require.Error(t, err)
	assert.Nil(t, strategy)
	assert.ErrorIs(t, err, obo.ErrEnterpriseRequired,
		"errors.Is must match the sentinel through the convert-to-strategy wrap")

	// Sanity: the wrap prefix is present, but recognition above is via
	// errors.Is, not by string matching.
	assert.Contains(t, err.Error(), "failed to convert to strategy")

	// Generic-error guards: the dispatch path must not leak generic strings.
	assert.NotContains(t, err.Error(), "unsupported external auth type")
	assert.NotContains(t, err.Error(), "unknown middleware type")
}

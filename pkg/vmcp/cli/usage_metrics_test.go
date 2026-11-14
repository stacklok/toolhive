// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/usagemetrics"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	aggmocks "github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

func strategy(strategyType string) *authtypes.BackendAuthStrategy {
	return &authtypes.BackendAuthStrategy{Type: strategyType}
}

func TestVmcpFeatures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		optimizer      bool
		codeMode       *config.CodeModeConfig
		compositeTools int
		outgoing       *config.OutgoingAuthConfig
		want           []string
	}{
		{name: "nothing enabled", want: nil},
		{
			name:     "code mode configured but disabled",
			codeMode: &config.CodeModeConfig{Enabled: false},
			outgoing: &config.OutgoingAuthConfig{Default: strategy(authtypes.StrategyTypeUnauthenticated)},
			want:     nil,
		},
		{
			name:           "everything enabled",
			optimizer:      true,
			codeMode:       &config.CodeModeConfig{Enabled: true},
			compositeTools: 2,
			outgoing:       &config.OutgoingAuthConfig{Source: "discovered"},
			want: []string{
				usagemetrics.FeatureOptimizer,
				usagemetrics.FeatureCodeMode,
				usagemetrics.FeatureCompositeTools,
				usagemetrics.FeatureOutgoingAuth,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, vmcpFeatures(tt.optimizer, tt.codeMode, tt.compositeTools, tt.outgoing))
		})
	}
}

func TestUsesOutgoingAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.OutgoingAuthConfig
		want bool
	}{
		{name: "nil config", cfg: nil, want: false},
		{name: "empty config", cfg: &config.OutgoingAuthConfig{}, want: false},
		{name: "discovered", cfg: &config.OutgoingAuthConfig{Source: "discovered"}, want: true},
		{
			name: "unauthenticated default only",
			cfg: &config.OutgoingAuthConfig{
				Source:   "inline",
				Default:  strategy(authtypes.StrategyTypeUnauthenticated),
				Backends: map[string]*authtypes.BackendAuthStrategy{"a": strategy(authtypes.StrategyTypeUnauthenticated)},
			},
			want: false,
		},
		{
			name: "default strategy with an empty type",
			cfg:  &config.OutgoingAuthConfig{Default: strategy("")},
			want: false,
		},
		{
			name: "header injection default",
			cfg:  &config.OutgoingAuthConfig{Source: "inline", Default: strategy(authtypes.StrategyTypeHeaderInjection)},
			want: true,
		},
		{
			name: "per-backend strategy",
			cfg: &config.OutgoingAuthConfig{
				Source:  "inline",
				Default: strategy(authtypes.StrategyTypeUnauthenticated),
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"a": nil,
					"b": strategy(authtypes.StrategyTypeTokenExchange),
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, usesOutgoingAuth(tt.cfg))
		})
	}
}

func TestVmcpAuthMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		incoming *config.IncomingAuthConfig
		embedded bool
		want     string
	}{
		{name: "embedded wins", incoming: &config.IncomingAuthConfig{Type: "oidc"}, embedded: true, want: usagemetrics.AuthModeEmbedded},
		{name: "nil incoming", incoming: nil, want: usagemetrics.ValueNone},
		{name: "empty type", incoming: &config.IncomingAuthConfig{}, want: usagemetrics.ValueNone},
		{name: "anonymous", incoming: &config.IncomingAuthConfig{Type: "anonymous"}, want: usagemetrics.ValueNone},
		{name: "oidc", incoming: &config.IncomingAuthConfig{Type: "oidc"}, want: usagemetrics.AuthModeOIDC},
		{name: "local", incoming: &config.IncomingAuthConfig{Type: "local"}, want: usagemetrics.AuthModeLocal},
		{name: "unknown", incoming: &config.IncomingAuthConfig{Type: "saml"}, want: usagemetrics.ValueOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, vmcpAuthMode(tt.incoming, tt.embedded))
		})
	}
}

func withToolCount(n int) *aggregator.AggregatedCapabilities {
	return &aggregator.AggregatedCapabilities{Metadata: &aggregator.AggregationMetadata{ToolCount: n}}
}

func TestToolCountObserver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	inner := aggmocks.NewMockAggregator(ctrl)
	registry := vmcpmocks.NewMockBackendRegistry(ctrl)

	u := &usageMetrics{}
	u.toolCount.Store(-1)
	observed := u.observe(inner)

	resolved := &aggregator.ResolvedCapabilities{}
	backends := []vmcp.Backend{{ID: "a"}}
	errAgg := errors.New("aggregation failed")

	// Each step calls the observer and checks the recorded count afterwards.
	steps := []struct {
		name   string
		call   func() (*aggregator.AggregatedCapabilities, error)
		expect func()
		want   int64
	}{
		{
			name:   "merge records the tool count",
			expect: func() { inner.EXPECT().MergeCapabilities(ctx, resolved, registry).Return(withToolCount(5), nil) },
			call: func() (*aggregator.AggregatedCapabilities, error) {
				return observed.MergeCapabilities(ctx, resolved, registry)
			},
			want: 5,
		},
		{
			name:   "aggregate records the tool count",
			expect: func() { inner.EXPECT().AggregateCapabilities(ctx, backends).Return(withToolCount(7), nil) },
			call: func() (*aggregator.AggregatedCapabilities, error) {
				return observed.AggregateCapabilities(ctx, backends)
			},
			want: 7,
		},
		{
			name:   "aggregate error keeps the previous count",
			expect: func() { inner.EXPECT().AggregateCapabilities(ctx, backends).Return(withToolCount(1), errAgg) },
			call: func() (*aggregator.AggregatedCapabilities, error) {
				return observed.AggregateCapabilities(ctx, backends)
			},
			want: 7,
		},
		{
			name:   "merge error keeps the previous count",
			expect: func() { inner.EXPECT().MergeCapabilities(ctx, resolved, registry).Return(nil, errAgg) },
			call: func() (*aggregator.AggregatedCapabilities, error) {
				return observed.MergeCapabilities(ctx, resolved, registry)
			},
			want: 7,
		},
		{
			name: "result without metadata keeps the previous count",
			expect: func() {
				inner.EXPECT().MergeCapabilities(ctx, resolved, registry).Return(&aggregator.AggregatedCapabilities{}, nil)
			},
			call: func() (*aggregator.AggregatedCapabilities, error) {
				return observed.MergeCapabilities(ctx, resolved, registry)
			},
			want: 7,
		},
		{
			name:   "zero tools is recorded",
			expect: func() { inner.EXPECT().AggregateCapabilities(ctx, backends).Return(withToolCount(0), nil) },
			call: func() (*aggregator.AggregatedCapabilities, error) {
				return observed.AggregateCapabilities(ctx, backends)
			},
			want: 0,
		},
	}

	for _, step := range steps {
		step.expect()
		_, _ = step.call() // results and errors are passed through unchanged; only the count matters here
		assert.Equal(t, step.want, u.toolCount.Load(), step.name)
	}
}

func TestToolCountObserver_PassesResultsThrough(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	inner := aggmocks.NewMockAggregator(ctrl)
	u := &usageMetrics{}
	observed := u.observe(inner)

	want := withToolCount(3)
	errAgg := errors.New("aggregation failed")
	inner.EXPECT().AggregateCapabilities(ctx, gomock.Nil()).Return(want, nil)
	inner.EXPECT().MergeCapabilities(ctx, gomock.Nil(), gomock.Nil()).Return(nil, errAgg)

	got, err := observed.AggregateCapabilities(ctx, nil)
	require.NoError(t, err)
	assert.Same(t, want, got)

	got, err = observed.MergeCapabilities(ctx, nil, nil)
	assert.ErrorIs(t, err, errAgg)
	assert.Nil(t, got)
}

func TestUsageMetricsProfileFunc(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	registry := vmcpmocks.NewMockBackendRegistry(ctrl)
	registry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{{ID: "a"}, {ID: "b"}}).Times(2)

	u := &usageMetrics{}
	u.toolCount.Store(-1)
	profile := u.profileFunc(registry, usageFacts{
		authMode: usagemetrics.AuthModeOIDC,
		authz:    true,
		features: []string{usagemetrics.FeatureOptimizer},
	})

	backends := 2
	want := usagemetrics.Profile{
		Transport:    "streamable-http",
		Runtime:      usagemetrics.ValueNone,
		AuthMode:     usagemetrics.AuthModeOIDC,
		Authz:        usagemetrics.ValueOn,
		BackendCount: &backends,
		Features:     []string{usagemetrics.FeatureOptimizer},
	}
	assert.Equal(t, want, profile(), "no tool count before the first aggregation")

	u.toolCount.Store(4)
	tools := 4
	want.ToolCount = &tools
	assert.Equal(t, want, profile())
}

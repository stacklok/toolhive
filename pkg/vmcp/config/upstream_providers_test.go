// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestUpstreamProvidersForTokenLoad(t *testing.T) {
	t.Parallel()

	te := &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeTokenExchange,
		TokenExchange: &authtypes.TokenExchangeConfig{SubjectProviderName: "te"}}
	inject := &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUpstreamInject,
		UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "inject"}}
	obo := &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeOBO,
		OBO: &authtypes.OBOConfig{SubjectTokenProviderName: "obo"}}
	sts := &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeAwsSts,
		AwsSts: &authtypes.AwsStsConfig{SubjectProviderName: "sts"}}
	xaa := &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeXAA,
		XAA: &authtypes.XAAConfig{SubjectProviderName: "xaa"}}

	authz := func(p string) *IncomingAuthConfig {
		return &IncomingAuthConfig{Authz: &AuthzConfig{PrimaryUpstreamProvider: p}}
	}

	tests := []struct {
		name      string
		cfg       *Config
		wantNames []string
	}{
		{name: "nil config", cfg: nil},
		{name: "no strategies, no cedar", cfg: &Config{OutgoingAuth: &OutgoingAuthConfig{Source: "inline"}}},
		{
			name: "every strategy type, sorted",
			cfg: &Config{OutgoingAuth: &OutgoingAuthConfig{
				Source: "inline", Default: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUnauthenticated},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"a": xaa, "b": te, "c": sts, "d": obo, "e": inject,
					"f": {Type: authtypes.StrategyTypeUnauthenticated},
				}}},
			wantNames: []string{"inject", "obo", "sts", "te", "xaa"},
		},
		{
			name:      "default strategy",
			cfg:       &Config{OutgoingAuth: &OutgoingAuthConfig{Default: te}},
			wantNames: []string{"te"},
		},
		{
			name: "cedar primary with no outgoing auth loads everything",
			cfg:  &Config{IncomingAuth: authz("cedar")},
		},
		{
			name: "cedar primary and default naming nothing",
			cfg: &Config{IncomingAuth: authz("cedar"),
				OutgoingAuth: &OutgoingAuthConfig{Default: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUnauthenticated}}},
			wantNames: []string{"cedar"},
		},
		{
			name: "inline without default loads everything: a discovered backend keeps its own strategy",
			cfg: &Config{IncomingAuth: authz("A"),
				OutgoingAuth: &OutgoingAuthConfig{Source: "inline", Backends: map[string]*authtypes.BackendAuthStrategy{
					"b": {Type: authtypes.StrategyTypeUpstreamInject, UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "B"}}}}},
		},
		{
			name: "backend named like the default key does not hide the default",
			cfg: &Config{OutgoingAuth: &OutgoingAuthConfig{Default: te, Backends: map[string]*authtypes.BackendAuthStrategy{
				defaultStrategyKey: {Type: authtypes.StrategyTypeUpstreamInject,
					UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "other"}}}}},
			wantNames: []string{"other", "te"},
		},
		{
			name: "whitespace-only names contribute nothing",
			cfg: &Config{IncomingAuth: authz(" "),
				OutgoingAuth: &OutgoingAuthConfig{Default: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUnauthenticated}}},
		},
		{
			name: "padded name is trimmed",
			cfg: &Config{OutgoingAuth: &OutgoingAuthConfig{Default: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange, TokenExchange: &authtypes.TokenExchangeConfig{SubjectProviderName: " te "}}}},
			wantNames: []string{"te"},
		},
		{
			name: "duplicates across default, backends and cedar collapse",
			cfg: &Config{
				IncomingAuth: authz("te"),
				OutgoingAuth: &OutgoingAuthConfig{Default: te, Backends: map[string]*authtypes.BackendAuthStrategy{"x": te}},
			},
			wantNames: []string{"te"},
		},
		{
			name: "empty provider name (not yet injected) contributes nothing",
			cfg: &Config{OutgoingAuth: &OutgoingAuthConfig{Default: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange, TokenExchange: &authtypes.TokenExchangeConfig{}}}},
		},
		{
			name: "strategy with nil config block is ignored",
			cfg:  &Config{OutgoingAuth: &OutgoingAuthConfig{Default: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeOBO}}},
		},
		{
			name: "discovered source loads everything",
			cfg: &Config{IncomingAuth: authz("cedar"),
				OutgoingAuth: &OutgoingAuthConfig{Source: "discovered", Default: te}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantNames, UpstreamProvidersForTokenLoad(tt.cfg))
		})
	}
}

func TestValidStrategyTypesClassified(t *testing.T) {
	t.Parallel()

	withProvider := map[string]*authtypes.BackendAuthStrategy{
		authtypes.StrategyTypeTokenExchange:  {TokenExchange: &authtypes.TokenExchangeConfig{SubjectProviderName: "p"}},
		authtypes.StrategyTypeUpstreamInject: {UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "p"}},
		authtypes.StrategyTypeOBO:            {OBO: &authtypes.OBOConfig{SubjectTokenProviderName: "p"}},
		authtypes.StrategyTypeAwsSts:         {AwsSts: &authtypes.AwsStsConfig{SubjectProviderName: "p"}},
		authtypes.StrategyTypeXAA:            {XAA: &authtypes.XAAConfig{SubjectProviderName: "p"}},
	}
	noProvider := map[string]bool{
		authtypes.StrategyTypeUnauthenticated: true,
		authtypes.StrategyTypeHeaderInjection: true,
	}

	for _, typ := range validStrategyTypes {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()
			strategy, ok := withProvider[typ]
			if !ok {
				require.True(t, noProvider[typ],
					"strategy type %q is accepted by the validator but not classified here: update "+
						"BackendAuthStrategy.SubjectProviderName and DefaultSubjectProviderName, then this test", typ)
				return
			}
			strategy.Type = typ
			require.Equal(t, "p", strategy.SubjectProviderName(),
				"update BackendAuthStrategy.SubjectProviderName and DefaultSubjectProviderName for %q", typ)
		})
	}
}

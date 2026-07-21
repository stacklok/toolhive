// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultSubjectProviderName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		strategy             *BackendAuthStrategy
		providerName         string
		hasMultipleUpstreams bool
		wantErr              error
		wantSame             bool // expect result pointer identical to input strategy
		checkResult          func(t *testing.T, result *BackendAuthStrategy)
	}{
		{
			name:     "nil strategy returns nil unchanged",
			strategy: nil,
			wantSame: false, // nil == nil, assert.Same doesn't apply; checked separately
			checkResult: func(t *testing.T, result *BackendAuthStrategy) {
				t.Helper()
				assert.Nil(t, result)
			},
		},
		{
			name: "token_exchange nil sub-config returned unchanged",
			strategy: &BackendAuthStrategy{
				Type: StrategyTypeTokenExchange,
			},
			providerName: "upstream1",
			wantSame:     true,
		},
		{
			name: "aws_sts nil sub-config returned unchanged",
			strategy: &BackendAuthStrategy{
				Type: StrategyTypeAwsSts,
			},
			providerName: "upstream1",
			wantSame:     true,
		},
		{
			name: "xaa nil sub-config returned unchanged",
			strategy: &BackendAuthStrategy{
				Type: StrategyTypeXAA,
			},
			providerName: "upstream1",
			wantSame:     true,
		},
		{
			name: "token_exchange already-set not overridden",
			strategy: &BackendAuthStrategy{
				Type:          StrategyTypeTokenExchange,
				TokenExchange: &TokenExchangeConfig{SubjectProviderName: "explicit"},
			},
			providerName: "upstream1",
			wantSame:     true,
			checkResult: func(t *testing.T, result *BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "explicit", result.TokenExchange.SubjectProviderName)
			},
		},
		{
			name: "aws_sts already-set not overridden",
			strategy: &BackendAuthStrategy{
				Type:   StrategyTypeAwsSts,
				AwsSts: &AwsStsConfig{SubjectProviderName: "explicit"},
			},
			providerName: "upstream1",
			wantSame:     true,
			checkResult: func(t *testing.T, result *BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "explicit", result.AwsSts.SubjectProviderName)
			},
		},
		{
			name: "xaa already-set not overridden",
			strategy: &BackendAuthStrategy{
				Type: StrategyTypeXAA,
				XAA:  &XAAConfig{SubjectProviderName: "explicit"},
			},
			providerName: "upstream1",
			wantSame:     true,
			checkResult: func(t *testing.T, result *BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "explicit", result.XAA.SubjectProviderName)
			},
		},
		{
			name: "token_exchange empty gets defaulted",
			strategy: &BackendAuthStrategy{
				Type:          StrategyTypeTokenExchange,
				TokenExchange: &TokenExchangeConfig{},
			},
			providerName:         "upstream1",
			hasMultipleUpstreams: false,
			wantSame:             false,
			checkResult: func(t *testing.T, result *BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "upstream1", result.TokenExchange.SubjectProviderName)
			},
		},
		{
			name: "aws_sts empty gets defaulted",
			strategy: &BackendAuthStrategy{
				Type:   StrategyTypeAwsSts,
				AwsSts: &AwsStsConfig{},
			},
			providerName:         "upstream1",
			hasMultipleUpstreams: false,
			wantSame:             false,
			checkResult: func(t *testing.T, result *BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "upstream1", result.AwsSts.SubjectProviderName)
			},
		},
		{
			name: "xaa empty gets defaulted with single upstream",
			strategy: &BackendAuthStrategy{
				Type: StrategyTypeXAA,
				XAA:  &XAAConfig{},
			},
			providerName:         "upstream1",
			hasMultipleUpstreams: false,
			wantSame:             false,
			checkResult: func(t *testing.T, result *BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "upstream1", result.XAA.SubjectProviderName)
			},
		},
		{
			name: "xaa ambiguous with multiple upstreams returns error",
			strategy: &BackendAuthStrategy{
				Type: StrategyTypeXAA,
				XAA:  &XAAConfig{},
			},
			providerName:         "upstream1",
			hasMultipleUpstreams: true,
			wantErr:              ErrAmbiguousSubjectProvider,
			checkResult: func(t *testing.T, result *BackendAuthStrategy) {
				t.Helper()
				assert.Nil(t, result)
			},
		},
		{
			name: "xaa already-set with multiple upstreams is not an error",
			strategy: &BackendAuthStrategy{
				Type: StrategyTypeXAA,
				XAA:  &XAAConfig{SubjectProviderName: "explicit"},
			},
			providerName:         "upstream1",
			hasMultipleUpstreams: true,
			wantSame:             true,
			checkResult: func(t *testing.T, result *BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "explicit", result.XAA.SubjectProviderName)
			},
		},
		{
			name: "non-applicable strategy type returned unchanged",
			strategy: &BackendAuthStrategy{
				Type:            StrategyTypeHeaderInjection,
				HeaderInjection: &HeaderInjectionConfig{HeaderName: "Authorization"},
			},
			providerName: "upstream1",
			wantSame:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := DefaultSubjectProviderName(tt.strategy, tt.providerName, tt.hasMultipleUpstreams)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
				return
			}

			require.NoError(t, err)
			if tt.strategy == nil {
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
				return
			}

			if tt.wantSame {
				assert.Same(t, tt.strategy, result)
			} else {
				assert.NotSame(t, tt.strategy, result)
			}

			if tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestGetAndValidateConfig(t *testing.T) {
	t.Parallel()

	ptrFloat := func(f float64) *float64 { return &f }
	ptrInt := func(i int) *int { return &i }

	tests := []struct {
		name        string
		cfg         *vmcpconfig.OptimizerConfig
		expected    *Config
		errContains string
	}{
		{
			name:     "nil config returns nil",
			cfg:      nil,
			expected: nil,
		},
		{
			name:     "empty config returns defaults",
			cfg:      &vmcpconfig.OptimizerConfig{},
			expected: &Config{},
		},
		{
			name: "embedding service is copied",
			cfg: &vmcpconfig.OptimizerConfig{
				EmbeddingService: "http://embeddings:8080",
			},
			expected: &Config{
				EmbeddingService: "http://embeddings:8080",
			},
		},
		{
			name: "all valid values are parsed",
			cfg: &vmcpconfig.OptimizerConfig{
				EmbeddingService:          "http://embeddings:8080",
				MaxToolsToReturn:          10,
				HybridSearchSemanticRatio: "0.7",
				SemanticDistanceThreshold: "1.5",
			},
			expected: &Config{
				EmbeddingService:          "http://embeddings:8080",
				MaxToolsToReturn:          ptrInt(10),
				HybridSemanticRatio:       ptrFloat(0.7),
				SemanticDistanceThreshold: ptrFloat(1.5),
			},
		},
		{
			name: "boundary: MaxToolsToReturn=1",
			cfg: &vmcpconfig.OptimizerConfig{
				MaxToolsToReturn: 1,
			},
			expected: &Config{
				MaxToolsToReturn: ptrInt(1),
			},
		},
		{
			name: "boundary: MaxToolsToReturn=50",
			cfg: &vmcpconfig.OptimizerConfig{
				MaxToolsToReturn: 50,
			},
			expected: &Config{
				MaxToolsToReturn: ptrInt(50),
			},
		},
		{
			name: "boundary: ratio=0.0",
			cfg: &vmcpconfig.OptimizerConfig{
				HybridSearchSemanticRatio: "0.0",
			},
			expected: &Config{
				HybridSemanticRatio: ptrFloat(0.0),
			},
		},
		{
			name: "boundary: ratio=1.0",
			cfg: &vmcpconfig.OptimizerConfig{
				HybridSearchSemanticRatio: "1.0",
			},
			expected: &Config{
				HybridSemanticRatio: ptrFloat(1.0),
			},
		},
		{
			name: "boundary: threshold=0.0",
			cfg: &vmcpconfig.OptimizerConfig{
				SemanticDistanceThreshold: "0.0",
			},
			expected: &Config{
				SemanticDistanceThreshold: ptrFloat(0.0),
			},
		},
		{
			name: "boundary: threshold=2.0",
			cfg: &vmcpconfig.OptimizerConfig{
				SemanticDistanceThreshold: "2.0",
			},
			expected: &Config{
				SemanticDistanceThreshold: ptrFloat(2.0),
			},
		},
		{
			name: "MaxToolsToReturn=0 treated as unset",
			cfg: &vmcpconfig.OptimizerConfig{
				MaxToolsToReturn: 0,
			},
			expected: &Config{},
		},
		{
			name: "error: MaxToolsToReturn too high",
			cfg: &vmcpconfig.OptimizerConfig{
				MaxToolsToReturn: 51,
			},
			errContains: "optimizer.maxToolsToReturn must be between 1 and 50",
		},
		{
			name: "error: MaxToolsToReturn negative",
			cfg: &vmcpconfig.OptimizerConfig{
				MaxToolsToReturn: -1,
			},
			errContains: "optimizer.maxToolsToReturn must be between 1 and 50",
		},
		{
			name: "error: ratio above 1.0",
			cfg: &vmcpconfig.OptimizerConfig{
				HybridSearchSemanticRatio: "1.1",
			},
			errContains: "optimizer.hybridSearchSemanticRatio must be between 0.0 and 1.0",
		},
		{
			name: "error: ratio negative",
			cfg: &vmcpconfig.OptimizerConfig{
				HybridSearchSemanticRatio: "-0.1",
			},
			errContains: "optimizer.hybridSearchSemanticRatio must be between 0.0 and 1.0",
		},
		{
			name: "error: ratio not a number",
			cfg: &vmcpconfig.OptimizerConfig{
				HybridSearchSemanticRatio: "abc",
			},
			errContains: "optimizer.hybridSearchSemanticRatio must be a valid number",
		},
		{
			name: "error: threshold above 2.0",
			cfg: &vmcpconfig.OptimizerConfig{
				SemanticDistanceThreshold: "2.1",
			},
			errContains: "optimizer.semanticDistanceThreshold must be between 0.0 and 2.0",
		},
		{
			name: "error: threshold negative",
			cfg: &vmcpconfig.OptimizerConfig{
				SemanticDistanceThreshold: "-0.5",
			},
			errContains: "optimizer.semanticDistanceThreshold must be between 0.0 and 2.0",
		},
		{
			name: "error: threshold not a number",
			cfg: &vmcpconfig.OptimizerConfig{
				SemanticDistanceThreshold: "not-a-float",
			},
			errContains: "optimizer.semanticDistanceThreshold must be a valid number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := GetAndValidateConfig(tt.cfg)

			if tt.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			require.NoError(t, err)

			if tt.expected == nil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expected.EmbeddingService, result.EmbeddingService)

			if tt.expected.MaxToolsToReturn != nil {
				require.NotNil(t, result.MaxToolsToReturn)
				assert.Equal(t, *tt.expected.MaxToolsToReturn, *result.MaxToolsToReturn)
			} else {
				assert.Nil(t, result.MaxToolsToReturn)
			}

			if tt.expected.HybridSemanticRatio != nil {
				require.NotNil(t, result.HybridSemanticRatio)
				assert.InDelta(t, *tt.expected.HybridSemanticRatio, *result.HybridSemanticRatio, 1e-9)
			} else {
				assert.Nil(t, result.HybridSemanticRatio)
			}

			if tt.expected.SemanticDistanceThreshold != nil {
				require.NotNil(t, result.SemanticDistanceThreshold)
				assert.InDelta(t, *tt.expected.SemanticDistanceThreshold, *result.SemanticDistanceThreshold, 1e-9)
			} else {
				assert.Nil(t, result.SemanticDistanceThreshold)
			}
		})
	}
}

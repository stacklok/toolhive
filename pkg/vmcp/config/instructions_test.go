// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestInstructionsConfig_NilSafeAccessors(t *testing.T) {
	t.Parallel()

	var unset *InstructionsConfig
	assert.False(t, unset.ForwardEnabled(), "unset config forwards nothing")
	assert.Equal(t, DefaultInstructionsMaxLength, unset.EffectiveMaxLength())

	zero := &InstructionsConfig{}
	assert.False(t, zero.ForwardEnabled())
	assert.Equal(t, DefaultInstructionsMaxLength, zero.EffectiveMaxLength(), "0 means the default")

	set := &InstructionsConfig{Forward: true, MaxLength: 100}
	assert.True(t, set.ForwardEnabled())
	assert.Equal(t, 100, set.EffectiveMaxLength())
}

func TestValidator_ValidateAggregation_Instructions(t *testing.T) {
	t.Parallel()
	base := func(instr *InstructionsConfig) *AggregationConfig {
		return &AggregationConfig{
			ConflictResolution:       vmcp.ConflictStrategyPrefix,
			ConflictResolutionConfig: &ConflictResolutionConfig{PrefixFormat: "{workload}_"},
			Instructions:             instr,
		}
	}
	tests := []struct {
		name    string
		instr   *InstructionsConfig
		wantErr string
	}{
		{name: "unset is valid", instr: nil},
		{name: "forward with default cap is valid", instr: &InstructionsConfig{Forward: true}},
		{name: "forward with explicit cap is valid", instr: &InstructionsConfig{Forward: true, MaxLength: 4096}},
		{name: "negative cap is rejected", instr: &InstructionsConfig{Forward: true, MaxLength: -1},
			wantErr: "instructions.maxLength must be >= 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := NewValidator().validateAggregation(base(tt.instr))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestLoadFromYAML_Instructions verifies the YAML spelling of the new block.
func TestLoadFromYAML_Instructions(t *testing.T) {
	t.Parallel()
	yamlText := `
name: test-vmcp
groupRef: g
aggregation:
  conflictResolution: prefix
  conflictResolutionConfig:
    prefixFormat: "{workload}_"
  instructions:
    forward: true
    maxLength: 2048
`
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(tmpFile, []byte(yamlText), 0o600))
	cfg, err := NewYAMLLoader(tmpFile, createMockEnvReader(t, nil)).Load()
	require.NoError(t, err)
	require.NotNil(t, cfg.Aggregation)
	require.NotNil(t, cfg.Aggregation.Instructions)
	assert.True(t, cfg.Aggregation.Instructions.Forward)
	assert.Equal(t, 2048, cfg.Aggregation.Instructions.MaxLength)
}

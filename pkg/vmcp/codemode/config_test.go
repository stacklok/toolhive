// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package codemode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   *config.CodeModeConfig
		want *Config
	}{
		{
			name: "nil config disables code mode",
			in:   nil,
			want: nil,
		},
		{
			name: "disabled config disables code mode",
			in:   &config.CodeModeConfig{Enabled: false, StepLimit: 5},
			want: nil,
		},
		{
			name: "enabled with all fields unset uses defaults",
			in:   &config.CodeModeConfig{Enabled: true},
			// StepLimit 0 is intentionally left for script.New to resolve to its default.
			want: &Config{StepLimit: 0, ParallelMax: defaultParallelMax, ToolCallTimeout: defaultToolCallTimeout},
		},
		{
			name: "enabled with explicit values overrides defaults",
			in: &config.CodeModeConfig{
				Enabled:                true,
				StepLimit:              50_000,
				ParallelMaxConcurrency: 4,
				ToolCallTimeout:        config.Duration(5 * time.Second),
			},
			want: &Config{StepLimit: 50_000, ParallelMax: 4, ToolCallTimeout: 5 * time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, FromConfig(tt.in))
		})
	}
}

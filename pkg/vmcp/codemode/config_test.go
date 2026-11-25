// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package codemode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/script"
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
			name: "enabled with all fields unset translates to zeros (defaulting deferred to resolve)",
			in:   &config.CodeModeConfig{Enabled: true},
			want: &Config{},
		},
		{
			name: "enabled with explicit values passes them through",
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

func TestResolve(t *testing.T) {
	t.Parallel()

	wantDefaults := Config{
		StepLimit:       script.DefaultStepLimit,
		ParallelMax:     defaultParallelMax,
		ToolCallTimeout: defaultToolCallTimeout,
	}

	tests := []struct {
		name string
		in   *Config
		want Config
	}{
		{name: "nil resolves to safe defaults", in: nil, want: wantDefaults},
		{name: "zero-valued resolves to safe defaults", in: &Config{}, want: wantDefaults},
		{
			name: "negative parallel/timeout resolve to defaults (never unbounded)",
			in:   &Config{ParallelMax: -1, ToolCallTimeout: -1},
			want: wantDefaults,
		},
		{
			name: "explicit positive values are preserved",
			in:   &Config{StepLimit: 5, ParallelMax: 2, ToolCallTimeout: time.Second},
			want: Config{StepLimit: 5, ParallelMax: 2, ToolCallTimeout: time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, resolve(tt.in))
		})
	}
}

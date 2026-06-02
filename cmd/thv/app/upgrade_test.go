// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/workloads/upgrade"
)

func TestNewEnvCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result *upgrade.CheckResult
		want   int
	}{
		{
			name:   "nil drift",
			result: &upgrade.CheckResult{},
			want:   0,
		},
		{
			name:   "empty drift",
			result: &upgrade.CheckResult{EnvVarDrift: &upgrade.EnvVarDrift{}},
			want:   0,
		},
		{
			name: "two added",
			result: &upgrade.CheckResult{EnvVarDrift: &upgrade.EnvVarDrift{
				Added: []upgrade.EnvVarInfo{{Name: "A"}, {Name: "B"}},
			}},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, newEnvCount(tt.result))
		})
	}
}

func TestPostureMarker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result *upgrade.CheckResult
		want   string
	}{
		{
			name:   "no config drift",
			result: &upgrade.CheckResult{},
			want:   "-",
		},
		{
			name: "with config drift",
			result: &upgrade.CheckResult{ConfigDrift: &upgrade.ConfigDrift{
				Transport: &upgrade.StringChange{From: "stdio", To: "sse"},
			}},
			want: "⚠ drift",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, postureMarker(tt.result))
		})
	}
}

func TestDashIfEmpty(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "-", dashIfEmpty(""))
	assert.Equal(t, "x", dashIfEmpty("x"))
}

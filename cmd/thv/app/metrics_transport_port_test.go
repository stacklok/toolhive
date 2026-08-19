// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveMetricsOnTransportPort covers the tri-state the migration depends on.
// An unset flag must stay unset rather than collapsing to false, so a workload that
// never expressed a preference follows the default — including after the default
// changes at the end of the migration window.
func TestResolveMetricsOnTransportPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want *bool
	}{
		{
			name: "flag absent stays unset",
			args: nil,
			want: nil,
		},
		{
			name: "explicit true is recorded",
			args: []string{"--otel-metrics-on-transport-port=true"},
			want: ptr(true),
		},
		{
			name: "explicit false is recorded",
			args: []string{"--otel-metrics-on-transport-port=false"},
			want: ptr(false),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runFlags := &RunFlags{}
			cmd := &cobra.Command{Use: "run", RunE: func(*cobra.Command, []string) error { return nil }}
			AddRunFlags(cmd, runFlags)

			cmd.SetArgs(tt.args)
			require.NoError(t, cmd.Execute())

			got := resolveMetricsOnTransportPort(cmd, runFlags)
			if tt.want == nil {
				assert.Nil(t, got, "an unset flag must not resolve to a value")
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tt.want, *got)
		})
	}
}

func ptr[T any](v T) *T { return &v }

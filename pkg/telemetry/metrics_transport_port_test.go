// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolPtr(b bool) *bool { return &b }

func TestServeMetricsOnTransportPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{
			name: "nil config resolves to the default",
			cfg:  nil,
			want: DefaultMetricsOnTransportPort,
		},
		{
			name: "unset field resolves to the default",
			cfg:  &Config{EnablePrometheusMetricsPath: true},
			want: DefaultMetricsOnTransportPort,
		},
		{
			name: "explicit true is honoured",
			cfg:  &Config{MetricsOnTransportPort: boolPtr(true)},
			want: true,
		},
		{
			name: "explicit false is honoured",
			cfg:  &Config{MetricsOnTransportPort: boolPtr(false)},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.cfg.ServeMetricsOnTransportPort())
		})
	}
}

// TestMetricsOnTransportPortNotPersistedWhenUnset guards the constraint the whole
// migration depends on: an unset value must survive a round trip through the
// persisted RunConfig as unset, never materialised into a concrete bool.
//
// If it were written out as `false` or `true`, changing
// DefaultMetricsOnTransportPort later would not move any workload that already
// exists — every one of them would carry the value chosen at creation time, and
// the cutover would silently do nothing.
func TestMetricsOnTransportPortNotPersistedWhenUnset(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(&Config{EnablePrometheusMetricsPath: true})
	require.NoError(t, err)

	assert.NotContains(t, string(encoded), "metricsOnTransportPort",
		"an unset value must be omitted, not written as a concrete bool")

	var decoded Config
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Nil(t, decoded.MetricsOnTransportPort, "round trip must preserve unset")
	assert.Equal(t, DefaultMetricsOnTransportPort, decoded.ServeMetricsOnTransportPort(),
		"an unset value must still resolve to the current default after a round trip")
}

// TestMetricsOnTransportPortPersistsExplicitChoice is the other half: a
// deployment that opted out must keep that choice across a restart, and must not
// be moved by a later change to the default.
func TestMetricsOnTransportPortPersistsExplicitChoice(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(&Config{MetricsOnTransportPort: boolPtr(false)})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "metricsOnTransportPort")

	var decoded Config
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.NotNil(t, decoded.MetricsOnTransportPort)
	assert.False(t, decoded.ServeMetricsOnTransportPort())
}

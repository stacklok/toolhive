// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBoolOrDefaultTrue pins the "unset means true" rule for the legacy-emission
// flags on the thv serve path. Reading the Go zero value of a bool instead would
// silently invert the documented default and disable dual emission — a failure
// that produces no error, just missing legacy metric series.
func TestBoolOrDefaultTrue(t *testing.T) {
	t.Parallel()

	tr, fa := true, false

	tests := []struct {
		name string
		in   *bool
		want bool
	}{
		{name: "unset defaults to true", in: nil, want: true},
		{name: "explicit true is honoured", in: &tr, want: true},
		{name: "explicit false is honoured", in: &fa, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, boolOrDefaultTrue(tt.in))
		})
	}
}

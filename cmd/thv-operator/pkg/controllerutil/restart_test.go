// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreserveKubectlRestartedAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		desired map[string]string
		live    map[string]string
		want    map[string]string
	}{
		{
			name:    "nil live leaves desired unchanged",
			desired: map[string]string{"keep": "me"},
			live:    nil,
			want:    map[string]string{"keep": "me"},
		},
		{
			name:    "missing live annotation is a no-op",
			desired: map[string]string{"keep": "me"},
			live:    map[string]string{"other": "x"},
			want:    map[string]string{"keep": "me"},
		},
		{
			name:    "empty live annotation is a no-op",
			desired: map[string]string{"keep": "me"},
			live:    map[string]string{KubectlRestartedAtAnnotation: ""},
			want:    map[string]string{"keep": "me"},
		},
		{
			name:    "copies onto existing desired map",
			desired: map[string]string{"keep": "me"},
			live:    map[string]string{KubectlRestartedAtAnnotation: "2026-08-19T06:00:00Z"},
			want: map[string]string{
				"keep":                       "me",
				KubectlRestartedAtAnnotation: "2026-08-19T06:00:00Z",
			},
		},
		{
			name:    "allocates desired when nil",
			desired: nil,
			live:    map[string]string{KubectlRestartedAtAnnotation: "2026-08-19T06:00:00Z"},
			want:    map[string]string{KubectlRestartedAtAnnotation: "2026-08-19T06:00:00Z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := PreserveKubectlRestartedAt(tt.desired, tt.live)
			require.Equal(t, tt.want, got)
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMapIsSubset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		subset   map[string]string
		superset map[string]string
		want     bool
	}{
		{
			name:     "both nil",
			subset:   nil,
			superset: nil,
			want:     true,
		},
		{
			name:     "both empty",
			subset:   map[string]string{},
			superset: map[string]string{},
			want:     true,
		},
		{
			name:     "nil subset of non-empty superset",
			subset:   nil,
			superset: map[string]string{"a": "1"},
			want:     true,
		},
		{
			name:     "empty subset of non-empty superset",
			subset:   map[string]string{},
			superset: map[string]string{"a": "1"},
			want:     true,
		},
		{
			name:     "exact match",
			subset:   map[string]string{"a": "1", "b": "2"},
			superset: map[string]string{"a": "1", "b": "2"},
			want:     true,
		},
		{
			name:     "proper subset",
			subset:   map[string]string{"a": "1"},
			superset: map[string]string{"a": "1", "b": "2", "c": "3"},
			want:     true,
		},
		{
			name:     "subset larger than superset",
			subset:   map[string]string{"a": "1", "b": "2", "c": "3"},
			superset: map[string]string{"a": "1"},
			want:     false,
		},
		{
			name:     "key missing from superset",
			subset:   map[string]string{"a": "1", "missing": "x"},
			superset: map[string]string{"a": "1", "b": "2"},
			want:     false,
		},
		{
			name:     "value mismatch",
			subset:   map[string]string{"a": "1"},
			superset: map[string]string{"a": "wrong"},
			want:     false,
		},
		{
			name:     "non-empty subset of nil superset",
			subset:   map[string]string{"a": "1"},
			superset: nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := MapIsSubset(tt.subset, tt.superset)
			require.Equal(t, tt.want, got)
		})
	}
}

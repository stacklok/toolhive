// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"reflect"
	"testing"
)

func TestFirstPeriodIfRepeated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  []string
		ok    bool
	}{
		{
			name:  "doubled",
			lines: []string{"a", "b", "a", "b"},
			want:  []string{"a", "b"},
			ok:    true,
		},
		{
			name:  "tripled collapses to one period",
			lines: []string{"a", "b", "a", "b", "a", "b"},
			want:  []string{"a", "b"},
			ok:    true,
		},
		{
			name:  "partial repeat is left alone",
			lines: []string{"a", "b", "a", "b", "a"},
			ok:    false,
		},
		{
			name:  "non-repeating list is left alone",
			lines: []string{"a", "b", "c", "d"},
			ok:    false,
		},
		{
			name:  "odd length",
			lines: []string{"a", "b", "a"},
			ok:    false,
		},
		{
			name:  "single element",
			lines: []string{"a"},
			ok:    false,
		},
		{
			name:  "empty",
			lines: []string{},
			ok:    false,
		},
		{
			name:  "already deduped, idempotent",
			lines: []string{"a", "b"},
			ok:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := firstPeriodIfRepeated(tt.lines)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

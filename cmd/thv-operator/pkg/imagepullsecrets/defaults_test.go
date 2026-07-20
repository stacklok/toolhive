// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package imagepullsecrets

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestNewDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []string
		want  []corev1.LocalObjectReference
	}{
		{
			name:  "nil slice returns empty defaults",
			input: nil,
			want:  nil,
		},
		{
			name:  "empty slice returns empty defaults",
			input: []string{},
			want:  nil,
		},
		{
			name:  "single name",
			input: []string{"regcred"},
			want:  []corev1.LocalObjectReference{{Name: "regcred"}},
		},
		{
			name:  "multiple names preserve order",
			input: []string{"regcred", "otherscred"},
			want: []corev1.LocalObjectReference{
				{Name: "regcred"},
				{Name: "otherscred"},
			},
		},
		{
			name:  "whitespace tolerated",
			input: []string{" regcred ", "\totherscred\n"},
			want: []corev1.LocalObjectReference{
				{Name: "regcred"},
				{Name: "otherscred"},
			},
		},
		{
			name:  "empty entries skipped",
			input: []string{"regcred", "", "  ", "otherscred"},
			want: []corev1.LocalObjectReference{
				{Name: "regcred"},
				{Name: "otherscred"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NewDefaults(tt.input).List()
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestLoadDefaultsFromEnv covers env-var parsing across the values an admin
// could plausibly set. The unset case is functionally redundant with the empty
// case (strings.Split("", ",") -> [""] which NewDefaults filters out), so it is
// not exercised separately. All cases mutate the process environment via
// t.Setenv, so this function cannot use t.Parallel().
func TestLoadDefaultsFromEnv(t *testing.T) {
	tests := []struct {
		name   string
		envVal string
		want   []corev1.LocalObjectReference
	}{
		{
			name:   "empty env var yields empty defaults",
			envVal: "",
			want:   nil,
		},
		{
			name:   "single secret",
			envVal: "regcred",
			want:   []corev1.LocalObjectReference{{Name: "regcred"}},
		},
		{
			name:   "comma-separated list",
			envVal: "regcred,otherscred",
			want: []corev1.LocalObjectReference{
				{Name: "regcred"},
				{Name: "otherscred"},
			},
		},
		{
			name:   "whitespace tolerated",
			envVal: " regcred , otherscred ",
			want: []corev1.LocalObjectReference{
				{Name: "regcred"},
				{Name: "otherscred"},
			},
		},
		{
			name:   "empty entries skipped",
			envVal: "regcred,,otherscred,",
			want: []corev1.LocalObjectReference{
				{Name: "regcred"},
				{Name: "otherscred"},
			},
		},
		{
			name:   "only commas yields empty",
			envVal: ",,,",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Cannot run in parallel because we mutate process env.
			t.Setenv(EnvVar, tt.envVal)
			got := LoadDefaultsFromEnv().List()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDefaultsMerge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		defaults []string
		crLevel  []corev1.LocalObjectReference
		want     []corev1.LocalObjectReference
	}{
		{
			name:     "both empty returns nil",
			defaults: nil,
			crLevel:  nil,
			want:     nil,
		},
		{
			name:     "defaults only",
			defaults: []string{"regcred", "otherscred"},
			crLevel:  nil,
			want: []corev1.LocalObjectReference{
				{Name: "regcred"},
				{Name: "otherscred"},
			},
		},
		{
			name:     "cr-level only",
			defaults: nil,
			crLevel: []corev1.LocalObjectReference{
				{Name: "cr-secret"},
			},
			want: []corev1.LocalObjectReference{
				{Name: "cr-secret"},
			},
		},
		{
			name:     "no overlap appends defaults after cr-level",
			defaults: []string{"chart-default"},
			crLevel: []corev1.LocalObjectReference{
				{Name: "cr-secret"},
			},
			want: []corev1.LocalObjectReference{
				{Name: "cr-secret"},
				{Name: "chart-default"},
			},
		},
		{
			name:     "name overlap: cr-level wins",
			defaults: []string{"shared", "chart-only"},
			crLevel: []corev1.LocalObjectReference{
				{Name: "cr-only"},
				{Name: "shared"},
			},
			want: []corev1.LocalObjectReference{
				{Name: "cr-only"},
				{Name: "shared"},
				{Name: "chart-only"},
			},
		},
		{
			name:     "duplicate cr-level entries deduplicated",
			defaults: nil,
			crLevel: []corev1.LocalObjectReference{
				{Name: "dup"},
				{Name: "dup"},
			},
			want: []corev1.LocalObjectReference{
				{Name: "dup"},
			},
		},
		{
			name:     "cr-level order preserved",
			defaults: []string{"a", "b"},
			crLevel: []corev1.LocalObjectReference{
				{Name: "z"},
				{Name: "y"},
			},
			want: []corev1.LocalObjectReference{
				{Name: "z"},
				{Name: "y"},
				{Name: "a"},
				{Name: "b"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := NewDefaults(tt.defaults)
			got := d.Merge(tt.crLevel)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDefaultsMergeDoesNotMutateCRLevel(t *testing.T) {
	t.Parallel()

	d := NewDefaults([]string{"chart-default"})
	crLevel := []corev1.LocalObjectReference{
		{Name: "cr-secret"},
	}
	originalCR := append([]corev1.LocalObjectReference(nil), crLevel...)

	got := d.Merge(crLevel)

	assert.Equal(t, originalCR, crLevel, "Merge must not mutate the caller's slice")
	assert.NotSame(t, &crLevel[0], &got[0], "Merge must return a fresh slice")
}

func TestDefaultsListReturnsCopy(t *testing.T) {
	t.Parallel()

	d := NewDefaults([]string{"regcred", "otherscred"})
	first := d.List()
	first[0] = corev1.LocalObjectReference{Name: "mutated"}

	second := d.List()
	assert.Equal(t, "regcred", second[0].Name, "List must return a fresh slice each call")
}

func TestZeroValueDefaults(t *testing.T) {
	t.Parallel()

	var d Defaults
	assert.Nil(t, d.List())
	assert.Nil(t, d.Merge(nil))

	cr := []corev1.LocalObjectReference{{Name: "cr"}}
	got := d.Merge(cr)
	assert.Equal(t, cr, got)
}

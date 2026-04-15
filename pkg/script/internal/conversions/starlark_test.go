// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversions

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

func TestGoToStarlark(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   interface{}
		check   func(t *testing.T, v starlark.Value)
		wantErr bool
	}{
		{
			name:  "nil",
			input: nil,
			check: func(t *testing.T, v starlark.Value) { t.Helper(); require.Equal(t, starlark.None, v) },
		},
		{
			name:  "bool true",
			input: true,
			check: func(t *testing.T, v starlark.Value) { t.Helper(); require.Equal(t, starlark.True, v) },
		},
		{
			name:  "int",
			input: 42,
			check: func(t *testing.T, v starlark.Value) {
				t.Helper()
				intVal, ok := v.(starlark.Int)
				require.True(t, ok)
				got, _ := intVal.Int64()
				require.Equal(t, int64(42), got)
			},
		},
		{
			name:  "float64 whole number becomes Int",
			input: float64(42),
			check: func(t *testing.T, v starlark.Value) {
				t.Helper()
				_, ok := v.(starlark.Int)
				require.True(t, ok, "whole float64 should become Int")
			},
		},
		{
			name:  "float64 fractional stays Float",
			input: float64(3.14),
			check: func(t *testing.T, v starlark.Value) {
				t.Helper()
				_, ok := v.(starlark.Float)
				require.True(t, ok, "fractional float64 should stay Float")
			},
		},
		{
			name:  "string",
			input: "hello",
			check: func(t *testing.T, v starlark.Value) {
				t.Helper()
				require.Equal(t, starlark.String("hello"), v)
			},
		},
		{
			name:  "slice",
			input: []interface{}{"a", "b"},
			check: func(t *testing.T, v starlark.Value) {
				t.Helper()
				list, ok := v.(*starlark.List)
				require.True(t, ok)
				require.Equal(t, 2, list.Len())
			},
		},
		{
			name:  "map",
			input: map[string]interface{}{"key": "val"},
			check: func(t *testing.T, v starlark.Value) {
				t.Helper()
				d, ok := v.(*starlark.Dict)
				require.True(t, ok)
				require.Equal(t, 1, d.Len())
			},
		},
		{
			name:  "json.Number integer",
			input: json.Number("42"),
			check: func(t *testing.T, v starlark.Value) {
				t.Helper()
				intVal, ok := v.(starlark.Int)
				require.True(t, ok)
				got, _ := intVal.Int64()
				require.Equal(t, int64(42), got)
			},
		},
		{
			name:  "json.Number float",
			input: json.Number("3.14"),
			check: func(t *testing.T, v starlark.Value) {
				t.Helper()
				_, ok := v.(starlark.Float)
				require.True(t, ok)
			},
		},
		{
			name:    "unsupported type",
			input:   struct{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v, err := GoToStarlark(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.check(t, v)
		})
	}
}

func TestStarlarkToGo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  starlark.Value
		expect interface{}
	}{
		{"None", starlark.None, nil},
		{"Bool", starlark.True, true},
		{"Int", starlark.MakeInt(42), int64(42)},
		{"Float", starlark.Float(3.14), float64(3.14)},
		{"String", starlark.String("hello"), "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := StarlarkToGo(tt.input)
			require.Equal(t, tt.expect, got)
		})
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	original := map[string]interface{}{
		"name":  "test",
		"count": int64(42),
		"items": []interface{}{"a", "b"},
	}

	sv, err := GoToStarlark(original)
	require.NoError(t, err)

	roundTripped := StarlarkToGo(sv)
	require.Equal(t, original, roundTripped)
}

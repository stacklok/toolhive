// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

func TestExecute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		script    string
		globals   starlark.StringDict
		stepLimit uint64
		check     func(t *testing.T, result *ExecuteResult)
		wantErr   string
	}{
		{
			name:      "returns integer",
			script:    `return 42`,
			stepLimit: 100_000,
			check: func(t *testing.T, result *ExecuteResult) {
				t.Helper()
				intVal, ok := result.Value.(starlark.Int)
				require.True(t, ok, "expected Int, got %T", result.Value)
				got, _ := intVal.Int64()
				require.Equal(t, int64(42), got)
			},
		},
		{
			name:      "returns string",
			script:    `return "hello"`,
			stepLimit: 100_000,
			check: func(t *testing.T, result *ExecuteResult) {
				t.Helper()
				strVal, ok := result.Value.(starlark.String)
				require.True(t, ok, "expected String, got %T", result.Value)
				require.Equal(t, "hello", string(strVal))
			},
		},
		{
			name:      "no return yields None",
			script:    `x = 1 + 1`,
			stepLimit: 100_000,
			check: func(t *testing.T, result *ExecuteResult) {
				t.Helper()
				require.Equal(t, starlark.None, result.Value)
			},
		},
		{
			name:      "uses predeclared globals",
			script:    `return x + 5`,
			globals:   starlark.StringDict{"x": starlark.MakeInt(10)},
			stepLimit: 100_000,
			check: func(t *testing.T, result *ExecuteResult) {
				t.Helper()
				intVal, ok := result.Value.(starlark.Int)
				require.True(t, ok)
				got, _ := intVal.Int64()
				require.Equal(t, int64(15), got)
			},
		},
		{
			name: "captures print output",
			script: `
print("line1")
print("line2")
return "done"
`,
			stepLimit: 100_000,
			check: func(t *testing.T, result *ExecuteResult) {
				t.Helper()
				require.Equal(t, []string{"line1", "line2"}, result.Logs)
			},
		},
		{
			name: "step limit exceeded",
			script: `
x = 0
for i in range(10000):
    x = x + 1
return x
`,
			stepLimit: 100,
			wantErr:   "too many steps",
		},
		{
			name:      "syntax error",
			script:    `return ][`,
			stepLimit: 100_000,
			wantErr:   "script execution failed",
		},
		{
			name: "loops and conditionals",
			script: `
items = [1, 2, 3, 4, 5]
total = 0
for item in items:
    if item % 2 == 0:
        total = total + item
return total
`,
			stepLimit: 100_000,
			check: func(t *testing.T, result *ExecuteResult) {
				t.Helper()
				intVal, ok := result.Value.(starlark.Int)
				require.True(t, ok)
				got, _ := intVal.Int64()
				require.Equal(t, int64(6), got)
			},
		},
		{
			name: "returns dict",
			script: `
d = {"key": "value", "count": 42}
return d
`,
			stepLimit: 100_000,
			check: func(t *testing.T, result *ExecuteResult) {
				t.Helper()
				dictVal, ok := result.Value.(*starlark.Dict)
				require.True(t, ok, "expected Dict, got %T", result.Value)
				require.Equal(t, 2, dictVal.Len())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := Execute(tt.script, tt.globals, tt.stepLimit)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			tt.check(t, result)
		})
	}
}

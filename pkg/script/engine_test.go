// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
		wantValue interface{}
		wantLogs  []string
		wantErr   string
	}{
		{
			name:      "return integer",
			script:    "return 42",
			wantValue: int64(42),
		},
		{
			name:      "return string",
			script:    `return "hello"`,
			wantValue: "hello",
		},
		{
			name:      "return dict",
			script:    `return {"a": 1, "b": 2}`,
			wantValue: map[string]interface{}{"a": int64(1), "b": int64(2)},
		},
		{
			name:      "return list",
			script:    `return [1, 2, 3]`,
			wantValue: []interface{}{int64(1), int64(2), int64(3)},
		},
		{
			name:   "call provided global function",
			script: "return double(21)",
			globals: starlark.StringDict{
				"double": starlark.NewBuiltin("double", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
					var x int
					if err := starlark.UnpackPositionalArgs("double", args, nil, 1, &x); err != nil {
						return nil, err
					}
					return starlark.MakeInt(x * 2), nil
				}),
			},
			wantValue: int64(42),
		},
		{
			name:    "syntax error",
			script:  "return !!!",
			wantErr: "script execution failed",
		},
		{
			name:    "runtime error division by zero",
			script:  "return 1 // 0",
			wantErr: "script execution failed",
		},
		{
			name:      "step limit exceeded",
			script:    "x = 0\nwhile True:\n    x += 1",
			stepLimit: 1000,
			wantErr:   "script execution failed",
		},
		{
			name:      "no return yields None",
			script:    "x = 1",
			wantValue: nil,
		},
		{
			name: "multi-line for loop building list",
			script: `result = []
for i in range(5):
    result.append(i * 2)
return result`,
			wantValue: []interface{}{int64(0), int64(2), int64(4), int64(6), int64(8)},
		},
		{
			name:      "print captured in logs",
			script:    "print(\"hello\")\nprint(\"world\")\nreturn 1",
			wantValue: int64(1),
			wantLogs:  []string{"hello", "world"},
		},
		{
			name:      "return boolean true",
			script:    "return True",
			wantValue: true,
		},
		{
			name:      "return None explicitly",
			script:    "return None",
			wantValue: nil,
		},
		{
			name:      "return float",
			script:    "return 3.14",
			wantValue: 3.14,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := Execute(tt.script, tt.globals, tt.stepLimit)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)

			got := starlarkToGo(result.Value)
			assert.Equal(t, tt.wantValue, got)

			if tt.wantLogs != nil {
				assert.Equal(t, tt.wantLogs, result.Logs)
			}
		})
	}
}

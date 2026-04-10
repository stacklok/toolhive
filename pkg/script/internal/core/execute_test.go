// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

func TestExecute_BasicReturn(t *testing.T) {
	t.Parallel()

	result, err := Execute(`return 42`, nil, 100_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	intVal, ok := result.Value.(starlark.Int)
	if !ok {
		t.Fatalf("expected Int, got %T", result.Value)
	}
	got, _ := intVal.Int64()
	if got != 42 {
		t.Errorf("expected 42, got %d", got)
	}
}

func TestExecute_StringReturn(t *testing.T) {
	t.Parallel()

	result, err := Execute(`return "hello"`, nil, 100_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	strVal, ok := result.Value.(starlark.String)
	if !ok {
		t.Fatalf("expected String, got %T", result.Value)
	}
	if string(strVal) != "hello" {
		t.Errorf("expected 'hello', got %q", strVal)
	}
}

func TestExecute_NoReturn(t *testing.T) {
	t.Parallel()

	result, err := Execute(`x = 1 + 1`, nil, 100_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Value != starlark.None {
		t.Errorf("expected None for no return, got %v", result.Value)
	}
}

func TestExecute_WithGlobals(t *testing.T) {
	t.Parallel()

	globals := starlark.StringDict{
		"x": starlark.MakeInt(10),
	}

	result, err := Execute(`return x + 5`, globals, 100_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	intVal, ok := result.Value.(starlark.Int)
	if !ok {
		t.Fatalf("expected Int, got %T", result.Value)
	}
	got, _ := intVal.Int64()
	if got != 15 {
		t.Errorf("expected 15, got %d", got)
	}
}

func TestExecute_PrintCapture(t *testing.T) {
	t.Parallel()

	result, err := Execute(`
print("line1")
print("line2")
return "done"
`, nil, 100_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Logs) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(result.Logs))
	}
	if result.Logs[0] != "line1" || result.Logs[1] != "line2" {
		t.Errorf("unexpected logs: %v", result.Logs)
	}
}

func TestExecute_StepLimitExceeded(t *testing.T) {
	t.Parallel()

	// A tight loop with a very small step limit should fail
	_, err := Execute(`
x = 0
for i in range(10000):
    x = x + 1
return x
`, nil, 100)

	if err == nil {
		t.Fatal("expected step limit error, got nil")
	}
	if !strings.Contains(err.Error(), "too many steps") {
		t.Errorf("error should mention too many steps, got: %v", err)
	}
}

func TestExecute_SyntaxError(t *testing.T) {
	t.Parallel()

	_, err := Execute(`return ][`, nil, 100_000)
	if err == nil {
		t.Fatal("expected syntax error, got nil")
	}
}

func TestExecute_LoopsAndConditionals(t *testing.T) {
	t.Parallel()

	result, err := Execute(`
items = [1, 2, 3, 4, 5]
total = 0
for item in items:
    if item % 2 == 0:
        total = total + item
return total
`, nil, 100_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	intVal, ok := result.Value.(starlark.Int)
	if !ok {
		t.Fatalf("expected Int, got %T", result.Value)
	}
	got, _ := intVal.Int64()
	if got != 6 {
		t.Errorf("expected 6 (2+4), got %d", got)
	}
}

func TestExecute_DictReturn(t *testing.T) {
	t.Parallel()

	result, err := Execute(`
d = {"key": "value", "count": 42}
return d
`, nil, 100_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dictVal, ok := result.Value.(*starlark.Dict)
	if !ok {
		t.Fatalf("expected Dict, got %T", result.Value)
	}
	if dictVal.Len() != 2 {
		t.Errorf("expected 2 entries, got %d", dictVal.Len())
	}
}

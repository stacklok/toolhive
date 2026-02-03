// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cel_test

import (
	"errors"
	"testing"

	celgo "github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/cel"
)

// newTestClaimsEngine creates a CEL engine for testing claims-based expressions.
// This demonstrates how consumers should configure the generic CEL engine.
func newTestClaimsEngine() *cel.Engine {
	return cel.NewEngine(
		celgo.Variable("claims", celgo.MapType(celgo.StringType, celgo.DynType)),
	)
}

func TestNewEngine(t *testing.T) {
	t.Parallel()

	engine := newTestClaimsEngine()
	require.NotNil(t, engine)

	// Should be able to compile a valid expression
	expr, err := engine.Compile(`claims["sub"] == "user123"`)
	require.NoError(t, err)
	require.NotNil(t, expr)
}

func TestEngine_Compile_ValidExpressions(t *testing.T) {
	t.Parallel()

	engine := newTestClaimsEngine()

	tests := []struct {
		name string
		expr string
	}{
		{
			name: "string equality",
			expr: `claims["sub"] == "user123"`,
		},
		{
			name: "membership in array",
			expr: `"admins" in claims["groups"]`,
		},
		{
			name: "key exists in map",
			expr: `"act" in claims`,
		},
		{
			name: "nested access",
			expr: `claims["act"]["sub"] == "agent123"`,
		},
		{
			name: "boolean and",
			expr: `"admins" in claims["groups"] && !("act" in claims)`,
		},
		{
			name: "boolean or",
			expr: `claims["sub"] == "user1" || claims["sub"] == "user2"`,
		},
		{
			name: "exists function",
			expr: `claims["groups"].exists(g, g in ["admin", "sre"])`,
		},
		{
			name: "string starts with",
			expr: `claims["sub"].startsWith("user-")`,
		},
		{
			name: "ternary expression",
			expr: `"act" in claims ? "delegated" : "direct"`,
		},
		{
			name: "true literal",
			expr: `true`,
		},
		{
			name: "false literal",
			expr: `false`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			expr, err := engine.Compile(tt.expr)
			require.NoError(t, err)
			require.NotNil(t, expr)
			assert.Equal(t, tt.expr, expr.Source())
		})
	}
}

func TestEngine_Compile_ParseErrors(t *testing.T) {
	t.Parallel()

	engine := newTestClaimsEngine()

	tests := []struct {
		name string
		expr string
	}{
		{
			name: "unclosed bracket",
			expr: `claims["sub"`,
		},
		{
			name: "invalid operator",
			expr: `claims["sub"] === "user123"`,
		},
		{
			name: "unclosed string",
			expr: `claims["sub] == "user123"`,
		},
		{
			name: "missing operand",
			expr: `claims["sub"] ==`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			expr, err := engine.Compile(tt.expr)
			require.Error(t, err)
			require.Nil(t, expr)

			var parseErr *cel.ParseError
			assert.True(t, errors.As(err, &parseErr), "expected ParseError, got %T", err)
		})
	}
}

func TestEngine_Compile_CheckErrors(t *testing.T) {
	t.Parallel()

	engine := newTestClaimsEngine()

	tests := []struct {
		name string
		expr string
	}{
		{
			name: "undefined variable",
			expr: `undefined_var == "test"`,
		},
		{
			name: "undefined function",
			expr: `undefined_func(claims)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			expr, err := engine.Compile(tt.expr)
			require.Error(t, err)
			require.Nil(t, expr)

			var checkErr *cel.CheckError
			assert.True(t, errors.As(err, &checkErr), "expected CheckError, got %T", err)
		})
	}
}

func TestEngine_Check(t *testing.T) {
	t.Parallel()

	engine := newTestClaimsEngine()

	t.Run("valid expression", func(t *testing.T) {
		t.Parallel()
		err := engine.Check(`claims["sub"] == "user123"`)
		require.NoError(t, err)
	})

	t.Run("invalid expression", func(t *testing.T) {
		t.Parallel()
		err := engine.Check(`claims["sub"`)
		require.Error(t, err)

		var parseErr *cel.ParseError
		assert.True(t, errors.As(err, &parseErr))
	})
}

func TestCompiledExpression_Evaluate(t *testing.T) {
	t.Parallel()

	engine := newTestClaimsEngine()

	tests := []struct {
		name     string
		expr     string
		claims   map[string]any
		expected any
	}{
		{
			name: "string equality true",
			expr: `claims["sub"] == "user123"`,
			claims: map[string]any{
				"sub": "user123",
			},
			expected: true,
		},
		{
			name: "string equality false",
			expr: `claims["sub"] == "user123"`,
			claims: map[string]any{
				"sub": "other-user",
			},
			expected: false,
		},
		{
			name: "membership in array true",
			expr: `"admins" in claims["groups"]`,
			claims: map[string]any{
				"groups": []any{"users", "admins", "developers"},
			},
			expected: true,
		},
		{
			name: "membership in array false",
			expr: `"admins" in claims["groups"]`,
			claims: map[string]any{
				"groups": []any{"users", "developers"},
			},
			expected: false,
		},
		{
			name: "key exists in map true",
			expr: `"act" in claims`,
			claims: map[string]any{
				"sub": "user123",
				"act": map[string]any{
					"sub": "agent456",
				},
			},
			expected: true,
		},
		{
			name: "key missing from map",
			expr: `"act" in claims`,
			claims: map[string]any{
				"sub": "user123",
			},
			expected: false,
		},
		{
			name: "complex boolean expression",
			expr: `"admins" in claims["groups"] && !("act" in claims)`,
			claims: map[string]any{
				"sub":    "user123",
				"groups": []any{"admins"},
			},
			expected: true,
		},
		{
			name: "complex boolean with agent delegation",
			expr: `"admins" in claims["groups"] && !("act" in claims)`,
			claims: map[string]any{
				"sub":    "user123",
				"groups": []any{"admins"},
				"act": map[string]any{
					"sub": "agent456",
				},
			},
			expected: false,
		},
		{
			name: "ternary expression",
			expr: `"act" in claims ? "delegated" : "direct"`,
			claims: map[string]any{
				"sub": "user123",
				"act": map[string]any{
					"sub": "agent456",
				},
			},
			expected: "delegated",
		},
		{
			name: "ternary expression no delegation",
			expr: `"act" in claims ? "delegated" : "direct"`,
			claims: map[string]any{
				"sub": "user123",
			},
			expected: "direct",
		},
		{
			name:     "true literal",
			expr:     `true`,
			claims:   map[string]any{},
			expected: true,
		},
		{
			name:     "false literal",
			expr:     `false`,
			claims:   map[string]any{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			expr, err := engine.Compile(tt.expr)
			require.NoError(t, err)

			ctx := map[string]any{"claims": tt.claims}
			result, err := expr.Evaluate(ctx)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCompiledExpression_EvaluateBool(t *testing.T) {
	t.Parallel()

	engine := newTestClaimsEngine()

	t.Run("returns true", func(t *testing.T) {
		t.Parallel()

		expr, err := engine.Compile(`claims["sub"] == "user123"`)
		require.NoError(t, err)

		ctx := map[string]any{"claims": map[string]any{"sub": "user123"}}
		result, err := expr.EvaluateBool(ctx)
		require.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("returns false", func(t *testing.T) {
		t.Parallel()

		expr, err := engine.Compile(`claims["sub"] == "user123"`)
		require.NoError(t, err)

		ctx := map[string]any{"claims": map[string]any{"sub": "other"}}
		result, err := expr.EvaluateBool(ctx)
		require.NoError(t, err)
		assert.False(t, result)
	})

	t.Run("error on non-bool result", func(t *testing.T) {
		t.Parallel()

		expr, err := engine.Compile(`claims["sub"]`)
		require.NoError(t, err)

		ctx := map[string]any{"claims": map[string]any{"sub": "user123"}}
		_, err = expr.EvaluateBool(ctx)
		require.Error(t, err)
		assert.ErrorIs(t, err, cel.ErrInvalidResult)
	})

	t.Run("evaluation error wraps ErrEvaluation", func(t *testing.T) {
		t.Parallel()

		// Compile an expression that accesses a nested key on a non-map value
		expr, err := engine.Compile(`claims["missing"]["nested"]`)
		require.NoError(t, err)

		// Provide an empty claims map so the nested access fails at runtime
		ctx := map[string]any{"claims": map[string]any{}}
		_, err = expr.EvaluateBool(ctx)
		require.Error(t, err)
		assert.ErrorIs(t, err, cel.ErrEvaluation)
	})
}

func TestParseError_Details(t *testing.T) {
	t.Parallel()

	engine := newTestClaimsEngine()

	_, err := engine.Compile(`claims["sub"`)
	require.Error(t, err)

	var parseErr *cel.ParseError
	require.True(t, errors.As(err, &parseErr))

	// Should contain source and error details
	assert.Contains(t, parseErr.Error(), "parse")
	assert.Contains(t, parseErr.Source, `claims["sub"`)
	assert.NotEmpty(t, parseErr.Errors)
}

func TestCheckError_Details(t *testing.T) {
	t.Parallel()

	engine := newTestClaimsEngine()

	_, err := engine.Compile(`undefined_var == "test"`)
	require.Error(t, err)

	var checkErr *cel.CheckError
	require.True(t, errors.As(err, &checkErr))

	// Should contain source and error details
	assert.Contains(t, checkErr.Error(), "check")
	assert.Contains(t, checkErr.Source, "undefined_var")
	assert.NotEmpty(t, checkErr.Errors)
}

func TestEngine_Concurrency(t *testing.T) {
	t.Parallel()

	engine := newTestClaimsEngine()

	// Compile the expression once
	expr, err := engine.Compile(`"admins" in claims["groups"]`)
	require.NoError(t, err)

	// Evaluate concurrently
	const numGoroutines = 100
	results := make(chan bool, numGoroutines)
	errs := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(i int) {
			groups := []any{"users"}
			if i%2 == 0 {
				groups = append(groups, "admins")
			}

			ctx := map[string]any{
				"claims": map[string]any{
					"groups": groups,
				},
			}

			result, err := expr.EvaluateBool(ctx)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}

	// Collect results
	for i := 0; i < numGoroutines; i++ {
		select {
		case err := <-errs:
			t.Fatalf("unexpected error: %v", err)
		case result := <-results:
			// Even indices should have "admins" and return true
			// We can't verify specific results without tracking indices
			_ = result
		}
	}
}

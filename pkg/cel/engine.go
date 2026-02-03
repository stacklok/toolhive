// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cel provides a generic CEL expression engine for evaluating
// expressions against arbitrary data contexts.
package cel

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
)

const (
	// DefaultMaxExpressionLength is the maximum allowed length for a CEL expression.
	// This limit prevents DoS attacks via excessively long expressions.
	DefaultMaxExpressionLength = 10000

	// DefaultCostLimit is the default runtime cost limit for CEL program evaluation.
	// This prevents DoS attacks via expensive operations in expressions.
	DefaultCostLimit = 1000000
)

// Engine provides CEL expression compilation and evaluation capabilities.
// It is safe for concurrent use from multiple goroutines.
type Engine struct {
	envCache            *envCache
	factory             envFactory
	maxExpressionLength int
	costLimit           uint64
}

// envFactory is a function that creates a CEL environment.
type envFactory func() (*cel.Env, error)

// envCache holds a lazily-initialized CEL environment.
type envCache struct {
	once sync.Once
	env  *cel.Env
	err  error
}

// CompiledExpression represents a pre-compiled CEL program ready for evaluation.
type CompiledExpression struct {
	source  string
	program cel.Program
}

// Source returns the original expression source string.
func (ce *CompiledExpression) Source() string {
	return ce.source
}

// NewEngine creates a new CEL engine with the specified variable declarations.
// The options are passed to cel.NewEnv to configure the CEL environment.
//
// The engine is created with default limits for expression length and evaluation cost
// to prevent denial-of-service attacks. Use WithMaxExpressionLength and WithCostLimit
// to customize these limits if needed.
//
// Example usage:
//
//	engine := cel.NewEngine(
//	    cel.Variable("claims", cel.MapType(cel.StringType, cel.DynType)),
//	)
func NewEngine(options ...cel.EnvOption) *Engine {
	return &Engine{
		envCache:            &envCache{},
		maxExpressionLength: DefaultMaxExpressionLength,
		costLimit:           DefaultCostLimit,
		factory: func() (*cel.Env, error) {
			return cel.NewEnv(options...)
		},
	}
}

// WithMaxExpressionLength sets the maximum allowed length for CEL expressions.
// Expressions exceeding this length will be rejected during compilation.
func (e *Engine) WithMaxExpressionLength(maxLen int) *Engine {
	e.maxExpressionLength = maxLen
	return e
}

// WithCostLimit sets the runtime cost limit for CEL program evaluation.
// Programs that exceed this cost during evaluation will return an error.
func (e *Engine) WithCostLimit(limit uint64) *Engine {
	e.costLimit = limit
	return e
}

// getEnv returns the CEL environment, creating it lazily on first access.
func (e *Engine) getEnv() (*cel.Env, error) {
	e.envCache.once.Do(func() {
		e.envCache.env, e.envCache.err = e.factory()
	})
	return e.envCache.env, e.envCache.err
}

// Compile parses and compiles a CEL expression, returning a CompiledExpression
// that can be evaluated multiple times against different contexts.
//
// Returns an error if the expression exceeds the maximum length, a ParseError
// if the expression has syntax errors, or a CheckError if the expression has
// type checking errors.
func (e *Engine) Compile(expr string) (*CompiledExpression, error) {
	// Check expression length to prevent DoS via excessively long expressions
	if len(expr) > e.maxExpressionLength {
		return nil, fmt.Errorf("%w: expression length %d exceeds maximum of %d",
			ErrExpressionCheck, len(expr), e.maxExpressionLength)
	}

	env, err := e.getEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to get CEL environment: %w", err)
	}

	// Parse the expression
	parsedAst, issues := env.Parse(expr)
	if issues.Err() != nil {
		return nil, newParseError(expr, issues)
	}

	// Type check the expression
	checkedAst, issues := env.Check(parsedAst)
	if issues.Err() != nil {
		return nil, newCheckError(expr, issues)
	}

	// Compile to a program with cost limit to prevent DoS via expensive operations
	program, err := env.Program(checkedAst, cel.CostLimit(e.costLimit))
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL program for %q: %w", expr, err)
	}

	return &CompiledExpression{
		source:  expr,
		program: program,
	}, nil
}

// Check verifies that a CEL expression is syntactically and semantically valid
// without creating a compiled program. This is useful for configuration validation.
//
// Returns an error if the expression exceeds the maximum length, a ParseError
// if the expression has syntax errors, or a CheckError if the expression has
// type checking errors.
func (e *Engine) Check(expr string) error {
	// Check expression length to prevent DoS via excessively long expressions
	if len(expr) > e.maxExpressionLength {
		return fmt.Errorf("%w: expression length %d exceeds maximum of %d",
			ErrExpressionCheck, len(expr), e.maxExpressionLength)
	}

	env, err := e.getEnv()
	if err != nil {
		return fmt.Errorf("failed to get CEL environment: %w", err)
	}

	// Parse the expression
	parsedAst, issues := env.Parse(expr)
	if issues.Err() != nil {
		return newParseError(expr, issues)
	}

	// Type check the expression
	_, issues = env.Check(parsedAst)
	if issues.Err() != nil {
		return newCheckError(expr, issues)
	}

	return nil
}

// Evaluate executes the compiled expression against the provided context
// and returns the result. The context should contain values for all variables
// declared when creating the Engine.
//
// Example:
//
//	ctx := map[string]any{"myVar": someValue}
func (ce *CompiledExpression) Evaluate(ctx map[string]any) (any, error) {
	out, _, err := ce.program.Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrEvaluation, err)
	}
	return out.Value(), nil
}

// EvaluateBool executes the compiled expression and returns the result as a bool.
// Returns an error if the expression does not evaluate to a boolean.
func (ce *CompiledExpression) EvaluateBool(ctx map[string]any) (bool, error) {
	result, err := ce.Evaluate(ctx)
	if err != nil {
		return false, err
	}

	boolResult, ok := result.(bool)
	if !ok {
		return false, fmt.Errorf("%w: expected bool, got %T", ErrInvalidResult, result)
	}

	return boolResult, nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cel

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/cel-go/cel"
)

// Sentinel errors for CEL operations.
var (
	// ErrExpressionCheck is returned when a CEL expression fails syntax or type checking.
	ErrExpressionCheck = errors.New("CEL expression check failed")

	// ErrEvaluation is returned when CEL expression evaluation fails.
	ErrEvaluation = errors.New("CEL expression evaluation failed")

	// ErrInvalidResult is returned when the CEL expression returns an unexpected type.
	ErrInvalidResult = errors.New("CEL expression returned invalid result type")
)

// ErrKind is a string identifying the type of CEL error.
type ErrKind string

const (
	// ErrKindParse indicates a syntax error in the CEL expression.
	ErrKindParse ErrKind = "parse"
	// ErrKindCheck indicates a type checking error in the CEL expression.
	ErrKindCheck ErrKind = "check"
)

// ErrInstance represents one occurrence of an error in a CEL expression.
type ErrInstance struct {
	Line int    `json:"line,omitempty"`
	Col  int    `json:"col,omitempty"`
	Msg  string `json:"msg,omitempty"`
}

// ErrDetails contains structured error information for CEL expressions.
type ErrDetails struct {
	Errors []ErrInstance `json:"errors,omitempty"`
	Source string        `json:"source,omitempty"`
}

// AsJSON returns the ErrDetails as a JSON string.
func (ed *ErrDetails) AsJSON() string {
	edBytes, err := json.Marshal(ed)
	if err != nil {
		return fmt.Sprintf(`{"error": "failed to marshal JSON: %s"}`, err)
	}
	return string(edBytes)
}

// errDetailsFromCelIssues converts CEL issues to ErrDetails.
func errDetailsFromCelIssues(source string, issues *cel.Issues) ErrDetails {
	var ed ErrDetails

	ed.Source = source
	ed.Errors = make([]ErrInstance, 0, len(issues.Errors()))
	for _, err := range issues.Errors() {
		ed.Errors = append(ed.Errors, ErrInstance{
			Line: err.Location.Line(),
			Col:  err.Location.Column(),
			Msg:  err.Message,
		})
	}

	return ed
}

// ParseError represents a CEL syntax error with location information.
type ParseError struct {
	ErrDetails
	original error
}

// Error implements the error interface for ParseError.
func (pe *ParseError) Error() string {
	return fmt.Sprintf("CEL %s error in expression %q: %s", ErrKindParse, pe.Source, pe.original)
}

// Unwrap returns the underlying error.
func (pe *ParseError) Unwrap() error {
	return pe.original
}

// CheckError represents a CEL type checking error with location information.
type CheckError struct {
	ErrDetails
	original error
}

// Error implements the error interface for CheckError.
func (ce *CheckError) Error() string {
	return fmt.Sprintf("CEL %s error in expression %q: %s", ErrKindCheck, ce.Source, ce.original)
}

// Unwrap returns the underlying error.
func (ce *CheckError) Unwrap() error {
	return ce.original
}

// newParseError creates a ParseError from CEL issues.
func newParseError(source string, issues *cel.Issues) error {
	return &ParseError{
		ErrDetails: errDetailsFromCelIssues(source, issues),
		original:   fmt.Errorf("%w: %w", ErrExpressionCheck, issues.Err()),
	}
}

// newCheckError creates a CheckError from CEL issues.
func newCheckError(source string, issues *cel.Issues) error {
	return &CheckError{
		ErrDetails: errDetailsFromCelIssues(source, issues),
		original:   fmt.Errorf("%w: %w", ErrExpressionCheck, issues.Err()),
	}
}

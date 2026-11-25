// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package validation provides validation functionality for the ToolHive operator.
package validation

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cedar-policy/cedar-go/ast"
)

// maxPolicyBytes bounds the size of a single Cedar policy, and with it the
// parser's recursion depth. At this size the worst input measured on cedar-go
// v1.8.0 (nested set literals) needs 16 MiB of goroutine stack and about
// 33 MB peak RSS. Re-measure when upgrading cedar-go.
const maxPolicyBytes = 8 << 10

var (
	errPolicyTooLarge     = errors.New("policy is too large")
	errMultipleStatements = errors.New("policy contains more than one statement")
)

// ValidateCedarPolicies validates the syntax of each Cedar policy string in the
// provided slice. Each entry must hold a single Cedar policy statement, is
// limited to maxPolicyBytes, and is only parsed, never compiled. It returns an
// error for the first entry that is too large, fails to parse, or contains more
// than one statement, or nil if all entries are valid (including when the slice
// is empty or nil).
func ValidateCedarPolicies(policies []string) error {
	for i, policy := range policies {
		if len(policy) > maxPolicyBytes {
			return fmt.Errorf("cedar policy at index %d rejected: %w (%d bytes, max %d)",
				i, errPolicyTooLarge, len(policy), maxPolicyBytes)
		}
		if err := validateSingleCedarPolicy(i, policy); err != nil {
			return err
		}
	}
	return nil
}

// validateSingleCedarPolicy parses policy without compiling it and checks that
// nothing other than whitespace or comments follows the first statement.
//
// Use ast.Policy, not cedar.Policy or cedar.PolicyList: compiling copies the
// left-hand side of every "has a.b.c" chain, which costs quadratic time and
// memory (exponential when nested). In cedar-go ast.Policy.UnmarshalCedar only
// parses, even though its doc comment says it also compiles, and compiling
// never returns an error. TestValidateCedarPoliciesDoesNotCompile fails its
// allocation budget if compilation is reintroduced.
//
// ast.Policy.UnmarshalCedar tokenizes the whole slice (validating string and
// comment termination everywhere) and then stops parsing at the first
// statement's terminating semicolon. In Cedar's grammar a semicolon outside
// strings and comments only ever terminates a policy statement, so scanning to
// that first semicolon and checking the remainder catches both trailing syntax
// errors and multi-statement entries without compiling.
func validateSingleCedarPolicy(index int, policy string) error {
	var p ast.Policy
	if err := p.UnmarshalCedar([]byte(policy)); err != nil {
		return fmt.Errorf("cedar policy at index %d has invalid syntax: %w", index, err)
	}
	trailing := trailingAfterStatement(policy)
	if trailing == "" {
		return nil
	}
	var extra ast.Policy
	if err := extra.UnmarshalCedar([]byte(trailing)); err != nil {
		return fmt.Errorf("cedar policy at index %d has invalid syntax: %w", index, err)
	}
	return fmt.Errorf("cedar policy at index %d rejected: %w", index, errMultipleStatements)
}

func trailingAfterStatement(s string) string {
	for i := 0; i < len(s); {
		i = skipTrivia(s, i)
		if i >= len(s) {
			return ""
		}
		switch s[i] {
		case ';':
			return s[skipTrivia(s, i+1):]
		case '"':
			i = skipQuotedString(s, i)
		default:
			i++
		}
	}
	return ""
}

func skipTrivia(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			i++
		case '/':
			if strings.HasPrefix(s[i:], "//") {
				nl := strings.IndexByte(s[i+2:], '\n')
				if nl < 0 {
					return len(s)
				}
				i += 2 + nl + 1
			} else if strings.HasPrefix(s[i:], "/*") {
				end := strings.Index(s[i+2:], "*/")
				if end < 0 {
					return len(s)
				}
				i += 2 + end + 2
			} else {
				return i
			}
		default:
			return i
		}
	}
	return i
}

func skipQuotedString(s string, i int) int {
	i++ // skip opening '"'
	for i < len(s) {
		if s[i] == '\\' {
			i += 2
			continue
		}
		if s[i] == '"' {
			return i + 1
		}
		i++
	}
	return len(s)
}

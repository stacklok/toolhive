// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package validation provides validation functionality for the ToolHive operator.
package validation

import (
	"errors"
	"fmt"

	"github.com/cedar-policy/cedar-go/ast"
)

// maxPolicyBytes bounds the size of a single Cedar policy, and with it the
// parser's recursion depth. At this size the worst input measured on cedar-go
// v1.8.0 (nested set literals) needs 16 MiB of goroutine stack and about
// 33 MB peak RSS. Re-measure when upgrading cedar-go.
const maxPolicyBytes = 8 << 10

var errPolicyTooLarge = errors.New("policy is too large")

// ValidateCedarPolicies validates the syntax of each Cedar policy string in the
// provided slice. Each policy is limited to maxPolicyBytes and is only parsed,
// never compiled. It returns an error for the first policy that is too large or
// fails to parse, or nil if all policies are valid (including when the slice is
// empty or nil).
func ValidateCedarPolicies(policies []string) error {
	for i, policy := range policies {
		if len(policy) > maxPolicyBytes {
			return fmt.Errorf("cedar policy at index %d rejected: %w (%d bytes, max %d)",
				i, errPolicyTooLarge, len(policy), maxPolicyBytes)
		}
		// Use ast.Policy, not cedar.Policy: cedar.Policy.UnmarshalCedar also
		// compiles, and compiling copies the left-hand side of every "has a.b.c"
		// chain, which costs quadratic time and memory (exponential when nested).
		// In cedar-go v1.8.0 ast.Policy.UnmarshalCedar only parses, even though
		// its doc comment says it also compiles. Compiling never returns an
		// error, so the accepted policies and error text are the same.
		// TestValidateCedarPoliciesDoesNotCompile fails its allocation budget if
		// this is switched back.
		var p ast.Policy
		if err := p.UnmarshalCedar([]byte(policy)); err != nil {
			return fmt.Errorf("cedar policy at index %d has invalid syntax: %w", i, err)
		}
	}
	return nil
}

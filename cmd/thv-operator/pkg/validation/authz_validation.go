// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"fmt"

	cedar "github.com/cedar-policy/cedar-go"
)

// ValidateCedarPolicySyntax validates that all Cedar policy strings are syntactically valid.
// Returns nil if policies is empty or all policies parse successfully.
func ValidateCedarPolicySyntax(policies []string) error {
	for i, policy := range policies {
		var p cedar.Policy
		if err := p.UnmarshalCedar([]byte(policy)); err != nil {
			return fmt.Errorf("cedar policy at index %d has invalid syntax: %w", i, err)
		}
	}
	return nil
}

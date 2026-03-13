// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"fmt"

	cedar "github.com/cedar-policy/cedar-go"
)

// ValidateCedarPolicies validates the syntax of each Cedar policy string in the
// provided slice. It returns an error for the first policy that fails to parse,
// or nil if all policies are valid (including when the slice is empty or nil).
func ValidateCedarPolicies(policies []string) error {
	for i, policy := range policies {
		var p cedar.Policy
		if err := p.UnmarshalCedar([]byte(policy)); err != nil {
			return fmt.Errorf("cedar policy at index %d has invalid syntax: %w", i, err)
		}
	}
	return nil
}

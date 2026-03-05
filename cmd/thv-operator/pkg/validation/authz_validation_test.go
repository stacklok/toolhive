// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

func TestValidateCedarPolicySyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		policies    []string
		wantErr     bool
		errContains string
	}{
		{
			name:     "nil policies slice is valid",
			policies: nil,
			wantErr:  false,
		},
		{
			name:     "empty policies slice is valid",
			policies: []string{},
			wantErr:  false,
		},
		{
			name: "valid Cedar policy is accepted",
			policies: []string{
				"permit(principal, action, resource);",
			},
			wantErr: false,
		},
		{
			name: "multiple valid Cedar policies are accepted",
			policies: []string{
				"permit(principal, action, resource);",
				`forbid(principal, action, resource) unless {
					resource.owner == principal
				};`,
			},
			wantErr: false,
		},
		{
			name: "invalid Cedar policy syntax returns error",
			policies: []string{
				"not valid cedar",
			},
			wantErr:     true,
			errContains: "invalid syntax",
		},
		{
			name: "mix of valid and invalid policies reports index of invalid policy",
			policies: []string{
				"permit(principal, action, resource);",
				"not valid cedar",
			},
			wantErr:     true,
			errContains: "index 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validation.ValidateCedarPolicySyntax(tt.policies)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

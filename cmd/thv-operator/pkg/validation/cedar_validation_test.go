// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

func TestValidateCedarPolicies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		policies    []string
		wantErr     bool
		errContains string
	}{
		{
			name:     "nil policies",
			policies: nil,
			wantErr:  false,
		},
		{
			name:     "empty policies",
			policies: []string{},
			wantErr:  false,
		},
		{
			name:     "valid permit policy",
			policies: []string{"permit (principal, action, resource);"},
			wantErr:  false,
		},
		{
			name:     "valid forbid policy",
			policies: []string{"forbid (principal, action, resource);"},
			wantErr:  false,
		},
		{
			name:        "invalid syntax",
			policies:    []string{"not valid cedar"},
			wantErr:     true,
			errContains: "invalid syntax",
		},
		{
			name: "mixed valid and invalid",
			policies: []string{
				"permit (principal, action, resource);",
				"bad policy",
			},
			wantErr:     true,
			errContains: "index 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validation.ValidateCedarPolicies(tt.policies)

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

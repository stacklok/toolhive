// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

const validPolicy = "permit (principal, action, resource);"

// paddedPolicy returns a valid policy padded with a trailing comment to
// exactly size bytes.
func paddedPolicy(t *testing.T, size int) string {
	t.Helper()
	pad := size - len(validPolicy) - len("//")
	require.GreaterOrEqual(t, pad, 0)
	p := validPolicy + "//" + strings.Repeat("a", pad)
	require.Len(t, p, size)
	return p
}

func TestValidateCedarPolicies(t *testing.T) {
	t.Parallel()

	deepParens := "permit(principal,action,resource) when { " +
		strings.Repeat("(", 300000) + "true" + strings.Repeat(")", 300000) + " };"

	tests := []struct {
		name        string
		policies    []string
		wantErr     bool
		wantErrIs   error
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
		{
			name:     "policy of exactly the maximum size is accepted",
			policies: []string{paddedPolicy(t, validation.MaxPolicyBytes)},
			wantErr:  false,
		},
		{
			name:        "policy one byte over the maximum size is rejected",
			policies:    []string{paddedPolicy(t, validation.MaxPolicyBytes+1)},
			wantErr:     true,
			wantErrIs:   validation.ErrPolicyTooLarge,
			errContains: "policy is too large (8193 bytes, max 8192)",
		},
		{
			name:        "deeply nested parentheses are rejected before parsing",
			policies:    []string{deepParens},
			wantErr:     true,
			wantErrIs:   validation.ErrPolicyTooLarge,
			errContains: "too large",
		},
		{
			name:        "oversized policy after a valid one names its index",
			policies:    []string{validPolicy, paddedPolicy(t, validation.MaxPolicyBytes+1)},
			wantErr:     true,
			wantErrIs:   validation.ErrPolicyTooLarge,
			errContains: "cedar policy at index 1 rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validation.ValidateCedarPolicies(tt.policies)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					require.ErrorIs(t, err, tt.wantErrIs)
				}
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
					require.NotContains(t, err.Error(), "%")
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidateCedarPoliciesDoesNotCompile fails if validation compiles the
// policy. It is deliberately not parallel: a test without t.Parallel runs
// before the parallel tests resume, so the process-wide MemStats delta only
// reflects this call.
//
//nolint:paralleltest // see above
func TestValidateCedarPoliciesDoesNotCompile(t *testing.T) {
	p := "permit(principal,action,resource) when { " +
		strings.Repeat("(", 16) + "context has a.a" + strings.Repeat(") has a.a", 16) + " };"

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	require.NoError(t, validation.ValidateCedarPolicies([]string{p}))
	runtime.ReadMemStats(&after)

	// Parsing allocates about 25 KB; compiling (cedar.Policy) allocates about
	// 46 MB at this depth.
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(4<<20))
}

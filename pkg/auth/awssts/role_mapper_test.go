// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package awssts_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/awssts"
)

func TestValidateRoleArn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		roleArn string
		wantErr bool
	}{
		// Valid ARNs
		{
			name:    "valid standard role",
			roleArn: "arn:aws:iam::123456789012:role/MyRole",
			wantErr: false,
		},
		{
			name:    "valid role with path",
			roleArn: "arn:aws:iam::123456789012:role/service-role/MyRole",
			wantErr: false,
		},
		{
			name:    "valid china partition",
			roleArn: "arn:aws-cn:iam::123456789012:role/MyRole",
			wantErr: false,
		},
		// Invalid ARNs
		{
			name:    "empty string",
			roleArn: "",
			wantErr: true,
		},
		{
			name:    "invalid format",
			roleArn: "not-an-arn",
			wantErr: true,
		},
		{
			name:    "non-IAM service",
			roleArn: "arn:aws:s3:::my-bucket",
			wantErr: true,
		},
		{
			name:    "IAM user instead of role",
			roleArn: "arn:aws:iam::123456789012:user/MyUser",
			wantErr: true,
		},
		{
			name:    "invalid account ID length",
			roleArn: "arn:aws:iam::12345:role/MyRole",
			wantErr: true,
		},
		{
			name:    "non-digit characters in account ID",
			roleArn: "arn:aws:iam::12345678901a:role/MyRole",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := awssts.ValidateRoleArn(tt.roleArn)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, awssts.ErrInvalidRoleArn)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestNewRoleMapper(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       *awssts.Config
		wantErr   bool
		wantErrIs error
	}{
		{
			name:    "nil config returns error",
			cfg:     nil,
			wantErr: true,
		},
		{
			name: "simple claim mapping",
			cfg: &awssts.Config{
				Region:    "us-east-1",
				RoleClaim: "groups",
				RoleMappings: []awssts.RoleMapping{
					{
						Claim:    "admins",
						RoleArn:  "arn:aws:iam::123456789012:role/AdminRole",
						Priority: 1,
					},
				},
			},
		},
		{
			name: "invalid CEL matcher",
			cfg: &awssts.Config{
				Region: "us-east-1",
				RoleMappings: []awssts.RoleMapping{
					{
						Matcher:  `invalid syntax here`,
						RoleArn:  "arn:aws:iam::123456789012:role/AdminRole",
						Priority: 1,
					},
				},
			},
			wantErr:   true,
			wantErrIs: awssts.ErrInvalidMatcher,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rm, err := awssts.NewRoleMapper(tt.cfg)
			if !tt.wantErr {
				require.NoError(t, err)
				assert.NotNil(t, rm)
				return
			}
			require.Error(t, err)
			if tt.wantErrIs != nil {
				assert.ErrorIs(t, err, tt.wantErrIs)
			}
			assert.Nil(t, rm)
		})
	}
}

func TestRoleMapper_SelectRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *awssts.Config
		claims   map[string]any
		expected string
		wantErr  error
	}{
		// Simple claim matching with default fallback
		{
			name: "match admins group",
			cfg: &awssts.Config{
				Region:          "us-east-1",
				RoleClaim:       "groups",
				FallbackRoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
				RoleMappings: []awssts.RoleMapping{
					{Claim: "admins", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
					{Claim: "developers", RoleArn: "arn:aws:iam::123456789012:role/DevRole", Priority: 2},
				},
			},
			claims:   map[string]any{"sub": "user123", "groups": []any{"users", "admins"}},
			expected: "arn:aws:iam::123456789012:role/AdminRole",
		},
		{
			name: "priority selection when multiple match",
			cfg: &awssts.Config{
				Region:          "us-east-1",
				RoleClaim:       "groups",
				FallbackRoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
				RoleMappings: []awssts.RoleMapping{
					{Claim: "admins", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
					{Claim: "developers", RoleArn: "arn:aws:iam::123456789012:role/DevRole", Priority: 2},
				},
			},
			claims:   map[string]any{"sub": "user123", "groups": []any{"admins", "developers"}},
			expected: "arn:aws:iam::123456789012:role/AdminRole",
		},
		{
			name: "fallback to default when no match",
			cfg: &awssts.Config{
				Region:          "us-east-1",
				RoleClaim:       "groups",
				FallbackRoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
				RoleMappings: []awssts.RoleMapping{
					{Claim: "admins", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			claims:   map[string]any{"sub": "user123", "groups": []any{"users"}},
			expected: "arn:aws:iam::123456789012:role/DefaultRole",
		},
		{
			name: "missing claim falls back to default",
			cfg: &awssts.Config{
				Region:          "us-east-1",
				RoleClaim:       "groups",
				FallbackRoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
				RoleMappings: []awssts.RoleMapping{
					{Claim: "admins", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			claims:   map[string]any{"sub": "user123"},
			expected: "arn:aws:iam::123456789012:role/DefaultRole",
		},
		{
			name: "no default role without match returns error",
			cfg: &awssts.Config{
				Region:    "us-east-1",
				RoleClaim: "groups",
				RoleMappings: []awssts.RoleMapping{
					{Claim: "admins", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			claims:  map[string]any{"sub": "user123", "groups": []any{"users"}},
			wantErr: awssts.ErrNoRoleMapping,
		},
		// No mappings configured
		{
			name: "no mappings returns default role",
			cfg: &awssts.Config{
				Region:          "us-east-1",
				FallbackRoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
			},
			claims:   map[string]any{"sub": "user123"},
			expected: "arn:aws:iam::123456789012:role/DefaultRole",
		},
		// Equal priority preserves config order
		{
			name: "equal priority preserves config order",
			cfg: &awssts.Config{
				Region:    "us-east-1",
				RoleClaim: "groups",
				RoleMappings: []awssts.RoleMapping{
					{Claim: "group-a", RoleArn: "arn:aws:iam::123456789012:role/RoleA", Priority: 1},
					{Claim: "group-b", RoleArn: "arn:aws:iam::123456789012:role/RoleB", Priority: 1},
				},
			},
			claims:   map[string]any{"groups": []any{"group-a", "group-b"}},
			expected: "arn:aws:iam::123456789012:role/RoleA",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rm, err := awssts.NewRoleMapper(tt.cfg)
			require.NoError(t, err)

			role, err := rm.SelectRole(tt.claims)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, role)
			}
		})
	}
}

func TestRoleMapper_SelectRole_CELMatcher(t *testing.T) {
	t.Parallel()

	cfg := &awssts.Config{
		Region:          "us-east-1",
		FallbackRoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
		RoleMappings: []awssts.RoleMapping{
			{
				Matcher:  `"admins" in claims["groups"] && !("act" in claims)`,
				RoleArn:  "arn:aws:iam::123456789012:role/AdminDirectRole",
				Priority: 1,
			},
			{
				Matcher:  `"admins" in claims["groups"]`,
				RoleArn:  "arn:aws:iam::123456789012:role/AdminRole",
				Priority: 2,
			},
			{
				Matcher:  `claims["sub"].startsWith("service-")`,
				RoleArn:  "arn:aws:iam::123456789012:role/ServiceRole",
				Priority: 3,
			},
		},
	}

	rm, err := awssts.NewRoleMapper(cfg)
	require.NoError(t, err)

	tests := []struct {
		name     string
		claims   map[string]any
		expected string
	}{
		{
			name: "admin direct access (no agent delegation)",
			claims: map[string]any{
				"sub":    "user123",
				"groups": []any{"admins"},
			},
			expected: "arn:aws:iam::123456789012:role/AdminDirectRole",
		},
		{
			name: "admin with agent delegation falls back",
			claims: map[string]any{
				"sub":    "user123",
				"groups": []any{"admins"},
				"act": map[string]any{
					"sub": "agent456",
				},
			},
			expected: "arn:aws:iam::123456789012:role/AdminRole",
		},
		{
			name: "service account",
			claims: map[string]any{
				"sub":    "service-worker",
				"groups": []any{"services"},
			},
			expected: "arn:aws:iam::123456789012:role/ServiceRole",
		},
		{
			name: "no match falls back to default",
			claims: map[string]any{
				"sub":    "user123",
				"groups": []any{"users"},
			},
			expected: "arn:aws:iam::123456789012:role/DefaultRole",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			role, err := rm.SelectRole(tt.claims)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, role)
		})
	}
}

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       *awssts.Config
		wantErr   bool
		wantErrIs error
	}{
		{
			name:    "nil config",
			cfg:     nil,
			wantErr: true,
		},
		{
			name: "missing region",
			cfg: &awssts.Config{
				FallbackRoleArn: "arn:aws:iam::123456789012:role/MyRole",
			},
			wantErr:   true,
			wantErrIs: awssts.ErrMissingRegion,
		},
		{
			name: "missing both role_arn and role_mappings",
			cfg: &awssts.Config{
				Region: "us-east-1",
			},
			wantErr:   true,
			wantErrIs: awssts.ErrMissingRoleConfig,
		},
		{
			name: "invalid default role ARN",
			cfg: &awssts.Config{
				Region:          "us-east-1",
				FallbackRoleArn: "invalid-arn",
			},
			wantErr:   true,
			wantErrIs: awssts.ErrInvalidRoleArn,
		},
		{
			name: "valid with default role only",
			cfg: &awssts.Config{
				Region:          "us-east-1",
				FallbackRoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
			},
		},
		{
			name: "valid with simple claim mapping",
			cfg: &awssts.Config{
				Region: "us-east-1",
				RoleMappings: []awssts.RoleMapping{
					{
						Claim:    "admins",
						RoleArn:  "arn:aws:iam::123456789012:role/AdminRole",
						Priority: 1,
					},
				},
			},
		},
		{
			name: "mapping with both claim and matcher",
			cfg: &awssts.Config{
				Region: "us-east-1",
				RoleMappings: []awssts.RoleMapping{
					{
						Claim:    "admins",
						Matcher:  `"admins" in claims["groups"]`,
						RoleArn:  "arn:aws:iam::123456789012:role/AdminRole",
						Priority: 1,
					},
				},
			},
			wantErr:   true,
			wantErrIs: awssts.ErrInvalidRoleMapping,
		},
		{
			name: "mapping with neither claim nor matcher",
			cfg: &awssts.Config{
				Region: "us-east-1",
				RoleMappings: []awssts.RoleMapping{
					{
						RoleArn:  "arn:aws:iam::123456789012:role/AdminRole",
						Priority: 1,
					},
				},
			},
			wantErr:   true,
			wantErrIs: awssts.ErrInvalidRoleMapping,
		},
		{
			name: "mapping with empty role ARN",
			cfg: &awssts.Config{
				Region: "us-east-1",
				RoleMappings: []awssts.RoleMapping{
					{
						Claim:    "admins",
						RoleArn:  "",
						Priority: 1,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "mapping with invalid role ARN",
			cfg: &awssts.Config{
				Region: "us-east-1",
				RoleMappings: []awssts.RoleMapping{
					{
						Claim:    "admins",
						RoleArn:  "invalid-arn",
						Priority: 1,
					},
				},
			},
			wantErr:   true,
			wantErrIs: awssts.ErrInvalidRoleArn,
		},
		{
			name: "session duration below minimum",
			cfg: &awssts.Config{
				Region:          "us-east-1",
				FallbackRoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
				SessionDuration: 100,
			},
			wantErr: true,
		},
		{
			name: "session duration above maximum",
			cfg: &awssts.Config{
				Region:          "us-east-1",
				FallbackRoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
				SessionDuration: 50000,
			},
			wantErr: true,
		},
		{
			name: "claim with CEL injection characters rejected",
			cfg: &awssts.Config{
				Region: "us-east-1",
				RoleMappings: []awssts.RoleMapping{
					{
						Claim:    `") || true || ("`,
						RoleArn:  "arn:aws:iam::123456789012:role/AdminRole",
						Priority: 1,
					},
				},
			},
			wantErr:   true,
			wantErrIs: awssts.ErrInvalidRoleMapping,
		},
		{
			name: "role_claim with unsafe characters rejected",
			cfg: &awssts.Config{
				Region:    "us-east-1",
				RoleClaim: `groups"])||true`,
				RoleMappings: []awssts.RoleMapping{
					{Claim: "admins", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			wantErr:   true,
			wantErrIs: awssts.ErrInvalidRoleMapping,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := awssts.ValidateConfig(tt.cfg)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tt.wantErrIs != nil {
				assert.ErrorIs(t, err, tt.wantErrIs)
			}
		})
	}
}

func TestRoleMapper_Concurrency(t *testing.T) {
	t.Parallel()

	cfg := &awssts.Config{
		Region:          "us-east-1",
		RoleClaim:       "groups",
		FallbackRoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
		RoleMappings: []awssts.RoleMapping{
			{
				Claim:    "admins",
				RoleArn:  "arn:aws:iam::123456789012:role/AdminRole",
				Priority: 1,
			},
			{
				Claim:    "developers",
				RoleArn:  "arn:aws:iam::123456789012:role/DevRole",
				Priority: 2,
			},
		},
	}

	rm, err := awssts.NewRoleMapper(cfg)
	require.NoError(t, err)

	// Run concurrent role selections
	const numGoroutines = 100

	type roleResult struct {
		actual   string
		expected string
	}

	results := make(chan roleResult, numGoroutines)
	errs := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(i int) {
			var groups []any
			var expected string
			switch i % 3 {
			case 0:
				groups = []any{"admins"}
				expected = "arn:aws:iam::123456789012:role/AdminRole"
			case 1:
				groups = []any{"developers"}
				expected = "arn:aws:iam::123456789012:role/DevRole"
			case 2:
				groups = []any{"users"}
				expected = "arn:aws:iam::123456789012:role/DefaultRole"
			}

			claims := map[string]any{
				"sub":    fmt.Sprintf("user%d", i),
				"groups": groups,
			}

			role, err := rm.SelectRole(claims)
			if err != nil {
				errs <- err
				return
			}
			results <- roleResult{actual: role, expected: expected}
		}(i)
	}

	// Collect results - all should succeed with the correct role
	for i := 0; i < numGoroutines; i++ {
		select {
		case err := <-errs:
			t.Fatalf("unexpected error: %v", err)
		case r := <-results:
			assert.Equal(t, r.expected, r.actual)
		}
	}
}

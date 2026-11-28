// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cedar

import (
	"context"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// countPolicies returns the number of policies in ps.
func countPolicies(ps *cedar.PolicySet) int {
	n := 0
	for range ps.All() {
		n++
	}
	return n
}

// listsWeather reports whether the weather tool is listed on tools/list under policies.
func listsWeather(t *testing.T, policies ...string) bool {
	t.Helper()
	ok, err := authorizeWithClaims(t, policies, nil,
		authorizers.MCPFeatureTool, authorizers.MCPOperationRead, "weather", nil)
	require.NoError(t, err)
	return ok
}

// Guards: these hold on main as well and keep existing policy sets unchanged.
func TestToolRead_GuardsKeepExistingVisibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policies []string
		want     bool
	}{
		{
			name:     "broad permit lists the tool",
			policies: []string{"permit(principal, action, resource);"},
			want:     true,
		},
		{
			name: "call-only forbid keeps hiding its tool",
			policies: []string{
				"permit(principal, action, resource);",
				`forbid(principal, action == Action::"call_tool", resource == Tool::"weather");`,
			},
			want: false,
		},
		{
			name: "unscoped permit on resource.operation grants no visibility",
			policies: []string{
				`permit(principal, action, resource) when { resource.operation == "read" };`,
			},
			want: false,
		},
		{
			name: "unless-call forbid does not hide a tool the call permit allows",
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
				`forbid(principal, action, resource) unless { action == Action::"call_tool" };`,
			},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, listsWeather(t, tc.policies...))
		})
	}
}

// Distinguishing: these only hold once read_tool-scoped policies exist.
func TestToolRead_ReadToolPolicies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policies []string
		want     bool
	}{
		{
			name:     "read_tool permit lists a tool that cannot be called",
			policies: []string{`permit(principal, action == Action::"read_tool", resource);`},
			want:     true,
		},
		{
			name: "read_tool permit plus call-only forbid keeps the tool hidden",
			policies: []string{
				`permit(principal, action == Action::"read_tool", resource);`,
				`forbid(principal, action == Action::"call_tool", resource == Tool::"weather");`,
			},
			want: false,
		},
		{
			name: "forbid naming read_tool hides a tool that call allows",
			policies: []string{
				"permit(principal, action, resource);",
				`forbid(principal, action == Action::"read_tool", resource == Tool::"weather");`,
			},
			want: false,
		},
		{
			name: "read_tool permit for another tool does not list this one",
			policies: []string{
				`permit(principal, action == Action::"read_tool", resource == Tool::"other");`,
			},
			want: false,
		},
		{
			name: "scope listing both actions grants visibility",
			policies: []string{
				`permit(principal, action in [Action::"read_tool", Action::"call_tool"], resource);`,
			},
			want: true,
		},
		{
			name: "action in read_tool scope grants visibility",
			policies: []string{
				`permit(principal, action in Action::"read_tool", resource);`,
			},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, listsWeather(t, tc.policies...))
		})
	}
}

func TestMentionsReadToolOutsideScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy string
		want   bool
	}{
		{
			name:   "unscoped forbid whose condition names read_tool is flagged",
			policy: `forbid(principal, action, resource) when { action == Action::"read_tool" };`,
			want:   true,
		},
		{
			name:   "forbid scoped to read_tool is not flagged",
			policy: `forbid(principal, action == Action::"read_tool", resource);`,
			want:   false,
		},
		{
			name:   "unrelated forbid is not flagged",
			policy: `forbid(principal, action == Action::"call_tool", resource);`,
			want:   false,
		},
		{
			name:   "permit mentioning read_tool in conditions is not flagged",
			policy: `permit(principal, action, resource) when { action == Action::"read_tool" };`,
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ps, err := parsePolicySet([]string{tc.policy})
			require.NoError(t, err)
			for _, p := range ps.All() {
				assert.Equal(t, tc.want, mentionsReadToolOutsideScope(p))
			}
		})
	}
}

func TestToolRead_AnswersToolReadNatively(t *testing.T) {
	t.Parallel()

	a, err := NewCedarAuthorizer(ConfigOptions{
		Policies:     []string{"permit(principal, action, resource);"},
		EntitiesJSON: "[]",
	}, "")
	require.NoError(t, err)
	native, ok := authorizers.Authorizer(a).(authorizers.ToolReadNative)
	require.True(t, ok, "the Cedar authorizer must implement ToolReadNative")
	assert.True(t, native.AnswersToolRead())
}

// A Cedar error from a forbid fails closed: the tool is not listed and the error is returned.
// An error from a permit only means that permit did not match, so it neither lists nor
// hides the tool on its own.
func TestToolRead_CedarErrorsFailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		policies  []string
		wantOK    bool
		wantError bool
	}{
		{
			name: "forbid error on the call check fails closed",
			policies: []string{
				"permit(principal, action, resource);",
				`forbid(principal, action == Action::"call_tool", resource) when { context.missing == "x" };`,
			},
			wantOK:    false,
			wantError: true,
		},
		{
			name: "forbid error on the read check fails closed",
			policies: []string{
				`permit(principal, action == Action::"read_tool", resource);`,
				`forbid(principal, action == Action::"read_tool", resource) when { context.missing == "x" };`,
			},
			wantOK:    false,
			wantError: true,
		},
		{
			name: "permit error on the call check alone hides the tool",
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource) when { context.missing == "x" };`,
			},
			wantOK: false,
		},
		{
			name: "permit error on the call check does not hide a read_tool grant",
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource) when { context.missing == "x" };`,
				`permit(principal, action == Action::"read_tool", resource);`,
			},
			wantOK: true,
		},
		{
			name: "permit error on the read check returns an error and hides the tool",
			policies: []string{
				`permit(principal, action == Action::"read_tool", resource) when { context.missing == "x" };`,
			},
			wantOK:    false,
			wantError: true,
		},
		{
			name: "permit error on the read check hides even a call grant",
			policies: []string{
				`permit(principal, action == Action::"call_tool", resource);`,
				`permit(principal, action == Action::"read_tool", resource) when { context.missing == "x" };`,
			},
			wantOK:    false,
			wantError: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ok, err := authorizeWithClaims(t, tc.policies, nil,
				authorizers.MCPFeatureTool, authorizers.MCPOperationRead, "weather", nil)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// A nil policy set yields an empty read set, and a forbid that only mentions read_tool in
// its conditions is left out of it.
func TestReadToolPolicySet_Filtering(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, countPolicies(readToolPolicySet(nil)))

	ps, err := parsePolicySet([]string{
		`forbid(principal, action, resource) when { action == Action::"read_tool" };`,
		`permit(principal, action == Action::"read_tool", resource);`,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, countPolicies(readToolPolicySet(ps)))
}

// UpdatePolicies must rebuild the read set with the call set: a read_tool permit added
// by an update lists the tool, and removing it hides the tool again.
func TestToolRead_UpdatePoliciesRebuildsReadSet(t *testing.T) {
	t.Parallel()

	a, err := NewCedarAuthorizer(ConfigOptions{
		Policies:     []string{"permit(principal, action == Action::\"call_tool\", resource == Tool::\"other\");"},
		EntitiesJSON: "[]",
	}, "")
	require.NoError(t, err)

	read := func() bool {
		t.Helper()
		ctx := auth.WithIdentity(context.Background(), &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{Subject: "test-user", Claims: jwt.MapClaims{"sub": "test-user"}},
		})
		ok, err := a.AuthorizeWithJWTClaims(ctx, authorizers.MCPFeatureTool, authorizers.MCPOperationRead, "weather", nil)
		require.NoError(t, err)
		return ok
	}

	assert.False(t, read(), "no read_tool policy: weather is not listed")

	require.NoError(t, a.(*Authorizer).UpdatePolicies([]string{`permit(principal, action == Action::"read_tool", resource);`}))
	assert.True(t, read(), "read_tool permit added by update lists weather")

	require.NoError(t, a.(*Authorizer).UpdatePolicies([]string{"permit(principal, action == Action::\"call_tool\", resource == Tool::\"other\");"}))
	assert.False(t, read(), "read_tool permit removed by update hides weather again")

	require.NoError(t, a.(*Authorizer).UpdatePolicies([]string{
		`permit(principal, action == Action::"read_tool", resource);`,
		`forbid(principal, action == Action::"read_tool", resource == Tool::"weather");`,
	}))
	assert.False(t, read(), "read_tool forbid added by update hides weather")
}

// With no identity attached, AuthorizeWithJWTClaims fails at the missing-principal guard
// before any policy is evaluated.
func TestAuthorizer_ToolReadWithoutIdentityReturnsErrMissingPrincipal(t *testing.T) {
	t.Parallel()

	a, err := NewCedarAuthorizer(ConfigOptions{
		Policies:     []string{"permit(principal, action, resource);"},
		EntitiesJSON: "[]",
	}, "")
	require.NoError(t, err)

	ok, err := a.AuthorizeWithJWTClaims(
		context.Background(), authorizers.MCPFeatureTool, authorizers.MCPOperationCall, "weather", nil)
	assert.False(t, ok)
	require.ErrorIs(t, err, ErrMissingPrincipal)
}

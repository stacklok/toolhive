// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
)

func TestScopeAuthorizationClaims(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, claimName string
		claims          map[string]any
		allowed, scope  bool
	}{
		{"missing", "", nil, false, true},
		{"empty", "", map[string]any{"scope": ""}, false, true},
		{"all scopes", "", map[string]any{"scope": "read write extra"}, true, false},
		{"partial", "", map[string]any{"scope": "read"}, false, true},
		{"case sensitive", "", map[string]any{"scope": "READ WRITE"}, false, true},
		{"array", "", map[string]any{"scope": []any{"read", "write"}}, true, false},
		{"typed array", "", map[string]any{"scope": []string{"read", "write"}}, true, false},
		{"selected scp", "scp", map[string]any{"scope": "other", "scp": []any{"read", "write"}}, true, false},
		{"no union", "", map[string]any{"scope": "read", "scp": "write"}, false, true},
		{"wrong type", "", map[string]any{"scope": 123}, false, false},
		{"mixed array", "", map[string]any{"scope": []any{"read", 123}}, false, false},
		{"array element is one token", "", map[string]any{"scope": []any{"read write"}}, false, false},
		{"invalid delimiter", "", map[string]any{"scope": "read\twrite"}, false, false},
		{"invalid quote", "", map[string]any{"scope": `read write"`}, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &ScopeAuthorizationConfig{ClaimName: tt.claimName, Tools: map[string]OAuthScopeSet{"write": {"write", "read"}}}
			require.NoError(t, cfg.Validate())
			err := cfg.Check(&auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: tt.claims}}, "write")
			if tt.allowed {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrAuthorizationFailed)
			var scope *InsufficientScopeError
			if tt.scope {
				require.ErrorAs(t, err, &scope)
				require.Equal(t, []string{"read", "write"}, scope.RequiredScopes)
			} else {
				require.NotErrorAs(t, err, &scope)
			}
			require.NoError(t, cfg.Check(nil, "unmapped"))
		})
	}
}

func TestScopeAuthorizationHierarchyAndOwnership(t *testing.T) {
	t.Parallel()
	cfg := &ScopeAuthorizationConfig{Tools: map[string]OAuthScopeSet{"tool": {"read"}},
		ScopeImplications: map[string]OAuthScopeSet{"admin": {"write"}, "write": {"read"}}}
	require.NoError(t, cfg.Validate())
	cloned := cfg.DeepCopy()
	cfg.Tools["tool"][0] = "other"
	cfg.ScopeImplications["write"][0] = "other"
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"scope": "admin"}}}
	require.NoError(t, cloned.Check(identity, "tool"))
	cloned.ScopeImplications["read"] = []string{"admin"}
	require.ErrorContains(t, cloned.Validate(), "cycles")
}

func TestScopeAuthorizationValidation(t *testing.T) {
	t.Parallel()
	for _, cfg := range []*ScopeAuthorizationConfig{
		{},
		{Tools: map[string]OAuthScopeSet{"": {"read"}}},
		{Tools: map[string]OAuthScopeSet{"tool": {}}},
		{Tools: map[string]OAuthScopeSet{"tool": {"read", "read"}}},
		{Tools: map[string]OAuthScopeSet{"tool": {"read write"}}},
		{Tools: map[string]OAuthScopeSet{"tool": {"read\n"}}},
		{Tools: map[string]OAuthScopeSet{"tool": {"read"}}, ScopeImplications: map[string]OAuthScopeSet{"read": {"read"}}},
	} {
		require.Error(t, cfg.Validate())
	}
	var disabled *ScopeAuthorizationConfig
	require.NoError(t, disabled.Validate())
	require.NoError(t, disabled.Check(nil, "anything"))
	require.Nil(t, disabled.DeepCopy())
}

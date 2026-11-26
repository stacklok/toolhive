// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
)

func TestScopeAuthorizationCore(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	cfg.ScopeAuthorization = &vmcp.ScopeAuthorizationConfig{Tools: map[string]vmcp.OAuthScopeSet{"tool_a": {"write"}, "hidden": {"secret"}}}
	m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{{ID: "be1"}}).AnyTimes()
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{backendTool("tool_a")},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"tool_a": backendTarget(), "hidden": backendTarget()}},
	}, nil).AnyTimes()
	m.client.EXPECT().CallTool(gomock.Any(), gomock.Any(), "tool_a", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&vmcp.ToolCallResult{}, nil).Times(1)
	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	// Constructor owns its policy, independent of subsequent caller changes.
	cfg.ScopeAuthorization.Tools["tool_a"][0] = "changed"
	ctx := context.Background()
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"scope": "read"}}}
	tools, err := c.ListTools(ctx, identity)
	require.NoError(t, err)
	require.Len(t, tools, 1, "missing scopes must not hide discoverable tools")
	var refusal *vmcp.InsufficientScopeError
	require.ErrorAs(t, c.CheckToolCall(ctx, identity, "tool_a", nil), &refusal)
	_, err = c.CallTool(ctx, identity, "tool_a", nil, nil)
	require.ErrorAs(t, err, &refusal)
	_, err = c.CallTool(ctx, identity, "hidden", nil, nil)
	require.ErrorIs(t, err, vmcp.ErrNotFound)
	require.NotErrorAs(t, err, &refusal)
	identity.Claims["scope"] = "write"
	_, err = c.CallTool(ctx, identity, "tool_a", nil, nil)
	require.NoError(t, err)
	identity.Claims["scope"] = "read"
	_, err = c.CallTool(ctx, identity, "tool_a", nil, nil)
	require.ErrorAs(t, err, &refusal, "downgraded current claims must take effect immediately")
	// Cedar ceiling still wins when both Cedar and scopes deny.
	c.(*coreVMCP).admission = newCedarAdmission(&mockAuthorizer{results: map[string]mockResult{}})
	err = c.CheckToolCall(ctx, identity, "tool_a", nil)
	require.ErrorIs(t, err, vmcp.ErrAuthorizationFailed)
	require.NotErrorAs(t, err, &refusal)
}

func TestScopeAuthorizationConstructorRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		configure func(*Config)
		message   string
	}{
		{
			name: "invalid scope policy",
			configure: func(cfg *Config) {
				cfg.ScopeAuthorization = &vmcp.ScopeAuthorizationConfig{}
			},
			message: "scope authorization requires",
		},
		{
			name: "scope policy with composite workflow",
			configure: func(cfg *Config) {
				cfg.WorkflowDefs = map[string]*composer.WorkflowDefinition{"workflow": {Name: "workflow"}}
			},
			message: "scope authorization does not support composite workflows",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// No collaborator expectations: invalid configuration must fail before
			// consulting the registry, aggregator, router, or backend client.
			cfg, _ := baseConfig(t)
			cfg.ScopeAuthorization = &vmcp.ScopeAuthorizationConfig{
				Tools: map[string]vmcp.OAuthScopeSet{"tool_a": {"write"}},
			}
			tc.configure(cfg)
			c, err := New(cfg)
			if c != nil {
				t.Cleanup(func() { require.NoError(t, c.Close()) })
			}
			require.ErrorIs(t, err, vmcp.ErrInvalidConfig)
			require.ErrorContains(t, err, tc.message)
			require.Nil(t, c)
		})
	}
}

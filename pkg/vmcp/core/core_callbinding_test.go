// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
)

// callBoundIdentity is the identity of an access token bound to one call of
// tool on server. details overrides authorization_details when non-nil.
func callBoundIdentity(server, tool string, details any) *auth.Identity {
	if details == nil {
		details = []any{map[string]any{
			"type": toolcalldetails.Type, "server": server, "tool": tool,
			"arguments_sha256": "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
			"operation_id":     "op-1",
		}}
	}
	return &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-1", Claims: map[string]any{
		"iss": "https://as.example.com", "sub": "user-1", "jti": "jti-1",
		"exp":                   float64(time.Now().Add(time.Hour).Unix()),
		"authorization_details": details,
	}}}
}

// TestCallTool_CallBindingHoldsAtDispatch changes the route between the
// lookup the call-binding middleware makes and the dispatch: the backend the
// token names drops out of the view, so the name moves to another backend or
// to the composite it shadowed. CallTool must refuse the call and reach no
// backend.
func TestCallTool_CallBindingHoldsAtDispatch(t *testing.T) {
	t.Parallel()

	bound := &vmcp.BackendTarget{WorkloadID: "github", BaseURL: "http://github:8080"}
	other := &vmcp.BackendTarget{WorkloadID: "other", BaseURL: "http://other:8080"}
	atLookup := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{{Name: "read", BackendID: "github"}, {Name: "other.echo", BackendID: "other"}},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{
			"read": bound, "other.echo": other,
		}},
	}
	tests := []struct {
		name       string
		atDispatch *aggregator.AggregatedCapabilities
	}{
		{name: "name moves to another backend", atDispatch: &aggregator.AggregatedCapabilities{
			Tools:        []vmcp.Tool{{Name: "read", BackendID: "other"}, {Name: "other.echo", BackendID: "other"}},
			RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"read": other, "other.echo": other}},
		}},
		{name: "name moves to the composite it shadowed", atDispatch: &aggregator.AggregatedCapabilities{
			Tools:        []vmcp.Tool{{Name: "other.echo", BackendID: "other"}},
			RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"other.echo": other}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, m := baseConfig(t)
			// A composite named like the bound tool, shadowed while the bound
			// backend serves that name. Its step runs on another backend.
			cfg.WorkflowDefs = map[string]*composer.WorkflowDefinition{
				"read": {Name: "read", Steps: []composer.WorkflowStep{
					{ID: "s1", Type: composer.StepTypeTool, Tool: "other.echo"},
				}},
			}
			m.reg.EXPECT().List(gomock.Any()).Return(nil).Times(2)
			gomock.InOrder(
				m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(atLookup, nil),
				m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(tc.atDispatch, nil),
			)
			m.client.EXPECT().
				CallTool(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Times(0)

			c, err := New(cfg)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })

			identity := callBoundIdentity("github", "read", nil)
			tool, err := c.LookupTool(context.Background(), identity, "read")
			require.NoError(t, err)
			require.Equal(t, "github", tool.BackendID, "the middleware's lookup sees the bound backend")

			_, err = c.CallTool(context.Background(), identity, "read", nil, nil)
			require.ErrorIs(t, err, vmcp.ErrAuthorizationFailed)
			assert.NotErrorIs(t, err, vmcp.ErrNotFound)
		})
	}
}

func TestCallTool_CallBindingAtDispatch(t *testing.T) {
	t.Parallel()

	target := backendTarget()
	tests := []struct {
		name     string
		identity *auth.Identity
		allowed  bool
	}{
		{name: "anonymous", identity: nil, allowed: true},
		{name: "token without an entry", identity: &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
			Claims: map[string]any{"iss": "https://as.example.com", "sub": "user-1"},
		}}, allowed: true},
		{name: "token bound to the target backend", identity: callBoundIdentity(testBackendID, "tool_a", nil),
			allowed: true},
		{name: "token bound to another backend", identity: callBoundIdentity("elsewhere", "tool_a", nil)},
		{name: "malformed authorization_details", identity: callBoundIdentity("", "", "not an array")},
		{name: "list-call token", identity: callBoundIdentity("", "", []any{map[string]any{
			"type": toolcalldetails.Type, "server": testBackendID, "method": "tools/list",
		}})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, m := baseConfig(t)
			expectAggregation(m, &aggregator.AggregatedCapabilities{
				Tools:        []vmcp.Tool{backendTool("tool_a")},
				RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"tool_a": target}},
			})
			calls := 0
			if tc.allowed {
				calls = 1
			}
			m.client.EXPECT().
				CallTool(gomock.Any(), target, "tool_a", gomock.Any(), gomock.Any(), gomock.Any()).
				Return(&vmcp.ToolCallResult{}, nil).Times(calls)

			c, err := New(cfg)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })

			_, err = c.CallTool(context.Background(), tc.identity, "tool_a", nil, nil)
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, vmcp.ErrAuthorizationFailed)
			}
		})
	}
}

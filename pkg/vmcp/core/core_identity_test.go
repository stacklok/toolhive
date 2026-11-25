// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

// identityCase is one of the three acceptance cases from #7143: the identity
// passed only as a parameter, a parameter that conflicts with the identity in
// ctx, and a nil parameter (anonymous) with a conflicting ctx identity.
// wantSubject is the subject the backend call or capability aggregation must
// see in ctx; "" means no identity at all (anonymous).
type identityCase struct {
	name        string
	ctxIdentity *auth.Identity
	identity    *auth.Identity
	wantSubject string
}

// identityParameterCases returns the three acceptance cases from #7143.
func identityParameterCases() []identityCase {
	jane := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-123", Name: "Jane Doe"}}
	mallory := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-666", Name: "Mallory"}}
	return []identityCase{
		{
			name:        "identity passed only as a parameter",
			identity:    jane,
			wantSubject: "user-123",
		},
		{
			name:        "parameter wins over a conflicting context identity",
			ctxIdentity: mallory,
			identity:    jane,
			wantSubject: "user-123",
		},
		{
			name:        "nil identity stays anonymous despite a context identity",
			ctxIdentity: mallory,
			wantSubject: "",
		},
	}
}

// subjectSeenInContext returns the subject of the identity ctx carries, or ""
// when ctx carries no identity.
func subjectSeenInContext(ctx context.Context) string {
	if id, ok := auth.IdentityFromContext(ctx); ok {
		return id.Subject
	}
	return ""
}

// identityTestCaps returns a capability view covering one tool, resource, and
// prompt on the single test backend, so every direct-call method can route.
func identityTestCaps() *aggregator.AggregatedCapabilities {
	target := backendTarget()
	return &aggregator.AggregatedCapabilities{
		Tools:     []vmcp.Tool{backendTool("tool_a")},
		Resources: []vmcp.Resource{{URI: "file://a", BackendID: testBackendID}},
		Prompts:   []vmcp.Prompt{{Name: "p1", BackendID: testBackendID}},
		RoutingTable: &vmcp.RoutingTable{
			Tools:     map[string]*vmcp.BackendTarget{"tool_a": target},
			Resources: map[string]*vmcp.BackendTarget{"file://a": target},
			Prompts:   map[string]*vmcp.BackendTarget{"p1": target},
		},
	}
}

// TestDirectBackendCallsCarryParameterIdentity verifies that a direct (non-composite)
// backend call — CallTool, ReadResource, GetPrompt, Complete — hands the backend
// client a context carrying the explicit identity parameter, never a differing
// identity the caller's ctx carries (#7143). The mock backend client stands in
// for the components below the core that read the caller from ctx
// (auth.IdentityFromContext): the outgoing auth strategies, backend telemetry,
// and the client's fallback identity.
func TestDirectBackendCallsCarryParameterIdentity(t *testing.T) {
	t.Parallel()

	methods := []struct {
		name   string
		expect func(m *coreMocks, capture *string)
		invoke func(c VMCP, ctx context.Context, identity *auth.Identity) error
	}{
		{
			name: "CallTool",
			expect: func(m *coreMocks, capture *string) {
				m.client.EXPECT().
					CallTool(gomock.Any(), gomock.Any(), "tool_a", gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, _ *vmcp.BackendTarget, _ string, _, _ map[string]any, _ map[string]string) (*vmcp.ToolCallResult, error) {
						*capture = subjectSeenInContext(ctx)
						return &vmcp.ToolCallResult{}, nil
					})
			},
			invoke: func(c VMCP, ctx context.Context, identity *auth.Identity) error {
				_, err := c.CallTool(ctx, identity, "tool_a", nil, nil)
				return err
			},
		},
		{
			name: "ReadResource",
			expect: func(m *coreMocks, capture *string) {
				m.client.EXPECT().
					ReadResource(gomock.Any(), gomock.Any(), "file://a").
					DoAndReturn(func(ctx context.Context, _ *vmcp.BackendTarget, _ string) (*vmcp.ResourceReadResult, error) {
						*capture = subjectSeenInContext(ctx)
						return &vmcp.ResourceReadResult{}, nil
					})
			},
			invoke: func(c VMCP, ctx context.Context, identity *auth.Identity) error {
				_, err := c.ReadResource(ctx, identity, "file://a")
				return err
			},
		},
		{
			name: "GetPrompt",
			expect: func(m *coreMocks, capture *string) {
				m.client.EXPECT().
					GetPrompt(gomock.Any(), gomock.Any(), "p1", gomock.Any()).
					DoAndReturn(func(ctx context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (*vmcp.PromptGetResult, error) {
						*capture = subjectSeenInContext(ctx)
						return &vmcp.PromptGetResult{}, nil
					})
			},
			invoke: func(c VMCP, ctx context.Context, identity *auth.Identity) error {
				_, err := c.GetPrompt(ctx, identity, "p1", nil)
				return err
			},
		},
		{
			name: "Complete",
			expect: func(m *coreMocks, capture *string) {
				m.client.EXPECT().
					Complete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, _ *vmcp.BackendTarget, _ vmcp.CompletionRef, _, _ string, _ map[string]string) (*vmcp.CompletionResult, error) {
						*capture = subjectSeenInContext(ctx)
						return &vmcp.CompletionResult{Values: []string{}}, nil
					})
			},
			invoke: func(c VMCP, ctx context.Context, identity *auth.Identity) error {
				ref := vmcp.CompletionRef{Type: vmcp.CompletionRefTypePrompt, Name: "p1"}
				_, err := c.Complete(ctx, identity, ref, "arg", "a", nil)
				return err
			},
		},
	}

	for _, method := range methods {
		for _, tc := range identityParameterCases() {
			t.Run(method.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				cfg, m := baseConfig(t)
				m.reg.EXPECT().List(gomock.Any()).
					Return([]vmcp.Backend{{ID: testBackendID, HealthStatus: vmcp.BackendHealthy}})
				m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
					Return(identityTestCaps(), nil)
				var gotSubject string
				method.expect(m, &gotSubject)

				c, err := New(cfg)
				require.NoError(t, err)
				t.Cleanup(func() { _ = c.Close() })

				ctx := context.Background()
				if tc.ctxIdentity != nil {
					ctx = auth.WithIdentity(ctx, tc.ctxIdentity)
				}
				require.NoError(t, method.invoke(c, ctx, tc.identity))
				assert.Equal(t, tc.wantSubject, gotSubject,
					"backend call must see the parameter identity, not the context identity")
			})
		}
	}
}

// TestCapabilityAggregationCarriesParameterIdentity verifies that capability
// aggregation — the path whose ctx the capability cache keys on
// (aggregator/cacheKey reads auth.IdentityFromContext) — sees the explicit
// identity parameter, never a differing identity the caller's ctx carries
// (#7143). The mock aggregator stands in for the caching aggregator: the
// subject it sees in ctx is the subject the cache key is derived from.
func TestCapabilityAggregationCarriesParameterIdentity(t *testing.T) {
	t.Parallel()

	methods := []struct {
		name   string
		invoke func(c VMCP, ctx context.Context, identity *auth.Identity) error
	}{
		{
			name: "ListTools",
			invoke: func(c VMCP, ctx context.Context, identity *auth.Identity) error {
				_, err := c.ListTools(ctx, identity)
				return err
			},
		},
		{
			name: "Discover",
			invoke: func(c VMCP, ctx context.Context, identity *auth.Identity) error {
				_, err := c.Discover(ctx, identity)
				return err
			},
		},
		{
			name: "ListBackends",
			invoke: func(c VMCP, ctx context.Context, identity *auth.Identity) error {
				_, err := c.ListBackends(ctx, identity, true)
				return err
			},
		},
	}

	for _, method := range methods {
		for _, tc := range identityParameterCases() {
			t.Run(method.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				cfg, m := baseConfig(t)
				m.reg.EXPECT().List(gomock.Any()).
					Return([]vmcp.Backend{{ID: testBackendID, HealthStatus: vmcp.BackendHealthy}})
				var gotSubject string
				m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, _ []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
						gotSubject = subjectSeenInContext(ctx)
						return identityTestCaps(), nil
					})

				c, err := New(cfg)
				require.NoError(t, err)
				t.Cleanup(func() { _ = c.Close() })

				ctx := context.Background()
				if tc.ctxIdentity != nil {
					ctx = auth.WithIdentity(ctx, tc.ctxIdentity)
				}
				require.NoError(t, method.invoke(c, ctx, tc.identity))
				assert.Equal(t, tc.wantSubject, gotSubject,
					"capability aggregation must see the parameter identity, not the context identity")
			})
		}
	}
}

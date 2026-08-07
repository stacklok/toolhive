// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// expectAggregation wires reg.List + agg.AggregateCapabilities to return agg once.
func expectAggregation(m *coreMocks, agg *aggregator.AggregatedCapabilities) {
	m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{{ID: "be1", HealthStatus: vmcp.BackendHealthy}})
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(agg, nil)
}

func TestCallTool_RoutesToBackend(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	target := backendTarget()
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{backendTool("tool_a")},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"tool_a": target}},
	})

	want := &vmcp.ToolCallResult{StructuredContent: map[string]any{"result": "ok"}}
	m.client.EXPECT().
		CallTool(gomock.Any(), gomock.Any(), "tool_a", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(want, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.CallTool(context.Background(), nil, "tool_a", map[string]any{"a": 1}, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, testBackendID, got.BackendID, "CallTool must stamp the routed target's backend onto the result")
}

func TestCallTool_NotFound(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	expectAggregation(m, &aggregator.AggregatedCapabilities{RoutingTable: &vmcp.RoutingTable{}})

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.CallTool(context.Background(), nil, "missing", nil, nil)
	assert.ErrorIs(t, err, vmcp.ErrNotFound)
}

func TestCallTool_CopyBeforeMutate(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	target := backendTarget()
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"tool_a": target}},
	})

	// The backend client mutates the maps it receives; the caller's originals
	// must be untouched because CallTool forwards clones.
	m.client.EXPECT().
		CallTool(gomock.Any(), gomock.Any(), "tool_a", gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, args, meta map[string]any, _ map[string]string) (*vmcp.ToolCallResult, error) {
			args["injected"] = true
			meta["injected"] = true
			return &vmcp.ToolCallResult{}, nil
		})

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	args := map[string]any{"a": 1}
	meta := map[string]any{"m": "n"}
	_, err = c.CallTool(context.Background(), nil, "tool_a", args, meta)
	require.NoError(t, err)

	assert.Equal(t, map[string]any{"a": 1}, args, "caller args must not be mutated")
	assert.Equal(t, map[string]any{"m": "n"}, meta, "caller meta must not be mutated")
}

func TestCallTool_CompositeWorkflow(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	cfg.WorkflowDefs = map[string]*composer.WorkflowDefinition{
		"wf": {Name: "wf", Steps: []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "be1.echo"}}},
	}

	target := backendTarget()
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{backendTool("be1.echo")},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"be1.echo": target}},
	})

	// The composite workflow's single tool step routes to the backend through the
	// per-call composer built from the aggregated routing table.
	m.client.EXPECT().
		CallTool(gomock.Any(), gomock.Any(), "be1.echo", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&vmcp.ToolCallResult{StructuredContent: map[string]any{"ok": true}}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.CallTool(context.Background(), nil, "wf", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.False(t, got.IsError)
	assert.Equal(t, true, got.StructuredContent["ok"])
	assert.Empty(t, got.BackendID, "a composite tool has no single serving backend")
}

func TestCallTool_CompositeNotAccessible(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	cfg.WorkflowDefs = map[string]*composer.WorkflowDefinition{
		"wf": {Name: "wf", Steps: []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "be1.echo"}}},
	}

	// Routing table does not contain be1.echo, so the composite is not reachable.
	expectAggregation(m, &aggregator.AggregatedCapabilities{RoutingTable: &vmcp.RoutingTable{}})

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.CallTool(context.Background(), nil, "wf", nil, nil)
	assert.ErrorIs(t, err, vmcp.ErrNotFound)
}

// TestStepAnnotationResolver_DottedNameResolution (Q5) verifies that a
// composite-tool step reference using the "{workloadID}.{toolName}" dot
// convention resolves to the correct backend annotations through the routing
// table's resolved (prefixed) key. With prefix conflict resolution the routing
// table stores "be1_echo" while the step still references "be1.echo"; the
// resolver must match via WorkloadID + original capability name and return the
// annotations registered under the resolved name.
func TestStepAnnotationResolver_DottedNameResolution(t *testing.T) {
	t.Parallel()

	trueVal, falseVal := true, false
	backendAnn := &vmcp.ToolAnnotations{
		ReadOnlyHint:    &trueVal,
		DestructiveHint: &falseVal,
		OpenWorldHint:   &falseVal,
	}

	agg := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{
			{Name: "be1_echo", BackendID: testBackendID, Annotations: backendAnn},
		},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{
			"be1_echo": {
				WorkloadID:             testBackendID,
				BaseURL:                "http://" + testBackendID + ":8080",
				OriginalCapabilityName: "echo",
			},
		}},
	}

	resolver := stepAnnotationResolver(agg)
	require.NotNil(t, resolver)

	// Dotted reference resolves to the prefixed routing-table key's annotations.
	got := resolver("be1.echo")
	require.NotNil(t, got, "dotted step ref must resolve through the routing table")
	assert.Same(t, backendAnn, got, "resolved annotations must come from the resolved (prefixed) tool name")

	// The resolved key itself also resolves (exact-match fast path).
	assert.Same(t, backendAnn, resolver("be1_echo"))

	// An unknown step tool resolves to nil (treated conservatively upstream).
	assert.Nil(t, resolver("be1.unknown"))
}

func TestReadResource(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	target := backendTarget()
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		RoutingTable: &vmcp.RoutingTable{Resources: map[string]*vmcp.BackendTarget{"file://a": target}},
	})

	want := &vmcp.ResourceReadResult{Contents: []vmcp.ResourceContent{{URI: "file://a", Text: "hi"}}}
	m.client.EXPECT().ReadResource(gomock.Any(), gomock.Any(), "file://a").Return(want, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.ReadResource(context.Background(), nil, "file://a")
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, testBackendID, got.BackendID, "ReadResource must stamp the routed target's backend onto the result")
}

func TestReadResource_NotFound(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	expectAggregation(m, &aggregator.AggregatedCapabilities{RoutingTable: &vmcp.RoutingTable{}})

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.ReadResource(context.Background(), nil, "file://missing")
	assert.ErrorIs(t, err, vmcp.ErrNotFound)
}

func TestGetPrompt(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	target := backendTarget()
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		RoutingTable: &vmcp.RoutingTable{Prompts: map[string]*vmcp.BackendTarget{"p1": target}},
	})

	want := &vmcp.PromptGetResult{Description: "a prompt"}
	m.client.EXPECT().GetPrompt(gomock.Any(), gomock.Any(), "p1", gomock.Any()).Return(want, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.GetPrompt(context.Background(), nil, "p1", map[string]any{"x": 1})
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, testBackendID, got.BackendID, "GetPrompt must stamp the routed target's backend onto the result")
}

func TestGetPrompt_CopyBeforeMutate(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	target := backendTarget()
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		RoutingTable: &vmcp.RoutingTable{Prompts: map[string]*vmcp.BackendTarget{"p1": target}},
	})

	// The backend client mutates the args it receives; the caller's original must be
	// untouched because GetPrompt forwards a clone (parity with CallTool).
	m.client.EXPECT().
		GetPrompt(gomock.Any(), gomock.Any(), "p1", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, args map[string]any) (*vmcp.PromptGetResult, error) {
			args["injected"] = true
			return &vmcp.PromptGetResult{}, nil
		})

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	args := map[string]any{"x": 1}
	_, err = c.GetPrompt(context.Background(), nil, "p1", args)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"x": 1}, args, "caller args must not be mutated")
}

func TestGetPrompt_NotFound(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	expectAggregation(m, &aggregator.AggregatedCapabilities{RoutingTable: &vmcp.RoutingTable{}})

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.GetPrompt(context.Background(), nil, "missing", nil)
	assert.ErrorIs(t, err, vmcp.ErrNotFound)
}

// TestCallTool_ResolvesRenamedTool exercises the conflict-resolution name path:
// the advertised name uses the dot convention ("be1.echo") while the routing
// table is keyed by the prefixed resolved name ("be1_echo") whose target carries
// a non-empty OriginalCapabilityName. The session router resolves it via
// GetBackendCapabilityName, and the core forwards the advertised name to the
// client (the client owns the translation to the backend's capability name).
func TestCallTool_ResolvesRenamedTool(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	target := &vmcp.BackendTarget{WorkloadID: "be1", OriginalCapabilityName: "echo", BaseURL: "http://be1:8080"}
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{{Name: "be1_echo", BackendID: "be1"}},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"be1_echo": target}},
	})

	m.client.EXPECT().
		CallTool(gomock.Any(), target, "be1.echo", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&vmcp.ToolCallResult{}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.CallTool(context.Background(), nil, "be1.echo", nil, nil)
	require.NoError(t, err)
}

// TestCompositeNameConflict_AdvertisedEqualsExecuted is the F1 parity guard: when a
// composite tool name collides with a backend tool name, ListTools advertises the
// backend tool (composites dropped) and CallTool must ALSO route that name to the
// backend — never execute the withheld composite. The single accessibleComposites
// gate keeps advertised == executed (matching the legacy decorator).
func TestCompositeNameConflict_AdvertisedEqualsExecuted(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	// Composite "shared" is accessible (its step routes), but its name also collides
	// with an advertised backend tool "shared".
	cfg.WorkflowDefs = map[string]*composer.WorkflowDefinition{
		"shared": {Name: "shared", Steps: []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "be1.echo"}}},
	}

	beTarget := &vmcp.BackendTarget{WorkloadID: "be1", BaseURL: "http://be1:8080"}
	agg := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{{Name: "shared", BackendID: "be1"}},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{
			"shared":   beTarget,
			"be1.echo": beTarget,
		}},
	}
	// Two aggregations: one for ListTools, one for CallTool.
	m.reg.EXPECT().List(gomock.Any()).Return(nil).Times(2)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(agg, nil).Times(2)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	// ListTools advertises only the backend tool; the conflicting composite is dropped.
	tools, err := c.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "shared", tools[0].Name)
	assert.Equal(t, "be1", tools[0].BackendID)

	// CallTool("shared") must route to the backend, not execute the composite.
	want := &vmcp.ToolCallResult{StructuredContent: map[string]any{"from": "backend"}}
	m.client.EXPECT().CallTool(gomock.Any(), beTarget, "shared", gomock.Any(), gomock.Any(), gomock.Any()).Return(want, nil)

	got, err := c.CallTool(context.Background(), nil, "shared", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got, "conflicting name must resolve to the backend tool, not the composite")
}

// TestCompositeAnnotationContradiction_AdvertisedEqualsExecuted is the parity
// guard for annotation drop: a composite whose explicit annotations contradict
// the derived floor is omitted from ListTools and must also be uncallable via
// CallTool (ErrNotFound). No backend CallTool expectation is set — if the
// composite incorrectly remains executable, CallTool would hit the composer and
// invoke the backend, failing the mock.
func TestCompositeAnnotationContradiction_AdvertisedEqualsExecuted(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	cfg.WorkflowDefs = map[string]*composer.WorkflowDefinition{
		"wf": {
			Name: "wf",
			// Optimistic readOnlyHint against a silent backend (nil annotations →
			// fail-closed floor with readOnly=false) is a contradiction → drop.
			Annotations: &config.ToolAnnotationsOverride{ReadOnlyHint: boolPtr(true)},
			Steps:       []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "be1.echo"}},
		},
	}

	beTarget := backendTarget()
	// Backend tool has nil Annotations → silent backend → conservative floor.
	agg := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{{Name: "be1.echo", BackendID: testBackendID}},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{
			"be1.echo": beTarget,
		}},
	}
	// Two aggregations: one for ListTools, one for CallTool.
	m.reg.EXPECT().List(gomock.Any()).Return(nil).Times(2)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(agg, nil).Times(2)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	tools, err := c.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "be1.echo", tools[0].Name)
	assert.Equal(t, testBackendID, tools[0].BackendID)

	// No m.client.EXPECT().CallTool — a regression that executes the workflow
	// would call the backend and fail the mock controller.
	_, err = c.CallTool(context.Background(), nil, "wf", nil, nil)
	assert.ErrorIs(t, err, vmcp.ErrNotFound,
		"contradicting composite must be uncallable; advertised equals executed")
}

// TestCompositeNameConflict_WithOptimisticAnnotations_RoutesToBackend is the
// bug-2 regression: a composite named like a backend tool and carrying
// optimistic annotations (readOnlyHint:true) must still be detected as a name
// conflict. Pre-fix, ConvertWorkflowDefsToTools + noop resolver dropped the
// composite from conversion (annotation contradiction), so ValidateNoToolConflicts
// never saw the name and CallTool executed the workflow. Post-fix,
// CompositeToolNames feeds the conflict check → ALL composites dropped →
// CallTool routes to the backend.
func TestCompositeNameConflict_WithOptimisticAnnotations_RoutesToBackend(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	cfg.WorkflowDefs = map[string]*composer.WorkflowDefinition{
		"be1.echo": {
			Name:        "be1.echo",
			Annotations: &config.ToolAnnotationsOverride{ReadOnlyHint: boolPtr(true)},
			Steps:       []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "be1.echo"}},
		},
	}

	beTarget := backendTarget()
	agg := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{{Name: "be1.echo", BackendID: testBackendID}},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{
			"be1.echo": beTarget,
		}},
	}
	// Two aggregations: one for ListTools, one for CallTool.
	m.reg.EXPECT().List(gomock.Any()).Return(nil).Times(2)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(agg, nil).Times(2)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	tools, err := c.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "be1.echo", tools[0].Name)
	assert.Equal(t, testBackendID, tools[0].BackendID)

	want := &vmcp.ToolCallResult{StructuredContent: map[string]any{"from": "backend"}}
	m.client.EXPECT().CallTool(gomock.Any(), beTarget, "be1.echo", gomock.Any(), gomock.Any(), gomock.Any()).Return(want, nil)

	got, err := c.CallTool(context.Background(), nil, "be1.echo", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got, "conflicting composite with optimistic annotations must route to the backend")
}

// TestComplete_RoutesPromptRef verifies a ref/prompt completion resolves the
// backend through the prompts routing table and forwards to the backend client.
func TestComplete_RoutesPromptRef(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	target := backendTarget()
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		RoutingTable: &vmcp.RoutingTable{Prompts: map[string]*vmcp.BackendTarget{"p1": target}},
	})

	want := &vmcp.CompletionResult{Values: []string{"alpha", "beta"}, Total: 2}
	ref := vmcp.CompletionRef{Type: vmcp.CompletionRefTypePrompt, Name: "p1"}
	m.client.EXPECT().
		Complete(gomock.Any(), target, ref, "arg", "a", gomock.Any()).
		Return(want, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.Complete(t.Context(), nil, ref, "arg", "a", nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestComplete_RoutesResourceTemplateRef verifies a ref/resource completion resolves
// the backend through the resource-templates routing table and forwards to the
// backend client. Per the MCP spec a ref/resource carries the URI TEMPLATE STRING
// itself (ResourceTemplateReference's uri is "@format uri-template"), which a
// template does not match by expansion — the router must route the exact template
// string to its backend. An expanded URI still routes via template matching.
func TestComplete_RoutesResourceTemplateRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		uri  string
	}{
		{
			name: "template string (spec form)",
			uri:  "file:///logs/{date}.txt",
		},
		{
			name: "expanded URI (template expansion match)",
			uri:  "file:///logs/2025-01-01.txt",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, m := baseConfig(t)

			target := backendTarget()
			expectAggregation(m, &aggregator.AggregatedCapabilities{
				RoutingTable: &vmcp.RoutingTable{
					ResourceTemplates: map[string]*vmcp.BackendTarget{"file:///logs/{date}.txt": target},
				},
			})

			want := &vmcp.CompletionResult{Values: []string{"2025-01-01.txt"}}
			ref := vmcp.CompletionRef{Type: vmcp.CompletionRefTypeResource, URI: tc.uri}
			m.client.EXPECT().
				Complete(gomock.Any(), target, ref, "date", "2025", gomock.Any()).
				Return(want, nil)

			c, err := New(cfg)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })

			got, err := c.Complete(t.Context(), nil, ref, "date", "2025", nil)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// TestComplete_UnroutableRefReturnsEmpty covers the lenient-completion contract:
// an unroutable prompt ref, an unroutable resource ref, and an unknown ref type all
// yield a non-nil empty result rather than an error, and never reach the backend.
func TestComplete_UnroutableRefReturnsEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  vmcp.CompletionRef
	}{
		{
			name: "unknown prompt",
			ref:  vmcp.CompletionRef{Type: vmcp.CompletionRefTypePrompt, Name: "missing"},
		},
		{
			name: "unmatched resource",
			ref:  vmcp.CompletionRef{Type: vmcp.CompletionRefTypeResource, URI: "file:///nope"},
		},
		{
			name: "unknown ref type",
			ref:  vmcp.CompletionRef{Type: "ref/bogus", Name: "x"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, m := baseConfig(t)
			// Empty routing table: nothing routes. No client.Complete expectation, so
			// a forwarded call would fail the test.
			expectAggregation(m, &aggregator.AggregatedCapabilities{RoutingTable: &vmcp.RoutingTable{}})

			c, err := New(cfg)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })

			got, err := c.Complete(t.Context(), nil, tc.ref, "arg", "", nil)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Empty(t, got.Values)
		})
	}
}

// TestComplete_AdmissionDenied verifies a completion ref whose referenced capability
// is denied by admission returns ErrAuthorizationFailed without reaching the backend,
// for both a prompt ref (get-side decision) and a resource ref (read-side decision).
func TestComplete_AdmissionDenied(t *testing.T) {
	t.Parallel()

	t.Run("prompt ref denied", func(t *testing.T) {
		t.Parallel()
		cfg, m := baseConfig(t)
		cfg.ServerName = "test-vmcp"
		cfg.Authz = cedarAuthzConfig(t,
			`permit(principal, action == Action::"get_prompt", resource == Prompt::"allowed");`)

		m.reg.EXPECT().List(gomock.Any()).
			Return([]vmcp.Backend{{ID: testBackendID, HealthStatus: vmcp.BackendHealthy}}).AnyTimes()
		m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
			RoutingTable: &vmcp.RoutingTable{Prompts: map[string]*vmcp.BackendTarget{"denied": backendTarget()}},
		}, nil).AnyTimes()

		c, err := New(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })

		ref := vmcp.CompletionRef{Type: vmcp.CompletionRefTypePrompt, Name: "denied"}
		_, err = c.Complete(t.Context(), cedarIdentity(), ref, "arg", "", nil)
		assert.ErrorIs(t, err, vmcp.ErrAuthorizationFailed)
	})

	t.Run("resource ref denied", func(t *testing.T) {
		t.Parallel()
		cfg, m := baseConfig(t)
		cfg.ServerName = "test-vmcp"
		cfg.Authz = cedarAuthzConfig(t,
			`permit(principal, action == Action::"read_resource", resource == Resource::"file:///ok");`)

		m.reg.EXPECT().List(gomock.Any()).
			Return([]vmcp.Backend{{ID: testBackendID, HealthStatus: vmcp.BackendHealthy}}).AnyTimes()
		m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
			RoutingTable: &vmcp.RoutingTable{
				ResourceTemplates: map[string]*vmcp.BackendTarget{"file:///{name}": backendTarget()},
			},
		}, nil).AnyTimes()

		c, err := New(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })

		ref := vmcp.CompletionRef{Type: vmcp.CompletionRefTypeResource, URI: "file:///secret"}
		_, err = c.Complete(t.Context(), cedarIdentity(), ref, "name", "", nil)
		assert.ErrorIs(t, err, vmcp.ErrAuthorizationFailed)
	})
}

// stubComposer is a configurable composer.Composer for unit-testing
// executeComposite's result/error conversion without the real workflow engine.
type stubComposer struct {
	result *composer.WorkflowResult
	err    error
}

func (s stubComposer) ExecuteWorkflow(
	_ context.Context, _ *composer.WorkflowDefinition, _ map[string]any,
) (*composer.WorkflowResult, error) {
	return s.result, s.err
}
func (stubComposer) ValidateWorkflow(_ context.Context, _ *composer.WorkflowDefinition) error {
	return nil
}
func (stubComposer) GetWorkflowStatus(_ context.Context, _ string) (*composer.WorkflowStatus, error) {
	return nil, nil
}
func (stubComposer) CancelWorkflow(_ context.Context, _ string) error { return nil }

func TestExecuteComposite(t *testing.T) {
	t.Parallel()

	def := &composer.WorkflowDefinition{Name: "wf"}

	tests := []struct {
		name        string
		composer    stubComposer
		wantIsError bool
		wantMsg     string // substring expected in the error content
		wantOutput  map[string]any
	}{
		{
			name:        "success",
			composer:    stubComposer{result: &composer.WorkflowResult{Output: map[string]any{"k": "v"}}},
			wantIsError: false,
			wantOutput:  map[string]any{"k": "v"},
		},
		{
			name:        "execution error",
			composer:    stubComposer{err: errors.New("boom")},
			wantIsError: true,
			wantMsg:     "Workflow execution failed",
		},
		{
			name:        "timeout",
			composer:    stubComposer{err: context.DeadlineExceeded},
			wantIsError: true,
			wantMsg:     "timeout",
		},
		{
			name:        "nil result",
			composer:    stubComposer{},
			wantIsError: true,
			wantMsg:     "nil result",
		},
		{
			name:        "result carries error",
			composer:    stubComposer{result: &composer.WorkflowResult{Error: errors.New("step failed")}},
			wantIsError: true,
			wantMsg:     "Workflow error",
		},
		{
			name: "marshal failure",
			// A channel is not JSON-serializable, forcing json.Marshal(Output) to fail.
			composer:    stubComposer{result: &composer.WorkflowResult{Output: map[string]any{"ch": make(chan int)}}},
			wantIsError: true,
			wantMsg:     "failed to marshal output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := executeComposite(context.Background(), tt.composer, def, nil)
			require.NoError(t, err) // workflow failures are returned as IsError results, not errors
			require.NotNil(t, got)
			assert.Equal(t, tt.wantIsError, got.IsError)
			if tt.wantMsg != "" {
				require.NotEmpty(t, got.Content)
				assert.Contains(t, got.Content[0].Text, tt.wantMsg)
			}
			if tt.wantOutput != nil {
				assert.Equal(t, tt.wantOutput, got.StructuredContent)
			}
		})
	}
}

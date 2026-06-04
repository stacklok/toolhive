// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
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
		CallTool(gomock.Any(), gomock.Any(), "tool_a", gomock.Any(), gomock.Any()).
		Return(want, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.CallTool(context.Background(), nil, "tool_a", map[string]any{"a": 1}, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)
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
		CallTool(gomock.Any(), gomock.Any(), "tool_a", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, args, meta map[string]any) (*vmcp.ToolCallResult, error) {
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
		CallTool(gomock.Any(), gomock.Any(), "be1.echo", gomock.Any(), gomock.Any()).
		Return(&vmcp.ToolCallResult{StructuredContent: map[string]any{"ok": true}}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.CallTool(context.Background(), nil, "wf", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.False(t, got.IsError)
	assert.Equal(t, true, got.StructuredContent["ok"])
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

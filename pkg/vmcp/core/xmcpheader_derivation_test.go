// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
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
)

// annotatedTool is backendTool with an x-mcp-header annotation on one parameter,
// the SEP-2243 worked example (execute_sql / Region).
func annotatedTool(name, header string) vmcp.Tool {
	t := backendTool(name)
	t.InputSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"region": map[string]any{"type": "string", "x-mcp-header": header},
			"query":  map[string]any{"type": "string"},
		},
	}
	return t
}

// TestCallTool_DerivesParamHeadersFromSchema is the pin that makes the whole
// feature non-vacuous: the core is the only layer holding both the tool's
// inputSchema and the call's arguments, so if it fails to derive the Mcp-Param-*
// headers nothing downstream can. Asserted on the exact map handed to the backend
// client rather than on a wire capture, because that hand-off is the seam this
// change introduces.
func TestCallTool_DerivesParamHeadersFromSchema(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	target := backendTarget()
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{annotatedTool("execute_sql", "Region")},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"execute_sql": target}},
	})

	var gotHeaders map[string]string
	m.client.EXPECT().
		CallTool(gomock.Any(), gomock.Any(), "execute_sql", gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any, _ map[string]any,
			paramHeaders map[string]string,
		) (*vmcp.ToolCallResult, error) {
			gotHeaders = paramHeaders
			return &vmcp.ToolCallResult{}, nil
		})

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.CallTool(context.Background(), nil, "execute_sql",
		map[string]any{"region": "eu-west1", "query": "select 1"}, nil)
	require.NoError(t, err)

	// Only the designated parameter is mirrored; "query" carries no annotation.
	assert.Equal(t, map[string]string{"Mcp-Param-Region": "eu-west1"}, gotHeaders)
}

// TestCallTool_NoAnnotationsDerivesNoHeaders covers the common case: a tool with
// no x-mcp-header annotation must hand the backend client nil, not an empty map,
// so nothing is added to the wire.
func TestCallTool_NoAnnotationsDerivesNoHeaders(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	target := backendTarget()
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{backendTool("tool_a")},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"tool_a": target}},
	})

	var gotHeaders map[string]string
	called := false
	m.client.EXPECT().
		CallTool(gomock.Any(), gomock.Any(), "tool_a", gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any, _ map[string]any,
			paramHeaders map[string]string,
		) (*vmcp.ToolCallResult, error) {
			gotHeaders = paramHeaders
			called = true
			return &vmcp.ToolCallResult{}, nil
		})

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.CallTool(context.Background(), nil, "tool_a", map[string]any{"a": 1}, nil)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Nil(t, gotHeaders)
}

// TestCallTool_UnmirrorableArgumentFailsBeforeDispatch pins the fail-closed
// direction. A caller-supplied value carrying a CRLF must abort the call rather
// than be dropped: dropping it would send no header, the backend would answer
// -32020, and a caller mistake would surface as an opaque backend failure. The
// backend client must therefore never be reached — asserted by setting no EXPECT
// on it, so any call fails the gomock controller.
func TestCallTool_UnmirrorableArgumentFailsBeforeDispatch(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	target := backendTarget()
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{annotatedTool("execute_sql", "Region")},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"execute_sql": target}},
	})

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.CallTool(context.Background(), nil, "execute_sql",
		map[string]any{"region": "eu\r\nX-Evil: 1"}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, vmcp.ErrInvalidInput),
		"a bad argument value must be reported as invalid input, got %v", err)
}

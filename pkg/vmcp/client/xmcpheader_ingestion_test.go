// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// toolWithSchema builds a backend tool whose inputSchema has a single property.
func toolWithSchema(name string, prop map[string]any) mcp.Tool {
	return mcp.Tool{
		Name: name,
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{"region": prop},
		},
	}
}

// toolNames extracts the ingested tool names, the observable outcome of the
// SEP-2243 rejection.
func toolNames(t *testing.T, tools []mcp.Tool) []string {
	t.Helper()
	caps := newCapabilityListFromMCP("backend-1", tools, nil, nil, nil)
	require.NotNil(t, caps)
	names := make([]string, 0, len(caps.Tools))
	for _, tool := range caps.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// TestNewCapabilityListFromMCP_RejectsInvalidXMCPHeader pins the SEP-2243
// requirement that a Streamable HTTP client reject a tool definition whose
// x-mcp-header annotations violate the extension's constraints. vMCP is the
// backend's client, so the rejection happens at ingestion.
//
// Crucially it must reject only the OFFENDING tool: the aggregated tools/list
// spans every backend, so failing the whole list would take unrelated tools down
// with it.
func TestNewCapabilityListFromMCP_RejectsInvalidXMCPHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		prop     map[string]any
		wantKept bool
	}{
		{
			name:     "unannotated tool is kept",
			prop:     map[string]any{"type": "string"},
			wantKept: true,
		},
		{
			name:     "valid annotation is kept",
			prop:     map[string]any{"type": "string", "x-mcp-header": "Region"},
			wantKept: true,
		},
		{
			// SEP-2243 excludes number: a float has no canonical wire spelling.
			name:     "number-typed annotation is rejected",
			prop:     map[string]any{"type": "number", "x-mcp-header": "Region"},
			wantKept: false,
		},
		{
			// The header-injection vector: a CRLF in the annotation would let a
			// backend forge additional headers on vMCP's outgoing request.
			name:     "CRLF in the annotation is rejected",
			prop:     map[string]any{"type": "string", "x-mcp-header": "R\r\nX: y"},
			wantKept: false,
		},
		{
			name:     "empty annotation is rejected",
			prop:     map[string]any{"type": "string", "x-mcp-header": ""},
			wantKept: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := toolNames(t, []mcp.Tool{toolWithSchema("subject", tt.prop)})
			if tt.wantKept {
				assert.Equal(t, []string{"subject"}, got)
				return
			}
			assert.Empty(t, got, "a tool with an invalid x-mcp-header must be rejected")
		})
	}
}

// TestNewCapabilityListFromMCP_RejectionIsScopedToTheOffendingTool is the
// non-vacuous half of the pin above: rejecting the whole backend's tool list (or
// panicking, or preserving a zero-valued gap in the slice) would all satisfy
// "the bad tool is absent". This asserts the good tools survive alongside it.
func TestNewCapabilityListFromMCP_RejectionIsScopedToTheOffendingTool(t *testing.T) {
	t.Parallel()

	got := toolNames(t, []mcp.Tool{
		toolWithSchema("before", map[string]any{"type": "string"}),
		toolWithSchema("offender", map[string]any{"type": "number", "x-mcp-header": "Region"}),
		toolWithSchema("after", map[string]any{"type": "string", "x-mcp-header": "Zone"}),
	})

	// Order is preserved and only the offender is missing — no zero-valued gap
	// left behind by the index-assignment the loop used to do.
	assert.Equal(t, []string{"before", "after"}, got)
}

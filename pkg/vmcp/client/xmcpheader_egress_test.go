// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModernCallTool_MirrorsParamHeaders is the wire-level pin for SEP-2243
// mirroring: the Mcp-Param-* headers the core derived must actually reach the
// backend on a Modern tools/call. A backend that designated a parameter and does
// not receive its header answers -32020, so dropping them anywhere along
// CallTool -> modernCallTool -> modernCall makes that backend uncallable.
func TestModernCallTool_MirrorsParamHeaders(t *testing.T) {
	t.Parallel()

	srv, hdr, _ := bodyRecordingServer(t, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
	})
	h, target := modernClient(t, srv.URL)

	_, err := h.CallTool(context.Background(), target, "execute_sql",
		map[string]any{"region": "eu-west1", "query": "select 1"},
		nil,
		map[string]string{"Mcp-Param-Region": "eu-west1"},
	)
	require.NoError(t, err)

	assert.Equal(t, "eu-west1", hdr.Get("Mcp-Param-Region"))
	// The protocol headers must be unaffected by the mirrored ones.
	assert.Equal(t, "tools/call", hdr.Get("Mcp-Method"))
	assert.Equal(t, "2026-07-28", hdr.Get("MCP-Protocol-Version"))
}

// TestModernCallTool_ParamHeadersCannotOverrideProtocolHeaders is the reason
// mirrored headers are set BEFORE the protocol headers rather than after. A
// backend controls the x-mcp-header annotation names, so it could designate a
// parameter named "Method" (yielding Mcp-Param-Method, harmless) — but a caller
// or a future derivation bug producing a bare "Mcp-Method" key must not be able
// to redirect the request to another method, which would slip past the
// authz/audit decision already made for the real one.
func TestModernCallTool_ParamHeadersCannotOverrideProtocolHeaders(t *testing.T) {
	t.Parallel()

	srv, hdr, _ := bodyRecordingServer(t, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
	})
	h, target := modernClient(t, srv.URL)

	_, err := h.CallTool(context.Background(), target, "echo", map[string]any{"input": "hi"}, nil,
		map[string]string{
			"Mcp-Method":           "resources/read",
			"Mcp-Name":             "spoofed",
			"MCP-Protocol-Version": "1999-01-01",
			"Mcp-Param-Region":     "eu-west1",
		},
	)
	require.NoError(t, err)

	assert.Equal(t, "tools/call", hdr.Get("Mcp-Method"), "protocol header must win")
	assert.Equal(t, "echo", hdr.Get("Mcp-Name"), "protocol header must win")
	assert.Equal(t, "2026-07-28", hdr.Get("MCP-Protocol-Version"), "protocol header must win")
	assert.Equal(t, "eu-west1", hdr.Get("Mcp-Param-Region"), "the genuine mirrored header still lands")
}

// TestModernCallTool_NoParamHeadersSendsNone confirms the common case adds
// nothing to the wire: almost no tool carries an x-mcp-header annotation, so nil
// must stay nil rather than becoming an empty header.
func TestModernCallTool_NoParamHeadersSendsNone(t *testing.T) {
	t.Parallel()

	srv, hdr, _ := bodyRecordingServer(t, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
	})
	h, target := modernClient(t, srv.URL)

	_, err := h.CallTool(context.Background(), target, "echo", map[string]any{"input": "hi"}, nil, nil)
	require.NoError(t, err)

	for name := range *hdr {
		assert.NotContains(t, name, "Mcp-Param", "no Mcp-Param-* header may be sent when none were derived")
	}
}

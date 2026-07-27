// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversion

import (
	"maps"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// FromMCPMeta converts MCP SDK meta to map[string]any for vmcp wrapper types.
// This preserves the _meta field from backend MCP server responses.
//
// Returns nil if meta is nil or empty, following the MCP specification that
// _meta is optional and should be omitted when empty.
func FromMCPMeta(meta *mcp.Meta) map[string]any {
	if meta == nil {
		return nil
	}

	result := make(map[string]any)

	// Merge additional fields first (custom metadata like trace context)
	maps.Copy(result, meta.AdditionalFields)

	// Set progressToken last to ensure it takes precedence over any
	// progressToken key in AdditionalFields (prevents malicious/incorrect overrides)
	if meta.ProgressToken != nil {
		result["progressToken"] = meta.ProgressToken
	}

	// Return nil if the map is empty (no metadata to preserve)
	if len(result) == 0 {
		return nil
	}

	return result
}

// ToMCPMeta converts vmcp meta map to MCP SDK meta for forwarding to clients.
// This reconstructs the _meta field when sending responses back through the MCP protocol.
//
// Reserved io.modelcontextprotocol/* keys are stripped first: vMCP, not the
// backend, is the client's MCP peer, so a backend must not speak for it. This is
// the single chokepoint for every Legacy egress that carries backend _meta
// (serve_handlers, sessionmanager, the elicitation adapter); the Modern path's
// mirror is newModernResultMeta, and both call the same mcpparser helper so the
// two revisions cannot drift.
//
// Note the Legacy/Modern asymmetry: Modern re-stamps its own
// io.modelcontextprotocol/serverInfo after stripping, and Legacy deliberately
// does not — the 2025-11-25 revision has no serverInfo _meta key at all. The
// missing Legacy stamp is correct, not an oversight.
//
// Returns nil if meta is nil or empty, following the MCP specification that
// _meta is optional and should be omitted when empty. A map consisting only of
// reserved keys therefore collapses to nil rather than an empty _meta object.
func ToMCPMeta(meta map[string]any) *mcp.Meta {
	meta = mcpparser.StripReservedMeta(meta)
	if len(meta) == 0 {
		return nil
	}

	result := &mcp.Meta{
		AdditionalFields: make(map[string]any),
	}

	for k, v := range meta {
		if k == "progressToken" {
			result.ProgressToken = v
		} else {
			result.AdditionalFields[k] = v
		}
	}

	return result
}

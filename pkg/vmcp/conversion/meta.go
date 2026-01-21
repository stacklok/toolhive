// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversion

import (
	"maps"

	"github.com/mark3labs/mcp-go/mcp"
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

	// Add progressToken if present
	if meta.ProgressToken != nil {
		result["progressToken"] = meta.ProgressToken
	}

	// Merge additional fields (custom metadata like trace context)
	maps.Copy(result, meta.AdditionalFields)

	// Return nil if the map is empty (no metadata to preserve)
	if len(result) == 0 {
		return nil
	}

	return result
}

// ToMCPMeta converts vmcp meta map to MCP SDK meta for forwarding to clients.
// This reconstructs the _meta field when sending responses back through the MCP protocol.
//
// Returns nil if meta is nil or empty, following the MCP specification that
// _meta is optional and should be omitted when empty.
func ToMCPMeta(meta map[string]any) *mcp.Meta {
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

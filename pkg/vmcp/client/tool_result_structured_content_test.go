// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestToolResultFromMCP_StructuredContent pins that structuredContent is passed
// through only when the backend sent an object, and is never synthesized from
// the content array. The result reaches clients on pass-through calls, so a
// synthesized map would copy blob and image payloads into structuredContent and
// turn a text block into {"text": ...}, a shape the backend never produced.
func TestToolResultFromMCP_StructuredContent(t *testing.T) {
	t.Parallel()

	const blob = "UEsDBBQAAAAIAA==" // stand-in for a base64 file payload

	tests := []struct {
		name           string
		content        []mcp.Content
		structured     any
		wantStructured map[string]any
		wantContent    []vmcp.Content
	}{
		{
			name: "blob resource without structuredContent",
			content: []mcp.Content{
				mcp.NewTextContent(`{"name":"f.xlsx","size":11}`),
				mcp.NewEmbeddedResource(mcp.BlobResourceContents{
					URI: "file:///f.xlsx", MIMEType: "application/octet-stream", Blob: blob,
				}),
			},
			wantContent: []vmcp.Content{
				{Type: vmcp.ContentTypeText, Text: `{"name":"f.xlsx","size":11}`},
				{Type: vmcp.ContentTypeResource, URI: "file:///f.xlsx", MimeType: "application/octet-stream", Data: blob},
			},
		},
		{
			name:        "image without structuredContent",
			content:     []mcp.Content{mcp.NewImageContent(blob, "image/png")},
			wantContent: []vmcp.Content{{Type: vmcp.ContentTypeImage, Data: blob, MimeType: "image/png"}},
		},
		{
			name:        "single text block without structuredContent",
			content:     []mcp.Content{mcp.NewTextContent(`{"vulns":[]}`)},
			wantContent: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: `{"vulns":[]}`}},
		},
		{
			name:           "backend structuredContent object passes through unchanged",
			content:        []mcp.Content{mcp.NewTextContent(`{"vulns":[]}`)},
			structured:     map[string]any{"vulns": []any{}},
			wantStructured: map[string]any{"vulns": []any{}},
			wantContent:    []vmcp.Content{{Type: vmcp.ContentTypeText, Text: `{"vulns":[]}`}},
		},
		{
			name:        "non-object structuredContent is dropped",
			content:     []mcp.Content{mcp.NewTextContent("[1,2]")},
			structured:  []any{1, 2},
			wantContent: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "[1,2]"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := toolResultFromMCP(slog.Default(), &mcp.CallToolResult{
				Content:           tc.content,
				StructuredContent: tc.structured,
			}, "tool", "backend")

			require.NotNil(t, result)
			assert.Equal(t, tc.wantStructured, result.StructuredContent)
			assert.Equal(t, tc.wantContent, result.Content)
		})
	}
}

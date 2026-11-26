// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestHTTPSession_CallTool_StructuredContent pins that the session-backed
// connector keeps structuredContent only when the backend sent an object and
// never synthesizes it from the content array (which would copy blob and image
// payloads into it and reach clients on pass-through calls).
func TestHTTPSession_CallTool_StructuredContent(t *testing.T) {
	t.Parallel()

	const blob = "UEsDBBQAAAAIAA==" // stand-in for a base64 file payload

	tests := []struct {
		name           string
		toolCallResult map[string]any
		wantStructured map[string]any
		wantContent    []vmcp.Content
	}{
		{
			name: "blob resource without structuredContent",
			toolCallResult: map[string]any{"content": []any{
				map[string]any{"type": "resource", "resource": map[string]any{
					"uri": "file:///f.bin", "mimeType": "application/octet-stream", "blob": blob,
				}},
			}},
			wantContent: []vmcp.Content{
				{Type: vmcp.ContentTypeResource, URI: "file:///f.bin", MimeType: "application/octet-stream", Data: blob},
			},
		},
		{
			name: "image without structuredContent",
			toolCallResult: map[string]any{"content": []any{
				map[string]any{"type": "image", "data": blob, "mimeType": "image/png"},
			}},
			wantContent: []vmcp.Content{{Type: vmcp.ContentTypeImage, Data: blob, MimeType: "image/png"}},
		},
		{
			name: "single text block without structuredContent",
			toolCallResult: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": `{"vulns":[]}`},
			}},
			wantContent: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: `{"vulns":[]}`}},
		},
		{
			name: "backend structuredContent object passes through unchanged",
			toolCallResult: map[string]any{
				"content":           []any{map[string]any{"type": "text", "text": `{"vulns":[]}`}},
				"structuredContent": map[string]any{"vulns": []any{}},
			},
			wantStructured: map[string]any{"vulns": []any{}},
			wantContent:    []vmcp.Content{{Type: vmcp.ContentTypeText, Text: `{"vulns":[]}`}},
		},
		{
			name: "non-object structuredContent is dropped",
			toolCallResult: map[string]any{
				"content":           []any{map[string]any{"type": "text", "text": "[1,2]"}},
				"structuredContent": []any{1, 2},
			},
			wantContent: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "[1,2]"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fb := &fakeBackend{
				advertiseTools: true,
				tools:          []mcp.Tool{{Name: "fetch"}},
				toolCallResult: tc.toolCallResult,
			}
			target := &vmcp.BackendTarget{
				WorkloadID:    "structured-content-backend",
				WorkloadName:  "structured-content-backend",
				BaseURL:       newFakeBackend(t, fb),
				TransportType: "streamable-http",
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)

			sess, _, err := NewHTTPConnector(newTestRegistry(t))(ctx, target, nil, "", nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = sess.Close() })

			result, err := sess.CallTool(ctx, "fetch", map[string]any{}, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStructured, result.StructuredContent)
			assert.Equal(t, tc.wantContent, result.Content)
		})
	}
}

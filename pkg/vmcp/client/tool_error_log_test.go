// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// TestToolResultFromMCP_LogsIsErrorOnlyAtDebug pins how a backend's isError
// result is logged. A caller can trigger it on every call, and a tool's error
// text often repeats the call's arguments (a query the backend could not parse,
// an identifier, a file name), so the result produces one Debug record with the
// tool, the backend, the error text, and the backend's _meta when it sent one,
// and nothing at WARN or above.
func TestToolResultFromMCP_LogsIsErrorOnlyAtDebug(t *testing.T) {
	t.Parallel()

	const (
		tool      = "search"
		backend   = "logs"
		errorText = `cannot parse query "status:failed AND customer:4711": unexpected token`
	)

	tests := []struct {
		name      string
		meta      *mcp.Meta
		wantDebug map[string]any
	}{
		{
			name: "result without _meta",
			wantDebug: map[string]any{
				"level": "DEBUG", "msg": "tool returned IsError=true", "tool": tool, "backend": backend,
				"error": errorText,
			},
		},
		{
			name: "result with _meta",
			meta: &mcp.Meta{AdditionalFields: map[string]any{"traceId": "trace-abc"}},
			wantDebug: map[string]any{
				"level": "DEBUG", "msg": "tool returned IsError=true", "tool": tool, "backend": backend,
				"error": errorText, "meta": map[string]any{"traceId": "trace-abc"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Each row logs to its own buffer through an injected logger, so the
			// test never touches the process-global slog default.
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{
				Level: slog.LevelDebug,
				ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
					if len(groups) == 0 && attr.Key == slog.TimeKey {
						return slog.Attr{}
					}
					return attr
				},
			}))

			result := toolResultFromMCP(logger, &mcp.CallToolResult{
				Result:  mcp.Result{Meta: tc.meta},
				IsError: true,
				Content: []mcp.Content{mcp.NewTextContent(errorText)},
			}, tool, backend)

			require.True(t, result.IsError)
			require.Len(t, result.Content, 1)
			assert.Equal(t, errorText, result.Content[0].Text, "the client still receives the full error text")

			var records []map[string]any
			for decoder := json.NewDecoder(&logs); decoder.More(); {
				var record map[string]any
				require.NoError(t, decoder.Decode(&record))
				records = append(records, record)
			}
			assert.Equal(t, []map[string]any{tc.wantDebug}, records,
				"an isError result logs exactly one Debug record and nothing at WARN or above")
		})
	}
}

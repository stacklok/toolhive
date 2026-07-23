// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// Reserved Modern _meta keys, mirrored from pkg/mcp/revision.go's unexported
// constants since classification_test.go cannot import them directly.
const (
	metaKeyProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaKeyClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
)

func sentinelEncode(v string) string {
	return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(v)) + "?="
}

type classificationErrorBody struct {
	Error struct {
		Code int64 `json:"code"`
	} `json:"error"`
}

func TestClassificationMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		parsed          *mcpparser.ParsedMCPRequest
		protocolHeader  string
		wantPassthrough bool
		wantCode        int64
	}{
		{
			name:            "nil parsed request passes through",
			parsed:          nil,
			wantPassthrough: true,
		},
		{
			name: "legacy body with no modern signal passes through",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/call",
			},
			wantPassthrough: true,
		},
		{
			// tools/list is deliberately not in the Mcp-Name-required set, so this
			// case only needs Mcp-Method (required on every Modern request) to pass.
			name: "modern header and complete meta pass through",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/list",
				Meta: map[string]any{
					metaKeyProtocolVersion:    mcpparser.MCPVersionModern,
					metaKeyClientCapabilities: map[string]any{},
				},
				MCPMethodHeader: "tools/list",
			},
			protocolHeader:  mcpparser.MCPVersionModern,
			wantPassthrough: true,
		},
		{
			name: "modern signal via reserved key with no protocolVersion and no header is invalid params",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/call",
				Meta: map[string]any{
					metaKeyClientInfo: map[string]any{},
				},
			},
			wantCode: mcpparser.CodeInvalidParams,
		},
		{
			name: "modern header/body protocolVersion mismatch is a header mismatch",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/call",
				Meta: map[string]any{
					metaKeyProtocolVersion:    mcpparser.MCPVersionModern,
					metaKeyClientCapabilities: map[string]any{},
				},
			},
			protocolHeader: "2099-01-01",
			wantCode:       mcpparser.CodeHeaderMismatch,
		},
		{
			name: "modern unsupported protocolVersion is rejected",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/call",
				Meta: map[string]any{
					metaKeyProtocolVersion: "1.0",
				},
			},
			wantCode: mcpparser.CodeUnsupportedProtocolVersion,
		},
		{
			name: "modern missing clientCapabilities is rejected",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/call",
				Meta: map[string]any{
					metaKeyProtocolVersion: mcpparser.MCPVersionModern,
				},
			},
			protocolHeader: mcpparser.MCPVersionModern,
			wantCode:       mcpparser.CodeMissingClientCapability,
		},
		{
			// No Modern signal anywhere (no header, no reserved _meta key), so this
			// classifies Legacy and ValidateHeaderConsistency must not run at all —
			// a stray/mismatched Mcp-Method header on Legacy traffic is not an error.
			name: "legacy request carrying a stray Mcp-Method header passes through unchanged",
			parsed: &mcpparser.ParsedMCPRequest{
				Method:          "tools/call",
				MCPMethodHeader: "resources/read",
			},
			wantPassthrough: true,
		},
		{
			name: "sentinel-encoded Mcp-Name header mismatched against ResourceID is a header mismatch",
			parsed: &mcpparser.ParsedMCPRequest{
				Method:     "tools/call",
				ResourceID: "echo",
				Meta: map[string]any{
					metaKeyProtocolVersion:    mcpparser.MCPVersionModern,
					metaKeyClientCapabilities: map[string]any{},
				},
				MCPMethodHeader: "tools/call",
				MCPNameHeader:   sentinelEncode("other-tool"),
			},
			protocolHeader: mcpparser.MCPVersionModern,
			wantCode:       mcpparser.CodeHeaderMismatch,
		},
		{
			name: "modern request missing required Mcp-Method header is rejected",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/list",
				Meta: map[string]any{
					metaKeyProtocolVersion:    mcpparser.MCPVersionModern,
					metaKeyClientCapabilities: map[string]any{},
				},
			},
			protocolHeader: mcpparser.MCPVersionModern,
			wantCode:       mcpparser.CodeHeaderMismatch,
		},
		{
			name: "modern tools/call request missing required Mcp-Name header is rejected",
			parsed: &mcpparser.ParsedMCPRequest{
				Method:     "tools/call",
				ResourceID: "echo",
				Meta: map[string]any{
					metaKeyProtocolVersion:    mcpparser.MCPVersionModern,
					metaKeyClientCapabilities: map[string]any{},
				},
				MCPMethodHeader: "tools/call",
			},
			protocolHeader: mcpparser.MCPVersionModern,
			wantCode:       mcpparser.CodeHeaderMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			if tt.parsed != nil {
				ctx = context.WithValue(ctx, mcpparser.MCPRequestContextKey, tt.parsed)
			}

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
			if tt.protocolHeader != "" {
				req.Header.Set("MCP-Protocol-Version", tt.protocolHeader)
			}

			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			})

			rec := httptest.NewRecorder()
			classificationMiddleware(next).ServeHTTP(rec, req)

			if tt.wantPassthrough {
				assert.True(t, nextCalled, "expected the request to fall through to next")
				return
			}

			assert.False(t, nextCalled, "expected classification to short-circuit before next")
			var body classificationErrorBody
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tt.wantCode, body.Error.Code)
		})
	}
}

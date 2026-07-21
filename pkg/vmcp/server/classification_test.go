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
	metaKeyProtocolVersion     = "io.modelcontextprotocol/protocolVersion"
	metaKeyClientInfo          = "io.modelcontextprotocol/clientInfo"
	metaKeyClientCapabilities  = "io.modelcontextprotocol/clientCapabilities"
	modernProtocolVersionValue = "2026-07-28"
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
			name: "modern header and complete meta pass through",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/call",
				Meta: map[string]any{
					metaKeyProtocolVersion:    modernProtocolVersionValue,
					metaKeyClientCapabilities: map[string]any{},
				},
			},
			protocolHeader:  modernProtocolVersionValue,
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
					metaKeyProtocolVersion:    modernProtocolVersionValue,
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
					metaKeyProtocolVersion: modernProtocolVersionValue,
				},
			},
			protocolHeader: modernProtocolVersionValue,
			wantCode:       mcpparser.CodeMissingClientCapability,
		},
		{
			name: "mismatched Mcp-Method header is a header mismatch",
			parsed: &mcpparser.ParsedMCPRequest{
				Method:          "tools/call",
				MCPMethodHeader: "resources/read",
			},
			wantCode: mcpparser.CodeHeaderMismatch,
		},
		{
			name: "sentinel-encoded Mcp-Name header mismatched against ResourceID is a header mismatch",
			parsed: &mcpparser.ParsedMCPRequest{
				Method:        "tools/call",
				ResourceID:    "echo",
				MCPNameHeader: sentinelEncode("other-tool"),
			},
			wantCode: mcpparser.CodeHeaderMismatch,
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

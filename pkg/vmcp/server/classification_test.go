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
	"github.com/stacklok/toolhive/pkg/vmcp"
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
		Code    int64  `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// classifyingHandlerTestServer builds a minimal *Server for driving
// classifyingHandler in isolation, carrying only the two fields the handler
// reads beyond config scalars: the kill-switch and the core a switch-on
// dispatch routes to.
func classifyingHandlerTestServer(modernDispatchEnabled bool) *Server {
	return &Server{
		config: &Config{
			Name:                  testServerName,
			Version:               testServerVersion,
			ModernDispatchEnabled: modernDispatchEnabled,
		},
		core: &modernFakeCore{tools: []vmcp.Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}},
	}
}

func TestClassifyingHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		parsed                *mcpparser.ParsedMCPRequest
		protocolHeader        string
		modernDispatchEnabled bool
		wantPassthrough       bool
		wantDispatched        bool
		wantCode              int64
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
			// A non-Modern MCP-Protocol-Version header, with no reserved _meta key,
			// is not a Modern signal (ClassifyRevision requires an exact match on
			// MCPVersionModern): this must still reach next, never dispatchModern,
			// now that Modern dispatch is unconditional for well-formed requests.
			name: "legacy request with an old protocol version header still passes through",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/call",
			},
			protocolHeader:  "2025-11-25",
			wantPassthrough: true,
		},
		{
			// tools/list is deliberately not in the Mcp-Name-required set, so this
			// case only needs Mcp-Method (required on every Modern request) to pass
			// ValidateHeaderConsistency; with the kill-switch on, a well-formed
			// Modern request then dispatches to the core instead of falling
			// through to next.
			name:                  "well-formed modern request dispatches to the core when the kill-switch is on",
			parsed:                wellFormedModernToolsList(),
			protocolHeader:        mcpparser.MCPVersionModern,
			modernDispatchEnabled: true,
			wantDispatched:        true,
		},
		{
			// Same well-formed Modern request, but with the kill-switch at its
			// default (off): dispatch must not happen and the request falls
			// through to the SDK path unchanged, byte-identical to pre-Modern-
			// dispatch wire behavior.
			name:            "well-formed modern request falls through to next when the kill-switch is off",
			parsed:          wellFormedModernToolsList(),
			protocolHeader:  mcpparser.MCPVersionModern,
			wantPassthrough: true,
		},
		{
			// A body that is otherwise a well-formed Modern request (valid _meta
			// protocolVersion + clientCapabilities) but omits the mandatory
			// MCP-Protocol-Version header MUST be rejected with -32020, not
			// dispatched: the draft Streamable HTTP spec requires the header on
			// every Modern POST. Without the header-presence check in
			// classifyingHandler this would classify Modern with a nil error and
			// return 200.
			name:           "well-formed modern body missing the protocol version header is rejected",
			parsed:         wellFormedModernToolsList(),
			protocolHeader: "",
			wantCode:       mcpparser.CodeHeaderMismatch,
		},
		{
			// initialize is forced Legacy unconditionally (ClassifyRevision), even
			// with a full spoofed Modern signal on both header and _meta -- mirrors
			// revision_test.go's "legacy: initialize wins over spoofed modern meta
			// and header" at the classifyingHandler boundary, so a future change
			// ahead of the ClassifyRevision call can't silently route this to
			// dispatchModern.
			name: "initialize with spoofed modern signal still passes through",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "initialize",
				Meta: map[string]any{
					metaKeyProtocolVersion:    mcpparser.MCPVersionModern,
					metaKeyClientCapabilities: map[string]any{},
				},
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
			classifyingHandlerTestServer(tt.modernDispatchEnabled).classifyingHandler(next).ServeHTTP(rec, req)

			if tt.wantPassthrough {
				assert.True(t, nextCalled, "expected the request to fall through to next")
				return
			}
			assert.False(t, nextCalled, "expected classification to short-circuit before next")

			if tt.wantDispatched {
				var body map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				result, ok := body["result"].(map[string]any)
				require.True(t, ok, "expected a Modern result envelope, got %v", body)
				assert.Equal(t, "complete", result["resultType"])
				return
			}

			var body classificationErrorBody
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tt.wantCode, body.Error.Code)
		})
	}
}

// wellFormedModernToolsList returns a well-formed Modern tools/list request:
// tools/list is deliberately not in the Mcp-Name-required set, so only
// Mcp-Method (required on every Modern request) is needed for it to pass
// ValidateHeaderConsistency.
func wellFormedModernToolsList() *mcpparser.ParsedMCPRequest {
	return &mcpparser.ParsedMCPRequest{
		Method:    "tools/list",
		ID:        "1",
		IsRequest: true,
		Meta: map[string]any{
			metaKeyProtocolVersion:    mcpparser.MCPVersionModern,
			metaKeyClientCapabilities: map[string]any{},
		},
		MCPMethodHeader: "tools/list",
	}
}

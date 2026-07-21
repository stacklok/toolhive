// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// withParsedRequest installs a ParsedMCPRequest on ctx under the same context key
// GetParsedMCPRequest reads, mirroring what the transport parsing middleware does.
func withParsedRequest(ctx context.Context, parsed *mcpparser.ParsedMCPRequest) context.Context {
	return context.WithValue(ctx, mcpparser.MCPRequestContextKey, parsed)
}

// TestCoreToolHandler_UsesGateParsedArguments verifies that when the context carries the
// transport parse for this tools/call, the handler forwards the parser's argument map to
// core.CallTool — the same map the pre-dispatch authz gate decided on (#5845) — rather
// than the SDK's decode of the request.
func TestCoreToolHandler_UsesGateParsedArguments(t *testing.T) {
	t.Parallel()

	const toolName = "t"
	fc := &fakeCore{tools: []vmcp.Tool{{Name: toolName}}}
	srv, sessionID, _ := registerServeSession(t, fc)

	// Disjoint keys so the assertion distinguishes a full replacement (correct) from a
	// merge: a merge would leak "sdkonly" — a key the gate never authorized on — into the
	// map forwarded to the backend, which is exactly the allow-then-execute-different-args
	// hole #5845 closes.
	ctx := withParsedRequest(t.Context(), &mcpparser.ParsedMCPRequest{
		Method:     "tools/call",
		ResourceID: toolName,
		Arguments:  map[string]any{"src": "parser", "parseronly": "y"},
	})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      toolName,
		Arguments: map[string]any{"src": "sdk", "sdkonly": "x"},
	}}

	res, err := srv.coreToolHandler(sessionID, toolName, "")(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsError)

	gotArgs, _ := fc.lastCallToolArgs.Load().(map[string]any)
	assert.Equal(t, map[string]any{"src": "parser", "parseronly": "y"}, gotArgs,
		"the handler must forward exactly the gate's transport-parsed arguments, not the SDK decode")
	assert.NotContains(t, gotArgs, "sdkonly",
		"SDK-only keys the gate never authorized on must not reach the backend")
}

// TestCoreToolHandler_FallsBackToSDKArguments verifies the handler keeps the SDK decode
// whenever no matching transport parse is available: a nil parse, a non-tools/call
// method, a resource-ID mismatch, or nil parsed arguments. Each of these must derive the
// call from the SDK's single decode so gate and dispatch cannot diverge.
func TestCoreToolHandler_FallsBackToSDKArguments(t *testing.T) {
	t.Parallel()

	const toolName = "t"

	tests := []struct {
		name   string
		parsed *mcpparser.ParsedMCPRequest
	}{
		{"nil parse", nil},
		{
			"method mismatch",
			&mcpparser.ParsedMCPRequest{Method: "resources/read", ResourceID: toolName, Arguments: map[string]any{"src": "parser"}},
		},
		{
			"resource-id mismatch",
			&mcpparser.ParsedMCPRequest{Method: "tools/call", ResourceID: "other", Arguments: map[string]any{"src": "parser"}},
		},
		{
			"nil arguments",
			&mcpparser.ParsedMCPRequest{Method: "tools/call", ResourceID: toolName, Arguments: nil},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fc := &fakeCore{tools: []vmcp.Tool{{Name: toolName}}}
			srv, sessionID, _ := registerServeSession(t, fc)

			ctx := t.Context()
			if tc.parsed != nil {
				ctx = withParsedRequest(ctx, tc.parsed)
			}
			req := mcp.CallToolRequest{Params: mcp.CallToolParams{
				Name:      toolName,
				Arguments: map[string]any{"src": "sdk"},
			}}

			res, err := srv.coreToolHandler(sessionID, toolName, "")(ctx, req)
			require.NoError(t, err)
			require.NotNil(t, res)
			assert.False(t, res.IsError)

			gotArgs, _ := fc.lastCallToolArgs.Load().(map[string]any)
			assert.Equal(t, "sdk", gotArgs["src"],
				"without a matching transport parse the handler must use the SDK decode")
		})
	}
}

// TestCoreToolHandler_NonObjectSDKArgsRejectedDespiteParse verifies the SDK type check
// runs first: a non-object SDK arguments payload is rejected with ErrInvalidInput even
// when a matching transport parse exists, so the gate's parse never masks a malformed
// request shape.
func TestCoreToolHandler_NonObjectSDKArgsRejectedDespiteParse(t *testing.T) {
	t.Parallel()

	const toolName = "t"
	fc := &fakeCore{tools: []vmcp.Tool{{Name: toolName}}}
	srv, sessionID, _ := registerServeSession(t, fc)

	ctx := withParsedRequest(t.Context(), &mcpparser.ParsedMCPRequest{
		Method:     "tools/call",
		ResourceID: toolName,
		Arguments:  map[string]any{"src": "parser"},
	})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      toolName,
		Arguments: "not-an-object",
	}}

	res, err := srv.coreToolHandler(sessionID, toolName, "")(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError, "a non-object SDK arguments payload must be rejected")
	assert.Equal(t, int32(0), fc.callToolCalls.Load(),
		"the core must not be reached when the SDK arguments are not an object")
}

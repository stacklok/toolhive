// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestCoreToolHandler_RedactsCredentialShapedToolResult verifies the Legacy
// (SDK) tools/call path scans and redacts credential-shaped content in the
// core's result when Config.RedactToolResultSecrets is enabled -- the same
// gap connector-gateway (which serves through this Server, not ToolHive's
// own proxies) would otherwise have.
func TestCoreToolHandler_RedactsCredentialShapedToolResult(t *testing.T) {
	t.Parallel()

	const toolName = "t"
	ghToken := "ghp_" + strings.Repeat("a", 36)
	fc := &fakeCore{
		tools: []vmcp.Tool{{Name: toolName}},
		callResult: &vmcp.ToolCallResult{
			Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "here is the token: " + ghToken}},
		},
	}
	srv, sessionID, _ := registerServeSession(t, fc)
	srv.config.RedactToolResultSecrets = true

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: toolName, Arguments: map[string]any{}}}
	res, err := srv.coreToolHandler(sessionID, toolName, "")(t.Context(), req)

	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	assert.NotContains(t, text.Text, ghToken)
	assert.Contains(t, text.Text, "REDACTED-BY-TOOLHIVE")
}

// TestCoreToolHandler_DisabledByDefault verifies redaction is opt-in: with
// Config.RedactToolResultSecrets left at its zero value, credential-shaped
// content passes through unchanged.
func TestCoreToolHandler_DisabledByDefault(t *testing.T) {
	t.Parallel()

	const toolName = "t"
	ghToken := "ghp_" + strings.Repeat("a", 36)
	fc := &fakeCore{
		tools: []vmcp.Tool{{Name: toolName}},
		callResult: &vmcp.ToolCallResult{
			Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: ghToken}},
		},
	}
	srv, sessionID, _ := registerServeSession(t, fc)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: toolName, Arguments: map[string]any{}}}
	res, err := srv.coreToolHandler(sessionID, toolName, "")(t.Context(), req)

	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, ghToken, text.Text, "disabled by default: content must pass through unmodified")
}

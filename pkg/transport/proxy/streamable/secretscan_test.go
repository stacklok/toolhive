// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	sdkmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

func TestInspectToolCallResponse_RedactsCredentialShapedToolResult(t *testing.T) {
	t.Parallel()

	ghToken := "ghp_" + strings.Repeat("a", 36)
	resp, err := jsonrpc2.NewResponse(
		jsonrpc2.Int64ID(1),
		map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "here is the token: " + ghToken},
			},
		},
		nil,
	)
	require.NoError(t, err)

	out := inspectToolCallResponse(string(sdkmcp.MethodToolsCall), resp)

	outResp, ok := out.(*jsonrpc2.Response)
	require.True(t, ok)
	require.NotContains(t, string(outResp.Result), ghToken)
	require.Contains(t, string(outResp.Result), "REDACTED-BY-TOOLHIVE")
}

func TestInspectToolCallResponse_IgnoresNonToolCallMethods(t *testing.T) {
	t.Parallel()

	ghToken := "ghp_" + strings.Repeat("a", 36)
	resp, err := jsonrpc2.NewResponse(
		jsonrpc2.Int64ID(1),
		map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": ghToken},
			},
		},
		nil,
	)
	require.NoError(t, err)

	out := inspectToolCallResponse("resources/read", resp)

	require.Same(t, resp, out)
}

func TestInspectToolCallResponse_IgnoresErrorResponses(t *testing.T) {
	t.Parallel()

	resp, err := jsonrpc2.NewResponse(jsonrpc2.Int64ID(1), nil, jsonrpc2.NewError(-32000, "boom"))
	require.NoError(t, err)

	out := inspectToolCallResponse(string(sdkmcp.MethodToolsCall), resp)

	require.Same(t, resp, out)
}

func TestInspectToolCallResponse_IgnoresNonResponseMessages(t *testing.T) {
	t.Parallel()

	req, err := jsonrpc2.NewNotification("notifications/message", nil)
	require.NoError(t, err)

	out := inspectToolCallResponse(string(sdkmcp.MethodToolsCall), req)

	require.Same(t, req, out)
}

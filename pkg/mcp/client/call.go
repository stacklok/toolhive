// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// CallTool connects to the MCP server at serverURL, performs the initialize
// handshake, invokes the named tool with the supplied arguments, and returns
// the result. The connection is closed before returning.
//
// args may be nil; in that case the `arguments` field is omitted from the
// tool call request (equivalent to calling the tool with no inputs).
//
// A nil error indicates the MCP call completed; the returned result may still
// have IsError=true to signal a tool-level failure that the caller should
// surface to the user.
func CallTool(
	ctx context.Context,
	serverURL, transport, clientName, toolName string,
	args map[string]any,
) (*mcp.CallToolResult, error) {
	c, err := Connect(ctx, serverURL, transport, clientName)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := c.Close(); cerr != nil {
			slog.Warn("failed to close MCP client", "error", cerr)
		}
	}()

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args

	result, err := c.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to call tool %q: %w", toolName, err)
	}
	return result, nil
}

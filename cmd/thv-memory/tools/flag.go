// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/memory"
)

// RegisterFlag registers the memory_flag tool.
func RegisterFlag(s *server.MCPServer, store memory.Store) {
	tool := mcp.NewTool("memory_flag",
		mcp.WithDescription("Mark a memory as potentially stale without deleting it."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Memory entry ID")),
		mcp.WithString("reason", mcp.Required(), mcp.Description("Why this memory may be stale")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := req.GetString("id", "")
		reason := req.GetString("reason", "")
		if err := checkMutable(ctx, store, id); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := store.Flag(ctx, id, reason); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(`{"status":"ok"}`), nil
	})
}

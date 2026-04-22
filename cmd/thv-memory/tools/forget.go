// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/memory"
)

// RegisterForget registers the memory_forget tool.
func RegisterForget(s *server.MCPServer, store memory.Store) {
	tool := mcp.NewTool("memory_forget",
		mcp.WithDescription("Delete a memory entry permanently."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Memory entry ID")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := req.GetString("id", "")
		if err := store.Delete(ctx, id); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(`{"status":"ok"}`), nil
	})
}

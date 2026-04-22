// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/memory"
)

// RegisterUpdate registers the memory_update tool.
func RegisterUpdate(s *server.MCPServer, store memory.Store) {
	tool := mcp.NewTool("memory_update",
		mcp.WithDescription("Correct or refine an existing memory entry. Previous content is saved to history."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Memory entry ID")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Updated content")),
		mcp.WithString("author", mcp.Description("Author type: human or agent (default: agent)")),
		mcp.WithString("correction_note", mcp.Description("Explanation for the correction")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := req.GetString("id", "")
		content := req.GetString("content", "")
		authorStr := req.GetString("author", "agent")
		if authorStr == "" {
			authorStr = "agent"
		}
		note := req.GetString("correction_note", "")

		if err := store.Update(ctx, id, content, memory.AuthorType(authorStr), note); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		entry, err := store.Get(ctx, id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(entry)
		return mcp.NewToolResultText(string(out)), nil
	})
}

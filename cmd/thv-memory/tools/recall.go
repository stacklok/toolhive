// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/memory"
)

// RegisterRecall registers the memory_recall tool.
func RegisterRecall(s *server.MCPServer, store memory.Store) {
	tool := mcp.NewTool("memory_recall",
		mcp.WithDescription("Fetch a specific memory entry by ID, including its full revision history."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Memory entry ID")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := req.GetString("id", "")
		entry, err := store.Get(ctx, id)
		if err != nil {
			if errors.Is(err, memory.ErrNotFound) {
				return mcp.NewToolResultError("entry not found"), nil
			}
			return mcp.NewToolResultError(err.Error()), nil
		}
		_ = store.IncrementAccess(ctx, id)
		out, _ := json.Marshal(entry)
		return mcp.NewToolResultText(string(out)), nil
	})
}

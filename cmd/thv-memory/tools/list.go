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

// RegisterList registers the memory_list tool.
func RegisterList(s *server.MCPServer, store memory.Store) {
	tool := mcp.NewTool("memory_list",
		mcp.WithDescription("List memory entries with structured filters (not semantic). Use memory_search for semantic queries."),
		mcp.WithString("type", mcp.Description("Filter by type: semantic or procedural")),
		mcp.WithString("author", mcp.Description("Filter by author: human or agent")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 20)")),
		mcp.WithNumber("offset", mcp.Description("Pagination offset")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var f memory.ListFilter
		if rawType := req.GetString("type", ""); rawType != "" {
			t := memory.Type(rawType)
			f.Type = &t
		}
		if rawAuthor := req.GetString("author", ""); rawAuthor != "" {
			a := memory.AuthorType(rawAuthor)
			f.Author = &a
		}
		f.Limit = req.GetInt("limit", 20)
		f.Offset = req.GetInt("offset", 0)
		active := memory.EntryStatusActive
		f.Status = &active

		entries, err := store.List(ctx, f)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(entries)
		return mcp.NewToolResultText(string(out)), nil
	})
}

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

// RegisterSearch registers the memory_search tool.
func RegisterSearch(s *server.MCPServer, svc *memory.Service) {
	tool := mcp.NewTool("memory_search",
		mcp.WithDescription(
			"Semantic search across memory entries. "+
				"Returns entries ranked by similarity with trust and staleness scores.",
		),
		mcp.WithString("query", mcp.Required(), mcp.Description("Natural language query")),
		mcp.WithString("type", mcp.Description("Filter by type: semantic or procedural")),
		mcp.WithNumber("top_k", mcp.Description("Maximum results to return (default 10)")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := req.GetString("query", "")

		var memType *memory.Type
		if rawType := req.GetString("type", ""); rawType != "" {
			t := memory.Type(rawType)
			memType = &t
		}

		topK := req.GetInt("top_k", 10)

		results, err := svc.Search(ctx, query, memType, topK)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(results)
		return mcp.NewToolResultText(string(out)), nil
	})
}

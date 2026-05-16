// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tools registers MCP tools for the memory server.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/memory"
)

// RegisterConsolidate registers the memory_consolidate tool.
func RegisterConsolidate(s *server.MCPServer, svc *memory.Service, store memory.Store) {
	tool := mcp.NewTool("memory_consolidate",
		mcp.WithDescription(
			"Merge related memory entries into one richer entry. "+
				"Originals are archived with a pointer to the new entry.",
		),
		mcp.WithArray("ids", mcp.Required(), mcp.Description("Array of memory IDs to consolidate"), mcp.WithStringItems()),
		mcp.WithString("content", mcp.Required(), mcp.Description("Content for the consolidated entry")),
		mcp.WithString("type", mcp.Required(), mcp.Description("Memory type for the consolidated entry")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ids, err := req.RequireStringSlice("ids")
		if err != nil {
			return mcp.NewToolResultError("ids must be an array of strings"), nil
		}
		if len(ids) < 2 {
			return mcp.NewToolResultError("at least 2 ids required"), nil
		}

		content := req.GetString("content", "")
		memTypeStr := req.GetString("type", "")

		result, err := svc.Remember(ctx, memory.RememberInput{
			Content: content,
			Type:    memory.Type(memTypeStr),
			Author:  memory.AuthorHuman,
			Force:   true,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("creating consolidated entry: %v", err)), nil
		}

		var archiveErrors []string
		for _, id := range ids {
			if err := store.Archive(ctx, id, memory.ArchiveReasonConsolidated, result.MemoryID); err != nil {
				archiveErrors = append(archiveErrors, fmt.Sprintf("%s: %v", id, err))
			}
		}

		resp := map[string]any{
			"consolidated_id": result.MemoryID,
			"archived_ids":    ids,
		}
		if len(archiveErrors) > 0 {
			resp["archive_errors"] = strings.Join(archiveErrors, "; ")
		}
		out, _ := json.Marshal(resp)
		return mcp.NewToolResultText(string(out)), nil
	})
}

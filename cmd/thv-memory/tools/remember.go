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

// RegisterRemember registers the memory_remember tool.
func RegisterRemember(s *server.MCPServer, svc *memory.Service) {
	tool := mcp.NewTool("memory_remember",
		mcp.WithDescription("Store a new semantic or procedural memory. Returns conflict_detected if a similar memory exists."),
		mcp.WithString("content", mcp.Required(), mcp.Description("The knowledge to store")),
		mcp.WithString("type", mcp.Required(), mcp.Description("Memory type: semantic or procedural")),
		mcp.WithString("author", mcp.Description("Author type: human or agent (default: agent)")),
		mcp.WithString("session_id", mcp.Description("Originating session ID")),
		mcp.WithNumber("ttl_days", mcp.Description("Optional TTL in days")),
		mcp.WithBoolean("force", mcp.Description("Write even if conflicts detected")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content := req.GetString("content", "")
		memTypeStr := req.GetString("type", "")
		authorStr := req.GetString("author", "agent")
		if authorStr == "" {
			authorStr = "agent"
		}
		force := req.GetBool("force", false)
		sessionID := req.GetString("session_id", "")

		var ttlDays *int
		args := req.GetArguments()
		if raw, ok := args["ttl_days"].(float64); ok {
			v := int(raw)
			ttlDays = &v
		}

		result, err := svc.Remember(ctx, memory.RememberInput{
			Content:   content,
			Type:      memory.Type(memTypeStr),
			Author:    memory.AuthorType(authorStr),
			SessionID: sessionID,
			TTLDays:   ttlDays,
			Force:     force,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(out)), nil
	})
}
